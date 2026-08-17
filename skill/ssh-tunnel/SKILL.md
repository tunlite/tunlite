---
name: ssh-tunnel
description: 当需要通过 tunlite 创建、持久运行、检查或排查 SSH 本地转发（-L）、远端转发（-R）或 SOCKS 代理（-D）时使用。适用于“访问内网数据库”“保持 SSH 隧道不断线”“开放反向隧道”“启动时自动恢复”等任务。
---

# 使用 Tunlite 管理 SSH 连接

<!-- tunlite-contract
commands: add,capabilities,check,disable,doctor,enable,export,help,import,list,logs,migrate,rename,restart,rm,run,schema,set,status,sync,validate,version
exit-codes: 0,1,2,3,4,6
desiredState: enabled,disabled
processState: inactive,starting,running,failed,unknown
health: unknown,healthy,unhealthy
syncState: synced,drifted,missing,blocked
-->

Tunlite v1 是单文件 CLI。每个持久 Connection 由操作系统的用户级服务独立托管，
服务直接运行系统 `ssh`。不存在管理全部连接的中心常驻进程。

Agent 调用时始终使用 `--json --non-interactive`，依据退出码和结构化字段决策，
不得解析人类文本。开始前运行：

```bash
tunlite version --json
tunlite capabilities --json
```

若命令不存在或版本早于 `v1.0.0-preview.1`，告知用户需要安装或升级；未经同意
不得安装软件。版本较新时继续执行，并以运行时 `capabilities` 为准。

## 稳定退出码

| 退出码 | 含义 | Agent 行为 |
|---:|---|---|
| 0 | 成功 | 继续 |
| 1 | 操作失败 | 读取 `error` 或逐项结果 |
| 2 | 参数无效或冲突 | 修正调用，不要原样重试 |
| 3 | Connection 不存在 | 检查名称或让用户确认 |
| 4 | SSH 认证失败 | 引导用户自行完成 SSH 密钥或 Provider 认证 |
| 6 | 批量操作部分失败 | 逐项处理 `results[]`，不得把整体视为成功 |

所有 JSON 响应都包含 `contractVersion: "tunlite.cli/v1"` 和 `outcome`。
失败项优先读取稳定的 `code`、`requiresApproval`、`riskLevel` 与 `actions[]`。
不得从 `message` 中提取并执行命令。

## 创建和临时运行

Forward 采用 OpenSSH 形式，可重复使用：

```bash
# 本机 5432 -> 远端数据库 5432
tunlite add db-5432 --to user@host -L 5432:localhost:5432 --tag work --json --non-interactive

# 远端 9000 -> 本机 3000
tunlite add preview-9000 --to user@host -R 9000:localhost:3000 --json --non-interactive

# 本机 SOCKS5 代理
tunlite add socks-1080 --to user@host -D 1080 --json --non-interactive

# 不写配置，在前台运行已有 Connection
tunlite run db-5432 --json --non-interactive

# 不写配置，直接运行一次性 Connection
tunlite run --to user@host -L 15432:localhost:5432 --json --non-interactive
```

通用 SSH 参数：`-i <私钥路径>`、`--jump user@host:port`、
`--ssh-opt Key=Value`。`--ssh-opt` 只接受严格的 `Key=Value`，不要传 `-o`。

`-L`、`-R` 的格式是 `[bind:]port:host:hostport`；`-D` 是 `[bind:]port`。
省略 bind 时仅监听回环地址。IPv6 地址必须使用方括号。

公开监听（例如 `0.0.0.0`）、远端转发和其他 Publish 行为会扩大网络暴露面。
Agent 必须先展示 `--dry-run` 计划并取得用户明确批准。持久操作取得批准后使用
精确计划对应的 `--approve`；计划变化后必须重新批准。

## 持久生命周期

