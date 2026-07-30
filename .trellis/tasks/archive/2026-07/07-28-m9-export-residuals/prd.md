# 收口 M9 Export 附件与 AC-33

## Goal

在保持已提交 M9-03 Smart Collection `MARKDOWN|METADATA_JSON` 导出合同不退化的前提下，恢复当前未提交 Export 改动削弱的 create-only 发布、prepared binding 删除和公开类型门禁，并交付从 Settings 发起的 Workspace 级 `ATTACHMENTS_ZIP` 异步导出。用户应能下载固定 `attachments/` 目录中普通文件的确定性 ZIP 与版本化 manifest，刷新或服务重启后继续追踪任务；过期与清理只能删除生成物，绝不能改写或删除源附件。

本任务完成后，以真实 API、Worker、PostgreSQL、文件系统和浏览器证据关闭 AC-33 的附件缺口。`EVALUATION_JSON`、`AUDIT_JSON` 及 `evaluation|audit_summary` 没有正式内容源，继续作为独立后续能力，不得以 `{"available":false}` 或空成功文件冒充交付。

## Confirmed Facts

- 已提交 M9-03 只公开 Smart Collection `MARKDOWN|METADATA_JSON`，其幂等、prepared-result 恢复、权限、过期清理、下载审计和浏览器闭环是本任务必须保持的基线。
- AC-33 要求 Markdown、附件和领域元数据可迁移导出；现有 Markdown 与 Metadata JSON 已交付，真实附件字节是唯一未关闭项。评测结果和审计摘要属于父任务 R-08 的其他能力，不属于 AC-33 关闭条件。
- 仓库没有 Attachment 表、稳定附件 ID 或 Collection/Document 到附件的关联事实。架构唯一稳定边界是 Workspace 根下固定 `attachments/`，因此本任务导出整个 Workspace 附件目录，不推导“关联附件”。
- 当前未提交 Export 改动包含阻断级回归：`Lstat -> Rename` 不能保证 create-only，通用无绑定 `Delete` 会失去 prepared hash/size 保护，且关键负测被删除。
- 当前未提交改动放宽了 `EVALUATION_JSON|AUDIT_JSON` 和 `evaluation|audit_summary`，但渲染内容只是 unavailable 占位；该行为不能形成成功导出。
- `migrations/00036_export_hardening.sql` 已提交，禁止修改。任何 scope/kind/约束扩展必须使用实施时下一个未占用编号的新前向 migration。

## In Scope

- 恢复并锁定 M9-03 基线：真正 create-only 的 staging promote、带 expected SHA-256/size 的 prepared 删除、独立 orphan 删除、冲突现场保留和对应并发/安全负测。
- Domain、Application、HTTP、OpenAPI 与 Web 继续拒绝未交付的 `EVALUATION_JSON|AUDIT_JSON` 和 `evaluation|audit_summary`；删除 unavailable 占位渲染路径，不生成假成功文件。
- 新增 tagged scope：`COLLECTION` 继续绑定 Collection ID/version/query hash；`WORKSPACE_ATTACHMENTS` 只允许 `ATTACHMENTS_ZIP`，绑定固定附件根合同而不携带 dummy Collection。
- 从 canonical Workspace 根下固定 `attachments/` 递归读取普通文件，生成标准 ZIP、根级 `manifest.json` 和 `attachment-export/v1` schema。
- Workspace 级创建、精确幂等重放、有界列表、详情、下载、River 执行、租约、prepared binding、重启恢复、过期清理、Server Event 与 append-only 下载 Audit。
- Create/List/Get/Download 全部要求当前 Workspace 的 `READ_LOCAL`。附件内容按 `RAW_USER_OWNED` 明确为原始用户数据，不用 `MASKED` 假装二进制已脱敏。
- Settings 数据导出入口、严格 wire decoder、Workspace-scoped Query、2 秒有界轮询、SSE 定向失效、Blob 下载、失败/过期后新建和桌面/移动真实浏览器验收。
- 新前向 migration、OpenAPI、专项 spec、产品/架构状态和浏览器 smoke 同步；完成 Go、SQL、通用及前端审查。

## Out Of Scope

