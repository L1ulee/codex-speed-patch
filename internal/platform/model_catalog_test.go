package platform

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNativeCatalogPatchPreservesOffsetsAndUnrelatedMetadata(t *testing.T) {
	root := map[string]any{"models": []any{
		map[string]any{"slug": "supported", "service_tiers": []any{map[string]any{"id": "priority", "name": "Fast"}}, "instructions": "unchanged"},
		map[string]any{"slug": "fast-only", "service_tiers": []any{map[string]any{"id": "priority"}}},
	}, "unknownCounter": json.Number("9007199254740993")}
	encoded, _ := json.MarshalIndent(root, "", "  ")
	// Supply sufficient native whitespace while keeping the JSON schema intact.
	encoded = bytes.Replace(encoded, []byte("\n"), []byte("\n               "), -1)
	encoded = bytes.Replace(encoded, []byte("{\n                 \"models\""), []byte("{\n  \"models\""), 1)
	data := append(append([]byte("executable-prefix\x00"), encoded...), []byte("\x00executable-suffix")...)
	plan, err := PlanModelCatalog(data, []string{"supported"})
	if err != nil || plan == nil {
		t.Fatalf("catalog plan: %#v, %v", plan, err)
	}
	patched := append([]byte(nil), data...)
	copy(patched[plan.Offset:], plan.Replacement)
	if len(patched) != len(data) || !bytes.Equal(patched[:plan.Offset], data[:plan.Offset]) || !bytes.Equal(patched[plan.Offset+len(plan.Original):], data[plan.Offset+len(plan.Original):]) {
		t.Fatal("executable bytes outside the catalog changed")
	}
	if !bytes.Contains(plan.Replacement, []byte(`9007199254740993`)) || !bytes.Contains(plan.Replacement, []byte(`"instructions":"unchanged"`)) {
		t.Fatal("unknown metadata or instructions were lost")
	}
	var updated struct {
		Models []struct {
			Slug         string
			ServiceTiers []struct{ ID string } `json:"service_tiers"`
		}
	}
	if err := json.Unmarshal(plan.Replacement, &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Models[0].ServiceTiers) != 2 || updated.Models[0].ServiceTiers[1].ID != "ultrafast" || len(updated.Models[1].ServiceTiers) != 1 {
		t.Fatal("catalog changed models without a declaration")
	}
	if again, err := PlanModelCatalog(patched, []string{"supported"}); err != nil || again != nil {
		t.Fatalf("catalog patch is not idempotent: %#v, %v", again, err)
	}
}

func TestNativeCatalogRejectsMissingAmbiguousOrInsufficientSpace(t *testing.T) {
	compact := []byte(`{"models":[{"slug":"supported","service_tiers":[]}]}`)
	for _, input := range [][]byte{[]byte("no catalog"), append(append([]byte(nil), compact...), compact...), compact} {
		if _, err := PlanModelCatalog(input, []string{"supported"}); err == nil {
			t.Fatal("unsafe native catalog was accepted")
		}
	}
}

func TestNativeCatalogUpdatesAllConfirmedModelsAndPreservesFast(t *testing.T) {
	declared := []string{"gpt-6.1-sol", "gpt-5.6-sol", "gpt-6-astra"}
	var models []any
	for _, slug := range append(append([]string(nil), declared...), "gpt-6-luna") {
		models = append(models, map[string]any{"slug": slug,
			"service_tiers":          []any{map[string]any{"id": "priority", "name": "Fast"}},
			"additional_speed_tiers": []any{"fast"}})
	}
	encoded, err := json.MarshalIndent(map[string]any{"models": models}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanModelCatalog(encoded, declared)
	if err != nil || plan == nil {
		t.Fatalf("confirmed native models: %#v, %v", plan, err)
	}
	var updated struct {
		Models []struct {
			Slug       string
			Tiers      []struct{ ID string } `json:"service_tiers"`
			Additional []string              `json:"additional_speed_tiers"`
		}
	}
	if err := json.Unmarshal(plan.Replacement, &updated); err != nil {
		t.Fatal(err)
	}
	for i, model := range updated.Models {
		if model.Tiers[0].ID != "priority" || model.Additional[0] != "fast" {
			t.Fatalf("Fast mapping lost for %s", model.Slug)
		}
		if i < len(declared) {
			if len(model.Tiers) != 2 || model.Tiers[1].ID != "ultrafast" || len(model.Additional) != 2 || model.Additional[1] != "ultrafast" {
				t.Fatalf("Ultrafast missing for %s", model.Slug)
			}
		} else if len(model.Tiers) != 1 || len(model.Additional) != 1 {
			t.Fatalf("unconfirmed model acquired Ultrafast: %s", model.Slug)
		}
	}
	if again, err := PlanModelCatalog(plan.Replacement, declared); err != nil || again != nil {
		t.Fatalf("catalog changes on repeated repair: %#v, %v", again, err)
	}
}

func TestNativeCatalogUpgradeReusesPaddingFromPreviousPatch(t *testing.T) {
	encoded, err := json.MarshalIndent(map[string]any{"models": []any{
		map[string]any{"slug": "gpt-6.1-sol", "service_tiers": []any{map[string]any{"id": "priority", "name": "Fast"}}},
		map[string]any{"slug": "gpt-5.6-sol", "service_tiers": []any{map[string]any{"id": "priority", "name": "Fast"}}},
	}}, "", "        ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte("{\n        \"models\""), []byte("{\n  \"models\""), 1)
	suffix := []byte("\x00unrelated-executable-data")
	data := append(append([]byte(nil), encoded...), suffix...)
	first, err := PlanModelCatalog(data, []string{"gpt-6.1-sol"})
	if err != nil || first == nil {
		t.Fatalf("initial catalog patch: %#v, %v", first, err)
	}
	copy(data[first.Offset:], first.Replacement)
	upgrade, err := PlanModelCatalog(data, []string{"gpt-6.1-sol", "gpt-5.6-sol"})
	if err != nil || upgrade == nil {
		t.Fatalf("upgrade must reuse reserved JSON whitespace: %#v, %v", upgrade, err)
	}
	if len(upgrade.Replacement) != len(first.Replacement) || !bytes.Equal(data[len(encoded):], suffix) {
		t.Fatal("catalog upgrade changed its reserved region or adjacent executable data")
	}
	copy(data[upgrade.Offset:], upgrade.Replacement)
	if again, err := PlanModelCatalog(data, []string{"gpt-6.1-sol", "gpt-5.6-sol"}); err != nil || again != nil {
		t.Fatalf("upgraded catalog is not stable: %#v, %v", again, err)
	}
}
