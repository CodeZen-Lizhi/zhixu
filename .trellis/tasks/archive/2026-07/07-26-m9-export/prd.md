# M9-03 Smart Collection 异步导出闭环

## Goal

让用户在 Smart Collection 详情中创建可恢复的异步 Markdown 或领域元数据 JSON 导出，刷新或服务重启后仍能追踪任务、下载经过校验和脱敏的结果，并在过期后可靠拒绝访问和回收物理文件。PostgreSQL 必须保存完整任务事实，River 只负责运输，任何失败或重放都不能制造第二个结果或把不一致内容伪装为成功。

## Confirmed Facts

- M7-03 已交付 Smart Collection 的 LIST/TABLE/COMPACT_CARD 详情与版本化 Query；M9-03 只补正式导出闭环，不重做 Collection 浏览能力。
- 工作区已有 `internal/export/**`、`migrations/00036_export_hardening.sql`、API/Worker/Router/Auth 基础接线，但严格幂等、落盘后崩溃恢复、后台过期清理、append-only 下载审计、OpenAPI 和前端仍未闭环。
- 产品 AC-33 要求 Markdown、附件和领域元数据可导出。本任务交付 Collection Markdown 与领域元数据 JSON；附件导出仍未交付，因此本任务完成后不得宣称 AC-33 全部关闭。
- 当前正式范围没有 Excel/CSV 导入导出、通用公式字段或字段映射；`EVALUATION_JSON`、`AUDIT_JSON` 也没有可作为正式内容源的实现。

## In Scope

- Smart Collection 的 `MARKDOWN`、`METADATA_JSON` 异步导出；任务绑定 Workspace、Collection ID/version、query hash、首次执行冻结的 read-model revision、exact count、字段白名单、schema version、脱敏策略和 TTL。
- 创建、精确重放、按 Collection 分页列表、任务详情、结果下载；首次创建返回 `202`，完全相同的 Idempotency-Key 请求返回原任务，不同请求稳定冲突。
- PostgreSQL 任务状态机、River 投递与重启恢复、有效租约、prepared result、原子文件写入、hash/size 校验、过期和孤儿文件清理。
- 默认 `MASKED`；未脱敏敏感导出只允许具有 `READ_LOCAL` 的认证 Session/API Token 主体。Secret、绝对路径和电子表格危险前缀不得泄漏或成为可执行公式。
- 下载次数、下载时间、actor、Export ID、结果 hash 和服务端返回结果的 append-only 审计；不能把客户端是否完整接收文件包装成已知事实。
- Collection 详情中的导出面板、严格 API decoder、刷新恢复、有界轮询、SSE 定向失效、下载和失败/过期状态。
- HTTP/OpenAPI、迁移、真实 PostgreSQL/River、API/Worker/Vite 浏览器 smoke、桌面与移动端、文档和独立审查。

## Out Of Scope

- CSV、XLSX、Excel 导入导出、通用字段映射、公式字段和附件打包。
- Artifact Markdown 导出；它继续由 M8 Artifact 自有事实和接口负责。
- `EVALUATION_JSON`、`AUDIT_JSON` 的真实内容导出。
- 独立 Export 路由/导航、前端敏感字段编排、浏览器持久化任务/cursor、用户取消或人工重投端点；`CANCELLED` 仅作为历史/运维事实兼容读取和展示，本任务不新增其产生入口。
- 把导出结果写入 Artifact、Review、正式 Document、Git、Index 或默认 RAG。

## Requirements

