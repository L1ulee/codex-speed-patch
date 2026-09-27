package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const macSigningEntitlements = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>com.apple.security.cs.disable-library-validation</key><true/>
<key>com.apple.security.cs.allow-jit</key><true/>
<key>com.apple.security.cs.allow-unsigned-executable-memory</key><true/>
</dict></plist>`

func Finalize(inst *Installation, archiveHeaderSHA256 string) error {
	switch runtime.GOOS {
	case "darwin":
		oldIntegrity, err := readASARIntegrity(inst.InfoPlist)
		if err != nil {
			return err
		}
		if err := setMacASARIntegrity(inst.InfoPlist, archiveHeaderSHA256); err != nil {
			return err
		}
		newIntegrity, err := readASARIntegrity(inst.InfoPlist)
		if err != nil {
			return err
		}
		if err := updateFrameworkASARIntegrity(inst, oldIntegrity, newIntegrity); err != nil {
			return err
		}
		entitlementsPath, err := writeMacSigningEntitlements()
		if err != nil {
			return err
		}
		defer os.Remove(entitlementsPath)
		steps, err := macSignSteps(inst, entitlementsPath)
		if err != nil {
			return err
		}
		for index, args := range steps {
			if index < len(steps)-2 {
				helperEntitlements, err := writeHelperSigningEntitlements(args[len(args)-1])
				if err != nil {
					return err
				}
				defer os.Remove(helperEntitlements)
				args = append(args[:len(args)-1], "--entitlements", helperEntitlements, args[len(args)-1])
			}
			if output, err := exec.Command("/usr/bin/codesign", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("re-sign %s: %s", args[len(args)-1], strings.TrimSpace(string(output)))
			}
		}
		return Verify(inst)
	case "windows", "linux":
		return Verify(inst)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func macSignSteps(inst *Installation, entitlementsPath string) ([][]string, error) {
	framework := filepath.Join(inst.AppPath, "Contents", "Frameworks", "Codex Framework.framework")
	helpers, err := filepath.Glob(filepath.Join(framework, "Helpers", "*.app"))
	if err != nil || len(helpers) == 0 {
		return nil, fmt.Errorf("locate Codex Framework helpers: %v", err)
	}
	steps := make([][]string, 0, len(helpers)+2)
	for _, helper := range helpers {
		if !isWithinApp(inst.AppPath, helper) {
			return nil, fmt.Errorf("Codex helper resolves outside app bundle: %s", helper)
		}
		steps = append(steps, []string{"--force", "--sign", "-", "--preserve-metadata=identifier,flags,runtime", helper})
	}
	steps = append(steps,
		[]string{"--force", "--sign", "-", "--preserve-metadata=identifier,flags,runtime", framework},
		[]string{"--force", "--sign", "-", "--preserve-metadata=identifier,flags,runtime", "--entitlements", entitlementsPath, inst.AppPath},
	)
	return steps, nil
}

func writeHelperSigningEntitlements(helper string) (string, error) {
	file, err := os.CreateTemp("", "codex-speed-patch-helper-entitlements-*.plist")
	if err != nil {
		return "", fmt.Errorf("create helper entitlements: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if output, err := exec.Command("/usr/bin/codesign", "-d", "--entitlements", path, "--xml", helper).CombinedOutput(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("read helper entitlements: %s", strings.TrimSpace(string(output)))
	}
	command := "Set :com.apple.security.cs.disable-library-validation true"
	if err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :com.apple.security.cs.disable-library-validation", path).Run(); err != nil {
		command = "Add :com.apple.security.cs.disable-library-validation bool true"
	}
	if output, err := exec.Command("/usr/libexec/PlistBuddy", "-c", command, path).CombinedOutput(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("update helper entitlements: %s", strings.TrimSpace(string(output)))
	}
	return path, nil
}

func writeMacSigningEntitlements() (string, error) {
	file, err := os.CreateTemp("", "codex-speed-patch-entitlements-*.plist")
	if err != nil {
		return "", fmt.Errorf("create macOS signing entitlements: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(macSigningEntitlements); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write macOS signing entitlements: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close macOS signing entitlements: %w", err)
	}
	return path, nil
}

func Preflight(inst *Installation, allowSignedTestCopy bool) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	helpersPath := filepath.Join(inst.AppPath, "Contents", "Frameworks", "Codex Framework.framework", "Helpers")
	if !isWithinApp(inst.AppPath, frameworkBinary(inst.AppPath)) || !isWithinApp(inst.AppPath, helpersPath) {
		return fmt.Errorf("Codex Framework resolves outside app bundle")
	}
	if err := Verify(inst); err != nil {
		return fmt.Errorf("verify original macOS app: %w", err)
	}
	output, err := exec.Command("/usr/bin/codesign", "-dv", "--verbose=4", inst.AppPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect macOS signing identity: %w: %s", err, strings.TrimSpace(string(output)))
	}
	allowSignedTestCopy = allowSignedTestCopy && isMacOSTestPath(inst.AppPath) && isMacOSTestPath(inst.AsarPath) && isMacOSTestPath(inst.InfoPlist) && isMacOSTestPath(inst.Executable) && isMacOSTestPath(frameworkBinary(inst.AppPath)) && isMacOSTestPath(helpersPath)
	return checkMacSigningOutput(output, allowSignedTestCopy)
}

func isWithinApp(appPath, target string) bool {
	app, err := filepath.EvalSymlinks(appPath)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	return strings.HasPrefix(resolved, app+string(os.PathSeparator))
}

func isMacOSTestPath(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for _, root := range []string{os.TempDir(), "/private/tmp"} {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err == nil && strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func checkMacSigningOutput(output []byte, allowSignedTestCopy bool) error {
	var team, signature string
	for _, line := range strings.Split(string(output), "\n") {
		if value, ok := strings.CutPrefix(line, "TeamIdentifier="); ok {
			team = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "Signature="); ok {
			signature = strings.TrimSpace(value)
		}
	}
	if team == "not set" && signature == "adhoc" {
		return nil
	}
	if team != "" && team != "not set" && !allowSignedTestCopy {
		return fmt.Errorf("macOS app has TeamIdentifier %s; ad-hoc re-signing removes this identity and can prevent launch", team)
	}
	if team != "" && team != "not set" {
		return nil
	}
	return fmt.Errorf("macOS app signing identity is not recognized; refusing to modify the app")
}

func Verify(inst *Installation) error {
	if _, err := os.Stat(inst.AsarPath); err != nil {
		return fmt.Errorf("verify ASAR: %w", err)
	}
	if runtime.GOOS == "darwin" {
		output, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", inst.AppPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("verify Codex signature: %s", strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func setMacASARIntegrity(infoPlist, archiveSHA256 string) error {
	command := fmt.Sprintf("Set :ElectronAsarIntegrity:Resources/app.asar:hash %s", archiveSHA256)
	output, err := exec.Command("/usr/libexec/PlistBuddy", "-c", command, infoPlist).CombinedOutput()
	if err != nil {
		return fmt.Errorf("update ElectronAsarIntegrity: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
