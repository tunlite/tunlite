# Git 开发与仓库治理规范

本文定义 Tunlite 长期使用的仓库、分支、提交、评审、发布、安全和归档规则。当前项目由
单维护者主导，规则优先保证简单、可执行和可追溯；团队扩大时按本文的升级条件增强治理，
不提前引入不必要的流程。

产品边界以 [PROJECT_DIRECTION.md](PROJECT_DIRECTION.md) 为准，架构边界以
[ARCHITECTURE.md](ARCHITECTURE.md) 为准，版本和发布验收以
[VERSIONING.md](VERSIONING.md) 为准。

## 1. 仓库决策

### 1.1 代码切代，仓库不断代

Go v1 在现有公开仓库 `github.com/yuanyuanzijin/tunlite` 中原地演进，不新建另一份产品
仓库，也不创建无父提交的 orphan 主线。现有公开仓库只有很短的 0.x 历史；重建只能让
`git log` 表面更干净，却会割裂 Tag、Release、Issue、外部链接和构建来源，不能改善 Go
程序本身。

旧 Node 实现从未来 `main` 删除，由 `v0.10.3` Tag 和 `legacy/node-0.x` 保留。这样当前
工作树只有 Go Core，同时继续保留项目身份和完整发布证据。

只有无法通过撤销和轮换处理的严重密钥泄露、法律删除要求或经验证的 Git 对象损坏，才
允许讨论历史重写。执行前必须备份全部 Refs、记录原因和影响，并获得仓库所有者明确批准。

### 1.2 迁移到 `tunlite` Organization

长期产品地址使用 `github.com/tunlite/tunlite`。迁移方式是把现有
`github.com/yuanyuanzijin/tunlite` **Transfer** 到 `tunlite` Organization，而不是创建
新仓库后复制文件。Transfer 保留 Git 历史、Issue、Pull Request、Tag、Release、Star 和
旧 URL 重定向。

迁移应在第一个 Go v1 Preview 发布前完成，并在同一迁移阶段把 Go module、内部 Import、
README、Bootstrap、Badge、Security Advisory 和 Release URL 更新到组织地址。0.x 是
Node 产品，不存在需要兼容的旧 Go module，因此现在改变 module path 的成本最低。

组织设置遵循：

- 强制成员启用 2FA，Owner 使用硬件密钥并离线保存恢复方式；
- 当前单维护者保留一个真实 Owner，不创建共享账号或虚假备用账号；
- 出现第二名长期维护者后增加第二位 Owner，日常权限通过 Team 分配；
- 默认权限最小化，发布、Security 和 Maintainer 权限分别管理；
- 仓库 Transfer 后重新核对 Actions、Secrets、Ruleset、Pages、Webhook 和 Release 权限。

### 1.3 公开仓库是唯一产品事实来源

公开仓库统一承载：

- 产品源码和公共文档；
- Issue、Pull Request 和工程决策；
- CI、Tag、Release、checksum 和构建证明；
- 兼容承诺、安全公告和公开路线图。

核心源码不得在 public/private 两个仓库中双向同步。保密漏洞使用 GitHub Security
Advisory 的临时私有 Fork，凭据使用 GitHub Secrets，商业资料和私人基础设施使用独立的
内部资料仓库。内部仓库不能维护另一份分叉的 `cmd/`、`internal/` 或发布流水线。

### 1.4 `tunlite_private` 永久归档

`tunlite_private` 保存公开前的原型历史，不删除，但停止产品开发：

1. 人工检查当前未提交内容，区分私人资料与应进入公开产品的内容。
2. 在私有仓库 `archive/final-state` 分支形成最后一个归档提交。
3. 创建包含全部 Refs 的 Git Bundle，生成 SHA-256，至少保存在线、离线两份。
4. 记录最终 Commit、Bundle checksum、归档日期和保管位置。
5. 在托管平台将仓库设为 Archive/只读，不删除 Branch、Tag 或原目录。

归档只用于审计和考古，不参与构建、发布或日常开发，也不作为公开仓库的镜像。

## 2. Organization 仓库拓扑

现在只需要创建或迁入 **一个** 产品仓库：

