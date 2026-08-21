# Tunlite V1 Preview 可靠性与竞品实测报告

- 日期：2026-08-22（Asia/Shanghai）
- 分支：`rewrite/v1-go`
- 候选版本：`v1.0.0-preview.1`
- 主机：Intel Mac，macOS 26.5.2
- Docker：29.5.2，Colima Ubuntu 24.04 x86_64，2 CPU / 2 GB

## 结论

Tunlite 不是第一个提供具名 SSH 隧道、`-L/-R/-D`、批量操作或自动重连的工具；这些
能力本身不能作为竞争优势。实测后仍成立的差异是：每条 Connection 由 launchd 或
systemd 直接运行系统 OpenSSH，不依赖中心 daemon，也不依赖 Tunlite CLI 长期存在。

这项差异已经由真实 launchd 和 PID 1 systemd 环境证明，不再只是架构推断：移走 CLI
不影响流量；Linux 在 CLI 缺失时重启容器仍恢复连接；恢复或替换 CLI 不更换 SSH PID；
`manage/unmanage` 和漂移修复也不要求重建中心控制进程。

因此 Tunlite 有明确的技术定位：面向需要“像系统服务一样可靠、又可声明和自动化”的
本机 SSH 转发。它不是 Purple/Termix 那种完整 SSH 工作台，也不应追求云 Provider、容器
管理和远程控制面的功能总量。

## Tunlite 真实验证

以下命令全部通过：

```bash
go test ./...
go test -race ./...
go vet ./...
TUNLITE_REAL_SSH=1 go test ./internal/cli \
  -run '^TestRealOpenSSHAllForwardTypes$' -count=1 -v
TUNLITE_REAL_SERVICE=1 go test ./internal/service ./internal/cli \
  -run '^(TestRealNativeServiceLifecycle|TestRealNativeManageAndUnmanageDoNotInterruptProvider)$' \
  -count=1 -v
scripts/test-release-e2e.sh v1.0.0-preview.1
scripts/test-migration-e2e.sh v1.0.0-preview.1
scripts/test-linux-docker-e2e.sh v1.0.0-preview.1
```

覆盖结果：

- Release：Linux x64/ARM64、macOS Universal 2 的归档、checksum、内容和嵌入版本通过。
- OpenSSH：真实隔离 sshd 下本地、远程、SOCKS 转发及真实 HTTP 流量通过。
- launchd：生命周期、日志、崩溃自动恢复、接管与交还不换 Provider PID 通过。
- systemd：Ubuntu 24.04、PID 1 systemd、systemd user manager 下生命周期通过。
- systemd 故障：SSH 被 `SIGKILL` 后出现新 PID，`NRestarts` 增加，流量恢复。
- CLI 独立性：移走 CLI 后 PID 与流量不变；CLI 缺失时重启容器仍自动恢复；恢复和替换
  CLI 不换 SSH PID。
- 所有权与漂移：`unmanage/manage` 不换 PID；外部修改 unit 后能检测 drift，dry-run 和
  `resolve --use tunlite` 均通过。
- 0.10.3：导入不修改旧配置和旧流量；逐条切换到 V1、停止 V1、重新启用旧版通过。

Linux Docker 测试不是非容器 Linux 真机的替代品。仍需在至少一台真实 Linux 主机验证
登录会话、linger、发行版差异和长期运行；Apple Silicon 的人工验收也仍是发布前人工项。

## 竞品样本

仓库公开指标来自 2026-08-22 的 GitHub API：

| 项目 | Stars | 最近 Push | 本次验证级别 |
| --- | ---: | --- | --- |
| `alebeck/boring` | 1,664 | 2026-07-14 | Release、源码、真实三类 SSH 流量、断线、daemon、升级 |
| `davrodpin/mole` | 1,725 | 2024-05-13 | Release/源码/命令与架构核对 |
| `erickochen/purple` | 595 | 2026-08-16 | 官方 v3.26.0 包、帮助、补全、Demo TUI 实跑 |
| `strandnerd/tunn` | 253 | 2025-10-26 | Release/源码/命令与架构核对 |
| `SamNet-dev/tunnelforge` | 109 | 2026-03-15 | 源码与产品范围核对 |
| `Termix-SSH/Termix` | 14,852 | 2026-08-20 | 仓库、文档与产品范围核对，未纳入同类 CLI 实跑 |

还核对了 macOS GUI 项目 `0fuz/ssh-tunnel-manager` 和
`RangerWolf/loris-tunnel-app`。它们证明端口冲突提示、系统通知、SSH 命令导入、实时延迟和
中英文 GUI 都有真实需求，但不是首个 CLI Preview 必须复制的范围。

## boring v0.16.1 实测

官方 Intel Mac 资产的 SHA-256 为
`6149c8ab29bd86ce3c4454716a1c86e569f4e50e0c8e6d558b9a3925c043cce5`，与 GitHub
Release API 声明完全一致，但 tar.gz 本身截断，无法完整解包。这是发布资产本身的问题，
不是下载损坏。本次改从同一 Tag `2facfe29697093ecbf4194a8cacee9df0797d73f` 构建后实测。

真实优点：

