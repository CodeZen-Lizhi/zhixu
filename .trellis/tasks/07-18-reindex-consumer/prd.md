# M6-B Reindex Consumer

## Goal

消费 Safe Writeback 已发布的 `retrieval.revision.reindex_requested`，从指定 Git Commit
安全捕获目标 SourceVersion，复用 Ingestion 生成 Canonical Chunk，构建完整 Workspace
FTS-only Index Snapshot，并且只在索引、结构回归、Writeback cleanup 与原 Workflow 均成功后，
原子切换 Active Index 并把 Proposal/Execution 推进到 `completed`。

## Background

- M6-A 已提供 Embedding/Index Version、Chunk Manifest/Projection、Lexical Builder、Ready、
  Activate/Rollback 与历史 Receipt，但没有 Outbox Consumer、Source Snapshot 或跨模块完成事务。
- Safe Writeback 已原子写入固定 11 字段 v1 Outbox，随后才清理 temp/backup 并完成原 Workflow；
  因此 Outbox 可见不等于 Writeback 已满足完成条件。
- `published_at` 只能表示 River Job 已派发，不能表示业务成功；业务结果必须由独立 Delivery 持有。
- 当前 M6-A Chunk Manifest 不能证明一个完整 Workspace Index 选择了哪些 SourceVersion；
  单文件重建若直接激活，会丢失其他 Source。
- 父任务要求索引或结构回归失败时保留 Git Commit、旧 Active Index 与
  `verifying/index_pending`。旧文档中“回归失败可自动反向 Commit”不适用于本任务，
  自动反向 Commit 仍需后续独立 Proposal/Approval 契约。

## Requirements

### R1. Shared Reindex Contract And Delivery

- 建立 Reindex Outbox v1 的唯一公共解码/编码事实源，严格拒绝缺字段、额外字段、未知版本、
  非 canonical ID/path/hash/Git Commit 和跨绑定 Payload；生产端与消费端不得各自手写匿名契约。
- 新增 `retrieval.reindex_delivery`，独立保存 Outbox、Workspace、Writeback、状态、dispatch/attempt、
  DB-time lease、Source/Ingestion/Index/Activation checkpoint、稳定 failure class/code 和完成时间。
- 新增 append-only `retrieval.reindex_delivery_attempt` 保存每次业务/River delivery generation、
  lease、Ingestion Attempt、结果和稳定失败；retry 不能覆盖或复用已终态失败 Attempt。
- 状态至少覆盖 `pending/dispatched/processing/retry_wait/succeeded/failed/manual_recovery`；
  同一 Consumer + Outbox 只能有一个 Delivery，同一 Workspace 最多一个会继续自动执行的 Delivery。
- `workflow.outbox_event.published_at` 只允许在 Delivery 与 River Job 同事务创建/重放成功后设置。

### R2. Transactional Dispatcher And River Transport

- Dispatcher 使用 `FOR UPDATE SKIP LOCKED` 按 `occurred_at,id` 稳定领取未发布 Reindex Outbox，
  同事务创建/重放 Delivery、通过现有 schema-scoped River Client `InsertTx` 插入 Job、
  标记 Delivery dispatched 并设置 `published_at`。
- Reindex Job Args 只包含 `schema_version/delivery_id/dispatch_no`，不得包含 Workspace、路径、
  Commit、正文、Credential、模型参数或任意标签。
- Reindex 使用独立 Worker/Delivery 状态机，不伪装为已终态的 Safe Writeback Workflow Node，
  不复制 Workflow Run/Node/Attempt 业务事实。
- 业务 retry 由 Delivery 的 `retry_wait/next_attempt_at/dispatch_no` 控制；River 只负责 transport
  redelivery 和进程崩溃恢复。
- Dispatcher 必须有两条互斥查询：未发布 Outbox 创建首次 Delivery/Job并设置 `published_at`；
  到期 `retry_wait` Delivery 创建下一 dispatch generation且不得再次修改 `published_at`。

### R3. Committed Source Capture And Ingestion

- 新增 `CaptureCommittedSourceVersion`，只能从服务端解析的 Workspace、指定 Git Commit 和受控
  相对路径读取确切 Blob；不得调用全量 `ScanWorkspace` 或读取可能已漂移的工作树文件。
- 捕获必须验证 Commit/Path、Git object、SHA-256=`result_hash`、允许扩展名、媒体类型、大小上限、
  取消和 Workspace 边界，再 create-only 写入 Content Artifact 并注册/重放 Source/SourceVersion。
