# 主动创作与 Document Draft 契约

## Scenario: Working Draft, Freeze And Governed Publication

### 1. Scope / Trigger

- 修改 `internal/authoring`、Document/Article Revision 写入、Authoring HTTP/OpenAPI、
  `CREATE_ONLY` Change Control 或迁移 `00069`-`00073` 时应用。
- Working Draft 是可恢复编辑状态；Document Draft 和 Article Revision 是正式领域事实；
  Git/文件写入继续由 Proposal、Approval 和 Safe Writeback 唯一拥有。

### 2. Signatures

```text
GET|POST /api/v1/workspaces/{workspace_id}/authoring/working-drafts
GET|PUT  /api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}
POST     /api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}/freeze
GET      /api/v1/workspaces/{workspace_id}/authoring/documents
GET      /api/v1/workspaces/{workspace_id}/documents/{document_id}
POST     /api/v1/workspaces/{workspace_id}/documents/{document_id}/revisions/{revision_id}/publish-proposals
GET      /api/v1/workspaces/{workspace_id}/authoring/overview
```

```go
Repository.Create(context.Context, CreateRecord) (CreateResult, error)
Repository.Update(context.Context, UpdateRecord) (UpdateResult, error)
Repository.List(context.Context, ListQuery) (Page, error)
Repository.ListDocuments(context.Context, DocumentListQuery) (DocumentPage, error)
Repository.Freeze(context.Context, FreezeRecord) (FreezeResult, error)
Repository.ReservePublication(context.Context, ReservePublicationRecord) (PublicationPreparation, error)
Repository.AbandonPublication(context.Context, AbandonPublicationRecord) (PublicationReservation, error)
Repository.CompletePublication(context.Context, CompletePublicationRecord) (PublishResult, error)
Repository.ReconcilePublications(context.Context, ReconcileQuery) (int, error)
```

### 3. Contracts

- Working Draft 允许空标题、空路径和暂时无效 Markdown；每次保存严格 `expected_version` CAS，
  服务端是恢复事实源，浏览器不建立第二份持久状态机。
- Freeze 才执行可发布校验。每次成功 Freeze 都追加不可变 Article Revision，不覆盖旧 Revision；
  首次 Freeze 原子创建 Document Draft，后续 Freeze 绑定同一 Document 和 parent Revision。
- Working Draft 与 Document Draft 列表都按 `updated_at,id DESC` 做 Workspace-bound keyset 分页；
  Document Draft 列表只返回 `lifecycle_status=DRAFT`，不复制正文摘要。
- 发布命令先保存不可变 Reservation。新文件使用 `CREATE_ONLY` 和服务端派生的 absence token；
  已发布文件使用 `REPLACE` 和当前 Published Revision content hash。客户端不能选择 target mode、base hash 或 absence token。
- 相同 Workspace/key/request exact replay 返回原 Reservation/Binding；同 key 不同请求拒绝。
  `CLOSED` Reservation 必须已有完全匹配的 Pending Binding；确定性父目录缺失只能进入
  `ABANDONED + WRITEBACK_TARGET_PARENT_NOT_FOUND`，不能伪造其他终态。
- Binding 插入同时核对 Proposal type/idempotency key、Proposal Revision target/mode/base/content、
  Article Revision content/hash 和 Reservation。Reservation 关闭与 Binding 插入在一个事务内完成。
- `proposal_commit` 是发布最终化的外部证明。只有 exact Workspace/Proposal/Revision/path/mode/result hash/Git commit
  同时匹配时，finalizer 才能将 Article Revision、Document pointer 和 Binding 推进为 Published；
  不一致进入受控 Recovery，Proposal 明确拒绝/退修/取消才允许关闭 Binding。
- 数据库拒绝直接插入 Published Revision、伪造 Git commit、伪造 Published Document pointer、
  无终态证明的 Binding 以及 nullable command receipt 形状。
- `00073` 对已执行旧版迁移的数据库前向校验 command receipt 和 publication 终态事实；
  deferred trigger 保证 Binding、Revision 与 Document pointer 只能在同一事务闭合后提交。
- GET 要求 `READ_LOCAL`；create/update/freeze/publish 要求 `WRITE_PROPOSAL`。发布成功只表示 Proposal 已创建，
  不能在 UI/API 中宣称文件或正式知识已经写入。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 非法 UUID/JSON/路径/版本或缺幂等键 | 400；无 Draft/Document/Revision/Reservation 副作用 |
| Workspace/资源绑定不一致 | 404 防枚举 |
| Draft CAS、同 key 不同请求、Revision 已被绑定 | 409；权威状态不变 |
| 发布目标漂移、Proposal binding 漂移、Revision 非最新 | 稳定 409 错误码并回查，不重建 Proposal |
| CREATE_ONLY 目标已存在或 REPLACE base 已变化 | 拒绝写回；不覆盖目标 |
| Commit/Document/Revision 终态不闭合 | `RECOVERY_REQUIRED` 或 consistency violation，不伪造 Published |
| 00069-00073 旧库存在受保护 Authoring 事实 | 前向升级保持迁移版本语义和业务事实不变 |

### 5. Tests Required

- Domain/Application：路径、请求哈希、CAS、每次 Freeze 新 Revision、exact replay/conflict、
  ABANDONED 重放、CREATE_ONLY/REPLACE 派生和 publication recovery。
- PostgreSQL：Workspace/FK、command shape、append-only、并发 Freeze/Publish、Binding/Reservation/Commit proof、
  direct-SQL forge rejection、fresh/repeated Up 和旧版本事实保留。
- Change Control/Git：CREATE_ONLY absence replay、目标竞争、private index、commit/reverse/recovery 与 REPLACE 兼容。
- HTTP/Auth/OpenAPI/Composition：严格 JSON/query/cursor、Capability、列表/detail、稳定错误码与依赖不可用。
- Web：strict decoder、Workspace Query key、autosave conflict、Freeze 后 stale query、发布状态恢复、XSS preview；
  最终浏览器门禁覆盖桌面和 `390x844`。

### 6. Wrong vs Correct

```text
Wrong: autosave 时直接创建 Article Revision，或 Freeze 覆盖旧 Revision。
Correct: Working Draft 可变；每次 Freeze 追加一个不可变 Revision。

Wrong: 看到 Proposal 创建成功就把 Document 标成 Published。
Correct: 等待 exact proposal_commit，再由 finalizer 原子推进 Revision、Document 和 Binding。

Wrong: CREATE_ONLY 在目标存在检查前拒绝 exact replay。
Correct: 先读取持久幂等绑定；只有新命令才证明目标仍不存在。
```
