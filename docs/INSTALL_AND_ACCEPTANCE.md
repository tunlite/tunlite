# Tunlite v1 安装与用户端到端验收

- **适用版本：** `v1.0.0-preview.1`
- **适用平台：** macOS 12+、使用 systemd 用户服务的 Linux
- **最后更新：** 2026-08-21

这份文档只保留用户视角的端到端验收：能否安装、能否连接用户自己的 SSH 服务器、
真实流量能否通过隧道，以及操作系统能否正确托管它。

单元测试、竞态检测、Schema、交叉编译、打包和内部状态组合由项目维护者验证，不要求
用户重复执行。产品与架构决策以 [PROJECT_DIRECTION.md](PROJECT_DIRECTION.md) 和
[ARCHITECTURE.md](ARCHITECTURE.md) 两份并列主文档为准。

## 1. 项目已经验证什么

以下项目已经由当前开发环境完成，用户不需要再验：

| 项目 | 结果 |
|---|---|
| 全量 Go 测试 | 通过 |
| Go 竞态检测与静态检查 | 通过 |
| 隔离 sshd 下的真实 OpenSSH `-L`、`-R`、`-D` | 通过 |
| Intel Mac 上的真实 launchd 生命周期、日志与崩溃恢复 | 通过，连续 3 次 |
| Intel Mac 上的接管与交还零中断（Provider PID 不变） | 通过，连续 3 次 |
| Intel Mac Release 二进制 + 隔离 sshd + launchd + `-L/-R/-D` 真实流量与崩溃恢复 | 通过 |
| macOS x64/arm64、Linux x64/arm64 原生构建 | 通过 |
| 三个发布归档的 SHA-256 | 通过 |
| bootstrap 下载、校验、原子替换和真实 Release 安装 | 通过 |

当前开发机是 Intel Mac，因此以下项目不能用交叉编译冒充真机通过：

- Apple Silicon Mac 上的实际安装、登录恢复和真实 SSH 流量。
- Linux 真机上的 systemd 用户服务、登录恢复和真实 SSH 流量。
- 用户自己的 SSH 密钥、网络、跳板机和目标服务。

这些才是后面需要用户或对应平台测试人员完成的内容。

## 2. 选择正确的原生二进制

先查看机器类型：

```bash
uname -s
uname -m
```

按结果选择一个包：

| 机器 | 应使用的原生二进制 |
|---|---|
| Intel 或 M 系列 Mac，`Darwin` | `tunlite-v1.0.0-preview.1-macos` |
| x64 Linux，`Linux` + `x86_64` | `tunlite-v1.0.0-preview.1-linux-x64` |
| ARM64 Linux，`Linux` + `aarch64`/`arm64` | `tunlite-v1.0.0-preview.1-linux-arm64` |

首个预览版不发布 Windows 版本。没有 systemd 用户会话的 Linux 只能验收前台 `run`，
暂时不能验收原生后台服务。

## 3. 安装

运行本地原生二进制只需要系统 `ssh`，不需要 Node.js、npm 或 Go：

```bash
command -v ssh
```

应输出 `ssh` 的路径。发布后的在线安装脚本另外需要 `curl`、`tar` 和
`sha256sum`/`shasum`。

### 3.1 现在：安装本地生成的原生二进制

当前 GitHub Release 尚未发布。由维护者直接把与机器匹配的单文件原生二进制交给测试
人员，不需要再解压 tar 包。设置它的绝对路径：

```bash
BINARY="/绝对路径/tunlite-v1.0.0-preview.1-对应平台"
```

不要原样使用中文占位路径。确认文件存在后，复制到用户级 PATH：

```bash
test -f "$BINARY"
chmod +x "$BINARY"
mkdir -p "$HOME/.local/bin"
install -m 0755 "$BINARY" "$HOME/.local/bin/tunlite"
```

这只安装 `~/.local/bin/tunlite`，不会创建、启动或修改任何 Connection，也不会创建
短命令。为了避免与旧 Node 版本混淆，整个验收过程使用规范路径：

```bash
TL="$HOME/.local/bin/tunlite"
"$TL" version --json
```

必须看到：

```json
{"outcome":"succeeded","protocolVersion":1,"version":"1.0.0-preview.1"}
```

### 3.2 发布以后：一条命令安装

GitHub Release 真正存在以后，macOS 和 Linux 用户使用：

```bash
curl -fsSL https://raw.githubusercontent.com/yuanyuanzijin/tunlite/master/bootstrap.sh | sh
```

安装脚本会自动识别系统和 CPU、校验 SHA-256，并安装到 `~/.local/bin/tunlite`。
Release 发布以前执行出现 404 是正常的。

### 3.3 Shell 配置不得被修改

V1 Preview 暂不提供 Shell 自动补全。安装验收只需要确认 Tunlite 不创建、
读取或修改 `.bashrc`、`.zshrc`、fish 配置等 Shell 启动文件，不需要执行任何补全安装
命令。安装、升级和卸载由发行渠道负责，不属于核心 CLI。

