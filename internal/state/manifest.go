package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const SchemaVersion = 1

type Manifest struct {
	SchemaVersion      int       `json:"schema_version"`
	PatchRuleVersion   string    `json:"patch_rule_version"`
	CreatedDate        string    `json:"created_date"`
	AppPath            string    `json:"app_path"`
	AppVersion         string    `json:"app_version"`
	CLIPath            string    `json:"cli_path,omitempty"`
	CLIVersion         string    `json:"cli_version,omitempty"`
	Platform           string    `json:"platform"`
	PackageKind        string    `json:"package_kind"`
	TargetAsset        string    `json:"target_asset,omitempty"`
	OriginalAsarSHA256 string    `json:"original_asar_sha256,omitempty"`
	PatchedAsarSHA256  string    `json:"patched_asar_sha256,omitempty"`
	BackupDir          string    `json:"backup_dir,omitempty"`
	Status             string    `json:"status"`
	LastCheckedAt      time.Time `json:"last_checked_at"`
	PatchedAt          time.Time `json:"patched_at,omitempty"`
	RestartRequired    bool      `json:"restart_required"`
	RestartRequestedAt time.Time `json:"restart_requested_at,omitempty"`
	RestartAttemptID   string    `json:"restart_attempt_id,omitempty"`
	RestartAttemptedAt time.Time `json:"restart_attempted_at,omitempty"`
	RestartCompletedAt time.Time `json:"restart_completed_at,omitempty"`
	ResumeSessionID    string    `json:"resume_session_id,omitempty"`
	RestartFailure     string    `json:"restart_failure,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
}

func Root(codexHome string) string {
	return filepath.Join(codexHome, "patches", "speed-mode")
}

func ManifestPath(codexHome string) string {
	return filepath.Join(Root(codexHome), "manifest.json")
}

func LockPath(codexHome string) string {
	return filepath.Join(Root(codexHome), "patch.lock")
}

func RestartLockPath(codexHome string) string {
	return filepath.Join(Root(codexHome), "restart.lock")
}

func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &manifest, nil
}

func Save(path string, manifest *Manifest) error {
	if manifest.SchemaVersion == 0 {
		manifest.SchemaVersion = SchemaVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create manifest temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod manifest temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write manifest temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync manifest temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close manifest temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	return nil
}

func AcquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return func() { _ = os.Remove(path) }, nil
}
