# Codex API Speed Mode 补丁

[English](README_EN.md)

这是一个版本感知的 Codex Desktop 补丁器，用于在 API/custom model provider 模式下显示 Codex 原生 Speed 速度选择器。

补丁器复用 Codex 已有的模型目录、`service_tiers`、`service_tier` 配置写入和请求传递流程，补齐 API 登录下 Speed 控件、保存档位读取及续跑/派生线程的权限路径。

## 当前状态

- 补丁器版本：`0.1.2`（未发布）
- macOS 实测日期：`2026-10-13`
- 已测试 Codex Desktop：`26.1007.30912`

本版隔离副本已通过以下检查：

- Codex CLI/app-server 初始化成功。
- 主窗口完成 `ready-to-show`。
- 原生 Speed 保存与读取函数通过 API 登录、档位映射和策略关闭的运行时对照测试。
- 本地假上游捕获首轮、推理续跑和桌面派生线程的 `service_tier: priority`。
- `codesign --verify --deep --strict` 通过。
- 完整回滚恢复原始 ASAR 与官方签名。

`0.1.2` 补丁已通过完整备份和整包替换部署到正式应用，用户确认 Speed/Fast 控件出现且可切换。CLI 的自动整包替换流程另在隔离副本中验证；自主子代理工具、真实上游、Keychain、推送和实际官方更新安装仍待端到端验证。

## 0.1.2 修复（未发布）

- 补丁规则升级为 `speed-mode-api-v2`：旧版仅打开控件的补丁显示为 `partial`，升级时同时修复保存档位、队列和续跑/派生线程的读取路径。Fast 使用模型目录定义的真实 ID（例如 `priority`），不强制重命名为 `fast`。
- 兼容 `26.1007.30912` 的独立 Fast/Ultrafast 权限，保留明确关闭档位的策略检查。
- Sparkle 的 Autoupdate、Updater、XPC 服务和 Framework 从内到外重签，与宿主保持一致，修复混合 Team ID 引发的安装连接 4005。保留更新公钥和更新包验证；仅移除不适用于 ad-hoc 的开发者身份 entitlements。
- 旧 manifest 的备份版本与已安装版本不同会停止恢复，避免将官方更新回退成旧版应用。
- 已通过隔离包启动、原生组件请求函数对照、ASAR 完整性及更新签名链检查；本地假上游已捕获 Speed 保存后首轮、推理续跑及桌面派生线程均携带 `service_tier: priority`。自主子代理工具与官方更新安装的端到端复测仍待完成。
- 手动执行 `repair --allow-macos-production` 时，CLI 自动备份、生成补丁包、调度一次性整包替换并重启验证；失败时恢复原应用和原 manifest。Hook 沿用原有处理流程，不触发整包部署。

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
2. 在 JavaScript 资源中核对控件、请求权限及保存档位读取规则；目标缺失或不唯一时终止。
3. 保留原始数据区（包括共享内容范围），追加变更资源并重建它们的索引。
4. 重算每个变更资源的 SHA-256 和 ASAR 分块哈希。
5. 更新 Header JSON 的偏移、大小和完整性记录，保留链接、unpacked 与未知元数据。
6. macOS 上将 Header JSON SHA-256 写入 `ElectronAsarIntegrity`，并同步 Framework 的 `__asar_integrity` 摘要。
7. 按 Electron Helper/Framework、Sparkle 辅助程序/Framework、外层应用的顺序重新签名，验证资源签名和更新链身份。

## 安全机制

- 控件与请求权限规则各匹配一次，全部保存档位读取函数均被覆盖。
- 无目标或多个候选目标时终止修改。
- 仅更新匹配规则所在资源的索引，其余原始数据保持完整。
- 修改前验证文件哈希和全部分块哈希。
- 修改后重新打开 ASAR，并确认状态为 `patched`。
- 每个 Codex 版本使用独立备份目录。
- macOS 上在写入前检查签名身份和路径；正式应用通过完整验证后的补丁包替换，官方签名预检保留在隔离修补流程中。
- 对允许补丁的 macOS 应用，修改前使用 APFS clone 备份整个 `.app`。
- 未知版本不会进行猜测性修改。
- 自动重启只请求正常退出，不使用强制终止。
- 重启 attempt ID 用于防止循环重启。

## macOS 重要说明

