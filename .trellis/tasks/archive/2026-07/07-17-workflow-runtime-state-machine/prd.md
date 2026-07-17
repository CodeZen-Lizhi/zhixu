# M4-B Workflow Runtime State Machine

## Goal

在 M4-A 已提供的 Definition/Executor Registry、River 事务入队和稳定 Node Job 身份之上，实现以 PostgreSQL 为唯一业务事实源的 Workflow Runtime 状态机：数据库时间 Claim/Heartbeat、append-only Attempt、Complete/Retry/Fail、DAG 后继激活、Human Task 恢复，以及版本化 Pause/Resume/Cancel。River 只负责投递；不得把 River Job 状态、客户端输入或 Worker 本地时间提升为 Workflow 事实。

## Scope

本任务严格包含：

- RunStatus、NodeStatus、AttemptStatus、FailureClass 与合法迁移。
- DB-time Claim、Heartbeat、lease reclaim、旧 owner fencing。
- `attempt_no / dispatch_no / retry_no / delivery_id` 的冻结语义。
- Executor 结果到 Success、Retryable、NonRetryable、Manual、LeaseLost、Cancelled 的归约。
- Complete/Retry/Fail、后继激活、Run 终态归约和事务型 River 入队。
- Human Task 创建/提交后的后继激活。
- Pause/Resume/Cancel 的 Repository、Application 与 HTTP/OpenAPI 契约。
- 迁移兼容、锁顺序、并发/崩溃/响应丢失验收。

本任务明确不包含：

- Approval、Proposal、Authorization、Safe Writeback Bootstrap、文件系统或 Git 副作用。
- River 依赖接入、官方 River migration、Definition/Executor Registry 的首次实现；它们属于 M4-A 前置任务。
- Worker Composition、readiness、指标、Trace、Compose、部署和运维实现；它们属于 M4-D。
- 通用 Outbox publisher、Retrieval/Reindex consumer、Workflow Center 前端或人工恢复 UI。

## Current Facts And Conflicts

1. 当前 `internal/workflow/domain` 让 Run 与 Node 共用 `Status`，只支持 `pending/running/waiting_for_human/succeeded/failed/cancelled`，缺少 `retry_wait/paused` 和 Attempt 状态；本任务以父任务冻结的新状态模型为准，旧枚举只作为迁移输入。
2. 当前 Repository 的 Claim/Heartbeat/Complete 接受调用方传入 `now/until`；这会信任 Worker 时钟。本任务要求 SQL 使用 `CURRENT_TIMESTAMP/clock_timestamp()` 计算资格、过期和 lease，应用层只传 lease duration。
3. 当前 `node_run.attempt` 在 Claim 时递增但没有 append-only 历史，也无法区分 delivery、lease reclaim 和业务 retry。本任务保留该列作为 `latest attempt_no` 兼容投影，并新增 `node_attempt` 为历史事实源。
4. 当前 Complete 只更新 Node 与 Outbox，不激活后继、不归约 Run；当前 Human Submit 把同一 Human Node 改回 pending。父任务要求按 canonical Definition 激活唯一后继，因此本任务采用后继节点模型，旧行为不得继续扩展。
5. 当前文档曾把基础 Lease/Human 标为已实现，但真实代码不满足 River、DB-time、Attempt 与后继语义；验收以真实并发 PostgreSQL/River 测试为准。

## Requirements

### R1. 独立状态与不变量

- `RunStatus` 与 `NodeStatus` 本期都只允许 `pending/running/waiting_for_human/retry_wait/paused/succeeded/failed/cancelled`，但二者拥有独立合法迁移表；新增状态必须通过后续前向迁移和契约评审，不能由数据库未知字符串隐式扩展。
- `AttemptStatus` 只使用 `running/succeeded/waiting_for_human/retry_scheduled/failed/manual_recovery/lease_lost/cancelled`，Attempt 一旦结束不可再改写业务结果。
- `succeeded/failed/cancelled` 为 Run/Node 终态；终态不得被 Resume、Heartbeat 或晚到结果重新打开。
- Manual Recovery 通过稳定 `FailureClass=manual_recovery` 和错误码可查询；不得伪装成 retry_wait 或 succeeded。
- Success 与 Failure 互斥；Output 只在 Success 持久化，FailureEnvelope 只在 Retry/Fail/Manual 持久化。

### R2. Attempt、Dispatch 与 Retry 计数

