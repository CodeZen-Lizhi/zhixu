# Authoring GORM staged implementation 静态验证

日期：2026-08-20；dev 提交前复核：2026-08-24

## 结论

- staged `GORMRepository` 已覆盖 Authoring Repository、Revision Search、Publication reconciliation 与 Restore publication consumer 契约，并通过局部编译、单元、race、vet 与静态检查。
- 所有多语句写事务及 Repeatable Read 多查询快照均复用 `Pool.UnitOfWork().Within` 和 `GORMTransaction(scope)`；只读单语句使用共享 GORM root。
- 旧 pgx 生产构造与 SQL 行为、生产 Composition、Change Control implementation、Foundation、migration 与测试文件未由本任务修改；共享 legacy scanner 只扩展了 `database/sql` no-row 识别。
- 任务保持 `in_progress`；TODO 9 真实 PostgreSQL 等价门禁通过前不切生产、不勾选完整验收、不归档。

## Trellis Check 与 Review 修复

- 修正 `classifyGORM`：保留非 dependency Foundation error；取消和 deadline 分别映射旧 non-retryable/retryable 语义；通过错误链保留自定义 `context.Cause`，公开 `Error()` 仍只暴露安全 code/kind；兼容 `sql.ErrNoRows`、SQLSTATE fallback，并让无 context 取消的 `sql.ErrTxDone` 保留调用阶段 fallback。
- 所有直接 root read 与 `Reconcile` 现在都拒绝 nil context；Repository/UoW 依赖同时拒绝 interface typed-nil，避免进入 GORM 后 panic。
- `SearchArticleRevisions` 删除 legacy 不存在的 Document/hash/完整 Aggregate 校验，只保留旧实现实际具有的 scan、UTC 与 projection 行为；任务 PRD/Design 同步明确各读取路径的精确校验粒度。
- 所有 Raw `Row` setup error 与 Create/Update/Freeze 等 DB error 统一进入操作级 `classifyGORM` fallback，Rows 路径继续保证 Close/Err 后才返回结果。
- `GORMRepository.within` 只在 callback 已成功后把 UoW 返回错误归类为 `AUTHORING_COMMIT_FAILED`，从而区分 callback/begin 与 commit/deferred-trigger failure；context cancel/deadline 仍优先保留 sentinel 与自定义 cause。
- 修正 Freeze 写入 `core.article_revision` 的 `optimization_mode`/commit/hash 参数错位，以及 `working_draft_command` receipt INSERT 多一个占位符的问题；两处原 SQL 均会在真实 PostgreSQL 上失败或错误绑定。
- Restore publication 复用 legacy 的确定性 revision ID、publication facts 与 existing revision 校验，消除 staged/legacy 双份规则漂移。
- 显式 persistence records 对 `CreatedAt`/`UpdatedAt` 关闭 GORM 自动时间戳，避免模型约定产生隐式写入行为；静态接口断言补齐私有 publication finalizer contract。
- PRD/Design/Schema research 修正 Update replay 描述、读取校验粒度及 `23505` constraint 到稳定错误码的精确映射；任务 context manifest 去除重复超大 spec 注入并通过 Trellis validate。

## 通过的门禁

```text
go test -mod=vendor ./internal/authoring/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/authoring/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/authoring/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/authoring/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s
go list -mod=vendor ./internal/authoring/... ./internal/platform/postgres ./cmd/api ./cmd/worker
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-authoring-migration
gofmt -d internal/authoring/adapter/postgres/errors.go internal/authoring/adapter/postgres/repository.go internal/authoring/adapter/postgres/gorm_*.go
git diff --check -- internal/authoring/adapter/postgres .trellis/tasks/08-19-gorm-authoring-migration
```

静态禁止模式扫描无命中：私有 GORM transaction/Begin/Commit/Rollback、`sql.Tx`、`pgx.Tx`、`pgxpool`、AutoMigrate/Migrator、`gorm.Model`、Save、Association、second pool 与 nil context fallback。Domain/Application 无 GORM、`database/sql` 或 pgx import；API/Worker 仍未调用 staged `NewGORMRepository`。

`go mod tidy -diff` 已只读执行但未通过：输出是任务前已有的全仓 `go.sum` 清理及 sqlite 依赖增量，未由 Authoring child 应用；`go mod verify` 通过。

## 2026-08-24 dev 提交前复核

- 对照 detached 来源快照复核当前 `dev`，确认 8/20 后续整合修复仍在：Freeze Article Revision 与 command receipt placeholder/参数绑定正确；事务失败统一返回零值结果；callback 成功后的 UoW 错误使用 commit-stage fallback；取消保留标准 sentinel 与自定义 cause。
- 重新运行本文件列出的 Authoring test/race/vet、integration compile、API/Worker compile、`go list`、`go mod verify`、Trellis validate、gofmt、禁止模式与 scoped `git diff --check`，结果全部通过。
- 生产 Composition 仍未调用 `NewGORMRepository`；本次只更新当前 child 的验证记录，任务继续保持 `in_progress`，PRD AC 与 TODO 9 保持未勾选。
- 当前环境仍未设置 `ZHIXU_TEST_DATABASE_URL`，因此没有新增真实 PostgreSQL、GORM 行为等价或 SQL `EXPLAIN` 证据。

## TODO 9 运行时盲区

- 当前环境未提供 `ZHIXU_TEST_DATABASE_URL`。现有 PostgreSQL integration tests 仍只实例化旧 pgx Repository；integration `-run '^$'` 只是测试编译，不是 staged GORM 运行证据。
- 仍需真实 PostgreSQL 验证 GORM commit/rollback、CAS/replay、并发锁序、constraint/trigger、response-loss、cancel/deadline 与数据库时间行为。
- commit/deferred-trigger 的运行时错误码仍需 TODO 9 在真实 PostgreSQL 上确认；静态实现已用 callback 成功标志恢复 legacy `AUTHORING_COMMIT_FAILED` fallback，未修改共享 Foundation 契约。
- 未执行 SQL `EXPLAIN` 或负载测试；查询形状、索引与真实数据基数仍由 TODO 9 PostgreSQL 环境验证。
