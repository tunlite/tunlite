# Tunlite v1 安装与用户端到端验收

- **适用版本：** `v1.0.0-preview.1`
- **适用平台：** macOS 12+、使用 systemd 用户服务的 Linux
- **最后更新：** 2026-11-06

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
| Intel Mac LaunchDaemon USER→SYSTEM→USER→SYSTEM、真实 `-L` 流量、system `set`、无 sudo restart、disable/remove 与精确清理 | 通过 |
| Ubuntu 24.04 PID 1 systemd 容器中的真实 OpenSSH、崩溃恢复、容器重启和 CLI 移除/替换 | 通过 |
| macOS x64/arm64、Linux x64/arm64 原生构建 | 通过 |
| 三个本地候选归档的 SHA-256 | 通过；不代表公开 Release 校验已完成 |
| bootstrap 下载、校验和原子替换的隔离测试 | 通过；公开 Release 安装须在发布后验证 |

当前开发机是 Intel Mac，因此以下项目不能用交叉编译冒充真机通过：

- Apple Silicon Mac 上的实际安装、登录恢复和真实 SSH 流量。
- Linux 真机上的 systemd 用户服务、登录恢复和真实 SSH 流量。
- 用户自己的 SSH 密钥、网络、跳板机和目标服务。

这些才是后面需要用户或对应平台测试人员完成的内容。
Apple Silicon user/system 与非容器 Linux 真机验收不阻塞 Preview.1，但仍是稳定
`v1.0.0` 前的硬门禁。

## 2. 选择安装渠道与平台

macOS 推荐 Homebrew 下载并校验预编译二进制，不安装 Go 或其他编译工具链。
也可直接下载预编译归档；只有希望手工编译的用户才需要 Go 1.25+ 和固定版本
`go install`。三种渠道安装相同的 CLI，不会自动迁移旧开发版的 Service Record。
Homebrew Tap 已公开，下文命令可直接使用。

选择预编译渠道时，先查看机器类型：

```bash
uname -s
uname -m
```

按结果选择一个包：

| 机器 | 维护者交付来源 | 收到后文件名 |
|---|---|---|
| Intel 或 M 系列 Mac，`Darwin` | macOS Universal 2 产物 | `tunlite` |
| x64 Linux，`Linux` + `x86_64` | Linux x64 产物 | `tunlite` |
| ARM64 Linux，`Linux` + `aarch64`/`arm64` | Linux arm64 产物 | `tunlite` |

首个预览版不发布 Windows 版本。没有 systemd 用户会话的 Linux 只能验收前台 `run`，
暂时不能验收原生后台服务。

## 3. 安装

运行本地原生二进制只需要系统 `ssh`，不需要 Node.js、npm 或 Go：

```bash
command -v ssh
```

应输出 `ssh` 的路径。发布后的在线安装脚本另外需要 `curl`、`tar` 和
`sha256sum`/`shasum`。

### 3.1 安装收到的原生二进制

在对应 GitHub Release 可用前，可由维护者直接提供文件名为 `tunlite` 的单文件原生二进制；
测试者不需要源码、Go、Node.js、压缩包或打包命令。终端进入该文件所在目录后执行：

```bash
test -f ./tunlite
chmod +x ./tunlite
mkdir -p "$HOME/.local/bin"
install -m 0755 ./tunlite "$HOME/.local/bin/tunlite"
```

这只安装 `~/.local/bin/tunlite`，不会创建、启动或修改任何 Connection，也不会创建
短命令。确保 `~/.local/bin` 在 PATH 后直接检查：

```bash
command -v tunlite
tunlite version --json
```

如果 `command -v` 没有输出 `~/.local/bin/tunlite`，当前终端可执行
`export PATH="$HOME/.local/bin:$PATH"` 后重试；Tunlite 不会自动修改 Shell 启动文件。

需要安装到全局 PATH 时，改用下面这一组，不要两处同时安装：

```bash
sudo mkdir -p /usr/local/bin
sudo install -m 0755 ./tunlite /usr/local/bin/tunlite
command -v tunlite
tunlite version --json
```

