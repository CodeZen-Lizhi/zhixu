# River Workflow Runtime 与 Safe Writeback Dispatcher

## Goal

以 PostgreSQL 中的 Workflow/Node/Attempt 状态为业务事实源，引入 River 作为唯一正式 Job 投递与 Worker 获取机制，补齐服务端 Definition/Executor Registry、事务型入队、数据库时间租约、心跳、重试、失败、暂停/恢复/取消、后继激活和可观测性；把“用户批准 Proposal”接成真实 Safe Writeback 异步闭环，最终稳定到 `verifying/index_pending`，不使用自制 pending-node polling。

## Background

- `internal/workflow` 已有 Definition、Run、NodeRun、HumanTask、Outbox、Start/Claim/Heartbeat/Complete 的 PostgreSQL 基础，但没有 River 依赖、River 表、Job Worker、Executor Registry、Retry/Fail API、后继激活或 Run 终态计算。
- `cmd/worker/main.go` 当前只构造 Safe Writeback Node 并定时 Ping 数据库，日志明确为 `workflow_dispatcher=not_configured`；这不是可执行 Workflow Runtime。
- `internal/changecontrol/workflow.Node` 只接受已存在的 `ExecutionID` 并调用 Saga Resume；Atomic Begin 又要求 Node 已为 `running` 且 lease owner 匹配。Credential 不能进入持久化输入或 River Args，也不能在签发响应丢失后恢复。
- Change Control HTTP 只有 Proposal、Approval 和 `apply-preflight`，后者明确 `write_performed=false`。产品 PRD 的演示路径是“用户批准后系统安全写回”，因此本任务采用批准后自动创建 Durable Dispatch，不再增加第二套显式 Apply 入口。
- 当前通用 Workflow HTTP 允许客户端提交任意 Graph 和 `first_node_type`，与“内置 Definition 由服务端注册、权限不能由客户端或模型自报”的既定安全决策冲突。本任务以服务端 Registry 为准，并提供明确兼容错误，不静默接受任意执行器。
- 隔离 PoC 已锁定 River/riverpgxv5 `v0.40.0`：Go Proxy 稳定版、Go 1.25.0 要求、pgx v5.10.0 依赖均与项目 Go 1.25.4/pgx 5.10.0 对齐。真实 PostgreSQL 18 验证了 7 个官方 migration、`workflow` 自定义 Schema、重复 Up、单步 Down→Up、`InsertTx` 回滚、唯一 Job、ScheduledAt、typed Worker、Start/Stop/StopAndCancel。不得跟随 master 伪版本或复制 River 内部表/迁移 SQL。

## Requirements

### R1. 服务端 Definition Registry

- Definition 以稳定 `key + version` 注册，包含 canonical DAG、Node Kind、输入/输出 Schema Version、依赖、超时、Retry Policy、权限和可选补偿信息。
- Start/Approval Dispatch 只能引用已注册 Definition；客户端 Graph/首节点字段不得决定执行器、DAG 或权限。过渡期若请求仍携带旧字段，必须与服务端 canonical 值一致，否则返回稳定冲突错误。
- 启动时校验所有启用 Definition 的 Node Kind、Schema Version 和权限均能解析；未知、重复或不完整注册 fail-fast。

### R2. Node Executor Registry

- Runtime 通过 `node_kind + schema_version` 查找项目自有 Executor 接口；Domain、Application Executor 和 Safe Writeback Node 不 import River、pgx、LocalFS 或 Git 具体类型。
- 首批注册无副作用 Deterministic Test Node 与 Safe Writeback Bootstrap Executor。
- 未知 Node、非法 Schema 或绑定不一致在副作用前进入 NonRetryable Failure，不得跳过、反射加载或返回假成功。

### R3. 批准后的 Durable Dispatch

- Approved 决策成功时，在同一 PostgreSQL 事务中持久化 Approval、固定 Safe Writeback Workflow Run、首 Node、业务 Outbox 和唯一 River Job；任何一步失败全部回滚。
- 重复 Approved 请求使用不可变 Approval binding 返回同一 Run/Node/Job，不要求新增审批幂等头，也不创建第二套 Workflow 或 Job；Rejected 不创建写回任务。
- 新增唯一 Cross-Schema Approval Dispatch Unit of Work：一个 Adapter 拥有 pgx Transaction，并在同一事务内完成 Change Control SQL、Workflow SQL 和 River `InsertTx`；现有各自开事务的 `Approve`/`Start` 不得串联冒充原子提交。
- `change_control.proposal.workflow_run_id` 在 Approved Dispatch 时由 NULL 单向绑定到唯一 Run；`workflow.run.idempotency_key/request_hash` 和 `workflow.node_run.idempotency_key` 提供重放查询与冲突保护。Proposal→Run→固定 Node 的绑定不可变。
- River Args 只携带 `schema_version + node_run_id + dispatch_no` 等稳定身份，不携带正文、路径、Credential、Git 参数、lease owner 或 lock token。
- Approval HTTP 新建成功返回 201，完全相同的重放返回 200；Approved 响应增加 `workflow_run_id/workflow_status_url`，Rejected 不返回 Workflow 字段。

