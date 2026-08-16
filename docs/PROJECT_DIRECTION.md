# Tunlite 项目方向

- **状态：** 已接受
- **适用范围：** Tunlite v1 及后续版本
- **最后更新：** 2026-08-16

本文档是 Tunlite 产品方向与 v1 架构的唯一事实来源。README、当前实现和
0.x 文档描述的是已经发布的 Node.js/daemon 产品；规划未来工作时，它们不能
覆盖本文档的决策。

任何改变下述决策的提案，都必须在同一个 Pull Request 中修改本文档，并说明
被改变的取舍。不得另外创建相互竞争的路线图或架构指引。

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
- 为 Agent 提供版本化 Schema、非交互保证、计划与审批，而不只提供 `--json`。
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
- 持久连接和前台连接两种生命周期。
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

## 2. 已接受的核心决策

下列决策已经接受，后续实现必须以此为准。

1. 使用单文件 Go 二进制重写 v1 核心。
2. 删除中心 Tunlite daemon、IPC socket、daemon PID 文件和全局内存
   reconcile 循环。
3. 由 systemd、launchd 和 Windows Task Scheduler 负责持久进程生命周期。
4. 每个已启用的 Connection 对应一个操作系统托管的服务。
5. 只要可行，服务直接运行 Driver 生成的底层可执行程序。一个已经编译完成的
   持久连接，不应仅仅为了存活而依赖 Tunlite 进程。
6. 保留 `tunlite run` 作为附着终端的前台模式。它可以提供更丰富的监督能力，
   但不是默认的持久架构。
7. 生成的服务定义是可以重建的产物。声明式配置和锁定的插件信息才是事实来源。
8. 增加 `tunlite sync`，检测并修复配置、生成物、插件安装和操作系统服务状态
   之间的漂移。
9. SSH 是内置 Driver，但必须实现与外部 Driver 相同的逻辑接口。
10. v1 的市场定位和完整交付范围是 SSH；外部插件系统不得阻塞 v1 发布。
11. 外部扩展使用进程外、与语言无关、带版本的 JSON Driver 协议，但该协议必须
    先以实验版验证，不能因为产品版本叫 v1 就提前冻结为 `tunlite.driver/v1`。
12. 用户未来可以直接从第三方仓库安装插件。官方索引可以帮助发现，但绝不能成为
    必经入口。
13. 公开仓库是代码、Issue、CI、Tag 和 Release 的唯一权威来源。私有仓库可以
    存放安全或商业资料，但不能维护另一份分叉的开源核心实现。

### 2.1 v1 必须保留的扩展边界

“v1 只正式交付 SSH”是发布范围决定，不是把核心实现成 SSH 专用程序。v1 必须完成
以下内部边界，保证未来增加 Driver 以增量开发为主，而不是进行第二次核心重写：

- 公共配置使用通用 Connection Envelope；SSH 字段只能存在于 SSH Driver 的
  `spec.config` 中。
- 核心编排只调用内部 Driver 接口，不直接判断 SSH Host、Forward 或 OpenSSH 参数。
- SSH 必须生成通用 `ExecutionPlan`；OS Service Adapter 只消费执行计划，不知道
  计划来自 SSH、Kubernetes 还是其他 Driver。
- CLI 的选择、批量操作、四维状态、日志、`sync`、审批和 JSON 结果面向
  Connection，不以 Tunnel 专属结构作为公共模型。
- `ExecutionPlan` 从 v1 起保留明确的执行模式字段；v1 只正式使用 `direct`，但不能
  用依赖 SSH 程序名的逻辑阻断未来 `managed`。
- 使用至少一个测试用假 Driver 验证核心没有 SSH 特判。该 Driver 不需要发布，也不
  需要引入插件安装系统。

v1 正式发布后，Connection Envelope、CLI 机器契约、状态维度和 OS Adapter 边界应
以兼容扩展和显式迁移为主，不能为了加入第一个外部 Driver 整体替换。实验 Driver
协议仍允许在稳定前调整；预留扩展性不等于提前冻结未经生产验证的公共协议。

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

- 配置加载、校验、迁移、导入和导出。
- Connection 名称、标签、选择和批量操作。
- Driver 发现、协议协商和调用。
- 阶段 5 以后才加入插件安装、校验、锁定、更新、回滚和卸载。
- 把 `ExecutionPlan` 转换成平台原生服务产物。
- 操作系统服务的启用、停用、重启、状态和日志接入。
- 通用健康检查和通用状态展示。
- `--json`、稳定退出码、脱敏、诊断和 Agent 集成。
- 通过 `tunlite sync` 检测和修复漂移。

核心不能理解 Kubernetes Resource、AWS Instance、Cloudflare Tunnel 等
Provider 专属字段。

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

