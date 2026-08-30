# Tunlite CLI 交互设计

- **状态：提案，待确认**
- **适用目标：** `v1.0.0-preview` 及其后续演进
- **文档性质：** 产品交互设计审查稿，不是当前行为承诺
- **最后核对：** 2026-08-30

> 本文同时记录“当前实现”和“建议终态”。两者必须严格区分：标为“建议”、
> “应”或“待确认”的内容，在用户确认并完成代码、测试和真机验收前，都不能写入帮助、
> Release Note、Schema 或能力清单，不能被视为已经支持。

## 1. 目的与审查方法

本文对 Tunlite 当前 26 个公开一级命令做一次完整交互审查，目标不是增加命令数量，
而是让第一次接触 Tunlite、不了解 launchd/systemd 和前后台模型的用户也能回答：

1. 我定义了什么 Connection？
2. 它现在是否在运行，在哪里运行？
3. 下次登录后是否自动启动？
4. 它由当前用户还是系统范围托管？
5. 真实流量是否健康，声明和系统状态是否一致？
6. 当前命令会读取、写入、启动、停止、重启或提权吗？
7. 如果是批量操作，哪些成功、哪些失败，是否能够安全重试？

审查遵循三条规则：

- 不用内部 `mode`、launchd、systemd 的实现细节代替用户语言。
- 不为了“看起来简单”而把真实的自动启动、Scope、健康和漂移状态折叠成一个真假值。
- 不以未来 Driver、Windows 或 Desktop 的假设改变当前 SSH Preview 的行为。

## 2. 已确认的能力边界

以下是本文的固定前提，不在本轮重新设计：

| 边界 | 当前产品范围 |
| --- | --- |
| Driver | Preview 只正式支持内置 SSH Driver；`ssh` 是默认 Driver。 |
| macOS | 支持 launchd user Scope；system Scope 已实现但必须通过管理员授权与专用真机门禁后才可对外确认。 |
| Linux | 支持 systemd user Scope；不支持 Linux system Scope。 |
| Windows | 核心可保持编译，但 Preview 不发布 Windows 产物，也不承诺后台服务。 |
| Connection | 一份可迁移 Connection 声明；前台/后台、自动启动、Scope 和原生绑定是机器本地状态，不复制成多份 Connection。 |
| 归属 | 只有一个可选 `group`；没有 `tag`，也不保留历史 tag 兼容层。 |
| 运行架构 | 没有管理所有连接的中心 daemon；每条后台 Connection 直接由 launchd/systemd 托管。 |
| 安装升级 | 核心 CLI 不提供 `install/update/uninstall`；发行渠道拥有二进制。 |
| 配置兼容 | 不为无人使用的历史 Preview/私有格式增加隐式兼容，新增兼容层必须单独确认。 |

这些边界意味着：

- `run/start/enable` 都只接受已存在的 Connection 名称，不再提供一套匿名隧道语法。
- `--driver ssh` 是通用 Driver 选择方式；不增加 `--ssh` 特殊入口。
- `group` 同时承担人类分段与批量选择，不再叠加 tag 概念。
- 后台故障恢复由每条原生服务完成；一个连接失败不应拖垮其他连接。

## 3. 当前公开命令与命令族

当前恰好有 26 个公开一级命令：

```text
add capabilities check disable doctor enable export help import list logs manage
monitor remove rename resolve restart run schema set start status stop
unmanage validate version
```

建议继续按用户任务分为七个命令族：

| 命令族 | 命令 | 用户问题 |
| --- | --- | --- |
| Connection 声明 | `add list set rename remove` | 我定义和管理了什么？ |
| 运行与检查 | `run check doctor monitor` | 能否连接、是否健康、如何实时查看？ |
| 原生生命周期 | `start stop enable disable restart status logs` | 现在如何运行，下次登录是否运行？ |
| 所有权与漂移 | `manage unmanage resolve` | 谁拥有原生服务，声明和系统冲突时听谁的？ |
| 声明交换 | `import export validate` | 如何移动、审查和合并声明？ |
| 机器契约 | `schema capabilities` | 自动化可以依赖什么？ |
| 基础入口 | `help version` | 如何发现用法和版本？ |

不建议新增 `watch`、`rm`、`discover`、`adopt`、`sync`、`migrate`、`tun` 等同义命令。
新概念应先证明无法由现有命令族清晰表达。

## 4. 跨命令交互原则

### 4.1 先解释意图，再暴露机制

人类界面优先使用以下正交表达：

- **状态：** 启动中、运行中、停止中、已停止、重启中、运行异常、需要处理、未知。
- **运行位置：** 当前终端、后台服务、未运行、未知。
- **自动启动：** 是、否、未知。
- **范围：** 当前用户、系统、未知。

`foreground/background/persistent` 仍可保留在 JSON 机器契约中，但不能作为用户理解产品
的唯一入口。尤其不能把 `persistent` 翻译成“后台”，因为它还包含自动启动意图。

### 4.1.1 本轮不再重开的已接受决策

以下三项已经是产品决策，不再放入待确认列表，也不以本提案为由重新设计：

- `add` 在受支持平台 eager materialize 默认停止的原生服务定义。
- `set` 一旦出现 forwards 或 options，就分别把该集合视为完整新集合，而不是追加。
- 批量和 `import` 按 Connection 逐项提交，允许 partial，并用逐项结果与 exit 5 表达。

本文仍要求把这些副作用解释清楚、补足 dry-run 和验收，但不改变其语义。

### 4.2 同名参数在命令间保持同义

- `--group` 始终表示选择一个业务 Group；不兼作创建 Group 或标签查询语法。
- `--all` 始终表示当前配置中的所有 Connection；不能扫描并接管系统服务。
- `--scope` 只在 `enable` 中选择或迁移原生服务范围，其他命令不得偷偷改 Scope。
- SSH 的 `-p/-i/-J` 用于 `add`、`set`、`check`；`-L/-R/-D/-o` 只用于定义或修改隧道的
  `add`、`set`。`check` 检查已存 Connection 时可以继承其中的 forwards/options 配置，
  但命令行不接受这些 flags。`set` 的“补丁还是整表替换”必须在帮助和预演结果中明确。
- `--force` 只解除明确的同名/文件覆盖保护，不跳过 Schema、安全或 Driver 校验。

### 4.3 先完成参数预检，再输出耗时进度

当前全局进度入口发生在命令自己的完整参数解析之前，虽然部分缺参已被挡住，但仍可能
对其他无效参数先打印“正在……”再报 usage。建议终态顺序固定为：

```text
解析全局参数 → 解析命令参数 → 纯读取预检 → 输出动作/风险 → 授权 → 执行 → 收敛观察 → 结果
```

usage、未知对象、互斥参数和明显越界值必须在任何“正在执行”文案之前返回。

### 4.4 一个动作只改变它承诺的状态

- `start` 创建只属于本次登录会话的 temporary background；`stop` 只停止这种模式。
- `enable` 注册 persistent：user 使用 LaunchAgent，system 使用 LaunchDaemon；`stop` 对任何
  persistent 一律报错并提示 `disable`。
- `disable` 解除 persistent 的加载和自动启动，但保留 Connection。user 不需要 sudo，并可
  保留用户态可重建 artifact；system 在同一次已授权事务中 bootout/disable 并删除 root-owned
  LaunchDaemon artifact，保留 Connection、用户态 Record 和 system Scope 意图。
- `restart` 对 temporary background、user persistent、system persistent 都支持，保持
  mode、autostart 和 Scope，也不能把 stopped 偷偷变成 running。
