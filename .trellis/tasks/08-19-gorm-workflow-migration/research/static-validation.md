# Workflow GORM staged 静态验证记录

日期：2026-08-20

## 结论

Workflow staged GORM 已覆盖 scoped Application Port、Workspace Analysis execution fence、legacy-compatible Repository、Runtime Start/State/Output，以及同事务 River database/sql producer。生产 `cmd/api`、`cmd/worker` 和跨 owner 调用仍使用 legacy pgx 路径；未修改 migration、测试或生产 Composition，未增加 selector、双写或 fallback。

本阶段不能完成 child：`ZHIXU_TEST_DATABASE_URL` 未配置，TODO 9 的真实 PostgreSQL 等价门禁未执行；Model Settings scoped enqueue fence 和跨 owner scoped Start/Hook 仍是生产切换前置。PRD AC、任务完成状态和归档保持未完成。

## 实现边界

- Application 新 Port 只公开 `foundation.TransactionScope` 与纯值 Request/Snapshot；不公开 GORM、`database/sql` 或 pgx，legacy `StartTx` 和 `any` Hook 保留。
- `GORMRepository`、`GORMRuntimeRepository` 和 execution fence 均从一个 `platformpostgres.Pool` 派生；caller-owned scoped 方法不 begin、commit、rollback、缓存 scope 或 fallback root。
- Runtime factory 从同一 Pool 创建 insert-only River client 与 `ScopedJobInserter`，拒绝 legacy fence 混入，并强制非 nil/typed-nil scoped enqueue fence；底层 scoped producer 同样不允许绕过 fence。
- Runtime 保持 Claim 的两个 DB time、固定锁序、CAS、exact replay、Human expiry 先提交后返回冲突，以及 Start commit-response-loss recovery。
- River worker、listener 和 migrator 继续使用 `riverpgxv5`；database/sql driver 仅用于当前 `*sql.Tx` 中的 job insert。
- GORM SQL 全部参数化；JSONB 使用 string Valuer 或 `::text` scan，多行读取检查 Scan、`Rows.Err` 与 Close。
- 新文件不使用 AutoMigrate、Migrator、Preload、Association、Save、独立连接池或生产 GORM wiring；execution fence 不访问 Agent 表。
- `ScopedRuntimeBindingReader` 返回同一 Workspace-bound Run -> Definition -> Node closure 的 raw `run.input`/`node.input`/`definition.graph` JSON；Attempt 仅报告归属存在性，不解码或 canonicalize 调用方数据。

## Review finding / fix ledger

1. Runtime Output readiness 曾错误依赖 UoW/River，导致只读投影在无 producer 时不可用；已收窄为只校验共享 GORM root。
2. `classifyGORMWorkflow` 曾对未显式分类的 PostgreSQL 错误只保留类型摘要，导致 `55000`、`55P03`、`57014` 等 SQLSTATE cause 丢失；现所有 `*pgconn.PgError` 均保留 cause，稳定 kind/code 不变，非 PostgreSQL 未知错误仍使用安全类型摘要。
3. scoped River producer 曾允许 nil fence；现构造和 InsertTx 都强制 scoped fence，禁止绕过 Model Settings rollout drain。
4. 非法/失效 scope 曾映射为 invalid input 或替换底层 cause；现 Workflow fence/UoW/River 均映射 dependency unavailable、可重试并保留 cause。
5. Control/Human 的 UoW begin 失败曾缺少 legacy 操作级 transaction code；现分别恢复 `WORKFLOW_CONTROL_TRANSACTION_FAILED`、`HUMAN_TASK_TRANSACTION_FAILED` 和 `HUMAN_SUBMIT_TRANSACTION_FAILED`。
6. 直接取消节点曾绕过统一 Terminal Hook 包装；现非 Foundation hook error 统一映射 `WORKFLOW_TERMINAL_HOOK_FAILED` 并触发同一 UoW 回滚。
7. Node 多行锁查询的 scan 失败曾未合并 `Rows.Err`/Close error；现错误与正常路径都显式收敛资源错误。

独立 Go Review、SQL/事务 Review 与 Trellis Check 复核后未发现剩余 P0/P1/P2。

## 已执行门禁

以下命令均通过：

```text
go test -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/workflow/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/workflow/... -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/artifact/... ./internal/conversation/... ./internal/changecontrol/... ./internal/health/... ./internal/graph/... ./internal/organizing/... -count=1 -timeout 60s
go list -mod=vendor ./internal/workflow/... ./internal/platform/postgres ./cmd/api ./cmd/worker
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workflow-migration
gofmt -d <Workflow staged Go files>
git diff --check
```

静态搜索确认：无禁止 ORM 模式、debug log、GORM production wiring、execution fence 的 Agent 表访问，以及 GORM Runtime 对 legacy Hook/`JobInserter(any)` 的调用。Application scoped 文件中的 `any` 仅存在于私有 typed-nil helper，不属于公开 API。

