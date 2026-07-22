# 后端错误处理规范

## 适用范围

适用于领域模块、Application Command/Query、HTTP/SSE 边界、Worker/Workflow、数据库、文件、Git、Parser、
Model 和 Tool Adapter。仓库已有可运行实现；M6-D 的 Search/Cursor/Evidence Problem 契约以当前代码、
OpenAPI 和实际测试为准，未执行的全仓门禁不得仅凭规范视为通过。

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
7. Adapter 可以用安全 `foundation.Error` 隐藏底层消息，但 caller cancel/deadline 必须作为 Cause 保留，
   使 `errors.Is(err, context.Canceled|DeadlineExceeded)` 成立；否则 Worker 可能把停机取消持久归约为业务失败。

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

### M6-04 Conversation HTTP 契约

- Conversation 命令只接受单个严格 `application/json` 文档；未知字段、多 JSON、非法 Unicode、错误媒体类型和缺失幂等键在进入 Application 前失败。
- M10 身份上下文落地前，Conversation detail、Turn 与 Answer Query 显式携带 `workspace_id`；Repository 必须以 Workspace 联合条件查询，跨 Workspace 与不存在统一 NotFound。
- Answer ETag 绑定 Answer 与 Workflow 两个版本，因为 pending Answer 的 Workflow 状态可以在 Answer 版本不变时推进；Conversation ETag 只绑定自身版本。
- 持久 RAG result 内 Citation 不含可导航链接；HTTP 外层 `Answer.citations` 由服务端稳定身份生成 `href`，OpenAPI 分别使用 `RAGResultCitation` 与 `AnswerCitation`，禁止混为同一 wire schema。
- Feedback 为 append-only 幂等事实；首次返回 201、精确重放返回 200，同键异请求返回 409，任何 Feedback 都不得修改 Answer 或正式 Knowledge。

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
    ResumeTarget(context.Context, foundation.ID, string, ResumeWrite) (TargetLock, PreparedWrite, *AppliedWrite, error)
}

