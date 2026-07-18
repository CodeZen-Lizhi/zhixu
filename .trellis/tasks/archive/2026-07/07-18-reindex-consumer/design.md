# M6-B Reindex Consumer 技术设计

## 1. Architecture Boundary

```mermaid
flowchart LR
    O["Writeback Reindex Outbox"] --> D["Transactional Dispatcher"]
    D --> RD["Reindex Delivery"]
    D --> RJ["River Reindex Job"]
    RJ --> W["Reindex Worker"]
    W --> C["Committed Source Capture"]
    C --> I["Ingestion Process"]
    I --> S["Workspace Snapshot Builder"]
    S --> B["M6-A Index Builder"]
    B --> R["Structural Regression V1"]
    R --> U["CompleteReindexTx"]
    U --> A["Active Index + Completed"]
```

- `internal/retrieval/contract`：只拥有 Reindex Outbox v1 DTO、canonical codec 和绑定校验；
  生产/消费共享，不扩张为跨域杂项 Shared Kernel。
- `internal/workspace/application`：单目标 `CaptureCommittedSourceVersion` 用例。
- `internal/platform/gitcli`：受限 Commit Blob Reader，不暴露任意 Git 参数。
- `internal/platform/filesystem`：create-only committed bytes Content Artifact capture。
- `internal/retrieval/domain`：Delivery、ManifestSource、Snapshot、Regression 与状态不变量。
- `internal/retrieval/application`：Dispatcher/Processor/Checkpoint/Failure 编排，只依赖 Port。
- `internal/retrieval/adapter/postgres`：Delivery、Snapshot Query、Index Build 与跨模块完成 UoW。
- `internal/retrieval/adapter/river`：Reindex Args、Worker/heartbeat；通过既有 Workflow River 包
  新增的受限共享注册/tx insertion seam 复用同一 transport，不访问私有 Client internals。
- `cmd/worker`：复用同一 River Client/Workers/queue，注册 Reindex Worker 并运行 Dispatcher。

Domain/Application 不依赖 pgx、River、Git CLI、文件系统实现或 Change Control Repository。
跨域完成由 PostgreSQL Adapter 实现，pgx transaction 不泄漏到公共接口。

## 2. Migration 00015

### index_version extension

- `source_manifest_hash text NULL CHECK (...)`
- `expected_source_count bigint NULL CHECK (> 0)`
- `source_parser_id/source_parser_version/source_parser_config_hash`
- `source_chunk_strategy_version/source_schema_version`
- legacy M6-A 行七字段都为空；M6-B Snapshot 七字段必须同时存在。

Replay、Chunk union、Ready 闭包和 included Source eligibility 统一引用持久化 Processing Contract。
Active 的处理契约与新请求不同时必须执行全量 eligible rebuild，不允许复制旧契约 Source Manifest。

兼容规则：原有无 Sources 的 `BeginIndex` 仍可创建/读取/Ready/Activate legacy Index，查询返回空
Source Manifest；新请求若 source hash/count 或 Sources 任一侧存在则必须全部存在并精确 replay。
M6-B Completion 只接受 source-bound Index；legacy Active 只能作为全量 eligible rebuild 的触发条件。

### index_manifest_source

```text
index_version_id, workspace_id, source_id, source_version_id NULL,
parse_projection_id NULL, selection_status, exclusion_code NULL, created_at
PK(index_version_id, source_id)
PARTIAL UNIQUE(index_version_id, source_version_id) WHERE selection_status='included'
```

补充 composite unique/FK 证明 Index、Source、SourceVersion、SourceVersionProjection 全部同作用域。
`included` 要求 SourceVersion/ParseProjection 非空且 exclusion_code 为空；`excluded` 要求两者
为空且 exclusion_code 为受限枚举。这样 Source Manifest 同时冻结 included 与 excluded Source，
不只保存不可审计的排除数量。Source Manifest append-only；数据库验证 count/FK/集合闭包。Source Manifest Hash 使用 Domain 的
canonical 算法计算，通过 Store 精确 replay、不可变字段和集合闭包保护；本任务不引入 pgcrypto
或在 SQL 中维护第二套字节编码。

Source Manifest Hash 使用固定 `source-manifest-v1` 前缀并按 Source ID 排序；每行编码
`source_id/selection_status/source_version_id/parse_projection_id/exclusion_code`，字段采用 uint32
big-endian 长度前缀 + UTF-8 bytes，NULL 编码为空长度。included/excluded 或任一绑定变化必须改变 Hash。

