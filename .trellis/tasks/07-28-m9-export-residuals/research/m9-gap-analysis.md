# Research: M9 Export 遗留项、附件语义与 AC-33 缺口

- Query: 核对附件导出的产品含义、AC-33 完整要求、已提交 M9-03 与当前未提交 Export 调整，评估权限、脱敏、过期、恢复、审计、下载和附件边界，并区分后续扩展、现存缺陷与 M11 验收。
- Scope: internal（仓库文档、Trellis 任务/spec、当前源码/测试/OpenAPI；Git 差异事实由主会话提供）
- Date: 2026-07-28

## Findings

### 1. 结论

1. **AC-33 的功能闭环是 Markdown、附件、领域元数据三类可迁移导出，不包含评测和审计导出。** 用户故事明确写的是“Markdown、附件和元数据”，验收矩阵也只因附件缺失而标记部分完成（`docs/product/PRD.md:4275`, `docs/product/PRD.md:4632`）。评测结果和审计摘要是更大的正式 v1 数据导出要求，但属于独立产品能力（`docs/product/PRD.md:2808`, `docs/product/PRD.md:2810`, `docs/product/PRD.md:2814`；`.trellis/tasks/07-16-product-delivery/prd.md:109`, `.trellis/tasks/07-16-product-delivery/prd.md:113`）。
2. **产品“附件”不是 HTTP `Content-Disposition: attachment`。** 它是用户 Workspace 中拥有的原始附件文件，架构固定展示为 `workspace/attachments/`，且必须与应用托管的 `.knowledge/exports` 分开（`docs/architecture/data-architecture.md:17`, `docs/architecture/data-architecture.md:23`, `docs/architecture/data-architecture.md:52`, `docs/architecture/data-architecture.md:65`, `docs/architecture/data-architecture.md:79`）。当前下载响应的 `attachment` 只表示浏览器下载 disposition（`internal/export/http/handler.go:306`, `internal/export/http/handler.go:312`），不能作为 AC-33 附件证据。
3. **已提交 M9-03 是应保留的稳定基线。** 它只交付 Smart Collection `MARKDOWN|METADATA_JSON`，同时交付幂等、冻结快照、prepared-result 恢复、Workspace/权限、过期清理、下载审计和浏览器闭环；归档任务明确把附件、Evaluation/Audit 内容导出留给独立任务（`.trellis/tasks/archive/2026-07/07-26-m9-export/prd.md:16`, `.trellis/tasks/archive/2026-07/07-26-m9-export/prd.md:24`, `.trellis/tasks/archive/2026-07/07-26-m9-export/prd.md:47`, `.trellis/tasks/archive/2026-07/07-26-m9-export/prd.md:64`）。父任务记录其工作提交为 `0165783`（`.trellis/tasks/07-16-product-delivery/implement.md:61`）。
4. **当前未提交补丁不能原样采纳。** 它没有附件来源、附件打包或附件 API/UI，却加入 `EVALUATION_JSON`、`AUDIT_JSON` 和两个公开 field，并把结果渲染为 `{"available":false}`；这不是内容导出，而是成功文件中的占位假能力（`internal/export/domain/model.go:16`, `internal/export/domain/model.go:80`, `internal/export/application/render.go:56`, `internal/export/application/render.go:176`, `web/src/api/exports.ts:8`, `api/openapi/openapi.json:14572`）。
5. **FileStore 改动是阻断级恢复/数据安全退化。** 当前接口把 HEAD 的绑定删除收窄语义合并为不带 hash/size 的 `Delete`（`internal/export/application/ports.go:44`, `internal/export/application/ports.go:49`），cleanup 仅凭持久路径删除（`internal/export/application/service.go:686`, `internal/export/application/service.go:693`），LocalFS 只检查“普通文件”而不验证 prepared binding（`internal/export/adapter/localfs/store.go:299`, `internal/export/adapter/localfs/store.go:315`）。当前 `Promote` 又在 Lstat 后使用会替换既有目标的 rename（`internal/export/adapter/localfs/store.go:208`, `internal/export/adapter/localfs/store.go:221`），失去真正 create-only 发布语义。
6. **最小可验收范围应只新增 Workspace 附件归档并关闭 AC-33，不把 Evaluation/Audit 混入。** 建议复用已验证 Job/租约/prepared/download/audit 生命周期，但新增明确的 `WORKSPACE_ATTACHMENTS` scope 和便携 archive contract；入口应位于 Settings 数据导出，而不是假设 Collection item 与附件存在当前并不存在的关联。

### 2. 附件导出的具体产品含义

#### 2.1 已有事实