`tunlite run` 继续服务于临时连接、容器、CI 和调试场景。它附着在当前终端，
调用者停止时退出。

### 4.4 状态模型

公共状态不能压缩成一个枚举。每条 Connection 必须同时报告四个互不替代的维度：

| 字段 | 取值 | 回答的问题 |
|---|---|---|
| `desiredState` | `enabled`、`disabled` | 用户希望它是否持续运行？ |
| `processState` | `inactive`、`starting`、`running`、`failed`、`unknown` | OS 托管进程现在怎样？ |
| `health` | `unknown`、`healthy`、`unhealthy` | 已声明端点是否有健康证据？ |
| `syncState` | `synced`、`drifted`、`missing`、`blocked` | 声明、生成物、依赖和 OS 服务是否一致？ |

`processState: running` 只表示进程仍在运行，不能声称端到端健康。
`desiredState: enabled` 也不能掩盖服务缺失或插件缺失。

认证失败、依赖缺失、端口冲突等原因放入版本化的 `conditions[]`，每项至少包含稳定
`code`、严重等级、可读说明和结构化 `actions[]`。动态端点放入 `endpoints[]`，不能
塞进状态字符串。所有观测结果必须带 `observedAt`，让调用者知道信息是否过期。

CLI 从操作系统服务状态和一次性的 Driver `inspect` 得出结果。系统中不存在控制
socket，也不存在常驻的聚合状态进程。

## 5. Connection 模型

v1 配置由很小的公共 Envelope 和 Driver 自己拥有的 `config` 组成。最终序列化
Schema 在实现阶段确定，但这个边界是强制性的：

```json
{
  "apiVersion": "tunlite.dev/v1",
  "kind": "Connection",
  "metadata": {
    "name": "staging-db",
    "labels": { "env": "staging" }
  },
  "spec": {
    "driver": "io.tunlite.ssh",
    "lifecycle": "persistent",
    "configVersion": 1,
    "config": {
      "host": "deploy@app01",
      "identityRef": "work-staging",
      "forwards": [
        { "type": "local", "bind": "127.0.0.1:5432", "target": "localhost:5432" }
      ]
    }
  }
}
```

核心可以校验公共 Envelope。只有被选中的 Driver 可以校验和解释
`spec.config`。

### 5.1 项目声明

项目级 Connection 声明可以提交到 Git，用于团队共享和审查。它只能包含可迁移的
逻辑配置和 Secret 引用，不能包含私钥、令牌、交互审批结果或机器专属绝对路径。

端口、Identity File、Kubernetes Context 等字段并非天然可迁移。Schema 必须标明
字段是否允许项目共享；机器差异通过具名引用和本机覆盖解决，不能假装所有机器环境
一致。

导入或检出项目声明只改变本地声明集合，默认不得：

- 安装缺失 Driver。
- 执行认证。
- 创建远程资源。
- 启用或启动 Connection。
- 接受公开监听或 Publish 风险。

### 5.2 本机状态

以下内容保存在权限受限的本机状态目录中，不进入 Git：

- Identity、Context、端口等本机覆盖和引用解析。
- 用户或 Agent 审批记录。
- 已安装 Driver、实际可执行路径和本机兼容信息。
- 生成的 OS 服务索引、最近观测状态和诊断缓存。
- 只对当前机器有意义的迁移与回滚记录。

本机状态不是项目声明的替代品。删除它不会改变 Git 中的期望配置，但会使需要本机
解析或审批的 Connection 进入 `syncState: blocked` 或 `missing`。

### 5.3 锁定与合并规则

可复现的项目级 Driver 版本和 checksum lock 可以提交到 Git；它表达团队希望解析的
精确供应链输入。本机已安装路径、信任审批和安装结果必须单独保存，不能提交。

配置求值顺序固定为：项目声明、本机覆盖、显式命令参数。最终求值结果必须可以通过
`plan` 检查，但包含 Secret 的解析值不得写回项目文件、日志或导出结果。

同名项目声明与本机私有 Connection 冲突时必须报错，不得静默覆盖。`import`、
`sync` 和批量 `enable` 都必须逐项返回结果，允许部分失败但不能隐瞒失败项。

未来可以允许 Connection 依赖其他具名 Connection，但在出现真实的组合 Driver
场景，并证明启动、停止和失败语义安全之前，不得公开依赖图能力。

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
| `provision` | 经计划和审批后创建 Driver 外部资源 |
| `cleanup` | 经计划和审批后删除 Driver 外部资源 |
| `migrate` | 迁移 Driver 自己的配置 |

`describe`、`schema`、`validate`、`check`、`plan`、`inspect` 和 `diagnose` 不得修改
本地或远程状态。`plan` 对相同标准化输入和 Driver 版本必须产生相同结果。