- `set` 可以因应用新执行计划而重启正在运行的服务，但必须提前明确展示。
- `rename` 修改用户可见名称，但当前保留旧的原生 service identity；必须向用户和 JSON
  如实展示，不得让人误以为系统 label/unit 也改名。

macOS 的 root 边界只包含 system persistent 的注册、解除注册，以及执行配置变化时对
LaunchDaemon 的原子替换。只读操作与 restart 不授权；temporary background 不通过 system
LaunchDaemon 表达。

### 4.5 读操作不能伪装成成功健康

命令成功读取状态与 Connection 健康是两件事。`status` 能成功执行但返回 unhealthy、
unknown 或 drifted；默认退出码是否继续为 0，应由脚本契约明确，而不是靠颜色暗示。

## 5. 逐命令审查

下表的“当前行为”来自当前实现核对；“建议终态”仍待产品确认和实现。

### 5.1 Connection 声明

| 命令 | 语法 | 当前行为与副作用 | 易用性判断 | 建议终态 |
| --- | --- | --- | --- | --- |
| `add` | `tunlite add <name> [--driver ssh] [--group group] [-p port] [-i key] [-J jump] [-L/-R/-D ...] [-o Key=Value ...] user@host` | 保存一份 Connection；默认 SSH 端口是 22；接受常见命令中的 `-N/-T`，但 Tunlite 本身固定生成无远端命令、无终端的隧道计划。在存在受支持服务管理器时 eager materialize 一份默认停止、禁用自动启动的原生服务定义。没有服务管理器时保存声明并警告只能前台运行。不会启动隧道。 | “添加配置”却写原生服务，有隐藏副作用，但它消除了以后 start 时才发现冲突的延迟失败。该 eager materialization 已是接受的主设计。 | 维持已接受的 eager materialization。帮助、dry-run 和成功结果必须明确列出“写 Connection + 创建停止的原生服务定义”，不使用“已启动”。 |
| `list` | `tunlite list [--group group]` | 只列配置中的受管 Connection，按一个 `group` 分段；无 group 进入未分组。不扫描外部原生服务。 | 简单清晰；若只显示声明而不提示异常状态，用户可能误以为都可运行。 | 保持轻量清单。默认展示名称、Group、Driver、主要端点；需要运行态时明确引导 `status`，不把 `list` 变成第二个 status。 |
| `set` | `tunlite set <name> [-p port] [-i key] [-J jump] [-L/-R/-D ...] [-o Key=Value ...] [--clear identity\|jump\|options ...] [user@host] [--group group\|--no-group]` | 根据已存 Driver 解析补丁，不能换 Driver。标量字段按出现项更新；一旦提供 forwards，会整表替换全部 forwards；一旦提供 `-o`，options 会整表替换全部 options。`--clear identity/jump/options` 显式清空。若执行计划变化且服务正在 background/persistent，会自动 materialize 并 restart；事务失败尝试恢复声明和服务。只有修改 system persistent 执行计划、需要原子替换 LaunchDaemon 时才进入统一授权门；普通 restart 本身不提权。 | 语法与 `add` 相似，但用户容易把重复 `-L` 或 `-o` 当作“追加一个”。运行中的自动重启是重要副作用。 | 维持已接受的整表替换，不新增另一套 append 语义。帮助明确“本次给出的 forwards/options 是完整新集合”；dry-run 输出 before/after diff、是否重启及中断风险；实际执行在重启前显示进度。 |
| `rename` | `tunlite rename <name> <new-name>` | 原子更新 Connection 名称和本机管理记录；拒绝前台运行和目标重名。为避免后台隧道中断，保留旧 `ServiceKey/UnitName`，即原生 service identity 不随展示名改名。 | 对用户是零中断好处，但 status/日志中出现旧 label 时会困惑。 | 保持零中断与旧 service identity；成功结果、status 详情和 JSON 显示 `connectionName` 与 `nativeIdentity`。是否提供未来的离线 identity 重建是单独功能，不塞入 rename。 |
| `remove` | `tunlite remove <name>` | 只允许已停止、不处于持久运行的 Connection。system 对象必须已由 disable 删除 root-owned artifact；remove 随后只删用户态 Record/Connection，零 sudo。user 对象可同时删除剩余的用户态可重建 artifact。不会删除远端资源。显式名称即授权，无二次确认。 | 安全边界合理；remove 不能暗中承担 system disable。 | 保持拒绝运行中对象和无确认的可脚本化行为。dry-run 列出将删除的用户态路径/identity，并证明不存在 root artifact；结果明确日志是否保留。不要提供 `rm`。 |

#### `add` 的设计结论

eager materialization 是已接受的主设计，理由是它在添加时一次性暴露：

- 本机是否有可用的 launchd/systemd；
- 原生 identity 是否冲突；
- plist/unit 是否可生成；
- 声明能否无损映射到后台服务。

它的代价是 `add` 不再是纯配置写入。解决方式是透明展示和可靠 dry-run，而不是把失败
推迟到第一次 `start`；本轮不再重新确认这一取舍。

#### `set` 的集合语义

建议帮助中直接给出例子：

```text
# 这不是追加；结果只保留下列两个 forward
tunlite set db -L 5432:localhost:5432 -D 1080

# 这不是追加；结果只保留下列 options
tunlite set db -o ServerAliveInterval=30 -o ServerAliveCountMax=3
```

当前实现的 SSH option allowlist **实际允许 `SetEnv`**，这是 Preview 安全缺口，不得写成
已经禁止。建议 P0 从 allowlist 删除并新增拒绝回归测试。原因不是语法做不到，而是它可把
任意环境值写入 provider 进程/远端环境，扩大 Secret 泄漏、服务文件持久化和不可审查配置
的攻击面。其他允许列表之外的 OpenSSH option 继续拒绝；不得用 `--force` 绕过。

### 5.2 运行与原生生命周期