| 事实 | 证据 | 含义 |
|---|---|---|
| 用户拥有 Markdown 与附件原文件 | `docs/product/PRD.md:84` | 导出目标是用户文件字节，不是数据库投影中的占位字段。 |
| Workspace 创建需求包含“附件目录” | `docs/product/PRD.md:701`, `docs/product/PRD.md:707` | 附件有独立目录边界，不应从任意 Source path 或 Markdown 文本启发式猜测。 |
| 架构目录是 `attachments/` | `docs/architecture/data-architecture.md:52`, `docs/architecture/data-architecture.md:65`; `docs/architecture/system-context.md:128`, `docs/architecture/system-context.md:135` | 当前最强实现依据是 Workspace 根下固定目录。 |
| 附件是为避免应用锁定而导出 | `docs/product/PRD.md:4275` | 导出必须携带原始可用字节和可解释目录/manifest，不能只输出计数或 `available:false`。 |
| Export cleanup 不得碰普通附件 | `docs/architecture/data-architecture.md:79`, `docs/architecture/data-architecture.md:81` | TTL 只能删除生成的 archive/staging，绝不能删除 `attachments/` 源文件。 |
| 当前 Collection snapshot 没有附件引用 | `internal/export/application/ports.go:85`, `internal/export/application/ports.go:97`, `internal/export/application/ports.go:110` | 不能声称“按 Collection 导出关联附件”；没有稳定关联事实。 |
| 当前 Workspace 聚合没有附件目录配置 | `internal/workspace/domain/model.go:26`, `internal/workspace/domain/model.go:30` | PRD 的可配置附件目录尚未落地；附件 residual 必须明确采用固定 `attachments/`，或先补配置契约。 |

仓库搜索未发现任何 Attachment 领域类型、表、Repository、HTTP API 或前端 wire owner；仅发现文档中的 `attachments/` 与 HTTP 下载头中的 `attachment`。因此下列问题目前没有仓库事实答案：

- 是导出整个 Workspace 的附件目录，还是只导出与某个 Collection/Markdown 关联的附件；
- 关联关系、附件稳定 ID、版本、MIME、逻辑删除和安全分类由谁拥有；
- archive 格式、manifest schema、文件数/总大小上限、重复路径/大小写/Unicode 冲突规则；
- 配置的附件目录是否可以位于 Workspace 根之外。

#### 2.2 建议的最小产品口径

在不发明“Collection 附件关系”的前提下，最小且可复核的定义应是：

> 用户从 Settings 发起一个 Workspace 级异步附件导出。服务端递归读取 Workspace 根下受控 `attachments/` 目录中的普通文件，将原始字节和一个版本化 manifest 打包为标准 ZIP；刷新/重启后可恢复任务并下载。ZIP 过期后只删除生成结果，源附件、Job、hash 和 Audit 永久不受影响。

这是**设计建议**而不是现有事实，理由如下：

- Settings 的正式数据导出需求拥有 Markdown/附件/元数据/评测/审计条目（`docs/product/PRD.md:2808`），而 Collection 页面合同只拥有两个当前 kind（`docs/product/PRD.md:4487`, `docs/product/PRD.md:4506`）。
- 当前 Collection read model 无附件 identity；从 Markdown link 或 Source path 猜测关联会建立不可审计的第二事实源。
- ZIP 是无需专有客户端的便携容器，符合“数据导出不依赖专有客户端”（`docs/product/PRD.md:2832`）。若产品选择 tar 等其他标准格式，必须在 PRD/design 中替换，不能让实现自行决定。

固定 `attachments/` 与 PRD 的“附件目录可输入”存在缺口。最小 residual 可以明确只支持架构约定的固定目录，并把可配置目录留在 AC-01/Workspace 配置任务；若要求严格兑现 `docs/product/PRD.md:707`，则必须先给 Workspace 增加持久、规范化、不可越界的附件目录配置，AC-33 才能按配置导出。

### 3. AC-33 完整要求与 v1 其他 Export 要求

#### 3.1 AC-33 本身

AC-33 完成至少需要：

1. 已有 Smart Collection Markdown 导出；
2. 已有领域 Metadata JSON 导出；
3. 新增真实附件原始字节导出；
4. 三类能力均能从真实 UI/API 使用并有恢复/失败/下载证据，而不是只存在枚举或测试 fixture。

旧 M9 任务已明确以“附件仍未交付”为 AC-33 唯一关闭缺口（`.trellis/tasks/archive/2026-07/07-26-m9-export/prd.md:11`, `.trellis/tasks/archive/2026-07/07-26-m9-export/prd.md:57`）。

#### 3.2 AC-33 之外仍需满足的跨导出质量合同

