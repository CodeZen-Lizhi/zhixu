# Ingestion GORM 静态验证记录

日期：2026-08-27

## 已通过

- `go test -mod=vendor ./internal/ingestion/... -count=1 -timeout 60s`
- `go test -race -mod=vendor ./internal/ingestion/... -count=1 -timeout 60s`
- `go vet -mod=vendor ./internal/ingestion/... ./internal/platform/postgres`
- `go test -race -mod=vendor -tags=integration -run '^$' ./internal/ingestion/adapter/postgres -count=1 -timeout 60s`（仅 integration 测试编译）
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- `go list -mod=vendor ./internal/ingestion/... ./cmd/api ./cmd/worker`
- `go mod verify`
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-ingestion-migration`（context 文件有效；大规格文件仅有既有截断 warning）
- `git diff --check`

## 真实 PostgreSQL 实证（本次会话）

- Docker/Testcontainers 可用；`testdb.Require` 使用共享 `internal/platform/testdb` 工厂和 `Pool.GORM()`，未创建第二连接池。
- `go test -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres -run '^TestRepository(PersistsDerivedEvidence|AttemptProjectionLifecycle)$' -count=1 -timeout 300s`：PASS（两个测试均分别运行 legacy/GORM；生命周期场景的 GORM Repository 在外层 GORM transaction 中执行，Repository 自身事务落在嵌套 savepoint，测试结束 rollback）。
- `go test -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres -run '^TestRepositoryConcurrentCreateAttemptIdempotency$' -count=1 -timeout 300s`：PASS（legacy/GORM 各自并发重复 CreateAttempt，恰好一个 Created，其余复用同一 ID）。
- `go test -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres -count=1 -timeout 300s`：PASS（全部 8 个 PostgreSQL integration tests，69 秒）。
- 生命周期断言已覆盖 `GetAttempt` 成功/NotFound、Attempt 幂等冲突、Projection replay、Strategy/Schema 双空与单空精确过滤、UTC/nullable、JSONB selector、derived evidence、stale CAS 和 immutable projection trigger。
- integration 测试文件增加 `//go:build integration`，普通 ingestion test/race 不会隐式启动容器；使用上述 tagged 命令显式运行真实数据库门禁。
- `go test -race -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres -run '^(TestRepositoryConcurrentTransitionAttemptCAS|TestRepositoryConcurrentSaveProjectionIdempotency|TestRepositorySaveProjectionRollsBackFailedChunkBatch)$' -count=1 -timeout 300s`：PASS（36 秒）。legacy/GORM 均证明同一 Attempt version 只有一个 CAS 成功者，重复 Projection 仅一个创建者并复用同一 Projection/Provenance/Span/Chunk 集合；Chunk 批次约束失败后在外层事务内断言四张目标表均无残留，覆盖 GORM nested savepoint 回滚。
- `go test -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres -run '^TestRepositoryRejectsConflictingDeterministicProjection$' -count=1 -timeout 300s`：PASS（23 秒）。legacy/GORM 均拒绝同一 Projection/Strategy/Schema/Sequence 的不同确定性 Chunk，并保持已持久化 Chunk 未被改写；当前共同公开错误为 `ErrorDependencyUnavailable/INGESTION_CHUNK_CREATE_FAILED`，测试锁定既有一致行为。

## 静态边界检查

- Domain/Application/HTTP 未出现 `gorm.io`、`database/sql` 或 `jackc/pgx` import。
- `internal/ingestion`、`cmd/api`、`cmd/worker` 未出现 `AutoMigrate` 或 `Migrator` 调用。
- API、Worker 与 Change Control 集成烟测仍构造 `ingestionpostgres.NewRepository`，没有生产 GORM 接线。
- staged GORM 写入仅在 `GORMRepository.SaveProjection` 的显式 `Transaction` callback 内执行；Span/Chunk 均以固定 500 条 `CreateInBatches` 写入。

## Go Review

