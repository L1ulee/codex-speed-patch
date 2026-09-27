package patch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codex-speed-patch/internal/asar"
	"codex-speed-patch/internal/platform"
	"codex-speed-patch/internal/state"
)

const RuleVersion = "speed-mode-api-v1"
const CreatedDate = "2026-09-06"

type Service struct {
	CodexHome            string
	AppPath              string
	AllowMacOSTest       bool
	AllowMacOSProduction bool
}

type Result struct {
	Changed  bool             `json:"changed"`
	Status   asar.ProbeStatus `json:"status"`
	Manifest *state.Manifest  `json:"manifest,omitempty"`
}

func (s Service) Status() (*Result, error) {
	inst, err := platform.Detect(s.AppPath)
	if err != nil {
		return nil, err
	}
	archive, err := asar.Open(inst.AsarPath)
	if err != nil {
		return nil, err
	}
	probe, err := archive.Probe()
	if err != nil {
		return nil, err
	}
	manifest, _ := state.Load(state.ManifestPath(s.CodexHome))
	return &Result{Status: probe.Status, Manifest: manifest}, nil
}

func (s Service) Repair(sessionID string, markRestart bool) (*Result, error) {
	release, err := state.AcquireLock(state.LockPath(s.CodexHome))
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return &Result{Changed: false, Status: asar.StatusUnknown}, nil
		}
		return nil, fmt.Errorf("acquire patch lock: %w", err)
	}
	defer release()

	inst, err := platform.Detect(s.AppPath)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" {
		appPath := filepath.Clean(inst.AppPath)
		isApplicationsPath := strings.HasPrefix(appPath, "/Applications/") || strings.HasPrefix(appPath, filepath.Join(os.Getenv("HOME"), "Applications")+string(os.PathSeparator))
		if isApplicationsPath && !s.AllowMacOSProduction {
			return nil, errors.New("macOS Applications paths require --allow-macos-production")
		}
		if !isApplicationsPath && !s.AllowMacOSTest {
			return nil, errors.New("isolated macOS copies require --allow-macos-test")
		}
	}
	archive, err := asar.Open(inst.AsarPath)
	if err != nil {
		return nil, err
	}
	probe, err := archive.Probe()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	manifestPath := state.ManifestPath(s.CodexHome)
	manifest, _ := state.Load(manifestPath)
	if manifest == nil {
		manifest = &state.Manifest{SchemaVersion: state.SchemaVersion, PatchRuleVersion: RuleVersion, CreatedDate: CreatedDate}
	}
	manifest.AppPath = inst.AppPath
	manifest.AppVersion = inst.Version
	manifest.CLIPath = inst.CLIPath
	manifest.CLIVersion = platform.CLIVersion(inst.CLIPath)
	manifest.Platform = inst.Platform
	manifest.PackageKind = inst.PackageKind
	manifest.LastCheckedAt = now
	if sessionID != "" {
		manifest.ResumeSessionID = sessionID
	}

	switch probe.Status {
	case asar.StatusPatched:
		hash, hashErr := asar.FileSHA256(inst.AsarPath)
		if hashErr != nil {
			return nil, hashErr
		}
		manifest.Status = string(asar.StatusPatched)
		manifest.PatchedAsarSHA256 = hash
		manifest.LastError = ""
		if err := state.Save(manifestPath, manifest); err != nil {
			return nil, err
		}
		return &Result{Changed: false, Status: probe.Status, Manifest: manifest}, nil
	case asar.StatusUnknown:
		manifest.Status = string(asar.StatusUnknown)
		manifest.LastError = "no unique supported speed-mode gate was found"
		_ = state.Save(manifestPath, manifest)
		return nil, errors.New(manifest.LastError)
	case asar.StatusOriginal:
		if err := platform.Preflight(inst, s.AllowMacOSTest); err != nil {
			return nil, err
		}
		if inst.PackageKind == "appimage-mounted" {
			manifest.Status = "unsupported-package"
			manifest.LastError = "mounted AppImage is immutable; use an extracted or repacked AppImage"
			_ = state.Save(manifestPath, manifest)
			return nil, errors.New(manifest.LastError)
		}
	}

	originalHash, err := asar.FileSHA256(inst.AsarPath)
	if err != nil {
		return nil, err
	}
	backupDir, err := createBackup(s.CodexHome, inst, originalHash, runtime.GOOS == "darwin")
	if err != nil {
		return nil, err
	}
	tempPath := inst.AsarPath + ".speedpatch.tmp"
	_ = os.Remove(tempPath)
	patchResult, err := asar.PatchCopy(inst.AsarPath, tempPath)
	if err != nil {
		return nil, err
	}
	if err := replaceFile(tempPath, inst.AsarPath); err != nil {
		return nil, err
	}
	rollback := func(cause error) error {
		if restoreErr := restoreBackup(inst, backupDir); restoreErr != nil {
			return fmt.Errorf("%v; rollback failed: %w", cause, restoreErr)
		}
		return cause
	}
	if err := platform.Finalize(inst, patchResult.PatchedHeaderHash); err != nil {
		return nil, rollback(err)
	}
	verifiedArchive, err := asar.Open(inst.AsarPath)
	if err != nil {
		return nil, rollback(err)
	}
	verifiedProbe, err := verifiedArchive.Probe()
	if err != nil || verifiedProbe.Status != asar.StatusPatched {
		if err == nil {
			err = fmt.Errorf("post-finalize probe returned %s", verifiedProbe.Status)
		}
		return nil, rollback(err)
	}
	finalHash, err := asar.FileSHA256(inst.AsarPath)
	if err != nil {
		return nil, rollback(err)
	}
	manifest.PatchRuleVersion = RuleVersion
	manifest.TargetAsset = patchResult.TargetPath
	manifest.OriginalAsarSHA256 = originalHash
	manifest.PatchedAsarSHA256 = finalHash
	manifest.BackupDir = backupDir
	manifest.Status = string(asar.StatusPatched)
	manifest.PatchedAt = now
	manifest.LastError = ""
	manifest.RestartRequired = markRestart
	manifest.RestartFailure = ""
	manifest.RestartAttemptID = ""
	manifest.RestartAttemptedAt = time.Time{}
	manifest.RestartCompletedAt = time.Time{}
	if err := state.Save(manifestPath, manifest); err != nil {
		return nil, err
	}
	return &Result{Changed: true, Status: asar.StatusPatched, Manifest: manifest}, nil
}

