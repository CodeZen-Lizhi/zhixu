# Tools GORM Migration Implementation Plan

## 0. Planning Gate

- [x] 读取父 GORM、Foundation、Workflow、Agent、Events、Audit 与 Tools 基线。
- [x] 盘点 Tools Application Port、legacy Repository 方法、锁序、事务、构造点和测试资产。
- [x] 盘点 Tool Call、Result Receipt、Workspace Analysis budget/operation/refusal 的 migration 与 trigger。
- [x] 确认同 Pool、opaque scope、生产不接线、无新 migration/测试文件的 child 边界。
- [x] 写入 Go/API、SQL/事务、Composition/tests 三份 research。
- [x] 独立 Go/架构与 SQL/事务 planning review 完成，P0/P1/P2 已收敛。
- [x] 用户明确批准最终 Tools 规划摘要（用户已确认并继续执行）。
- [x] Workflow owner 交付 raw scoped Tool policy snapshot 与 SKIP LOCKED recovery Port。
- [x] Agent owner 交付 scoped Tool participant、durable closure verifier、refusal store/exact load 与
      authority reader Port。
- [x] 执行 `task.py start .trellis/tasks/08-19-gorm-tools-migration`，任务进入 `in_progress`。

以上前置门禁现已完成；Tools 代码仍不越权实现 Workflow/Agent concrete Adapter，后续只在本 child 范围内维护 staged sibling。

## 1. Core Boundary And Mapping

- [x] 新增 single-Pool ordinary `GORMRepository` 与 `GORMWorkspaceAnalysisRepository`，共享私有 core、ready、
      within/read transaction 与 callback/commit stage，不互持 Repository 或嵌套 UoW。
- [x] 新增 Raw Row/Rows/Exec、strict scanner、JSONB/bytea/null/time/version carrier。
- [x] 复用 legacy pure validation/equality/receipt codec，避免 pgx/GORM 双事实源。
- [x] 统一 context Cause、cancel/deadline、no-row、`sql.ErrTxDone`、PgError 与 commit unknown 分类。
- [x] ordinary constructor 强制 Workflow policy snapshot/recovery Port；WA constructor 强制 Workflow execution fence、
      Agent participant/refusal/authority、Event 与 Audit，全部拒绝 nil/typed-nil。
- [x] staged GORM 文件无 pgx Tx/Row/Rows/pool/protocol、gorm.Open、root Transaction、DDL、
      AutoMigrate/Migrator、Save/Preload/Association；`pgconn` 仅用于 SQLSTATE。
- [x] Tools GORM 文件不含 `workflow.run`/`workflow.node_*`/`agent.workspace_analysis_*` SQL；
      `workflow.tool_call` 与 result receipt/failure 是 Tools-owned allowlist。

## 2. Policy, Calls, Recovery And Trusted Write

- [x] 通过 Workflow raw scoped policy snapshot 迁移 Policy/RecordRefused/StartCall/Trusted Write 的冻结快照
      admission；commit recovery 只读 Tool-owned durable fact，不重跑当前 lease/cancel/deadline。
- [x] 迁移 RecordRefused、StartCall、FinalizeCall、MarkUnknown 与 exact replay/CAS。
- [x] 通过 Workflow scoped recovery fence 迁移 RecoverStaleStarted，保持 DB clock、Node -> Attempt
      SKIP LOCKED、有界 batch、found/skipped/stale、Call 后锁和 Trusted Write 排除。
- [x] 迁移 ListTimeline，保持稳定排序、上限、Workspace scope、完整 scanner 与无 N+1。
- [x] 迁移 Start/LoadTrustedWriteCall，保持 writeback binding、side-effect idempotency 与 recovery 隔离。
- [x] 所有 nullable/JSON/时间/版本字段写入与读取等价，无隐式 GORM 时间或零值漏写。

## 3. Workspace Analysis Authorization And Closure

- [x] 迁移 AuthorizeWorkspaceAnalysisToolCall：先 Workflow execution fence，再 Agent participant 锁
      Analysis -> Operation -> Reservation，最后 Tools 锁/写 Call；保持 DB time、owner/lease/deadline/cancel。
- [x] Agent participant 与 Tools Call 协作保持 exact create/replay、same-attempt reconcile、replacement
      UNKNOWN/UNKNOWN_CHARGED 和 budget CAS 等价。
- [x] 迁移 FinalizeWorkspaceAnalysisToolCall，保持 Tool Call 与 Agent participant 的 Reservation/Run/Operation
      FAILED/UNKNOWN 同 scope闭合。
- [x] 迁移 FinalizeCallWithReceipt，保持 canonical receipt、Agent budget/operation success 与 Event 同 scope。
- [x] 迁移 FinalizeCallWithReceiptFailure，保持 observation-only failure fact、rollback 和 replay closure。
- [x] 迁移 LoadResultReceipt，严格校验 output/private binding/hash/bytes/candidate 与 owner binding。
- [x] 所有 staged write 只由 Tools outer UoW Begin/Commit/Rollback，scoped collaborator 不拥有事务。
- [x] WA commit recovery 使用 Workflow immutable snapshot + Agent durable closure/refusal exact load，禁止复用
      live admission；租约/cancel/deadline 在提交后变化不影响已证明 replay。

