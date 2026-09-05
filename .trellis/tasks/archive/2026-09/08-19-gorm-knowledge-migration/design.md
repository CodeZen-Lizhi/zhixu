# Knowledge GORM Migration Design

## 1. Boundary And Compatibility

本 child 使用“legacy pgx 保留 + staged GORM sibling + Final 统一切换”策略：

- 保留 `postgres.Repository`、`postgres.DB`、`NewRepository`、`ApprovedRelationApplyRepository` 与现有 Application/Domain 端口；
- 新增 `postgres.GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`；
- 新增 `postgres.GORMApprovedRelationApplyRepository` / `NewGORMApprovedRelationApplyRepository(*platformpostgres.Pool, foundation.IDGenerator, foundation.Clock, ...eventsapplication.ScopedAppender)`；
- 两个构造只从同一 Pool 取得 GORM root 与 Unit of Work，caller 不能分别注入或创建第二 root；
- 多语句写入只使用 `UnitOfWork.Within`，callback 内通过 `platformpostgres.GORMTransaction(scope)` 取得 scoped transaction；scope 不缓存、不逃逸、不自行 commit/rollback；
- Foundation scope 当前不携带可校验的 Pool identity。staged constructor 与 Final Composition 必须保证 scoped Events/Audit 和 caller UoW 来自同一 Pool；active foreign-Pool scope 无法在本 child 识别，TODO 9 fixture 必须固定同源；
- TODO 9 前不改 `cmd/**`、不双写、不 fallback、不删 legacy、不修改 migration。

Impact 的过渡契约保持现有 Service/Repository 调用：

- legacy `ImpactAuditPort.RecordImpactAnalysisTx(ctx, any, record)` 继续供 pgx Repository 使用；
- 新增补充接口 `ScopedImpactAuditPort.RecordImpactAnalysisScoped(ctx, foundation.TransactionScope, record)` 与 `ScopedImpactRepository.SaveImpactReportWithScopedAudit(...)`；
- 现有 `adapter/audit.ImpactRecorder` 同时实现两者，scoped 方法调用 `auditapplication.Recorder.RecordScoped`；
- 新增 `NewScopedImpactServiceWithAudit(ScopedImpactRepository, ..., ScopedImpactAuditPort)`，让GORM Composition在编译期选择 scoped能力；现有`NewImpactServiceWithAudit`与legacy方法完整保留。不得让GORM Service运行时退回legacy `any`方法，也不得单独提交Audit。

Approved Relation Apply 不调用会自行开事务的 Change Control GORM Repository。它继续用固定 SQL锁定和推进 Change Control 事实，再用 scoped Event appender 追加事件，从而保留当前跨 Schema 单事务协议。Proposal/Revision 的定位、加锁读取与状态 CAS 必须绑定 `proposal.current_revision_id=revision.id`；只保留与 Change Control staged adapter 一致的 `current_revision_id IS NULL` 扩容期兼容，禁止旧 revision 在指针推进后继续 Approval/Apply。

## 2. File Layout

- `gorm_core.go`：构造、ready/within、callback-vs-commit stage、Raw Row/Rows/Exec、防御性 no-row/context/SQLSTATE classifier、接口断言。
- `gorm_model.go`：JSONB string `driver.Valuer`/`sql.Scanner`、nullable 值、显式 persistence records。
- `gorm_commands.go`：receipt、Topic 与 Claim command/advisory locks。
- `gorm_relation.go`：Relation command、端点/evidence lock 与 fingerprint replay。
- `gorm_conflict.go`：Conflict command/member locks 与 Claim disputed CAS。
- `gorm_read.go`：三类 bounded aggregate read，RepeatableRead + ReadOnly snapshot。
- `gorm_evidence.go`：eligibility 与 evidence topic read。
- `gorm_timeline.go`：event read/append、Impact read/save、scoped Audit。
- `gorm_timeline_projection.go`：claim/project/poison CAS。
- `gorm_relation_apply.go`：单独的 `GORMApprovedRelationApplyRepository` 与跨 owner orchestration。