| 仓库 | 可见性 | 创建时机 | 职责 |
|---|---|---|---|
| `tunlite/tunlite` | Public | 现在 | Go Core、CLI、内置 SSH Driver、Schema、Skill、文档、CI 和 Release |
| `tunlite/.github` | Public | 出现第二个公开仓库时 | Organization Profile 和跨仓库默认社区文件 |
| `tunlite/homebrew-tap` | Public | 首次正式支持 Homebrew 时 | Homebrew Formula 的独立发布入口 |
| `tunlite/desktop` | Public | Desktop 有独立构建和发布周期时 | Desktop 应用；依赖公开 Core，不复制 Core 源码 |
| `tunlite/driver-*` | Public | 外部 Driver 协议验证并开放时 | 每个官方 Driver 独立版本和发布 |
| `tunlite/website` | Public | 网站需要独立技术栈或发布权限时 | 官网；在此之前继续放在核心仓库 |
| `tunlite/ops-private` | Private | 确有商业或私有运维资料时 | 私有资料和基础设施；禁止复制公开 Core |

不要预先创建空仓库。是否拆分由“独立版本、独立权限、独立发布周期”决定，不按目录数量
决定。`use-tunlite` Skill 当前与 CLI 契约共同测试，继续放在核心仓库；只有它能够独立
兼容多个 Core 版本并需要独立发布时才拆分。

旧 `tunlite_private` 是历史档案，不迁入 Organization 充当 `ops-private`，也不计入活跃
产品仓库数量。

## 3. 长期分支模型

```text
main                         唯一常驻开发主线，始终可测试
feat/<topic>                 短期功能分支
fix/<topic>                  短期缺陷分支
docs/<topic>                 短期文档分支
refactor/<topic>             不改变公共行为的重构分支
release/<major>.<minor>      仅在确实需要回补旧版本时创建
legacy/node-0.x              冻结的 Node 0.10.3 维护线
v<major>.<minor>.<patch>     不可移动的发布 Tag
```

完成 v1 迁移后不再保留永久 `dev`、`develop` 或 `next` 分支。大型开发可以使用 Git
Worktree 做物理隔离，但 Worktree 仍属于同一个公开仓库，分支合入后删除。

## 4. v1 一次性迁移

### 4.1 冻结 Node 线

1. 确认 `v0.10.3` 指向最后一个 Node 发布提交。
2. 从该 Tag 创建 `legacy/node-0.x`。
3. 该分支只接受高危安全、严重数据损坏或阻断 v1 导入的修复。
4. 停止 npm 新版本发布，保留旧 Release 和升级资料。

### 4.2 Transfer 到 Organization

1. 在 Transfer 前暂停 Push，确认本地工作区和远端 Branch/Tag 状态并创建 Mirror/Bundle。
2. 将现有公开仓库 Transfer 到 `tunlite` Organization，仓库名仍为 `tunlite`。
3. 更新本地 `origin` 和 Go module/import 路径，搜索并替换旧 GitHub 地址。
4. 验证旧 URL 重定向、Clone、Actions、Pages、Security、Release 和权限设置。
5. Transfer 和 module path 迁移通过独立 PR 完成，不与 v1 Tag 发布放在同一操作中。

### 4.3 Go v1 成为主线

1. 推送 `rewrite/v1-go`，通过 Pull Request 和完整 CI 合入当前默认分支。
2. 将默认分支统一命名为 `main`，同步更新 Ruleset、本地跟踪分支和文档链接。
3. v1 导入验收稳定后，从 `main` 删除 `src/`、旧 `test/`、旧 `bin/` 等 Node 实现。
4. 保留 0.10.3 黑盒契约、导入夹具、升级文档、Release notes、Tag 和 legacy 分支。
5. 删除已合入的 rewrite 分支、临时 Worktree，以及过渡完成后的 `dev/master` 分支。

删除过渡分支不等于删除历史：发布 Tag 和 `legacy/node-0.x` 必须先验证能从空目录取回。

## 5. 日常开发

### 5.1 开始工作

```bash
git switch main
git pull --ff-only
git switch -c feat/short-topic
```

一个分支只解决一个可以独立解释和回滚的问题。名称使用小写英文和连字符，不使用个人
姓名、版本号或 `update`、`changes` 等模糊名称。长期试验可以使用 `spike/<topic>`，但
试验成果必须整理后通过正常 PR 合入。

### 5.2 Commit

