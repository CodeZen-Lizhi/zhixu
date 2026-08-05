# Document Draft 主动创作：技术设计

## Module Boundary

新增 `internal/authoring`，内部划分 domain、application、adapter/postgres 与 http。Authoring 拥有可变 Working Draft、Document/Article Revision 冻结规则和 publication binding；Change Control 继续独占审批、授权、文件写入和 Git Commit。

## Persistent Model

新增 `authoring.working_draft`：

- `id/workspace_id/document_id nullable/title/target_path/body/status/version/created_at/updated_at`。
- 空标题、路径和正文允许存在于 EDITING Working Draft；版本从 1 开始并以 CAS 更新。
- `document_id` 在首次冻结时绑定，之后不可切换到其他 Document。

新增 `authoring.working_draft_command` 记录 Workspace-scoped idempotency key、request hash 和响应身份；create/freeze/publish 精确重放。

新增 append-only `authoring.document_publication_binding`：

- 绑定 Workspace、Document、Article Revision、Proposal、Proposal Revision、目标路径、content hash、创建时间。
- 同一 Article Revision 只能绑定一个 Proposal；同一 Proposal Revision 只能发布一个 Article Revision。
- finalizer 仅允许把绑定状态从 PENDING 推进到 PUBLISHED 或 RECOVERY_REQUIRED，不改写冻结字段。

新增 `authoring.document_publication_reservation`：

- 在跨 Change Control 创建 Proposal 前独立提交，冻结 Workspace、Document、Article Revision、幂等键和请求哈希。
- `PENDING` 表示外部结果仍需恢复，`CLOSED` 必须已有完全匹配的 immutable binding，`ABANDONED` 只表示已证明 Proposal 未创建的确定性失败。
- 相同 key 精确重放返回原 binding 或 ABANDONED error；未知结果不得释放 reservation，确定性父目录缺失不得永久占用 Document path。

复用现有 `core.document` 与 `core.article_revision`，不放宽其非空 title/path/content 约束。迁移采用 additive `00069_document_draft_authoring.sql`；存在 Working Draft、Revision 或 binding 时 Down fail closed。

## Application Flow

```text
Create Working Draft
  -> CAS autosave title/path/body
  -> Freeze(expected draft version, idempotency key)
       -> validate canonical .md path and non-empty content
       -> atomic Document(DRAFT) + ArticleRevision(DRAFT) + draft binding
  -> Publish(frozen revision, idempotency key)
       -> reserve publication identity
       -> derive CREATE_ONLY absence proof or current REPLACE base hash
       -> create existing file_patch Proposal from frozen content
       -> atomically close reservation and persist immutable publication binding
  -> existing Approval / Safe Writeback / Git Commit
  -> idempotent Authoring finalizer
       -> ArticleRevision(PUBLISHED + git_commit)
       -> Document(PUBLISHED + current_published_revision_id)
```

Authoring 不读取前端传入的 Proposal 正文或 base hash作为权威发布内容；Publish command 只携带 Workspace、Document、Revision 和幂等身份，服务端从 Revision 与受控目标读取重建 Proposal。

新建文章使用 Change Control 的显式 `CREATE_ONLY` 目标模式，而不是把空文件哈希冒充为“路径不存在”。该模式必须作为 Proposal Revision、Writeback Execution 和 Git intent 的不可变绑定持久化；既有 Proposal 默认且只能走 `REPLACE`，旧链路语义不变。

## HTTP Contract

- `POST /api/v1/workspaces/{workspace_id}/authoring/working-drafts`
- `GET /api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}`
- `PUT /api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}`
- `POST /api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}/freeze`
- `POST /api/v1/workspaces/{workspace_id}/documents/{document_id}/revisions/{revision_id}/publish-proposals`
- `GET /api/v1/workspaces/{workspace_id}/authoring/overview`
- `GET /api/v1/workspaces/{workspace_id}/documents/{document_id}`

JSON 使用 strict decoder，未知字段、重复字段、超限内容和多重 Idempotency-Key fail closed。Working Draft update 必须携带 expected_version。所有读写按 Workspace 复合身份查询，跨 Workspace 返回不可区分的 not found。

## Change Control Bridge

发布复用 `application.Service.CreateProposal` 的 `file_patch` 类型。Authoring bridge 从冻结 Revision 生成：