这里的 sudo 只用于复制二进制，不会注册或启动 Tunlite Connection。

必须看到：

```json
{"outcome":"succeeded","protocolVersion":1,"version":"1.0.0-preview.1"}
```

### 3.2 发布以后：一条命令安装

对应 GitHub Release、归档和 `SHA256SUMS` 可用后，macOS 和 Linux 用户使用：

```bash
curl -fsSL https://raw.githubusercontent.com/tunlite/tunlite/main/bootstrap.sh | sh
```

安装脚本会自动识别系统和 CPU、校验 SHA-256，并安装到 `~/.local/bin/tunlite`。
新构建的三个平台归档均带 `skills/use-tunlite/SKILL.md`（0644），与二进制来自同一
源码检出，受同一归档 checksum 和 Attestation 覆盖。bootstrap 将它安装到
`~/.local/share/tunlite/skills/use-tunlite`，打印实际目录和给 Agent 的启用提示。
`--install-dir <prefix>/bin` 或 `TUNLITE_INSTALL_DIR=<prefix>/bin` 会将 Skill 放到
`<prefix>/share/tunlite/skills/use-tunlite`；任意自定义二进制目录都使用其父目录下的
`share`。包含空格的路径须加引号。脚本先暂存并验证再替换；下载、校验、二进制检查或
安装失败时保留或恢复旧的二进制和 Skill。若系统连恢复操作也拒绝，脚本会报错并
保留备份目录，打印其路径供手动恢复，不会报告安装成功。

现有公开 `v1.0.0-preview.1` 归档及其 checksum 不变，其中没有 Skill。bootstrap
仍可安装二进制，并提示同版本
`https://raw.githubusercontent.com/tunlite/tunlite/v1.0.0-preview.1/skill/use-tunlite/SKILL.md`
交给 Agent 安装；不下载未校验内容，也不声称 Skill 已安装到本地。
对应 Release 尚未发布时，下载地址可能返回 404；请使用维护者提供的候选二进制，不能
跳过校验。公开发布完成仍须下载实际 Release 产物，重新校验 checksum 与 Attestation。

### 3.3 Homebrew 二进制安装（macOS 推荐）

