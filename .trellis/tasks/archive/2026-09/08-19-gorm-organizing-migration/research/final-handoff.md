# Organizing GORM Final 交接

日期：2026-09-08。范围仅 `internal/organizing/**` 与本 child 文档。所有核心门禁已通过；本 child 未修改 `cmd/**`、Atlas、go.mod/go.sum 或 active-task pointer，未提交、归档或删除生产 legacy。

## 交付

- `adapter/postgres/gorm_{core,models,read,draft,template,runtime}.go`：显式现有 Schema Model、Draft/Material 版本、Template/Revision、Snapshot、Receipt、Outbox、RunBinding/Result。常规读写使用 GORM Create、CreateInBatches、Updates map；复杂领取/锁 SQL 保留参数绑定 Raw/Exec。
- `adapter/postgres/gorm_fence.go`：Source → Document → Claim → Collection → Evidence 固定锁顺序。Knowledge/Retrieval 通过稳定 scoped Port 批读；无需构造其他 owner 的具体 PostgreSQL Repository。
- `adapter/postgres/gorm_generation.go`：Generation 与 Agent Model Run 在同一 scope 完成绑定、成功或失败终态；保留原始 JSON 字节和哈希。
- `adapter/postgres/gorm_start.go`：Workflow Run、首节点、Workflow Outbox、River Job、Organizing RunBinding 与启动 Outbox 在同一事务完成；失效 lease、错误输入或 owner 身份漂移拒绝提交。
- `application/scoped.go`、`workflow/scoped_{dispatcher,terminal}.go`：公共边界使用 opaque TransactionScope；终态 hook 只解码/校验并调用 scoped writer。
- `adapter/owner/{adapter,fence_policy}.go`：注入 scoped FrozenFence，复用既有 Source availability 与 Collection 有界策略。
- 原位调整 `adapter/postgres/{repository,generation_repository,migration_00074_document_sources}_integration_test.go` 和 `workflow/terminal_test.go`，没有新增测试文件。Integration fixture 使用 `testdb.Require` 的共享平台 Pool；本次未配置 external-admin URL，实际使用 Testcontainers。
- `workflow/dispatcher.go` 只把既有 retry/poison 策略提为包内函数供两条路径复用。

## Final 构造和接线

全部构造返回 `(pointer, error)`，都须处理 error。

```go
organizingpostgres.NewGORMRepository(pool)
organizingpostgres.NewGORMFrozenMaterialFence(sourceReferences, claimReader)
organizingpostgres.NewGORMGenerationRepository(pool, agentFinalizer)
organizingpostgres.NewGORMStartRepository(pool, workflowStarter, workflowBindings)
organizingworkflow.NewScopedTerminalHook(organizingRepository)
organizingworkflow.NewScopedDispatcher(dependencies)
```

对应精确 Port：

| 参数 | 契约 / 实际注入对象 |
| --- | --- |
| `pool` | 同一 `*platformpostgres.Pool` |
| `sourceReferences` | `retrievalapp.ScopedSourceVersionReferenceBatchStore`；`retrievalpostgres.NewGORMSearchRepository(pool)` |
| `claimReader` | `knowledgeapp.ScopedClaimReader`；`knowledgepostgres.NewGORMRepository(pool)` |
| `agentFinalizer` | `agentapp.ScopedModelRunFinalizer`；`agentpostgres.NewGORMRepository(pool)` |
| `workflowStarter` | `workflowapp.ScopedRuntimeStarter`；Workflow GORM Runtime |
| `workflowBindings` | `workflowapp.ScopedRuntimeBindingReader`；`workflowpostgres.NewGORMRuntimeBindingReader(pool)` |
| terminal writer | `organizingapp.ScopedTerminalResultWriter`；Organizing GORMRepository |

构造顺序：先 OrganizingRepository → ScopedTerminalHook → Workflow GORM Runtime（将 hook 加入 scoped composite hook）→ GORMStartRepository → ScopedDispatcher。Generation 独立接 Agent scoped finalizer。

`organizingowner.Dependencies` 新增 `FrozenFence organizingapp.ScopedFrozenMaterialFence`。现阶段 Application 的 `ConfirmRecord.Fence` 仍是 legacy `FrozenMaterialFence`，GORM Confirm 会验证它同时实现 scoped 契约并且只调用 `VerifyFrozenScoped`；生产应传入已注入 FrozenFence 的 `*organizingowner.Adapter`。直接传 `*GORMFrozenMaterialFence` 不能满足旧 `VerifyFrozen(any)`。Final 删除 legacy 时，把旧公共 fence 契约与校验一并收口即可。

`ScopedDispatcherDependencies` 字段为 `Repository AtomicStartRepository`、`Definitions *workflowapp.DefinitionRegistry`、`IDs`、`Clock`、`Owner`、`LeaseDuration`、`RetryBase`、`MaxAttempts`。`DispatchBatch` 返回原来的 `DispatchBatchResult`。旧 `Workflows WorkflowStarter` 字段由新 AtomicStartRepository 与 registry 取代。

## 验证证据

所有 Go test 设 `-timeout=60s`。本次用例没有 Skip，未运行全仓矩阵。

