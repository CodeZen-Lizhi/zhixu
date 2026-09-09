# Source-ready 事务通知接口

本工作包仅实现 Ingestion → 既有 `workflow.outbox_event` 的 durable seam；模型、合成 Workflow、消费者 provenance gate 与进程接线由其余工作包负责。复用现有 Outbox、GORM 和同池 UoW，无新增迁移或队列状态机。

## 生产构造

```go
outbox, err := workflowpostgres.NewGORMSourceReadyOutbox(pool)
repository, err := ingestionpostgres.NewGORMRepositoryWithSourceReady(pool, outbox)
```

新构造器从一个 `*platformpostgres.Pool` 同时取得 GORM root / `foundation.UnitOfWork`，必须注入非 nil `ingestiondomain.SourceReadyAppender`。旧 `NewGORMRepository(*gorm.DB)` 为既有隔离测试和历史调用保持兼容，没有自动通知；生产必须改用新构造器。

2026-09-09 源码核对到两个生产构造点：`cmd/api/main.go:595`（API Ingestion Handler 的 Repository），`cmd/worker/main.go:3508`（`newSourceProcessingComponents` 的共享 Ingestion 依赖，提供给 Source refresh / Capture 等 Worker 调用方）。接线方应再次搜索 `ingestionpostgres.NewGORMRepository` 确认全部调用点；本工作包不改 `cmd/`。非测试生产源码的 `UPDATE ingestion.attempt` 仅由该 Repository 持有，没有发现另一个绕过入口。

## 公开类型与方法

```go
// internal/ingestion/domain/source_ready.go
type SourceReady struct {
    WorkspaceID       foundation.ID `json:"workspace_id"`
    SourceID          foundation.ID `json:"source_id"`
    SourceVersionID   foundation.ID `json:"source_version_id"`
    ContentArtifactID foundation.ID `json:"content_artifact_id"`
    ParseProjectionID foundation.ID `json:"parse_projection_id"`
    ContentHash       string        `json:"content_hash"`
    IngestionAttemptID foundation.ID `json:"ingestion_attempt_id"`
    OccurredAt        time.Time     `json:"occurred_at"`
}
type SourceReadyAppender interface {
    AppendSourceReadyScoped(context.Context, foundation.TransactionScope, SourceReady) error
}

// internal/workflow/application/source_ready_outbox.go
const SourceReadyEventType = "ingestion.source.ready"
type SourceReadyOutboxFact struct {
    EventID foundation.ID
    Ready   ingestiondomain.SourceReady
}
type ScopedSourceReadyOutbox interface {
    ClaimSourceReadyScoped(context.Context, foundation.TransactionScope) (SourceReadyOutboxFact, bool, error)
    PublishSourceReadyScoped(context.Context, foundation.TransactionScope, SourceReadyOutboxFact) error
}
```

`GORMSourceReadyOutbox` 实现上述两个端口。Claim 在调用方 scope 内按 `occurred_at,id` 领取一个 unpublished 事件并 `FOR UPDATE SKIP LOCKED`；不引用 Reindex 的 Delivery/Workspace blocker。Publish 按 Event ID、Workspace 和全部持久 payload 精确 CAS，不能自行提交。消费方在同一 UoW 内完成 Claim → owner provenance gate → `StartScoped` → Publish；被 provenance gate 明确排除的派生资料可直接 Publish，不创建合成 Run。

## 持久性与绑定

- 新 Repository 路径在同一 scope 内读取 Attempt、校验状态/CAS、更新 Attempt；只有 `chunked + passed + parse_projection_id != NULL` 才读取经过 `Source/Version/Artifact/Projection/provenance` 关系核验的身份与原始 `content_hash`，再追加 Outbox。任一步失败，Attempt 更新与 Outbox 一起回滚。解析投影和原始资料已在此前保存，继续保留。
- Event 的 `run_id=NULL`；`event_key` / `idempotency_key` 为 `ingestion.source-ready:v1:<source_version_id>:<parse_projection_id>`，`schema_version=1,event_version=1`。Source Version ID 在全库唯一，Workspace 同时保存在事件与 payload 并精确校验。
- 同一来源 tuple 的不同 Attempt/重投递只保留首次事件；首次 `IngestionAttemptID` 和 `OccurredAt` 保留，后续不会改写。相同 key 但 Source/Workspace/Artifact/hash 变化必须冲突。Consumer 的 processor version 在映射为 Synthesis input 时注入，producer 不依赖 Organizing 或模型配置。
- `OccurredAt` 取持久完成时间，按 UTC 微秒精度保存。队列仅含身份与 hash，不含路径、标题、正文、凭据。
- 提交响应丢失返回错误，不声称回滚。调用方通过既有 Ingestion `Process` 同 key 重试恢复已完成 Attempt；不会重新追加事件或创建新 Attempt。直接 `TransitionAttempt` 仍保持版本 CAS 契约。
- Scan 继续只登记 Source；模型不可用或合成失败只影响消费者自己的持久处理状态。升级不主动补扫历史完成记录。回流排除必须在消费者服务端 provenance gate 完成，producer 不按路径过滤。