父任务 R-08 要求 Export 记录 `schema_version`、Workspace ID、权限、脱敏、生命周期、幂等、可追踪性和公式注入防护（`.trellis/tasks/07-16-product-delivery/prd.md:113`, `.trellis/tasks/07-16-product-delivery/prd.md:114`）；父设计要求 request/query version/fields/permission/hash/status/expiry/download audit（`.trellis/tasks/07-16-product-delivery/design.md:164`, `.trellis/tasks/07-16-product-delivery/design.md:169`）。附件是二进制，不能声称对原始字节做文本脱敏或公式中和；这些规则应改写为：

- raw archive 是显式 `READ_LOCAL` 数据导出，不使用 `MASKED` 假语义；
- manifest、Job、Problem、事件和 Audit 不包含绝对路径、Secret、附件正文或可疑文件名明文；
- ZIP entry path 必须防路径穿越/Zip Slip，拒绝 symlink、device、socket、FIFO 和越界目录；
- 所有上限显式失败，禁止静默截断；
- 原始附件内容保持字节级不变，archive manifest 保存相对路径、SHA-256、size 和稳定排序。

#### 3.3 Evaluation/Audit 的位置

- `EVALUATION_JSON`：正式 v1 确实要求“评测结果”，但当前只有运行时输出到 stdout 的离线 Report（`eval/agent/eval.go:70`, `eval/agent/cmd/main.go:16`, `eval/agent/cmd/main.go:26`; `eval/semanticlink/eval.go:107`, `eval/semanticlink/cmd/main.go:16`），没有持久 evaluation result/run 表或统一 Workspace-scoped 内容源。M11-02 才拥有 AI Eval 全量回归门禁（`.trellis/tasks/07-16-product-delivery/implement.md:68`）。
- `AUDIT_JSON`：已有 `ops.audit_event` 和 Workspace-scoped 有界读取（`internal/audit/application/ports.go:11`, `internal/audit/application/ports.go:19`; `internal/audit/adapter/postgres/store.go:85`, `internal/audit/adapter/postgres/store.go:113`），但产品要求的是“审计摘要”，尚未定义 summary projection、时间范围、事件类别、全局事件处理、actor/path 脱敏和 archive schema；M10-01 的全局 Audit/Secret Redaction 仍待开始（`.trellis/tasks/07-16-product-delivery/implement.md:63`）。
- 因而两个 JSON kind 都是**后续独立扩展**。它们不阻塞 AC-33，但最终 v1/R-08 仍需实现；不能以删除需求方式规避。

### 4. 已有 M9-03 合同

| 维度 | 已有合同与证据 | residual 必须保持 |
|---|---|---|
| Public kind | HTTP、Web、OpenAPI 均只公开 `MARKDOWN|METADATA_JSON`（`internal/export/http/handler.go:141`; `web/src/api/exports.ts:5`, `web/src/api/exports.ts:88`; `api/openapi/openapi.json:14568`） | 附件合同未设计前不扩大现有判别联合。 |
| Idempotency | 同 key exact replay 先于当前 Collection 校验（`.trellis/spec/backend/export-contract.md:24`, `.trellis/spec/backend/export-contract.md:34`） | 新 scope 的 request hash 也必须绑定 actor/capability/TTL/scope。 |
| Recovery | `Claim -> snapshot -> render -> create-only staging -> Prepare -> atomic promote -> Complete`（`.trellis/spec/backend/export-contract.md:39`） | prepared 后不能重读附件源并生成第二份结果。 |
| Lease/expiry | 所有 lifecycle CAS 使用数据库时间；TTL 只回收物理结果（`.trellis/spec/backend/export-contract.md:42`, `.trellis/spec/backend/export-contract.md:45`） | 过期 archive 返回 410，Job/Audit 保留。 |
| Permission | Auth capability matrix 对 create/get/list/download 均要求 `READ_LOCAL`（`internal/auth/http/handler.go:156`, `internal/auth/http/handler.go:167`, `internal/auth/http/handler.go:205`; `internal/auth/http/export_capability_test.go:12`, `internal/auth/http/export_capability_test.go:29`） | raw 附件下载不能降为普通 MASKED 请求。 |
| File verification | 下载前验证受控 path/symlink/hash/size（`.trellis/spec/backend/export-contract.md:49`） | ZIP 下载也必须验证 archive hash/size。 |
| Download audit | 统计与 actor-bound `export.download` 同事务，只说明服务端已准备返回（`.trellis/spec/backend/export-contract.md:49`, `.trellis/spec/backend/export-contract.md:51`） | 不声称客户端完整接收。 |
| Cleanup | 只删除 prepared staging/final；orphan sweep 不碰 prepared binding 或普通 Workspace 文件（`.trellis/spec/backend/export-contract.md:52`, `.trellis/spec/backend/export-contract.md:53`） | 源 `attachments/` 永不进入 cleanup namespace。 |
| Frontend recovery | REST 为事实源，2 秒轮询、SSE 只失效、Blob 下载校验（`.trellis/spec/backend/export-contract.md:76`, `.trellis/spec/backend/export-contract.md:81`, `.trellis/spec/backend/export-contract.md:84`） | 新 Settings UI 使用 Workspace-scoped Query，不复用 Collection cursor 假绑定。 |