- `attempt_no`：每次成功取得新的 Workflow lease 时递增；相同 delivery 在同一活动 lease 下重放不增加，lease 过期后重新取得 lease才增加。
- `dispatch_no`：每次创建新的 River Job generation 时递增，包括 initial、业务 retry、Human resume、Pause resume 和显式 recovery republish；River 对同一 Job 的重复 delivery 不递增。
- `retry_no`：仅在业务 Retryable 结果被原子持久化时递增；进程崩溃、数据库断连、lease reclaim 或 River transport retry 不消耗业务 retry 次数。
- NodeRun 保持稳定；`node_attempt` append-only 使用冻结字段 `attempt_no/dispatch_no/retry_no/river_job_id/river_job_attempt/delivery_id/lease_owner/lease_until/status/output_schema_version/output_hash/failure_class/error_kind/error_code/error_summary/next_attempt_at/started_at/heartbeat_at/ended_at`。
- 每个 Node 的 `attempt_no` 严格递增，每个 `(node_run_id, dispatch_no, delivery_id)` 在活动 lease 内至多一个 Attempt。

### R3. DB-time Claim 与锁序

- Claim、Heartbeat、Complete、Retry、Fail、Pause/Resume/Cancel 的资格和时间戳只使用数据库可信时间；API/Worker 不能提交 `now` 或绝对 `lease_until`。
- Claim 先锁 Run，再锁 Node，再读取/锁定活动 Attempt；事务型创建后继时按确定性的 `node_key` 升序锁 Node，并最后写 Outbox/River Job。禁止相反顺序或先锁 Attempt 再锁 Run。
- Claim 只有在 Run 可执行、未 pause/cancel、Node 为 pending 或到期 retry、或 running lease 已过期时成功。
- 活动 lease 不可抢占；过期 lease 可回收。旧 owner 的 Heartbeat/Complete/Retry/Fail 必须同时通过 `owner + attempt_no + node_version + unexpired lease` CAS。
- lease reclaim 必须先把旧 Attempt 归约为 `lease_lost`，再 append 新 Attempt；两 Worker 并发只能有一个活动 owner。
- Heartbeat 扩展 lease 但不创建 Attempt、不递增 dispatch/retry；Heartbeat 失败返回稳定 `WORKFLOW_LEASE_LOST` 并取消 Executor Context。

### R4. 错误分类

- Executor 只返回项目自有 `ExecutionResult` 或 `FailureEnvelope`，不返回 River 专有决策。
- 分类唯一来源为受信 Executor 明确结果和 `foundation.Error` 的 Kind/Code/Retryable；顺序固定为“受信显式分类 → 特定稳定 Code → 已证明的取消语义 → Kind/Retryable 默认规则”。未知错误默认 NonRetryable，不能因为 River 会 retry 而自动升级为业务 Retryable。
- 冻结 FailureClass：`retryable/non_retryable/manual_recovery/lease_lost/cancelled`。特定 Code 可覆盖较宽泛的 ErrorKind（例如 `WORKFLOW_LEASE_LOST` 覆盖 `version_conflict`），但每个输入必须且只能命中一个最终分类分支。
- `retry_after` 只接受受信来源，负值非法、零值视为未指定、正值设置上限；错误摘要有长度上限且脱敏，不保存原始 cause、stderr、路径、正文或 Secret。
- 只有 Workflow 分类事务尚未提交且数据库不可用时，当前 River Job 才可返回 transport error；一旦 Complete/Retry/Fail 事务提交，River delivery 必须视为成功结束。

### R5. Retry、Fail 与 Manual

- Retryable 事务必须结束当前 Attempt、将 Node/Run 归约为 `retry_wait`、递增 `retry_no`、计算并持久化 `next_attempt_at`、递增 `dispatch_no`，并用 M4-A 的 tx-scoped River Inserter 插入唯一 Scheduled Job。
- 退避公式为有上限的指数退避加确定性 jitter；有效 Retry-After 大于计算值时优先。所有输入、舍入、溢出和上限规则必须由纯函数测试冻结。
- RetryPolicy 使用 `max_retries`，只统计业务 Retryable 转移：初次执行时 `retry_no=0`，当当前 `retry_no >= max_retries` 时不再创建下一 Job并归约为 `RETRY_EXHAUSTED`；attempt_no 和 River Job attempt 不参与 exhaustion。
- NonRetryable 原子结束 Attempt、Fail Node，并按 DAG 失败策略归约 Run；不得新增 Job。
- Manual 原子结束 Attempt、保存稳定分类和恢复摘要、Fail Node/Run；不自动 retry、不删除领域恢复证据。
- 相同 delivery 的事务响应丢失重放返回既有归约；不得重复 retry_no、dispatch_no、Attempt、Outbox 或 River Job。

### R6. Complete、后继与 Run 归约

