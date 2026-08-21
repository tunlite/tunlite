# Tunlite V1 Preview 双 Mac 人工验证

- **候选版本：** `v1.0.0-preview.1`
- **测试机器：** 安装过 Tunlite `0.10.3` 的 Intel Mac；一台全新的 Apple Silicon Mac
- **目的：** 验证真实旧配置迁移、真实 SSH 流量、launchd 生命周期、登录恢复和回滚

这不是开发测试。Go 单元测试、竞态检测、静态检查、打包和自动化 E2E 已经通过；这里
只验证自动化无法替代的真实机器、真实密钥、真实网络和真实业务流量。

## 先记住三条

1. 不要测试正在承载当前唯一远程登录的隧道；必须有本机控制台或另一条恢复通道。
2. 同一条 Connection 不能同时运行在 `0.10.3` 和 V1，两边会争抢端口或重复建立远端转发。
3. Intel Mac 上不要覆盖旧命令、不要删除旧配置；先旁路验证，失败就切回 `0.10.3`。

Intel 迁移命令里的 `<连接名>`，以及两台机器命令里的 `user@example-host`，都要替换成
真实值。Apple Silicon 测试默认直接使用文中的固定端口，只有端口被占用时才修改数字。

## 一、使用维护者提供的候选包

测试者不需要安装 Go，不需要克隆源码，也不要自己运行打包脚本。候选包由维护者使用固定
工具链生成，并在交付前运行 `scripts/verify-release.sh` 完成 checksum、归档内容、
Universal 2 架构和嵌入版本校验。

本轮 `v1.0.0-preview.1` 候选包已经生成并校验。维护者保留的标准 Release 目录包含：

```text
SHA256SUMS
tunlite_1.0.0-preview.1_macos.tar.gz
tunlite_1.0.0-preview.1_linux_x64.tar.gz
tunlite_1.0.0-preview.1_linux_arm64.tar.gz
```

Intel Mac 迁移测试需要从标准 macOS 归档旁路运行。如果候选包就在当前 Go V1 工作树的
`dist` 目录，先进入工作树并设置：

```bash
DIST="$PWD/dist"
test -f "$DIST/tunlite_1.0.0-preview.1_macos.tar.gz"
test -f "$DIST/SHA256SUMS"
```

如果候选包由其他方式交付，就把 `DIST` 改成收到的目录绝对路径。

全新的 Apple Silicon Mac 不参与构建和归档校验。维护者从已校验的 macOS 归档中提取
一个原始可执行文件，文件名就是 `tunlite`。新机器只接收这一个文件，然后把它安装到
`/usr/local/bin/tunlite`；不需要接收压缩包、`SHA256SUMS`、源码或 Go 工具链。

## 二、Intel Mac：验证 0.10.3 迁移和回滚

### 1. 记录旧版本并备份旧配置

```bash
uname -a
sw_vers

OLD_TUNLITE="$(command -v tunlite)"
printf '旧命令：%s\n' "$OLD_TUNLITE"
"$OLD_TUNLITE" version
"$OLD_TUNLITE" status
```

版本应为 `0.10.3`。确定真实旧配置路径：

```bash
if [ -n "${TUNLITE_HOME:-}" ]; then
  OLD_CONFIG="$TUNLITE_HOME/config/config.json"
else
  OLD_CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/tunlite/config.json"
fi

test -f "$OLD_CONFIG"
OLD_BACKUP="$OLD_CONFIG.before-v1-manual-test-$(date +%Y%m%d-%H%M%S)"
cp "$OLD_CONFIG" "$OLD_BACKUP"
OLD_HASH_BEFORE="$(shasum -a 256 "$OLD_CONFIG" | awk '{print $1}')"
printf '旧配置：%s\n旧配置备份：%s\n旧配置 SHA-256：%s\n' \
  "$OLD_CONFIG" "$OLD_BACKUP" "$OLD_HASH_BEFORE"
```

### 2. 在独立目录运行 V1

使用维护者提供的候选包目录；如果换了终端，先把 `DIST` 重新设为真实 `dist` 绝对路径。

```bash
V1_ROOT="$HOME/.tunlite-v1-preview-binary-$(date +%Y%m%d-%H%M%S)"
test ! -e "$V1_ROOT"
mkdir -p "$V1_ROOT"
tar -xzf "$DIST/tunlite_1.0.0-preview.1_macos.tar.gz" -C "$V1_ROOT" tunlite

V1="$V1_ROOT/tunlite"
V1_HOME="$HOME/.tunlite-v1-preview-test-$(date +%Y%m%d-%H%M%S)"

"$V1" version --json
printf 'V1 测试目录：%s\n' "$V1_HOME"
```

版本输出必须是：