type TargetLock interface {
    Prepare(context.Context, PrepareWrite) (PreparedWrite, error)
    CommitCAS(context.Context, PreparedWrite) (AppliedWrite, error)
    RestoreCAS(context.Context, AppliedWrite) (RestoreResult, error)
    Cleanup(context.Context, AppliedWrite) error
    Close() error
}
```

`PrepareWrite` 必须绑定 `ExecutionID + ExpectedBaseHash + ApprovedChangeHash + Content`；`PreparedWrite/AppliedWrite` 只持久化 Workspace 相对 temp/backup locator、hash、byte size、mode，以及原始目标 `LockToken`、Result temp/应用后目标 `ResultLockToken`、Base backup `BackupLockToken`，不暴露裸 device/inode、绝对路径或正文。

#### 3. Contracts

- 目标必须是服务端 Workspace ID 下现存、规范相对 `.md/.markdown` 普通文件；父链/目标不能是 symlink，目标必须与 Workspace 根同 device 且归当前进程 owner。
- LocalFS 在解析 Workspace 根目录前必须校验 `WorkspaceStore.GetWorkspaceByID` 返回对象的 `ID` 与请求 `workspace_id` 精确一致；不一致是内部事实源绑定错误，返回 `PROPOSAL_WORKSPACE_BINDING_INVALID`/`ConsistencyViolation`，不得继续读取或计算 Hash。
- `.knowledge/locks/<sha256(device:inode)>.lock` 使用 `0600 + flock`；释放只 unlock/close，不 unlink。
- temp/backup 与目标同目录，名称匹配 `.zhixu-writeback-*`，创建使用 `O_EXCL|0600`；Git local exclude 同时包含 `/.knowledge/` 与 `**/.zhixu-writeback-*`。
- `Prepare` 必须通过正式 Markdown Parser、10 MiB 上限、Result Hash 和 `ComputeChangeHash(target,base,content)` 校验，并保留目标 POSIX mode。
- `CommitCAS` 必须重新验证 inode/Base Hash、独立复制并 sync backup、rename、sync 父目录、复核 Result Hash/size/mode。
- `ResumeTarget` 必须重新获取 stable path lock + 原始 inode lock，并验证 target/temp/backup 的 owner/device/hash/size/mode 与三个 durable identity token；同内容、同 mode 但不同 inode 也属于篡改。
- `RestoreCAS` 只有当前目标仍匹配 Result Hash/mode/Result identity 时，才可原子 rename 已 fsync 的 Base backup；当前已为 Base 返回 replay，其他内容或 identity 一律不覆盖。
- rename 后结果未知时返回非空 `AppliedWrite + ManualRecoveryRequired`，保留 backup；`RestoreCAS` 证明恢复前禁止 Cleanup。

#### 4. Validation & Error Matrix

| 条件 | 稳定错误码 | 分类 | 是否重试/恢复 |
|---|---|---|---|
| Workspace Store 返回的对象 ID 与请求 `workspace_id` 不一致 | `PROPOSAL_WORKSPACE_BINDING_INVALID` | ConsistencyViolation | 否，修复 Store/调用绑定 |
| Workspace/目标路径、扩展名或大小非法 | `WRITEBACK_TARGET_INVALID` / `WRITEBACK_TARGET_TOO_LARGE` | InvalidInput | 否 |
| 父/目标 symlink、特殊文件、跨 device、owner 不匹配 | `WRITEBACK_TARGET_PARENT_UNSAFE` / `WRITEBACK_TARGET_UNSAFE` / `WRITEBACK_TARGET_CROSS_DEVICE` / `WRITEBACK_TARGET_OWNER_INVALID` | PermissionDenied | 否，修复文件边界后重试 |
| 同一 device/inode 锁等待超时 | `WRITEBACK_TARGET_LOCK_TIMEOUT` | RetryableFailure | 是，退避并重新 Acquire |
| 锁等待被调用方取消 | `WRITEBACK_TARGET_LOCK_CANCELLED` | NonRetryableFailure | 否，终止当前节点 |
| Markdown、UTF-8、二进制、正文大小或 Change Hash 不合法 | Parser `SOURCE_*` / `WRITEBACK_PREPARE_INVALID` | InvalidInput / VersionConflict | 否，重新生成 Proposal Revision |
| 最终目标 inode 或 Base Hash 已变化 | `TARGET_IDENTITY_CONFLICT` / `TARGET_BASE_HASH_CONFLICT` | VersionConflict | 否，进入 Needs Revision |
| temp/backup 在 rename 前写入、mode 或 sync 失败 | `WRITEBACK_TEMP_*` / `WRITEBACK_BACKUP_*` | DependencyUnavailable | 正式文件未替换；清理后按 Workflow 策略重试 |
| rename、父目录 sync、结果复核无法证明 | `WRITEBACK_RENAME_RESULT_UNKNOWN` / `WRITEBACK_PARENT_SYNC_RESULT_UNKNOWN` / `WRITEBACK_RESULT_VERIFY_UNKNOWN` | ManualRecoveryRequired | 禁止盲目重试；先用 Applied 摘要执行 Restore/人工核对 |
| Restore 时目标已被用户再次编辑 | `WRITEBACK_RESTORE_CONFLICT` | VersionConflict | 不覆盖用户内容，人工处理 |
| 目标/temp/backup 内容和 mode 相同但 inode token 不同 | `WRITEBACK_RESTORE_CONFLICT` / `WRITEBACK_RESUME_RESULT_UNKNOWN` | VersionConflict / ManualRecoveryRequired | 不 Commit、不 Restore、不 Cleanup，保留现场 |
| backup/temp locator、identity、hash 或 mode 被篡改 | `WRITEBACK_MANAGED_FILE_*` / `WRITEBACK_CLEANUP_*` | ConsistencyViolation / PermissionDenied | 保留现场，不删除未知文件 |

Filesystem Adapter 只负责文件副作用与补偿，不消费 Approval/Git Authorization，也不创建 Git Commit。`ManualRecoveryRequired` 必须保留 Base/Result Hash、受控 locator 和 backup；只有 Restore 已证明目标回到 Base 或完整 Saga 成功后才允许 Cleanup。

#### 5. Good / Base / Bad Cases

- Good：同一 inode 的大小写、Unicode 或 hardlink alias 竞争同一 flock；最终 Base Hash 未变时生成独立 backup，原子替换并验证 Result Hash。
- Base：补偿 rename backup 成功但响应丢失时，重启观察目标已为 Base、backup 已消失，`RestoreCAS` 仅返回 replay，不再写目标。
- Bad：把审批时的 Hash 读取当作最终 CAS、用 path string 作为锁 key、使用 hardlink backup、rename 报错后盲目重试或在 unknown 状态删除 backup。

#### 6. Tests Required

- Domain：路径/扩展名、10 MiB、Change Hash、Result Hash、locator 父目录、mode/token 和 Prepared→Applied 不可变绑定。
- Parser Adapter：有效 Markdown、空内容、非法 UTF-8、NUL/控制字节、取消、nil/unsupported Parser 和输入不可变。
- Filesystem Contract：父/目标 symlink、目录/FIFO/socket、跨 device/owner、mode、Base/identity conflict、Workspace Store 错绑（`CurrentHash` 与 `CurrentContent` 双路径）、独立 backup、restore replay、用户后续编辑、目标/temp/backup 同内容不同 inode 篡改和 Cleanup 幂等。
- Fault：temp sync、backup sync、rename 已执行但报错、parent sync、result verify；断言 rename 前正式文件不变，unknown 时 `errors.Is(ErrWritebackManualRecoveryRequired)` 且 Applied 摘要有效。
- Concurrency：helper subprocess 验证跨进程 flock、取消/超时、进程退出释放、hardlink 同 inode 和不同目标并行；锁用例执行 `-race -count=20`。

#### 7. Wrong vs Correct

```text
Wrong: Approval 时 CurrentHash == Base → 直接 os.WriteFile(target) → Git Commit。
Correct: Acquire inode lock → Prepare/parser/temp fsync → final inode+Base CAS → independent backup → rename+dir sync+Result verify。