- Complete 在单一事务内按冻结锁序校验 lease/Attempt，保存版本化 Output，结束 Attempt/Node，计算 canonical DAG 中满足全部依赖的后继，创建或激活唯一 NodeRun：新建 NodeRun 的首个 Job 固定 `dispatch_no=1`，仅既有 NodeRun 创建新 generation 时才 `dispatch_no+1`；随后 `InsertTx`、写唯一 Outbox并归约 Run。
- `(run_id,node_key)` 唯一；多前驱并发完成只能激活一次后继并生成一个对应 dispatch generation。
- 同一 Attempt + canonical Output hash 的 Complete 重放幂等成功；不同 Output 返回 `WORKFLOW_COMPLETION_CONFLICT`。
- 全部节点 succeeded 时 Run succeeded；存在不可恢复失败时 Run failed；仍有 waiting/retry/running/pending 节点时不得提前终结。
- 后继、Job 或 Outbox 任一步失败，Complete 全部回滚，当前 lease 仍可按原状态恢复。

### R7. Human Task

- Human Executor 创建 Human Task 时把当前 Attempt 终结为 `waiting_for_human`，原子结束运行 lease，把 Node/Run 归约为 waiting_for_human，并写唯一 Outbox；等待期间不占 Worker lease。
- Submit 要求 task pending、未过期、`target_version` 匹配，并按注册 Definition 验证 decision schema。
- Submit 成功应完成 Human Node并激活满足依赖的唯一后继，而不是把同一 Human Node重新变成 pending；新建后继首个 Job 使用 `dispatch_no=1`，已有后继的新 generation 才递增，`retry_no` 不变并事务型入队。
- 相同 task/version/decision 重放幂等返回；不同 decision、过期、已取消或版本不匹配返回稳定冲突且不入队。
- paused Run 可接受已授权的 Human decision 并持久化，但不得入队后继，后继由 Resume 统一激活；cancelled Run 拒绝新 decision。

### R8. Pause、Resume 与 Cancel

- 提供 `POST /api/v1/workflows/{run_id}/pause|resume|cancel`；Header 必须有 `Idempotency-Key`（1..128），Body 为 `{ "expected_version": <positive integer> }`，请求不接受 workspace_id。
- 统一响应为 `{workflow_run_id,status,version,status_url}`。稳定错误至少包括：`IDEMPOTENCY_KEY_REQUIRED` 400、`WORKFLOW_CONTROL_INVALID` 400、`WORKFLOW_RUN_NOT_FOUND` 404、`WORKFLOW_VERSION_CONFLICT` 409、`WORKFLOW_CONTROL_CONFLICT` 409。
- 控制命令按 `(run_id, command, idempotency_key)` 持久化 request hash 和 result；同绑定重放返回原结果，不同绑定冲突。
- Pause 设置 `pause_requested_at`，阻止新 Claim 和新 Job generation；running Node 在下一个 Runtime checkpoint 归约 paused。Pause 不撤销已经完成的领域副作用。
- Resume 清除 pause 请求，只为满足依赖且没有有效 Job 的 Node 创建 dispatch：未投递 Node 使用初始 `dispatch_no=1`，已有 generation 的 Node 才 `dispatch_no+1`；不得增加 retry_no。终态或非 paused Run 拒绝 Resume。
- Cancel 设置 `cancel_requested_at`，取消 pending/retry/waiting Node，阻止新副作用和后继；running Executor Context 被通知取消，返回安全 checkpoint 后归约 cancelled。已提交外部副作用不得伪造撤销。
- Pause/Cancel 后已存在的 River Job 不要求同步物理删除，也不为失效而单独递增 `dispatch_no`；Claim 先依据持久 `pause_requested_at/cancel_requested_at` 与 Node 状态返回 `WORKFLOW_DELIVERY_STALE` benign result，Worker 向 River 返回 nil，禁止 snooze、transport retry、创建 Attempt或改变任一计数。Resume 真正创建新 Job时，未投递 Node 使用初始 `dispatch_no=1`，已有 generation 的 Node 才 `dispatch_no+1`，`retry_no` 不变；不得查询 River Job 状态作为业务事实源。
- Pause/Cancel 与 Complete/Retry/Fail 并发时都遵守同一 Run→Node→Attempt 锁序，最终只能得到一套可解释状态。

### R9. 迁移与兼容

- 只新增前向迁移，不修改已应用的 `00003_workflow.sql` 或其他历史迁移。
- 新迁移增加独立状态约束、`node_attempt`、计数/错误/调度/控制字段、控制命令幂等表、唯一约束和必要索引；旧 Run/Node 必须继续可查询。
- 本任务项目迁移固定为 `00012_workflow_runtime_state_machine.sql`，复用 `00011` identity/schema/dispatch 字段，不重复定义 Proposal binding。
- 旧数据库列 `node_run.attempt` 保留为唯一 latest-attempt 投影，Go 模型可命名为 `LatestAttemptNo`；00012 不新增第二个 `latest_attempt_no` 列。新代码只把 `node_attempt` 作为历史事实源。
- 旧 terminal 数据映射为新 terminal 状态且不可重新执行；缺少 registry/schema/dispatch binding 的 legacy active 数据进入明确 `WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED` 只读失败，不静默生成 Job。
- Down 迁移不得在存在新 Attempt、控制命令或 active 新状态时丢数据；需使用 SQLSTATE `55000` 安全拒绝。

