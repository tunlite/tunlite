# Tunlite

> 面向开发者和 AI Agent 的可靠 SSH 连接管理器。

[English](README.md) · **简体中文**

[![Go CI](https://github.com/yuanyuanzijin/tunlite/actions/workflows/go-ci.yml/badge.svg)](https://github.com/yuanyuanzijin/tunlite/actions/workflows/go-ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Tunlite 把 OpenSSH 端口转发变成有名字的 Connection，可以在前台运行，也可以交给
操作系统服务独立托管。它只使用机器上已有的 `ssh`，不要求账号或服务端，也不创造
新的传输协议。

v1 预览版是单文件 Go 二进制。系统中没有管理全部连接的中心常驻进程：每个持久
Connection 直接属于 systemd 或 launchd，一条连接失败不会拖垮其他连接。

## 安装预览版

运行时唯一前提是系统 `ssh`。

```bash
# macOS / Linux；下载 v1.0.0-preview.1 并校验 SHA-256
curl -fsSL https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.sh | sh
```

每个 Release 都包含独立压缩包和 `SHA256SUMS`。第一次安装或参与发布验收，请按照
[从零安装与验收手册](docs/INSTALL_AND_ACCEPTANCE.md)执行；现有 0.x 用户必须先阅读
[0.x 升级到 Go v1](docs/UPGRADE_FROM_0_X.md)，版本边界见[预览版指南](docs/V1_PREVIEW.md)。

拿到手动下载的原生二进制时，直接复制到 PATH：

```bash
chmod +x ./tunlite
mkdir -p "$HOME/.local/bin"
install -m 0755 ./tunlite "$HOME/.local/bin/tunlite"
"$HOME/.local/bin/tunlite" version
```

安装、升级和卸载由 Release、包管理器或未来 Desktop 负责，核心 CLI 不提供对应命令，
也不创建 `tun` 别名。替换 CLI 文件不会停止已经交给系统运行的 SSH Connection。
V1 预览版暂不提供 Shell 自动补全，也不识别或修改 Agent 私有目录；需要让 Agent 使用
Tunlite 时，请让 Agent 安装官方
[`use-tunlite` Skill 包](skill/use-tunlite/)。

## 快速开始

```bash
# 声明本地、远端或 SOCKS 转发；同类参数可以重复。
tunlite add db --to deploy@app01 -L 5432:localhost:5432
tunlite add preview --to deploy@app01 -R 9000:localhost:3000
tunlite add socks --to deploy@app01 -D 1080

# 在当前终端前台运行。
tunlite run db

# 只在本次登录期间后台运行，并在失败后自动恢复。
tunlite start db
tunlite stop db

# 跨登录长期运行，并立即启动。
tunlite enable db
tunlite status db
tunlite logs db --lines 100

# 认证尚未配置时，使用系统 OpenSSH 工具完成密钥配置，再检查连接。
tunlite check db

# 异常漂移必须明确选择事实来源。
tunlite resolve db --use tunlite --dry-run
tunlite resolve db --use system
```

每个 Connection 都是独立服务：

```text
tunlite CLI -> SSH Driver -> ExecutionPlan -> systemd/launchd -> ssh
```

生成的服务直接执行 `ssh`。安装完成后，即使 Tunlite 二进制暂时丢失，已经运行的连接
也不会因此退出；只是在恢复 CLI 前不能继续管理。

## Agent 契约

自动化调用统一使用 `--json --non-interactive`，并查询运行时能力，不解析人类文本：

```bash
tunlite capabilities --json --non-interactive
tunlite schema cli --json --non-interactive
tunlite add db --to deploy@app01 -L 5432:localhost:5432 --dry-run --json --non-interactive
```

自动化在执行动作前必须检查 `capabilities` 返回的整数 `protocolVersion`。原始 Schema
和导出的 Connection 配置是独立文档，分别使用 `$schema` 与 `configVersion`，不套 CLI
响应 Envelope。需要在 stdout 读取原始 JSON 时，使用 `schema --output -` 或
`export --output -`。

状态同时包含 `serviceState`、`mode`、`processState`、`health` 和
`consistencyState` 五个维度。
批量操作逐项返回结果，部分失败使用退出码 `5`。公开监听和 Publish 在声明写入及
`--dry-run` 中返回结构化风险提示；V1 不维护独立的批准状态。

仓库独立提供的 [`use-tunlite` Agent Skill](skill/use-tunlite/SKILL.md) 会在测试中与
CLI 的命令、退出码、状态和 Schema 契约校验一致；安装和卸载由 Agent 自己负责。

## 配置与迁移

所有连接使用通用 `Connection` Envelope，SSH 专属字段只存在于内置
`io.tunlite.ssh` Driver 的配置中。这样为未来 Driver 保留增量扩展能力，同时不在 v1
提前冻结未经真实使用验证的公共插件协议。

```bash
tunlite export --output tunlite.json
tunlite validate tunlite.json
tunlite import tunlite.json --dry-run

# 0.10.3 配置显式导入；旧文件和旧隧道不会被修改。
tunlite import ~/.config/tunlite/config.json --dry-run --json --non-interactive
tunlite import ~/.config/tunlite/config.json --json --non-interactive
```

V1 配置写入 `~/.config/tunlite/connections.json`。导入后的 Connection 全部停止，用户
按照升级文档逐条切换，避免新旧进程争用同一端口。

## 预览版范围

- 内置 SSH `-L`、`-R`、`-D`，支持多 Forward、跳板机、Identity File、严格的
  OpenSSH `Key=Value` Option、检查、诊断、日志和漂移修复。
- 单文件原生二进制；安装、升级和卸载由发行渠道负责。
- macOS 12+ 使用 launchd、Linux 使用 systemd 用户服务持久运行。
- 首个预览版不发布 Windows 产物。每 Connection Task Scheduler Adapter、迁移和
  Windows 真机验证完整通过后，Windows 才进入正式支持矩阵。
- 外部 Driver 和插件安装延后到真实非 SSH 需求得到验证之后。

[产品设计](docs/PROJECT_DIRECTION.md)和[架构设计](docs/ARCHITECTURE.md)是 v1 以及后续
工作的两份并列主文档。

## 开发

```bash
go test ./...
go test -race ./...
go vet ./...
scripts/package-release.sh v1.0.0-preview.1
```

真实 OpenSSH 与原生服务生命周期测试为显式开启，入口位于 `internal/cli` 和
`internal/service` 对应测试文件。

MIT License。