Wrong: 只比较 Hash/mode；用户把 Result 或 backup 替换为同内容新 inode 后仍自动 Restore/Cleanup。
Correct: file_prepared 持久化 target/Result/backup 三个 opaque identity token；恢复逐一比较，任一 identity 漂移都保留现场并进入人工恢复。

Wrong: 信任 Workspace Store 按 ID 返回的根目录对象，直接按其路径读取 Proposal 当前文件。
Correct: 先比较返回对象 `ID == workspace_id`，不一致立即返回 `PROPOSAL_WORKSPACE_BINDING_INVALID`，不触碰文件系统。
```

### 7. Wrong vs Correct

```text
Wrong: Parse Projection 已存在 → 直接返回所有 Chunk。
Correct: Parse Projection 可共享，但查询/写入 Chunk 必须带 chunk_strategy_version + schema_version。
```

## M5 Safe Writeback Git Code-Spec

### 1. Scope / Trigger

- Trigger：批准正文已通过文件 CAS，需要把唯一目标变更安全发布为 Git Commit，并在命令超时、进程取消或反向提交失败时恢复确定事实。
- 目标：Workspace/HEAD、原始字节、Blob、Diff、Commit Trailer 和恢复行为保持同一不可变绑定；任何未知发布结果不得被上层当作“未提交”。

### 2. Signatures

```go
type GitRepository interface {
    Inspect(context.Context, foundation.ID, string) (GitSnapshot, error)
    DiffApproved(context.Context, GitDiffRequest) (GitDiff, error)
    CommitApproved(context.Context, GitCommitRequest) (GitCommit, error)
    FindWritebackCommit(context.Context, GitCommitLookup) (GitCommit, error)
    CreateReverseCommit(context.Context, ReverseCommitRequest) (GitCommit, error)
}
```

`GitCommitLookupLimit` 固定为 256；调用方不能扩大为无界历史扫描。领域接口不暴露 Git CLI 参数、绝对路径或原始 stderr。

### 3. Contracts

- Workspace ID 由服务端 Repository 解析 canonical root，Git top-level 必须等于 Workspace root；正式操作要求 attached HEAD、approved HEAD、全仓 clean、无 hidden index flags 和进行中操作。
- 目标必须是 approved HEAD 中的 tracked regular blob；结果原始字节同时绑定 Result SHA-256、raw Blob OID 和稳定 binary/full-index Diff SHA-256。
- Git 发布固定为 `raw hash-object → controlled update-index → write-tree → immutable tree verify → commit-tree -p approved → update-ref expected-old CAS`，禁止 `git add`、普通 `git commit` 和仓库 filter/hook/signature program。
- Commit Message/Trailer 只由领域绑定生成，Operation 区分 `apply/revert`；同一 Execution+Operation 的恢复必须验证 parent、target、blob、result、diff 和全部 Trailer。
- 发布命令返回未知结果时使用独立、短时 context 在当前分支可达历史内查询；只有唯一 exact Commit 可返回 `Recovered=true`，否则进入 ManualRecoveryRequired。
- ref 明确未发布时，只有当前 target index 仍为系统 Result Blob（或已为 Base）且不存在其他 staged path 才允许恢复 Base index；任何用户新 staged 内容都不得覆盖。

### 4. Validation & Error Matrix

| 条件 | 稳定错误码 | 分类 | 自动动作 |
|---|---|---|---|
| repo 缺失/root mismatch/unborn/detached/HEAD drift | `GIT_REPOSITORY_NOT_FOUND` / `GIT_REPOSITORY_ROOT_MISMATCH` / `GIT_REPOSITORY_UNBORN` / `GIT_REPOSITORY_DETACHED` / `GIT_HEAD_CONFLICT` | NotFound / PermissionDenied / VersionConflict | 不产生 Git 副作用 |
| staged/unstaged/untracked/conflict/hidden index flag/in-progress operation | `GIT_REPOSITORY_*` / `GIT_INDEX_FLAGS_UNSAFE` / `GIT_OPERATION_IN_PROGRESS` | VersionConflict / PermissionDenied | 拒绝批准或写回 |
| tracked/target filter、transform/diff/merge 属性、replace/graft/hook/signature 边界不安全 | `GIT_REPOSITORY_FILTER_UNSAFE` / `GIT_TARGET_*_UNSAFE` / `GIT_OBJECT_OVERRIDES_UNSAFE` | PermissionDenied | 不执行仓库 filter/hook/gpg program |
| Result/Blob/Diff/mode/index/tree 与批准绑定不符 | `GIT_*_CONFLICT` | VersionConflict | ref 未发布；只有能证明 index 仍是系统 Result 时才恢复 |
| 同 Execution+Operation 候选多个或 raw Commit 内容冲突 | `GIT_COMMIT_LOOKUP_CONFLICT` / `GIT_COMMIT_*_CONFLICT` | ConsistencyViolation | direct lookup 拒绝选择 |
| `update-ref`/Reverse 发布结果未知但 exact Commit 可验证且为当前/可达历史 | 返回 `Recovered=true` | 成功恢复 | 不重复 Commit；Reverse 幂等清理预期 REVERT_HEAD |
| 发布结果未知且无法 exact 证明，或发布后 post-verify/quit 失败 | `GIT_COMMIT_RESULT_UNKNOWN` / `GIT_REVERSE_RESULT_UNKNOWN` / `GIT_*_POSTCONDITION_FAILED` | ManualRecoveryRequired | 保留 Commit/index/worktree/marker，禁止文件补偿 |
| 明确未发布且 HEAD 仍为 approved，target index 仍为系统 Result | 原命令稳定错误码 | Retryable/NonRetryable | 恢复 Base index 后由 Saga 决定文件 RestoreCAS |
| index 已被用户再次 stage、含其他 staged path 或 HEAD 漂移 | `GIT_INDEX_RECOVERY_FAILED` | ManualRecoveryRequired | 不覆盖用户 index/文件 |

### 5. Good / Base / Bad Cases

- Good：`update-ref` 已成功但 runner 返回错误，exact Trailer 查询证明 Commit 为当前 HEAD 且完整绑定一致，返回 `Recovered=true` 并继续 DB reconcile。
- Base：ref 明确未发布、HEAD 仍为 approved、index 仍是系统 Result Blob，安全恢复 Base index，再由 Saga 执行文件 RestoreCAS。
- Bad：看到 `git commit`/`update-ref` 错误就直接重试、只按 Writeback ID 接受候选、覆盖用户新 staged blob，或在发布后验证失败时恢复文件。

### 6. Tests Required

- Domain：SHA-1/SHA-256 对象 ID、Operation、Trailer 单行值、Diff/Commit/Lookup/Reverse 完整绑定和 256 条查询上限。
- Inspect/Diff：repo missing/root mismatch/unborn/detached/HEAD drift、dirty/staged/untracked/conflict、hidden index flags、特殊路径、filter/attribute、replace/graft 和 raw blob/diff hash。
- Commit：Hook/GPG/signature/external diff 禁用、immutable tree staged-path 注入、update-ref old-value CAS、post-publish 完整验证和 replay。
- Recovery/Reverse：publish success-then-error、无 exact 结果、用户再次 stage、ancestor replay、revert conflict/unknown、REVERT_HEAD 清理；并执行 `go test -race -count=20 ./internal/platform/gitcli`。

### 7. Wrong vs Correct

```text
Wrong: git add target → git commit → 命令报错就再次提交或恢复文件。
Correct: raw blob → controlled index → immutable tree → commit-tree → update-ref CAS；错误先 exact lookup，无法证明则 ManualRecoveryRequired。

