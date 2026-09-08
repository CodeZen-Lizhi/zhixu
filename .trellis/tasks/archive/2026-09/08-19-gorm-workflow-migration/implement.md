# Workflow Repository GORM 迁移实施清单

## 1. 规划与基线

- [x] 盘点 Workflow Domain/Application/PostgreSQL/River 公开面、生产与测试构造点、跨模块 caller-owned transaction。
- [x] 固定 Repository、Runtime Start/State/Output 的 DB time、锁序、CAS、exact replay、commit-response-loss 和错误语义。
- [x] 核对 `00003/00011/00012/00031/00033/00065/00066` 的表、约束、索引、trigger 与 River 兼容边界。
- [x] 盘点所有 River 入队点、legacy `JobInserter/EnqueueFence`、scoped producer 和 pgx worker/listener/migrator 限制。
- [x] 盘点 `StartTx` 五个直接 owner 与 Cancellation/Terminal/Control Hook 的 pgx-only 实现。
- [x] 冻结 Agent 所需 execution fence 的请求、快照、`found`、错误合同和 Run -> Node -> Attempt 锁序。
- [x] 完成独立 Go/API、SQL/事务和 Trellis planning review，修复全部 P0/P1/P2。
- [x] 用户审批最终方案。
- [x] 运行 `task.py start`，确认 child 为 `in_progress` 后重新加载实施上下文。

## 2. Scoped Application Ports

- [x] 新增 `ScopedWorkspaceAnalysisExecutionFence` 与设计中固定字段的纯值 Request/Snapshot；nullable owner/lease 使用显式 `Set` 标志，公开 API 不含 `any`、GORM、database/sql 或 pgx。
- [x] 新增 `ScopedRuntimeStarter.StartScoped`，保留 legacy `StartTx(pgx.Tx)`。
- [x] 新增 scoped Cancellation/Terminal/Control Hook 与 typed-nil 安全的 composite；保留全部 legacy `any` Hook。
- [x] 新增 `ScopedRuntimeBindingReader` 与 raw Run/Definition/Node Snapshot；Attempt 为可选归属存在性，不解码或重编码 JSON。
- [x] 对纯 validation/comparison/codec helper 做最小共享，禁止建立弱类型 DB abstraction。
- [x] 新增 `ScopedToolExecutionPolicySnapshot` 与 `ScopedToolCallRecoveryFence` 的纯值 Request/Result；公开 API 不含 Tools、GORM、database/sql 或 pgx。

## 3. Execution Fence

- [x] 新增 `gorm_execution_fence.go`，仅在 live caller scope 中按 Run -> Node Run -> Node Attempt 执行 `FOR UPDATE`。
- [x] 验证 Workspace 与父子 binding；任一缺失或 mismatch 返回零快照、`found=false,nil`。
- [x] 不访问 Agent 表、不读取 DB clock、不管理事务、不 fallback root、不缓存 scope。
- [x] 覆盖 nil/foreign/stale scope、nil ctx、custom cause、SQLSTATE 与 typed-nil 防御的静态/现有测试验证；cross-pool affinity 记录为 Foundation 限制。
- [x] Tool policy Adapter 在 caller scope 中保持单条 join 与 `FOR SHARE OF run,definition,node,attempt`，返回完整 Definition/Run/Node/Attempt/DB-time 原始快照。
- [x] Tool recovery Adapter 保持 binding preflight -> Node Run `FOR UPDATE SKIP LOCKED` -> Node Attempt `FOR UPDATE SKIP LOCKED`，区分 missing/skipped/stale，且不锁 Run。
- [x] 两个 Tool Port 均不访问 Agent/Tool Call 表、不管理事务、不缓存 scope、不 fallback root；durable replay 不调用 live policy/fence。

## 4. GORM Repository

