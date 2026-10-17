package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"codex-speed-patch/internal/asar"
	"codex-speed-patch/internal/platform"
	"codex-speed-patch/internal/state"
)

func TestRestoreDoesNotReplaceNewVersionWithStaleBackup(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS bundle lifecycle")
	}
	root := t.TempDir()
	app := filepath.Join(root, "Current.app")
	backupDir := filepath.Join(root, "backup")
	home := filepath.Join(root, "home")
	write := func(app, version, payload string) {
		t.Helper()
		for _, dir := range []string{"Resources", "MacOS"} {
			if err := os.MkdirAll(filepath.Join(app, "Contents", dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		files := map[string]string{"Info.plist": fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.openai.codex</string><key>CFBundleShortVersionString</key><string>%s</string></dict></plist>`, version), "Resources/app.asar": payload, "MacOS/ChatGPT": "fixture"}
		for name, value := range files {
			if err := os.WriteFile(filepath.Join(app, "Contents", name), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(app, "26.1007.30912", "new-version")
	write(filepath.Join(backupDir, "ChatGPT.app"), "26.924.22138", "old-version")
	if err := state.Save(state.ManifestPath(home), &state.Manifest{AppPath: app, AppVersion: "26.924.22138", BackupDir: backupDir}); err != nil {
		t.Fatal(err)
	}
	if err := (Service{CodexHome: home}).Restore(); err == nil {
		t.Fatal("stale backup was accepted")
	}
	data, err := os.ReadFile(filepath.Join(app, "Contents", "Resources", "app.asar"))
	if err != nil || string(data) != "new-version" {
		t.Fatalf("restore touched the updated app: %q, %v", data, err)
	}
}

func TestRecordedNativeCLIHashDetectsReplacementAndIgnoresOtherVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(path, []byte("original CLI"), 0o700); err != nil {
		t.Fatal(err)
	}
	hash, err := asar.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	inst := &platform.Installation{AppPath: "test-app", Version: "1", CLIPath: path}
	manifest := &state.Manifest{AppPath: inst.AppPath, AppVersion: inst.Version, Status: "patched", PatchedCLISHA256: hash}
	if err := verifyRecordedCLI(inst, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replaced CLI"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyRecordedCLI(inst, manifest); err == nil {
		t.Fatal("changed native CLI was accepted")
	}
	manifest.AppVersion = "older"
	if err := verifyRecordedCLI(inst, manifest); err != nil {
		t.Fatal("an older installation's record was applied to the new CLI")
	}
}
