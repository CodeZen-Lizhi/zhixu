# Authoring SQL、Schema 与事务研究

## Schema owner

### `00069_document_draft_authoring.sql`

- `authoring.working_draft`：Workspace-bound mutable aggregate；version 从 1 逐次 +1，Document 绑定后不可换绑；active list 索引 `(workspace_id,updated_at DESC,id DESC)`。
- `authoring.working_draft_command`：CREATE/UPDATE/FREEZE/PUBLISH immutable receipt；主键 `(workspace_id,idempotency_key)`，完整 response identity 受 FK/shape trigger 保护。
- `document_publication_reservation`：Workspace/key unique、pending Document partial unique、CREATE_ONLY/REPLACE shape。
- `document_publication_binding`：Revision/Proposal Revision unique、nonterminal Document partial unique、status/version/terminal shape。
- trigger 拒绝 immutable mutation、非法 Draft version、无 proof 的 publication transition 和伪造 Published Revision/Document pointer。

### `00071/00072/00073`

- `00071` 将 Proposal type/idempotency key、Proposal Revision path/mode/base/content、Article content/hash、Reservation 和 Binding 绑定为一组精确事实。
- `00072` 只允许确定性 pre-Proposal parent-not-found 进入 ABANDONED，且不能已有 Binding。
- `00073` 前向验证 legacy command/publication facts，并用 deferred constraint trigger 在 commit 时验证 Binding、Revision、Document terminal closure。

### `00075_document_file_history.sql`

- Restore Proposal Revision 增加 owner/version/hash facts。
- `authoring.document_restore_publication` 以 Writeback ID 为主键，Proposal Commit/Article Revision unique，Revision FK deferred；insert 后 append-only。
- trigger 要求 exact restore Proposal/Revision/Commit，并只允许该 bridge 驱动 published Restore Revision 与 Document pointer。
- `idx_authoring_publication_history_git(workspace_id,document_id,target_path,git_commit) WHERE git_commit IS NOT NULL` 支撑历史映射。

本迁移不修改以上 migration、constraint、trigger 或 index。

## 锁与事务矩阵

| Path | Isolation | 固定锁/顺序 | 原子事实 |
| --- | --- | --- | --- |
| Create | default | advisory command lock -> receipt | Draft + CREATE receipt |
| Update | default | advisory -> receipt -> Draft FOR UPDATE | Draft CAS + UPDATE receipt |
| Freeze | default | advisory -> receipt -> Draft -> Document -> latest Revision | Document/Revision append + Draft CAS + FREEZE receipt |
| Reserve | default | advisory -> receipt/reservation -> Document -> target/latest Revision -> pending checks | Reservation |
| Abandon | default | advisory -> Document/Revision -> Reservation -> Binding | ABANDONED terminal proof |
| Complete | default | advisory -> receipt -> Document/Revision -> Reservation -> Binding/Proposal FOR SHARE | Binding + Reservation CLOSED + PUBLISH receipt |
| Reconcile | per item default | Document -> Revision -> Binding; previous Revision when replacing | Revision/Document/Binding terminal closure |
| Detail/Overview | Repeatable Read, Read Only | snapshot reads | internally consistent projection |
| Restore | default | restore advisory -> Proposal -> Document -> current/latest Revision | restore bridge + Revision + Document pointer |

GORM 必须使用 Foundation TransactionOptions 匹配这些值。callback 成功才 commit；任何 validation、RowsAffected、trigger、cancel 或 commit error 都 rollback。

## SQL shape and carriers

- PostgreSQL-specific `pg_advisory_xact_lock`、`FOR UPDATE`、`FOR SHARE OF`、CTE、`RETURNING`、`NULLIF(?,'')::uuid` 和 deferred constraints 保留 Raw SQL。
- identifier、ordering 和 status literals 固定；Reconcile optional filters 只从 validated IDs 构造固定片段，values 全部参数化。
- keyset 是 `(updated_at,id) < (?,?::uuid)` + DESC + Limit+1；禁止 Offset。
- Revision batch 使用 `unnest(?::uuid[],?::uuid[]) WITH ORDINALITY`；裸 `[]string` 会被 GORM 展开，必须用 `pq.Array`/单值 `driver.Valuer`。
- Raw no-row 使用 `Row().Scan` 或显式 Rows.Next，不用 GORM `Scan(&struct)` 推断 not-found。
- nullable UUID/time 显式 carrier；时间转 UTC，Application-provided time 截断到微秒，更新保留 `GREATEST`。

## Error mapping

legacy 精确分类必须镜像：

- `23505` + constraint 精确映射：
  - `uq_core_document_workspace_path` -> path conflict；
  - `working_draft_command_pkey`、`uq_authoring_reservation_workspace_key` -> idempotency conflict；
  - `uq_authoring_reservation_pending_document`、`uq_authoring_publication_nonterminal_document`、`uq_authoring_publication_revision`、`uq_authoring_publication_proposal_revision`、`document_publication_binding_reservation_id_key` -> publication conflict；
  - `uq_core_article_revision_no`、`uq_authoring_working_draft_document` -> version conflict；
- `23503`：not found；
- `23514/22001/22021`：invalid input；
- `55000`：consistency violation；
- `40001/40P01/55P03/57P01/57P02/57P03/08000/08003/08006`：retryable；
- 其余：dependency unavailable。

GORM path 额外识别 `sql.ErrNoRows`、`gorm.ErrRecordNotFound`、`sql.ErrTxDone`，并保留 `ctx.Err()` 与不同的 `context.Cause(ctx)`。不得扩大 SQLSTATE class，也不得把 query/args/content/path/DSN 写入错误。

## TODO 9 SQL/transaction proof

- 同 key 并发 Create 和并发 Freeze 只能有一个 canonical result；response-loss replay 无重复事实。
- proposal mismatch/trigger failure 必须整事务 rollback，Reservation/Binding/receipt 不得半提交。
- Reconcile 的 CREATE_ONLY/REPLACE、previous supersede、Recovery/Closed/Published 与 deferred closure 完全等价。
- Restore 并发 finalize 恰好一个 append，另一个 exact replay；bridge/Revision/Document commit 或 rollback 一致。
- array/cast/nullable/no-row/SQLSTATE 在 pgx stdlib + GORM 上真实执行；cancel 后 Rows/连接归还且 Pool 可复用。
- EXPLAIN 核对 Draft/Document keyset、latest Revision、publication candidate/history 索引；不得用 ORM preload 或循环查询替换 bounded SQL。

当前没有 `ZHIXU_TEST_DATABASE_URL`，以上只能列为未完成门禁。
