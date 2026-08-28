# Retrieval GORM Migration Implementation Plan

## 0. Planning Gate

- [x] 读取父GORM设计、Foundation、Workflow、Change Control、Model Settings与Retrieval基线。
- [x] 盘点Repository/Search/Evidence/Delivery/Dispatcher/Completion/Processor/native所有方法与构造点。
- [x] 盘点pgvector、COPY、temp/session lock、River、SQLSTATE、EXPLAIN与500k容量门禁。
- [x] 运行Retrieval unit、race、vet与integration compile baseline。
- [x] 写入Go/API、SQL/native、Composition/tests三份research。
- [x] 独立Go/架构与SQL/事务planning review完成，P1/P2收敛。
- [x] 用户明确批准本次最终规划摘要。
- [x] 执行`task.py start .trellis/tasks/08-19-gorm-retrieval-migration`。

以上门禁完成前不得修改Retrieval业务代码。

## 1. Core Boundary And Scoped Contracts

- [x] 新增single-Pool GORM root/UoW、ready、within/read snapshot与callback/commit stage helper。
- [x] 新增Raw Row/Rows/Exec、strict scanner、JSONB/array/vector/null/time carrier。
- [x] 统一context Cause/no-row/sql.ErrTxDone/PgError classifier与敏感错误脱敏。
- [x] 新增Application parallel scoped Dispatcher/Job与consumer-owned outbox/binding/completion interfaces；
      legacy`any`接口保持不变。
- [x] staged GORM文件无pgx Tx/Row/Rows/pool/protocol数据访问、gorm.Open、root Transaction、DDL、
      AutoMigrate/Migrator；`pgconn`仅用于SQLSTATE错误分类。

## 2. Search, Evidence And Scoped Read

- [x] 实现Active Search Index、Lexical transaction-local threshold和Vector三种固定operator。
- [x] 实现SourceVersion/Span/Citation/Provenance单条与批量查询，保持ordinality、上限和strict mapping。
- [x] 实现caller-owned scoped SourceVersion batch read，拒绝nil、非平台GORM类型和失活scope且不fallback root；
      active foreign-Pool无法识别，由single-Pool Composition与TODO9 fixture保证。
- [x] 所有Rows Close+Err、stable order、Workspace predicate与无N+1检查通过。

## 3. Core Index, Vector And Regression

- [x] 实现Embedding/Index getter/register/transition、BuildLexical、Activate/Rollback。
- [x] 实现SaveVectorBatch、LoadVectorBuildPage、CommitVectorBuildBatch，保持set SQL/readback/CAS。
- [x] 实现RepeatableRead可写Regression：观察与结构失败Index CAS在同一transaction。
- [x] 保持DB time、target/previous排序锁、receipt、single-active与deferred closure。

## 4. Native Allowlist

- [x] 抽取Manifest native transaction，复用metadata/create/replay/CopyFrom/commit事实源。
- [x] 抽取Snapshot pinned connection/session lock/RR/temp stage/COPY/unlock quarantine能力。
- [x] 抽取Source Refresh session lease，保持retry/cancel/idempotent release/hijack-close。
- [x] GORMRepository只通过三个窄interface组合完整Store，普通方法不能fallback legacy Repository。
- [x] 静态pgx inventory记录owner、接口、原因、测试与Final退出条件。

## 5. Delivery And Processor Context

- [x] 实现Claim/Heartbeat/Checkpoint/Fail，保持Delivery->Attempt->DB clock锁序、lease/fence和exact replay。
- [x] 实现RR/read-only ProcessorContext固定integration projection，不新增跨owner mutation。
- [x] callback error、commit response-loss、manual recovery与connection release分类等价。

## 6. Dispatcher And River

- [x] legacy/scoped Dispatcher共享fairness loop；ScopedDispatcher继续实现BatchDispatcher。
- [x] Retrieval scoped River adapter映射ReindexJob到Workflow generic scoped typed inserter。
- [ ] 冻结完整constructor：同Pool内部创建insert-only River client/typed inserter，拒绝legacy
      `options.EnqueueFence`，只接受scoped fence与typed owner collaborators。
