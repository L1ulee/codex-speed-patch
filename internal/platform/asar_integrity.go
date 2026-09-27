package platform

import (
	"crypto/sha256"
	"debug/macho"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

const integritySlotSentinel = "AGbevlPCksUGKNL8TSn7wGmJEuJsXb2A"

type asarIntegrityEntry struct {
	Algorithm string `json:"algorithm"`
	Hash      string `json:"hash"`
}

func readASARIntegrity(infoPlist string) (map[string]asarIntegrityEntry, error) {
	output, err := exec.Command("/usr/bin/plutil", "-extract", "ElectronAsarIntegrity", "json", "-o", "-", infoPlist).Output()
	if err != nil {
		return nil, fmt.Errorf("read ElectronAsarIntegrity: %w", err)
	}
	var entries map[string]asarIntegrityEntry
	if err := json.Unmarshal(output, &entries); err != nil {
		return nil, fmt.Errorf("decode ElectronAsarIntegrity: %w", err)
	}
	return entries, nil
}

func integrityDictionaryDigest(entries map[string]asarIntegrityEntry) ([32]byte, error) {
	var zero [32]byte
	if len(entries) == 0 {
		return zero, fmt.Errorf("empty ElectronAsarIntegrity")
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		entry := entries[key]
		if key == "" || entry.Algorithm != "SHA256" || len(entry.Hash) != 64 {
			return zero, fmt.Errorf("unsupported ElectronAsarIntegrity entry %q", key)
		}
		_, _ = io.WriteString(h, key+entry.Algorithm+entry.Hash)
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func decodeIntegritySlot(slot []byte) ([32]byte, error) {
	var digest [32]byte
	if len(slot) != 66 || string(slot[:32]) != integritySlotSentinel || slot[32] != 1 || slot[33] != 1 {
		return digest, fmt.Errorf("unsupported __asar_integrity slot")
	}
	copy(digest[:], slot[34:])
	return digest, nil
}

func frameworkBinary(appPath string) string {
	return filepath.Join(appPath, "Contents", "Frameworks", "Codex Framework.framework", "Codex Framework")
}

func integritySlotOffset(binaryPath string) (int64, error) {
	f, err := macho.Open(binaryPath)
	if err != nil {
		return 0, fmt.Errorf("open Framework Mach-O: %w", err)
	}
	defer f.Close()
	for _, section := range f.Sections {
		if section.Seg == "__DATA_CONST" && section.Name == "__asar_integrity" {
			if section.Size != 66 {
				return 0, fmt.Errorf("unexpected __asar_integrity size: %d", section.Size)
			}
			return int64(section.Offset), nil
		}
	}
	return 0, fmt.Errorf("Framework has no __asar_integrity section")
}

func updateFrameworkASARIntegrity(inst *Installation, oldEntries, newEntries map[string]asarIntegrityEntry) error {
	oldDigest, err := integrityDictionaryDigest(oldEntries)
	if err != nil {
		return err
	}
	newDigest, err := integrityDictionaryDigest(newEntries)
	if err != nil {
		return err
	}
	path := frameworkBinary(inst.AppPath)
	offset, err := integritySlotOffset(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open Framework for integrity update: %w", err)
	}
	defer f.Close()
	slot := make([]byte, 66)
	if _, err := f.ReadAt(slot, offset); err != nil {
		return err
	}
	current, err := decodeIntegritySlot(slot)
	if err != nil {
		return err
	}
	if current != oldDigest {
		return fmt.Errorf("Framework integrity digest does not match Info.plist")
	}
	copy(slot[34:], newDigest[:])
	if _, err := f.WriteAt(slot, offset); err != nil {
		return fmt.Errorf("write Framework integrity digest: %w", err)
	}
	return f.Sync()
}