```json
{"outcome":"succeeded","protocolVersion":1,"version":"1.0.0-preview.1"}
```

V1 的所有命令都带上 `TUNLITE_HOME="$V1_HOME"`，这样 V1 配置和旧版配置完全分开。

### 3. Dry-run、导入并确认旧配置没变

```bash
TUNLITE_HOME="$V1_HOME" "$V1" import "$OLD_CONFIG" --dry-run
TUNLITE_HOME="$V1_HOME" "$V1" import "$OLD_CONFIG" --dry-run --json --non-interactive

OLD_HASH_AFTER_DRY_RUN="$(shasum -a 256 "$OLD_CONFIG" | awk '{print $1}')"
test "$OLD_HASH_BEFORE" = "$OLD_HASH_AFTER_DRY_RUN"

TUNLITE_HOME="$V1_HOME" "$V1" import "$OLD_CONFIG"
TUNLITE_HOME="$V1_HOME" "$V1" validate
TUNLITE_HOME="$V1_HOME" "$V1" list
TUNLITE_HOME="$V1_HOME" "$V1" status
TUNLITE_HOME="$V1_HOME" "$V1" doctor --offline

OLD_HASH_AFTER_IMPORT="$(shasum -a 256 "$OLD_CONFIG" | awk '{print $1}')"
test "$OLD_HASH_BEFORE" = "$OLD_HASH_AFTER_IMPORT"
cmp "$OLD_CONFIG" "$OLD_BACKUP"
```

验收结果：

- dry-run 和正式导入都成功；
- Connection 数量、Host、SSH Port、Identity File、Jump Host、`-L`、`-R`、`-D` 正确；
- 导入后的 V1 Connection 全部是 `stopped`；
- 旧版隧道仍在运行；
- 两次哈希比较和 `cmp` 都成功，旧配置没有被修改。

### 4. 只切换一条安全的 Connection

选择一条即使短暂中断也不会导致失联的 Connection：

```bash
NAME='<连接名>'

"$OLD_TUNLITE" status "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" check "$NAME" --timeout 12
```

先用旧版状态和真实业务请求记录基线。真实业务请求应是你平时实际使用的方式，例如访问
本地转发端口、数据库、HTTP 服务或 SOCKS 代理，不能只看 `status`。

停止旧版，再立即启动 V1：

```bash
"$OLD_TUNLITE" disable "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" start "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" status "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" logs "$NAME" --lines 50
```

现在执行同一条真实业务请求。必须确认 V1 显示“正常运行 / 系统后台 / 登录自启：否”，
并且 SSH 真实流量正常。

如果 V1 启动或流量验证失败，立即回滚：

```bash
TUNLITE_HOME="$V1_HOME" "$V1" stop "$NAME"
"$OLD_TUNLITE" enable "$NAME"
"$OLD_TUNLITE" status "$NAME"
```

再次执行原来的真实业务请求，确认旧版已经恢复。

### 5. 主动验证一次正常回滚

即使 V1 没有失败，也要主动走一次回滚：

```bash
TUNLITE_HOME="$V1_HOME" "$V1" stop "$NAME"
"$OLD_TUNLITE" enable "$NAME"
"$OLD_TUNLITE" status "$NAME"
```

真实流量恢复后，说明回退路径有效。

### 6. 再切到 V1，验证登录自启和重新登录恢复

```bash
"$OLD_TUNLITE" disable "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" start "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" enable "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" status "$NAME"
```

状态必须显示“正常运行 / 系统后台 / 登录自启：是”，真实流量必须正常。然后退出并重新
登录当前 macOS 用户，或者重启这台测试机。不要在当前唯一远程登录依赖该隧道时做这一步。

重新登录后恢复变量并检查：

```bash
V1='<上面解压得到的 tunlite 绝对路径>'
V1_HOME='<上面打印的 V1 测试目录>'
NAME='<连接名>'

TUNLITE_HOME="$V1_HOME" "$V1" status "$NAME"
TUNLITE_HOME="$V1_HOME" "$V1" logs "$NAME" --lines 50
```

状态仍应显示“正常运行 / 系统后台 / 登录自启：是”，真实业务流量仍应正常。

### 7. Intel Mac 最终恢复旧版

```bash
OLD_TUNLITE='<第一步打印的旧命令绝对路径>'
OLD_CONFIG='<第一步打印的旧配置绝对路径>'
OLD_BACKUP='<第一步打印的旧配置备份绝对路径>'
V1='<上面解压得到的 tunlite 绝对路径>'
V1_HOME='<上面打印的 V1 测试目录>'
NAME='<连接名>'

TUNLITE_HOME="$V1_HOME" "$V1" disable "$NAME"
"$OLD_TUNLITE" enable "$NAME"
"$OLD_TUNLITE" status "$NAME"

shasum -a 256 "$OLD_CONFIG"
cmp "$OLD_CONFIG" "$OLD_BACKUP"
```

