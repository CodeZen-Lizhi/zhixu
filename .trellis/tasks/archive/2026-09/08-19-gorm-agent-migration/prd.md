# Agent Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

在不改变 Agent Schema、领域状态机、错误码、锁顺序和跨模块事务原子性的前提下，为 Model Run/Call、RAG Memory Snapshot、Workspace Analysis 与 RAG Progress 建立未接生产 Composition 的 GORM staged 实现，并由 Agent owner 提供基于 `foundation.TransactionScope` 的稳定跨模块事务 Port。

## Background

- `internal/agent/adapter/postgres` 当前由一个 pgx `Repository` 承担 Model Run/Call、crash recovery、RAG Memory Snapshot、Workspace Analysis Run/Capability/Model Operation/Candidate 等多组持久化契约；`workspace_analysis_model_operations.go` 单文件超过两千行，包含 Workflow lease、预算、Run/Call 与结果事实的多表状态机。
- `internal/agent/application` 仍有三组 `transaction any` 契约：`ModelRunTxFinalizer`、Workspace Analysis Run Persistence/Starter、Workspace Analysis Capability ready 检查。实际 Adapter 将它们断言为 `pgx.Tx`，不能由 GORM `*sql.Tx` 事务复用。
- Capture Profile 必须在自己拥有的事务中锁定 Agent Model Run、读取按 `call_no` 稳定排序的 Calls，并与 Profile Revision/Evidence、Attempt 和 Run 终结一起提交。拆成两个事务会破坏证据闭包与终态原子性。
- RAG Progress 在一个短事务中获取 advisory lock 并追加 Server Event；Workspace Analysis 模型操作按固定顺序锁定 Workflow Run、Node Run、Node Attempt、Analysis Run、Operation、Reservation、Model Run 和 Model Call。
- 生产 API/Worker、Capture、Artifact、Conversation、Organizing 等调用方仍构造 legacy `NewRepository` 或传入 `pgx.Tx`。TODO 9 真实 PostgreSQL 门禁通过前，本 child 不切换这些调用点。

## Requirements

### R1. Staged 范围与阶段

- 代码范围仅包括 Agent owner 的新 Application scoped Port、`internal/agent/adapter/postgres` GORM 实现，以及必要的共享纯 validation/scanner/error helper；不修改 `cmd/**`、migration、HTTP/Workflow Adapter 或其他 owner 的业务实现。
- 按四个阶段实施：Model Run/Call 与 Capture 前置 Port；Memory Snapshot/Recovery；Workspace Analysis；RAG Progress + Events scoped append。
- 每个阶段都保留 legacy pgx 实现，不增加运行时 selector、双读、双写或 fallback。完整 TODO 9 前所有实现只作为 staged Adapter。

### R2. Scoped Model Run Port

- 在 `internal/agent/application/scoped_model_run.go` 新增 `ScopedModelRunFinalizer`，只包含 Capture 实际需要的 `GetModelRunScoped`、`GetModelRunRecordScoped` 和 `FinalizeModelRunScoped`。
- 为 Artifact/Conversation 现有按 Node Attempt 查询的事务调用另建窄 `ScopedModelRunAttemptFinder.GetModelRunByAttemptScoped`，并以组合 `ScopedModelRunStore` 表达完整 scoped 能力；Capture 只依赖 Finalizer，不被迫接收未使用方法。
- Port 只接受 `foundation.TransactionScope`；公开签名不得出现 `any`、GORM、`database/sql` 或 pgx。
- `forUpdate=true` 必须在 caller-owned scope 中执行 `FOR UPDATE`；Record 的 Calls 按 `call_no` 稳定排序；Finalize 保持 RUNNING + expected version CAS、完整终态 exact replay 和 Workspace 隔离。
- Port 不开始、提交或回滚事务；nil、无效、非平台或失效 scope 必须 fail closed，禁止回退到 root DB。
- 保留 legacy `ModelRunTxFinalizer(any)` 到 Final，避免提前破坏 Capture/Artifact/Conversation/Organizing 的 pgx 调用链。

