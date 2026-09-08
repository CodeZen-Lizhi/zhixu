# Agent / Tools Final 收口记录

日期：2026-09-08。实施范围为 `internal/agent/**`、`internal/tools/**`；七个未完成模块的核心实库 gate 通过后，由 Final 主会话放行本次 legacy 删除。API/Worker/CLI Composition、Tools Core + Workspace Analysis 聚合及其他 owner 的消费者由主会话整合。

## 变更和保留边界

- 删除 Agent、Tools 普通业务 pgx Repository、SQL 执行 helper、事务接口、构造和旧错误分类器。整文件删除 Agent `calls.go`、`runs.go`、`workspace_analysis_candidate_authority.go`，以及 Tools `repository.go`、`workspace_analysis_events.go`；其余旧文件只保留共用 codec、scanner、绑定校验和错误码。
- 删除 Agent Application 的 `ModelRunTxFinalizer`、`WorkspaceAnalysisRunPersistence`、`WorkspaceAnalysisRunStarter`、旧 Run Service、旧 readiness/capability wrapper 和 opaque transaction nil helper。最终契约为 `ScopedModelRunStore`、窄 `ScopedModelRunFinalizer`、`ScopedWorkspaceAnalysis*` 和 Capability lifecycle Port。
- 删除 Tools refusal audit 的旧 `any` 事务端口。GORM 的所有 `NewGORM*` 构造名称和签名保持不变，跨 owner 合作者始终接收同一个 `foundation.TransactionScope`。
- Agent 和 Tools 的 GORM 错误分类统一调用 `platform/postgres.SQLState`，不再由业务 Adapter 导入 `pgconn`；SQLSTATE、`foundation.Error`、取消、重试和错误链语义保留。
- Tools Workspace Analysis 事件写入在缺失 Repository、Appender 或 scope（包含 typed nil）时返回既有数据库不可用错误，避免 panic 或漏记事件；未改变事件投影、精确重放和外层事务边界。
- 保留现有纯合同测试使用的 validator，例如 `activeReceiptExecutionFence`、`validateWorkspaceAnalysisOperationReceiptBinding` 和 `workspaceAnalysisToolAdmissionError`。这些函数没有 pgx/SQL 执行路径，不是兼容 Repository。

## 既有测试原位迁移

- 删除 legacy 变体、旧构造和 pgx commit-response-loss wrapper；既有断言改用最终 GORM Repository。提交响应丢失仍由真实 UnitOfWork 提交后返回错误来注入，未用假事务代替持久状态。
- Run/capability 的 Application fake 改用 `foundation.TransactionScope`；模型闭包及 capability SQL 合同测试直接检验最终 GORM 实现。
- Tools authority、事件、nil dependency 和 SQLSTATE 合同测试转向 GORM。旧“无事件 Appender 时跳过”的兼容断言改为最终实现必须拒绝缺失 Appender。
- Agent Workflow fixture 改为 `testdb.Require(...FailWhenUnavailable, MaxConns:16)`，共享一个 Platform Pool，Agent、Knowledge、Workspace、Retrieval 均使用 GORM 构造。移除历史 DSN 缺失跳过和手工建库/迁移路径。
- Agent Workflow 的既有 fixture 同步显式注入已有 Eino Structured Scheduler；实际 Workspace root 在首次 INSERT 时传入，保留根绑定不可变约束，去掉历史后续 root UPDATE。三个原有实库场景全部通过。
- Memory snapshot 和其他 integration 中保留的 pgx 仅用于种子写入、结果断言和 PostgreSQL 错误事实；不再调用被删除的 legacy 业务 helper。Workflow Cancel fixture 改为同 Pool 的 GORM Runtime 和有效 scoped fence。
- Tools refusal fixture 初次复跑命中 `agent_conversation_time_order`：同一行按顺序求值的多个 `clock_timestamp()` 会使 `last_activity_at < created_at`。三个会话时间统一为 `statement_timestamp()`，约束及生产 SQL 均未改动。
- 未新增测试文件或临时测试用例，未新增 Skip/build tag。Test 函数只有三项与最终契约对应的重命名，其余 Test 函数均保留。

## 验证命令和结果

全部后端执行测试显式限制为 60 秒；未运行全仓测试或全量 integration 矩阵。

| 检查 | 结果 |
| --- | --- |
| `go test -mod=vendor ./internal/agent/... ./internal/tools/... -count=1 -timeout=60s` | PASS；所有 Agent/Tools 包，单包输出 0.342–1.518 秒 |
| `go vet -mod=vendor ./internal/agent/... ./internal/tools/...` | PASS |
| Agent 代表性实库组（命令如下） | PASS，43.637 秒 |
| Tools 代表性实库组（命令如下） | 修复种子时间后 PASS，38.157 秒；四个顶层场景及 Core 内全部子场景通过 |
| Agent 取消/授权竞态（命令如下） | PASS，13.984 秒 |
| Agent Workflow 三条现有 integration（命令如下） | PASS，17.456 秒 |
| 变更范围内 `gofmt -l` | 45 个仍存在的变更 Go 文件均无输出 |
| `git diff --check -- internal/agent internal/tools` | PASS |
| `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-composition-pgx-convergence` | PASS；database-guidelines/error-handling 原有超长注入警告，不是 JSONL 失效 |

