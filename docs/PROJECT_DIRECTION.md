# Tunlite 产品设计

- **状态：** 已接受
- **适用范围：** Tunlite v1 及后续版本
- **最后更新：** 2026-08-21

本文档与 `docs/ARCHITECTURE.md` 是 Tunlite 并列的两份主文档。本文档定义产品为什么
做、做什么、用户心智模型、公共命令与路线；架构主文档定义系统如何实现、模块边界、
数据与事务、安全、性能、测试和代码风格。README 负责介绍已经交付的产品，命令帮助
负责说明当前用法，测试报告负责记录验证结果，Release Note 负责记录版本变化；它们
都不能定义第三套产品或架构设计，也不能覆盖两份主文档的决策。

`docs/superpowers/` 中的旧规格与计划以及 Node.js/daemon 实现，只是产品演进的历史
记录，不再具有规范性。未来可以编写临时实施清单和版本升级记录，但它们必须
引用本文档，不能成为相互竞争的路线图或架构指引。

产品语义与范围冲突时以本文档为准，实现边界与工程约束由架构主文档定义。任何改变
公共产品语义或核心架构边界的提案，都必须在同一个 Pull Request 中同步修改相应
主文档，并说明被改变的取舍。不得另外创建相互竞争的路线图或架构指引。

## 1. 产品愿景

### 1.1 当前市场定位

未来 12—18 个月，Tunlite 首先是一款面向开发者和 AI Agent 的可靠 SSH 隧道
管理器。用户选择 Tunlite，是因为他们需要建立、保持、检查和恢复 SSH 端口转发，
而不是因为他们正在寻找一个抽象的“连接平台”。

当前一级叙事必须保持具体：

> 把 SSH 隧道声明一次，Tunlite 让它可靠运行，并让人和 Agent 都能安全管理。

这一阶段优先服务确实管理多条隧道、依赖隧道访问私有服务，并在重启、断线和升级后
仍要求连接可靠恢复的用户。SSH `-L`、`-R`、`-D` 都属于产品能力，但“访问私有
服务”是主要市场入口；不得把动态 Publish Provider 与 SSH 管理同时包装成两个
同等重要的新产品故事。

### 1.2 长期架构愿景

Tunlite 长期可以成为面向人和 AI Agent 的开放式、声明式连接工具。

它的职责是：通过一个有名字、可检查的连接，让私有服务或本地服务变得可达，
而不要求用户记住每一种底层连接工具的复杂参数和生命周期。

长期承诺是：

> 连接只需声明一次。Tunlite 使用用户已经信任的工具，把它编译成可靠、
> 可审计的操作系统原生服务。

SSH 是第一个内置 Driver，但不是产品永久的边界。未来 Driver 可以封装
Kubernetes 端口转发、云厂商身份隧道、认证型中继，以及其他命令行连接工具。
Tunlite 不重新实现这些工具的传输协议。只有真实用户开始要求统一管理这些连接，
并愿意持续使用时，Driver 平台才升级为产品主体。

架构应当为未来保留边界，但产品不能提前假设插件生态已经存在。

### 1.3 Tunlite 的核心差异

- 在近期范围内，成为比手写 SSH 命令和通用进程守护器更可靠的隧道管理体验。
- 同时稳定服务于人类用户、脚本和 AI Agent。
- 为 Agent 提供版本化 Schema、非交互保证、计划与结构化风险，而不只提供 `--json`。
- 连接有名字、可声明、可迁移。
- 使用操作系统原生生命周期管理，而不是 Tunlite 中心控制 daemon。
- 使用用户已有并信任的可执行程序和凭据存储。
- 不强制 Tunlite 账号、托管服务、私有传输协议或供应商锁定。
- 长期 Driver 生态与编程语言无关，不需要修改核心仓库即可扩展。

### 1.4 产品边界

Tunlite 管理的是服务级连接，不管理连接两端的计算环境。

属于核心范围：

- 通过本地 TCP 端点访问私有服务。
- SSH 原生支持的远端转发。
- Driver 支持时提供 SOCKS 等动态代理。
- 具名 Connection 的前台运行、临时后台运行和自动启动策略。
- 前置检查、状态、日志、健康检查和诊断。
- 把 Provider 身份认证交给 Provider 自己的 CLI 或凭据存储。

长期候选范围，但不属于 v1 发布门槛：

- Kubernetes 端口转发。
- 云厂商身份隧道。
- 通过认证型动态端点 Provider 发布本地服务。
- 外部 Driver 的公开安装、升级和分发生态。

不属于核心范围：

- Tunlite 自建中继、托管控制面、用户账号或私有隧道协议。
- 整网 VPN 或 Overlay Network 管理。
- 远程 Shell、文件同步、容器生命周期、WSL 生命周期或云工作区创建。
- 密钥存储。Tunlite 可以传递 Secret 引用，但它不是 Vault。
- 在 CLI 和 Driver 契约稳定前建设桌面 GUI 或 Web 控制台。
- 在操作系统能力本来不同的情况下，伪造完全一致的平台行为。

VS Code Remote Development 可以作为架构参考：它在统一入口下提供多个
Provider。其中 SSH 和 Remote Tunnels 属于连接 Provider；Dev Containers、
WSL 和 Codespaces 属于开发环境，不是 Tunlite Driver 的目标。

### 1.5 设计哲学

Tunlite 希望成为一个经得起长期演进的工具。这里的“稳定”不是承诺某一版配置永远
不变，而是无论十年后支持多少 Provider，产品仍保持一套清楚、诚实、可解释的心智
模型。

所有设计必须遵循以下原则：

- **把复杂性放在正确的位置。** 操作系统已经擅长管理进程，Provider 已经擅长传输
  和认证，Tunlite 只负责声明、编译和管理连接，不重复发明它们。
- **用户永远拥有退路。** Tunlite 不制造锁定。连接可以进入 Tunlite 管理，也可以
  在不中断的前提下交还操作系统；移除 Tunlite 不能让已经正常运行的连接集体失效。
- **一个意图只对应一个动作。** 正常修改必须自动生效，不能让用户记住额外的
  `sync`；发现现有连接和接管它也不能拆成两个必须连续执行的公共步骤。
- **正常路径简单，异常路径明确。** 日常操作不暴露内部协调机制；只有两边被绕过
  修改、确实发生漂移时，才要求用户明确选择以哪一边为准。
- **状态比过程更长寿。** 公共模型描述 Connection、管理权、期望状态和一致性，
  而不是把当前实现中的扫描、PID、socket 或某个服务管理器写成产品概念。
- **透明胜过魔法。** Tunlite 生成什么、运行什么、读取哪里的日志、存在什么风险，
  都应当可以检查。自动化不能以隐藏副作用为代价。
- **人和 Agent 使用同一套安全契约。** Agent 获得结构化计划、风险和结果，但不会
  获得绕过用户授权的另一条捷径。
- **扩展边界先稳定，生态能力后验证。** v1 只交付 SSH，但核心不能写死 SSH；外部
  协议只有经过不同 Provider 的真实使用，才有资格成为长期承诺。
- **少就是产品能力。** 新命令、新状态和新后台组件都必须证明不可替代。能够由已有
  概念自然表达的能力，不再创造第二套名词。
- **发行渠道管理程序，Tunlite 管理 Connection。** 二进制安装、升级和卸载由包管理器、
  Desktop 安装包或用户选择的手工下载流程负责；核心 CLI 不同时充当自己的安装器和
  更新器。一次性的历史格式兼容应复用现有数据入口，不能扩张成永久的产品子系统。

## 2. 已接受的核心决策

下列决策已经接受，后续实现必须以此为准。

1. 使用单文件 Go 二进制重写 v1 核心。
2. 删除中心 Tunlite daemon、IPC socket、daemon PID 文件和全局内存
   reconcile 循环。
3. 由当前正式支持平台的 OS Service Adapter 负责所有 Tunlite 后台进程的生命周期：
   v1 首发使用 systemd 和 launchd；未来 Windows 产品使用 Task Scheduler。不以
   fork、nohup、PID 文件或隐藏 Supervisor 伪造跨平台后台管理。
4. 在受支持的平台上，`add` 创建具名 Connection 的同时生成一份默认停止的原生
   服务定义。底层差异由 Adapter 处理，公共产品只暴露 `stopped`、`foreground`、
   `background` 和 `persistent` 四种经过验证的运行方式，不开放任意生命周期组合。
5. 只要可行，服务直接运行 Driver 生成的底层可执行程序。一个已经编译完成的
   持久连接，不应仅仅为了存活而依赖 Tunlite 进程。
6. 保留 `tunlite run <name>` 作为附着终端的前台模式。所有执行都必须引用已有的
   具名 Connection；不提供直接接受任意 SSH 参数的临时 `run` 语法。
7. 生成的服务定义是可以重建的产物。声明式配置和锁定的插件信息才是事实来源。
8. 持久 Connection 的正常修改必须在同一次事务中自动更新原生服务，不提供要求
   用户在正常操作后再次执行的 `sync` 工作流。只有外部修改造成真实漂移时，才使用
   `tunlite resolve` 明确选择事实来源。
9. SSH 是内置 Driver，但必须实现与外部 Driver 相同的逻辑接口。
10. v1 的市场定位和完整交付范围是 SSH；外部插件系统不得阻塞 v1 发布。
11. 外部扩展使用进程外、与语言无关、带版本的 JSON Driver 协议，但该协议必须
    先以实验版验证，不能因为产品版本叫 v1 就提前冻结为 `tunlite.driver/v1`。
12. 用户未来可以直接从第三方仓库安装插件。官方索引可以帮助发现，但绝不能成为
    必经入口。
13. 公开仓库是代码、Issue、CI、Tag 和 Release 的唯一权威来源。私有仓库可以
    存放安全或商业资料，但不能维护另一份分叉的开源核心实现。
14. 每个 Connection 声明始终只有一份；user/system Scope 是本机原生服务归属，不进入
    portable Connection。默认 user，只有显式 `enable --scope` 才选择或迁移 Scope。

### 2.1 v1 必须保留的扩展边界

“v1 只正式交付 SSH”是发布范围决定，不是把核心实现成 SSH 专用程序。v1 必须完成
以下内部边界，保证未来增加 Driver 以增量开发为主，而不是进行第二次核心重写：

- 公共配置使用通用 Connection Envelope；SSH 字段只能存在于 SSH Driver 的
  `spec.config` 中。
- 核心编排只调用内部 Driver 接口，不直接判断 SSH Host、Forward 或 OpenSSH 参数。
- SSH 必须生成通用 `ExecutionPlan`；OS Service Adapter 只消费执行计划，不知道
  计划来自 SSH、Kubernetes 还是其他 Driver。
- CLI 的选择、批量操作、五维状态、日志、一致性处理、风险和 JSON 结果面向
  Connection，不以 Tunnel 专属结构作为公共模型。
- `ExecutionPlan` 从 v1 起保留明确的执行模式字段；v1 只正式使用 `direct`，但不能
  用依赖 SSH 程序名的逻辑阻断未来 `managed`。
- 使用至少一个测试用假 Driver 验证核心没有 SSH 特判。该 Driver 不需要发布，也不
  需要引入插件安装系统。

v1 正式发布后，Connection Envelope、CLI 机器契约、状态维度和 OS Adapter 边界应
以兼容扩展和显式迁移为主，不能为了加入第一个外部 Driver 整体替换。实验 Driver
协议仍允许在稳定前调整；预留扩展性不等于提前冻结未经生产验证的公共协议。

### 2.2 重写不等于删减用户能力

从 Node.js 重写为 Go、删除中心 daemon 和更换后台运行机制时，应重新确认真正需要的
用户能力；历史实现和未形成真实用户依赖的接口不自动成为 V1 兼容承诺。V1 Preview
发布后形成的公共契约才按版本规则演进。

V1 的基础体验至少包括：彩色且可关闭颜色的人类输出、对齐的状态表格、单 Connection
详情、`doctor` 环境与连接诊断、唯一实时入口 `tunlite monitor`、完整帮助、友好拼写提示、
日志查看与流式输出。`--json`、非 TTY 输出和设置 `NO_COLOR` 时不得输出 ANSI 控制符。
架构变化导致字段语义改变时可以提供更准确的新表达，但不能退化成难以阅读的裸制表符
输出。

