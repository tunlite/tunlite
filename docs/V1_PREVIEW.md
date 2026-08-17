# Tunlite v1 预览版指南

本文说明 `v1.0.0-preview.N` 的安装、支持范围、0.10.3 迁移、回滚和发布验收。
产品与架构决策仍以 [PROJECT_DIRECTION.md](PROJECT_DIRECTION.md) 为唯一事实来源。

## 支持矩阵

| 平台 | 架构 | 前台与配置 CLI | 持久 Connection | 当前验证等级 |
|---|---|---:|---:|---|
| macOS 12+ | arm64、amd64 | 支持 | launchd，支持 | 自动测试、plist 原生校验、真实生命周期测试 |
| Linux | arm64、amd64 | 支持 | systemd 用户服务，支持 | 自动测试与交叉编译；发布前补真实发行版验收 |
| Windows | amd64 | 支持 | 不支持 | 自动测试与交叉编译 |

系统 `ssh` 必须位于 PATH，或通过 `TUNLITE_SSH` 指定绝对路径。Linux 持久模式需要
可用的 systemd 用户会话；容器、精简发行版和没有用户会话总线的环境应使用
`tunlite run`，或由已有的进程管理器托管它。

首个预览版不承诺 Windows Task Scheduler。CLI 会通过 `capabilities --json` 如实返回
当前平台是否具备持久服务，不能以文档假装平台能力一致。

## 项目声明的预览限制

当前 `--config` 指向一份完整的 Connection 声明文件，还没有实现“可提交的项目声明”与
“不入库的本机 override”分层，也没有可迁移的 Identity 引用机制。`identityFile` 仍可能
保存本机绝对路径，端口和其他设置也可能只适用于当前机器。

因此，导出或通过 `--config` 创建的文件在提交 Git 前必须人工检查；包含绝对私钥路径、
机器专用端口或其他本机信息时，不应直接提交。导入项目声明不会安装插件、启动 Connection
或取得高风险操作批准。配置分层属于后续预览能力，当前版本不能宣称已经完成 GitOps。

## 安装与校验

macOS 或 Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.sh | sh
tunlite version --json
tunlite capabilities --json
```

安装脚本下载与平台匹配的 Release 压缩包，先使用同一 Release 中的 `SHA256SUMS`
校验，再写入 `${HOME}/.local/bin/tunlite`。可以通过环境变量覆盖：

```bash
TUNLITE_VERSION=v1.0.0-preview.2 \
TUNLITE_INSTALL_DIR=/usr/local/bin \
sh bootstrap.sh
```

Windows PowerShell 默认安装到 `%LOCALAPPDATA%\Programs\Tunlite\tunlite.exe`：

```powershell
irm https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.ps1 | iex
```

也可以手动下载压缩包和 `SHA256SUMS`。任何 checksum 不一致都必须停止安装，不能用
“重新下载后跳过校验”的方式继续。

正式 Release 还应提供 GitHub Artifact Attestation。启用发布工作流后可验证：

```bash
gh attestation verify tunlite_1.0.0-preview.1_darwin_arm64.tar.gz \
  --repo yuanyuanzijin/tunlite
```

Checksum 只证明下载内容和发布清单一致；Attestation 才把产物绑定到仓库身份、提交和
GitHub Actions 构建。发布工作流尚未得到外部写入授权时，不得虚构已存在的证明。

## 从 0.10.3 迁移

0.10.3 的中心服务无法读取 v1 配置。旧进程如果继续运行，可能把 v1 文件理解为空配置，
因此顺序不能颠倒。

### 1. 用 0.10.3 退出旧运行时

先确认当前执行的仍是 0.10.3，然后卸载旧登录服务并停止旧后台进程：

```bash
tunlite version
tunlite uninstall service --json
tunlite daemon stop --json
```

不要先执行 `disable all`，否则会把旧配置中的 `enabled` 意图改成 false。迁移器本身不会
自动启动这些连接，而是把原来的启用意图作为结构化建议返回。

### 2. 安装 v1 并只做规划

```bash
tunlite version --json
tunlite migrate --dry-run --json --non-interactive
```

检查返回的 Connection 数量和 `conditions[]`。重点处理：

- `legacy_enabled_not_started`：旧连接曾启用，但迁移不会启动它。
- `legacy_settings_not_migrated`：旧版全局重试、通知和后台设置不属于 v1。
- `legacy_auto_setup_key_not_migrated`：v1 不自动向远端写入 SSH 公钥。

迁移器兼容 0.10.3 曾保存过的 `sshOptions` 两种历史形式：`["Compression=yes"]`
和 `["-o", "Compression=yes"]`，统一写成严格的 `Key=Value`。无法安全表达的任意参数
会导致整个迁移失败，原文件保持不变。

### 3. 应用、校验、逐条恢复

```bash
tunlite migrate --json --non-interactive
tunlite validate --json --non-interactive
tunlite doctor --offline --json --non-interactive
tunlite status --json --non-interactive
```

确认配置正确后，逐条执行 `check` 和 `enable --dry-run`。公开监听或远端转发先取得明确
批准，再对精确计划使用 `--approve`。不要根据迁移返回的 action 自动启用全部连接。

## 迁移的备份与回滚

应用迁移前会完成三件事：

1. 在原配置旁保存 `config.json.v0-sha256-<摘要>.bak`，内容与旧文件字节级一致。
2. 在本机状态目录保存前后 SHA-256、原路径、备份路径和迁移状态。
3. 通过同一配置锁原子替换为 v1 声明。

重复运行迁移不会覆盖旧备份。若进程在替换前后中断，下次运行会根据摘要完成或恢复迁移
日志，而不是猜测状态。

需要人工回滚时，先停止所有 v1 Connection：

```bash
tunlite disable all --json --non-interactive
tunlite migrate --rollback --dry-run --json --non-interactive
tunlite migrate --rollback --json --non-interactive
```

回滚只在当前文件仍与“刚迁移完成的 v1 文件”摘要一致时执行。一旦用户通过 `add`、
`set`、`import` 等方式修改过 v1 配置，就返回 `migration_drifted`，绝不覆盖新数据。

回滚只恢复配置，不重新安装或启动 0.10.3。需要恢复旧产品时，必须另外安装固定版本的
0.10.3，并由用户明确决定是否恢复旧服务。

## 预览版发布门禁

发布候选必须全部通过：

```bash
go test ./...
go test -race ./...
go vet ./...
TUNLITE_REAL_SSH=1 go test ./internal/cli -run TestRealOpenSSHAllForwardTypes -count=1 -v
TUNLITE_REAL_SERVICE=1 go test ./internal/service -run TestRealNativeServiceLifecycle -count=1 -v
scripts/package-release.sh v1.0.0-preview.1
```

发布产物统一使用 Go 1.26.x 构建；`go.mod` 中的 1.22 仅表示源码采用的最低语言版本。
不得用 Go 1.22 生成正式 macOS 产物，因为其链接结果会被当前 macOS dyld 拒绝。

还必须在 Linux 真机完成 systemd 用户服务的 `enable → status → restart → sync → disable`
验收，逐个平台解压产物并确认嵌入版本正确，核对 `SHA256SUMS`。没有完成的项目必须在
Release Notes 和支持矩阵中明确写出，不能用“预计支持”替代事实。
