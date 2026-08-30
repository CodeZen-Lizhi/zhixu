# Change Control GORM Migration Design

## 1. Boundary And Compatibility

本 child 使用“legacy pgx 保留 + staged GORM sibling + Final 统一切换”策略：

- `postgres.Repository`、`postgres.DB`、`NewRepository`、`approvaldispatchpostgres.ApprovalDispatchRepository` 与 `NewApprovalDispatchRepository` 原样保留；
- 新增 `postgres.GORMRepository` / `NewGORMRepository(*platformpostgres.Pool, ...eventsapplication.ScopedAppender)`；
- 新增 `approvaldispatchpostgres.GORMApprovalDispatchRepository` / `NewGORMApprovalDispatchRepository(*platformpostgres.Pool, workflowapplication.ScopedRuntimeStarter, foundation.IDGenerator, foundation.Clock, ...eventsapplication.ScopedAppender)`；
- 两个构造都从同一个 Pool 取得 GORM root 与 Unit of Work，caller 不能分别注入或创建第二个 root；
- 多语句写入只使用 `UnitOfWork.Within`，callback 内通过 `platformpostgres.GORMTransaction(scope)` 取得 scoped transaction；scope 不缓存、不逃逸、不自行 commit/rollback；
- 跨 owner 原子写只调用现有 `AppendScoped` / `StartScoped`，不新增 `any` 或具体 Tx Port；
- Graph Candidate Confirm 需要的 owner 能力由本模块提供 `CreateKnowledgeChangeProposalScoped` 与 `GetInitialKnowledgeChangeProposalScoped`；两者只接受 `foundation.TransactionScope` 和 Change Control Domain 值，解包 caller-owned live GORM transaction 后立即使用，不拥有 commit/rollback；
- Foundation scope 当前不携带可校验的 Pool identity，staged constructor 与 Final Composition 必须保证 runtime/event store 和 caller UoW 来自同一 Pool；该限制在 TODO 9 以同一 fixture 证明，不能在本 child 用私有类型断言伪造 affinity；
- TODO 9 前不改 `cmd/**`、不双写、不 fallback、不删 legacy，不修改 migration。

Audit owner 已具备 scoped append，但当前 Change Control PostgreSQL Adapter 的审计由 Application/Tools 层完成，不直接参与本 Repository UoW。本 child 不新增重复 Audit event；Final 仅验证既有审计链在切换后仍完整。

## 2. File Layout

主 Repository staged 文件建议：

- `gorm_core.go`：构造、ready/within、Raw Row/Rows/Exec、防御性 no-row/context/SQLSTATE classifier、接口断言。
- `gorm_model.go`：JSONB `driver.Valuer`/`sql.Scanner`、nullable 值与显式 Proposal/Revision/Approval/Authorization/Writeback persistence records。
- `gorm_proposal.go`：五类 Proposal create/replay、Get/List、Approve、MarkNeedsRevision、downstream builder。
- `gorm_scoped_knowledge_proposal.go`：caller-owned scope 下的 typed Knowledge Proposal create-or-exact-load 与 immutable Revision 1 精确读取；不启动 UoW、不回退 root。
- `gorm_authorization.go`：Workflow context 与 Authorization issue/get/consume/revoke/expiry。
- `gorm_revision.go`、`gorm_revision_fence.go`：receipt/base snapshot/history、AppendRevision、supersede fences。
- `gorm_writeback.go`：cancel safety、Begin/lease/cleanup/create/get/find/checkpoint/publish。

Approval Dispatch staged 文件：

- `gorm_repository.go`：构造、UoW、`DecideAndDispatch`、approved/rejected/replay orchestration。
- `gorm_queries.go`：固定 SQL、scan/no-row/error helper。

只抽取能证明无数据库行为变化的纯 codec/equality/domain helper。legacy pgx SQL 与 constructor 继续编译；TODO 9 只扩展现有 integration 文件，不新增测试文件。

## 3. Schema And Ownership

Schema 继续由历史 migration/trigger 唯一管理，GORM 不拥有 DDL：

