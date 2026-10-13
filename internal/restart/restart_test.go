package restart

import "testing"

func TestBundleProcessDetectionIncludesHelpersWithoutMatchingOtherApps(t *testing.T) {
	app := "/private/tmp/My Test.app"
	for _, executable := range []string{
		app + "/Contents/MacOS/ChatGPT",
		app + "/Contents/Frameworks/Codex Framework.framework/Helpers/Codex (Renderer).app/Contents/MacOS/Codex (Renderer)",
	} {
		if !hasBundleProcesses(" 42 "+executable+"\n", app) {
			t.Fatalf("missed live process %q", executable)
		}
	}
	if hasBundleProcesses("42 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n43 /private/tmp/My Test.app-old/Contents/MacOS/ChatGPT\n", app) {
		t.Fatal("another application's process blocked the selected bundle")
	}
}
