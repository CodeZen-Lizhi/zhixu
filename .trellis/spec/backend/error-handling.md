# 后端错误处理规范

## 适用范围

适用于领域模块、Application Command/Query、HTTP/SSE 边界、Worker/Workflow、数据库、文件、Git、Parser、Model 和 Tool Adapter。当前没有可运行实现，规范中的代码落点和验证命令将在 M1 代码基线落地后校验。

## 已确认事实

- 统一错误分类为：`InvalidInput`、`NotFound`、`VersionConflict`、`PermissionDenied`、`DependencyUnavailable`、`RetryableFailure`、`NonRetryableFailure`、`ConsistencyViolation`、`ManualRecoveryRequired`（依据 [`interfaces-and-adapters.md`](../../../docs/architecture/interfaces-and-adapters.md) 第 12 节）。
- Adapter 必须把 SDK、命令行和数据库原始错误映射为领域错误；外部错误类型不得泄漏到领域层。
- API 使用稳定 `error_code`、可理解的 `message`、`retryable`、可选 `workflow_run_id` 和结构化 `details`；message 可本地化，错误码用于程序判断和日志检索（依据 [`api-and-events.md`](../../../docs/architecture/api-and-events.md) 第 7 节）。
- 预计超过 3 秒的操作必须返回异步 Workflow Run；客户端不能把创建任务的 202 当作业务已成功完成。
- 版本/一致性冲突必须包含当前版本、期望版本、冲突类型和可行解决动作；Proposal 进入 `NEEDS_REVISION` 或等价状态，不得静默覆盖用户修改。
- 不可确认权限、引用失效、Git/DB 不一致或安全检查异常时必须拒绝、隔离或进入只读/人工恢复，不得返回假成功（依据 [`security.md`](../../../docs/architecture/security.md) 第 16 节）。

## 目标代码落点（M1 起）

- 领域错误类型和错误码：shared kernel 或各领域模块的稳定接口包；最终包路径由 M1 代码确认。
- Application 层：命令/查询错误到 HTTP/SSE/Workflow 结果的统一映射。
- `cmd/api` 或 `internal/app`：Problem Details 响应、HTTP 状态码、`request_id`/`workflow_run_id` 关联。
- `internal/workflow`：Retryable/NonRetryable/Manual 分类、退避、租约和补偿状态。
- `internal/platform/*`：pgx、Git CLI、文件、解析器和模型原始错误映射。
- `internal/audit` 与 observability 边界：安全阻断、审批、写回、回滚和人工恢复的审计事件。

## 错误分类与传播

1. 在最靠近根因的 Adapter 处保留可诊断上下文并映射稳定类别；使用 Go error wrapping 保持 `errors.Is/As` 可判定，禁止直接返回厂商/命令行错误。
2. Application 层将领域错误转为契约错误；只暴露稳定错误码、用户可理解的安全信息和最小结构化详情。
3. Worker 按错误类别决定重试：超时、限流、暂时网络或锁冲突通常可重试；Schema、权限、非法状态和证据缺失不可自动重试；一致性损坏、补偿失败进入人工恢复。具体策略遵循 [`workflow-engine.md`](../../../docs/architecture/workflow-engine.md) 第 9 节。
4. Retryable 错误必须有最大次数、指数退避、抖动和必要的 `Retry-After`；未知副作用结果不得盲目重试，应先查询幂等记录或进入人工恢复。
5. 同一错误在 API、Workflow、日志、Trace 和 Audit 中共享 error code；SSE 只推送摘要，客户端重新查询资源状态，SSE 本身不是事实源。
6. 每个失败路径都要保留状态、错误类别、可重试性和最后失败节点；不要通过捕获异常后标记成功来隐藏失败。

## API 与 SSE 响应

API 错误响应至少包含：

```json
{
  "error_code": "CONSISTENCY_CONFLICT",
  "message": "目标文件在审批后发生变化",
  "retryable": false,
  "workflow_run_id": "optional-id",
  "details": {}
}
```

