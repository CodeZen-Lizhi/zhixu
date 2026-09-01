# Agent GORM Staged 静态验证记录

## 结论

2026-08-31 完成 Agent staged 范围的 Application scoped Port、Model Run/Call、Recovery、RAG Memory Snapshot、Workspace Analysis Run/Capability、Model Operation/Result/Candidate/Checkpoint、Tools owner scoped participant/refusal/authority 与 RAG Progress 实现。生产 `cmd/api`、`cmd/worker`、Capture、Conversation、Artifact 和 Organizing 仍使用 legacy pgx 路径；未增加 selector、双写、fallback、migration 或新测试文件。

Workflow owner 的 scoped execution fence 已由独立 `GORMWorkspaceAnalysisRepository` 在同一 UoW scope 内消费；Agent 自身验证 `found=true` 快照四元身份，并把嵌套 Workflow error envelope 剥离后按 Agent context/SQLSTATE/invalid/conflict/unavailable 合同重分类。`ZHIXU_TEST_DATABASE_URL` 未配置，TODO 9 的真实 PostgreSQL 等价门禁仍未执行；PRD AC、任务完成状态和归档保持未完成。

## 实现边界

- `internal/agent/application` 新增强类型 `foundation.TransactionScope` Port；公开 scoped 签名不含 `any`、GORM、`database/sql` 或 pgx。
- `GORMRepository` 只从一个 `platformpostgres.Pool` 取得 GORM root 与 Unit of Work；scoped 方法只解包 live scope，不拥有 commit/rollback，也不回退 root DB。
- Model Run/Call 保持 Workspace predicate、exact replay、expected-version CAS 和稳定 Call 顺序。
- Recovery 保持单 CTE、`FOR UPDATE SKIP LOCKED`、有界批次和 caller time。
- Memory Snapshot 保持 `Snapshot FOR UPDATE -> Model Run INSERT -> READY CAS -> 双向 readback -> commit`。
- Workspace Analysis Run/Capability 保持 caller-owned scope、readiness -> Run 顺序、DB clock 和 10..60 秒 lease。
- Workspace Analysis Model Operation 固定执行 Workflow fence -> Analysis Run -> Operation -> Reservation -> Model Run -> Model Call -> DB clock；Authorize/Finalize/Result/Candidate、UNKNOWN/replacement、CAS、deferred constraint 与 commit-loss durable recovery 均保持 legacy 语义。
- Result/Candidate document 按 `00085` 的 canonical `bytea` 契约绑定和扫描，并在返回前执行 domain canonical/hash/bytes/schema 校验；Checkpoint/Authority 为 Workspace-scoped bounded Raw read。
- Tools scoped participant 保持 Analysis Run -> Operation -> Reservation 锁序，先由 Tools 写入 Call 再 Reserve；UNKNOWN 使用 `UNKNOWN_CHARGED` 全额归约。
- Refusal 只写 Agent refusal 与 caller 提供的 Audit Event ID；Authority 只返回 bounded Agent-owned 投影，Candidate 正文不跨 Port。
- RAG Progress 在一个 UoW 内取得 advisory lock、读取 durable `occurred_at` 并调用 Events `AppendScoped`。
- 新 GORM 文件不访问 `workflow.*`，不使用 AutoMigrate、Migrator、Preload、Association、Hook、`gorm.Model`、隐式软删除或独立连接池。

## 已执行门禁

以下命令在 2026-08-31 对 Agent staged 范围复跑并通过：

```text
go test -mod=vendor ./internal/agent/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/agent/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/agent/... ./internal/platform/postgres ./internal/events/application
go test -mod=vendor -tags=integration -run '^$' ./internal/agent/... ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/capture/... ./internal/conversation/... ./internal/artifact/... ./internal/organizing/... -count=1 -timeout 60s
go list -mod=vendor ./internal/agent/... ./internal/platform/postgres ./internal/events/... ./cmd/api ./cmd/worker
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-agent-migration
gofmt -d <受影响 Agent Go 文件>
git diff --check -- internal/agent .trellis/tasks/08-19-gorm-agent-migration
```

`integration -run '^$'` 只证明测试可编译，不是数据库行为证据。Model fence error translator 与 snapshot scope drift 的新增回归用例位于既有 `workspace_analysis_model_operations_test.go`，未新增测试文件。Trellis validate 仅有既有大型 spec 注入截断 warning，验证本身通过。

`go mod tidy -diff` 返回 1，输出为本任务开始前已存在的 `go.sum` 大范围规范化差异，并建议补充 SQLite checksum；未应用输出，未由本任务修改 `go.mod`、`go.sum` 或 vendor。

静态搜索结果：

```text
rg 'AutoMigrate|Migrator\(|Preload\(|Association\(|gorm\.Model|AppendTx\(' internal/agent/adapter/postgres/gorm*.go
# 无结果

rg 'workflow\.' internal/agent/adapter/postgres/gorm*.go
# 无结果

rg 'NewGORMRepository|NewGORMRAGProgressStore' cmd internal --glob '*.go'
# Agent 仅出现 staged 构造定义；cmd 无 Agent GORM 构造
```

## Review 结果

- Go Review：检查 scoped Port、scope 生命周期、UoW 所有权、context、DTO/domain closure、资源和 production compatibility；复审发现 fence translator 对嵌套或 `errors.Join` 分支中的 `foundation.Error` 可能透传 `WORKFLOW_*` 或丢失同层 cause，已改为递归剥离全部跨 owner envelope、保留 joined context/SQLSTATE cause 后生成 Agent error，同时补齐 `found=true` 快照四元身份 drift 拒绝。修复后未发现剩余 P0-P3。
- SQL Review：逐项核对 Model Operation、Participant、Refusal、Authority、legacy 路径与 `00085/00086/00087/00089/00091`。确认 document 为 `bytea`、锁序/CAS/参数化/deferred constraint/commit recovery 正确，Agent 未复制 Workflow/Tools SQL；未发现 P0-P3。
- Trellis Check：确认 owner 边界、生产未接线、Model Operation staged 阶段已完成，PRD AC 与 TODO 9 因真实 PostgreSQL 门禁缺失保持未完成。

## 未完成与风险

1. 没有真实 PostgreSQL 证据证明 GORM `?` 重写、UUID/interval/`bytea` 参数、trigger/deferred constraint、真实 SQLSTATE、锁竞争和目标执行计划。
2. Model Operation 双授权、cancel/deadline/replacement/UNKNOWN/commit-loss、Memory claimant/READY 双向绑定、Recovery 并发 `SKIP LOCKED`、Capability DB-clock freshness、Tools participant/refusal/authority 的 immediate/deferred FK 与锁竞争、RAG advisory lock + Event append、context cancel/deadline 和连接释放仍需 TODO 9 原位参数化 integration fixture。
3. Foundation `TransactionScope` 没有 Pool affinity identity。当前只能拒绝 nil/foreign type/stale scope，不能识别来自另一个平台 Pool 的 active scope；同池由 Composition 与 TODO 9 fixture 保证，严格 cross-pool rejection 需 Foundation 后续能力。
4. 生产切换、legacy `any` Port 删除和全仓 pgx 收口仍归 Final child；本 child 不具备切换条件。
