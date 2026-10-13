package asar

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

func headerNode(header map[string]any, path string) (map[string]any, error) {
	node := header
	for _, name := range strings.Split(path, "/") {
		files, ok := node["files"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("header directory missing for %s", path)
		}
		node, ok = files[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("header node missing for %s", path)
		}
	}
	return node, nil
}

func rewritePatchedArchive(archive *Archive, probe *ProbeResult, destination string) (_ *PatchResult, err error) {
	groups := map[string][]Candidate{}
	for _, edit := range probe.Candidates {
		groups[edit.Entry.Path] = append(groups[edit.Entry.Path], edit)
	}
	changed := map[string][]byte{}
	integrities := map[string]*Integrity{}
	for path, edits := range groups {
		entry := edits[0].Entry
		if entry.Node.Integrity == nil || entry.Node.Integrity.Algorithm != "SHA256" {
			return nil, fmt.Errorf("unsupported integrity for %s", path)
		}
		data, e := archive.ReadEntry(entry)
		if e != nil {
			return nil, e
		}
		if e = verifyIntegrity(data, entry.Node.Integrity); e != nil {
			return nil, fmt.Errorf("verify %s: %w", path, e)
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].MatchStart < edits[j].MatchStart })
		last := 0
		for _, edit := range edits {
			if edit.MatchStart < last || edit.MatchEnd > len(data) || !bytes.Equal(data[edit.MatchStart:edit.MatchEnd], edit.Original) {
				return nil, fmt.Errorf("overlapping or stale edit in %s", path)
			}
			last = edit.MatchEnd
		}
		for i := len(edits) - 1; i >= 0; i-- {
			edit := edits[i]
			next := make([]byte, 0, len(data)-len(edit.Original)+len(edit.Replacement))
			next = append(next, data[:edit.MatchStart]...)
			next = append(next, edit.Replacement...)
			next = append(next, data[edit.MatchEnd:]...)
			data = next
		}
		changed[path] = data
		integrities[path] = calculateIntegrity(data, entry.Node.Integrity.BlockSize)
	}

	// Decode the original index without discarding fields such as links,
	// executable flags, unpacked entries, or future Electron metadata.
	var header map[string]any
	decoder := json.NewDecoder(bytes.NewReader(archive.HeaderJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&header); err != nil {
		return nil, err
	}
	input, e := os.Open(archive.Path)
	if e != nil {
		return nil, e
	}
	defer input.Close()
	info, e := input.Stat()
	if e != nil {
		return nil, e
	}
	originalPayloadSize := info.Size() - archive.ContentStart
	if originalPayloadSize < 0 {
		return nil, fmt.Errorf("truncated ASAR content")
	}
	// Keep the original payload byte-for-byte, including deduplicated ranges
	// and padding. Append rewritten assets and redirect only their index nodes.
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	cursor := originalPayloadSize
	for _, path := range paths {
		node, e := headerNode(header, path)
		if e != nil {
			return nil, e
		}
		node["offset"] = strconv.FormatInt(cursor, 10)
		data := changed[path]
		node["size"] = len(data)
		integrity, ok := node["integrity"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("integrity metadata missing for %s", path)
		}
		v := integrities[path]
		integrity["algorithm"] = v.Algorithm
		integrity["hash"] = v.Hash
		integrity["blockSize"] = v.BlockSize
		integrity["blocks"] = v.Blocks
		cursor += int64(len(data))
	}
	headerJSON, e := json.Marshal(header)
	if e != nil {
		return nil, e
	}
	headerSize := (8 + len(headerJSON) + 3) &^ 3
	if uint64(headerSize) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("ASAR header exceeds pickle size limit")
	}
	prefix := make([]byte, 8+headerSize)
	binary.LittleEndian.PutUint32(prefix[0:4], 4)
	binary.LittleEndian.PutUint32(prefix[4:8], uint32(headerSize))
	binary.LittleEndian.PutUint32(prefix[8:12], uint32(headerSize-4))
	binary.LittleEndian.PutUint32(prefix[12:16], uint32(len(headerJSON)))
	copy(prefix[16:], headerJSON)
	output, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if e != nil {
		return nil, e
	}
	defer func() {
		output.Close()
		if err != nil {
			os.Remove(destination)
		}
	}()
	if _, e = output.Write(prefix); e != nil {
		return nil, e
	}
	if _, e = io.CopyN(output, io.NewSectionReader(input, archive.ContentStart, originalPayloadSize), originalPayloadSize); e != nil {
		return nil, e
	}
	for _, path := range paths {
		if _, e = output.Write(changed[path]); e != nil {
			return nil, e
		}
	}
	if e = output.Sync(); e != nil {
		return nil, e
	}
	if e = output.Close(); e != nil {
		return nil, e
	}
	verified, e := Open(destination)
	if e != nil {
		return nil, e
	}
	v, e := verified.Probe()
	if e != nil {
		return nil, e
	}
	if v.Status != StatusPatched {
		return nil, fmt.Errorf("complete patch verification returned %s", v.Status)
	}
	for _, entry := range verified.Entries() {
		if _, ok := changed[entry.Path]; ok {
			data, e := verified.ReadEntry(entry)
			if e != nil {
				return nil, e
			}
			if e = verifyIntegrity(data, entry.Node.Integrity); e != nil {
				return nil, e
			}
		}
	}
	archiveHash, e := FileSHA256(destination)
	if e != nil {
		return nil, e
	}
	first := probe.Candidates[0].Entry
	return &PatchResult{TargetPath: probe.TargetPath, TargetPaths: probe.TargetPaths, OriginalFileHash: first.Node.Integrity.Hash, PatchedFileHash: integrities[first.Path].Hash, PatchedArchiveHash: archiveHash, PatchedHeaderHash: sum(headerJSON)}, nil
}