| 命令 | 语法 | 当前行为与副作用 | 易用性判断 | 建议终态 |
| --- | --- | --- | --- | --- |
| `run` | `tunlite run <name>` | 在当前终端运行已有 Connection；持有前台锁；若存在后台记录，必须证明它已停止且一致。Ctrl-C/context cancel 会输出 stopped/cancelled 并 exit 0；provider 自身异常退出仍报错。`--dry-run` 输出执行计划。JSON 当前先写 starting Envelope，必须保留为流式首帧回归。 | 心智清晰；机器事件与 provider 输出的 stream 归属仍要完全固化。 | 保持前台语义。TTY 人类输出到 stderr；机器模式按已接受的首帧流式契约回归，不能假装是单个 JSON 文档。 |
| `start` | `tunlite start <name> \| --group <group> \| --all` | stopped → temporary background；现在运行但不自动启动。background 时幂等；persistent 时拒绝。批量逐项执行；观察有预算，可返回“仍在 starting”。 | 与 enable 的区别需要持续展示。 | 固定为当前登录会话的临时后台，不注册 system LaunchDaemon、不请求 sudo。结果显示“运行位置：后台服务；自动启动：否”；若未收敛，明确返回 pending/starting。 |
| `stop` | `tunlite stop <name> \| --group <group> \| --all` | temporary background → stopped；stopped 幂等。 | stop 与 disable 的边界必须只由 mode 决定。 | 只允许停止 temporary background；对 user/system persistent 一律报错并提示 `disable`，不请求 sudo。 |
| `enable` | `tunlite enable (<name> \| --group <group> \| --all) [--scope user\|system]` | 确保现在运行并开启自动启动；显式不同 Scope 做迁移，观察可返回 still starting。 | “enable”实际是注册 persistent，副作用必须直说。 | 注册 persistent：user 安装/加载 LaunchAgent，无 sudo；system 安装/加载 LaunchDaemon，需要统一 sudo 授权。新服务默认 user，已有 persistent 未传 Scope 时保留原 Scope。 |
| `disable` | `tunlite disable <name> \| --group <group> \| --all` | persistent → stopped，同时停止并关闭自动启动；background 时拒绝使用 stop；stopped 幂等。保留 Connection。system 在授权事务中 bootout/disable 并删除 root-owned LaunchDaemon artifact，保留用户态 Record 和 system Scope 意图；user 可保留可重建 artifact。 | 与 enable 对称，不能删除。 | user LaunchAgent 不需要 sudo；system 解除注册和删除 root artifact 需要统一 sudo 授权。temporary background 必须提示使用 stop。 |
| `restart` | `tunlite restart <name> \| --group <group> \| --all` | 支持 temporary background、user persistent、system persistent；保持 mode、autostart、Scope；stopped 拒绝。会短暂中断流量，可能返回仍在 restarting。**当前实现缺口：system Restart 仍通过 `runMutation` 执行特权 `bootout/bootstrap`，尚不符合已确认终态。** | 高频且清晰；system restart 不应因为服务定义是 root-owned 就索要密码。 | 全 Scope 支持；显式 CLI 命令不二次确认，Monitor `r` 需要确认。system restart 只有在 PID/身份、原生定义 consistent、KeepAlive 已启用且会拉起预期 Provider 全部可证明后才终止，任一前置失败零变更；不请求 sudo，也不得降级到特权 service-manager 重载。 |
| `status` | `tunlite status [<name> \| --group <group> \| --all]` | 读取声明、记录、原生服务、进程、端点健康和一致性。默认实现预算为整批 5 秒、单项 2 秒、最多并发 8（可调实现参数，不是长期协议）。即使状态 unhealthy/drifted/unknown，只要读取成功仍 exit 0。缺少 record/identity 时当前会先把 scope 初始化为 user，可能把“未知”误显示为 user。actual mode 与 record 中 desired mode 不同会归为 drift，但人类界面没有并列展示两者。 | 信息最重要，但单个“正常运行”会把 `health=unknown` 误当健康；exit 0 容易被脚本误用。 | `health=unknown` 显示“运行中，健康未知”，绝不能叫“正常”。人类界面无 Scope 证据时 P0 先显示 `—/未知`；JSON 如何表达 unknown 单独确认。详情并列 desired/actual mode。默认 exit 0 保持“读取成功”兼容，并在 JSON 明确 conditions；是否增加 `--fail-if unhealthy|attention` 待确认。 |
| `logs` | `tunlite logs <name> [--lines N] [--follow]` | 默认读取 100 行，允许范围 1–100000；`--json` 非 follow 返回 Envelope，follow 返回 NDJSON log events。日志是 provider 外部输出，无法保证清除未知 Secret。 | 功能明确；流式 JSON 与普通 JSON 必须区别。 | 保留。`logs --follow --json` 首帧声明协议/stream 是已接受契约并列为 P0；Ctrl-C 正常结束；文档明确日志不可信和脱敏边界。 |
| `monitor` | `tunlite monitor [--group group] [--interval seconds]` | 默认 1 秒刷新，允许 0.25–60 秒；需要交互终端；全屏 top 风格。当前任意一行状态采集失败可能拖垮整体，system restart 也尚未实现无 sudo 路径。 | 是最适合新手的运行入口，但必须容忍单连接故障并准确映射生命周期动作。 | `x` 按 mode 映射：temporary background→stop，persistent→disable，后者使用明确的强确认；`r` 对 temporary/user persistent/system persistent 全部可用并确认，system `r` 不提示密码。只有 system disable 等真正 root 操作才退出 raw mode进入 sudo。 |

### 5.3 所有权、发现与漂移

| 命令 | 语法 | 当前行为与副作用 | 易用性判断 | 建议终态 |
| --- | --- | --- | --- | --- |
| `manage` | `tunlite manage [native-service] [--name name]` | 扫描可无损解析的外部 SSH 原生服务，导入 Connection、绑定记录和基线，不重启兼容的在运服务。`--name` 省略时默认使用发现到的原生 Connection 名。无显式 target 时当前会读 stdin 让用户编号选择；`--non-interactive` 要求 target，但普通管道 stdin 并未被严格拒绝。 | 省略 target 的交互选择方便，但必须是真实 TTY，不能在 CI 中悬挂。 | 无 target 时要求 stdin 和终端输出都是真实 TTY；有效请求因 non-interactive/非 TTY 无法选择时按已接受契约 exit 4。只有参数语法错误才 exit 2。dry-run 展示将接管的 identity、Scope、mode 和解析出的 SSH 配置。 |
| `unmanage` | `tunlite unmanage <name>` | 删除 Tunlite 声明和绑定，但保留原生 service 定义及其当前运行/自动启动状态；适用于外部接管或 Tunlite 创建的服务。 | “取消管理”容易被误解为停止或删除。 | 成功前后明确显示“原生服务将继续运行/保持自动启动，Tunlite 不再管理”。若无法证明已完整交还则失败回滚，不能静默留下半绑定。 |
| `resolve` | `tunlite resolve <name> --use tunlite\|system` | 漂移时显式选择事实来源。`--use tunlite` 可能重写原生定义并重启运行服务；`--use system` 正常路径只读取原生定义，再写 Tunlite Connection/record，不修改 system 原生服务，因此不需要 system sudo。 | 方向明确，但两个方向的权限与副作用不同，必须预演。 | 保留显式 `--use`，无默认值。`--use tunlite` 的真实 system 写/补偿进入授权门；正常 `--use system` 不得无故 sudo。dry-run 输出字段 diff、是否重启、native identity 与 Scope；参数已是明确选择，不再二次确认。 |

### 5.4 检查与诊断

| 命令 | 语法 | 当前行为与副作用 | 易用性判断 | 建议终态 |
| --- | --- | --- | --- | --- |
| `check` | `tunlite check <connection-or-user@host> [-p port] [-i key] [-J host] [--timeout seconds]` | 对已存 Connection 或临时 SSH target 做只读连通性/认证检查；不保存临时 target，不启动隧道。默认超时 12 秒，允许 0.25–120 秒。 | 两类输入实用，但错误必须区分 DNS、TCP、host key、认证。 | 保留。参数预检后立即显示检查阶段；JSON 给稳定 condition/action，不建议把原始 ssh 文本当机器契约。`--dry-run` 在发起任何 DNS/TCP/SSH 前 exit 2。 |
| `doctor` | `tunlite doctor [name] [--offline] [--timeout seconds]` | 综合检查本机环境、配置、服务管理器和 Connection；`--offline` 跳过网络深检。默认每 Connection 超时 12 秒，允许 0.25–120 秒；可能扫描多个对象。 | 与 check 分工合理。 | 保留。先输出检查范围和 online/offline；每项独立超时，单项失败不阻断其余诊断；最终按 error/warning/info 汇总。`--dry-run` 在环境/服务管理器/网络探测前 exit 2。 |

### 5.5 声明交换

