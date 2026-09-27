# Codex API Speed Mode 补丁

[English](README_EN.md)

这是一个版本感知的 Codex Desktop 补丁器，用于在 API/custom model provider 模式下显示 Codex 原生 Speed 速度选择器。

补丁器复用 Codex 已有的模型目录、`service_tiers`、`service_tier` 配置写入和请求传递流程，仅修改桌面端把 Speed 控件限制为 ChatGPT 登录模式的能力判断。

## 当前状态

- 补丁器版本：`0.1.1`
- macOS 实测日期：`2026-09-28`
- 已测试 Codex Desktop：`26.924.22138`

隔离副本和正式应用均通过以下检查：

- Codex CLI/app-server 初始化成功。
- 主窗口完成 `ready-to-show`。
- 模型选择器可以打开。
- API/custom provider 下原生 Fast 控件可见且可切换（人工确认）。
- `codesign --verify --deep --strict` 通过。

本次正式应用采用已验证隔离副本离线替换，不是补丁器 CLI 对官方签名应用的原位修改。真实模型请求、Keychain、推送和自动更新尚未验证。

## 工作原理

Codex 模型目录已经可以声明速度档位：

```json
{
  "additional_speed_tiers": ["fast"],
  "service_tiers": [
    { "id": "priority", "name": "Fast" },
    { "id": "ultrafast", "name": "Ultrafast" }
  ]
}
```

桌面端仍会根据登录方式计算 `isServiceTierAllowed`。原始逻辑要求使用 ChatGPT 身份验证，因此 API/custom provider 即使提供合法速度档位，界面也会隐藏 Speed 控件。

补丁器按以下流程工作：

1. 解析 `app.asar` 文件索引。
2. 在 JavaScript 资源中查找唯一的速度权限语义表达式。
3. 使用等长表达式替换，保持 ASAR 文件偏移不变。
4. 重算目标文件 SHA-256 和 ASAR 分块哈希。
5. 更新 ASAR Header JSON 中的完整性记录。
6. macOS 上将 Header JSON SHA-256 写入 `ElectronAsarIntegrity`，并同步 Framework 的 `__asar_integrity` 摘要。
7. 按 Helper、Framework、外层应用的顺序重新签名并验证。

## 安全机制

- 必须且只能找到一个受支持的语义目标。
- 无目标或多个候选目标时终止修改。
- 替换表达式必须与原表达式等长。
- 修改前验证文件哈希和全部分块哈希。
- 修改后重新打开 ASAR，并确认状态为 `patched`。
- 每个 Codex 版本使用独立备份目录。
- macOS 上在写入前检查签名身份和路径，拒绝修改保留 TeamIdentifier 的正式应用。
- 对允许补丁的 macOS 应用，修改前使用 APFS clone 备份整个 `.app`。
- 未知版本不会进行猜测性修改。
- 自动重启只请求正常退出，不使用强制终止。
- 重启 attempt ID 用于防止循环重启。

## macOS 重要说明

修改 `.app` 内的资源会破坏 OpenAI 原始 Developer ID 签名。本工具只能执行本地 ad-hoc 重签名。补丁器仍会在预检阶段拒绝对带 TeamIdentifier 的官方应用原位修改；本次正式应用使用隔离副本离线替换，并保留了原签名完整备份。

`--allow-macos-production` 仅放行生产路径检查，不能绕过官方签名预检：

```bash
--allow-macos-production
```

隔离副本测试必须显式传入：

```bash
--allow-macos-test
```

测试模式仅允许临时目录中的隔离副本，拒绝修改 `/Applications` 和用户 `Applications` 目录；复制出的官方签名副本会先完整备份，再按隔离测试流程重签。

### ElectronAsarIntegrity

`ElectronAsarIntegrity` 使用 **ASAR Header JSON 的 SHA-256**，不是整个 `app.asar` 文件的 SHA-256。

写入整个 ASAR 文件哈希会导致 Electron 启动完整性失败，可能出现：

```text
RBSRequestErrorDomain Code=5
NSPOSIXErrorDomain Code=163
Launchd job spawn failed
```

当前实现还会更新 Framework 中对整个 `ElectronAsarIntegrity` 字典的摘要。只更新 `Info.plist` 会导致 Electron 报 `Failed to get integrity for validatable asar archive`。

### 签名影响

ad-hoc 签名不能保留 OpenAI Developer ID 和原始 TeamIdentifier，可能影响：

- Sparkle 自动更新。
- Keychain 访问组。
- 推送通知。
- Application Groups。
- 依赖官方签名身份的系统服务。

这些功能可能受影响。已修改过的应用可使用完整官方备份恢复原签名身份。

## 快速开始

### 1. 选择二进制

本次 GitHub Release 仅含源码。按下文命令构建后，`dist/` 可包含：

```text
codex-speed-patch-darwin-arm64
codex-speed-patch-darwin-amd64
codex-speed-patch-linux-amd64
codex-speed-patch-linux-arm64
codex-speed-patch-windows-amd64.exe
codex-speed-patch-windows-arm64.exe
```

macOS 示例：

```bash
chmod +x dist/codex-speed-patch-darwin-arm64
```

### 2. 检查状态

```bash
dist/codex-speed-patch-darwin-arm64 status \
  --app "/Applications/ChatGPT.app"
```

状态值：

- `original`：尚未应用补丁。
- `patched`：补丁已生效。
- `unknown`：当前版本不符合安全补丁规则。
- `restored`：manifest 记录应用已经回滚。

### 3. macOS 隔离副本测试