| Owner | Relation | Contract |
| --- | --- | --- |
| Change Control | `change_control.proposal` / `proposal_revision` / `approval` | Workspace idempotency、append-only Revision/Approval、status/version/current revision pointer |
| Change Control | `proposal_revision_base_snapshot` / `proposal_revision_lineage` / `proposal_revision_command` / `proposal_revision_dispatch` | immutable snapshot/lineage/receipt/dispatch、deferred closure、response-loss source |
| Change Control | `tool_authorization` | token hash only、DB-time TTL、binding/transition/delete triggers |
| Change Control | `writeback_execution` / `proposal_commit` | dual authorization binding、durable state machine、append-only commit mapping |
| Workflow | `workflow.run` / `node_run` / `node_attempt` / `outbox_event` / River tables | lease/fence、dispatch runtime、reindex outbox；只通过固定 SQL或 scoped Workflow port参与 |
| Events | `ops.server_event` | 通过 scoped Event appender 同事务追加，不由本 adapter 复制 SQL |
| Read-only owners | `core.workspace/document/claim/article_revision`、Artifact/Review/Impact/Knowledge facts | 仅用于 binding/fence；不得由本 child ORM association 写入 |

关键 migration 为 `00004`、`00005`、`00008`、`00009`、`00010`、`00013`、`00015`、`00040`、`00070`、`00075`、`00082`、`00090`。`00090` 修复的 revision snapshot trigger，以及 `00015` 的 reindex completion guard，必须在 TODO 9 真库执行，不能用静态编译替代。

Persistence records 显式列/类型/schema/TableName，不嵌入 `gorm.Model`，不使用 hooks、soft delete、auto timestamps 或 association。JSONB carrier 校验 JSON 后返回 string，避免 pgx stdlib 把 `[]byte` 绑定为 `bytea`；数组只通过 `pq.Array` 作为单个 `driver.Valuer`。

## 4. Transaction And Lock Matrix

不同业务流的锁序不可收敛成一个通用 Repository lock helper：

| Flow | Fixed order |
| --- | --- |
| Proposal create | `INSERT ... ON CONFLICT DO NOTHING RETURNING`; conflict branch locks/compares the Proposal binding, returns an internal rollback sentinel, then loads exact Proposal from root after rollback; inserted branch writes immutable Revision/typed JSON/base snapshot -> commit |
| Downstream proposal create | Proposal inserted branch -> `ops.impact_report FOR UPDATE` -> current report/fingerprint check -> Artifact or Review owner facts `FOR SHARE` -> exact owner-binding compare -> immutable Revision -> commit; conflict branch follows the same rollback-sentinel + root-read replay |
| Restore proposal | Proposal create order + `core.document FOR SHARE` binding fence |
| Approve | Proposal + current Revision `FOR UPDATE` -> exact Approval replay; without Event appender replay returns via rollback sentinel/no commit, with Event appender it exact-appends and commits; new decision writes Approval -> Proposal CAS -> optional `Event.AppendScoped` -> commit |
| CreateAuthorization | DB `CURRENT_TIMESTAMP` -> authorization insert; binding trigger locks Proposal/Revision/Approval then Workflow Run/Node -> conflict replay row `FOR UPDATE` -> exact identity compare -> commit |
| GetAuthorization | DB `CURRENT_TIMESTAMP` -> authorization by key, then token fallback, `FOR UPDATE` -> optional issued-to-expired update -> commit |
| ConsumeAuthorization | DB `CURRENT_TIMESTAMP` -> authorization by key, then token fallback, `FOR UPDATE` -> exact consumed replay/terminal/expiry branches -> for issued rows Proposal/Revision/Approval then Workflow Run/Node revalidation -> consumed CAS -> commit |
| RevokeAuthorization | authorization `FOR UPDATE` -> terminal checks -> issued-to-revoked update with `CURRENT_TIMESTAMP` -> commit |
| AppendRevision | advisory receipt key -> receipt `FOR UPDATE` -> Authorization rows `ORDER BY id FOR UPDATE` -> Proposal/source Revision -> Approval/dispatch/Workflow/Execution fences -> revoke issued auth -> snapshot/revision/lineage -> Proposal pointer CAS -> receipt -> Event |
| BeginWriteback | DB clock -> two Authorization IDs sorted + `FOR UPDATE` -> Proposal/Revision/Approval -> Workflow Run/Node lease -> exact Execution `FOR UPDATE` -> consume both -> insert Execution -> Proposal approved-to-applying CAS |
| CreateWritebackExecution | exact Execution lookup -> DB clock -> `INSERT ... ON CONFLICT DO NOTHING RETURNING`; the insert trigger validates and locks Authorization/Proposal bindings -> Proposal approved-to-applying CAS -> commit |
| Checkpoint | unlocked immutable `execution.proposal_id` lookup -> Proposal `FOR UPDATE` -> Execution `FOR UPDATE` -> DB clock -> Execution CAS -> optional Proposal CAS -> commit |
| Publish | unlocked immutable `execution.proposal_id` lookup -> Proposal `FOR UPDATE` -> Execution `FOR UPDATE` -> exact Proposal Commit -> exact Workflow Outbox -> Execution verifying CAS -> Proposal verifying CAS -> commit |
| Cancellation guard | caller-owned live scope -> all Execution rows for node -> fail closed unless every status is cancellation-safe |
| Approval dispatch approved | Proposal/Revision `FOR UPDATE` -> exact Approval -> safety binding -> optional Approval insert -> `Workflow.StartScoped` -> revision dispatch -> Proposal CAS -> commit |
| Approval dispatch rejected | Proposal/Revision + exact Approval -> Approval/Proposal CAS -> optional `Event.AppendScoped` -> commit; no Workflow/River facts |

