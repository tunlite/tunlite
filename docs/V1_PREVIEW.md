# Tunlite v1 预览版指南

本文说明 `v1.0.0-preview.N` 的安装、支持范围和发布验收。
`v1.0.0-preview.1` 候选准备日期为 2026-11-06；该日期不代表已经公开发布。
公开发布事实以仓库的 Tag、GitHub Release、校验清单和 Artifact Attestation 为准。
产品与架构决策以 [PROJECT_DIRECTION.md](PROJECT_DIRECTION.md) 和
[ARCHITECTURE.md](ARCHITECTURE.md) 两份并列主文档为准。

需要从空白机器开始安装，并验证用户自己的 SSH、真实隧道流量、原生后台运行、登录
恢复和接管/交还时，请按照[用户端到端验收手册](INSTALL_AND_ACCEPTANCE.md)执行。
Go 测试、协议、交叉编译和打包由项目维护者完成，不转嫁给用户。

## 支持矩阵

| 平台 | 架构 | 前台与配置 CLI | 持久 Connection / Scope | 当前验证等级 |
|---|---|---:|---:|---|
| macOS 12+ | x64 | 支持 | launchd user/system 支持 | Intel 真机已通过 Universal 2 构建校验、LaunchAgent Release E2E，以及隔离 LaunchDaemon 双向迁移、真实流量、配置更新、无 sudo restart、disable/remove 与精确清理 |
| macOS 12+ | arm64 | 支持 | launchd user/system 支持 | 自动测试、Universal 2 构建与交叉编译通过；Apple Silicon user/system 真机仍未验证 |
| Linux | x64、arm64 | 支持 | systemd user 支持；system 不支持 | 自动测试、交叉编译及 Ubuntu 24.04 PID 1 systemd 容器 E2E；稳定版前补非容器发行版真机验收 |

系统 `ssh` 必须位于 PATH，或通过 `TUNLITE_SSH` 指定绝对路径。Linux 持久模式需要
可用的 systemd 用户会话；容器、精简发行版和没有用户会话总线的环境应使用
`tunlite run`，或由已有的进程管理器托管它。

首个预览版不发布 Windows 产物。Task Scheduler 和真实 Windows 验证完成后，Windows
按独立验收计划进入支持矩阵。

每个 Connection 声明只有一份，Scope 不进入导出配置。`enable` 对新服务默认 user，
已有 Record 未传 `--scope` 时保留原范围；显式使用 `--scope user|system` 才选择或迁移。
macOS system Scope 对应 LaunchDaemon，需要管理员授权；Linux system Scope 稳定返回
`scope_unsupported`。人工交互终端会在整批只读检查成功后、首次写入前执行一次
`sudo -v`；密码提示由 `sudo` 自己显示，Tunlite 不读取或保存密码。没有交互终端时快速
返回 `privilege_required`。`--non-interactive` 不提示也不调用交互授权，只使用外部预置、
可供平台层 `sudo -n -- ...` 使用的授权；`--dry-run` 不授权、不写入。

V1 继续提供彩色状态表格、`status <name>` 详情、`doctor` 诊断和前台实时
`tunlite monitor`。Monitor 使用类似 `top` 的全屏布局，标题显示当前 Tunlite 版本，
总览显示运行、变更中、异常和停止数量；`Enter`/右键进入详情，`Esc`/左键返回，最外层
`Esc` 退出。详情页同时显示选中 Connection 最近 20 行原生日志。启动、停止和重启期间
立即显示“启动中”“停止中”“重启中”；停止和重启必须按 `y` 二次确认，其他任意键取消，
启动直接执行。停止长期后台连接时，确认文字必须明确同时关闭自动启动；重启确认必须说明
连接会短暂中断且自动启动设置不变。

人类状态界面固定拆成“状态、运行位置、自动启动、范围”四个概念，不要求普通用户理解内部
`foreground/background/persistent` 模式。Monitor 宽屏列为“名称、状态、运行位置、
自动启动、范围、PID、已运行、重启次数、SSH 目标、转发”；窄屏按优先级隐藏次要列，
`PID`、`UP`/已运行和 `RESTARTS`/重启次数始终优先保留。

帮助、生命周期进度、`status` 和 `monitor` 首批支持中文和英文：`--lang zh-CN|en`
优先，其次读取 `TUNLITE_LANG`，再跟随系统语言，无法识别时回退英文。当前 Preview 的
部分次要命令结果和底层错误仍为英文，不承诺整套人工输出已经完整本地化。CLI 命令、配置
字段、错误码和全部 JSON 字段及枚举值保持英文；`--json` 不因界面语言改变。OpenSSH、
launchd、systemd 的原始日志不翻译。