```bash
# 从未补丁的官方签名应用复制
cp -cR "/Applications/ChatGPT.app" "/private/tmp/Codex-Speed-Test.app"

codex-speed-patch repair \
  --allow-macos-test \
  --app "/private/tmp/Codex-Speed-Test.app" \
  --restart=never
```

测试模式仅允许临时目录中的副本；即使副本保留官方签名，也会先完整备份，再执行 ad-hoc 重签。官方签名应用不支持 CLI 原位补丁。

### 4. 恢复已修改的 macOS 应用

```bash
codex-speed-patch restore --app "/Applications/ChatGPT.app"
```

仅当 manifest 记录了备份时，`restore` 才能恢复。先前版本创建的完整备份通常位于：

```text
~/.codex/patches/speed-mode/backups/<APP_VERSION>-<ASAR_HASH>/ChatGPT.app
```

### 5. 安装自动维护 hook

将二进制放到稳定路径，例如：

```text
~/.codex/patches/speed-mode/bin/codex-speed-patch
```

安装 hook：

```bash
~/.codex/patches/speed-mode/bin/codex-speed-patch install-hook \
  --binary ~/.codex/patches/speed-mode/bin/codex-speed-patch
```

安装器会在 `~/.codex/hooks.json` 中添加以下 Hook；macOS 官方签名应用不应安装自动补丁 Hook：

- `SessionStart`：检测 Codex 更新并重新应用补丁。
- `Stop`：当前轮次完成后调度一次延迟重启。

Codex 会为 hook 计算 `currentHash`。新 hook 需要在 Codex 中审核，或将确认后的哈希写入 `hooks.state`。

## 自动更新和重启

`SessionStart` hook 会读取当前版本和 ASAR 哈希。若更新恢复了原始速度权限逻辑，它会创建新版本完整备份，要求目标唯一匹配，应用补丁并验证完整性，成功后写入 `restart_required`。

`Stop` hook 会在当前轮次结束后启动独立重启助手。助手请求 Codex 正常退出，最多等待 15 秒，随后重新启动应用；存在 session ID 时会打开 `codex://threads/SESSION_ID`。

正常退出失败时保留待重启状态，不执行强制终止。

## 常用命令

```bash
# 查看状态
codex-speed-patch status

# 自动检测并修复
codex-speed-patch repair

# macOS 官方签名应用会被预检拒绝
codex-speed-patch repair --allow-macos-production --restart=deferred-auto

# 临时目录中的 macOS 隔离副本允许完整备份和 ad-hoc 重签
codex-speed-patch repair --allow-macos-test --app "/private/tmp/Test.app"

# 恢复备份
codex-speed-patch restore --app "/Applications/ChatGPT.app"

# 安装或删除 hook
codex-speed-patch install-hook --binary /absolute/path/to/codex-speed-patch
codex-speed-patch remove-hook

# 重启相关
codex-speed-patch restart-diagnose
codex-speed-patch mark-restart --session-id SESSION_ID
codex-speed-patch schedule-restart --session-id SESSION_ID --delay 20s
codex-speed-patch clear-restart

# 查看版本
codex-speed-patch version
```

## 回滚

```bash
codex-speed-patch restore \
  --app "/Applications/ChatGPT.app"
```

manifest 记录完整 macOS `.app` 备份时，恢复流程会克隆官方备份，将补丁应用移动到暂存路径，启用官方应用，验证官方签名，最后删除暂存的补丁应用。

同时删除自动维护 hook：

```bash
codex-speed-patch remove-hook
```

## 平台说明

### macOS

- 提供 Apple Silicon 和 Intel 构建。
- 带有 TeamIdentifier 的官方签名应用在修改前被拒绝。
- `--allow-macos-production` 与 `--allow-macos-test` 仅控制路径范围；临时隔离副本仍需完整备份和重签。
- 已有完整 APFS 应用备份可用于恢复原签名版本。

### Windows

- 提供 AMD64 和 ARM64 构建。
- 当前用户对安装目录具有写权限时可应用补丁。
- `Program Files` 等系统目录可能需要管理员权限。
- 安装器更新可能覆盖补丁，`SessionStart` hook 会重新检测。

### Linux

- 提供 AMD64 和 ARM64 构建。
- 支持可写的 `.deb`、`.rpm` 或解压安装目录。
- 挂载状态的 AppImage 是只读的，不进行原位修改。
- Snap 和 Flatpak 属于不可变软件包，不进行原位修改。

## 从源码构建

要求 Go `1.24` 或更高版本。

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

[CHECKSUMS.txt](CHECKSUMS.txt) 仅记录 `v0.1.0` 的历史二进制校验值，不适用于本次仅含源码的 `v0.1.1` Release。

## 状态文件

默认状态目录：

```text
~/.codex/patches/speed-mode/
```

- `manifest.json`：版本、补丁规则、哈希、备份和重启状态。
- `patch.lock`：补丁并发锁。
- `restart.lock`：重启并发锁。
- `backups/`：各 Codex 版本的回滚副本。
- `bin/`：稳定安装的补丁器二进制。

## 已知限制

- Codex 前端结构变化后，旧语义规则可能返回 `unknown`。
- ad-hoc 签名不能替代 OpenAI Developer ID 身份；官方签名的 macOS 应用不支持原位补丁。
- 自动更新程序本身可能依赖官方签名。
- 多窗口或并行任务期间重启会影响整个 Codex 应用。
- API 服务端必须实际接受所选 `service_tier`；显示按钮不代表上游一定支持该档位。