- 按 Smart Collection、Document 或 Markdown 链接推导关联附件；附件领域表、逻辑删除、预览或执行。
- 可配置或 Workspace 外部附件目录、网络下载、自动解压、Source PDF、`.knowledge/sources`、Artifact export 或 Git bundle。
- `EVALUATION_JSON`、`AUDIT_JSON`、审计摘要 projection、CSV/XLSX、通用字段映射和公式字段。它们仍是父任务后续项，不能从需求中删除。
- 流式 Range 下载、无上限大归档、后台压缩服务或新的对象存储。
- 修改已提交的 `00036`、清理普通 Workspace 文件、改写源附件、用 Down migration 回滚生产事实。
- M11 的全量 AC-01..36 矩阵、11 步最终演示、AI Eval 总门禁和最终发布包；本任务只提供 AC-33 可复用的真实闭环证据。

## Requirements

1. **先恢复安全基线。** `Promote` 必须使用 hard-link 或等价 no-replace primitive 实现 create-only；目标冲突时不得覆盖 winner，staging 保持可解释。prepared cleanup 必须验证 Workspace、受控 path、expected hash 与 size；不匹配时保留文件并报告不一致。orphan 删除继续是只面向严格 staging namespace 的独立能力。
2. **拒绝假能力。** Collection 的 kind 仍只有 `MARKDOWN|METADATA_JSON`，fields 仍只有已交付白名单。`EVALUATION_JSON|AUDIT_JSON|evaluation|audit_summary` 在 Domain/Application/HTTP/OpenAPI/Web 一致 fail closed，且没有 `available:false` 成功渲染。
3. **Scope 必须可判别。** `COLLECTION` 要求完整 Collection binding 且禁止附件字段；`WORKSPACE_ATTACHMENTS` 禁止 Collection ID/version/query hash，只允许 `ATTACHMENTS_ZIP`、`attachment-export/v1`、`workspace-attachments/v1` 与 `RAW_USER_OWNED`。数据库以互斥 CHECK 和 FK/NULL 组合保护同一规则。
4. **迁移只能前进。** 新 migration 将既有 Job 回填为 `COLLECTION`，增加 attachment scope/result facts 和互斥约束；覆盖 fresh、repeat、从现有 `00036` 数据升级、空数据 Down→Up 及存在 attachment Job 时 SQLSTATE `55000` guarded Down。不得改写 `00036`。
5. **幂等请求必须冻结意图。** request hash 至少绑定 Workspace、scope、kind、schema/root contract version、raw policy、actor/capability、TTL 和限制合同版本。同 `(workspace_id,idempotency_key)` exact replay 先于当前附件目录检查；相同 key 不同规范请求稳定 `409`，过期 Job 也不复用原 key。
6. **附件边界必须 fail closed。** 从已验证 Workspace root directory handle 开始，使用 fd-relative `openat`/`os.Root` 等价的逐组件 no-follow 遍历；不得先解析绝对路径再 reopen。只接受 canonical `attachments/` 下的 regular file；拒绝根/祖先 symlink、symlink、link count 大于 1 的 hardlink、device、socket、FIFO、无效 UTF-8、越界与 Zip Slip path。检测规范化、大小写折叠和 Unicode normalization 冲突；目录不存在显式失败，存在但为空可成功导出空 manifest。
7. **上限必须显式且不截断。** v1 固定最多 10,000 个文件、单文件 256 MiB、总未压缩字节 1 GiB、ZIP 结果 1 GiB；任一超限返回稳定失败，不跳过、不部分成功。后续调整必须升级限制/归档合同版本。
8. **ZIP 必须确定。** `manifest.json` 记录 Workspace ID、schema/root contract version、entry count、total bytes，以及按规范化相对路径 UTF-8 byte order 排序的 `{path,sha256,size}`。payload 位于 `attachments/<relative-path>`；采用固定 entry 顺序、`zip.Store`、固定时间/权限、无注释和无源扩展字段，相同冻结输入产生相同 manifest digest 与 ZIP SHA-256。
9. **源变化不能产生成功。** 扫描、hash 和写 ZIP 时必须验证打开后的文件 identity/类型/size 与读取结果；任一文件在过程中新增、删除、替换或改变均使任务显式失败。失败 staging 可由 orphan sweep 回收，源 `attachments/` 从不进入 Export cleanup namespace。
10. **恢复沿用 prepared 协议。** 执行顺序为 `Claim -> bounded scan/hash -> deterministic ZIP staging -> Prepare(manifest/archive binding) -> create-only promote -> Complete`。Prepare 后恢复只验证固定 manifest digest、entry count/bytes、staging/final path、archive hash/size，不重读附件源或生成第二份结果；所有租约与 TTL fence 使用数据库时间。
11. **过期和下载保持可追踪。** 到期返回 `410 EXPORT_EXPIRED`；cleanup 只删除 hash/size 匹配的 staging/final ZIP，失败保留重试事实。Job、manifest/archive digest、统计和 Audit 保留。下载前重验 path/symlink/hash/size，统计与 `export.download` Audit 同事务记录真实 actor、Export ID、hash/size、entry count 和 `prepared_for_return`，不声称客户端已收完。
12. **公开接口必须独立且严格。** 新增 Workspace attachment-export 创建/列表/详情/下载路由，避免 dummy Collection 和 Collection cursor；请求/响应/OpenAPI 使用 tagged DTO，拒绝未知/重复字段及非法状态组合。ZIP 下载返回 `application/zip`、受控 ASCII filename、Content-Length、`private, no-store` 和 `nosniff`。
13. **Settings 以 REST 为事实源。** 新 wire owner 从 `unknown` 严格解码；Query key 绑定 Workspace、scope 与 cursor。刷新/重启后从列表/详情恢复，活动态每 2 秒轮询，SSE 只触发当前 Workspace attachment-export Query 失效，终态停止。下载经 `authFetch` 校验 headers、Blob size 与 Job binding，不使用直链。
14. **完成声明必须有真实证据。** 浏览器 smoke 必须创建真实二进制/嵌套附件，刷新和服务重启恢复，下载后解包验证 manifest 与逐文件 hash；同时覆盖权限不足、跨 Workspace、unsafe entry、扫描中源变化、篡改结果、过期 410、cleanup 后源附件原样存在、失败/过期新 key、桌面与 390x844、键盘/焦点/overflow 及 console/network。
15. **新 union 必须显式激活。** migration 后持久 capability gate 默认关闭；兼容 release 先让所有 API/Worker 能读取 tagged scope、让 Collection 查询显式过滤 `COLLECTION`，但不得创建/claim attachment Job。只有确认全部实例支持同一 attachment contract version 后才原子启用；混部、回滚或 gate 关闭时创建返回 unavailable，maintenance 不领取 attachment scope。

