package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckMacSigningOutput(t *testing.T) {
	tests := []struct {
		name            string
		output          string
		allowSignedCopy bool
		wantErr         bool
	}{
		{
			name:    "Developer ID app",
			output:  "Identifier=com.openai.codex\nAuthority=Developer ID Application: OpenAI OpCo, LLC\nTeamIdentifier=2DC432GLL2\n",
			wantErr: true,
		},
		{
			name:            "Developer ID test copy",
			output:          "Identifier=com.openai.codex\nAuthority=Developer ID Application: OpenAI OpCo, LLC\nTeamIdentifier=2DC432GLL2\n",
			allowSignedCopy: true,
		},
		{
			name:   "ad-hoc app",
			output: "Identifier=com.openai.codex\nSignature=adhoc\nTeamIdentifier=not set\n",
		},
		{
			name:    "unrecognized signature output",
			output:  "Identifier=com.openai.codex\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMacSigningOutput([]byte(tt.output), tt.allowSignedCopy)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkMacSigningOutput() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.name == "Developer ID app" && !strings.Contains(err.Error(), "TeamIdentifier") {
				t.Fatalf("error should name the unsupported signature: %v", err)
			}
		})
	}
}

func TestMacSignStepsSignFrameworkBeforeOuterApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Test.app")
	helpers := filepath.Join(app, "Contents", "Frameworks", "Codex Framework.framework", "Helpers")
	if err := os.MkdirAll(filepath.Join(helpers, "Codex (Renderer).app"), 0o700); err != nil {
		t.Fatal(err)
	}
	steps, err := macSignSteps(&Installation{AppPath: app}, "/tmp/entitlements.plist")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 || !strings.HasSuffix(steps[len(steps)-2][len(steps[len(steps)-2])-1], "Codex Framework.framework") || steps[len(steps)-1][len(steps[len(steps)-1])-1] != app {
		t.Fatalf("unexpected signing order: %v", steps)
	}
	for _, args := range steps {
		if strings.Contains(strings.Join(args, " "), "--deep") {
			t.Fatalf("recursive signing is not expected: %v", args)
		}
	}
}

func TestMacSignStepsRejectsHelperSymlinkOutsideApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Test.app")
	helpers := filepath.Join(app, "Contents", "Frameworks", "Codex Framework.framework", "Helpers")
	if err := os.MkdirAll(helpers, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "External.app")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(helpers, "External.app")); err != nil {
		t.Fatal(err)
	}
	if _, err := macSignSteps(&Installation{AppPath: app}, "/tmp/entitlements.plist"); err == nil {
		t.Fatal("helper symlink outside the app was accepted")
	}
}

func TestSignedMacTestCopyMustResolveInsideTemporaryDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS path check")
	}
	dir := filepath.Join(t.TempDir(), "Test.app")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if !isMacOSTestPath(dir) {
		t.Fatal("temporary app copy was rejected")
	}
	if isMacOSTestPath("/Applications/ChatGPT.app") {
		t.Fatal("production app was accepted as a test copy")
	}
	alias := filepath.Join(t.TempDir(), "Applications")
	if err := os.Symlink("/Applications", alias); err != nil {
		t.Fatal(err)
	}
	if isMacOSTestPath(filepath.Join(alias, "ChatGPT.app")) {
		t.Fatal("symlink to production app was accepted as a test copy")
	}
}