### R4. Safe Writeback Pre-Begin Bootstrap

- River Worker 先 Claim Node 获得数据库时间 lease，再由 Safe Writeback Bootstrap Executor 查询稳定 `safe-writeback:<node_run_id>` 幂等键对应的 Execution。
- Execution 已存在时直接构造现有 Safe Writeback Node Input 并 Resume；不存在时才由受信 Worker 瞬时签发两份短 TTL Authorization Credential，调用 Atomic Begin，然后立即丢弃明文 Credential。
- 授权签发后、Begin 前崩溃允许旧 Authorization 自然过期或撤销；新 delivery 可用新的服务端签发幂等键重签。Begin 已提交但响应丢失时，必须通过稳定写回幂等键找回既有 Execution，不依赖旧 Credential，不创建第二个 Execution。
- 同一 River delivery/Workflow lease owner 完成 Claim → Begin/Recover → Node Execute → Complete；Heartbeat 或 lease 丢失后取消执行 Context，旧 owner 不得开始新副作用或完成 Node。
- Writeback Repository 必须新增按 `workspace_id + idempotency_key` 的 exact lookup，并验证 Execution 的 Proposal/Revision/Approval/Run/Node/Change Hash 全绑定；查询结果与当前 Node input 不一致时进入 Consistency/Manual，不能 Resume 或重新 Begin。

### R5. River 事务型投递

- Runnable Node 与 River Job 使用官方 `InsertTx` 在同一 PostgreSQL 事务提交；River 是唯一执行投递机制，不扫描 pending Node 制作第二套队列。
- 每个 `node_run_id + dispatch_no` 只有一个有效 Job；`dispatch_no` 是 River Job generation，每次真正创建新的 Job（initial、业务 retry、Human resume、Pause resume 或显式 recovery republish）才递增。`retry_no` 只在业务 Retry 递增。相同 Job 的重复 delivery 或 lease 恢复不递增两者，也不得消耗业务 Retry 次数。
- River Job 状态不是 Workflow/Node/Saga 事实源；Job 成功只表示 Runtime 已完成相应领域状态事务。

### R6. 状态、Attempt 与数据库时间租约

- 拆分 RunStatus 与 NodeStatus；本期数据库 CHECK 只允许 `pending/running/waiting_for_human/retry_wait/paused/succeeded/failed/cancelled`，二者使用独立合法迁移表。Manual Recovery 必须有可查询的稳定失败分类，不能自动重试。
- 保留稳定 NodeRun，并新增 append-only Attempt 历史。`attempt_no` 每次成功取得新 Workflow lease 时递增；`dispatch_no` 每次创建新 River Job generation 时递增；`retry_no` 只在业务重试时递增；记录 River Job ID/Job Attempt/delivery ID、owner、开始/结束、结果分类、稳定错误码、next_attempt_at 和脱敏摘要。
- 相同 River delivery 在活动 lease 下不得创建第二 Attempt；lease 过期后若 River 仍重投同一 Job，可创建新 Attempt 并沿用原 `dispatch_no/retry_no`；只有旧 Job 已终止且 Runtime 显式 recovery republish 时才创建新 `dispatch_no`，仍沿用 `retry_no`，不占用最大业务重试次数。
- Claim/Heartbeat/Complete/Retry/Fail 均使用数据库可信时间；活动 lease 不可抢占，过期 lease 可回收，旧 owner 的晚到写入由 owner/version/attempt CAS 拒绝。

### R7. Retry、Fail 与 Manual Recovery

- 只以 `foundation.Error` 的 Kind/Code/Retryable 和 Executor 明确结果作为分类源，统一映射 Retryable、NonRetryable、Manual、LeaseLost 和 Cancelled。
- Executor 返回项目自有 `ExecutionResult/FailureEnvelope`；受信 Failure 可携带有上限的 `retry_after`，原始 error 只用于错误链和边界日志。Runtime 将现有 `foundation.Error` 映射为唯一 FailureClass，River error 不得绕过该映射。
- Success 与 Failure 互斥；FailureClass、ErrorKind 映射和 Retry-After 的负值/零值/上限规则必须冻结并逐项测试。
- Retryable 先原子结束当前 Attempt、持久化 `retry_wait`、稳定错误码和 `next_attempt_at`，并在同一事务为下一 Attempt 插入定时 River Job；指数退避、确定性 jitter、最大次数和 Retry-After 优先规则必须可测试。只有“数据库不可用导致无法持久化分类”时才允许 River 做传输级重试，恢复后仍以 Workflow 状态为准。
- NonRetryable 原子 Fail Node/Run 且不再执行；Manual 不自动 retry、不清除 Writeback checkpoint、文件/Git 恢复证据或审计事实。
- 失败/重试事务响应丢失时，相同 delivery 重放必须返回既有结果，不重复 Attempt 或事件。

