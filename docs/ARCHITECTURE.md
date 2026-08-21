# Tunlite 架构设计

- **状态：** 已接受
- **适用范围：** Tunlite Core/CLI V1 及后续兼容演进
- **最后更新：** 2026-08-20

本文档与 `docs/PROJECT_DIRECTION.md` 并列为 Tunlite 的两份主文档。

- 产品设计定义为什么做、做什么、用户心智模型、公共命令和产品路线。
- 架构设计定义如何实现、模块边界、依赖方向、数据与事务、安全、性能、测试和代码风格。
- `docs/UPGRADE_FROM_0_X.md`、README、Agent Skill、命令帮助、Schema 和 Release Note
  都是派生资料，不能创造第三套产品或架构设计。

产品语义和范围发生冲突时，以产品设计为准；实现边界和工程约束发生冲突时，以本文档
为准。改变公共语义或核心架构边界的 Pull Request，必须同时修改相应主文档和契约测试。

本文使用“必须”“不得”表示强制约束，使用“应当”表示只有记录充分理由才能偏离的
默认选择，使用“可以”表示兼容边界内的实现自由。

## 1. 架构目标

V1 要交付的是一个可靠、可审计、适合人和 Agent 使用的 SSH Connection 管理器。它
同时要保留足够稳定的内部边界，使未来增加 Kubernetes、云身份隧道或其他 Provider
时，以增加 Driver 为主，而不是推翻 Core、状态模型和系统服务实现。

架构要长期守住五个目标：

1. **没有中心故障域。** Tunlite 不运行管理全部 Connection 的常驻 daemon。
2. **连接独立存活。** 后台 Connection 由操作系统逐条管理，已生成的直接执行服务不
   依赖 Tunlite 二进制继续存在。
3. **声明与执行分离。** Connection 描述用户意图，Driver 生成执行计划，Adapter 负责
   操作系统生命周期；三者不能互相越界。
4. **正常路径自动一致。** Tunlite 修改声明时，在同一事务内更新本机服务；只有用户
   绕过 Tunlite 修改系统产物后，才进入显式 `resolve`。
5. **公共契约可长期演进。** 人类输出可以改进，机器 Schema、状态维度、退出码、配置
   版本和模块依赖必须以兼容扩展或显式迁移为主。

## 2. V1 范围和非目标

V1 正式交付：

- 单文件 Go Core/CLI。
- 内置 SSH Driver，支持 `-L`、`-R`、`-D`、多 Forward、Jump Host、Identity File。
- 前台、临时后台和长期后台三种运行行为，以及停止状态。
- Linux systemd 用户服务和 macOS launchd LaunchAgent。
- 声明管理、原生服务管理、接管与交还、漂移解决、状态、日志、检查和诊断。
- 版本化 JSON 配置、机器输出、Schema、退出码、风险提示和 Agent Skill。
- 从冻结的 0.10.3 配置直接 `import`，新旧配置隔离，逐条切换和回退。

V1 不实现：

- 中心 daemon、IPC socket、全局 Supervisor 或 reconcile 循环。
- Windows Task Scheduler Adapter、Desktop、外部 Driver 安装体系和稳定 Driver 协议。
- Webhook、Shell 自动补全、自更新、自安装、自卸载、彻底清理命令。
- `migrate`、`sync`、`watch`、`discover`、`adopt`、`plugin`、`skill` 等额外入口。
- Tunlite 自有传输协议、账号、托管控制面或 Secret Store。
- 项目配置自动发现、全局 `--config` 和复杂的本机覆盖合并。V1 只保证 `import`、
  `export` 和 `validate`；项目声明、配置命名空间与本机覆盖的完整分层在真实团队需求
  出现后实现，且不得改变 Connection Envelope。
- SSH 私钥生成和远端 `authorized_keys` 修改。V1 不提供 `setup-key`，由系统
  OpenSSH 工具负责凭据初始化。
- Connection 保护状态。防止用户中断远程登录所依赖的 Connection 是未来待讨论问题；
  V1 不增加 `protected` 字段、命令或不可靠的自动依赖判断。

最后一项是范围收缩，不是否定产品设计中的长期项目声明模型。

## 3. 不可破坏的架构约束

### 3.1 进程与故障域

- 后台运行必须由受支持的 OS Service Adapter 实现；不得回退到 `nohup`、PID 文件、
  隐藏 Supervisor 或另一个 Tunlite daemon。
- 一个 Connection 的启动、崩溃、日志和重启必须与其他 Connection 隔离。
- `direct` 服务必须直接执行 Provider 程序，不能通过 `tunlite run` 或 CLI launcher。
- 未来 `managed` 模式只能使用按 Connection 隔离、固定版本、位于共享稳定运行目录的
  Driver runtime；不得演变成管理全部 Connection 的中心进程。
- 删除或替换 Tunlite 二进制不得停止已经运行的 Provider。

### 3.2 声明、计划与平台分层

- Core 公共模型只认识 Connection、Driver ID、ExecutionPlan 和标准状态。
- SSH 字段只能由 SSH Driver 解释，不能散落在 CLI、Application 或 Adapter 中。
- Driver 只决定“运行什么”，不得直接写 systemd unit 或 launchd plist。
- Adapter 只决定“在当前系统上如何注册、运行、观察和记录日志”，不得解析 Provider
  配置或判断 SSH 语义。
