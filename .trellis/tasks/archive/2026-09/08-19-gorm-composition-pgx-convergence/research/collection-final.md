# Collection Final 收口

## 2026-09-08 最终状态

主会话确认七个模块核心 gate 已通过并放行 Final 清理后，完成 Collection 单一 GORM 实现。生产与集成测试都不再有旧 Repository、pgx verifier、伪 pgx Query/Rows/CommandTag、SQL lexer 或 `shiftWhere`。构造器继续为 `NewGORMRepository(*platformpostgres.Pool)`，调用方事务验证继续为 `VerifyDurableScanBindingScoped`。

### 实现

- `gorm_models.go` 显式映射 Atlas 的 `learning.smart_collection` 与不可变 command receipt，保留 nullable 指针、JSONB、版本及时间列，关闭自动时间，没有 AutoMigrate、关联写或 Hook。
- Create 使用 GORM `Create`；Update/Archive 使用 Workspace/ID/version 条件和 `Updates(map)`，保留零值并检查 `RowsAffected == 1`。原 Workspace → receipt → aggregate 锁序、精确重放、历史快照、query_version 及归档不可变语义保持。
- Get/List/Search 直接使用 GORM 查询、显式列、行锁和有界分页。原 active-name 排名与 `updated_at DESC,id DESC` 稳定顺序保持。
- Query AST 原编译器输出 `@queryN` 命名参数 map，CTE、keyset、limit、durable pair 和 hydration 分别绑定固定参数名；没有外部 SQL 拼接或占位符重写。唯一 Collection 外调用方 Organizing 仅使用 Canonical 定义/hash，不使用参数载荷。
- `query.go` / `durable_scan.go` 直接使用 `*gorm.DB`；重复参数由 GORM 原生 NamedExpr 绑定，数组使用既有 `pq.Array`，typed cast 参数统一写作 `(@name)::type`。
- JSONB 使用显式 Scanner；可选 applicability 使用指针；durable node arrays 使用 `pq.StringArray` 和文本输出。原完整性、重复行、成员/节点范围检查保留。
- 结果页仍在一次 repeatable-read/read-only UoW 内完成 timeout、单次 O(1) revision、count、limit+1 page 和一次批量 hydration。Health 摘要影响结果游标；仅 Health 参与成员定义时影响 durable binding。
- 既有测试原位改为最终 GORM fixture。pgx 仅在 `_test.go` 中 seed/检查实库。旧 renderer 测试改为 GORM 多位/重复参数顺序以及查询边界非法输入检查；没有新增测试文件、临时测试程序、skip 或隐藏构建标签。

### 验证

全部定向实库命令使用 `-mod=vendor -tags=integration -count=1 -timeout=60s`，项目 Testcontainers fixture 为 fail-on-unavailable。

| 已执行场景 | 结果 |
| --- | --- |
| LifecycleIdempotencyCASAndWorkspaceIsolation + QueryExecutesEveryRegisteredFieldOperator | PASS，25.054s |
| QuerySnapshotCountAndReferenceP95 + QueryTimeoutClassificationAndConnectionReuse | PASS，25.901s |
| DurableScanRestartsWithStructuredKeysetAndFailsClosedOnDrift + DurableScanIgnoresOwnHealthOutputsUnlessHealthDefinesMembership | PASS，26.262s |
| NullableConfidenceKeysetTraversesNullTail | PASS，13.616s |
| HTTPExactReplayAfterMutationAndArchive | PASS，13.673s |
| QueryPlanUsesCanonicalIndexes | PASS，9.302s |

执行命令：

```bash
go test -mod=vendor -count=1 -timeout=60s ./internal/collection/...
go test -mod=vendor -tags=integration -run '^$' -timeout=60s ./internal/collection/adapter/postgres ./internal/collection/http
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^(TestCollectionRepositoryLifecycleIdempotencyCASAndWorkspaceIsolation|TestCollectionQueryExecutesEveryRegisteredFieldOperator|TestCollectionQueryNullableConfidenceKeyset)$' ./internal/collection/adapter/postgres
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^(TestCollectionQuerySnapshotCountAndReferenceP95|TestCollectionQueryTimeoutClassificationAndConnectionReuse)$' ./internal/collection/adapter/postgres
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^TestCollectionDurableScan(RestartsWithStructuredKeysetAndFailsClosedOnDrift|IgnoresOwnHealthOutputsUnlessHealthDefinesMembership)$' ./internal/collection/adapter/postgres
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^TestCollectionNullableConfidenceKeysetTraversesNullTail$' ./internal/collection/adapter/postgres
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^TestCollectionHTTPExactReplayAfterMutationAndArchive$' ./internal/collection/http
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^TestCollectionQueryPlanUsesCanonicalIndexes$' ./internal/collection/adapter/postgres
go vet -mod=vendor ./internal/collection/... ./internal/export/...
git diff --check -- internal/collection internal/export
```

第一条实库分组中的旧 nullable 名称未匹配，随后已单独执行实际 NullableConfidenceKeysetTraversesNullTail 用例。查询次数/P95 首次运行暴露 GORM logger 的 display-only Explain 将 `$1` 格式化为 `$1$`，导致旧字符串识别断言失败；tracer 现从 ParamsFilter 记录真实参数化 SQL，再由真实 UoW delegate 记录成功 begin/commit。没有更改产品 SQL 或放宽 7/8 次 statement、单次 revision、25 个样本 P95 < 2s 的断言。EXPLAIN 使用生产 page/List/Search builder 生成的 SQL/Vars，在同一真实事务内执行，保持目标索引和禁止退化 SeqScan 的断言。

### Go / SQL review

按 go-review 与 sql-code-review 自审：检查直接调用方、参数化、显式映射、零值、nullable/JSONB、Workspace 边界、锁序/CAS、UoW 生命周期、稳定 keyset、limit+1、批量 hydration、错误分类、取消 cause 和 rows 关闭。没有发现剩余范围内的明确缺陷。

GORM 官方 SQL builder/update 文档和锁定 vendor 均已核对：NamedExpr 不以冒号终止名称，必须使用 `(@query1)::uuid`；`Updates(map)` 保留显式零值。未引入新依赖或自研基础设施。

### 集成与风险

- 主会话负责 cmd 的 GORM 接线及 Graph/Health scoped verifier 集成；已告知 cmd 的两个 health_smart_collection composition integration fixture 仍需同步构造器。
- 没有 Schema、HTTP wire、历史数据或依赖变更。回滚应与 Final Composition/调用方按依赖闭包恢复，不能单独恢复已删除的跨模块 pgx verifier。
- 本轮没有全仓测试、生产容量/时延测试、浏览器运行、部署、commit/push/archive 或 active pointer 修改。EXPLAIN 的 `enable_seqscan=off` 只证明索引可选；参考 fixture 的 P95 不是生产容量承诺。
