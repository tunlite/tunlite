---
name: use-tunlite
description: 当需要使用 Tunlite 创建、持久运行、检查或排查 SSH 本地转发（-L）、远端转发（-R）或 SOCKS 代理（-D）时使用。适用于“访问内网数据库”“保持 SSH 隧道不断线”“开放反向隧道”“启动时自动恢复”等任务。
---

# 使用 Tunlite 管理 SSH 连接

<!-- tunlite-contract
protocolVersion: 1
commands: add,capabilities,check,disable,doctor,enable,export,help,import,list,logs,manage,monitor,remove,rename,resolve,restart,run,schema,set,start,status,stop,unmanage,validate,version
exit-codes: 0,1,2,3,4,5
serviceState: registered,absent,unsupported,unknown
mode: stopped,foreground,background,persistent,unknown
processState: inactive,starting,running,failed,unknown
health: unknown,healthy,unhealthy
consistencyState: consistent,drifted,missing,blocked
-->

Tunlite v1 是单文件 CLI。每个持久 Connection 由操作系统的用户级服务独立托管，
服务直接运行系统 `ssh`。不存在管理全部连接的中心常驻进程。

Agent 调用时始终使用 `--json --non-interactive`，依据退出码和结构化字段决策，
不得解析人类文本。执行任何动作前先运行：

```bash
tunlite capabilities --json --non-interactive
```

响应顶层 `protocolVersion` 必须等于 `1`。若命令不存在、协议版本不受支持或 Core
版本早于 `v1.0.0-preview.1`，停止执行并告知用户需要安装、修复或升级；未经同意
不得安装软件。握手成功后以运行时 `capabilities` 为准。

## 稳定退出码

| 退出码 | 含义 | Agent 行为 |
|---:|---|---|
| 0 | 成功 | 继续 |
| 1 | 操作失败 | 读取 `error` 或逐项结果 |
| 2 | 参数无效或冲突 | 修正调用，不要原样重试 |
| 3 | Connection 不存在 | 检查名称或让用户确认 |
| 4 | SSH 认证失败 | 引导用户自行完成 SSH 密钥或 Provider 认证 |
| 5 | 批量操作部分失败 | 逐项处理 `results[]`，不得把整体视为成功 |

所有 CLI JSON 成功响应、错误响应和流式首帧都包含整数 `protocolVersion: 1` 和
`outcome`。原始 Schema 与导出的 Connection 配置不是 CLI 响应，分别使用 `$schema`
和 `configVersion`，不应从中查找 `protocolVersion`。
失败项优先读取稳定的 `code`、`riskLevel` 与 `actions[]`。
不得从 `message` 中提取并执行命令。

## 创建和临时运行

Forward 采用 OpenSSH 形式，可重复使用：

```bash
# 本机 5432 -> 远端数据库 5432
tunlite add db-5432 --to user@host -L 5432:localhost:5432 --group work --json --non-interactive

# 远端 9000 -> 本机 3000
tunlite add preview-9000 --to user@host -R 9000:localhost:3000 --json --non-interactive

# 本机 SOCKS5 代理
tunlite add socks-1080 --to user@host -D 1080 --json --non-interactive

# 在前台运行已有 Connection
tunlite run db-5432 --json --non-interactive
```

`run --json` 在连接存续期间输出逐行 JSON 事件流，而不是单个 JSON 文档。Agent 必须
逐行读取 `starting`、`stopped` 等事件，并持续持有该前台进程；不能等待进程退出后再把
完整 stdout 当成一个 JSON 对象解析。

通用 SSH 参数：`-i <私钥路径>`、`--jump user@host:port`、
`--ssh-opt Key=Value`。`--ssh-opt` 只接受严格的 `Key=Value`，不要传 `-o`。

`-L`、`-R` 的格式是 `[bind:]port:host:hostport`；`-D` 是 `[bind:]port`。
省略 bind 时仅监听回环地址。IPv6 地址必须使用方括号。

公开监听（例如 `0.0.0.0`）、远端转发和其他 Publish 行为会扩大网络暴露面。
Agent 必须在 `add`、`set`、`import` 或 `manage` 引入这类声明前展示 `--dry-run` 计划并
取得用户明确同意，再执行实际命令。Tunlite V1 只返回结构化风险提示，不提供
`--approve`，也不保存批准状态。

## 持久生命周期

```bash
# 先检查计划；该命令不应修改系统
tunlite enable db-5432 --dry-run --json --non-interactive

# 进入临时后台运行，不在下次登录或开机时恢复
tunlite start db-5432 --json --non-interactive
tunlite stop db-5432 --json --non-interactive

# 进入后台长期运行，并立即启动
tunlite enable db-5432 --json --non-interactive

# 查看一条或全部连接
tunlite status db-5432 --json --non-interactive
tunlite status --json --non-interactive

# 异常漂移必须明确选择事实来源
tunlite resolve db-5432 --use tunlite --dry-run --json --non-interactive
tunlite resolve db-5432 --use system --json --non-interactive

tunlite restart db-5432 --json --non-interactive
tunlite disable db-5432 --json --non-interactive
```

