# Retrieval staged GORM 静态验证

- 日期：2026-08-21
- 范围：Retrieval Index/Embedding、Vector、Regression、Search/Evidence、Delivery、Processor Context、
  scoped Dispatcher/River contracts、native pgx allowlist 及本任务工件
- 结论：ordinary staged GORM sibling 与窄 native capability 已通过局部静态门禁；Dispatcher/Completion
  owner concrete、真实 PostgreSQL TODO 9、生产 Composition 和 legacy 删除保持未完成。

## 实现边界

- `GORMRepository`、`GORMSearchRepository` 和 `GORMDeliveryRepository` 只从一个完整
  `platformpostgres.Pool` 取得 GORM root、`foundation.UnitOfWork` 及批准的 native capability；没有
  `gorm.Open`、第二连接池、root transaction、Schema 管理、双写、shadow read 或 legacy fallback。
- ordinary 路径使用参数化 GORM Raw/Exec，保持 DB time、锁序、CAS、exact replay、callback/commit stage、
  RR/read-only snapshot、Rows Close/Err、Workspace predicate、集合写入和 Domain validation。
- Search 使用受控 `$n -> ?` renderer 复用固定 SQL；数组经 `pq.Array` 单 binding，JSONB Valuer 返回
  validated string，向量使用 pgvector Scanner/Valuer，距离操作符只来自固定三值枚举。
- Application 新增 additive scoped Dispatcher/Job、Workflow outbox、Change Control binding/completion 和
  SourceVersion batch read contracts；legacy `any`/pgx contracts 及 production constructors 保持不变。
- Retrieval scoped River adapter只映射 Reindex job 到 Workflow official database/sql typed inserter；
  Worker/listener/migrator 仍由既有 `riverpgxv5` runtime 负责。

## Native pgx allowlist

| Capability | Owner/interface | 保留原因 | 当前验证 | Final 退出条件 |
| --- | --- | --- | --- | --- |
| Manifest COPY | Retrieval `retrievalManifestCopier` | metadata、manifest `CopyFrom` 与 commit 必须同一 pgx transaction | 复用 legacy helper，unit/race/compile 通过 | database/sql/GORM 能在同事务提供等价高吞吐 COPY，或产品移除 COPY |
| Workspace Snapshot | Retrieval `retrievalSnapshotBuilder` | pinned connection、session advisory lock、RR、temp stage、COPY 与 unlock quarantine | 复用 legacy helper，静态生命周期对照通过 | 出现可证明同一物理 session 的平台抽象并通过容量/故障门禁 |
| Source Refresh Lease | Retrieval `retrievalSourceRefreshLocker` | session lock 跨多个业务事务存活，release 失败需 hijack-close | 复用 legacy helper，unit/race/compile 通过 | 平台提供等价 session lease 与失败连接淘汰能力 |

新 GORM 文件不使用 pgx Tx/Row/Rows/pool/protocol 做 ordinary 数据访问；`pgconn`仅用于 SQLSTATE
分类并保留可 `errors.As` 的原始 cause。Final 另行保留平台 River worker/listener/migrator 例外，不能据此
放宽 Retrieval Repository allowlist。

## Review 结果

1. **P2 已修复**：`LoadSourceVersionReferencesScoped` 初版只有 Adapter 方法，没有 Application Port；现已新增
   additive `ScopedSourceVersionReferenceBatchStore` 和编译期断言，Organizing 后续可仅依赖 Application contract。
2. Go Review 未发现 P0/P1；UoW、typed-nil、context cause、Rows、CAS、native fallback 与 production wiring
   静态检查通过。
3. SQL Review 未发现 P0/P1 数据访问缺陷；Workspace 条件、Delivery -> Attempt -> DB clock、Search/Processor
   RR、Regression 可写 RR、数组/JSONB/vector carrier 与固定动态 SQL 白名单均与 legacy/设计一致。
4. **未关闭的静态覆盖缺口**：`renderSearchGORMPositional` 尚无直接表驱动测试，重复/乱序 marker、quoted
   literal/comment/dollar quote 与 malformed marker 目前只有代码审查和包级编译证据。按本轮“不新增测试”
   边界未改测试文件；后续获准时应在现有测试文件补覆盖，真实 SQL binding 仍由 TODO 9 验证。
5. Trellis Check 未发现新的 P0/P1/P2；确认普通 GORM 路径无 pgx 数据访问、production 仍为 legacy、
   Dispatcher/Completion 依赖阻塞和 TODO 9/PRD 状态均与任务边界一致。

## 已执行门禁

- `go test -mod=vendor ./internal/retrieval/... -count=1 -timeout 60s`：PASS
- `go test -race -mod=vendor ./internal/retrieval/... -count=1 -timeout 60s`：PASS
- `go vet -mod=vendor ./internal/retrieval/... ./internal/platform/postgres`：PASS
- `go test -mod=vendor -tags=integration -run '^$' ./internal/retrieval/... -count=1 -timeout 60s`：PASS（仅编译）
- `go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s`：PASS（仅编译）
- `go list -mod=vendor ./internal/retrieval/...`：PASS
- `go mod verify`：PASS
- `go mod tidy -diff`：非零，仅显示任务开始前已有的全仓 `go.sum` 规范化漂移及 SQLite checksum；
  未应用输出，未由本任务修改依赖文件
- 禁用模式、native pgx data-access 和 production wiring 扫描：PASS
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-retrieval-migration`：PASS，
  仅有大规格/research 文件注入截断警告
- 受影响 Go 文件 `gofmt -d`、已跟踪和未跟踪文件 whitespace 检查、全局 `git diff --check`：PASS

## 未完成门禁

`ZHIXU_TEST_DATABASE_URL` 未配置。compile-only 与现有 legacy 测试不能证明 GORM 在真实 migrated
PostgreSQL 上的行为。TODO 9 仍需在现有 integration 文件中用独立 legacy/GORM disposable database 验证：

- pgvector 三种 operator、dimension/normalization、JSONB/array 与实际 GORM placeholder binding；
- Manifest/Snapshot COPY、temp/session identity、Source Refresh lease 竞争、cancel、unlock quarantine；
- Delivery reclaim/lease/CAS、Dispatcher SKIP LOCKED/FIFO/fairness、River commit/rollback 与 pgx Worker 互操作；
- Completion 跨 owner 锁序、逐阶段 rollback、deferred closure 和 response-loss exact replay；
- 真实 23505/23503/23514/55000/40001/40P01/55P03、no-row、custom cause、`sql.ErrTxDone`；
- GORM Raw EXPLAIN、连接释放，以及 500k Chunk/384 维/30 samples/P95/recall 容量门禁。

Workflow outbox 与 Change Control binding/completion 的 concrete scoped Adapter 尚未交付，因此完整
GORM Dispatcher/Completion 仍明确阻塞；本 child 没有跨 owner 重写 SQL 或提交空壳实现。

Foundation scope 当前不携带可验证的 Pool identity，staged adapters 无法独立拒绝另一 active Pool 的
scope。Final Composition 和 TODO 9 fixture 必须从同一个 `platformpostgres.Pool` 派生 Retrieval、Workflow、
Change Control、Model Settings、River 与 UoW。

本任务继续保持 `in_progress`，PRD AC、TODO 9、owner concrete 与 Final handoff 未勾选，不归档，也不切
production Composition。