| 命令 | 语法 | 当前行为与副作用 | 易用性判断 | 建议终态 |
| --- | --- | --- | --- | --- |
| `import` | `tunlite import <path> [--force]` | 严格读取声明并逐 Connection 合并。对每项同时处理 eager materialization/更新；不是整批原子：前项成功、后项失败时保留前项并返回 partial/exit 5。`--force` 允许同名更新但不允许换 Driver；若更新的是正在 background/persistent 运行的 Connection，执行计划变化可能自动 restart。只有更新 system persistent 并需要原子替换 LaunchDaemon 时才进入统一授权门；普通 restart 本身不提权。不会接管外部服务或隐式启动原本 stopped 的对象。 | partial 与整表语义已接受，但必须把覆盖、重启和逐项提交说清。 | 维持已接受的逐项 partial；执行前显示 N 项、覆盖数、可能重启和“逐项提交”；JSON 必须逐项结果，不新增 `--atomic` 候选。 |
| `export` | `tunlite export [--output path\|-] [--force]` | 导出可迁移 Connection，不含本机 record、Scope、mode、原生 identity 或日志。默认直接向 stdout 输出原始声明 JSON；指定普通文件时已有文件需 force。`--json` 无 output 时改为 Envelope；`--json --output -` 拒绝，避免原始文档和 Envelope 混淆。 | 边界清晰。 | 保留。帮助明确不导出本机生命周期和 Secret 管理承诺；raw stdout 与 JSON Envelope 继续互斥。 |
| `validate` | `tunlite validate [path]` | 校验指定声明；无路径时校验当前配置；只读。 | 清晰。 | 保留。错误带 JSON Pointer/Connection 名和稳定 code；多错误尽量一次返回，不要求用户逐个修。 |

### 5.6 机器契约与基础入口

| 命令 | 语法 | 当前行为与副作用 | 易用性判断 | 建议终态 |
| --- | --- | --- | --- | --- |
| `schema` | `tunlite schema [connection\|connection-list\|ssh\|cli\|service-state] [--output path\|-] [--force]` | 默认输出 `connection` Schema；写文件时有覆盖保护；`--json --output -` 拒绝。 | 清晰。 | 保留。Schema 与对应 config/protocol version 同步测试；raw Schema 不包 Envelope，机器元信息模式才包。 |
| `capabilities` | `tunlite capabilities` | 返回版本、平台、26 命令、Driver、服务管理器、Scope、状态枚举和退出码。会探测本机服务管理器；人类模式较简略。 | 是 Agent/Desktop 握手入口。 | JSON 保持唯一权威能力入口；不可用/未知能力不能默认成 user 或 false 而丢失原因。人类模式增加“为什么不可用”的短说明。 |
| `help` | `tunlite help [command]` | 展示命令族、生命周期说明或单命令 usage；中英文可选。当前单命令帮助多数只有一行 usage。 | 可发现性不足以解释副作用。 | 每个变更命令增加“读取/写入/进程/自动启动/Scope/是否可能重启/支持 flags”的固定摘要；不复制整本手册。 |
| `version` | `tunlite version` | 人类模式输出版本；JSON 输出 version 与 protocolVersion。兼容 `--version/-v`。只读。 | 清晰。 | 保留；版本输出不做网络检查，不混入更新提示。 |

## 6. 全局 Flag 矩阵

全局 flag 可以出现在一级命令前后，当前由统一提取器剥离。终态必须对每个命令显式声明
支持情况，避免“解析成功但被忽略”。

### 6.1 单项语义

| Flag | 当前语义 | 建议终态 |
| --- | --- | --- |
| `--json` | 输出 JSON Envelope、NDJSON 或命令特定机器结果；Monitor 拒绝。 | 成功和错误都不得混入 ANSI、进度或本地化字段值。流式命令明确标为 NDJSON。 |
| `--non-interactive` | 目标语义是禁止选择/授权交互并让 system scope 只使用外部预授权 sudo，但当前覆盖不完整：Monitor 仍没有显式拒绝该 flag，manage 的非 TTY 判定也不完整。 | 整条调用链零提示、零 stdin 读取。参数语法错误 exit 2；有效请求因认证、选择或其他人工交互无法继续时按已接受契约 exit 4。 |
| `--dry-run` | 对实现了预演的变更命令不写入；全局解析器也会接受部分无意义组合。Monitor 拒绝。 | 仅在能给出真实计划的命令上声明支持。读命令要么明确等价只读，要么以 usage 拒绝，不能静默忽略。 |
| `--lang en\|zh-CN` | 控制帮助、部分人类进度/状态/TUI；JSON 字段和值保持英文。 | 只影响人类文字。JSON `code/outcome/action/type/state` 永不翻译；message 可否本地化需一次性定契约，建议稳定英文并另加可选 displayMessage。 |

### 6.2 命令族支持矩阵（建议终态）

| 命令/命令族 | `--json` | `--non-interactive` | `--dry-run` | `--lang` |
| --- | --- | --- | --- | --- |
| `add set rename remove` | 支持 | 支持 | 支持 | 支持人类输出 |
| `run` | 支持，但定义为 NDJSON/流式 | 支持 | 支持 | 支持人类输出 |
| `start stop enable disable restart` | 支持 | 支持 | 支持 | 支持人类输出 |
| `status` | 支持 | 等价只读，允许 | 不支持；P0 以 exit 2 拒绝无意义 flag | 支持人类输出 |
| `logs` | 支持；follow 为 NDJSON | 支持 | 不支持 | 支持非 JSON 辅助文字 |
| `monitor` | **不支持** | 不执行；因需要人工交互 exit 4 | **不支持** | 支持 |
| `manage unmanage resolve` | 支持 | 支持；manage 无 target 时拒绝 | 支持 | 支持人类输出 |
| `check doctor` | 支持 | 支持 | **不支持；P0 在外部探测前 exit 2** | 支持人类输出 |
| `import` | 支持 | 支持 | 支持逐项合并/服务更新预演 | 支持人类输出 |
| `export` | 支持；默认 raw stdout，`--json` 改为 Envelope | 支持 | 仅写普通文件时有实际预演意义 | 支持人类输出 |
| `validate` | 支持 | 等价只读，允许 | 不支持 | 支持人类输出 |
| `schema` | 支持 | 等价只读，允许 | 仅 `--output <file>` 时支持 | 支持人类输出 |
| `capabilities help version list` | 支持 | 等价只读，允许 | 不支持 | 支持人类输出 |

### 6.3 明确不支持或互斥的组合

以下组合不进入耗时工作；除 `monitor --non-interactive` 的已注明交互语义外，均在参数预检
阶段 exit 2：

- `monitor --json`
- `monitor --non-interactive` 不执行，但它属于有效请求缺少人工交互，按全局契约 exit 4，
  不与本列表其他参数组合错误共用 exit 2
- `monitor --dry-run`
- `check --dry-run`，且必须在 DNS/TCP/SSH 前拒绝
- `doctor --dry-run`，且必须在环境、服务管理器或网络探测前拒绝
- `logs --dry-run`，特别是 `logs --follow --dry-run` 必须在打开日志流前拒绝
- `export --json --output -`
- `schema --json --output -`
- 任何 raw document stdout 与 JSON Envelope 的组合
- 任何未声明支持 `--dry-run` 的纯读/流式命令加 `--dry-run`
- `manage --non-interactive` 且未给显式 native target
- `enable --scope ...` 之外的命令使用 `--scope`
- `set --group ... --no-group`
- `set` 对同一 SSH 字段同时 set 与 `--clear`

以下组合应支持并有组合测试：

- 变更命令 `--json --dry-run --non-interactive`
- `enable ... --scope system --json --dry-run --non-interactive`：只读计划，不授权、不 sudo、不写入
- `enable ... --scope system --json --non-interactive`：仅使用已预授权 sudo，stdout 保持纯 JSON
- `--lang zh-CN --json`：机器字段和值仍为稳定英文
- `logs --follow --json`：按已接受契约以协议/stream 首帧开头的 NDJSON，不是单个 Envelope 文档

