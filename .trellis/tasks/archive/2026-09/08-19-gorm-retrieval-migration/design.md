# Retrieval GORM Migration Design

## 1. Boundary And Rollout

本 child 采用“legacy pgx 保留 + staged GORM sibling + Final统一切换”的双轨策略。新类型只从一个
完整 `*platformpostgres.Pool` 取得 GORM root、UoW和批准的native capability；不接受可错配的裸
`*gorm.DB`/UoW/pgx pool，不创建第二 pool。

本 child 2026-09-08 收口边界：

- 不改 `cmd/**`、migration、生产 constructor 或 selector；
- 不双写、不 shadow read、不 fallback、不删 legacy；
- TODO 9 `testdb.Require` 已为现有 legacy/GORM integration 提供独立正式迁移数据库；
- 按父任务 2026-09-01 精简政策验收实现，生产统一接线与归档由主会话/Final 处理。

Foundation scope可验证具体类型、live lifetime和transaction handle，但不能验证Pool identity。所有
Retrieval sibling、Workflow River producer和scoped collaborators必须由Composition从同一个Pool构造；
active foreign-Pool scope是平台已知限制，不在Retrieval中用反射或伪造token解决。

## 2. Staged Types And Files

建议文件按事务模型拆分：

- `gorm_core.go`：root/UoW、ready、within/read snapshot、callback-vs-commit stage、Raw Row/Rows/Exec、
  context/no-row/SQLSTATE分类；
- `gorm_model.go`：显式records、JSONB/array/vector/null/time carriers、严格mapper；
- `gorm_repository.go`：Embedding/Index getter、transition、lexical、activation/rollback；
- `gorm_vector.go`：vector batch、build page与commit；
- `gorm_search.go`、`gorm_evidence.go`：Search/Evidence及scoped SourceVersion batch read；
- `gorm_delivery.go`、`gorm_processor_context.go`、`gorm_regression.go`；
- `gorm_dispatcher.go`、`gorm_completion.go`：唯一outer UoW与scoped collaborators；
- `native_capabilities.go`：三个窄 native interface；复用 `repository.go`、`snapshot.go`、
  `source_refresh_lock.go` 中的原生实现，Final 删除 legacy wrapper 前必须保留这些实现；
- `internal/retrieval/application/scoped_dispatcher.go`：parallel scoped job/store contract；
- `internal/retrieval/application/scoped_reindex_collaborators.go`：consumer-owned outbox/binding/completion契约；
- `internal/retrieval/adapter/river/gorm_inserter.go`：Retrieval Job与Workflow generic scoped inserter映射。

实际实现可按现有ownership文件调整命名，但不得把所有路径压入单一巨型helper，也不得复制第二套
Domain状态机、receipt codec或SQL事实源。

实际 constructor：

```go
NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error)
NewGORMSearchRepository(pool *platformpostgres.Pool) (*GORMSearchRepository, error)
NewGORMDeliveryRepository(pool *platformpostgres.Pool, ids foundation.IDGenerator) (*GORMDeliveryRepository, error)
NewGORMDispatcher(
    pool *platformpostgres.Pool,
    ids foundation.IDGenerator,
    options workflowriver.Options,
    fence workflowriver.ScopedEnqueueFence,
    outbox ScopedReindexOutbox,
    binding ScopedReindexBindingVerifier,
) (*application.ScopedDispatcher, error)
NewGORMCompletionRepository(pool *platformpostgres.Pool, completion ScopedReindexCompletion) (*GORMCompletionRepository, error)
```

`GORMRepository`通过窄native interfaces组合完整`application.Store`；普通方法不能调用legacy
`Repository`作为万能fallback。

## 3. Shared SQL And Persistence Mapping

Migration/trigger仍是Schema唯一事实源；GORM不执行DDL。固定SQL保留Raw/Exec，placeholder使用`?`。
若抽取legacy SQL，使用显式双模板或受控renderer，不能对外部SQL/identifier开放。

共用规则：

- 只抽取最小`Scan(...any) error`、RowsAffected、scanner、codec、equality和validation helper；
- `Raw(...).Row().Scan`用于必须区分no-row的单行查询；`Rows()`先检查statement error/nil handle，
  defer Close并检查Rows.Err/Close；
- JSONB carrier验证后`Value()`返回string，避免`[]byte`推断为bytea；
- `[]string`/UUID/text arrays使用`pq.Array`单binding；不传裸slice；
- `pgvector.Vector`直接使用锁定库的Scanner/Valuer；dimension、normalization和distance继续由Domain/DB验证；
- persistence record显式列出schema/table/column/null/time/version，不使用`gorm.Model`、hook、implicit time、
  soft delete、Save、Association或零值Updates；
- 写后继续ParseID、canonical JSON、UTC/microsecond和Domain validation；部分结果fail closed。

## 4. Native pgx Capability Inventory

