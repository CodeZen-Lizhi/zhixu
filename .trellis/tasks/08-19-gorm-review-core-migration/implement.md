# Review Core GORM 迁移实施清单

## 0. Planning Gate

- [x] 读取 AGENTS、Trellis workflow、父 PRD/Design/Implement、Foundation 事务契约和 backend database/error/logging/quality 规范。
- [x] 盘点 21 个 `application.Repository` 方法、`EvidenceVerifier`、1 个生产构造点和现有 PostgreSQL integration suite。
- [x] 固定 Workspace-first 锁序、Deck/Card/Session/Schedule CAS、Answer+Schedule+receipt 原子性、due/evidence/invalidation Raw SQL、DB time、错误与 Final 边界。
- [x] PRD 完成 lossless convergence；Design、Implement、baseline 与 context manifests 已配置。
- [x] 独立 Planning Review 发现并修正 GetSession shell 兼容边界、Raw helper context 和 GORM response-loss harness；无剩余 P0/P1。
- [x] 运行 `task.py start` 将 child 设为 `in_progress`；确认后才修改 Go 文件。
- [x] 按不重叠文件范围派发实现；完成后由独立 reviewer 执行 Go/SQL/Trellis 检查，主会话整合并复验。

## 1. Staged Adapter Skeleton

- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，从同一 Pool 取得 GORM root 与 Unit of Work，防御 nil/typed-invalid root。
- [x] 建立唯一 `within` 边界：多语句写事务用 Foundation UoW，callback 内一次 unwrap GORM scope；单 SQL读取走共享 root，禁止 root `.Transaction`、`*sql.Tx` unwrap 或 scope 逃逸。
- [x] 编译期断言实现现有 `application.Repository` 和 `application.EvidenceVerifier`；不修改 Application/Domain Port。
- [x] 新增显式 persistence model、TableName、JSONB/nullable carrier；禁止 `gorm.Model`、自动时间、soft delete、association/hook。
- [x] 新增固定 `?` placeholder SQL、显式接收 caller context 的 Raw Row/Rows/Exec helper、no-row/context/SQLSTATE classifier；所有 identifier 为包内常量。
- [x] 仅抽取可证明数据库无关的 scanner/codec/validation；legacy Repository、生产 wiring、migration 和测试保持不变。

## 2. Deck, Card And Evidence

- [x] 实现 Deck Create/Get/List 与 Card Create/Get/List/Edit/Decision/FindReplay，保持 Workspace lock、receipt-first、聚合 `FOR UPDATE`、Evidence `FOR SHARE`、CAS 与 commit recovery。
- [x] Approve 在同一 UoW 中更新 Card、upsert Schedule、写 receipt；Reject/Invalidate 依赖现有 trigger 删除 Schedule，不手动建立第二事务。
- [x] 实现 EvidenceVerifier，使用单值 array carrier、完整 Workspace/Claim/Source/Span/Hash 绑定与 cardinality 检查；无 N+1。
- [x] 保留 `rankedDueCardsSQL`，实现 ListDue/CheckAnswerEligibility 的一致 eligibility、UTC daily limit、稳定排序与 fail-closed Evidence。

## 3. Session, Answer And Deck Schedule

- [x] 实现 Session Start/Get/Complete：Get 保持任意合法 Session shell 的 Workspace-scoped lookup；Start/Complete 保持 REVIEW+Deck 约束、receipt-first、Deck/Session lock、status CAS 与 response-loss recovery。
- [x] 实现 FindAnswerReplay/SubmitAnswer，保持 Session -> Card -> Evidence -> Schedule lock、Card fingerprint/version、immutable Answer insert 与 Schedule CAS 同一事务。
- [x] 实现 ChangeDeckSchedule，保持 Deck CAS、set-based pause/resume/reset、Schedule version 推进、receipt 与 replay。
- [x] 检查 caller time/UTC 与 legacy fallback，不引入 GORM 自动 timestamp 或新的 DB/Application time 混用。

## 4. Bounded Invalidation

- [x] 迁移五种静态 selector target SQL 和统一 CTE，保持 `ORDER BY id LIMIT batch+1 FOR UPDATE`、前 batch update、count/has_more 与 receipt 原子性。
- [x] 保留 selector AND 语义、200 上限、目标索引、trigger 驱动 Schedule/Health/Timeline/SSE 投影；禁止 JSON 全扫、逐 Card/N+1 或 `SKIP LOCKED`。

## 5. Error, Context, Security And Static Compatibility