Wrong: index 当前含 target 就恢复 Base，默认它仍是系统 staged 内容。
Correct: 先核对 target mode/blob、其他 staged path 和 HEAD；发现任何用户漂移都保留现场并转人工恢复。
```

## M6-D Search HTTP And Evidence Error Contract

### 1. Scope / Trigger

- Trigger：Retrieval Domain/Application 错误需要稳定映射到 Search、Source Version 和 Span HTTP Problem，
  且 Cursor/Workspace 隔离不能通过空结果或差异化 404 隐藏错误。
- Scope：只覆盖 M6-D 查询边界；正式身份、Session、Token、CSRF/Origin 与 Capability 拒绝归 M10。

### 2. Signatures

```json
{
  "error_code": "RETRIEVAL_SEARCH_CURSOR_STALE",
  "message": "安全且可本地化的消息",
  "retryable": false,
  "details": {}
}
```

```text
POST /api/v1/search
GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}
GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}
```

### 3. Contracts

- HTTP Handler 使用统一 `foundation.Error` → Problem 状态映射；Domain/Application/Adapter 保留稳定 code
  与 retryable，不向客户端暴露 SQL、Provider cause、DSN、Query 正文、绝对路径或 managed locator。
- Search JSON 必须严格拒绝未知字段和多 JSON 值；Domain Canonicalization 失败不能转换为 `items=[]`。
- 共享 JSON 边界必须读取 `limit+1` 字节后再判断上限，不能让“合法 JSON + 超长尾随空白”绕过请求大小限制；
  解码前还必须拒绝原始非法 UTF-8，字符串 escape 必须拒绝未配对 surrogate，同时允许合法 surrogate pair。
- Search 可选字段必须区分“省略”与“显式 null”：只有省略可以触发默认值；显式 null 的 mode/filter/limit/
  filter 子字段统一返回 `RETRIEVAL_SEARCH_REQUEST_INVALID`，显式 null 或空 cursor 返回
  `RETRIEVAL_SEARCH_CURSOR_INVALID`，显式空 mode 也不得被当作默认 Hybrid。
- JSON 语法合法但字段类型错误时也必须按字段语义分类：Workspace/Query/mode/filter/limit 及 filter 子字段
  返回 `RETRIEVAL_SEARCH_REQUEST_INVALID`，cursor 类型错误返回 `RETRIEVAL_SEARCH_CURSOR_INVALID`；
  不能把 `json.UnmarshalTypeError` 直接暴露成漂移的 `INVALID_JSON`。
- mode、filter time 与 UUID 必须在 wire 解码后显式校验，使语法合法但业务非法的输入稳定归类为
  `RETRIEVAL_SEARCH_REQUEST_INVALID`，不能因直接解码为 `time.Time` 而漂移成 `INVALID_JSON`。
- Cursor invalid 与 stale 分离：签名/请求/格式/进程 key 失配是 400；Active Index 或完整结果漂移是 409。
- Source Version/Span 的不存在、跨 Workspace 与错绑统一 `404 RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND`，
  不通过状态码、code、message 或 details 暴露其他 Workspace 对象是否存在。
- caller cancel 映射为安全 503，并在内部保留 Context cause；Handler 不启动脱离 request Context 的检索。
- FTS-only Semantic 是明确 503 capability unavailable；Hybrid 只有在 SearchService 返回受控降级结果时
  才能 200，不允许 Handler 捕获任意 Provider 错误后自行回退。

### 4. Validation & Error Matrix

| 条件 | HTTP | 稳定 code | retryable |
|---|---:|---|---:|
| 非法 JSON/未知字段/多值 | 400 | `INVALID_JSON` | false |
| UUID/Query/mode/filter/limit 非法 | 400 | `RETRIEVAL_SEARCH_REQUEST_INVALID` | false |
| Cursor 签名/请求/版本/offset/进程 key 失配 | 400 | `RETRIEVAL_SEARCH_CURSOR_INVALID` | false |
| Active Index 或完整结果变化 | 409 | `RETRIEVAL_SEARCH_CURSOR_STALE` | false |
| Source Version/Span miss、跨 Workspace、错绑 | 404 | `RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND` | false |
| FTS-only Semantic | 503 | `RETRIEVAL_SEMANTIC_UNAVAILABLE` | false |
| Search/Evidence/Cursor 依赖未构造 | 503 | 对应 `*_SERVICE_UNAVAILABLE`/`*_CURSOR_UNAVAILABLE` | false |
| 请求取消 | 503 | `RETRIEVAL_SEARCH_CANCELLED` | false |
| 持久候选、Evidence 或 Artifact 绑定损坏 | 409 | 原稳定 Consistency code | false |
| 暂时 DB/Provider 故障 | 503 | 原稳定 dependency/retryable code | 保留原值 |

### 5. Good / Base / Bad Cases

- Good：第二页重算发现结果 Hash 变化，返回 409 stale 与空 details；客户端从第一页重启，不返回部分页。
- Base：Embedding disabled 的 Hybrid 返回 200 effective Keyword + degradation；Semantic 返回 503；
  Keyword 零命中返回 200 空 items。
- Bad：把所有 Retrieval 错误映射成 500，或把 Semantic unavailable 当作 200 空 items。
- Bad：跨 Workspace Source Version 返回“对象存在但无权限”，从而帮助枚举 ID。

### 6. Tests Required

- Handler table test 覆盖每个错误分类、状态、code、retryable 和无敏感 details。
- 共享 DecodeJSON test 覆盖 `limit+1`、超长尾随空白、原始非法 UTF-8、未配对 surrogate 与合法 surrogate pair。
- Handler table test 覆盖省略默认值，以及 Workspace/Query/mode/cursor/filter/limit/全部 filter 子字段的显式
  null 与错误类型；嵌套未知字段仍必须拒绝。
- Cursor test 覆盖篡改、跨请求、重启 key、stale Index/结果与 100 窗口边界。
- PostgreSQL HTTP test 覆盖跨 Workspace/错绑统一 404、Artifact consistency 409 与 Provider/DB 503。
- Secret canary 覆盖 Problem、Cursor、日志和测试失败输出；Query/正文/DSN/绝对路径不得出现。
- M10 之前仅验证 Workspace isolation + loopback，不得把 Auth/CSRF/Capability 负测标记为已完成。

### 7. Wrong vs Correct

```text
Wrong: if semanticErr != nil { return 200 items=[] }
Correct: Semantic capability unavailable 返回稳定 503；只有 Hybrid 的受控降级结果返回 200。

