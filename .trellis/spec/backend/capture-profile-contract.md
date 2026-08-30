# 快速记录与文档知识画像契约

## Scenario: Quick Capture And Document Knowledge Profile v1

### 1. Scope / Trigger

- 修改 `internal/capture`、Workspace 单条 Source 注册、Ingestion/Retrieval 衔接、Capture HTTP/OpenAPI、
  API/Worker composition 或迁移 `00068_capture_profile.sql` 时应用。
- Capture 只编排不可变输入及派生处理状态；Source/Content Artifact/Source Version、Ingestion、Retrieval 和
  Agent Model Run 继续由各自 owner 持有。Profile 是候选投影，不是正式 Topic、Claim、Relation 或 Proposal。

### 2. Signatures

```text
POST /api/v1/workspaces/{workspace_id}/captures
POST /api/v1/workspaces/{workspace_id}/capture-files
GET  /api/v1/workspaces/{workspace_id}/captures
GET  /api/v1/workspaces/{workspace_id}/captures/{capture_id}
POST /api/v1/workspaces/{workspace_id}/captures/{capture_id}/retry
GET  /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/knowledge-profile
POST /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/knowledge-profile/retry
```

```go
Repository.Create(context.Context, CreateRecord) (CreateResult, error)
Repository.Get(context.Context, foundation.ID, foundation.ID) (domain.Capture, error)
Repository.List(context.Context, ListQuery) (Page, error)
ManagedContentWriter.StageManagedBytes(context.Context, WorkspaceID, SourceRef, Bytes, ContentHash) (ManagedContentStage, error)
ManagedContentWriter.PublishManagedBytes(context.Context, WorkspaceID, ManagedContentStage) (ContentCapture, error)
RetryScheduler.ScheduleRetry(context.Context, RetryRecord) (RetryResult, error)
ProfileReader.GetProfile(context.Context, ProfileLookup) (ProfileView, error)
ProfileGenerator.Generate(context.Context, ProfileGenerationRequest) (ProfileGenerationResult, error)
```

- 持久事实表为 `core.capture`、`core.capture_command`、`ops.capture_outbox`、`ops.capture_attempt`、
  `learning.document_knowledge_profile`、`document_knowledge_profile_revision`、
  `document_knowledge_profile_attempt` 和 `document_knowledge_profile_evidence`。

### 3. Contracts

- Capture kind 固定为 `TEXT|URL|FILE|IMAGE`；聚合状态固定为 `RECEIVED|SOURCE_SAVED|FETCHING|PROCESSING|
  READY|READY_DEGRADED|FETCH_FAILED|PROCESSING_FAILED`。Fetch/Ingestion/Index/Profile 各自维护
  `PENDING|RUNNING|READY|FAILED|CAPABILITY_UNAVAILABLE|STALE|NOT_APPLICABLE` 的适用子集，单层失败不得覆盖
  其他层已完成事实。
- TEXT/FILE/IMAGE 在命令事务前只把已验证原始字节写入 Workspace 受控暂存；数据库事务保存稳定的
  `.knowledge/sources/<content_hash>` 最终位置，并原子创建 Capture、Source、Source Version、command receipt 和
  `capture.process_requested` outbox。只有确认事务提交后才以 create-only hard link 发布最终 Artifact。URL 在任何网络访问前
  先提交 Capture + Source + receipt + outbox；抓取成功采用相同 stage -> transaction -> publish 协议追加 HTML Source Version，
  失败保留原 URL 和同一 Capture identity。
- 暂存 locator 不进入数据库、outbox、事件或日志。提交结果未知时不得删除最终文件或暂存；exact command receipt replay、
  URL 状态恢复或已绑定 Artifact 的首次读取必须能验证暂存 hash/size 后自愈发布。只有权威查询证明本次唯一暂存未被引用时
  才可清理该暂存；URL 暂存 identity 必须包含 proposed Source Version ID，不能只绑定可被重复 delivery 共享的 processing attempt；
  清理 API 永远不接受最终 content-addressed 路径。
- TEXT/FILE/IMAGE 暂存的 `SourceRef` 必须由 `workspace_id + idempotency_key` 稳定派生，不能使用事务前新生成的 Capture ID；
  未知提交实际未落库时，完全相同的命令重试必须复用并最终清理同一 locator，不能逐次遗留不可寻址的 stage。
- 首次创建 `.knowledge/sources/.staging/<content_hash>` 目录链时，每个成功创建的目录都必须在继续前 `fsync` 父目录；
  stage hard link、最终 hard link 与清理也要同步各自承载目录，避免数据库已提交但掉电后目录项丢失。