上面的字段和语义来自 API 架构文档；`workflow_run_id` 仅在已有异步运行实例时返回，示例中的值不是项目固定 ID。

- 输入校验失败、未找到、权限拒绝、冲突和依赖不可用必须映射到稳定 HTTP 状态；具体状态码由 OpenAPI 在 M1 锁定，不能在各 Handler 中自行发明。
- `details` 只放帮助客户端恢复的字段（如 `current_version`、`expected_version`、`conflict_type`、`resolution_actions`）；不放 Secret、完整 Prompt、内部堆栈或任意文件路径。
- SSE 事件带 `id`、`type`、`occurred_at`、`workspace_id`、`resource_ref`、`payload_summary`。断线后通过 Last-Event-ID 补发保留窗口或改为重新查询。

## 禁止模式

- `panic` 或空 `catch`/空 error 分支处理普通业务输入、外部依赖和用户可恢复错误。
- 吞掉错误、只记录日志后返回零值，或将失败响应包装成成功/空数据。
- 把完整堆栈、SQL、Authorization、模型原始响应或 Workspace 绝对路径返回给客户端。
- 让模型文本决定权限、重试或是否批准；这些判断必须在服务端 Workflow/Tool/Change Control 执行。
- 对未知副作用结果自动重复执行文件写入、Git Commit、Tool Call 或审批。
- 为了兼容旧客户端在 Handler 中增加静默 fallback；契约变化应通过版本化字段、错误码和迁移策略明确处理。

## 验证方式

### M0 当前（仅规范）

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

### M1 代码落地后

- 单元测试覆盖错误分类、`errors.Is/As`、Error Mapping、Change Hash/Version Conflict 和状态机非法转移。
- API Contract Test 覆盖输入校验、Problem Details 字段、稳定 error code、Idempotency、ETag/Version 冲突和 202 异步响应。
- Workflow 集成测试覆盖 Crash、租约过期、重复投递、Retryable/NonRetryable/Manual 分类、取消和补偿失败。
- 安全负测确认权限失败、路径越界、SSRF、Prompt Injection 和 Secret Store 不可用不会产生成功写入。

## 待 M1 代码验证

- 领域错误类型、错误码常量和 HTTP 状态码的实际包路径。
- 是否采用 RFC 9457 `type/title` 兼容字段；当前产品契约只锁定 `error_code/message/retryable/details`。
- 结构化错误的序列化、SSE 失败事件和多语言 message 策略。
- 全量错误码清单、客户端生成类型和 OpenAPI Breaking Gate。

## M5 Ingestion API Code-Spec

### 1. Scope / Trigger

- Trigger：Source Version → Parser → Projection → Chunk 横跨文件系统、数据库、Application、API 和 Workflow Node。
- 目标：同一错误码、幂等语义和恢复状态在 API、Workflow、日志与数据库中保持一致。

### 2. Signatures

```text
POST /api/v1/source-versions/{source_version_id}/ingestion-attempts
Header: Idempotency-Key (1..128)
Body: { workflow_run_id?: uuid, attempt_number: positive integer }
```

```go
Process(context.Context, application.ProcessRequest) (application.ProcessResult, error)
```

### 3. Contracts

- 首次 Attempt 完成返回 201；同一幂等键重放返回 200，并只返回已持久化 Attempt/Projection 摘要。
- `chunked` 仅表示解析/分块投影完成，不表示 `indexed` 或 `ready`。
- `quarantined` 固定为 `status=validating + security_status=quarantined`，错误码必须保留在 Attempt。
- API 不返回原始文件全文；`chunk_count`、`warning_count` 和稳定 ID 足够驱动后续查询。

### 4. Validation & Error Matrix

| 条件 | Attempt 状态 | API 错误 |
|---|---|---|
| Idempotency-Key 缺失/超长 | 不创建 | `400 IDEMPOTENCY_KEY_REQUIRED` |
| MIME、UTF-8、大小不合法 | `parse_failed` | `400`，稳定 `error_code` |
| NUL/伪二进制 | `validating/quarantined` | `403 SOURCE_BINARY_CONTENT` |
| Parser/Projection 可重试依赖失败 | `parse_failed + retryable=true` | `503` |
| 请求取消/超时 | `cancelled + retryable=false` | `503/非重试` |
| 已有 `parsed/chunking/chunked` Attempt | 从 Projection 检查点恢复 | 不重新执行 Parser |

