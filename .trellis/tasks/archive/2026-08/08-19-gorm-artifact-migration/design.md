# Artifact GORM Migration Design

## 1. Boundary And Rollout

本 child 采用“legacy pgx 保留 + staged GORM sibling + TODO 9 真库对照 + Final 统一切换”。

- `GORMRepository` 和 `GORMSectionGenerationRepository` 只从同一个 `*platformpostgres.Pool` 取得 GORM root
  与 `foundation.UnitOfWork`；不接收裸 GORM/UoW/DSN，不创建第二 pool。
- legacy `Repository`、`SectionGenerationRepository`、`SectionGenerationTerminalHook` 与所有 production constructor
  保持不动，作为 TODO 9 对照基线。
- staged 路径不 fallback 到 legacy，不双写、不 shadow read。任何 dependency/scope/SQL/closure 不完整都 fail closed。
- TODO 9 前不改 `cmd/**`、migration、生产 selector 或现有 integration fixture，不归档 child。

Foundation scope 能校验平台类型、live lifetime 和 transaction handle，不能识别 active foreign-Pool scope。
Artifact、Workflow 与 Agent concrete adapter 必须由 Final/TODO 9 fixture 从同一 Pool 构造；Artifact 不通过 DSN、反射
或 private marker 伪造 pool identity。

## 2. Ownership And Public Surface

Artifact owner 继续拥有：

- `learning.artifact`、`artifact_revision`、`artifact_command`、`artifact_export`、`artifact_publication`；
- `artifact_external_transition_reservation`、`artifact_visibility_hold`；
- `artifact_revision_citation_selector`、`artifact_citation_selector_backfill`；
- `artifact_section_generation`。

只读/写入依赖边界：

- Workflow owner 独占 `workflow.definition/run/node_run/node_attempt` 与 River job；Artifact 通过 scoped Port 启动或验证；
- Agent owner 独占 `agent.model_run/model_call`；Artifact 通过 `ScopedModelRunStore` 读取/锁定/终结；
- Change Control proposal 创建仍经 `artifactapplication.PublicationCreator` 在 Artifact DB 事务之外完成；
- Citation verifier 仍是事务外不可变证据读取，Finalize 在验证后重新获取锁并核对全部 binding。

Artifact staged public surface：

1. `NewGORMRepository(pool)`：实现 `artifactapplication.Repository` 与 `CitationBackfillPort`；
2. `NewGORMSectionGenerationRepository(pool, runtimeStarter, runtimeBindingReader, modelRuns, evidence, ids, clock,
   profile)`：实现现有 Generation Starter/Context/Lookup/Finalize ports；
3. `NewGORMSectionGenerationTerminalHook(modelRuns, profile)`：实现
   `workflowapplication.ScopedWorkflowTerminalHook`，只消费 caller scope；
4. legacy constructors 不改名、不改签名、不包装 GORM。

## 3. Required Scoped Prerequisites

### 3.1 Workflow

已存在 `ScopedRuntimeStarter.StartScoped`，用于 Start 在 Artifact outer UoW 中原子创建 Workflow Run/Node/River job。

Artifact 实现前，Workflow owner task 还需提供一个通用、只读的 scoped durable runtime binding Port。建议形状：

```go
type ScopedRuntimeBindingQuery struct {
    WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
    NodeAttemptID foundation.ID // optional
}

type ScopedRuntimeBindingSnapshot struct {
    RunInput, NodeInput json.RawMessage
    DefinitionKey string
    DefinitionVersion int64
    DefinitionGraph json.RawMessage
    NodeKey, NodeType string
    InputSchemaVersion, OutputSchemaVersion int
    AttemptFound bool
}

type ScopedRuntimeBindingReader interface {
    LoadRuntimeBindingScoped(context.Context, foundation.TransactionScope, ScopedRuntimeBindingQuery) (ScopedRuntimeBindingSnapshot, error)
}
```

最终命名可遵循 Workflow package 风格，但契约必须：

- 只访问 Workflow-owned 表，Workspace predicate 完整；
- 使用 caller scope/context，不 Begin/Commit/Rollback，不 root fallback；
- 返回 raw durable snapshot；`DefinitionGraph`、RunInput、NodeInput 均保留数据库原始 JSON，由 Artifact 使用严格
  decoder 拒绝未知字段/尾随值，再校验 registered definition、canonical graph、run/node input 和 attempt；
