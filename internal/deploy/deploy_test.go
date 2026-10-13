package deploy

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-speed-patch/internal/asar"
	"codex-speed-patch/internal/state"
)

type fixture struct {
	service  Service
	record   *Record
	prior    []byte
	quits    int
	launches int
}

func makeFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	id := "0123456789abcdef01234567"
	dir := filepath.Join(state.Root(home), "deployments", id)
	sibling := filepath.Join(root, ".codex-speed-deploy-"+id)
	app := filepath.Join(root, "Current.app")
	stage := filepath.Join(sibling, "ChatGPT.app")
	backup := filepath.Join(root, "backup")
	writeBundle(t, app, "original")
	writeBundle(t, stage, "patched")
	writeBundle(t, filepath.Join(backup, "ChatGPT.app"), "original")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	priorManifest := &state.Manifest{AppPath: app, AppVersion: "1", Status: "restored", BackupDir: "prior-backup"}
	if err := state.Save(state.ManifestPath(home), priorManifest); err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(state.ManifestPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(dir, "previous-manifest.json"), prior); err != nil {
		t.Fatal(err)
	}
	sourceHash, _ := asar.FileSHA256(filepath.Join(app, "Contents/Resources/app.asar"))
	infoHash, _ := asar.FileSHA256(filepath.Join(app, "Contents/Info.plist"))
	patchedHash, _ := asar.FileSHA256(filepath.Join(stage, "Contents/Resources/app.asar"))
	f := &fixture{prior: prior}
	f.record = &Record{ID: id, Status: "scheduled", AppPath: app, AppVersion: "1", SourceHash: sourceHash,
		SourceInfoHash: infoHash, PatchedHash: patchedHash, Directory: dir, StagePath: stage,
		RollbackPath: filepath.Join(sibling, "original.app"), TestMode: true, HadManifest: true,
		Manifest: &state.Manifest{AppPath: app, AppVersion: "1", Status: "patched", OriginalAsarSHA256: sourceHash, PatchedAsarSHA256: patchedHash, BackupDir: backup}}
	f.service = Service{CodexHome: home, operations: &operations{
		verify: func(path, version, hash string, patched bool) error {
			actual, err := asar.FileSHA256(filepath.Join(path, "Contents/Resources/app.asar"))
			if err != nil {
				return err
			}
			if actual != hash {
				return errors.New("fixture hash mismatch")
			}
			return nil
		},
		quit:   func(path string) error { f.quits++; return nil },
		launch: func(path, session string) error { f.launches++; return nil },
		rename: os.Rename, manifest: state.Save,
	}}
	if err := save(home, f.record); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeBundle(t *testing.T, path, payload string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "Contents/Resources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Contents/Resources/app.asar"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Contents/Info.plist"), []byte(payload+" metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) assertOriginal(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.record.AppPath, "Contents/Resources/app.asar"))
	if err != nil || string(data) != "original" {
		t.Fatalf("original not restored: %q, %v", data, err)
	}
	manifest, err := os.ReadFile(state.ManifestPath(f.service.CodexHome))
	if err != nil || string(manifest) != string(f.prior) {
		t.Fatalf("previous manifest not restored: %q, %v", manifest, err)
	}
}

func TestReplacementCommitsBundleAndManifestAndCannotRepeatWorker(t *testing.T) {
	f := makeFixture(t)
	if err := f.service.RunWorker(f.record.ID); err != nil {
		t.Fatal(err)
	}
	r, err := Load(f.service.CodexHome)
	if err != nil || r.Status != "deployed" {
		t.Fatalf("result: %#v, %v", r, err)
	}
	data, _ := os.ReadFile(filepath.Join(r.AppPath, "Contents/Resources/app.asar"))
	if string(data) != "patched" {
		t.Fatal("replacement was not activated")
	}
	m, err := state.Load(state.ManifestPath(f.service.CodexHome))
	if err != nil || m.Status != "patched" || m.BackupDir != r.Manifest.BackupDir || m.RestartCompletedAt.IsZero() {
		t.Fatalf("manifest: %#v, %v", m, err)
	}
	if err := f.service.RunWorker(f.record.ID); err == nil {
		t.Fatal("completed worker ran twice")
	}
	if f.quits != 1 || f.launches != 1 {
		t.Fatal("duplicate worker changed the lifecycle")
	}
	if _, err := os.Stat(filepath.Join(r.Manifest.BackupDir, "ChatGPT.app")); err != nil {
		t.Fatal("durable backup was removed")
	}
}

func TestReplacementFailuresRestoreExactBundleAndPriorManifest(t *testing.T) {
	for _, failure := range []string{"activation", "signature", "manifest", "launch"} {
		t.Run(failure, func(t *testing.T) {
			f := makeFixture(t)
			switch failure {
			case "activation":
				f.service.operations.rename = func(source, destination string) error {
					if source == f.record.StagePath {
						return errors.New("injected activation failure")
					}
					return os.Rename(source, destination)
				}
			case "signature":
				original := f.service.operations.verify
				f.service.operations.verify = func(path, version, hash string, patched bool) error {
					if path == f.record.AppPath && patched {
						return errors.New("injected signing failure")
					}
					return original(path, version, hash, patched)
				}
			case "manifest":
				f.service.operations.manifest = func(path string, m *state.Manifest) error { return errors.New("injected manifest failure") }
			case "launch":
				f.service.operations.launch = func(path, session string) error {
					f.launches++
					if f.launches == 1 {
						return errors.New("injected startup crash")
					}
					return nil
				}
			}
			if err := f.service.RunWorker(f.record.ID); err == nil {
				t.Fatal("failure was ignored")
			}
			f.assertOriginal(t)
			r, _ := Load(f.service.CodexHome)
			if r.Status != "failed" || !r.RolledBack || r.Error == "" {
				t.Fatalf("rollback result: %#v", r)
			}
		})
	}
}

