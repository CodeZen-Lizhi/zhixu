# Graph GORM Migration Design

## 1. Boundary And Compatibility

本 child 采用“legacy pgx 保留 + staged GORM sibling + Final 统一切换”策略：

- legacy `Repository`、`CandidateConfirmRepository`、`SemanticLinkScanRepository`、planner、
  page source、discovery writer、cancellation guard 与所有生产构造保持不变；
- staged 实现只从一个完整 `*platformpostgres.Pool` 取得 GORM root 与
  `foundation.UnitOfWork`，不接受可错配的裸 GORM root/UoW，不创建第二 pool；
- 多语句写入只通过 `UnitOfWork.Within`，scope 在 callback 内立即解包、不得缓存或逃逸；
- GORM sibling 不调用 `Begin/Commit/Rollback`、root `.Transaction`、`gorm.Open`、
  `AutoMigrate` 或 `Migrator`；
- Graph Application/Domain/Public Port 不出现 GORM、database/sql、pgx 或 transaction `any`；
- TODO 9 前不改 `cmd/**`、不切 Composition、不双写、不 fallback、不删 legacy。

Foundation scope 当前只能校验 concrete type、live lifetime 和 transaction handle，不能校验
Pool identity。Graph、Change Control、Collection 和 Workflow 的完整构造必须来自同一个
platform Pool；active foreign-Pool scope 是平台已知限制，由 Final Composition 与 TODO 9
fixture 保证，不在 Graph 中伪造 identity。

## 2. Staged Types And Files

建议按现有 ownership 文件拆分：

- `gorm_core.go`：`GORMRepository`、单 Pool 构造、ready、within/read-snapshot、
  callback-vs-commit stage、Raw Row/Rows/Exec、context/no-row/SQLSTATE 分类；
- `gorm_adapter.go`：仅适配包内固定 `$n` SQL 到 GORM `?`，database/sql Row/Rows、
  array/JSON carrier 与 RowsAffected；
- `gorm_model.go`：Candidate/Evidence/Decision/Scan persistence records、JSONB carrier、
  nullable ID/time 与严格 mapper；
- `gorm_query.go`：七个 `graphapp.QueryPort` 方法与单 RR/read-only snapshot；
- `gorm_candidate.go`：Candidate upsert/list/get/decision；
- `gorm_candidate_confirm.go`：Graph-owned Confirm UoW 与 Change Control scoped collaborator；
- `gorm_scan.go`：Start/Get/GetByWorkflowRun/Advance/Finish；
- `gorm_scan_page.go`：Topic planner/page、Smart Collection planner/page、discovery writer；
- `gorm_scan_cancellation.go`：Workflow scoped cancellation guard；
- `internal/graph/candidateconfirm/scoped_proposal.go`：consumer-owned最小 scoped capability。

文件可在实现时按既有命名细分，但不得把查询、Candidate Confirm 和 Scan 三种事务模型
压入一个超大 Repository helper，也不得复制第二套 Domain validation。

staged constructors：

```go
NewGORMRepository(pool *platformpostgres.Pool)

NewGORMCandidateConfirmRepository(
    pool *platformpostgres.Pool,
    proposals candidateconfirm.ScopedKnowledgeProposalPort,
    ids foundation.IDGenerator,
    clock foundation.Clock,
)

NewGORMSemanticLinkScanRepository(
    pool *platformpostgres.Pool,
    runtime workflowapplication.ScopedRuntimeStarter,
    collection collectionapplication.ScopedDurableScanBindingVerifier,
    ids foundation.IDGenerator,
    clock foundation.Clock,
)
```

State-only、planner/page 和 writer 可提供窄构造；凡完整接触数据库的构造都接完整 Pool。
Discovery writer应接受已经构造好的 `*GORMRepository`，不能再注入另一个可错配 root。
Smart Collection planner/page继续接受 Application reader；Graph 不依赖 Collection concrete adapter。

`ScopedKnowledgeProposalPort` 的Change Control GORM实现不属于本child。它必须先在仍为
`in_progress`的Change Control migration task中独立补齐typed Proposal helper抽取、owner验证与
静态证据；Graph task只定义consumer接口并注入结构化满足者。这样两个task不共同修改同一模块，
同时不会形成Go import cycle。

## 3. Shared SQL And Database Boundary

Graph 现有 SQL 以 PostgreSQL `$n` 编号，同一 marker可能重复；GORM Raw 只识别 `?`。
为了保持一个 SQL/validation事实源，实施时允许一个 Graph-private renderer：

