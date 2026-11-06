# Tunlite

> 面向开发者和 AI Agent 的可靠 SSH 连接管理器。

[English](README.md) · **简体中文**

[![Go CI](https://github.com/tunlite/tunlite/actions/workflows/go-ci.yml/badge.svg)](https://github.com/tunlite/tunlite/actions/workflows/go-ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Tunlite 把 OpenSSH 端口转发变成有名字的 Connection，可以在前台运行，也可以交给
操作系统服务独立托管。它只使用机器上已有的 `ssh`，不要求账号或服务端，也不创造
新的传输协议。

v1 预览版是单文件 Go 二进制。系统中没有管理全部连接的中心常驻进程：每个持久
Connection 直接属于 systemd 或 launchd，一条连接失败不会拖垮其他连接。

## 安装预览版

运行时唯一前提是系统 `ssh`。macOS 推荐使用
[`tunlite/tap`](https://github.com/tunlite/homebrew-tap)：Homebrew 下载并校验已发布的
预编译二进制，不安装 Go，也不编译源码：

```bash
brew install tunlite/tap/tunlite
```

已经添加过旧 Tap 时，先执行 `brew update` 再安装或升级。
`brew upgrade tunlite/tap/tunlite` 可升级为带匹配 Skill 的 revision 2 二进制包；
需要重新安装当前包时使用 `brew reinstall tunlite/tap/tunlite`。Homebrew 分发不等同于
Apple 开发者签名或公证，也不保证符合企业设备策略。

需要手工编译源码时，可选安装 Go 1.25 或更新版本，再从固定版本编译：

```bash
git clone --depth 1 --branch v1.0.0-preview.1 https://github.com/tunlite/tunlite.git tunlite-source
cd tunlite-source
go install -trimpath -ldflags='-s -w -X github.com/tunlite/tunlite/internal/cli.Version=1.0.0-preview.1' ./cmd/tunlite
```

使用固定 Tag 检出：远程 `go install ...@v1.0.0-preview.1` 当前会在历史上尚未采用 Go 的
`v0.10.3` Tag 校验查询处失败。
二进制写入 `GOBIN`，未设置时写入 `GOPATH` 第一项的 `bin` 目录（通常为 `~/go/bin`）；
请将该目录加入 `PATH`。源码编译通常避免浏览器下载带来的 quarantine，但不等同于
Apple 开发者签名或公证，也不保证符合企业设备策略。

仍然保留预编译下载渠道。对应 GitHub Release 和校验清单可用后执行：

```bash
# macOS / Linux；下载 v1.0.0-preview.1 并校验 SHA-256
curl -fsSL https://raw.githubusercontent.com/tunlite/tunlite/main/bootstrap.sh | sh
```

每个 Release 都包含独立压缩包和 `SHA256SUMS`。第一次安装或参与发布验收，请按照
[从零安装与验收手册](docs/INSTALL_AND_ACCEPTANCE.md)执行；当前版本边界和发布门禁见
[预览版指南](docs/V1_PREVIEW.md)。

新构建的归档还包含 `skills/use-tunlite/SKILL.md`，与二进制来自同一源码检出，
受归档 checksum 和 Attestation 覆盖。已公开的 `v1.0.0-preview.1` 归档保持不变，
其中没有 Skill：Homebrew 从同版本 Tag 下载并校验固定 SHA-256 的 Skill 资源；
bootstrap 安装二进制后只提示同版本 Skill 地址，不下载未校验的 Skill。

使用带 Skill 的归档时，bootstrap 将其安装到
`~/.local/share/tunlite/skills/use-tunlite`。自定义
`--install-dir <prefix>/bin`（或 `TUNLITE_INSTALL_DIR`）对应
`<prefix>/share/tunlite/skills/use-tunlite`；其他二进制目录也按其父目录下的 `share`
映射。脚本会打印实际本地目录；Homebrew 提示使用稳定的 `opt` 路径。可复制提示给
Agent：`请从目录 "<安装器打印的本地目录>" 安装并启用 use-tunlite Skill。`
旧版 bootstrap 归档则把打印的同版本 URL 交给 Agent。启用是可选步骤，由 Agent
按自身机制完成；安装器不会修改 Agent 目录。升级后请让 Agent 刷新已复制的 Skill；
只有 Agent 支持时才可使用稳定路径的符号链接。

拿到手动下载的原生二进制时，直接复制到 PATH：

```bash
chmod +x ./tunlite
mkdir -p "$HOME/.local/bin"
install -m 0755 ./tunlite "$HOME/.local/bin/tunlite"
"$HOME/.local/bin/tunlite" version
```

安装、升级和卸载由发行渠道负责。Homebrew 使用
`brew update && brew upgrade tunlite/tap/tunlite` 升级、`brew uninstall tunlite/tap/tunlite`
卸载；源码安装升级时在新目录检出新 Tag，并同步更新安装命令的注入版本，卸载时只删除所选安装
目录中的二进制。切换渠道后用 `type -a tunlite` 和 `tunlite version --json` 检查：
PATH 中靠前的旧副本可能遮住新版本。请选择一个渠道；安装不会迁移旧开发版服务记录。
核心 CLI 不提供安装、升级或卸载命令，
也不创建 `tun` 别名。替换 CLI 文件不会停止已经交给系统运行的 SSH Connection。
V1 预览版暂不提供 Shell 自动补全，也不识别或修改 Agent 私有目录；需要让 Agent 使用
Tunlite 时，请让 Agent 安装官方
[`use-tunlite` Skill 包](skill/use-tunlite/)。

## 快速开始

```bash
# 使用熟悉的 OpenSSH 语法声明本地、远端或 SOCKS 转发。
tunlite add db -L 5432:localhost:5432 deploy@app01
tunlite add preview -R 9000:localhost:3000 deploy@app01
tunlite add socks -D 1080 deploy@app01

# 在当前终端前台运行。
tunlite run db

# 只在本次登录期间后台运行，并在失败后自动恢复。
tunlite start db
tunlite stop db

# 跨登录长期运行，并立即启动。
tunlite enable db
tunlite status db
tunlite logs db --lines 100

# macOS 可显式迁移为系统范围，不依赖用户登录；该操作需要管理员授权。
tunlite enable db --scope system

# 认证尚未配置时，使用系统 OpenSSH 工具完成密钥配置，再检查连接。
tunlite check db

# 异常漂移必须明确选择事实来源。
tunlite resolve db --use tunlite --dry-run
tunlite resolve db --use system
```

SSH 永久作为默认 Driver，通常不必写 `--driver ssh`。非默认端口使用 `-p`，私钥使用
`-i`，跳板机使用 `-J`，额外安全选项使用白名单内的 `-o Key=Value`。目标只写一个明确的
`host` 或 `user@host`，端口不要拼到目标后面。

Preview 的用户名语法有意保持严格：只接受 ASCII 字母、数字、`.`、`_`、`-`。UPN 或多个
`@`、包含 `+` 的用户名、`DOMAIN\user` 都不支持，也没有独立 `-l` 参数可绕过。必须使用
这些形式的账号暂时超出本 Preview 的 SSH Target 表达范围。

`set` 只修改本次给出的字段。需要删除可选 SSH 字段时，按需重复同一个
清除操作：

```bash
tunlite set db --clear identity --clear jump --clear options
```

同时支持 `--clear jump` 和 `--clear=jump`。可清除字段只有 `identity`、`jump` 和
`options`，不提供单字段别名。未知字段、重复清除，或在同一条
命令中同时设置与清除同一字段，都会被拒绝。任何 `-L`/`-R`/`-D` 或 `-o`
仍会替换对应的完整列表。

Tunlite 的 SSH 执行与检查固定使用 `ssh -F none`，不会继承 `~/.ssh/config` 中的 alias、命令、隐藏
Forward 或连接复用。请写明真实主机和安全参数。未来可以提供 SSH Config 安全导入，
但只会生成显式快照，不会让运行行为脱离 Connection 声明。

每个 Connection 都是独立服务：

```text
tunlite CLI -> SSH Driver -> ExecutionPlan -> systemd/launchd -> ssh
```

生成的服务直接执行 `ssh`。安装完成后，即使 Tunlite 二进制暂时丢失，已经运行的连接
也不会因此退出；只是在恢复 CLI 前不能继续管理。

### 服务范围

每个 Connection 声明永远只有一份。Scope（服务范围）只是当前机器上的运行归属，保存在
Service Record 和原生服务产物中，不会写入可导出、可迁移的 Connection。

- `tunlite enable <name>` 对新服务默认使用 `user`，已有服务未显式传 Scope 时保留原范围。
- `tunlite enable <name> --scope user|system` 显式选择或迁移范围，不增加单独的 `migrate` 命令。
- `user` 不需要管理员权限。macOS 的 `system` 对应 launchd LaunchDaemon，需要管理员
  授权；Linux 当前只支持 systemd 用户服务，system Scope 会明确返回不支持。
- 在交互式终端中，Tunlite 会先说明权限影响；整批只读检查全部通过后、第一次写入前，
  只执行一次 `sudo -v`。密码提示和读取完全由 `sudo` 负责，Tunlite 不读取也不保存密码。
  没有可交互终端时会快速返回 `privilege_required`，并提示如何重试。
- `--non-interactive` 绝不提示或调用交互授权。自动化使用 system Scope 时必须预置授权，
  供平台层精确的 `sudo -n -- ...` 命令使用；否则稳定返回 `privilege_required`。
- `--dry-run` 只做只读规划，不请求授权，也不写入原生服务。

人工终端迁移会显示来源、目标、权限影响和执行进度；`--json` 的 stdout 始终保持纯机器输出，
并逐项返回稳定结果。

## Agent 契约

自动化调用统一使用 `--json --non-interactive`，并查询运行时能力，不解析人类文本：

```bash
tunlite capabilities --json --non-interactive
tunlite schema cli --json --non-interactive
tunlite add db -L 5432:localhost:5432 deploy@app01 --dry-run --json --non-interactive
```

自动化在执行动作前必须检查 `capabilities` 返回的整数 `protocolVersion`。原始 Schema
和导出的 Connection 配置是独立文档，分别使用 `$schema` 与 `configVersion`，不套 CLI
响应 Envelope。需要在 stdout 读取原始 JSON 时，使用 `schema --output -` 或
`export --output -`。

状态包含 `serviceState`、`mode`、`processState`、`health` 和
`consistencyState` 五个相互独立的观测维度；另有 `scope: user|system` 表示本机服务归属。
批量操作逐项返回结果，部分失败使用退出码 `5`。公开监听和 Publish 在声明写入及
`--dry-run` 中返回结构化风险提示；V1 不维护独立的批准状态。

仓库独立提供的 [`use-tunlite` Agent Skill](skill/use-tunlite/SKILL.md) 会在测试中与
CLI 的命令、退出码、状态和 Schema 契约校验一致；安装和卸载由 Agent 自己负责。

## 配置

所有连接使用通用 `Connection` Envelope，SSH 专属字段只存在于内置
`io.tunlite.ssh` Driver 的配置中。这样为未来 Driver 保留增量扩展能力，同时不在 v1
提前冻结未经真实使用验证的公共插件协议。

```bash
tunlite export --output tunlite.json
tunlite validate tunlite.json
tunlite import tunlite.json --dry-run
```

V1 配置写入 `~/.config/tunlite/connections.json`。`import` 只接受文档定义的 V1
`ConnectionList` 格式；不支持的版本和未知字段会被严格拒绝。

## 预览版范围

- 内置 SSH `-L`、`-R`、`-D`，支持多 Forward、跳板机、Identity File、严格的
  OpenSSH `Key=Value` Option、检查、诊断、日志和漂移修复。
- 单文件原生二进制；安装、升级和卸载由发行渠道负责。
- macOS 12+ 支持 launchd 用户与系统范围；Linux 使用 systemd 用户服务，尚未实现 Linux
  system Scope。
- 首个预览版不发布 Windows 产物。每 Connection Task Scheduler Adapter 和 Windows
  真机验证完整通过后，Windows 才进入正式支持矩阵。仓库仍保持 Windows 全仓编译，
  Monitor 会在尝试进入终端界面前明确返回平台不支持。
- 外部 Driver 和插件安装延后到真实非 SSH 需求得到验证之后。

当前硬件证据有意窄于构建矩阵。2026-08-31，Intel macOS 已通过最终 Universal 2 构建
校验、LaunchAgent Release E2E，以及隔离 LaunchDaemon 的
USER→SYSTEM→USER→SYSTEM、真实 `-L` 流量、system `set`、无 sudo restart、
disable/remove 与精确清理；Ubuntu 24.04 PID 1 systemd Docker E2E 也已通过。
Apple Silicon 真机和非容器 Linux 主机仍未验证，它们是稳定 `v1.0.0` 而非 Preview.1
之前的硬门禁。

[产品设计](docs/PROJECT_DIRECTION.md)和[架构设计](docs/ARCHITECTURE.md)是 v1 以及后续
工作的两份并列主文档。

## 开发

```bash
go test ./...
go test -race ./...
go vet ./...
scripts/package-release.sh v1.0.0-preview.1
scripts/test-linux-docker-e2e.sh v1.0.0-preview.1
```

真实 OpenSSH 与原生服务生命周期测试为显式开启，入口位于 `internal/cli` 和
`internal/service` 对应测试文件。参与开发前请同时阅读 [贡献指南](CONTRIBUTING.md)和
[Git 开发与仓库治理规范](docs/GIT_GOVERNANCE.md)。

MIT License。