### R3. Model Run/Call、Recovery 与 Memory Snapshot

- `GORMRepository` 实现现有 `ModelRunRepository`、RAG Memory Snapshot、Workspace Analysis Run/Capability read/write 契约及新 scoped Port；使用一个 `platformpostgres.Pool` 的 GORM root 和 Unit of Work。
- Workspace Analysis Model Operation/Result/Checkpoint/Candidate Authority 由独立 `GORMWorkspaceAnalysisRepository` 承担，并要求构造时注入 Workflow owner 的 scoped execution fence；禁止把该依赖做成可选项或 fallback 到 Agent 内部 Workflow SQL。
- 保持 Run/Call 创建的 exact replay、Node Attempt/Call number 唯一性、Call phase/active-call 约束、乐观锁、终态逐字段比较、Workspace scope 和调用历史稳定顺序。
- Recovery 保持有界 `FOR UPDATE SKIP LOCKED`、排序、UNKNOWN 归约和 caller time；不得改成先查后逐行更新。
- Memory Snapshot 保持 PREPARING claimant、READY + Model Run 双向绑定、FAILED exact replay、数据库时间和同事务 readback；不得使用 association cascade。

### R4. Workspace Analysis scoped 过渡

- Agent owner 为 Workspace Analysis Run 插入/查找和 Capability ready 检查新增 `foundation.TransactionScope` sibling Port/Service；legacy `any` Port 继续服务 Conversation pgx 路径直到其 owner 迁移和 Final 收口。
- 新增 scoped Capability Checked Run Starter；它必须在同一个 caller-owned scope 中先执行 exact readiness 检查，再委托 scoped Run Starter，且自身不得开始、提交或回滚事务。
- Workspace Analysis Model Operation 的 GORM 阶段硬依赖 Workflow owner 提供 `ScopedWorkspaceAnalysisExecutionFence`：在 caller-owned scope 内按 Workflow Run -> Node Run -> Node Attempt 加锁并返回不可变 lease/cancel/definition 快照；缺失/父子 binding 不匹配以 `found=false` 表达，Agent 映射为现有 authorization conflict；Agent 不得直接查询或锁定 `workflow.*`，也不得透传 Workflow 错误码。
- GORM 路径不得通过 `any` 接收 scope，也不得在 Application 暴露具体数据库类型。
- Workspace Analysis Model Operation 保持固定锁序、Workflow lease/fence、数据库时钟、Operation/Reservation/Run/Call CAS、预算预留/结算、deferred constraint 强制检查、commit response-loss recovery 和 exact replay。
- 读取 Result/Candidate/Checkpoint/Authority 时继续验证 Workspace、Analysis Run、Operation、Reservation、Model Run/Call 的完整闭包；一行损坏时 fail closed。
- Agent owner 还必须提供 Tools 所需的 scoped participant：在 caller scope 内准备并锁定 Analysis Run -> Operation -> Reservation，
  在 Tools 写入 Tool Call 后完成 reservation/Run budget/Operation CAS，支持 terminal attempt advance；公开 DTO 只使用 Agent Domain 值。
- Agent 必须提供 durable-only `VerifyWorkspaceAnalysisToolClosureScoped`，只核对不可变 Operation/Reservation/Run/Call/result binding，
  不重跑当前 Workflow lease、cancel 或 deadline admission；commit response-loss 由 Tools 组合该闭包与自身 receipt。
- Agent 必须提供 `ScopedWorkspaceAnalysisToolRefusalStore`：拒绝写入/精确回放只拥有 Agent refusal 事实，显式接收 Audit Event ID，
  不创建 Tool Call、Operation、Reservation 或 Server Event。另提供 `ScopedWorkspaceAnalysisToolAuthorityReader` 的最小 Run/Operation/
  SourceRead-count/Candidate projection，Tools 不得复制 Agent SQL。

