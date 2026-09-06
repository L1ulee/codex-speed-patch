# Codex API Speed Mode Patch

[中文](README.md)

This is a version-aware Codex Desktop patcher that exposes the native Speed selector when Codex uses an API/custom model provider.

The patcher reuses Codex's existing model catalog, `service_tiers`, `service_tier` configuration writes, and request forwarding. It changes only the desktop capability gate that limits the Speed control to ChatGPT authentication.

## Current Status

- Patcher version: `0.1.0`
- macOS test date: `2026-09-07`
- Tested Codex Desktop: `26.901.41123`, build `7942`
- Tested Codex CLI: `0.153.3`
- Tested model: `gpt-5.6-sol`
- Verified control: `aria-label="速度 快速"`, `disabled=false`

Both the production application and an isolated copy passed these checks:

- Codex CLI/app-server initialized successfully.
- The main window reached `ready-to-show`.
- The model picker opened successfully.
- An enabled Speed control appeared under an API/custom provider.
- `codesign --verify --deep --strict` passed.

## How It Works

The Codex model catalog can already advertise speed tiers:

```json
{
  "additional_speed_tiers": ["fast"],
  "service_tiers": [
    { "id": "priority", "name": "Fast" },
    { "id": "ultrafast", "name": "Ultrafast" }
  ]
}
```

Codex Desktop still computes `isServiceTierAllowed` from the authentication mode. The original gate requires ChatGPT authentication, so the UI hides Speed even when an API/custom provider model advertises valid speed tiers.

The patcher performs these steps:

1. Parse the `app.asar` file index.
2. Find exactly one supported semantic speed-permission expression in the JavaScript assets.
3. Apply an equal-length replacement so ASAR offsets do not move.
4. Recalculate the target file SHA-256 and ASAR block hashes.
5. Update integrity records inside the ASAR Header JSON.
6. On macOS, write the Header JSON SHA-256 to `ElectronAsarIntegrity`.
7. Re-sign and verify the application.

## Safety Model

- Exactly one supported semantic target is required.
- The patch stops if no target or multiple targets are found.
- The replacement expression must have the same length as the original.
- The original file hash and every block hash are verified before modification.
- The patched ASAR is reopened and must report the `patched` state.
- Each Codex version uses a separate backup directory.
- A full APFS-cloned `.app` backup is created before a production macOS patch.
- Unknown Codex versions are not modified speculatively.
- Automatic restart requests graceful shutdown only; force termination is disabled.
- Restart attempt IDs prevent restart loops.

## Important macOS Notes

Modifying a resource inside the `.app` invalidates the original OpenAI Developer ID signature. The patcher applies a local deep ad-hoc signature to the modified application.

A production patch requires:

```bash
--allow-macos-production
```

An isolated-copy test requires:

```bash
--allow-macos-test
```

Test mode refuses to modify applications inside `/Applications` or the user's `Applications` directory. Production mode creates a full backup before allowing a modification inside an Applications directory.

### ElectronAsarIntegrity

`ElectronAsarIntegrity` contains the **SHA-256 of the ASAR Header JSON**, not the SHA-256 of the complete `app.asar` file.

Writing the complete ASAR file hash causes Electron startup integrity failure. Typical errors include:

```text
RBSRequestErrorDomain Code=5
NSPOSIXErrorDomain Code=163
Launchd job spawn failed
```

Version `0.1.0` uses the correct Header JSON hashing algorithm.

### Signing Impact

The patched application no longer retains the OpenAI Developer ID or original TeamIdentifier. Potentially affected capabilities include:

- Sparkle automatic updates.
- Keychain access groups.
- Push notifications.
- Application Groups.
- System services that depend on the official signing identity.

The full official application backup restores these capabilities.

## Quick Start

### 1. Select a Binary

`dist/` contains:

```text
codex-speed-patch-darwin-arm64
codex-speed-patch-darwin-amd64
codex-speed-patch-linux-amd64
codex-speed-patch-linux-arm64
codex-speed-patch-windows-amd64.exe
codex-speed-patch-windows-arm64.exe
```

macOS example:

```bash
chmod +x dist/codex-speed-patch-darwin-arm64
```

### 2. Inspect the State

```bash
dist/codex-speed-patch-darwin-arm64 status \
  --app "/Applications/ChatGPT.app"
```

State values:

- `original`: the patch is not active.
- `patched`: the patch is active.
- `unknown`: the installed version does not match a safe patch rule.
- `restored`: the manifest records a completed rollback.

### 3. Test an Isolated macOS Copy

```bash
cp -cR "/Applications/ChatGPT.app" "/path/to/ChatGPT-Speed-Test.app"

dist/codex-speed-patch-darwin-arm64 repair \
  --allow-macos-test \
  --app "/path/to/ChatGPT-Speed-Test.app" \
  --restart=never
```

### 4. Patch the Production macOS App

```bash
dist/codex-speed-patch-darwin-arm64 repair \
  --allow-macos-production \
  --app "/Applications/ChatGPT.app" \
  --restart=deferred-auto
```

Before modifying the production application, the patcher creates a full backup under:

