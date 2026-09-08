# Conversation GORM 验证记录（2026-09-08）

## 实施与环境

本 owner 完成 Conversation、Turn/Answer、Feedback、Question Dispatch、Execution Context、Draft Stream、Answer Finalizer、Workspace Analysis publication/proof、timeline、audit 和 runtime scoped hooks 的 GORM 迁移。Final 放行后清除 13 个旧 pgx 实现文件；纯校验、codec、状态矩阵和事件映射迁到对应 `*_contract.go`。

全部数据库验证使用既有 `testdb.Require` 工厂、OrbStack Docker、`pgvector/pgvector:pg16` 和真实 Atlas/River migration。未设置外部测试 DSN，未因环境缺失 skip。只原位适配既有测试和 fixture，没有新增测试文件、删断言或新增临时测试代码。测试用 pgx 仅保留 seed/SQL 断言等 fixture 能力，不进入生产代码。

## 核心门禁（Final 清理前）

以下命令前缀相同：

```text
go test -timeout=60s -tags=integration ./internal/conversation/adapter/postgres -run '<下列精确正则>' -count=1 -v
```

| 精确正则 | 结果与覆盖 |
| --- | --- |
| `^TestRepositoryCreatesConversationWithEventAndExactReplay$` | PASS，8.700s；创建、事件与精确重放 |
| `^TestQuestionDispatcherAtomicallyCreatesAndExactlyReplaysQuestionWorkflowAndEvents$` | PASS，9.358s；Question/Answer/Workflow/River/Events 原子创建和重放 |
| `^(TestAnswerFinalizerAtomicallyPublishesRefusalAndExactlyReplaysConcurrently\|TestAnswerFinalizerRollsBackDraftModelRunAndAnswerWhenTerminalEventFails\|TestQuestionDispatcherRollsBackEveryFactAfterRuntimeOrEventFailure\|TestWorkspaceAnalysisFinalizerCancellationCommitsBundleAndRecoversExactResponseLoss)$` | PASS，44.189s；并发发布、Draft/ModelRun/Answer/Event 回滚、Runtime/River 写后失败回滚、审计失败回滚、取消 proof bundle 与 commit response-loss 精确查证 |
| `^(TestDraftStreamRepositoryFencesLeaseAndKeepsOrderedChunks\|TestWorkspaceAnalysisCancellationTerminalHookDirectRuntimeCancelClosesPublicationAndReplays\|TestWorkspaceAnalysisFinalizerClarificationAcceptsPlannerResultWithoutSubjectCandidateAndReplays)$` | PASS，27.956s；Draft 主路径、直接取消、planner clarification 发布与重放 |

## Final 清理后验证

```bash
go test -timeout=60s ./internal/conversation/...
go vet ./internal/conversation/...
go vet -tags=integration ./internal/conversation/adapter/postgres
git diff --check -- internal/conversation .trellis/tasks/08-19-gorm-conversation-migration
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-conversation-migration
```

上述检查均 PASS。单元测试包含 typed-nil 构造拒绝、非法输入在访问数据库前拒绝、事件和审计安全绑定，以及 Workspace Analysis 纯 builder/proof/authority 校验。task validate 对 database/error spec 有注入截断提示；所需契约此前已分段读取，并非缺失文件或校验失败。

```bash
go test -timeout=60s -tags=integration ./internal/conversation/adapter/postgres -run '^(TestRepositoryListsAndGetsConversationsWithStableWorkspaceCursor|TestRepositoryReadsTurnsAnswersAndPublishedContextWithoutCrossWorkspaceLeak|TestRepositoryRecordsFeedbackWithExactReplayAndImmutableProductFacts)$' -count=1 -v
```

PASS，43.433s。覆盖 Workspace 隔离、稳定 keyset、Turn/Answer/历史有界读取、查询数量与 Feedback 幂等和事实不可变。

```bash
go test -timeout=60s -tags=integration ./internal/conversation/adapter/postgres -run '^(TestDraftStreamRepositoryFencesLeaseAndKeepsOrderedChunks|TestAnswerFinalizerDraftLeaseUsesWallClock|TestWorkspaceAnalysisCancellationTerminalHookDirectRuntimeCancelClosesPublicationAndReplays)$' -count=1 -v
```

PASS，38.510s。Draft 显式 claim 锁与 lease 检查、锁读取暂停 fixture 和 Finalizer wall-clock fixture 均已经过 GORM UoW/scoped 入口；同时验证 Runtime 取消时 proof、Answer、通知的原子终结和精确重放。未使用旧 pgx Adapter。

清理期间的首次编译暴露了 Execution Context 的 counting fixture 仍传 raw pool、Finalizer 两个 lease helper 仍引用旧名称。已原位迁到 shared Pool/GORM UoW；后续实库命令均实际执行并通过。

## Go/SQL 自检

使用 `go-review` 与 `sql-code-review` 对本模块变更进行人工静态自检，复用同一组 SQL、锁、取消和实库证据；没有独立 reviewer 子代理。

- 同一物理 Pool 提供 GORM root 与 UnitOfWork，所有跨 owner 副作用通过 `foundation.TransactionScope`。未读取 `ConnPool`、自行断言 `*sql.Tx` 或把 scope 转回 pgx。Runtime hook 不拥有提交权。
- Schema 和 nullable/时间模型与 Atlas 对齐。`00085_workspace_analysis_persistence.sql` 的两种 proof `published_document` 均为 bytea，显式保存精确字节；Answer JSONB 用 Valuer 绑定为单一参数，保留 hash 与 byte-count 约束。
- Conversation→Answer→ModelRun→Draft，以及 Workspace Analysis Workflow/Attempt→Analysis/Authority→Publication 的既有锁序、CAS、数据库 wall-clock、TTL、SKIP LOCKED 和 deferred constraints 保留。
- 精确重放不新建已清理事件；新副作用失败回滚整个 scope。新发布 commit 不确定保留错误分类与 exact lookup；Workspace Analysis 用独立短上下文查证相同 proof ID，未伪造成功。
- 复杂 SQL 使用静态 `?` 参数；没有 `$n` 运行时转译、`sql.Named` 拼接或外部输入标识符。无 AutoMigrate/Migrator、隐式关联写入、N+1、无界分页或日志正文输出。
- production `internal/conversation/**` 搜索无 pgx/pgconn、`AppendTx`/`RecordTx`/`StartTx`/`transaction any`；Domain/Application 无 GORM/driver 类型。

本次自检没有剩余的已知阻断缺陷。

## 验证边界

- 未运行全量 integration/race/端到端矩阵，遵循父任务精简政策。
- 本 owner 初次盘点未包含 `cmd/worker` 与 migration 包的 Workspace Analysis 场景；初次实库只覆盖 cancellation 与 planner clarification。Final 主会话复核确认：Worker 的 `TestPublicConversationRunsThroughRiverWorkspaceAnalysis` 从 `NewGORMWorkspaceAnalysisFinalizer` 经 metrics wrapper 调用真实 `FinalizeSuccess`，11.341s 通过，断言六节点、四 Receipt、一个 Candidate、三次 Model Call、Run/Workflow succeeded、正文发布及重放不重复 Provider。
- Final 追加的 `TestWorkspaceAnalysisTimelineProjectsAuthoritativeSafeSnapshot` 已通过，命令耗时 14.072s。以上补齐成功发布与 Timeline 主路径覆盖；完整 Finalizer 故障矩阵没有执行。
- API/Worker/命令入口接线与全仓 pgx allowlist gate 由主会话 Final 集成负责。