`authenticate`、`provision` 和 `cleanup` 属于副作用方法，必须遵守：

- 身份登录与远程资源创建是两个独立操作，不能在检查或规划时顺带执行。
- 先返回结构化副作用计划、风险等级、资源范围和 `requiresApproval`，再使用计划摘要
  进入 `apply`；计划发生变化后原审批失效。
- `provision` 创建的每项远程资源必须写入 `OwnershipRecord`，至少记录 Driver、
  Connection、Provider 资源 ID、创建时间、来源和所有权策略。
- `tunlite rm` 默认只删除本地声明与本地服务，不调用远程 `cleanup`。
- `cleanup` 只能处理 OwnershipRecord 明确归属于当前 Connection 的资源，并且必须
  单独展示删除计划、再次授权和逐项结果。
- 非交互调用缺少有效审批时必须停止并返回结构化动作，不能降级为默认同意。

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

`ExecutionPlan` 是最重要的稳定边界。它描述操作系统应该执行什么，不描述如何
编写 systemd unit 或 plist。

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
- 安装插件、公开监听、使用 Publish、创建远程资源和删除资源必须返回统一的结构化
  风险与审批对象，不能只打印确认文字。
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
- Driver 自己拥有配置版本和 `migrate` 路径。
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

1. **内置 SSH：** 产品 v1 的全部正式 Driver 范围，也是 0.x 的迁移路径。
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

公共命令面向 Connection，而不是绑定 SSH：

```text
tunlite add / set / rm / rename
tunlite list
tunlite enable / disable / restart
tunlite status / logs / doctor
tunlite run
tunlite sync
tunlite import / export
tunlite schema / capabilities
tunlite plugin install / list / update / remove / doctor / test
```

官方 Driver 可以使用命名空间参数或配置文档表达专属配置。核心不能把每个 Provider
的参数全部堆入同一个全局参数解析器。

公共机器接口必须提供：

- 所有自动化相关命令支持 `--json`，并在输出中带机器契约版本。
- 官方发布版本同时发布版本化 JSON Schema；兼容性由 Schema 和契约测试定义，
  不能只依赖示例文档。
- `--non-interactive` 保证整个调用链绝不提示输入。需要认证、选择或审批时返回结构化
  状态，不得等待 stdin。
- 所有改变状态的命令支持 `--dry-run`。执行计划带稳定摘要；高风险操作只有携带对应
  计划摘要的明确批准才能执行，计划变化后批准自动失效。
- Apply 必须可重试和幂等；同一个操作请求重复提交不能重复创建远程资源。
- 稳定、公开的退出码，并明确成功、部分成功、需要审批、需要认证和失败的区别。
- 结构化错误包含 `code`、`driver`、`connection`、`message`、`riskLevel`、
  `requiresApproval` 和 `actions[]`。
- `actions[]` 使用版本化动作类型和参数，不能要求 Agent 解析或直接执行错误信息中的
  Shell 命令字符串。
- 批量操作返回逐 Connection 的 `results[]` 和整体 `outcome`；部分失败不能只通过
  一条顶层错误丢失已经成功或失败的对象。
- `capabilities` 可以机器查询当前核心、平台和 Driver 支持的动作、执行模式、状态
  精度和审批要求。
- Agent 不需要解析人类说明文本，也不能把人类文本中的建议当作授权。
- stdout、stderr、生成文件、诊断和导出全部执行 Secret 脱敏。

风险等级至少包含 `low`、`medium`、`high` 和 `critical`。安装本地可执行插件、公开
监听地址、Publish、远程资源创建和远程资源删除不得标记为 `low`。

Agent Skill 不是独立事实来源。它必须从 CLI Schema 和命令元数据生成，或由测试证明
与其一致。CI 必须验证 Skill 中的命令、字段、退出码和示例，禁止长期维护一份无法
校验的手写契约副本。

如果完全兼容 0.x 输出意味着继续暴露 daemon 概念，则无需保持这种兼容。必须提供
配置迁移和清楚的升级说明。

## 10. 明确改变或删除的能力

v1 不能为了保留 0.x 的每个字段，而把 daemon 换个名字重新实现。

删除：

- `tunlite daemon ...`。
- 中心 IPC socket 和 daemon PID 文件。
- daemon ping 和 daemon 专属退出码语义。
- 全局 daemon 生命周期告警。
- 手工修改配置后的内存自动 reconcile。
- 用一个总服务启动所有 Connection 的 `install service` 模式。

改变：

