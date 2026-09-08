# Graph GORM Final Handoff

日期：2026-09-08。Graph 已完成父任务 2026-09-01 精简政策要求的主查询、Candidate/Confirm、Scan 跨 owner 原子性实库门禁及定向 Go/SQL Review。此记录只交接 Graph child；生产接线、legacy 删除、父任务 AC 与归档由主会话和 Final 拥有。

## 已验证构造

三个现有 integration 场景均用 `testdb.Require` 创建迁移后的独立 PostgreSQL，先从 `Fixture.Pool().DB()` 提交 seed，再从同一个完整 `platformpostgres.Pool` 构造 GORM。没有将旧 fixture 的未提交 pgx transaction 注入 GORM。

| 能力 | Final 构造 |
| --- | --- |
| 七个 Graph Query、Candidate command/query | `NewGORMRepository(pool)` |
| Candidate Confirm | `NewGORMCandidateConfirmRepository(pool, changeControlGORM, ids, clock)`；Change Control 实现 `candidateconfirm.ScopedKnowledgeProposalPort` |
| API Scan Start/State | `NewGORMSemanticLinkScanRepository(pool, workflowGORM, collectionGORM, ids, clock)` |
| Worker Scan State | `NewGORMSemanticLinkScanStateRepository(pool)` |
| Topic planner/page | `NewGORMSemanticLinkTopicScanPlanner(pool)` / `NewGORMSemanticLinkTopicScanPageRepository(pool)` |
| Smart Collection planner/page | `NewGORMSmartCollectionScanPlanner(collectionService)` / `NewGORMSmartCollectionScanPageRepository(pool, collectionService)`；Service 使用同池 Collection GORM Repository |
| Discovery Candidate writer | `NewGORMSemanticLinkDiscoveryCandidateWriter(graphGORM, ids, clock)`；复用已构造的 Graph Repository |
| Workflow cancellation | Graph `GORMRepository.SafeToCancelWorkflowNodeScoped`，注册到 `NewCompositeScopedCancellationSafetyGuard`；无需单独的 GORM guard 构造器 |

同池构造顺序：Pool → Audit/Events、Model Settings scoped enqueue fence、Change Control、Collection、Graph → scoped cancellation composite → Workflow GORM Runtime → Graph Scan。Graph Repository 自身不依赖 Workflow，可先创建后注册 guard，避免构造循环。Workflow 使用 `NewGORMRuntimeRepositoryWithHooks(pool, riverOptions, settings, hooks)`；`riverOptions.EnqueueFence` 的 legacy 字段必须为空，事务入队由 scoped producer 负责。Worker/listener 继续使用同池的官方 pgx River driver。

生产 API/Worker 应先停止接收新工作并停止 River Worker，待事务结束后由 Pool owner 调用 `Pool.Close()`；现有实现先关闭 database/sql facade，再关闭物理 pgxpool。Graph Adapter 不关闭共享 Pool。

## 生产替换点

下列位置基于本次读取的 `cmd` 文件；Final 编辑时以函数名重新定位，行号只作检索锚点。

| 入口 | 现有位置与替换要求 |
| --- | --- |
| API Organizing Claim Search | `cmd/api/main.go:992` 的 Graph `NewRepository` 改用完整 Pool 构造 GORM |
| API Graph | `cmd/api/main.go:1383` 的 `newGraphHandler` 改收完整 Pool，保留 cursor/service/handler 与独立 readiness |
| API Candidate/Scan | `cmd/api/main.go:1404` 的 `newCandidateHandler` 增加同池 scoped Change Control 与 Collection 依赖；Confirm 不能调用 root Proposal create；Scan 不能调用 legacy `StartTx` |
| API cancellation | `cmd/api/main.go:463` 的 legacy composite 改用 scoped composite，以 Graph GORM Repository 替换 `NewSemanticLinkScanCancellationGuard()` |
| Worker discovery | `cmd/worker/main.go:1374`–`1423` 的 Graph/State/Topic page/Smart page/writer 全部切到上表构造；保留 Topic v1 和 Smart v2 executor 注册 |
| Worker cancellation | `cmd/worker/main.go:1465` 的 composite 同步切 scoped，复用该 Worker 的 Graph GORM Repository |

## Legacy 清理清单

Graph 没有要求新增业务 pgx allowlist。Final 必须同时处理旧 Repository 和 GORM 的兼容接口，不能仅删除文件名不带 `gorm_` 的文件。