通过公开的 [`tunlite/homebrew-tap`](https://github.com/tunlite/homebrew-tap) 安装：

```bash
brew install tunlite/tap/tunlite
type -a tunlite
tunlite version --json
```

Formula 下载固定 `v1.0.0-preview.1` 的官方 Release 二进制归档并验证对应 SHA-256。
macOS 使用 Universal 2 产物，Linux 按 x86_64 或 arm64 选择；不安装 Go，也不编译源码。
二进制由 Homebrew 安装到自己的 prefix，按照 Homebrew 的 `brew shellenv` 提示配置 PATH。
没有 `brew services start tunlite`：每个 Connection 仍由 launchd 或 systemd 独立管理。
Linux 使用同一 Formula 时，持久连接仍要求 systemd 用户会话，Linux system Scope 不支持。

```bash
brew update
brew upgrade tunlite/tap/tunlite
# 需要重新安装当前包时：
brew reinstall tunlite/tap/tunlite
# 仅在需要卸载 CLI 时执行：
brew uninstall tunlite/tap/tunlite
```

已添加过 Tap 的用户重试前必须先 `brew update`，以取得新的二进制 Formula。
revision 2（Homebrew 包版本 `1.0.0-preview.1_2`）在二进制包中增加匹配 Skill，
使此前同版本包也能升级；不会自动
删除之前已安装的 Go。升级只会获得 Tap 已发布的包。Homebrew 分发不提供 Apple
开发者签名或公证，也不保证不受 quarantine 或企业设备策略限制。

### 3.4 可选：固定版本源码 go install

安装 Go 1.25 或更新版本后执行（未覆盖 `GOSUMDB`，保留 Go 默认校验）：

```bash
git clone --depth 1 --branch v1.0.0-preview.1 https://github.com/tunlite/tunlite.git tunlite-source
cd tunlite-source
go install -trimpath -ldflags='-s -w -X github.com/tunlite/tunlite/internal/cli.Version=1.0.0-preview.1' ./cmd/tunlite
go env GOBIN GOPATH
```

固定 Tag 检出需要 Git。不要省略检出后直接使用远程
`go install github.com/tunlite/tunlite/cmd/tunlite@v1.0.0-preview.1`：当前 Go 会查询
旧稳定版 `v0.10.3` 的弃用信息，该 Tag 属于此前的非 Go 实现，`sum.golang.org` 对其
`go.mod` 返回 404。上面的本地模块安装保留正常依赖校验，避免这项远程模块查询；
不要通过关闭 `GOSUMDB` 或放宽 `GONOSUMDB` 绕过失败。

`GOBIN` 非空时就是安装目录；否则使用 `GOPATH` 第一项下的 `bin`（通常为
`$HOME/go/bin`）。把实际目录加入 PATH，例如默认配置下：

```bash
export PATH="$HOME/go/bin:$PATH"
type -a tunlite
tunlite version --json
```

必须看到 `1.0.0-preview.1`，不是源码默认的 `1.0.0-preview.1-dev`。升级时在新目录检出
目标 Tag，并同步更新 `-X` 注入版本；Tag 保留前缀 `v`，注入值不带 `v`。
卸载只删除实际 `GOBIN` 或 `GOPATH/bin` 中的 `tunlite` 文件，不删除整个目录。

直接源码编译通常不会带有浏览器下载文件的 quarantine 属性；这不等同于
Apple 开发者认证、签名或公证，也不能保证绕过企业设备策略。

### 3.5 PATH、渠道切换与 Shell 配置

先用 `type -a tunlite` 检查所有命中的副本，选择 Homebrew、源码或预编译中的一个渠道。
`~/.local/bin`、`~/go/bin` 或 `/usr/local/bin` 中靠前的旧文件可能遮住新版本；调整 PATH
或在核对所属渠道后手动移除确切旧文件，不要用 `brew link --overwrite` 覆盖不明副本。
再次检查 `tunlite version --json`。安装、升级与卸载都不升级旧开发版服务记录。
卸载 CLI 不会停止已由操作系统托管的 SSH 服务；需要停止连接时，须在卸载前显式使用
对应的 Connection 生命周期命令，不能依靠包管理器完成。

V1 Preview 暂不提供 Shell 自动补全。安装验收只需要确认 Tunlite 不创建、
读取或修改 `.bashrc`、`.zshrc`、fish 配置等 Shell 启动文件，不需要执行任何补全安装
命令。安装、升级和卸载由发行渠道负责，不属于核心 CLI。

Agent Skill 的启用是可选步骤。Homebrew 为现有 Preview.1 归档下载同 Tag、固定
SHA-256 的 [`use-tunlite` Skill](../skill/use-tunlite/)，安装到包的
`share/tunlite/skills/use-tunlite`，提示使用稳定 `opt` 路径。把安装器打印的实际目录
交给 Agent，例如：`请从目录 "<打印的目录>" 安装并启用 use-tunlite Skill。`
对于不带 Skill 的旧版 bootstrap 归档，使用提示中的同版本 URL。二进制目录已在 PATH
时仍会显示此提示。由 Agent 根据自身扩展机制安装、检查、更新和卸载；升级包后还需
让 Agent 刷新已复制的 Skill，只有支持符号链接的 Agent 才可直接链接稳定目录。
安装器不自动写入 Agent 目录；CLI 和 `doctor` 也不读取 Agent 的私有目录。

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

下面固定使用测试名 `preview-test` 和 SOCKS 端口 `18080`、`18081`。先确认没有同名
Connection，且端口未被其他程序使用：

```bash
tunlite status preview-test
lsof -nP -iTCP:18080 -sTCP:LISTEN
lsof -nP -iTCP:18081 -sTCP:LISTEN
```

`status` 报告不存在、两条 `lsof` 没有输出才可继续。若名称或端口冲突，把本文后续所有
`preview-test` 或对应端口统一替换成另一个明确值；端口应大于 1024。自定义 SSH 端口时，
在下面 `add` 的 `-D` 前增加 `-p 2222`。

## 5. 核心端到端验收

以下流程在 macOS 和 Linux 上相同。

### 第一步：创建，但不启动

```bash
tunlite add preview-test -D 18080 user@example-host
tunlite status preview-test
```

验收结果：

- Connection 创建成功。
- 状态显示“已停止 / 运行位置：— / 自动启动：否”。
- 没有 SSH 隧道进程被启动。
- macOS/Linux 支持原生服务时，服务定义已经创建，但尚未运行。

### 第二步：验证用户自己的 SSH 认证

```bash
tunlite check preview-test --timeout 12
```

必须看到 `ssh_authentication_succeeded`。失败时先解决 Host Key、密钥、网络或跳板机问题，
不要继续测试后台运行。如果只是尚未安装本机公钥，用户可以在交互终端使用系统工具：

```bash
ssh-copy-id user@example-host         # 系统提供 ssh-copy-id 时
# 或按照服务器文档把 ~/.ssh/id_ed25519.pub 加入远端 ~/.ssh/authorized_keys
tunlite check preview-test --timeout 12
```

OpenSSH 可能直接要求远端账户密码；只能由用户在终端输入。自动化和 Agent 不得等待、
收集或代用户提交密码。

### 第三步：前台运行和真实流量

终端 A：

```bash
tunlite run preview-test
```

保持终端 A 不动，在终端 B 验证真实 SOCKS 流量：

```bash
curl --socks5-hostname 127.0.0.1:18080 https://example.com/
```

必须能返回网页内容。然后回到终端 A 按 `Ctrl-C`，再次执行：

```bash
tunlite status preview-test
```

状态应回到“已停止”。这一步证明 `run` 的生命周期确实属于当前终端。

### 第四步：临时后台运行

```bash
tunlite start preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18080 https://example.com/
tunlite logs preview-test --lines 50
```

验收结果：

- 状态显示“正常运行 / 系统后台 / 自动启动：否”。
- 关闭启动它的终端后，隧道仍然存在。
- 真实 SOCKS 流量仍能通过。
- 日志来自 launchd 或 systemd，而不是 Tunlite 自建的 daemon。

### 第五步：修改后自动联动

保持 background 运行，修改 SOCKS 端口：

```bash
tunlite set preview-test -D 18081
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

新端口必须可用，状态仍显示“正常运行 / 系统后台 / 自动启动：否”。修改执行计划时这一
条隧道会短暂重启，但不需要用户执行 `sync`。然后停止：

```bash
tunlite stop preview-test
tunlite status preview-test
```

状态应回到“已停止”。

### 第六步：长期后台和登录恢复

```bash
tunlite enable preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

状态必须显示“正常运行 / 系统后台 / 自动启动：是”。

完整平台验收还需要退出并重新登录当前用户，或者重启测试机；重新进入终端后直接验证：

```bash
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

必须仍显示“正常运行 / 系统后台 / 自动启动：是”，真实流量能够通过。不要在一条承担
当前唯一远程登录通道的隧道上执行重启验收，以免把自己锁在服务器外面。

## 6. 接管与交还专项验收

这是产品的核心能力，但普通首次试用可以在第五节通过后再做。

先确认测试 Connection 仍显示“正常运行 / 系统后台 / 自动启动：是”：

```bash
tunlite status preview-test
```

交还给操作系统：

```bash
tunlite unmanage preview-test
tunlite list
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

验收结果：

- Connection 从 Tunlite 列表消失。
- 原生服务和 SSH 进程没有重启或停止。
- 真实流量不中断。

重新接管。macOS：

```bash
tunlite manage dev.tunlite.connection.preview-test --name preview-test
```

Linux：

```bash
tunlite manage tunlite-preview-test.service --name preview-test
```

最后验证：

```bash
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

必须重新出现在 Tunlite 中，保持 `persistent`，接管过程不应中断真实流量。

## 7. 各平台只需要额外确认什么

### macOS：Intel 与 M 系列

Intel 与 Apple Silicon 使用同一个 macOS Universal 2 文件和完全相同的安装、验收步骤；
区别只有 `uname -m` 的架构结果，以及 Apple Silicon 真机门禁仍待完成。先确认 launchd
用户域可用：

```bash
launchctl print "gui/$(id -u)"
```

运行中的测试 Connection 可以直接从系统侧查看：

```bash
launchctl print "gui/$(id -u)/dev.tunlite.connection.preview-test"
```

日志文件位于：

```text
~/Library/Logs/Tunlite/<Connection>.stdout.log
~/Library/Logs/Tunlite/<Connection>.stderr.log
```

Intel Mac 当前已完成 LaunchAgent Release E2E，以及 LaunchDaemon
USER→SYSTEM→USER→SYSTEM、真实 `-L` 流量、system `set`、无 sudo restart 和清理验证；
用户自己的 SSH 目标与网络仍需按本文验证。M 系列 Mac 还需在 Apple Silicon 真机完成
安装、user/system 生命周期、Monitor、真实流量和重新登录恢复，才能标记为硬件通过。

macOS 还应在不承担唯一远程登录通道的测试 Connection 上验收 system Scope。先从 user
迁移到 system：

```bash
tunlite enable preview-test --scope system
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

Tunlite 应先说明将注册 system 服务；随后由 `/usr/bin/sudo` 显示自己的 `Password:` 提示。
Tunlite 不读取或保存密码。同一条命令只做一次交互授权，后续精确系统写入只使用该授权；
是否复用到下一条命令由 macOS 的 sudo 策略和缓存决定。成功后状态中的范围为 `SYSTEM`，
服务在 LaunchDaemon 域运行。

先验证 system restart 不需要管理员密码。清除当前 sudo timestamp 后执行：

```bash
sudo -k
tunlite restart preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

`restart` 不应显示密码提示，范围和自动启动保持不变，流量短暂中断后恢复。只有注册、解除
system 服务或原子替换 system 配置才需要 root；只读、status、logs 和 restart 不需要。
再迁回 user：

```bash
tunlite enable preview-test --scope user
tunlite status preview-test
```

离开 system 同样需要删除系统定义，因此也会请求一次管理员授权；成功后范围恢复为
`USER`。Tunlite 不读取或保存密码，密码输入完全属于 `sudo`。

脚本不得依赖交互提示。以下命令在没有预置 sudo 授权时必须快速返回
`privilege_required`，不能挂住；已经由运维预置授权时，平台层仍只执行精确的
`sudo -n -- ...`：

```bash
tunlite enable preview-test --scope system --json --non-interactive
```

`--dry-run` 只检查和展示计划，不应显示密码提示、请求授权或修改 user/system 服务。

### Linux：systemd 用户服务

先确认用户级 systemd 可用：

```bash
systemctl --user show-environment
```

运行中的测试 Connection 可以直接从系统侧查看：

```bash
systemctl --user status tunlite-preview-test.service
journalctl --user -u tunlite-preview-test.service -n 50 --no-pager
```

Linux 真机必须按本文完成一次安装、真实流量、重新登录恢复和接管/交还，才能标记为
systemd 真机通过。容器、WSL 或交叉编译结果不能代替这项验收。

## 8. 验收后清理

先查看当前状态：

```bash
tunlite status preview-test
```

如果“自动启动”为“是”：

```bash
tunlite disable preview-test
```

如果“运行位置”为“系统后台”且“自动启动”为“否”：

```bash
tunlite stop preview-test
```

状态变成“已停止”后删除：

```bash
tunlite remove preview-test
tunlite list
```

测试 Connection、原生服务和 SSH 进程都应消失。macOS 历史日志和 Linux journald 历史
可以继续保留，这不是清理失败。卸载二进制不会自动停止其他 Connection；不要直接删除
正在承载隧道的 Tunlite 或系统 `ssh`。

如果还要验证删除 CLI 不影响其他 Connection，先保留至少一条已交还给系统的原生服务，
再把二进制移出 PATH：

```bash
mv "$HOME/.local/bin/tunlite" "$HOME/.local/bin/tunlite.disabled"
```

原生服务和 SSH 进程应继续运行。恢复管理能力时把文件移回即可：

```bash
mv "$HOME/.local/bin/tunlite.disabled" "$HOME/.local/bin/tunlite"
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