- [x] 分类保留 foundation error、caller cancel/deadline cause、sql/gorm no-row、sql.ErrTxDone 与现有 PgError cause/SQLSTATE/constraint 语义。
- [x] 所有公开入口拒绝 nil ctx；所有 Row/Rows 检查 statement error、nil rows、Close 与 Rows.Err；无 partial return。
- [x] 确认错误/GORM logger/验证记录不泄漏 Answer、Score/Feedback、Evidence、receipt JSON、SQL 参数、DSN、Secret 或绝对路径。
- [x] 静态确认 `cmd/api/main.go` 仍只构造 legacy `NewRepository`，未新增 selector、fallback、双写或生产 GORM 路径。
- [x] 静态确认本 child 未修改 Interview/Learning Path、Application/Domain、Foundation、migration、go.mod/go.sum/vendor；工作树中的其他并行 child 改动不归属本 child。

## 6. Focused Static Verification

- [x] `go test -mod=vendor ./internal/review/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/review/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/review/... ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/review/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/review/... ./cmd/api`、`go mod verify` 与只读 `go mod tidy -diff`（tidy 仅报告预存 `go.sum` 漂移，未应用）。
- [x] 扫描 pgx/GORM/database/sql import、`AutoMigrate|Migrator`、root `.Transaction(`、`SQLTransaction`、`*sql.Tx`、动态 SQL、生产 wiring 和日志敏感参数。
- [x] `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-review-core-migration`
- [x] `gofmt -d internal/review/adapter/postgres/gorm_*.go`、scoped `git diff --check` 与新增文件逐个 `git diff --no-index --check`。

## 7. Required Review

- [x] `go-review`：Port 完整性、构造防御、context/error chain、scope 生命周期、Rows、并发、commit/rollback/recovery 与 legacy 兼容。
- [x] `sql-code-review`：参数化、Workspace 隔离、JSONB/array、CAS、锁序、unique/FK/trigger、DB time、bounded batch、索引/N+1 与 rollback。
- [x] `trellis-check`：PRD/Design/Implement 合规、修改范围、验证证据、dirty worktree 保护与 TODO 9/Final 状态门禁。
- [x] 修复范围内明确缺陷并重跑门禁；将证据与剩余风险写入 `research/static-validation.md`。

## 8. TODO 9 PostgreSQL Gate

- [ ] 2026-08-27 筛选未通过：现有 integration fixture 之外仍缺五类 selector 的 `InvalidateCards`、200/has_more、due/invalidation `EXPLAIN`、取消和连接释放门禁；详见 `research/todo9-screening-2026-08-27.md`。保持 `in_progress`，不以 TODO 9 工厂完成替代本 child 实库回归。
- [ ] 仅在现有 `repository_integration_test.go` 原位建立 legacy/GORM implementation factory；每个实现使用独立 migrated disposable database，不新增测试文件。
- [ ] 使用一个普通 `platformpostgres.Pool` 同时提供 `DB()` / `GORM()` / `UnitOfWork()`，证明无第二 pool、scope root 错配或关闭泄漏。
- [ ] 成对运行 Answer/Schedule/receipt 原子性、same/different-key 并发、CAS rollback、REVIEW/INTERVIEW shell、ABA、Deck schedule race；GORM commit response-loss 用同包 UoW wrapper 在真实 `Within` 成功后返回受控错误，再验证 root exact lookup。
- [ ] 成对运行 Approval/quarantine/high-conflict 串行、due UTC/daily limit、malformed Evidence、refuting/superseded Claim 与五类有界失效。
- [ ] 验证 trigger 投影、cancel/deadline/sql.ErrTxDone、真实 SQLSTATE、JSONB/array binding、Rows/连接释放。
- [ ] 在用户允许的 PG/Testcontainers 环境中运行 `go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 60s ./internal/review/adapter/postgres`。
- [ ] 记录 due/invalidation 关键索引、EXPLAIN、bounded statement count 和无 N+1 证据。

## 9. Completion And Rollback

- [x] TODO 9 不可用时保持 PRD AC 未勾选、`task.json.status=in_progress`、legacy production wiring 不变，不归档或宣称完成。
- [ ] TODO 9 全部通过后记录 Review 三 child 跨模块门禁与 Final 构造/legacy 删除清单；本 child 仍不修改 `cmd/**`。
- [ ] 回滚只删除本 child staged GORM 文件并还原本包内必要共享 helper/test factory；不回滚 Schema、历史数据、Foundation 或其他 child。
