# Agent Repository GORM 迁移实施清单

## 1. 规划与基线

- [x] 核对 Agent Application/Domain/PostgreSQL Adapter、全部接口实现、生产/测试构造点和跨模块 transaction caller。
- [x] 固定 Model Run/Call、Recovery、Memory Snapshot、Workspace Analysis、RAG Progress 的事务、锁序、CAS、DB time、exact replay 和错误语义。
- [x] 核对 `00018/00020/00022/00041/00061/00066/00068/00074/00083/00085-00089/00091` Schema、constraint、index、trigger 和 Down guard 事实。
- [x] 盘点现有 unit/integration/migration/Worker tests、真实 PostgreSQL fixture 和 TODO 9 双实现方案。
- [x] 完成独立 Go/API/调用链、SQL/Schema/事务和测试/接线研究；修复规划 Review 的全部 P0/P1/P2。
- [x] 冻结 Workflow owner 的 `ScopedWorkspaceAnalysisExecutionFence` 请求、`found`/错误合同、快照、锁序、事务所有权与 Agent 阶段依赖。
- [x] 用户审批本最终方案。
- [x] 运行 `task.py start`，确认 child 为 `in_progress` 后使用 `trellis-before-dev` 重新加载实施上下文。

## 2. Scoped Application Ports

- [x] 新增三方法 `ScopedModelRunFinalizer`、窄 `ScopedModelRunAttemptFinder` 与组合 `ScopedModelRunStore`，签名只使用 `foundation.TransactionScope`；Capture 只依赖 Finalizer，保留 legacy `ModelRunTxFinalizer(any)`。
- [x] 新增 scoped Workspace Analysis Run persistence/starter/readiness sibling Port 和 Service；新增 Capability Checked Run Starter，固定在同一 scope 内按 readiness -> Run 顺序委托且不管理事务；保留 legacy `*Tx(any)` 路径。
- [x] 将 Capability lifecycle 三方法依赖收窄为不含 legacy transaction 方法的 Port，保持现有构造调用兼容。
- [x] 静态确认新 Port 不含 GORM、database/sql、pgx 或 any；GORM Repository 不实现 legacy any Port。

## 3. GORM Core 与 Model Runtime

- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，从同一 Pool 取得 GORM root/UoW 并拒绝 nil/失效依赖。
- [x] 实现 Create/Get Model Run、Start/Complete Model Call、Finalize Model Run，保持 Workspace、exact replay、CAS、stable call order 和 trigger 语义。
- [x] 实现 Get/Record/ByAttempt/Finalize Scoped；只 unwrap caller scope，不 begin/commit/rollback/fallback/cache。
- [x] 实现 Call/Run UNKNOWN Recovery，保持单 CTE、SKIP LOCKED、limit 500、caller time 和稳定排序。
- [x] 抽取/复用 column/scanner/validation/equality/error 纯 helper；Raw Row/Rows 防御 statement/nil handle、Close/Err 和整页 fail-closed。

## 4. RAG Memory Snapshot

- [x] 实现 Begin claimant exact replay 和跨 attempt/workspace 约束。
- [x] 实现 Finalize Snapshot + Create Model Run 单 UoW：锁、Run insert、Snapshot READY CAS、双向 readback、commit。
- [x] 实现 Fail Snapshot CAS/exact replay，保留 DB time 与 terminal 行为。
- [x] 验证 UoW 统一回滚，不使用 association/cascade/自动时间。

## 5. Workspace Analysis Run 与 Capability

- [x] 实现 root Load Run 和 scoped Insert/Find，保持 Question/Answer/Workflow 冻结 binding、budget/deadline 与 replay。
- [x] 实现 Capability Advertise/Heartbeat/Release 短 UoW，保持 clock_timestamp、lease 范围、contract drift/released/stale 语义。
- [x] 实现 Require Ready Scoped，在 caller scope 内 exact contract 查询，不另开事务。
- [x] 实现 scoped Capability Checked Run Starter，构造时冻结 contract，并验证 readiness 与 Run 委托收到同一个 scope、任一步失败都不继续。
- [x] 保留 legacy Repository 和 Conversation dispatch wiring；TODO 9 前不切换生产 starter。

## 6. Workspace Analysis Model Operation

