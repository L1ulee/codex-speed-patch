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

func TestMacSigningIncludesEntireSparkleChain(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Test.app")
	frameworks := filepath.Join(app, "Contents", "Frameworks")
	for _, path := range []string{"Codex Framework.framework/Helpers/Codex (Renderer).app", "Sparkle.framework/XPCServices/Installer.xpc", "Sparkle.framework/XPCServices/Downloader.xpc", "Sparkle.framework/Updater.app"} {
		if err := os.MkdirAll(filepath.Join(frameworks, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(frameworks, "Sparkle.framework", "Autoupdate"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	steps, err := macSignSteps(&Installation{AppPath: app}, "/tmp/entitlements.plist")
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, args := range steps {
		positions[args[len(args)-1]] = i
	}
	sparkle := filepath.Join(frameworks, "Sparkle.framework")
	frameworkIndex, ok := positions[sparkle]
	if !ok {
		t.Fatal("Sparkle framework is omitted from signing")
	}
	for _, relative := range []string{"Autoupdate", "Updater.app", "XPCServices/Installer.xpc", "XPCServices/Downloader.xpc"} {
		i, ok := positions[filepath.Join(sparkle, relative)]
		if !ok || i >= frameworkIndex {
			t.Fatalf("Sparkle child %s was not signed before the framework", relative)
		}
	}
	if frameworkIndex >= positions[app] {
		t.Fatal("Sparkle must be signed before the outer app")
	}
}

func TestAdHocEntitlementsKeepPermissionsWithoutDeveloperIdentity(t *testing.T) {
	input := map[string]any{"com.apple.developer.team-identifier": "TEAM", "com.apple.developer.aps-environment": "production", "com.apple.application-identifier": "TEAM.app", "keychain-access-groups": []string{"TEAM.app"}, "com.apple.security.app-sandbox": false, "com.apple.security.network.client": true}
	result := adHocEntitlements(input)
	if len(result) != 2 || result["com.apple.security.network.client"] != true || result["com.apple.security.app-sandbox"] != false {
		t.Fatalf("ad-hoc permissions = %v", result)
	}
	if len(input) != 6 {
		t.Fatal("original entitlement dictionary was modified")
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
