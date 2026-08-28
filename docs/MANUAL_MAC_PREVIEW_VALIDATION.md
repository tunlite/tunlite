# Tunlite V1 Preview 双 Mac 人工验证

- **候选版本：** `v1.0.0-preview.1`
- **测试机器：** Intel Mac、Apple Silicon Mac
- **目的：** 验证原生二进制、真实 SSH 流量、launchd 生命周期和登录恢复

维护者提供文件名为 `tunlite` 的原生可执行文件。测试者不需要源码、Go、Node.js、压缩包
或任何打包命令。每台机器都使用一个不会影响当前唯一远程登录的 SSH 目标。

## Apple Silicon Mac：直接安装

终端进入收到的 `tunlite` 文件所在目录，然后执行：

```bash
sudo mkdir -p /usr/local/bin
sudo install -m 0755 ./tunlite /usr/local/bin/tunlite

command -v tunlite
tunlite version --json
uname -m
sw_vers
```

`command -v` 应输出 `/usr/local/bin/tunlite`，`uname -m` 应输出 `arm64`，版本应为
`1.0.0-preview.1`。安装只复制这一个文件。

## Intel Mac：隔离运行候选文件

如果机器已有同名命令，不覆盖它。终端进入收到的 `tunlite` 文件所在目录，执行：

```bash
chmod +x ./tunlite
./tunlite version --json
uname -m
sw_vers
```

版本应为 `1.0.0-preview.1`，`uname -m` 应输出 `x86_64`。后续 Intel 命令把开头的
`tunlite` 替换为 `./tunlite`。V1 只读取自己的 `connections.json`，不会读取其他格式。

## 两台机器执行相同的真实验证

先确认系统 SSH 可以无密码认证：

```bash
ssh -o BatchMode=yes user@example-host true
```

把后续命令中的 `user@example-host` 换成真实目标。自定义 SSH 端口时在目标前增加
`-p 2222`。固定测试名为 `preview-test`，SOCKS 端口使用 `18080` 和
`18081`；端口冲突时直接修改数字。

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
停止和重启必须先显示确认，执行期间应显示“停止中”或“重启中”。退出 Monitor 后继续：

```bash
tunlite set preview-test -D 18081
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

### 4. 登录自启与恢复

```bash
tunlite enable preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

状态应显示正常运行、系统后台、登录自启为是。退出并重新登录当前 macOS 用户，或重启
测试机；不要在唯一远程登录依赖该 Connection 时执行。重新登录后：

```bash
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
tunlite logs preview-test --lines 50
```

连接应自动恢复，真实流量仍然可用。

### 5. 清理

```bash
tunlite disable preview-test
tunlite status preview-test
tunlite remove preview-test
tunlite list
```

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
清理：通过 / 失败
```

任何一步失败时保留现场，把失败命令、完整 stdout/stderr、`status` 和
`logs --lines 50` 的输出一起反馈。