Agent Skill 不属于 CLI 的安装验收。仓库只提供标准
[`use-tunlite` Skill 包](../skill/use-tunlite/)，由 Agent 根据自身扩展机制安装、检查、
更新和卸载；CLI 和 `doctor` 都不读取 Agent 的私有目录。

## 4. 准备一个真实 SSH 目标

需要一个用户平时确实能够登录的 SSH 服务器。以下示例假设使用默认 22 端口：

```bash
ssh user@example-host true
ssh -o BatchMode=yes user@example-host true
```

把 `user@example-host` 换成真实目标。第二条命令必须在不输入密码的情况下成功，因为
操作系统后台服务无法弹出密码输入框。

如果使用自定义端口，先运行：

```bash
ssh -p 2222 -o BatchMode=yes user@example-host true
```

不要为了通过验收关闭 Host Key 校验。Tunlite 的任何命令都不会生成密钥或修改远端
`authorized_keys`；SSH 凭据由用户使用系统 OpenSSH 工具管理。

设置本次测试变量：

```bash
TL="$HOME/.local/bin/tunlite"
TL_NAME="acceptance-$(date +%s)"
TL_TARGET="user@example-host"
TL_PORT=18080
TL_PORT_2=18081
printf 'name=%s target=%s\n' "$TL_NAME" "$TL_TARGET"
```

自定义 SSH 端口时，把 `TL_TARGET` 写成 `user@example-host:2222`。测试名称带时间戳，
不会覆盖已有 Connection。确认 18080 和 18081 没有被其他程序使用；被占用时换成其他
大于 1024 的端口。

## 5. 核心端到端验收

以下流程在 macOS 和 Linux 上相同。

### 第一步：创建，但不启动

```bash
"$TL" add "$TL_NAME" --to "$TL_TARGET" -D "$TL_PORT"
"$TL" status "$TL_NAME"
```

验收结果：

- Connection 创建成功。
- 状态显示“已停止 / 运行位置：— / 登录自启：否”。
- 没有 SSH 隧道进程被启动。
- macOS/Linux 支持原生服务时，服务定义已经创建，但尚未运行。

### 第二步：验证用户自己的 SSH 认证

```bash
"$TL" check "$TL_NAME" --timeout 12
```

必须看到 `ssh_authentication_succeeded`。失败时先解决 Host Key、密钥、网络或跳板机问题，
不要继续测试后台运行。如果只是尚未安装本机公钥，用户可以在交互终端使用系统工具：

```bash
ssh-copy-id "$TL_TARGET"             # 系统提供 ssh-copy-id 时
# 或按照服务器文档把 ~/.ssh/id_ed25519.pub 加入远端 ~/.ssh/authorized_keys
"$TL" check "$TL_NAME" --timeout 12
```

OpenSSH 可能直接要求远端账户密码；只能由用户在终端输入。自动化和 Agent 不得等待、
收集或代用户提交密码。

### 第三步：前台运行和真实流量

终端 A：

```bash
"$TL" run "$TL_NAME"
```

保持终端 A 不动，在终端 B 验证真实 SOCKS 流量：

```bash
curl --socks5-hostname "127.0.0.1:$TL_PORT" https://example.com/
```

必须能返回网页内容。然后回到终端 A 按 `Ctrl-C`，再次执行：

```bash
"$TL" status "$TL_NAME"
```

状态应回到“已停止”。这一步证明 `run` 的生命周期确实属于当前终端。

### 第四步：临时后台运行

```bash
"$TL" start "$TL_NAME"
"$TL" status "$TL_NAME"
curl --socks5-hostname "127.0.0.1:$TL_PORT" https://example.com/
"$TL" logs "$TL_NAME" --lines 50
```

验收结果：

- 状态显示“正常运行 / 系统后台 / 登录自启：否”。
- 关闭启动它的终端后，隧道仍然存在。
- 真实 SOCKS 流量仍能通过。
- 日志来自 launchd 或 systemd，而不是 Tunlite 自建的 daemon。

### 第五步：修改后自动联动

保持 background 运行，修改 SOCKS 端口：

```bash
"$TL" set "$TL_NAME" -D "$TL_PORT_2"
"$TL" status "$TL_NAME"
curl --socks5-hostname "127.0.0.1:$TL_PORT_2" https://example.com/
```

新端口必须可用，状态仍显示“正常运行 / 系统后台 / 登录自启：否”。修改执行计划时这一
条隧道会短暂重启，但不需要用户执行 `sync`。然后停止：

```bash
"$TL" stop "$TL_NAME"
"$TL" status "$TL_NAME"
```

状态应回到“已停止”。

### 第六步：长期后台和登录恢复

```bash
"$TL" enable "$TL_NAME"
"$TL" status "$TL_NAME"
curl --socks5-hostname "127.0.0.1:$TL_PORT_2" https://example.com/
```

状态必须显示“正常运行 / 系统后台 / 登录自启：是”。

完整平台验收还需要退出并重新登录当前用户，或者重启测试机；重新进入终端后再次设置：