- target path：Document canonical path。
- content：Article Revision content。
- target mode：首次发布新 Document 固定为 `CREATE_ONLY`；后续已发布 Document 固定为 `REPLACE`。
- base hash：`REPLACE` 由受控 Workspace Reader 返回；`CREATE_ONLY` 使用版本化 absence token，并同时要求文件系统路径与批准 Git HEAD 均不存在，token 不能被解释为空文件哈希。
- evidence summary：明确标识手写 Document Revision 与内容哈希。
- risk/rollback：固定、受控文本，不接受前端绕过。

Writeback publish 事务完成后必须触发 Authoring finalizer。优先在 Change Control 的 owner event/outbox 上消费完成事实，避免把 Authoring SQL写入通用 Change Control domain。若 finalizer 未完成，overview 显示“发布状态待恢复”，并允许后台/显式恢复；不回滚已经成功的 Git Commit。

当前仓库没有通用 owner hook，因此本期 Authoring Reconciler 以不可变 `change_control.proposal_commit` 为权威输入：按 exact Proposal/Proposal Revision 扫描 `PENDING|RECOVERY_REQUIRED` binding，在固定锁序事务中复核 Workspace、Document、Article Revision、路径、结果哈希和 Git Commit 后完成发布。暂时性失败保留 `PENDING`；可诊断的一致性冲突进入 `RECOVERY_REQUIRED`，修复后允许幂等推进为 `PUBLISHED`。不得以 outbox `published_at` 或浏览器状态作为发布事实。

`CREATE_ONLY` Safe Writeback 约束：

- 以 canonical Workspace + target path 派生持久路径锁；批准快照和应用前都复核路径未跟踪、工作树目标不存在、父链为同设备普通目录。
- 准备同目录受控临时文件并 fsync；发布使用 no-replace 原子创建语义，目标在任一时刻出现即冲突，绝不覆盖。
- Git intent 记录新增文件 mode/blob/diff；提交只允许 `A` 目标，使用既有受控 index、`commit-tree` 与 `update-ref` CAS，不调用普通 `git add/commit`。
- `CREATE_ONLY` 的 absence token 只绑定 Proposal/Writeback 的 `base_version` / `base_hash`；批准 Git tree 中没有 base blob，因此 Git `BaseBlobID` 必须为空，不能把 absence token 或空文件哈希伪装为 Git object ID。
- 失败补偿只删除仍与本 Execution identity、hash、mode 完全一致的新增目标；无法证明时进入人工恢复并保留证据。

## Web Boundary

新增 `web/src/api/authoring.ts` 作为唯一 wire decoder；Query keys 全部包含 Workspace 与 Draft/Document 身份。

新增：

- `features/authoring/AuthoringPage.tsx`：两条创作路径和真实 overview 列表。
- `features/authoring/NewDocumentPage.tsx`：表单、Monaco Editor、单向 Markdown preview、保存/冲突/发布状态。
- `features/authoring/query-keys.ts`、`queries.ts` 与组件测试。

Monaco worker 初始化抽到 shared owner，Editor URI 包含 Workspace/Draft；卸载后释放 detached model。Markdown renderer 明确禁用/转义原始 HTML，并只允许 `http/https/mailto` 等受控协议。页面暂存尚未确认送达的编辑缓冲仅驻留内存；服务端确认后清除。

## Compatibility And Rollback

新能力使用独立 handler/readiness。关闭 Authoring 后现有 Proposal、Artifact、Workspace 文件和已发布 Document 仍可读取；未发布 Working Draft 保留。旧 `/proposals`、`/workflows`、`/artifacts` 路由不变。

## Risks

- 自动保存竞态：expected version CAS + 明确冲突，不做 last-write-wins。
- 发布双写：immutable binding + idempotent finalizer + recovery 状态。
- 新文件竞态：显式 `CREATE_ONLY` + 路径锁 + no-replace 创建 + Git HEAD/工作树双重复核。
- 路径逃逸：Domain canonicalization 与 Workspace reader 双层校验。
- XSS：受控 renderer、原始 HTML 转义、危险协议拒绝。
- Revision 爆炸：自动保存只更新 Working Draft，只有显式 freeze 新增 Revision。
