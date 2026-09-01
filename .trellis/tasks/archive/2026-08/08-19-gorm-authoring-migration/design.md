# Authoring Repository GORM 迁移设计

## 1. 目标与阶段边界

Authoring 同时拥有可变 Draft、append-only Revision、immutable command receipt、跨 Change Control 的 publication proof，以及 Restore 的恢复闭环，不是普通 CRUD。本 child 采用“共享平台事务 + 参数化 GORM Raw SQL + 显式 scanner”的 staged 路径，不在 ORM 迁移中重写状态机。

TODO 9 真实 PostgreSQL 门禁通过前：

- 保留 legacy `Repository`、`NewRepository(*pgxpool.Pool)` 和全部 pgx 路径；
- 不改 API/Worker 的两个生产构造点及三个 Worker consumer；
- 不改 migration、trigger、index、HTTP、Application 或 Domain；
- 不完成/归档任务，不新增 selector、双写、fallback 或第二 pool。

## 2. Schema 事实源

Authoring 当前行为由五个 migration 共同约束，不能只看最初的 `00069-00073`：

| Migration | Authoring 事实 |
| --- | --- |
| `00069_document_draft_authoring.sql` | `working_draft`、immutable command receipt、publication reservation/binding、基础索引与 mutation trigger |
| `00071_document_draft_authoring_binding_hardening.sql` | Proposal/Revision/Reservation/Binding 的跨 owner 精确绑定和 publication mutation guard |
| `00072_document_publication_reservation_abandonment.sql` | 受控 `ABANDONED` 状态、error/abandoned time 与 terminal proof |
| `00073_authoring_publication_terminal_closure.sql` | command shape 前向校验与 Binding/Revision/Document deferred terminal closure |
| `00075_document_file_history.sql` | Restore Proposal 字段、append-only `document_restore_publication`、Restore Revision/Document mutation proof 与 history 索引 |

GORM record 只做显式列映射和 carrier，不拥有 Schema。禁止 `AutoMigrate`、`Migrator`、`gorm.Model`、soft delete、association、Hook 和自动 timestamp。

## 3. 接口与 staged 构造

新增：

```go
NewGORMRepository(*platformpostgres.Pool) (*GORMRepository, error)
```

`GORMRepository` 从同一 Pool 获取共享 `*gorm.DB` root 与 `foundation.UnitOfWork`，禁止接收可错配的独立 root/UoW，也禁止自行 `gorm.Open`、`sql.Open` 或建立 pgx pool。

静态实现：

- `authoring/application.Repository`；
- `authoring/application.ArticleRevisionSearchRepository`；
- Change Control finalizer 的 `ReconcilePublications`、`ValidateRestoreWriteback`、`FinalizeRestorePublication` 所需接口。

公开 Application/Domain 不新增 GORM、`database/sql`、pgx 或 `any`。Authoring 的事务全部由 Repository 自己拥有，因此本 child 不新增 caller-owned transaction Port。

## 4. GORM 数据访问结构

建议按现有 owner 文件拆分 staged 实现：

- `gorm_repository.go`：构造、Draft create/get/update/list/freeze；
- `gorm_publication.go`：reserve/abandon/complete/reconcile；
- `gorm_read.go`：search/detail/batch/overview；
- `gorm_restore_publication.go`：Restore owner check/finalize；
- `gorm_model.go`：私有 record、nullable carrier、row scanner 和 Domain 映射；
- `gorm_queries.go`：固定 `?` placeholder SQL 与受限可选片段。

legacy 路径保持可编译和行为不变。只抽取不依赖数据库实现的 validation、binding、hash、UTC 和 result mapper；共享 row scanner 可收窄到私有 `Scan(...any) error` 接口。不得引入一个弱类型 `any` DB 抽象，也不得让 staged 方法 fallback 到 legacy root。

所有 GORM Raw 查询使用 `WithContext(ctx)`，先检查 statement/row/rows handle；多行结果必须 Close 并检查 `Rows.Err()`。固定 SQL 的动态部分仅允许既有 `FOR UPDATE`、已验证 Reconcile filter 和 Limit placeholder，不接受 caller 提供 identifier。

## 5. Draft 命令事务

Create、Update、Freeze 使用：

```text
UnitOfWork.Within(ctx, TransactionOptions{}, callback)
  -> GORMTransaction(scope)
  -> advisory lock(workspace:idempotency key)
  -> immutable receipt lookup / exact replay
  -> aggregate locks and CAS
  -> aggregate/Document/Revision writes
  -> receipt insert
  -> callback success commits; any error/cancel/panic rolls back
```

Create 先锁 key 并查回执；新命令插入空 Draft v1，再写 CREATE receipt。response-loss 后只从 receipt 恢复同一 Draft。

Update 回执 replay 不读取当前 Draft；它仅使用 immutable receipt identity/version/timestamp 与已校验请求中的 title/path/body 重建原响应。新命令锁 Draft，要求 `version=expected`，CAS 更新 `version+1`，再写 UPDATE receipt。`RowsAffected()!=1` 保持 version conflict。