Wrong: evidence miss 根据“跨 workspace”或“span 不存在”返回不同 code/details。
Correct: 所有不可见绑定统一 RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND。
```

## M6-03 Tool Error Contract

- Registry/Contract/Executor 缺失必须返回稳定 DependencyUnavailable；API 不注入 Fake，Worker enabled 时缺真实 Executor/Workflow/依赖 readiness fail closed。
- Tool Request/Input/Output Schema 错误为 InvalidInput，且 Executor 调用数为 0；输出错误不能发布部分结果或把空对象记为成功。
- Workflow Definition/Run/Node/Attempt/lease 漂移返回 `TOOL_CONTEXT_STALE`；Permission、exact allowlist 和 Workflow binding 使用独立稳定错误，不互相降级。
- 成功 Tool Call 的 replay 只有在 `ResultReceiptLoader` 能读取权威 receipt 或重算无 IO 纯函数时才返回；不得重新执行网络/写入。无权威 Search receipt 返回 `TOOL_SEARCH_KNOWLEDGE_RECEIPT_UNAVAILABLE`，不能空成功。
- 副作用结果无法证明时只允许 UNKNOWN/ManualRecoveryRequired，不自动重试或标记成功。
- Safe Writeback `RequireStarted` 只把真正 NotFound 映射为 `TOOL_TRUSTED_WRITE_AUDIT_MISSING`；ContextStale、Permission 和 retryable DB 错误必须保留原分类，防止 lease-loss 被伪装成缺审计。
- Web Fetch policy 未持久接线时配置 enabled 返回 `WORKER_WEB_FETCH_POLICY_UNAVAILABLE`，且不得开始 DNS/Dial。

## M9 Proposal Detail Success Contract

### 1. Scope / Trigger

- 修改 Proposal detail/create response DTO、Approval snapshot、OpenAPI Proposal 判别联合或前端严格 decoder 时应用。
- 目标是区分“详情尚未审批”与“列表摘要未加载 Approval”，防止 Go `omitempty` 造成跨层契约漂移。

### 2. Signatures

```text
GET /api/v1/proposals/{proposal_id}
POST /api/v1/workspaces/{workspace_id}/proposals