只抽取可证明不改变数据库行为的纯 validation/codec/equality/scanner helper。不得创建 pgx-shaped generic GORM DB bridge；GORM 文件不得 import pgx，仅 legacy 文件保留 pgx。

## 3. Schema And Ownership

Schema 继续由历史 migration/trigger 管理，GORM 不拥有 DDL：

| Owner | Relation | Contract |
| --- | --- | --- |
| Knowledge | `core.topic` / `topic_alias` | Workspace identity、alias 唯一、Topic lifecycle |
| Knowledge | `core.claim` / `claim_source` | fingerprint replay、source provenance、Confirmed SUPPORTS deferred fence |
| Knowledge | `core.relation` / `relation_evidence` | canonical symmetric endpoints、fingerprint、confirmed evidence deferred fence |
| Knowledge | `core.conflict` / `conflict_member` | 至少两个成员、member snapshot、Open Conflict 与 disputed Claim closure |
| Knowledge | `core.knowledge_command_receipt` | Workspace-scoped immutable command binding 与 aggregate/version replay |
| Timeline | `ops.knowledge_event` / `timeline_projection_outbox` | source ref exact replay、trigger-generated projection source、SKIP LOCKED/CAS |
| Impact | `ops.impact_report` 及 selector/outbox projection | source+analysis-version identity、owner-backed object binding、supersedes |
| Change Control/Graph | Proposal/Revision/Approval、Semantic Link Candidate | Relation Apply 只读/锁定并按现有 CAS推进，不由 ORM association 管理 |
| Events/Audit | `ops.server_event` / Audit facts | 只通过 scoped Port 原子追加，不复制 owner SQL |

关键 migration 为 `00017_knowledge_domain.sql`、Timeline/Impact `00037`、Impact v2 `00062`、Proposal current revision `00082` 及 Relation Apply 所依赖的 Change Control/Graph migrations。Confirmed Claim/Relation、Conflict closure、Topic/Claim retirement等 deferred constraint必须在真实commit上验证，不能用AutoMigrate或Go hook替代。

Persistence records显式列/类型/schema/TableName，不嵌入`gorm.Model`，不使用hooks、soft delete、auto timestamps或association。JSONB carrier校验JSON后返回string，避免pgx stdlib把`[]byte`绑定为`bytea`；`uuid[]`/`text[]`只用`pq.Array`作为单个`driver.Valuer`。

## 4. Transaction And Lock Matrix

| Flow | Fixed order |
| --- | --- |
| Common command | `core.workspace FOR KEY SHARE` -> advisory `(workspace,idempotency key)` -> receipt lookup/binding -> aggregate work -> immutable receipt -> commit |
| Create Topic | common command -> sorted identity advisory locks -> Topic -> batched aliases via `unnest` -> receipt |
| Suggest Claim | common command -> `INSERT ... ON CONFLICT fingerprint DO NOTHING RETURNING`; conflict branch Claim `FOR UPDATE` + exact payload -> receipt |
| Confirm/Transition Claim | common command -> Claim `FOR UPDATE` -> optional Claim Source -> version/status CAS -> receipt |
| Suggest Relation | common command -> endpoints in canonical `(node type,id)` order `FOR SHARE` -> fingerprint insert/replay `FOR UPDATE` -> optional evidence -> receipt |
| Confirm/Transition Relation | common command -> canonical endpoint locks -> Relation `FOR UPDATE` -> evidence/confirmation -> version/status CAS -> receipt |
| Open Conflict | common command -> optional Topic `FOR SHARE` -> member Claims `ORDER BY id FOR UPDATE` -> Conflict/Members -> Claims disputed update -> receipt |
| Transition Conflict | common command -> Topic/Claims in the same stable order -> Conflict `FOR UPDATE` -> resolution/version CAS -> receipt |
| Bounded reads | UoW `RepeatableRead + ReadOnly` -> aggregate rows -> batched child facts -> full cardinality/domain validation -> commit |
| Timeline append | advisory source ref -> existing event `FOR UPDATE` exact compare or insert -> commit |
| Timeline projection | oldest pending source `FOR UPDATE SKIP LOCKED LIMIT 1` -> decode/validate -> event exact append -> projected/poison version CAS -> commit |
| Impact save + Audit | report exact insert/replay + owner binding -> build redacted audit record -> `RecordImpactAnalysisScoped` in same scope -> commit |
| Relation approval/apply | bind current Revision without locking Proposal -> Candidate `FOR UPDATE` before Proposal -> current Proposal/Revision `FOR UPDATE` -> Approval `FOR UPDATE`/append -> receipt advisory/fence -> canonical endpoints/provenance -> relation fingerprint advisory -> Relation/Evidence/receipt -> current Revision-bound Proposal CAS -> `Event.AppendScoped` -> commit |