Freeze 先锁 Draft，再按既有顺序锁/创建 Document 与 latest Revision：首次创建 DRAFT Document；后续锁已绑定 Document；锁 latest Revision 并计算 parent/revision number；插入 immutable Revision；CAS 更新 Draft；写完整 FREEZE receipt；replay 通过 receipt identity/hash 精确回读 Revision content。不得用 GORM association 或 Save 拆分/重排步骤。

## 6. Publication 事务与锁序

Reserve/Abandon/Complete 固定 publish command advisory lock `workspaceID:idempotencyKey`，并保持 Document -> target Revision -> Reservation/Binding 的既有锁序。Reserve 先检查 receipt/reservation/latest Revision/nonterminal uniqueness；Abandon 只允许 PENDING -> ABANDONED 且没有 Binding；Complete 在同一事务验证 Proposal snapshot，插入 PENDING Binding、关闭 Reservation、写 PUBLISH receipt。

Reconcile 外层按 Workspace、可选 Document/Proposal 和 limit<=100 有界列出 ID；随后每个 ID 独立事务：先读 identity，再锁 Document、Revision、Binding；验证 Reservation/Proposal；Proposal 拒绝类终态只允许 Closed；缺 commit 保持不变；mismatch 进入 Recovery；exact commit 才能 supersede previous、publish Revision、CAS Document、publish Binding。逐 Binding 事务保留部分进度和故障隔离，不能改成一个大事务。

## 7. Restore Publication

`ValidateRestoreWriteback` 精确核对 Workspace、Document version/path/lifecycle 和 current published content hash。

`FinalizeRestorePublication` 使用默认隔离 UoW，并保持：restore advisory lock；Proposal `FOR UPDATE`；exact Proposal Revision + commit facts；稳定 UUIDv5 Revision ID；append-only bridge `ON CONFLICT DO NOTHING` 后完整 recheck；existing Revision replay；Document/current/latest Revision locks；supersede old、insert published Restore Revision、CAS Document pointer；最后由 deferred FK/trigger 闭合。Restore 不能并入普通 publication helper，也不能跳过 `00075`。

## 8. 读取、array 与一致快照

- Get/Search/List/ListDocuments 使用共享 root 参数化 Raw；Search 保持 bounded lateral latest-revision query 与 limit<=25。
- List 保持 Workspace、`(updated_at,id) < (?,?::uuid)`、DESC、Limit+1 和原 cursor。
- GetDocumentDetail/GetOverview 使用 `TransactionIsolationRepeatableRead + ReadOnly:true`；Overview 三段查询在同一 scope。
- GetArticleRevisions 使用一个 `unnest(?::uuid[],?::uuid[]) WITH ORDINALITY`；两个数组用 `pq.Array` 等单值 `driver.Valuer`，扫描后复核 ordinality、重复、Workspace、identity 和完整 cardinality。
- nullable UUID/time 使用 `sql.Null*` 或 pointer；完整 Aggregate 读取沿用既有 Domain Validate，Search 与 Overview summary 保持 legacy 的投影校验粒度，不能单方面收紧或放宽。

## 9. 时间、错误、取消与安全

- record 时间 UTC + 微秒规范化，`GREATEST(updated_at, ?)` 保持单调；禁止自动时间或 `NowFunc` 替代 Application clock。
- no-row 识别 pgx、sql 和 GORM；Raw 单行用 guarded `Row().Scan`，不能用无行不报错的 `Scan(&struct)`。
- 精确 SQLSTATE/constraint 分类沿用 legacy，不扩大 retryable class。
- GORM classifier 接收 context，以 `errors.Join(ctx.Err(), context.Cause(ctx))` 保留 sentinel 与自定义 cause，并处理 `sql.ErrTxDone`、begin/commit/rollback。
- unknown error 不含 SQL、args、正文、path、idempotency key、DSN、Secret 或绝对路径；沿用平台安全 logger。

## 10. TODO 9、发布与回滚

TODO 9 原位扩展现有三个 integration test 文件，不新增测试文件。fixture 每个 legacy/GORM 子用例创建独立数据库；迁移后以一个完整 `platformpostgres.Pool` 同时提供 DB/GORM/UoW。legacy 使用 `NewRepository(database.DB())`，GORM 使用 `NewGORMRepository(database)`；seed/断言仍用同一个 `DB()`，不得自建 GORM root。

必须比较 Draft lifecycle/CAS/replay/并发/rollback，keyset/Search/batch/repeatable snapshot，Reservation/Abandon/Complete/Reconcile 和 CREATE_ONLY/REPLACE，Restore exact proof/并发 replay，真实 FK/CHECK/trigger/SQLSTATE，cancel/deadline/Rows/连接释放，以及关键查询的目标数据量 EXPLAIN。

Schema-only migration/hardening tests无需双实现。TODO 9 前回滚只删除 staged GORM 文件并还原纯 helper 抽取，生产与 Schema 不变；Final 切换失败由 Final 恢复 legacy Composition，禁止双写或 fallback。
