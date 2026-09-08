# Workflow、Events、Audit Final 清理与验收

日期：2026-09-08。主会话确认七模块门禁通过后，授权删除三个 owner 的旧 pgx 路径。此阶段只修改
`internal/workflow/**`、`internal/events/**`、`internal/audit/**` 及本记录；未修改 cmd、Schema 或发布状态。
前一阶段模块门禁见 `../../08-19-gorm-workflow-migration/final-handoff.md`。

## 最终实现与公共边界

- Workflow 删除 `adapter/postgres/repository.go`、`runtime_start.go`、`runtime_state.go`、
  `runtime_output.go`；保留已迁出的纯 scanner、显式列、nullable 转换和领域校验于
  `runtime_rows.go` / `runtime_validation.go`。所有最终 Repository 构造均为已有 `NewGORM*`。
- River 删除 `adapter/river/inserter.go`、`Client.insert`、`Client.fence`、`Options.EnqueueFence`。
  `InsertOptions`、`JobReceipt`、唯一状态、UTC 调度和 traceparent 编码在 `insert_options.go` 保留。
  `NewScopedJobInserter` / `NewScopedTypedJobInserter` 使用相同 platform Pool 的 database/sql driver；
  Node wrapper 保留先校验参数再检查 nil receiver 的既有行为。
- Application 删除旧 `CancellationSafetyGuard`、`WorkflowTerminalHook`、`WorkflowControlHook` 及
  两个旧 composite。保留 `WorkflowTerminalOutcome`、`WorkflowNodeTerminalEvent`、
  `WorkflowControlEvent` 和全部 scoped Port/composite；把 owner no-op、事务所有权及拒绝取消约束
  保留在最终 Port 注释中。
- Events 删除旧 PostgreSQL Store、DB 和 `AppendTx(any)`，保留被 GORM 消费的查询列、codec、
  binding 和错误 helper。Application 的只读 `Store` 保留，事务追加只接受 `ScopedAppender`。
- Audit 删除旧 PostgreSQL Store/Repository alias、`AppendTx(any)`、Application 旧 Store/Appender
  和 `Recorder.RecordTx`。保留 `Record`、`RecordScoped`、`ReadScoped`、scoped Port 及纯 codec、
  redaction、cursor 和错误 helper。
- 三 owner 普通 GORM 产品文件均无直接 pgx/pgconn import；SQLSTATE 通过
  `platformpostgres.SQLState` 投影。Workflow 和 Audit 的 no-row 判断保留 sql/gorm sentinel；
  当前 pgx ErrNoRows unwrap 到 sql.ErrNoRows。Audit 的安全错误摘要与 Events 原有 context 分类不变。
- 公共接口退出已同步主会话；其他 owner 与 cmd 接线由主会话协调，未越界修改。

## Static 与 managed admission

`NewStaticScopedEnqueueFence()` 返回私有 immutable policy，只验证 context、取消/deadline cause 和
`platformpostgres.SQLTransaction(scope)` 的活跃性；不访问 Model Settings、不查询数据库或取得锁。
Composition 只在确认的 static 配置下显式选用。Managed 模式仍注入同 Pool bootstrap 的真实
Model Settings GORM fence；nil/typed-nil 或 bootstrap 失败不能转为 static。

保留以下构造形状：

```go
workflowpostgres.NewGORMRuntimeRepositoryWithHooks(pool, riverOptions, fence, hooks)
workflowriver.NewScopedJobInserter(pool, client, fence)
workflowriver.NewScopedTypedJobInserter(pool, client, validate, fence)
workflowriver.NewStaticScopedEnqueueFence()
```

River Worker/listener 仍使用同 pool 的官方 pgx driver，队列、schema 和 lifecycle 保留。
三个允许的底层 pgx 文件为 `adapter/river/client.go`、`migrator.go`、`queue_metrics.go`。

## 既有测试的原位适配

- Workflow Start/State/Execution Fence/Organizing/Repository/List 与两个 River Worker integration
  文件均构造最终 GORM/scoped 实现。SQL seed、锁竞争和原始持久事实断言仍可使用测试 pgx 连接。
- 提交响应丢失 fixture 现在包装真实 UnitOfWork，在真实 commit 成功后注入 response-loss error，
  保留恢复读取及稳定 Run/Node/Job identity 的断言。
- DAG 测试手工播种的 Node 与 River Job 在同一 scope 中提交；终态/控制 Hook 使用实际 scope
  解包的 SQL transaction 检查提交前事实。
- Events 旧 AppendTx 用例改为 caller UoW；并发用例复用已有 scoped concurrency fixture。
  原双 Store parity 改为固定预期序号、水位、Workspace 和直接数据库行的完整内容对比，不做自比。
- Audit 原跨 Store parity 改为 store-owned 与 caller-scoped 互读、互相精确重放；既有 List unit
  fixture 改为 database/sql driver，仍断言真实生成 SQL、参数顺序以及非法 cursor 不触库。