## 7. 状态与术语

### 7.1 五个状态维度加 Scope

`status --json` 应继续分别报告：

| 维度 | 典型值 | 回答的问题 |
| --- | --- | --- |
| `serviceState` | registered/absent/unsupported/unknown | 本机原生服务定义是否存在？ |
| `mode` | stopped/foreground/background/persistent/unknown | Tunlite 记录/期望的运行方式是什么？ |
| `processState` | inactive/starting/running/failed/unknown | Provider 进程现在怎样？ |
| `health` | unknown/healthy/unhealthy | 已声明端点是否有健康证据？ |
| `consistencyState` | consistent/drifted/missing/blocked | 声明、record 与原生服务是否一致？ |
| `scope` | user/system/unknown | 原生服务实际属于当前用户还是系统？ |

`scope` 当前 Schema 只有 user/system；若缺少 record 且也发现不到 identity，当前实现会误用
user。这里拆成两个决策层：人类界面 P0 直接显示 `—/未知`，不等待协议升级；JSON 如何
表达 unknown、是否增加枚举或可空字段仍待确认。在任何形式下都不能伪造 user 证据。

### 7.2 健康未知不能叫“正常”

当前人类状态把 `processState=running && health=unknown` 显示为绿色“正常运行”。建议改为：

| Process | Health | 人类标签 | 颜色/严重度 |
| --- | --- | --- | --- |
| running | healthy | 正常运行 | 绿色 |
| running | unknown | 运行中 · 健康未知 | 中性/黄色，不使用“正常” |
| running | unhealthy | 运行异常 | 红色 |
| starting | any | 启动中 | 黄色 |
| failed | any | 运行失败 | 红色 |
| unknown | unknown | 状态未知 | 中性/黄色 |

### 7.3 desired 与 actual mode

Record 中的 mode 是 Tunlite 的期望/记录，launchd/systemd 观察结果是 actual mode。两者不同时，
当前只给 `service_mode_drifted` condition。建议详情明确显示：

```text
期望运行方式  长期后台（自动启动）
实际运行方式  已停止
一致性        已漂移
建议动作      resolve --use tunlite / resolve --use system
```

不能用一个 `MODE=persistent` 覆盖“实际上没有运行”的事实。

### 7.4 Status 详细页的最小字段

详细页必须保留而不是省略：

- Connection 名、Group、Driver
- 状态、运行位置、自动启动、Scope
- desired mode、actual mode（有漂移或 JSON 时始终提供）
- PID、UP/已运行时长、RESTARTS/重启次数
- health 与每个 endpoint 的结果
- consistency 与 conditions/actions
- SSH host、jump、forward 类型、监听端与目标端
- native identity、原生定义路径、日志路径（详情/JSON，不必挤入总表）
- observedAt 与数据是否过期

`status` 默认 exit 0 表示“状态成功读取”，不是“全部健康”。是否新增脚本判定选项由产品确认，
不能直接把 unhealthy 改成非 0 破坏现有自动化。

## 8. TTY、耗时进度与批量操作

### 8.1 TTY 判定

| 场景 | 终态要求 |
| --- | --- |
| 普通人类命令 | 只有进度的实际输出目的地 stderr 是 TTY 时才显示短进度；不能因为 stdout 是 TTY 就向重定向的 stderr 写交互进度。结果仍按 stdout/stderr 契约输出。 |
| `monitor` | stdin 与输出必须是真实 TTY；否则快速失败并引导 `status --json`。 |
| `manage` 无 target | stdin 与输出必须是真实 TTY；管道、重定向、CI 均不得读取选择。 |
| `--non-interactive` | 不论是否附着 TTY，都禁止选择、密码、确认和 stdin 读取。 |
| `--json` | 不输出 spinner、颜色、清屏、光标移动或人类进度。 |

`NO_COLOR` 只表示禁用颜色，不能被解释为“禁用 TUI”。Monitor 即使无颜色也需要清屏和光标
控制；不接受控制序列的环境应使用非 TTY `status`。若未来需要持续纯文本观察，应设计
独立 `--plain` 或外部轮询，而不是扩张 NO_COLOR 语义。

### 8.2 耗时操作必须有阶段指示

参数预检完成后，TTY 中超过约 300ms 的操作应至少显示一个阶段；未知耗时不能一直静默：

- SSH：解析 → TCP/SSH 检查 → 认证 → 结果。
- 生命周期：预检 → 写服务定义 → 请求服务管理器 → 等待进程 → 健康稳定窗口 → 完成。
- Scope：只读预检 → 批次授权 → 安装/迁移 → 健康验证 → 保存 record → 完成。
- import：读取/校验 N 项 → 预检 → 应用第 i/N 项 → 汇总。
- doctor/status --all：采集第 i/N 项或周期性汇总，不逐毫秒刷屏。

进度写 stderr；JSON stdout 保持纯净。进度不是成功证明，最终必须给收敛状态。

### 8.3 批量语义

当前普通批量操作主要按 Connection 逐项处理；不能把下面的“整批目标快照 + 全量只读预检”
描述成已经普遍实现。`--group/--all` 的**建议新增终态流程**是：

1. 固定目标快照并按稳定顺序排序。
2. 对全部目标做不写入的参数、能力、冲突和 Scope 预检。
3. 如真实涉及 system 注册、解除注册或配置原子替换，整批只做一次交互授权。
4. 每个 Connection 独立事务应用和回滚；不并行争抢 sudo/服务管理器。
5. 返回逐项 `results[]`、成功/失败/未执行计数和整体 outcome。
6. 部分成功 exit 5；全部失败使用执行失败；零目标不得伪装成功。

这不是整批原子。若业务要求“一个失败全部回滚”，必须设计独立原子模式和跨对象补偿，
不能用文案暗示。

## 9. System Scope 统一授权门

### 9.1 固定流程

当前只有显式 `enable --scope ...` 会在 CLI 层预先进入统一交互授权；其他真正修改 system
注册或定义的路径可能直接落到 Adapter 的 `sudo -n`，缓存未预置或已过期时只会失败而
不会先给用户统一授权机会。授权门不能只看命令行有没有 `--scope`，也不能因为 Record 的
Scope 是 system 就无条件提权。P0 必须让**所有且仅有实际会注册、解除注册或原子替换
LaunchDaemon 的路径**统一经过授权门：

| 入口 | 为什么可能写 system Scope |
| --- | --- |
| `set` | 修改 system persistent 的执行计划，需要原子替换 LaunchDaemon；运行中还可能 restart。 |
| `enable --scope system` | 新建 system 或 user→system 迁移。 |
| `enable --scope user` | 现有 system→user 迁移仍需要删除/停止 system 侧。 |
| `enable` 不写 `--scope` | 现有 record 保留 system，仍会写 system，而不是默认降为 user。 |
| `disable` | 在一个授权事务中 bootout/disable system LaunchDaemon 并删除 root-owned artifact；保留 Connection、用户态 Record 和 system Scope 意图。 |
| `import --force` | 修改已绑定 system persistent 的执行计划，需要原子替换 LaunchDaemon。 |
| `resolve --use tunlite` | 用 Tunlite 声明原子替换 system LaunchDaemon 定义。 |
| Monitor 的 persistent `x` | 映射为 `disable`；system persistent 因解除注册进入同一授权门。 |