- ExecutionPlan 必须是 argv 结构；公共或内部接口不得用 Shell command string 表示
  Provider 执行。
- 生成的原生服务是可重建产物，不是可迁移事实来源。

### 3.3 产品语义

- 所有运行命令必须引用已存在、具名的 Connection。
- `add` 在支持平台上同时生成默认停止的原生服务定义。
- `run` 只前台运行，不修改原生服务的运行和自动启动状态。
- 正常修改自动联动服务，不提供日常 `sync`。
- `manage` 同时完成发现、验证和接管；不得要求用户先执行另一个 discover 命令。
- `unmanage` 交还原生服务并保留其当前状态，不中断 Provider。
- 删除运行中的 Connection 必须拒绝；不得用 `--force` 偷偷停止用户连接。
- `stop` 必须拒绝 persistent Connection，并明确引导用户使用 `disable`。

## 4. 总体结构

```text
人类 / 脚本 / Agent / 未来 Desktop
                │
                ▼
        CLI / Machine Adapter
      参数、帮助、颜色、JSON、渲染
                │
                ▼
       Application Use Cases
   事务、风险、幂等、批量、锁和回滚
        │          │             │
        ▼          ▼             ▼
 Connection     Driver       Service Record
   Store        Registry         Store
                   │               │
                   ▼               ▼
             ExecutionPlan   OS Service Adapter
                              systemd / launchd
                   │               │
                   └───────┬───────┘
                           ▼
                   ssh / future Provider
```

前台运行与后台运行共享同一个 Driver 和 ExecutionPlan：

```text
run   -> Foreground Executor -> Provider
start/enable -> OS Service Adapter -> Provider
```

两条路径不能各自实现一套 SSH 参数生成或风险判断。

## 5. 模块边界和依赖方向

目标 Go 包结构：

```text
cmd/tunlite/                 极薄的进程入口
internal/cli/                参数解析、人类输出、JSON 输出、帮助
internal/app/                Use Case、事务、批量、风险和依赖编排
internal/core/               Connection、Driver Port、通用校验和条件
internal/plan/               Provider 无关的 ExecutionPlan
internal/drivers/ssh/        内置 SSH Driver
internal/service/            Adapter Port、状态求值、服务记录
internal/service/systemd/    systemd 实现，允许先保留在 service 包内再拆分
internal/service/launchd/    launchd 实现，允许先保留在 service 包内再拆分
internal/configstore/        ConnectionList 原子存储
internal/filelock/           短事务跨进程文件锁
internal/processlock/        前台运行存活锁
internal/execution/          前台进程执行
internal/paths/              规范路径
schemas/                     随版本发布的机器契约
skill/use-tunlite/           从契约派生并接受测试的 Agent Skill
```

强制依赖方向：

```text
cmd -> cli -> app -> core / plan
                 -> Driver Port / Adapter Port / Stores
ssh driver -> core + plan
systemd/launchd -> plan + service types
core + plan -X-> cli / app / ssh / systemd / launchd
```

约束：

- `cmd/tunlite` 只负责传入参数、标准流、版本和退出码。
- `internal/cli` 不应长期拥有事务、回滚、Adapter 选择和 Store 编排；这些逻辑迁入轻量
  `internal/app`，供 CLI 和未来 Desktop 共用。
- `internal/app` 不是框架，不建立事件总线、依赖注入容器或长期运行对象；它只是显式
  依赖组成的应用服务。
- 依赖通过 `App` 或 `Dependencies` 实例注入，不使用包级可变全局变量替换 Adapter、
  Probe 或 Runner。
- 接口应由使用方定义，保持最小；不得为“以后也许需要”建立大而全的抽象。
- Driver Registry 是唯一的 Driver 查找入口。核心 Use Case 不得按 Driver ID 写分支。

## 6. 核心数据模型

### 6.1 ConnectionList

公共配置文件是一个整体版本化的 `ConnectionList`：

```json
{
  "configVersion": 1,
  "kind": "ConnectionList",
  "items": [
    {
      "kind": "Connection",
      "metadata": {
        "name": "staging-db",
        "tags": ["staging", "database"]
      },
      "spec": {
        "driver": "io.tunlite.ssh",
        "config": {
          "host": "deploy@app01",
          "identityFile": "~/.ssh/id_ed25519",
          "forwards": [
            {
              "type": "local",
              "bind": "127.0.0.1",
              "srcPort": 5432,
              "destHost": "localhost",
              "destPort": 5432
            }
          ]
        }
      }
    }
  ]
}
```

规范：

- `configVersion` 只在文档根节点出现一次，不能在每个 Connection 中重复。
- Core 校验公共 Envelope，Driver 校验 `spec.config`。
- Connection 声明不保存 foreground、background、persistent、PID 或平台服务名。
- `metadata.tags` 是无序、去重的字符串集合；它只用于选择和批量操作，不形成另一种
  Group 资源，也不承载生命周期或共享配置。