### 5. 当前未提交补丁评估

#### 5.1 主会话提供的 Git 差异事实

研究角色按隔离规则没有执行 Git 命令。主会话于 2026-07-28 提供以下 HEAD/worktree 事实：

- dirty M9 路径：`internal/export/{domain/model.go,model_test.go,application/ports.go,render.go,service.go,service_test.go,adapter/localfs/store.go,store_test.go}`、`migrations/00036_export_hardening.sql`、`web/src/api/exports.{ts,test.ts}`，另有混合 `Makefile`、OpenAPI、`cmd/worker` 调整；
- HEAD `FileStore` 为 `Stage/Promote/Read + DeletePrepared(workspace,path,hash,size) + DeleteOrphan(workspace,path)`；补丁合并为无 hash/size 的 `Delete`；
- HEAD `Write/Promote` 使用 `root.Link` 实现 create-only；补丁改为 `Lstat -> Rename`；
- 被删除的负测包括 `TestStoreDeletePreparedPreservesMismatchedFile`、final conflict 内容保持断言、`TestM9OnlyAcceptsDeliveredKindsAndFields` 和 Web 对 evaluation field 的拒绝断言；
- 补丁新增/放宽 Evaluation/Audit kind/field 与 migration constraint，但 HTTP public kind 未增加，OpenAPI 只把两个 field 加入现有请求/响应，未形成真实新 kind API。

这些 Git 事实与当前文件内容相互吻合，但提交侧具体行号只能由主会话的 Git diff 复核。

#### 5.2 问题分类

| 项目 | 分类 | 证据与影响 | 处理建议 |
|---|---|---|---|
| `EVALUATION_JSON|AUDIT_JSON` 加入 domain valid kind | Spec drift / latent defect | Domain 接受四 kind（`internal/export/domain/model.go:16`, `internal/export/domain/model.go:308`），但 Application/HTTP 只接受两个（`internal/export/application/service.go:562`, `internal/export/application/service.go:563`; `internal/export/http/handler.go:141`）。Repository 或历史行可形成内部行为不一致。 | 恢复 HEAD 的 M9-only kind/field gate；未来各自有内容源与设计后再前向扩展。 |
| `evaluation|audit_summary` 加入现有 Metadata field | 公开缺陷 | Web/OpenAPI 允许客户端发送两个 field（`web/src/api/exports.ts:8`, `web/src/api/exports.ts:90`, `web/src/api/exports.ts:229`; `api/openapi/openapi.json:14572`, `api/openapi/openapi.json:14593`），HTTP 把 fields 原样传入 Service（`internal/export/http/handler.go:165`, `internal/export/http/handler.go:167`）。 | 立即拒绝这两个 field；不能用字段占位模拟独立导出。 |
| `{"available":false}` render | 假成功缺陷 | 所有非 Markdown kind 都走同一 Collection JSON envelope（`internal/export/application/render.go:56`, `internal/export/application/render.go:60`），两个 field 固定输出 unavailable（`internal/export/application/render.go:176`, `internal/export/application/render.go:179`）。成功 Job 会有 hash/size，却没有要求的内容。 | 删除占位；无内容源时返回 capability unavailable/非法请求，不产生 SUCCEEDED 文件。 |
| 修改 `00036` supported-kind 约束 | 迁移缺陷 / 发布风险 | 当前历史 migration 的 path constraint允许三个 JSON kind（`migrations/00036_export_hardening.sql:238`, `migrations/00036_export_hardening.sql:247`）；M9 兼容合同明确“只做前向 schema 扩展，不改已提交迁移历史”（`.trellis/tasks/archive/2026-07/07-26-m9-export/design.md:77`, `.trellis/tasks/archive/2026-07/07-26-m9-export/design.md:79`）。 | 不修改已提交 `00036`；用新的前向 migration 收紧/扩展并覆盖 upgrade、guarded Down。 |
| `DeletePrepared/DeleteOrphan -> Delete` | 安全/恢复回归 | `Delete` 没有 expected hash/size（`internal/export/application/ports.go:45`, `internal/export/application/ports.go:49`）；cleanup 无条件按路径调用（`internal/export/application/service.go:686`, `internal/export/application/service.go:694`）；Store 仅校验 regular file（`internal/export/adapter/localfs/store.go:315`, `internal/export/adapter/localfs/store.go:322`）。篡改/冲突 final 会被 cleanup 删除，破坏 fail-closed 和取证。 | 保留绑定删除与 orphan 删除两个深接口；prepared 删除必须验证 identity/hash/size，mismatch 返回一致性错误并保留文件。 |
| `Link -> Lstat + Rename` | 并发覆盖回归 | 目标不存在检查与 rename 间有 TOCTOU（`internal/export/adapter/localfs/store.go:208`, `internal/export/adapter/localfs/store.go:221`）。Go 1.25.4 官方 `os.Rename` 文档明确：若目标已存在且不是目录，Rename 会替换目标。当前顺序测试只验证预先存在冲突（`internal/export/adapter/localfs/store_test.go:138`, `internal/export/adapter/localfs/store_test.go:155`），未覆盖检查后竞争创建。 | 恢复 create-only link/等价 no-replace primitive；增加并发 barrier 测试，断言 winner 唯一且 loser/final/staging 都保留可解释状态。 |
| 删除负测 | 测试退化 | 当前 domain 只剩 3 个测试，无 M9 kind/field gate（`internal/export/domain/model_test.go:11`, `internal/export/domain/model_test.go:37`, `internal/export/domain/model_test.go:86`）；当前 Store conflict 测试不再断言 final 内容未被改写（`internal/export/adapter/localfs/store_test.go:152`, `internal/export/adapter/localfs/store_test.go:158`）。 | 先恢复删除的测试，再实现任何附件扩展。 |

