# Research: Workspace Analysis Stop 连续 409

- Query: 调查 `make compose-workspace-analysis-worker-restart-smoke` 中 Stop 响应序列 `409,409` 的错误码来源、Workflow version 与控制命令幂等语义，并判断应修前端恢复还是修 smoke 时序。
- Scope: internal
- Date: 2026-08-17

## Findings

### 结论

1. 连续 version 推进是真实可达竞态，不是候选屏障失效。取消 E2E 在 Question 接受后立即点击 Stop；取消路径明确不等待候选屏障。失败清理时看到 `entered_current=1` 只能说明 Workflow 后来推进到了 Synthesis，不能说明 cancel 发起时 Run version 已稳定。
2. 当前证据无法确定第二个 409 的精确 `error_code`。E2E 对两个 409 只输出状态序列，没有读取两个 Problem；运行时捕获还把 cancel 路由上的任意 409 都当作预期 HTTP 失败。正常 Runtime Control 分支中，第二个 409 可稳定为：
   - `WORKFLOW_VERSION_CONFLICT`：刷新 Answer 后、第二次 POST 取得 Run 锁前，又一个 Node Claim 推进了 Run version。这是本次最符合代码和时序的解释。
   - `WORKFLOW_CONTROL_CONFLICT`：版本已匹配但 Run 已终态或已存在取消请求；也包括相同幂等键绑定不一致。当前单击路径不会主动制造后两者，但并发控制方可以。
   - 其他 409 表示授权、审计 hook 或一致性异常，不属于可恢复 CAS 冲突，前端不能按状态码泛化重试。
3. 最小安全产品修复是前端严格有界的 version-conflict 恢复，而不是让 smoke 等候候选屏障。测试时序修正会移除“运行快速推进时用户立即 Stop”的真实覆盖。
4. 建议当前最小上限为 4 次恢复、5 次 cancel POST 总量。固定流程从初始 v1 到 Synthesis Claim 后 v5 有四次 Claim version 推进，且当前 deterministic candidate barrier 会让 v5 稳定。若要把上限覆盖到没有候选屏障的完整六节点基线，应采用 6 次恢复、7 次 POST；这仍不构成 lease reclaim 的无限保证。无论选 5 还是 7，总量必须是显式常量和测试边界，不能用 TanStack 通用 retry。

### 为什么连续 `WORKFLOW_VERSION_CONFLICT` 可达

- Question 接受响应返回 Start 事务的 Run version，初始通常为 v1：`internal/conversation/adapter/postgres/dispatch.go:650-656`。
- Stop 从当前 Answer 投影传入 `answer.workflow.version`：`web/src/features/rag/WorkspaceAnalysisTimeline.tsx:185-203`。
- 每个新的 Node Claim 都执行 `workflow.run.version=version+1`，即使 Run 已经是 `running`：`internal/workflow/adapter/postgres/runtime_state.go:156-164`。
- Heartbeat 只推进 Node version，不推进 Run version：`internal/workflow/adapter/postgres/runtime_state.go:292-363`。
- 中间 Node 成功后，如果归约状态仍是 `running`，`persistRunReduction` 不写 Run；下一个 Node Claim 再推进 Run version：`internal/workflow/adapter/postgres/runtime_state.go:1134-1157`、`internal/workflow/adapter/postgres/runtime_state.go:1457-1469`。
- 固定流程顺序是 Inspect -> Retrieve -> Read -> Synthesize -> Validate -> Review：`.trellis/spec/backend/workspace-analysis-contract.md:77-79`。因此初始 v1 可在前四次 Claim 后成为 v5，并在 Synthesis Provider barrier 内稳定。
- Answer refresh 是 Answer 与 Workflow Run 的一次联合权威读取：`internal/conversation/adapter/postgres/turns.go:21-49`、`internal/conversation/adapter/postgres/turns.go:125-145`。但 GET 与随后的 cancel POST 不是同一事务；两者之间的新 Claim 可以再次推进 version。