- nil、typed-nil、invalid/expired scope fail closed；不引入 Artifact domain import。

Artifact child 不直接复制 legacy `validatePersistedWorkflowBinding`/`validateNodeAttemptBinding` 的跨 owner SQL。

### 3.2 Agent

现有 `agentapplication.ScopedModelRunStore` 已覆盖：

- `GetModelRunScoped`；
- `GetModelRunRecordScoped`；
- `GetModelRunByAttemptScoped`；
- `FinalizeModelRunScoped`。

这些方法足以替换 Generation/Terminal 的 pgx-only `ModelRunTxFinalizer(any)`。Artifact outer UoW 或 Workflow outer
UoW 始终拥有提交/回滚；Agent scoped adapter 不得自建事务。

## 4. Staged File Layout

- `gorm_core.go`：constructors、ready、within/readWithin、callback-vs-commit stage、Raw Row/Rows/Exec；
- `gorm_model.go`：explicit persistence records、JSONB/nullable/time/array carriers；
- `gorm_errors.go`：context/no-row/SQLSTATE/commit classification；
- `gorm_codec.go`：database/sql scanner、state/revision/receipt strict mapping、citation selector helpers；
- `gorm_repository.go`：Find/Probe/Reserve/Create/Transition/Get/List/GetExport；
- `gorm_citation_backfill.go`：bounded claim/process/validate/fail with savepoint；
- `gorm_generation.go`：Start/LoadContext/Lookup/Finalize and durable recovery；
- `gorm_generation_query.go`：RR+RO Section Generation snapshot；
- `gorm_generation_terminal.go`：scoped Workflow terminal hook。

实际文件可按 package 体量拆细，但不得产生两套 domain validation/receipt codec/markdown/citation equality 事实源。
legacy 与 GORM 仅通过 `interface{ Scan(...any) error }` 和纯 helper 共享映射。

## 5. Persistence And SQL Rules

Schema 事实来自 migrations 00039、00040、00041、00042、00043、00053、00057、00060、00062、00074 及后续修订。
GORM 不执行或生成 DDL。

- 所有 fixed SQL 使用 `?` bind；外部字符串不得进入 identifier、ORDER BY 或 predicate 文本。
- advisory lock、`FOR UPDATE`、`SKIP LOCKED`、CAS、RETURNING、CTE、SAVEPOINT、`unnest`/`ANY` 继续 Raw/Exec。
- persistence record 显式映射 schema/table/columns/nullable/UTC/version；不用 `gorm.Model`、hook、soft delete、
  Save、Updates、Preload、Association 或 implicit timestamp。
- JSONB carrier 的 `driver.Valuer.Value()` 返回 string；读取后 strict decode、DisallowUnknownFields、拒绝 trailing JSON。
- `uuid[]` 使用 `pq.Array` 或已验证的 driver Valuer 单 bind；不得把 `[]string`/`[]foundation.ID` 交给 GORM 推断。
- markdown/large JSON 不进入日志或 error；scan 后复用 Domain Validate 和 canonical equality。
- Revision scanner/insert 同时支持 `artifact-revision/v1` 与 `artifact-revision/v2`。存在 DocumentSources 时必须写 v2；
  migration 00074 的 trigger 会投影 immutable `learning.artifact_revision_document_source`，staged 写路径须在同一事务
  精确回读 source identity/revision/hash 集合，读路径须拒绝 schema、JSON 与 projection 不一致。
- 多行查询完整处理 statement error、nil rows、Scan、Rows.Err 与 Close error；partial page/selector set fail closed。
- GORM 文件不使用 pgx Tx/Row/Rows/pool/protocol；`pgconn.PgError` 只允许做 SQLSTATE 分类。

## 6. Command Repository Transactions

### 6.1 Find And Reads

- `FindCommand` 按 Workspace+idempotency key 读取 receipt，严格匹配 request hash/type/artifact/version 和 response；
- `GetCommandState` 使用 raw state，不过滤 visibility hold；`Get` 与 `List` 过滤 active hold；
- List 使用 `updated_at DESC,id DESC` keyset、limit+1、Workspace predicate，禁止 offset/N+1；
- `ProbeExternalTransition` 保持 RepeatableRead+ReadOnly，同一 snapshot 读取 state 和 reservation，绝不写 reservation。

### 6.2 Reserve

一个 UoW 内：Artifact `FOR UPDATE` -> expected version -> reservation exact load -> absent insert / exact replay / conflict。
调用方在 commit 后才执行文件或 Change Control 副作用。commit error 不伪造成功；相同命令下一次通过 durable
reservation 继续。

