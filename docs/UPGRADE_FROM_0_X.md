# 从 Tunlite 0.10.3 升级到 Go V1

- **适用平台：** macOS、使用 systemd 用户服务的 Linux
- **旧版本：** 冻结的最终 Node.js 版本 `0.10.3`
- **目标版本：** Tunlite V1 Preview

V1 首个预览版不支持 Windows。Windows 用户继续使用 `0.10.3`，等待 Task Scheduler
版本完成真实验证。

## 1. 升级方式

升级不需要独立迁移工具，也没有 `tunlite migrate` 命令。V1 的 `import` 可以直接读取
0.x 配置：

```bash
./tunlite-v1 import ~/.config/tunlite/config.json
```

旧版和 V1 使用不同的配置文件：

```text
0.x：~/.config/tunlite/config.json
V1： ~/.config/tunlite/connections.json
```

`import` 只读取旧文件，把能够准确对应的 SSH Connection 写入 V1 配置。它不会：

- 修改或删除旧配置；
- 停止旧 daemon 或旧隧道；
- 启动或 enable 导入后的 V1 Connection；
- 卸载 Node.js、0.x 或替换当前 `tunlite` 命令；
- 在新旧配置之间建立同步关系。

因此，配置导入阶段不会中断现有隧道。用户可以先完整检查 V1，再逐条切换。

## 2. 会导入和不会导入的内容

以下字段有明确对应关系，会直接导入：

- Connection 名称；
- 零个或一个旧标签：一个标签会转换为 V1 的单值 Group；
- SSH Host 和 Port；
- Identity File；
- Jump Host；
- `-L`、`-R`、`-D` 转发；
- 可以安全表示为 `Key=Value` 的 OpenSSH 选项。

以下内容没有 V1 的同义配置，不会导入：

- 同一 Connection 的多个旧标签：它们没有可靠的主次关系，导入会停止并要求先选择
  一个 Group，不会按数组顺序猜测；
- 旧 daemon 的重试、Keepalive 和日志等级等全局设置；
- Webhook 设置；
- `enabled` 对应的当前运行状态；
- `autoSetupKey`。

V1 使用自己的固定可靠性策略，也不生成或安装 SSH 密钥。需要安装 SSH 公钥时，用户
应使用系统 OpenSSH 工具，例如 `ssh-keygen` 和 `ssh-copy-id`；macOS 等没有
`ssh-copy-id` 的平台按安装文档中的原生步骤操作。

旧版处于 enabled 状态的隧道在 V1 中仍以 `stopped` 导入。这不是数据丢失，而是为了
避免两套程序同时占用同一个本地端口，或者重复建立相同的远端转发。

## 3. 必须提前知道的风险

- 配置导入本身不会中断隧道。
- 同一端口不能同时由新旧两个 SSH 进程监听，因此每条 Connection 从旧进程切换到
  V1 进程时，会有一次短暂停机。
- 不要同时运行新旧两版的同一条 Connection。本地转发会端口冲突，远端转发可能重复。
- 如果当前远程登录依赖准备切换的隧道，不要在这条远程会话中执行切换。先准备控制台、
  第二条 SSH 路径、其他 VPN，或者能够重新登录的恢复方式。
- V1 验证完成以前，不要删除 0.x 配置，也不要执行 0.x 的
  `tunlite uninstall --purge`。

## 4. 确认旧版本和旧配置

确认当前 PATH 中仍然是旧版：

```bash
command -v tunlite
tunlite version
tunlite status
```

版本应为 `0.10.3`。

默认旧配置路径为：

```text
~/.config/tunlite/config.json
```

如果设置过环境变量，路径可能是：

- `XDG_CONFIG_HOME`：`$XDG_CONFIG_HOME/tunlite/config.json`
- `TUNLITE_HOME`：`$TUNLITE_HOME/config/config.json`

下面的命令可以确定本机旧配置路径：

```bash
if [ -n "${TUNLITE_HOME:-}" ]; then
  OLD_CONFIG="$TUNLITE_HOME/config/config.json"
else
  OLD_CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/tunlite/config.json"
fi

test -f "$OLD_CONFIG"
printf '旧配置：%s\n' "$OLD_CONFIG"
```

