# Codex API Speed Mode Patch

[中文](README.md)

This is a version-aware Codex Desktop patcher that exposes the native Speed selector when Codex uses an API/custom model provider.

The patcher reuses Codex's existing model catalog, `service_tiers`, `service_tier` configuration writes, and request forwarding. It covers the API-authenticated Speed control, saved tier readers, and continuation/derived-thread permission paths.

## Current Status

- Patcher version: `0.1.3`
- macOS test date: `2026-10-17`
- Codex Desktop tested for this release: `26.1013.40934`; previously tested: `26.1007.30912`

The isolated copy passed these checks:

- CLI/app-server initialization, stable application startup, and normal shutdown.
- Native Speed submenus, selected Ultrafast values, and Speed command interactions for `gpt-6.1-sol`, `gpt-5.6-sol`, and `gpt-6-astra`.
- Other visible models retain the ordinary Fast control and `priority` mapping; explicit policy denials remain effective.
- A local mock upstream captured `service_tier: ultrafast` on the first turn, reasoning continuation, and desktop-derived thread for all three models. Ordinary Fast requests retained `priority`.
- `codesign --verify --deep --strict`, nested CLI signing, and Sparkle signer-chain checks.
- The full Go test suite and `go vet`.

The production app completed bundle replacement and startup checks, with deployment state `deployed`. This does not confirm Ultrafast interactions in a real account or support by the actual upstream. Real upstream requests, autonomous subagent tools, Keychain, push notifications, and actual official update installation still require end-to-end verification. Windows/Linux release binaries were cross-compiled and checked for format only.

## 0.1.3 Fixes

- Rule `speed-mode-api-v3` repairs both native tier options and selected-tier resolution, including the separate menu/resolver modules in `26.1013.40934`.
- Uses native model variants and provider tier declarations first. The operator-confirmed `gpt-6.1-sol`, `gpt-5.6-sol`, and `gpt-6-astra` gain Ultrafast when their structured descriptions exist in the app. Other models keep their declared tiers; model-family names do not imply Ultrafast support.
- Updates the embedded CLI model catalog as well as the UI, preventing the CLI from dropping `ultrafast` from requests. Equal-length replacement preserves executable offsets, unknown metadata, and bytes outside the catalog.
- Reuses whitespace reserved by earlier patches when extending the catalog, fixing false insufficient-space errors and preserving idempotency.
- Detects the nested `codex-cli/CodexCLI.app` and re-signs it only when the CLI changes. Status and deployment checks validate CLI hashes; rollback retains original ASAR and CLI backups.

## 0.1.2 Fixes

- Rule `speed-mode-api-v2` covers saved tier readers, queued execution, and continuation/derived-thread paths. Fast retains the model catalog's actual tier ID, such as `priority`.
- Supports independent Fast/Ultrafast permissions and preserves explicit policy denials.
- Repairs signer identity mismatches between Sparkle helpers and the host while retaining update public keys and package validation. Actual official update installation still requires end-to-end verification.
- `repair --allow-macos-production` provides a full backup, one-shot bundle replacement, startup checks, and rollback on failure. Hooks do not trigger bundle deployment, and stale backups cannot overwrite an updated app.

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
2. Match the UI, request-permission, and saved tier reader rules; stop if required targets are missing or ambiguous.
3. Preserve the original payload, including shared content ranges, append changed assets, and rebuild their index nodes.
4. Recalculate every changed asset's SHA-256 and ASAR block hashes.
5. Update Header JSON offsets, sizes, and integrity while retaining links, unpacked entries, and unknown metadata.
6. On macOS, write the Header JSON SHA-256 to `ElectronAsarIntegrity` and update the Framework's `__asar_integrity` digest.
7. Reconcile the embedded CLI catalog with verified model capabilities; re-sign its nested bundle when changed, then verify signatures and update signer identities across Electron, Sparkle, and the outer app.

## Safety Model

