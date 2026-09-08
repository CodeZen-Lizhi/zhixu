# Tools GORM Migration Design

## 1. Boundary And Rollout

本 child 采用“legacy pgx 保留 + staged GORM sibling + TODO 9 实库对照 + Final 统一切换”。
`GORMRepository` 只从一个完整 `*platformpostgres.Pool` 取得 GORM root 和 UoW，不接受可错配的
裸 `*gorm.DB`/UoW/pgx pool，不创建第二 pool。

TODO 9 前：

- 不改 `cmd/**`、migration、生产 constructor/selector、legacy Repository 或现有测试文件；
- 不双写、不 shadow read、不 fallback、不删 legacy；
- 新路径仅由静态编译和后续 TODO 9 fixture 构造；
- PRD AC 保持未完成，任务保持 `in_progress`，不得归档。

Workflow/Agent prerequisite Ports 已由各 owner task 交付；Tools task 已进入 `in_progress`，产品代码只通过这些
稳定 Application contract 参与同一 scope，仍不复制 owner SQL。

Foundation scope 可校验具体平台类型、live lifetime 与 transaction handle，但不能验证 Pool identity。
Tools、Events、Audit 及其他 scoped collaborator 必须由 Composition 从同一个 Pool 构造；active
foreign-Pool scope 是平台已知限制，不在 Tools 中通过反射、DSN 比较或私有类型断言解决。

## 2. Ownership And Public Contracts

Tools 继续实现现有 Application surface：

- `WorkflowPolicyReader`；
- `ToolCallRepository`：refusal/start/finalize/unknown/recovery/timeline；
- `ResultReceiptRepository` 与 `ResultReceiptFailureRepository`；
- `WorkspaceAnalysisToolOperationRepository`；
- `WorkspaceAnalysisToolRefusalRepository`；
- `TrustedWriteCallRepository`；
- Workspace Analysis synthesis/citation/publication authority readers。

所有权按父设计收敛：

- Tools GORM 直接持久化 Tool-owned `workflow.tool_call`、result receipt/failure 和其状态机；
- Workflow owner 独占 Definition/Run/Node Run/Node Attempt 的查询与锁；
- Agent owner 独占 Analysis Run/Operation/Reservation/Refusal/Candidate 的查询与 mutation；
- Tools 仍拥有业务编排和唯一 outer UoW，但只能通过 owner Application Port 让各参与者加入同一 scope；
- Events 使用 `ScopedAppender.AppendScoped`，Audit 使用 `Recorder.RecordScoped`；refusal 的提交不确定恢复
  通过同一 Recorder 的 `ReadScoped` 读取 immutable Audit closure，不绕过 Audit owner 直查表。

legacy 的跨 schema SQL 是行为基线，不是 GORM 实现模板。Tools GORM 文件禁止直接访问
`workflow.run`/`workflow.node_*` 或 `agent.workspace_analysis_*`。

### 2.1 Required Owner Prerequisites

Workflow task 已交付：

1. `ScopedToolExecutionPolicySnapshot`：在 caller scope 内读取/锁定 Definition/Run/Node/Attempt 并返回
   不可变 graph/hash/policy/owner/lease snapshot，供普通 refusal/start/trusted-write admission 使用；
   Port 不判断当前是否可执行，由 Tools 在首次 mutation 前校验；
2. `ScopedToolCallRecoveryFence`：按 Node -> Attempt 保持 `FOR UPDATE SKIP LOCKED`，返回
   found/skipped/stale 与 DB-time snapshot；
3. 已交付的 `ScopedWorkspaceAnalysisExecutionFence` 继续用于 WA Run -> Node -> Attempt 锁序。

Agent task 已交付：

1. `ScopedWorkspaceAnalysisToolParticipant`：锁/读取 Analysis Run -> Operation -> Reservation，并在 Tools
   插入/锁定 Call 前后执行 reserve/settle/attempt-advance/CAS；同时提供独立
   `VerifyWorkspaceAnalysisToolClosureScoped`，只核对 durable immutable closure，不复用 live admission；
