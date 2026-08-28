# Review Interview GORM 迁移实施清单

## 0. Planning Gate

- [x] 读取 AGENTS、Trellis workflow、backend specs、父 PRD/Inventory、Foundation 验证记录和目标代码/Schema/测试。
- [x] 盘点 15 个 `application.Store` 方法、QuestionSource、maintenance、2 个生产构造点和现有 integration suite。
- [x] 固定 Session/Question/Turn、completion reservation/two holds、共享 Path views、锁顺序、CAS、receipt、DB time、错误与 TODO 9/Final 边界。
- [x] PRD 完成 lossless convergence；Design、Implement、baseline、implementation contract 与两个 context manifests 已配置。
- [x] 独立 planning review 无剩余 P0/P1；Start shell、completion 阶段职责与 List cursor 的 P1/P2 已修复并重跑 Trellis validate。
- [x] 运行 `python3 ./.trellis/scripts/task.py start .trellis/tasks/08-19-gorm-review-interview-migration`；已确认 status=`in_progress` 后才修改 Go 文件。
- [x] 实现前已读取 backend database/error/logging/quality 规范的完整相关章节；JSONL 截断内容不作为完整规范来源。

## 1. Staged Adapter Foundation

- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，从同一 Pool 获取 GORM root 和 Unit of Work，校验 nil/typed-invalid 状态。
- [x] 建立唯一 `within` 事务边界；callback 内只用 `platformpostgres.GORMTransaction(scope)`，不缓存 scope、不自行 Begin/Commit/Rollback、不解包 `*sql.Tx`。
- [x] 编译期断言 GORMRepository 实现现有 Store/QuestionSource；不修改 Application/Domain Port。
- [x] 新增显式 persistence record、JSONB/nullable/array carrier、固定 `?` SQL 与 Row/Rows helper；禁止 ORM Schema/association/implicit time。
- [x] 仅共享数据库无关 strict codec/equality/domain helper；legacy pgx constructor/SQL/production wiring 保持可编译。

## 2. Selection And Reads

- [x] 实现 QuestionSource `Select`，保持 active-index set query、topic/claim/provenance、`pq.Array` 单参数、claim ID 字典序/claim 内 evidence 稳定顺序、cardinality 和 fail-closed validation。
- [x] 实现 `Get` / `List`，保持 Workspace、Interview shell/type、ordered Questions/Turns、keyset limit+1 和完整聚合 validation。
- [x] 实现 `GetPath`，只访问 INTERVIEW compatibility views，同一 UoW/read scope 中读取 Path+Steps，保持 origin、Artifact/evidence 和 step order 校验。
- [x] 检查所有 Row/Rows no-row、Close/Err、JSONB/nullable/time 和 partial-return 路径。

## 3. Start, Submit And Path Commands

- [x] 实现 Start replay/transaction：advisory -> receipt -> shell/session/questions -> receipt；保留 root response-loss recovery。
- [x] 实现 Submit replay/transaction：advisory -> receipt -> session/shell -> reservation fence -> Turn/Question/Session CAS -> receipt；保留 follow-up 与 root recovery。
- [x] 实现 Path Step replay/update：INTERVIEW view、advisory、receipt、Path/Steps lock、Step+parent CAS 和 atomic receipt。
- [x] 实现 Path Status replay/update：INTERVIEW view、advisory、receipt、all-terminal/transition validation、CAS 和 atomic receipt。

## 4. Completion And Maintenance

- [x] 实现 BeginComplete，保持 Session-first 锁、exact terminal receipt/reservation replay、snapshot version freeze/reopen 和问题闭包；不得提前设置 digest 或读写 hold。
- [x] 实现 PrepareComplete，保持 reservation `FOR UPDATE`、单一 Artifact digest CAS、mismatched ACTIVE -> ORPHANED、same-digest ORPHANED recovery 与 exact replay。
- [x] 实现 Complete，保持 Session -> reservation -> Questions -> two holds -> Report/Path/Steps -> shell/session CAS -> exact-two release -> receipt -> reservation COMPLETED 的单 UoW 原子性。
- [x] 实现 root `FindCompleteReplay` response-loss recovery，只有完整 durable binding 才返回 replay。
- [x] 实现 `AbandonStaleCompletions` 单 CTE，保留 DB time、stable bounded `SKIP LOCKED`、ABANDONED/ORPHANED 和无 Session lock 例外。