`start`/`stop` 只操作 temporary background，不通过 system LaunchDaemon 表达，因此零 sudo。
`restart` 对 system persistent 走第 9.2 节的非特权路径，零 sudo。system `remove` 只允许在
已经 disable、且确认 root-owned artifact 不存在后删除用户态 Record/Connection，不再代替
`disable` 删除 system 注册，因此零 sudo。user remove 可删除剩余的用户态可重建 artifact。
`resolve --use system` 正常路径只读原生定义并写 Tunlite 配置/record，也不需要 sudo。
`status/list/logs/check/doctor/capabilities` 等纯读取不触发写授权；`rename` 保留旧 native
identity、`manage/unmanage` 只改变 Tunlite 所有权记录时，也不能为了省事无条件 sudo。

任何进入、离开或修改 macOS system Scope 的操作统一遵循：

```text
全部只读预检
  → 计算本批真实需要提权的动作和方向
  → TTY 展示批次数量、Connection 与 user↔system 方向
  → 整批一次 /usr/bin/sudo -v
  → 后续每步只用 /usr/bin/sudo -n -- <绝对路径命令>
  → 每个 Connection 独立事务写入、验证、提交/回滚
```

- dry-run 不授权、不调用 sudo、不写入。
- non-interactive 不调用 `sudo -v`，只允许外部预授权；失败必须在任何 stage/record 写入前返回。
- JSON 模式的授权提示只能写 stderr；stdout 只包含机器响应。
- 没有 TTY 时不得偷偷读 stdin 或让 sudo 自己接管不确定的终端。
- 授权是身份验证，不等同于产品二次确认。

### 9.2 system persistent 的无特权 restart

system persistent 的服务定义是 root-owned，不代表每次重启都需要修改 system domain。只要
Provider 被明确配置为当前非 root 用户运行，restart 必须使用以下受控流程：

```text
Inspect system job
  → 取得 PID、UserName 和原生 identity
  → 验证 UserName 是当前非 root 用户
  → 验证 PID 确实对应预期 Provider、Connection 和原生 identity，防止 PID 重用
  → 验证原生定义与 Record/ExecutionPlan baseline consistent，未漂移
  → 验证 KeepAlive 已启用，且其条件会拉起同一预期 Provider/Connection
  → 由当前用户直接向该 Provider 发送终止信号
  → 等待已有 KeepAlive 拉起不同 PID
  → 等待进程与健康状态收敛
  → 成功提交结果，或明确失败
```

所有验证必须在发出信号前完成。任一验证失败，包括进程归属不明、目标是 root/其他用户、
原生定义漂移、KeepAlive 未启用或无法证明会拉起预期 Provider，都必须零变更退出。该路径
不得调用 system domain 的 `bootout`、`bootstrap` 或 `kickstart`，不得调用 sudo，也不得在
验证失败后降级为 sudo。终止后 PID 没有变化、KeepAlive 未拉起或健康未在预算内收敛时，
restart 必须失败并给出可行动原因，不能把服务静默留在 stopped。

### 9.3 Monitor 中的 sudo

Monitor 只有在执行 system persistent 的 `disable`（以及未来从 Monitor 明确开放的 system
注册或配置替换）时才进入 sudo 流程：

1. 暂停刷新和键盘事件消费。
2. 退出 raw mode。
3. 恢复光标、换到干净行并停止备用屏控制。
4. 显示动作摘要和 sudo 提示。
5. 授权结束后重新进入 raw mode。
6. 全量重绘；失败也必须恢复终端。

任何 panic、Ctrl-C、sudo 失败或 context cancel 都必须执行终端恢复 defer。密码字符不得进入
Monitor 输入缓冲或日志。

temporary 的 start/stop、user persistent 的 disable 和任何 Scope 的 restart 都不进入此
流程。system restart 仍需产品确认并显示 `restarting`，但不退出 raw mode索要密码。

## 10. 确认策略

确认用于“键盘上容易误触且参数没有充分表达意图”的交互场景，不用于重复询问已经明确的 CLI
参数。建议终态：

| 场景 | 是否确认 | 理由 |
| --- | --- | --- |
| Monitor `x`：temporary background | 是，`y`；实际执行 stop | 单键误触会断流。 |
| Monitor `x`：user/system persistent | 是，使用更强文案并明确“停止并关闭自动启动”；实际执行 disable | 副作用大于普通 stop；只有 system disable 需要 sudo。 |
| Monitor `r`：temporary/user persistent/system persistent | 是，文案明确短暂中断且 mode、自动启动、Scope 不变 | 单键误触会断流；system restart 不触发 sudo。 |
| Monitor start | 否 | 可逆，且用户选中后按明确动作键。 |
| CLI `restart db` | 否 | 完整命令已表达对象和动作，重复确认破坏脚本。 |
| CLI `remove db` | 否，但必须先证明 stopped | 显式名称 + 状态门足够；提供 dry-run。 |
| `resolve --use ...` | 否 | 方向已显式，不应再问同一问题。 |
| `import --force` / `export --force` | 否 | force 本身是显式覆盖意图。 |
| system scope sudo | 不是产品确认 | sudo 只验证权限；动作摘要必须在授权前展示。 |

所有**产品二次确认**仅接受明确 `y/Y`；其他键取消。这里不包括 sudo 自己的密码输入或系统
认证交互。取消是未执行，不得留下 starting/stopping 临时状态。`--non-interactive` 永远不做
产品确认，也不能把“默认 yes”当作替代。

## 11. 中英文与 JSON 契约

### 11.1 人类界面

- 命令、flag、配置字段、错误 code 保持英文。
- 帮助、进度、状态标签、Monitor、确认和可执行建议提供 en/zh-CN。
- 同一概念使用固定翻译：Scope=范围、Autostart=自动启动、Runs as=运行位置、
  Restart=重启、Restarts=重启次数。
- 不直接翻译 launchd/systemd/provider 原始日志；必须标记来源。
- 英文避免 `enable it` 这种缺宾语文案，写成 `start and enable autostart`。
- 中文避免“开启/关闭”不说明对象，写成“启动连接”“关闭自动启动”。

### 11.2 JSON

- 每个非原始文档响应包含整数 `protocolVersion`。
- `outcome/code/state/action.type` 为稳定英文枚举，不受 `--lang` 或系统 locale 影响。
- 成功、失败、partial 都使用同一 Envelope 家族；批量永远保留逐项结果。
- stdout 不得出现进度、sudo 提示、ANSI、表格、确认或 provider 人类日志。
- 流式接口使用 NDJSON；第一帧声明协议、命令、Connection 和 stream 类型。
- 接收方必须忽略未知可选字段；破坏性语义才升级 protocolVersion。
- JSON 的 exit code 与 outcome 同时使用，不能要求调用方解析 message。

## 12. 安全约束

1. 当前 allowlist 允许 SSH `SetEnv`；P0 必须修复为 Preview 禁止，并以回归测试固定安全 option allowlist。
2. 不把 Secret 主动写入 argv、plist/unit、导出或诊断；身份文件只保存路径。
3. provider/原生日志是不可信外部输出，只能脱敏已知值，不能承诺识别全部 Secret。
4. 所有服务文件使用 argv 数组/原生转义，不通过 shell 拼接。
5. system scope 固定绝对路径 `/usr/bin/sudo` 与内层系统命令，禁止 PATH 劫持。
6. dry-run 必须做到零配置写入、零 marker、零服务 mutation、零启动、零 sudo。
7. `manage/resolve` 只接收可无损解析的原生服务；未知 key/wrapper/用户身份一律拒绝猜测。
8. 公开监听、远程转发、覆盖与重启风险进入 conditions；条件不是授权，也不保存“已批准”。
9. TUI 无论正常、失败或信号退出都恢复终端。