批量选择支持 `--all` 或单值 `--group`。名称、`--all` 和 `--group` 三种选择器互斥。
批量操作可能部分成功，必须检查每个结果：

```bash
tunlite enable --all --json --non-interactive
tunlite status --group work --json --non-interactive
```

状态必须同时读取五个维度，不能只看进程是否存在：

- `serviceState`: `registered`、`absent`、`unsupported`、`unknown`
- `mode`: `stopped`、`foreground`、`background`、`persistent`、`unknown`
- `processState`: `inactive`、`starting`、`running`、`failed`、`unknown`
- `health`: `unknown`、`healthy`、`unhealthy`
- `consistencyState`: `consistent`、`drifted`、`missing`、`blocked`

`processState: running` 不代表端点健康；`health: unknown` 也不等于失败。
结合 `conditions[]`、`observedAt` 和五维状态判断。

## 修改、删除和日志

```bash
tunlite list --json --non-interactive
tunlite list --group work --json --non-interactive

# 只修改目标；未给出的字段保持不变
tunlite set db-5432 --to deploy@new-host --json --non-interactive

# 一旦出现任意 -L/-R/-D，会替换完整 Forward 集合，必须先 dry-run
tunlite set db-5432 -L 5432:localhost:5432 -D 1080 --dry-run --json --non-interactive

tunlite rename db-5432 staging-db-5432 --json --non-interactive
tunlite logs staging-db-5432 --lines 100 --json --non-interactive

# 只有 stopped Connection 可以删除
tunlite remove staging-db-5432 --dry-run --json --non-interactive
tunlite remove staging-db-5432 --json --non-interactive

# 原生服务无中断进入或离开 Tunlite 管理；先检查计划
tunlite manage native-service --name staging-db-5432 --dry-run --json --non-interactive
tunlite manage native-service --name staging-db-5432 --json --non-interactive
tunlite unmanage staging-db-5432 --dry-run --json --non-interactive
tunlite unmanage staging-db-5432 --json --non-interactive
```

`logs --follow --json` 输出逐行 NDJSON；每条记录包含 `connection`、`stream`、`line` 和
`observedAt`。Agent 必须持续逐行读取，不能等待命令结束后再解析整个 stdout。

## 检查和诊断

```bash
# 可检查 Connection 名称，也可直接检查 SSH 目标
tunlite check db-5432 --json --non-interactive
tunlite check user@host --timeout 12 --json --non-interactive

tunlite doctor --json --non-interactive
tunlite doctor db-5432 --json --non-interactive
tunlite doctor --offline --json --non-interactive
```

认证失败时，Agent 不得索取、读取或复制用户私钥，也不得在非交互模式下尝试提交密码。
Tunlite V1 不生成密钥、不修改远端 `authorized_keys`。需要配置密钥时，应让用户使用
系统 OpenSSH 工具亲自在终端完成，随后重新执行 `check --json --non-interactive`。

## 声明、迁移与机器契约

```bash
tunlite export --output tunlite.json --json --non-interactive
tunlite validate tunlite.json --json --non-interactive
tunlite import tunlite.json --dry-run --json --non-interactive
tunlite import tunlite.json --json --non-interactive

# 0.x -> v1：显式导入；不会自动启动旧配置中 enabled 的连接
tunlite import ~/.config/tunlite/config.json --dry-run --json --non-interactive
tunlite import ~/.config/tunlite/config.json --json --non-interactive

tunlite schema cli --json --non-interactive
tunlite schema connection --output connection.schema.json
tunlite schema ssh --output -
```

`schema --json` 返回带 `protocolVersion` 的 CLI Envelope，原始 Schema 位于 `schema`
字段。需要保存或直接读取原始 Schema 时使用 `--output <文件>` 或 `--output -`；
`--output -` 不能与 `--json` 同用。导出到 stdout 时同理使用
`tunlite export --output -`，不要把原始配置误当作 CLI 响应。

导入不会安装依赖、执行认证或启动 Connection。对
`legacy_running_state_ignored`，应把 `start_connection_after_legacy_shutdown` 展示为后续
建议，不能自动执行。
安装、更新和卸载不属于 Tunlite CLI；使用发行页或受支持的第三方包管理器，并在改变
本机软件前取得用户同意。

## 安全规则

- 所有修改操作优先运行 `--dry-run`；相同调用可重复执行，但仍需检查逐项结果。
- 安装软件、公开监听、远端转发、删除声明和覆盖导入都需要用户明确同意。
- `remove` 只删除本机声明与本机服务，不应推断为删除任何远端资源。
- 不把私钥、令牌或 Secret 值写入参数、配置、日志或回复。
- 运行时能力与本 Skill 不一致时，停止高风险操作，读取 `capabilities` 和 Schema，
  并向用户报告契约不一致。
