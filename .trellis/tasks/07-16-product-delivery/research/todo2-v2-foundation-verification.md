# TODO2 v2 决策持久化检查记录

Owner：dynamic_foundation。2026-09-09 更新。Agent 持久化、Run starter、`00098` 整合及本文列出的终态 PostgreSQL 回归已完成。全部正式实库结果使用标准迁移目录与共享 `atlas.sum`，迁移至 `00099`；没有使用 checksum overlay。本文不代表 TODO2、TODO4 或整个项目已完成，本机 Compose/River、页面与最终部署由主会话整合。

## 实现范围

- `internal/agent/domain/workspace_analysis_v2_*.go` 与既有 Operation/Run/Candidate 分支：严格四字段决策、五种 action、v1/v2 独立预算和 deadline、candidate schema 2。
- `internal/agent/application/workspace_analysis_v2_runs.go`：创建 v2 Run；历史幂等重放读取已持久的版本与配置。`workspace_analysis_runs.go` 中的版本中立比较器使用 `Time.Equal` 校验时间，v1 scoped starter 共用同一实现。
- `internal/agent/adapter/postgres/gorm_workspace_analysis_decision_authorization.go`、`gorm_workspace_analysis_decisions.go` 与既有 model operations/finalize：Attempt 内复用 ModelRun、逐 Call 授权结算、journal 批量读取、exact replay、UNKNOWN 全额结算及真实 PENDING admission denial。
- `atlas/migrations/00098_workspace_analysis_dynamic.sql`：v1/v2 约束、append-only decision/journal/evidence、ModelRun checkpoint 和跨表 closure。已并入 dynamic_contract 的 Finalizer、Run/Answer 与终态 hook SQL，文件为 4172 行；`BEGIN/END TODO2 V2 CONVERSATION FINALIZER CONTRACTS` 标记内正文与冻结片段一致，v1 bodies/common checks 和 `00091` trigger 路由保留。
- 永久实库测试：`workspace_analysis_dynamic_integration_test.go`、`workspace_analysis_dynamic_finalizer_integration_test.go`、`workspace_analysis_dynamic_publication_integration_test.go`、`workspace_analysis_dynamic_terminal_hook_integration_test.go`、`workspace_analysis_dynamic_rejection_integration_test.go`、`workspace_analysis_dynamic_runs_integration_test.go`；Run starter 单测位于 `application/workspace_analysis_v2_runs_test.go`。旧 v1 capability integration 的 replay 断言已同步历史重放合同。
- [Runtime 接口](todo2-v2-runtime-interface.md) 已同步四字段、Citation action、全局决策序号与 Attempt 内 CallNo、真实 denial 和独立 final Citation。

Finalizer Go、成功/终止 proof 与 Workflow terminal hook 由 dynamic_contract 实现，本范围负责 SQL 并入与实库验证；Tools/Evidence 与 `00099` 归 dynamic_tools；Eino/Workflow executor 和模型 runner 归 dynamic_runtime。`atlas.sum`、声明 Schema、main 和部署归主会话，本范围没有改写这些共享产物。

## 正式验证

### 单测与静态检查

| 验证 | 结果 |
| --- | --- |
| Agent domain/application/postgres 与 Conversation workflow 的 Workspace Analysis 单测 | PASS；复用已有证据 |
| Agent domain/application/postgres 的 `TestWorkspaceAnalysis*` 定向 race | PASS，1.695 / 1.596 / 1.411 秒 |
| Agent domain/application/postgres 定向 `go vet -mod=vendor` | PASS |
| `go run -mod=vendor ./cmd/persistencecheck` | PASS，30 个 owner、1950 个 Go 文件（执行时快照） |
| 最新 v1/v2 Run starter 定向单测及 race | PASS，0.462 / 1.420 秒；包括同一时刻不同 Location、真实时间与配置漂移拒绝 |
| 最新 Run starter 修改后的 `go vet -mod=vendor ./internal/agent/application` | PASS |
| 最终格式、空白与整合核对 | 24 个 Go 文件 gofmt PASS；27 个 tracked/untracked 文件空白 PASS；Finalizer SQL 正文一致，临时 smoke 文件不存在 |

### 标准 PostgreSQL 入口

以下各批次均使用正式 `-tags=integration` 测试入口、完整迁移与数据库约束，没有临时 checksum、副本迁移或关闭约束。模型响应使用确定性的持久端口 fixture；RuntimeCoordinator 和事务 hook 为生产实现。这些结果证明协议、事务与恢复行为，不是实际 Provider 的回答质量评测，也不是完整 River Worker E2E。

| 场景 | 实际结果 |
| --- | --- |
| 无证据 finish → synthesize 节点拒答，Finalizer exact replay 与终态 Timeline | PASS，10.685 秒 |
| 第 13 个 Decision 真实 budget denial → failed，关闭原 RUNNING ModelRun；无 Git 成功候选 → 独立 Citation → Review → publication | PASS，25.091 秒 |
| Deadline：授权前与真实 PENDING 两条路径；replacement UNKNOWN 完整终态与全额记账 | PASS，19.036 秒 |
| 成功决策前缀取消、Decision 13 PENDING 后取消、成功前缀 Runtime failure；真实 RuntimeCoordinator、原子终态与 hook replay | PASS，25.599 秒 |
| CitationInvalid、ReviewRejected、ModelFailed；E1/E6 同来源元组的独立读取与公开 Citation 去重 | PASS，37.400 秒 |
| Decision 提交回执丢失恢复、成功前缀 replacement、Search → Read → Search → Read 的 journal/Timeline 顺序 | PASS，32.031 秒 |
| ToolFailed / ToolUnknown，Executor 只执行一次，Tool 与 Finalizer exact replay、前缀关闭和精确预算 | PASS，21.684 秒 |
| v2 ScopedRunStart：`Asia/Shanghai` 会话时区、Answer + Run 事务回滚、queued 创建、running 后不同 config replay、错误 Answer 绑定拒绝 | PASS，9.189 秒 |
| v1 CapabilityCheckedRunStarter：无 readiness 新请求拒绝、事务回滚、正常创建、Worker release 后按历史身份 replay、记录不新增 | PASS，6.205 秒 |