- 配置读取必须拒绝未知字段、混合版本和不支持的更高版本。
- 只读命令不得改写配置；真正需要新格式的首次写操作才允许按产品设计执行版本升级。
- 输出顺序必须确定；同一输入不应因 map 遍历产生无意义 diff。

Tag 选择语义从 V1 起固定：

- 修改命令必须明确选择一个 Connection 名称、一个或多个 `--tag`，或者 `--all`。
- 多个 `--tag` 使用 AND 语义，只选择同时包含全部 tag 的 Connection。
- `--all` 是显式批量标志，不把字符串 `all` 保留成魔法 Connection 名称。
- `list`、无参数 `status` 和无参数 `monitor` 是只读查询，默认展示全部。
- `set <name> --tag ...` 替换该 Connection 的完整 tag 集合；import/export 原样保留。

### 6.2 SSH Driver 配置

SSH 配置至少表达：

- Host，可包含用户；单独 SSH Port。
- Identity File 和 Jump Host。
- local、remote、dynamic Forward；端口和地址为结构化字段。
- 多个 Forward。
- 经过允许列表校验的额外 OpenSSH `Key=Value` 选项。

Driver 必须生成固定安全基线，包括非交互后台执行所需的 BatchMode、Forward 建立失败
退出和 Keepalive 策略。用户选项不能覆盖这些不变量。

### 6.3 ExecutionPlan

ExecutionPlan 是 Driver 与执行环境之间唯一的 Provider 无关边界：

```text
mode                direct | managed
exec                program、args[]、environment、workingDirectory
lifecycle           restart、restartDelay、stopTimeout
endpoints[]         名称、方向、协议、地址
healthChecks[]      类型、地址、超时
redactions[]        需要脱敏的来源
```

要求：

- 相同的标准化 Connection 和 Driver 版本必须生成相同计划。
- `exec.program` 与每个参数独立传递；不得经过 Shell 拼接。
- Environment 只保存运行必需的非 Secret 值或安全引用；不能把解析后的 Secret 写入产物。
- Adapter 在消费前必须调用通用计划校验。
- 计划摘要使用稳定序列化与加密摘要，用于漂移判断和 dry-run 前后对照。
- V1 只交付 `direct`，但模型和校验保留 `managed`；未经验证不得公开 managed 能力。

### 6.4 Service Record

Service Record 是权限受限的本机协调状态，不属于可迁移 Connection：

```text
recordRevision
connection
serviceKey
adapter
mode
artifactPath / unitName / logPaths
planDigest / baselineDigest
declarationPath / origin
```

它的用途是快速定位已接管的原生服务并保存一致性基线，避免每次命令都扫描
全部系统服务。它不是一份隐藏的第二配置：Provider 意图必须能从 Connection 重新生成。

内部 `recordRevision` 可以独立演进，不能被宣传成第五种公共产品版本。

## 7. 文件与所有权

默认路径：

```text
配置：${XDG_CONFIG_HOME:-~/.config}/tunlite/connections.json
状态：${XDG_STATE_HOME:-~/.local/state}/tunlite/core/
服务记录：<状态目录>/services/<connection>.json
```

macOS 当前同样使用以上 XDG 兼容路径；设置 `TUNLITE_HOME` 时使用其下的
`config/connections.json` 和 `state/core`。所有路径解析集中在 `internal/paths`，
其他包不得自行拼接用户目录。

所有权规则：

- ConnectionList 是 Tunlite 声明的事实来源。
- 原生服务文件由对应 Adapter 生成和更新。
- Service Record 由 Core 管理，用户不需要手工编辑。
- 原生服务日志由操作系统或 Adapter 指定的确定性文件拥有。
- 0.x 配置始终只读，V1 不删除、不覆盖。
- 发行渠道拥有二进制；Core 不删除或更新自己的程序文件。
- `unmanage` 后原生服务归还系统，Tunlite 删除自己的 Connection 和 Service Record，
  但不得删除或停止被归还的服务。

敏感文件和状态目录默认权限必须限制为当前用户。原子写必须在同一目录创建临时文件，
写入、同步、关闭后 rename；失败时保留旧文件。

## 8. Driver 契约

V1 内部最小 Driver Port 包含：

- 稳定 Driver ID。
- Connection 语义校验。
- 生成 ExecutionPlan。
- 可选的认证/依赖 Check。
- 可选的 Inspect 和 Diagnose。
- 将可无损识别的原生服务候选还原为 Driver 配置。

长期外部 Driver 仍采用进程外、版本化 JSON 协议，但 V1 不公开安装命令，也不冻结
稳定协议。内置 SSH Driver 必须经过同样的逻辑边界；测试用假 Driver 必须证明 Core、
Application 和 Adapter 没有 SSH 特判。

Driver 方法边界：

- validate、schema、plan、inspect、diagnose 必须无副作用。
- 身份登录、建立凭据和创建远程资源必须是独立的显式操作。
- Driver 产生的远程资源未来必须有 OwnershipRecord；删除 Connection 默认不得删除
  远程资源。
- Driver 不接收 Shell，也不能要求 Core 执行错误字符串中建议的命令。

## 9. OS Service Adapter 契约

Adapter 最小能力：

