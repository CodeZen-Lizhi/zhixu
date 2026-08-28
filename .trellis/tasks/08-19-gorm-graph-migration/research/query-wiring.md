# Research: Graph Query, Composition, Test And Capacity Baseline

- Query: 冻结 Graph Query Port、生产构造、测试夹具、EXPLAIN/benchmark 与 staged GORM 边界。
- Scope: internal
- Date: 2026-08-20

## 1. Executive Conclusion

Graph 的查询不是普通 CRUD。Global、Neighborhood、Path 和 Evidence 都依赖固定 CTE、
稳定顺序、结果窗口与多语句快照。staged GORM Repository 应以 GORM Raw SQL 执行现有
查询形状，并由一个 `RepeatableRead + ReadOnly` Unit of Work 包住完整调用。Path 的端点读取、
双向 BFS 和 hydrate，Neighborhood 的整层展开与 hydrate，都不能各自开启事务。

生产 Composition 仍全部构造 legacy pgx Adapter。本 child 只增加显式 GORM sibling；
TODO 9 后在现有 integration 文件原位参数化 legacy/GORM，每个变体使用独立迁移数据库。
当前 `ZHIXU_TEST_DATABASE_URL` 未配置，真实 PostgreSQL 与容量验收不可执行。

## 2. Query Contract

`graphapp.QueryPort` 包含七个行为：

- `GlobalWindow`
- `SearchNodes`
- `NeighborhoodWindow`
- `FindPath`
- `NodeDetail`
- `RelationDetail`
- `RelationEvidenceWindow`

legacy `Repository` 对每个顶层查询开启 RR/read-only transaction，并通过 transaction-local
`set_config('statement_timeout','1500ms',true)` 限制执行时间。嵌套查询通过
`inReadSnapshot` 标记复用同一 transaction。

不可改变的查询语义：

- Global 与 Evidence 使用 `limit+1`，最大窗口 500，截断原因保持 `RESULT_WINDOW_LIMIT`；
- Search/Global/Neighborhood/Path 的排序与 cursor 输入保持稳定，cursor 仍由 Application 生成；
- depth=1 Neighborhood 同时受 edge/node budget 约束；depth>1 只返回完整层，不能返回半层；
- Path 先验证端点，再分层双向 BFS，最后一次批量 hydrate；同一调用只看一个快照；
- Workspace predicate、Knowledge lifecycle、relation/evidence applicability 和 Domain validation
  继续 fail closed；损坏投影不能降级成空结果；
- `ANY/unnest`、CTE、`DISTINCT ON`、LATERAL、ORDER/LIMIT 均保留固定参数化 SQL；
- 数组输入通过 `pq.Array` 单参数绑定，不能让 GORM 展开裸 slice；数组输出需要
  database/sql 可扫描 carrier，再映射到现有 Domain 类型。

## 3. Adapter Shape

推荐新增 `GORMRepository`，从一个 `*platformpostgres.Pool` 取得 GORM root 与
`foundation.UnitOfWork`。顶层查询进入 `gormReadSnapshot`，以 RR/read-only options 执行，
设置相同 statement timeout，并把同一个 scoped database handle 传给所有内部步骤。

为避免 SQL/领域校验形成第二事实源，实施时应将 legacy 与 GORM 共享的部分收敛为：

- 固定 SQL 常量与包内白名单片段；
- 只依赖最小 Row/Rows/RowsAffected 能力的 scanner/query helper；
- legacy-only pgx adapter 与 GORM/database/sql adapter 分别实现该私有能力；
- GORM 文件不拥有 pgx transaction/pool，不把 pgx 类型暴露到 Application/Domain；
- PostgreSQL `$n` 到 GORM `?` 的转换只针对包内固定 SQL，按出现次数展开复用参数，
  跳过引号/注释并拒绝 `$0`、越界和未使用参数；
- JSONB 与 array 的 carrier 转换集中在一个私有边界。