### R10. 安全与边界

- Run/Node/Attempt/Human/控制命令/Outbox/River Args 不得保存 Credential、正文、绝对路径、任意 Git 参数、原始 stderr 或 lock token。
- Run 的 Workspace 从数据库绑定，控制 API 不信任调用方伪造 Workspace；实际认证接入属于 M5-05，不在本任务伪造。
- Job payload、客户端 Graph、模型文本或 Executor error 不能改变 DAG、Retry Policy、权限或 FailureClass 映射。

## API Contract

| Operation | Preconditions | Success | Idempotent replay | Conflict |
|---|---|---|---|---|
| Pause | run version match; non-terminal; not cancel-requested | 200 paused/transitioning result | same command key and request hash returns stored result | 409 version/control conflict |
| Resume | run version match; paused | 200 running/waiting result | same command key and request hash returns stored result | 409 terminal/not-paused/binding conflict |
| Cancel | run version match; non-terminal | 200 cancelled/cancel-requested result | same command key and request hash returns stored result | 409 version/binding conflict |

OpenAPI 必须明确 UUID、Header/Body 校验、状态枚举、201 不适用、400/404/409 Problem Details，并以 Contract Test 防止破坏。

## Acceptance Criteria

- [ ] Run/Node/Attempt 独立状态和所有合法/非法迁移有表驱动单测；Success/Failure 互斥。
- [ ] 所有 Foundation ErrorKind、未知错误、Retry-After 负/零/上限和 retry exhaustion 映射有单测。
- [ ] 真实 PostgreSQL 证明 Claim/Heartbeat/Complete/Retry/Fail 使用 DB time，应用层时钟偏移不能提前抢租约或延长旧 lease。
- [ ] 两 Worker 并发 Claim、Heartbeat 与 lease reclaim 只产生一个活动 owner；旧 owner 晚到 Heartbeat/Complete/Retry/Fail 全部被 fencing。
- [ ] duplicate delivery、lease reclaim、业务 Retry、Human resume、Pause resume 分别证明 `attempt_no/dispatch_no/retry_no` 冻结语义；infra crash 不消耗业务 retry。
- [ ] Attempt append-only；同一活动 delivery 不重复 Attempt；历史包含稳定错误码且 Secret 扫描无敏感数据。
- [ ] Retryable 在同一事务持久 retry_wait/next_attempt/计数并插入 Scheduled River Job；回滚和响应丢失不会重复 Job、Attempt 或计数。
- [ ] NonRetryable 不重试；Manual Recovery 不自动重试且恢复摘要保留；达到 max_retries 后稳定失败，infra attempt 不计数。
- [ ] Complete 原子更新 Output/Attempt/Node/Run、唯一后继、River Job 与 Outbox；事务任一点失败全回滚。
- [ ] 多前驱并发、重复 Complete 和响应丢失只激活一个后继；不同 Output 返回稳定冲突。
- [ ] Human Task 等待不占 lease；提交完成 Human Node 并激活唯一后继；过期/版本/不同 decision/paused/cancelled 行为符合契约。
- [ ] Pause/Resume/Cancel HTTP/OpenAPI/Repository 在幂等、版本冲突、并发运行 Node、终态和非法状态下通过 Contract/Integration Test。
- [ ] Pause/Cancel 后没有新的副作用或后继 Job；Resume 只重投合法 Node且不增加 retry_no。
- [ ] 锁顺序在 Repository 设计和 SQL 集成测试中固定；两 Worker + control/complete/retry 并发 `-race -count=20` 无死锁和状态分叉。
- [ ] 前向迁移通过空库 Up、重复 Up、真实旧 `00003` 数据升级、terminal 兼容查询、legacy active 明确失败和有新数据时 Down 安全拒绝。
- [ ] `go test -race ./internal/workflow/...`、关键并发测试 `-count=20`、`go vet ./...`、OpenAPI check、go-review、sql-code-review 和 `git diff --check` 通过。

## Dependencies And Stop Gate

- 前置：M4-A 已提供冻结 Definition/Executor Registry、tx-scoped River Inserter、稳定 Job Args 和项目/River migration 基础；若这些契约未完成，本任务只能实现并验证纯状态机，不得自建 polling 或第二套队列绕过。
- Stop Gate：两 Worker并发、duplicate、lease reclaim、retry/manual、后继、Human 和 Pause/Resume/Cancel 的真实 PostgreSQL/River 测试全部通过，才可进入 M4-C Approval/Safe Writeback Dispatch。