## 4. Refusal, Event, Audit And Authority

- [x] 通过 Agent scoped refusal store 迁移 RecordWorkspaceAnalysisToolRefusal，保证只写 refusal + scoped Audit，
      无 Server Event/Call/预算事实。
- [x] replay 核对 logical slot、error code、Workflow binding、Audit binding；commit recovery 通过 Audit owner
      `ReadScoped` 证明 refusal+Audit 完整闭包，append failure 全回滚。
- [x] Event/Audit DTO 与日志只包含稳定脱敏字段，不含 prompt/request/output/private binding/credential。
- [x] 通过 Agent scoped authority projection 与 Tools-owned receipt 组合六个 authority reader，保持完整
      owner/receipt/candidate/publication/citation closure。
- [x] 多语句 authority read 使用 legacy 等价的默认 transaction options；Rows Close/Err、stable order、
      固定最多 3 条 receipt 循环与无界 N+1 检查通过。

## 5. Static Verification And Review

- [x] `gofmt` 仅作用于本任务新增/修改 Go 文件。
- [x] `go test -mod=vendor ./internal/tools/... -count=1 -timeout 60s`。
- [x] `go test -race -mod=vendor ./internal/tools/... -count=1 -timeout 60s`。
- [x] `go vet -mod=vendor ./internal/tools/... ./internal/platform/postgres`。
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/tools/... ./internal/platform/migration -count=1 -timeout 60s`。
- [x] `go test -mod=vendor ./cmd/api ./cmd/worker -run '^$' -count=1 -timeout 60s`。
- [x] `go list -mod=vendor ./internal/tools/...`、`go mod verify`、`go mod tidy -diff` 只检查（tidy diff 发现工作区既有 go.sum 漂移，未修改）。
- [x] 静态扫描无 production GORM wiring、第二 pool、DDL、动态输入拼 SQL、跨 owner SQL、
      JSONB/bytea carrier 混淆或无界 N+1。
- [x] Go Review、SQL Review、Trellis Check 修复 P0/P1/P2 后重跑门禁。
- [x] `git diff --check` 与 `task.py validate`。
- [x] 写入 `research/static-validation.md`，记录实现范围、生产仍 legacy、review 与 TODO 9 盲区。

## 6. TODO 9 Real PostgreSQL Gate (External Blocker)

本 Phase 由后续独立共享实库验证任务执行；Tools child 不修改 integration fixture，只维护场景契约并等待
证据回填。每个 legacy/GORM variant 使用独立迁移数据库。

- [ ] 一个 platform Pool 提供 GORM/UoW、Tools、Workflow、Agent、Events、Audit 与所有 scoped dependencies。
- [ ] 普通 Call、Policy、Trusted Write、Timeline 与 SKIP LOCKED recovery 行为等价。
- [ ] WA 授权 budget/concurrency、same-attempt reconcile、replacement UNKNOWN_CHARGED 和固定锁序无死锁。
- [ ] success/failure Receipt、FAILED/UNKNOWN Event 与 refusal Audit 各 stage fault 均无部分提交。
- [ ] DB clock/lease/deadline、trigger/deferred closure、bytea/JSONB/null/time 执行等价。
- [ ] 真实 23505/23503/23514/55000/40001/40P01/55P03、no-row、custom Cause、`sql.ErrTxDone`。
- [ ] UoW commit response-loss 后同一次调用用新 transaction exact recovery；无法证明时返回 commit/unknown；
      Rows/connection release 与 Pool acquired count 归零。
- [ ] 敏感 canary 不出现在 Event/Audit/error/log/receipt failure；Tools Adapter GORM integration 通过。

当前无 TODO 9/DSN 时本 Phase 全部保持未完成，不得把 compile-only 或 skip 当验收，也不得在本 child
越权修改 `internal/platform/migration` 或 `cmd/**` integration fixture。

## 7. Final Handoff

- [ ] 记录 API/Worker basic/full Repository constructor 替换点、同 Pool 依赖链和关闭顺序。
- [ ] 在 Final composition gate 参数化 Worker Tool execution/Workspace Analysis integration；Tools child 不改
      `cmd/**` 或伪造不可装配的 GORM Worker variant。
- [ ] 记录 legacy Repository、pgx transaction seam 与非 allowlist import 删除清单。
- [ ] 记录 Final 静态规则：Tools 普通 data access 不允许 pgx；平台/worker 既有 River runtime 另按父任务 allowlist。
- [ ] PRD AC 只在 TODO 9/fault/production composition gates 通过后勾选；此前不完成、不归档。

## Rollback Point

TODO 9 前只 revert Tools staged GORM/Application additive scoped文件和本任务工件。不得回滚 migration、历史
Tool/Receipt/预算/Event/Audit 事实、其他 owner scoped实现或 legacy 生产路径。