所有可能等待 SSH、服务管理器或批量状态采集的人工终端命令，都会在开始等待前打印当前
动作，不允许无输出地长时间阻塞；`--json` 仍只输出稳定机器契约，不混入人类进度文本。
颜色只在交互终端中启用，`NO_COLOR`、管道和 `--json` 输出均不包含 ANSI 控制符。
Webhook 告警本预览版暂缓，不在能力列表中。

## 配置边界

V1 只使用一份规范配置：`~/.config/tunlite/connections.json`；设置 `TUNLITE_HOME` 时为
`$TUNLITE_HOME/config/connections.json`。不提供全局 `--config`、项目自动发现、本机
override 或 GitOps 合并规则。

需要交换声明时使用 `export`、`validate` 和 `import`。导出文件可能包含绝对私钥路径、
机器专用端口或其他本机信息，提交 Git 前必须人工检查。导入不会安装插件、执行认证或
启动 Connection。

Scope 是本机 Service Record、Artifact 和 State 的属性，不属于 ConnectionList。
因此 export/import 不会把一台机器的 user/system 归属带到另一台机器；目标机器首次
创建服务时仍默认 user。

`set` 只修改明确给出的 Driver 字段。SSH 的私钥、跳板列表和额外 Option 分别用
`--clear identity`、`--clear jump`、`--clear options` 删除，`--clear` 可在同一条命令中
按不同字段重复，也接受 `--clear=<field>`。未知字段、重复清除或同字段设置+
清除都是参数错误，不做静默兼容，也不提供单字段别名。

SSH Target 可以是明确的 `host` 或 `user@host`，但 Preview 用户名只接受 ASCII 字母、数字、
`.`、`_`、`-`。UPN/多个 `@`、`+` 和 `DOMAIN\user` 形式均不支持，CLI 也没有独立 `-l`
参数。由于运行时固定 `ssh -F none`，不能依靠 SSH Config 的 `User` 隐式补偿；需要这些账号
形式的用户应把它视为 Preview 的已知兼容限制，而不是尝试转义绕过 Parser。

## 安装与校验

