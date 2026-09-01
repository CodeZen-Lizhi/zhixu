# Capture Repository GORM 迁移实施清单

## 1. 规划与基线

- [x] 核对 Capture Domain/Application/Adapter、Workspace writer、Agent Model Run、API/Worker Composition 和全部 pgx 事务入口。
- [x] 固定 Create/Retry/Outbox/Processing/Profile 的原子事务、锁序、CAS、DB time、exact replay 和错误语义。
- [x] 核对 `00002/00006/00068` Schema、constraint、index、trigger、append-only 与 guarded Down 事实。
- [x] 盘点现有 unit/integration/migration/Worker tests、真实 PostgreSQL fixture 和 TODO 9 双实现方案。
- [x] 识别 Profile 的 Agent pgx-only transaction Port 阻断，修正父依赖图与阶段边界。
- [x] 完成独立 Go/调用链、SQL/Schema 和规划 Review；修复全部 P0/P1/P2 规划缺口。
- [x] 用户审批本最终方案。
- [x] 运行 `task.py start`，确认 child 为 `in_progress` 后使用 `trellis-before-dev` 重新加载实施上下文。

## 2. Capture Core staged 构造

- [x] 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool, workspaceapplication.ScopedSourceWriter)`；只使用一个 Pool 的 GORM root/UoW。
- [x] 添加 Repository、RetryScheduler、OutboxStore、ProcessingRepository 静态断言；constructor/ready 拒绝 nil、typed-nil、无 GORM/UoW 与 nil context。
- [x] 保留 legacy Repository、Workspace TransactionWriter 和全部 cmd 构造；禁止 selector、双写、fallback、second pool。
- [x] 抽取/复用 receipt codec、domain mapper、validation、column/scanner 等纯逻辑；不新增弱类型 DB 抽象。

## 3. Create、读取与 Retry

- [x] 实现 Replay/Create，使用同一 UoW 调 Workspace scoped writer并写 Capture/Outbox/receipt；冲突 rollback 后 root exact replay。
- [x] 实现 Get/List，保持 Workspace scope、可选过滤、DESC tuple keyset、Limit+1、limit 100 和整页 fail-closed。
- [x] 实现 ReplayRetry/ScheduleRetry，保持 Capture lock/CAS、状态分支、Outbox、strict receipt 和 response-loss 语义。
- [x] Raw Row/Rows 防御 statement/nil handle，Rows Close/Err；UUID/enum/nullable/time/JSON 显式 mapping。

## 4. Outbox 与 Processing

- [x] 实现 ClaimNext CTE、DB-time lease、SKIP LOCKED、稳定排序与 lease/version scan。
- [x] 实现 MarkPublished/Reschedule/Poison，保持 owner/version/terminal predicate 和 RowsAffected lease-lost 映射。
- [x] 实现 BeginAttempt、MaterializeURL、MarkRefreshRunning、MarkProfileRunning、MarkReady、MarkReadyDegraded、FailAttempt。
- [x] 保持 Capture -> Attempt 锁序、Workflow binding、stage/status 分支、Refresh checkpoint closure、version+1 与 terminal replay。
- [x] MaterializeURL 在同一 scope 内调用 Workspace `RegisterSourceVersionScoped`，并复核 Source/Version binding。

## 5. Profile 前置与 staged 实现

- [x] Agent child 在 `internal/agent/application/scoped_model_run.go` 提供 `GetModelRunScoped`、`GetModelRunRecordScoped`、`FinalizeModelRunScoped` 及同 transaction GORM 实现；保留 for-update/CAS/exact replay，Capture 不实现或复制 Agent SQL。
- [x] 新增 `GORMProfileRepository` 构造与 ProfileGenerationRepository/ProfileReader/ProfileBatchReader/ProfileRetryScheduler 静态断言。
- [x] 实现 LookupReady/LoadSource/Prepare/Bind/Complete/Fail/Get/GetProfiles，保持 advisory/shared/row lock 顺序和 bounded set queries。
- [x] 实现 Profile retry，保持 Profile -> Capture 锁序、旧 Revision 可读、Outbox/receipt/CAS 同事务。
- [x] Revision/Evidence/Profile Attempt/Agent Model Run 使用同一 scope 原子终结；Evidence/Profile batch arrays 使用 `pq.Array`。
- [x] 保持 Profile JSON/content digest、Evidence 闭包、Chunk 500/128 KiB SQL fail-closed、READY/STALE/capability/replay 语义。

## 6. 错误、安全与静态约束

- [x] 保留 SQLSTATE/constraint、not-found、version/idempotency conflict、retryability、manual recovery 和 commit unknown。
- [x] 处理 `sql.ErrNoRows`、`gorm.ErrRecordNotFound`、`sql.ErrTxDone`；context cancel/deadline 保留 sentinel 与 custom cause。
- [x] 日志/错误不含正文、URL 响应、Profile JSON、receipt、SQL 参数、DSN、Secret 或 stage locator。
- [x] 静态确认无 AutoMigrate/Migrator、association/preload、hook、gorm.Model/soft delete、自动时间、pgx/any 新泄漏。

## 7. Core 局部验证

- [x] `go test -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/capture/... ./internal/workspace/application ./internal/agent/application ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/platform/migration ./cmd/worker -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/workspace/... ./internal/agent/... -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/capture/... ./internal/workspace/... ./internal/platform/postgres ./cmd/api ./cmd/worker`
- [x] `go mod verify`；`go mod tidy -diff` 只审查，若仍为预存 `go.sum` 漂移则记录且不应用。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-capture-migration`
- [x] `gofmt -d`（受影响 Go 文件）与 `git diff --check`

