package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func Finalize(inst *Installation, archiveHeaderSHA256 string) error {
	switch runtime.GOOS {
	case "darwin":
		if err := setMacASARIntegrity(inst.InfoPlist, archiveHeaderSHA256); err != nil {
			return err
		}
		args := []string{"--force", "--deep", "--sign", "-", inst.AppPath}
		if output, err := exec.Command("/usr/bin/codesign", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("re-sign Codex: %s", strings.TrimSpace(string(output)))
		}
		return Verify(inst)
	case "windows", "linux":
		return Verify(inst)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
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