### reindex_delivery

关键字段：

- identity：`id/consumer_name/outbox_event_id/workspace_id/writeback_execution_id`
- state：`status/dispatch_no/attempt_no/current_attempt_id/version/next_attempt_at`
- checkpoints：`source_version_id/parse_projection_id/index_version_id/activation_id`
- snapshot observation：`excluded_source_count`（必须等于 Source Manifest excluded 行数）
- regression：`regression_code/regression_hash/regression_passed_at`
- failure：`failure_class/error_kind/error_code/error_summary/manual_recovery_required`
- time：`created_at/updated_at/completed_at`

约束：Consumer+Outbox 唯一；一个 Workspace 最多一个 `pending/dispatched/processing/retry_wait/manual_recovery`；
checkpoint 随状态单调增加且不可改写；terminal completion/failure 字段一致。

### reindex_delivery_attempt

append-only 保存 `delivery_id/attempt_no/dispatch_no/river_job_id/river_attempt/delivery_key/lease_owner/
lease_until/status/ingestion_attempt_id/failure fields/started_at/heartbeat_at/ended_at`。同 Delivery
AttemptNo 唯一；retry generation 使用新的 Ingestion 幂等键，成功后 Delivery 才冻结成功
ParseProjection。旧 owner 只能结束自己仍有效的 attempt。

### completion guards

- completed 必须设置 `completed_at`。
- 在 Delivery、Execution、Proposal 上使用 `DEFERRABLE INITIALLY DEFERRED` Constraint Trigger，
  提交时由单一 completion invariant 验证 Delivery 与 Outbox/Payload/Proposal Commit/Execution
  全绑定、SourceVersion content hash=payload result hash、Regression code/hash/passed_at 与
  Source/Chunk/Projection 闭包、cleanup、Workflow Run/Node succeeded、Activation、Active Index、
  Delivery succeeded、Execution/Proposal completed。Trigger 只读最终状态，不执行 `FOR UPDATE`
  或改变锁序。
- Down 锁表并在任何 Source Manifest/Delivery 存在时 SQLSTATE `55000`；不删除 M6-A 数据。

## 3. Shared Payload Contract

`contract.RequestV1` 固定 11 字段，并提供：

- `DecodeStrict([]byte) (RequestV1,error)`：`DisallowUnknownFields`、单对象、EOF、schema=1。
- `EncodeCanonical(RequestV1) ([]byte,error)`：固定字段顺序与 lower-case hash/commit。
- `ValidateBinding(...)`：Workspace/Run/Node/Proposal/Revision/Approval/Execution/Path/Hash/Commit。

Change Control `buildPublishWriteback` 与数据库测试改用同一 contract；Consumer 不读取 nullable
Runtime `event_key/schema_version/event_version`，只信已验证的 event type + payload v1。

## 4. Dispatch And Retry

Dispatcher 每轮执行两条互斥短事务路径：

1. First dispatch：选择最早且未发布、Workspace 无阻塞 Delivery 的 Reindex Outbox，
   `FOR UPDATE SKIP LOCKED`；创建 Delivery/dispatch 1、InsertTx、Delivery→dispatched、
   `published_at=CURRENT_TIMESTAMP`。
2. Retry dispatch：按 `next_attempt_at,id` 选择到期 `retry_wait` Delivery，`FOR UPDATE SKIP LOCKED`；
   递增 dispatch_no、InsertTx、Delivery→dispatched，不读取或修改 `published_at`。

retryable 业务失败在持锁事务内 `processing→retry_wait` 并计算有界 next_attempt_at；
Dispatcher 到期领取时才原子递增 dispatch_no 并插入新 Job。崩溃导致的 processing lease expiry 由同一
Job 的 River rescue/retry reclaim，不提前创建第二代业务 Job。

Worker 返回语义：业务归约事务已确认提交为 `retry_wait/failed/manual_recovery/succeeded` 后，
当前 River Job 必须返回 nil；只有 Claim/Heartbeat/归约事务未确认提交时才向 River 返回 error，
让同一 dispatch transport redelivery。新 generation 后到达的旧 dispatch 只做 stale no-op。