2. `ScopedWorkspaceAnalysisToolRefusalStore`：完成 no-operation proof、refusal insert/replay、Audit binding 和
   不依赖当前 lease/cancel/deadline 的 exact durable load；
3. `ScopedWorkspaceAnalysisToolAuthorityReader`：在 caller scope 返回 Analysis/Operation/Reservation/Candidate
   的最小不可变投影，Tools 再与 Tool-owned receipt closure 组合。

以上名称在 owner task 中冻结；公开签名只含 `context.Context`、`foundation.TransactionScope` 与 owner
Application/Domain 值，不含 GORM/sql/pgx/`any`。Tools child 不实现这些 concrete Adapter。

### 2.2 Staged Repository Split

- `NewGORMRepository(pool, policySnapshot, recoveryFence)` 构造 ordinary Tools repository，静态实现 Policy、
  Tool Call 与 Trusted Write Port；
- `NewGORMWorkspaceAnalysisRepository(pool, executionFence, participant, refusalStore, authorityReader, events,
  audit)` 构造完整 WA repository；Recorder 的 staged `ReadScoped` 只读能力用于 commit-response-loss 的
  refusal+Audit 完整闭包证明，不在 Tools 内直查 Audit owner 表；
- 两者共享同包私有 core/scanner/codec，不互相持有或开启嵌套 UoW；Final 通过 Application interfaces 组合；
- ordinary constructor 拒绝 nil/typed-nil Workflow ports；WA constructor 拒绝全部 owner/Event/Audit 依赖，
  因而 requested/completed Event 和 refusal Audit 在 staged 完整路径中始终存在。

legacy 三 constructor 及其基础路径的可选 Event 兼容行为保持不变，直到 Final 删除；新 staged full WA
constructor 采用必需 Event/Audit 依赖，避免产生新的无事件 Workspace Analysis 事实。

## 3. Staged Files

实现按现有 ownership 拆分，避免单一巨型文件：

- `gorm_core.go`：Pool/UoW、ready、within、callback-vs-commit stage、Raw Row/Rows/Exec；
- `gorm_model.go`：显式 persistence records、JSONB/bytea/null/time/version carrier；
- `gorm_errors.go`、`gorm_scans.go`：context/no-row/SQLSTATE 分类与共享严格 mapper；
- `gorm_policy.go`、`gorm_calls.go`、`gorm_recovery.go`、`gorm_trusted_write.go`；
- `gorm_receipts.go`、`gorm_receipt_failures.go`；
- `gorm_workspace_analysis_core.go`、`gorm_workspace_analysis_operations.go`、`gorm_workspace_analysis_events.go`；
- `gorm_workspace_analysis_authority.go`、`gorm_workspace_analysis_refusals.go`。

实际文件名可随既有 package 风格微调，但不得复制 Domain 状态机、receipt codec、canonicalization 或 SQL
事实源。legacy 与 GORM scanner 仅可共享最小 `Scan(...any) error` 接口和纯 validation/equality helper。

## 4. Persistence Mapping And SQL Rules

Migration/trigger 是唯一 Schema 事实源；GORM 不执行 DDL。Tools GORM persistence model 只覆盖：

- `workflow.tool_call`；
- `workflow.tool_result_receipt`、`workflow.tool_result_receipt_failure`；

Agent/Workflow owner 表仍参与同一 transaction，但只通过前置 scoped Port 访问；Tools 不为它们声明
persistence model、Raw SQL 或 mapper。

规则：

- 固定 SQL 全部参数化，placeholder 使用 `?`；外部值不得参与 identifier 或 SQL 拼接；
- 锁、CTE、`RETURNING`、CAS、`FOR UPDATE/SHARE`、`SKIP LOCKED`、DB clock 使用 Raw/Exec；
- persistence record 显式列出列、nullable、UTC/time、version，不使用 `gorm.Model`、hook、implicit time、
  soft delete、Save、Preload、Association 或零值 Updates；
