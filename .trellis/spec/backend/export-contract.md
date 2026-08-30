# 可恢复异步导出契约

## Scenario: Collection 与 Workspace 附件 Export

### 1. Scope / Trigger

- 修改 `internal/export/**`、Export Router/Auth/Worker/LocalFS、`ops.export_job`、下载 Audit、
  `export.*` Server Event、Collection Export 或 Settings 附件导出时，必须应用本契约。
- `COLLECTION` scope 只公开 `MARKDOWN|METADATA_JSON` 与 `export/v1`；
  `WORKSPACE_ATTACHMENTS` 只公开 `ATTACHMENTS_ZIP`、`attachment-export/v1`、
  `workspace-attachments/v1` 与 `RAW_USER_OWNED`。
- `EVALUATION_JSON`、`AUDIT_JSON`、`evaluation|audit_summary` 没有正式内容源，继续 fail closed；
  CSV/XLSX、通用字段映射、公式字段和 Collection 到附件的推导关系也不属于当前范围。
- PostgreSQL 是 Job、幂等、scope、租约、prepared result、capability gate、TTL、清理、下载统计和 Audit
  的唯一事实源；River 只运输 `{workspace_id,export_id}`，LocalFS 只读固定 `attachments/` 并管理
  `.knowledge/exports` 下的生成物。

### 2. Signatures

```text
# Collection
POST /api/v1/exports
GET  /api/v1/exports/{export_id}?workspace_id=...
GET  /api/v1/workspaces/{workspace_id}/exports?collection_id=...&limit=...&cursor=...
GET  /api/v1/exports/{export_id}/download?workspace_id=...

# Workspace attachments
POST /api/v1/workspaces/{workspace_id}/attachment-exports
GET  /api/v1/workspaces/{workspace_id}/attachment-exports?limit=...&cursor=...
GET  /api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}
GET  /api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}/download
```

```go
type FileStore interface {
    Stage(context.Context, foundation.ID, foundation.ID, string, []byte) (PreparedFile, error)
    Promote(context.Context, foundation.ID, PreparedFile) error
    Open(context.Context, foundation.ID, string, string, int64) (io.ReadCloser, error)
    DeletePrepared(context.Context, foundation.ID, string, string, int64) error
    DeleteOrphan(context.Context, foundation.ID, string) error
}

type AttachmentArchiver interface {
    StageAttachments(context.Context, foundation.ID, foundation.ID) (AttachmentArchive, error)
}
```

- `ops.export_job.scope_kind` 是 `COLLECTION|WORKSPACE_ATTACHMENTS` tagged union discriminator；附件结果额外冻结
  `attachment_root_contract_version`、`manifest_sha256`、`entry_count` 和 `total_uncompressed_bytes`。
- `ops.export_capability('workspace-attachments','workspace-attachments/v1')` 是持久启用门禁，migration 后默认关闭。

### 3. Contracts

- Create 必须有唯一 `Idempotency-Key`。相同 `(workspace_id,key)` 的 exact replay 先于当前 Collection 或
  `attachments/` 检查；首次返回 `202`，完全相同请求返回原 Job（`200`），不同 canonical request 返回
  `409 EXPORT_IDEMPOTENCY_CONFLICT`。过期 Job 也不复用原 key 创建第二个任务。
- `COLLECTION` 必须携带 Collection ID/version/query hash、非空 fields 和 `MASKED|FULL` 策略；
  `WORKSPACE_ATTACHMENTS` 禁止这些 Collection 字段，fields 必须为 `[]`，只允许 `RAW_USER_OWNED`。
  数据库 scope、prepared 和 path `CHECK` 必须以完整表达式 `IS TRUE` 并显式检查必填非 NULL，防止 PostgreSQL
  `UNKNOWN` 绕过 tagged binding。
- capability gate 关闭或 contract version 不匹配时，附件 Create 返回 unavailable，Claim/Recovery 不领取附件 Job；
  Collection 查询始终显式过滤 `COLLECTION`。只有所有 API/Worker 都支持同一 contract 后才原子启用 gate。