- [x] 前置确认 Workflow child 已交付 `ScopedWorkspaceAnalysisExecutionFence`；Agent Tools scoped 实现仍不复制 `workflow.*` SQL。
- [x] 新增 `GORMWorkspaceAnalysisRepository`，构造时强制接收同一 Pool 和非 nil/typed-nil Workflow fence，不提供默认/fallback。
- [x] 实现 fence error translator：`found=false`/快照 drift -> Agent authorization conflict；invalid/scope/context/SQLSTATE/未知依赖按既有 Agent code/retryability 翻译并保留 cause，禁止透传 Workflow code。
- [x] 实现 Authorize、Finalize Call/Result/Candidate 四个 Store-owned UoW 入口。
- [x] 先在同一 scope 调 Workflow fence 锁 Run -> Node Run -> Node Attempt，再锁 Analysis Run -> Operation -> Reservation -> Model Run -> Model Call，最后读取 DB clock。
- [x] 保持 Operation/Reservation/Analysis budget/Run/Call CAS、UNKNOWN 全额结算、replacement attempt 和 cancel/deadline fence。
- [x] 保持 `SET CONSTRAINTS ALL IMMEDIATE`、commit response-loss recovery 和 durable exact replay，不猜测提交结果。
- [x] 实现 Result/Candidate/Checkpoint/Authority bounded Raw reads 及完整闭包/canonical JSON/hash/bytes/schema 验证。
- [x] 按职责拆分私有 helper，避免复制 2163 行单体，但不改变 SQL 语义或锁顺序。

## 6A. Tools Owner Scoped Contracts

- [x] 新增 Agent Application `ScopedWorkspaceAnalysisToolParticipant`：Prepare/LockByCall/Reserve/Settle/Advance/Verify 六段方法，公开 DTO 只含 Agent Domain 值与 Foundation ID。
- [x] Participant 保持 Analysis Run -> Operation -> Reservation 锁序；Tools Call ID 只在 Reserve 后进入 Agent 事实，UNKNOWN 全额结算，CAS/trigger 错误 fail closed。
- [x] 新增 `ScopedWorkspaceAnalysisToolRefusalStore`，拒绝 command 显式绑定 Audit Event ID；写入/精确回放不创建 Operation/Reservation/Call，不发 Server Event。
- [x] 新增 `ScopedWorkspaceAnalysisToolAuthorityReader` 的 Run/Operation/SourceRead-count/Candidate 四类最小投影；查询 bounded、Workspace scoped、正文不跨 Port。
- [x] Agent 侧实现只访问 Agent-owned schema；Workflow facts 仅经已交付 scoped fence，Tools owner 不得反向复制 SQL。
- [x] Tools consumer 启动前完成 Application/Adapter 静态门禁；Agent task 未改 Tools/cmd/production wiring。

## 7. RAG Progress 与 Events

- [x] 新增 `GORMRAGProgressStore` 构造，接收同一 Pool 与 `eventsapplication.ScopedAppender` 并做 typed-nil 检查。
- [x] 在一个 UoW 中获取 transaction advisory lock、读取既有 occurred_at 并调用 AppendScoped。
- [x] 保持 source ref、UTC 微秒、payload 白名单、exact replay 和 commit-unknown/manual-recovery。
- [x] 静态确认未调用 legacy `AppendTx(any)`、未复制 Event SQL、未创建第二事务/pool。

## 8. Error、安全与静态约束

- [x] 保留 SQLSTATE/constraint、not-found/found=false、version/replay conflict、retryability 和 commit unknown；GORM 文件统一经 `platformpostgres.SQLState` 读取状态码，不再 import pgx/pgconn。
- [x] 处理 `sql.ErrNoRows`、`gorm.ErrRecordNotFound`、`sql.ErrTxDone`；cancel/deadline 保留 sentinel 和 custom cause。
- [x] 公开错误/日志不含 Prompt、Evidence、response、JSON document、SQL 参数、DSN、Secret 或绝对路径；内部 cause 支持 errors.Is/As。
- [x] 静态确认无 AutoMigrate/Migrator、association/preload、Hook、gorm.Model/soft delete、自动时间、new any/pgx 泄漏或独立 pool。
- [x] 确认生产仍只构造 legacy Agent Repository/RAG Progress Store，未出现 selector/双写/fallback。

