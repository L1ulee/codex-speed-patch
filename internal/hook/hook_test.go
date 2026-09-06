package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	initial := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"existing"}]}]}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, "/tmp/codex-speed-patch"); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, "/tmp/codex-speed-patch"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if count := strings.Count(text, "repair --hook"); count != 2 {
		t.Fatalf("expected Unix and Windows commands once each, got %d", count)
	}
	if count := strings.Count(text, "restart-if-needed --hook"); count != 2 {
		t.Fatalf("expected Unix and Windows restart commands once each, got %d", count)
	}
	if !strings.Contains(text, "existing") {
		t.Fatal("existing hook was removed")
	}
}
