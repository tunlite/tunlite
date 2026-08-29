# 版本与发布规则

仓库、分支、Review、Tag 保护、维护线和归档的长期规则见
[Git 开发与仓库治理规范](GIT_GOVERNANCE.md)。本文只定义版本号、发布产物和发布验收。

Tunlite v1 只使用两类版本名：

- `v1.0.0-preview.N`：公开预览版，`N` 从 1 递增。
- `v1.0.0`：预览验证完成后的稳定版。

不增加 Alpha、Beta、RC 等层级，不移动或复用已经公开的 Tag。

## 当前候选状态

当前仓库准备的是 `v1.0.0-preview.1` 候选，不是稳定 `v1.0.0`。Preview 可以暴露需要
验证和调整的产品边界；在至少多个公开 Preview 之间没有破坏性契约漂移，并完成本文列出的
稳定版条件前，不创建 `v1.0.0` Tag，也不在文案中把当前能力称为稳定版。

## 唯一权威

公开仓库的提交、Tag、GitHub Release 和构建证明是唯一发布事实。npm 中的 0.x 包只代表
历史 Node 产品，不是 v1 运行时的来源，也不得继续声明为最新架构。

发布版本由 Git Tag 注入 Go 二进制：

```text
v1.0.0-preview.1 -> tunlite version -> 1.0.0-preview.1
```

开发构建使用 `1.0.0-preview.1-dev`，不能冒充发布产物。

## 发布产物

每个版本固定提供：

- macOS：同时包含 x86_64 和 arm64 的 Universal 2 `.tar.gz`。
- Linux：分别提供 x64、arm64 的 `.tar.gz`。
- 覆盖全部压缩包的 `SHA256SUMS`。
- GitHub Actions 对产物生成的 Artifact Attestation。

本地和 CI 使用同一入口：

```bash
scripts/package-release.sh v1.0.0-preview.1
```

打包工具使用 `CGO_ENABLED=0`、`-trimpath`、固定归档时间和固定文件顺序，版本通过链接
参数注入。相同源码、Go 工具链和版本应产生可复核的归档。

## 发布流程

1. 运行全部自动测试、race、vet、依赖校验、staticcheck、govulncheck、真实 OpenSSH 和
   当前平台原生服务测试。
2. 在支持矩阵要求的平台做真机验收。
3. 生成本地候选包，逐个校验嵌入版本、归档内容和 `SHA256SUMS`。
4. 更新 CHANGELOG、预览版指南和支持矩阵，只记录已经验证的事实。
5. 提交发布变更并完成人工审查。
6. 获得明确发布授权后，才允许推送 Tag、创建 GitHub Release 和生成在线构建证明。
7. 下载公开 Release 的产物重新校验 checksum 与 Attestation，再宣布发布完成。

自动发布工作流拥有仓库写权限和 OIDC 身份，已经作为单独的安全开关获得授权并完成审查。
它只响应 `v1.0.0-preview.*` 和 `v1.0.0` Tag；普通 Push、Pull Request 和手工操作不能
触发发布。加入工作流不等于获得推送 Tag 的授权，每次实际发布仍需由人明确决定。
Tag 工作流必须在自身运行中重新执行跨平台 test/vet、race、最低 Go 版本、module tidy/
verify、staticcheck、govulncheck、真实 OpenSSH，以及 macOS 归档二进制的 launchd E2E；
`publish` 只能依赖全部 Gate 之后产生并完成 Attestation 的 Artifact，不能把另一个松散的
Push CI 结果当作发布许可。

## 稳定版条件

`v1.0.0` 不以日期自动触发。至少满足：

- SSH 核心行为和机器契约在多个预览版中没有破坏性漂移。
- systemd 与 launchd 经真实用户和真机持续验证。
- 安装成功率、首次连接耗时、断线恢复和多隧道使用达到可接受水平。
- 已知高风险问题均已关闭，安装和版本升级文档能覆盖主流路径。

外部 Driver 协议不属于 `v1.0.0` 稳定条件；它必须在后续经过多类 Provider 的真实使用
验证后独立稳定。

## v1.0.0 之后

稳定版以后遵循 SemVer：向后兼容的功能增加 Minor，向后兼容的修复增加 Patch，公共
CLI、机器契约或配置的破坏性变化只能进入新的 Major。仅当旧版本实际需要 Backport 时才
创建 `release/X.Y`；发布 Tag 不移动、不复用，失败的发布使用新版本号修复。

当前不承诺长期维护多个版本：默认主动维护最新稳定版本。用户规模和维护团队足以支撑
多版本维护后，再明确 `release/X.Y` 和支持窗口；范围变化必须在发布前同步更新
SECURITY.md。