- [ ] [BLOCKED] owner concrete交付后，按outbox claim -> workspace lock -> CC binding -> Delivery ->
      River -> publish顺序实现GORM Dispatcher；Retry保持due/FIFO/try-lock与dispatch generation。
- [ ] 缺Workflow/CC concrete scoped collaborator时保持依赖阻塞；不在本child修改owner Adapter、
      不以跨schema mutation或不可装配空壳替代。

## 7. Completion

- [ ] 定义两阶段CC scoped consumer capability与Retrieval-owned completion helper边界。
- [ ] [BLOCKED] Change Control concrete交付后，GORM Completion拥有唯一outer UoW。
- [ ] 保持workspace->proposal->execution->delivery->attempt->sorted indexes锁序与final recheck。
- [ ] activation、attempt/delivery、CC completion全部同scope；任何stage失败全回滚。
- [ ] first/replay/prerequisite/manual recovery/commit response-loss/error code与legacy等价。
- [ ] GORM文件不直接新增Change Control mutation SQL；依赖未满足时不伪造完成状态。

## 8. Static Verification And Review

- [x] `gofmt`仅作用于本任务新增/修改Go文件。
- [x] `go test -mod=vendor ./internal/retrieval/... -count=1 -timeout 60s`。
- [x] `go test -race -mod=vendor ./internal/retrieval/... -count=1 -timeout 60s`。
- [x] `go vet -mod=vendor ./internal/retrieval/... ./internal/platform/postgres`。
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/retrieval/... -count=1 -timeout 60s`。
- [x] `go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s`。
- [x] `go list -mod=vendor ./internal/retrieval/...`、`go mod verify`、`go mod tidy -diff`只检查。
- [x] 静态扫描：无production GORM wiring、第二pool、DDL、动态输入拼SQL、裸slice/JSON bytea、N+1。
- [ ] Go Review、SQL Review、Trellis Check修复P0/P1/P2后重跑门禁。
- [x] `git diff --check`与`task.py validate`。
- [x] 写入`research/static-validation.md`，记录生产仍legacy和TODO9/owner concrete盲区。

## 9. TODO 9 Real PostgreSQL Gate

仅扩展现有integration文件，每个legacy/GORM variant使用独立正式迁移数据库。

- [ ] 一个platform Pool提供GORM/UoW、native capability、Workflow/CC/ModelSettings scoped依赖与River。
- [ ] pgvector三operator、dimension/normalization、JSONB/array、actual GORM SQL与目标EXPLAIN。
- [ ] manifest/snapshot COPY、temp/session lock、lease竞争/cancel/unlock failure与无partial state。
- [ ] Delivery/Dispatcher concurrency、SKIP LOCKED/FIFO、River commit/rollback、pgx worker interoperability。
- [ ] Completion lock/recheck/deferred closure、逐stage fault rollback和commit response-loss exact replay。
- [ ] 真实23505/23503/23514/55000/40001/40P01/55P03、no-row、custom Cause、sql.ErrTxDone。
- [ ] HTTP Search/Evidence和`TestApprovalDispatchRealRiverSafeWritebackSmoke` GORM variant。
- [ ] 外部DSN 500k/384维/HNSW+IVFFlat/30 samples/P95<=2s/recall>=0.95 benchmark。

当前无TODO9/DSN时本Phase全部保持未完成，不得把compile-only或skip当验收。

## 10. Final Handoff

- [ ] 记录API/Worker/Organizing所有constructor替换点、同Pool依赖链与关闭顺序。
- [ ] 记录legacy Repository/Search/Delivery/Dispatcher/River inserter与`any` seam删除清单。
- [ ] 记录Final pgx allowlist静态规则，只保留native COPY/session capability和runtime worker/listener。
- [ ] PRD AC只在TODO9、owner concrete与fault/capacity gates通过后勾选；此前不归档。

## Rollback Point

TODO9前只revert Retrieval staged GORM/Application scoped文件。不得回滚migration、历史Index/Delivery/
Activation事实、native safety逻辑、legacy生产路径或其他owner task的scoped实现。