- JSONB object 使用校验后的 `driver.Valuer`，`Value()` 返回 string；
- Receipt output/private binding/candidate 使用 `bytea`，scan 后复制并按 Domain 规则严格验证；
- Receipt Failure 只保存 observation hash/bytes，不落被拒绝 document；
- 单行 no-row 使用 `Raw(...).Row().Scan`；多行先检查 statement error/nil rows，defer Close，最后检查
  Rows.Err 与 Close error；部分结果 fail closed；
- GORM 文件不使用 pgx Tx/Row/Rows/pool/protocol 或 pgconn；SQLSTATE 通过 `platformpostgres.SQLState` 投影读取。
- 静态禁止 Tools `gorm_*.go` 出现 `workflow.run`/`workflow.node_*` 或
  `agent.workspace_analysis_*` SQL；`workflow.tool_call` 与 receipt 是 Tools-owned allowlist。

## 5. Tool Call, Policy And Trusted Write

### 5.1 Ordinary Tool Calls

`RecordRefused`、`StartCall`、`FinalizeCall`、`MarkUnknown` 保持 Workspace、Workflow Run/Node/Attempt、
call number、tool identity、request hash、idempotency key、expected status/version 与 lease fence 的完整谓词。
exact replay 在读取或修改可变状态前处理；CAS 零行继续映射既有 not-found/state-conflict 错误。

业务时间、request/response summary、status transition 与 immutable binding 均由 caller/SQL 原语控制；
GORM 不自动写 timestamp，也不把空 JSON、NULL 与 `{}` 混同。

### 5.2 Policy

Tools outer UoW 先调用 Workflow `ScopedToolExecutionPolicySnapshot`，把 owner snapshot 映射为既有
`WorkflowToolPolicy`，再执行 Tool-owned insert/replay。Policy 保持 existing allow/deny、Workspace/Workflow
binding 与 fail-closed semantics；Tools 不直接查询 Workflow 表，也不新增默认允许或 no-op fence。

### 5.3 Trusted Write

`StartTrustedWriteCall`/`LoadTrustedWriteCall` 保持 writeback execution binding、side-effect idempotency 与
exact replay。Trusted Write 的 STARTED call 不得被通用 stale recovery 改为 UNKNOWN。

### 5.4 Recovery And Timeline

stale recovery 先选择有界 Tool-owned candidate，再调用 Workflow `ScopedToolCallRecoveryFence` 按
Node -> Attempt 执行 `FOR UPDATE SKIP LOCKED` 和 DB-time 重检，最后 Tools 锁 Call；保持 found/skipped/stale、
稳定排序和 Trusted Write 排除。每次 UoW 的 candidate scan budget 固定不超过
`application.MaxToolStaleRecoveryLimit`（当前 100），实际恢复数量仍受调用方 `limit` 限制；Repository 以并发
安全的 runtime keyset cursor 在成功提交后轮转到下一页，避免每次从最早的活跃调用重复开始。事务失败不推进
cursor，进程重启会从头开始；跨进程/多实例公平性与 durable cursor 由 TODO 9/Final 调度层验证。Timeline 保持
Workspace scope、`started_at,call_no,id` 既有顺序、上限与完整 scanner；不得 offset/unbounded scan 或 N+1。

## 6. Workspace Analysis Transaction Closure

固定锁序：

```text
Workflow ScopedWorkspaceAnalysisExecutionFence
  -> workflow.run
  -> workflow.node_run
  -> workflow.node_attempt
Agent ScopedWorkspaceAnalysisToolParticipant
  -> agent.workspace_analysis_run
  -> agent.workspace_analysis_operation
  -> agent.workspace_analysis_budget_reservation
Tools GORM
  -> workflow.tool_call
```

### 6.1 Authorization

一个 UoW 内按顺序完成：