Revision 的 Authorization-before-Proposal 顺序与 Atomic Begin 一致；Create 的触发器锁序以及 Checkpoint/Publish 的 Proposal-before-Execution 顺序都必须保持。任何 ORM association、root DB call 或独立 scoped collaborator transaction 都会破坏这些不变量。

### Scoped Knowledge Proposal owner boundary

- Graph 先锁 Candidate，随后才调用本模块 scoped 方法；本方法不得预先取得 Proposal 锁或改变 Graph 的 Candidate-before-Proposal 顺序。
- create 使用与 root Knowledge Proposal 相同的 canonical risk、typed payload、request hash、JSONB 和 `INSERT ... ON CONFLICT DO NOTHING RETURNING` 规则。新建时 Proposal 与 immutable Revision 1 同一 caller scope 写入，并设置 `current_revision_id=revision.id`。
- 冲突时在同一 caller scope 锁定 `(workspace_id,idempotency_key)` Proposal，严格比较 type/risk/request hash，再读取并锁定 `revision_no=1`。它返回 exact aggregate，不使用 root replay，也不通过 sentinel 要求调用方回滚。
- initial-read 按 `workspace_id + proposal_id + proposal_type=knowledge_change + revision_no=1` 锁定 Proposal/Revision，严格扫描 typed JSON 和领域不变量；允许历史 `current_revision_id IS NULL`，但非空指针若不指向 Revision 1 仍返回 immutable Revision 1，不能改读可变 current Revision。
- nil context、nil/foreign type/stale scope fail closed；Foundation 暂不能识别另一个 Pool 的 active scope，因此同 Pool affinity 仍由 Composition/TODO 9 保证。

## 5. Idempotency, Time And Recovery