### 4.1 Manifest

`BeginIndex`整体保留窄pgx事务：create/exact replay -> metadata insert -> manifest `CopyFrom` -> commit。
不能先GORM insert再另一连接COPY，也不能把`pgx.Tx`暴露到Application。legacy和GORM composite复用同一
typed native函数/能力；commit response-loss继续按legacy返回首次错误、后续exact replay。

### 4.2 Workspace Snapshot

固定生命周期：Acquire同Pool connection -> session advisory lock -> RepeatableRead transaction -> temp stage ->
bounded page/hash/count -> metadata/source/chunk COPY -> commit -> 原session unlock。unlock失败hijack-close，
禁止把可能持锁的connection放回pool。中间不穿插GORM root。

### 4.3 Source Refresh Lease

Acquire固定connection并循环`pg_try_advisory_lock`；重试尊重caller context。Release幂等，必须在原session
unlock；失败hijack-close。lease可跨多个普通GORM事务，因此不能伪装成transaction advisory lock。

Final静态allowlist分开记录：Retrieval Repository只允许上述三种native能力；平台/runtime另允许pgvector
pool registration与`riverpgxv5` worker/listener/migrator。River runtime例外不得扩展为普通Repository pgx权限。

## 5. Core Index, Vector And Regression

- Register/Get Embedding保持contract uniqueness和exact replay；DB生成/应用时间语义不变。
- BuildLexical/SaveVectorBatch保持set-based SQL、精确集合锁、写后count/readback与无partial rollback。
- Transition/Activate/Rollback保持target/previous排序锁、workspace xact lock、single-active约束、receipt与
  deferred closure；不得用普通ORM Save拆事务。
- Vector page使用稳定cursor、`LIMIT+1`和bounded batch；cache/projection使用unnest set write与exact readback。
- Regression使用RepeatableRead可写UoW：观察阶段只读，但结构失败时在同一snapshot transaction内将
  IndexVersion CAS为failed；不能声明read-only，也不能把失败更新拆到第二个transaction。

## 6. Search And Evidence

每个Search入口使用一个read transaction。Lexical先在同一transaction执行transaction-local
`set_config('pg_trgm.similarity_threshold','0.3',true)`再查询；Vector operator只来自现有三值enum。
Active-only、Workspace、embedding/index/filter/provenance、rank/fusion和stable order不变。

Evidence单条/批量保持Workspace scope、最大输入、`unnest ... WITH ORDINALITY`、请求顺序、去重/cardinality
和strict metadata验证。新增scoped SourceVersion batch方法只解包caller scope，不拥有transaction，供
Organizing owner后续接入；legacy`NewSearchRepository(pgx.Tx)`保留到Final。

## 7. Delivery And Processor Context

Delivery所有mutation由GORM UoW拥有，锁序固定为Delivery `FOR UPDATE` -> Attempt `FOR UPDATE` -> DB clock。
Claim/Reclaim、lease generation、owner/fence、Checkpoint/Fail、exact terminal replay和commit response-loss不变。

`ProcessorContext`是现有Retrieval Application拥有的只读integration projection。GORM实现保持一个
RepeatableRead/read-only snapshot和固定跨Workflow/ChangeControl/Core/Ingestion SELECT，只读取、不修改
owner表；继续校验delivery/attempt/lease、outbox/commit/writeback、source/version/ingestion/index完整绑定。
若未来要求owner-clean read model，另立投影任务；本迁移不拆成跨事务N+1 owner calls。

## 8. Scoped Dispatcher And River

Application新增parallel接口：

```go
type ScopedJobInserter interface {
    InsertScoped(context.Context, foundation.TransactionScope, ReindexJob) (JobReceipt, error)
}

type ScopedDispatcherStore interface {
    DispatchOneScoped(context.Context, DispatchPath, ScopedJobInserter) (bool, error)
}
```

`ScopedDispatcher`与legacy`Dispatcher`共享fairness loop，均实现`BatchDispatcher`。新路径不得把scope塞回`any`。
Retrieval River adapter把`ReindexJob`映射为`reindexriver.Args`，调用Workflow generic
`ScopedTypedJobInserter`；producer使用`riverdatabasesql`，worker/listener继续`riverpgxv5`。

`NewGORMDispatcher`必须从传入Pool内部创建insert-only Workflow River client与scoped typed inserter，
并强制`options.EnqueueFence`为空，只接受单独传入的scoped fence；这样queue/schema/logger来自同一options，
且不会误用legacy pgx fence。constructor还必须拒绝nil/typed-nil outbox、binding、IDs和fence。

First路径顺序冻结为：

```text
Retrieval UoW owns scope
  -> Workflow scoped outbox claim FOR UPDATE SKIP LOCKED
  -> workspace transaction advisory lock
  -> Change Control scoped binding verification
  -> create/exact-load Delivery
  -> scoped River insert
  -> Workflow scoped outbox publish CAS
  -> commit
```

