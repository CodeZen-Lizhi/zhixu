# Runbook：Workflow 故障恢复

## 1. 触发

- Run 长时间 Running。
- Lease 反复过期。
- 重试耗尽。
- Human Task 无法提交。
- Side Effect 状态未知。
- Compensation 失败。

## 2. 分类

- Dependency：模型、DB、Git、网络。
- Definition：Schema/版本。
- Data：证据/目标变化。
- Side Effect：文件/Git/索引。
- Worker：Crash/Lease。

## 3. 安全检查

1. 查询 API `/readyz` 与 Worker 容器内 `:8081/readyz`，不要用 API health 代替
   Worker health。
2. 查看 Workflow/Node/Attempt/Tool/Audit。
3. 确认 Node Type、River Job ID/attempt 和 Worker queue。
4. 检查 Idempotency Record、Writeback Execution 和 checkpoint。
5. 检查目标文件、Git HEAD、exact Trailer 和 Mapping/Outbox。
6. 检查 lease_owner、lease_until、heartbeat 和 River Job 状态。
7. 判断是否可自动重试；证据不完整时进入 Manual Recovery。

Worker `/readyz` 的稳定 code 可用于第一响应：

| Code | 含义 | 第一动作 |
|---|---|---|
| `WORKER_DATABASE_UNAVAILABLE` | DB Ping 失败 | 恢复 PostgreSQL/网络，不手工完成 Job |
| `WORKER_RIVER_SCHEMA_UNAVAILABLE` | River migration Validate 失败 | 停 Worker，运行 `/app/zhixu-migrate` 并检查版本 |
| `WORKER_RIVER_NOT_STARTED` | River Client 未运行或已停止 | 查看启动/停机稳定错误码，避免重复启动同进程 |
| `WORKER_DEFINITIONS_UNAVAILABLE` | Definition Registry 未就绪 | 校验部署版本和 Definition 注册 |
| `WORKER_EXECUTORS_UNAVAILABLE` | Executor Registry 未就绪 | 校验 Job Kind/Schema 与 Executor 兼容性 |
| `WORKER_DEPENDENCIES_UNAVAILABLE` | 启用 Definition 缺依赖 | 修复 Composition，不用 fake executor 绕过 |
| `WORKER_SHUTTING_DOWN` | 已摘 readiness | 等待该实例退出或启动兼容的新实例 |

## 4. 普通重试

适用：

- 无副作用。
- 明确未执行。
- 幂等工具有结果。

操作：

- Release/Expire Lease。
- 创建新 Attempt。
- 保留旧错误。

不得直接把 River Job 标成 completed 来“释放” Workflow lease。更高 River
transport attempt 在旧 lease 未过期时应返回 retryable `WORKFLOW_LEASE_HELD`；
lease 到期后由新的 delivery reclaim 并追加 Attempt。

## 5. Side Effect 未知

- 不立即重试。
- 检查文件 Hash。
- 检查 Commit Mapping。
- 检查 Tool Idempotency。
- 得出 Executed/Not Executed/Unknown。

Unknown → MANUAL_RECOVERY_REQUIRED。

## 6. Human Task

- 校验 Proposal Version。
- 旧 Task 过期则创建新 Task。
- 双提交只接受第一个 Idempotency Key。

## 7. Definition 不兼容

- 运行中 Run 使用旧 Definition。
- 若旧执行器不可用，编写显式 Migration/Upcaster。
- 不直接篡改 Context JSON。

## 8. 取消

- 设置 Cancel Requested。
- 停止新 Node。
- 当前 Side Effect 完成/补偿。
- 记录未撤销结果。
- Safe Writeback 已创建 Execution 后，只有 `needs_revision`、`apply_failed`、
  `compensated`、`manual_recovery_required`、`verify_failed`、`rolled_back`、
  `completed`，或 `verifying` 且 cleanup 已完成，才允许 Workflow/Node 归约为
  terminal cancelled。
- 若返回 `WORKFLOW_CANCELLATION_DEFERRED`，保持 Job 可恢复并继续 exact recovery；
  不要手工把 Run 改为 cancelled，也不要删除 River Job、temp/backup 或 lease 事实。

## 9. Worker 停机、Stuck Job 与崩溃

### 9.1 计划停机