func TestReplacementRejectsChangedInputsBeforeShutdown(t *testing.T) {
	for _, changed := range []string{"source", "stage", "backup", "metadata", "manifest", "attempt"} {
		t.Run(changed, func(t *testing.T) {
			f := makeFixture(t)
			id := f.record.ID
			path := ""
			switch changed {
			case "source":
				path = filepath.Join(f.record.AppPath, "Contents/Resources/app.asar")
			case "stage":
				path = filepath.Join(f.record.StagePath, "Contents/Resources/app.asar")
			case "backup":
				path = filepath.Join(f.record.Manifest.BackupDir, "ChatGPT.app/Contents/Resources/app.asar")
			case "metadata":
				path = filepath.Join(f.record.AppPath, "Contents/Info.plist")
			case "manifest":
				path = state.ManifestPath(f.service.CodexHome)
			case "attempt":
				id = "stale"
			}
			if path != "" {
				if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.service.RunWorker(id); err == nil {
				t.Fatal("changed input was accepted")
			}
			if f.quits != 0 {
				t.Fatal("changed inputs closed the application")
			}
			if _, err := os.Stat(f.record.RollbackPath); !os.IsNotExist(err) {
				t.Fatal("changed inputs moved the application")
			}
		})
	}
}

func TestUpdateDuringShutdownIsPreservedAndReopened(t *testing.T) {
	f := makeFixture(t)
	f.service.operations.quit = func(path string) error {
		writeBundle(t, path, "official-update")
		return nil
	}
	if err := f.service.RunWorker(f.record.ID); err == nil {
		t.Fatal("updated application was overwritten")
	}
	data, _ := os.ReadFile(filepath.Join(f.record.AppPath, "Contents/Resources/app.asar"))
	if string(data) != "official-update" || f.launches != 1 {
		t.Fatal("updated application was not retained and reopened")
	}
}

func TestRollbackRetainsOriginalIfReplacementCannotStop(t *testing.T) {
	f := makeFixture(t)
	f.service.operations.launch = func(path, session string) error { return errors.New("startup verification failed") }
	f.service.operations.quit = func(path string) error {
		f.quits++
		if f.quits > 1 {
			return errors.New("helpers still running")
		}
		return nil
	}
	if err := f.service.RunWorker(f.record.ID); err == nil || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(f.record.RollbackPath, "Contents/Resources/app.asar"))
	if err != nil || string(data) != "original" {
		t.Fatal("original rollback package was lost")
	}
}

func TestReplacementWithoutPriorManifestDoesNotLeaveFalsePatchedState(t *testing.T) {
	f := makeFixture(t)
	f.record.HadManifest = false
	if err := os.Remove(state.ManifestPath(f.service.CodexHome)); err != nil {
		t.Fatal(err)
	}
	if err := save(f.service.CodexHome, f.record); err != nil {
		t.Fatal(err)
	}
	f.service.operations.launch = func(path, session string) error {
		f.launches++
		if f.launches == 1 {
			return errors.New("launch failed")
		}
		return nil
	}
	if err := f.service.RunWorker(f.record.ID); err == nil {
		t.Fatal("failure was ignored")
	}
	if _, err := os.Stat(state.ManifestPath(f.service.CodexHome)); !os.IsNotExist(err) {
		t.Fatal("failed deployment retained a patched manifest")
	}
}

func TestReplacementRejectsRedirectedTransactionPathsBeforeShutdown(t *testing.T) {
	f := makeFixture(t)
	f.record.StagePath = filepath.Join(filepath.Dir(f.record.AppPath), "Unrelated.app")
	if err := save(f.service.CodexHome, f.record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RunWorker(f.record.ID); err == nil {
		t.Fatal("redirected transaction was accepted")
	}
	f.assertOriginal(t)
	if f.quits != 0 {
		t.Fatal("invalid transaction closed the application")
	}
}

func TestReplacementCancelsIfSelectedProcessesCannotStop(t *testing.T) {
	f := makeFixture(t)
	f.service.operations.quit = func(string) error { return errors.New("helpers still running") }
	if err := f.service.RunWorker(f.record.ID); err == nil {
		t.Fatal("replacement proceeded while processes were running")
	}
	f.assertOriginal(t)
	if f.launches != 0 {
		t.Fatal("failed shutdown started another instance")
	}
}

func TestMetadataUpdateDuringShutdownIsPreserved(t *testing.T) {
	f := makeFixture(t)
	f.service.operations.quit = func(path string) error {
		return os.WriteFile(filepath.Join(path, "Contents/Info.plist"), []byte("updated metadata"), 0o600)
	}
	if err := f.service.RunWorker(f.record.ID); err == nil {
		t.Fatal("metadata update was overwritten")
	}
	data, _ := os.ReadFile(filepath.Join(f.record.AppPath, "Contents/Info.plist"))
	if string(data) != "updated metadata" || f.launches != 1 {
		t.Fatal("changed metadata was not retained and the app reopened")
	}
}

func TestLaunchJobUsesLiteralPathsAndDoesNotKeepAlive(t *testing.T) {
	f := makeFixture(t)
	plist := launchPlist(f.record, "/private/tmp/A&B <copy>/worker", "/private/tmp/A&B <copy>/home")
	decoder := xml.NewDecoder(strings.NewReader(plist))
	for {
		_, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("job plist is not valid XML: %v", err)
		}
	}
	if !strings.Contains(plist, "A&amp;B &lt;copy&gt;") || !strings.Contains(plist, "<key>KeepAlive</key><false/>") {
		t.Fatal("job paths were altered or job was configured to repeat")
	}
}
