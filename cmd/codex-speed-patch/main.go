package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"codex-speed-patch/internal/deploy"
	"codex-speed-patch/internal/hook"
	"codex-speed-patch/internal/patch"
	"codex-speed-patch/internal/platform"
	"codex-speed-patch/internal/restart"
	"codex-speed-patch/internal/state"
)

const version = "0.1.3"

type hookInput struct {
	SessionID string `json:"session_id"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "codex-speed-patch:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "version", "--version", "-version":
		fmt.Println(version)
		return nil
	case "status", "check":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		appPath, codexHome := commonFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		result, err := (patch.Service{CodexHome: *codexHome, AppPath: *appPath}).Status()
		if err != nil {
			return err
		}
		deployment, err := deploy.Load(*codexHome)
		if err != nil {
			return err
		}
		if deployment != nil {
			inst, err := platform.Detect(*appPath)
			if err != nil {
				return err
			}
			actual, err := filepath.Abs(inst.AppPath)
			if err != nil {
				return err
			}
			if deployment.AppPath != actual {
				deployment = nil
			}
		}
		return printJSON(struct {
			*patch.Result
			Deployment *deploy.Record `json:"deployment,omitempty"`
		}{result, deployment})
	case "apply", "repair":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		appPath, codexHome := commonFlags(flags)
		hookMode := flags.Bool("hook", false, "read hook input from stdin")
		sessionIDFlag := flags.String("session-id", "", "session to reopen after restart")
		restartMode := flags.String("restart", "never", "restart policy: never or deferred-auto")
		allowMacOSTest := flags.Bool("allow-macos-test", false, "allow patching an isolated macOS app copy outside Applications")
		allowMacOSProduction := flags.Bool("allow-macos-production", false, "replace a macOS app with a verified patched bundle, backing up and restarting it")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		sessionID := *sessionIDFlag
		if *hookMode {
			if hookSessionID := readHookInput().SessionID; hookSessionID != "" {
				sessionID = hookSessionID
			}
		}
		markRestart := *restartMode == "deferred-auto"
		if *restartMode != "never" && !markRestart {
			return fmt.Errorf("invalid restart policy: %s", *restartMode)
		}
		if runtime.GOOS == "darwin" && *allowMacOSProduction && !*hookMode {
			service := deploy.Service{CodexHome: *codexHome, AppPath: *appPath, AllowTestPath: *allowMacOSTest}
			result, err := service.Prepare(sessionID)
			if err != nil {
				return err
			}
			if result.Deployment != nil {
				result, err = service.Schedule(result.Deployment.ID)
				if err != nil {
					return err
				}
			}
			return printJSON(result)
		}
		result, err := (patch.Service{CodexHome: *codexHome, AppPath: *appPath, AllowMacOSTest: *allowMacOSTest, AllowMacOSProduction: *allowMacOSProduction}).Repair(sessionID, markRestart)
		if err != nil {
			return err
		}
		return printJSON(result)
	case "deploy-worker":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		_, codexHome := commonFlags(flags)
		attemptID := flags.String("attempt-id", "", "deployment attempt identifier")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		time.Sleep(5 * time.Second)
		err := (deploy.Service{CodexHome: *codexHome}).RunWorker(*attemptID)
		deploy.Unload(*attemptID)
		return err
	case "restore":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		appPath, codexHome := commonFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if err := (patch.Service{CodexHome: *codexHome, AppPath: *appPath}).Restore(); err != nil {
			return err
		}
		fmt.Println(`{"restored":true}`)
		return nil
	case "install-hook":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		_, codexHome := commonFlags(flags)
		binaryPath := flags.String("binary", "", "stable patcher binary path")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *binaryPath == "" {
			return errors.New("--binary is required")
		}
		changed, err := hook.Install(filepath.Join(*codexHome, "hooks.json"), *binaryPath)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"changed": changed, "hooks_file": filepath.Join(*codexHome, "hooks.json")})
	case "remove-hook":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		_, codexHome := commonFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		changed, err := hook.Remove(filepath.Join(*codexHome, "hooks.json"))
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"changed": changed})
	case "restart-if-needed", "schedule-restart":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		appPath, codexHome := commonFlags(flags)
		hookMode := flags.Bool("hook", false, "read hook input from stdin")
		sessionIDFlag := flags.String("session-id", "", "session to reopen after restart")
		delay := flags.Duration("delay", 5*time.Second, "delay before graceful restart")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		sessionID := *sessionIDFlag
		if *hookMode {
			if hookSessionID := readHookInput().SessionID; hookSessionID != "" {
				sessionID = hookSessionID
			}
		}
		scheduled, err := (restart.Service{CodexHome: *codexHome, AppPath: *appPath}).Schedule(sessionID, *delay)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"scheduled": scheduled})
	case "restart-worker":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		appPath, codexHome := commonFlags(flags)
		attemptID := flags.String("attempt-id", "", "restart attempt identifier")
		delay := flags.Duration("delay", 5*time.Second, "delay before graceful restart")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return (restart.Service{CodexHome: *codexHome, AppPath: *appPath}).RunWorker(*attemptID, *delay)
	case "restart-diagnose":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		appPath, codexHome := commonFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		diagnostic, err := (restart.Service{CodexHome: *codexHome, AppPath: *appPath}).Diagnose()
		if err != nil {
			return err
		}
		return printJSON(diagnostic)
	case "clear-restart":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		_, codexHome := commonFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		path := state.ManifestPath(*codexHome)
		manifest, err := state.Load(path)
		if err != nil {
			return err
		}
		if manifest != nil {
			manifest.RestartRequired = false
			manifest.RestartFailure = ""
			if err := state.Save(path, manifest); err != nil {
				return err
			}
		}
		return nil
	case "mark-restart":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		_, codexHome := commonFlags(flags)
		sessionID := flags.String("session-id", "", "session to reopen after restart")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		path := state.ManifestPath(*codexHome)
		manifest, err := state.Load(path)
		if err != nil {
			return err
		}
		if manifest == nil || manifest.Status != "patched" {
			return errors.New("a patched manifest is required before marking restart")
		}
		manifest.RestartRequired = true
		manifest.RestartFailure = ""
		if *sessionID != "" {
			manifest.ResumeSessionID = *sessionID
		}
		if err := state.Save(path, manifest); err != nil {
			return err
		}
		return printJSON(map[string]any{"restart_required": true, "session_id": manifest.ResumeSessionID})
	default:
		return usage()
	}
}

func commonFlags(flags *flag.FlagSet) (*string, *string) {
	home, _ := os.UserHomeDir()
	return flags.String("app", "", "Codex Desktop app or installation path"), flags.String("codex-home", filepath.Join(home, ".codex"), "Codex home directory")
}

func readHookInput() hookInput {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
	if err != nil || len(data) == 0 {
		return hookInput{}
	}
	var input hookInput
	_ = json.Unmarshal(data, &input)
	return input
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage() error {
	return errors.New("usage: codex-speed-patch <status|check|apply|repair|restore|install-hook|remove-hook|restart-if-needed|schedule-restart|restart-diagnose|mark-restart|version>")
}