```bash
go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^(TestRepositoryModelRunCallReplayCASAndUnknownRecovery|TestRepositoryGetModelRunRecordTxUsesCallerTransaction|TestWorkspaceAnalysisModelOperationSuccessReplayLoadScopeAndCommitRecoveryIntegration|TestRAGProgressStoreAppenderFailureRollsBackIntegration)$' \
  -count=1 -timeout=60s

go test -mod=vendor -tags=integration ./internal/tools/adapter/postgres \
  -run '^(TestRepositoryPolicyRefusedStartCASUnknownTimelineAndSecretBoundary|TestWorkspaceAnalysisToolOperationParticipantAndAuthorityParityIntegration|TestWorkspaceAnalysisToolOperationEventFailureRollsBackAllOwnersIntegration|TestWorkspaceAnalysisToolRefusalAuditIsAtomicReplayableAndBudgetFree)$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^TestWorkspaceAnalysisCancellationRacingModelAuthorizationHasNoOrphanFacts$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/agent/adapter/workflow \
  -run '^(TestExecutorPersistsRealPostgresModelRunAndCallForWorkflowAttempt|TestExecutorLoadsDisputedExistingClaimAndDisclosureThroughProductionAdapters|TestRecordingReviewPersistsPostgresCallMetricsWithoutSensitiveBodies)$' \
  -count=1 -timeout=60s
```

## Go / SQL Review

按已加载的 `go-review`、`sql-code-review` 和 Trellis check 要求做静态审查，并以实库结果验证关键事务不变量。

- Agent/Tools 非测试 Go 文件定向扫描：无 pgx/pgconn import、旧 `transaction any`/`tx any`、旧 Repository/DB/RAGProgressStore 和已删除 Application 端口；所有 `NewGORM*` 签名与 HEAD 相同。
- 删除旧执行路径没有增加新的 SQL、表、Schema、迁移、关联 Hook、默认事务、连接池或跨 owner 查询。保留的 SQL 仍属于已验收的 GORM 实现；参数、Workspace/Attempt 身份和事务参加者在原边界校验。
- 实库确认 Model/Tool CAS、唯一执行/预算绑定、提交响应丢失恢复、事件/Audit 原子性、取消竞争及无孤立事实。事件写入错误继续穿透以回滚整个 UnitOfWork，未吞错或伪造成功。
- Timeline/Event/Audit 投影的敏感字段限制仍有原断言覆盖；未增加日志正文、凭据或完整 DSN。
- 当前 owner 范围未发现未修复的 Go/SQL 缺陷。所有测试消费和时间种子改动均在原文件，不降低原场景断言。

## 消费者、验证边界和回滚

- 已向主会话同步：Conversation 使用 `ScopedWorkspaceAnalysisRunStarter`/`ScopedModelRunStore`；Capture/Organizing 使用窄 `ScopedModelRunFinalizer`；Artifact 使用 `ScopedModelRunStore`。生产 cmd Run starter、Worker capability、Tools 聚合由 Final 主会话负责。
- Agent Workflow integration 原先受其他 owner 的 `knowledge/relation_apply.go` 旧 `eventsapplication.Appender` 编译阻断；依赖清理后，已完成上述三条实库验证，没有遗留该项验证阻塞。
- 本次未修改 Schema，也未 commit、push、部署或归档。若回滚，应按 Agent/Tools 与已切换消费者的依赖闭包恢复 Adapter、Application Port 和 Composition，保持同一 Atlas 版本；不能单独恢复一个旧 Composition 调用而留下已删除的端口。

## 追加迁移 Fixture 收口

Final 追加授权的范围为 `internal/platform/migration/` 中以下六个现有文件：

- `workspace_analysis_run_loader_integration_test.go`
- `workspace_analysis_capability_integration_test.go`
- `workspace_analysis_tool_execution_integration_test.go`
- `workspace_analysis_publication_integration_test.go`
- `workspace_analysis_receipt_repository_integration_test.go`
- `workspace_analysis_authorization_repository_integration_test.go`

所有业务构造改为最终 GORM Adapter。迁移完成后调用主会话提供的 `openMigrationRuntimePool`：先关闭 migration raw Pool，再打开同数据库的 Platform Pool；后续种子和断言使用 `runtime.DB()`，GORM、UnitOfWork 和所有参与者共享这个物理 Pool。`runtime.Close()` 的 defer 注册在原数据库 cleanup 之后，先释放业务连接再清理数据库。

授权 fixture 中增加 `newWorkspaceAnalysisMigrationToolsRepository`，只聚合现有 GORM Core 与 Workspace Analysis Repository，并注入真实 Workflow policy/recovery/execution fence、Agent、Events、Audit；这个聚合只用于现有测试文件。Worker capability 的 readiness 改为 `ScopedWorkspaceAnalysisReadiness` 与平台 UnitOfWork，不再使用旧 pgx 事务接口。

