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

# Windows PowerShell（首个预览版支持前台 CLI）
irm https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.ps1 | iex
```

每个 Release 都包含独立压缩包和 `SHA256SUMS`。手动安装、平台能力、构建证明和
0.10.3 迁移步骤见[预览版指南](docs/V1_PREVIEW.md)。

## 快速开始

```bash
# 声明本地、远端或 SOCKS 转发；同类参数可以重复。
tunlite add db --to deploy@app01 -L 5432:localhost:5432
tunlite add preview --to deploy@app01 -R 9000:localhost:3000
tunlite add socks --to deploy@app01 -D 1080

# 在当前终端前台运行。
tunlite run db

# 或让原生服务持续运行，并在失败后自动重启。
tunlite enable db
tunlite status db
tunlite logs db --lines 100

# 检测并修复声明与原生服务之间的漂移。
tunlite sync db --dry-run
tunlite sync db
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
tunlite capabilities --json
tunlite schema cli
tunlite add db --to deploy@app01 -L 5432:localhost:5432 --dry-run --json --non-interactive
```

状态同时包含 `desiredState`、`processState`、`health` 和 `syncState` 四个维度。
批量操作逐项返回结果，部分失败使用退出码 `6`。公开监听和持久 Publish 计划必须获得
与精确计划绑定的显式批准。

随仓库提供的 [SSH Agent Skill](skill/ssh-tunnel/SKILL.md) 会在测试中与 CLI 的命令、
退出码、状态和 Schema 契约校验一致。

## 配置与迁移

所有连接使用通用 `Connection` Envelope，SSH 专属字段只存在于内置
`io.tunlite.ssh` Driver 的配置中。这样为未来 Driver 保留增量扩展能力，同时不在 v1
提前冻结未经真实使用验证的公共插件协议。

```bash
tunlite export --output tunlite.json
tunlite validate tunlite.json
tunlite import tunlite.json --dry-run

# 先停止并卸载 0.10.3 的旧服务，再执行：
tunlite migrate --dry-run --json --non-interactive
tunlite migrate --json --non-interactive
```

迁移会保存按内容摘要命名的字节级备份。迁移后只要 v1 配置被修改，
`migrate --rollback` 就会拒绝覆盖。

## 预览版范围

- 内置 SSH `-L`、`-R`、`-D`，支持多 Forward、跳板机、Identity File、严格的
  OpenSSH `Key=Value` Option、检查、诊断、日志和漂移修复。
- macOS 使用 launchd、Linux 使用 systemd 用户服务持久运行。
- Windows amd64 支持配置和前台 CLI；首个预览版不提供持久服务。
- 外部 Driver 和插件安装延后到真实非 SSH 需求得到验证之后。

[项目方向](docs/PROJECT_DIRECTION.md)是 v1 以及后续工作的唯一产品和架构指引。

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