- Collection 执行固定为 `Claim -> durable snapshot -> render -> Stage -> Prepare -> Promote -> Complete`；
  prepared 后恢复只验证固定 revision/count/path/hash/size，不重新读取 Collection。
- 附件执行固定为 `Claim -> fd-relative scan/hash -> deterministic ZIP staging -> Prepare -> Promote -> Complete`。
  从 canonical Workspace root handle 开始逐组件 no-follow；只接受 `attachments/` 中 link count=1 的 regular file，
  拒绝 symlink/hardlink/device/FIFO/socket、无效 UTF-8、Zip Slip 及 NFC/case-fold/path collision。
- v1 上限为 10,000 entries、单文件 256 MiB、总未压缩 1 GiB、ZIP 1 GiB；任一超限统一返回
  `EXPORT_ATTACHMENT_LIMIT_EXCEEDED`，禁止截断。目录使用有界批次遍历，并在 Prepare 前复核文件、空目录、祖先
  和 root identity 的最终快照。
- manifest 位于 ZIP 根级，payload 位于 `attachments/<relative-path>`；entry 按 NFC 规范化相对路径的 UTF-8 byte
  order 排序，使用 `zip.Store`、固定时间/权限、无 comment/extra。相同冻结输入必须产生相同 manifest/archive hash。
- `Promote` 必须 create-only，冲突不得覆盖 winner。`DeletePrepared` 先把当前命名对象原子隔离、重新验证
  hash/size 后再删；竞争产生的 replacement 必须保留。orphan 删除只接受严格 staging namespace。
- 下载前 `FileStore.Open` 在同一 FD 上有界校验 path/inode/hash/size 并 rewind，Service 随后原子写下载统计与
  `export.download` Audit，HTTP 使用 durable `Content-Length` 与 `io.CopyN` 流式返回。任何中间失败必须 Close，
  Audit 只声明 `server_outcome=prepared_for_return`，不声称客户端收完。
- List 使用 `created_at DESC,id DESC` 有界 opaque cursor；Collection cursor 绑定 Workspace/Collection/limit，附件
  cursor 绑定 Workspace/scope/limit。Create/List/Get/Download 全部要求当前 Workspace `READ_LOCAL`，并防跨 Workspace
  枚举。
- TTL 只删除 hash/size 匹配的生成物；源 `attachments/`、Job、manifest/archive digest、统计和 Audit 永不由 cleanup
  删除或改写。删除失败保留可重试事实。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
|---|---|
| 非法/重复/未知 JSON 字段、UUID/hash/enum/TTL/cursor/key 非法 | `400 EXPORT_REQUEST_INVALID` 或 `INVALID_JSON` |
| scope/kind/schema/root/policy/Collection binding 不匹配 | `400 EXPORT_REQUEST_INVALID`，不创建 Job |
| gate 关闭、contract mismatch 或依赖不可用 | `503 EXPORT_DEPENDENCY_UNAVAILABLE`，不创建/claim 新附件 Job |
| 权限、Session/API Token、CSRF/Origin 或 `READ_LOCAL` 不满足 | `403 EXPORT_PERMISSION_DENIED` |
| 跨 Workspace、Job 或 Collection 不存在 | `404 EXPORT_NOT_FOUND`，不帮助枚举 |
| 同 key 不同 canonical request | `409 EXPORT_IDEMPOTENCY_CONFLICT` |
| 下载未完成 | `409 EXPORT_RESULT_NOT_READY` |
| 下载或读取时已过期 | `410 EXPORT_EXPIRED`，保留 cleanup 事实 |
| missing root、unsafe entry、源变化或 path collision | 稳定失败，不产生可下载 partial ZIP |
| 文件数、单文件、总字节或 ZIP 超限 | `EXPORT_ATTACHMENT_LIMIT_EXCEEDED`，不截断 |
| path/inode/hash/size/prepared binding 无法证明一致 | `500 EXPORT_RESULT_INCONSISTENT`，不增加下载统计 |
| cleanup mismatch | 保留 replacement/源文件和失败事实，后续可重试 |