- Proposal create 继续按 `(workspace_id,idempotency_key)` 精确比较 type/risk/request hash 和 typed Revision binding；冲突不能返回宽松 replay。
- Proposal create conflict replay 必须让 `UnitOfWork.Within` 回滚后再从 root `GetProposal`；内部 rollback sentinel 只用于控制事务，不能泄漏到调用方。Approve 的无 Event exact replay同样回滚返回，有 Event replay才因 scoped append提交。
- Revision command receipt 是 AppendRevision 的唯一 durable response-loss source；receipt replay 必须早于可变状态判断，事务返回未知后只从 root 精确查询 receipt。
- Authorization 生命周期和 Writeback lease/cleanup/checkpoint/publish 使用 PostgreSQL `CURRENT_TIMESTAMP`，不使用 GORM `NowFunc` 或 Application Clock。
- BeginWriteback exact replay 必须在首次消费授权前，且只允许完整 Execution identity；已 consumed 但无 exact Execution 必须 fail closed。
- Approval Dispatch 完整 replay不再读取文件/Git safety input；Workflow `StartScoped` 必须返回 exact duplicate job/binding。
- commit-response-loss 按每个方法的 legacy 恢复协议处理，禁止统一成“重复同一命令即成功”：具备 durable receipt/binding 的 Proposal create、Approval/Authorization、AppendRevision、Begin/Create/Publish/Cleanup Writeback 与 Approval Dispatch，首次返回稳定 commit failure，后续只按其既有 exact binding 重放；Checkpoint 不接受已提交的同版本/同 transition 重放，必须由调用方先 `GetWritebackExecution` 再通过 Application `Resume` 从 durable checkpoint 继续。除非现有端口已定义即时 recovery，不在迁移中扩大成功语义。

## 6. SQL, JSON And Read Contracts

- `FOR UPDATE/SHARE`、advisory lock、CTE、`ON CONFLICT ... RETURNING`、CAS、array、JSONB、trigger-sensitive writes 和 cross-schema reads 全部使用固定 Raw/Exec SQL。
- GORM SQL 使用 `?` placeholders 和显式 `::uuid`/`::jsonb`/`::text[]` casts；外部值只作为参数，表/列/order/status仅来自包内固定文本。
- Begin authorization ID set 的 `ANY(?::text[])` 使用 `pq.Array([]string)`；裸 slice 禁止交给 GORM 展开。
- Proposal/List/history 保持 Workspace predicate、稳定 tuple keyset、limit+1 与 rows Close/Err；扫描/JSON/domain 校验失败不返回部分结果。
- 单行 no-row 使用 `Raw(...).Row().Scan` 或显式 Rows Next；不依赖 `Raw().Scan` 的零行行为。
- typed Proposal、receipt 和 outbox payload 继续严格 JSON decode/canonical/binding 检查，禁止保存 Credential、正文副本、绝对路径、lock token 或额外 Runtime payload 字段。
- scoped initial-read 复用同一 Proposal/Revision scanner 与 typed JSON 校验；不得为 Graph 返回宽松的局部 record，也不得把 mutable current Revision 当作 response-loss snapshot。

## 7. Error, Context And Logging

分类优先级：

1. 现有 `foundation.Error` 原样保留；
2. `ctx.Err()` 与 distinct `context.Cause(ctx)`，保留 canceled/deadline sentinel 和自定义 cause；
3. `sql.ErrTxDone`、`sql.ErrNoRows`、`gorm.ErrRecordNotFound`；
4. `*pgconn.PgError` 的 SQLSTATE/constraint；
5. 脱敏的 dependency unavailable。

通用 Proposal classifier 保持 `23505`、`23503/23514`、`40001/40P01/57P01/08000/08003/08006` 的 legacy 分类；Authorization 保留 constraint-aware identity conflict；Writeback/Dispatch 另保留 `55000` 与 `55P03` 等现有语义。原始 PgError/cancel cause 必须可通过 `errors.Is/As` 获取，但公开错误不得包含 SQL、参数、Credential/token hash、正文、JSON payload、DSN、绝对路径或 lock token。

## 8. Verification Design

### Static stage

- 运行 Change Control、Approval Dispatch 和 Application 局部 test/race/vet；integration compile-only；API/Worker compile-only；vendor list、module verify、Trellis validate、gofmt 和 diff-check。
- 静态确认 production 仍只构造 legacy Repository/Dispatch；无 selector、双写、AutoMigrate、独立 `gorm.Open`、root `.Transaction`、动态用户 SQL或敏感日志。
- 使用 Go Review、SQL Review 和独立 Trellis Check；修复明确缺陷并写入 `research/static-validation.md`。

### TODO 9 PostgreSQL stage

只扩展现有 integration fixtures。每个 legacy/GORM case 使用独立 migrated disposable database；migration pool 关闭后，由一个 `platformpostgres.Open` Pool 提供全部 staged 依赖。GORM fixture 按以下顺序构造，禁止 no-op fence、第二 Pool 或独立 `gorm.Open`：