V1 不会修改旧文件，但仍建议在升级前保留一份人工副本，防止用户之后误操作旧版：

```bash
cp "$OLD_CONFIG" "$OLD_CONFIG.before-v1"
```

## 5. 把 V1 放在临时路径

不要立即覆盖当前的 `tunlite` 命令。先从 GitHub Release 下载与本机匹配的 V1 原生
二进制，并保存为当前目录下的 `tunlite-v1`。

查看平台和 CPU：

```bash
uname -s
uname -m
```

选择对应的 Release 产物。两种 Mac 使用同一个 Universal 2 包：

| 系统输出 | Release 平台 | 机器类型 |
|---|---|---|
| `Darwin` + `x86_64`/`arm64` | `macos` | Intel Mac 或 Apple Silicon Mac |
| `Linux` + `x86_64` | `linux-x64` | x64 Linux |
| `Linux` + `aarch64` 或 `arm64` | `linux-arm64` | ARM64 Linux |

增加执行权限并确认版本：

```bash
chmod 0755 ./tunlite-v1
./tunlite-v1 version
```

整个升级过程中保持两个入口明确：

```text
tunlite       = 旧版 0.10.3
./tunlite-v1  = 新版 V1
```

## 6. 先预览导入结果

旧 daemon 此时继续运行。先执行只读预览：

```bash
./tunlite-v1 import "$OLD_CONFIG" --dry-run
```

需要机器输出时执行：

```bash
./tunlite-v1 import "$OLD_CONFIG" --dry-run --json --non-interactive
```

重点核对：

- Connection 数量是否与旧版一致；
- 每条 Connection 是否显示为 `added`；
- 是否存在无法安全转换的 SSH 选项；
- 哪些旧 Connection 处于 enabled 状态；
- daemon 设置、Webhook 和 `autoSetupKey` 被忽略的提示。

只要有一条旧 Connection 无法解析，V1 就拒绝读取整个旧文件，不生成半份转换结果。
先用 0.10.3 的正常命令修正对应 Connection，再重新执行预览。

## 7. 正式导入并检查 V1

确认预览无误后执行：

```bash
./tunlite-v1 import "$OLD_CONFIG"
```

然后检查 V1：

```bash
./tunlite-v1 validate
./tunlite-v1 list
./tunlite-v1 status
./tunlite-v1 doctor --offline
```

此时应满足：

- 旧版隧道仍然正常运行；
- V1 能列出全部导入的 Connection；
- V1 Connection 全部为 `stopped`；
- 旧配置仍然存在，并且没有因导入而改变。

逐条核对 Host、SSH Port、Identity File、Jump Host 和所有 `-L`、`-R`、`-D` 转发。
对重要 Connection 执行只读认证检查：

```bash
./tunlite-v1 check <连接名> --timeout 12
```

如果已经存在同名 V1 Connection，`import` 默认跳过该项并返回部分失败，不会静默覆盖。
只有确认 V1 的同名声明应被旧配置替换时，才使用：

```bash
./tunlite-v1 import "$OLD_CONFIG" --force
```

不要为了消除提示而直接全量强制覆盖。

## 8. 逐条切换 Connection

不要一次性停止全部旧隧道。每次只切换一条，可以把影响控制在单条 Connection 内。

先查看旧状态：

```bash
tunlite status <连接名>
```

停止这一条旧隧道，然后立即启动 V1：

```bash
tunlite disable <连接名>
./tunlite-v1 start <连接名>
```

检查状态、日志和真实业务流量：

```bash
./tunlite-v1 status <连接名>
./tunlite-v1 logs <连接名> --lines 50
```

`start` 表示临时后台运行，不会在下次用户会话启动时自动运行。确认这条 Connection 的
真实流量没有问题以后，如果它原本就需要长期运行，再执行：

```bash
./tunlite-v1 enable <连接名>
./tunlite-v1 status <连接名>
```

V1 的 `enable` 会把已经通过 `start` 运行的 Connection 直接转为长期运行，不要求先
`stop`；对 stopped Connection 执行时则会立即启动并设置自动启动。因此不应在验证前
批量执行。

