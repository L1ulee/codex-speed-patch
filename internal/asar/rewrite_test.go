package asar

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMultipleAssetsRetainPayloadAndUnknownMetadata(t *testing.T) {
	source := filepath.Join(t.TempDir(), "app.asar")
	gate := `let d=a&&!u&&c!=null&&c?.requirements?.featureRequirements?.fast_mode!==!1;`
	saved := requestFixture[strings.Index(requestFixture, "async function saved("):strings.Index(requestFixture, "async function request(")]
	payload := bytes.Repeat([]byte("unchanged payload"), 150)
	writeMultiFixture(t, source, map[string][]byte{".vite/build/main.js": []byte(saved), "webview/assets/app.js": []byte(gate + requestFixture), "zz-payload.bin": payload})
	dest := filepath.Join(t.TempDir(), "patched.asar")
	result, err := PatchCopy(source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TargetPaths) != 2 {
		t.Fatalf("target paths = %v", result.TargetPaths)
	}
	a, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range a.Entries() {
		if entry.Path == "zz-payload.bin" {
			data, err := a.ReadEntry(entry)
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("unmodified payload changed: %v", err)
			}
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(a.HeaderJSON, &raw); err != nil {
		t.Fatal(err)
	}
	files := raw["files"].(map[string]any)
	if files["alias.js"].(map[string]any)["link"] != "webview/assets/app.js" || files["zz-payload.bin"].(map[string]any)["executable"] != true || raw["futureMetadata"] != "retained" {
		t.Fatal("unknown metadata was discarded")
	}
	if !files["unpacked.dat"].(map[string]any)["unpacked"].(bool) {
		t.Fatal("unpacked metadata discarded")
	}
	if files["shared-payload.bin"].(map[string]any)["offset"] != files["zz-payload.bin"].(map[string]any)["offset"] {
		t.Fatal("deduplicated data range was not retained")
	}
}

func writeMultiFixture(t *testing.T, path string, assets map[string][]byte) {
	t.Helper()
	keys := make([]string, 0, len(assets))
	for k := range assets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	root := map[string]any{"files": map[string]any{}, "futureMetadata": "retained"}
	var payload []byte
	for _, path := range keys {
		parts := strings.Split(path, "/")
		node := root
		for _, part := range parts[:len(parts)-1] {
			files := node["files"].(map[string]any)
			next, ok := files[part]
			if !ok {
				next = map[string]any{"files": map[string]any{}}
				files[part] = next
			}
			node = next.(map[string]any)
		}
		data := assets[path]
		node["files"].(map[string]any)[parts[len(parts)-1]] = map[string]any{"size": len(data), "offset": strconv.Itoa(len(payload)), "integrity": calculateIntegrity(data, blockSize), "executable": true}
		payload = append(payload, data...)
	}
	root["files"].(map[string]any)["alias.js"] = map[string]any{"link": "webview/assets/app.js"}
	root["files"].(map[string]any)["unpacked.dat"] = map[string]any{"unpacked": true, "size": 4}
	root["files"].(map[string]any)["shared-payload.bin"] = root["files"].(map[string]any)["zz-payload.bin"]
	j, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	size := align4(8 + len(j))
	buffer := make([]byte, 8+size)
	binary.LittleEndian.PutUint32(buffer[0:4], 4)
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(size))
	binary.LittleEndian.PutUint32(buffer[8:12], uint32(size-4))
	binary.LittleEndian.PutUint32(buffer[12:16], uint32(len(j)))
	copy(buffer[16:], j)
	if err := os.WriteFile(path, append(buffer, payload...), 0o600); err != nil {
		t.Fatal(err)
	}
}
