package deploy

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"codex-speed-patch/internal/state"
)

// Schedule uses a one-shot launchd job independently of the application.
// KeepAlive is false; the plist is not installed as a login LaunchAgent.
func (s Service) Schedule(id string) (*Result, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("production replacement requires macOS")
	}
	release, err := state.AcquireLock(state.LockPath(s.CodexHome))
	if err != nil {
		return nil, err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	record, err := Load(s.CodexHome)
	if err != nil {
		return nil, err
	}
	if record == nil || record.ID != id || record.Status != "prepared" {
		return nil, errors.New("deployment attempt is not prepared")
	}
	fail := func(cause error) (*Result, error) {
		record.Status, record.Error = "failed", cause.Error()
		if err := save(s.CodexHome, record); err != nil {
			return nil, fmt.Errorf("%v; save scheduling failure: %w", cause, err)
		}
		return nil, cause
	}
	executable, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return fail(err)
	}
	worker := filepath.Join(record.Directory, "codex-speed-patch")
	if err := writeAtomic(worker, data); err != nil {
		return fail(err)
	}
	if err := os.Chmod(worker, 0o700); err != nil {
		return fail(err)
	}
	home, err := filepath.Abs(s.CodexHome)
	if err != nil {
		return fail(err)
	}
	plistPath := filepath.Join(record.Directory, "deployment.plist")
	if err := writeAtomic(plistPath, []byte(launchPlist(record, worker, home))); err != nil {
		return fail(err)
	}
	record.Status = "scheduled"
	if err := save(s.CodexHome, record); err != nil {
		return nil, err
	}
	// The worker may start before bootstrap returns. The scheduled record now
	// excludes another preparation, so release the lock before starting it.
	release()
	release = nil
	if output, err := exec.Command("/bin/launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("schedule replacement: %s", strings.TrimSpace(string(output))))
	}
	return &Result{Status: "scheduled", Deployment: record}, nil
}

func launchPlist(record *Record, worker, home string) string {
	escape := func(value string) string {
		var result strings.Builder
		_ = xml.EscapeText(&result, []byte(value))
		return result.String()
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>deploy-worker</string><string>--codex-home</string><string>%s</string><string>--attempt-id</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><false/>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, escape(jobLabel(record.ID)), escape(worker), escape(home), escape(record.ID), escape(filepath.Join(record.Directory, "deployment.log")), escape(filepath.Join(record.Directory, "deployment.log")))
}

func jobLabel(id string) string { return "com.liulee.codex-speed-patch.deploy-" + id }

// Unload runs only after the final deployment result has been persisted.
func Unload(id string) {
	if runtime.GOOS != "darwin" || id == "" || strings.ContainsAny(id, "/\\ \n\r") {
		return
	}
	// Wait for launchctl to deliver bootout. Releasing a child and immediately
	// exiting can leave the job registered even though KeepAlive is false.
	_ = exec.Command("/bin/launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), jobLabel(id))).Run()
}
