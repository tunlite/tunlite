# Tunlite V1 Preview 双 Mac 人工验证

- **候选版本：** `v1.0.0-preview.1`
- **测试机器：** Intel Mac、Apple Silicon Mac
- **目的：** 验证原生二进制、真实 SSH 流量、launchd 生命周期和登录恢复
- **最后更新：** 2026-08-31

维护者提供文件名为 `tunlite` 的原生可执行文件。测试者不需要源码、Go、Node.js、压缩包
或任何打包命令。每台机器都使用一个不会影响当前唯一远程登录的 SSH 目标。

## 两台 Mac：直接安装收到的文件

终端进入收到的 `tunlite` 文件所在目录。Intel 与 Apple Silicon 使用同一个 Universal 2
文件，不要求测试者构建或生成包。安装到全局 PATH：

```bash
sudo mkdir -p /usr/local/bin
sudo install -m 0755 ./tunlite /usr/local/bin/tunlite

command -v tunlite
tunlite version --json
uname -m
sw_vers
```

`command -v` 应输出 `/usr/local/bin/tunlite`，版本应为 `1.0.0-preview.1`。Apple Silicon
的 `uname -m` 应为 `arm64`，Intel 应为 `x86_64`。安装阶段的 sudo 只复制这一个文件，
不会注册或启动 Connection。若不能覆盖机器上的既有 `tunlite`，应换专用测试机，不要用
不同调用路径完成所谓“最终安装验收”。V1 只读取自己的 `connections.json`，不会读取
其他格式。

## 两台机器执行相同的真实验证

先确认系统 SSH 可以无密码认证：

```bash
ssh -o BatchMode=yes user@example-host true
```

把后续命令中的 `user@example-host` 换成真实目标。自定义 SSH 端口时在目标前增加
`-p 2222`。本文固定使用测试名 `preview-test` 和 SOCKS 端口 `18080`、`18081`。首次
`add` 前执行安全预检：

```bash
tunlite status preview-test
lsof -nP -iTCP:18080 -sTCP:LISTEN
lsof -nP -iTCP:18081 -sTCP:LISTEN
```

`status` 必须明确报告 `preview-test` 不存在，两条 `lsof` 必须没有输出。发现同名 Connection
或端口占用时立即停止，不执行下面的 `add`，更不能覆盖、修改或删除既有 Connection。
选择另一个明确测试名或大于 1024 的空闲端口后，把本文后续所有对应名称或端口全文一致
替换，再从本节预检重新开始；不使用 Shell 变量隐藏实际测试对象。

### 1. 声明与认证

```bash
tunlite add preview-test -D 18080 user@example-host
tunlite status preview-test
tunlite check preview-test --timeout 12
```

状态应为已停止，`check` 应成功。

### 2. 前台运行与真实流量

终端 A：

```bash
tunlite run preview-test
```

终端 B：

```bash
curl --socks5-hostname 127.0.0.1:18080 https://example.com/
```

必须返回网页内容。回到终端 A 按 `Ctrl-C`，再检查：

```bash
tunlite status preview-test
```

### 3. 临时后台、日志与 Monitor

```bash
tunlite start preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18080 https://example.com/
tunlite logs preview-test --lines 50
tunlite monitor
```

Monitor 应显示版本、PID、UP 和 RESTARTS；左右键或 Enter/Esc 可以进入和退出详情。
在详情页按 `r`，应先出现重启确认；只有 `y/Y` 执行，其他键取消，执行期间显示“重启中”。
按 Esc 返回总览，最外层再按 Esc 退出。不要在这里按 `x` 停止，避免改变后续步骤。退出
Monitor 后继续：

```bash
tunlite set preview-test -D 18081
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

### 4. 自动启动与恢复

```bash
tunlite enable preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

状态应显示正常运行、系统后台、自动启动为是。退出并重新登录当前 macOS 用户，或重启
测试机；不要在唯一远程登录依赖该 Connection 时执行。重新登录后：

```bash
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
tunlite logs preview-test --lines 50
```

连接应自动恢复，真实流量仍然可用。

### 5. macOS system Scope

只在不承担当前唯一远程登录的 `preview-test` 上执行。把 user LaunchAgent 迁移为 system
LaunchDaemon：

```bash
tunlite enable preview-test --scope system
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

Tunlite 应先说明将注册 system 服务，随后由系统 `/usr/bin/sudo` 在终端显示 `Password:`。
测试者自己输入 macOS 管理员密码；输入时没有回显是正常现象。Tunlite 和测试 Agent 都不
读取、记录或转交密码。单条命令只做一次交互授权；下一条命令能否复用由本机 sudo 策略
决定。

成功后范围应为 `SYSTEM`，自动启动仍为“是”。清除 sudo timestamp，再验证 restart 不
需要密码：

```bash
sudo -k
tunlite restart preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

`restart` 不得显示密码提示；它只终止已验证属于当前用户的 SSH Provider，再由 LaunchDaemon
已有的 KeepAlive 拉起，不修改 root-owned 服务定义。如果身份、定义或 KeepAlive 无法
证明一致，命令应零变更失败，不能降级请求 sudo。

验证双向迁移，再回到 system 供 Monitor 验收：

```bash
tunlite enable preview-test --scope user
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/

tunlite enable preview-test --scope system
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

两次迁移都应保持 persistent 和真实流量，范围依次为 `USER`、`SYSTEM`。进入或离开 system
都涉及 root-owned 定义，可能显示密码提示；同一终端能否复用刚才授权由 sudo 策略决定。

清除授权缓存后打开 Monitor，确保 system restart 不会意外创建授权，而 system disable
确实由系统读取密码：

```bash
sudo -k
tunlite monitor
```

system 行应显示 Scope、PID、UP 和 RESTARTS。按 `r` 并用 `y` 确认，仍不得索要密码；
执行时显示“重启中”，终端始终可导航和退出。随后按 `x`，应出现“停止并关闭自动启动”
的强确认；确认后 Monitor 必须先恢复正常终端，再让系统 sudo 读取密码，结束后恢复 TUI。
此动作实际执行 system `disable`，因此需要管理员权限。若不希望在 Monitor 中完成 disable，
按其他键取消，退出 Monitor 后在下一节执行同一操作。

### 6. 清理

```bash
tunlite disable preview-test
tunlite status preview-test
tunlite remove preview-test
tunlite list
```

如果上一节已经在 Monitor 中确认 system disable，再执行 `tunlite disable preview-test` 应
保持幂等，不应再次要求密码。若尚未 disable，CLI 会由 `/usr/bin/sudo` 读取密码并解除
LaunchDaemon；随后 `remove` 只删除已停止的用户态 Connection/Record，不需要 sudo。
测试 Connection、launchd 服务和 SSH 进程都应消失。

## 回传结果

```text
机器：Intel Mac / Apple Silicon Mac
macOS 版本：
uname -a：
tunlite version --json：
安装或隔离运行：通过 / 失败
SSH check：通过 / 失败
前台真实流量：通过 / 失败
临时后台与日志：通过 / 失败
Monitor 导航、确认和中间状态：通过 / 失败
端口修改与真实流量：通过 / 失败
重新登录或重启恢复：通过 / 失败
user→system 与真实流量：通过 / 失败
system→user→system 与真实流量：通过 / 失败
system restart 零密码：通过 / 失败
Monitor system restart/disable 与终端恢复：通过 / 失败
清理：通过 / 失败
```

任何一步失败时保留现场，把失败命令、完整 stdout/stderr、`status` 和
`logs --lines 50` 的输出一起反馈。