| 范围 | 删除或收窄 | 必须保留的共享内容 |
| --- | --- | --- |
| `repository.go` 与 legacy Query receivers | `Repository`、`NewRepository`、`transactionBeginner`、pgx read transaction helper | timeout 常量、固定 Query SQL、scanner、filter/排序/BFS 纯 helper |
| `candidate_repository.go` | legacy receiver/transaction helper、pgx-shaped DB 参数 | upsert result、Candidate/Decision record、SQL、批量 hydration、canonical/receipt/CAS 校验与错误码 |
| `candidate_confirm.go` | `CandidateConfirmDB`、旧构造/receiver、直接跨 schema 写事务 | `buildKnowledgeChange`、Proposal binding 与 v1/v2 replay 校验、纯错误/nullable helper |
| `scan_repository.go` | `SemanticLinkScanRuntimeStarter.StartTx`、legacy Scan 构造/receiver | Scan columns、scope/request/replay 校验、Workflow definition/key、checkpoint/error codec、scanner |
| `scan_cancellation.go` | legacy `transaction any` → `pgx.Tx` guard | staged `SafeToCancelWorkflowNodeScoped` 是替代者 |
| `scan_planner.go`、`topic_scan_source.go`、`smart_collection_scan.go`、`discovery_candidate_writer.go` | legacy constructor/receiver 与 db 查询路径 | Topic pair SQL、discovery node/pair/evidence helper；GORM Smart planner 当前仍委托纯 `SmartCollectionScanPlanner`，需先抽取共享函数再删除旧 type |
| `gorm_adapter.go` | `gormDB` 对 `DB` 的 pgx Rows/CommandTag 返回类型、`gormDBRows` 的 `CommandTag/FieldDescriptions/Values/RawValues/Conn` 兼容方法 | `Scan/Next/Err/Close`、RowsAffected、array/JSONB carrier、参数 renderer；收敛为 database/sql 或私有中性接口 |
| `gorm_core.go`、`gorm_adapter.go`、`gorm_candidate_confirm.go`、`errors.go` 与保留的 shared classifier | `pgx.ErrNoRows`、`pgconn.PgError` 直接依赖 | `sql.ErrNoRows`/GORM no-row 语义与平台 SQLSTATE/constraint helper，保持 `errors.Is/As` 和稳定错误类别 |

integration seed、故障注入和 `internal/graph/testfixture/**` 的 pgx 是测试工具；该目录的实际 fixture/CLI 文件带 `integration` build tag，不可误称为普通生产 Repository。Final 若删除旧测试构造，必须原位迁移仍在使用的 fixture；不得把新主路径场景删掉以通过编译。

## 验证证据与边界

完整命令/结果见 [static-validation.md](research/static-validation.md) 的 2026-09-08 记录。

- GlobalWindow：canonical Knowledge 投影、排序/计数、Workspace 隔离通过。
- Candidate/Confirm：两条 Evidence 的 JSONB/array 批量写入、fingerprint replay、列表 hydration；全部写入后注入回滚时 Proposal/Revision/Decision 均为零，Candidate 未变；成功时仅一套事实，绑定初始 Revision，精确重放和异请求冲突通过。Confirm 不直接创建 Relation。
- Smart Scan：真实 Collection verifier、Model Settings fence、Workflow/Outbox/River/Scan 的提交与回滚；精确重放、Workspace 隔离、分页、并发 Advance 仅一次成功、scoped cancellation 与 Collection drift 通过。
- Graph 普通测试、vet、gofmt、task validate、`git diff --check` 通过。此轮没有新增测试文件或修改生产代码，沿用并验证已有在途改动。

未重新执行全量 BFS/CTE、v1/null-pointer 历史回放、真实网络 commit response-loss、完整 SQLSTATE/取消/连接释放矩阵、EXPLAIN、容量 benchmark、integration race、HTTP/browser/Compose。未改变查询形状、索引或上述故障机制，按精简政策保留为风险触发专项；不把它们记为 PASS。

Foundation scope 仍无运行时 Pool identity，必须由上述 Composition 保证同池。Smart Collection page 与 Graph exclusion 仍是两个已有快照边界。正式 Knowledge Approval/Apply 继续归 Knowledge owner，该 owner 的完整验收和所有生产切换不由本记录代替。

回滚保持 Schema 和历史事实：Final 切换前可撤回 Graph staged Adapter；切换后由 Final 恢复对应 Composition/Adapter 发布版本，不能删除 Candidate/Proposal/Scan 历史或回滚 migration。