- UI and request-permission rules must each match once, and every saved tier reader must be covered.
- The patch stops if no target or multiple targets are found.
- Only changed asset index nodes are redirected; the original payload remains intact.
- The original file hash and every block hash are verified before modification.
- The patched ASAR is reopened and must report the `patched` state.
- Each Codex version uses a separate backup directory.
- On macOS, signing identity and path are checked before writing; installed apps are replaced with verified patched bundles, retaining signing preflight in the isolated repair flow.
- A full APFS-cloned `.app` backup is created before modifying an eligible macOS app.
- Unknown Codex versions are not modified speculatively.
- Automatic restart requests graceful shutdown only; force termination is disabled.
- Restart attempt IDs prevent restart loops.

## Important macOS Notes

The patched bundle uses a local ad-hoc signature, while the full official backup retains the OpenAI Developer ID. Production deployment repairs a temporary copy using the existing patching and signing logic, verifies it, and activates the complete bundle at the installed path.

Deploy the installed app manually:

```bash
codex-speed-patch repair --allow-macos-production --app "/Applications/ChatGPT.app"
```

The command returns `status: scheduled`. An independent one-shot job requests termination of the selected process, waits for its helpers, replaces the bundle, and relaunches it. In `status` output, `deployment.status: deployed` confirms bundle and startup checks; `failed` includes `deployment.error` and `rolled_back`. If an official update changes the app after preparation, deployment is cancelled and the updated app is retained.

Bundle replacement always requires exit and relaunch; `--restart` controls the existing in-place repair flow only. An already complete patch with valid signer identities returns `changed: false` without another replacement or restart. Backups and logs live under the selected `--codex-home`; user configuration and authentication are not copied or rewritten.

An isolated-copy test requires:

```bash
--allow-macos-test
```

Test mode only allows isolated copies in temporary directories and refuses to modify applications inside `/Applications` or the user's `Applications` directory. A copied official app is fully backed up before the isolated signing experiment.

### ElectronAsarIntegrity

`ElectronAsarIntegrity` contains the **SHA-256 of the ASAR Header JSON**, not the SHA-256 of the complete `app.asar` file.

Writing the complete ASAR file hash causes Electron startup integrity failure. Typical errors include:

```text
RBSRequestErrorDomain Code=5
NSPOSIXErrorDomain Code=163
Launchd job spawn failed
```

The current implementation also updates the Framework digest of the complete `ElectronAsarIntegrity` dictionary. Updating only `Info.plist` causes Electron to report `Failed to get integrity for validatable asar archive`.

### Signing Impact

An ad-hoc signature cannot retain the OpenAI Developer ID or original TeamIdentifier. Potentially affected capabilities include:

- Sparkle automatic updates.
- Keychain access groups.
- Push notifications.
- Application Groups.
- System services that depend on the official signing identity.

These capabilities may be affected. A full official application backup restores the original signing identity.

## Quick Start

### 1. Select a Binary

