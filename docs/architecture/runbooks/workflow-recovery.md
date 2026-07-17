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

1. 查看 Workflow/Node/Tool/Audit。
2. 确认 Node Type。
3. 检查 Idempotency Record。
4. 检查目标文件和 Git。
5. 检查 Lease。
6. 判断是否可自动重试。

## 4. 普通重试

适用：

- 无副作用。
- 明确未执行。
- 幂等工具有结果。

操作：

- Release/Expire Lease。
- 创建新 Attempt。
- 保留旧错误。

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

## 9. 验证

- Run 进入真实终态。
- 无重复 Commit/Write。
- 后继节点只创建一次。
- Audit 连续。
- 受影响 Proposal 状态正确。

## 10. Approval Safe Writeback Dispatch

- `proposal.workflow_run_id` 非空时，先按原 Approval ID、固定 Definition/Node、Outbox 和 River Job 做 exact replay；禁止重新捕获已变化的文件/Git 基线。
- Approval 已存在但 `workflow_run_id` 为空时，必须重新执行 Target Hash、strict-clean attached Git HEAD 安全门，再原子补建 Run/Node/Job；`approved_git_head=NULL` 不允许补建。
- Worker 在 Claim 后先查询 `safe-writeback:<node_run_id>`；Execution 存在则验证 Workspace/Run/Node/Proposal/Revision/Approval/Hash/HEAD 全绑定，冲突进入 `WRITEBACK_EXECUTION_BINDING_CONFLICT` 人工恢复。
- River transport error 后更高 Job attempt 若遇到尚未过期的旧 Workflow lease，应观察到 retryable `WORKFLOW_LEASE_HELD` 并继续重投；若 Job 已 completed 但 Node 仍 running，先检查是否运行了旧版本 stale 逻辑，禁止手工重复创建领域 Execution。
- Execution 不存在时必须使用新的 Authorization generation；旧 delivery 的明文 Credential 不可恢复或复用。Begin 响应丢失后按 exact key 查询，禁止创建第二 Execution。
- Run `succeeded` 只表示写回已到 `verifying/index_pending`；Retrieval/Regression 未完成时不得手工改为 Proposal `completed`。