如果直接复用现有 pgx-shaped helper 会让 staged GORM Repository继续依赖 `pgx.Tx`，则必须先
收窄私有 helper，而不是在 GORM 层伪造 transaction 或打开第二连接。

## 4. Production Composition Inventory

当前生产仍是 legacy：

- API Organizing Claim Search：`cmd/api/main.go:992`；
- API Graph handler：`cmd/api/main.go:1383`；
- API Candidate/Scan handler：`cmd/api/main.go:1404`；
- API Workflow cancellation composite：`cmd/api/main.go:462-466`；
- Worker Graph/Scan/Candidate executor：`cmd/worker/main.go:1374-1423`；
- Worker Workflow cancellation composite：`cmd/worker/main.go:1461-1469`。

Final 才能把这些构造替换为同一个 platform Pool 派生的 Graph、Collection、Workflow、
Change Control 和 River staged 实现。本 child 不修改 `cmd/**`，不增加运行时 selector 或双写。

## 5. Existing Test And Capacity Assets

TODO 9 只扩展现有文件：

- Query behavior：`repository_integration_test.go`；
- Query plan：`plan_integration_test.go`；
- Capacity/statement count：`benchmark_integration_test.go`；
- Candidate：`candidate_repository_integration_test.go`；
- Confirm：`candidate_confirm_integration_test.go`；
- Scan：`scan_repository_integration_test.go`；
- River/cancellation/fault：`scan_river_integration_test.go`；
- Smart Collection：`smart_collection_scan_integration_test.go`；
- downstream approval/apply：`candidate_approval_apply_integration_test.go`。

legacy query fixture 常将 Repository 绑定到 caller-owned、尚未提交的 pgx transaction。
GORM production contract不能复用该 transaction。GORM variant应先提交 seed，再从同一个
`platformpostgres.Pool` 的 GORM/UoW 读取；legacy 与 GORM 变体使用独立数据库或隔离数据，
避免相同 receipt/fingerprint 互相污染。

现有 Make targets：

- `make graph-integration`
- `make graph-smoke`
- `make graph-benchmark`
- `make semantic-link-integration`
- `make semantic-link-fault-smoke`

benchmark 固定 20k Topic、100k Relation、100k Evidence，5 次 warmup、30 个样本、
p95 不超过 1.5 秒，并约束 statement count。EXPLAIN gate拒绝 relation/relation_evidence
关键访问路径退化为 Seq Scan。GORM variant必须使用相同数据与断言，不另造轻量替代门禁。

## 6. Baseline Verification

本轮规划前已通过：

```text
go test -mod=vendor ./internal/graph/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/graph/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/graph/adapter/postgres -count=1 -timeout 60s
```

这只证明 legacy 与 integration build 可编译，不证明 GORM/database/sql placeholder、数组/JSONB、
锁、trigger、SQLSTATE、连接释放、EXPLAIN 或容量行为。

## 7. Risks And Deferred Work

- Foundation scope 无 Pool identity，另一个 active platform Pool 的 scope无法在 Adapter内识别；
  同 Pool 由 staged constructor、Final Composition 和 TODO 9 fixture保证。
- Graph query/fixture 的 test-only pgx seed/tracer 属测试 allowlist，不是 production migration残留。
- GORM 数组 scan 和 positional renderer必须用单元/现有 integration证据验证；不得静默返回零值。
- TODO 9 未提供真实数据库时，任务必须保持 `in_progress`，不能切生产或勾选 AC。

## Related Specs

- `.trellis/spec/backend/database-guidelines.md`
- `.trellis/spec/backend/error-handling.md`
- `.trellis/spec/backend/logging-guidelines.md`
- `.trellis/spec/backend/quality-guidelines.md`
- `.trellis/tasks/08-18-gorm-data-access-migration/design.md`
- `.trellis/tasks/08-19-gorm-platform-transaction-foundation/design.md`