- Commit 后工作树再次变化不得改变捕获结果；响应丢失重放必须返回同一 Artifact/SourceVersion。
- 每次 Delivery retry generation 使用独立稳定的 Ingestion attempt number/idempotency key；
  只接受关联 `ingestion.attempt` 的 `security_status=passed`、`status=chunked` 且
  Parse Projection/Chunk 契约完整的结果。成功 Projection 仍按现有不可变契约复用。

### R4. Full Workspace Snapshot

- 迁移新增不可变 `retrieval.index_manifest_source`，冻结每个 Index 选择的
  Source/SourceVersion/ParseProjection；`index_version` 增加 source manifest hash/count。
- Source Manifest 必须冻结 Workspace 中每个 Source 的选择结果：`included` 行用复合 FK 证明
  同 Workspace、SourceVersion 属于 Source、ParseProjection 属于 SourceVersion；`excluded`
  行保存稳定排除码且不绑定版本。全部行创建后禁止 UPDATE/DELETE。
- 首次构建：目标 Source 固定选择本次 committed SourceVersion 与成功 Ingestion Attempt；
  其他 Source 只在存在当前 Parser ID/Version/Config Hash、Chunk Strategy 与 Schema 契约下
  `status=chunked/security_status=passed` 的 Attempt 时进入 eligible set，并按 SourceVersion
  `captured_at DESC,id DESC` 与 Attempt `started_at DESC,id DESC` 稳定选择。无合格版本的 Source
  显式排除并记录计数，不阻断健康资料；目标 Source 无合格 Attempt 必须失败。
- 增量构建：复制当前 Active 的 Source Manifest，只替换目标 Source，再从新的 Source 选择重新
  推导 Chunk 并集。历史 Active 缺 Source Manifest 时执行全量重建，禁止不完整增量。
- Chunk Manifest 必须恰好等于全部 included ParseProjection 的 active Chunk 并集；共享
  ParseProjection 的 Chunk 按 Chunk ID 去重，多余或缺失 Chunk 都不能 Ready/Active。
- 同 Workspace Reindex 严格串行，后一个 Snapshot 必须基于前一个 Active，不能发生 lost update。
- Snapshot Source/Chunk 使用稳定 keyset 分页、增量 Hash 和分批 COPY，禁止一次性无界加载。
  可配置安全上限默认至少覆盖 10,000 Source/500,000 Chunk；超限返回稳定错误且不产生部分 Index。

### R5. Reindex Processing And Structural Regression

- Delivery checkpoint 顺序为 Capture SourceVersion → Ingestion Attempt/Projection →
  Source/Chunk Snapshot → Begin Index → Build Lexical → Structural Regression → Ready → Complete。
- 每个 checkpoint 必须可查询、可精确重放；进程在任意已提交 checkpoint 后退出，重启不能创建
  第二 SourceVersion、Attempt、Index 或 Activation。
- M6-B 只构建真实 FTS-only Index，并显式保留 `degraded_capabilities=["vector"]`；
  不调用假 Embedding，不创建伪向量。
- `SNAPSHOT_STRUCTURE_V1` 在 Index 仍为 Building 时证明目标 SourceVersion/Result Hash 被选中、
  Source/Chunk Manifest 闭包成立、BuildStatus 足以 Ready；失败时执行受控 `building→failed`，
  不留下可被普通 Activate 绕过的 Ready Index。Search 质量回归属于 M6-C/D。

### R6. Atomic Completion

- 新增 `CompleteReindexTx`，使用数据库时间和固定锁序，不能串联已提交的 `Activate` 与
  Change Control checkpoint。
- 完成前必须验证：Delivery/Event/Commit Mapping/Execution/Proposal 全绑定；Execution 与
  Proposal 均为 verifying；`cleanup_completed_at` 非空；原 Workflow Run/Node 均 succeeded；
  结构回归 passed；目标 Index Ready 且 Manifest/Projection 完整。
- 同一事务追加 Activation Receipt、旧 Active→Retiring、目标 Ready→Active、
  Delivery→Succeeded并设置 completed_at、Execution→Completed并设置 completed_at、
  Proposal→Completed并更新数据库时间/version。
- 事务任一步失败必须全部回滚；提交响应丢失重放不得再次激活、递增版本或完成第二次。
- 数据库使用 `DEFERRABLE INITIALLY DEFERRED` Constraint Trigger 在提交时统一验证
  Delivery/Event/Commit Mapping/Source Result Hash/Regression/cleanup/Workflow Run+Node/Activation/
  Active Index/Execution/Proposal 最终闭包；不得用相互依赖的即时 Trigger。
  直接 SQL 单独完成 Execution/Proposal 必须在提交时失败。Delivery 与 Execution completed 必须有
  `completed_at`；Proposal 只更新 status/version/updated_at，不新增猜测字段。