分发体验也属于用户能力，但所有权必须清楚：Homebrew 等包管理器、Desktop 安装包和
手工下载分别负责自己安装的二进制，核心 CLI 不提供 `install`、`update` 或 `uninstall`
命令，也不建立 `tun` 短命令。官方必须提供签名或 checksum、明确的安装升级命令和
不触碰 Connection 的卸载说明。V1 不提供 `setup-key`：Core 不生成 SSH 私钥，也不
修改远端 `authorized_keys`；认证初始化由 `ssh-keygen`、`ssh-copy-id` 或文档给出的
系统 OpenSSH 操作完成，非交互模式绝不能等待密码。

Shell 自动补全不进入 V1 Preview。当前尚未形成一个同时满足跨 Shell、跨平台、多个
安装来源、明确文件所有权和零意外副作用的长期方案，因此不提供 `completion`，也不
读写任何 Shell 配置。未来只有在职责边界明确后才重新立项：优先由 Homebrew 等包管理器安装其
负责的补全文件，Tunlite 可以从统一命令模型生成脚本，但不能把自动识别 Shell、修改
启动文件和清理第三方内容变成核心 CLI 的长期负担。暂缓补全不允许命令定义散落；帮助、
机器能力和 Agent Skill 仍必须从同一份命令契约生成或接受契约测试。

Agent Skill 属于 Agent 自己的扩展体系，不属于 Tunlite CLI 的本机安装状态。Tunlite
只在公开仓库提供一个通用、标准、可独立安装的 Skill 包：

```text
skill/use-tunlite/
└── SKILL.md
```

Skill 的稳定名称是 `use-tunlite`。名称面向长期产品，触发描述在 v1 继续明确聚焦 SSH
连接和端口转发；未来增加正式 Driver 时只扩展描述，不再次重命名。用户把仓库目录或
固定版本 Tag 的目录地址交给 Agent，由 Agent 根据自身规则完成安装、状态检查、更新和
卸载。Tunlite 不识别 Claude、Codex 或其他 Agent 的私有目录，不提供
`tunlite skill ...`、`tunlite install skill ...`，也不在 `doctor` 中检查 Agent Skill
是否安装。

Skill 不是独立事实来源。CI 必须验证它声明的命令、字段、退出码和示例与当前 CLI
契约一致；同一版本只维护 `use-tunlite` 一份包，不保留 `ssh-tunnel` 别名。

## 3. 为什么删除中心 daemon

0.x 的 daemon 在当时是合理设计。它提供了统一 Supervisor、精细的内存状态、
流式日志、告警、配置 reconcile，以及缺少原生服务管理器时的兜底能力。

但它也在原生服务管理器上方重新实现了一层服务管理器：

```text
操作系统服务管理器 -> Tunlite daemon -> N 个 Supervisor -> N 个 ssh 进程
```

这带来了全局故障域和大量生命周期复杂度：

- 双 daemon 竞争。
- socket 权限、过期 socket 和 IPC 超时。
- daemon 重启所有权和重启接力。
- 更新时断开承载更新命令本身的隧道。
- PID、残留状态和异常退出处理。
- 一个中心进程故障导致全部连接一起受影响。

v1 主动用一部分跨平台状态精度，换取更少的概念和更好的隔离：

```text
Tunlite CLI -> Driver -> ExecutionPlan -> 操作系统服务管理器 -> Provider 命令
```

每个连接独立失败。更新或删除 Tunlite CLI 不得终止已经运行的 Provider 命令。
当服务直接运行 Provider 程序时，即使 `tunlite` launcher 或 Go 二进制丢失，
现有服务也不受影响；只是恢复 Tunlite 前无法进行新的管理操作。

## 4. 目标架构

```text
                         +----------------------+
Connection 声明 -------->| 内置 SSH Driver      |
                         | 外部 Driver           |----> ExecutionPlan
                         +----------------------+             |
                                                              v
Tunlite CLI -----------------------------------------> OS Service Adapter
                                                    /        |        \
                                             systemd     launchd   Task Scheduler
                                                    \        |        /
                                                              v
                                             ssh / kubectl / aws / ...
```

### 4.1 核心职责

Go 核心负责：

- 配置加载、校验、导入和导出，以及公共配置版本确有需要时的内部原子升级。
- Connection 名称、单值 Group、选择和批量操作。
- Driver 发现、协议协商和调用。
- 阶段 5 以后才加入插件安装、校验、锁定、更新、回滚和卸载。
- 通过 OS Service Adapter 把 `ExecutionPlan` 转换成平台原生服务产物。
- 原生服务的生成、删除、启停、自动启动、状态、发现和日志接入。
- 通用健康检查和通用状态展示。
- `--json`、稳定退出码、脱敏、诊断和 Agent 集成。
- 在正常变更中自动保持声明、生成物和原生服务一致，并在异常漂移时提供明确的
  双向解决方案。

核心不能理解 Kubernetes Resource、AWS Instance、Cloudflare Tunnel 等
Provider 专属字段。

#### 4.1.1 OS Service Adapter

OS Service Adapter 是核心内部的受信任 Go 接口，不是 Driver，也不是 v1 对外开放的
第三方插件。Driver 决定“运行什么”，Adapter 只决定“当前操作系统如何把它注册、
启停、观察和记录日志”。两者只通过平台无关的 `ExecutionPlan` 与标准化状态交互。

内部能力至少包括：

| 能力 | 作用 |
|---|---|
| `Capabilities` | 报告注册、后台运行、自动启动、日志和发现能力 |
| `Materialize` | 把 `ExecutionPlan` 原子生成为默认禁用的原生服务定义 |
| `Remove` | 停止并删除原生服务定义 |
| `Start / Stop / Restart` | 实现临时后台运行模式及其当前进程控制 |
| `Enable / Disable` | 原子进入或退出长期运行模式 |
| `Inspect` | 返回服务注册、自动启动、进程和平台原生状态 |
| `Logs` | 从平台原生日志源读取日志 |
| `Discover` | 枚举已启用和未启用的可接管服务候选 |

核心按运行环境探测并选择一个明确的 Adapter。v1 首发内置 systemd 与 launchd
实现，不发布或宣称支持尚未形成完整产品体验的 Windows 版本。未来 Windows 产品
使用 Task Scheduler Adapter；支持 OpenRC、runit 或其他服务管理器时，也只增加
新的 Adapter，不修改 Connection、Driver、`ExecutionPlan` 或公共 CLI 语义。

Adapter 不得解析 SSH Host、Forward 或其他 Provider 配置。`Discover` 只返回服务
身份、程序、参数、环境引用、自动启动、当前状态和日志来源等标准化快照；由核心调用
相应 Driver 判断能否无损还原 Connection。

所有 Adapter 必须通过同一套契约测试，至少覆盖原子生成、三种原生服务模式转换、幂等
操作、失败回滚、日志来源、候选发现和交还后独立运行。核心测试使用 Fake Adapter
证明编排不包含 systemd、launchd 或 SSH 特判。

### 4.2 Driver 职责

Driver 负责：

- 自己的连接配置 Schema 和配置迁移。
- 语义校验。
- 依赖和认证前置检查。
- 翻译成不经过 Shell 的执行计划。
- Provider 专属的状态检查和诊断。
- 清理由自己明确创建的外部资源。
- 说明外部 Provider 及其安全模型。

Driver 应尽量把凭据交给底层 Provider 管理，不能创造另一套 Tunlite 凭据存储。

### 4.3 持久运行策略

持久执行计划必须明确声明以下两种模式之一。

#### `direct`

操作系统服务直接执行 Provider 命令：

```text
systemd -> ssh
systemd -> kubectl port-forward
systemd -> aws ssm start-session
```

这使资源占用主要来自 Provider 自身，避免每个连接常驻一个 Go 或 Node runtime，
也使已安装连接不依赖 Tunlite launcher 继续存在。

SSH 默认并优先使用 `direct`。只要静态程序、参数、环境引用、生命周期和有限健康检查
足以表达连接，就必须选择该模式。

#### `managed`

操作系统服务运行由 Driver 提供的、每 Connection 独立的 runtime，再由该 runtime
适配 Provider：

```text
systemd -> 固定版本的 Driver runtime -> Provider
```

只有以下场景可以选择 `managed`：

- 端点只能在运行后动态发现。
- 运行期间必须刷新凭据或重新认证。
- 必须解释 Provider 输出，才能提供正确状态。
- Provider 本身不能表达所需的重连或生命周期语义。

`managed` runtime 必须由 Driver 独立拥有和版本化，服务必须引用不可变的精确路径；
它只能管理当前 Connection，不能发现、控制或汇总其他 Connection。每个 Connection
独立失败，删除 Tunlite CLI 不得让已经安装的 runtime 立即失效。

明确禁止第三种模式：一个中心化常驻进程管理全部 Connection。`managed` 是复杂
Provider 的适配边界，不是换名字重新引入 Tunlite daemon。

`tunlite run <name>` 继续服务于前台运行、容器、CI 和调试场景。它附着在当前终端，
调用者停止时退出。

### 4.4 运行与管理权模型

Tunlite 内部必须准确处理服务定义、当前进程、用户会话启动以及故障恢复，但不能把
这些操作系统细节变成普通用户需要组合的产品概念。“扫描”“导入服务定义”“记录
绑定”和“更新生成物”同样只是实现步骤。

所有 Tunlite 执行都基于已有的具名 Connection。`add` 或项目声明负责定义连接，
`run`、`start` 和 `enable` 只选择运行方式，不能各自接受另一套任意 Provider 参数。

#### 4.4.1 前台运行：`tunlite run`

`tunlite run <name>` 读取已有 Connection，在当前终端直接运行其 Provider：

- 不创建匿名或一次性 Connection，不接受纯 SSH 参数形式。
- 不创建、接管或修改操作系统服务。
- 不改变任何现有连接的管理权。
- 只允许从 `stopped` 进入 `foreground`；其他运行方式必须拒绝，不能启动重复实例。
- 终端退出、收到停止信号或调用者结束时，连接随之回到 `stopped`。

它适用于调试、CI、容器和没有受支持原生服务管理器的环境。用户只需要一次原始 SSH
命令时应直接使用 `ssh`；Tunlite 的价值从具名、可检查和可复用的 Connection 开始。
前台运行不是一种需要以后清理的半持久状态。

#### 4.4.2 四种标准运行方式

在 systemd、launchd 或其他已经正式支持的 OS Service Adapter 上，`add` 必须原子
完成两件事：保存具名 Connection，并把确定性的 `ExecutionPlan` 编译成一份默认
停止、可由操作系统独立使用的原生服务定义。

公共产品只提供四种运行方式：

| 运行方式 | 中文含义 | 使用原生服务 | 退出终端后继续 | 用户会话启动后恢复 | 异常退出恢复 |
|---|---|---|---|---|---|
| `stopped` | 停止 | 已生成但未运行 | 不适用 | 否 | 不适用 |
| `foreground` | 前台运行 | 否 | 否 | 否 | 否 |
| `background` | 后台运行 | 是 | 是 | 否 | 是 |
| `persistent` | 后台长期运行 | 是 | 是 | 是 | 是 |

表中的“用户会话启动后恢复”描述默认 user Scope；macOS system Scope 的 persistent 由系统
启动阶段恢复，不依赖该用户先登录。

这四个值是生命周期实现和机器契约中的标准模式，不直接作为新手界面的唯一状态答案。
给人看的 `status`、Monitor 和未来图形界面固定拆成四个问题：当前是否正常、在哪里运行、
是否自动启动、归用户还是系统。对应展示为“状态 / 运行位置 / 自动启动 / 范围”；其中运行位置只有“当前终端 / 系统
后台 / —”，自动启动只有“是 / 否”。内部五维状态仍完整保留在 JSON 中供脚本和 Agent
判断，不能为了简化界面而压缩机器事实。