## Acceptance Criteria

- [x] M9-03 回归门禁恢复：并发 promote 只有一个 winner，既有 final 内容不被覆盖；prepared hash/size mismatch 文件保留；orphan 删除不能触碰 prepared binding 或普通 Workspace 文件。
- [x] Domain/Application/HTTP/OpenAPI/Web 一致拒绝 `EVALUATION_JSON|AUDIT_JSON|evaluation|audit_summary`，仓库中不存在将这些请求渲染为 `{"available":false}` 的成功路径。
- [x] 新前向 migration 在不修改 `00036` 的情况下回填既有 Collection Job，并以数据库约束证明 `COLLECTION` 与 `WORKSPACE_ATTACHMENTS` 字段互斥、kind 匹配；fresh/repeat/upgrade/guarded Down 三轮真实 PostgreSQL 通过。
- [x] 持久 attachment capability gate 默认关闭；旧/新 API 与 Worker 混部时不能产生或领取 attachment Job，Collection list/get/recovery 不读取错 scope；所有新实例就绪后启用才可创建，关闭 gate 能立即阻止新建/claim 且不删除既有事实。
- [x] 相同 Workspace/key/规范请求的并发与 response-loss 重试只返回同一 attachment Job；不同 payload/TTL 冲突，过期 exact replay 不创建第二个 Job，跨 Workspace 不可枚举。
- [x] 空 `attachments/`、嵌套目录、任意二进制均生成可被标准工具解压的确定性 ZIP；重复输入的 manifest 与 archive hash 一致，manifest entry 的 path/hash/size 与原始字节逐项一致。
- [x] missing root、symlink/hardlink/device/FIFO/socket、Zip Slip、重复/大小写/Unicode path 冲突、数量/大小超限和扫描中源变化全部显式失败，不产生可下载 partial ZIP，也不泄漏绝对路径或正文。
- [x] staging/Prepare/promote/Complete 各崩溃窗口、租约接管和服务重启均最多形成一个权威 archive；Prepare 后恢复不读取已改变的源附件，旧 lease owner 与过期 Job 无法提交成功。
- [x] Create/List/Get/Download 均要求当前 Workspace `READ_LOCAL`；Session/API Token/disabled local actor、CSRF/Origin、权限不足和跨 Workspace负测通过。原始附件策略明确为 `RAW_USER_OWNED`。
- [x] 过期下载返回 `410`，结果篡改返回不一致且不增加下载统计；cleanup 仅删除匹配的生成 ZIP，删除失败可重试，源附件在成功、失败、过期和 cleanup 后字节与 metadata 均未被修改。
- [x] 每次服务端准备成功下载都原子增加统计并追加 actor-bound Audit；Audit/Server Event/Problem/日志不包含绝对路径、附件正文或完整文件名列表，也不宣称客户端已接收完成。
- [x] Settings 可创建、刷新/重启恢复、查看全部状态、下载和失败/过期后新建；严格 decoder、Query cache、轮询/SSE 和 Blob 校验测试通过，Collection Export UI/路由行为保持不变。
- [x] 真实 API/Worker/PostgreSQL/Vite 浏览器验收在桌面和 `390x844` 完成下载解包与逐字节验证，无横向溢出、console warning/error 或异常网络请求。
- [x] 定向 Go race、真实 PostgreSQL migration/repository、OpenAPI、前端 lint/typecheck/test/build、`git diff --check` 通过；Go、SQL、通用及前端审查无未解决的当前范围缺陷。
- [x] Export spec、产品/架构文档和父任务状态仅在上述证据通过后标记附件已交付、AC-33 已关闭；Evaluation/Audit 内容导出仍准确列为后续项。