1. `auditpostgres.NewGORMStore(pool)`，以及由固定测试密钥构造的 `modelcrypto.NewSealer`；
2. `modelsettingspostgres.NewGORMRepository(pool, WithGORMSecretSealer(...), WithGORMAuditAppender(auditStore))`，其 `CheckEnqueue` 作为 scoped River enqueue fence；
3. `eventspostgres.NewGORMStore(pool)`；
4. Change Control `NewGORMRepository(pool, eventStore)`，并将其 scoped cancellation guard 放入 Workflow hooks；
5. `workflowpostgres.NewGORMRuntimeRepositoryWithHooks(pool, riverOptions, modelSettingsRepository, hooks)`，由构造器建立同 Pool 的 scoped River producer；
6. `NewGORMApprovalDispatchRepository(pool, workflowRuntime, ids, clock, eventStore)`。

legacy 与 GORM 子测试必须使用不同数据库，teardown 只调用各自 platform Pool 的 `Close` 后删除该隔离数据库。

必须覆盖：

- 五类 Proposal create/replay/conflict（冲突事务 rollback 后 root read）、Downstream report/owner 并发漂移、Restore document fence、List/history keyset与严格 JSON；
- scoped Knowledge Proposal 在同一外部 UoW 内的 create/exact replay/rollback、Candidate-before-Proposal 锁序、new pointer 写入、legacy NULL pointer + Revision 1 replay、nil/foreign/stale scope；
- Approval exact replay（分别覆盖 nil Event 的 rollback/no commit 与有 Event 的 exact append/commit）、decision race/Event rollback；Authorization issue/get/expire/consume/revoke与 DB time；
- Revision same-key response loss、advisory concurrency、Authorization-to-Proposal lock order、supersede closure、00090 trigger；
- BeginWriteback双授权恰一次、rollback、consumed-without-execution、lease、checkpoint/publish/cleanup、00015 reindex completion guard、deadlock regression；Checkpoint commit response-loss 后必须经 Get+Application Resume 恢复，不能以原命令 exact replay；
- Approval Dispatch approved/rejected/concurrent/runtime-or-event failure、Workflow/River exact binding和commit-response-loss后续 replay；
- context cancel/deadline、SQLSTATE/trigger、Rows/connection release、关键 EXPLAIN/索引与无 N+1。

GORM response-loss 测试在各 owner 的同包现有 integration 文件中替换被测对象自身的 UnitOfWork：主 Repository 用例包装 `GORMRepository.unitOfWork`，Dispatch 用例包装 `GORMApprovalDispatchRepository.unitOfWork`。包装器先调用真实 `Within` 完成 commit，再返回注入错误；Workflow `StartScoped` 不拥有该 commit，禁止错误地包装 Workflow runtime 的 UoW。首次调用必须返回 legacy-compatible commit failure。随后：具备 receipt/binding 的方法用同一 Pool 上未包装实例 exact replay；Checkpoint 用 `GetWritebackExecution` + Application `Resume` 继续；Dispatch 核验完整 Approval/Revision Dispatch/Run/Node/Outbox/River Job binding。不得复用 pgx Begin wrapper或 sleep。

## 9. Rollback And Final Handoff

- TODO 9 前回滚只删除本 child staged GORM 文件与本 task 工件的实现证据，legacy production/Schema不变。
- TODO 9 后、Final 前仍保留 pgx wiring作为回退；Final统一构造同 Pool Events、Model Settings fence、Workflow runtime、Change Control与Approval Dispatch，并删除 legacy。
- Knowledge/Graph/Retrieval/Artifact 等消费者在各自 child迁移 scoped contract；本 child不跨 owner改生产调用方。
- Graph child只消费本 owner能力并自行拥有 Candidate UoW；本 child不导入 Graph 包、不修改 Graph adapter或生产构造。
- TODO 3 Atlas 前置已于 2026-08-30 交付，不再阻断 Final schema/composition 收口；本 child 的真实 PostgreSQL、生产 Composition 与其他父任务门禁保持不变。