- 只接受包内固定 SQL，外部输入永远不能成为 SQL文本；
- 扫描 `$` 后连续十进制数字，按每次出现顺序输出 `?` 并复制对应参数；
- 支持 `$10+`、marker乱序和重复；拒绝 `$0`、越界、malformed、未使用参数；
- 跳过单引号、双引号、line/block comment 和 PostgreSQL dollar-quoted literal；
- 每个 `[]string`/UUID/text/int array 转为单一 `pq.Array` Valuer；不展开裸 slice；
- JSONB 输入使用校验后的 string Valuer；不把 `[]byte` 交给 driver推断为 bytea；
- Raw Row/Rows 必须先检查 statement error/nil handle，Rows 必须 Close 并检查 Err；
- `sql.ErrNoRows` 在调用点映射成原来 `pgx.ErrNoRows` 分支对应的 NotFound/Conflict，
  不能全部归类为 DependencyUnavailable。

renderer属于高风险纯函数，静态阶段必须在现有`repository_test.go`中增加表驱动用例，覆盖重复/
乱序marker、`$10`、单/双引号、line/block comment、dollar quote、`$0`/越界/未使用参数和
array仍为单binding；不得等到TODO 9才发现错绑。

只抽取最小 Row/Rows/RowsAffected、SQL常量、scanner、codec、equality和 validation helper。
新 GORM 文件不得拥有 pgx transaction/pool；legacy-only wrapper保留 pgx。若复用 legacy helper
要求伪造完整 `pgx.Tx`，应先收窄私有 helper interface，而不是实现假的 transaction。

## 4. Persistence And Projection Models

Schema 继续由 migration/trigger 唯一管理：

| Owner | Relation | GORM contract |
| --- | --- | --- |
| Graph | `graph.semantic_link_candidate` | explicit columns、Workspace/fingerprint/status/version、nullable Proposal/defer/reopen、无 auto time |
| Graph | `graph.semantic_link_candidate_evidence` | append-only ordered evidence、provenance、批量 arrays |
| Graph | `graph.semantic_link_candidate_decision` | immutable idempotency receipt、nullable Proposal/relation/reason/defer |
| Graph | `graph.semantic_link_scan` | immutable scope/binding、workflow run、checkpoint/counters/status/version |
| Knowledge | Topic/Claim/Relation/Evidence projections | read-only Raw projection；Graph 不写 canonical Knowledge tables |
| Change Control | Proposal/Revision | 只通过 scoped owner capability写入；Graph不复制新的跨 owner SQL |
| Workflow/River | Definition/Run/Node/Outbox/Job | 只通过 `StartScoped` 参与 Graph scope |
| Collection | Smart Collection definition/member projection | 只通过 scoped exact verifier或 Application reader |

Persistence record 位于 PostgreSQL Adapter，不复用 Domain entity。不使用 `gorm.Model`、soft delete、
association auto-save、implicit timestamp、Hook 或零值 `Updates`。每次 scan 后继续 ParseID、enum、
canonical JSON/hash、time UTC 和 Domain validation，损坏数据 fail closed。

## 5. Query Projection

`GORMRepository` 完整实现七个 Query Port。每个顶层调用使用：

```text
UnitOfWork.Within(RepeatableRead, ReadOnly)
  -> SET LOCAL statement_timeout = 1500ms
  -> all endpoint/frontier/hydration/detail queries on one scoped GORM tx
  -> callback return
  -> read-only commit/rollback
```

固定行为：

- Global/Evidence `limit+1`、最大 500 和截断原因不变；
- Search/Global/Neighborhood/Path排序、window/cursor inputs不变；
- Neighborhood depth=1 edge/node budget与 depth>1整层语义不变；
- Path端点、双向 BFS、layer frontier与 final hydrate共享快照；
- CTE、LATERAL、`DISTINCT ON`、ANY/unnest、ORDER/LIMIT保持原 SQL形状；
- Workspace与Knowledge lifecycle predicates不减少；
- statement timeout/cancel返回原稳定 Graph error family并保留 cause；
- 现有 plan/benchmark 的索引、statement count和p95门禁不放宽。

## 6. Candidate Repository

Candidate root写使用默认 read-write UoW；读窗口使用 RR/read-only UoW。

### 6.1 Upsert

固定顺序：

```text
fingerprint lookup FOR UPDATE
  -> exact replay/suppression return
  -> validate both Knowledge endpoints in Workspace
  -> newest suppressed Candidate lock
  -> INSERT ... ON CONFLICT DO NOTHING RETURNING
  -> concurrent winner exact load
  -> batch Evidence insert via parallel unnest arrays
  -> supersede prior endpoint-pair Candidate rows
  -> commit
```

