// Package deploy replaces macOS bundles after repairing and verifying a copy.
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codex-speed-patch/internal/asar"
	"codex-speed-patch/internal/patch"
	"codex-speed-patch/internal/platform"
	"codex-speed-patch/internal/restart"
	"codex-speed-patch/internal/state"
)

type Service struct {
	CodexHome     string
	AppPath       string
	AllowTestPath bool
	operations    *operations
}

type Record struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	AppPath        string          `json:"app_path"`
	AppVersion     string          `json:"app_version"`
	SourceHash     string          `json:"source_asar_sha256"`
	SourceInfoHash string          `json:"source_info_sha256"`
	SourceCLIHash  string          `json:"source_cli_sha256,omitempty"`
	PatchedHash    string          `json:"patched_asar_sha256"`
	Directory      string          `json:"directory"`
	StagePath      string          `json:"stage_path"`
	RollbackPath   string          `json:"rollback_path"`
	TestMode       bool            `json:"test_mode"`
	Manifest       *state.Manifest `json:"manifest"`
	HadManifest    bool            `json:"had_manifest"`
	CreatedAt      time.Time       `json:"created_at"`
	CompletedAt    time.Time       `json:"completed_at,omitempty"`
	Error          string          `json:"error,omitempty"`
	RolledBack     bool            `json:"rolled_back,omitempty"`
}

type Result struct {
	Changed    bool            `json:"changed"`
	Status     string          `json:"status"`
	Manifest   *state.Manifest `json:"manifest,omitempty"`
	Deployment *Record         `json:"deployment,omitempty"`
}

func RecordPath(home string) string {
	return filepath.Join(state.Root(home), "deployment.json")
}

func Load(home string) (*Record, error) {
	data, err := os.ReadFile(RecordPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode deployment record: %w", err)
	}
	return &record, nil
}