### R5. RAG Progress 与共享 owner

- 新增 `GORMRAGProgressStore`，构造时接收同一个平台 Pool 和 `eventsapplication.ScopedAppender`。
- Store 拥有短 UoW，在 scope 内获取同一 transaction advisory lock、读取既有事件时间并调用 `AppendScoped`；Event 与锁必须处于同一事务。
- 保持 source event ref、DB 中既有 occurred_at 的 exact replay、UTC 微秒和 commit-unknown/manual-recovery 语义。
- Workflow/Events/Audit 是生产切换依赖；本 child 不复制这些 owner 的 SQL、不切换 River Worker，也不修改其 Composition。

### R6. GORM/SQL、错误与安全

- 使用平台共享 GORM root/UoW；禁止自行 `gorm.Open`、`sql.Open` 或创建第二物理 pool。
- 复杂 CTE、`FOR UPDATE`、`FOR SHARE`、`SKIP LOCKED`、advisory lock、`SET CONSTRAINTS ALL IMMEDIATE`、CAS 和多表闭包查询保留参数化 Raw/Exec；禁止 AutoMigrate/Migrator、`gorm.Model`、Hook、association/preload、隐式时间或软删除。
- 保留现有 SQLSTATE 分类：`40001/40P01/55P03` retryable，`23505` replay/version conflict，`23503/23514/55000` consistency；按调用点区分 not-found、found=false、version conflict 和 commit unknown。
- cancel/deadline 同时保留 `ctx.Err()` sentinel 与不同的 `context.Cause(ctx)`；`sql.ErrTxDone` 映射 dependency unavailable；公开错误不泄露 SQL、参数、Prompt、Evidence、原始响应、DSN 或路径，内部 cause chain 保持 `errors.Is/As`。

### R7. 验证与发布门禁

- 不新增测试文件；TODO 9 后原位参数化现有 Agent/Workspace Analysis/Capture Profile integration fixture，使 legacy 与 GORM 各使用独立数据库和同一个 `platformpostgres.Pool`。
- 静态门禁不能替代真实 PostgreSQL 证据；2026-09-01 精简政策规定的核心实库场景、局部 unit/vet 和 Go/SQL Review 通过后即可验收 child，生产切换仍归 Final。
- TODO 9 真实 PostgreSQL 至少验证 Agent 主路径；仅在本 child 直接改动 scoped caller transaction、RAG/Event 或 Tools participant 原子性时，补一条最关键的提交/回滚、冲突或并发场景。
- 生产 Composition 切换和 legacy pgx 删除由 Final child 统一完成；本 child 只记录构造点和回滚边界。

## Acceptance Criteria

- [x] AC1：Agent staged `GORMRepository` 与 `GORMWorkspaceAnalysisRepository` 覆盖 Model Run/Call、Recovery、Memory Snapshot、Workspace Analysis 与 Candidate/Checkpoint/Capability 现有契约，结果和状态语义与 legacy 一致。
- [x] AC2：`ScopedModelRunFinalizer` 与独立 `ScopedModelRunAttemptFinder` 不暴露 `any`/GORM/sql/pgx，在 caller-owned scope 中保持锁、稳定 Calls、按 Attempt 查询、CAS 与 exact replay；提供可由 Capture 与 Profile 原子组合的三方法 Finalizer，下游接线由 Capture/Final 验收。
- [x] AC3：Workspace Analysis 的 scoped Capability Checked Starter 在同一 scope 内按 readiness -> Run 顺序执行；Model Operation 通过 Workflow owner 的 scoped fence 保持 Workflow Run -> Node Run -> Node Attempt -> Analysis -> Operation -> Reservation -> Run -> Call 锁序，`found=false`/scope/context/SQLSTATE 被稳定翻译为 Agent 既有错误，且 Agent GORM 路径不直接访问 `workflow.*`。
- [x] AC3a：Agent 为 Tools 提供 participant、durable closure、refusal 和 authority scoped Ports；Tools 可在一个 caller-owned scope 内完成 Tool Call/预算/Operation/拒绝 Audit 原子闭包，且 Agent 公开 API 不泄漏 Tools/GORM/sql/pgx/`any`。
- [x] AC4：RAG Progress 通过 Events `ScopedAppender` 在一个 UoW 内锁定并追加；失败回滚通过实库验证，commit unknown 保持既有 manual-recovery 错误合同。
- [x] AC5：错误码、retryability、敏感日志、Workspace 隔离、数据库时间、分页/批量和直接改动的资源边界检查通过。
- [x] AC6：受影响包既有 test/vet、核心 integration、Trellis validate、gofmt 与 `git diff --check` 通过，Go/SQL/Trellis Review 无未解决 P0/P1/P2；全量 race/cmd compile 按风险触发。
- [x] AC7：本 child 完成核心真实 PostgreSQL 门禁，生产 Composition 和 legacy 删除交由 Final；TODO 3 仅阻断 Final。