补丁包使用本地 ad-hoc 签名；完整官方备份保留 OpenAI 原始 Developer ID。正式部署会在临时目录中调用现有补丁和签名逻辑，验证后将整个应用包切换到原来的正式路径。

手动部署正式应用：

```bash
codex-speed-patch repair --allow-macos-production --app "/Applications/ChatGPT.app"
```

命令返回 `status: scheduled` 表示已调度；随后独立的一次性任务请求选定进程退出，等待 Helper 退出，替换应用并重启。`status` 输出中的 `deployment.status` 为 `deployed` 时表示包验证与启动检查已完成；`failed` 时可查看 `deployment.error` 和 `rolled_back`。应用在准备后被官方更新时会取消替换，保留新版本。

正式整包部署必需退出与启动应用，`--restart` 仅控制原有原位补丁流程。已经完整补丁且签名链正常的应用返回 `changed: false`，不重复替换或重启。备份和部署日志保存在所选 `--codex-home` 下，用户配置与认证不被复制或改写。

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

GitHub 的 `v0.1.1` Release 仅含源码。此前六平台 `0.1.2` 构建保留在 `dist/v0.1.2/`；本轮包含自动整包替换的 Apple Silicon 构建位于 `dist/v0.1.2-direct-replace/`，使用同目录的 `SHA256SUMS.txt` 核对。

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
chmod +x dist/v0.1.2-direct-replace/codex-speed-patch-darwin-arm64
```

### 2. 检查状态

```bash
dist/v0.1.2-direct-replace/codex-speed-patch-darwin-arm64 status \
  --app "/Applications/ChatGPT.app"
```

状态值：

- `original`：尚未应用补丁。
- `patched`：补丁已生效。
- `partial`：仅有旧版部分补丁，完整请求规则尚未覆盖。
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

测试模式仅允许临时目录中的副本；即使副本保留官方签名，也会先完整备份，再执行 ad-hoc 重签。

整包替换测试可以同时传入两个范围参数及独立状态目录：

```bash
codex-speed-patch repair --allow-macos-production --allow-macos-test \
  --app "/private/tmp/Codex-Speed-Test.app" --codex-home "/private/tmp/Codex-Speed-Test-State"
```

这会完整替换指定测试包，并用独立浏览器资料和空认证配置启动；不会复用正式应用的会话。

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

# macOS 正式应用：备份、整包替换并重启
codex-speed-patch repair --allow-macos-production

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

manifest 记录完整 macOS `.app` 备份且版本与安装版本一致时，恢复流程会克隆官方备份，将补丁应用移动到暂存路径，启用官方应用，验证官方签名，最后删除暂存的补丁应用。

同时删除自动维护 hook：

```bash
codex-speed-patch remove-hook
```

## 平台说明

### macOS

- 提供 Apple Silicon 和 Intel 构建。
- `--allow-macos-production` 为手动命令启用正式应用的整包替换流程。
- 单独使用 `--allow-macos-test` 在临时副本内修补；同时传入两个参数可测试整包替换。
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

请求与新版权限的运行时回归测试还需要 Node.js；缺少 Node.js 时这些测试会明确跳过。

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

本地 `0.1.2` 产物的校验值位于 `dist/v0.1.2/SHA256SUMS.txt`。[CHECKSUMS.txt](CHECKSUMS.txt) 仅记录 `v0.1.0` 的历史二进制校验值。

## 状态文件

默认状态目录：

```text
~/.codex/patches/speed-mode/
```

- `manifest.json`：版本、补丁规则、哈希、备份和重启状态。
- `deployment.json`：最近一次整包替换的状态、错误及回滚结果。
- `deployments/`：各部署任务的原 manifest、一次性助手和日志。
- `patch.lock`：补丁并发锁。
- `restart.lock`：重启并发锁。
- `backups/`：各 Codex 版本的回滚副本。
- `bin/`：稳定安装的补丁器二进制。

## 已知限制

- Codex 前端结构变化后，旧语义规则可能返回 `unknown`。
- 整包替换后的应用使用 ad-hoc 签名，依赖官方 Developer ID 的能力仍需验证。
- 自动更新程序本身可能依赖官方签名。
- 多窗口或并行任务期间重启会影响整个 Codex 应用。
- API 服务端必须实际接受所选 `service_tier`；显示按钮不代表上游一定支持该档位。
