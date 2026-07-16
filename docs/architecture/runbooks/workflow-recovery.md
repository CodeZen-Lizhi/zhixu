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

