# Git 远端同步

## Goal

让用户通过标准 HTTPS Git Remote 安全同步 Workspace 中被 Git 跟踪的内容，并在 dirty、分叉、离线或认证失败时得到明确状态，而不是静默覆盖或假成功。

## Dependencies

- 硬依赖 `08-02-quick-capture-profile` 已交付外部文件变化重新捕获、Ingestion 与 Index 入口；远端 Fast-forward 后必须调用该契约。
- 与 `08-02-document-file-history` 共用 Git CLI Runner 安全基础，但 Remote Adapter 必须是独立端口，不能扩宽 Change Control GitRepository。
- 复用 Workspace root resolver、Auth/Capability、Outbox/Worker 和通用 Secret Sealer。

## Requirements

### Configuration And Secret

- 每个 Workspace 首版只配置一个标准 HTTPS Remote URL；支持 GitHub、GitLab、Gitee、Gitea 等标准 Git 服务，不依赖专有 API。
- Remote URL 禁止 userinfo、非 HTTPS、fragment 和不安全重定向；显示时规范化并脱敏。
- 首版以访问令牌认证。Token 使用服务端专用加密 Secret Store；明文不得进入响应、日志、审计、URL、Git Config、Workspace 或 Git 命令参数。
- 配置修改使用 expected revision；`keep|replace|clear` Secret action 与返回语义明确，Token 不可回读。

### Sync

- 用户可手动同步，也可选择批准写回完成后自动同步；自动同步默认关闭。
- 每次同步先持久化 SyncRun，再 Fetch 并重新计算本地分支、HEAD、工作树、远端 OID 和 ancestry。
- 远端单向领先且工作树严格干净时可 Fast-forward 本地；成功后触发外部变更捕获、Ingestion 和 Index。
- 本地单向领先且远端无独有提交时可非 force Push；Push 后重新读取远端 OID 验证。
- 两端相同则完成且不产生多余 Git 变化。
- dirty、detached、历史分叉、ref 漂移、non-fast-forward 或结果未知时停止；不自动 Merge、Rebase、Force Push、Reset、Checkout 或冲突解决。
- 同步展示未配置、待拉取、待推送、同步中、已同步、离线、认证失败、dirty、分叉、失败和人工恢复状态。
- Git 已同步与拉取内容的捕获/索引状态分开展示；后者失败不能回滚 Git 或把同步标为失败。

### Failure And Consistency

- 手动失败不改变本地正式知识；自动同步失败只记录远端未同步并允许重试，不改变已完成 Proposal/Commit/Index 语义。
- SyncRun、attempt、expected refs 和 post-check 持久化，response loss 重试不得重复危险副作用。
- 配置变化、Workspace 切换和并发运行受 CAS/唯一活动运行约束；旧运行不能使用新 Token 或写新 Workspace 状态。
- Git Remote 同步只覆盖 Git 跟踪内容，不声称备份 PostgreSQL、原始大文件、ignored Content Artifact 或完整应用状态。

## Acceptance Criteria

- [x] 标准 HTTPS Remote 使用加密 Token 完成 Fetch/Push，明文不出现在响应、日志、argv、Git Config 或 Workspace。
- [x] Remote 单向领先且本地 clean 时 Fast-forward，并随后捕获/索引变更。
- [x] Local 单向领先时非 force Push，post-check 证明远端 OID 一致后才显示已同步。
- [x] dirty、detached 或 diverged 时停止并展示双方 Commit/文件差异，没有 Merge/Rebase/Force/Reset/Checkout。
- [x] 自动同步失败不回滚或改写本地 Proposal、Commit 和索引状态。
- [x] Git 同步成功而重新索引失败时，两个状态分别显示且索引可重试。
- [x] response loss、Worker restart 和相同幂等键不会创建重复 SyncRun 或重复 Push。
- [x] Workspace/Remote revision 漂移、认证失败、离线和结果未知都有稳定可恢复状态。
- [x] Settings 桌面与 `390x844` 页面可配置、手动同步、查看状态和冲突，无 Secret 回显。

## Out Of Scope

- SSH Key、多 Remote、Provider OAuth/专有 API。
- 应用内 Merge/Rebase/冲突编辑、Force Push、自动冲突解决。
- PostgreSQL、Content Artifact 和完整 Workspace 备份。
- 多主数据库或实时协作同步。
