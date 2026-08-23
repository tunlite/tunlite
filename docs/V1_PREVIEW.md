# Tunlite v1 预览版指南

本文说明 `v1.0.0-preview.N` 的安装、支持范围和发布验收。
产品与架构决策以 [PROJECT_DIRECTION.md](PROJECT_DIRECTION.md) 和
[ARCHITECTURE.md](ARCHITECTURE.md) 两份并列主文档为准。

需要从空白机器开始安装，并验证用户自己的 SSH、真实隧道流量、原生后台运行、登录
恢复和接管/交还时，请按照[用户端到端验收手册](INSTALL_AND_ACCEPTANCE.md)执行。
Go 测试、协议、交叉编译和打包由项目维护者完成，不转嫁给用户。

## 支持矩阵

| 平台 | 架构 | 前台与配置 CLI | 持久 Connection | 当前验证等级 |
|---|---|---:|---:|---|
| macOS 12+ | x64 | 支持 | launchd，支持 | 自动测试、plist 原生校验，以及 Intel 真机生命周期、日志、崩溃恢复和接管/交还测试 |
| macOS 12+ | arm64 | 支持 | launchd，支持 | 自动测试与交叉编译；发布前补 Apple Silicon 真机验收 |
| Linux | x64、arm64 | 支持 | systemd 用户服务，支持 | 自动测试与交叉编译；发布前补真实发行版验收 |

系统 `ssh` 必须位于 PATH，或通过 `TUNLITE_SSH` 指定绝对路径。Linux 持久模式需要
可用的 systemd 用户会话；容器、精简发行版和没有用户会话总线的环境应使用
`tunlite run`，或由已有的进程管理器托管它。

首个预览版不发布 Windows 产物。Task Scheduler 和真实 Windows 验证完成后，Windows
按独立验收计划进入支持矩阵。

V1 继续提供彩色状态表格、`status <name>` 详情、`doctor` 诊断和前台实时
`tunlite monitor`。Monitor 使用类似 `top` 的全屏布局，标题显示当前 Tunlite 版本，
总览显示运行、变更中、异常和停止数量；`Enter`/右键进入详情，`Esc`/左键返回，最外层
`Esc` 退出。详情页同时显示选中 Connection 最近 20 行原生日志。启动、停止和重启期间
立即显示“启动中”“停止中”“重启中”；停止和重启必须按 `y` 二次确认，其他任意键取消，
启动直接执行。停止长期后台连接时，确认文字必须明确同时关闭登录自启；重启确认必须说明
连接会短暂中断且登录自启设置不变。

人类状态界面固定拆成“状态、运行位置、登录自启”三个概念，不要求普通用户理解内部
`foreground/background/persistent` 模式。Monitor 宽屏列为“名称、状态、运行位置、
登录自启、PID、已运行、重启次数、SSH 目标、转发”；窄屏先隐藏 SSH 目标和转发，
`PID`、`UP`/已运行和 `RESTARTS`/重启次数始终优先保留。

人类界面首批支持中文和英文：`--lang zh-CN|en` 优先，其次读取 `TUNLITE_LANG`，再跟随
系统语言，无法识别时回退英文。CLI 命令、配置字段、错误码和全部 JSON 字段及枚举值仍
保持英文；`--json` 不因界面语言改变。OpenSSH、launchd、systemd 的原始日志不翻译。

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

## 安装与校验

macOS 或 Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.sh | sh
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

正式 Release 还应提供 GitHub Artifact Attestation。启用发布工作流后可验证：

```bash
gh attestation verify tunlite_1.0.0-preview.1_macos.tar.gz \
  --repo yuanyuanzijin/tunlite
```

Checksum 只证明下载内容和发布清单一致；Attestation 才把产物绑定到仓库身份、提交和
GitHub Actions 构建。发布工作流尚未得到外部写入授权时，不得虚构已存在的证明。

## 预览版发布门禁

发布候选必须全部通过：

```bash
go test ./...
go test -race ./...
go vet ./...
TUNLITE_REAL_SSH=1 go test ./internal/cli -run TestRealOpenSSHAllForwardTypes -count=1 -v
TUNLITE_REAL_SERVICE=1 go test ./internal/service -run TestRealNativeServiceLifecycle -count=1 -v
scripts/test-release-e2e.sh v1.0.0-preview.1
scripts/test-linux-docker-e2e.sh v1.0.0-preview.1
```

Release E2E 门禁只在 macOS 上运行：它重新构建并独立校验完整 Release 目录，再使用归档中的
原生二进制驱动本机隔离 sshd 和真实 launchd，验证 `-L`、`-R`、`-D` 流量、
background → persistent 不重启，以及 SSH 进程崩溃后的自动恢复。测试使用固定专用标签，
执行前必须确认该标签未被占用，结束后移除 plist 和进程。

Linux Docker E2E 使用 Ubuntu 24.04、PID 1 systemd、lingering systemd user manager 和真实
OpenSSH。它验证 `-L`、`-R`、`-D` 流量，background → persistent 不换 PID，SSH 被
`SIGKILL` 后由 systemd 恢复，移走 CLI 后连接不断流、在 CLI 仍缺失时重启容器仍自动
恢复，恢复或替换 CLI 不重启连接，`unmanage → manage` 不断流，以及原生 unit 漂移的
检测、dry-run 和修复。失败时保留测试镜像用于诊断；成功后自动清理容器和镜像。
该门禁证明 Linux 产物与 systemd Adapter 的真实集成，但不能替代至少一台非容器 Linux
主机的登录、发行版差异和长期运行验收。

发布产物统一使用 Go 1.26.x 构建；`go.mod` 中的 1.25 是开发和源码兼容基线。发布只使用
CI 固定的工具链，不能用本机任意 Go 版本生成后手工上传。

还必须在非容器 Linux 真机完成 systemd 用户服务的
`add → start → stop → enable → status → restart → disable → remove`
验收，逐个平台解压产物并确认嵌入版本正确，核对 `SHA256SUMS`。没有完成的项目必须在
Release Notes 和支持矩阵中明确写出，不能用“预计支持”替代事实。