Retry路径保持Delivery due/FIFO/try-lock、dispatch generation与River insert同scope。Workflow outbox和
Change Control concrete实现不属于本child；Retrieval只定义consumer最小接口并拒绝nil/typed-nil。
Workflow `NewGORMReindexOutbox(pool)` 与 Change Control `NewGORMRepository(pool)` 已提供实际 owner
capability。现有 integration 用真实 Model Settings scoped fence 和 official River producer 验证
Outbox、Delivery、River 的共同 commit/rollback，以及 Retry workspace try-lock 不等待。

## 9. Completion

Completion outer UoW由Retrieval拥有，Change Control提供两阶段consumer capability：

1. `LockReindexCompletionScoped`：在caller scope内按proposal -> execution顺序锁定并返回immutable facts、
   current/replay/stale状态；
2. `CompleteReindexScoped`：按expected versions将execution/proposal从verifying CAS到completed；不提交/回滚。

完整顺序：workspace xact lock -> CC proposal -> CC execution -> Retrieval delivery -> attempt -> sorted indexes ->
DB clock/final binding recheck -> activate -> complete attempt/delivery -> CC CAS -> outer commit。任何owner、lease、
version、manifest或trigger失败全部回滚。首次commit error返回`REINDEX_COMPLETION_COMMIT_FAILED`；下次由
durable activation/delivery/CC facts进行exact replay。

GORM Completion 不直接新增 `change_control.*` mutation SQL；nil/typed-nil collaborator 在构造时
fail closed。Change Control GORM owner 的 Lock/Complete 两阶段方法已接入。既有 completion
只读 closure 查询留在同一 scope；activation 复用已经排序取得的 Index 行锁。实库验证覆盖两个
owner CAS 完成后注错全回滚、cleanup/workflow 前置条件、锁等待后 lease 重验、提交响应丢失和历史重放。

## 10. Error, Context And Logging

- GORM入口统一ready(ctx)，nil context拒绝；callback context贯穿每条SQL。
- classifier先保留distinct context Cause与sentinel，cancel优先deadline；随后Foundation Error、no-row、
  `sql.ErrTxDone`、`*pgconn.PgError`和unknown type。
- SQLSTATE集合严格保持legacy：40001/40P01/55P03 retryable；23505按具体idempotency/version处理；
  23503/23514/55000 consistency；57014只在caller context明确时按cancel/deadline处理。
- callback成功后的UoW error使用具体commit/unknown code；事务内error保持原operation code。
- error/log只保留稳定code、安全ID和类型；不包含SQL、DSN、payload、Source正文、vector或credential。

## 11. Verification And TODO 9

2026-09-08 按父任务 `research/lean-test-policy-2026-09-01.md` 执行。准确命令、耗时、修复和盲区见
`research/static-validation.md`。最小门禁为 Retrieval 既有 unit/vet、Testcontainers 主读写路径、
直接涉及事务/锁/River 的代表性场景、Go/SQL 自检、task validate 和 diff 检查；本次另跑局部 race。

真实 PostgreSQL 场景复用既有文件和 fixture，不新增测试文件：

- FTS/hybrid build、ready/activate/replay、向量 batch；
- Active Search、Workspace/Source/path/time filter、bounded provenance、既有 EXPLAIN 与 incremental Snapshot；
- Regression pass/replay、五种 structural mismatch、严格 Processor Context；
- Dispatcher 真 River insert 后故障全回滚、成功共同提交、Retry generation 与 workspace try-lock；
- Completion 跨 owner rollback、prerequisite、锁等待后 lease、commit response-loss 与历史 replay。

未执行完整 SQLSTATE/故障矩阵、pgx worker 进程烟测和 500k/384 维容量基准，不将上述证据扩展为这些门禁通过。
Search 的现有 EXPLAIN 允许两个有界 source-manifest index；小数据 fixture 不强制 planner 选择同一个索引名。

## 12. Final Handoff And Rollback

Final 统一替换 API/Worker/Organizing 构造并确保 Retrieval、Workflow、Change Control、Model Settings
与 River 来自同一个 Pool。`GORMCompletionRepository` 是独立对象，不能直接把 GORM Store 当作
CompletionStore。构造示例、调用点和逐文件 legacy 清理边界见 `final-handoff.md`。

本 child 回滚只撤销本次 Retrieval staged Dispatcher/Completion、共享 helper 适配和 fixture 改动；
不回滚 migration、历史 Index/Delivery/Activation 事实、其他 owner 的 scoped 实现或用户已有改动。
`gorm_delivery.go` 的显式 timestamptz binding 与 shared Snapshot 列限定是已复现缺陷修复，建议保留。
生产切线后的代码回滚顺序与 legacy 删除由 Final 单独说明。