Commit 必须原子、可构建、可解释，并包含保持行为一致所需的测试、Schema 和直接相关
文档。禁止提交 Token、私钥、真实用户数据、私人主机名、生产配置和构建产物。

标题使用 Conventional Commits：

```text
feat(cli): add connection selector
fix(service): preserve ownership during restart
docs: define release workflow
refactor(store): isolate atomic replacement
test(ssh): cover jump host failure
build: update release packaging
ci: split race detector job
chore: refresh repository metadata
```

破坏性变更在正文写 `BREAKING CHANGE:` 并提供迁移说明。合入前整理无意义的 `WIP` 和
反复修补提交。个人分支允许 `git push --force-with-lease`，禁止裸 `--force`；`main`、
`release/*` 和发布 Tag 永远禁止 Force Push。

### 5.3 Pull Request

进入 `main` 和 `release/*` 的变更必须通过 PR。PR 需要说明：

- 解决什么问题、为什么这样解决；
- 用户行为、兼容性、安全和平台影响；
- 实际运行了哪些自动测试和真机测试；
- 数据或原生服务变更如何升级、失败恢复和回滚。

公共 CLI/JSON 变化必须同步更新测试、Schema、Capability、Agent Skill 和文档。PR 不得
混入无关格式化和重命名。未准备好评审时使用 Draft。

默认使用 **Squash Merge**，PR 标题成为符合 Conventional Commits 的主线 Commit。
只有大型迁移需要保留多个独立且已评审的提交时，维护者才选择 Rebase Merge；普通 PR
不向 `main` 制造 Merge Commit。

### 5.4 当前单维护者模式

单维护者阶段允许作者在以下条件全部满足后合入自己的 PR：

- 必需 CI 全部通过；
- PR 模板完整填写；
- 已查看完整 Diff，并确认没有秘密和无关文件；
- 用户可见行为已更新测试和文档；
- 发布、安全、不可逆数据操作仍经过单独人工确认。

出现第二名长期拥有合入权限的维护者后，代码 PR 启用至少一名非作者批准。涉及发布权限、
配置迁移、原生服务所有权和安全模型的变更再要求领域所有者参与。流程随团队增长增强，
不在单维护者阶段虚构无法执行的多人门槛。

## 6. CI 和仓库保护

`main` 至少启用：

- 必须通过 Pull Request 合入；
- 禁止 Force Push 和删除；
- 必须通过 Go test（支持矩阵）、race、vet 和 release-package；
- 必须解决 Review conversation；
- 管理员绕过保留审计记录，只用于正在发生的安全或发布事故。

`release/*` 使用相同保护。`v*` Tag 只有发布维护者可以创建，任何人不能更新或删除。
发布 Tag 使用 Annotated Tag；稳定版和安全修复优先使用可验证签名。

GitHub Actions 默认 `contents: read`。写权限、OIDC、Attestation 和 Release 权限只放在
需要它们的 Job。新增第三方 Action 先审查来源；依赖更新通过 PR 和完整测试合入。

## 7. 版本、发布与维护

- 版本遵循 SemVer，预览版规则见 [VERSIONING.md](VERSIONING.md)。
- `main` 面向下一个版本，开发构建不能冒充 Release。
- Tag 永不移动、删除或复用；失败的发布使用新的版本号修复。
- 只有旧版本实际需要回补时，才从对应 Tag 创建 `release/X.Y`。
- Backport 使用 `git cherry-pick -x` 保留来源，并把修复继续合入 `main`。
- 用户可见变化在同一 PR 更新 CHANGELOG 的 Unreleased 部分。
- Release 只由 Tag 触发，产物由 CI 从该 Tag 构建，禁止上传来源不明的本机二进制。

当前不承诺长期多版本支持：预览阶段只修复最新 Preview；稳定后默认主动维护最新稳定
版本。用户规模和维护团队扩大后，再在 SECURITY.md 中明确增加支持窗口。

## 8. 架构与兼容决策

以下变化必须先写 Issue 或设计文档，再实现：

- 公共 CLI、退出码、JSON、Schema 和配置版本；
- 数据迁移、回滚边界和文件所有权；
- Driver/插件协议与安全信任模型；
- 支持平台、最低 Go/OS 版本和发行格式；
- 默认分支、合并策略、许可证和发布签名策略。