| 能力 | 责任 |
| --- | --- |
| `Capabilities` | 如实报告注册、后台、长期运行、日志和发现能力 |
| `Build` | 把 ExecutionPlan 编译成平台原生产物 |
| `Materialize` | 原子写入默认停止的服务定义 |
| `Remove` | 停止并删除 Tunlite 受管服务定义 |
| `Start/Stop/Restart` | 操作临时后台实例，或保持当前长期模式重启 |
| `Enable/Disable` | 原子进入或退出 persistent 模式 |
| `Inspect` | 返回安装、自动启动、进程、产物和平台观测 |
| `Logs` | 读取确定的原生日志源 |
| `Discover` | 显式枚举可接管候选 |

所有 Adapter 必须通过同一套契约测试：原子生成、幂等操作、状态转换、失败回滚、日志、
候选发现、不中断接管和交还后独立运行。

平台约束：

- systemd 使用用户级 unit，不假设所有 Linux 都有可用的 `systemctl --user`。
- launchd 使用用户 LaunchAgent，不静默创建需要管理员权限的 LaunchDaemon。
- 没有合格 Adapter 的平台只支持 `run`，后台命令返回结构化“不支持”。
- Windows 支持只能在 Task Scheduler Adapter 完成真实 Windows 验证后宣布。

## 10. 运行方式和状态机

公共运行方式只有四种：

| 模式 | 含义 | 是否附着终端 | 自动启动 |
| --- | --- | --- | --- |
| `stopped` | 当前未运行 | 否 | 否 |
| `foreground` | `run` 前台运行 | 是 | 否 |
| `background` | 原生服务临时后台运行 | 否 | 否 |
| `persistent` | 原生服务后台运行并在用户会话恢复 | 否 | 是 |

命令状态转换：

| 命令 | 允许的来源 | 结果 | 关键约束 |
| --- | --- | --- | --- |
| `add` | 不存在 | stopped | 同时生成默认停止的服务定义 |
| `run` | stopped | foreground，退出后 stopped | 不改变原生服务设置 |
| `start` | stopped | background | 不开启自动启动 |
| `stop` | background | stopped | persistent 必须拒绝并提示 disable |
| `enable` | stopped/background | persistent | stopped 会启动；background 不重启地转长期 |
| `disable` | persistent | stopped | 同时取消自动启动并停止 |
| `restart` | background/persistent | 保持原模式 | 不改变自动启动语义 |
| `remove` | stopped | 不存在 | 先安全删除服务，再删除声明 |

对已处于目标状态的操作必须幂等成功，不能把重复命令当作异常。无法证明当前状态时，
修改命令应拒绝并引导 `status`/`resolve`，不能猜测。

`foreground` 由仍存活的运行锁观测，不写入 Service Record。系统原生进程状态始终来自
Adapter，而不是 Tunlite 保存的 PID。

## 11. 五维状态模型

状态必须分开表达：

```text
serviceState      registered | absent | unsupported | unknown
mode              stopped | foreground | background | persistent | unknown
processState      inactive | starting | running | failed | unknown
health            unknown | healthy | unhealthy
consistencyState  consistent | drifted | missing | blocked
```

这些维度不能合并成一个枚举。例如：

- persistent + running + unhealthy：服务长期运行，但本地端点不健康。
- background + failed：临时后台服务启动失败。
- registered + drifted：服务仍存在，但声明或系统产物被绕过修改。
- absent + missing：声明存在，但原生服务定义丢失。
- unknown + blocked：当前平台或权限无法可靠观察，不能伪造结果。

每个状态结果还必须包含 `observedAt`、endpoints、结构化 conditions 和可选 platform
信息。平台原始字段只能作为附加诊断，不能替代公共维度。

## 12. 核心数据流

### 12.1 Add、Set、Import

```text
解析输入 -> 规范化 Connection -> Driver 校验 -> 生成计划 -> 风险分析
        -> 预览/风险提示 -> 获取短事务锁 -> 生成原生产物
        -> 原子更新声明和 Service Record -> 完成或完整回滚
```

- `--dry-run` 必须完成到“即将产生什么变化”的阶段，但不写文件、不注册、不启停。
- `add`、新项 `import` 默认形成 stopped 服务。
- 修改 background/persistent Connection 时，必须在同一事务内更新服务并只重启确实需要
  重启的 Connection；中断影响要出现在计划中。
- 批量 import 逐项返回结果；解析源文件失败时整体拒绝，不能生成半份未知配置。

### 12.2 Manage

```text
用户执行 manage -> Adapter 显式发现候选 -> 过滤已受管服务
  -> Driver 判断能否无损还原 -> 用户选择并补充必要字段
  -> 预览/风险提示 -> 原子写入 Connection + Service Record
  -> 保留原生服务身份、当前进程和自动启动状态
```

只有明确执行 `manage` 才扫描候选。普通 `list`、`status` 和生命周期命令必须使用
Service Record 直接定位服务，不能每次扫描全系统。

不能无损还原、依赖 Shell、包含未知 Secret 或不满足可靠重启语义的候选，必须明确拒绝
接管；不能“先接管再说”。

### 12.3 Unmanage

```text
检查一致性 -> 确认原生服务可脱离 Tunlite 独立运行
  -> 保持进程和自动启动状态 -> 删除 Tunlite 声明与 Service Record
```