- [x] 新增 `NewGORMRepository(*platformpostgres.Pool)`、GORM root/UoW/ready/Raw Row/Rows/error helpers。
- [x] 实现 legacy-compatible `Start` 以及 `domain.Repository` 的 Get/Claim/Heartbeat/Complete/Human 方法，保持现有事务、DB/调用方时间与错误粒度。
- [x] 实现 Run List 与 Pending Human Task，保持 Workspace predicate、keyset、Limit+1、稳定排序和目标索引。
- [x] 复用纯 scanner/validation，JSONB 使用 string carrier 或 `::text` scan；Rows Close/Err、corrupt row fail closed。

## 5. GORM Runtime Start / Output

- [x] 新增 `GORMRuntimeRepository` 与 scoped-only Hooks 构造；公开 factory 只接 Pool/River Options/scoped fence，内部从 `pool.DB()` 创建 insert-only Client 和同 Pool `ScopedJobInserter`，拒绝 Options 中的 legacy fence。
- [x] Root Start 通过一个 UoW 委托 StartScoped；caller-owned StartScoped 不 commit/rollback。
- [x] 保持 DB timestamp、Definition/Run/Node/Outbox exact replay、legacy active Run guard 与 River job duplicate consistency。
- [x] Root Start 保持 callback-success commit error 后的 durable Run/Node/Outbox/Job recovery；StartScoped 不猜测 caller commit。
- [x] 实现 Run input、succeeded node output、pending human node 与 definition 只读投影，保持 bound/size/JSON 校验。

## 6. GORM Runtime State

- [x] Claim 只锁 exact-delivery Attempt，随后取第一次 DB time 并完成 replay/stale 分支；仅新 Claim 再锁 Model Settings state/runtime、取 freshness DB time并做 admission/CAS，禁止扩大 history 锁或提前 rollout 锁。
- [x] Heartbeat 保持 Run control fence -> Node -> Attempt 锁序和完整 lease fence CAS。
- [x] Delivery 保持 Run -> sorted Nodes -> Attempt 锁序、success/failure/retry/join、Outbox、River 和 scoped Terminal Hook 同事务。
- [x] Control 保持 receipt replay、pause/resume/cancel、安全取消、successor activation、scoped Control/Terminal Hook 与 receipt 同事务。
- [x] Human Wait 保持 Run -> sorted Nodes -> exact Attempt -> INSERT Task；Human Submit 保持 Run -> authorization -> sorted Nodes -> Task -> replay/expiry -> latest Attempt，successor/River 同事务。
- [x] 所有入队点只用 scoped producer；不调用 legacy `JobInserter(any)`，不把 root DB 用于事务内锁/写。
- [x] Model Settings scoped enqueue fence 未交付时保持生产未接线，不增加 no-op 或绕过 rollout drain。

## 7. 静态验证与 Review

- [x] `go test -mod=vendor ./internal/workflow/... -count=1 -timeout 60s`。
- [x] `go test -race -mod=vendor ./internal/workflow/... -count=1 -timeout 60s`。
- [x] `go vet -mod=vendor ./internal/workflow/... ./internal/platform/postgres`。
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/workflow/... -count=1 -timeout 60s`。
- [x] Scoped Runtime Binding Reader 已通过 Workflow unit/race/vet 与 integration compile；真实 PostgreSQL binding/锁/SQLSTATE 证据留在 TODO 9。
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/artifact/... ./internal/conversation/... ./internal/changecontrol/... ./internal/health/... ./internal/graph/... ./internal/organizing/... -count=1 -timeout 60s`。
- [x] `go list -mod=vendor`、`go mod verify`、task validate、gofmt 与 `git diff --check`。
- [x] 静态确认无 AutoMigrate/Migrator/Preload/Association/Save，无 GORM production wiring，无 Workflow fence 访问 Agent 表，无 GORM Runtime 调 legacy Hook/JobInserter。
- [x] 独立 Go Review、SQL/事务 Review 与 Trellis Check 无剩余 P0/P1/P2；证据写入 `research/static-validation.md`。
- [x] Tool policy/recovery 新增代码完成 Go Review、SQL/事务 Review 与 Trellis Check 同维度人工复核，并把命令及 finding/fix ledger 追加到 `research/static-validation.md`。