1. 调用 Workflow fence 锁定并复核 run/node/attempt、definition、owner、lease、cancel/deadline；
2. 调用 Agent participant 锁定 Analysis Run；
3. Agent participant `INSERT ... ON CONFLICT DO NOTHING` Operation 后 `FOR UPDATE` exact load；
4. 处理 existing terminal/reconcile/replacement；
5. 复核单 Tool 并发、剩余预算和 DB clock；
6. 创建 STARTED Tool Call；
7. Agent participant 执行 Analysis Run reserved budget CAS；
8. Agent participant 执行 Operation STARTED CAS；
9. Agent participant 创建 RESERVED reservation；
10. 同 scope 追加稳定、脱敏 Event。

同 Attempt 已存在 STARTED Call 时只能返回 reconcile；不得提前 UNKNOWN。合法 replacement 必须在既有
lease/fence 条件下把旧 Call、Reservation、Run budget 与 Operation 一次性收敛为
UNKNOWN/UNKNOWN_CHARGED，再创建新事实。

### 6.2 Successful Receipt

成功路径在同一 UoW 中按既有锁序调用 Workflow fence 和 Agent participant，随后：Tool Call terminal CAS ->
canonical Result Receipt -> Agent participant 的 Reservation SETTLED / Run reserved-to-settled budget CAS /
Operation SUCCEEDED -> scoped Event。Receipt exact replay 必须核对 call、operation、hash、bytes、binding、
预算事实和 completed Event 的完整闭包。

### 6.3 Receipt Failure And Failed/Unknown

Receipt Failure 保持 immutable observation fact，只记录 hash/bytes。FAILED/UNKNOWN 路径必须同事务完成
Call CAS、Agent participant 的 Reservation/Run budget/Operation 归约与 Event；UNKNOWN 使用既有全额
charge 规则。任何 participant、trigger、CAS、Event 或 scan 错误都使整个 callback 回滚。

### 6.4 Deterministic Refusal

pre-executor deterministic refusal 先调用 Workflow fence，再由 Agent `ScopedWorkspaceAnalysisToolRefusalStore`
完成 no-operation proof 与 refusal insert/replay，随后同 scope Audit；不写 Server Event。它不得创建 Tool Call、
Operation、Reservation 或修改预算。exact replay 必须核对 logical slot、错误码、Workflow 与 Audit binding；
Audit 不包含 prompt、request、output、private binding 或 credential。

## 7. Authority Reads, Event And Audit

六个 Workspace Analysis authority reader 在 Tools outer read transaction 内调用 Agent
`ScopedWorkspaceAnalysisToolAuthorityReader` 取得 Analysis/Operation/Reservation/Candidate 最小投影，再与
Tool-owned receipt closure 组合。保持 Workspace、run/node/attempt、operation、receipt、candidate、publication
与 citation 完整 predicate。transaction options 与 legacy 默认值等价，不擅自升级为 RR/ReadOnly；Search
选择最多 3 条，现有固定上限 receipt 循环可保留，禁止无界 N+1。

Workflow/Agent/Event/Audit collaborator 只接 `foundation.TransactionScope`，不得获得 GORM/SQL/pgx concrete
transaction。Tools 拥有 outer UoW，collaborator 不 Begin/Commit/Rollback；nil、typed-nil、foreign type或
失活 scope fail closed。active foreign-Pool scope 当前无法识别，依赖 single-Pool Composition 与 TODO 9 fixture。

## 8. Error, Context And Response Loss

- 每个入口先 `ready(ctx)`；nil context 拒绝，callback context 贯穿全部 SQL 与 scoped collaborator；
- distinct `context.Cause(ctx)` 与 cancel/deadline sentinel 用 `errors.Join` 保留；cancel 优先 deadline；
- no-row 同时识别 `sql.ErrNoRows`/`gorm.ErrRecordNotFound`，并按调用点映射 not-found/replay/conflict；
- `sql.ErrTxDone` 映射 dependency unavailable；
- 40001/40P01/55P03 retryable；23505 按 idempotency/receipt/CAS 调用点分类；
  23503/23514/55000 consistency；未知数据库错误只暴露安全类型并保留 cause；