一个合法时序如下：

```text
Question response       Run v1
inspect Claim           Run v2
cancel(v1)              409 WORKFLOW_VERSION_CONFLICT
GET Answer              observes v2
retrieve Claim          Run v3
cancel(v2)              409 WORKFLOW_VERSION_CONFLICT
GET Answer              observes v3
...
synthesize Claim        Run v5, candidate barrier entered
cancel(v5)              accepted
```

### Runtime Control 的版本和幂等语义

- HTTP handler 从 body 读取 `expected_version`、从 header 读取 `Idempotency-Key`，然后委托 Application：`internal/workflow/http/handler.go:442-474`。
- Web 的控制幂等键包含 Run、action 和 expected version：`m9-workflow-${id}-${action}-${expectedVersion}`，所以严格递增的新 version 会产生新键：`web/src/api/business.ts:1327-1335`。
- Application request hash 同时绑定 schema、Run、action、expected version 与幂等键：`internal/workflow/application/runtime_contract.go:332-352`、`internal/workflow/application/runtime_contract.go:444-447`。
- PostgreSQL 先锁 Run，再查相同 `(run_id, command, idempotency_key)` 的持久命令：`internal/workflow/adapter/postgres/runtime_state.go:442-461`。
- 已有相同键且 hash/version 一致会 exact replay，检查发生在当前 Run version 和 terminal 检查之前：`internal/workflow/adapter/postgres/runtime_state.go:462-488`。这保留了成功响应丢失后的精确回放。
- 相同键绑定不同请求才返回 `WORKFLOW_CONTROL_CONFLICT`：`internal/workflow/adapter/postgres/runtime_state.go:462-465`。正常 Web 生成方式不会在同一 version 上改变绑定。
- 新键没有记录时，Run version 不等于 expected version 返回 `WORKFLOW_VERSION_CONFLICT`：`internal/workflow/adapter/postgres/runtime_state.go:490-495`。
- version 匹配后，terminal Run 或已经请求取消返回 `WORKFLOW_CONTROL_CONFLICT`：`internal/workflow/adapter/postgres/runtime_state.go:496-497`、`internal/workflow/adapter/postgres/runtime_state.go:560-563`。
- 成功 cancel 先推进 Run version，最后持久 control command；stale-version 返回发生在写入之前，因此失败尝试不会留下半条命令：`internal/workflow/adapter/postgres/runtime_state.go:564-624`。
- HTTP 将 `ErrorVersionConflict` 与一致性错误统一映射为 409，但保留稳定 `error_code`：`internal/workflow/http/handler.go:555-578`。因此前端必须匹配 code，不能只匹配 status。
- `BusinessApiError` 保存 HTTP status 和 Problem `error_code`，只有合法 Problem 才得到 code：`web/src/api/business.ts:265-283`、`web/src/api/business.ts:703-752`。

### 当前前端恢复边界

- 只匹配 `status===409 && errorCode==="WORKFLOW_VERSION_CONFLICT"`，这一点是正确的：`web/src/features/rag/commands.ts:59-62`。
- 首次冲突后刷新 Answer，并校验 Answer、Workspace、Conversation、Run、pending、可取消状态及 version 严格推进：`web/src/features/rag/commands.ts:66-84`。
- 但只允许一次刷新和一次重试：`web/src/features/rag/commands.ts:66-85`。
- 单测明确把“第二次 version conflict 后停止”冻结为当前行为：`web/src/features/rag/commands.test.tsx:133-161`。
- TanStack mutation 自身 `retry:false`：`web/src/features/rag/commands.ts:88-97`。建议继续保持，由命令函数拥有精确业务重试。

建议循环不变量：

```text
只捕获精确 WORKFLOW_VERSION_CONFLICT
-> 未到显式上限
-> GET 权威 Answer（不带 If-None-Match）
-> id/workspace/conversation/run 绑定完全一致
-> Answer 仍 pending，Workflow status 仍可取消
-> refreshedVersion > lastAttemptedVersion
-> 更新 lastAttemptedVersion 后再发一次 cancel
```