### 6.3 Create And Transition

两条命令都先取得 `pg_advisory_xact_lock(hashtextextended(workspace+":"+idempotencyKey,0))`，再做 receipt-first
exact replay。

Create 新路径：Artifact insert -> immutable Revision -> citation selectors exact readback -> Receipt -> optional
Visibility Hold。任一步失败整事务回滚。

Transition 新路径：Artifact `FOR UPDATE` -> expected version/current revision -> external reservation `FOR UPDATE` ->
optional immutable Revision/selectors -> Artifact CAS -> optional Export/Publication -> Receipt -> exact reservation delete。
不得将 immutable revision 比较、side fact closure 或 reservation delete 拆出 UoW。

legacy `write` 对 exact receipt replay 仍提交当前事务；staged 路径保持该行为和对应 commit error，不擅自改成 rollback sentinel。

## 7. Citation Selector Backfill

每次调用仅推进一个 Workspace，且 revision limit 遵循 `MaxCitationBackfillRevisionBatch`：

1. 先查询 `core.schema_meta`，只在 `timeline_impact='m7-v2'` 时继续；未启用时返回 legacy 稳定的
   dependency-unavailable，不访问尚不存在的 marker/selector 表；
2. 选择一个 marker `FOR UPDATE SKIP LOCKED`；
3. 按 cursor 读取 bounded revisions；
4. 建立 `SAVEPOINT artifact_citation_backfill_insert`；
5. 批量写 selector；可持久化数据错误时 rollback to savepoint、release，再把 marker CAS 为 FAILED；
6. 成功则 release savepoint，更新 process cursor/count/version；
7. validation 阶段精确回读 selector 集合，更新 validate cursor/count，最终 COMPLETED 或 FAILED；
8. callback 成功后的 commit error 按既有 manual-recovery/unknown 语义返回，不把内存 result 当已提交。

SAVEPOINT 名称是静态常量；不得拼接外部输入。SKIP LOCKED 不得替换成普通 polling 或无界扫描。

## 8. Section Generation

### 8.1 Start

Artifact outer UoW 顺序固定：

```text
advisory(workspace:idempotency)
  -> Generation receipt row FOR UPDATE / exact replay
  -> Artifact FOR UPDATE
  -> active source-slot lookup
  -> Workflow StartScoped (Run/Node/River job in same scope)
  -> Generation insert
  -> outer commit
```

Workflow replay on a brand-new Artifact Generation is consistency failure；Generation exact replay 必须验证 request hash、
source revision/version/section key 和 Workflow binding。callback 已成功后 commit error 才执行 root durable recovery；
只有 exact Generation+Workflow binding 成立才返回结果。

### 8.2 Read Context And Lookup

- LoadGenerationContext 与 ListSectionGenerations 使用 RepeatableRead+ReadOnly UoW；同一 snapshot 读取
  Generation、current Artifact/Revision、source Revision 和 Workflow binding。
- Lookup 读取 durable terminal closure；Pending 时通过 Agent scoped lookup 验证不存在 split binding，Completed 时验证
  Agent Run/Calls 与 recorded Revision/receipt；不使用 live lease 作为 terminal replay 门槛。
- 所有 Workflow facts 通过 scoped durable binding reader；所有 Agent facts 通过 scoped ModelRunStore。

### 8.3 Finalize

Citation verification 在一个短只读 snapshot 结束后执行，不持有 Generation/Artifact/Agent locks；正式 Finalize UoW
重新验证全部 durable identity，防止验证后漂移。

正式锁序固定：

```text
Artifact Generation FOR UPDATE
  -> Workflow durable binding snapshot (read-only owner port)
  -> Artifact FOR UPDATE
  -> Agent Model Run FOR UPDATE + ordered Calls
```

新完成路径在同一 scope：source/current rebase -> immutable Revision/selectors -> Artifact CAS -> Generation COMPLETED CAS
-> Agent FinalizeModelRunScoped。completed replay 精确验证 Model Run、final successful call、recorded Revision、metadata、
content hash 和 receipt；不可重写事实。

Finalize commit error 保持 legacy `generation_finalization_unknown`，由调用方后续 Lookup 证明；不得在未证明时返回 output。

## 9. Scoped Terminal Hook

`GORMSectionGenerationTerminalHook.OnWorkflowNodeTerminalScoped` 运行于 Workflow 已持有的 caller scope：