Evidence必须一次批量写；list/get先加载有界 Candidate root window，再用一个 ANY(uuid[])查询完整
Evidence，保持两查询 hydration和 cardinality/hash/domain校验，禁止 N+1。

### 6.2 Ordinary Decision

固定顺序：receipt lookup -> Candidate `FOR UPDATE` -> stale version下 receipt recheck ->
optional Proposal `FOR SHARE` binding -> DB `CURRENT_TIMESTAMP` -> append Decision -> Candidate CAS。
相同 key不同 request、CAS loss、append-only trigger、nullable relation/defer/proposal与 monotonic time
保持原错误与结果。GORM root不改变 legacy commit error的首次暴露；后续相同请求通过 receipt replay。

Discovery Candidate writer仍以 fingerprint为事实幂等边界，以 Scan checkpoint为 once-only counter边界。
它批量读取 claim evidence，再按 bounded discovery hits调用 GORM Candidate Repository；不引入 N+1
数据库查询，也不把整个 page误包成一个跨外部 provider transaction。

## 7. Candidate Confirm And Change Control Port

Candidate Confirm 不能调用会自开 UoW 的 Change Control root Repository，也不能继续在 Graph
GORM 文件直接写 `change_control.*`。最小 consumer-owned Port：

```go
type ScopedKnowledgeProposalPort interface {
    CreateKnowledgeChangeProposalScoped(
        context.Context,
        foundation.TransactionScope,
        changecontroldomain.Proposal,
    ) (changecontroldomain.Proposal, error)

    GetInitialKnowledgeChangeProposalScoped(
        context.Context,
        foundation.TransactionScope,
        foundation.ID,
        foundation.ID,
    ) (changecontroldomain.Proposal, error)
}
```

两个 ID 分别是 Workspace与Proposal。Port位于 `candidateconfirm`，只表达 Graph Confirm所需能力；
Change Control GORM Adapter将在其owner task中结构化满足它，无需依赖Graph concrete Repository。
以下是Graph消费前必须由owner task冻结并验证的contract；不由Graph task实现：

- 只解包 live scope，不开事务、不提交/回滚、不 fallback root；
- 复用 Change Control typed Proposal canonical validation、JSON codec与错误分类；
- create-or-exact-load `knowledge_change` Proposal与 immutable Revision 1；
- exact reader按 Workspace+Proposal ID锁定 Proposal/Revision 1，不能读取 mutable current revision；
- 新 Proposal按当前 owner契约同时写 `current_revision_id=revision.id`；
- 历史空 pointer/v1事实仍可从 Revision 1回放；
- 任何 owner error由 Graph映射回 `RELATION_PROPOSAL_CONFIRM_*` family并保留 cause。

Graph Confirm唯一事务顺序：

```text
Graph default UoW owns scope
  -> exact Decision receipt lookup
  -> Candidate FOR UPDATE
  -> stale version: exact receipt recheck
  -> Change Control scoped create/exact initial Proposal+Revision lock
  -> append Decision receipt
  -> Candidate version/status/current_proposal CAS
Graph commits or rolls back all facts
```

Candidate必须先于Proposal加锁；反向会与 approval/apply trigger/路径形成死锁。v2新建只允许 HIGH；
历史v1 key/risk兼容仅用于 exact replay。Decision receipt是commit response-loss唯一恢复锚点，不能从
Candidate `PROPOSAL_CREATED` 推断成功。Confirm首次commit error按legacy返回错误，第二次用未包装
Repository从exact receipt回放；Change Control scoped collaborator不拥有recovery。

## 8. Semantic Link Scan

### 8.1 Start

构造强制 Workflow scoped runtime、Collection scoped verifier、IDs和Clock均非nil/typed-nil。
Graph拥有一个 `RepeatableRead` UoW：

```text
Scan receipt FOR UPDATE
  -> exact replay return before mutable dependency checks
  -> SMART only: Collection binding FOR SHARE + revision + exact count
  -> Workflow StartScoped
       -> Definition/Run/Node/Outbox
       -> Model Settings scoped enqueue fence
       -> River InsertTx on same sql.Tx
  -> DB-owned Scan timestamp
  -> Scan INSERT / exact winner comparison
  -> Graph root commit
```

Graph root是唯一commit owner。callback retryable failure在rollback后fresh-root查receipt；commit返回错误
也fresh-root查receipt，命中必须完整比较 Workspace/scope/fingerprint/hash/key/total nodes。不得让
Workflow scoped方法执行root recovery或让Graph在同一aborted RR transaction读取winner。