任一条件失败都抛出触发该轮恢复的 conflict。不得重试 `WORKFLOW_CONTROL_CONFLICT`、未知 409、网络错误、5xx、终态、绑定漂移、版本相等或倒退。每轮比较必须相对“上一轮尝试的 version”，不能一直与初始 input version 比较。

### 为什么不应修 smoke 时序

- E2E 在收到 Question 202、看到 Stop 后立刻单击：`web/e2e/workspace-analysis.smoke.spec.ts:369-388`。
- 它只接受 `[200]` 或 `[409,200]`；`[409,409]` 直接变成状态序列错误：`web/e2e/workspace-analysis.smoke.spec.ts:389-425`。
- 取消路径的 shell 设置 `fixture_entry_required=0`，因此不要求 candidate barrier entered：`deploy/compose-rag-smoke.sh:590-625`。
- 浏览器只有在 Stop 成功并完成断言后才创建 ready 文件；shell 随后释放 barrier：`web/e2e/workspace-analysis.smoke.spec.ts:432-437`、`deploy/compose-rag-smoke.sh:617-638`。
- 项目规范明确 cancelled/refused 可以在 candidate 前合法终止，不应等待不可达 entered：`.trellis/spec/backend/workspace-analysis-contract.md` 的 deterministic candidate barrier 测试条款。
- 因此把取消 smoke 改为等待 candidate barrier，会把真实 CAS 窗口移除，并偏离“运行中随时 Stop”的需求：`.trellis/tasks/08-15-workspace-agent-user-flow/prd.md:74-80`。

### E2E 诊断缺口

- 运行时问题捕获将 cancel 路由上的任意 409 都视为预期：`web/e2e/workspace-analysis.smoke.spec.ts:327-338`。
- 只有单个 409 或 `[409,200]` 路径会调用 `assertWorkflowVersionConflict`；`[409,409]` 不读取两个 body：`web/e2e/workspace-analysis.smoke.spec.ts:397-425`。
- Shell 只从 Playwright 日志抽取状态类 gate code：`deploy/compose-rag-smoke.sh:117-145`。所以历史失败输出不能反推第二个 code。

建议 E2E 记录并校验每个 cancel 响应的安全 tuple：`status:error_code`。合法序列只能是零到上限个 `409:WORKFLOW_VERSION_CONFLICT`，最后一个 `200`。任一 `409:WORKFLOW_CONTROL_CONFLICT`、未知 code、非 Problem 结构、超上限或无最终 200 都应失败，并把仅含稳定 code 的有界序列写入 gate error；不要输出 Problem message/body。

## Files Found

- `web/src/features/rag/commands.ts` - Stop 的单次 stale-version 恢复和严格绑定校验。
- `web/src/features/rag/commands.test.tsx` - 当前只允许一次恢复的单测矩阵。
- `web/src/features/rag/WorkspaceAnalysisTimeline.tsx` - Stop 使用 Answer 投影中的 Workflow version。
- `web/src/api/business.ts` - Business Problem 解码、控制幂等键和响应 version 校验。
- `web/src/api/conversation.ts` - 不带 ETag 的 Answer 权威刷新。
- `web/e2e/workspace-analysis.smoke.spec.ts` - Stop 请求、响应捕获和当前 `[200] | [409,200]` 断言。
- `deploy/compose-rag-smoke.sh` - candidate barrier generation、取消分支不等 entered、失败诊断。
- `internal/workflow/application/runtime_contract.go` - 控制 request hash 与应用边界。
- `internal/workflow/adapter/postgres/runtime_state.go` - Claim version 推进、Control CAS、exact replay 和冲突顺序。
- `internal/workflow/http/handler.go` - 控制 HTTP 输入与 409 Problem 映射。
- `internal/conversation/adapter/postgres/turns.go` - Answer/Workflow 联合权威读取。
- `internal/conversation/adapter/postgres/dispatch.go` - Question 接受时返回初始 Run version。
- `internal/conversation/workflow/workspace_analysis_contract.go` - 六节点 no-retry Definition。

