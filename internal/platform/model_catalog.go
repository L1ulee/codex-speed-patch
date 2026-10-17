package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// CatalogPatch changes only an embedded JSON data region. The replacement has
// the same length, preserving executable offsets and all bytes outside it.
type CatalogPatch struct {
	Offset      int
	Original    []byte
	Replacement []byte
	Models      []string
}

func PlanModelCatalog(data []byte, declared []string) (*CatalogPatch, error) {
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
	if len(encoded) > length {
		return nil, errors.New("expanded native model catalog does not fit its original data region")
	}
	replacement := append(encoded, bytes.Repeat([]byte{' '}, length-len(encoded))...)
	sort.Strings(changed)
	return &CatalogPatch{Offset: start, Original: append([]byte(nil), data[start:start+length]...), Replacement: replacement, Models: changed}, nil
}

func ModelCatalogPatch(inst *Installation, declared []string) (*CatalogPatch, error) {
	if len(declared) == 0 {
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
	return PlanModelCatalog(data, declared)
}

func ApplyModelCatalog(inst *Installation, plan *CatalogPatch) error {
	if plan == nil {
		return nil
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
