# 文档文件历史与受控恢复契约

## Scenario: Bounded Current-Path History And Append-Only Restore

### 1. Scope / Trigger

- 修改 `internal/documenthistory`、`internal/platform/gitcli/history.go`、迁移 `00075`、
  `restore_document` Proposal、Safe Writeback 恢复路径或 Authoring restore publication 时应用。
- History 是当前分支、当前 Document canonical `.md` path 的有界只读投影；Git 和 Authoring/Change Control
  仍分别拥有 Commit 与业务映射。恢复只能追加 Proposal、Revision 和 Commit，不能改写历史。

### 2. Signatures

```text
GET  /api/v1/workspaces/{workspace_id}/documents/{document_id}/history
GET  /api/v1/workspaces/{workspace_id}/documents/{document_id}/history/compare?left={ref}&right={ref}
POST /api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-previews
POST /api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-proposals
```

```go
type DocumentReader interface {
    GetDocument(context.Context, foundation.ID, foundation.ID) (DocumentSnapshot, error)
    MapCommits(context.Context, foundation.ID, foundation.ID, string, []string) ([]domain.CommitMapping, error)
}

type GitHistoryReader interface {
    Inspect(context.Context, foundation.ID, string) (domain.RepositoryState, error)
    List(context.Context, foundation.ID, string, int, int) (domain.CommitPage, error)
    Compare(context.Context, foundation.ID, string, string, string) (domain.Diff, error)
    ReadBlob(context.Context, foundation.ID, string, string) (domain.Blob, error)
}

type RestoreProposalCreator interface {
    CreateRestoreDocumentProposal(context.Context, CreateRestoreProposalRecord) (RestoreProposalReceipt, error)
}
```

- 版本引用只允许规范 SHA-1/SHA-256 OID 或 compare 专用 `WORKTREE`；恢复目标必须是 Commit OID。
- 时间线项是 `MANAGED | EXTERNAL | CURRENT_CHANGE` 判别联合。

### 3. Contracts

- 服务端先按 Workspace + Document ID 读取当前 Document/version/canonical path，再绑定一次 Git root、attached branch、HEAD、
  object format 和工作树状态。首版不 follow rename、不读其他分支或远端引用，也不提供通用 Git 图谱。
- canonical path 必须是 Workspace 内规范相对 POSIX `.md` 路径，拒绝绝对路径、反斜杠、控制字符、`.`、`..`、
  `.git`、`.knowledge` 和符号链接逃逸。Git root 在一次操作内保持同一 identity，避免 root 切换 TOCTOU。
- 首页仅在该 path dirty 时添加一条 `CURRENT_CHANGE`；响应中的 `dirty` 表示整个仓库 dirty。其他文件 dirty 时可以没有
  `CURRENT_CHANGE`，但仍阻止 restore，不能伪造当前 Document 的 Diff。
- Commit 映射按整页批量查询，不逐 Commit N+1。无映射为 `EXTERNAL`；有合法部分映射仍为 `MANAGED`，缺少的 Revision、
  Proposal、Approval、Workflow 或 Writeback 关系保持空，不补造旧审批事实。重复/歧义映射 fail closed。
- Cursor 使用 HMAC 绑定 Workspace、Document、path、branch、HEAD、limit、offset 和上一 Commit。每页最多 50 条，窗口最多
  5000 条；HEAD/path/位置变化返回 stale，客户端必须重开首屏。
- branch 最多 512 bytes、父 Commit 最多 64 个；OID 必须匹配仓库 object format。Blob 最大 10 MiB、Diff 最大 2 MiB，
  输出必须完整 UTF-8 且无 NUL；超限拒绝，不能截断后冒充可信结果。
- Git Adapter 固定禁用 Hook、pager/editor、签名、replace object、global/system config、optional lock 和调用方参数；拒绝
  `info/grafts` 与不安全 attributes。只允许有界 log/show/cat-file/diff/ancestry/object validation。
- restore preview 由服务端从当前 HEAD 与当前分支目标 Commit 的 exact blob 生成，绑定 Workspace、Document、path、
  expected HEAD、Document version、current/target content hash 和 diff hash。dirty 时可以展示 preview，但不能创建 Proposal。
- restore proposal 使用 `Idempotency-Key`。exact replay 在重新读取可变 Git 状态前返回原结果；同 key 不同请求冲突。
  新请求必须重新证明 clean/attached、HEAD/version/path/blob/preview 全部一致，随后创建强类型 `restore_document` Proposal。
- Approval 后只走 Safe Writeback。恢复不调用 reset、checkout、rebase、cherry-pick、ref move 或历史重写；外部 Commit
  只能提供目标快照，不能获得伪造的历史审批关系。
- `FILE_PREPARED` 崩溃恢复在真正 Commit 前必须重新加载 Proposal、校验 Execution binding、重新执行 Authoring owner
  preflight 和 lease fence。Document 已推进时转 `NEEDS_REVISION`，不得把陈旧 restore 提交到 Git。
