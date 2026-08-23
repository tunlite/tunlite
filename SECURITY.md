# 安全策略

## 支持版本

公开预览开始后，仅最新的 `v1.0.0-preview.N` 接受常规安全修复。稳定版发布后默认主动
维护最新稳定版本；当前不承诺长期维护多个版本。预览版不降低安全问题的处理优先级，
完整维护规则见
[Git 开发与仓库治理规范](docs/GIT_GOVERNANCE.md)。

## 私下报告漏洞

请不要为未修复漏洞创建公开 Issue。使用 GitHub Private Vulnerability Reporting：

https://github.com/yuanyuanzijin/tunlite/security/advisories/new

项目会尽快确认报告。修复发布后会致谢报告者，除非报告者希望匿名。

## 安全边界

Tunlite v1：

- 调用系统 OpenSSH，不实现 SSH 协议，也不保存密码或私钥。
- 持久服务直接运行无 Shell 的 `program + args[]` 执行计划。
- 每个 Connection 独立托管，不存在聚合全部连接的控制 socket。
- 公开监听和持久 Publish 返回结构化风险提示，不维护容易漂移的第二套批准状态。
- 配置和服务记录使用本机权限受限目录，并通过原子写入和文件锁保护。
- `import` 只接受严格校验的 V1 `ConnectionList`，不会启动 Connection。

特别欢迎报告以下问题：参数注入或 Shell 绕过、敏感信息进入 argv/日志/导出、公开监听
风险提示遗漏、配置或服务所有权竞争、导入覆盖或数据丢失、原生服务产物权限、checksum
或构建证明绕过，以及一条 Connection 影响其他 Connection 的故障域扩大。

未来外部 Driver 会是任意本地代码，不会被宣传成安全沙箱。该能力发布前必须另外完成
来源身份、签名、构建证明、锁文件和副作用审批的安全评审。