## 验证记录

2026-09-09，本工作包实现落盘后：

- PASS：`go test -mod=vendor -timeout=60s ./internal/ingestion/domain ./internal/ingestion/adapter/postgres ./internal/workflow/application ./internal/workflow/adapter/postgres`。
- PASS：`go test -mod=vendor -race -timeout=60s ./internal/ingestion/... ./internal/workflow/application ./internal/workflow/adapter/postgres`。覆盖 Ingestion application/domain/adapter/workspace/http/workflow 与 Workflow application/postgres；尚不含 `integration` build tag 的真实事务。
- PASS：`go vet -mod=vendor ./internal/ingestion/... ./internal/workflow/application ./internal/workflow/adapter/postgres`。
- PASS：`go build -mod=vendor ./internal/ingestion/... ./internal/workflow/application ./internal/workflow/adapter/postgres`。
- PASS：`make persistence-check`，输出为 30 owner / 1817 Go files。
- PASS：本工作包文件 `git diff --check`。
- 首次未执行到业务断言：`go test -mod=vendor -tags=integration -run '^TestGORMSourceReadyTransactions$' -count=1 -timeout=60s -v ./internal/ingestion/adapter/postgres`。Testcontainers 创建独占 pgvector/pg16 容器成功，迁移阶段失败，工厂已停止并清理本次容器。
- 已确认前置原因：`make atlas-migrate-validate` 返回 `checksum mismatch / L95: 00094_generated_authoring_revisions.sql was added`；共享 `atlas.sum` 未覆盖并行新增的 Authoring 迁移。已通知主会话协调，本工作包未修改共享迁移或 checksum。

为不阻塞业务验证，将当前迁移目录复制到临时目录，仅对副本使用 Makefile 锁定的 Atlas 镜像执行 `migrate hash`；Go build overlay 把该副本的 SQL 与 checksum 嵌入测试二进制。此方式仍运行完整 Atlas/River 迁移与真实 Testcontainers 数据库，不跳过或改写任何迁移。验证后比对 94 个 SQL 与当前工作树全部字节相等，无额外 SQL；副本 checksum 的 SHA-256 为 `1b23450b97e0ae33cf03631a2fad2e38ca0ff85250c38ccca84e6e4e6ed71151`。

实际测试 overlay 位于 `/var/folders/fg/bzpd9ft96g976xqf_w4lwbrr0000gn/T/zhixu-source-ready-validation-50n9j_ve/overlay.json`。以下用 `$SOURCE_READY_OVERLAY` 表示该完整路径，临时副本在验证结束后清理：

```sh
go test -mod=vendor -overlay "$SOURCE_READY_OVERLAY" -race -tags=integration -run '^(TestGORMSourceReadyTransactions|TestRepositoryAttemptProjectionLifecycle)$' -count=1 -timeout=60s -v ./internal/ingestion/adapter/postgres
go test -mod=vendor -overlay "$SOURCE_READY_OVERLAY" -race -tags=integration -run '^TestGORMSourceReadyTransactions$' -count=1 -timeout=60s -v ./internal/ingestion/adapter/postgres
```

- 第一条命令中，旧 `TestRepositoryAttemptProjectionLifecycle` PASS（11.55s）；Source-ready 的初始原子回滚/重解析/发布断言 PASS，后续夹具因同库重用 Workspace fingerprint 失败。修正本测试的指纹，使各 Workspace 唯一；生产代码没有因此更改。
- 第二条命令 PASS（测试 13.59s，package 15.474s），9 个子场景全部通过：依赖/live scope、append 后失败双写回滚、重复解析保持首次事实与绑定冲突、精确 publish/CAS/回滚、失败/取消/隔离与非法 Projection 零通知、缺 provenance 拒绝、执行中取消 cause 与双写回滚、提交响应丢失后的原 Attempt exact replay、并发解析去重和 FIFO/SKIP LOCKED。
- 每次工厂只创建独占随机名 pgvector/pg16 容器；上述容器均已由工厂停止和清理，未操作已有容器。
- 最终 `go vet -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres ./internal/workflow/adapter/postgres` PASS，包含修正后的集成测试源码。

`go-review` 已按本次 Go/SQL 范围核对同池 scope、CAS、连接生命周期、并发去重、Row locking、上下文错误和 JSONB 参数载体，没有剩余本工作包缺陷。后续由主会话完成两个生产构造点切换、Synthesis consumer 的 provenance/StartScoped 接线和共享 `atlas.sum` 正式门禁；本工作包没有将这些未执行的整合项标为完成。