Proposal detail/create: approval: Approval | null  # required
Proposal summary:       approval?: Approval        # optional, non-null
```

### 3. Contracts

- `file_patch` 与 `knowledge_change` 两种 Proposal detail 都必须显式序列化 `approval`；未审批时为 `null`，
  已审批时为与当前 Proposal、Revision 和 Change Hash 绑定的持久快照。
- Detail 的 Go response tag 不得使用 `omitempty`；Summary 必须继续使用 `omitempty`，且出现时只能是合法对象。
- OpenAPI 两个 detail schema 必须同时把 `approval` 声明为 required 和 nullable；前端缺字段时 fail closed。

### 4. Validation & Error Matrix

| Wire 状态 | Detail decoder | Summary decoder |
|---|---|---|
| `approval: null` | 接受，表示尚未审批 | 拒绝 |
| `approval: { ... }` 且绑定一致 | 接受 | 接受 |
| 缺少 `approval` | 拒绝 | 接受 |
| Approval 与 Proposal/Revision/Change Hash 错绑 | 拒绝 | 拒绝 |

### 5. Good / Base / Bad Cases

- Good：已批准详情返回完整 Approval snapshot，前后端共同校验当前 Revision 与 Change Hash 绑定。
- Base：`ready_for_review` 详情返回显式 `approval: null`，列表摘要可以完全省略该字段。
- Bad：detail DTO 使用 `json:"approval,omitempty"`，导致严格前端把合法未审批资源判为 `INVALID_RESPONSE`。

### 6. Tests Required

- Handler HTTP 回归分别断言未审批 `file_patch` 与 `knowledge_change` 的 JSON 含显式 null。
- OpenAPI drift gate 断言两个 detail schema 均 required + nullable，Summary 保持 optional non-null。
- 前端 decoder 覆盖 detail null/缺字段与 summary 省略/null，并运行 Go、Web、OpenAPI 全量门禁。

### 7. Wrong vs Correct

```text
Wrong: detail 与 summary 共用 `omitempty`，让字段缺失同时代表“未审批”和“未加载”。
Correct: detail 必需 nullable；summary optional non-null；HTTP、OpenAPI 与 decoder 用回归测试共同锁定。
```