成功下载按 kind 返回固定 Content-Type、受控 ASCII filename、`Content-Length`、
`Cache-Control: private, no-store` 和 `X-Content-Type-Options: nosniff`。Server Event 只提供 typed invalidation hint；
客户端必须回查 REST Job/List，不能从事件 payload 推导终态。

### 5. Good / Base / Bad Cases

- Good：兼容 release 读取 tagged union，gate 启用后 Settings 创建 Workspace 附件 Job；Worker 生成确定性 ZIP，
  重启从 prepared binding 恢复，下载以验证后的 FD 流式返回，cleanup 后源附件字节和 metadata 不变。
- Base：空 `attachments/` 生成 zero-entry manifest；任务失败/过期保留历史并允许用户用新 key 新建；gate 关闭时
  Collection Export 继续正常，附件入口明确 unavailable。
- Bad：用 dummy Collection 表示附件、用 `{"available":false}` 冒充 Evaluation/Audit、`Lstat -> Rename` 覆盖 final、
  无 hash/size 删除 prepared 文件、把 1 GiB ZIP `ReadAll` 到 Go 堆，或在 cleanup 中扫描/删除源附件。

### 6. Tests Required

- Domain/Application：tagged scope、canonical request、exact replay/conflict、lease/TTL fence、prepared replay、
  gate disabled/enabled、流关闭和下载 Audit actor/binding。
- LocalFS：create-only 并发 winner、prepared replacement/quarantine、orphan namespace、empty/binary/nested/determinism、
  NFC order、symlink/hardlink/FIFO/socket、limits、源与祖先变化、最终目录 fingerprint、流式 hash/size 校验。
- PostgreSQL/Migration：fresh/repeat/upgrade、NULL/UNKNOWN `23514`、scope/prepared/path 互斥、历史事实保留、同 key
  并发、DB-time Claim/Prepare/Complete/Fail、scope isolation、cleanup retry、download+Audit 原子性。
- HTTP/OpenAPI/Auth：严格 body/header/query/cursor、`202/200/400/403/404/409/410/500/503`、ZIP headers、
  Collection 兼容与 Evaluation/Audit rejection。
- Frontend/Browser：两个 strict wire owner、Workspace query/cache/Abort 隔离、same-key retry、2 秒轮询、SSE invalidation、
  Blob binding，以及真实 PostgreSQL/API/Worker/Vite 的刷新/重启、ZIP 解包/hash、权限/跨 Workspace/unsafe/源变化/
  tamper/expiry/cleanup、桌面与 390x844。

```bash
go test -race -count=1 -timeout 60s ./internal/export/... ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/export/adapter/postgres
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s -run '^(TestExport|TestM9|TestAttachment)' ./internal/platform/migration
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/export-browser-smoke.sh
git diff --check
```

### 7. Wrong vs Correct

```text
Wrong: PostgreSQL CHECK 只写 `query_hash ~ ...`，允许 NULL 产生 UNKNOWN 后通过。
Correct: tagged scope/prepared/path 完整表达式 `IS TRUE`，分支必填列同时显式 `IS NOT NULL`，并用真实 23514 负测锁定。

Wrong: 下载前 `io.ReadAll` 最大 1 GiB ZIP，或 RecordDownload 后丢失未关闭的文件流。
Correct: 同一 FD 校验并 rewind；Audit 成功后用 Content-Length + CopyN 流式返回，所有失败路径 Close。

Wrong: Collection Markdown 能下载就声称 AC-33 完成，或把 Evaluation/Audit 占位文件算作交付。
Correct: AC-33 由 Collection Markdown、Metadata JSON 和真实 Workspace 附件 ZIP 共同关闭；Evaluation/Audit 仍独立 deferred。
```