1. `go test -timeout=60s ./internal/organizing/...`：PASS。最新 PostgreSQL unit 3.197s、Workflow unit 3.193s，其余包 PASS/cache；终态既有四组 unit 已直接切到 ScopedTerminalHook。
2. `go vet ./internal/organizing/...`：PASS。
3. `go test -tags=integration -timeout=60s ./internal/organizing/adapter/postgres -run '^(TestRepositoryPostgreSQLDraftTemplateSnapshotAndRuntimeLifecycle|TestConfirmDraftPostgreSQLRevalidatesCollectionWithoutLeakingStatementTimeout)$' -count=1`：PASS，18.880s。最终 fixture 的 Artifact、Collection 协作者均使用 GORM。
4. `go test -tags=integration -timeout=60s ./internal/organizing/adapter/postgres -run '^(TestConfirmDraftPostgreSQLRejectsOwnerDriftAtomicallyAndReplaysWithoutFence|TestConfirmDraftPostgreSQLReturnsStableConflictAfterConcurrentIdempotencyWinner|TestConfirmDraftPostgreSQLRevalidatesCollectionWithoutLeakingStatementTimeout)$' -count=1`：PASS，25.366s。后续 Collection 协作者切换后由第 3 项重验；Owner/并发逻辑无后续变化。
5. `go test -tags=integration -timeout=60s ./internal/organizing/adapter/postgres -run '^TestGenerationRepositoryEnforcesRuntimeAtEveryPersistenceBoundary$' -count=1`：PASS，12.511s。
6. `git diff --check`：PASS；Organizing 与 child 的限定范围检查亦 PASS。
7. `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-organizing-migration`：PASS。工具只提示 database-guidelines.md、error-handling.md 超出自动注入大小；本次按需显式读取有关规范与业务契约。

第 3 项主场景除既有 Draft/Template/Confirm/Outbox/Result 全流程外，原位包含以下实库证据：

- ScopedTerminalHook 在调用方 UoW 插入结果后返回 sentinel，外部读取确认结果不存在；同样事件提交后，普通 BindRunResult 精确 replay 返回原结果。
- 使用真实 Workflow GORM Runtime、ModelSettings scoped enqueue fence 与 River，故意在最后一步复用已有 RunBinding ID 导致失败；Run、Node、Workflow Event、River Job 均为 0，Organizing 投影仍 PENDING。
- 同一 lease 重试正确 Binding ID 后提交，精确重放仍只有一个 River Job，Organizing 投影为 STARTED。
- Raw SQL 中保留的 PostgreSQL `$n` 参数、`pq.Array` 数组、JSONB/bytea 映射均经过实库路径；没有运行时占位符转换器或 pgx 兼容层。

## Go / SQL 审查

按 go-review 与 sql-code-review 完成本次 scoped owner 边界、错误链、typed nil、取消传播、锁顺序、Workspace 隔离、CAS 行数、zero/false/nil 写入、append-only 和有界批量检查。

审查修复：ScopedTerminalHook 统一使用覆盖所有 nilable kind 的依赖检查；GORM 错误分类保留原 error 包装链，避免抽出 foundation.Error 时丢掉其外层上下文。没有剩余阻断发现。

所有 Models 对照 `atlas/schema.sql`：主键/复合主键显式标记，version/position 禁止隐式自增，时间关闭 GORM 自动写入，generated identity 列不写入，nullable 列使用 pointer，JSONB 用 string Valuer，Generation 原文使用 bytea。更新采用显式 map，保证 `false`、`nil` 和 `0` 不被省略。

## Final 删除 legacy 时保留的纯 helper

不能整文件盲删；新路径刻意复用以下原有领域校验/codec：

| legacy 文件 | 新路径仍需要保留的内容 |
| --- | --- |
| `adapter/postgres/repository.go` | `isNilInterface`、binding/command/create/confirm validators、`validHash`、`verifyTemplatePolicy` |
| `adapter/postgres/template.go` | `validateCreateTemplateRecord`、`validateReviseTemplateRecord`、`sameBuiltIn` |
| `adapter/postgres/runtime.go` | `maxStartLease`、`maxStartAttempts`、`validateStartLease`、`validRuntimeErrorCode`、`sameRunResultRequest`、`definitionForTemplateKind` |
| `adapter/postgres/generation.go` | `validateGenerationModelRecord` 及其后的 runtime/call/result/binding/create validators、hash/ID/time/error helpers |
| `adapter/postgres/codec.go` | `scanner`、template selects、material/evidence/declaration codecs、`scanTemplateDetail`、`receiptRow`/select/scanner/matches、ID/time helpers。`queryer` 与 pgx import 仅供 legacy，可在删除后移除 |
| `adapter/postgres/errors.go` | 公共 error helper；旧 pgconn 分类由 `classifyGORM` 替代后可以删除 |
| `workflow/terminal.go` | `decodeFinalReceipt`、`terminalContract`、`terminalInvalid`、`terminalUnavailable`。旧 writer、pgx SQL、旧 hook 已由新路径替代 |
| `workflow/dispatcher.go` | `StartInput`、`DispatchBatchResult`、`dispatchRecoveryTimeout`、`recoverStartFailure`、`poisonStart`、`dispatchRecoveryContext`、`boundedBackoff`、`stableFailure`、`definitionForKind`、`canonicalTime` |

`adapter/owner/transaction_fence.go` 的事务函数全由新 PostgreSQL fence 替代；其 Collection revision 等纯 helpers 在 owner 其他文件没有调用方，本次搜索确认。Final 还需删除旧 Adapter 的 `FrozenMaterialFence` 接口断言或同步改为 scoped 契约，并更新业务规范中旧 `any`/pgx 事务说明。

## 验证边界与回滚

本 child 的模型和事务核心完成；生产 API/Worker 接线、legacy 删除、全仓 pgx allowlist、跨模块最终编译由父任务 Final 负责。没有 Schema 变化，回滚以 Repository/Composition 版本为边界。未增加完整 Worker 端到端、response-loss、race、容量或浏览器矩阵；这是父任务精简门禁明确允许的范围，不代表这些额外路径已实测。