Impact 的 scoped 方法同时承载首次保存和 Application 已有报告 replay：首次保存必须进入 `gormSaveImpactReport` 并检查 V2 selector readiness；已有报告 replay 在 Application 已完成 source/object/fingerprint 校验后，只在同一 scope 精确读取 `sameImpactReport` 并追加 Audit，不重复 readiness。该分支保持 legacy Application replay 语义，不能与直接调用 legacy `SaveImpactReportWithAudit` 的 Repository 级行为混为一谈。

普通apply错误全部回滚；baseline stale只允许在同一事务把Proposal提交为`needs_revision`，随后向caller返回版本冲突。Relation Apply的Candidate-before-Proposal、普通command的Workspace-before-receipt、Conflict member排序和endpoint排序都不能被通用ORM CRUD重排。

## 5. Idempotency, Time And Recovery

- Command receipt必须在外部副作用前精确比较Workspace、key、request hash、command type、aggregate type；同key不同绑定是冲突。
- Suggest Claim/Relation fingerprint replay、Timeline source ref、Impact source+analysis-version、Relation Apply Approval+receipt都必须exact compare，禁止宽松replay。
- callback成功后UoW返回错误视为commit response loss。首次按各方法legacy commit code返回，不猜测成功；下一请求只通过durable receipt/source/binding恢复。
- Timeline Projection commit failure不把未确认source猜成projected；poison路径提交后返回ManualRecoveryRequired与原始cause。
- Knowledge aggregate时间沿用command/Clock输入并UTC/微秒规范化；Timeline outbox的`CURRENT_TIMESTAMP`由migration trigger保持。禁止GORM `NowFunc`或自动时间字段改写。
- Relation Apply stale分支与普通成功分支使用不同稳定commit code；stale commit失败不能返回已持久化needs_revision。

## 6. SQL, JSON And Read Contracts

- `FOR UPDATE/SHARE/KEY SHARE`、advisory lock、CTE、`ON CONFLICT ... RETURNING`、CAS、array、JSONB、trigger-sensitive write、SKIP LOCKED与cross-schema reads全部使用固定Raw/Exec SQL。
- GORM SQL使用`?` placeholders与显式casts；外部值只作参数，表/列/order/status仅来自包内固定文本。
- 单行使用`Raw(...).Row().Scan`或显式Rows Next；不得依赖`Raw().Scan`的零行行为。
- 多行路径必须`Close`+`rows.Err()`，严格扫描/JSON/domain校验失败不返回部分结果。
- 批量query最大500，数组通过`pq.Array`单绑定；禁止裸slice被GORM展开。Claim/Relation/Conflict子事实一次批量hydration，禁止per-row查询。
- Timeline correlation/operator/owner binding、Impact objects/selector/report、receipt/evidence JSON继续严格decode/canonical/equality；错误与日志不得带正文、payload、SQL参数、路径或DSN。

## 7. Error, Context And Logging

分类优先级：

1. 现有`foundation.Error`原样保留；
2. `ctx.Err()`与distinct `context.Cause(ctx)`，保留canceled/deadline sentinel和自定义cause；
3. `sql.ErrTxDone`、`sql.ErrNoRows`、`gorm.ErrRecordNotFound`；
4. `*pgconn.PgError`的SQLSTATE/constraint；
5. 脱敏dependency unavailable。