- restore Commit finalization 在 PostgreSQL 事务内精确绑定 Proposal Commit、Document version、current Revision 和目标内容，
  append 一条 Article Revision 并推进 Document；并发或响应丢失重放只能有一个 writer，其余返回无副作用 replay。
- `00075` 为 additive migration，mapping 查询有 `(workspace_id, document_id, target_path, git_commit)` partial index；后续迁移必须
  保留 restore publication 事实与 Authoring guard，不得用逆向 DDL 删除。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 非法 UUID/path/OID/ref/limit、重复参数或未知 JSON 字段 | 400；不执行 Git/DB 写入 |
| Document 不属于 Workspace | 404 防枚举 |
| Cursor 签名无效 | `DOCUMENT_HISTORY_CURSOR_INVALID`；不返回部分页面 |
| Cursor 的 HEAD/path/position 漂移 | 409 `DOCUMENT_HISTORY_CURSOR_STALE`；客户端清理旧页并取首屏 |
| Git detached、root/path/object format 不一致或结果 shape 非法 | fail closed；不返回部分可信历史 |
| Blob/Diff/history window 超限 | `DOCUMENT_HISTORY_OUTPUT_TOO_LARGE`；不截断 |
| restore 时仓库任意 dirty | 409 `DOCUMENT_RESTORE_DIRTY_WORKTREE`；不创建 Proposal |
| HEAD、Document version、path、blob 或 preview hash 漂移 | 409 `DOCUMENT_RESTORE_STALE` 或一致性错误；不创建/提交 restore |
| 相同 idempotency key 绑定不同 restore 请求 | 409 `IDEMPOTENCY_KEY_REUSED` |
| `FILE_PREPARED` 恢复时 Authoring owner 已变化 | Proposal 转 `NEEDS_REVISION`；不调用 Git Commit CAS |
| `00075` 下存在受保护 restore publication 事实 | SQLSTATE `55000`；不删除历史 |

### 5. Good / Base / Bad Cases

- Good：用户浏览当前 path 历史，比较外部 Commit 与 HEAD，预览恢复后创建 Proposal；批准产生新 Commit 和新 Article Revision，
  原 Commit 全部保留。
- Base：历史 Commit 没有业务映射时明确显示外部变更；部分映射只展示真实存在的关系；其他文件 dirty 时历史仍可读但恢复禁用。
- Bad：把 worktree 当 Commit 分页、为外部 Commit 补审批、逐 Commit 查数据库、截断大 Diff、在 `FILE_PREPARED` 重试时
  跳过 owner preflight，或用 reset/checkout 实现恢复。

### 6. Tests Required

- Domain/Application：union、partial mapping、HMAC cursor、stale/position drift、SHA-1/SHA-256、parent/branch/offset/输出边界、
  dirty 与 path-dirty 区分、preview hash、exact replay/idempotency conflict。
- Git：临时仓库 normal/external/dirty/detached、symlink/path traversal、root TOCTOU、grafts/replace/attributes、输出超限，
  并静态证明无 reset/checkout/history rewrite 命令路径。
- PostgreSQL：批量映射无 N+1、Workspace/path/Document 绑定、fresh/repeat/旧版本数据前向升级、partial index、restore publication
  exact-once/concurrent replay 和函数定义精确恢复。
- Change Control/Authoring：typed Proposal、approval/writeback/reindex、`FILE_PREPARED` owner drift、Commit finalization、
  response loss 与并发 finalize。
- HTTP/Auth/OpenAPI/Composition：strict query/body/header、Capability、Problem、API/Worker production wiring。
- Web：strict decoder、Workspace/head/path/version/cursor Query key、SSE recovery、partial relations、Monaco cleanup、长文本与响应式布局。
- Canonical：受影响 Go race/vet、真实 PostgreSQL、OpenAPI、Web lint/typecheck/test/build、`go mod tidy -diff` 和 `git diff --check`。

### 7. Wrong vs Correct

```text
Wrong: external Commit 没有 Approval，就推断它未经审批并填入假 ID。
Correct: 标记 EXTERNAL；所有缺失关系保持 null，历史只陈述可验证事实。

Wrong: restore 时 checkout 旧版本或 reset 当前分支。
Correct: 读取旧 blob 创建 typed Proposal；Approval 后按 Safe Writeback 追加新 Commit。

Wrong: FILE_PREPARED 恢复直接 CommitCAS，因为文件 intent 已经持久化。
Correct: 重新校验 Proposal/Execution、Authoring owner 和 lease；owner 漂移转 NEEDS_REVISION。

Wrong: 其他文件 dirty 时伪造当前 Document 的 CURRENT_CHANGE，或允许恢复覆盖仓库状态。
Correct: CURRENT_CHANGE 只由 path dirty 决定；全局 dirty 独立展示并阻止 restore。
```
