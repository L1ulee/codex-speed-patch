package asar

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	jsonOffset = 16
	blockSize  = 4 * 1024 * 1024
)

type Integrity struct {
	Algorithm string   `json:"algorithm"`
	Hash      string   `json:"hash"`
	BlockSize int      `json:"blockSize"`
	Blocks    []string `json:"blocks"`
}

type Node struct {
	Files     map[string]*Node `json:"files,omitempty"`
	Size      int64            `json:"size,omitempty"`
	Offset    string           `json:"offset,omitempty"`
	Unpacked  bool             `json:"unpacked,omitempty"`
	Integrity *Integrity       `json:"integrity,omitempty"`
}

type Header struct {
	Files map[string]*Node `json:"files"`
}

type Archive struct {
	Path         string
	HeaderSize   int64
	HeaderJSON   []byte
	Header       Header
	ContentStart int64
}

type Entry struct {
	Path string
	Node *Node
}

type ProbeStatus string

const (
	StatusOriginal ProbeStatus = "original"
	StatusPatched  ProbeStatus = "patched"
	StatusUnknown  ProbeStatus = "unknown"
)

type Candidate struct {
	Entry       Entry
	MatchStart  int
	MatchEnd    int
	Original    []byte
	Replacement []byte
}

type ProbeResult struct {
	Status     ProbeStatus
	Candidate  *Candidate
	TargetPath string
}

type PatchResult struct {
	TargetPath         string
	OriginalFileHash   string
	PatchedFileHash    string
	PatchedArchiveHash string
	PatchedHeaderHash  string
}