运行方式表达用户意图，不替代观测状态。`background` 或 `persistent` 仍可能因为正在
重试、依赖缺失或故障而暂时不是 `processState: running`，必须如实报告原因。

架构内部，`foreground` 由当前 CLI 直接运行 Provider，其余三种方式由原生服务定义
承载；这一区别不能迫使用户学习第二套状态模型。底层 Adapter 可以使用 systemd 的
enable/restart、launchd 的加载策略与 KeepAlive 等不同机制实现上述语义，但公共配置
不能暴露允许用户任意组合的 `autostart`、`restartPolicy` 或等价布尔字段。

公共命令只表达用户意图，并固定进行以下模式转换：

- `add`：创建原生服务并进入 `stopped`，不运行。
- `run`：从 `stopped` 进入 `foreground`；当前调用结束后回到 `stopped`。
- `start`：从 `stopped` 进入 `background`；本次运行异常退出时由系统恢复，但下次
  用户会话启动时不再启动。
- `stop`：只允许把 `background` 变成 `stopped`。对 `persistent` 必须拒绝并提示
  使用 `disable`，避免制造“现在停止、以后又出现”的隐藏组合。
- `enable`：从 `stopped` 或 `background` 进入 `persistent`，并确保现在已经运行。
- `disable`：把 `persistent` 变成 `stopped`，同时停止当前进程并取消以后自动启动。
- `restart`：只允许重启正在运行的 `background` 或 `persistent`；对 `stopped` 拒绝，
  不能把 `restart` 偷偷解释成 `start`。
- `remove`：只允许删除 `stopped` Connection；删除原生服务和声明，但不删除 Driver
  的远程资源。

重复执行已经满足目标的操作必须幂等成功，不能启动重复进程。`run` 与后台服务使用
同一 Connection 时必须持有互斥运行锁；已经存在前台或后台实例时，另一个启动动作
必须拒绝，而不能等到端口冲突后才失败。

服务定义必须直接包含独立、安全的 Provider 执行计划，不能要求 Tunlite CLI、临时
配置文件或可变路径常驻才能继续运行。创建服务定义不产生常驻进程；大量尚未启动的
Connection 只占用少量配置和服务文件空间，不产生按 Connection 计算的运行时内存。

`persistent` 只能通过用户明确执行 `enable` 进入。`add`、`run`、`start`、项目配置
导入、普通 `set` 和异常恢复都不得顺便进入长期运行。显式 Scope 迁移本身就是
`enable --scope`，因此成功后必须进入 `persistent`，不能恢复迁移前的 Mode。Agent 执行
`enable` 或 `disable` 时必须把启动、停止和本机持久化副作用完整放入结构化计划，不能从项目声明
推导出静默授权。

Scope 与上述 Mode 正交。新 Service Record 默认 `user`；已有 Record 不传 `--scope` 时
保留当前值。`enable <name>|--group|--all --scope user|system` 是唯一显式选择入口；不新增
`migrate` 命令，也不允许 `start/disable/restart` 顺带改 Scope。显式改变 Scope 时必须先
规划整批、说明影响，再执行停旧、装新、按 `enable` 语义进入 `persistent`、稳定观测、
更新 Record、删旧的可回滚事务。成功后 Mode 是 `persistent`；只有失败回滚才恢复原
Scope 和原 Mode。任何目标不支持、权限不可用或计划冲突都必须在首个写操作前让整批失败。
预检通过后的每个 Connection 独立提交并自行回滚；后项运行时失败返回 partial 和逐项结果，
不再次中断并迁回已经成功的前项。

`system` 属于管理员授权能力。人工调用可以通过系统既有授权机制完成，但 Tunlite 不读取
密码；`--non-interactive` 只使用外部预置、可由 `sudo -n` 验证的授权，不能等待终端输入。
缺少授权返回稳定 `privilege_required`。Connection export/import 永远不携带 Scope。

处于 `foreground` 时，当前 `run` 调用者拥有进程生命周期。`start`、`stop`、
`enable`、`disable`、`restart`、`set`、`rename`、`unmanage` 和 `remove` 必须拒绝；
用户先结束前台运行，再进行其他生命周期或配置操作。不能由第二个 CLI 调用抢走、
终止或改变前台进程。

没有受支持 OS Service Adapter 时，`add` 仍可保存具名 Connection，但必须把它明确
标记为 `foreground-only`。`run` 继续可用；`start`、`stop`、`enable`、`disable`
以及需要原生服务作为交接对象的 `unmanage` 必须返回稳定的能力不支持结果。Tunlite
不能回退到 PID 文件、nohup 或内置 Supervisor。未来按真实需求增加 OpenRC、runit
等 Adapter，而不是重新引入第二套后台架构。

#### 4.4.3 纳入管理：`tunlite manage`

`tunlite manage` 把一个已经拥有原生服务定义、符合接管规范的连接纳入 Tunlite
管理；服务当前可以运行或停止，自动启动也可以开启或关闭。一个命令必须完成发现
候选、选择、校验、导入完整配置、绑定原生服务和建立
一致性基线；不提供要求用户先执行的独立 `discover`、`scan`、`adopt` 或 `import`
步骤。

默认交互流程可以让用户选择候选并补充无法可靠推导的必要字段；非交互模式必须要求
明确目标并返回结构化结果。Adapter 必须扫描所有可识别的原生服务定义，而不是只读
自动启动列表；已启用和未启用的服务具有同等接管资格。裸 `ssh` 进程、终端命令、
nohup 进程和其他没有稳定服务定义的后台进程不属于 v1 接管范围。

候选还必须能够在不修改服务的前提下，无损映射到 Tunlite 标准模式：未运行且不自动
启动对应 `stopped`，当前运行、不在用户会话启动时恢复且具备故障恢复对应
`background`，自动启动且具备故障恢复对应 `persistent`。自定义定时条件、无法表达的
依赖关系、运行后不恢复或其他生命周期组合不符合接管规范，必须说明原因并拒绝，不能
为了扩大接管数量而引入公共 `custom` 模式。

全量扫描系统服务只发生在用户显式执行 `manage` 时，
普通的 `list`、`status` 和批量操作只读取 Tunlite 已管理的 Connection 及其已绑定
服务，不能每次扫描整台机器。

接管的硬性条件是零中断：

- 不停止、不重启正在运行的 Provider 进程。
- 不为了统一格式而重写或替换当前服务。
- 不改变当前运行状态、自动启动状态、日志位置、重启策略或网络端点。
- 不能无歧义还原配置，或原生服务不具备安全、稳定的后续管理能力时，明确拒绝接管，
  不能“尽量猜测”。

成功后，完整 Connection 声明进入 Tunlite，Tunlite 成为该 Connection 的配置事实
来源。原生服务身份、实际执行计划摘要以及“两边最后一次一致”的基线写入本机管理
状态。此后的启停、修改、重命名、日志和诊断都通过普通 Tunlite 命令完成。
同一个原生服务在任意时刻只能绑定一个 Tunlite Connection；接管事务必须使用仅在
命令执行期间持有的短期互斥锁，防止两个并发 `manage` 产生双重管理关系。该锁不是
常驻进程，也不参与 Connection 的运行。
接管后的所有重建和回滚必须继续使用已绑定的原生服务身份、文件路径和日志位置；
Tunlite 内的重命名不能偷偷创建第二份服务，也不能把原生服务改名后造成连接中断。

`manage` 只用于接管已经存在的原生连接。通过 Tunlite `add` 创建的 Connection
天然处于 managed 状态，不需要再执行一次 `manage`。

#### 4.4.4 交还管理：`tunlite unmanage`

`tunlite unmanage <name>` 让 Connection 脱离 Tunlite 管理，把后续控制权交还给
操作系统。它适用于所有拥有原生服务的已管理 Connection，包括 Tunlite 接管的连接和
Tunlite 自己创建的连接。

`unmanage` 的语义固定为：

- 当前正在运行就继续运行，当前已经停止就保持停止。
- 保留原生服务、当前标准模式、底层恢复行为和原生日志。
- 删除 Tunlite 中的 Connection 声明、服务绑定和一致性基线。
- 不停止连接、不删除服务、不删除日志，也不恢复成接管前的历史版本。

执行前必须先验证脱离 Tunlite 后，原生服务仍拥有独立、完整、可持续运行的定义。
如果服务仍依赖临时文件、可变插件路径或即将被删除的 Tunlite 组件，必须先生成安全
计划并修复依赖；无法保证独立运行时拒绝交还。`unmanage` 不是 `disable` 或 `remove` 的
别名。

在受支持的平台上，Tunlite 自己创建的 Connection 从 `add` 起就具有默认禁用的原生
服务定义，因此即使它从未启动，也有真实对象可以交还。在 `foreground-only` 平台上
没有原生服务可以交接，`unmanage` 必须明确返回能力不支持；不能把删除配置或遗留一个
裸进程伪装成成功交还。

公共状态和文档统一使用 `managed / unmanaged`；公共命令统一使用
`manage / unmanage`；中文产品文案使用“接管 / 交还”。这些词描述的是管理关系，
不是进程是否运行。

选择这组词，是因为它直接回答“现在由谁管理”，并能自然形成长期稳定的
`managed / unmanaged` 状态。`takeover / handover`、`claim / release` 和
`own / disown` 过度强调所有权，`opt in / opt out` 更像加入某项计划，
`adopt / release` 又不能共享同一个词根；它们都不如 `manage / unmanage` 准确表达
“退出 Tunlite 管理但不停止连接”。

几个容易混淆的动作必须保持以下边界：

| 动作 | Tunlite 声明 | 原生服务 | 模式结果 | 管理关系 |
|---|---|---|---|---|
| `add` | 创建 | 生成 | `stopped` | Tunlite 管理 |
| `run` | 读取已有声明 | 不使用、不修改 | `foreground`，退出后回到 `stopped` | 不改变 |
| `start / stop` | 保留 | 启动或停止 | `background` / `stopped` | Tunlite 管理 |
| `enable / disable` | 保留 | 进入或退出长期运行 | `persistent` / `stopped` | Tunlite 管理 |
| `manage` | 从现有服务完整导入 | 原样保留 | 保留可映射的标准模式 | 转入 Tunlite 管理 |
| `unmanage` | 删除 | 完整保留 | 不改变 | 交还操作系统 |
| `remove` | 删除 | 删除 | 仅允许从 `stopped` 结束 | 结束 |

#### 4.4.5 正常修改自动联动

Tunlite 是已管理 Connection 的事实来源。所有正常写操作，包括 `set`、`rename`、
项目配置应用和 Driver 配置迁移，都必须在同一次事务中完成：校验新声明、生成计划、
在原生服务存在时更新它、应用不可避免的进程变化、验证结果并写入新基线。

成功意味着 Tunlite 声明和原生系统已经一致；失败必须回滚到上一个可工作状态，或
返回准确的部分失败与恢复动作。用户不能看到“配置已经改了，请再执行 `sync` 才会
生效”的中间产品状态。

如果计划变化必然造成连接短暂重启，必须在执行前明确展示；能由服务管理器原子更新
且无需重启的变化不得主动中断连接。

#### 4.4.6 异常漂移：`tunlite resolve`

漂移不是正常工作流。它只表示用户或其他程序绕过 Tunlite，直接修改了 Tunlite 声明、
原生服务或关键依赖，使当前两边与最后一致基线不再相同。

Tunlite 必须报告双方差异和可判断的变化方向，但不能猜测用户想保留哪一边。处理时
必须明确选择：

```text
tunlite resolve <name> --use tunlite
tunlite resolve <name> --use system
```

- `--use tunlite`：以 Tunlite 声明为准，重新生成并更新原生服务。
- `--use system`：以当前原生服务为准，重新校验并更新 Tunlite 声明。

无法安全反向还原的系统变化必须说明具体字段并停止，不能产生有损配置。解决成功后
写入新的共同基线。批量解决必须逐项返回结果，任何一项失败都不能被顶层成功掩盖。
如果系统侧生命周期已经无法映射到 `stopped`、`background`、`persistent` 三种原生
服务模式，`--use system` 必须拒绝，并提供
恢复 Tunlite 标准模式或 `unmanage` 的结构化动作，不能把高级系统配置偷偷引入公共
Connection 模型。