运行期授权 fixture 的迁移目标从 00085 提升到 00089，因为最终 Agent Prepare 会查询 00089 已定义的 refusal 表；历史 schema-only 00085/00086 场景不变，没有修改迁移 SQL 或 Schema。六份文件的 11 个原有 Test 函数名称和数量保持不变，原断言保留，没有新增 Skip/build tag。

Receipt success/failure 两条 response-loss 用例改用 Conversation owner 提供的 `armMigrationCommitResponseLoss`。该 helper 在构造 Repository 前装入官方 stdlib connector，借用同一物理 Pool，`SetMaxIdleConns(0)`；第一次实际成功提交后才注入响应错误。它保持真实 `*sql.Tx` 与 scope，cleanup 恢复 ConnPool、关闭 SQL facade，并断言故障确实触发。旧 pgx DB/Tx wrapper 已删除，原子完成、失败结算、重放和敏感数据断言均继续执行。

## 追加实库验证证据

迁移 00080 包含 cluster 级 `ALTER ROLE zhixu_local_model_runtime`。最初多个独立数据库共用同一 PostgreSQL cluster 并行迁移，出现 `tuple concurrently updated` 和两组 60 秒超时；这些中断结果未计为通过。Publication authority 子场景已在该环境完整通过，其余追加用例改用本 owner 独立的 `pgvector/pgvector:pg16` 临时实例，严格串行执行，结果如下。

| 现有场景 | 实际结果 |
| --- | --- |
| Publication authority 子场景 | PASS，27.113 秒 |
| Receipt success commit-response-loss | PASS，11.368 秒 |
| Receipt failure commit-response-loss | PASS，12.401 秒 |
| Search private binding 的成功、缺失回滚、错配回滚三子场景 | PASS，32.747 秒 |
| 授权 reconciliation/failure settlement 与 replacement attempt 两条 | PASS，28.669 秒 |
| 过期授权无事实拒绝与 Run loader 精确绑定两条 | PASS，22.652 秒；loader 的五个失配子场景全部通过 |
| Worker capability 持久化合同 | PASS，13.450 秒 |
| 工具执行重放与 replacement Inspect 恢复两条 | PASS，28.339 秒 |
| `go vet -mod=vendor -tags=integration ./internal/platform/migration ./internal/agent/adapter/workflow` | PASS |
| Agent/Tools 与六份 migration fixture 的定向 `gofmt -l` / `git diff --check` | PASS；51 个仍存在的变更 Go 文件无格式差异 |

实际测试命令如下。数据库环境通过当前 shell 的临时 0600 文件提供，不将 DSN 或密码写入仓库或输出；每条命令均为独立的小组，未并行执行迁移进程。

```bash
go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^TestWorkspaceAnalysisPublicationProofCompletesOnlyAuthoritativeProjection$/^authority_readers_replay_only_the_bound_workspace_run$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^TestWorkspaceAnalysisReceiptRepositoryAtomicCompletionReplayAndCommitRecovery$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^TestWorkspaceAnalysisReceiptFailureRepositoryAtomicClosureReplayAndCommitRecovery$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^TestWorkspaceAnalysisReceiptRepositorySearchPrivateBindingPersistenceAndRollback$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^(TestWorkspaceAnalysisToolAuthorizationReconcileAndFailureSettlement|TestWorkspaceAnalysisToolAuthorizationReplacementAttemptChargesUnknown)$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^(TestWorkspaceAnalysisToolAuthorizationRejectsPreAuthorizationDeadlineWithoutFacts|TestWorkspaceAnalysisRunLoaderRequiresExactWorkspaceExecutionBinding)$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^TestWorkspaceAnalysisWorkerCapabilityPersistenceContract$' \
  -count=1 -timeout=60s -v

go test -mod=vendor -tags=integration ./internal/platform/migration \
  -run '^(TestWorkspaceAnalysisToolExecutionAuthorizesPersistsAndReplaysWithoutReexecution|TestWorkspaceAnalysisInspectNodeRecoversCanonicalReceiptAcrossReplacementAttempt)$' \
  -count=1 -timeout=60s -v
```

追加 Go/SQL Review 确认单池生命周期、同一 scope 事务参与、response-loss 注入和精确 Workspace/Run/Attempt 绑定均保留。fixture 的迁移目标调整只补足真实运行期依赖；参数化 SQL、历史状态机约束、失败回滚和私有 binding 校验未被放宽。当前范围无未解决缺陷。未运行未改动的历史 schema-only 全矩阵；Publication 仅执行此次修改的 authority 子场景，不宣称该文件全部历史场景均已重跑。

专用容器 `zhixu-todo10-agent-tools-59189c2b` 已停止并自动移除，其本轮生成的环境与 metadata 临时文件已删除；未操作主会话管理的 PostgreSQL 容器。Final PRD/status、任务归档和整体验收由主会话负责。