`go mod tidy -diff` 只读执行并返回 1，输出为任务开始前已有的全仓 `go.sum` 规范化差异并建议补充 SQLite checksum；未应用输出，也未由 Workflow child 修改依赖文件。

## 2026-08-21 scoped runtime binding 增量

Artifact GORM 的 scoped Generation 前置要求 Workflow 交付 durable binding reader。新增 `application.ScopedRuntimeBindingReader`、纯值 query/snapshot，以及 `NewGORMRuntimeBindingReader`。实现以 caller-owned `foundation.TransactionScope` 解封同一 GORM transaction，并按 `workspace_id + run.id + node.id` 查询 `workflow.run/definition/node_run`；不存在返回 `WORKFLOW_RUNTIME_BINDING_NOT_FOUND`，无效或过期 scope 按现有 Workflow scoped 语义返回可重试的 `WORKFLOW_RUNTIME_BINDING_TRANSACTION_UNAVAILABLE`，损坏持久事实返回一致性错误。节点 schema version 的历史 NULL 被明确拒绝，不让驱动 Scan 错误掩盖一致性语义。

本增量已通过：

```text
go test -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/workflow/...
go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^$' -count=1 -timeout 60s
```

## 生产与测试 wiring

- 生产仍由现有 `NewRepository`、legacy `RuntimeRepository`、`StartTx(pgx.Tx)`、legacy Hook 和 pgx River worker/listener/migrator 构造；`cmd/**` 没有调用本 child 的 GORM 构造器。
- Artifact、Conversation、Change Control、Health、Graph 仍使用 legacy caller-owned `StartTx`；Organizing 与其他 owner 的 terminal/control/cancellation Hook 也未在本 child 切换。
- 现有 Workflow `*_test.go` 没有引用 `NewGORMRepository`、`NewGORMRuntimeRepositoryWithHooks` 或 `NewGORMWorkspaceAnalysisExecutionFence`。unit/race 证明旧行为和编译不回归，不是新 GORM SQL 的运行证据。
- integration `-run '^$'` 只证明测试可编译；TODO 9 将原位参数化现有 fixture，不新增平行事实源。

## 未完成与风险

1. 当前环境没有 `ZHIXU_TEST_DATABASE_URL`，因此没有真实 PostgreSQL 证据证明 GORM placeholder/cast、锁等待、DB time、SQLSTATE、trigger、连接释放和执行计划。
2. 尚未验证 Workflow/Node/Outbox/River job 同 commit/rollback、并发 exact replay、commit response-loss，以及 database/sql producer 写入后 pgx worker 消费。
3. Model Settings 尚未交付 `ScopedEnqueueFence`，因此 GORM Runtime 不能接生产，也不得使用 no-op fence。
4. Artifact、Conversation、Change Control、Health、Graph、Organizing 等 owner 尚未全部交付 scoped Start/Hook，跨 owner 原子性仍需各 child 与 TODO 9 验证。
5. Foundation `TransactionScope` 不携带 Pool affinity identity。当前能拒绝 nil、非平台和 stale scope，但无法识别另一个 Pool 的 active scope；同池由 Composition 与 TODO 9 fixture 保证，严格 cross-pool rejection 需 Foundation 后续能力。

## 2026-08-25 Tool policy / recovery fence 增量

本次恢复审计确认 `ScopedToolExecutionPolicySnapshot`、`ScopedToolCallRecoveryFence` 及两个 GORM Adapter
文件已经存在，但此前没有任何测试引用，也没有真实 PostgreSQL 证据，不能按“文件存在”视为完成。审计与修复结果：

1. `classifyGORMWorkflow` 曾把 caller cancel 统一归为 `dependency_unavailable/retryable=true`；现按后端错误合同固定为 cancel -> `non_retryable_failure/false`、deadline -> `retryable_failure/true`，并同时保留 context sentinel 与 distinct custom cause。
2. 非 PostgreSQL未知错误曾只保留类型摘要，导致原始 cause chain 丢失；现由安全 `foundation.Error.Error()` 隐藏文案，同时通过 `Unwrap` 保留原错误。`23505/23503/23514/22P02/40001/40P01` 与未知 SQLSTATE 的既有 kind/code/retryability 不变。
3. recovery binding preflight 已找到目标但 Node 或 Attempt 被 `SKIP LOCKED` 跳过时，Result 曾返回 `Found=false,Skipped=true`；现固定 `Skipped => Found`，与 missing/mismatch 的零 Result 明确区分。
4. 现有 integration fixture 首次运行触发 `uq_workspace_single_active`，原因是 mismatch fixture 插入了第二个 active Workspace；修为真实合法的 inactive Workspace 后重跑通过，没有放宽生产约束。