```text
Workflow Run/Node/Attempt locks (caller)
  -> Artifact Generation FOR UPDATE
  -> Agent Model Run/Calls FOR UPDATE (when attempt exists)
  -> Artifact Generation terminal CAS
```

Hook 不 Begin/Commit/Rollback，不 root fallback，不把 scope 强转 pgx。非 Artifact Generation node 返回 nil；已 terminal
Generation exact no-op；PENDING 根据 Workflow outcome + Agent durable state 收敛 FAILED/CANCELLED/RECOVERY_REQUIRED。
任何 Hook error 由 Workflow outer UoW 回滚 Workflow 与 Artifact/Agent 事实。

## 10. Error, Context And Response Loss

- constructor/entry 拒绝 nil、typed-nil dependency；scope 入口拒绝 invalid/expired/non-platform scope；
- legacy Generation/Terminal 明确把 nil context 归一为 Background，staged sibling 保持这一兼容行为；其他入口按现有
  Application validation 和平台 context contract 处理；
- no-row 识别 `sql.ErrNoRows`/`gorm.ErrRecordNotFound`，再由调用点映射 not-found/replay/conflict；
- cancel 优先 deadline；保留 `context.Cause` 和原始 cause；`sql.ErrTxDone` 映射 dependency unavailable；
- 40001/40P01/55P03 retryable；23505 按 idempotency/CAS/receipt 语义；23503/23514/55000 consistency；
- UoW helper 区分 callback failure 与 callback 成功后的 commit failure，只有 legacy 已有恢复契约的 Start 路径执行
  exact durable proof；普通 command 和 Finalize 不新增假恢复；
- error/log 不包含 DSN、SQL 全文、markdown、JSON 正文、citation excerpt、model prompt/output 或 credential。

## 11. Verification And TODO 9

静态阶段：

```text
go test -mod=vendor ./internal/artifact/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/artifact/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/artifact/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/artifact/... ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s
go list -mod=vendor ./internal/artifact/...
go mod verify
git diff --check
```

TODO 9 在同一 disposable migrated PostgreSQL/同一 platform Pool 下原位参数化现有 integration 文件：

- `repository_integration_test.go`：Workspace CAS、receipt、immutable binding、side-fact closure；
- `command_concurrency_integration_test.go`：Export/Publish 竞争、外部副作用预留、exact recovery；
- `visibility_hold_integration_test.go`：public/internal read 差异；
- `citation_backfill_integration_test.go`：selector 同事务、SAVEPOINT failure/resume/exact validation；
- `generation_integration_test.go`：Start/replay/rebase/evidence/Finalize/reverse order/commit response loss；
- `generation_terminal_integration_test.go`：Workflow terminal rollback 与所有终态。

还必须覆盖：

- migration 00074 的 document-backed Revision v2、document-source trigger projection、malformed/missing projection 与
  SQLSTATE fail-closed；
- `timeline_impact` feature gate 未启用时 Backfill 的稳定 dependency-unavailable；
- Workflow Definition graph 含未知字段或尾随 JSON 时 durable binding reader+Artifact strict decoder拒绝；
- 先修复现有 `seedArtifactWorkspace` 写入 status `test` 与 migration 00067 lifecycle冲突，并在改动GORM fixture前
  单独证明 legacy integration 全绿。

测试 fixture 必须先 OpenMigration+迁移，再由同一 URL `platformpostgres.Open` 得到一个完整 Pool；legacy 与 GORM 使用隔离
数据库，避免相互污染。GORM commit-response-loss 通过包装 Repository UoW，在真实 callback 成功提交后注入错误；不复用
pgx Begin wrapper。compile-only/legacy-only 通过不能记为 GORM parity。

## 12. Final Handoff And Rollback

Final 负责：

- API/Worker 所有 Artifact Repository/Generation/Terminal/Backfill 构造切换；
- 同一个 Pool 构造 Artifact、Workflow、Agent、Change Control dependencies；
- 运行 TODO 9 full matrix 后删除 legacy pgx DB/Tx ports 与非 allowlist import；
- 保留文件导出、Change Control proposal、River worker/listener/migration 的批准边界。

本 child 回滚只删除 Artifact staged GORM files 和 Artifact-owned additive port；Workflow prerequisite 由 Workflow task 独立回滚。
不回滚 migration、不删除生产 legacy 实现、不修改已持久化事实。