### R7. Failure, Security And Operations

- Retryable 失败保持旧 Active 和 verifying，按有界退避进入 retry_wait；明确不可重试且无未知
  副作用可 failed；绑定损坏、结果未知或无法安全恢复进入 manual_recovery，不得静默重建。
- 无法严格解码或无法建立合法 Execution/Workspace 外键的 poisoned Outbox 不伪造 Delivery：保持
  unpublished 并触发 fatal/readiness=false 等待人工修复；failed 仅适用于已建立合法 Delivery 后的业务失败。
- manual_recovery 阻止同 Workspace 后续自动 Reindex；普通 failed 允许后续更完整 Snapshot 覆盖。
- 日志、Metrics、Trace 和 Delivery 只记录 bounded identity/status/code，不保存正文、Credential、
  DSN、绝对路径、模型密钥或 Git 命令输出。
- 新增 migration/Dispatcher/Worker/恢复/双 Worker/真实 PostgreSQL+River smoke，并同步产品、
  Retrieval、Workflow、数据库、测试与部署文档。

## Acceptance Criteria

- [x] Migration 空库 Up、重复 Up、空数据 Down→Up 通过；存在 Source Manifest/Delivery 时 Down 返回 `55000`。
- [x] Payload 公共 codec 对 11 字段 v1 精确 round-trip，缺失/额外/未知版本/跨绑定全部拒绝。
- [x] 并发 Dispatcher 只创建一个 Delivery/一个 River Job；InsertTx 或事务失败不设置 `published_at`。
- [x] Job Args 只有 schema/delivery/dispatch，错误版本或额外字段在进入 Application 前拒绝。
- [x] 合法 legacy Reindex Outbox 的 Runtime tuple 全 NULL 时仍可派发，不要求回填 event_key/version 字段。
- [x] Commit 后修改工作树仍捕获指定 Commit Blob；Commit/Path/Hash/大小/取消失败路径明确。
- [x] Capture 响应丢失只产生一个 Content Artifact/SourceVersion；Ingestion 重放只产生一个逻辑 Attempt/Projection。
- [x] Source Manifest 拒绝跨 Workspace、错 SourceVersion/Projection、缺 Source、缺 Chunk、多余 Chunk和 Hash/Count 不一致。
- [x] 首次 Snapshot 对每个 eligible Source 选择最新且匹配当前处理契约的成功 Attempt；增量替换目标 Source 且不丢其他 Source。
- [x] 无合格版本的非目标 Source 被确定性排除并记录计数；目标 Source 无成功 Attempt 时失败。
- [x] 两个不同 Source 的连续 Reindex 串行，第二个 Active 同时包含两次新版本；同 Source 连续更新只保留后一版本。
- [x] Delivery 在 Capture/Ingestion/Snapshot/Begin/Lexical/Regression Passed/Ready 后任一点重启都从持久 checkpoint 恢复。
- [x] cleanup 未完成或原 Workflow Run/Node 未 succeeded 时不得完成或切换 Active。
- [x] retryable Ingestion 失败使用新 Delivery Attempt/幂等 generation 重试，不能永久重放旧 parse_failed。
- [x] 索引/结构回归失败不回滚 Git、不污染旧 Active，Proposal/Execution 保持 verifying。
- [x] 完成事务故障注入保持 Active/Delivery/Execution/Proposal 全部不变；响应丢失重放副作用唯一。
- [x] 直接 SQL 绕过 completion、Safe Writeback response-loss 与 Reindex completion 并发均 fail closed。
- [x] 双 Worker、lease expiry 和旧 owner fence 下最多一个 Processor、一个 Activation、一个 Active。
- [x] 10,000 Source/500,000 Chunk 基线使用分页/分批路径；配置超限不出现 OOM 或部分 Manifest。
- [x] Safe Writeback Outbox→Committed Source→Ingestion→FTS-only Index→Active→Completed 真实 PostgreSQL/River smoke 通过。
- [x] `go test -race`、关键包 `-count=20`、PostgreSQL integration、River smoke、`go vet ./...`、`make test`、go-review、sql-code-review、独立审查和 Trellis check 通过。

## Out Of Scope

- 生产 Embedding/OpenAI-Compatible/Ollama、向量查询、RRF、Dedup、Rerank 和 Search API；归 M6-C/D。
- Search 质量指标、Citation/Evidence API、Agent/RAG、Conversation、Tool Registry 和前端。
- 自动反向 Commit、用户回滚 UI、Knowledge Topic/Claim/Relation/Conflict 与删除/归档 Source 生命周期。
- 全局 HNSW、50 万容量参数、跨 Workspace 调度和多节点分布式锁。