保持legacy SQLSTATE：`23505` version/idempotency冲突，`23502/23503/23514/55000`以及其他`22xxx` consistency（按原调用点），`40001/40P01/55P03/57P01/08000/08003/08006` retryable。不得把未在legacy矩阵中的SQLSTATE（例如`57014`）擅自纳入重试；原始PgError/context cause须可通过`errors.Is/As`获取，公开错误不包含SQL、JSON、证据正文、Actor标识、绝对路径或凭据。

## 8. Verification Design

### Static stage

- Knowledge domain/application/postgres/audit局部test/race/vet；integration compile-only；API/Worker/Organizing compile-only；vendor list、module verify、Trellis validate、gofmt与diff-check。
- 静态确认production与Organizing transaction fence仍构造legacy pgx；无selector、双写、AutoMigrate、独立`gorm.Open`、root`.Transaction`、动态用户SQL或敏感日志。
- 使用Go Review、SQL Review和独立Trellis Check；修复明确缺陷并写入`research/static-validation.md`。

### TODO 9 PostgreSQL stage

2026-09-01 调整：以下完整矩阵保留为 Final 与直接风险触发时的验证清单；
本 child 默认完成门禁改为父任务精简政策规定的一个 GORM 主读写/查询路径，
加一条直接事务或跨 owner 原子性代表场景。精简不改变单 Pool、Workspace、
幂等、deferred constraint、敏感数据与 production Composition 边界。

仅扩展现有integration文件。fixture必须先从`ZHIXU_TEST_DATABASE_URL`连接管理库，创建唯一disposable database；用migration pool执行完整migration后关闭它，再对目标DSN调用一次`platformpostgres.Open`。该Pool同时提供`DB()`给legacy seed/断言、GORM root、UoW、Events GORM与Audit GORM。legacy/GORM每个case使用不同数据库；GORM seed必须先commit，禁止把未提交pgx Tx交给GORM、复用全局pending outbox、创建第二Pool、独立`gorm.Open`或no-op collaborator。

完整清单（仅在上述条件触发时必须覆盖）：

- Topic/Claim/Relation/Conflict全部command的receipt replay/conflict、CAS、对称Relation并发、fingerprint、provenance、deferred constraint与member/endpoint锁序；
- 三类500条批读、Evidence eligibility/topic、RepeatableRead快照、array/JSONB/null binding、连接释放和关键EXPLAIN；
- Timeline exact/conflicting source、并行dispatcher`SKIP LOCKED`、project/poison CAS、trigger outbox与response-loss；
- Impact v1/v2/report replay/supersedes、owner fact校验、Report+Audit失败回滚与commit-response-loss；
- Relation Apply approval+candidate+relation+receipt+event全有或全无、stale仅needs_revision、Event失败、Candidate-before-Proposal并发和commit-response-loss exact replay；
- 真实SQLSTATE/trigger、cancel/deadline/custom cause、stale scope、rollback/commit可见性与连接泄漏。

GORM response-loss测试在同包现有integration文件中包装被测Repository自身的UnitOfWork：装饰器只在第一次调用中先执行真实`Within`完成commit，再返回注入错误；随后使用同Pool未装饰实例按durable receipt/source/binding重放。首次必须返回legacy-compatible commit failure。不得复用pgx Begin wrapper或sleep。

## 9. Rollback And Final Handoff

- TODO 9前回滚只删除staged GORM文件、scoped Impact capability和本task实现证据；legacy production/Schema不变。
- TODO 9后、Final前仍保留pgx wiring作回退；Final统一构造同Pool Audit、Events、Knowledge core/Timeline/Relation Apply并删除legacy。
- Organizing caller-owned pgx transaction由Organizing child迁移到opaque scope；本child不拆其原子事务。
- TODO 3 Atlas 前置已于 2026-08-30 交付，不再阻断 Final schema/composition 收口；本 child 的真实 PostgreSQL、生产 Composition 与其他父任务门禁保持不变。