`unmanage` 既适用于接管来的服务，也适用于 Tunlite 创建的服务。它是管理权转移，不是
删除或停止。存在未解决漂移、产物仍依赖 Tunlite 二进制或无法确认服务完整时必须拒绝。

### 12.4 Resolve

正常修改不需要 resolve。只有声明、Service Record 基线和原生产物出现异常不一致时：

- `resolve <name> --use tunlite`：以 Connection 为准重建原生服务并更新基线。
- `resolve <name> --use system`：由 Driver 从当前系统产物还原 Connection，并更新基线。

方向必须由用户显式选择。无法无损反向还原时不得提供 `--use system` 的假成功。

## 13. 事务、回滚与锁

所有涉及“配置 + 原生服务 + Service Record”的修改都是一个应用事务。外部系统没有
通用数据库事务，因此实现采用 staged mutation 和补偿回滚：

1. 读取并保存变更前快照。
2. 构建新产物和回滚所需信息。
3. 在短锁内按可恢复顺序执行。
4. 任一步失败，恢复原生服务、Service Record 和 ConnectionList。
5. 如果补偿也失败，返回 blocked 状态、已完成动作和人工恢复指引，不得谎报回滚成功。

锁分层：

- **配置版本门禁：** 未来第一次引入 `configVersion` 原子升级时，普通操作持共享侧，
  公共配置版本升级持排他侧；V1 尚无第二种正式格式，因此不预建这层机制。
- **配置文件锁：** 串行化同一 ConnectionList 的短写事务。
- **每 Connection 服务锁：** 防止同一 Connection 并发启停或改服务。
- **服务绑定锁：** 防止同一原生服务被两个 Connection 同时接管。
- **前台运行锁：** 利用操作系统释放语义判断 `run` 是否仍存活。

锁只保护协调和提交，不覆盖 Provider 长期运行、`logs --follow`、`monitor` 或远程认证
等待。`run` 读取快照后必须释放配置版本门禁，只保留对应 Connection 的运行锁。

V1 没有第二种正式公共配置格式，不预建通用配置迁移门禁。真正设计
`configVersion 2` 时，必须根据实际转换规则再设计短期排他协调；它不属于 0.x import，
也不能覆盖 Provider 生命周期。

## 14. 状态、日志和性能预算

规模目标是几十到上百个 Connection。未运行 Connection 只能占用配置、Service Record
和原生服务元数据，不得产生常驻 Tunlite 进程。

查询边界：

- `list` 只读 ConnectionList，不调用服务管理器，不探测网络。
- 单 Connection `status` 使用记录直接定位并观察一个服务。
- 全量 `status` 和 `monitor` 只做本机观察；不得执行远程 SSH 认证或 Provider 登录。
- `check` 做单 Connection 的依赖和认证检查。
- `doctor` 做更深诊断，并明确区分 offline 与需要网络的检查。
- `logs` 直接读取 Adapter 日志源，不经过 IPC 或中心 Fan-out。

实现预算：

- 状态采集必须有整体截止时间、单项超时和有限并发。
- 默认最大并发应为一个小的固定上限，可根据平台测试调整，不能为每个 Connection
  无限制创建 goroutine 或子进程。
- 一个服务管理器调用或 TCP 健康检查超时不能串行阻塞后续所有 Connection。
- 整体截止后未完成项返回 unknown/blocked 和结构化原因，不能拖住整个命令。
- `monitor` 复用同一批量观察器和预算，不能每次刷新堆叠上一轮未完成任务。
- 深度远程健康检查不进入 `status`；本地 TCP 探测若保留，必须有限并发且服从整体预算。

V1 Preview 的具体毫秒值由真实 macOS、Linux 以及 100 Connection 基准测试确定，不能
在没有测量时把某个偶然超时固化为长期公共契约。

## 15. 安全与风险

### 15.1 基础安全边界

- 所有 Provider 执行使用 program + args，不经过 Shell。
- 日志、JSON、错误、dry-run、Service Record 和原生产物不得泄露 Secret。
- 配置和状态默认只允许当前用户读取。
- 非交互模式不得等待密码、确认或 TTY；需要交互时返回结构化操作建议。
- Core 不生成 SSH 私钥，也不修改远端 `authorized_keys`。认证初始化使用
  `ssh-keygen`、`ssh-copy-id` 或文档给出的系统 OpenSSH 操作。
- 插件未来就是用户机器上的任意本地代码，不能宣传不存在的沙箱保证。

### 15.2 SSH 额外选项

`--ssh-opt` 不能仅检查 `Key=Value` 语法。V1 必须拒绝以下类别：

- 执行本地或远程辅助命令的选项，例如 `ProxyCommand`、`LocalCommand`、
  `PermitLocalCommand`。
- 加载额外配置并绕过声明边界的选项。
- 在选项中另行声明 Forward，从而绕过结构化 forwards 和风险检查。
- 覆盖 BatchMode、ExitOnForwardFailure、Keepalive 等 Tunlite 安全与可靠性基线的选项。
- 不能安全、确定地序列化和回读的选项。