1. 发送 SIGTERM/SIGINT。
2. 确认 Worker `/readyz` 返回 503 `WORKER_SHUTTING_DOWN`。
3. 等待 graceful `Stop`；River 到 soft stop timeout 后会取消活动 Job Context，
   但仍等待 Worker 返回。
4. 超过 hard deadline 视为失败退出，不记录“优雅完成”。按 checkpoint/lease
   恢复，不假设外部副作用已回滚。

### 9.2 Fatal invariant

只有“继续执行可能扩大副作用”的稳定 Runtime/Supervisor invariant 才允许首次
选择 `StopAndCancel`。graceful 与 emergency 的第一个事件获胜，后续 signal/fatal
不得再调用另一停机 API。操作人员不应连续发送不同信号来模拟两阶段停机。

### 9.3 非优雅退出与 rescue

1. 保留 River Job、Workflow Attempt、Writeback Execution、temp/backup 和 Git
   现场；不要清理恢复证据。
2. 启动与当前 Job Kind/Args/Schema 兼容的新 Worker，确认相同
   `ZHIXU_WORKER_QUEUE`。
3. 等待 River stuck rescue 与 Workflow lease 到期，不手工制造第二 Job。
4. 观察新 delivery：active lease 时允许 `WORKFLOW_LEASE_HELD` 重投；到期后应
   reclaim 并从 durable checkpoint 继续。
5. 验证 Git Commit/Mapping/Reindex Outbox/领域副作用唯一，Run/Execution 状态
   一致，再恢复流量。

仓库同时保留两类独立证据：`writeback_fault_smoke_integration_test.go` 证明文件/Git
checkpoint 故障恢复；`worker_kill_smoke_integration_test.go` 启动真实 Worker 子进程、
在 Job running 后发送 SIGKILL，并验证 River maintenance rescue 后新 attempt 完成。
SIGKILL smoke 使用独立临时数据库并执行完整迁移；只换 queue 无法隔离同一 Schema 的
River maintenance leader，可能让常驻 Worker 的 rescue 配置污染测试。
两者都不能单独替代 Approval 双 Worker、Workflow lease reclaim 与领域副作用唯一性
测试，发布时应组合执行。

## 10. Telemetry 故障

- `disabled`：没有 exporter 是预期状态。
- `optional`：exporter 不可用记录稳定 degraded code，Workflow Runtime 可继续
  ready；必须在事件记录中声明观测缺口。
- `required`：初始化失败时 Worker 不启动。恢复 endpoint/factory 后重新启动，
  不绕过为 noop success。
- 健康和日志只记录稳定 code，不粘贴 endpoint、DSN 或原始 exporter error。

## 11. 验证

- Run 进入真实终态。
- 无重复 Commit/Write。
- 后继节点只创建一次。
- Audit 连续。
- 受影响 Proposal 状态正确。
- Worker `/readyz` 恢复 200，且 API/Worker health 语义没有混用。
- 未出现 terminal Workflow + 非终态 Writeback Execution。

## 12. Approval Safe Writeback Dispatch

- `proposal.workflow_run_id` 非空时，先按原 Approval ID、固定 Definition/Node、Outbox 和 River Job 做 exact replay；禁止重新捕获已变化的文件/Git 基线。
- Approval 已存在但 `workflow_run_id` 为空时，必须重新执行 Target Hash、strict-clean attached Git HEAD 安全门，再原子补建 Run/Node/Job；`approved_git_head=NULL` 不允许补建。
- Worker 在 Claim 后先查询 `safe-writeback:<node_run_id>`；Execution 存在则验证 Workspace/Run/Node/Proposal/Revision/Approval/Hash/HEAD 全绑定，冲突进入 `WRITEBACK_EXECUTION_BINDING_CONFLICT` 人工恢复。
- River transport error 后更高 Job attempt 若遇到尚未过期的旧 Workflow lease，应观察到 retryable `WORKFLOW_LEASE_HELD` 并继续重投；若 Job 已 completed 但 Node 仍 running，先检查是否运行了旧版本 stale 逻辑，禁止手工重复创建领域 Execution。
- Execution 不存在时必须使用新的 Authorization generation；旧 delivery 的明文 Credential 不可恢复或复用。Begin 响应丢失后按 exact key 查询，禁止创建第二 Execution。
- Run `succeeded` 只表示写回已到 `verifying/index_pending`；Retrieval/Regression 未完成时不得手工改为 Proposal `completed`。
