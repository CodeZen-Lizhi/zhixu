# Tools GORM Static Validation

## 当前结论（2026-09-08）

本 child 的实现、直接依赖和父任务精简政策最低门禁已完成，可交 Final 接线。真实 PostgreSQL legacy/GORM
对照通过现有 `internal/platform/testdb` fixture 执行，无需外部 DSN。没有新增或修改测试文件；进入本轮前
`repository_integration_test.go`、`workspace_analysis_refusals_integration_test.go` 的工作区改动完整保留。

本轮代码仅将 `gorm_errors.go` 的普通与 receipt 错误分类改用 `platformpostgres.SQLState`，保留既有错误码、
retryability、cause 与 `23505` receipt conflict 区分；所有 Tools GORM 文件无 pgx/pgconn import。
本 child 未修改 `cmd/**`、其他 owner、migration、task 状态或 active pointer，也未删除 legacy。

## 2026-09-08 验证命令与结果

```bash
go test -mod=vendor -tags=integration ./internal/tools/adapter/postgres -run '^(TestRepositoryPolicyRefusedStartCASUnknownTimelineAndSecretBoundary|TestWorkspaceAnalysisToolOperationParticipantAndAuthorityParityIntegration|TestWorkspaceAnalysisToolOperationEventFailureRollsBackAllOwnersIntegration)$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration ./internal/tools/adapter/postgres -run '^TestWorkspaceAnalysisToolRefusalAuditIsAtomicReplayableAndBudgetFree$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration ./internal/tools/adapter/postgres -run '^TestRepositoryPolicyRefusedStartCASUnknownTimelineAndSecretBoundary$/gorm$' -count=1 -timeout=60s
go test -mod=vendor ./internal/agent/... ./internal/tools/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/agent/... ./internal/tools/...
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-tools-migration
git diff --check -- internal/agent internal/tools .trellis/tasks/08-19-gorm-agent-migration .trellis/tasks/08-19-gorm-tools-migration
```

- 第一组 integration PASS，45.019s：普通 Call/Policy/Timeline/敏感数据主路径、Workspace Analysis participant/receipt/authority、Event 失败全 owner 回滚，全部 legacy/GORM 对照。
- 第二组 integration PASS，19.278s：refusal/Audit 同事务、exact replay、不写 Server Event 和无预算副作用，legacy/GORM 对照。
- 第三组在错误依赖修改后 PASS，26.894s：GORM 主路径复验。
- 修改后的全部受影响 unit 与 vet PASS；gofmt 无差异，定向 diff check 与 Trellis validate PASS。validate 仅报告既有大型 spec 超出注入长度的 warning，无失效上下文路径。

## 2026-09-08 Review 与当前边界

- Go Review：逐一核对 SQLSTATE、receipt conflict、context、`sql.ErrTxDone`、cause 和公开接口。静态方法对照确认两个 GORM Repository 合计覆盖 legacy 21 个公开方法。
- SQL Review：本轮没有修改 SQL、锁序、Workspace predicate、CAS、DB clock 或 Schema；复核跨 owner 事务与敏感数据边界，GORM 文件没有复制 Workflow/Agent SQL。关键 Event 回滚与 refusal/Audit 闭包已有实库证据。
- Trellis Check：已按精简政策更新 AC 和交接，不将历史全矩阵视为必跑或已通过。本轮未发现未解决 P0/P1/P2。
- Final 必须让传给 `ExecutionService.calls` 的同一个对象满足普通 Call、Receipt、Receipt Failure、Workspace Analysis Operation 与 Refusal 接口。仅传普通 `GORMRepository` 会缺失动态断言的能力；精确组合见 `../final-handoff.md`，主会话负责落地。
- 未执行完整 fault/response-loss/SQLSTATE/锁竞争/连接释放/全量 race/EXPLAIN/多进程公平性矩阵。生产 Worker 完整可达性由 Final 验证。
- active scope 的 Pool identity 由 Composition 保证；所有 collaborator 必须从同一个 Pool 构造。

以下保留首次 staged 实现的历史记录；当时的外部 DSN/实库阻塞结论已被以上证据取代。

## 历史 Scope

本记录对应 `08-19-gorm-tools-migration` 的 staged 实现。生产 API/Worker 仍使用 legacy pgx
Repository；本 child 没有修改 `cmd/**`、migration、现有 integration test 或生产 selector。
Workflow/Agent 的 scoped prerequisite 由各自 owner task 提供，Tools 只通过 Application Port 使用。