依次切换剩余 Connection。每切换一条，都应验证用户真正依赖的数据库、HTTP 服务、
SOCKS 代理或远端转发，而不只是看到进程为 running。

## 9. 替换正式命令

所有 Connection 都已经由 V1 正常运行后，卸载旧版运行组件。不要添加 `--purge`：

```bash
tunlite uninstall --force
```

这是 0.10.3 的命令，用于停止并删除旧 daemon、旧服务和旧 Node runtime。它不会删除
刚才由 `./tunlite-v1` 创建的 V1 原生服务。

确认旧 daemon 已经退出：

```bash
pgrep -af 'tunlite.*daemon run' || true
```

把已经验证过的 V1 二进制安装到 PATH：

```bash
mkdir -p "$HOME/.local/bin"
install -m 0755 ./tunlite-v1 "$HOME/.local/bin/tunlite"
"$HOME/.local/bin/tunlite" version
```

如果 `command -v tunlite` 仍然没有指向 `~/.local/bin/tunlite`，把 `~/.local/bin` 放到
PATH 前面，或者继续使用绝对路径。V1 不修改 Shell 配置，也不创建 `tun` 短命令。

最终检查：

```bash
command -v tunlite
tunlite version
tunlite status
tunlite doctor --offline
```

## 10. 出现问题时回退

### 10.1 旧版尚未卸载

如果 V1 Connection 显示“系统后台 / 登录自启：否”：

```bash
./tunlite-v1 stop <连接名>
tunlite enable <连接名>
```

如果 V1 Connection 显示“系统后台 / 登录自启：是”：

```bash
./tunlite-v1 disable <连接名>
tunlite enable <连接名>
```

确认旧版恢复：

```bash
tunlite status <连接名>
```

不要同时启用两边。

### 10.2 旧版已经卸载

先停止或 disable 对应 V1 Connection，然后重新安装固定的最终旧版：

```bash
npx tunlite@0.10.3 install
tunlite enable <连接名>
```

0.x 配置仍在原路径，V1 的导入从未删除它。

## 11. 旧文件什么时候可以删除

预览阶段建议始终保留：

```text
~/.config/tunlite/config.json
~/.config/tunlite/config.json.before-v1
```

它们占用空间很小，却是最直接的回退和排查依据。只有所有 Connection 都经过真实流量、
断线恢复，以及至少一次登录或重启恢复验证后，才考虑手工归档旧文件。

V1 不提供自动清理 0.x 数据的命令。

## 12. 用户体验评审

这套升级流程的优点：

- 配置只有一个入口：`import`；
- 不安装迁移工具，也不学习一次性的 `migrate` 命令；
- 导入阶段零中断，旧配置和旧运行状态不变；
- 新旧版本使用不同配置路径，不会覆盖旧数据；
- 可以逐条切换、逐条验证和逐条回退，不必一次性停止所有隧道；
- V1 的二进制安装与 Connection 运行相互独立。

仍然存在的必要成本：

- 同一个端口不能同时由新旧进程监听，因此单条 Connection 切换时无法承诺绝对零中断；
- daemon 设置和 Webhook 没有 V1 等价物，产品不能假装已经迁移；
- 远程登录依赖被切换隧道时，用户必须准备备用入口。

整体判断：这套流程对于一次性的 0.x → V1 升级已经足够轻，同时保留了必要的安全边界。
继续减少步骤会迫使 Tunlite 静默停止旧进程或自动启用新 Connection，风险明显大于节省的
一两条命令；增加迁移日志、回滚引擎或独立工具，也不会给当前配置差异带来相称价值。

## 13. 维护者回归门禁

发布候选在 macOS 上运行：

```bash
scripts/test-migration-e2e.sh v1.0.0-preview.1
```

该脚本从 Git `v0.10.3` 标签提取冻结旧版，并使用临时配置、短 Unix socket、本机隔离
sshd、专用 launchd 标签和真实 HTTP 流量执行本章的核心流程。它验证 dry-run 不写入、
正式导入不修改旧配置且不中断旧隧道、逐条切换成功，以及停止 V1 后 0.10.3 能恢复流量。
测试不会使用用户现有的 Tunlite 配置或 Connection；专用 launchd 标签被占用时直接拒绝
运行。
