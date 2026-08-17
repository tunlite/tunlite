# 安全策略

## 支持版本

0.10.3 是最后一个 Node.js 版本，只接受严重安全、数据丢失或 v1 迁移修复。
公开预览开始后，仅最新的 `v1.0.0-preview.N` 接受常规安全修复；稳定版发布后仅维护最新
稳定版本。预览版不降低安全问题的处理优先级。

## 私下报告漏洞

请不要为未修复漏洞创建公开 Issue。使用 GitHub Private Vulnerability Reporting：

https://github.com/yuanyuanzijin/tunlite/security/advisories/new

项目会尽快确认报告。修复发布后会致谢报告者，除非报告者希望匿名。

## 安全边界

Tunlite v1：

- 调用系统 OpenSSH，不实现 SSH 协议，也不保存密码或私钥。
- 持久服务直接运行无 Shell 的 `program + args[]` 执行计划。
- 每个 Connection 独立托管，不存在聚合全部连接的控制 socket。
- 公开监听和持久 Publish 需要与精确计划摘要绑定的批准。
- 配置、服务记录、审批和迁移日志使用本机权限受限目录。
- 0.x 迁移先做字节级备份；检测到迁移后配置漂移时拒绝自动回滚。

特别欢迎报告以下问题：参数注入或 Shell 绕过、敏感信息进入 argv/日志/导出、公开监听
审批绕过、配置或服务所有权竞争、迁移覆盖或回滚数据丢失、原生服务产物权限、checksum
或构建证明绕过，以及一条 Connection 影响其他 Connection 的故障域扩大。

未来外部 Driver 会是任意本地代码，不会被宣传成安全沙箱。该能力发布前必须另外完成
来源身份、签名、构建证明、锁文件和副作用审批的安全评审。