Download your platform binary and `SHA256SUMS.txt` from the [v0.1.3 Release](https://github.com/L1ulee/codex-speed-patch/releases/tag/v0.1.3). Local source builds are in `dist/v0.1.3/`; verify against the checksum manifest from the same Release.

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
chmod +x dist/v0.1.3/codex-speed-patch-darwin-arm64
```

### 2. Inspect the State

```bash
dist/v0.1.3/codex-speed-patch-darwin-arm64 status \
  --app "/Applications/ChatGPT.app"
```

State values:

- `original`: the patch is not active.
- `patched`: the patch is active.
- `partial`: UI, request paths, or the embedded CLI catalog still require repair.
- `unknown`: the installed version does not match a safe patch rule.
- `restored`: the manifest records a completed rollback.

### 3. Test an Isolated macOS Copy

```bash
# Copy an unpatched, officially signed app
cp -cR "/Applications/ChatGPT.app" "/private/tmp/Codex-Speed-Test.app"

codex-speed-patch repair \
  --allow-macos-test \
  --app "/private/tmp/Codex-Speed-Test.app" \
  --restart=never
```

Test mode only allows copies in temporary directories. Even when a copy retains the official signature, it is fully backed up before ad-hoc re-signing.

To test complete bundle replacement, pass both scope flags and a separate state directory:

```bash
codex-speed-patch repair --allow-macos-production --allow-macos-test \
  --app "/private/tmp/Codex-Speed-Test.app" --codex-home "/private/tmp/Codex-Speed-Test-State"
```

This replaces only the selected test bundle and launches it with a separate browser profile and empty authentication configuration.

### 4. Restore a Previously Patched macOS App

```bash
codex-speed-patch restore --app "/Applications/ChatGPT.app"
```

`restore` requires a backup recorded in the manifest. Full backups from earlier versions are commonly stored under:

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

The installer adds these entries to `~/.codex/hooks.json`; do not install the automatic patch hook for official signed macOS apps:

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

# Installed macOS app: back up, replace the bundle, and relaunch
codex-speed-patch repair --allow-macos-production

# An isolated macOS copy in a temporary directory is backed up and re-signed
codex-speed-patch repair --allow-macos-test --app "/private/tmp/Test.app"

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

When the manifest records a full macOS backup matching the installed version, rollback clones it, stages the patched app, activates the official copy, verifies its signature, and removes the staged patched app.

To remove the automatic maintenance hooks as well:

```bash
codex-speed-patch remove-hook
```

## Platform Notes

### macOS

- Apple Silicon and Intel builds are available.
- `--allow-macos-production` enables complete installed-app replacement for manual commands.
- `--allow-macos-test` alone repairs a temporary copy; both flags enable isolated replacement testing.
- An existing full APFS app backup can restore the original signed version.

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

Runtime regression tests require Node.js; the complete suite cannot pass without it.

```bash
go test ./...
go vet ./...
go build ./cmd/codex-speed-patch

GOOS=darwin GOARCH=arm64 go build -o dist/v0.1.3/codex-speed-patch-darwin-arm64 ./cmd/codex-speed-patch
GOOS=darwin GOARCH=amd64 go build -o dist/v0.1.3/codex-speed-patch-darwin-amd64 ./cmd/codex-speed-patch
GOOS=linux GOARCH=amd64 go build -o dist/v0.1.3/codex-speed-patch-linux-amd64 ./cmd/codex-speed-patch
GOOS=linux GOARCH=arm64 go build -o dist/v0.1.3/codex-speed-patch-linux-arm64 ./cmd/codex-speed-patch
GOOS=windows GOARCH=amd64 go build -o dist/v0.1.3/codex-speed-patch-windows-amd64.exe ./cmd/codex-speed-patch
GOOS=windows GOARCH=arm64 go build -o dist/v0.1.3/codex-speed-patch-windows-arm64.exe ./cmd/codex-speed-patch
```

Local `0.1.3` checksums are in `dist/v0.1.3/SHA256SUMS.txt`. [CHECKSUMS.txt](CHECKSUMS.txt) records only historical `v0.1.0` binary checksums.

## State Files

The default state directory is:

```text
~/.codex/patches/speed-mode/
```

- `manifest.json`: version, patch rule, hashes, backup, and restart state.
- `deployment.json`: the most recent bundle replacement status, error, and rollback result.
- `deployments/`: previous manifests, one-shot workers, and logs for each attempt.
- `patch.lock`: patch concurrency lock.
- `restart.lock`: restart concurrency lock.
- `backups/`: rollback copies for each Codex version.
- `bin/`: stable patcher binary.

## Known Limitations

- A Codex frontend change can cause the existing semantic rule to return `unknown`.
- Replaced bundles use ad-hoc signatures; capabilities depending on the official Developer ID still require verification.
- The automatic updater itself may depend on the official signing identity.
- Restarting during multiple windows or parallel tasks affects the entire Codex application.
- The upstream API must accept the selected `service_tier`; displaying the control does not guarantee server-side tier support.
