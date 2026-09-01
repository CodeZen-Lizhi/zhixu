# Review Learning Path GORM 迁移实施清单

## 0. Planning Gate

- [x] 读取 AGENTS、Trellis workflow、backend specs、父 PRD/Design/Implement、Foundation 验证记录和目标代码/Schema/测试。
- [x] 盘点 11 个 `application.Store` 方法、2 个生产构造点、2 个 pgx 产品文件和现有并发/恢复 integration suite。
- [x] 固定 shared Path、receipt/reservation/hold、锁顺序、CAS、历史保护、DB time、错误和 TODO 9/Final 边界。
- [x] PRD 完成 lossless convergence；Design、Implement、baseline 与两个 context manifests 已配置。
- [x] 用户明确审批本最终规划摘要。
- [x] 审批后的后续消息中运行 `python3 ./.trellis/scripts/task.py start .trellis/tasks/08-19-gorm-review-learningpath-migration`；确认 status=`in_progress` 后才修改 Go 文件。
- [x] 按 Trellis 自动模式派发 `trellis-implement`；实现完成后派发独立 `trellis-check`，主会话负责审查、整合和最终验证。
- [x] implement/check agent 从文件系统完整读取 database/error/logging/quality 规范的相关章节；不得把 JSONL 注入的 32 KiB 截断内容当作完整规范。

## 1. Staged Adapter Skeleton

- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，从同一 Pool 获取 GORM root 和 Unit of Work，校验 nil/typed-invalid 状态。
- [x] 建立唯一 `within` 执行边界：多语句写事务使用默认 write options，公开 Path+Steps 聚合读取使用 `ReadOnly:true`，callback 内只用 `platformpostgres.GORMTransaction(scope)`；只有单 SQL 只读 receipt lookup 走共享 root。
- [x] 聚合读取 helper 接受 caller 当前 scoped DB：公开 Get 由 read-only UoW 包裹，写事务内调用复用当前 write scope，禁止 nested UoW 或 scope 逃逸。
- [x] 禁止直接 `database.Transaction`、`SQLTransaction(scope)`、自行解包 `*sql.Tx`、缓存 scoped DB 或建立第二 transaction helper；`internal/platform` 零改动。
- [x] 编译期断言 `GORMRepository` 实现现有 `application.Store`；不修改 Application/Domain Port。
- [x] 新增四个显式 persistence model/table mapping 和 JSONB/nullable carrier；不使用 `gorm.Model`、association、hook、soft delete 或自动时间。
- [x] 新增固定 `?` placeholder SQL 常量与 GORM Row/Rows helper；禁止用户控制 identifier、`SELECT *`、独立 pool 或 `gorm.Open`。
- [x] 仅在数据库无关且无行为变化时复用/抽取 strict codec、snapshot/result validation；legacy pgx constructor/SQL/production wiring 保持可编译。

## 2. Creation And Recovery Transactions

- [x] 实现 `FindCreateReplay`，严格校验 receipt key/hash/type/expected/path version/response 并区分 miss。
- [x] 实现 `BeginReviewCreate`，保持 Workspace -> receipt -> Answer -> reservation 锁序、set-based evidence projection、PENDING replay、COMPLETED replay/repair、ABANDONED attempt CAS 和 DB time。
- [x] 实现 `PrepareReviewCreate`，保持 key/hash/source digest/attempt/artifact digest fence、PENDING CAS 与 completed replay。
- [x] 实现 `CompleteReviewCreate`，在同一 UoW transaction 内显式 insert Path、batch Steps、receipt、reservation CAS 和 exact hold release；验证任一步失败整体 rollback。
- [x] 实现 `MaintainReservations`，保持 bounded order、`FOR UPDATE SKIP LOCKED`、ABANDONED/ORPHANED、statement timestamp 和 duplicate-hold consistency guard。

## 3. Reads And User Commands

- [x] 实现 `GetByReviewAnswer` / `Get`，在同一 opaque UoW scope 内完成 Path+Steps 两条查询，保持 Workspace/REVIEW origin、显式列、`step_no,id` 顺序、完整聚合 validation 与无 partial return。
- [x] 实现 `FindPathStatusReplay` / `UpdatePathStatus`，保持 advisory lock、exact replay、aggregate lock、expected-version/transition/all-terminal validation、CAS 和 receipt 原子性。
- [x] 实现 `FindStepReplay` / `UpdateStep`，保持 advisory lock、Path/Steps locked read、Step CAS、父 Path CAS/自动完成、receipt 与 rollback。
- [x] 检查所有 Row/Rows no-row、Close/Err、JSONB/array/nullable UUID/time、RowsAffected 与 ordered/bounded collection 路径。