## 13. 实施优先级提案

### P0：进入公开 Preview 前

- 修正 `health=unknown` 被显示为“正常运行”。
- 人类界面缺少 Scope 证据时显示 `—/未知`，不默认 user；JSON 表达方式仍按第 14 节确认。
- status 详情并列 desired/actual mode，漂移给明确 actions。
- Monitor 行级错误隔离，一条坏 Connection 不拖垮整体。
- 为第 9 节枚举的全部 system 注册、解除注册和配置原子替换建立统一授权门，不只覆盖显式
  `enable --scope`；同时证明 `start`、`stop`、`restart` 和只读路径不会调用 sudo。
- 将 system restart 从当前特权 `runMutation` + `bootout/bootstrap` 改为“验证进程身份 →
  验证原生定义 consistent、KeepAlive 已启用且会拉起预期 Provider → 当前用户终止 Provider
  → KeepAlive 拉起 → PID 与健康收敛”；禁止 `kickstart` 和任何 sudo fallback，任一前置
  证明失败时零变更退出。
- Monitor 只有 system disable 等真实提权动作才在授权前退出 raw mode，授权后可靠恢复并
  重绘；system restart 保持在 TUI 中执行确认和无密码重启。
- `manage` 无 target 时强制真实 TTY；non-interactive/管道快速失败。
- 保持交互/认证所需退出码数值 4 不变，但把 capabilities 当前过窄的 `authenticationFailed`
  与 CLI Schema 一并对齐为覆盖“认证、选择或人工交互所需”的名称（建议
  `interactionRequired`）；命名迁移必须同步契约版本与测试，不能只改单侧文案。
- 所有耗时进度移动到完整参数预检之后。
- 固化全局 flag 支持矩阵和不支持组合测试；JSON/非 TTY 零 ANSI/TUI 控制序列。
- 独立完成 dry-run 边界：`check`、`doctor`、`logs`（尤其 `--follow`）必须在任何外部探测/日志流之前 exit 2；全部支持 dry-run 的写命令保持零副作用。
- `set` help/dry-run 明确 forwards/options 整表替换及可能自动 restart。
- `unmanage` 明确原生服务继续运行；`rename` 明确保留 native identity。
- 实现已接受的流式首帧契约：`logs --follow --json` 新增协议/stream 首帧，`run --json` 固定现有 starting 首帧并补回归。
- 完成既定人类界面的中英文双语一致性；单命令 help 是否采用统一副作用模板仍是待确认门，不伪装为已实现。
- 修复当前仍允许 `SetEnv` 的安全缺口，并验证 Preview 明确拒绝。

### P1：Preview 反馈后、稳定版前评估

- 决定 status 是否增加 `--fail-if unhealthy|attention`，默认 exit 0 是否长期固定。
- 评估持续纯文本观察的 `monitor --plain`，不改变 NO_COLOR 标准含义。
- 把所有单命令 help 从 usage 扩展为副作用摘要。
- 在 Status/Monitor 中统一 native identity、Scope、PID、UP、RESTARTS 的展示优先级。

### P2：稳定版后且有真实需求再做

- 外部 Driver 协议与独立 Driver 仓库。
- Windows 后台服务与发布支持。
- Desktop/通知/长期事件接口。
- 跨对象真正原子批量事务。
- 原生 identity 离线重建/改名工具。
- Shell completion 和额外本地化语言。

## 14. 需要用户明确确认的产品决策

在改代码前，需要逐项确认：

1. `rename` 是否正式承诺保留旧 native service identity 以换取零中断。
2. `status` 默认 exit 0 是否正式定义为“读取成功”；是否增加可选失败条件。
3. Scope 缺失时，JSON 是否升级 Schema 允许 `unknown`、使用可空字段，及何时升级协议；人类 P0 显示 `—/未知` 不再等待该决策。
4. Monitor 的停止 persistent 是否继续映射为 disable，并使用更强确认文案。
5. 是否需要 `monitor --plain`；确认 NO_COLOR 不承担禁用 TUI 控制序列的职责。
6. 单命令 help 是否采用固定“读取/写入/进程/自动启动/Scope/重启风险”摘要模板。
7. P0 清单是否全部作为 Preview 发布门禁，还是允许其中哪项延后并公开记录风险。

已接受而不再重开的决策：eager add、`set` 集合整表替换、批量/import 逐项 partial，以及
`run`/`logs --follow` 的流式首帧方向。

用户确认应形成一份带日期的决策记录。没有确认的项目不能由实现者自行选择后写成既定事实。

## 15. 用户确认后才执行的验收矩阵

本节只是未来验收计划。**在用户确认本文产品决策前，不执行以下 macOS 真机、sudo、
LaunchDaemon 或 Linux Docker 操作。**

### 15.1 macOS 真机

| 平台/场景 | user Scope | system Scope | 交互重点 | 通过标准 |
| --- | --- | --- | --- | --- |
| Intel Mac，装过 0.x | add/start/stop/enable/restart/disable/remove；登录恢复；真实 -L/-R/-D；SIGKILL 恢复 | user→system→user 迁移、回滚、冲突、权限失败 | 历史环境不应触发隐式兼容；进度、状态术语、旧 binary 路径 | 流量、PID、UP、RESTARTS、Scope、Autostart 与真实 launchd 一致；无残留。 |
| Apple Silicon 新机 | 从 Release Universal 2 安装；同一 user 生命周期和真实 SSH | 首次 sudo、LaunchDaemon 跨登录/注销、删除 CLI 后持续运行 | 新手文案、无变量安装流程、授权提示 | 无 Rosetta 依赖；用户能按文档独立完成；退出/失败恢复终端。 |
| Monitor + sudo | user 行与 system 行混合 | 对 system persistent 执行 `x`→disable；另测 system `r` | disable 时 raw mode → sudo → raw mode；restart 始终无密码 | 密码字符不进入 TUI，所有路径恢复光标和终端；restart 的 sudo 调用数为 0。 |
| 故障隔离 | 一条 plist/状态/日志故意损坏 | system 行权限不可读 | 一坏行不能拖垮 Monitor/status --all | 其他行继续刷新；坏行有 condition/action。 |
| Flag/语言 | en、zh-CN、NO_COLOR、非 TTY、JSON | sudo 提示与 JSON 同时存在 | stdout/stderr、ANSI、控制序列 | JSON 纯净；NO_COLOR 无颜色；非 TTY 不进入 TUI。 |

system Scope 真机验收必须在专用测试 Connection 上执行，记录 `/Library/LaunchDaemons`
精确目标和清理结果；不得用生产隧道承载验收自身的远程会话。

### 15.2 Linux Docker（PID 1 systemd）

| 场景 | 操作 | 通过标准 |
| --- | --- | --- |
| systemd user 基线 | add/start/stop/enable/restart/disable/remove | mode、autostart、PID、restarts 与 systemctl --user 一致。 |
| lingering/容器重启 | enable 后重启容器 | CLI 缺失时服务仍由 systemd 恢复；恢复 CLI 后可继续管理。 |
| 真实 SSH | -L/-R/-D 流量，kill provider | 新 PID 出现，三类流量恢复。 |
| Scope 边界 | `enable --scope system` | 稳定返回 unsupported，不执行 sudo，不写 system unit。 |
| 批量/partial | group/all，注入一个失败项 | 其他项按定义执行；JSON results 与 exit 5 正确。 |
| set/import | 运行时替换 forwards/options；两项 import 一项失败 | dry-run 展示重启与完整集合；实际 partial 与文档一致。 |
| 非 TTY/JSON | 所有自动化路径 | 无提示、无 ANSI、无 TUI；稳定 Envelope/NDJSON。 |