## Required Test Matrix

| 层 | Case | 必须断言 |
| --- | --- | --- |
| Web unit | 首次成功 | 1 次 cancel、0 次 GET |
| Web unit | 1 次 version conflict 后成功 | versions 严格递增，1 GET、2 cancel |
| Web unit | 2 次连续 version conflict 后成功 | 2 GET、3 cancel；直接覆盖本次失败 |
| Web unit | 上限边界成功 | 恰好 `maxRecoveries` 个 conflict 后最后一次成功 |
| Web unit | 上限耗尽 | 不发送第 `maxAttempts+1` 次 cancel，不再 GET，抛最后一个 conflict |
| Web unit | 后续出现 `WORKFLOW_CONTROL_CONFLICT` | 立即停止，不再 GET/cancel |
| Web unit | 普通 409、网络、5xx、无合法 Problem code | 0 次业务恢复 |
| Web unit | 每一恢复轮绑定漂移 | workspace/answer/conversation/run 任一漂移均 fail closed |
| Web unit | 每一恢复轮状态变化 | Answer 非 pending 或 Workflow 非可取消均 fail closed |
| Web unit | 每一恢复轮 version 非单调 | 相等或倒退均 fail closed；跳跃式增长允许 |
| Web unit | Answer 不存在或 GET 失败 | 不再 cancel |
| Browser | 立即 Stop | 不等待 candidate entered；接受 bounded `VERSION_CONFLICT* -> 200` |
| Browser | 每个 409 Problem | 精确 schema、`error_code`、`retryable=false`；未知 code 必须失败 |
| Browser | 请求绑定 | 每轮 `expected_version` 严格递增，幂等键与该 version 一致 |
| Browser | 响应数量 | 总数不超过显式上限，最后必须 200 |
| PostgreSQL（建议补强） | stale version | `WORKFLOW_VERSION_CONFLICT`，Run/control_command/audit 均不变 |
| PostgreSQL（建议补强） | accepted cancel exact replay | 相同键返回相同结果，不新增 control/audit |
| PostgreSQL（建议补强） | current version + 已取消请求 | `WORKFLOW_CONTROL_CONFLICT`，证明 code 顺序 |

## External References

- 无。结论全部来自当前仓库的一方源代码、任务文档和项目规范；未依赖外部 API 或版本文档。

## Related Specs

- `.trellis/spec/backend/workspace-analysis-contract.md:77-79` - 固定六节点顺序。
- `.trellis/spec/backend/workspace-analysis-contract.md:140-153` - Stop 使用权威 Workflow version，Browser/Compose 必须验证实际 Stop。
- `.trellis/tasks/08-15-workspace-agent-user-flow/prd.md:74-80` - 运行中 Stop 和安全检查点需求。
- `.trellis/tasks/08-15-workspace-agent-user-flow/design.md:313-319` - Running 状态 Stop UI 和权威投影优先。

## Caveats / Not Found

- 失败 smoke 的临时 `playwright.log` 已随 cleanup 删除；仓库和 `/tmp` 中未找到可读取的本次响应 body。因此不能把第二个 409 宣称为已证实的 `WORKFLOW_VERSION_CONFLICT`，只能说明该 code 的连续出现由当前代码真实允许。
- `entered_current=1` 是失败诊断时的状态，不包含 cancel 请求时间戳或请求对应 version，不能用于证明 E2E 点击前已进入 barrier。
- 5 次 POST 上限是当前 deterministic Synthesis barrier 场景的最小结构性上限；完整六节点或 lease reclaim 需要更高上限或接受终态刷新后停止恢复。产品代码必须保持显式有界，不能声称在任意调度下保证 cancel 一定抢先成功。
- 本研究未修改代码，也未重跑 Compose smoke。