- 发布与 read-repair 在计算 hash 前必须校验打开文件的普通文件类型和精确 size，并最多读取 `expected_size + 1` 字节；
  被本地篡改的 `0600` stage 不得触发无界 I/O，也不得以匹配前缀冒充已验证内容。
- kind、原始位置/URL/hash、Source、captured time 均不可变；Capture 更新必须严格 `version + 1`。文件名只用于显示，
  不能成为 managed path。原始正文、URL 响应正文和 Secret 不进入 outbox、事件或日志。
- URL fetch 仅允许无 credential 的 HTTP(S)，并对 DNS/私网、每次重定向、解析地址固定、媒体类型、Header/Body 大小和
  deadline fail closed。HTTP `408|425|429|5xx` 可重试；其他策略拒绝不伪装为网络成功。
- 相同 `workspace_id + idempotency_key + request_hash + command_type` 精确回放完整响应；同 key 不同 binding 返回
  `CAPTURE_IDEMPOTENCY_CONFLICT`。Retry 还绑定 Capture/Profile current version，outbox 与 receipt 必须和状态更新原子提交。
- Ingestion 成功后建立 Keyword 可用的 Active Index；Embedding disabled 只把 vector 记录为 capability unavailable，不能阻断
  Keyword。图片无 OCR/视觉能力时保留原图，并以 `READY_DEGRADED` 明确结束。
- Capture 的 refresh checkpoint 必须同时验证已 `chunked/passed` 的 Ingestion Attempt、Source/Parse/Manifest 绑定和至少已
  `ready` 的 Index；`building` Index 不得投影为 Capture `index_status=READY`。
- Profile Schema 固定为 `document-knowledge-profile/v1`，冻结 Source Version、Parse Projection、Index Version、
  Prompt、Model Run、Model Settings 和内容 digest。Topic/Term/Knowledge Point/Example 都必须绑定当前 Source Version 的
  已验证 Source Span；模型输出不能直接写正式知识表。
- Profile loader 最多返回 500 个 Chunk，单 Chunk 正文最多 128 KiB；大小上限必须在 SQL 投影阶段 fail closed，禁止先把
  超限正文完整传入进程后再由模型输入校验拒绝。
- Profile Revision/Evidence append-only；mutable Profile 只移动 current pointer 和状态。重试期间、失败后和 `STALE` 状态
  继续保留旧 current Revision 可读；新 revision 只有与 Model Run、attempt、evidence 闭包在同一事务完成后才能成为 READY。
- GET 要求 `READ_LOCAL`；Capture/Profile mutation 要求 `WRITE_PROPOSAL`。System Status 的 `capture.status` 是必填公开能力，
  只有 HTTP composition 可用时为 `ready`。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知 JSON/multipart 字段、非法 kind/UUID/version、空文本、非法 MIME/大小 | 400；不写 Capture、Source、Artifact、receipt 或 outbox |
| URL 含 credential、私网/DNS rebinding、超限重定向/Body/Header、非 HTML | 稳定 fetch 错误；Source/Capture 保留，是否可重试由错误分类决定 |
| 相同 idempotency key 不同 request/command/target | 409 `CAPTURE_IDEMPOTENCY_CONFLICT`，无重复对象 |
| Workspace/Capture/Source Version/Profile 绑定不匹配 | 404 防枚举，不返回跨 Workspace 数据 |
| Capture/Profile CAS、attempt 或 Model Run replay binding 漂移 | 409 或 manual recovery；旧 Revision/Evidence 不变 |
| Capture/URL 数据库提交响应丢失或 publish 终态未知 | `CAPTURE_CONTENT_FINALIZATION_UNKNOWN`；保留暂存，exact replay/Artifact read 自愈，不删除最终文件 |
| 未知提交未落库后重放相同 materialized command | 复用稳定 stage locator；成功事务后发布并清理，不新增孤儿 stage |
| stage 目录为 symlink、权限非 `0700`，或 stage 文件非普通 `0600` 文件 | `CONTENT_ARTIFACT_STAGE_INVALID|UNSAFE`；不得创建最终 hard link |
| stage/final 文件 size 或 hash 与冻结 binding 不同 | 有界读取并返回 `CONTENT_ARTIFACT_CONTENT_CONFLICT`；不得发布或返回正文 |
| Parser/OCR/Embedding/Profile capability disabled | 已支持层继续完成；对应 stage 明确 `CAPABILITY_UNAVAILABLE`，不得伪造内容 |
| Refresh checkpoint 指向 building Index 或不闭合的 ingestion/manifest binding | `CAPTURE_REFRESH_CHECKPOINT_BINDING_INVALID`；Capture/Attempt 版本不变 |
| Profile 输出缺 topic/knowledge point、Span、digest 或冻结契约 | `CAPTURE_PROFILE_OUTPUT_INVALID|CONTEXT_INVALID`，不追加 READY revision |
| Profile Chunk 为空、超过 128 KiB 或总数超过 500 | `CAPTURE_PROFILE_CONTEXT_INVALID`；不返回超限正文，不调用 Provider |
| Provider 成功但 Profile/Model Run 终结未知 | `CAPTURE_PROFILE_FINALIZATION_UNKNOWN`，不得重复调用 Provider 或假成功 |
| `00068` 旧库有受保护 Capture/Profile 事实 | 前向升级保持数据与迁移版本语义不变 |