SSH 跳板应使用结构化 Jump Host 或安全的 OpenSSH 参数，不以允许任意 ProxyCommand
替代。0.x import 遇到不安全选项时整体拒绝并指出具体 Connection 和选项。

### 15.3 高风险提示

远端转发和公开监听可能扩大网络暴露面。V1 必须在 `add`、`set`、`import`、`manage`
和 `--dry-run` 结果中返回明确的 endpoint、风险等级和 warning，但不提供 `--approve`，
不保存批准状态，也不要求用户重复表达已经通过参数或声明明确给出的意图。

未来安装任意插件、创建云端资源或删除远程资源时，需要重新设计独立的授权契约；不得
把尚未验证的未来授权模型提前塞进 V1 SSH 生命周期。

## 16. CLI、JSON 和 Agent 契约

V1 公开命令只允许来自产品设计列出的 26 个一级命令。命令元数据必须有单一来源，并
驱动或验证：

- 顶层帮助和单命令帮助。
- 拼写建议。
- `capabilities --json`。
- Agent Skill。
- 文档中的命令清单。
- JSON Schema 与契约测试。

机器契约要求：

- `--json` stdout 只输出 JSON/NDJSON，诊断不能混入。
- `--non-interactive` 强保证不会等待输入。
- 修改命令支持确定性的 `--dry-run` 和幂等执行。
- 错误包含稳定 code、message、riskLevel 和结构化 actions。
- 批量操作逐项报告 succeeded/failed/skipped，并以明确退出码表达部分失败。
- JSON 字段兼容扩展；删除、改名或改变语义必须提升对应契约版本。
- 每种稳定响应不能只依赖一个 `results: any[]` 的宽泛 Envelope；应有可验证的命令结果
  Schema 或受约束的联合类型。
- `capabilities` 是运行时能力查询来源，调用方不能只根据软件版本猜平台能力。

人类输出要求：

- 默认使用清楚的颜色、对齐表格和可操作提示。
- 非 TTY、`--json` 或 `NO_COLOR` 时不输出 ANSI。
- `monitor` 是唯一实时交互总览；不增加 `watch` 别名。
- 人类界面用“状态 / 运行位置 / 登录自启”解释生命周期；`foreground`、`background`、
  `persistent` 保留为机器契约值，不要求新手先理解内部模式。
- 人类文案首批支持中文和英文；语言优先级为 `--lang`、`TUNLITE_LANG`、系统语言、英文
  回退。命令、配置、JSON 字段与枚举、错误码保持英文，原生工具日志不翻译。
- 文案使用稳定的 Connection、接管/交还等产品词汇；不得让内部 Record、digest 或 lock
  成为日常概念。

## 17. 0.x 直接导入

0.x → V1 是历史输入兼容，不是永久迁移子系统：

```text
0.x config（只读） -> legacy decoder -> V1 ConnectionList -> 正常 import 事务
```

约束：

- 入口只有 `tunlite import <旧配置>`，不增加 `migrate`。
- 旧配置和 V1 配置使用不同路径。
- 导入不停止旧 daemon、不启停旧隧道、不卸载旧程序、不替换当前命令。
- 导入后的所有 Connection 都是 stopped，避免新旧进程冲突。
- 只转换有明确同义语义的 SSH 字段；settings、Webhook、enabled 和 autoSetupKey 给出
  明确警告，不伪造迁移。
- 源文件解析有任何不确定性时整体拒绝；目标同名项按 import 规则逐项报告。
- V1 不建立迁移日志、双向同步、自动回滚引擎或 0.x 自动清理。
- 完整用户流程以 `docs/UPGRADE_FROM_0_X.md` 为准，并由真实 0.10.3 fixture 和端到端测试
  保护。

`configVersion` 的未来原子升级与 0.x import 是两件事，代码和文案不得混用“迁移”。

## 18. 发行与二进制独立性

Core/CLI 内部仍按 OS 和 CPU 构建原生二进制；用户可见的 Release 收敛为三个归档：

```text
macos       # Universal 2，同时包含 x86_64 和 arm64
linux-x64
linux-arm64
```

用户可见的发布包名称不使用容易被误解为 CPU 厂商的 `amd64`；`x64` 在构建时映射为
Go 的 `GOARCH=amd64`。机器协议中的 `platform.arch` 继续使用 Go 标准架构值，不把展示
命名混进协议。macOS 必须先分别构建两个原生切片，再合并、校验、签名和公证最终的
Universal 2 文件。Linux 没有通用二进制机制，因此继续区分 x64 和 arm64。

发行产物必须附 checksum，并逐步加入签名和构建证明。包管理器、Desktop 或手工安装
流程各自拥有其安装的二进制，Core 不提供 `install`、`update`、`uninstall`。

原生服务不得指向容易被包管理器替换或删除的 Tunlite CLI。`direct` 服务指向用户已
安装的 Provider；未来 managed runtime 位于共享稳定目录，并按 Connection 引用计数。

Core version、未来 Desktop version、`configVersion`、`protocolVersion` 和 Driver
协议/软件版本相互独立，不能复用一个数字表达不同兼容关系。

## 19. Go 代码与产品风格

### 19.1 Go 代码风格