### R8. 完成、后继与 Run 终态

- Complete 必须在同一事务内校验 lease/attempt，保存版本化 Output，完成 Node，计算并创建唯一后继 Node，事务型插入后继 River Job，写业务 Outbox，并更新 Run 状态/checkpoint。
- 相同 Output 的重复 Complete 幂等成功；不同 Output 返回冲突。多前驱并发完成时每个后继只创建一次。
- Safe Writeback 单节点成功只把 Workflow Run 标为 succeeded，并保持 Proposal/Execution 为 `verifying/index_pending`；不得把 Retrieval、Regression 或 Proposal 标为 completed。

### R9. Human、暂停、恢复与取消

- Human Task 提交后按 Definition 激活唯一后继并事务型入队。
- Pause 阻止领取新 Node；Resume 只重新激活满足依赖的 Node；Cancel 标记 Run/Node 取消请求并阻止新副作用。
- 已开始且不可中断的原子副作用允许返回到安全 checkpoint，但取消不得伪造撤销已提交 Git Commit；后续由 Saga/人工恢复决定。
- 提供版本化控制面 API：`POST /workflows/{run_id}/pause|resume|cancel`，要求 `Idempotency-Key + expected_version`；重复相同命令幂等返回，不同版本/绑定冲突。`pause_requested_at/cancel_requested_at` 持久化，正在运行的 Node 到安全 checkpoint 后归约状态。
- Pause/Cancel 不依赖同步删除旧 River Job，也不为失效而单独递增 `dispatch_no`：持久 pause/cancel 状态立即阻止 Claim 与新副作用。旧 delivery Claim 返回稳定 `WORKFLOW_DELIVERY_STALE` benign no-op，Worker 返回 nil 结束该 River Job，不 snooze、不作为 transport error 重试、不创建 Attempt或改变计数；Resume 真正创建新 Job时，未投递 Node 使用初始 `dispatch_no=1`，已有 generation 的 Node 才 `dispatch_no+1`，retry_no 不变，且不以 River Job 状态作为业务事实源。

### R10. 安全与权限

- Job Args、Node Input/Output、Attempt、Outbox、日志和 River metadata 不得包含 Credential、Proposal 正文、绝对路径、任意 Git 参数、原始 stderr 或 lock token。
- 执行前重新加载持久化 Workspace/Run/Node/Definition/权限事实；Job Args、客户端 Graph、模型文本或 Eino Context 不能扩大权限。
- Safe Writeback 的双 Authorization 仍由现有 Change Control 规则签发和 Atomic Begin 消费；River retry 不得绕过 Approval、lease、Target CAS、Git exact lookup 或 Saga checkpoint。

### R11. 可观测性与运行维护

- 日志/Trace 关联 `workspace_id/workflow_run_id/node_run_id/attempt/river_job_id/proposal_id`，敏感字段脱敏。
- 提供 Run/Node/Attempt、队列深度、执行时长、retry、lease expiry、heartbeat failure、manual recovery 和 worker shutdown 指标。
- Worker readiness 校验数据库、River schema、Definition Registry、Executor Registry 和启用 Definition 的依赖；启动成功后不再输出 `workflow_dispatcher=not_configured`。
- Worker 暴露独立只读 health server（默认容器内 `:8081`）的 `/livez` 与 `/readyz`；Compose healthcheck 必须实际调用 `/readyz`，不能只依赖日志或 DB Ping。
- Graceful shutdown 先停止领取新 Job，再等待安全 checkpoint；超时后取消 Context，重启从 Workflow lease 和领域 checkpoint 恢复。

### R12. 迁移、兼容与交付

- 项目迁移只做前向扩展；新增项目 `cmd/migrate`（或 PoC 证实的等价官方接法）顺序执行项目 Up 段和 River 官方 migrator，Compose migrate service 使用同一可执行物。不得复制 River 内部 SQL，必须保留独立 schema history、重复执行和升级测试。
- 旧 Definition/Run/Node 数据可查询；不满足新 Registry 的历史运行进入只读/明确失败，不被静默重解释。
- OpenAPI、产品 PRD、Workflow、数据库、Tool Security、Observability、Deployment 和恢复文档必须与真实行为同步。

## Acceptance Criteria