## 9. 局部验证（初次 staged 实现记录）

- [x] `go test -mod=vendor ./internal/agent/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/agent/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/agent/... ./internal/platform/postgres ./internal/events/application`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/agent/... ./internal/platform/migration -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/capture/... ./internal/conversation/... ./internal/artifact/... ./internal/organizing/... -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/agent/... ./internal/platform/postgres ./internal/events/... ./cmd/api ./cmd/worker`
- [x] `go mod verify`；`go mod tidy -diff` 只审查，现有 `go.sum` 漂移已记录且未应用。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-agent-migration`
- [x] `gofmt -d`（受影响 Go 文件）与 `git diff --check`

integration `-run '^$'` 只验证测试编译，不作为真实 PostgreSQL 证据。预计超过 60 秒的命令停止扩大范围并记录盲区。

## 10. Review

- [x] 使用 `go-review` 检查 Port/constructor、Workflow fence owner boundary、UoW/scope、typed nil、context cause、Raw Row/Rows、scanner、资源和 production compatibility。
- [x] 使用 `sql-code-review` 检查参数化、JSONB/nullable、Workspace scope、Workflow fence -> Agent locks 锁序、CAS、DB time、SKIP LOCKED、advisory/deferred constraints、SQLSTATE 和执行计划。
- [x] 使用 `trellis-check` 检查 PRD/Design、Workflow/Events/Audit/Capture 依赖、production wiring、验证证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；结果记录到 `research/static-validation.md`。

## 11. 真实 PostgreSQL 门禁（2026-09-08，按精简政策）

- [x] 复用已参数化的现有 `internal/platform/testdb` fixture；每个 legacy/GORM variant 使用独立迁移数据库，GORM collaborators 来自同一个 `platformpostgres.Pool`。无需配置外部 DSN；未新增或修改测试文件。
- [x] Model Run/Call create/replay、CAS、UNKNOWN recovery 主路径，以及 Record 在 caller transaction 中读取通过 legacy/GORM 对照。
- [x] Workspace Analysis Model Operation success、Result replay/load scope 与 commit recovery 通过 legacy/GORM 对照。
- [x] Agent participant/receipt/authority 随 Tools integration 验证，Event 失败时所有 owner 写入回滚；refusal/Audit 的原子回放与无预算副作用通过。
- [x] RAG Progress conflicting replay 回滚、Agent 真实 SQLSTATE 与 caller context 在错误依赖调整后复验通过。
- [x] 两个 owner 的局部 unit、vet、gofmt、`git diff --check` 和本 child Trellis validate 通过；最新命令与耗时见 `research/static-validation.md`。
- [x] Go/SQL/Trellis 复审通过，PRD child AC 与 `final-handoff.md` 已回填。

以下按风险或下游 owner/Final 执行，未声称本轮已覆盖：完整 Memory/Capability/cancel/replacement/fault 矩阵、所有真实 SQLSTATE、连接释放、目标 EXPLAIN、全量 integration race、多进程恢复公平性，以及 Capture/Conversation/Artifact/Organizing/API/Worker 完整生产接线。生产切换和 legacy 删除仍归 Final，不阻断本 child 的最低完成证据。

## 12. 回滚点

- Port 前：只改规划工件，无运行时影响。
- Model Runtime staged：删除新增 GORM core/runtime 和 scoped Model Run 文件，legacy 不变。
- Workspace Analysis/Memory/RAG staged：按阶段删除新增文件并还原共享纯 helper，legacy 生产不变。
- Final 切换失败：由 Final 恢复 legacy Composition；禁止双写、fallback 或拆事务掩盖差异。

## 2026-09-01 精简测试门禁

按父任务精简政策，本 child 保留一个 Agent Model Run/Workspace Analysis 主路径 Testcontainers 场景；只有本轮直接改动 scoped caller transaction、RAG/Event 或 Tools participant 原子性时，再补一条代表性提交/回滚、冲突或并发场景。此前完整 commit-loss、所有 trigger/SQLSTATE、连接释放、目标 EXPLAIN 和跨模块端到端清单改为按风险触发；公开 Port 不泄漏数据库类型、Workspace/预算/状态机和单 scope 原子性仍是硬门禁。
