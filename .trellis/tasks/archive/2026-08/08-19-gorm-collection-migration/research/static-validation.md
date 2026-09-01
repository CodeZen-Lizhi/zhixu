# Collection staged GORM 完成验证

## 结论

Collection staged `GORMRepository`、共享 Unit of Work 写事务、只读快照查询和 scoped durable binding verifier 已完成。legacy/GORM 在独立、已迁移的 disposable PostgreSQL 数据库上通过等价门禁；生产 `cmd/api` 与 `cmd/worker` 的 Collection 构造仍全部使用 legacy `NewRepository`，本 child 没有切换 Composition、修改 Schema 或删除兼容路径。

PRD Acceptance Criteria 与 TODO 9 清单已满足。Graph/Health caller 接线及最终生产切换仍归对应 owner child 与 Final。

## 实现与边界

- `gorm_adapter.go` 提供 database/sql-backed GORM 适配、`$n -> ?` renderer、array/JSONB carrier、严格 Row/Rows scanner 和 context-aware 错误分类。
- `gorm_repository.go` 实现 Create/Update/Archive/Get/List/Search、Results/Preview、Durable Plan/Read/Revision Verify 及 scoped binding verification。
- Application 只新增 opaque `ScopedDurableScanBindingVerifier`；签名不暴露 GORM、database/sql、pgx 或 `any`。
- legacy helper 只抽取共享 validation/scanner/SQL builder，并增强损坏行、跨边界、重复与缺失结果的 fail-closed 检查。
- 未新增 AutoMigrate、Migrator、Hook、association/preload、soft delete、第二物理池、双写、fallback 或 delete。

## 真实 PostgreSQL 证据

每个 legacy/GORM 子用例通过 `testdb.Require` 创建独立数据库，Atlas/River migration 完成后从同一 `platformpostgres.Pool` 派生 pgx DB、GORM root 与 Unit of Work。seed 均已提交，不复用外层未提交 pgx transaction。

已通过的双实现场景：

- lifecycle：create、重开 Repository 后 exact replay、跨 mutation/archive 历史 snapshot replay、idempotency/type conflict、active name `23505`、CAS、archive immutable、Workspace isolation、List/Search/cursor stale。
- transaction/SQLSTATE：失败 create 的 aggregate/receipt 均无残留；成功 create 可由新 Repository 重放；归档行 trigger 返回并保留 `55000`；损坏 view config 在已扫描有效行后仍返回空 page，事务回滚且连接可复用。
- query：全部 Registry field/operator、IN/array、JSONB、重复和多位 marker、NULLS LAST、稳定 keyset、no-gap/no-duplicate、cursor binding/revision stale、bounded hydration。
- snapshot/资源：Results/Preview count/page/hydration/revision 同快照，statement timeout、deadline、`context.WithCancelCause` sentinel+cause、Rows Close/Err 和 pool reuse。
- durable：Plan/Read、跨 Repository restart、checkpoint、pair/node ordering 与唯一性、definition/query hash/revision/count drift、health membership revision、caller-owned commit/rollback。
- HTTP：create/update 在 archive 后的 exact replay 通过 legacy/GORM Handler 契约。

## 性能与计划

- reference fixture 为 512 个额外 Topic；legacy statement trace 固定 begin/timeout/revision/count/page/hydration/commit 次数，GORM 执行同一共享 SQL builder；25 次采样 P95 低于 2 秒门限。
- EXPLAIN fixture 包含 512 条 Collection、384 个 active Topic 与 2048 个 deprecated Topic。List/Search、Results 与 Durable page 对 owner relation 均使用批准索引，无目标 relation 顺序扫描；relation/health predicate 命中 canonical relation/health indexes。
- 当前 `idx_learning_smart_collection_workspace_status_updated` 的末列为 `id ASC`，List 契约为 `updated_at DESC,id DESC`。目标规模下 PostgreSQL 选择 `uq_learning_smart_collection_active_name` 并执行有界排序；这是既有 Schema 的非阻断优化点，本 child 按 Out Of Scope 未修改 migration。

## 验证命令

以下门禁均通过：

```text
go test -mod=vendor -race -count=1 -timeout=60s ./internal/collection/...
go test -mod=vendor -tags=integration -race -count=1 -run '<各 Collection 顶层场景>' -timeout=60s ./internal/collection/adapter/postgres
go test -mod=vendor -tags=integration -race -count=1 -run '^TestCollectionHTTPExactReplayAfterMutationAndArchive$' -timeout=60s ./internal/collection/http
go test -mod=vendor -tags=integration -race -count=1 -run '^TestFunctionalFixtureCommitsAndCleansCanonicalFacts$' -timeout=60s ./internal/graph/testfixture
go test -mod=vendor -tags=integration -race -count=1 -run '^TestM10MixedCapacityFixtureReferenceWindow$' -timeout=60s ./internal/graph/testfixture
go test -mod=vendor -tags=integration -run '^$' -count=1 -timeout=60s ./internal/collection/...
go vet -mod=vendor ./internal/collection/... ./internal/platform/postgres ./internal/foundation
go vet -mod=vendor -tags=integration ./internal/graph/testfixture
go test -mod=vendor -run '^$' -count=1 -timeout=60s ./cmd/api ./cmd/worker ./internal/graph/... ./internal/health/... ./internal/export/... ./internal/organizing/...
go list -mod=vendor ./internal/collection/... ./cmd/api ./cmd/worker ./internal/graph/... ./internal/health/... ./internal/export/... ./internal/organizing/...
go mod tidy -diff
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-collection-migration
git diff --check
```

真实数据库顶层场景均单独执行，以遵守后端测试 60 秒上限；integration compile-only 不计作行为证据。

## Review

- Go Review：未发现剩余 P0-P2。公开 Port、typed nil、UoW/scope 所有权、context cause、Row/Rows、scanner、cursor 与 legacy 接口兼容均符合契约。
- SQL Review：未发现剩余 P0-P2。Workspace predicate、锁序、receipt/CAS、精确 SQLSTATE、动态 identifier 白名单、参数化、array/JSONB、keyset、revision、hydration、durable bounded query 与 EXPLAIN 均通过。
- Trellis/接线 Review：生产 Collection 构造仍为 5 个 legacy 调用点；Application/Domain 无具体数据库 import；任务工件与真实门禁一致。

## 剩余非阻断风险

- Foundation `TransactionScope` 暂无 Pool owner identity；active foreign-Pool scope 无法由 Collection 单独识别。TODO 9 fixture 与后续 Composition 必须继续从同一 `platformpostgres.Pool` 派生 caller UoW 与 verifier。
- List 索引末列方向与 `id DESC` 契约不完全一致；当前 512 行门禁使用批准的 partial index且无顺序扫描。若规模增长导致排序成本超预算，应由 Schema owner 新增匹配方向的 migration 后重新 EXPLAIN。