## Implemented Surface

- ordinary `GORMRepository`：Policy、refusal、Tool Call start/finalize/unknown、Timeline、Trusted Write、stale recovery。
- `GORMWorkspaceAnalysisRepository`：authorization、terminal settlement、success/failure receipt、refusal、
  Event/Audit、authority closure。
- 所有 GORM 数据访问来自一个 `platformpostgres.Pool` 的 GORM root/UoW；跨 owner facts 只通过 opaque
  `foundation.TransactionScope` 与 scoped Application Port 参与。
- JSONB 使用 string carrier，receipt output/private binding 使用 bytea carrier；Rows 完整检查 Scan、Err 和 Close。
- callback failure 与 commit response-loss 分离；commit loss 只用新 UoW 验证 durable closure，不能重新执行 live
  admission。
- stale recovery 每次最多检查 `MaxToolStaleRecoveryLimit` 个候选；同一 Repository 的调用由 mutex 串行，
  keyset cursor 在成功 commit 后轮转，失败不推进。跨进程/多实例的 durable 公平游标留给 TODO9/Final 调度层。

## Review Findings And Fixes

本轮 Go/SQL review 发现并修复：

1. WA 四条提交响应丢失路径缺少 durable recovery；增加 Agent durable closure、Tool-owned receipt/failure 与
   completed Event 的精确验证。
2. WA live fence 未完整校验 Run/Node/Attempt 状态、pause/cancel、DB-time lease；补齐同 scope DB clock 和
   fail-closed admission。
3. `RECEIPT_INVALID` 终态 replay 未返回 immutable receipt failure；补齐 failure proof 加载与 binding 校验。
4. 所有新 GORM helper 统一使用 callback-bound GORM transaction；Rows close 错误与 context cause 保留。
5. stale recovery 从无界 keyset 扫描收敛为每次固定 candidate budget，并增加同实例 runtime cursor，避免长期活跃
   头部候选让后续 stale 调用永久饥饿。
6. ordinary Policy 快照曾遗漏 `pause_requested`/`cancel_requested` admission；现与 WA fence 一致 fail closed，
   Start/RecordRefused/Trusted Write 不会在控制请求期间继续建立新执行事实。
7. refusal commit-response-loss 恢复曾只证明 Agent refusal；现通过 Audit owner 的 `Recorder.ReadScoped` 在同一
   opaque transaction 中读取并比对完整 Audit binding，缺任一事实均不返回成功。
8. authorization/finalization durable recovery 失败时同时保留 commit error 与 recovery error，避免丢失实际
   closure 失败原因。

审查结论：最新 Go review 与 SQL review 未发现剩余 P0/P1/P2。已知验证盲区是跨进程游标公平性和真实 PostgreSQL
锁竞争/约束执行，均不能由 compile-only 证据替代。

## Static Gates

已执行并通过（`go mod tidy -diff` 除外：它只报告工作区既有的 go.sum 漂移，未修改依赖文件）：

```text
go test -mod=vendor ./internal/tools/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/tools/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/tools/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/tools/... ./internal/platform/migration -count=1 -timeout 60s
go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s
go list -mod=vendor ./internal/tools/...
go mod verify
gofmt -d (affected Go files)
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-tools-migration
```

静态扫描确认 staged Tools GORM 文件没有 `gorm.Open`、root transaction、DDL/AutoMigrate、pgx Tx/Row/Rows/pool、
`workflow.run`/`workflow.node_*`/`agent.workspace_analysis_*` owner SQL、生产 GORM constructor 引用或动态用户
输入拼接 SQL。`pgconn.PgError` 仅用于 SQLSTATE 分类。

## 历史 TODO 9 Blocker（2026-09-08 已解除）

首次 staged 验证时 `ZHIXU_TEST_DATABASE_URL` 未设置，尚未执行真实 PostgreSQL parity。当时 TODO9 要求在迁移后的独立数据库上
用同一 platform Pool 验证：锁序与 SKIP LOCKED、budget/operation/deferred closure、JSONB/bytea/null/time 驱动
绑定、SQLSTATE、commit response-loss、连接释放、敏感数据不泄漏，以及多进程恢复游标公平性。在这些证据回填前，
PRD Acceptance Criteria 保持未勾选，任务保持 `in_progress`，生产不切换。
