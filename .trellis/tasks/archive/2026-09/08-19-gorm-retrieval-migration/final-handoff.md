# Retrieval GORM Final Handoff

日期：2026-09-08。该 child 的实现、直接 owner 依赖和按风险精简的实库证据已齐备。
主会话接续 Final 和任务归档；本代理只编辑 internal/retrieval 与本 task，没有改 cmd、migration、
active pointer，没有删除 legacy、commit 或 push。

## 已交付与验证

- 完整 GORMDispatcherStore/NewGORMDispatcher、独立 GORMCompletionRepository。
- 真实 Workflow outbox、CC verifier/两阶段 completion、Model Settings scoped fence、official River SQL producer。
- 现有 Testcontainers fixture 和 legacy/GORM 对照：FTS/hybrid、Search/filter/provenance、Regression、
  Processor Context、Dispatcher River rollback/retry、Completion owner rollback/lease/replay。
- 修复 GORM Delivery.Fail completed_at 的 timestamptz binding，及 shared native Snapshot 增量查询列歧义。
- unit/race/vet 通过。准确命令、耗时、先前失败与未覆盖范围见 research/static-validation.md。

## 同 Pool 构造

| 用途 | 构造/依赖 |
| --- | --- |
| Store、VectorBuildStore、RegressionStore、SourceRefreshLocker | retrievalpostgres.NewGORMRepository(pool) |
| SearchStore、ScopedSourceVersionReferenceBatchStore | retrievalpostgres.NewGORMSearchRepository(pool) |
| DeliveryRuntimePort、ProcessorContextLoader | retrievalpostgres.NewGORMDeliveryRepository(pool, ids) |
| Workflow Outbox collaborator | workflowpostgres.NewGORMReindexOutbox(pool) |
| CC binding/completion collaborator | changecontrolpostgres.NewGORMRepository(pool, ...) |
| CompletionStore | retrievalpostgres.NewGORMCompletionRepository(pool, ccRepository) |
| BatchDispatcher | retrievalpostgres.NewGORMDispatcher(pool, ids, riverOptions, scopedFence, outbox, ccRepository) |

pool 为同一个完整 `*platformpostgres.Pool`，不是 pool.DB()，不另建 DSN/client Pool。
Model Settings 的 scoped fence 必须从这个 Pool 构造并携带其正常 Sealer/Audit 依赖。
riverOptions 保留实际 queue/schema/logger，但 legacy EnqueueFence 必须为 nil；将 scoped fence 作为
单独参数传入。NewGORMDispatcher 内部创建 insert-only client 和 scoped typed producer，保留原 pgx Worker。

**Completion 是独立对象。** GORMRepository 不实现 CompletionStore；Worker 的
NewCompletionService 必须注入上述 GORMCompletionRepository，不能继续传 processing.store。
与旧 Repository 关联的 Source/Vector/Regression/Refresh 则继续使用 GORMRepository。

## Final 接线位置

截至本 child 交接前，生产 Retrieval 调用仍在 cmd/api/main.go 与 cmd/worker/main.go：

- API newOrganizingHandler、newRetrievalHandler、newArtifactCitationVerifier：Search constructor。
- Worker Tool runtime、Conversation/RAG、Workspace Analysis：Search constructor。
- Worker sourceProcessingComponents.store 当前是 *retrievalpostgres.Repository；
  newSourceProcessingComponents 要同步改字段/构造，保持 Store、Vector、Regression、Refresh 的所有消费者。
- Worker newReindexComponents：Delivery/Processor、独立 Completion、Dispatcher。
  legacy reindexriver.NewInserter(insertClient) 由 NewGORMDispatcher 内部 scoped producer 取代。
- Organizing owner 已有 ScopedSourceVersionReferenceBatchStore 消费能力，Final 必须注入同 Pool Search。
- Worker consumer/listener/migrator 的 riverpgxv5 保留；停机保持先停 Dispatcher/Worker，再关唯一 Pool，
  不因 producer 改 database/sql 改变运行时 lifecycle/readiness。

精确行号会随主会话其他 Composition 改动移动；按上述 constructor/函数名搜索再改。
跨模块实库已确认 CC 两次 CAS 成功后回滚、真实 River insert 后回滚，并非只用 fake owner 证明事务。

