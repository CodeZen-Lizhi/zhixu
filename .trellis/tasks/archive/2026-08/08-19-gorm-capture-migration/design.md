# Capture Repository GORM 迁移设计

## 1. 目标与阶段边界

Capture 同时拥有幂等命令、跨 Workspace Source 写入、Outbox lease、Processing Attempt 状态机和 Profile/Agent 跨聚合终结，不适合改写成普通 ORM CRUD。本 child 采用共享平台 UoW、参数化 GORM Raw SQL、显式 scanner 和 owner-defined scoped Port。

实施分为两个阶段：

1. **Capture Core**：迁移 `Repository`、Retry、Outbox 与 Processing；Workspace `ScopedSourceWriter` 已可用，因此可以先交付未接 Composition 的 staged Adapter。
2. **Profile Closure**：等待 Agent owner 提供基于 `foundation.TransactionScope` 的 Model Run reader/finalizer，再迁移 Profile/Retry。Profile closure 完成前 child 不满足 AC，也不能归档。

TODO 9 前不改生产 Composition、migration、Domain/Application 行为或文件发布协议。任一 staged 路径可通过删除新增文件回滚，legacy 生产路径不受影响。

## 2. Owner 与 Schema 事实源

| Owner | 事实 |
| --- | --- |
| Capture | `core.capture`、`core.capture_command`、`ops.capture_outbox`、`ops.capture_attempt` |
| Capture Profile | `learning.document_knowledge_profile`、revision、attempt、evidence |
| Workspace | `core.source`、`core.content_artifact`、`core.source_version` |
| Agent | `agent.model_run`、`agent.model_call` |
| Workflow/Retrieval/Model Settings | Capture/Profile 只读取冻结 binding 或外键事实，不取得 owner 写权限 |

`00068_capture_profile.sql` 是 Capture/Profile 表、约束、index、append-only trigger、stale trigger 和 Down guard 的唯一事实源；Workspace 内容事实来自 `00002_workspace_sources.sql` 与 `00006_content_artifact.sql`。本 child 不新增 migration，也不让 GORM 生成或推断 Schema。

## 3. Staged 构造与接口

Capture Core 新构造：

```go
NewGORMRepository(
    pool *platformpostgres.Pool,
    workspaceWriter workspaceapplication.ScopedSourceWriter,
) (*GORMRepository, error)
```

构造器只接完整 Pool 和稳定 Port，从同一 Pool 取得 `GORM()` 与 `UnitOfWork()`。`GORMRepository` 静态实现：

- `captureapplication.Repository`
- `captureapplication.RetryScheduler`
- `captureapplication.OutboxStore`
- `captureapplication.ProcessingRepository`

Profile 依赖由 Agent owner 在 `internal/agent/application/scoped_model_run.go` 新增以下最小 Port；不加入 Capture 未使用的 attempt lookup：

```go
type ScopedModelRunFinalizer interface {
    GetModelRunScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (domain.ModelRun, error)
    GetModelRunRecordScoped(context.Context, foundation.TransactionScope, foundation.ID, foundation.ID, bool) (ModelRunRecord, error)
    FinalizeModelRunScoped(context.Context, foundation.TransactionScope, FinalizeModelRunCommand) (domain.ModelRun, bool, error)
}
```

`forUpdate=true` 必须在当前 scope 中执行 `FOR UPDATE`；Finalize 保留 CAS、version conflict 与 exact replay 返回值。该 Port 不开始、提交或回滚事务，拒绝 nil/invalid/stale scope，并由 Agent GORM Repository 使用传入 scope 实现。公开签名不出现 `any`、GORM、database/sql 或 pgx。Capture 后续构造：

```go
NewGORMProfileRepository(
    pool *platformpostgres.Pool,
    modelRuns agentapplication.ScopedModelRunFinalizer,
) (*GORMProfileRepository, error)
```

Capture child 不实现 Agent-owned Port，也不把 Agent SQL 复制到 Capture。Agent Port 未就绪时不创建虚假适配器、不传 legacy `ModelRunTxFinalizer`、不拆事务。

Foundation scope 当前不能验证 active scope 的 Pool identity。Capture 自己拥有的 UoW 与构造时 Port 必须由同一 Composition Pool 创建；active foreign-Pool scope 的运行时拒绝需要 Foundation 后续 affinity 能力，不能在 Capture 里用类型断言伪造。

