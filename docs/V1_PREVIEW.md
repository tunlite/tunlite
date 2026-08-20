# Tunlite v1 预览版指南

本文说明 `v1.0.0-preview.N` 的安装、支持范围、0.10.3 迁移、回滚和发布验收。
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

首个预览版不发布 Windows 产物，也不向 Windows 0.x 用户自动升级。Task Scheduler、
迁移和真实 Windows 验证完成后，Windows 按独立验收计划进入支持矩阵。

V1 继续提供彩色状态表格、`status <name>` 详情、`doctor` 诊断和前台实时
`tunlite monitor`；详情页同时显示选中 Connection 最近 20 行原生日志。颜色只在交互终端中启用，`NO_COLOR`、管道和 `--json` 输出均不
包含 ANSI 控制符。Webhook 告警本预览版暂缓，不在能力列表中；0.x 的 Webhook 设置
不会在 v1 中生效，但仍完整保留在未经修改的 0.x 配置和迁移备份中。

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

## 从 0.10.3 迁移

Go v1 不做静默自动升级，也不会自动停止 daemon、卸载 Node runtime 或覆盖旧命令。
升级只读取 `~/.config/tunlite/config.json`，并在独立的
`~/.config/tunlite/connections.json` 中生成新配置；旧配置始终保持不变。

必须按照[从 Tunlite 0.x 升级到 Go v1](UPGRADE_FROM_0_X.md)完整执行。核心命令是：

```bash
./tunlite-v1 import ~/.config/tunlite/config.json --dry-run
./tunlite-v1 import ~/.config/tunlite/config.json
```

配置导入本身不会中断旧隧道。真正切换时逐条停止 0.x Connection，再启动对应 V1
Connection；两套运行时不得同时启动同一条隧道。V1 不建立迁移日志、自动回滚引擎或
旧版本清理命令；回退按升级文档逐条停止 V1 并重新启动 0.10.3。

## 预览版发布门禁

发布候选必须全部通过：

```bash
go test ./...
go test -race ./...
go vet ./...
TUNLITE_REAL_SSH=1 go test ./internal/cli -run TestRealOpenSSHAllForwardTypes -count=1 -v
TUNLITE_REAL_SERVICE=1 go test ./internal/service -run TestRealNativeServiceLifecycle -count=1 -v
scripts/test-release-e2e.sh v1.0.0-preview.1
```

最后一条门禁只在 macOS 上运行：它重新构建并独立校验完整 Release 目录，再使用归档中的
原生二进制驱动本机隔离 sshd 和真实 launchd，验证 `-L`、`-R`、`-D` 流量、
background → persistent 不重启，以及 SSH 进程崩溃后的自动恢复。测试使用固定专用标签，
执行前必须确认该标签未被占用，结束后移除 plist 和进程。

发布产物统一使用 Go 1.26.x 构建；`go.mod` 中的 1.25 是开发和源码兼容基线。发布只使用
CI 固定的工具链，不能用本机任意 Go 版本生成后手工上传。

还必须在 Linux 真机完成 systemd 用户服务的
`add → start → stop → enable → status → restart → disable → remove`
验收，逐个平台解压产物并确认嵌入版本正确，核对 `SHA256SUMS`。没有完成的项目必须在
Release Notes 和支持矩阵中明确写出，不能用“预计支持”替代事实。