## Legacy 删除前必须保留的内容

不要整文件盲删。新 GORM 仍复用 legacy 文件中的 SQL、scanner、validator、fact types 或 native helpers。

| 文件 | Final 保留/抽取 | 可删除边界 |
| --- | --- | --- |
| repository.go | beginIndexNative 和它依赖的 metadata/create/replay/COPY 原生 helpers/窄 DB 能力 | 普通 Repository receiver CRUD/transaction 路径 |
| snapshot.go | beginWorkspaceSnapshotNative、pinned session/RR/temp stage/COPY/unlock quarantine，含本次列限定修复 | legacy receiver wrapper |
| source_refresh_lock.go | acquireSourceRefreshNative 与幂等 release/unlock 失败 hijack-close | legacy receiver wrapper |
| native_capabilities.go | 三个窄 interface 及同 Pool native adapter | 不应作为普通 pgx Repository 豁免 |
| search.go / evidence.go | 固定 SQL 模板、operator/filter 白名单、scanner、validation/限额 | legacy SearchDB/NewSearchRepository 与 pgx 执行方法 |
| scans.go | retrievalRowScanner、列常量/纯 scanner、manifest equality、activationIndexSnapshot；beginIndexNative 所需 readback helper | 非 native ordinary pgx 查询 helper，先搜索引用 |
| activation_tx.go | activationLockedIndexes（历史 snapshot helper 在 scans.go） | pgx activate/replay transaction 方法 |
| delivery_runtime.go / delivery_scans.go | fence/checkpoint/status/replay helpers、共享 scanner/constants | legacy DeliveryRepository 与 pgx mutation/read transaction |
| dispatcher.go | ReindexConsumerName、selectedOutbox/writebackBinding、validateOutboxBinding/validatePendingDeliveryReplay、共享错误 | legacy DispatcherDB/constructor 与普通 pgx transaction |
| completion.go | completionIdentity/Proposal/Execution/ReadOnlyFacts、state/fact validators、prerequisite/lease errors | legacy completion transaction/read helpers |
| processor_context.go / regression.go / vector_build.go | GORM 引用的纯 contract/validation/result helpers | 普通 pgx 数据访问；删除前逐个核对引用 |
| adapter/river/inserter.go | 共用 Args/ValidateArgs/receipt 所在内容按引用保留 | legacy any/pgx producer；Worker runtime 不删除 |
| application/dispatcher.go | fairness loop、BatchDispatcher、options/共享 nil helper | 最后调用方切换后删除 legacy any JobInserter/Dispatcher seam |

Final 必须同步现有测试构造，不允许用 legacy fallback 保住编译。真实基线证据在本任务文档中保留。
本 child 没有替 Final 删除或重命名这些文件。

## pgx 最终 allowlist

Retrieval 仅三个生产数据能力：

1. Manifest：metadata 与 CopyFrom 同一 native transaction。
2. Workspace Snapshot：同一 pinned session、advisory session lock、RR、temp stage、COPY、原 session unlock；
   unlock 失败 hijack-close。
3. Source Refresh lease：同一 session 跨多个业务 transaction 持锁，取消/释放保持幂等与淘汰策略。

普通 pgvector、JSONB/array、Raw SQL、事务 advisory lock、River producer 不构成 pgx 例外。
平台 pgvector 注册与 River worker/listener/migrator 是独立 runtime 例外，不能扩展到普通 Repository。
pgconn 仅作 SQLSTATE/cause 类型检查；最终扫描应按数据访问行为区分。

## 风险、后续与回滚

- Foundation scope 无 active foreign-Pool identity 校验，同池构造是 Final 必须维持的条件。
- 未运行容量、完整故障/SQLSTATE/连接释放、HTTP 和真实 Worker 进程矩阵；不宣称这些 gate 通过。
  capacity fixture 的 Workspace test seed 与清理 marker 还需在未来容量任务中一起适配。
- 本 child 没有修改 migration/schema 或持久历史事实；可以按文件撤销 staged 实现与 fixture 接入。
  Delivery timestamp 和 Snapshot 列歧义修复已有实库证据，回滚迁移编排时建议保留。
- Final 只在本 child 文件不再被修改后开始 legacy 清理，并对实际生产 Composition 做必要的局部编译/校验。