var gatePattern = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)&&!([A-Za-z_$][A-Za-z0-9_$]*)&&([A-Za-z_$][A-Za-z0-9_$]*)!=null&&([A-Za-z_$][A-Za-z0-9_$]*)\?\.requirements\?\.featureRequirements\?\.fast_mode!==!1`)
var patchedGatePattern = regexp.MustCompile(`!([A-Za-z_$][A-Za-z0-9_$]*)&&\(!([A-Za-z_$][A-Za-z0-9_$]*)\|\|!!([A-Za-z_$][A-Za-z0-9_$]*)&&([A-Za-z_$][A-Za-z0-9_$]*)\?\.requirements\?\.featureRequirements\?\.fast_mode!==!1\)`)
var previousPatchedGatePattern = regexp.MustCompile(`!([A-Za-z_$][A-Za-z0-9_$]*)&&\(!([A-Za-z_$][A-Za-z0-9_$]*)\|\|([A-Za-z_$][A-Za-z0-9_$]*)\?\.requirements\?\.featureRequirements\?\.fast_mode!==!1\)`)

func Open(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ASAR: %w", err)
	}
	defer f.Close()

	prefix := make([]byte, jsonOffset)
	if _, err := io.ReadFull(f, prefix); err != nil {
		return nil, fmt.Errorf("read ASAR prefix: %w", err)
	}
	if binary.LittleEndian.Uint32(prefix[0:4]) != 4 {
		return nil, errors.New("unsupported ASAR size pickle")
	}
	headerSize := int64(binary.LittleEndian.Uint32(prefix[4:8]))
	stringLength := int64(binary.LittleEndian.Uint32(prefix[12:16]))
	if headerSize < 8 || stringLength <= 0 || stringLength > headerSize-8 {
		return nil, errors.New("invalid ASAR header lengths")
	}
	headerJSON := make([]byte, stringLength)
	if _, err := f.ReadAt(headerJSON, jsonOffset); err != nil {
		return nil, fmt.Errorf("read ASAR JSON header: %w", err)
	}
	var header Header
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("decode ASAR JSON header: %w", err)
	}
	return &Archive{
		Path:         path,
		HeaderSize:   headerSize,
		HeaderJSON:   headerJSON,
		Header:       header,
		ContentStart: 8 + headerSize,
	}, nil
}

func (a *Archive) Entries() []Entry {
	var entries []Entry
	var walk func(prefix string, files map[string]*Node)
	walk = func(prefix string, files map[string]*Node) {
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			node := files[name]
			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}
			if node.Files != nil {
				walk(path, node.Files)
				continue
			}
			entries = append(entries, Entry{Path: path, Node: node})
		}
	}
	walk("", a.Header.Files)
	return entries
}

func (a *Archive) ReadEntry(entry Entry) ([]byte, error) {
	if entry.Node.Unpacked {
		return os.ReadFile(filepath.Join(a.Path+".unpacked", filepath.FromSlash(entry.Path)))
	}
	offset, err := strconv.ParseInt(entry.Node.Offset, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse offset for %s: %w", entry.Path, err)
	}
	if entry.Node.Size < 0 || entry.Node.Size > 512*1024*1024 {
		return nil, fmt.Errorf("invalid size for %s", entry.Path)
	}
	data := make([]byte, entry.Node.Size)
	f, err := os.Open(a.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.ReadAt(data, a.ContentStart+offset); err != nil {
		return nil, fmt.Errorf("read %s: %w", entry.Path, err)
	}
	return data, nil
}

func (a *Archive) Probe() (*ProbeResult, error) {
	var originals []Candidate
	patchedCount := 0
	patchedPath := ""
	for _, entry := range a.Entries() {
		if entry.Node.Unpacked || !strings.HasSuffix(strings.ToLower(entry.Path), ".js") || entry.Node.Size == 0 {
			continue
		}
		data, err := a.ReadEntry(entry)
		if err != nil {
			return nil, err
		}
		for _, match := range gatePattern.FindAllSubmatchIndex(data, -1) {
			if len(match) != 10 {
				continue
			}
			dataVar1 := data[match[6]:match[7]]
			dataVar2 := data[match[8]:match[9]]
			if !bytes.Equal(dataVar1, dataVar2) {
				continue
			}
			authVar := string(data[match[2]:match[3]])
			loadingVar := string(data[match[4]:match[5]])
			dataVar := string(dataVar1)
			original := append([]byte(nil), data[match[0]:match[1]]...)
			replacement := []byte(fmt.Sprintf("!%s&&(!%s||!!%s&&%s?.requirements?.featureRequirements?.fast_mode!==!1)", loadingVar, authVar, dataVar, dataVar))
			if len(replacement) > len(original) {
				return nil, fmt.Errorf("generated replacement is larger than original expression in %s", entry.Path)
			}
			replacement = append(replacement, bytes.Repeat([]byte{' '}, len(original)-len(replacement))...)
			originals = append(originals, Candidate{
				Entry:       entry,
				MatchStart:  match[0],
				MatchEnd:    match[1],
				Original:    original,
				Replacement: replacement,
			})
		}
		count := len(patchedGatePattern.FindAllIndex(data, -1)) + len(previousPatchedGatePattern.FindAllIndex(data, -1))
		if count > 0 {
			patchedCount += count
			patchedPath = entry.Path
		}
	}
	if len(originals) == 1 && patchedCount == 0 {
		return &ProbeResult{Status: StatusOriginal, Candidate: &originals[0], TargetPath: originals[0].Entry.Path}, nil
	}
	if len(originals) == 0 && patchedCount == 1 {
		return &ProbeResult{Status: StatusPatched, TargetPath: patchedPath}, nil
	}
	return &ProbeResult{Status: StatusUnknown}, nil
}

func PatchCopy(sourcePath, destinationPath string) (*PatchResult, error) {
	archive, err := Open(sourcePath)
	if err != nil {
		return nil, err
	}
	probe, err := archive.Probe()
	if err != nil {
		return nil, err
	}
	if probe.Status != StatusOriginal || probe.Candidate == nil {
		return nil, fmt.Errorf("ASAR is not in a uniquely patchable state: %s", probe.Status)
	}
	candidate := probe.Candidate
	if candidate.Entry.Node.Integrity == nil || candidate.Entry.Node.Integrity.Algorithm != "SHA256" {
		return nil, errors.New("target entry does not contain supported SHA256 integrity metadata")
	}
	originalData, err := archive.ReadEntry(candidate.Entry)
	if err != nil {
		return nil, err
	}
	if count := bytes.Count(originalData, candidate.Original); count != 1 {
		return nil, fmt.Errorf("expected one target expression, found %d", count)
	}
	if err := verifyIntegrity(originalData, candidate.Entry.Node.Integrity); err != nil {
		return nil, fmt.Errorf("verify original target integrity: %w", err)
	}
	patchedData := append([]byte(nil), originalData...)
	copy(patchedData[candidate.MatchStart:candidate.MatchEnd], candidate.Replacement)

	newIntegrity := calculateIntegrity(patchedData, candidate.Entry.Node.Integrity.BlockSize)
	headerJSON := append([]byte(nil), archive.HeaderJSON...)
	type replacement struct {
		newValue string
		count    int
	}
	replacements := map[string]replacement{}
	addReplacement := func(oldValue, newValue string) error {
		if oldValue == newValue {
			return nil
		}
		current, ok := replacements[oldValue]
		if ok && current.newValue != newValue {
			return fmt.Errorf("one original integrity hash maps to multiple patched hashes")
		}
		current.newValue = newValue
		current.count++
		replacements[oldValue] = current
		return nil
	}
	if err := addReplacement(candidate.Entry.Node.Integrity.Hash, newIntegrity.Hash); err != nil {
		return nil, err
	}
	for index, oldHash := range candidate.Entry.Node.Integrity.Blocks {
		newHash := newIntegrity.Blocks[index]
		if err := addReplacement(oldHash, newHash); err != nil {
			return nil, fmt.Errorf("prepare target block %d hash: %w", index, err)
		}
	}
	for oldValue, replacement := range replacements {
		headerJSON, err = replaceExactCount(headerJSON, oldValue, replacement.newValue, replacement.count)
		if err != nil {
			return nil, fmt.Errorf("update integrity hash: %w", err)
		}
	}
	if len(headerJSON) != len(archive.HeaderJSON) {
		return nil, errors.New("ASAR header length changed")
	}

	if err := copyFile(sourcePath, destinationPath); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(destinationPath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open destination ASAR: %w", err)
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := f.WriteAt(headerJSON, jsonOffset); err != nil {
		return nil, fmt.Errorf("write ASAR header: %w", err)
	}
	entryOffset, _ := strconv.ParseInt(candidate.Entry.Node.Offset, 10, 64)
	if _, err := f.WriteAt(patchedData, archive.ContentStart+entryOffset); err != nil {
		return nil, fmt.Errorf("write patched target: %w", err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("sync patched ASAR: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close patched ASAR: %w", err)
	}
	patchedArchive, err := Open(destinationPath)
	if err != nil {
		return nil, err
	}
	verification, err := patchedArchive.Probe()
	if err != nil {
		return nil, err
	}
	if verification.Status != StatusPatched {
		return nil, fmt.Errorf("patched ASAR verification failed: %s", verification.Status)
	}
	archiveHash, err := FileSHA256(destinationPath)
	if err != nil {
		return nil, err
	}
	cleanup = false
	return &PatchResult{
		TargetPath:         candidate.Entry.Path,
		OriginalFileHash:   candidate.Entry.Node.Integrity.Hash,
		PatchedFileHash:    newIntegrity.Hash,
		PatchedArchiveHash: archiveHash,
		PatchedHeaderHash:  sum(headerJSON),
	}, nil
}

func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func calculateIntegrity(data []byte, requestedBlockSize int) *Integrity {
	size := requestedBlockSize
	if size <= 0 {
		size = blockSize
	}
	blocks := make([]string, 0, (len(data)+size-1)/size)
	for start := 0; start < len(data); start += size {
		end := start + size
		if end > len(data) {
			end = len(data)
		}
		blocks = append(blocks, sum(data[start:end]))
	}
	return &Integrity{Algorithm: "SHA256", Hash: sum(data), BlockSize: size, Blocks: blocks}
}

func verifyIntegrity(data []byte, integrity *Integrity) error {
	actual := calculateIntegrity(data, integrity.BlockSize)
	if actual.Hash != integrity.Hash {
		return fmt.Errorf("file hash mismatch: expected %s, got %s", integrity.Hash, actual.Hash)
	}
	if len(actual.Blocks) != len(integrity.Blocks) {
		return errors.New("block count mismatch")
	}
	for index := range actual.Blocks {
		if actual.Blocks[index] != integrity.Blocks[index] {
			return fmt.Errorf("block %d hash mismatch", index)
		}
	}
	return nil
}

func replaceExactCount(data []byte, oldValue, newValue string, expectedCount int) ([]byte, error) {
	if len(oldValue) != len(newValue) {
		return nil, errors.New("integrity replacement length mismatch")
	}
	oldBytes := []byte(oldValue)
	if count := bytes.Count(data, oldBytes); count != expectedCount {
		return nil, fmt.Errorf("expected %d occurrences of %s, found %d", expectedCount, oldValue, count)
	}
	return bytes.ReplaceAll(data, oldBytes, []byte(newValue)), nil
}

func sum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy file: %w", err)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}