## 4. 文件与复用策略

Capture Core staged 文件按责任拆分：

- `gorm_core.go`：构造、ready/context、UoW、error/context classifier、Row/Rows 防御；
- `gorm_repository.go`：Create/Replay/Get/List；
- `gorm_retry.go`：Capture retry 与 receipt；
- `gorm_runtime.go`：Outbox 与 Processing Attempt；
- `gorm_model.go`、`gorm_queries.go`：显式 carrier/scanner、固定 SQL、JSONB/nullable/UTC mapping。

Profile Closure staged 文件：

- `gorm_profile_repository.go`：read/prepare/bind/complete/fail；
- `gorm_profile_retry.go`：Profile retry/receipt；
- `gorm_profile_model.go`、`gorm_profile_queries.go`：Profile/Revision/Attempt/Evidence scanner 与固定 SQL。

`GORMProfileRepository` 必须静态实现 `ProfileGenerationRepository`、`ProfileReader`、`ProfileBatchReader` 与 `ProfileRetryScheduler`，避免批量 owner 读取到接线阶段才暴露漏实现。

优先抽取 legacy 与 GORM 可共享的纯 validation、receipt codec、domain mapper、column list 和窄 `Scan(...any) error` helper。不得创建一个同时模拟 pgx/GORM 的 `any` DB 抽象，也不得让 staged Adapter fallback legacy root。

## 5. Capture Create、Replay 与 List

`Create` 先在 callback 外完成纯输入验证和 receipt payload 准备，然后在一个默认隔离 UoW 内：

```text
Workspace RegisterSourceScoped / RegisterSourceVersionScoped
-> binding revalidation
-> Capture INSERT ... RETURNING
-> Capture Outbox INSERT
-> command receipt INSERT
-> UoW commit
```

任何唯一冲突都先退出 callback，让平台 rollback，再从 root 查询 command receipt。只有完整 binding、schema version、strict JSON 和 Domain Capture 一致时返回 exact replay；否则 idempotency conflict。callback 成功后的 commit error保持 `CAPTURE_COMMIT_FAILED`/结果未知，不猜测提交状态。

Get 使用 Workspace+Capture predicate 和 guarded `Row().Scan`。List 的可选过滤只拼接封闭 SQL 片段，值全部参数化；保持 `(captured_at,id) DESC`、Limit+1、limit 1..100、完整 scanner 和全页 fail-closed。

## 6. Retry、Outbox 与 Processing

Capture retry 的锁/写顺序保持：Capture `FOR UPDATE` -> receipt replay -> state/version 检查 -> Capture CAS -> Outbox -> receipt -> commit。唯一 receipt 冲突在 rollback 后 root replay。

Outbox claim 保留现有单条 CTE：due/terminal/lease predicate、`ORDER BY available_at,id`、`FOR UPDATE SKIP LOCKED LIMIT 1`、DB-time lease 与 `RETURNING`。MarkPublished、Reschedule、Poison 是固定参数的 CAS Exec，`RowsAffected != 1` 仍映射 lease lost。

Processing 写路径统一由 UoW 拥有事务，锁序保持：

```text
Capture FOR UPDATE
-> Attempt FOR UPDATE / idempotent insert-and-lock
-> optional Workspace scoped Source Version write
-> Capture CAS
-> Attempt CAS
-> commit
```

BeginAttempt 保留 `(capture_id,workflow_run_id,attempt_number)` exact replay。MaterializeURL 通过同一 scope 调 `RegisterSourceVersionScoped`，复核 Source/Version ID 后才推进状态。Checkpoint、ready/degraded/fail 保留每一阶段的固定状态分支、Refresh binding 校验、version+1、terminal replay 和 caller time；Outbox lease 继续使用 DB time。

## 7. Profile Closure

Profile GORM 路径只在 Agent scoped Port 可用后实施。关键事务保持 legacy 锁序：

- Prepare：Source key transaction advisory lock -> Capture binding -> ready/profile/attempt row locks；
- Bind：Profile Attempt `FOR UPDATE` -> Agent Model Run `FOR UPDATE` -> Attempt CAS；
- Complete：Profile Attempt -> Model Settings/Index shared locks -> Source advisory fence -> Profile -> Agent Model Run/Calls -> Revision/Evidence -> Profile/Attempt CAS -> Agent Model Run CAS；
- Fail：Profile Attempt -> Profile -> optional Agent Model Run -> Profile/Attempt CAS；
- Retry：Profile `FOR UPDATE` -> Capture `FOR UPDATE` -> Profile/Capture CAS -> Outbox -> receipt。

