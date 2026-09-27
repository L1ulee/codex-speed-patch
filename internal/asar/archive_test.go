package asar

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchCopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "app.asar")
	destination := filepath.Join(dir, "patched.asar")
	content := []byte(`let a=true,u=false,c={requirements:{featureRequirements:{fast_mode:true}}},d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;`)
	writeFixture(t, source, content)

	archive, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := archive.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if probe.Status != StatusOriginal {
		t.Fatalf("expected original, got %s", probe.Status)
	}
	result, err := PatchCopy(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if result.PatchedHeaderHash == "" {
		t.Fatal("patched header hash was not returned")
	}
	patched, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	patchedProbe, err := patched.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if patchedProbe.Status != StatusPatched {
		t.Fatalf("expected patched, got %s", patchedProbe.Status)
	}
	entries := patched.Entries()
	patchedData, err := patched.ReadEntry(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patchedData, []byte(`!u&&(!a||!!c&&c?.requirements?.featureRequirements?.fast_mode!==!1)`)) {
		t.Fatal("patch did not preserve the authenticated requirements check")
	}
}

func TestProbeRecognizesPreviousAndCurrentPatches(t *testing.T) {
	for name, expression := range map[string]string{
		"previous": `!u&&(!a||c?.requirements?.featureRequirements?.fast_mode!==!1)`,
		"current":  `!u&&(!a||!!c&&c?.requirements?.featureRequirements?.fast_mode!==!1)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.asar")
			writeFixture(t, path, []byte("let d="+expression+";"))
			archive, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := archive.Probe()
			if err != nil {
				t.Fatal(err)
			}
			if probe.Status != StatusPatched {
				t.Fatalf("expected patched, got %s", probe.Status)
			}
		})
	}
}

func writeFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	header := Header{Files: map[string]*Node{
		"webview": {Files: map[string]*Node{
			"assets": {Files: map[string]*Node{
				"app.js": {
					Size:   int64(len(content)),
					Offset: "0",
					Integrity: &Integrity{
						Algorithm: "SHA256",
						Hash:      hash,
						BlockSize: blockSize,
						Blocks:    []string{hash},
					},
				},
			}},
		}},
	}}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	headerBufferSize := align4(8 + len(headerJSON))
	archive := make([]byte, 8+headerBufferSize+len(content))
	binary.LittleEndian.PutUint32(archive[0:4], 4)
	binary.LittleEndian.PutUint32(archive[4:8], uint32(headerBufferSize))
	binary.LittleEndian.PutUint32(archive[8:12], uint32(headerBufferSize-4))
	binary.LittleEndian.PutUint32(archive[12:16], uint32(len(headerJSON)))
	copy(archive[16:], headerJSON)
	copy(archive[8+headerBufferSize:], content)
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
}

func align4(value int) int {
	return (value + 3) &^ 3
}
