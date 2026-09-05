# Change Control GORM Migration Checklist

## 0. Planning Gate

- [x] 读取 Trellis workflow、backend database/error/logging/quality spec、父任务、Foundation/Workflow/Events staged contract、目标代码/Schema/测试。
- [x] 盘点 Change Control 主 Repository、Approval Dispatch、生产构造、跨 owner transaction Port 与 integration suite。
- [x] 固定 Proposal/Revision/Approval/Authorization/Writeback/Dispatch 的锁序、DB time、CAS、JSON、错误、response-loss 与敏感边界。
- [x] 补齐 PRD、Design、Implement、baseline、implementation contract 与 manifests。
- [x] 独立 planning review 无剩余 P0/P1，并运行 task validate。
- [x] `task.py start` 后确认 task status=`in_progress`，再修改 Go 文件。

## 1. Staged Foundation

- [x] 主包新增 `GORMRepository`，从单一 Pool取得 GORM/UoW，提供 ready/within/Raw Row/Rows/Exec/no-row/context/SQLSTATE helper。
- [x] 增加显式 persistence records、JSONB string carrier、nullable/array carrier；禁止 Schema ownership 与 implicit GORM behavior。
- [x] 编译期断言现有 Proposal/typed proposal/list/Revision/history/Authorization/Writeback/cancellation scoped Port；不改 Domain/Application 旧接口。
- [x] legacy Repository、DB、constructor、SQL 和 production wiring 保持可编译。

## 2. Proposal, Approval And Authorization

- [x] 实现五类 Proposal create/replay、Find/Get/List、BuildDownstreamUpdate；冲突分支用内部 rollback sentinel 退出 UoW 后 root `GetProposal`，并保持 typed JSON/binding、Downstream report `FOR UPDATE` + owner `FOR SHARE`、Restore fence、keyset limit+1。
- [x] 实现 Approve/MarkNeedsRevision，保持 current Revision lock、Approval append、Proposal CAS 与 optional Event `AppendScoped` 原子性；无 Event 的 exact Approval replay必须 rollback/no commit，有 Event replay才 exact append并commit。
- [x] 实现 ValidateWorkflowContext 和 Authorization issue/get/consume/revoke/expire，保持 DB clock、key/token row lock、完整 binding 与 SQLSTATE/constraint mapping。

## 3. Revision And Fence

- [x] 实现 receipt lookup、base snapshot、history list/get 与严格 scanner。
- [x] 实现 AppendRevision：advisory/receipt -> Authorization -> Proposal/Revision -> Approval/dispatch/Workflow/Execution fences -> revoke -> snapshot/revision/lineage -> pointer CAS -> receipt/Event。
- [x] 保持 response-loss root receipt lookup、deferred closure、00090 trigger contract 与 authorization set re-read。

## 4. Safe Writeback

- [x] 实现 scoped cancellation safety，拒绝 nil、非 platform 类型与 stale scope并保持 all-executions fail-closed；active cross-Pool scope 当前不可识别，按同一 Pool Composition/TODO 9 硬门禁处理。
- [x] 实现 BeginWriteback 双授权 deterministic lock/DB-time/lease/replay/consume/Execution/Proposal CAS 单 UoW。
- [x] 实现 ValidateLease、FinalizeCleanup、legacy Create、Get/Find、Checkpoint 与 Publish；保持 Proposal/Execution 锁序、Commit/Outbox exact replay 与 state CAS。
- [x] 确认 Credential 只在调用栈哈希，JSON/outbox/log/error不含 secret/content/绝对路径/lock token。

## 5. Approval Dispatch

- [x] 新增 `GORMApprovalDispatchRepository`，构造依赖单 Pool、`ScopedRuntimeStarter`、IDs、Clock 和 optional `ScopedAppender`。
- [x] Approved 分支在单 scope 内维护 Approval、Workflow Start/River、revision dispatch、Proposal CAS；完整 replay不读取 safety input。
- [x] Rejected 分支保持 zero Workflow/River、Approval/Proposal/Event 原子性与 exact replay。
- [x] 保持 Dispatch 的 legacy commit-response-loss：首次报 commit failure、后续请求按完整 durable binding exact replay；不把部分事实猜成成功。

## 6. Static Compatibility And Security

- [x] 所有 Raw SQL 参数化，Rows Close/Err/no-row按调用点映射；无 AutoMigrate/Migrator/Save/Preload/Association/root Transaction/独立 gorm.Open。
- [x] context classifier 保留 canceled/deadline/custom cause、sql.ErrTxDone与原始 PgError chain；稳定 code/retryability与legacy等价。
- [x] 静态确认 API/Worker仍只构造 legacy pgx，无 selector/双写/fallback；其他 owner、migration、go.mod/go.sum/vendor不因本 child改变。