## 5. Error, Context, Security And Static Compatibility

- [x] 实现 context-aware classifier：保留 foundation error、cancel/deadline/custom cause、no-row/tx-done 与既有精确 SQLSTATE/constraint/code/retryability。
- [x] 确认公开错误/GORM logger/验证记录不泄漏 SQL、参数、答案、证据、报告、Path、receipt、Artifact、DSN 或绝对路径。
- [x] 静态确认 `cmd/api/main.go`、`cmd/worker/main.go` 仍只调用 legacy `NewRepository`，无 selector/双写/fallback。
- [x] 静态确认 Review Core/Learning Path、Application/Domain、Foundation、migration、go.mod/go.sum/vendor 和其他 child 零改动。

## 6. Focused Static Verification

- [x] `go test -mod=vendor ./internal/review/interview/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/review/interview/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/review/interview/...`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/review/interview/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/review/interview/... ./cmd/api ./cmd/worker` 与 `go mod verify`
- [x] `go mod tidy -diff` 只读检查；若仅为共享工作树预存 go.sum 漂移则记录、不得应用或覆盖。
- [x] 扫描 pgx/GORM/database/sql import、`AutoMigrate|Migrator|Save|Preload|Association`、root `.Transaction(`、独立 `gorm.Open`、动态 SQL、生产 wiring 和敏感日志。
- [x] `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-review-interview-migration`
- [x] `gofmt -d internal/review/interview/adapter/postgres/*.go` 与 scoped `git diff --check`。

## 7. Required Review

- [x] `go-review`：接口完整性、构造防御、context/error chain、UoW 生命周期、Rows、并发锁序、response-loss 与 legacy 兼容。
- [x] `sql-code-review`：参数化、Workspace/origin 隔离、array/JSONB、CAS、advisory/row lock、unique/FK/trigger、DB time、SKIP LOCKED、批量与 N+1。
- [x] `trellis-check`：PRD/Design/Implement 合规、修改范围、验证证据、dirty worktree 保护和 TODO 9/Final 状态门禁。
- [x] 修复范围内明确缺陷后重跑门禁，并写入 `research/static-validation.md`。

## 8. TODO 9 PostgreSQL Gate

- [ ] 只在现有 integration 文件建立 legacy/GORM implementation fixture；每个实现使用独立 migrated disposable database，不新增测试文件。
- [ ] 一个普通 `platformpostgres.Open` Pool 同时提供 `DB()` / `GORM()` / `UnitOfWork()`，验证无第二 pool/root 错配/关闭泄漏。
- [ ] 成对运行 Question Select、Start/Submit、Completion、Path/Step、List 与 maintenance；覆盖 same/different key、并发、rollback、trigger 与坏数据。
- [ ] 以 UnitOfWork success-then-error wrapper 确定性验证 GORM commit response-loss，不复用 pgx Begin double、不靠 sleep。
- [ ] 验证 REPORT/PATH 两个 hold、Artifact digest/attempt、ABANDONED/ORPHANED、late completion 和 exact release。
- [ ] 验证 Interview/Review Path 双向不可见、shell/provenance guard、zero FSRS writes、SQLSTATE/cancel/deadline/Rows/连接释放。
- [ ] 在用户允许的外部 DSN/Testcontainers 环境运行 `go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 60s ./internal/review/interview/adapter/postgres`。
- [ ] 记录关键 Select/List/Completion/maintenance 查询的 statement count、索引与 EXPLAIN，无 N+1/无界扫描。

## 9. Completion And Rollback

- [ ] TODO 9 不可用时保持全部 PRD AC 未勾选、`task.json.status=in_progress`、legacy production wiring 不变，不归档或宣称完成。
- [ ] TODO 9 全部通过后记录 Review 三 child 跨模块门禁与 Final 构造/legacy 删除清单；本 child 不修改父任务或 `cmd/**`。
- [ ] 回滚只删除本 child staged GORM 文件并还原本包必要共享 helper/test fixture；不回滚 Schema、Foundation、其他 owner 或用户改动。