#### 4.4.7 日志与能力边界

Tunlite 不搬运、复制或接管日志存储。`logs` 根据已绑定服务读取 journald、launchd
对应日志文件或平台原生日志，并清楚标明来源。交还后日志仍留在原位置。

接管成功意味着 Tunlite 能够可靠执行该平台对 managed Connection 承诺的全部公共
命令。无法支持启停、修改、状态、日志或安全交还中的必要能力时，必须在 `manage`
校验阶段拒绝，而不能接管后才暴露一个“只能看、不能管”的残缺对象。平台确实无法
提供的可选能力必须通过 Capability 明示。

### 4.5 状态模型

公共状态不能压缩成一个枚举。每条 Connection 必须同时报告五个互不替代的维度：

| 字段 | 取值 | 回答的问题 |
|---|---|---|
| `serviceState` | `registered`、`absent`、`unsupported`、`unknown` | 当前平台是否存在可用的原生服务定义？ |
| `mode` | `stopped`、`foreground`、`background`、`persistent`、`unknown` | 用户当前采用哪种标准运行方式？ |
| `processState` | `inactive`、`starting`、`running`、`failed`、`unknown` | Provider 进程现在怎样？ |
| `health` | `unknown`、`healthy`、`unhealthy` | 已声明端点是否有健康证据？ |
| `consistencyState` | `consistent`、`drifted`、`missing`、`blocked` | 声明、生成物、依赖和 OS 服务是否一致？ |

此外必须报告正交的 `scope: user|system`，回答本机原生服务由当前用户还是系统管理；它
不是第六种 Mode，也不进入 Connection 声明。

`processState: running` 只表示进程仍在运行，不能声称端到端健康。
`mode: persistent` 表达长期运行意图，但不能掩盖服务缺失、插件缺失或进程故障；真实
运行状态始终由 `processState` 和 `health` 单独报告。

认证失败、依赖缺失、端口冲突等原因放入版本化的 `conditions[]`，每项至少包含稳定
`code`、严重等级、可读说明和结构化 `actions[]`。动态端点放入 `endpoints[]`，不能
塞进状态字符串。所有观测结果必须带 `observedAt`，让调用者知道信息是否过期。

CLI 从操作系统服务状态和一次性的 Driver `inspect` 得出结果。系统中不存在控制
socket，也不存在常驻的聚合状态进程。

人类总览的状态收敛为“已停止、启动中、正常运行、停止中、重启中、运行异常、需要处理”。
Monitor 的运维列按重要性保留名称、状态、PID、已运行时长和历史重启次数，再展示运行
位置、自动启动、范围、SSH 目标和转发；终端变窄时先隐藏 SSH 目标、转发及次要语义列，
不能先隐藏 PID、已运行和重启次数。`restarting` 表示当前动作，`RESTARTS` 表示历史次数，“自动启动”表示
跨登录意图，三者不得都写成含糊的“重启”。

`list`、`status` 和持续刷新的 `monitor` 必须保持本机、只读、有明确时限：不得执行
远程认证、远程连通性检查或为每条 Connection 启动昂贵的 Provider 操作。Driver
`inspect` 只有在它是廉价本机观测时才能进入默认状态路径；否则对应字段返回
`unknown`，由用户显式使用 `check` 或 `doctor` 执行有超时和并发上限的深度检查。
因此管理上百条 Connection 不会因为打开状态列表而产生上百次网络访问。

## 5. Connection 模型

v1 配置由很小的公共 Envelope 和 Driver 自己拥有的 `config` 组成。最终序列化
Schema 在实现阶段确定，但这个边界是强制性的：