### 8.2 State

- Get/GetByWorkflowRun保持Workspace binding和严格scan；
- AdvancePage改为一个默认UoW：Scan `FOR UPDATE` -> DB time -> expected-version/status CAS；
  这修复legacy root pool多语句中锁提前释放的问题，不改变公开版本/计数/时间语义；
- Finish保持单条 expected-version/status `UPDATE ... RETURNING`；
- terminal time仍使用executor Clock并遵循PostgreSQL微秒精度；GORM不自动更新时间。

### 8.3 Cancellation

新增 guard实现 `workflowapplication.ScopedCancellationSafetyGuard`，只解包Workflow caller scope：

- 无关联Scan：safe；
- PENDING/RUNNING：`FOR UPDATE`后CAS到CANCELLED，DB time写完成时间；
- 已CANCELLED：safe；
- SUCCEEDED/FAILED：VersionConflict；未知状态：ConsistencyViolation；
- nil/foreign-type/stale scope fail closed；active foreign-Pool scope无法识别，属于平台限制。

锁序保持Workflow node在前、Graph Scan在后；Graph guard不得反调Workflow。

## 9. Planner And Page Sources

Topic planner保持Active Topic version +正式CONFIRMED/DISPUTED membership exact count。Topic page
完整运行于一个RR/read-only UoW：version -> stable source keyset page -> per-source LATERAL targets ->
batch missing-target hydration -> single batch exclusion。page上限100、per-source target上限100、
`limit+1`、排序、partial-index predicates和 exclusion ordinality不变。

Smart Collection planner/page继续使用现有Application reader。Collection durable page自己的
RR snapshot结束后，Graph exclusion从Graph root读取；当前Port无法把二者放进同一scope。本child
明确保留这一现有边界，不通过嵌套UoW声称原子。若产品以后要求单快照，需单独设计跨owner
projection Port，不在本迁移擅自扩大。

## 10. Error, Context And Logging

- nil context在所有GORM入口拒绝；每条Raw调用使用caller/callback context；
- cancel优先于deadline分类；distinct `context.Cause` 与 sentinel通过 `errors.Join`保留；
- `sql.ErrNoRows`/`gorm.ErrRecordNotFound`按具体调用点映射；`sql.ErrTxDone`为dependency unavailable；
- `*pgconn.PgError` SQLSTATE/constraint与legacy Graph/Candidate/Confirm/Scan family保持，原cause可
  `errors.As`；unknown driver error只记录类型，不暴露原SQL/payload；
- retryable serialization/deadlock/lock timeout/connection error不吞cause；23503/23514/55000
  按现有consistency，23505按具体receipt/version路径处理；
- 日志不得包含SQL、DSN、Candidate/Evidence/Proposal JSON、risk/rollback原文、Collection query、
  Workflow input或PostgreSQL原始message；只记录稳定code与安全correlation ID。

## 11. Verification And TODO 9

静态阶段使用现有测试，不新增测试文件：

```text
go test -mod=vendor ./internal/graph/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/graph/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/graph/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/graph/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s
go list -mod=vendor ./internal/graph/...
go mod verify
git diff --check
```

`go mod tidy -diff`只检查，不接纳无关全仓漂移。完成后执行Go Review、SQL Review和Trellis Check。

TODO 9使用一个platform Pool构造每个GORM fixture，并在现有integration文件原位做legacy/GORM
独立数据库对照，覆盖：query快照/cursor/窗口、Candidate并发/arrays/JSON、Confirm锁序/rollback/
v1-v2/current pointer/commit loss、Scan Collection+Workflow+River原子性/取消/commit loss、
真实SQLSTATE/context/连接释放、EXPLAIN与20k/100k benchmark。无真实PG时这些均保持未完成。

## 12. Final Handoff And Rollback

Final需统一替换API/Worker中的Graph Query、Candidate、Confirm、Scan、planner/page/writer和Workflow
cancellation composite，并用同一Pool构造Change Control、Collection、Workflow/River依赖。Final验证
后才删除legacy constructor、pgx transaction seams和非allowlist imports；test fixture pgx seed/tracer
可继续作为测试工具。

TODO 9前回滚只revert staged Graph文件与`candidateconfirm` scoped capability。Change Control
scoped实现由其owner task独立回滚；Schema、migration、legacy production与历史事实不变。
新建v2 Proposal的`current_revision_id`是当前Change Control owner契约；历史空pointer仍可读，
不需要数据回滚。
