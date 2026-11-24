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
	StatusPartial  ProbeStatus = "partial"
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
	Status          ProbeStatus
	Candidate       *Candidate
	Candidates      []Candidate
	TargetPath      string
	TargetPaths     []string
	UltrafastModels []string
	CatalogLoader   bool
	SignedWorker    bool
}

type PatchResult struct {
	TargetPath         string
	TargetPaths        []string
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
	var report ruleReport
	var hostReaders, bootstrapReaders, webviewReaders int
	catalogLoader := false
	signedWorker := false
	var paths []string
	uiPath := ""
	var models []string
	type tierAsset struct {
		entry Entry
		data  []byte
	}
	var tierAssets []tierAsset
	var accountAssets []tierAsset
	references := make(map[rspackReference]bool)
	policyModules := make(map[string]bool)
	effortModules := make(map[string]bool)
	for _, entry := range a.Entries() {
		if !isJavaScript(entry) {
			continue
		}
		data, err := a.ReadEntry(entry)
		if err != nil {
			return nil, err
		}
		models = append(models, nativeUltrafastModels(data)...)
		if bytes.Contains(data, []byte(".getAccount(")) && bytes.Contains(data, []byte("__webpack_modules__=")) {
			accountAssets = append(accountAssets, tierAsset{entry, data})
		}
		if !strings.HasPrefix(entry.Path, ".vite/") && (bytes.Contains(data, []byte("?.serviceTiers")) || bytes.Contains(data, []byte(tierHelperName))) {
			tierAssets = append(tierAssets, tierAsset{entry, data})
		}
		r, err := scanRules(entry, data)
		if err != nil {
			return nil, err
		}
		catalog, loaded, err := scanCatalogRules(entry, data)
		if err != nil {
			return nil, err
		}
		catalogLoader = catalogLoader || loaded
		r.patched += catalog.patched
		r.edits = append(r.edits, catalog.edits...)
		worker, signed, err := scanWorkerRules(entry, data)
		if err != nil {
			return nil, err
		}
		signedWorker = signedWorker || signed
		r.patched += worker.patched
		r.edits = append(r.edits, worker.edits...)
		efforts, err := scanEffortRules(entry, data)
		if err != nil {
			return nil, err
		}
		if efforts.effortModule != "" {
			effortModules[efforts.effortModule] = true
		} else {
			report.effortQueries += efforts.effortQueries
		}
		if report.effortQueries+len(effortModules) > 1 {
			return nil, errors.New("multiple native effort queries across ASAR assets")
		}
		r.patched += efforts.patched
		r.edits = append(r.edits, efforts.edits...)
		if r.ui+r.readers+r.policies+efforts.effortQueries > 0 || len(catalog.edits)+len(worker.edits) > 0 {
			paths = append(paths, entry.Path)
		}
		if r.ui > 0 {
			uiPath = entry.Path
		}
		report.ui += r.ui
		report.readers += r.readers
		if entry.Path == ".vite/build/app-server-host.js" {
			hostReaders += r.readers
		} else if strings.HasPrefix(entry.Path, ".vite/build/early-bootstrap-") {
			bootstrapReaders += r.readers
		} else if strings.HasPrefix(entry.Path, "webview/assets/") {
			webviewReaders += r.readers
		}
		if r.policyModule != "" {
			policyModules[r.policyModule] = true
		} else {
			report.policies += r.policies
		}
		for _, reference := range r.accountReferences {
			references[reference] = false
		}
		report.patched += r.patched
		report.edits = append(report.edits, r.edits...)
	}
	report.policies += len(policyModules)
	report.effortQueries += len(effortModules)
	for _, asset := range accountAssets {
		r, err := scanRspackAccountReaders(asset.entry, asset.data, references)
		if err != nil {
			return nil, err
		}
		if len(r.edits) > 0 {
			found := false
			for _, path := range paths {
				found = found || path == asset.entry.Path
			}
			if !found {
				paths = append(paths, asset.entry.Path)
			}
		}
		report.patched += r.patched
		report.edits = append(report.edits, r.edits...)
	}
	for reference, found := range references {
		if !found {
			return nil, fmt.Errorf("Speed identity module %s.%s is missing", reference.module, reference.export)
		}
	}
	for _, asset := range tierAssets {
		r, err := scanTierRules(asset.entry, asset.data, sortedModels(models))
		if err != nil {
			return nil, err
		}
		if r.tierMenus+r.tierLookups > 0 {
			found := false
			for _, path := range paths {
				found = found || path == asset.entry.Path
			}
			if !found {
				paths = append(paths, asset.entry.Path)
			}
		}
		report.tierMenus += r.tierMenus
		report.tierLookups += r.tierLookups
		report.tierMenusRspack = report.tierMenusRspack || r.tierMenusRspack
		report.patched += r.patched
		report.edits = append(report.edits, r.edits...)
	}
	result := &ProbeResult{Status: StatusUnknown, TargetPath: uiPath, TargetPaths: paths, Candidates: report.edits, UltrafastModels: sortedModels(models), CatalogLoader: catalogLoader, SignedWorker: signedWorker}
	tiersComplete := report.tierMenus == 0 && report.tierLookups == 0 || report.tierMenus == 1 && report.tierLookups == 1 || report.tierMenusRspack && report.tierLookups == 0
	// Newer packages include a saved reader in the separate app-server host as
	// well as the bootstrap and webview. Extra unrelated readers stay ambiguous.
	readersComplete := report.readers >= 1 && report.readers <= 2 ||
		report.readers == 3 && hostReaders == 1 && bootstrapReaders == 1 && webviewReaders == 1
	complete := report.ui >= 1 && report.policies == 1 && readersComplete && tiersComplete
	if !complete {
		if report.ui >= 1 && report.patched > 0 {
			result.Status = StatusPartial
		}
		return result, nil
	}
	if len(report.edits) == 0 {
		result.Status = StatusPatched
	} else if report.patched == 0 {
		result.Status = StatusOriginal
	} else {
		result.Status = StatusPartial
	}
	if len(report.edits) > 0 {
		result.Candidate = &result.Candidates[0]
	}
	return result, nil
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
	if (probe.Status != StatusOriginal && probe.Status != StatusPartial) || len(probe.Candidates) == 0 {
		return nil, fmt.Errorf("ASAR is not in a uniquely patchable state: %s", probe.Status)
	}
	return rewritePatchedArchive(archive, probe, destinationPath)
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

func sum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