新增测试仍原位放在既有 `runtime_start_test.go` / `runtime_start_integration_test.go`，未创建平行测试文件。真实 PostgreSQL用例使用工作区专用 pgvector/PostgreSQL 18 容器作为管理库，每次创建、迁移并删除唯一 disposable database；legacy Runtime 仅负责提交合法 execution fixture，两个待验收 Adapter、UoW 与 pgx 断言共享同一个 `platformpostgres.Pool`。

真实用例已验证：

- policy 返回完整 Definition/Run/Node/Attempt/DB-time raw snapshot，四个目标行均持有 `FOR SHARE`；Workspace/Run/Node/Attempt 四类 existing-but-mismatched binding 均返回零快照、`found=false,nil`；
- recovery healthy execution 返回 `Found=true,Skipped=false,Stale=false`，持有 Node 与 Attempt `FOR UPDATE` 且不锁 Run；分别竞争 Node/Attempt 时立即返回 `Found=true,Skipped=true`，不读取 DB time；匹配但过期的租约返回 `Stale=true`；
- nil context、invalid request、foreign/stale scope、两个 Port 的 custom cancel cause，以及 policy 锁等待的真实 `55P03` 均保持稳定 error kind/code/retryability 和底层 cause。

本增量执行并通过：

```text
go test -mod=vendor ./internal/workflow/adapter/postgres -run '^(TestClassifyGORMWorkflowPreservesContextContract|TestClassifyGORMWorkflowPreservesSQLStateAndUnknownCause|TestStaleGORMToolCallExecution)$' -count=1 -timeout 60s
go test -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/workflow/...
go vet -mod=vendor -tags=integration ./internal/workflow/adapter/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/workflow/... -count=1 -timeout 60s
ZHIXU_TEST_DATABASE_URL=<disposable task PostgreSQL> go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestGORMToolExecutionPolicyAndRecoveryFences$' -count=1 -timeout 60s
ZHIXU_TEST_DATABASE_URL=<disposable task PostgreSQL> go test -race -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestGORMToolExecutionPolicyAndRecoveryFences$' -count=1 -timeout 60s
go mod verify
gofmt -d <本增量 Workflow Go files>
git diff --check
```

本增量已按 Go Review 与 SQL/事务 Review 检查参数化、Workspace/父子 binding、锁序、事务归属、context/cause、SQLSTATE、查询上界和目标主键访问，未发现剩余 P0/P1/P2。Trellis Check 由主会话统一执行，本 implementer 不越过 recursion guard 伪造该证据。

任务继续保持 `in_progress`，PRD AC 与 TODO 9 不勾选。尚未由本增量覆盖：真实 `40001/40P01/57014` 注入、recovery candidate 公平性（Tools owner）、其余 Runtime/Repository/Output/River/worker/commit-response-loss/连接释放/EXPLAIN 门禁、Model Settings scoped enqueue fence、跨 owner scoped Start/Hook，以及 Foundation 无法识别 active cross-pool scope 的限制。

## 2026-08-25 Repository / execution fence 真实 PostgreSQL 收口

现有 integration fixture 已收敛为同一个参数化入口：每个用例创建并迁移唯一 disposable database，再打开唯一 `platformpostgres.Pool`；legacy 用例取其 pgx facade，GORM 用例直接使用完整 Pool。没有引入第二套迁移或 Schema 事实源。

真实 PostgreSQL 验收发现并修复了三个实现/fixture 缺陷：

1. `database/sql` 对 `jsonb::text` 返回 string，而 Repository 曾复用只接受 pgx byte carrier 的 scanner；现 GORM scanner 使用明确的 SQL nullable/JSON carrier，损坏 JSON 继续 fail closed。
2. GORM Repository `Start` 曾丢弃 Run/Node/Outbox 的 runtime identity nullable 字段，导致最新 identity trigger 在后续 Claim 时拒绝合法数据；现按 legacy 契约持久化，空值仍为 NULL。
3. Tool mismatch fixture 曾插入第二个 active Workspace，触发真实 `uq_workspace_single_active`；现改为合法 inactive Workspace，未放宽生产约束。

以下真实 PostgreSQL 场景已通过：

```text
ZHIXU_TEST_DATABASE_URL=<disposable PostgreSQL> go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestGORM(RepositoryListAndOutputRunWithPostgres|ToolExecutionPolicyAndRecoveryFences|WorkspaceAnalysisExecutionFenceLocksExecutionWithPostgres)$' -count=1 -timeout=60s
ZHIXU_TEST_DATABASE_URL=<disposable PostgreSQL> go test -race -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestGORM(RepositoryListAndOutputRunWithPostgres|ToolExecutionPolicyAndRecoveryFences|WorkspaceAnalysisExecutionFenceLocksExecutionWithPostgres)$' -count=1 -timeout=60s
```