现有 Worker 已有 insert-only Client 与 runtime Client：Dispatcher 复用 insert-only Client，
Reindex Worker 注册到现有 runtime Workers，不新增第三个 Client或第二条 queue。实施前先给
Workflow River 包增加不依赖 Retrieval 的受限通用 `AddWorkerSafely` 与 tx typed job insertion seam。

严格解码失败、跨绑定或找不到合法 Writeback Execution 的 poisoned Outbox 不创建伪 Delivery，也不
放宽非空 FK：保持 `published_at=NULL`，以 `REINDEX_OUTBOX_CONTRACT_INVALID` 触发 fatal/readiness=false
等待人工修复。R7 的普通 failed 仅适用于已建立合法 Delivery 后、无未知副作用的不可重试业务输入。

### Dispatcher Runner

- 配置：有界 poll interval、batch size 与 error backoff；默认值进入现有 Config。
- 启动：构造依赖/注册 Workers → 启动 Dispatcher runner → 启动 River runtime Client → ready。
- 停止：先把 readiness 置 false并停止新领取，等待当前短事务结束，再停止 River。
- DB 瞬时错误记录 bounded metric并退避；稳定 schema/invariant 错误进入 fatal channel，触发现有
  Worker emergency shutdown。Runner 未启动或意外退出时 readiness=false。

## 5. Committed Source Capture

```go
type CommittedBlobReader interface {
    ReadCommittedBlob(context.Context, workspaceID, commit, relativePath) (CommittedBlob, error)
}

type CommittedContentStore interface {
    CaptureCommitted(context.Context, workspaceRoot, relativePath string, content []byte, expectedHash string) (ContentCapture, error)
}
```

Git Adapter 仅执行固定 `cat-file blob <commit>:<path>`，限制输出大小并校验 object/path；
Workspace Application 计算 SHA-256、媒体类型和 byte size，验证 payload result hash 后写入
`.knowledge/sources/<hash>`。Repository 继续通过 Source location 与 Source+Hash 幂等复用。

工作树不参与读取，因此 Commit 后的用户编辑不会污染本次 SourceVersion。

## 6. Snapshot Algorithm

Source Manifest canonical key 为 `source_id/source_version_id/parse_projection_id`，按 Source ID
排序计算 SHA-256。Chunk Manifest 由选中 ParseProjection 的 active Chunk 并集推导。

### First build

1. 目标 Source 选择本次 committed + chunked SourceVersion。
2. 每个其他 Source 只从关联成功 Attempt 中选择：Attempt 必须匹配当前 Parser ID/Version/
   Config Hash、Chunk Strategy、Schema 且 `status=chunked/security_status=passed`。
3. SourceVersion 按 `captured_at DESC,id DESC`，同版本 Attempt 按 `started_at DESC,id DESC` 稳定
   选择并冻结其 ParseProjection。有合格 Attempt 的 Source 写 included 行；无合格版本 Source
   写 excluded 行及 `NO_CURRENT_SUCCESSFUL_PROJECTION`，Delivery 记录并校验 excluded count；
   目标 Source 无合格 Attempt 则失败。

### Incremental build

1. 读取当前 Active 的完整 included/excluded Source Manifest。
2. 只重新评估并替换目标 Source 选择；其他 included/excluded 冻结事实保持不变。
3. 从新 Source Manifest 重新查询并规范化 Chunk 并集。
4. Active 缺 Source Manifest 时执行 First build；若 legacy Chunk 与全量重建事实冲突则 fail closed。

Snapshot ref 使用 `reindex-v1:<outbox_event_id>`，不伪装成所有 Source 均已被 Git Tree 逐一证明。

### Bounded snapshot materialization

- Source 与 Chunk 查询都使用稳定 keyset cursor 和配置化 page size；不使用无界 `SELECT`。
- 在同一 repeatable-read + Workspace advisory-lock 事务中第一遍分页计算 canonical hash/count，
  插入 Index 后第二遍分页 COPY Source/Chunk Manifest，避免一次性持有 500,000 Chunk。
- 默认安全上限至少为产品基线 10,000 Source/500,000 Chunk，并可配置提高；超限返回
  `REINDEX_SNAPSHOT_CAPACITY_EXCEEDED`，事务不留下 Index/Manifest。
- M6-A 原有 bounded `BeginIndex` 保持兼容；M6-B 新增 `BeginWorkspaceSnapshot` Store Port 复用
  Index/Manifest tx helper，不把全量 Snapshot 退化为巨型 Slice API。