## Verification Evidence

- Export 受影响 Go 包全量 `-race`、`go vet`、OpenAPI、`go mod tidy -diff` 与 `git diff --check` 通过。
- 真实 PostgreSQL 中 6 个 Export Repository 用例和 9 个 M9 migration 用例分别以
  `-race -count=3 -p 1 -timeout 60s` 通过；聚合三轮命令仅因总迁移时间超过包级 60 秒而拆分，无业务断言失败。
- 前端 lint/typecheck、78 个测试文件 814 个用例和 production build 通过。
- `deploy/export-browser-smoke.sh` 通过 fresh migration、真实 API/Worker/Vite、刷新/重启恢复、ZIP 解包/hash、
  权限/跨 Workspace/unsafe/源变化/tamper/expiry/cleanup、桌面与 390x844 验收。
- Go、SQL、通用和前端质量审查无剩余 Critical/Required finding；`00036_export_hardening.sql` 与 HEAD 一致。

## Risks And Deferred Items

- 固定 `attachments/` 是当前架构中唯一可验证的 owner；PRD 中可配置附件目录仍由 Workspace 配置的独立任务处理，本任务不得读取 Workspace 外路径。
- `zip.Store` 以结果体积换取跨重试的稳定字节；v1 上限同时保护 Worker、磁盘和浏览器 Blob。需要更大或压缩归档时应升级 archive contract 并补容量设计。
- 前向 migration、OpenAPI、Worker Composition 和现有 Export LocalFS 是共享文件；实施时由主 Agent 协调迁移编号并逐 hunk 整合当前脏工作树，禁止整文件覆盖或混入其他模块。
- `EVALUATION_JSON` 等统一持久评测结果源形成后再单独设计；`AUDIT_JSON` 等 M10-01 完成全局 Audit/Secret Redaction 与摘要投影后再单独设计。
- M11-01 仍负责把本任务证据纳入全量 AC 矩阵和最终 11 步演示；M11-03 负责最终发布包与全量文档审计。
