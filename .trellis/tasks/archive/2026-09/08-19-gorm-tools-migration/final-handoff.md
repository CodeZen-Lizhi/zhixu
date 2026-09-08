# Tools GORM Final 交接（2026-09-08）

Tools child 的实现、直接依赖与精简政策最低验证证据已完成。未修改 `cmd/**`、其他 owner/Schema 或测试，
未删除 legacy；进入本轮前两个 integration fixture 的用户改动完整保留。状态与归档由主会话处理。

## 同 Pool 构造顺序

以下 constructor 均检查返回错误，并从同一个 `*platformpostgres.Pool` 构造。Repository 不拥有独立连接池或 Close。

| 能力 | 构造 |
| --- | --- |
| Workflow Policy | `workflowpostgres.NewGORMToolExecutionPolicySnapshot(pool)` |
| Workflow Recovery | `workflowpostgres.NewGORMToolCallRecoveryFence(pool)` |
| Workflow WA Fence | `workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(pool)` |
| Agent participant/refusal/authority | `agentpostgres.NewGORMRepository(pool)`；三个 Port 使用同一对象 |
| Events | `eventspostgres.NewGORMStore(pool)` |
| Audit | `auditpostgres.NewGORMStore(pool)` → `auditapplication.NewRecorder(store)` |
| 普通 Tools | `toolpostgres.NewGORMRepository(pool, policySnapshot, recoveryFence)` |
| Workspace Analysis Tools | `toolpostgres.NewGORMWorkspaceAnalysisRepository(pool, executionFence, agentCore, agentCore, agentCore, events, auditRecorder)` |

Workspace Analysis constructor 强制所有协作者非 nil/typed-nil，只有 Tools 拥有 outer UoW；不得用 no-op、
另一事务或提交后补写替代 Event/Audit/Agent 原子性。应用停止所有使用者后统一关闭 Pool。

## ExecutionService 必须看到完整能力

`ExecutionService` 会对 `service.calls` 做动态断言，启用 Workspace Analysis 时 Final 必须提供同一个组合对象：

| 路径 | 接口来源 |
| --- | --- |
| 普通调用、Timeline、恢复 | 普通 Repository 的 `ToolCallRepository` |
| Policy | 普通 Repository 的 `WorkflowPolicyReader` |
| Trusted Write | 普通 Repository 的 `TrustedWriteCallRepository` |
| 成功/失败 Receipt | WA Repository 的 `ResultReceiptRepository`、`ResultReceiptFailureRepository` |
| WA 授权/终结、拒绝 | WA Repository 的 `WorkspaceAnalysisToolOperationRepository`、`WorkspaceAnalysisToolRefusalRepository` |
| Search/publication/synthesis/citation | WA Repository 的六个 authority/receipt reader 接口 |

动态断言目前位于 `internal/tools/application/execution.go` 的 Receipt、Receipt Failure 和 WA Operation 路径，
以及 `workspace_analysis_execution.go` 的 Refusal 路径。单独把普通 GORMRepository 传给 `NewExecutionService`
会缺失上述能力。Final 负责完整 Application 接口集合和纯委托组合；组合本身不开始事务、不复制业务逻辑。

六个读取接口为 `SearchKnowledgeV2ReceiptReader`、`SearchKnowledgeV2PublicationAuthorityReader`、
`WorkspaceAnalysisSynthesisEvidenceAuthorityReader`、`ValidateCitationV3AuthorityReader`、
`ValidateCitationV3ReceiptReader`、`ValidateCitationV3PublicationAuthorityReader`。

## Final 生产接线位置

以下为 child 审查时定位，编辑后按符号搜索：

- `cmd/worker/main.go`：`toolRuntimeComponents.repository` 目前是 legacy concrete pointer，改为完整 Application 能力集合。
- `newToolRuntimeComponentsWithWorkspaceAnalysisAudit` 内的普通与 `NewRepositoryWithWorkspaceAnalysisEventsAndAudit` constructor（2255、2264 附近）。
- `NewExecutionService`（2387 附近）目前两次传入同一 Repository；切换后保留 Policy 与完整 Calls 能力。
- `newWorkspaceAnalysisToolExecutors`（2411 附近）改为所需 authority 接口，避免要求 legacy concrete pointer。
- 生产 Tool execution/Workspace Analysis 到 Worker 的完整可达性与队列边界在 Final 验证。

Foundation 不验证 active scope 的 Pool identity；Final 必须保证所有 collaborator 的 Pool 相同。
普通 Tools SQL 只可访问 Tool-owned Call/Receipt facts，Workflow/Agent facts 继续经 scoped Port 获取。

## Legacy 清理边界

Final 前保留 `repository.go`、`calls.go`、`policy.go`、`trusted_write.go`、`result_receipts.go`、
`result_receipt_failures.go`、`workspace_analysis_operations.go`、`workspace_analysis_authority.go`、
`workspace_analysis_refusals.go`、`workspace_analysis_events.go` 与 legacy `errors.go`。

GORM 复用上述文件中的 validation、equality、receipt codec、scanner、SQL column constants 和错误码。
Final 先提取纯 helper，再删除 pgx implementation/constructor/transaction seam，核对现有 fixture 引用。
普通 production data access 不允许 pgx；平台/Worker River runtime 保留范围依父任务 allowlist。

本轮只修改 `gorm_errors.go`：SQLSTATE 由平台投影读取，普通 `23505` 与 receipt `23505` 仍分别映射原 conflict code。
所有 Tools GORM 文件已无 pgx/pgconn import，legacy 分类保持不变。

## 验证与 Review

精确命令见 `research/static-validation.md`：

- Call/Policy/refusal/start/CAS/UNKNOWN/Timeline/敏感边界 + participant/receipt/authority + Event 失败全回滚：PASS，45.019s，legacy/GORM 对照。
- Refusal/Audit 原子回放、不写 Server Event、无预算副作用：PASS，19.278s，legacy/GORM 对照。
- 修改后的 GORM 主路径复验：PASS，26.894s。
- Agent/Tools 局部 unit、vet、gofmt、定向 diff check、Trellis validate：PASS。
- Go/SQL/Trellis 复审无未解决 P0/P1/P2。两 Repository 合计覆盖 legacy 21 个公开方法；本轮没有修改 SQL/锁序/状态机。

完整 fault/response-loss/SQLSTATE/并发/连接释放/EXPLAIN/全量 race 和多进程恢复公平性没有全跑，按风险或 Final
门禁处理。本轮没有真实 Provider/browser 或 production Worker 操作。

## 回滚

本轮错误依赖调整可单独撤销 `gorm_errors.go` diff，不影响数据。Final 回滚须恢复完整构造与依赖链，
不能混接 transaction、拆事务、双写或 fallback；不回滚 Tool/Receipt/预算/Event/Audit 历史事实或 migration。
