package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"codex-speed-patch/internal/asar"
)

// CatalogPatch changes only an embedded JSON data region. The replacement has
// the same length, preserving executable offsets and all bytes outside it.
type CatalogPatch struct {
	Offset      int
	Original    []byte
	Replacement []byte
	Models      []string
	External    bool
}

func PlanModelCatalog(data []byte, declared []string) (*CatalogPatch, error) {
	return planModelCatalog(data, declared, true)
}

func planModelCatalog(data []byte, declared []string, preserveOffsets bool) (*CatalogPatch, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	var starts []int
	for _, prefix := range [][]byte{[]byte("{\n  \"models\": ["), []byte(`{"models":[`)} {
		for from := 0; from < len(data); {
			i := bytes.Index(data[from:], prefix)
			if i < 0 {
				break
			}
			starts = append(starts, from+i)
			from += i + len(prefix)
		}
	}
	if len(starts) != 1 {
		return nil, fmt.Errorf("native model catalog is missing or ambiguous: %d candidates", len(starts))
	}
	start := starts[0]
	decoder := json.NewDecoder(bytes.NewReader(data[start:]))
	decoder.UseNumber()
	var catalog map[string]any
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode native model catalog: %w", err)
	}
	models, ok := catalog["models"].([]any)
	if !ok || len(models) == 0 {
		return nil, errors.New("native model catalog contains no model declarations")
	}
	allowed := make(map[string]bool)
	for _, slug := range declared {
		allowed[slug] = true
	}
	var changed []string
	seen := make(map[string]bool)
	for _, value := range models {
		model, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("native model declaration is not an object")
		}
		slug, ok := model["slug"].(string)
		if !ok || slug == "" || seen[slug] {
			return nil, errors.New("native model slug is missing or duplicated")
		}
		seen[slug] = true
		declaresUltra := allowed[slug]
		if extra, ok := model["additional_speed_tiers"].([]any); ok {
			for _, value := range extra {
				declaresUltra = declaresUltra || value == "ultrafast"
			}
		}
		if !declaresUltra {
			continue
		}
		tiers, ok := model["service_tiers"].([]any)
		if !ok {
			return nil, fmt.Errorf("native model %s has no explicit tier list", slug)
		}
		hasUltra := false
		for _, value := range tiers {
			tier, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid service tier for %s", slug)
			}
			hasUltra = hasUltra || tier["id"] == "ultrafast"
		}
		if hasUltra {
			continue
		}
		model["service_tiers"] = append(tiers, map[string]any{"id": "ultrafast", "name": "Ultrafast", "description": "The fastest available responses for latency-sensitive work."})
		if extra, ok := model["additional_speed_tiers"].([]any); ok {
			found := false
			for _, value := range extra {
				found = found || value == "ultrafast"
			}
			if !found {
				model["additional_speed_tiers"] = append(extra, "ultrafast")
			}
		}
		changed = append(changed, slug)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	// Keep models first so a patched catalog can be found without a fixed offset.
	encodedModels, err := json.Marshal(catalog["models"])
	if err != nil {
		return nil, err
	}
	encoded := append([]byte(`{"models":`), encodedModels...)
	var keys []string
	for key := range catalog {
		if key != "models" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		name, _ := json.Marshal(key)
		value, err := json.Marshal(catalog[key])
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, ',')
		encoded = append(encoded, name...)
		encoded = append(encoded, ':')
		encoded = append(encoded, value...)
	}
	encoded = append(encoded, '}')
	length := int(decoder.InputOffset())
	// A prior patch compacted the JSON and filled the rest of its original
	// region with spaces. Decoder.InputOffset excludes that reserved capacity;
	// retain it when upgrading instead of treating the compact JSON as full.
	for start+length < len(data) && data[start+length] == ' ' {
		length++
	}
	if preserveOffsets && len(encoded) > length {
		return nil, errors.New("expanded native model catalog does not fit its original data region")
	}
	replacement := encoded
	if preserveOffsets {
		replacement = append(encoded, bytes.Repeat([]byte{' '}, length-len(encoded))...)
	}
	sort.Strings(changed)
	return &CatalogPatch{Offset: start, Original: append([]byte(nil), data[start:start+length]...), Replacement: replacement, Models: changed}, nil
}

func ModelCatalogPatch(inst *Installation, declared []string) (*CatalogPatch, error) {
	if len(declared) == 0 && inst.PackageKind != "macos-app" {
		return nil, nil
	}
	if inst.CLIPath == "" || !regularFile(inst.CLIPath) {
		return nil, errors.New("native CLI is missing; Ultrafast request support cannot be verified")
	}
	if inst.PackageKind == "macos-app" && !isWithinApp(inst.AppPath, inst.CLIPath) {
		return nil, errors.New("native CLI resolves outside the application")
	}
	data, err := os.ReadFile(inst.CLIPath)
	if err != nil {
		return nil, err
	}
	if inst.PackageKind == "macos-app" {
		external, err := externalModelCatalog(data, declared)
		if err != nil {
			return nil, err
		}
		path := ModelCatalogPath(inst)
		if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
			return nil, errors.New("external model catalog must be a regular file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		current, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if bytes.Equal(current, external) {
			return nil, nil
		}
		return &CatalogPatch{External: true, Original: current, Replacement: external, Models: declared}, nil
	}
	return PlanModelCatalog(data, declared)
}

func ModelCatalogPath(inst *Installation) string {
	return filepath.Join(inst.AppPath, "Contents", "Resources", asar.CatalogFileName)
}

func externalModelCatalog(data []byte, declared []string) ([]byte, error) {
	plan, err := planModelCatalog(data, declared, false)
	if err != nil {
		return nil, err
	}
	if plan != nil {
		data = plan.Replacement
	}
	start := bytes.Index(data, []byte(`{"models":[`))
	if start < 0 {
		start = bytes.Index(data, []byte("{\n  \"models\": ["))
	}
	if start < 0 {
		return nil, errors.New("native model catalog is missing")
	}
	var raw json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(data[start:])).Decode(&raw); err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	return append(compact.Bytes(), '\n'), nil
}

func ApplyModelCatalog(inst *Installation, plan *CatalogPatch) error {
	if plan == nil {
		return nil
	}
	if plan.External {
		path := ModelCatalogPath(inst)
		current, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !bytes.Equal(current, plan.Original) {
			return errors.New("external model catalog changed since verification")
		}
		return os.WriteFile(path, plan.Replacement, 0o600)
	}
	data, err := os.ReadFile(inst.CLIPath)
	if err != nil {
		return err
	}
	end := plan.Offset + len(plan.Original)
	if plan.Offset < 0 || end > len(data) || !bytes.Equal(data[plan.Offset:end], plan.Original) || len(plan.Replacement) != len(plan.Original) {
		return errors.New("native catalog changed since verification; modification cancelled")
	}
	copy(data[plan.Offset:end], plan.Replacement)
	info, err := os.Stat(inst.CLIPath)
	if err != nil {
		return err
	}
	return os.WriteFile(inst.CLIPath, data, info.Mode().Perm())
}