- 普通 ctor unit fixture 使用 platform.Open 的 lazy Pool（不可达本地 DSN、min=0），只做依赖检查，
  不连接数据库。Hook 顺序/短路/typed-nil、默认及自定义 freshness、参数校验优先级等断言保留。
  Runtime ctor 已只有一个 cancellation slot；原多 variadic guard 校验改为显式 scoped composite
  对无效成员的校验，不再能通过函数签名传入多个隐式 guard。

## 执行结果

所有命令均使用 vendor，测试均显式 60 秒超时；没有新增测试文件或临时测试程序。

```text
go test -mod=vendor ./internal/workflow/... ./internal/events/... ./internal/audit/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/workflow/... ./internal/events/... ./internal/audit/...
go vet -mod=vendor -tags=integration,testcontainers ./internal/workflow/... ./internal/events/... ./internal/audit/...
go test -mod=vendor -tags=integration,testcontainers ./internal/events/... ./internal/audit/... -run '^$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration ./internal/workflow/adapter/river -run '^$' -count=1 -timeout=60s
```

以上 PASS。最终普通 unit 各包 0.320–0.880s；两条空匹配仅证明 fixture 编译，不计入实库通过证据。

实际实库命令：

```text
go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestRuntimeNodeWorkerExecutesDeterministicDeliveryEndToEnd$' -count=1 -timeout=60s
# PASS 9.485s；明确 static policy + GORM Runtime + 真实 pgx River Worker

go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestRuntimeRepository(StartReplayConflictRollbackAndLegacyGuard|RecoversCommitResponseLoss|StartScopedUsesCallerTransactionAndReportsReplay)$' -count=1 -timeout=60s
# PASS 43.241s

go test -mod=vendor -tags=integration,testcontainers ./internal/events/adapter/postgres -run '^(TestStoreAppendScopedCreatesExactReplayRejectsConflictAndLeavesCommitToCaller|TestStoreAppendScopedSerializesConcurrentSourceReferenceClaims|TestGORMStorePostgresReadContractAndScopedTransactions)$' -count=1 -timeout=60s
# PASS 40.024s

go test -mod=vendor -tags=integration,testcontainers ./internal/audit/adapter/postgres -run '^(TestAuditStorePostgresAppendOnlyAndIdempotency|TestAuditGORMStorePostgresReadContractAndScopedTransaction)$' -count=1 -timeout=60s
# PASS 26.685s

go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^(TestRepositoryLeaseCompletionAndHumanSubmission|TestRepositoryListRunsWithPostgres|TestOrganizingTerminalHookBindsResultAfterSucceededStateInSameTransaction)$' -count=1 -timeout=60s
# PASS 37.542s

go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^TestRuntimeState(ClaimSelectsManagedBindingAndReplaysOldAttemptAcrossCutover|ClaimAndActivationCommitSerializeCompleteBinding)$' -count=1 -timeout=60s
# PASS 14.427s
```

迁移中发现并修复的 fixture 问题：旧 managed cutover 用例在 arming 后用无 fence 的旧 Start
创建节点。真实 managed admission 正确拒绝该操作（MODEL_SETTINGS_ENQUEUE_PAUSED）。现在在
preparing 阶段先将待 Claim 节点入队，再保留 arming/activating Claim 拒绝、旧 Attempt replay、
其他 owner 拒绝和零新 Attempt 的全部断言。产品 admission 未降级。
初始命令包含 PauseResumeAndCancel、ActivatesJoinSuccessor 和该 cutover 用例；前两项未报告
失败，组级因 cutover fixture 失败而返回非零，不将该命令记为整体 PASS。修正后的 cutover 与
activation 并发提交命令如上已 PASS。

## Go / SQL 自审与剩余边界

按 go-review、sql-code-review 人工审查：共享 helper 函数体/列顺序保持一致；Application 不引入
数据库驱动；scope 由单一 UoW 负责 commit/rollback；River 入队仍同事务；命名 Hook 保留顺序和
首错短路；SQL 参数化、稳定 keyset、NULL workspace 幂等锁、Audit 脱敏与 append-only 约束保持。
已修复迁移后暂态编译遗漏、Node nil-receiver 校验优先级和上述 managed fixture 问题。

`git diff --check -- internal/workflow internal/events internal/audit` PASS。pgx import 搜索仅剩上述
三个 allowlist 文件，旧 Repository/AppendTx/RecordTx/JobInserter 构造调用已清空。

未重新执行所有 Workflow integration/race、独立 River SIGKILL rescue、完整 HTTP SSE integration、
全仓组合和真实发布/回滚演练；前序模块门禁证据不能冒充本阶段的重新执行。最终全仓入口整合由
主会话承担。本阶段没有 Schema 变更、commit/push 或外部发布；回滚需按 owner 依赖闭包同时恢复
Adapter、Port 与 Composition，保持原 Atlas 版本和数据。