func (s Service) Restore() error {
	release, err := state.AcquireLock(state.LockPath(s.CodexHome))
	if err != nil {
		return fmt.Errorf("acquire patch lock: %w", err)
	}
	defer release()
	manifestPath := state.ManifestPath(s.CodexHome)
	manifest, err := state.Load(manifestPath)
	if err != nil {
		return err
	}
	if manifest == nil || manifest.BackupDir == "" {
		return errors.New("no patch backup is recorded")
	}
	inst, err := platform.Detect(manifest.AppPath)
	if err != nil {
		return err
	}
	if err := restoreBackup(inst, manifest.BackupDir); err != nil {
		return err
	}
	manifest.Status = "restored"
	manifest.RestartRequired = false
	manifest.RestartFailure = ""
	manifest.LastCheckedAt = time.Now().UTC()
	return state.Save(manifestPath, manifest)
}

func createBackup(codexHome string, inst *platform.Installation, originalHash string, fullAppBackup bool) (string, error) {
	version := sanitize(inst.Version)
	if version == "" {
		version = "unknown"
	}
	dir := filepath.Join(state.Root(codexHome), "backups", version+"-"+originalHash[:12])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.asar")); errors.Is(err, os.ErrNotExist) {
		if err := copyFile(inst.AsarPath, filepath.Join(dir, "app.asar")); err != nil {
			return "", err
		}
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(filepath.Join(dir, "Info.plist")); errors.Is(err, os.ErrNotExist) {
			if err := copyFile(inst.InfoPlist, filepath.Join(dir, "Info.plist")); err != nil {
				return "", err
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "ChatGPT")); errors.Is(err, os.ErrNotExist) {
			if err := copyFile(inst.Executable, filepath.Join(dir, "ChatGPT")); err != nil {
				return "", err
			}
		}
		signatureDir := filepath.Join(inst.AppPath, "Contents", "_CodeSignature")
		if _, err := os.Stat(filepath.Join(dir, "_CodeSignature")); errors.Is(err, os.ErrNotExist) {
			if err := copyTree(signatureDir, filepath.Join(dir, "_CodeSignature")); err != nil {
				return "", err
			}
		}
		if fullAppBackup {
			fullBackupPath := filepath.Join(dir, "ChatGPT.app")
			if _, err := os.Stat(fullBackupPath); errors.Is(err, os.ErrNotExist) {
				if output, err := exec.Command("/bin/cp", "-cR", inst.AppPath, fullBackupPath).CombinedOutput(); err != nil {
					return "", fmt.Errorf("clone full macOS app backup: %s", strings.TrimSpace(string(output)))
				}
			}
		}
	}
	return dir, nil
}