Docker 证明 Linux Adapter 的真实集成，但不能替代稳定版前至少一台非容器 Linux 的登录会话、
linger、真实 SSH 和发行包验收。

### 15.3 macOS system 授权缓存过期矩阵

每个真正的 root 入口都必须单独测试两种缓存过期，而不是只测一次
`enable --scope system`：

- **授权前已过期：** 完成只读预检后只出现一次 `sudo -v`，随后操作成功。
- **授权后、首个或后续写入前过期：** 内层 `/usr/bin/sudo -n -- ...` 快速失败，不在事务
  中途再次弹密码；已写步骤回滚，record 不提前提交。用户重试整条命令时才重新进入授权门。

| 入口 | 必测 system 场景 | 缓存过期通过标准 |
| --- | --- | --- |
| `set` | system persistent，执行计划变化并需原子替换注册后 restart | plist、进程和 record 全部保持旧版本或全部提交，不出现半更新。 |
| `enable` 无 `--scope` | 已有 system record | 不因缺少 flag 绕过授权；过期失败仍保持原 mode/scope。 |
| `enable --scope system` | 新建 system、user→system | 目标未健康前不删 user；过期时精确回滚目标。 |
| `enable --scope user` | system→user | 过期时不能留下 user/system 双进程或双 identity。 |
| `disable` | system persistent | 过期失败保持原运行、自动启动和 root artifact；成功则在同一事务中 bootout/disable 并删除 root artifact，保留 Connection、用户态 Record 和 system Scope 意图。 |
| `import --force` | 更新已有 system persistent，需原子替换注册后 restart | 每个导入项保持逐项事务；过期项失败，已完成项按 partial 契约保留。 |
| `resolve --use tunlite` | 用声明覆盖 system 定义 | 过期时保持旧基线和原生定义，或返回明确恢复阻断。 |
| Monitor `x`→disable | 对 system persistent 行确认停止并关闭自动启动 | 产品确认与 sudo 分离；失败保持原状态并恢复 TUI。 |

此外对每行再覆盖：用户取消 `sudo -v`、无 TTY、`--non-interactive` 无预授权、JSON stdout
纯净、批量第 N 个 `sudo -n` 失败。真实密码和缓存操纵只在用户批准的专用 Mac 上执行。

`start`、`stop`、`restart`、`remove`、`resolve --use system` 和相应 Monitor 动作另设零 sudo
断言，不进入缓存过期矩阵。system restart 必须覆盖：只有归属/identity 可证明、原生定义
consistent 且未漂移、KeepAlive 已启用并可证明会拉起预期 Provider 时，才无密码终止并等待
PID 改变；任一前置验证失败均零变更退出；终止后 KeepAlive 未拉起或健康不收敛时明确失败；
全过程不得调用 `bootout`、`bootstrap`、`kickstart` 或 sudo，且不得提供 sudo fallback。

### 15.4 26 个公开命令的验收覆盖

以下清单用于证明没有因为只关注生命周期而漏掉其他公共命令：

| 命令 | 最小验收 |
| --- | --- |
| `add` | 默认 22、显式 `-p`、`-N/-T`、`-o`、Group、eager materialization、冲突与 dry-run。 |
| `list` | 分组/未分组、filter、空列表、人类与 JSON。 |
| `set` | 标量 patch、forwards/options 整表替换、clear、运行时 restart、system 授权与 rollback。 |
| `rename` | 零中断、旧 native identity、重名/前台拒绝、record 回滚。 |
| `remove` | stopped/disabled 门、零 sudo、日志保留说明、dry-run 和失败回滚；system 必须先证明 disable 已删除 root artifact，remove 只删用户态 Record/Connection。 |
| `run` | 前台流量、锁冲突、Ctrl-C cancelled exit 0、provider 失败、JSON starting 首帧。 |
| `start` | stopped/background/persistent 矩阵、temporary background、starting pending、零 sudo、批量 partial。 |
| `stop` | temporary background/stopped/persistent 矩阵，persistent 拒绝并引导 disable；零 sudo。 |
| `enable` | 默认/保留 Scope、双向迁移、starting、统一授权、登录恢复。 |
| `disable` | persistent/background/stopped 矩阵，保留 Connection；user 零 sudo且可保留可重建 artifact；system 统一授权并原子停止、解除注册、删除 root artifact，保留用户态 Record/system Scope 意图。 |
| `restart` | stopped 拒绝、temporary/user persistent/system persistent、pending、PID 变化、mode/autostart/Scope 不变；system 在终止前验证身份、定义 consistent、KeepAlive 已启用且会拉起预期 Provider，任一失败零变更，零 sudo且禁用特权 fallback。 |
| `status` | 单项/group/all、5s/2s/8 预算行为、health unknown、scope unknown、actual/desired drift、exit 0。 |
| `logs` | 默认 100、边界 1/100000、stdout/stderr、follow、JSON/NDJSON 首帧、dry-run 早拒绝。 |
| `monitor` | 默认 1s、0.25/60 边界、导航、行级故障、确认、NO_COLOR、非 TTY；`x` 按 mode 映射 stop/disable，`r` 覆盖所有运行模式，system `r` 无密码，只有真实 root 动作验证 raw/sudo 恢复。 |
| `manage` | 显式 identity、真实 TTY 选择、`--name` 默认/覆盖、non-interactive/非 TTY exit 4、语法错误 exit 2、在运零中断。 |
| `unmanage` | 运行/自动启动保持、声明与 record 删除、交还失败回滚。 |
| `resolve` | 两个 `--use` 方向、diff/dry-run、restart；`--use tunlite` 的 system 写授权，`--use system` 正常路径零 sudo，及 recovery_blocked。 |
| `check` | 名称/临时 target、默认 12s、0.25/120 边界、认证 exit 4、dry-run 外部操作前 exit 2。 |
| `doctor` | 单项/全部、online/offline、默认 12s、0.25/120 边界、单项隔离、dry-run 早拒绝。 |
| `import` | 新增/force 更新、运行时 restart、system 授权、逐项 partial/exit 5、dry-run。 |
| `export` | 默认 raw stdout、Envelope、文件/force、`--output -` 与 JSON 互斥、dry-run。 |
| `validate` | 当前配置/文件、多错误、只读、人类与 JSON。 |
| `schema` | 默认 connection、五类 Schema、文件/force、raw stdout 与 JSON 互斥。 |
| `capabilities` | 平台/Driver/26 命令/user-system 能力、不可用原因、人类与 JSON。 |
| `help` | 总帮助/26 个单命令、中英文、usage 与已确认的副作用说明。 |
| `version` | 人类版本、JSON protocolVersion、`--version/-v`、无网络副作用。 |

## 16. 交付门槛

本文经用户确认后，实施仍需满足：

1. 先更新契约测试和帮助期望，再改实现。
2. 所有“当前行为”变化写入 Changelog；未实现的建议不写成 Release 能力。
3. 普通、race、vet、staticcheck、diff-check 全绿。
4. 机器契约、Skill、Schema 与 26 命令清单一致。
5. 完成用户批准的 macOS/Linux 矩阵并保存原始证据。
6. 独立 Reviewer 复审交互一致性、安全、回滚和文档真实性。
7. 未通过的项目明确标为 Preview 限制；不能用“应该可以”代替验证。