#### 5.3 为什么当前绿灯不能接受补丁

本研究实际执行并通过：

```text
go test -race -count=1 -timeout 60s ./internal/export/...
node api/openapi/check.mjs
npm run typecheck --prefix web
npm run test --prefix web -- --run src/api/exports.test.ts src/features/collections/export-queries.test.ts src/features/collections/CollectionExportPanel.test.tsx
```

结果分别为 Go Export 全绿、OpenAPI checker 通过、TypeScript 通过、前端 3 files / 21 tests 通过。它们只证明代码与被同步放宽的测试/checker 自洽；由于关键负测已经删除，不能证明 create-only、mismatch preservation、附件内容或 AC-33。

### 6. 附件最小可验收合同

#### 6.1 范围

**纳入：**

- Workspace 根下固定 `attachments/` 的递归普通文件 archive；
- 标准 ZIP + 根级 `manifest.json`，schema 建议为 `attachment-export/v1`；
- Workspace-scoped 异步 Job、幂等、任务列表/详情、重启恢复、过期清理、下载审计；
- Settings 数据导出入口与刷新恢复；
- 空目录、正常二进制、多级目录、失败、过期和重新创建。

**不纳入：**

- 按 Smart Collection/Document 自动推导“关联附件”；
- Source PDF、`.knowledge/sources`、Artifact export、Git bundle；
- `EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX、通用字段映射；
- 附件执行/预览、外部目录、网络下载、自动解压；
- 无上限的大 archive 或流式 range 下载（如需支持，另做容量/协议设计）。

#### 6.2 Scope 与持久事实

当前 `domain.Scope` 和 `Job.Validate` 强制完整 Collection ID/version/query hash（`internal/export/domain/model.go:56`, `internal/export/domain/model.go:161`），数据库也强制 Collection FK（`migrations/00036_export_hardening.sql:196`, `migrations/00036_export_hardening.sql:204`）。因此不能只新增 `ATTACHMENT_ZIP` kind 并塞入 dummy Collection。

建议使用一个明确的 tagged scope：

```text
COLLECTION           -> collection_id/version/query_hash
WORKSPACE_ATTACHMENTS -> attachment_root_contract_version
```

并以新的前向 migration 增加互斥 CHECK。附件 Job 至少冻结：

- Workspace ID、scope kind、schema version、request hash/TTL/actor/capability；
- manifest digest、entry count、total uncompressed bytes；
- archive staging/final path、SHA-256、size；
- lifecycle/version/lease/cleanup/download facts。

manifest 内每个 entry 至少包含规范化相对路径、SHA-256、size；稳定按 UTF-8 byte order 排序。不得包含 Workspace 绝对路径、服务器 locator、数据库 ID 之外的内部路径、Secret 扫描结果正文或文件内容摘要。

#### 6.3 权限与脱敏

- Create/List/Get/Download 均要求当前 Workspace 的 `READ_LOCAL`；复用现有 capability matrix，但增加新路由的显式测试。
- raw 附件内容不可可靠“MASKED”。产品应显示这是原始文件导出，并把 request policy 定义为 `RAW_USER_OWNED` 或等价明确值；不得复用 `MASKED + include_sensitive=false` 假装二进制已脱敏。
- 本地 disabled-auth 模式可使用稳定 local actor；自托管必须 Session/API Token。Audit 必须记录真实 actor，不能落为无来源 `system`。
- 文件名属于用户数据；manifest/日志/Audit 默认只记录 entry count/bytes/archive hash，不记录完整路径列表。

#### 6.4 文件与 archive 边界

- 只从 canonical Workspace root 下的固定 `attachments/` 打开；拒绝根或任何祖先 symlink。
- 只接受 regular file；拒绝 symlink、hardlink 逃逸、device、socket、FIFO；打开后用 fstat/identity 再验证。
- ZIP entry 使用 slash-normalized relative path，拒绝绝对路径、`..`、空 segment、NUL、反斜杠逃逸和目录外目标；检测重复、大小写折叠和 Unicode normalization 冲突并显式失败。
- 设置显式 `max_entries`、单文件上限、uncompressed 总量和 archive 上限；超过返回稳定 error，不截断、不跳过。
- 扫描/打包期间源文件改变时 fail closed；不能生成“部分旧、部分新”却标记成功的 archive。
- 生成 archive 仍写 `.knowledge/exports/.staging`；发布必须 create-only，不能用会替换目标的 rename。

#### 6.5 恢复、过期和审计

恢复顺序建议沿用：

```text
Claim -> bounded attachment scan/hash -> deterministic ZIP staging
      -> Prepare(manifest/archive binding) -> create-only promote -> Complete