### 5. Good / Base / Bad Cases

- Good：同一 Artifact + Parser/Config/Schema + Chunk Strategy 重放返回相同 Projection/Chunk ID。
- Base：同一 Parse Projection 使用新 Chunk Strategy 时只新建对应 Strategy/Schema 的 Chunk，不混合旧策略。
- Bad：把 `retry_wait` 写入 Attempt、把解析失败返回 200、或从已完成 Projection 重新解析后覆盖状态。

### 6. Tests Required

- Handler：Idempotency-Key、UUID、JSON、201/200、Problem Details。
- Application：quarantine、retryable error 保真、取消、BOM warning 去重、parsed/chunking/chunked 恢复。
- PostgreSQL：空库/重复迁移、Attempt 乐观锁、Projection/Span/Chunk 不可变、策略隔离和版本交叉约束。
- Compose/API smoke：扫描真实文件后首次摄取成功，第二次请求复用 Attempt/Projection。

### M5 Write Authorization Error Matrix

| 条件 | 稳定错误码 | 分类 |
|---|---|---|
| Proposal 未批准、已拒绝或已 Needs Revision | `WRITE_AUTHORIZATION_APPROVAL_REQUIRED` | PermissionDenied |
| 当前目标基线变化 | `TARGET_BASE_HASH_CONFLICT` | VersionConflict，并标记 Needs Revision |
| Workflow Run/Node 不存在或跨 Workspace | `WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID` | ConsistencyViolation |
| 授权字段/Change Hash 不匹配 | `WRITE_AUTHORIZATION_BINDING_CONFLICT` | VersionConflict |
| 授权过期或已撤销 | `WRITE_AUTHORIZATION_EXPIRED` / `WRITE_AUTHORIZATION_REVOKED` | PermissionDenied |
| 同一幂等键绑定不同授权 | `WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT` | VersionConflict |

Write Authorization 消费成功只确认数据库内的 Proposal/Approval/Revision/Workflow 授权事实，不代表文件或 Git 写回成功。当前文件基线冲突可以在完整绑定校验后提前返回 `TARGET_BASE_HASH_CONFLICT`；Safe Writeback 仍必须在原子替换点重新执行 CAS，不能把消费前读取当作跨存储事务保证。

### M5 Safe Writeback Persistence Error Matrix

| 条件 | 稳定错误码 | 分类 | 是否重试 |
|---|---|---|---|
| Create/Checkpoint 字段、路径或哈希非法 | `WRITEBACK_INVALID` | InvalidInput | 否 |
| 同幂等键、Proposal Revision 或 Authorization 身份冲突 | `WRITEBACK_IDENTITY_CONFLICT` | VersionConflict | 否，必须新请求/重新审批 |
| Execution 乐观锁冲突 | `WRITEBACK_VERSION_CONFLICT` | VersionConflict | 先重读检查点 |
| 非法 Execution 状态迁移 | `WRITEBACK_STATUS_CONFLICT` | VersionConflict | 否 |
| Proposal 状态与 Execution 检查点不一致 | `WRITEBACK_PROPOSAL_STATE_CONFLICT` | VersionConflict | 先人工核对事实源 |
| Mapping、Outbox 或精确 Payload 绑定冲突 | `WRITEBACK_PUBLISH_BINDING_CONFLICT` | ConsistencyViolation | 否，不重复 Commit |
| 已 verifying 但 Mapping/Outbox 缺失 | `WRITEBACK_OUTBOX_MISSING` / 查询错误 | ConsistencyViolation | 进入恢复，不返回假成功 |
| 序列化、死锁或连接瞬断 | 当前操作稳定码 | RetryableFailure | 是，先查 Execution/Mapping 幂等记录 |