- [x] River/riverpgxv5 `v0.40.0` 隔离 PoC 通过：Go/pgx 编译、7 个官方 migration、空库/重复/Down→Up、自定义 Schema、事务入队、unique/scheduled job 和生命周期均有真实 PostgreSQL 证据。
- [ ] 正式引入后 `go.mod/go.sum/vendor` 精确锁定且项目/River migration 在现有库升级上通过。
- [ ] Registry 对重复 Definition/Executor、未知 Node Kind、未知 Schema 和缺失权限 fail-fast；客户端任意 Graph/Node Type 不能扩大执行能力。
- [ ] Cross-Schema Unit of Work 的锁顺序、共享 pgx tx 和 tx-scoped River Inserter 有故障注入测试；禁止串联现有多事务 Repository。
- [ ] Approved 决策与 Approval、Run、首 Node、Outbox、River Job 原子提交；任一点故障全回滚，重复请求只有一套事实。
- [ ] Approved 新建/重放分别返回 201/200 和同一 `workflow_run_id/workflow_status_url`；Rejected 不产生或暴露 Workflow。
- [ ] Approval 与 Pause/Resume/Cancel 的 OpenAPI request/response/枚举/400/404/409 契约通过；Run 从数据库绑定 Workspace，不接受调用方伪造 workspace_id。
- [ ] Rejected 决策不创建 Run/Node/Job；历史 Approval/Run 兼容查询。
- [ ] 真实 River + PostgreSQL 自动执行 Deterministic Node，无自制 pending polling。
- [ ] Safe Writeback 真实 River Job 完成 Claim → Authorization → Atomic Begin/Recover → Node Execute → Complete，并达到 `verifying/index_pending`。
- [ ] 第一/第二 Authorization 签发后崩溃、Begin 提交前崩溃、Begin 提交响应丢失、Execution 恢复后 Resume 前崩溃均可恢复，且只存在一个 Execution、Commit、Mapping 和 Reindex Outbox。
- [ ] River Args/Node/Attempt/Outbox/日志/数据库 Secret 扫描不含 Credential、正文、绝对路径、Git 参数或 lock token。
- [ ] 两 Worker 并发、重复 Job 和 kill -9 后只有一个活动 owner、一个逻辑 Node 完成和一个后继 Job。
- [ ] Heartbeat 续租、数据库断连、lease 过期回收、旧 owner 晚到 Complete/Fail/副作用前检查均有真实 PostgreSQL 测试。
- [ ] Retryable 按退避/jitter/Retry-After 重试，达到最大次数后失败；NonRetryable 不重试；Manual 不自动重试且保留恢复证据。
- [ ] Attempt 历史 append-only，错误码和摘要可查询且不泄漏敏感数据。
- [ ] Success/Failure 互斥、特定 Code 优先于 ErrorKind 的所有分类分支、Retry-After 边界和未分类错误均有测试，每个输入只产生一个 FailureClass。
- [ ] duplicate delivery、lease reclaim 和业务 Retry 的 `attempt_no/dispatch_no/retry_no` 语义分别验证；infra crash 不消耗最大业务 Retry 次数。
- [ ] Complete/Fail/Retry/后继创建/Job 插入/Outbox/Run 状态在事务失败和响应丢失下保持幂等一致。
- [ ] 冻结的 Workflow/Proposal Outbox event type、event key、schema version 和最小 payload 在 replay 下唯一，且 Reindex 事件保持 unpublished。
- [ ] Human resume、Pause、Resume、Cancel 阻止非法新副作用，并保持已提交 Git 的诚实恢复语义。
- [ ] Worker graceful stop、强制取消与重启恢复有自动化测试；Compose Worker readiness 能证明 River/Registry 已加载。
- [ ] OpenAPI、产品/架构/数据库/安全/可观测性/部署文档与实现一致，不把 `index_pending` 描述为 completed。
- [ ] `go test -race ./...`、关键并发包 `-count=20`、`go vet ./...`、`make test`、真实 PostgreSQL/River smoke、Compose/Docker readiness、go-review、sql-code-review 和 Trellis full-scope check 通过。

## Out of Scope

- M6 Retrieval 对 `retrieval.revision.reindex_requested` 的真实消费、索引构建、Regression 和 `verifying → completed`。
- 完整 Tool Registry、Agent Tool Calling、Eino 节点、RAG/Artifact/Graph/Ingestion 等业务 Executor。
- 通用业务 Outbox fan-out/publisher 和 M6 Reindex consumer；本任务只保证事件身份、事务写入和未发布状态，不把 runnable Node 交给 Outbox。
- Workflow Center 前端、SSE Event Store 和人工恢复 UI；本任务只提供后续 UI 可查询的状态、Attempt 和稳定错误。
- 通用补偿 DAG 引擎；Safe Writeback 继续使用现有 Application Saga。
- Temporal、Kafka、Redis、多数据库事务、跨 Workspace 事务和多节点分布式文件锁。