```json
{
  "configVersion": 1,
  "kind": "ConnectionList",
  "items": [
    {
      "kind": "Connection",
      "metadata": {
        "name": "staging-db",
        "group": "staging"
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

核心可以校验公共 Envelope。只有被选中的 Driver 可以校验和解释
`spec.config`。

V1 的规范声明路径是 `${XDG_CONFIG_HOME:-~/.config}/tunlite/connections.json`；设置
`TUNLITE_HOME` 时使用 `$TUNLITE_HOME/config/connections.json`。这个文件永久只保存
可迁移的 `ConnectionList`。运行方式、系统服务绑定、日志位置和一致性基线进入本机
状态目录，未来 Driver 锁和界面偏好也不能混入 Connection 声明。

`configVersion` 是 Tunlite 所有公共、可迁移配置唯一的格式版本，不再为 Desktop、
CLI、安装渠道或单个配置文件增加另一套公共兼容版本。它覆盖 Connection 的公共
Envelope；Driver 配置随锁定的 Driver 版本迁移。本机缓存、服务绑定、观测结果和其他
实现状态可以有内部修订，并在需要时局部迁移或重建，但不形成公共版本概念。每个 Core
在语义解析公共配置前必须先检查 `configVersion`：

所有可以独立保存或传输的公共配置文档都使用 `ConnectionList`，并且只在文档根节点
包含一次 `configVersion`。`items[]` 中的 Connection 继承根版本，不重复保存版本字段；
一个文件里不存在逐 Connection 选择版本的语义。缺失根版本或使用不支持版本的文档
必须在解释具体配置前整体拒绝，不能猜测版本或部分导入。

- 新 Core 可以读取旧版本，并在内存中转换后用于只读操作，不能因为查看状态就改写
  用户数据。
- 第一次确实需要新格式的写操作，必须先明确提示升级影响，创建原始备份，再在同一个
  事务中原子迁移和执行操作；失败则完整回滚。
- 提示必须明确说明：迁移后，旧 Core 将不能再读取、展示、修改或运行这些 Connection。
- 旧 Core 遇到更高的 `configVersion` 时必须停止语义解析和全部管理操作；它只允许
  识别版本号、原样备份数据并提示用户改用兼容版本，不能以“尽力兼容”方式继续运行。

因此多个新旧 Core 可以安装在同一台机器上，但不能在日常运行中各自维护一份配置或
猜测字段含义。V1 Preview 只接受唯一、严格的 `configVersion: 1` 文档；不读取 0.x 或
未发布 Go Preview 格式，也不为它们提供别名和静默转换。

Connection 声明不保存 `foreground`、`background`、`persistent` 或 `scope` 生命周期字段。
同一份具名声明可以被 `run`、`start` 或原生自动启动复用；当前进程和自动启动属于
本机运行状态，不能写进可迁移的 Provider 配置。

公共配置同样不提供 `autostart`、`restartPolicy`、`KeepAlive` 或 systemd `Restart`
等高级生命周期覆盖。四种标准运行方式是产品契约；平台细节只允许作为只读诊断信息出现。

`metadata.group` 是可选的单值业务归属，同时用于 `list`、`status`、`monitor` 的分组
展示、过滤和批量操作。它不形成 Group 对象，不拥有生命周期、嵌套、共享配置或独立
管理命令。修改命令必须明确选择名称、一个 `--group`，或者 `--all`。未设置 Group 的
Connection 进入“未分组”；`all` 不再是魔法位置参数或保留 Connection 名称。

V1 不公开 Tag 概念，也不读取旧 Tag 字段。Group 只通过当前 V1 Schema 和 `--group`
显式表达，避免同时存在两个重叠概念。

### 5.1 项目声明

项目级 Connection 声明可以提交到 Git，用于团队共享和审查。它只能包含可迁移的
逻辑配置和 Secret 引用，不能包含私钥、令牌或机器专属绝对路径。

端口、Identity File、Kubernetes Context 等字段并非天然可迁移。Schema 必须标明
字段是否允许项目共享；机器差异通过具名引用和本机覆盖解决，不能假装所有机器环境
一致。

显式导入或应用项目声明时，在受支持平台上，对能够完成 Driver 解析和校验的
Connection 必须同时生成默认禁用的原生服务定义，但默认不得：

- 安装缺失 Driver。
- 执行认证。
- 创建远程资源。
- 启用或启动 Connection。
- 隐藏公开监听或 Publish 风险。

缺少 Driver、依赖或本机引用的 Connection 保留声明并进入 `blocked`，不能为了满足
“生成服务”而自动安装或猜测；条件补齐后的正常写操作必须自动生成缺失服务。

单纯检出 Git 文件不会触发任何本机变化。项目声明中的任何字段都不能表达或暗示
`mode: persistent`；长期运行只由当前机器上的显式 `enable` 决定。

### 5.2 本机状态

以下内容保存在权限受限的本机状态目录中，不进入 Git：

- Identity、Context、端口等本机覆盖和引用解析。
- 已安装 Driver、实际可执行路径和本机兼容信息。
- `stopped`、`background` 或 `persistent` 标准模式。
- 生成的 OS 服务索引、最近观测状态和诊断缓存。
- 只对当前机器有意义的迁移与回滚记录。

`foreground` 只由仍然存活的 `run` 调用和进程锁观测得出，不写成持久状态；调用异常
结束后锁由操作系统释放，Connection 回到 `stopped`，不能依赖可残留的 PID 文件。

本机状态不是项目声明的替代品。删除它不会改变 Git 中的期望配置，但会使需要本机
解析的 Connection 进入 `consistencyState: blocked` 或 `missing`。

### 5.3 锁定与合并规则

可复现的项目级 Driver 版本和 checksum lock 可以提交到 Git；它表达团队希望解析的
精确供应链输入。本机已安装路径、信任决定和安装结果必须单独保存，不能提交。

配置求值顺序固定为：项目声明、本机覆盖、显式命令参数。最终求值结果必须可以通过
相关修改命令的 `--dry-run` 检查，但包含 Secret 的解析值不得写回项目文件、日志或
导出结果。

同名项目声明与本机私有 Connection 冲突时必须报错，不得静默覆盖。`import`、
`resolve` 和批量 `enable` 都必须逐项返回结果，允许普通生命周期执行出现部分失败但不能
隐瞒失败项；显式 Scope 变更是例外，必须先完成全批预检，不能在发现后项不支持前已经
迁移前项。

未来可以允许 Connection 依赖其他具名 Connection，但在出现真实的组合 Driver
场景，并证明启动、停止和失败语义安全之前，不得公开依赖图能力。

防止用户中断远程登录所依赖的 Connection 是未来待讨论问题。V1 不增加
`protected` 字段或命令，也不使用不可靠的自动依赖判断；运行中禁止 remove、monitor
中断确认和远程升级备用入口仍属于基础安全体验。

## 6. Driver 插件体系

本章定义长期扩展边界，从阶段 5 才开始实现，不属于 Tunlite 产品 v1 的交付清单。
v1 只实现相同的内部逻辑接口和测试边界，不公开或冻结外部进程协议。

### 6.1 进程边界

外部 Driver 是独立可执行程序，不是共享库或语言包。例如：

```text
tunlite-driver-kubernetes
tunlite-driver-aws-ssm
tunlite-driver-cloudflare
```

Tunlite 以短生命周期子进程方式调用它们。stdin 接收一份 JSON 请求，stdout
返回一份 JSON 响应，诊断日志写入 stderr。

明确拒绝：

- Go `plugin`、`.so`、`.dylib` 或 `.dll` 动态加载。
- 把内部 Go struct 暴露为公共 ABI。
- 把 npm package 注入核心进程。
- 使用 Shell command string 作为执行接口。
- 正常 CLI 操作依赖常驻插件 RPC Server。

可执行程序协议允许插件使用 Go、Rust、JavaScript、Python 或任何能够读写 JSON
的语言实现。

### 6.2 协议 Envelope

Driver 协议独立于 Tunlite 版本进行版本化：

```json
{
  "protocol": "tunlite.driver/experimental",
  "requestId": "01J...",
  "method": "plan",
  "platform": { "os": "linux", "arch": "amd64" },
  "connection": {}
}
```

每个响应都要回传 `protocol` 和 `requestId`，并返回类型化结果或结构化错误。
同一个协议大版本内，接收方必须忽略不认识的可选字段。

第一版方法集合：

| 方法 | 作用 |
|---|---|
| `describe` | 返回身份、支持的协议版本和 Capability |
| `schema` | 返回 Driver 配置的 JSON Schema |
| `validate` | 无副作用的语义校验 |
| `check` | 只读检查依赖、认证和运行环境 |
| `plan` | 生成确定性的、无 Shell 的 `ExecutionPlan` |
| `inspect` | 一次性查询状态和动态端点 |
| `diagnose` | 返回结构化检查和可执行修复建议 |
| `authenticate` | 显式进入 Provider 自己的认证流程 |
| `provision` | 经计划和明确授权后创建 Driver 外部资源 |
| `cleanup` | 经计划和明确授权后删除 Driver 外部资源 |
| `migrate` | 在 Core 事务中迁移 Driver 配置 |

`describe`、`schema`、`validate`、`check`、`plan`、`inspect` 和 `diagnose` 不得修改
本地或远程状态。`plan` 对相同标准化输入和 Driver 版本必须产生相同结果。

`authenticate`、`provision` 和 `cleanup` 属于副作用方法，必须遵守：

- 身份登录与远程资源创建是两个独立操作，不能在检查或规划时顺带执行。
- 先返回结构化副作用计划、风险等级和资源范围。具体授权字段、计划绑定和失效规则必须
  在实验 Driver 经真实使用后重新设计，不能复用或假装 V1 已经存在的批准状态。
- `provision` 创建的每项远程资源必须写入 `OwnershipRecord`，至少记录 Driver、
  Connection、Provider 资源 ID、创建时间、来源和所有权策略。
- `tunlite remove` 默认只删除本地声明与本地服务，不调用远程 `cleanup`。
- `cleanup` 只能处理 OwnershipRecord 明确归属于当前 Connection 的资源，并且必须
  单独展示删除计划、取得明确授权并返回逐项结果。
- 在未来授权契约稳定以前，非交互调用不得执行这些远程副作用，也不能降级为默认同意。

在宣布 `tunlite.driver/v1` 稳定前，实验协议必须明确：

- 超时和取消。
- 最大消息体。
- stdout 纯净性和 stderr 处理。
- 错误码。
- 交互规则。
- 脱敏规则。

### 6.3 Capability

Driver 通过 Capability 描述差异，而不是把所有 Provider 强塞进同一个 Schema。
初始 Capability 包括：

- `access`
- `publish`
- `socks`
- `persistent`
- `dynamicEndpoints`
- `interactiveAuth`
- `multiForward`
- `inspect`
- `diagnose`

核心根据 Capability 判断生命周期是否合法，以及哪些状态字段和命令有意义。

### 6.4 ExecutionPlan

`ExecutionPlan` 是最重要的职责边界。它描述操作系统应该执行什么，不描述如何
编写 systemd unit 或 plist。这个责任划分从 v1 起保持稳定，但下面的 JSON 字段只是
实验阶段的概念示例；在多类 Provider 验证完成前，它的序列化形状不是公共兼容承诺。

执行计划概念示例：

```json
{
  "mode": "direct",
  "exec": {
    "program": "/usr/local/bin/kubectl",
    "args": [
      "--context", "staging",
      "-n", "backend",
      "port-forward", "service/postgres", "5432:5432"
    ],
    "environment": {},
    "workingDirectory": null
  },
  "lifecycle": {
    "restart": "on-failure",
    "restartDelaySeconds": 10,
    "stopTimeoutSeconds": 5
  },
  "endpoints": [
    {
      "name": "default",
      "direction": "access",
      "protocol": "tcp",
      "address": "127.0.0.1:5432"
    }
  ],
  "healthChecks": [
    {
      "type": "tcp",
      "address": "127.0.0.1:5432",
      "timeoutMs": 1500
    }
  ],
  "redactions": []
}
```

`mode: direct` 时，`exec` 指向 Provider；`mode: managed` 时，`exec` 指向固定版本的
每 Connection Driver runtime。两种模式使用同一生命周期、端点、健康检查和脱敏
结构，核心不能从程序名称猜测模式。

强制安全规则：

- 命令必须是 `program` 加 `args[]`，禁止 Shell 字符串。
- Secret 不能进入 argv 或生成的服务文件。
- 环境变量可以引用外部 Secret 来源，但核心不能变成 Secret Store。
- 健康检查只能使用核心定义的有限类型。
- 相同的标准化输入和插件版本必须生成确定性计划。
- 插件不能直接生成平台服务文件。

### 6.5 内置 SSH Driver

SSH 内置在核心中，提供零摩擦的初次体验，但它仍必须实现与外部插件相同的逻辑
Driver 契约，并生成相同类型的执行计划。

内置实现可以直接调用 Go 接口，而不必把自己作为子进程启动；但核心编排中不能出现
SSH 特判。契约测试必须同时适用于内置 Driver 和外部 Driver。

SSH 继续基于系统 OpenSSH Client。Tunlite 不实现自己的 SSH 协议栈。

## 7. 插件分发与开放性

本章同样属于 v1 之后的 Driver 协议阶段。提前写清开放性和供应链边界，是为了避免
未来形成私有接口或中心商店依赖，不代表当前团队应先建设插件市场。

### 7.1 去中心化安装

用户无需 Tunlite 项目批准，即可从独立仓库安装：

```bash
tunlite plugin install github.com/acme/tunlite-driver-foo@v1.2.3
tunlite plugin install https://example.com/releases/tunlite-plugin.json
tunlite plugin install ./tunlite-driver-foo
```

官方索引可以提供 `tunlite plugin search`、验证标签和发现能力，但它只是可选
元数据，不是守门人，也不是运行时依赖。

插件分类：

- **内置：** 随 Tunlite 二进制发布，初始只有 SSH。
- **官方：** 由 Tunlite 组织在独立公开仓库维护。
- **社区：** 第三方插件，可选择进入社区索引。
- **直接安装：** 用户从任意明确来源安装的兼容插件。

### 7.2 仓库和 Release 约定

插件仓库建议包含：

```text
tunlite-plugin.json
README.md
LICENSE
schemas/
源代码
```

Release 提供平台专属可执行程序和校验和。Manifest 声明：

- 全局唯一插件 ID。
- 插件版本。
- 仓库和许可证。
- 支持的 Driver 协议版本。
- 最低 Tunlite 版本。
- Capability。
- 各平台 Artifact。

已安装版本不可变，并按来源、精确版本和 checksum 锁定。激活必须原子完成，上一个
版本要保留以便回滚。服务需要插件可执行程序时，必须引用不可变路径，不能依赖可变的
PATH 查找结果。

未来可以增加 OCI 分发，但 Driver 协议实验阶段优先支持 GitHub 风格 Release
Artifact 和直接 Manifest URL。插件分发不是 Tunlite 产品 v1 的发布门槛。

### 7.3 安全模型

插件本质上是任意本地可执行代码。JSON 消息只是接口，不是 Sandbox。

必须具备：

- 安装前展示来源、发布者、版本、许可证、checksum 和验证状态。
- 校验 checksum，但明确它只能证明获取内容与清单一致，不能证明发布者身份。
- 签名必须绑定发布者身份和来源；长期加入 Sigstore 等构建来源与证明链。
- 默认锁定版本，不静默自动升级插件。
- 导入配置绝不能自动下载或执行缺失插件。
- AI Agent 不能仅因为配置要求，就自行安装陌生插件；必须得到用户明确授权。
- 安装插件、创建远程资源和删除资源必须返回结构化风险与授权信息，具体契约在 Driver
  协议稳定前重新设计；不能只打印确认文字，也不能套用 V1 SSH 中不存在的批准状态。
- Connection 仍在引用插件时，正常卸载必须拒绝。
- 提供 `tunlite plugin doctor` 和 `tunlite plugin test`。
- 明确区分官方、已验证社区和未验证来源。
- 不声称实现了核心实际上无法提供的跨平台 Sandbox。

安装插件等价于安装一个本地程序，文档必须明确说明这一点。

### 7.4 兼容策略

- Driver 协议版本独立于 Tunlite 应用版本。
- 同一个协议大版本只允许向后兼容的增量变化；破坏性变化必须升级协议大版本。
- 出现第二个大版本后，核心至少同时支持最近两个稳定大版本。
- 插件声明所有支持的协议版本，并在使用前完成协商。
- Driver 配置随锁定的 Driver 软件版本迁移，并由该版本提供 `migrate` 实现；不再暴露
  第二个公共 Driver 配置版本字段。
- 官方 JSON Schema、Fixture 和与语言无关的兼容性测试套件共同定义契约。
- 可以发布 Go、TypeScript、Rust SDK，但 JSON 协议而不是 SDK 才是权威。

产品 v1 不依赖稳定 Driver 协议。不能只测试 SSH 就冻结 `tunlite.driver/v1`；协议先
使用 `tunlite.driver/experimental`，并且至少需要三种差异显著的参考 Driver：

1. 内置 SSH：多转发、访问、发布和 SOCKS。
2. Kubernetes port-forward：外部 CLI、Context/Resource 配置，以及 Pod
   消失后的进程重启。
3. Microsoft Dev Tunnels 或 Cloudflare 等动态端点 Provider：托管认证中继，
   并且端点只能在启动后得知。

## 8. Driver 路线

Driver 路线服务于验证，而不是预先承诺建设完整 Provider 矩阵。

1. **内置 SSH：** 产品 v1 的全部正式 Driver 范围。
2. **Kubernetes port-forward：** v1 发布后的第一个实验性外部 Driver，用于验证
   独立仓库、公开协议和 `direct` 模式；它不使用私有核心接口。
3. **第二个外部 Driver：** 从真实用户请求最多的 AWS SSM、GCP IAP、Azure
   Bastion、数据库认证代理等场景中选择，不按当前文档预定赢家。
4. **动态端点 Driver：** 只有出现明确 Publish 或动态端点需求时，才选择 Microsoft
   Dev Tunnels、Cloudflare 或同类 Provider 验证 `managed`、Inspect 和远程资源
   所有权。

每增加一个官方 Driver，必须同时证明存在持续用户需求、维护者和安全维护能力。
“能够包装”不是立项理由。在三个差异显著的 Driver 经真实生产使用前，协议保持实验
状态，不承诺 `tunlite.driver/v1` 稳定性。

Dev Containers、WSL、Codespaces、Docker 生命周期、SSHFS、rsync 和完整 VPN
管理不在核心 Driver 路线上。

新的官方 Driver 通常应放在独立公开仓库。这既能保持核心精简，也能证明第三方作者
拥有与官方维护者相同的扩展能力。

## 9. CLI 契约

V1 Preview 的公共命令面向 Connection，而不是绑定 SSH。下表是完整清单；未列入表格的
词不是 V1 公共命令。每次增加、删除或改变命令语义，都必须先修改本节并完成产品确认，
再修改代码。

### 9.1 Connection 声明

| 命令 | 语法 | 长期语义与当前判断 |
| --- | --- | --- |
| `add` | `tunlite add <name> [--driver ssh] <Driver args>` | 创建具名 Connection，并同步建立停止状态的原生服务定义。SSH 永久作为默认 Driver，因此常用写法是 `tunlite add db -L 5432:localhost:5432 user@host`。名称是全部生命周期操作的稳定身份。 |
| `list` | `tunlite list [--group group]` | 按 Group 分段列出 Tunlite 已管理的 Connection，不扫描未管理的系统服务；未设置归属的进入“未分组”。保留；它回答“我管理了什么”。 |
| `set` | `tunlite set <name> <Driver patch args>` | 先从声明确定 Driver，再由该 Driver 解析补丁；修改声明并在同一事务中联动原生服务。SSH 字段用可重复的 `--clear <field>` 显式清除，field 只能是 `identity`、`jump` 或 `options`；未知字段、重复清除和同字段设置+清除均拒绝。禁止用 `set` 更换 Driver。 |
| `rename` | `tunlite rename <name> <new-name>` | 原子修改 Connection 身份及其服务绑定。保留；不能退化成复制后删除。 |
| `remove` | `tunlite remove <name>` | 只允许删除已停止且不再持久启用的 Connection，同时删除其原生服务定义。保留完整词，不提供 `rm` 公共别名。 |

Core 只解析名称、`--driver`、Group 与全局机器参数，其余 argv 保序交给选中的 Driver。
SSH 接受受限的原生 `-p/-i/-J/-L/-R/-D/-o` 与唯一 `host` 或 `user@host`。Preview 的
用户名只允许 ASCII 字母、数字、`.`、`_`、`-`；UPN/多个 `@`、`+`、`DOMAIN\user` 以及
独立 `-l` 均不支持，这是公开兼容限制。未来 Driver 使用
`--driver <name>` 这一种选择方式和自己的参数解析器；不增加 `--ssh`、`--kubernetes`
等特殊选择器，也不把 Target 强加为所有 Driver 的 Core 概念。

### 9.2 运行与原生服务

| 命令 | 语法 | 长期语义与当前判断 |
| --- | --- | --- |
| `run` | `tunlite run <name>` | 在当前终端前台运行已有 Connection；退出即停止，不创建临时匿名配置。保留。 |
| `start` | `tunlite start <name> \| --group group \| --all` | 通过已有原生服务在后台运行，但不设置自动启动。保留。 |
| `stop` | `tunlite stop <name> \| --group group \| --all` | 把临时后台运行变为停止；对后台长期运行明确拒绝并提示使用 `disable`，避免“现在停了、下次登录又出现”的隐藏状态。保留。 |
| `restart` | `tunlite restart <name> \| --group group \| --all` | 重新启动后台进程，不改变是否持久启用。保留；这是高频运维动作，不强迫用户组合两条命令。 |
| `enable` | `tunlite enable (<name> \| --group group \| --all) [--scope user\|system]` | 设置自动启动并确保现在运行；新服务默认 user，未传 Scope 时保留已有范围，显式不同值执行可回滚迁移。macOS system 需要管理员授权；Linux system 尚未实现。保留。 |
| `disable` | `tunlite disable <name> \| --group group \| --all` | 把后台长期运行变为停止：同时停止当前进程并取消以后自动启动；对临时后台运行提示使用 `stop`。保留。 |
| `status` | `tunlite status [<name> \| --group group \| --all]` | 读取声明、服务、进程、健康和一致性状态；无目标时按 Group 查看全部。保留，作为脚本和 Agent 的状态入口。 |
| `logs` | `tunlite logs <name> [--lines N] [--follow]` | 通过原生服务适配器查看一条 Connection 的历史或实时日志。保留。 |
| `monitor` | `tunlite monitor [--group group] [--interval seconds]` | 唯一交互式实时总览；采用类似 `top` 的全屏布局，按 Group 分区显示版本、状态统计、对齐表格、详情和日志。`Enter`/右键进入详情，`Esc`/左键返回，最外层 `Esc` 退出；停止和重启需要 `y` 二次确认，其他键取消，启动直接执行。不支持 `--json`，自动化使用 `status --json`。保留，不再增加 `watch`。 |

四种标准模式继续存在于机器契约，但人类界面使用“状态 / 运行位置 / 自动启动 / 范围”表达。
原生服务定义从 `add` 起就存在；`start` 和 `enable` 的差异只在自动启动意图，不在是否
“注册过”。`enable` / `disable` 必须保留：删除两个动词并不会删除自动启动概念，反而会
让 `start` 同时承担临时启动和修改长期意图，让 `stop` 产生难以发现的长期副作用，并且
失去“只关闭自动启动”的对称表达。它们也与原生服务管理工具和 0.10.3 用户心智一致。
任何可能等待 SSH、原生服务管理器或批量状态采集的人工终端操作，都必须在进入等待前
输出动作指示；稳定 JSON Envelope 不得混入这些人类进度文本。

### 9.3 系统服务接管与漂移

| 命令 | 语法 | 长期语义与当前判断 |
| --- | --- | --- |
| `manage` | `tunlite manage [native-service] [--name name]` | 发现可安全还原且尚未受管的原生 SSH 服务，明确选择后导入声明和控制权；无目标时允许交互选择。保留。 |
| `unmanage` | `tunlite unmanage <name>` | 删除 Tunlite 声明和绑定记录，但保留可由系统继续管理的原生服务及其运行状态。既适用于接管来的服务，也适用于 Tunlite 创建的服务。保留。 |
| `resolve` | `tunlite resolve <name> --use tunlite\|system` | 只在外部修改造成真实漂移时使用；显式选择 Tunlite 声明或系统服务为事实来源。保留，不提供日常 `sync`。 |

`import` / `export` 不转移系统服务管理权；进入或离开 Tunlite 控制只使用 `manage` /
`unmanage`。`manage` 已包含发现，不再提供独立 `discover` 或 `adopt` 命令。

### 9.4 SSH 检查与修复

| 命令 | 语法 | 长期语义与当前判断 |
| --- | --- | --- |
| `check` | `tunlite check <connection-or-user@host> [-p port] [-i key] [-J host] [--timeout seconds]` | 对单个具名 Connection 或临时 SSH 目标执行只读连通性与认证检查。SSH 端口、私钥和跳板继续使用同一套原生短参数；临时目标只允许检查，不能借此运行匿名隧道。 |
| `doctor` | `tunlite doctor [name] [--offline] [--timeout seconds]` | 综合检查本机环境、配置、原生服务和一个或全部 Connection；给出结构化问题与动作。保留，与只检查单个 SSH 目标的 `check` 分工明确。 |

### 9.5 声明交换

| 命令 | 语法 | 长期语义与当前判断 |
| --- | --- | --- |
| `import` | `tunlite import <path> [--force]` | 将严格校验的 V1 声明文件合并到本机配置；不修改来源文件、不自动取得系统服务，也不隐式启动。保留。 |
| `export` | `tunlite export [--output path\|-] [--force]` | 导出可迁移的 Connection 声明，不导出本机服务绑定或凭据；已存在文件只有显式 `--force` 才覆盖。保留。 |
| `validate` | `tunlite validate [path]` | 校验指定声明文件；省略路径时校验当前配置，不改变状态。保留。 |

### 9.6 机器契约

| 命令 | 语法 | 长期语义与当前判断 |
| --- | --- | --- |
| `schema` | `tunlite schema [connection\|connection-list\|ssh\|cli\|service-state] [--output path\|-] [--force]` | 输出随当前版本发布的正式 JSON Schema；已存在文件只有显式 `--force` 才覆盖。保留，供 Agent、编辑器和集成工具使用。 |
| `capabilities` | `tunlite capabilities` | 返回当前二进制、平台、Driver、原生服务和公共命令能力。保留；调用方不能靠猜版本判断能力。 |

### 9.7 软件分发与基础入口

安装、升级和卸载由安装当前二进制的发行渠道负责。手工安装用户从官方 Release 下载、
校验并替换 `tunlite`；包管理器和未来 Desktop 只管理各自拥有的程序文件。核心 CLI
不提供 `install`、`update`、`uninstall`，也不建立 `tun` 别名。删除或替换 CLI 不得
停止 Connection，也不得删除配置、原生服务或日志。

| 命令 | 语法 | 长期语义与当前判断 |
| --- | --- | --- |
| `help` | `tunlite help [command]` | 展示完整命令分组或单个命令帮助。保留，并与本节和机器契约保持一致。 |
| `version` | `tunlite version` | 输出当前版本；同时保留惯用的全局 `--version` / `-v`。保留。 |

V1 一共公开 26 个一级命令：

```text
add capabilities check disable doctor enable export help import list logs manage
monitor remove rename resolve restart run schema set start status stop
unmanage validate version
```

V1 不提供 Shell 补全命令，也不提供 `install`、`update`、`uninstall`、`migrate`、`skill`、
`plugin`、`discover`、`adopt`、`sync`、`watch`、`rm`、`tun`、`setup-key` 等别名或入口。
`tunlite plugin install / list / update / remove / doctor / test`
属于 Driver 协议经多类 Provider 验证后的未来候选接口，不是当前 V1 承诺，不能提前进入
帮助、Skill 或机器能力清单。

### 9.8 全局行为

以下是修饰公共命令的全局选项，不是一级命令：

| 选项 | 稳定语义 |
| --- | --- |
| `--json` | 输出稳定机器 Envelope；交互式 `monitor` 明确不支持。 |
| `--non-interactive` | 整条调用链不得提示输入；需要选择、认证或人工交互时返回结构化错误和动作。 |
| `--dry-run` | 对会改变状态的命令只生成计划，不落盘、不注册服务、不启动进程、不修改远端。 |
| `--lang en\|zh-CN` | 选择已本地化的帮助、生命周期进度、状态和 Monitor 界面；优先于 `TUNLITE_LANG` 和系统语言。Preview 中未完成本地化的次要输出仍为英文；该选项不改变命令、配置或 JSON 机器契约。 |

`run`、`start` 和 `enable` 只接受已有 Connection 名称，不提供另一套内联 SSH 配置面。

官方 Driver 可以使用命名空间参数或配置文档表达专属配置。核心不能把每个 Provider
的参数全部堆入同一个全局参数解析器。

公共机器接口必须提供：

- 所有自动化相关命令支持 `--json`，并在每个 JSON 响应的稳定顶层 Envelope 中返回
  唯一的机器契约版本 `protocolVersion`。
- 官方发布版本同时发布版本化 JSON Schema；兼容性由 Schema 和契约测试定义，
  不能只依赖示例文档。
- `--non-interactive` 保证整个调用链绝不提示输入。需要认证、选择或人工交互时返回结构化
  状态，不得等待 stdin。
- 所有改变状态的命令支持 `--dry-run`。远端转发和公开监听在 `add`、`set`、`import`、
  `manage` 与 dry-run 结果中返回明确 endpoint、风险等级和 warning，但 V1 不提供
  `--approve`，不保存批准状态，也不要求用户重复表达参数中已经明确的意图。
- Apply 必须可重试和幂等；同一个操作请求重复提交不能重复创建远程资源。
- 稳定、公开的退出码：`0` 成功、`1` 执行失败、`2` 参数或输入无效、`3` 对象不存在、
  `4` 需要认证或人工交互、`5` 批量操作部分成功。
- 结构化错误包含 `code`、`driver`、`connection`、`message`、`riskLevel` 和
  `actions[]`。
- `actions[]` 使用版本化动作类型和参数，不能要求 Agent 解析或直接执行错误信息中的
  Shell 命令字符串。
- 批量操作返回逐 Connection 的 `results[]` 和整体 `outcome`；部分失败不能只通过
  一条顶层错误丢失已经成功或失败的对象。
- `capabilities` 可以机器查询当前核心、平台和 Driver 支持的动作、执行模式、状态
  精度和风险信息。
- Agent 不需要解析人类说明文本，也不能把人类文本中的建议当作授权。
- Tunlite 自己生成的结构化 stdout、stderr、服务文件、诊断和导出必须对已知 Secret
  执行脱敏，并且不得主动把 Secret 值写入 argv 或服务定义。Provider、第三方 Driver
  和原生服务日志属于不可信外部输出；Tunlite 可以脱敏已知值，但不能虚假承诺识别并
  清除其中所有未知敏感信息，文档和 Agent 必须如实说明这条边界。

`protocolVersion` 是递增整数，只标识 JSON 机器契约，不作为 CLI 请求参数，也不是权限、
认证或防绕过机制。请求、响应、错误、事件或动作语义发生破坏性变化时才升级；新增可选
字段不升级，接收方必须忽略不认识的可选字段。所有 JSON 成功与错误响应都必须返回它；
流式接口必须在第一帧返回稳定 Envelope 和 `protocolVersion`，然后才能发送业务事件。

Desktop 启动时必须先调用 `capabilities --json` 完成握手，在确认响应版本等于自身支持的
版本后才能启用操作；Agent 和正式自动化在执行动作前遵循相同流程。不维护版本范围、
回退选择或自动切换 Core。绕过握手、解析人类输出或忽略版本属于不受支持的调用方式，
不能靠 `protocolVersion` 阻止。未来高风险远程副作用必须另行设计授权契约，不能预先
复用 V1 中不存在的 approval 状态。
`protocolVersion` 与持久数据的 `configVersion` 是两个独立问题。

`protocolVersion` 只属于 CLI 机器响应 Envelope，不能写入原始 JSON Schema、导出的
Connection 配置或其他可保存文档。三类 JSON 的版本标识固定为：

| JSON 类型 | 根节点版本标识 |
|---|---|
| CLI JSON 成功响应、错误响应和流式首帧 | `protocolVersion` |
| 原始 JSON Schema 文档 | `$schema`，并使用 `$id` 标识具体 Schema |
| `Connection`、`ConnectionList` 和 `export / import` 配置 | `configVersion` |

CLI 响应携带配置或 Schema 时保留两层边界：外层响应使用 `protocolVersion`，内层公共
配置使用 `configVersion`，内层 Schema 使用 `$schema` 和 `$id`。`--json` 始终输出
CLI 机器响应 Envelope；`schema` 或 `export` 通过 `--output <path>` 写入原始文档，
通过 `--output -` 输出原始文档到 stdout，原始输出不套响应 Envelope。两种 stdout
模式不得在同一次调用中混用。

风险等级至少包含 `low`、`medium`、`high` 和 `critical`。安装本地可执行插件、公开
监听地址、Publish、远程资源创建和远程资源删除不得标记为 `low`。

Agent Skill 不是独立事实来源。它必须从 CLI Schema 和命令元数据生成，或由测试证明
与其一致。CI 必须验证 Skill 中的命令、字段、退出码和示例，禁止长期维护一份无法
校验的手写契约副本。

## 10. 明确改变或删除的能力

v1 不能为了保留 0.x 的每个字段，而把 daemon 换个名字重新实现。

删除：

- `tunlite daemon ...`。
- 中心 IPC socket 和 daemon PID 文件。
- daemon ping 和 daemon 专属退出码语义。
- 全局 daemon 生命周期告警。
- 手工修改配置后的内存自动 reconcile。
- 用一个总服务启动所有 Connection 的 `install service` 模式。
- `tunlite install skill ...`、`tunlite uninstall skill ...` 和 `install skill --link`。
  Skill 的安装生命周期属于 Agent；Tunlite 只发布标准 `use-tunlite` Skill 包。
- 核心 CLI 自身的 `install`、`update`、`uninstall` 和一次性历史命令 `migrate`。
- 自动建立 `tun` 短命令，以及迁移日志、配置摘要、额外备份和自动回滚子系统。

改变：

- `add` 在受支持平台上同时生成默认禁用、默认停止的原生服务定义。
- `run <name>` 只在前台运行已有具名 Connection，不接受任意 SSH 参数。
- `start` 进入可自动恢复但不跨用户会话的 `background`，`stop` 只退出该模式。
- `enable` 立即进入长期可靠运行的 `persistent`，`disable` 立即停止并回到 `stopped`。
- `restart` 只允许用于正在运行的标准模式，对 `stopped` 不得隐式执行 `start`。
- `remove` 只允许删除 `stopped` Connection；正在运行或长期启用时必须拒绝。
- `manage` 在不中断现有连接的前提下，将符合规范的原生服务完整纳入 Tunlite 管理。
- `unmanage` 保留原生服务及其当前标准模式，只移除 Tunlite 的声明与管理关系。
- 正常修改自动联动原生服务；`resolve` 只处理绕过 Tunlite 修改造成的异常漂移，并
  要求明确选择以 Tunlite 或系统为准。
- `status` 返回公共 OS 状态和 Driver 支持的细节，不伪造所有 Driver 都能
  证明端到端连接。
- `monitor` 保留为唯一实时交互入口，采用前台轮询视图；不新增 `status --watch`，也
  不能以它为理由引入后台控制进程。用户触发生命周期动作后，界面立即显示
  `starting`、`stopping` 或 `restarting` 临时状态；这些只是前台操作反馈，不写入
  Connection 声明或 Service Record，完成后由真实观察状态替换。
- 日志来自原生服务日志或确定性的每 Connection 日志文件，不经过 IPC Fan-out。
- Webhook 告警暂缓，不纳入 V1 基础能力恢复范围。未来如果恢复，应采用无状态 Hook、
  插件集成或外部监控，不能要求核心常驻事件引擎；在此之前 CLI、能力契约和文档不得
  宣称已经支持。

保留或增强：

- 声明式名称、Group 分区和批量操作。
- SSH `-L`、`-R`、`-D`、Jump Host、Identity File 和安全 argv 生成。
- 导入、导出和配置校验。
- 认证前置检查和可操作诊断。
- Agent Skill、JSON 输出和稳定退出行为。
- 单文件二进制分发，以及由发行渠道负责且不影响 Connection 的安装、升级和卸载。
- `check` 和 `doctor` 对 SSH 认证问题给出系统 OpenSSH 操作指引；V1 不生成密钥或
  修改远端 `authorized_keys`。
- 没有可用服务管理器时使用前台 `run`。

## 11. 规模与发行生命周期

默认持久架构必须能够合理管理几十乃至上百个 Connection。因此，在操作系统能够直接
执行 Provider 命令时，不能为每个 Connection 创建一个 Node 或 Go Supervisor
runtime。

未启动的 Connection 只有声明和原生服务定义，不占用常驻内存或进程。规模成本主要
是少量文件和服务管理器元数据；只有实际运行的 Connection 才产生 Provider 进程。

用户把多个 Forward 声明为同一个 SSH Connection 时，它们可以共享一个 SSH 进程。
Tunlite 不能擅自合并名称不同的 Connection，因为这会改变隔离和生命周期语义。

### 11.1 安装

Go CLI 以签名并带 checksum 的多平台二进制分发。随着维护者到位，包管理器应覆盖
Homebrew、Scoop 或 winget，以及 Linux 常见渠道。npm 可以保留为下载 Shim，但
Node 不是 v1 runtime 依赖。

发行渠道拥有它安装的程序文件：包管理器负责包管理器版本的安装、升级和卸载，Desktop
负责应用内版本，手工安装用户按官方文档替换原生二进制。`bootstrap.sh` 或未来的下载页
可以帮助识别平台、下载和校验 Release，但它们是发行辅助，不调用核心 CLI 自安装，
不创建 `tun`，不修改 Shell，也不迁移配置。

Tunlite 提供两种可以共存的发行形态：

- **Core/CLI 发行包：** 只安装 Go Core 与 `tunlite` 命令，面向终端用户、服务器、
  脚本和 Agent。
- **Desktop 发行包：** 在一个安装包中同时包含 Desktop、与其兼容的 Go Core 和
  `tunlite` 命令入口。Desktop 用户只需下载和安装一次。

Core/CLI 与 Desktop 分别拥有独立的软件发布版本。每个 Desktop 版本固定携带一个经过
完整验证的精确 Core 版本，用户不负责选择 Core 或解决两者的兼容关系；Desktop 与
Core/CLI 的发布版本号不要求相同。

主产品层长期只使用四个明确的版本概念：

- **Core version：** Core/CLI 的软件发布版本。
- **Desktop version：** Desktop 的软件发布版本。
- **`configVersion`：** 第 5 节定义的公共、可迁移配置格式契约。
- **`protocolVersion`：** 第 9 节定义的 Desktop/Agent 与 Core 机器通信契约。

前两个是产品发版号，后两个是兼容性契约，不能互相比较、复用或要求数值一致。外部
Driver 的软件版本和 Driver 协议版本继续按第 7 节独立管理，不混入这四个主产品版本。

Desktop 永远通过确定的应用内路径调用随包 Core，不发现、不复用也不切换到 Homebrew、
winget、手工安装或其他 Desktop 带来的外部 Core。Desktop 所带的 `tunlite` 命令入口
同样调用这份随包 Core；独立 Core/CLI 发行包则始终调用自己的 Core。系统 `PATH` 最终
选择哪个 `tunlite` 入口属于普通命令解析问题，不引入 Tunlite 的“活动 Core”、安装
所有权、接管或更新权转移概念。各发行渠道不得覆盖其他渠道的二进制；目标命令路径
已被占用时应保留现状并提示用户调整 `PATH`。

安装过程只增加当前发行包的程序文件和命令入口，不迁移配置、不改变原生服务，也不
重启 Connection。首次安装且没有现有数据时，由第一次 Core 写操作创建当前
`configVersion` 的共享数据；已经存在数据时按原样保留。

不同 Core 二进制只共享一份规范配置和本机状态，并遵守同一套跨进程锁协议，而不是让
所有操作争用一把全局锁：

- 配置迁移使用全局排他的迁移门禁；它必须等待已经开始的普通操作结束，并阻止新操作
  在迁移完成前进入。
- 普通操作通过迁移门禁的共享侧后使用每 Connection 锁；门禁不让普通操作彼此串行，
  不同 Connection 可以并行处理。
- `manage`、`unmanage` 和服务绑定变更使用服务绑定锁，防止同一原生服务被重复接管。

所有锁只在相应命令事务期间持有，写入必须原子落盘；锁不是 daemon，也不负责选择
Core。`configVersion` 按第 5 节决定某个 Core 能否操作公共配置。兼容 Core 可以交替
操作；新 Core 迁移配置后，不兼容的旧 Core 必须明确拒绝。

全局迁移门禁只保护“读取共享数据—迁移—原子提交”这段短事务，完成后必须立即释放。
它不得覆盖 `tunlite run` 的 Provider 运行周期、`logs --follow` 的流式读取周期、任何
后台 Provider 的生命周期或 Desktop 的存活周期。`run` 在门禁内读取配置快照后即释放，
防止重复运行使用独立的 Connection 运行锁，不能复用配置迁移门禁。

原生服务和正在运行的 Connection 不能依赖 Desktop 应用包、独立 Core 或 CLI launcher
继续存在。服务定义只能直接引用 Provider，或引用位于共享稳定运行目录中的锁定 Driver
runtime；仍被 Connection 引用的 runtime 不得随某个发行包升级或卸载而删除。

### 11.2 升级与降级

Desktop 界面、随包 Core 和它的 CLI 入口必须作为一个整体原子升级；失败时整体回滚。
独立 Core/CLI 按自己的渠道原子升级，双方不发现、不替换也不更新对方的二进制。升级
安装包本身只替换程序文件，不迁移共享配置，也不重启 Connection。

Core 的补丁版本独立发布。修复影响 Desktop 使用的功能、随 Desktop 安装的 CLI、数据
安全或安全基线时，必须同时触发一个新的 Desktop 补丁版本；该版本可以不改变界面，
唯一产品变化就是把随包 Core 固定到已验证的修复版本。安全漏洞、数据损坏和崩溃类问题
不得等待 Desktop 的常规功能发布；只影响 Desktop 不支持的平台或不可达代码路径时，
可以随下一次 Desktop 发布带入。

这条补丁链路必须自动化完成 Core 固定、组合测试、签名和 Desktop 发布。传输层可以只
下载二进制差异，但产品和回滚层面仍是一份新的、原子更新的 Desktop 发行包，不能绕过
Desktop 更新器热替换应用内 Core。用户不选择 Core 版本，也不处理兼容关系。纯 Bug
修复不改变机器或配置契约时，`protocolVersion` 和 `configVersion` 都保持不变。

新 Core 可以按第 5 节读取旧配置。只有用户第一次执行确实需要新格式的写操作时，才能
在明确提示、原始备份和跨进程锁保护下原子迁移 `configVersion`。因此仅完成程序升级、
启动 Desktop、查看列表或检查状态，都不能让旧 Core 失去兼容性。

更新 Core 本身不能成为重启 Provider 的理由。只有 Connection 的生成计划或锁定
Driver runtime 确实变化，并且操作计划已经说明中断影响、用户允许中断时，才能重启
相应 Provider。生成物必须记录足够信息，使更新或重新安装后的 Core 能重新检查并
reconcile 它们。

如果配置尚未迁移，用户可以回滚或降级到仍兼容该 `configVersion` 的旧 Core。配置
一旦完成迁移，旧 Core 必须拒绝读取和管理；产品不提供自动向下迁移。此时只有两个
安全恢复方案：重新升级到兼容 Core，或者显式恢复迁移前备份，并明确告知用户恢复会
丢弃迁移后的配置修改。

### 11.3 卸载与彻底清理

操作系统的普通卸载只删除当前发行包创建的程序文件和仍指向该发行包的命令入口：

- 卸载 Desktop 删除界面、随包 Core 和 Desktop 创建的 CLI 入口。
- 卸载独立 Core/CLI 只删除该渠道安装的 Core 和命令入口。
- 两者默认都保留共享配置、本机状态、锁定 Driver runtime、原生服务和正在运行的
  Connection；已经安装的另一发行形态可在 `configVersion` 兼容时继续管理。

删除应用、Core 或 CLI launcher 不得停止 Provider。卸载器也不能因为没有发现另一份
Core，就推断用户希望删除共享数据。

V1 不提供 `purge` 或其他彻底清理命令。需要彻底清理时，用户必须在发行渠道删除程序
文件之前，先通过普通生命周期命令逐条处理 Connection，再按官方文档手工删除明确的
共享数据路径。未来如果提供 Desktop 中的“删除全部 Tunlite 数据”或新的 Core 清理
入口，必须重新经过产品决策；它必须先展示完整计划，并要求用户在以下两种结果中明确
选择：

1. **交还并保留 Connection：** 按 `unmanage` 的安全条件把原生服务交还操作系统，
   保持当前运行状态，然后删除 Tunlite 的共享配置和本机状态。
2. **停止并删除本地 Connection：** 停止所有受管 Provider、删除原生服务，再删除
   共享配置、本机状态以及已经没有引用的 Driver runtime。

彻底清理也不得自动删除 Driver 创建的远程资源；远程 `cleanup` 始终是另外一次带计划
和授权的操作。存在 `OwnershipRecord` 时，清理流程不能直接销毁最后一份所有权记录；
用户必须先完成远程 `cleanup`，或显式导出可恢复的所有权记录并确认保留远程资源。

## 12. 平台策略

- Linux/systemd 用户服务是后台能力的参考实现，但不能宣称所有 Linux 都提供
  systemd 或可用的 `systemctl --user`；Linux system Scope 未实现，Capability 必须明确。
- macOS/launchd 用户 LaunchAgent 是默认的一等支持范围；显式 `enable --scope system`
  可以迁移为 LaunchDaemon。Intel 与 Apple Silicon 使用同一语义，系统范围需要管理员
  授权且不得在非交互模式等待密码。
- v1 首发平台是 Linux 和 macOS。Windows 不进入首发支持矩阵，原因是 Task Scheduler
  Adapter 和真实 Windows 验证不在本次交付范围，而不是把 GUI 当作某个平台
  是否获得核心支持的前置条件。
- Alpine/OpenRC、Void/runit、容器、CI、没有用户级服务管理器的精简系统和其他
  尚无 Adapter 的平台，Connection 标记为 `foreground-only`，使用
  `tunlite run <name>`。后台命令返回能力不支持，不能回退到 nohup、PID 文件或隐藏
  Supervisor。
- 早于用户登录的系统级自动启动只在 macOS system Scope 承诺；必须显式选择、授权和
  测试，不能与默认用户级 `enable` 混为一谈。

公共契约只能承诺各平台能够诚实提供的交集。平台特有信息可以放在可选命名空间对象中，
不能为了表面一致而伪造状态。

### 12.1 桌面端产品方向

Core/CLI 和 Desktop 是同一产品的两个入口，不是两套产品，也不能互相成为是否支持某个
操作系统的前置条件。Core/CLI 面向开发者、脚本和 Agent，本身必须能够完整管理连接；
Desktop 面向希望持续看到状态并用图形界面完成日常控制的用户，是重要但可选的体验层。

Desktop 应从一开始同时面向 macOS 和 Windows：

```text
macOS Menu Bar ─┐
                ├─> Tunlite Go Core -> OS Service Adapter -> Provider
