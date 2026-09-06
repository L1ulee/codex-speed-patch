package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const marker = "codex-speed-patch"

type File struct {
	Hooks map[string][]Group `json:"hooks"`
}

type Group struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []Handler `json:"hooks"`
}

type Handler struct {
	Type           string `json:"type"`
	Command        string `json:"command"`
	CommandWindows string `json:"commandWindows,omitempty"`
	Timeout        int    `json:"timeout,omitempty"`
	Async          bool   `json:"async,omitempty"`
}

func Install(path, binaryPath string) (bool, error) {
	file, err := load(path)
	if err != nil {
		return false, err
	}
	removeManaged(file)
	unixBinary := shellQuote(binaryPath)
	windowsBinary := `"` + strings.ReplaceAll(binaryPath, `"`, `\"`) + `"`
	sessionStart := Group{Hooks: []Handler{{
		Type:           "command",
		Command:        unixBinary + " repair --hook --restart=deferred-auto --allow-macos-production",
		CommandWindows: windowsBinary + " repair --hook --restart=deferred-auto --allow-macos-production",
		Timeout:        180,
		Async:          true,
	}}}
	stop := Group{Hooks: []Handler{{
		Type:           "command",
		Command:        unixBinary + " restart-if-needed --hook",
		CommandWindows: windowsBinary + " restart-if-needed --hook",
		Timeout:        30,
		Async:          true,
	}}}
	file.Hooks["SessionStart"] = append(file.Hooks["SessionStart"], sessionStart)
	file.Hooks["Stop"] = append(file.Hooks["Stop"], stop)
	return true, save(path, file)
}

func Remove(path string) (bool, error) {
	file, err := load(path)
	if err != nil {
		return false, err
	}
	changed := removeManaged(file)
	if !changed {
		return false, nil
	}
	return true, save(path, file)
}

func removeManaged(file *File) bool {
	changed := false
	for event, groups := range file.Hooks {
		filteredGroups := make([]Group, 0, len(groups))
		for _, group := range groups {
			filteredHandlers := make([]Handler, 0, len(group.Hooks))
			for _, handler := range group.Hooks {
				if strings.Contains(handler.Command, marker) || strings.Contains(handler.CommandWindows, marker) {
					changed = true
					continue
				}
				filteredHandlers = append(filteredHandlers, handler)
			}
			if len(filteredHandlers) == 0 {
				if len(group.Hooks) > 0 {
					changed = true
				}
				continue
			}
			group.Hooks = filteredHandlers
			filteredGroups = append(filteredGroups, group)
		}
		file.Hooks[event] = filteredGroups
	}
	return changed
}

func load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Hooks: map[string][]Group{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hooks file: %w", err)
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode hooks file: %w", err)
	}
	if file.Hooks == nil {
		file.Hooks = map[string][]Group{}
	}
	return &file, nil
}

func save(path string, file *File) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		backup := path + ".backup.codex-speed-patch"
		if err := os.WriteFile(backup, existing, 0o600); err != nil {
			return fmt.Errorf("back up hooks file: %w", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hooks-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