- `go-review` 的 repository/data、context、batch/performance 和 quality-gate 清单已审查本次三个 staged GORM 文件及直接 legacy 对照实现。
- Review 发现并修复三项行为漂移：新 Projection 的 Chunk 引用写入外 Span 时恢复 `INGESTION_CHUNK_SPAN_INVALID`；取消/deadline 使用当前操作的稳定 fallback code 且保持非重试 cause；GORM `Transaction` 依据 callback 是否成功区分 begin/callback 与 commit 失败，commit 不再误报 transaction code。
- 复用 Projection 的 Span remap 只要求实际被 Chunk 引用的输入 Span 能映射到持久 Span，保持 legacy 对未引用输入 Span 的行为；Raw Rows 增加 nil 结果防护，避免异常 GORM root/driver 路径 panic。
- 删除未使用的 Attempt/Projection GORM record；Attempt/Projection 保持显式 Raw SQL 列映射，Clause/批量 record 只保留 Provenance/Span/Chunk，设计与实施清单已同步。
- 修复后未发现可静态证明的 P0/P1：所有动态值使用参数绑定；无 `Save`/自动迁移/关联写入；Rows 显式关闭并检查迭代错误；多批 Span/Chunk 写入和 provenance 都使用同一事务 `tx`；复用 Chunk 冲突后有一次有界重读和确定性复核。
- 仍无法静态证明 PostgreSQL 侧的 commit failure 和实际 statement 数，留在 TODO 9。

## SQL Review

- Attempt/Projection Raw SQL 均显式列出字段并使用 GORM `?` 参数绑定；没有动态标识符、字符串拼接、`SELECT *` 或外部输入进入 SQL 文本。
- Attempt 幂等键、Projection contract、Provenance 主键和 Canonical Chunk Strategy/Schema/Sequence 冲突目标与 `00007`/`00015` 一致；SQLSTATE 保持 `23505`、`23503`/`23514`/`22P02`、`40001`/`40P01`、`55000` 分类。
- Projection、Provenance、Span、Chunk 写入均在同一显式 GORM transaction；Span/Chunk 使用 500 条批次，复用 Chunk 仅对精确唯一键 `DO NOTHING` 并在一次有界重读后复核，未发现 N+1 或逐行 SQL。
- Workspace/Artifact/Projection/Span、parser/schema 和 evidence shape 继续由现有 trigger/constraint fail closed；没有 migration、AutoMigrate、association update 或软删除路径。
- 真实 PostgreSQL 的 JSONB carrier、trigger 执行、并发幂等/CAS/Projection、确定性内容冲突与 Chunk 批次失败回滚已通过上述窄场景；执行计划和 statement 上界仍未运行，明确保留为 TODO 9 盲区。

## Trellis Check

- PRD、Design、Implement 与当前 staged 文件已逐项核对；修正了已删除 record 和 GORM-only 查询文件的文档漂移。
- `cmd/api`、`cmd/worker` 与 Change Control 烟测仍构造 legacy `NewRepository`；未接 `NewGORMRepository`，无双写或 runtime fallback。
- PRD Acceptance Criteria 和 TODO 9 全部保持未勾选，`task.json.status` 保持 `in_progress`；任务未完成、未归档。
- R6/Design 目标要求 legacy/GORM 最终共用接收 `ctx` 的分类入口；当前生产 pgx 仍保留 `classify(err, fallback)`，staged 路径使用 `gormClassify(ctx, err, fallback)`。本阶段不改变冻结的生产基线，TODO 9 必须先比较两条路径的 cancel/deadline/`sql.ErrTxDone` 行为，再共同收敛该入口；在此之前不能把 R6 或对应 AC 视为完成。

## 未通过但未修改

- `go mod tidy -diff` 退出 1，仅报告任务前已存在的 `go.sum` 漂移；本任务没有修改 `go.mod` 或 `go.sum`，未应用 tidy 输出。
- 本次已移除对 `ZHIXU_TEST_DATABASE_URL` 的依赖，改用共享 Testcontainers fixture；未修改 `go.mod`/`go.sum`。

## TODO 9 留存

本次已在现有 integration 文件原位覆盖五个方法的主路径、JSONB/evidence、Strategy 双空/单空、immutable trigger、并发 CreateAttempt/CAS/Projection、确定性内容冲突与 Chunk 批次失败回滚；仍需补齐 context/`sql.ErrTxDone`、5,000 条批量 statement 上界及 commit-time connection loss 盲区记录。生产 Composition 继续保持 legacy pgx，child 状态保持 `in_progress`，直到 TODO 9 全部通过并由 Final child 完成切换。
