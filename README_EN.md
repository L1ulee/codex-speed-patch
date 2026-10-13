# Codex API Speed Mode Patch

[中文](README.md)

This is a version-aware Codex Desktop patcher that exposes the native Speed selector when Codex uses an API/custom model provider.

The patcher reuses Codex's existing model catalog, `service_tiers`, `service_tier` configuration writes, and request forwarding. It covers the API-authenticated Speed control, saved tier readers, and continuation/derived-thread permission paths.

## Current Status

- Patcher version: `0.1.2` (unreleased)
- macOS test date: `2026-10-13`
- Tested Codex Desktop: `26.1007.30912`

The isolated copy for this version passed these checks:

- Codex CLI/app-server initialized successfully.
- The main window reached `ready-to-show`.
- Native Speed writer/readers passed runtime comparisons for API authentication, tier mapping, and policy denials.
- A local mock upstream captured `service_tier: priority` on the first turn, reasoning continuation, and desktop-derived thread.
- `codesign --verify --deep --strict` passed.
- Full rollback restored the original ASAR and official signature.

The `0.1.2` patch was deployed to the installed app through full backup and bundle replacement, and the user confirmed that Speed/Fast is visible and switchable. The CLI's automatic replacement flow is tested separately on isolated copies. Autonomous subagent tools, real upstream requests, Keychain, push notifications, and an actual official update installation still require end-to-end verification.

## 0.1.2 Fixes (Unreleased)

- Rule `speed-mode-api-v2` classifies old UI-only patches as `partial` and covers saved tier readers, queued execution, and continuation/derived-thread paths. Fast retains the model catalog's actual tier ID, such as `priority`.
- Supports the separate Fast/Ultrafast policies in `26.1007.30912` while retaining explicit policy denials.
- Re-signs Sparkle Autoupdate, Updater, XPC services, and Framework from the inside out to match the host and address installer connection error 4005. Update public keys and package validation remain intact; only developer-identity entitlements incompatible with ad-hoc signing are removed.
- Rejects restoration when a manifest backup belongs to another installed version, preventing rollback over an official update.
- Isolated startup, native request-function comparisons, ASAR integrity, and update signing-chain checks passed. A local mock upstream captured `service_tier: priority` on the first turn, reasoning continuation, and desktop-derived thread after the native Speed writer saved it. Autonomous subagent tools and an actual official update installation still require end-to-end verification.
- A manual `repair --allow-macos-production` command backs up the app, prepares a patched bundle, schedules one replacement, and checks startup. Failures restore the previous bundle and manifest. Hooks retain their existing behavior and do not trigger bundle deployment.

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
7. Re-sign Electron Helpers/Framework, Sparkle helpers/Framework, and outer app in that order; verify resource seals and update signer identities.

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

The GitHub `v0.1.1` Release contains source only. Earlier six-platform `0.1.2` builds remain in `dist/v0.1.2/`. The Apple Silicon build with automatic bundle replacement is in `dist/v0.1.2-direct-replace/`, with its own `SHA256SUMS.txt`.

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
chmod +x dist/v0.1.2-direct-replace/codex-speed-patch-darwin-arm64
```

### 2. Inspect the State

```bash
dist/v0.1.2-direct-replace/codex-speed-patch-darwin-arm64 status \
  --app "/Applications/ChatGPT.app"
```

State values:

- `original`: the patch is not active.
- `patched`: the patch is active.
- `partial`: a legacy partial patch is present, without complete request coverage.
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

JavaScript request and policy runtime regression tests also require Node.js; they explicitly skip when Node.js is absent.

```bash
go test ./...
go vet ./...
go build ./cmd/codex-speed-patch

GOOS=darwin GOARCH=arm64 go build -o dist/v0.1.2/codex-speed-patch-darwin-arm64 ./cmd/codex-speed-patch
GOOS=darwin GOARCH=amd64 go build -o dist/v0.1.2/codex-speed-patch-darwin-amd64 ./cmd/codex-speed-patch
GOOS=linux GOARCH=amd64 go build -o dist/v0.1.2/codex-speed-patch-linux-amd64 ./cmd/codex-speed-patch
GOOS=linux GOARCH=arm64 go build -o dist/v0.1.2/codex-speed-patch-linux-arm64 ./cmd/codex-speed-patch
GOOS=windows GOARCH=amd64 go build -o dist/v0.1.2/codex-speed-patch-windows-amd64.exe ./cmd/codex-speed-patch
GOOS=windows GOARCH=arm64 go build -o dist/v0.1.2/codex-speed-patch-windows-arm64.exe ./cmd/codex-speed-patch
```

Local `0.1.2` checksums are in `dist/v0.1.2/SHA256SUMS.txt`. [CHECKSUMS.txt](CHECKSUMS.txt) records only historical `v0.1.0` binary checksums.

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