确认旧版真实流量恢复后，可以逐个删除 V1 中导入的测试 Connection：

```bash
TUNLITE_HOME="$V1_HOME" "$V1" remove '<连接名>'
```

对所有导入项重复 `remove`，确认没有残留服务后，隔离的 `$V1_HOME` 和 `$V1_ROOT` 才
可以手工删除。旧配置和 `*.before-v1-manual-test-*` 备份继续保留，不要删除。

## 三、Apple Silicon Mac：一个文件直接验证

新机器收到的文件名就是 `tunlite`。下面假设终端已经进入这个文件所在目录。

### 1. 放进 bin 目录

```bash
sudo mkdir -p /usr/local/bin
sudo install -m 0755 ./tunlite /usr/local/bin/tunlite

command -v tunlite
tunlite version --json
uname -m
sw_vers
```

`command -v tunlite` 应输出 `/usr/local/bin/tunlite`，`uname -m` 应输出 `arm64`，版本必须
是 `1.0.0-preview.1`。安装只复制这一个文件，不创建 Connection，也不修改 Shell 配置。

### 2. 准备真实 SSH 目标

```bash
ssh -o BatchMode=yes user@example-host true
```

这条命令必须不输入密码就成功。自定义 SSH 端口时使用：

```bash
ssh -p 2222 -o BatchMode=yes user@example-host true
```

后面的命令统一使用固定测试名 `preview-test`、SOCKS 端口 `18080` 和 `18081`，不需要
设置任何变量。把 `user@example-host` 替换成真实目标；自定义 SSH 端口时写成
`user@example-host:2222`。端口被占用时再直接修改命令中的端口数字。

### 3. 创建和认证检查

```bash
tunlite add preview-test --to user@example-host -D 18080
tunlite status preview-test
tunlite check preview-test --timeout 12
```

状态应显示“已停止 / 运行位置：— / 登录自启：否”，`check` 必须显示
`ssh_authentication_succeeded`。

### 4. 前台运行和真实流量

终端 A：

```bash
tunlite run preview-test
```

保持终端 A 不动，在终端 B：

```bash
curl --socks5-hostname 127.0.0.1:18080 https://example.com/
```

必须返回网页内容。回到终端 A 按 `Ctrl-C`：

```bash
tunlite status preview-test
```

状态应回到“已停止”，运行位置显示 `—`，登录自启显示“否”。

### 5. 临时后台运行

```bash
tunlite start preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18080 https://example.com/
tunlite logs preview-test --lines 50
launchctl print "gui/$(id -u)/dev.tunlite.connection.preview-test"
```

状态应显示“正常运行 / 系统后台 / 登录自启：否”；关闭启动终端以后流量仍应正常。

### 6. 修改端口并验证自动联动

```bash
tunlite set preview-test -D 18081
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/

tunlite stop preview-test
tunlite status preview-test
```

新端口必须可用，停止后状态应回到“已停止”。

### 7. 长期运行和重新登录恢复

```bash
tunlite enable preview-test
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
```

状态必须显示“正常运行 / 系统后台 / 登录自启：是”。然后退出并重新登录当前 macOS
用户，或者重启测试机。

重新登录以后：

```bash
tunlite status preview-test
curl --socks5-hostname 127.0.0.1:18081 https://example.com/
tunlite logs preview-test --lines 50
```

必须仍显示“正常运行 / 系统后台 / 登录自启：是”，真实流量必须正常。

### 8. 清理 Apple Silicon Mac 测试项

```bash
tunlite disable preview-test
tunlite status preview-test
tunlite remove preview-test
tunlite list
```

测试 Connection、launchd 服务和 SSH 进程都应消失。二进制可以保留用于后续验证；如要
卸载，在所有 Connection 都已停止并删除后，再删除 `/usr/local/bin/tunlite`。

## 四、把这些结果发回来

两台机器分别提供：

```text
机器：Intel Mac / Apple Silicon Mac
macOS 版本：
uname -a：
tunlite version --json：

安装或旁路运行：通过 / 失败
旧配置导入前 SHA-256：
旧配置导入后 SHA-256：
dry-run：通过 / 失败
正式导入：通过 / 失败
SSH check：通过 / 失败
stopped → background：通过 / 失败
background → persistent：通过 / 失败
端口修改和真实流量：通过 / 失败
重新登录或重启恢复：通过 / 失败
V1 → 0.10.3 回滚（仅 Intel）：通过 / 失败
清理：通过 / 失败
```

任何一步失败时，保留现场，不要反复覆盖安装。把失败命令、完整 stdout/stderr、
`status` 和 `logs --lines 50` 的输出一起发回来。
