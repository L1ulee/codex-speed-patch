package restart

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"codex-speed-patch/internal/patch"
	"codex-speed-patch/internal/platform"
	"codex-speed-patch/internal/state"
)

type Service struct {
	CodexHome string
	AppPath   string
}

type Diagnostic struct {
	AppPath    string `json:"app_path"`
	Executable string `json:"executable"`
	PID        int    `json:"pid"`
}

func (s Service) Diagnose() (*Diagnostic, error) {
	inst, err := platform.Detect(s.AppPath)
	if err != nil {
		return nil, err
	}
	pid, err := findPID(inst)
	if err != nil {
		return nil, err
	}
	return &Diagnostic{AppPath: inst.AppPath, Executable: inst.Executable, PID: pid}, nil
}

func (s Service) Schedule(sessionID string, delay time.Duration) (bool, error) {
	release, err := state.AcquireLock(state.RestartLockPath(s.CodexHome))
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	defer release()

	manifestPath := state.ManifestPath(s.CodexHome)
	manifest, err := state.Load(manifestPath)
	if err != nil {
		return false, err
	}
	if manifest == nil || !manifest.RestartRequired || manifest.Status != "patched" {
		return false, nil
	}
	if sessionID != "" {
		manifest.ResumeSessionID = sessionID
	}
	attemptID := patch.RandomID()
	manifest.RestartRequired = false
	manifest.RestartAttemptID = attemptID
	manifest.RestartRequestedAt = time.Now().UTC()
	manifest.RestartFailure = ""
	if err := state.Save(manifestPath, manifest); err != nil {
		return false, err
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	args := []string{
		"restart-worker",
		"--codex-home", s.CodexHome,
		"--attempt-id", attemptID,
		"--delay", delay.String(),
	}
	if manifest.AppPath != "" {
		args = append(args, "--app", manifest.AppPath)
	}
	if err := startDetached(executable, args); err != nil {
		manifest.RestartRequired = true
		manifest.RestartFailure = err.Error()
		_ = state.Save(manifestPath, manifest)
		return false, err
	}
	return true, nil
}

func (s Service) RunWorker(attemptID string, delay time.Duration) error {
	manifestPath := state.ManifestPath(s.CodexHome)
	manifest, err := state.Load(manifestPath)
	if err != nil {
		return err
	}
	if manifest == nil || manifest.RestartAttemptID != attemptID || attemptID == "" {
		return errors.New("restart attempt is stale or missing")
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	inst, err := platform.Detect(firstNonEmpty(s.AppPath, manifest.AppPath))
	if err != nil {
		return s.fail(manifest, err)
	}
	manifest.RestartAttemptedAt = time.Now().UTC()
	if err := state.Save(manifestPath, manifest); err != nil {
		return err
	}

	if err := requestQuit(inst); err != nil {
		return s.fail(manifest, err)
	}
	if err := waitForExit(inst, 15*time.Second); err != nil {
		return s.fail(manifest, err)
	}
	if err := launch(inst, manifest.ResumeSessionID); err != nil {
		return s.fail(manifest, err)
	}
	manifest.RestartCompletedAt = time.Now().UTC()
	manifest.RestartFailure = ""
	return state.Save(manifestPath, manifest)
}

func (s Service) fail(manifest *state.Manifest, cause error) error {
	manifest.RestartRequired = true
	manifest.RestartFailure = cause.Error()
	_ = state.Save(state.ManifestPath(s.CodexHome), manifest)
	return cause
}

func requestQuit(inst *platform.Installation) error {
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.Command("/usr/bin/osascript", "-e", "tell application "+strconv.Quote(inst.AppPath)+" to quit").CombinedOutput()
		if err != nil {
			return fmt.Errorf("request Codex quit: %s", strings.TrimSpace(string(output)))
		}
		return nil
	case "windows":
		pid, err := findPID(inst)
		if err != nil || pid == 0 {
			return nil
		}
		output, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("request Codex quit: %s", strings.TrimSpace(string(output)))
		}
		return nil
	case "linux":
		pid, err := findPID(inst)
		if err != nil || pid == 0 {
			return nil
		}
		if output, err := exec.Command("kill", "-TERM", strconv.Itoa(pid)).CombinedOutput(); err != nil {
			return fmt.Errorf("request Codex quit: %s", strings.TrimSpace(string(output)))
		}
		return nil
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func waitForExit(inst *platform.Installation, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pid, err := findPID(inst)
		if err != nil {
			return err
		}
		if pid == 0 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("Codex did not exit within %s; force termination is disabled", timeout)
}

// QuitAndWait stops only the selected application and waits for its helpers.
// Process inspection errors abort replacement instead of treating it as stopped.
func QuitAndWait(inst *platform.Installation, timeout time.Duration) error {
	if runtime.GOOS != "darwin" {
		return errors.New("bundle replacement requires macOS")
	}
	remaining := func() (bool, error) {
		output, err := exec.Command("/bin/ps", "-axo", "pid=,comm=").Output()
		if err != nil {
			return false, fmt.Errorf("inspect application processes: %w", err)
		}
		return hasBundleProcesses(string(output), inst.AppPath), nil
	}
	running, err := remaining()
	if err != nil || !running {
		return err
	}
	// Target the selected PID: two isolated bundles may share a bundle ID.
	pid, err := findPID(inst)
	if err != nil {
		return err
	}
	if pid == 0 {
		return errors.New("application helpers remain without a main process; replacement cancelled")
	}
	// NSRunningApplication sends the normal quit event to this exact PID and
	// lets the app clean up its native children. SIGTERM can orphan monitors.
	script := fmt.Sprintf(`ObjC.import("AppKit"); var selected = $.NSRunningApplication.runningApplicationWithProcessIdentifier(%d); if (!selected.isNil() && !selected.terminate) throw Error("Application rejected normal termination");`, pid)
	if output, err := exec.Command("/usr/bin/osascript", "-l", "JavaScript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("request application shutdown: %s", strings.TrimSpace(string(output)))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err = remaining()
		if err != nil || !running {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("application or helpers still running after %s; replacement cancelled", timeout)
}

func hasBundleProcesses(output, appPath string) bool {
	prefix := filepath.Clean(appPath) + string(os.PathSeparator) + "Contents" + string(os.PathSeparator)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

type LaunchOptions struct {
	Args []string
	Env  []string
}

// LaunchAndVerify detects immediate crashes, not just a successful open command.
func LaunchAndVerify(inst *platform.Installation, sessionID string, options ...LaunchOptions) error {
	if err := launch(inst, sessionID, options...); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		var err error
		pid, err = findPID(inst)
		if err != nil {
			return err
		}
		if pid != 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if pid == 0 {
		return errors.New("application did not start")
	}
	time.Sleep(10 * time.Second)
	current, err := findPID(inst)
	if err != nil {
		return err
	}
	if current != pid {
		return errors.New("application exited shortly after launch")
	}
	return nil
}

func findPID(inst *platform.Installation) (int, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
		psPath, err := exec.LookPath("ps")
		if err != nil {
			return 0, err
		}
		output, err := exec.Command(psPath, "-axo", "pid=,command=").Output()
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 2 {
				continue
			}
			pid, parseErr := strconv.Atoi(fields[0])
			if parseErr != nil || pid <= 0 || pid == os.Getpid() {
				continue
			}
			commandLine := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
			if commandLine == inst.Executable || strings.HasPrefix(commandLine, inst.Executable+" ") {
				return pid, nil
			}
		}
		return 0, nil
	case "windows":
		command := exec.Command("powershell", "-NoProfile", "-Command", fmt.Sprintf(`(Get-Process | Where-Object {$_.Path -eq '%s'} | Select-Object -First 1 -ExpandProperty Id)`, strings.ReplaceAll(inst.Executable, "'", "''")))
		output, err := command.Output()
		if err != nil {
			return 0, nil
		}
		for _, line := range strings.Fields(string(output)) {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(line))
			if parseErr == nil && pid > 0 && pid != os.Getpid() {
				return pid, nil
			}
		}
		return 0, nil
	default:
		return 0, nil
	}
}

func launch(inst *platform.Installation, sessionID string, options ...LaunchOptions) error {
	deepLink := ""
	if sessionID != "" {
		deepLink = "codex://threads/" + sessionID
	}
	switch runtime.GOOS {
	case "darwin":
		args := []string{"-na", inst.AppPath}
		if len(options) > 0 {
			for _, entry := range options[0].Env {
				args = append(args, "--env", entry)
			}
			if len(options[0].Args) > 0 {
				args = append(args, "--args")
				args = append(args, options[0].Args...)
			}
		}
		if output, err := exec.Command("/usr/bin/open", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("launch Codex: %s", strings.TrimSpace(string(output)))
		}
		if deepLink != "" {
			time.Sleep(2 * time.Second)
			_ = exec.Command("/usr/bin/open", deepLink).Run()
		}
		return nil
	case "windows":
		args := []string{}
		if deepLink != "" {
			args = append(args, deepLink)
		}
		return exec.Command(inst.Executable, args...).Start()
	case "linux":
		args := []string{}
		if deepLink != "" {
			args = append(args, deepLink)
		}
		return exec.Command(inst.Executable, args...).Start()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