产品方向变化更新 `PROJECT_DIRECTION.md`，核心边界变化更新 `ARCHITECTURE.md`。需要长期
保留背景和取舍的单项决定放入 `docs/decisions/NNNN-*.md`；后续决策明确替代旧决策，不
通过删除历史假装旧决策没有发生。

## 9. 安全、生成物和备份

- 漏洞通过 SECURITY.md 和 Private Vulnerability Reporting 报告，不先开公开 Issue。
- 凭据泄露先撤销和轮换；删除 Commit 不能使秘密失效。
- 二进制、`dist/`、覆盖率、日志和本机配置不进入 Git；发布产物放 GitHub Release。
- 测试夹具必须最小、脱敏并说明来源；默认不启用 Git LFS。
- 公开仓库定期创建包含全部 Refs 的 Mirror 或 Bundle，并备份 Release checksum。
- 备份至少保存在两个独立位置，定期抽样验证能够恢复和构建。

公开 GitHub 仓库是权威来源，但不是唯一副本；备份和 Fork 都不能获得发布权或成为新的
开发主线。

## 10. 规则升级条件

以下情况发生时调整治理，而不是现在预设全部企业流程：

- 两名以上长期维护者：启用非作者 Review 和更细 CODEOWNERS。
- 同时维护多个稳定版本：正式启用 `release/X.Y`、Backport 标签和支持窗口。
- 发布频率或下载量明显增长：增加 Release Environment 审批和定期恢复演练。
- 引入外部 Driver：单独建立协议稳定性、插件来源和安全评审规则。
- 引入付费/闭源组件：建立独立内部仓库，公开 Core 仍是唯一开源事实来源。

本规范通过普通 PR 修改。默认分支、发布权限、安全边界和支持承诺的变化必须说明迁移
步骤、生效时间和回滚方式；每年至少复审一次，也可以留下“无需修改”的结论。

## 11. 一次性操作清单

### 11.1 公开仓库迁移

```bash
cd /Users/admin/jly_gh/tunlite-go-v1
git status --short
go test ./...
go test -race ./...
go vet ./...
git push -u origin rewrite/v1-go

cd /Users/admin/jly_gh/tunlite
git fetch origin --tags
git branch legacy/node-0.x v0.10.3
git push origin legacy/node-0.x
```

在 GitHub 仓库 Settings 中将现有仓库 Transfer 给 `tunlite` Organization。Transfer 后：

```bash
cd /Users/admin/jly_gh/tunlite
git remote set-url origin git@github.com:tunlite/tunlite.git
git remote -v
git fetch origin --prune --tags

cd /Users/admin/jly_gh/tunlite-go-v1
git remote -v
rg -n "github.com/yuanyuanzijin/tunlite|raw.githubusercontent.com/yuanyuanzijin/tunlite" .
```

Worktree 共享 Remote 配置，所以只在主 Worktree 修改一次 `origin`；第二段用于复核。随后
通过单独 PR 更新 `go.mod`、Go Import 和全部旧 URL。创建并合入 `rewrite/v1-go` PR，
再把默认分支重命名为 `main`，本地更新：

```bash
cd /Users/admin/jly_gh/tunlite
git fetch origin --prune
git branch -m <旧默认分支> main
git branch --set-upstream-to=origin/main main
git pull --ff-only
```

更新仍指向旧默认分支的 Raw URL 和 Release notes，确认保护规则生效，再清理已合入的
rewrite 分支和 Worktree。每次删除远端过渡分支之前，单独确认默认分支、Tag 和 legacy
分支均可取回；不把改默认分支、删分支和发布 Tag 塞进一个脚本。

### 11.2 private 归档

人工确认归档内容后执行：

```bash
cd /Users/admin/jly_gh/tunlite_private
git switch -c archive/final-state
git add -A
git diff --cached --check
git commit -m "chore(archive): preserve final private state"
git tag -a archive/private-final -m "Final private repository archive"
git push -u origin archive/final-state
git push origin archive/private-final

git bundle create ../tunlite_private-final.bundle --all
shasum -a 256 ../tunlite_private-final.bundle
git bundle verify ../tunlite_private-final.bundle
```

复制 Bundle 和 checksum 到第二个独立位置，记录最终 Commit 后，将 private 仓库设为
Archive/只读。整个过程不删除原目录、远端仓库、Branch 或 Tag。