- 遵循标准 Go 风格、`gofmt`、小接口、显式错误和 context 取消。
- 业务错误使用稳定类型或 code；不能靠解析错误字符串决定流程。
- 不使用 `panic` 处理用户输入、平台缺失或 Provider 失败。
- 不在库代码调用 `os.Exit`；退出码只在进程边界决定。
- 不使用包级可变全局作为依赖注入或测试开关。
- 文件系统、时间、命令执行、网络探测和 Adapter 都通过最小依赖边界测试。
- 并发必须有上限、取消和收敛；不得泄漏 goroutine、子进程或日志 follow。
- 公共 JSON 结构使用明确类型；仅平台附加信息等真正开放字段使用 `map[string]any`。
- 注释解释不变量和原因，不复述代码；历史兼容代码标明输入范围和删除条件。
- 新抽象必须消除已存在的重复或形成已确认边界，不能只为想象中的未来建立框架。

### 19.2 产品实现风格

- 先给用户结果和下一步，不暴露内部实现名词。
- 正常路径少步骤，危险路径明确计划和风险，异常路径明确事实来源。
- 同一意图只保留一个公共入口；别名只有形成行业惯例且无语义歧义时才考虑。
- 不因为平台做不到就伪造成功；返回 unsupported/unknown 比隐藏降级更可信。
- 不自动安装 Driver、修改 Shell、登录账号、创建远程资源或开放监听。
- 不删除已有能力，除非产品设计明确记录取舍和替代方案。

## 20. 测试架构和发布门禁

测试分层：

1. **纯单元测试：** Connection、SSH 配置、ExecutionPlan、状态求值、摘要和选择逻辑。
2. **Store/事务测试：** 原子写、并发、失败注入、回滚、锁和版本门禁。
3. **Adapter 契约测试：** 同一套 Fake 合约覆盖 systemd、launchd 和未来 Adapter。
4. **CLI 黑盒测试：** argv、stdout、stderr、退出码、最终配置和服务状态。
5. **机器契约测试：** Schema、capabilities、Skill、示例与运行时一致。
6. **真实 Provider E2E：** OpenSSH local/remote/dynamic、多 Forward、断线恢复和日志。
7. **真实平台验收：** Intel/Apple Silicon macOS、systemd Linux 的安装、启停、登录恢复、
   manage/unmanage/resolve 和 0.x 逐条升级。
8. **规模与故障测试：** 100 Connection 状态预算、服务管理器超时、部分失败、进程终止、
   CLI 二进制删除后服务存活。

V1 Preview 发布必须满足：

- `go test ./...` 全部通过，无已知失败被当作“与发布无关”。
- 26 个命令在帮助、capabilities、Schema、Skill 和主文档中一致。
- 真实 OpenSSH 转发与 systemd/launchd 生命周期通过。
- 0.10.3 fixture 的 dry-run、导入、逐条切换和回退通过。
- 高风险 endpoint 被明确提示，危险 SSH option 被拒绝。
- 彩色/无色、人类/JSON、TTY/非 TTY 输出均通过契约。
- 构建产物、checksum、版本号和升级指南一致。

## 21. 禁止模式

以下设计不得进入 Core：

- 管理全部 Connection 的中心 daemon、全局 Supervisor 或常驻 reconcile loop。
- 通过 Shell command string 执行 Provider。
- Adapter 解析 SSH/Kubernetes/AWS 字段，或 Driver 写原生服务文件。
- 正常修改后要求用户再运行 `sync`。
- 每次 status 扫描全部系统服务或执行远程认证。
- 用 PID 文件作为真实进程状态。
- 运行中 remove、隐式 enable、SSH 私钥生成、远端密钥修改、隐式插件安装或隐式远程
  资源清理。
- 用 checksum 冒充发布者身份或插件安全沙箱。
- CLI、Desktop、Skill 和 README 各自维护业务规则。
- 为一次 0.x 导入建立永久 migrate、日志、回滚和清理子系统。
- 在没有真实多 Provider 验证前冻结稳定外部 Driver 协议。

## 22. 当前实现与目标差距

以下盘点基于 `rewrite/v1-go` 分支 2026-08-22 的工作区。公共模型和命令面已经按本文
收口，剩余工作集中在内部边界、规模预算和真实平台验证。