## 7. Processor Checkpoints

```text
dispatched
  -> processing
  -> source_captured
  -> ingested
  -> index_building
  -> regression_passed
  -> index_ready
  -> succeeded
```

数据库状态仍使用受限 Delivery status，细阶段由不可逆 checkpoint 字段推导。Processor 每次
从 Delivery 恢复：存在 SourceVersion 就不重读/重注册；存在终态 Ingestion Attempt 就恢复
Projection；存在 Index 就按状态调用 Build/查询；Lexical 完成后在 Building 状态执行结构回归，
passed 后才 Ready；最后进入 UoW。

所有领域幂等键从 Outbox Event ID + 操作 label 派生，不能跨领域复用一个模糊 key。Ingestion
幂等 generation 使用 `dispatch_no`（业务 retry generation），不使用 River job attempt 或
Delivery AttemptNo；同一 dispatch 的 transport redelivery 继续原 Ingestion Attempt，只有进入
retry_wait 后的新 dispatch 创建新 Ingestion Attempt。成功 Projection 和 SourceVersion 继续复用。

## 8. Structural Regression V1

M6-B 不实现 Search 质量评测。`SNAPSHOT_STRUCTURE_V1` 在 Building 状态真实验证：

- Delivery 目标 Source/SourceVersion/Result Hash 在 Source Manifest 中精确存在。
- Source Manifest hash/count 与持久化相同。
- Chunk Manifest 等于选中 ParseProjection active Chunk 并集。
- BuildStatus manifest/projection/lexical counts 足以进入 Ready，Index=`building`。
- FTS-only vector 状态全部 disabled 且 degraded vector 被显式声明。

结果 Hash 使用固定 `snapshot-regression-v1` 前缀；Source/Chunk 记录先稳定排序，每个字符串字段
按 uint32 big-endian 长度前缀 + UTF-8 bytes 编码，整数用 big-endian int64，空值长度为零。
上述 canonical bytes 做 SHA-256 并持久化；顺序变化不得改变 Hash，字段变化必须改变 Hash。
失败时将 Building Index 受控转为 failed；只有 passed checkpoint 才允许 Ready。

## 9. CompleteReindexTx

为避免与现有 Change Control 锁序冲突，固定顺序：

1. Workspace advisory transaction lock。
2. Proposal `FOR UPDATE`。
3. Writeback Execution `FOR UPDATE`。
4. Delivery `FOR UPDATE`。
5. 当前 Delivery Attempt `FOR UPDATE`。
6. 目标/当前 Active Index 按 ID 稳定顺序 `FOR UPDATE`。
7. 核验 Outbox、Proposal Commit、Workflow Run/Node。

完成先分两支：先查询 exact succeeded Delivery + Activation Receipt + Active Index + completed
Execution/Proposal，完整绑定一致时不要求 lease，直接重建历史结果。只有首次完成路径才要求
Delivery=`processing`、Delivery version 与当前 `delivery_attempt_id/attempt_no/lease_owner/lease_until`
有效。随后验证 cleanup、Run/Node succeeded、Proposal/Execution verifying、Regression passed、
目标 Ready，并复用 M6-A 包内 `activateTx`：先写
Activation Receipt，再旧 Active→Retiring、目标→Active；然后 Delivery succeeded、Execution
completed（设置 completed_at）、Proposal completed（只更新 status/version/updated_at）。
所有行锁只由 UoW 显式取得；deferred invariant trigger 不反向加锁。提交响应丢失按
Delivery/Activation Receipt 重建历史结果。

## 10. Failure And Compatibility

- Index/Regression 失败不创建 Activation，不回滚 Git；旧 Active 继续服务。
- 依赖短暂失败进入 retry_wait；非法 Payload/不支持格式且无副作用可 failed；未知 Commit/DB
  binding、checkpoint 冲突或不确定完成进入 manual_recovery，并阻塞 Workspace 后续自动 Reindex。
- 停止 Dispatcher 阻止新 Job；已派发 Delivery 由 River + lease 恢复。
- M6-B 未启用时既有 Outbox 继续 pending；旧 Safe Writeback 行为不变。
- M6-C/D 可在现有 Delivery/Source Manifest 之上增加 Embedding 与 Search，不改变完成事实语义。