```text
~/.codex/patches/speed-mode/backups/<APP_VERSION>-<ASAR_HASH>/ChatGPT.app
```

### 5. Install Automatic Maintenance Hooks

Place the binary at a stable path, for example:

```text
~/.codex/patches/speed-mode/bin/codex-speed-patch
```

Install the hooks:

```bash
~/.codex/patches/speed-mode/bin/codex-speed-patch install-hook \
  --binary ~/.codex/patches/speed-mode/bin/codex-speed-patch
```

The installer adds these entries to `~/.codex/hooks.json`:

- `SessionStart`: detects Codex updates and reapplies the patch.
- `Stop`: schedules one deferred restart after the active turn completes.

Codex calculates a `currentHash` for each hook. A new hook must be reviewed in Codex, or its confirmed hash must be written to `hooks.state`.

## Automatic Update and Restart

The `SessionStart` hook reads the current version and ASAR hash. If an update restores the original speed gate, it creates a new full-version backup, requires one unique target, applies the patch, verifies integrity, and then sets `restart_required`.

The `Stop` hook starts a detached restart helper after the current turn finishes. The helper requests graceful Codex shutdown, waits for up to 15 seconds, relaunches the app, and opens `codex://threads/SESSION_ID` when a session ID is available.

If graceful shutdown fails, the pending restart state is preserved. Codex is not force-terminated.

## Command Reference

```bash
# Inspect state
codex-speed-patch status

# Detect and repair
codex-speed-patch repair

# Patch the production macOS app
codex-speed-patch repair --allow-macos-production --restart=deferred-auto

# Patch an isolated macOS copy
codex-speed-patch repair --allow-macos-test --app "/path/to/Test.app"

# Restore a backup
codex-speed-patch restore --app "/Applications/ChatGPT.app"

# Install or remove hooks
codex-speed-patch install-hook --binary /absolute/path/to/codex-speed-patch
codex-speed-patch remove-hook

# Restart operations
codex-speed-patch restart-diagnose
codex-speed-patch mark-restart --session-id SESSION_ID
codex-speed-patch schedule-restart --session-id SESSION_ID --delay 20s
codex-speed-patch clear-restart

# Print the version
codex-speed-patch version
```

## Rollback

```bash
codex-speed-patch restore \
  --app "/Applications/ChatGPT.app"
```

When a complete macOS `.app` backup exists, rollback clones the official backup, moves the patched app to a staging path, atomically activates the official copy, verifies the official signature, and removes the staged patched app.

To remove the automatic maintenance hooks as well:

```bash
codex-speed-patch remove-hook
```

## Platform Notes

### macOS

- Apple Silicon and Intel builds are available.
- Production patching requires `--allow-macos-production`.
- Isolated tests require `--allow-macos-test`.
- A full APFS application backup is created before production modification.
- The modified app receives a deep ad-hoc signature.

### Windows

- AMD64 and ARM64 builds are available.
- The patch can be applied when the current user can write the installation directory.
- System locations such as `Program Files` may require elevation.
- Installer updates may overwrite the patch; the `SessionStart` hook detects this state.

### Linux

- AMD64 and ARM64 builds are available.
- Writable `.deb`, `.rpm`, and extracted installations are supported.
- A mounted AppImage is read-only and is not modified in place.
- Snap and Flatpak packages are immutable and are not modified in place.

## Building from Source

Go `1.24` or newer is required.

```bash
go test ./...
go vet ./...
go build ./cmd/codex-speed-patch

GOOS=darwin GOARCH=arm64 go build -o dist/codex-speed-patch-darwin-arm64 ./cmd/codex-speed-patch
GOOS=darwin GOARCH=amd64 go build -o dist/codex-speed-patch-darwin-amd64 ./cmd/codex-speed-patch
GOOS=linux GOARCH=amd64 go build -o dist/codex-speed-patch-linux-amd64 ./cmd/codex-speed-patch
GOOS=linux GOARCH=arm64 go build -o dist/codex-speed-patch-linux-arm64 ./cmd/codex-speed-patch
GOOS=windows GOARCH=amd64 go build -o dist/codex-speed-patch-windows-amd64.exe ./cmd/codex-speed-patch
GOOS=windows GOARCH=arm64 go build -o dist/codex-speed-patch-windows-arm64.exe ./cmd/codex-speed-patch
```

Release SHA-256 values are listed in [CHECKSUMS.txt](CHECKSUMS.txt).

## State Files

The default state directory is:

```text
~/.codex/patches/speed-mode/
```

- `manifest.json`: version, patch rule, hashes, backup, and restart state.
- `patch.lock`: patch concurrency lock.
- `restart.lock`: restart concurrency lock.
- `backups/`: rollback copies for each Codex version.
- `bin/`: stable patcher binary.

## Known Limitations

- A Codex frontend change can cause the existing semantic rule to return `unknown`.
- An ad-hoc signature cannot replace the OpenAI Developer ID identity.
- The automatic updater itself may depend on the official signing identity.
- Restarting during multiple windows or parallel tasks affects the entire Codex application.
- The upstream API must accept the selected `service_tier`; displaying the control does not guarantee server-side tier support.