```

- Prepare 前崩溃：仅留下未绑定 staging，由 orphan sweep 删除。
- Prepare 后崩溃：只验证固定 ZIP binding，不重新读取 `attachments/`。
- 到期：返回 `410 EXPORT_EXPIRED`，删除匹配 hash/size 的 staging/final ZIP；源附件不动，Job/hash/manifest digest/download stats/Audit 保留。
- cleanup 遇到不匹配文件：记录 `EXPORT_RESULT_INCONSISTENT`/cleanup failure，保留冲突文件，不以“清理成功”覆盖现场。
- Create/Prepare/Complete/Expire/Cleanup 写 server event；成功准备下载时原子写 actor、Export ID、archive hash/size、entry count 和 `prepared_for_return` Audit。不得记录“客户端下载完成”。

#### 6.6 下载与 UI

- 成功响应：`application/zip`、受控 ASCII filename、`Content-Length`、`Cache-Control: private, no-store`、`X-Content-Type-Options: nosniff`；Job 提供 archive SHA-256/size。
- 非成功：未完成 409、过期 410、不一致 500、依赖不可用 503、权限 403；不返回 partial ZIP。
- 前端继续通过 `authFetch`/Blob，校验 Content-Type、Disposition、Length、Blob size 和 Job binding；禁止直链/新标签页绕过 401/410。
- Settings 展示 `PENDING/RUNNING/SUCCEEDED/FAILED/EXPIRED`、entry count/total bytes、创建/过期时间和重新创建。SSE 只失效 Workspace attachment-export query，终态来自 REST。

### 7. 后续扩展、当前缺陷与 M11 验收分工

| 项目 | 归类 | 所属时点 |
|---|---|---|
| Workspace 附件 archive、Settings UI、权限/恢复/过期/审计 | 当前功能缺口 | 本 residual 任务实现并关闭 AC-33。 |
| FileStore 绑定删除、create-only publish、被删负测 | 当前回归缺陷 | 在任何新能力前恢复/修复。 |
| Evaluation/Audit field 占位与 public allowlist 放宽 | 当前缺陷 | 从当前补丁移除；无内容源时 fail closed。 |
| `EVALUATION_JSON` 真正内容导出 | 后续扩展 | 等 M11-02 形成统一、持久、版本化评测结果源后单独设计。 |
| `AUDIT_JSON` 审计摘要导出 | 后续扩展 | 等 M10-01 全局 Audit/Secret Redaction 与 summary contract 完成后单独设计。 |
| AC-33 固定 E2E、全量 AC-01..36 矩阵、11 步演示 | M11 验收 | M11-01 验证，不能把实现推迟到验收阶段（`.trellis/tasks/07-16-product-delivery/implement.md:67`）。 |
| AI Eval 阈值/回归报告 | M11 验收与内容源建设 | M11-02（`.trellis/tasks/07-16-product-delivery/implement.md:68`）。 |
| 最终文档、SBOM、交付包与全量 review | M11 发布验收 | M11-03（`.trellis/tasks/07-16-product-delivery/implement.md:69`）。 |

### 8. 共享文件风险

1. **`migrations/00036_export_hardening.sql`：禁止继续改已提交 migration。** 新 scope/kind/constraint 必须放到新的前向 migration；否则 fresh install 与已升级数据库获得不同历史语义，且违反 M9 rollback contract（`.trellis/tasks/archive/2026-07/07-26-m9-export/design.md:79`; `.trellis/spec/backend/database-guidelines.md:1449`）。
2. **`api/openapi/openapi.json` / `api/openapi/check.mjs`：当前是混合大 diff。** 附件 API 必须以可审查的 additive schema/path/checker hunk进入；先移除 Evaluation/Audit field 漂移，避免把别的 OpenAPI 重排或 M10 改动一起提交。
3. **`cmd/worker/main.go`：共享 Composition 与 maintenance loop。** 当前同时包含 Timeline、Memory、Interview、Learning Path 等维护（`cmd/worker/main.go:464`, `cmd/worker/main.go:501`）。附件若复用 Export worker，应只扩展现有 typed dispatch；若新 worker，必须单独 readiness/registration，不覆盖其他 dirty hunk。
4. **`Makefile`：M11-03 也会修改的共享门禁文件。** 优先复用现有 Export/Browser 命令；需要新 target 时只加最小入口，并与当前混合改动拆分。
5. **`web/src/api/exports.ts`：Collection wire owner。** 不应把 Workspace attachment job 强塞进现有 Collection-only `ExportJob`。若共用文件，必须使用显式 discriminated scope 并让每个 decoder 验证自己的字段组合；更小风险是新增独立 `attachment-exports.ts` wire owner。
6. **`internal/export/adapter/localfs/store.go`：所有 Export 共用安全边界。** 先恢复 HEAD 的 create-only/绑定删除，再扩展 `.zip`；不能为了 archive 扩展把 `.md/.json` 的已验收语义一起放宽。
7. **产品文档与 `.trellis/spec/**`：当前仍准确描述 M9-03 两 kind。** 只有新附件合同经评审并通过后才更新“AC-33 complete”；Evaluation/Audit 仍标 deferred，不能随 enum 一起改状态。

### 9. 建议验证命令与必须新增的断言

#### 9.1 先恢复当前 M9 基线

```bash
go test -race -count=1 -timeout 60s ./internal/export/... ./internal/events/... ./internal/audit/... ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
node api/openapi/check.mjs
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
git diff --check
```

必须恢复/新增：

- `TestM9OnlyAcceptsDeliveredKindsAndFields`；
- Web/OpenAPI 拒绝 `evaluation|audit_summary|EVALUATION_JSON|AUDIT_JSON`；
- `TestStoreDeletePreparedPreservesMismatchedFile`；
- final conflict 保持原内容、staging 保持可恢复；
- barrier 控制的并发 publish，证明检查后竞争不能覆盖 winner。

#### 9.2 附件领域/Application/LocalFS

```bash
go test -race -count=1 -timeout 60s ./internal/export/domain ./internal/export/application ./internal/export/adapter/localfs
```

覆盖：空目录、任意二进制、嵌套路径、稳定 manifest/ZIP hash、entry/byte 上限、源文件扫描中变化、symlink/hardlink/device/FIFO/socket、Zip Slip、重复/大小写/Unicode 冲突、绝对路径和 Secret 不进入控制面、prepared replay 不重读源、mismatch cleanup 保留现场。

#### 9.3 PostgreSQL/migration/River

```bash
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/export/adapter/postgres
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s -run '^(TestExport|TestM9|TestAttachment)' ./internal/platform/migration
```

覆盖：新 migration fresh/repeat/upgrade/guarded Down、Collection/Workspace scope 互斥、同 key response-loss、跨 Workspace、DB-time lease/TTL、双 Worker、Prepare/promote/Complete crash window、过期和 cleanup retry、download+Audit 原子性、attachment root/source 永不被 cleanup SQL 或 FileStore 删除。

#### 9.4 HTTP/Auth/OpenAPI/Web/Browser

```bash
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/export-browser-smoke.sh
```

Browser smoke 需新增真实 Settings 附件导出：创建、刷新/服务重启恢复、下载并解包验证 manifest 与每个字节 hash、权限不足、跨 Workspace、unsafe entry、源变化失败、结果篡改、过期 410、cleanup 后源附件仍存在、失败/过期以新 key 创建、桌面/390x844、键盘、focus、overflow、console/network 无异常。

### 10. Files Found

- `docs/product/PRD.md`：AC-33、Settings 数据导出、Workspace 附件目录和用户故事事实源。
- `docs/architecture/data-architecture.md`：Workspace/attachments/.knowledge/exports 所有权与 cleanup 边界。
- `.trellis/tasks/07-16-product-delivery/{prd.md,design.md,implement.md}`：父任务 R-08、M9/M10/M11 分工。
- `.trellis/tasks/archive/2026-07/07-26-m9-export/{prd.md,design.md,implement.md,research/current-state.md}`：已提交 M9-03 范围、恢复协议与完成证据。
- `.trellis/spec/backend/export-contract.md`：当前两 kind 的 durable/public/security/download/cleanup 单一合同。
- `.trellis/spec/backend/database-guidelines.md`：`ops.export_job`、前向 migration、Audit/cleanup 数据合同。
- `.trellis/spec/frontend/{type-safety,state-management,quality-guidelines}.md`：Collection Export wire、恢复和浏览器门禁。
- `internal/export/domain/model.go`：当前 worktree kind/field/Job/Collection scope。
- `internal/export/application/{ports.go,render.go,service.go}`：Collection snapshot、占位 field、FileStore 与生命周期编排。
- `internal/export/adapter/localfs/{store.go,store_test.go}`：当前 rename/delete 文件语义及已缩弱测试。
- `migrations/{00034_learning_ops_auth.sql,00036_export_hardening.sql}`：当前四 kind base schema 与 M9 prepared binding。
- `internal/export/http/handler.go`, `internal/auth/http/handler.go`：公开两 kind、下载头、actor/capability 边界。
- `web/src/api/exports.ts`, `api/openapi/{openapi.json,check.mjs}`：当前公开 kind 仍为两个，但 fields 已被 worktree 放宽。
- `internal/audit/**`, `eval/**`：Audit 有持久查询源；Evaluation 只有离线报告，无统一持久结果源。

### 11. Related Specs

- `.trellis/spec/backend/export-contract.md:7`：任何 Export/LocalFS/Job/API/UI 修改必须应用。
- `.trellis/spec/backend/database-guidelines.md:1420`：M9 Export persistence/recovery。
- `.trellis/spec/backend/quality-guidelines.md:431`：Export fault/security/browser quality gate。
- `.trellis/spec/backend/auth-security.md`：Workspace、Capability、Secret 与文件边界。
- `.trellis/spec/frontend/type-safety.md:301`：Export 严格 wire boundary。
- `.trellis/spec/frontend/state-management.md:197`：Export REST/SSE/Query state ownership。
- `.trellis/spec/frontend/quality-guidelines.md:278`：Export frontend/browser gate。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：新增 kind/scope 必须同步 Domain/DB/HTTP/OpenAPI/Web/Test/Docs。

### 12. External References

- Go 1.25.4 标准库 `os.Rename` 官方文档（本机 `go doc os.Rename`）：目标已存在且不是目录时会被替换；非 Unix 即使同目录也不保证原子。这直接否定 `Lstat -> Rename` 作为 create-only primitive。
- Repository module version：`go.mod:3` 指定 Go `1.25.4`；本机 `go version go1.25.4 darwin/arm64`。

## Caveats / Not Found

- 当前 residual `prd.md` 仍是 TBD（`.trellis/tasks/07-28-m9-export-residuals/prd.md:3`, `.trellis/tasks/07-28-m9-export-residuals/prd.md:13`）；本研究应先用于补 PRD/design/implement，不能直接当作已批准设计。
- Trellis researcher 角色禁止 Git 操作；HEAD/worktree 差异来自主会话提供，当前文件行号由本研究直接读取复核。提交侧精确 hunk 仍应由主会话在整合时检查。
- 未发现 Attachment 领域模型、数据库表、稳定关联、公开 API、前端 client 或真实 fixture；“Workspace 级 ZIP + manifest”是最小建议，不是既有需求原文。
- 未发现持久 Evaluation Run/Result 数据源；只发现离线 CLI Report。`AUDIT_JSON` 虽有底层 Audit Store，但“摘要”投影和导出选择合同未找到。
- 本研究未运行真实 PostgreSQL migration/integration、River、Compose 或浏览器 smoke；仅运行了当前 Export Go race、OpenAPI checker、Web typecheck 和 3 个定向 Vitest 文件。当前绿灯因负测被删而存在明确盲区。
- 当前 `migrations/00034_learning_ops_auth.sql:281` 历史基表本就列出四 kind；已提交 M9-03 通过后续约束/Application/Public gate 只交付两个。未来修复必须用前向 migration 保持 fresh/upgrade 一致，不能回改历史文件来“清理”。