`git_committed` 之后的数据库失败不能触发文件恢复；否则会让 Git Commit 与文件系统分叉。恢复路径必须保留 Commit，查询 Trailer/Execution 后补 Mapping/Outbox。

### M5 Safe Writeback Filesystem Error Matrix

#### 1. Scope / Trigger

- Trigger：批准正文从数据库事实进入本地 Workspace 文件副作用边界，横跨 Change Control Domain、LocalFS、Markdown Parser 和后续 Git Saga。
- 目标：文件身份、Base/Result/Change Hash、受控 locator、错误分类和恢复证据在 Adapter 与 Workflow 间保持一致；任何未知副作用结果不得自动重试或清理 backup。

#### 2. Signatures

```go
type WorkspaceStore interface {
    AcquireTarget(context.Context, foundation.ID, string) (TargetLock, error)
}

type TargetLock interface {
    Prepare(context.Context, PrepareWrite) (PreparedWrite, error)
    CommitCAS(context.Context, PreparedWrite) (AppliedWrite, error)
    RestoreCAS(context.Context, AppliedWrite) (RestoreResult, error)
    Cleanup(context.Context, AppliedWrite) error
    Close() error
}
```

`PrepareWrite` 必须绑定 `ExecutionID + ExpectedBaseHash + ApprovedChangeHash + Content`；`PreparedWrite/AppliedWrite` 只持久化 Workspace 相对 temp/backup locator、hash、byte size、mode 和 lock token，不暴露绝对路径或正文。

#### 3. Contracts

- 目标必须是服务端 Workspace ID 下现存、规范相对 `.md/.markdown` 普通文件；父链/目标不能是 symlink，目标必须与 Workspace 根同 device 且归当前进程 owner。
- `.knowledge/locks/<sha256(device:inode)>.lock` 使用 `0600 + flock`；释放只 unlock/close，不 unlink。
- temp/backup 与目标同目录，名称匹配 `.zhixu-writeback-*`，创建使用 `O_EXCL|0600`；Git local exclude 同时包含 `/.knowledge/` 与 `**/.zhixu-writeback-*`。
- `Prepare` 必须通过正式 Markdown Parser、10 MiB 上限、Result Hash 和 `ComputeChangeHash(target,base,content)` 校验，并保留目标 POSIX mode。
- `CommitCAS` 必须重新验证 inode/Base Hash、独立复制并 sync backup、rename、sync 父目录、复核 Result Hash/size/mode。
- `RestoreCAS` 只有当前目标为 Result Hash 时才能恢复；当前已为 Base 返回 replay，其他内容一律不覆盖。
- rename 后结果未知时返回非空 `AppliedWrite + ManualRecoveryRequired`，保留 backup；`RestoreCAS` 证明恢复前禁止 Cleanup。

#### 4. Validation & Error Matrix