最后一项的复验命令：

```sh
go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^TestWorkspaceAnalysisCapabilityCheckedRunStarterAtomicityIntegration$' \
  -count=1 -timeout=30s
```

上述场景还实际覆盖：

- 第 13 个 Decision 可持久为 PENDING，但不能创建 Call/Reservation；重放复用同一 denial，不能伪造 admission proof。
- 活跃 Attempt 保留同一个 RUNNING ModelRun；replacement 的新 ModelRun 从 CallNo 1 接续全局 decision ordinal，已完成工具不重复执行。
- UNKNOWN 无可证明 usage 时按完整 reservation（输入 65536、decision 输出 512）结算，不能按零记账。
- 成功发布的 Citation 独立绑定 candidate；loop Citation receipt 不能替代 final validation，也不能顶替 rejection proof。CitationInvalid 在 review_publish 节点终止，且失败 Citation 禁止开始 Review。
- E1 与 E6 的两次 Read 均保留为事实；相同 immutable 来源元组只公开一条 Citation。无 confirmed Claim 的 E2 会被最终 Citation gate 拒绝。
- Cancel → Complete checkpoint 同事务关闭 Run、Node、Attempt、Answer 与 proof；先关闭决策前缀，再生成终态证明，最后强制检查 deferred constraints。

dynamic_contract 另已完成 8 项 v1 Finalizer/cancel/runtime/audit 正式 PostgreSQL 回归与四包 race/vet，见 [Finalizer 检查记录](todo2-v2-finalizer-verification.md)。本范围没有重复该矩阵。

## 已修复的实际缺陷

1. 成功前缀 replacement 捕获 SQLSTATE `42702`：`close_workspace_analysis_v2_prior_decision_runs` 中变量 `model_id` 与列重名。改为 `checkpoint_model_id`，checkpoint helper 接口不变；正式恢复复验通过。
2. 首次正式迁移捕获 SQLSTATE `42601`：Finalizer 的 Answer schema 与 preoperation deadline 中三处比较使用裸 `CASE`。contract owner 在冻结片段中加括号，本范围同步到 `00098`，主会话更新 checksum；其后全部正式实库批次通过。
3. 真实 Compose 的新分析 POST 曾返回 `HTTP 409 AGENT_WORKSPACE_ANALYSIS_RUN_START_CONFLICT`。v2 starter 对整个 Run 使用 `reflect.DeepEqual`，把 PostgreSQL 解码时间的 Location 差异误判为持久事实漂移。改用共享的逐字段比较器，所有时间用 `Time.Equal`，预算与非时间绑定保持严格相等；没有放宽真实时间精度。对应单测修前实际失败，修后通过；真实 PG scoped starter 同时证明回滚、创建及历史 replay。
4. 旧 v1 capability integration 仍断言 Worker release 后历史 replay 必须报 readiness 不可用，已与当前合同不符。修前实际返回成功而使旧断言失败；现断言 replay 保留原 ID、Definition/policy、hash、catalog、config，且不新增记录。新请求仍受 readiness 限制，未回退正确的历史重放逻辑。

此前已修复 v2 capability 被 v1 Validate 拒绝、候选 terminal binding 被 v1 schema 限制、Candidate SQL CASE 括号与 nullable completed_at 约束。Runtime owner 已补 denial replay 复用 OperationID，以及模型结算前验证来源短引用。

## 历史探索证据与清理

早期 `workspace_analysis_dynamic_smoke_integration_test.go` 曾复制迁移到独立目录并自行计算副本 checksum；当时的 7.952 秒首条决策、24.228 秒恢复/UNKNOWN/deadline、9.611 秒成功前缀 replacement、10.127 秒 Timeline 仅作为开发排错历史，不能作为标准 checksum 验收。早期第 13 次 Decision denial 也曾通过同一探索入口。

该临时文件已删除并复核不存在。上文正式实库批次已替代相关探索证据；不再留有“等待合并 Finalizer”“尚待删除 overlay”的未完成项。

## 验证边界与后续归属

- 完整 Compose/River、真实 HTTP 与桌面/窄屏页面验收以及本机部署由主会话和 dynamic_runtime 继续汇总，见 [Compose 记录](todo2-v2-compose-verification.md)。RunStart 修复已通知两者重新执行原失败链路，本文不预写其结果。
- v2 Runtime failure SQL 支持 node attempt 为 0 且尚无 Attempt 行的分支；本轮没有对该精确分支单独执行实库测试。已执行的是带成功决策前缀的真实 Runtime failure，不能据此声称穷尽所有终止时机。
- M11 全产品 E2E、实际 Provider 质量与最终发布包按用户要求暂缓。无 Git commit/push，本范围不执行最终部署。