- TOML 很短，`open/close/list`、group 和 glob 的学习成本低。
- 状态表紧凑，断线时明确显示 `reconn`。
- Bash、Zsh、Fish 补全成熟，并能根据当前 open/closed 状态补全名称。
- 支持 Unix socket、远端 SOCKS、ProxyJump 和 ssh-agent，SSH 使用面比 Tunlite Preview 宽。
- 真正支持本地、远程和 SOCKS 流量；强制断开 sshd 后会指数退避并自动恢复。

真实不足：

- 一个中心 daemon 持有全部运行态；daemon 没有 launchd/systemd 持久托管。
- CLI 移走时，现有 daemon 和流量继续，说明“移走 CLI 不断流”不是 Tunlite 独有；但
  daemon 一旦被杀，三类流量全部消失，恢复 CLI 只会启动一个空 daemon，原连接均为
  `closed`。
- 使用内置 `BORING_COMMIT_OVERRIDE` 模拟新版本后，CLI 检测到 daemon 不兼容并重启它，
  三条正在运行的隧道全部变成 `closed`。
- 本轮故障恢复按 0.5、1、2、4、8、16、32、60 秒退避。`list` 只显示 `reconn`，下一次
  重试时间和最后错误只存在日志里。
- 不支持 `StrictHostKeyChecking=accept-new`；未知主机必须预置 known_hosts 或关闭校验。
- 没有稳定 JSON、Schema、dry-run、风险 Condition、原生服务漂移与所有权模型。

## Purple v3.26.0 实测

官方 Intel Mac 包 SHA-256 校验通过。`purple --help`、`tunnel --help`、版本、Zsh 补全和
隔离 HOME 下的 Demo TUI 均真实运行；Zsh 补全输出 1,323 行。

真实优点：

- TUI 的 hosts/tunnels/containers/snippets/keys 分区、总数和在线/错误统计、搜索、Tag、
  详情与跳转路径都很成熟，信息层级明显优于普通状态表。
- 支持 SSH config/known_hosts 导入、五种 Shell 补全、OS Keychain、主题、命令片段、MCP
  和多个云 Provider 同步；新用户更容易从已有 SSH 环境进入。
- 发布包、checksum、版本输出和 Demo 体验完整。

边界：`purple tunnel` 的公开命令是 `list/add/remove/start`，其中 `start` 明确为前台运行、
Ctrl+C 停止。它没有证明逐 Connection OS 原生持久服务、CLI 缺失后的跨重启恢复或稳定
自动化协议。它是更大的 SSH 工作台，不是 Tunlite 可靠性模型的直接替代品。

## 缺口分级

### Preview 前必须完成

- 把 Linux PID 1 systemd、真实 SSH、崩溃、CLI 移除/替换、跨重启、漂移和接管固化为
  可重复门禁。**已完成：** `scripts/test-linux-docker-e2e.sh`。
- Release 归档必须独立校验，避免出现 boring 本轮的“摘要正确但包损坏”。**已完成：**
  `scripts/verify-release.sh` 与 Release E2E 会实际解包、检查格式并运行归档二进制。
- 生命周期和长操作必须有可见进度。**已完成基础：** 人类 TTY 命令显示动作提示，Monitor
  显示启动中/停止中/重启中；发布与 E2E 脚本逐阶段输出。JSON 模式保持纯机器输出。
- 真实失败必须可解释。**已完成基础：** `conditions[]`、稳定 code、actions、日志和
  `doctor/check`；仍需通过外部测试者验证文案是否足够易懂。

### Preview 后优先补

- Shell completion 生成命令；只输出脚本，不自动修改 `.zshrc/.bashrc`。
- 从现有 `ssh` 命令或 `~/.ssh/config` 显式导入，先做 dry-run 和冲突说明。
- Monitor 详情增加“最后错误、下一次系统重启时间/策略”的易读解释，降低 `UP`、
  `RESTARTS`、登录自启和当前动作的认知成本。
- 生命周期动作使用有上限、可并发的收敛观察：避免 `start` 刚返回时短暂出现
  `outcome=succeeded` 但端点仍在建立；不能串行等待每条远程连接，也不能伪造健康。
- 根据真实需求评估 Unix socket 和远端 SOCKS，不在 Preview 前扩展 SSH 参数面。

### 不追或独立产品再做

- Purple/Termix 式完整 SSH 终端、容器管理、十多个云 Provider、云同步和远程 Web 控制面。
- 为了功能数量改回中心 daemon。Tunlite 的可靠性差异恰恰来自逐 Connection 原生服务。
- 在没有真实需求和安全模型前加入自动修改 Shell 配置、自动写远端 authorized_keys 或
  自动接受所有 host key。

## 发布判断

从自动化证据看，Preview 的核心技术主张成立，Linux systemd 自动门禁已补齐，可以进入
双 Mac 和非容器 Linux 的人工验收阶段。它还不是“已证明的市场优势”：必须让不了解内部
架构的外部用户完成安装、导入/创建、查看失败原因、重启机器和卸载回退，才能证明这套
可靠性价值能被用户感知。

在 Apple Silicon 与非容器 Linux 人工验收完成前，不应发布稳定 `v1.0.0`；可以发布
`v1.0.0-preview.1`，并在 Release Notes 明确 Windows、Shell completion、SSH 命令导入和
非容器 Linux 长期运行仍不在已验证范围内。