### 5. Good / Base / Bad Cases

- Good：TEXT Capture 创建后，Worker 解析、分块、建立 Keyword Index，并生成带 Source Span 的 Profile Revision；刷新只读取
  REST 权威状态。
- Base：模型和 Embedding disabled 时，Capture 仍是 `READY_DEGRADED`，全文检索可用，Profile 返回
  `CAPTURE_PROFILE_CAPABILITY_UNAVAILABLE` 且没有伪造摘要。
- Bad：抓取 URL 成功后覆盖原始 URL；把 Profile Topic 写进正式 Graph；Profile 重试先清空 current revision；只写 outbox
  不写 command receipt。

### 6. Tests Required

- Domain/Application：全状态组合、immutable provenance、request hash、exact replay/conflict、retry/version、stage/commit/publish
  顺序、明确回滚/未知提交/publish response-loss、未知未提交后的稳定 locator 回收、同 hash 并发、独立降级和 Profile strict schema/evidence/digest。
- Filesystem：首次目录链逐级 parent sync、stage directory/file symlink、权限、超大/篡改内容、并发 create-only publish、
  discard 不删除同 hash final，以及 DB 已绑定但 final 缺失时的 read-repair。
- PostgreSQL/Migration：空库 Up、重复 Up、旧版本数据前向升级、Workspace/FK/CAS、outbox claim/retry/poison、response-loss replay、
  Profile old revision survival、Chunk 读取上限和 Model Run terminal closure；使用真实 PostgreSQL。
- HTTP/Auth/OpenAPI/Composition：strict JSON/multipart、大小/MIME、Capability、System Status capture 字段、API/Worker wiring。
- Network/Parser：SSRF、DNS pinning、redirect、timeout、status classification、HTML/PDF 边界和取消后的 terminal persistence。
- Canonical：受影响 Go race/vet/tidy、全仓 test/vet、OpenAPI check、前端 lint/typecheck/test/build、`git diff --check`。
- Browser：真实 PostgreSQL/API/Worker/Vite 完成 Capture -> Inbox -> Detail；桌面和 `390x844` 断言快捷键、焦点恢复、
  零横向溢出、零 console error/warning 和明确 capability degradation。

### 7. Wrong vs Correct

```text
Wrong: URL 抓取成功后才创建 Source；失败时用户在 Inbox 找不到原始输入。
Correct: 先原子提交 Capture + Source + receipt + outbox；抓取只追加不可变 HTML Source Version。

Wrong: Profile retry 把 current_revision_id 清空，或模型失败后覆盖上一版内容。
Correct: retry 只启动新 attempt；旧 Revision/Evidence 始终 append-only 且可读，成功事务才移动 current pointer。

Wrong: Embedding/Profile 不可用就把整个 Capture 标记为处理失败。
Correct: 每层独立记录状态；Keyword/解析完成事实保留，聚合明确 READY_DEGRADED。

Wrong: 原始字节先发布到最终 hash 路径，数据库失败时再尝试删除该最终文件。
Correct: 先写唯一受控暂存，事务只保存稳定最终路径；确认提交后发布，未知结果保留暂存并通过 receipt/读取自愈。

Wrong: 用每次尝试新生成的 Capture ID 作为 stage locator，或只同步 stage 文件而不持久化新建目录的父目录。
Correct: materialized create 由幂等命令派生稳定 locator；新建目录、stage link、final link 和清理均同步对应父目录。

Wrong: 发现 stage 是 `0600` 普通文件后用 `context.Background()` 无界读取再比较 size/hash。
Correct: 校验打开文件的精确 size，并在调用方 context 下最多读取 `expected_size + 1` 字节后比较冻结 hash。
```