- `enable` 创建或更新当前 Connection 的原生服务，并立即启动。
- `disable` 停止并禁用当前 Connection 的原生服务。
- `sync` 显式处理手工编辑和状态漂移。
- `status` 返回公共 OS 状态和 Driver 支持的细节，不伪造所有 Driver 都能
  证明端到端连接。
- `monitor` 可以改成前台轮询视图或 `status --watch`，不能以它为理由引入
  后台控制进程。
- 日志来自原生服务日志或确定性的每 Connection 日志文件，不经过 IPC Fan-out。
- 告警如果保留，应采用无状态 Hook、插件集成或外部监控，不能要求核心常驻事件引擎。

保留或增强：

- 声明式名称、标签和批量操作。
- SSH `-L`、`-R`、`-D`、Jump Host、Identity File 和安全 argv 生成。
- 导入、导出和配置校验。
- 认证前置检查和可操作诊断。
- Agent Skill、JSON 输出和稳定退出行为。
- 没有可用服务管理器时使用前台 `run`。

## 11. 规模、安装和更新

默认持久架构必须能够合理管理几十乃至上百个 Connection。因此，在操作系统能够直接
执行 Provider 命令时，不能为每个 Connection 创建一个 Node 或 Go Supervisor
runtime。

用户把多个 Forward 声明为同一个 SSH Connection 时，它们可以共享一个 SSH 进程。
Tunlite 不能擅自合并名称不同的 Connection，因为这会改变隔离和生命周期语义。

Go CLI 以签名并带 checksum 的多平台二进制分发。随着维护者到位，包管理器应覆盖
Homebrew、Scoop 或 winget，以及 Linux 常见渠道。npm 可以保留为下载 Shim，但
Node 不是 v1 runtime 依赖。

更新必须原子完成。只有生成计划或锁定插件确实变化，并且用户允许连接中断时，才能
重启 Provider 进程。删除 Tunlite launcher 不得停止运行中的 Provider。生成物必须
记录足够信息，使重新安装的 Tunlite 能重新检查并 reconcile 它们。

## 12. 平台策略

- Linux/systemd 是持久服务的参考实现。
- macOS/launchd 是一等支持平台，但遵循平台本身的语义。
- Windows Task Scheduler 在重启、日志和状态语义达到明确测试标准前保持
  尽力支持。
- 容器、CI、没有用户级服务管理器的精简系统和不支持的平台，使用前台
  `tunlite run` 或直接 Driver 命令。

公共契约只能承诺各平台能够诚实提供的交集。平台特有信息可以放在可选命名空间对象中，
不能为了表面一致而伪造状态。

## 13. 仓库与开发策略

重写保留现有 Git 历史和项目身份。

- 当前分支继续维护 0.x，v1 在 `rewrite/v1-go` 开发。
- 使用独立 Git Worktree 做物理隔离，不复制仓库，也不永久保留 `next/` 子树。
- v1 切换前，为最后一个 Node 版本打 Tag；必要时保留 `legacy/node` 维护分支。
- Go 版本成为默认分支时，代码位于仓库根目录。
- 不长期发布两套核心实现或两套 runtime backend。
- 把 0.x 的重要行为转换成与语言无关的黑盒契约测试：输入命令、初始配置、退出码、
  stdout JSON、stderr 和最终状态。
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
- 用 0.x 黑盒契约、真实 OpenSSH 端到端测试和测试用假 Driver 验证行为与扩展边界。

### 第二周：8 月 24 日—8 月 30 日

完成公开预览版的发布准备：

- 完成 systemd 与 launchd 的每 Connection 原生服务。
- 完成 `enable`、`disable`、`restart`、`status`、`logs` 和 `sync`。
- 完成四维状态、批量结果、0.x 配置迁移、失败回滚和 Agent Skill 一致性。
- 完成签名二进制、checksum、升级文档、支持矩阵和预览版发布验证。

Windows Task Scheduler 保持实验支持，不阻塞 V1。外部插件、Kubernetes、云 Driver、
`managed` runtime、GUI 和托管服务不进入本次发布范围。

### 公开预览发布：不晚于 8 月 31 日

只有 SSH 核心链路、systemd、launchd、迁移和回滚通过自动测试与发布检查，才发布
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

如果第一个问题的答案是否定的，这项功能应当推迟或属于另一个项目。如果第二至第七个
问题有任何一个是否定的，必须修改设计后才能实现。

## 16. 一句话方向

> 当前，Tunlite 要成为面向开发者和 AI Agent 的可靠 SSH 隧道管理器：声明一次，
> 由操作系统原生服务持续运行，Tunlite 自身不成为后台控制平面。

长期扩展通过稳定的 Connection、Driver 和 ExecutionPlan 边界增量加入；扩展性从
v1 开始预留，但外部协议只在真实需求和多类 Provider 验证后稳定。