Windows Tray ───┘
```

两个桌面入口使用同一套信息架构和机器契约，至少提供整体健康状态、Connection 列表、
启停与重启、日志与诊断、端点复制和断线通知。macOS 使用符合平台习惯的 Menu Bar，
Windows 使用 System Tray；不能因为实现外观不同而产生不同的 Connection 语义。

Desktop 只是 Core 的可视化控制器：

- 不保存第二份配置，不复制 Core 的业务规则。
- 不直接保活 Connection，也不成为新的中心 daemon。
- 关闭 Desktop 后，原生服务管理器中的 Connection 必须继续运行。
- CLI 与 Desktop 中的动作、风险和结果必须一致。

用户不需要先安装 Core 再安装 Desktop。Desktop 发行包自带匹配的 Core 和 CLI 入口，
一次安装即可同时获得图形界面、`tunlite` 命令和 Agent 所需的机器接口；它们最终调用
第 11 节定义的随包 Core。只需要 CLI 的用户则继续下载更小的 Core/CLI 发行包；两种
发行形态共存时仍只共享一份数据，不共享或争夺某个“活动 Core”。

Desktop 按第 9 节先通过 `capabilities --json` 检查 `protocolVersion`，再调用 Core；
它不参与配置迁移，也不能用协议版本替代 `configVersion`。不兼容时不得猜测字段或继续
发起修改，只显示清楚的安装损坏与恢复指引。由于 Desktop 与随包 Core 总是整体发布和
原子更新，正常安装不会出现高低版本组合；一旦出现，应视为安装不完整或文件被外部替换。

Linux Desktop 是否提供 Tray 由真实需求决定，不是核心 Linux 支持的门槛。桌面端在
Core/CLI v1 稳定后作为独立产品阶段推进；如果未来面向更广泛桌面用户进行正式发布，
macOS Menu Bar 与 Windows Tray 应作为同一个 Desktop 计划设计，而不是只为 Windows
建立一套孤立 GUI。

### 12.2 Windows 平台方向

Windows 是后续正式支持的平台。它的核心运行架构是：

```text
Tunlite Go Core -> 每 Connection 一个 Task Scheduler 任务 -> Provider
```

Task Scheduler Adapter 为每个 Connection 创建独立的用户级任务，承载 `stopped`、
`background` 和 `persistent`；任务直接运行 Provider，不恢复中心 daemon。Windows
Core/CLI 是否正式支持，只由这条核心链路的可靠性决定，不以 Windows Tray 是否已经
发布作为硬性门槛。

默认 Windows 运行方式面向当前登录用户，不需要管理员权限，并使用该用户的 OpenSSH、
密钥、SSH Agent 和凭据环境。它承诺关闭终端后继续运行、异常退出恢复，以及用户登录
后的自动恢复。无人登录时也必须启动的系统级 Windows Service 属于未来独立的高权限
Machine Mode，不是默认 Windows 体验。

Windows 只有同时满足以下条件后才能进入正式核心支持矩阵：

- 每 Connection Task Scheduler Adapter 完成启停、自动启动、失败恢复、状态、日志、
  原子更新和回滚的真实 Windows 验证。
- Windows 10/11 的发行渠道安装、OpenSSH 检查、升级和卸载形成完整用户旅程。
Windows Tray 随跨平台 Desktop 计划交付，可以与 Windows Core 支持同期或随后发布，
但不能形成另一套配置和生命周期实现。

## 13. 仓库与开发策略

重写保留现有 Git 历史和项目身份。

- Node `v0.10.3` 已冻结为 0.x 最终版本，不再迭代；v1 在 `rewrite/v1-go` 开发。
- 使用独立 Git Worktree 做物理隔离，不复制仓库，也不永久保留 `next/` 子树。
- v1 切换前，为最后一个 Node 版本打 Tag；必要时保留 `legacy/node-0.x` 维护分支。
- Go 版本成为默认分支时，代码位于仓库根目录。
- 不长期发布两套核心实现或两套 runtime backend。
- 用与语言无关的 V1 Preview 黑盒契约测试固定输入命令、初始配置、退出码、stdout JSON、
  stderr 和最终状态。
- 公开仓库保持唯一权威。私有开发不能产生第二份事实来源。

## 14. 周发布计划

目标明确为：一周完成可运行的 Go SSH 预览构建，并在 2026 年 8 月 31 日前公开发布
Tunlite V1 预览版 `v1.0.0-preview.1`。AI Agent 负责并行实现、测试、文档和审查，
人负责产品判断、风险授权和最终验收。

版本流程只保留两级：

- `v1.0.0-preview.N`：公开预览；需要修订时递增 `N`。
- `v1.0.0`：验证完成后的正式稳定版。

不引入 Alpha、Beta、RC 等额外层级。

### 第一周：8 月 17 日—8 月 23 日

完成内部可运行的 Go SSH 预览构建：

- 完成单二进制 Go CLI、内置 SSH Driver 和通用 `ExecutionPlan` 边界。
- 覆盖 SSH `-L`、`-R`、`-D`、多 Forward、Jump Host、Identity File 和前台运行。
- 提供配置管理、`--json`、`--non-interactive`、`--dry-run` 和基础诊断。
- 用 V1 Preview 黑盒契约、真实 OpenSSH 端到端测试和测试用假 Driver 验证行为与扩展边界。

### 第二周：8 月 24 日—8 月 30 日

完成公开预览版的发布准备：

- 完成 systemd 与 launchd 的每 Connection 原生服务。
- 完成 `add` 时生成禁用服务、`start`、`stop`、`restart`、`enable`、`disable`、
  `status`、`logs`、`manage`、`unmanage` 和异常漂移 `resolve`。
- 完成五维状态、批量结果和 Agent Skill 一致性。
- 完成签名二进制、checksum、安装文档、支持矩阵和预览版发布验证。

Windows Task Scheduler Adapter、macOS/Windows Desktop、外部插件、Kubernetes、云
Driver、`managed` runtime 和托管服务不进入本次发布范围。Windows 不以实验或尽力
支持的名义随首版发布；它按第 12.2 节独立验收。Desktop 按第 12.1 节作为横跨 macOS
和 Windows 的统一产品阶段推进。

### 公开预览发布：不晚于 8 月 31 日

只有 SSH 核心链路、systemd、launchd 通过自动测试与发布检查，才发布
`v1.0.0-preview.1`。若非核心渠道集成尚未完成，可以在预览版后补充，不能扩大范围
或推迟核心发布。

### 公开预览版之后

根据真实反馈发布必要的后续预览版，验证完成后直接发布稳定的 `v1.0.0`。同时验证
用户留存、多隧道使用和非 SSH 需求；达到需求门槛后，再启动实验性 Driver 协议和
Kubernetes Driver。公开协议仍需三类 Provider 的真实使用验证后才能稳定。

## 15. 未来提案的决策检查

接受新功能前，必须回答：

1. 在当前阶段，它是否直接改善 SSH 安装、管理、可靠性、迁移或 Agent 安全性？如果
   是新 Driver，是否已经达到发布后的真实需求门槛？
2. 它是否保持 Connection、Driver、ExecutionPlan、状态和 OS Adapter 的既有边界，
   避免未来再做一次核心重写？
3. 它能否作为 Driver 或无状态核心能力实现，而不是新增中心进程？
4. 没有 Tunlite 时，用户是否仍能检查和操作底层原生工具？
5. 它的机器接口是否版本化、幂等并且适合 Agent 安全使用？
6. 它是否把凭据留在 Provider，而不是建立 Tunlite Secret Store？
7. 外部 Driver 能否通过公共协议获得与官方相同的能力？
8. 是否有真实需求和维护者足以承担长期兼容与安全成本？

如果第一个问题的答案是否定的，这项功能应当推迟或属于另一个项目。如果第二至第八个
问题有任何一个是否定的，必须修改设计后才能实现。

## 16. 一句话方向

> 当前，Tunlite 要成为面向开发者和 AI Agent 的可靠 SSH 隧道管理器：声明一次，
> 由操作系统原生服务持续运行，Tunlite 自身不成为后台控制平面。

长期扩展通过稳定的 Connection、Driver 和 ExecutionPlan 边界增量加入；扩展性从
v1 开始预留，但外部协议只在真实需求和多类 Provider 验证后稳定。