func save(home string, record *Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(RecordPath(home), append(data, '\n'))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".deploy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

// Prepare keeps the installed bundle unchanged. Only Schedule starts replacement.
func (s Service) Prepare(sessionID string) (result *Result, err error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("production bundle replacement requires macOS")
	}
	s.CodexHome, err = filepath.Abs(s.CodexHome)
	if err != nil {
		return nil, err
	}
	release, err := state.AcquireLock(state.LockPath(s.CodexHome))
	if err != nil {
		return nil, fmt.Errorf("acquire deployment lock: %w", err)
	}
	defer release()
	inst, err := platform.Detect(s.AppPath)
	if err != nil {
		return nil, err
	}
	inst.AppPath, err = filepath.Abs(inst.AppPath)
	if err != nil {
		return nil, err
	}
	// Detect again so that all installation paths use the same absolute root.
	inst, err = platform.Detect(inst.AppPath)
	if err != nil {
		return nil, err
	}
	if err := validateTarget(inst.AppPath, s.AllowTestPath); err != nil {
		return nil, err
	}
	previous, err := Load(s.CodexHome)
	if err != nil {
		return nil, err
	}
	if previous != nil && (previous.Status == "prepared" || previous.Status == "scheduled" || previous.Status == "deploying") {
		return nil, fmt.Errorf("deployment %s is %s; inspect status before starting another replacement", previous.ID, previous.Status)
	}
	current, err := (patch.Service{CodexHome: s.CodexHome, AppPath: inst.AppPath}).Status()
	if err != nil {
		return nil, err
	}
	if err := platform.Verify(inst); err != nil {
		return nil, err
	}
	if current.Status == asar.StatusPatched {
		if err := platform.VerifyUpdateSigning(inst); err != nil {
			return nil, err
		}
		return &Result{Status: "patched", Manifest: current.Manifest}, nil
	}
	if current.Status != asar.StatusOriginal && current.Status != asar.StatusPartial {
		return nil, fmt.Errorf("unsupported patch state %s; application left unchanged", current.Status)
	}
	sourceHash, err := asar.FileSHA256(inst.AsarPath)
	if err != nil {
		return nil, err
	}
	infoHash, err := asar.FileSHA256(inst.InfoPlist)
	if err != nil {
		return nil, err
	}
	sourceCLIHash := ""
	if inst.CLIPath != "" {
		sourceCLIHash, err = asar.FileSHA256(inst.CLIPath)
		if err != nil {
			return nil, err
		}
	}
	work, err := os.MkdirTemp("", "codex-speed-deploy-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	copyPath := filepath.Join(work, "ChatGPT.app")
	if err := clone(inst.AppPath, copyPath); err != nil {
		return nil, err
	}
	// The existing patcher enforces its signing preflight on this temporary copy.
	patched, err := (patch.Service{CodexHome: filepath.Join(work, "patch-home"), AppPath: copyPath, AllowMacOSTest: true}).Repair(sessionID, false)
	if err != nil {
		return nil, err
	}
	manifest := patched.Manifest
	backupDir := filepath.Join(state.Root(s.CodexHome), "backups", filepath.Base(manifest.BackupDir))
	backupHash := sourceHash
	// A partial upgrade must retain the previously recorded original backup.
	if current.Status == asar.StatusPartial && current.Manifest != nil && current.Manifest.AppPath == inst.AppPath && current.Manifest.AppVersion == inst.Version && current.Manifest.BackupDir != "" {
		backupDir = current.Manifest.BackupDir
		backupHash = current.Manifest.OriginalAsarSHA256
		if err := verifyBundle(filepath.Join(backupDir, "ChatGPT.app"), inst.Version, backupHash, false); err != nil {
			return nil, fmt.Errorf("verify existing original backup: %w", err)
		}
	} else {
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return nil, err
		}
		fullBackup := filepath.Join(backupDir, "ChatGPT.app")
		if _, err := os.Stat(fullBackup); errors.Is(err, os.ErrNotExist) {
			pendingDirectory, err := os.MkdirTemp(backupDir, ".preparing-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(pendingDirectory)
			pending := filepath.Join(pendingDirectory, "ChatGPT.app")
			if err := clone(filepath.Join(manifest.BackupDir, "ChatGPT.app"), pending); err != nil {
				return nil, err
			}
			if err := verifyBundle(pending, inst.Version, sourceHash, false); err != nil {
				return nil, err
			}
			if err := os.Rename(pending, fullBackup); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if err := verifyBundle(fullBackup, inst.Version, sourceHash, false); err != nil {
			return nil, fmt.Errorf("verify original backup: %w", err)
		}
	}
	id := patch.RandomID()
	directory := filepath.Join(state.Root(s.CodexHome), "deployments", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	sibling := filepath.Join(filepath.Dir(inst.AppPath), ".codex-speed-deploy-"+id)
	if err := os.Mkdir(sibling, 0o700); err != nil {
		return nil, fmt.Errorf("create replacement directory beside application: %w", err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(sibling)
		}
	}()
	stagePath := filepath.Join(sibling, "ChatGPT.app")
	if err := clone(copyPath, stagePath); err != nil {
		return nil, err
	}
	if err := verifyBundle(stagePath, inst.Version, manifest.PatchedAsarSHA256, true); err != nil {
		return nil, err
	}
	previousBytes, err := os.ReadFile(state.ManifestPath(s.CodexHome))
	hadManifest := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if hadManifest {
		if err := writeAtomic(filepath.Join(directory, "previous-manifest.json"), previousBytes); err != nil {
			return nil, err
		}
	}
	manifest.AppPath = inst.AppPath
	manifest.CLIPath = inst.CLIPath
	manifest.BackupDir = backupDir
	manifest.OriginalAsarSHA256 = backupHash
	record := &Record{ID: id, Status: "prepared", AppPath: inst.AppPath, AppVersion: inst.Version,
		SourceHash: sourceHash, SourceInfoHash: infoHash, PatchedHash: manifest.PatchedAsarSHA256,
		SourceCLIHash: sourceCLIHash,
		Directory:     directory, StagePath: stagePath, RollbackPath: filepath.Join(sibling, "original.app"),
		TestMode: s.AllowTestPath, Manifest: manifest, HadManifest: hadManifest, CreatedAt: time.Now().UTC()}
	if err := save(s.CodexHome, record); err != nil {
		return nil, err
	}
	return &Result{Status: "prepared", Deployment: record}, nil
}

func validateTarget(path string, test bool) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if filepath.Clean(path) != resolved {
		return errors.New("replacement target must be its canonical path, without symlink aliases")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if test {
		for _, root := range []string{os.TempDir(), "/private/tmp"} {
			canonical, err := filepath.EvalSymlinks(root)
			if err == nil && strings.HasPrefix(resolved, canonical+string(os.PathSeparator)) {
				return nil
			}
		}
		return errors.New("replacement tests require a canonical temporary app path")
	}
	for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
		if strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
			return nil
		}
	}
	return errors.New("production replacement requires an Applications path")
}

func clone(source, destination string) error {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clone destination already exists or is inaccessible: %s", destination)
	}
	output, err := exec.Command("/bin/cp", "-cR", source, destination).CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone application: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func verifyBundle(path, version, hash string, patched bool) error {
	inst, err := platform.Detect(path)
	if err != nil {
		return err
	}
	if inst.Version != version {
		return errors.New("application version changed during deployment")
	}
	actual, err := asar.FileSHA256(inst.AsarPath)
	if err != nil {
		return err
	}
	if actual != hash {
		return errors.New("application hash changed during deployment")
	}
	if err := platform.Verify(inst); err != nil {
		return err
	}
	if patched {
		if err := platform.VerifyUpdateSigning(inst); err != nil {
			return err
		}
		archive, err := asar.Open(inst.AsarPath)
		if err != nil {
			return err
		}
		probe, err := archive.Probe()
		if err != nil {
			return err
		}
		if probe.Status != asar.StatusPatched {
			return fmt.Errorf("replacement bundle reports %s", probe.Status)
		}
		if plan, err := platform.ModelCatalogPatch(inst, probe.UltrafastModels); err != nil || plan != nil {
			if err != nil {
				return err
			}
			return errors.New("replacement native CLI still requires Ultrafast catalog repair")
		}
	}
	return nil
}

type operations struct {
	verify   func(string, string, string, bool) error
	quit     func(string) error
	launch   func(string, string) error
	rename   func(string, string) error
	manifest func(string, *state.Manifest) error
}

func (s Service) ops(record *Record) operations {
	if s.operations != nil {
		return *s.operations
	}
	return operations{verify: verifyBundle, rename: os.Rename, manifest: state.Save,
		quit: func(path string) error {
			inst, err := platform.Detect(path)
			if err != nil {
				return err
			}
			return restart.QuitAndWait(inst, 30*time.Second)
		},
		launch: func(path, session string) error {
			inst, err := platform.Detect(path)
			if err != nil {
				return err
			}
			if record.TestMode {
				return restart.LaunchAndVerify(inst, "", restart.LaunchOptions{
					Args: []string{"--user-data-dir=" + filepath.Join(record.Directory, "profile")},
					Env:  []string{"CODEX_HOME=" + filepath.Join(record.Directory, "app-home")},
				})
			}
			return restart.LaunchAndVerify(inst, session)
		}}
}

// RunWorker owns the patch lock until activation, launch verification or rollback
// completes. It revalidates both packages and the previous manifest before exit.
func (s Service) RunWorker(id string) error {
	release, err := state.AcquireLock(state.LockPath(s.CodexHome))
	if err != nil {
		return err
	}
	defer release()
	record, err := Load(s.CodexHome)
	if err != nil {
		return err
	}
	if record == nil || id == "" || record.ID != id || record.Status != "scheduled" {
		return errors.New("deployment attempt is stale, missing or not scheduled")
	}
	if err := s.activate(record); err != nil {
		record.Status, record.Error = "failed", err.Error()
		record.CompletedAt = time.Now().UTC()
		if saveErr := save(s.CodexHome, record); saveErr != nil {
			return fmt.Errorf("%v; save deployment failure: %w", err, saveErr)
		}
		return err
	}
	return nil
}

func validateRecord(home string, record *Record) error {
	if record.Manifest == nil || record.ID == "" || strings.ContainsAny(record.ID, "/\\. \n\r") {
		return errors.New("invalid deployment record")
	}
	if err := validateTarget(record.AppPath, record.TestMode); err != nil {
		return err
	}
	directory, err := filepath.Abs(filepath.Join(state.Root(home), "deployments", record.ID))
	if err != nil {
		return err
	}
	sibling := filepath.Join(filepath.Dir(record.AppPath), ".codex-speed-deploy-"+record.ID)
	if record.Directory != directory || record.StagePath != filepath.Join(sibling, "ChatGPT.app") || record.RollbackPath != filepath.Join(sibling, "original.app") {
		return errors.New("deployment paths differ from the prepared transaction")
	}
	if record.Manifest.AppPath != record.AppPath || record.Manifest.AppVersion != record.AppVersion || record.Manifest.PatchedAsarSHA256 != record.PatchedHash || record.Manifest.BackupDir == "" {
		return errors.New("deployment manifest differs from the prepared transaction")
	}
	return nil
}

func (s Service) activate(record *Record) (err error) {
	if err := validateRecord(s.CodexHome, record); err != nil {
		return err
	}
	ops := s.ops(record)
	manifestPath := state.ManifestPath(s.CodexHome)
	previousPath := filepath.Join(record.Directory, "previous-manifest.json")
	var previous []byte
	if record.HadManifest {
		previous, err = os.ReadFile(previousPath)
		if err != nil {
			return err
		}
	}
	installedManifest, readErr := os.ReadFile(manifestPath)
	if record.HadManifest {
		if readErr != nil || string(previous) != string(installedManifest) {
			return errors.New("patch manifest changed after deployment preparation")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("patch manifest appeared or became inaccessible after preparation")
	}
	if err := ops.verify(record.AppPath, record.AppVersion, record.SourceHash, false); err != nil {
		return err
	}
	if err := ops.verify(record.StagePath, record.AppVersion, record.PatchedHash, true); err != nil {
		return err
	}
	if err := verifyCLIHash(record.StagePath, record.Manifest.PatchedCLISHA256); err != nil {
		return err
	}
	if err := verifyCLIHash(record.AppPath, record.SourceCLIHash); err != nil {
		return err
	}
	if err := ops.verify(filepath.Join(record.Manifest.BackupDir, "ChatGPT.app"), record.AppVersion, record.Manifest.OriginalAsarSHA256, false); err != nil {
		return fmt.Errorf("original backup is invalid: %w", err)
	}
	infoHash, err := asar.FileSHA256(filepath.Join(record.AppPath, "Contents", "Info.plist"))
	if err != nil || infoHash != record.SourceInfoHash {
		return errors.New("installed application metadata changed after preparation")
	}
	if _, err := os.Lstat(record.RollbackPath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("rollback path is already occupied or inaccessible")
	}
	record.Status = "deploying"
	if err := save(s.CodexHome, record); err != nil {
		return err
	}
	if err := ops.quit(record.AppPath); err != nil {
		return err
	}
	originalMoved := false
	defer func() {
		if err != nil && !originalMoved {
			if reopenErr := ops.launch(record.AppPath, record.Manifest.ResumeSessionID); reopenErr != nil {
				err = fmt.Errorf("%v; reopen unchanged application: %w", err, reopenErr)
			}
		}
	}()
	// An update may have finished while the application was shutting down.
	if err := ops.verify(record.AppPath, record.AppVersion, record.SourceHash, false); err != nil {
		return err
	}
	if err := verifyCLIHash(record.AppPath, record.SourceCLIHash); err != nil {
		return err
	}
	infoHash, err = asar.FileSHA256(filepath.Join(record.AppPath, "Contents", "Info.plist"))
	if err != nil || infoHash != record.SourceInfoHash {
		return errors.New("application metadata changed during shutdown")
	}
	if err := ops.rename(record.AppPath, record.RollbackPath); err != nil {
		return fmt.Errorf("stage original application: %w", err)
	}
	originalMoved = true
	activated := false
	defer func() {
		if err == nil {
			return
		}
		if activated {
			if stopErr := ops.quit(record.AppPath); stopErr != nil {
				err = fmt.Errorf("%v; rollback shutdown failed: %w; original retained at %s", err, stopErr, record.RollbackPath)
				return
			}
			if moveErr := ops.rename(record.AppPath, record.StagePath); moveErr != nil {
				err = fmt.Errorf("%v; stage failed replacement: %w", err, moveErr)
				return
			}
		}
		if restoreErr := ops.rename(record.RollbackPath, record.AppPath); restoreErr != nil {
			err = fmt.Errorf("%v; restore original application: %w", err, restoreErr)
			return
		}
		if record.HadManifest {
			if restoreErr := writeAtomic(manifestPath, previous); restoreErr != nil {
				err = fmt.Errorf("%v; restore patch manifest: %w", err, restoreErr)
				return
			}
		} else if removeErr := os.Remove(manifestPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = fmt.Errorf("%v; remove replacement manifest: %w", err, removeErr)
			return
		}
		if verifyErr := ops.verify(record.AppPath, record.AppVersion, record.SourceHash, false); verifyErr != nil {
			err = fmt.Errorf("%v; verify rolled-back application: %w", err, verifyErr)
			return
		}
		record.RolledBack = true
		if launchErr := ops.launch(record.AppPath, record.Manifest.ResumeSessionID); launchErr != nil {
			err = fmt.Errorf("%v; reopen restored application: %w", err, launchErr)
		}
	}()
	if err := ops.rename(record.StagePath, record.AppPath); err != nil {
		return fmt.Errorf("activate replacement: %w", err)
	}
	activated = true
	if err := ops.verify(record.AppPath, record.AppVersion, record.PatchedHash, true); err != nil {
		return err
	}
	if err := verifyCLIHash(record.AppPath, record.Manifest.PatchedCLISHA256); err != nil {
		return err
	}
	manifest := record.Manifest
	manifest.LastCheckedAt = time.Now().UTC()
	manifest.RestartRequired = false
	if err := ops.manifest(manifestPath, manifest); err != nil {
		return err
	}
	if err := ops.launch(record.AppPath, manifest.ResumeSessionID); err != nil {
		return err
	}
	manifest.RestartCompletedAt = time.Now().UTC()
	if err := ops.manifest(manifestPath, manifest); err != nil {
		return err
	}
	record.Status, record.CompletedAt = "deployed", time.Now().UTC()
	if err := save(s.CodexHome, record); err != nil {
		return err
	}
	// A full durable backup remains available to the existing restore command.
	_ = os.RemoveAll(filepath.Dir(record.RollbackPath))
	return nil
}

func verifyCLIHash(appPath, expected string) error {
	if expected == "" {
		return nil
	}
	inst, err := platform.Detect(appPath)
	if err != nil {
		return err
	}
	hash, err := asar.FileSHA256(inst.CLIPath)
	if err != nil {
		return err
	}
	if hash != expected {
		return errors.New("native CLI changed after deployment preparation")
	}
	return nil
}
