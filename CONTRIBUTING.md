# 为 Tunlite 贡献代码

提交架构或产品变更前，请先阅读 [项目方向](docs/PROJECT_DIRECTION.md)和
[Git 开发与仓库治理规范](docs/GIT_GOVERNANCE.md)。前者定义 SSH 聚焦、Go 单文件、原生
每 Connection 服务和未来 Driver 边界，后者定义分支、Commit、Review、发布和归档规则。

## 基本原则

- v1 只正式交付内置 SSH Driver，但通用 Connection、ExecutionPlan 和 Service Adapter
  不能写成 SSH 专用。
- 不新增管理全部 Connection 的中心常驻进程、控制 socket 或全局 reconcile。
- 不自造传输协议；继续调用用户已经信任的系统 Provider。
- 机器接口必须保持 `--json`、稳定退出码、`--non-interactive`、`--dry-run`、结构化
  action 和逐项部分失败语义。
- 平台能力不同就通过 Capability 和支持矩阵如实表达，不能静默降级。
- 外部 Driver 协议仍是未来实验范围，不能为了一个假想生态提前冻结。

## 开发环境

需要 Go 1.22 或更高版本。运行时端到端测试还需要系统 OpenSSH。

```bash
git clone https://github.com/yuanyuanzijin/tunlite.git
cd tunlite
go run ./cmd/tunlite help
go test ./...
go test -race ./...
go vet ./...
```

普通测试不修改真实原生服务。真实测试必须显式开启：

```bash
TUNLITE_REAL_SSH=1 go test ./internal/cli \
  -run TestRealOpenSSHAllForwardTypes -count=1 -v

TUNLITE_REAL_SERVICE=1 go test ./internal/service \
  -run TestRealNativeServiceLifecycle -count=1 -v
```

原生服务测试会使用临时、唯一名称并在测试结束后清理。新增平台逻辑时，必须同时提供无特权
自动测试；真机测试不能替代可重复的普通测试。

## 提交要求

1. 从最新 `main` 创建符合治理规范的短期独立分支。
2. 修改公共 JSON 行为时同步更新 Schema、Capability、Agent Skill 契约测试和迁移说明。
3. 修改原生服务时覆盖幂等、漂移、并发、失败回滚和声明所有权。
4. 确认 test、race、vet 和相关真实测试通过。
5. Pull Request 说明改变了什么、为什么，以及兼容和安全影响。

Commit 和 Pull Request 标题必须使用 Conventional Commits（`feat:`、`fix:`、`docs:` 等）；
默认使用 Squash Merge，详细规则见 [Git 开发与仓库治理规范](docs/GIT_GOVERNANCE.md)。

Bug 和功能建议可以提交公开 Issue；安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。
贡献内容按 [MIT License](LICENSE) 授权。

## 历史 Node 实现

根目录的 `src/`、`test/`、`bin/` 和 `package.json` 仅保留用于审计 0.10.3、验证迁移和
必要的严重修复。npm 包已标记为 private，旧 Node CI 只允许人工触发。v1 功能、测试和
发布不得继续建立在这些文件上；普通贡献请修改 Go 实现。