覆盖证据包括：Repository Start/Get/Claim/Heartbeat/Complete/exact replay、Human Task、keyset List/Limit+1、Runtime input/output/definition/human 投影、跨 Workspace 不可见、损坏 graph fail closed、真实 `23503` cause、循环查询后连接归还，以及迁移目标索引存在；Workspace execution fence 的完整 snapshot、Run -> Node -> Attempt 锁序与释放、各 binding mismatch、nil/foreign/stale scope、custom cancel cause 和真实 `55P03` 也已实测。Tool policy/recovery 的锁序、missing/skipped/stale 与 SQLSTATE 证据见上一节。

按发布协调要求，索引存在性与损坏图断言完成后不再扩大测试矩阵。`EXPLAIN` 大数据计划、Tool recovery 候选公平性、Runtime/River commit/rollback/worker consumption/response-loss、其余 Runtime state、人机控制、最终 Composition 与跨 owner 原子性仍未满足，因此相关 TODO 9 条目、PRD AC 和 task 状态继续保持未完成/`in_progress`。

Go Review 检查了错误分类与 cause chain、context cancel/deadline、typed nil、事务归属、资源释放和 legacy 兼容；SQL/事务 Review 检查了参数化、Workspace predicate、固定锁序、`SKIP LOCKED`、keyset/Limit+1、Rows Close/Err、SQLSTATE 和目标索引，未发现剩余 P0/P1/P2。Trellis Check Agent 因当前会话工具路由不可用，按用户指示不再重试派发，改由主会话依据同一 spec compliance、测试、静态搜索、跨层数据流和 path-only 边界维度人工复核；这是明确的执行方式降级，不作为独立 Agent 证据。

发布闭包仍有两项 owner 外依赖：Agent 的 `internal/agent/application/scoped_model_run.go` 由 Agent owner 后续原子提交；父任务 `.trellis/tasks/08-18-gorm-data-access-migration/{design.md,research/gorm-river-compatibility.md}` 不属 Workflow owner，其缺失会阻塞全局 task validate/push，本任务不越权纳入或改写。

## 2026-08-25 干净发布快照门禁

Workflow 闭包已迁入以 Foundation `78b63d0c2f64f1fb725a8c43ed22515b442d5b09` 为唯一父提交的独立 worktree；path-only 审计只允许 `internal/workflow/**` 与 `.trellis/tasks/08-19-gorm-workflow-migration/**`。四个发布必需文件 `application/scoped_runtime.go`、`adapter/postgres/gorm_core.go`、`application/scoped_workspace_analysis_execution_fence.go`、`adapter/postgres/gorm_execution_fence.go` 均在闭包内，未纳入 Foundation、Agent、依赖文件、vendor 或其他任务。

干净 worktree 已通过：

```text
go test -mod=vendor ./internal/workflow/... -count=1 -timeout=60s
go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/workflow/...
go test -mod=vendor -tags=integration -run '^$' ./internal/workflow/... -count=1 -timeout=60s
ZHIXU_TEST_DATABASE_URL=<disposable PostgreSQL> go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestGORM(RepositoryListAndOutputRunWithPostgres|ToolExecutionPolicyAndRecoveryFences|WorkspaceAnalysisExecutionFenceLocksExecutionWithPostgres)$' -count=1 -timeout=60s
ZHIXU_TEST_DATABASE_URL=<disposable PostgreSQL> go test -race -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestGORM(RepositoryListAndOutputRunWithPostgres|ToolExecutionPolicyAndRecoveryFences|WorkspaceAnalysisExecutionFenceLocksExecutionWithPostgres)$' -count=1 -timeout=60s
go list -mod=vendor ./internal/workflow/...
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workflow-migration
gofmt -d <Workflow closure Go files>
git diff --check
```

Workflow JSONL 原先引用了未提交的 Tools task 文档，导致首次 clean `task validate` 报 4 个缺文件错误。Workflow 自身 `design.md` 已固定两个 Port 与锁序，现又在本任务 `research/planning-review.md`、`research/quality-gates.md` 固定 owner boundary 和验收门禁；JSONL 将外部原因合并到这些已存在的 Workflow-owned 唯一条目，不复制或提交 Tools 目录。复验 implement 11 条、check 9 条全部通过。

精确命令 `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/migrate` 中，`cmd/migrate` 通过；`cmd/api` 与 `cmd/worker` 仅因已提交 Artifact 依赖后续 Agent owner 的 `agentapplication.ScopedModelRunStore` 而失败。错误中已无 Workflow 缺失符号，不能在 Workflow path-only 提交内越权补入 `internal/agent/application/scoped_model_run.go`。因此 consumer compile 继续作为发布阻塞，待 Agent owner 原子提交后在最终干净 tip 重跑；在此之前不得 push。