func restoreBackup(inst *platform.Installation, backupDir string) error {
	if runtime.GOOS == "darwin" {
		fullBackupPath := filepath.Join(backupDir, "ChatGPT.app")
		if info, err := os.Stat(fullBackupPath); err == nil && info.IsDir() {
			restoredPath := inst.AppPath + ".speedpatch.restore"
			brokenPath := inst.AppPath + ".speedpatch.broken"
			_ = os.RemoveAll(restoredPath)
			_ = os.RemoveAll(brokenPath)
			if output, err := exec.Command("/bin/cp", "-cR", fullBackupPath, restoredPath).CombinedOutput(); err != nil {
				return fmt.Errorf("clone full app restore: %s", strings.TrimSpace(string(output)))
			}
			if err := os.Rename(inst.AppPath, brokenPath); err != nil {
				return fmt.Errorf("stage patched app for restore: %w", err)
			}
			if err := os.Rename(restoredPath, inst.AppPath); err != nil {
				_ = os.Rename(brokenPath, inst.AppPath)
				return fmt.Errorf("activate restored app: %w", err)
			}
			_ = os.RemoveAll(brokenPath)
			return platform.Verify(inst)
		}
	}
	if err := copyFile(filepath.Join(backupDir, "app.asar"), inst.AsarPath); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := copyFile(filepath.Join(backupDir, "Info.plist"), inst.InfoPlist); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(backupDir, "ChatGPT"), inst.Executable); err != nil {
			return err
		}
		signatureDir := filepath.Join(inst.AppPath, "Contents", "_CodeSignature")
		if err := os.RemoveAll(signatureDir); err != nil {
			return err
		}
		if err := copyTree(filepath.Join(backupDir, "_CodeSignature"), signatureDir); err != nil {
			return err
		}
	}
	return platform.Verify(inst)
}

func replaceFile(tempPath, destination string) error {
	rollbackPath := destination + ".speedpatch.old"
	_ = os.Remove(rollbackPath)
	if err := os.Rename(destination, rollbackPath); err != nil {
		return fmt.Errorf("stage original ASAR: %w", err)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		_ = os.Rename(rollbackPath, destination)
		return fmt.Errorf("activate patched ASAR: %w", err)
	}
	if err := os.Remove(rollbackPath); err != nil {
		return fmt.Errorf("remove staged original ASAR: %w", err)
	}
	return nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %s: %w", destination, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode().IsRegular() {
			return copyFile(path, target)
		}
		return nil
	})
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
	return strings.Trim(value, "-")
}

func RandomID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data)
}