## 8. TODO 9 真实 PostgreSQL 门禁

- [x] 全部 integration fixture 已切换到共享 Testcontainers 工厂（每测试独立数据库与唯一 platform Pool），包括 legacy `repository_integration_test.go`（tag 从 legacy_integration 并入 integration）与 river adapter 的 worker/kill-smoke。
- [x] Fence 锁序、完整快照、各 mismatch `found=false`、scope/context/SQLSTATE 实测通过（gorm_execution_fence 套件）。
- [x] Tool policy snapshot 的 `FOR SHARE`/DB-time/binding 与 recovery 的 missing/skipped/stale、Node -> Attempt `SKIP LOCKED` 在真实 PostgreSQL 并发场景通过。
- [x] 2026-09-08 原位改用 GORM Runtime，实测 Workflow/Node/Outbox/River job 同 scope rollback、并发 exact replay 与 pgx Runtime worker 消费。历史 SIGKILL/commit response-loss legacy 证据不作为本轮 GORM 证据。
- [x] 2026-09-08 GORM Claim/Heartbeat/Delivery 主路径、terminal exact replay、并发 Claim/lease reclaim、Terminal Hook 失败后的 Delivery/Control 回滚通过。其余 Human/retry/join/全量故障矩阵按父任务精简政策不重复执行。
- [x] Repository/List/Output 的 JSON、分页、corrupt row、连接释放和 EXPLAIN 目标索引通过（gorm_repository/list 套件）。
- [x] Model Settings scoped enqueue fence 已交付；真实 GORM Runtime 使用同 Pool 的 Settings/Audit/Sealer，StartScoped 验证 singleton 锁在 scope 中持有且 rollback 后释放。
- [x] Final 汇总 Artifact、Conversation、Change Control、Health、Graph、Organizing 各 child 的 scoped Start/Hook 验证；Workflow 已提供稳定 scoped Port，不代替消费 owner 的组合验收。
- [x] Workflow 自身 PRD AC 按精简政策已有实库证据；child 不改生产、不删除 legacy、不执行归档，统一交由主会话与 Final。

## 9. 交付记录

- [x] 更新 `research/static-validation.md`、review finding/fix ledger、生产与测试 wiring 清单。
- [x] task.json 保持 `in_progress`，本轮按实际证据勾选自身 PRD AC；未改 parent 或 active pointer。
- [x] Final 已切换 Composition 并清理 legacy；回滚恢复 Adapter/Scoped Port/Composition 的完整依赖闭包，保留 Schema 与历史事实。

## 2026-09-08 实际收口

- [x] 三个现有 Runtime integration 文件原位切换代表性用例，新增共享 fixture 构造 helper；无新增测试文件、临时测试代码或第二套数据库工厂。
- [x] Repository、Execution Fence、Tools policy/recovery fence 和并发 Claim 的定向 integration `-race` 通过。
- [x] 保留并审查已有 `gorm_reindex_outbox.go`；接口不变，真实 Dispatcher 组合证据由 Retrieval child 汇总。
- [x] 完成 Go/SQL 并发审查，准确区分历史 legacy 门禁和本轮 GORM 门禁；新增 `final-handoff.md`。

## 2026-09-01 精简测试门禁

按父任务精简政策，Workflow 保留一个 Repository/Runtime 主路径 Testcontainers 场景，并针对本 child 直接负责的 River/Outbox 或 scoped fence 事务补一条提交/回滚、冲突或并发场景。response-loss、SIGKILL、全量 EXPLAIN、跨 owner 端到端和整包 integration race 仅在相应机制被改动或存在明确风险时执行；Model Settings fence、owner scoped Start/Hook 等未交付前置仍继续阻断。