macOS 推荐 Homebrew 从固定版本源码构建；
[`tunlite/homebrew-tap`](https://github.com/tunlite/homebrew-tap) 公开后可执行：

```bash
brew install tunlite/tap/tunlite
type -a tunlite
tunlite version --json
```

Formula 固定 `v1.0.0-preview.1` 的 HTTPS 源码归档与 SHA-256，Go 仅为构建依赖；
运行时仍只需要系统 `ssh`。Homebrew 安装目录与 PATH 请遵循 `brew shellenv` 的提示。
用 `brew update` 后的 `brew upgrade tunlite/tap/tunlite` 升级，用
`brew uninstall tunlite/tap/tunlite` 移除 CLI。没有中心 `brew services` 服务。

已安装 Go 1.25+ 时也可以直接编译同一个固定版本：

```bash
git clone --depth 1 --branch v1.0.0-preview.1 https://github.com/tunlite/tunlite.git tunlite-source
cd tunlite-source
go install -trimpath -ldflags='-s -w -X github.com/tunlite/tunlite/internal/cli.Version=1.0.0-preview.1' ./cmd/tunlite
go env GOBIN GOPATH
```

当前远程 `go install ...@v1.0.0-preview.1` 会在历史非 Go Tag `v0.10.3` 的 `go.mod`
校验查询处失败，因此使用上述固定 Tag 本地模块安装，保留正常 Go 依赖校验。
将 `GOBIN` 或 `GOPATH` 第一项的 `bin` 目录（通常为 `~/go/bin`）加入 PATH。
版本注入是必要步骤，否则发布源码仍报告 `-dev`。升级时在新目录检出新 Tag 并同步修改注入版本，
卸载时只删除实际安装目录里的 `tunlite`。Homebrew 与自行编译通常避免浏览器下载的
quarantine，但不提供 Apple 开发者签名或公证，也不保证符合企业设备策略。

用 `type -a tunlite` 检查 PATH 中的所有副本并选择一个安装渠道；靠前的旧副本可能遮住
新安装。不要盲目覆盖其他渠道的文件；这些渠道不迁移旧开发版服务记录。卸载 CLI
不会停止已经由 launchd 或 systemd 托管的 SSH Connection。

预编译渠道继续保留。详情见[安装与验收手册](INSTALL_AND_ACCEPTANCE.md)。
对应 GitHub Release 和校验清单可用后，macOS 或 Linux 用户可执行：

```bash
curl -fsSL https://raw.githubusercontent.com/tunlite/tunlite/main/bootstrap.sh | sh
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

手动拿到单文件二进制时，直接复制到 PATH：

```bash
chmod +x ./tunlite
mkdir -p "$HOME/.local/bin"
install -m 0755 ./tunlite "$HOME/.local/bin/tunlite"
"$HOME/.local/bin/tunlite" version
```

核心 CLI 不提供 `install`、`update` 或 `uninstall`，也不创建 `tun` 短命令。日常升级由
原安装渠道负责；手工安装用户下载、校验并替换原二进制：

```bash
install -m 0755 ./tunlite-new "$HOME/.local/bin/tunlite"
```

替换 CLI 文件不读取或改写 Connection，也不重启直接由原生服务运行的 SSH。下载归档
checksum 不一致、归档缺少二进制或二进制报告版本不一致时必须停止。

V1 Preview 暂不提供 Shell 自动补全，也不读取或修改 `.bashrc`、`.zshrc`、fish 配置
等 Shell 启动文件；补全只有在跨 Shell、跨平台和包管理器
的长期职责边界明确后才重新立项。Agent Skill 由公开仓库中的
[`use-tunlite` 包](../skill/use-tunlite/)独立提供；用户把仓库目录或固定版本 Tag 地址
交给 Agent，由 Agent 按自己的扩展机制安装、更新和卸载。Tunlite CLI 不读取 Agent
目录，也不维护 Skill 安装状态。

也可以手动下载压缩包和 `SHA256SUMS`。任何 checksum 不一致都必须停止安装，不能用
“重新下载后跳过校验”的方式继续。

每个 Release 必须提供 GitHub Artifact Attestation。Tag 工作流完成全部门禁、生成证明
并发布对应产物后，可验证：

```bash
gh attestation verify tunlite_1.0.0-preview.1_macos.tar.gz \
  --repo tunlite/tunlite
```

Checksum 只证明下载内容和发布清单一致；Attestation 才把产物绑定到仓库身份、提交和
GitHub Actions 构建。发布工作流已获单独授权并完成审查，但每次推送 Tag 仍须获得明确
发布授权；工作流存在不代表对应 Release 或证明已经存在。

## 预览版发布门禁

发布候选必须全部通过：

```bash
go test ./...
go test -race ./...
go vet ./...
go mod verify
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.8.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
TUNLITE_REAL_SSH=1 go test ./internal/cli -run TestRealOpenSSHAllForwardTypes -count=1 -v
TUNLITE_REAL_SERVICE=1 go test ./internal/service -run TestRealNativeServiceLifecycle -count=1 -v
scripts/test-release-e2e.sh v1.0.0-preview.1
scripts/test-linux-docker-e2e.sh v1.0.0-preview.1
```

macOS system Scope 必须同时通过两类互补门禁：自动故障注入覆盖权限缺失、目标冲突、
每个迁移步骤失败与完整回滚；专用真机覆盖真实 sudo、`/Library/LaunchDaemons`、system
launchd domain 和成功生命周期。自动测试不得调用真实 sudo，真机测试不得触碰开发者
已有服务。2026-08-31，Intel 真机已经通过最终候选 Universal 2 产物的
USER→SYSTEM→USER→SYSTEM 成功路径，核对 root/user plist 的 owner、mode 和 domain，
验证真实 `-L` 流量、system `set`、清除 sudo timestamp 后的无密码 `restart`、
`disable`/`remove` 以及测试身份和进程的精确清理。失败迁移与回滚结论仍来自自动故障
注入，不能把这次成功路径表述成所有真实故障都已在硬件上复现。整段验收在同一 TTY
执行，共由系统读取两次密码；中间双向迁移与 `set` 复用了有效 sudo timestamp，脚本在
无密码 restart 门禁前主动清除 timestamp，最终 disable 因而再次授权。

Tag 触发的 Release workflow 会在自身运行中执行跨平台 test/vet、race、最低 Go 版本、
module/staticcheck/govulncheck、真实 OpenSSH，以及归档二进制的 macOS launchd E2E；只有
这些 Job 全部成功后才 Attest 并允许 `publish`。Workflow 把已经生成的 `dist` 显式传给
`scripts/test-release-e2e.sh`，E2E 解包并运行的正是随后 Attest、上传和发布的 macOS
归档，不允许测试后再重建另一份产物。Linux Docker E2E 在推送 Preview Tag 前单独完成并
记录，因为 GitHub macOS 打包 Runner 不提供 Docker，而 Ubuntu Runner 无法生成需要
Apple `lipo` 的完整 Release 目录。

`v1.0.0-preview.1` 的门禁是本节命令列表以及已记录的 Intel Mac 验收。Apple Silicon
真机和非容器 Linux 真机不阻塞 Preview.1，但必须在 Release Notes 和支持矩阵中继续明确
标为尚未验证；两项都是稳定 `v1.0.0` 前的硬门禁。

Release E2E 门禁只在 macOS 上运行：默认本地调用会构建并校验临时 Release 目录；传入第二个
参数时则严格校验并使用已有目录。例如 workflow 使用
`scripts/test-release-e2e.sh v1.0.0-preview.1 dist` 测试最终发布产物。归档中的原生二进制
驱动本机隔离 sshd 和真实 launchd，验证 `-L`、`-R`、`-D` 流量、
background → persistent 不重启，以及 SSH 进程崩溃后的自动恢复。测试使用固定专用标签，
执行前必须确认该标签未被占用，结束后移除 plist 和进程。

真实 LaunchDaemon 门禁必须由人在专用 macOS 真机的交互终端显式开启，Intel 与 Apple
Silicon 共用同一个脚本：

```bash
TUNLITE_REAL_SYSTEM_E2E=1 scripts/test-macos-system-e2e.sh v1.0.0-preview.1
# 复用已经校验过、准备发布的产物：
TUNLITE_REAL_SYSTEM_E2E=1 scripts/test-macos-system-e2e.sh v1.0.0-preview.1 dist
```

脚本只使用 `it-system-e2e-*` 隔离身份和临时 `TUNLITE_HOME`，明确排除端口 19999；它
覆盖成功生命周期：USER→SYSTEM→USER→SYSTEM、root/user-owned plist、对应 launchd
domain、真实 `-L` 流量、system `set`、无密码 `restart`、`disable` 和 `remove`。权限缺失、
目标冲突和迁移步骤失败后的回滚仍由自动故障注入测试覆盖，不能把本脚本单独视为每种
故障都经过真机复现。

为让 LaunchAgent 与 LaunchDaemon 都直接执行真实 `/usr/bin/ssh`，脚本会把本次随机本机
sshd 的唯一 host key 临时追加到当前用户的 `~/.ssh/known_hosts`。它记录文件 inode 和
本次条目的精确字节位置；清理永不截断、删除或替换该文件，只在文件身份和本次
完整条目仍匹配时把该条目行首原地改为 `#`，因此随后追加的内容不受影响。这个安全取舍
会留下一个无效注释行；若检测到路径替换或已登记区间变化，脚本不再自动修改，而是保留
诊断并打印仅删除该随机端点的 `ssh-keygen -R` 命令。若新机器原本没有 `.ssh` 或
`known_hosts`，脚本创建的目录或只含无效注释的文件也会保留，避免以存在竞态的方式自动
删除。

无 sudo 调用的硬证明来源是 Launchd 单测
`TestLaunchdSystemRestartUsesVerifiedNonPrivilegedKeepAliveReplacement`：Runner 遇到
`MacOSSudoProgram` 会立即失败，并断言 system restart 只经过受校验的
`launchctl print`、`ps` 和 `kill` 路径。真机脚本提供
的是补充的无凭证**行为门禁**：先执行 `sudo -k`，确认 `sudo -n` 无可用 timestamp，再以
关闭 stdin、`--non-interactive` 和硬超时运行命令，并在完成后再次确认 timestamp 仍不可用、
stderr 没有密码提示。因为产品使用绝对 `/usr/bin/sudo`，普通 PATH wrapper 不能可靠拦截
调用；该行为门禁也不能排除只针对 install/rm 等命令配置的 command-specific NOPASSWD，
因此不得把它表述成完整系统调用审计。失败清理绝不从 trap 请求密码；如果 root artifact
无法无交互移除，脚本会保留诊断目录并打印精确人工恢复命令。

Linux Docker E2E 使用 Ubuntu 24.04、PID 1 systemd、lingering systemd user manager 和真实
OpenSSH。它验证 `-L`、`-R`、`-D` 流量，background → persistent 不换 PID，SSH 被
`SIGKILL` 后由 systemd 恢复，移走 CLI 后连接不断流、在 CLI 仍缺失时重启容器仍自动
恢复，恢复或替换 CLI 不重启连接，`unmanage → manage` 不断流，以及原生 unit 漂移的
检测、dry-run 和修复。失败时保留测试镜像用于诊断；成功后自动清理容器和镜像。
该门禁证明 Linux 产物与 systemd Adapter 的真实集成，但不能替代至少一台非容器 Linux
主机的登录、发行版差异和长期运行验收。

发布产物统一使用 Go 1.26.x 构建；`go.mod` 中的 1.25 是开发和源码兼容基线。发布只使用
CI 固定的工具链，不能用本机任意 Go 版本生成后手工上传。

稳定 `v1.0.0` 前还必须在非容器 Linux 真机完成 systemd 用户服务的
`add → start → stop → enable → status → restart → disable → remove`
验收，并完成 Apple Silicon 的 user/system 真机验收；逐个平台解压产物并确认嵌入版本
正确，核对 `SHA256SUMS`。Intel macOS 的 user↔system 专用管理员真机成功路径已经完成，
自动故障注入继续作为迁移失败/回滚门禁。这些未完成项不阻塞 `v1.0.0-preview.1`，但必须在 Preview Release Notes
和支持矩阵中明确写出，不能用“预计支持”替代事实。