| 条件 | 稳定错误码 | 分类 | 是否重试/恢复 |
|---|---|---|---|
| Workspace/目标路径、扩展名或大小非法 | `WRITEBACK_TARGET_INVALID` / `WRITEBACK_TARGET_TOO_LARGE` | InvalidInput | 否 |
| 父/目标 symlink、特殊文件、跨 device、owner 不匹配 | `WRITEBACK_TARGET_PARENT_UNSAFE` / `WRITEBACK_TARGET_UNSAFE` / `WRITEBACK_TARGET_CROSS_DEVICE` / `WRITEBACK_TARGET_OWNER_INVALID` | PermissionDenied | 否，修复文件边界后重试 |
| 同一 device/inode 锁等待超时 | `WRITEBACK_TARGET_LOCK_TIMEOUT` | RetryableFailure | 是，退避并重新 Acquire |
| 锁等待被调用方取消 | `WRITEBACK_TARGET_LOCK_CANCELLED` | NonRetryableFailure | 否，终止当前节点 |
| Markdown、UTF-8、二进制、正文大小或 Change Hash 不合法 | Parser `SOURCE_*` / `WRITEBACK_PREPARE_INVALID` | InvalidInput / VersionConflict | 否，重新生成 Proposal Revision |
| 最终目标 inode 或 Base Hash 已变化 | `TARGET_IDENTITY_CONFLICT` / `TARGET_BASE_HASH_CONFLICT` | VersionConflict | 否，进入 Needs Revision |
| temp/backup 在 rename 前写入、mode 或 sync 失败 | `WRITEBACK_TEMP_*` / `WRITEBACK_BACKUP_*` | DependencyUnavailable | 正式文件未替换；清理后按 Workflow 策略重试 |
| rename、父目录 sync、结果复核无法证明 | `WRITEBACK_RENAME_RESULT_UNKNOWN` / `WRITEBACK_PARENT_SYNC_RESULT_UNKNOWN` / `WRITEBACK_RESULT_VERIFY_UNKNOWN` | ManualRecoveryRequired | 禁止盲目重试；先用 Applied 摘要执行 Restore/人工核对 |
| Restore 时目标已被用户再次编辑 | `WRITEBACK_RESTORE_CONFLICT` | VersionConflict | 不覆盖用户内容，人工处理 |
| backup/temp locator、identity、hash 或 mode 被篡改 | `WRITEBACK_MANAGED_FILE_*` / `WRITEBACK_CLEANUP_*` | ConsistencyViolation / PermissionDenied | 保留现场，不删除未知文件 |

Filesystem Adapter 只负责文件副作用与补偿，不消费 Approval/Git Authorization，也不创建 Git Commit。`ManualRecoveryRequired` 必须保留 Base/Result Hash、受控 locator 和 backup；只有 Restore 已证明目标回到 Base 或完整 Saga 成功后才允许 Cleanup。

#### 5. Good / Base / Bad Cases

- Good：同一 inode 的大小写、Unicode 或 hardlink alias 竞争同一 flock；最终 Base Hash 未变时生成独立 backup，原子替换并验证 Result Hash。
- Base：Commit 结果未知但目标仍为 Base，`RestoreCAS` 返回 replay，随后才清理 temp/backup；目标为 Result 时恢复并验证 Base。
- Bad：把审批时的 Hash 读取当作最终 CAS、用 path string 作为锁 key、使用 hardlink backup、rename 报错后盲目重试或在 unknown 状态删除 backup。

#### 6. Tests Required

- Domain：路径/扩展名、10 MiB、Change Hash、Result Hash、locator 父目录、mode/token 和 Prepared→Applied 不可变绑定。
- Parser Adapter：有效 Markdown、空内容、非法 UTF-8、NUL/控制字节、取消、nil/unsupported Parser 和输入不可变。
- Filesystem Contract：父/目标 symlink、目录/FIFO/socket、跨 device/owner、mode、Base/identity conflict、独立 backup、restore replay、用户后续编辑、backup/temp 篡改和 Cleanup 幂等。
- Fault：temp sync、backup sync、rename 已执行但报错、parent sync、result verify；断言 rename 前正式文件不变，unknown 时 `errors.Is(ErrWritebackManualRecoveryRequired)` 且 Applied 摘要有效。
- Concurrency：helper subprocess 验证跨进程 flock、取消/超时、进程退出释放、hardlink 同 inode 和不同目标并行；锁用例执行 `-race -count=20`。

#### 7. Wrong vs Correct

```text
Wrong: Approval 时 CurrentHash == Base → 直接 os.WriteFile(target) → Git Commit。
Correct: Acquire inode lock → Prepare/parser/temp fsync → final inode+Base CAS → independent backup → rename+dir sync+Result verify。

Wrong: rename 返回 error → 自动再次 Commit 或 Cleanup backup。
Correct: 返回 AppliedWrite + ManualRecoveryRequired → 先按 Base/Result Hash 执行 RestoreCAS/人工核对 → 证明状态后 Cleanup。
```

### 7. Wrong vs Correct

```text
Wrong: Parse Projection 已存在 → 直接返回所有 Chunk。
Correct: Parse Projection 可共享，但查询/写入 Chunk 必须带 chunk_strategy_version + schema_version。
```