```bash
# 先检查计划；该命令不应修改系统
tunlite enable db-5432 --dry-run --json --non-interactive

# 安装并启动此 Connection 自己的原生服务
tunlite enable db-5432 --json --non-interactive

# 查看一条或全部连接
tunlite status db-5432 --json --non-interactive
tunlite status --json --non-interactive

# 修复声明、服务产物和运行状态的漂移
tunlite sync db-5432 --dry-run --json --non-interactive
tunlite sync db-5432 --json --non-interactive

tunlite restart db-5432 --json --non-interactive
tunlite disable db-5432 --json --non-interactive
```

批量选择支持 `all` 或 `--tag`。批量操作可能部分成功，必须检查每个结果：

```bash
tunlite enable all --json --non-interactive
tunlite status --tag work --json --non-interactive
tunlite sync --tag work --json --non-interactive
```

持久状态必须同时读取四个维度，不能只看进程是否存在：

- `desiredState`: `enabled`、`disabled`
- `processState`: `inactive`、`starting`、`running`、`failed`、`unknown`
- `health`: `unknown`、`healthy`、`unhealthy`
- `syncState`: `synced`、`drifted`、`missing`、`blocked`

`processState: running` 不代表端点健康；`health: unknown` 也不等于失败。
结合 `conditions[]`、`observedAt` 和四维状态判断。

## 修改、删除和日志

```bash
tunlite list --json --non-interactive
tunlite list --tag work --json --non-interactive

# 只修改目标；未给出的字段保持不变
tunlite set db-5432 --to deploy@new-host --json --non-interactive

# 一旦出现任意 -L/-R/-D，会替换完整 Forward 集合，必须先 dry-run
tunlite set db-5432 -L 5432:localhost:5432 -D 1080 --dry-run --json --non-interactive

tunlite rename db-5432 staging-db-5432 --json --non-interactive
tunlite logs staging-db-5432 --lines 100 --json --non-interactive

# 已启用的 Connection 必须先 disable
tunlite rm staging-db-5432 --dry-run --json --non-interactive
tunlite rm staging-db-5432 --json --non-interactive
```

`logs --follow` 是流式人类输出，不能与 `--json` 同用。

## 检查和诊断

```bash
# 可检查 Connection 名称，也可直接检查 SSH 目标
tunlite check db-5432 --json --non-interactive
tunlite check user@host --timeout 12 --json --non-interactive

tunlite doctor --json --non-interactive
tunlite doctor db-5432 --json --non-interactive
tunlite doctor --offline --json --non-interactive
```

认证失败时 Tunlite 不会替用户写入远端 `authorized_keys`。让用户先在终端中完成
正常 SSH 认证配置，再重试 `check`。不要索取、读取或复制用户私钥。

## 声明、迁移与机器契约

```bash
tunlite export --output tunlite.json --json --non-interactive
tunlite validate tunlite.json --json --non-interactive
tunlite import tunlite.json --dry-run --json --non-interactive
tunlite import tunlite.json --json --non-interactive

# 0.x -> v1：先规划，再迁移；不会自动启动旧配置中 enabled 的连接
tunlite migrate --dry-run --json --non-interactive
tunlite migrate --json --non-interactive

# 仅当迁移后的配置没有发生修改时，才允许字节级恢复
tunlite migrate --rollback --dry-run --json --non-interactive
tunlite migrate --rollback --json --non-interactive

tunlite schema cli
tunlite schema connection
tunlite schema ssh
```

导入和迁移不会安装依赖、执行认证或启动 Connection。对迁移返回的
`legacy_enabled_not_started`，应把 `enable_connection` 展示为后续建议，不能自动执行。

## 安全规则

- 所有修改操作优先运行 `--dry-run`；相同调用可重复执行，但仍需检查逐项结果。
- 安装软件、公开监听、远端转发、删除声明、覆盖导入和回滚都需要用户明确同意。
- `rm` 只删除本机声明与本机服务，不应推断为删除任何远端资源。
- 不把私钥、令牌或 Secret 值写入参数、配置、日志或回复。
- 运行时能力与本 Skill 不一致时，停止高风险操作，读取 `capabilities` 和 Schema，
  并向用户报告契约不一致。
