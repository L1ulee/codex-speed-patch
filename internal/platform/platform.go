package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Installation struct {
	Platform    string `json:"platform"`
	PackageKind string `json:"package_kind"`
	AppPath     string `json:"app_path"`
	AsarPath    string `json:"asar_path"`
	InfoPlist   string `json:"info_plist,omitempty"`
	Executable  string `json:"executable"`
	CLIPath     string `json:"cli_path,omitempty"`
	Version     string `json:"version"`
}

func Detect(explicitPath string) (*Installation, error) {
	if explicitPath != "" {
		return inspect(explicitPath)
	}
	for _, candidate := range candidates() {
		installation, err := inspect(candidate)
		if err == nil {
			return installation, nil
		}
	}
	return nil, errors.New("could not locate a supported Codex Desktop installation")
}

func inspect(candidate string) (*Installation, error) {
	candidate = filepath.Clean(expandHome(candidate))
	switch runtime.GOOS {
	case "darwin":
		appPath := candidate
		if !strings.HasSuffix(strings.ToLower(appPath), ".app") {
			for {
				if strings.HasSuffix(strings.ToLower(appPath), ".app") {
					break
				}
				parent := filepath.Dir(appPath)
				if parent == appPath {
					return nil, errors.New("path is not inside a macOS app bundle")
				}
				appPath = parent
			}
		}
		resources := filepath.Join(appPath, "Contents", "Resources")
		asarPath := filepath.Join(resources, "app.asar")
		infoPlist := filepath.Join(appPath, "Contents", "Info.plist")
		executable := filepath.Join(appPath, "Contents", "MacOS", "ChatGPT")
		if !regularFile(asarPath) || !regularFile(infoPlist) || !regularFile(executable) {
			return nil, errors.New("incomplete macOS Codex app bundle")
		}
		identifier, err := plistValue(infoPlist, "CFBundleIdentifier")
		if err != nil || identifier != "com.openai.codex" {
			return nil, errors.New("bundle is not Codex Desktop")
		}
		version, _ := plistValue(infoPlist, "CFBundleShortVersionString")
		return &Installation{
			Platform:    runtime.GOOS,
			PackageKind: "macos-app",
			AppPath:     appPath,
			AsarPath:    asarPath,
			InfoPlist:   infoPlist,
			Executable:  executable,
			CLIPath:     filepath.Join(resources, "codex"),
			Version:     version,
		}, nil
	case "windows":
		root := candidate
		if strings.EqualFold(filepath.Base(root), "ChatGPT.exe") || strings.EqualFold(filepath.Base(root), "Codex.exe") {
			root = filepath.Dir(root)
		}
		asarPath := filepath.Join(root, "resources", "app.asar")
		if !regularFile(asarPath) {
			return nil, errors.New("resources/app.asar was not found")
		}
		executable := firstExisting(filepath.Join(root, "ChatGPT.exe"), filepath.Join(root, "Codex.exe"))
		if executable == "" {
			return nil, errors.New("Codex executable was not found")
		}
		return &Installation{Platform: runtime.GOOS, PackageKind: "windows-install", AppPath: root, AsarPath: asarPath, Executable: executable, CLIPath: filepath.Join(root, "resources", "codex.exe"), Version: fileVersionFallback(root)}, nil
	case "linux":
		root := candidate
		if info, err := os.Stat(root); err == nil && !info.IsDir() {
			root = filepath.Dir(root)
		}
		asarPath := firstExisting(filepath.Join(root, "resources", "app.asar"), filepath.Join(root, "app.asar"))
		if asarPath == "" {
			return nil, errors.New("app.asar was not found")
		}
		kind := "linux-install"
		if os.Getenv("APPIMAGE") != "" || strings.Contains(root, "/.mount_") {
			kind = "appimage-mounted"
		}
		executable := os.Getenv("APPIMAGE")
		if executable == "" {
			executable = firstExisting(filepath.Join(root, "chatgpt"), filepath.Join(root, "codex"), candidate)
		}
		return &Installation{Platform: runtime.GOOS, PackageKind: kind, AppPath: root, AsarPath: asarPath, Executable: executable, CLIPath: firstExisting(filepath.Join(root, "resources", "codex"), filepath.Join(root, "codex")), Version: fileVersionFallback(root)}, nil
	default:
		return nil, fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func candidates() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Applications/ChatGPT.app", filepath.Join(home, "Applications", "ChatGPT.app"), "/Applications/Codex.app", filepath.Join(home, "Applications", "Codex.app")}
	case "windows":
		return []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "ChatGPT"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Codex"),
			filepath.Join(os.Getenv("PROGRAMFILES"), "ChatGPT"),
			filepath.Join(os.Getenv("PROGRAMFILES"), "Codex"),
		}
	case "linux":
		return []string{
			os.Getenv("APPDIR"),
			os.Getenv("APPIMAGE"),
			"/opt/ChatGPT",
			"/opt/chatgpt",
			"/opt/Codex",
			"/usr/lib/chatgpt",
			filepath.Join(home, ".local", "opt", "chatgpt"),
		}
	default:
		return nil
	}
}

func CLIVersion(path string) string {
	if path == "" || !regularFile(path) {
		return ""
	}
	output, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func plistValue(path, key string) (string, error) {
	output, err := exec.Command("/usr/bin/plutil", "-extract", key, "raw", "-o", "-", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read plist %s: %s", key, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func firstExisting(paths ...string) string {
	for _, path := range paths {
		if path != "" && regularFile(path) {
			return path
		}
	}
	return ""
}

func expandHome(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func fileVersionFallback(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "unknown"
	}
	return info.ModTime().UTC().Format("20060102T150405Z")
}