## 4. Error, Context, Security And Static Compatibility

- [x] 实现 context-aware GORM classifier：保留 foundation error、cancel/deadline cause、no-row、tx-done 与现有精确 SQLSTATE/code/retryability。
- [x] 确认公开错误/GORM logger/检查记录不泄漏 SQL、参数、score/evidence、snapshot/receipt、Artifact 正文、DSN、Secret 或绝对路径。
- [x] 静态确认 `cmd/api/main.go`、`cmd/worker/main.go` 仍只调用 legacy `NewRepository`，未新增 selector/双写/fallback。
- [x] 静态确认 Review Core/Interview、Application/Domain、Foundation、migration、go.mod/go.sum/vendor 和其他 child 零改动。

## 5. Focused Static Verification

- [x] `go test -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/review/learningpath/...`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/review/learningpath/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/review/learningpath/... ./cmd/api ./cmd/worker` 与 `go mod verify`
- [x] `go mod tidy -diff` 已只读执行并通过，无输出；未修改依赖文件。
- [x] 扫描 target 的 pgx/GORM/database/sql import、`AutoMigrate|Migrator`、root `.Transaction(`、`SQLTransaction`、`*sql.Tx`、动态 SQL、生产 wiring 和日志敏感参数。
- [x] `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-review-learningpath-migration`
- [x] `gofmt -d internal/review/learningpath/adapter/postgres/*.go` 与 scoped `git diff --check`。

## 6. Required Review

- [x] `go-review`：Store 完整性、构造防御、context/error chain、transaction lifecycle、Rows 关闭、并发锁顺序、commit/rollback 与 legacy 兼容。
- [x] `sql-code-review`：参数化、Workspace 隔离、array/JSONB、CAS、advisory/row lock、unique/FK/trigger、DB time、SKIP LOCKED、batch insert、N+1 与 rollback。
- [x] `trellis-check`：PRD/Design/Implement 合规、修改范围、验证证据、dirty worktree 保护和 TODO 9/Final 状态门禁。
- [x] 将范围内明确缺陷修复后重跑受影响门禁；将静态结果与剩余风险写入本任务 `research/static-validation.md`。

## 7. TODO 9 PostgreSQL Gate

- [x] 只在现有两个 integration 文件中建立 legacy/GORM implementation factory；每个实现使用独立 migrated disposable database，不新增测试文件。
- [x] 由普通 `platformpostgres.Open` 的同一个 Pool 提供 `DB()` / `GORM()` / `UnitOfWork()`，验证不存在第二 pool、transaction root 错配或关闭泄漏。
- [x] 成对运行 create/get/status/step、same/different-key、reservation replay/reopen、evidence order、unique/history trigger 与 rollback。
- [x] 成对运行 maintenance-vs-hold/Complete、ABANDONED/ORPHANED、late old attempt、exact release、commit response-loss、cancel/deadline/SQLSTATE。
- [x] 在 Testcontainers 环境中将 12 个顶层真实 PostgreSQL 场景逐项以 `-race -count=1 -timeout=60s` 串行运行，全部通过。
- [x] 记录无 N+1、bounded statement count 和关键 evidence/maintenance 查询的索引/EXPLAIN 证据。

## 8. Completion And Rollback

- [x] TODO 9 不可用时保持全部 PRD AC 未勾选、`task.json.status=in_progress`、legacy production wiring 不变，不归档或宣称完成。
- [x] staged GORM 文件已提交到当前 `dev`：`2cbad522 feat(review): 新增 Learning Path GORM 持久化实现`；任务记录同步到该分支，且未纳入 Foundation、依赖或其他会话改动。
- [x] TODO 9 全部通过后记录 Review 三 child 跨模块门禁所需证据和 Final 构造/legacy 删除清单；本 child 仍不修改父任务或 `cmd/**`。
- [x] 回滚仅删除本 child staged GORM 文件并还原本 child 内共享 helper/test factory；不回滚 Schema、历史数据、Foundation 或其他会话改动。