## 2026-09-08 验收结论

按父任务精简政策完成本 child 的实现、直接依赖和最低验证证据。复用现有 Testcontainers fixture，Model Run/Call 主路径、caller-owned transaction、Workspace Analysis Result/commit recovery、Tools participant/receipt/authority 与 Event 回滚、RAG replay 回滚均已通过。未新增或修改测试文件。具体命令与未覆盖范围见 `research/static-validation.md`，构造和 legacy 边界见 `final-handoff.md`。本 child 未修改生产 Composition；任务状态和归档由主会话统一处理。

## 2026-09-01 测试范围调整

按父任务精简政策，Agent 保留 Model Run/Workspace Analysis 主路径及实际修改的 scoped atomicity 代表场景，不再默认执行全部 commit-loss、trigger、EXPLAIN、连接释放和跨模块端到端矩阵。

## Out Of Scope

- 修改 Agent、Workflow、Events、Audit、Conversation、Capture 或其他 owner 的 Schema、migration、Domain 状态机、HTTP/Event contract。
- 在本 child 切换 `cmd/api`、`cmd/worker`、Capture/Artifact/Conversation/Organizing 构造或删除 legacy `ModelRunTxFinalizer(any)`。
- 为迁移方便拆分 Workspace Analysis、Capture Profile 或 RAG Progress 的原子事务，或复制其他 owner 的 SQL。
- 实现 TODO 9 Testcontainers、TODO 3 Atlas、Foundation transaction affinity 或 Final 全仓 pgx 收口。

## Dependencies

- 实现依赖 `gorm-platform-transaction-foundation`；RAG Progress staged 路径依赖 Events `ScopedAppender`。
- Workspace Analysis Model Operation staged 路径依赖 Workflow owner 先提供 `ScopedWorkspaceAnalysisExecutionFence`；该前置只阻断此阶段，不阻断 Model Runtime、Recovery、Memory 或 Run/Capability staged 实现。
- Workflow、Events、Audit 是 Agent 生产切换依赖；除上述 scoped fence 外，它们不阻止先完成未接 Composition 的 Agent staged 实现。
- Capture Profile closure 依赖本 child 的 `ScopedModelRunFinalizer`；该下游依赖不授权 Capture 复制 Agent SQL或拆分事务。
- Foundation scope 当前不能验证 active scope 来自哪个 Pool；同池由 Composition 与 TODO 9 fixture 保证，严格 cross-pool rejection 需 Foundation 后续 affinity 能力。

## 2026-09-08 最终收口

本模块子阶段验收完成。生产入口切换、旧 pgx 实现及过渡端口清理由 Final 统一完成，最终构造、核心实库与回滚依赖见 `final-handoff.md` 和 Final 的 `research/final-acceptance.md`。容量/全量故障矩阵等未执行项目不计 PASS；不影响已批准精简政策下的模块开发验收。