- UoW helper 区分 callback failure 与 callback 已成功后的 commit error。发生 commit error 后，同一次调用
  立即用 root 上的新 UoW transaction 重新读取并验证完整 durable closure。普通 Call/Trusted Write 只读
  Tool-owned 事实，不重跑 Workflow policy/admission；WA 使用 Workflow 原始 snapshot 仅核对 immutable
  binding，并调用 Agent `Verify...ClosureScoped`/exact refusal load，不重跑 live lease/cancel/deadline。WA
  operation 还验证 Event replay，Refusal 验证 Audit 存在。只有证明成功才返回 canonical `Replayed=true`；
  无法证明时
  返回 legacy 对应的 commit/unknown 错误，不把未证实输出当成功；
- error/log 不包含 SQL、DSN、JSON/bytea payload、prompt、tool request/output、private binding 或 secret。

## 9. Static Verification And TODO 9

静态阶段：

```text
go test -mod=vendor ./internal/tools/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/tools/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/tools/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/tools/... ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s
go list -mod=vendor ./internal/tools/...
go mod verify
git diff --check
```

`go mod tidy -diff` 只检查，不接纳任务前已有 `go.sum` 漂移。完成后执行 Go Review、SQL Review、
Trellis Check，并记录 static-validation；compile-only/skip 不能写成数据库 parity 通过。静态扫描还必须证明
Tools `gorm_*.go` 不含 Workflow/Agent owner SQL，且 owner Port concrete 实现来自各自模块。

TODO 9 是独立的共享实库验证任务，不属于本 child 的文件修改范围。该任务获得明确授权后，在现有
integration 文件原位参数化，每个 legacy/GORM variant 使用独立迁移数据库。GORM variant 从一个
`platformpostgres.Pool` 构造 Tools、Workflow、Agent、Events、Audit 与 UoW；fixture seed 可使用测试专用
pgx，但被测 GORM Repository 只能读取已提交数据。response-loss fault 通过同 package 可替换的私有 UoW interface field 包装
真实 `Within`：真实 commit 成功后返回注入错误，再断言该次 Repository 调用立即以新 UoW transaction
恢复并返回 canonical replay；公开 constructor 仍只接受 Pool，不新增弱类型 transaction abstraction。

2026-09-08 已复用现有参数化 fixture 完成实库主路径、Event 回滚和 refusal/Audit 原子性；外部 DSN 不再是阻塞。
child AC 按父任务 2026-09-01 精简政策验收，实际命令见 `research/static-validation.md`，状态/归档由主会话统一处理。

以下为完整风险场景目录，其余矩阵按直接改动或 Final 风险触发，不作为每个 child 的重复门禁：

- 普通 Call start/replay/CAS/terminal、policy、timeline、stale recovery 与 Trusted Write 排除；
- Authorization budget/concurrency、same-attempt reconcile、replacement UNKNOWN_CHARGED、DB clock 与固定锁序；
- success receipt、receipt failure、FAILED/UNKNOWN 的逐阶段 rollback、trigger/deferred closure；
- refusal + Audit 原子性、无 Server Event、exact replay、append failure rollback 与敏感 canary；
- 真实 23505/23503/23514/55000/40001/40P01/55P03、no-row、cancel/deadline/custom cause、
  `sql.ErrTxDone`；
- commit response-loss exact recovery、Rows/connection release、Pool acquired count 与并发无死锁。

## 10. Final Handoff And Rollback

Final 记录并统一替换 API/Worker 的 basic/full Repository constructor，保证 Tools、Workflow、Agent、Events、
Audit 与 River 依赖来自同一 Pool。只有 TODO 9/fault gates 通过后，才删除 legacy constructor、pgx
transaction seams 与非 allowlist imports。

TODO 9 前回滚只 revert Tools staged GORM/Application additive scoped文件和本任务工件；不得回滚 migration、
历史 Tool/Receipt/预算/Audit/Event 事实、其他 owner scoped implementation 或 legacy 生产路径。
