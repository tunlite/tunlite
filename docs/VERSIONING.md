# 版本与发布规则

Tunlite v1 只使用两类版本名：

- `v1.0.0-preview.N`：公开预览版，`N` 从 1 递增。
- `v1.0.0`：预览验证完成后的稳定版。

不增加 Alpha、Beta、RC 等层级，不移动或复用已经公开的 Tag。

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

1. 运行全部自动测试、race、vet、真实 OpenSSH 和当前平台原生服务测试。
2. 在支持矩阵要求的平台做真机验收。
3. 生成本地候选包，逐个校验嵌入版本、归档内容和 `SHA256SUMS`。
4. 更新 CHANGELOG、预览版指南和支持矩阵，只记录已经验证的事实。
5. 提交发布变更并完成人工审查。
6. 获得明确发布授权后，才允许推送 Tag、创建 GitHub Release 和生成在线构建证明。
7. 下载公开 Release 的产物重新校验 checksum 与 Attestation，再宣布发布完成。

自动发布工作流拥有仓库写权限和 OIDC 身份，已经作为单独的安全开关获得授权并完成审查。
它只响应 `v1.0.0-preview.*` 和 `v1.0.0` Tag；普通 Push、Pull Request 和手工操作不能
触发发布。加入工作流不等于获得推送 Tag 的授权，每次实际发布仍需由人明确决定。

## 稳定版条件

`v1.0.0` 不以日期自动触发。至少满足：

- SSH 核心行为和机器契约在多个预览版中没有破坏性漂移。
- systemd 与 launchd 经真实用户和真机持续验证。
- 0.10.3 迁移、失败恢复和人工回滚有真实案例。
- 安装成功率、首次连接耗时、断线恢复和多隧道使用达到可接受水平。
- 已知高风险问题均已关闭，升级文档能覆盖主流路径。

外部 Driver 协议不属于 `v1.0.0` 稳定条件；它必须在后续经过多类 Provider 的真实使用
验证后独立稳定。