## 7. Focused Static Verification

- [x] `go test -mod=vendor ./internal/changecontrol/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/changecontrol/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/changecontrol/... ./internal/platform/postgres ./internal/events/... ./internal/workflow/...`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/changecontrol/adapter/postgres ./internal/changecontrol/adapter/approvaldispatchpostgres ./internal/changecontrol/application -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- [x] `go list -mod=vendor` scoped packages、`go mod verify`、`go mod tidy -diff` read-only。
- [x] forbidden/static scan、`task.py validate`、`gofmt -d`、scoped/global `git diff --check`。

## 8. Required Review

- [x] Go Review：接口完整性、typed-nil/context、scope/UoW生命周期、Rows、replay/commit ambiguity、生产兼容。
- [x] SQL Review：参数化、Workspace隔离、JSON/array、锁序/deadlock、CAS、DB time、trigger/constraint、outbox/River原子性、性能/N+1。
- [x] Trellis Check：spec/task范围、dirty worktree保护、验证证据、TODO 9/Final状态门禁。
- [x] 修复范围内明确问题后重跑门禁，并写 `research/static-validation.md`。

## 9. TODO 9 PostgreSQL Gate

2026-09-01 精简政策取代本节原有的逐项完整矩阵；旧矩阵仍作为 Final
或直接风险触发时的备查清单，不再阻断本 child 归档。

- [x] 只扩展两个 owner 的现有 integration 文件，均使用 `testdb.Require` 创建 disposable PostgreSQL；未新增测试文件。
- [x] GORM Proposal/Approval 主路径覆盖 create、exact replay、幂等冲突回滚和单行 Approval 不变量。
- [x] scoped Knowledge Proposal 在 caller UoW 中覆盖 create/exact replay、canonical current Revision pointer、commit、rollback，以及 nil、foreign type、stale scope 拒绝。
- [x] Approval Dispatch 使用单一 Pool 按 `Audit GORM -> fixed-test-key Sealer -> Model Settings GORM enqueue fence -> Events GORM -> Change Control GORM cancellation guard -> Workflow GORM scoped River runtime -> Dispatch GORM` 完成 approved/replay，并断言唯一 Approval/Run/Node/Outbox/River Job。
- [x] replay 仅提交 Workspace 与持久 Approval binding，不携带 safety observations，证明完整重放不重读 safety input。
- [x] 未使用 no-op fence、第二 Pool、独立 `gorm.Open` 或生产 Composition 切换。

历史完整矩阵包括五类 Proposal 成对覆盖、response-loss、广泛锁竞争、
00015/00090 逐项负测、取消/连接释放、EXPLAIN 和 integration race；仅在
Final 或其机制被直接改动时按风险执行。

## 10. Completion And Rollback

- [x] 2026-09-01 精简 PostgreSQL 门禁通过；legacy production 保持不变，归档只表示 staged child 完成。
- [x] 下游保持 Knowledge/Graph/Retrieval/Artifact scoped contract；Final 统一负责 constructor 切换、legacy 删除和 pgx allowlist 收口。
- [x] 回滚只删除本 child staged adapter/helper与必要测试 fixture；不回滚 Schema/Foundation/Workflow/Events/Audit或用户改动。

## 11. Graph Scoped Proposal Prerequisite

- [x] 在 Change Control owner 内新增 `CreateKnowledgeChangeProposalScoped` 与 `GetInitialKnowledgeChangeProposalScoped`，只解包 live `foundation.TransactionScope`，不自行 UoW/commit/rollback/root fallback。
- [x] 复用现有 Knowledge Proposal canonical validation、JSONB/scanner、SQLSTATE 与 stable error；新建设置 `current_revision_id=revision.id`，exact read 固定 Revision 1 并兼容历史 NULL pointer。
- [x] 静态证明不导入 Graph、Graph 不直接拥有 `change_control.*` GORM SQL、production wiring 不变；运行 Change Control test/race/vet/integration compile 与独立 Go/SQL/Trellis review。
- [x] 真 PostgreSQL 精简门禁原位验证同 scope commit/rollback 与 nil/foreign/stale scope；并发 replay、历史 pointer、取消/SQLSTATE/连接释放按直接风险或 Final 触发。

## 2026-09-01 精简测试门禁

按父任务精简政策，本 child 的最低实库证据为一个既有 Testcontainers 主路径，加一条 Change Control 最关键的同 scope 提交/回滚或幂等冲突场景。此前列出的完整 response-loss、取消/连接释放、EXPLAIN 和全量 integration race 仅在本 child 直接改动对应机制时执行；旧清单作为风险备查，不再逐项阻断归档。Workflow/Model Settings 前置、权限、锁序、状态机和原子性仍不可豁免。