Revision/Evidence 保持 append-only。Evidence `unnest(?::text[])` 使用 `pq.Array` 单参数；Profile batch 的 `ANY(?::uuid[])` 也使用 driver.Valuer。Profile content/receipt JSON 必须先 canonical/strict validation，JSONB carrier 返回 string 而非可能推断为 bytea 的裸 `[]byte`。

LoadSource 保留 SQL 中的 Chunk 数量和 `octet_length` 上限、稳定 chunk/span 顺序、Source/Projection/Index binding 复核；GetProfiles 保留三次 bounded set query 并按请求 Source Version 顺序重建，不引入 N+1 或 association preload。

## 8. 错误、上下文与资源

新增 GORM classifier 保留 legacy SQLSTATE：

- `23505`：按调用点映射 version/idempotency conflict；
- `23503/23514/55000`：consistency violation；
- `40001/40P01/55P03`：retryable failure；
- no row：按方法映射 not found、found=false 或 CAS conflict；
- `sql.ErrTxDone`：dependency unavailable；
- cancel/deadline：保留 `ctx.Err()` 与不同的 `context.Cause(ctx)`。

Foundation Error 原样传递。新分类继续以安全的 `foundation.Error` 暴露稳定 code，公开 `Error()` 不包含底层 cause；内部 `Unwrap` 保留原始错误链供 `errors.Is/As` 诊断，任何日志仍不得展开 SQL 参数、payload 或原始内容。所有 root 方法入口验证 repository、GORM root、context；scope 仅在 callback 中立即解包并使用，不缓存。Raw `Row()`/`Rows()` 检查 statement error 和 nil handle；多行总是 Close 并检查 `Rows.Err()`。

## 9. TODO 9 验证

不新增测试文件。每个 legacy/GORM 子用例使用独立数据库。固定 fixture 流程是：raw migration pool 创建子库并完成 migration，保存 `raw.Config().ConnString()` 后关闭 raw pool，再用该 URL 调 `platformpostgres.Open` 创建该子用例唯一完整 Pool；cleanup 先关闭平台 Pool，再执行既有 force-drop cleanup。URL 只在内存传递，不进入日志。

GORM 子用例从这个平台 Pool 创建 `workspacepostgres.NewGORMRepository(platformPool)`，把它作为真实 `ScopedSourceWriter` 注入 Capture `NewGORMRepository(platformPool, writer)`；legacy 子用例只使用同一个平台 Pool 的 `DB()`。原位扩展：

- `internal/platform/migration/capture_repository_integration_test.go`：每个 legacy/GORM 子用例独立数据库，覆盖并发 Create、exact replay/conflict、六类原子事实、Retry、Attempt、URL materialization 与阶段状态；
- `internal/platform/migration/capture_profile_repository_integration_test.go`：Agent scoped Port 可用后对照 Profile capability/retry、READY/STALE/rebuild、Evidence closure、Model Run terminal、response replay 和 Index activation 交错锁；
- `internal/platform/migration/capture_profile_integration_test.go`：Schema/Down guard/跨 Workspace 约束；
- `cmd/worker/capture_composition_integration_test.go`：Final 阶段验证 Outbox -> Workflow/River，不由本 child 提前改 production wiring。

真实 PostgreSQL 必测：每个原子写点的故障回滚、commit response-loss、并发 exact replay、lease reclaim、锁竞争/deadlock SQLSTATE、trigger/deferred FK、array/JSONB binding、cancel/cause/deadline、stale scope、连接释放和目标查询计划。`ZHIXU_TEST_DATABASE_URL` 缺失时 integration compile-only 不能勾 AC。

## 10. 回滚与 Final 交接

- Core staged 回滚：删除新增 GORM Core 文件；legacy Repository/Composition 未动。
- Profile staged 回滚：删除新增 GORM Profile 文件；legacy Profile/Agent 端口未动。
- Final 统一把 API/Worker helper 改为完整平台 Pool 和稳定 Port，切换 Capture/Profile 构造，复跑 Worker Outbox/Workflow 与浏览器发布门禁，再删除非 allowlist pgx。
- Final 切换失败恢复 legacy Composition；禁止同时写两套 Adapter 或在失败时静默 fallback。