1. 导出请求必须精确绑定规范化请求内容和 Idempotency-Key。已有任务先于 Collection 当前态校验返回；即使首次响应丢失后 Collection 已变化，相同请求仍返回原任务。过期任务也不复用原 key 创建新任务。
2. PostgreSQL 是 Job、租约、冻结快照、prepared result、终态、清理和下载统计的唯一事实源。River 投递失败只能留下可恢复的 `PENDING`，River Job 不得成为第二状态源。
3. Worker 首次执行必须读取同一 Collection version/query hash 的完整 durable scan，冻结 read-model revision 与 exact count；超过 10,000 项必须显式失败，禁止截断。后续恢复不得重新解释已冻结的可变 Collection。
4. 文件与数据库之间必须有可恢复协议：prepared binding 同时保存 staging/final path、hash 和 size，重试只验证并完成该固定结果；旧租约执行者不得提交；到期任务及崩溃遗留 staging/orphan 文件必须可由后台清理。
5. `Get`、`List` 和后台 sweep 必须把到期任务归约为 `EXPIRED`。下载在到期后返回 `410`；物理删除失败保留可重试清理事实，不能静默丢失 Job/hash/history。
6. 所有读取和下载按 Workspace 隔离并使用当前请求授权。默认导出不暴露敏感正文、绝对路径或 Secret；`FULL + include_sensitive` 同时要求认证主体和 `READ_LOCAL`，前端首版不提供该开关。
7. 成功下载前必须重新校验文件路径、symlink 边界、hash 和 size；统计更新与 append-only 下载审计在同一 PostgreSQL 事务完成，并记录当前 actor 与结果绑定。
8. 公共 API 必须提供严格 JSON、受限 body、稳定 Problem Details、`collection_id` 列表过滤和 opaque cursor；下载响应提供安全 Content-Type/Disposition/Length、`private, no-store` 和 `nosniff`。
9. 前端只消费严格解码并绑定当前 Workspace/Collection/version/query hash 的 Job。`PENDING/RUNNING` 使用 2 秒有界轮询并响应 SSE，终态停止轮询；刷新后从服务端列表/详情恢复。
10. UI 必须明确展示 `PENDING`、`RUNNING`、`SUCCEEDED`、`FAILED`、`EXPIRED`、历史兼容的 `CANCELLED` 和 `dispatch_pending`；失败/过期使用新 Idempotency-Key 创建新任务，创建响应丢失重试保留原 key。

## Acceptance Criteria

- [x] 在 Collection 详情创建 Markdown 或 Metadata JSON 导出，首次请求返回 `202`；相同 key + 相同规范请求并发或响应丢失重试只得到同一 Job，Collection 后续变化不阻断重放，不同 payload/TTL 返回稳定 `409`。
- [x] 真实 PostgreSQL + River Worker 完成 `PENDING -> RUNNING -> SUCCEEDED`，结果包含 `export/v1`、Workspace/Collection binding、frozen read-model revision、exact count、字段/脱敏信息，下载内容与 Job 的 SHA-256/size 完全一致。
- [x] 在 staging 写入前后、prepared binding 前后、文件提升后和 DB Complete 前注入崩溃，重启后均只得到一个权威结果；租约超时双 Worker 中旧 owner 无法 Prepare/Complete/Fail；Prepare 后到期不能提交成功。
- [x] Collection version/query hash/read-model revision 漂移、跨 Workspace、超过 10,000 项、文件 hash/size 不一致均 fail closed，不返回部分成功或截断结果。
- [x] 无认证主体的敏感导出、缺少 `READ_LOCAL`、CSRF/Origin/API Token 能力不符均被拒绝；Secret、绝对路径和公式前缀 canary 不出现在 DB、文件、Problem 或日志中。
- [x] `Get`/按 Collection `List`/后台 sweep 可观察到过期归约；过期下载返回 `410`，prepared-but-unpromoted staging、已提升 final 和无引用 orphan 文件最终物理删除，删除失败可重试且保留事实。
- [x] 每次服务端准备并返回成功下载都原子增加统计并追加一条含 actor、Export ID、hash 和 outcome 的 Audit；并发下载不丢计数，审计记录不可更新或删除。
- [x] OpenAPI 与 Router 完整映射创建、列表、详情和下载；前端 decoder 拒绝未知/重复字段、非法枚举/UUID/hash/time、跨绑定和状态字段冲突。
- [x] Collection 导出面板可刷新恢复，SSE `export.*` 定向失效查询，轮询在终态停止；桌面和 390x844 移动端可键盘创建/刷新/下载，无横向溢出和 console error/warning。
- [x] 真实 API/Worker/Vite smoke 完成创建、运行、成功下载、失败后新建、过期后新建；Go race、PostgreSQL migration/integration、OpenAPI、前端 lint/typecheck/test/build 和独立复审通过。
- [x] 产品与架构文档准确标记 M9-03 已交付的 Collection Markdown/Metadata JSON 范围，并明确 AC-33 的附件部分仍未关闭。

## Risks And Deferred Items

- read-model revision 在 Worker 第一次成功准备结果时冻结，而不是在 HTTP 创建事务中物化最多 10,000 条内容；prepared binding 前崩溃可重新读取，prepared binding 后不得重新读取。这避免把大快照写入创建请求，同时保证一旦建立结果事实就可精确恢复。
- Worker 执行超时必须严格短于租约并保留安全余量；Prepare/Complete/Fail 仍以 PostgreSQL 当前时间校验租约，不能信任进程时钟。
- 任务历史与 Audit 保留；TTL 只回收导出文件，不删除任务或审计事实。
- 附件、Evaluation 和 Audit 内容导出需要各自的来源、权限和打包合同，留给后续独立任务。