| 领域 | 当前情况 | V1 必须完成的变化 |
| --- | --- | --- |
| 核心分层 | Connection、Driver、Plan、Adapter 边界已存在；CLI 包仍承担业务用例编排 | 保持逻辑边界；物理提取 `internal/app` 不阻塞 Preview，在 Desktop 或第二个 Driver 前渐进完成 |
| 依赖注入 | Adapter 已通过每次调用显式 factory 注入，不再使用包级可变全局 | 后续提取 Application 层时保持实例级 Dependencies，不退回全局替换 |
| 无 daemon | Go 后台链路已使用逐 Connection systemd/launchd；Intel Mac 和 Linux systemd 容器已证明移走 CLI 后不断流，Linux 容器在 CLI 缺失时重启仍恢复 | 补非容器 Linux 和 Apple Silicon 的同等验收 |
| 运行语义 | 四种模式、完整转换矩阵、幂等、冲突和失败补偿测试已完成；Linux systemd 容器已证明跨重启恢复 | 继续补非容器 Linux 登录恢复和长期运行证明 |
| 接管/交还 | manage 会显式扫描并过滤受管项，unmanage 保留原生服务；Intel Mac 与 Linux systemd 容器已连续证明接管与交还不改变 Provider PID | 补非容器 Linux、Apple Silicon 和以系统为准的 resolve 漂移验收 |
| 风险提示 | 已删除 V1 approval 状态；公开监听和 Publish 返回结构化 warning | 保持风险提示测试，不引入批准持久态 |
| 状态性能 | status/monitor 共用 5 秒整体、2 秒单项、最大并发 8 的观察器；100 Connection 自动测试已覆盖并发上限和超时降级 | 在 macOS、Linux 真机记录 100 Connection 数据，再决定是否调整内部预算 |
| SSH options | 已使用保守允许列表拒绝执行命令、额外 Forward、配置加载和可靠性覆盖；0.x import 复用同一校验 | 用真实用户配置持续验证允许列表，新增选项必须先证明可安全序列化和回读 |
| 配置分层 | 已删除 `--config`，只使用规范 ConnectionList 路径 | 项目声明和本机 override 延期到真实需求出现 |
| 机器 Schema | 通用 Envelope、错误、Condition、服务结果和 import 结果已使用受约束联合类型；流式日志已纳入 outcome | 继续为仍只有顶层字段约束的稳定响应增加契约 fixture，不能退回 `any[]` |
| 命令契约 | 已收敛为 26 个一级命令，Skill 接受运行时契约测试 | 持续保证帮助、Schema、文档和 capabilities 同步 |
| 0.x 导入 | 已实现只读转换、独立路径和 stopped 导入，并固化真实 0.10.3 四 Connection export fixture | 完成真实旧 daemon 逐条切换和回退验收，继续保留旧文件不变 |
| 配置演进 | V1 没有第二种正式配置格式，也没有运行时 migration gate | 真正设计 configVersion 2 时，按产品设计第 11.1 节实现原子备份、转换和短期排他协调 |
| 测试现状 | 单元、黑盒、契约、竞态、真实 OpenSSH 三类转发、Intel Mac launchd，以及 Ubuntu 24.04 PID 1 systemd 容器的崩溃恢复、跨重启、CLI 移除/替换、漂移和接管/交还零中断已通过；真实 0.10.3 切换回退已通过 | 补非容器 Linux、Apple Silicon、真实登录会话和长期运行门禁 |

已经基本符合且应保留：无中心 daemon、逐 Connection 原生服务、direct 服务不依赖
Tunlite 二进制、通用 ExecutionPlan、内置 SSH Driver 边界、四种运行方式、五维状态、
日志/monitor/doctor/颜色基础、显式 manage/unmanage/resolve 和新旧配置路径隔离。

## 23. 实施顺序

用户确认本文档后，代码按下面顺序推进；每一步都先保持测试可运行，不并行引入新的
公共能力：

1. **契约收口：** 持续验证 26 个命令、帮助、README、Schema、Skill 和升级文档一致。
2. **安全收口：** 拒绝危险 SSH options，补充 legacy import 和风险提示测试。
3. **边界收口：** 保持 Application Use Case 的逻辑边界；物理提取 `internal/app` 不作为
   Preview 阻塞项，在 Desktop 或第二个 Driver 前渐进完成。
4. **事务收口：** 覆盖 add/set/import/remove/rename/manage/unmanage/resolve 的失败回滚。
5. **状态收口：** 有限并发、整体预算、monitor 复用和规模基准。
6. **平台收口：** macOS 和 systemd 真机生命周期、日志、漂移、接管/交还测试。
7. **升级收口：** 用真实 0.10.3 fixture 完成 dry-run、导入、逐条切换和回退。
8. **发布收口：** 多平台构建、checksum、全新安装和 V1 Preview 端到端验收。

这个顺序不增加产品范围。任何步骤发现需要新增一级命令、中心进程、公共状态、配置字段
或远程副作用，必须暂停编码并先回到两份主文档确认。

## 24. 架构变更流程

评审任何实现或新功能时，依次回答：

1. 是否保持无中心 daemon 和每 Connection 故障隔离？
2. 是否保持 Connection、Driver、ExecutionPlan、Adapter 的单向边界？
3. 是否让正常路径自动一致，又不给异常漂移猜方向？
4. 是否保持原生服务不依赖 Tunlite 二进制继续存在？
5. 是否给人和 Agent 同样可审计、可预览、可回滚的结果？
6. 是否把复杂性放在 Provider、操作系统或发行渠道应承担的位置？
7. 是否有真实需求足以承担新的公共概念和长期兼容成本？

任何一个答案是否定的，方案不能直接实现。修改公共产品语义时同步更新产品设计；修改
核心模块、数据、事务、安全或性能边界时同步更新本文档。派生文档和代码注释只能解释
已经接受的设计，不能绕过主文档创造新规范。

## 25. 一句话架构

> Tunlite 把具名 Connection 交给 Driver 编译成无 Shell 的 ExecutionPlan，再由每个
> 操作系统原生服务独立运行；Core 只在命令执行期间负责声明、事务、观察和安全边界，
> 永远不成为后台控制平面。