```bash
TL="$HOME/.local/bin/tunlite"
TL_NAME="之前打印并保存的测试名称"
TL_PORT_2=18081
"$TL" status "$TL_NAME"
curl --socks5-hostname "127.0.0.1:$TL_PORT_2" https://example.com/
```

必须仍显示“正常运行 / 系统后台 / 登录自启：是”，真实流量能够通过。不要在一条承担
当前唯一远程登录通道的隧道上执行重启验收，以免把自己锁在服务器外面。

## 6. 接管与交还专项验收

这是产品的核心能力，但普通首次试用可以在第五节通过后再做。

先确认测试 Connection 仍显示“正常运行 / 系统后台 / 登录自启：是”：

```bash
"$TL" status "$TL_NAME"
```

交还给操作系统：

```bash
"$TL" unmanage "$TL_NAME"
"$TL" list
curl --socks5-hostname "127.0.0.1:$TL_PORT_2" https://example.com/
```

验收结果：

- Connection 从 Tunlite 列表消失。
- 原生服务和 SSH 进程没有重启或停止。
- 真实流量不中断。

重新接管。macOS：

```bash
TL_NATIVE_ID="dev.tunlite.connection.$TL_NAME"
"$TL" manage "$TL_NATIVE_ID" --name "$TL_NAME"
```

Linux：

```bash
TL_NATIVE_ID="tunlite-$TL_NAME.service"
"$TL" manage "$TL_NATIVE_ID" --name "$TL_NAME"
```

最后验证：

```bash
"$TL" status "$TL_NAME"
curl --socks5-hostname "127.0.0.1:$TL_PORT_2" https://example.com/
```

必须重新出现在 Tunlite 中，保持 `persistent`，接管过程不应中断真实流量。

## 7. 各平台只需要额外确认什么

### macOS：Intel 与 M 系列

两种 Mac 的使用步骤相同，只有安装包不同。先确认 launchd 用户域可用：

```bash
launchctl print "gui/$(id -u)"
```

运行中的测试 Connection 可以直接从系统侧查看：

```bash
launchctl print "gui/$(id -u)/dev.tunlite.connection.$TL_NAME"
```

日志文件位于：

```text
~/Library/Logs/Tunlite/<Connection>.stdout.log
~/Library/Logs/Tunlite/<Connection>.stderr.log
```

Intel Mac 当前已完成自动化真机生命周期、日志、崩溃恢复和接管/交还零中断验证；用户
自己的 SSH 目标、真实业务流量和重新登录恢复仍需按本文人工验收。M 系列 Mac 还需在
Apple Silicon 真机完成安装、真实流量、生命周期和重新登录恢复，才能标记为平台通过。

### Linux：systemd 用户服务

先确认用户级 systemd 可用：

```bash
systemctl --user show-environment
```

运行中的测试 Connection 可以直接从系统侧查看：

```bash
systemctl --user status "tunlite-$TL_NAME.service"
journalctl --user -u "tunlite-$TL_NAME.service" -n 50 --no-pager
```

Linux 真机必须按本文完成一次安装、真实流量、重新登录恢复和接管/交还，才能标记为
systemd 真机通过。容器、WSL 或交叉编译结果不能代替这项验收。

## 8. 验收后清理

先查看当前状态：

```bash
"$TL" status "$TL_NAME"
```

如果“登录自启”为“是”：

```bash
"$TL" disable "$TL_NAME"
```

如果“运行位置”为“系统后台”且“登录自启”为“否”：

```bash
"$TL" stop "$TL_NAME"
```

状态变成“已停止”后删除：

```bash
"$TL" remove "$TL_NAME"
"$TL" list
```

测试 Connection、原生服务和 SSH 进程都应消失。macOS 历史日志和 Linux journald 历史
可以继续保留，这不是清理失败。卸载二进制不会自动停止其他 Connection；不要直接删除
正在承载隧道的 Tunlite 或系统 `ssh`。

如果还要验证删除 CLI 不影响其他 Connection，先保留至少一条已交还给系统的原生服务，
再把二进制移出 PATH：

```bash
mv "$TL" "$TL.disabled"
```

原生服务和 SSH 进程应继续运行。恢复管理能力时把文件移回即可：

```bash
mv "$TL.disabled" "$TL"
```

核心 CLI 不提供卸载或彻底清理命令；配置与服务必须通过 `remove`、`unmanage` 和本文
明确步骤处理，二进制由安装它的发行渠道删除。

## 9. 最终只需要提交这份结果

```text
日期：
测试人：
Tunlite 版本：
平台：Intel Mac / M 系列 Mac / Linux x64 / Linux arm64
系统版本：
安装：通过 / 失败
真实 SSH check：通过 / 失败
前台真实流量：通过 / 失败
临时后台真实流量：通过 / 失败
修改自动联动：通过 / 失败
重新登录或重启恢复：通过 / 失败
接管与交还不中断：通过 / 失败 / 未执行
清理：通过 / 失败
失败日志：
最终结论：通过 / 不通过
```

用户不需要附带 Go 测试、Schema、交叉编译或打包结果；这些属于项目维护者的发布证据。