integration `-run '^$'` 只验证测试编译，不作为真实 PostgreSQL 证据。预计超过 60 秒的命令停止扩大范围并记录盲区。

## 8. Review

- [x] 使用 `go-review` 检查 Port/constructor、UoW/scope、typed nil、context cause、Raw Row/Rows、scanner、资源和 production compatibility。
- [x] 使用 `sql-code-review` 检查参数化、array/JSONB、Workspace predicate、锁序、CAS、DB time、SKIP LOCKED、trigger/SQLSTATE 和执行计划。
- [x] 使用 `trellis-check` 检查 PRD/Design、Workspace/Agent 依赖、production wiring、验证证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；记录到 `research/static-validation.md`。

## 9. TODO 9 真实 PostgreSQL门禁

- [x] 原位参数化现有 integration factory；每个子用例通过 `testdb.Require` 完成迁移池关闭、唯一 `platformpostgres.Pool` 打开，以及平台 Pool 先关闭再 DROP 的 cleanup。
- [x] legacy 从该 Pool 的 `DB()` 构造；GORM 从同一 Pool 创建 Workspace `GORMRepository`/真实 ScopedSourceWriter，再注入 Capture `GORMRepository`，未使用 mock 证明跨 owner 事务。
- [x] 比较并发 Create、exact replay/conflict、Source/Artifact/Version/Capture/Outbox/receipt 原子 commit/rollback 和 response-loss。
- [x] 比较 Retry、Outbox 双 worker claim/reclaim、owner/version CAS、Attempt replay、URL materialization 和全部已使用 stage/terminal transition。
- [x] 比较 Profile prepare/bind/complete/fail/retry、旧 revision survival、Evidence closure、Model Run 终结和 replay。
- [x] 覆盖 Index activation/Profile completion 交错、advisory/shared/row locks、deferred FK/trigger 与真实 SQLSTATE；生产组合 deadlock/serialization 按父任务留给 Final 复跑，不阻断模块归档。
- [x] 覆盖 UUID/JSONB/array/cast、cancel/cause/deadline、stale scope、commit failure、corrupt row no-partial、连接释放和目标查询计划；scope/commit failure 复用 Foundation 真实 UoW 门禁。
- [x] 已在 `final-handoff.md` 固定 Final 阶段需复跑的 Worker Capture Outbox -> Workflow/River、API/Worker Composition、发布链和生产组合 deadlock/serialization 门禁；本 child 未提前修改 `cmd/**`。
- [x] TODO 9 模块级门禁与 Profile 前置全部通过，PRD AC 已勾选；生产切换和 legacy 删除继续由 Final 独占。

## 10. 回滚点

- Core 前：只改规划工件，无运行时影响。
- Core staged：删除新增 GORM Core 文件即可，legacy Repository 与生产接线不变。
- Profile staged：删除新增 GORM Profile 文件即可，legacy Profile/Agent 路径不变。
- Final 切换失败：由 Final 恢复 legacy Composition；禁止双写或 fallback 掩盖差异。
