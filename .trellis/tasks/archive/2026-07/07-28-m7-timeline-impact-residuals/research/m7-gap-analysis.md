# Research: M7 Timeline/Impact residual gap analysis

- Query: 识别 M7 Knowledge Timeline / Impact Analysis 在 M8-M10 已交付后仍缺失的产品与工程闭环，给出最小安全范围、影响文件、关键契约、验证方式和不可越界项。
- Scope: internal
- Date: 2026-07-28

## Findings

### 1. 结论

M7 后端不是待重做的骨架。现有实现已经具备 append-only Timeline、持久 Outbox/Worker、Workspace-bound cursor、只读 Impact Report、原子 `IMPACT_ANALYZED` Audit、四个认证 API 和严格 OpenAPI；残余工作集中在三类：

1. 可直接收口：真实 Timeline/Impact Web 工作台、过滤/分页/详情/分析/报告、现有可证明关联的跳转。
2. 需要 additive 后端扩展：Artifact 与 Review Card 的只读 Impact、`ARTIFACT_GENERATED` / `REVIEW_CARD_INVALIDATED` 一等 Timeline 事件，以及 UI 所需的稳定操作者/导航契约。
3. 不能在本任务里伪造：正式 downstream update Proposal、Document owner、AI Evaluation impact、双 Revision 版本比较、全局 Audit/OTel/Metrics/POISONED 运维入口。它们分别需要新的 owner 语义或仍属于 M10/M11。

当前任务 PRD 仍是 `TBD`，没有冻结上述取舍（`.trellis/tasks/07-28-m7-timeline-impact-residuals/prd.md:3-13`）。实施前必须先把“本次只交付 UI + owner-backed read impact，还是同时新增正式 Proposal 类型”写成可验收范围；否则无法诚实宣布 M7 残余全部关闭。

### 2. 已存在且应复用的基线

| 能力 | 已验证事实 | 证据 |
| --- | --- | --- |
| 公共 API | Timeline 列表/详情、Impact 分析、报告详情四端点已存在 | `internal/knowledge/http/handler.go:62-67`; `api/openapi/openapi.json:4569`; `api/openapi/openapi.json:4682`; `api/openapi/openapi.json:4718`; `api/openapi/openapi.json:6004` |
| 领域模型 | Event、filter、keyset position、Impact Report/Draft 都有稳定类型和上限 | `internal/knowledge/domain/timeline.go:19-35`; `internal/knowledge/domain/timeline.go:38-211` |
| 只读边界 | Impact Application 只读正式事实并保存报告；Draft 不是执行命令 | `internal/knowledge/application/impact.go:18-35`; `internal/knowledge/application/impact.go:57-64`; `internal/knowledge/application/impact.go:85-178` |
| 生产接线 | API composition、Router readiness 与 `knowledge_timeline` capability 已接线 | `cmd/api/main.go:466-503`; `internal/app/router.go:227-232`; `internal/app/router.go:321-342` |
| 投影可靠性 | 持久 Outbox、短事务 projector、Worker 启动和周期 dispatch 已存在 | `migrations/00037_timeline_impact_hardening.sql:197-246`; `cmd/worker/main.go:285-286`; `cmd/worker/main.go:377-380` |
| 权限 | GET 走 `READ_LOCAL`，Impact POST 走 `WRITE_PROPOSAL`；匿名/权限不足已有集成证据 | `internal/auth/http/handler.go:144-177`; `internal/auth/http/handler.go:229`; `cmd/api/timeline_impact_integration_test.go:253-308` |
| OpenAPI 门禁 | 路由、状态码、Problem、cursor/filter bounds、空 body、幂等键和对象 enum 被 checker 锁定 | `api/openapi/check.mjs:267-306`; `api/openapi/check.mjs:1446-1463` |
| 既有 smoke | 三个真实 PostgreSQL/Worker target 已存在 | `Makefile:128-140` |

当前 production Impact 查询只返回 Relation、Conflict、Health Issue，而且三个分支都固定 `requires_proposal=false`（`internal/knowledge/adapter/postgres/timeline.go:183-214`）。`draftsForReport` 只为 `RequiresProposal=true` 的对象派生不可执行草稿，因此生产响应目前必然没有 Draft（`internal/knowledge/application/impact.go:225-236`）。这是正确的 fail-closed 基线，不应通过前端假按钮绕过。

### 3. 正式产品缺口

产品基线要求 Timeline 显示时间排序、事件过滤、操作者、来源、摘要、关联对象，并可打开 Proposal、Commit、证据或 Workflow（`docs/product/PRD.md:2197-2203`）。Impact 要覆盖 Document、Artifact、Review Card、评测样本与 Conflict，并且只生成报告，由用户选择对象创建更新 Proposal，绝不自动级联改写（`docs/product/PRD.md:2215-2230`; `docs/product/PRD.md:2241-2246`）。AC-22/AC-36 还要求版本、Proposal、Commit、Conflict、影响互查以及 Conflict 触发下游分析（`docs/product/PRD.md:4621`; `docs/product/PRD.md:4635`）。

当前缺口是实质性的：

- 没有 `/timeline` route、导航项、Timeline client、query hooks 或 feature module（`web/src/routes/AppRoutes.tsx:30-60`; `web/src/app/AppShell.tsx:10-23`）。Frontend spec 也明确页面未交付（`.trellis/spec/frontend/index.md:17-18`）。
- Event wire 有 ID、source refs、summary、correlation、时间，但没有 operator 或已解析导航链接（`internal/knowledge/http/handler.go:429-443`; `api/openapi/openapi.json:11963-12042`）。Correlation 只包含 Proposal/Approval/Workflow/Audit/Git ref（`internal/knowledge/domain/timeline.go:77-84`）。
- 已有前端详情 route 只覆盖 Proposal、Workflow 和 Source Version（`web/src/routes/AppRoutes.tsx:37-42`）；没有 Commit viewer、Review Card detail 或通用证据 route。
- Event enum 没有 `SOURCE_IMPORTED`、`CLAIM_CREATED`、`ARTIFACT_GENERATED`、`REVIEW_CARD_INVALIDATED`（`internal/knowledge/domain/timeline.go:41-57`）。`topic.create` / `claim.confirm` 当前被归为 `VERSION_PUBLISHED`（`migrations/00037_timeline_impact_hardening.sql:377-395`），Artifact/Review 没有一等 source。

### 4. 建议的最小安全交付范围

#### Slice A：Timeline/Impact Web 工作台（可独立交付）

1. 新增严格 `web/src/api/timeline.ts`，逐字段解码现有四个 API，未知/缺失字段 fail closed；不得在组件内直接使用未经校验的 JSON。
2. 新增 `web/src/features/timeline/`：
   - `query-keys.ts`：所有 key 以 `['timeline', workspaceId, ...]` 开头；list key 包含 canonical filter/limit，detail/report key 包含稳定 ID。
   - `queries.ts`：列表、详情、报告 query 和 Impact mutation；workspace 为空时禁用。
   - `TimelinePage.tsx`：事件类型多选、aggregate type/ID、source event ref、UTC 时间范围、刷新、空/错/不可用状态、opaque cursor 下一页、详情、Impact 分析与报告结果。
   - 过滤条件适合写入 URL；opaque cursor 只作为绑定当前 canonical filter 的页状态，过滤变化立即丢弃，不能跨 filter/workspace 复用。服务端现有参数和约束见 `internal/knowledge/http/handler.go:219-253`。
3. Impact mutation 的 `Idempotency-Key` 必须按一次用户意图生成并在网络未知/重试时复用；只有收到确定成功或用户显式开始新分析后才轮换。POST body 固定 `{}`，不能把对象选择塞进现有分析端点（`internal/knowledge/http/handler.go:310-342`）。
4. 在 `AppRoutes` 和 `AppShell` 增加 `/timeline` 与 `/timeline/:eventId`，路由 lazy-load；增加 route/nav/component/API tests。现有导航和 breadcrumb 都由单一 `navigation` 数组派生（`web/src/app/AppShell.tsx:10-37`）。
5. 只为可证明的结构化关联提供链接：`proposal_id -> /proposals/:id`、`workflow_run_id -> /workflows/:id`；Source Version 只有拿到正式 UUID 时才可到 `/documents/:id`。禁止从任意 `source_ref` 字符串猜 route。Commit 当前无详情 owner，必须显示为不可导航的稳定 ref，或先交付新的只读 Commit route/API。
6. UI 应显示 `knowledge_timeline` capability 不可用状态，但 API query 仍是业务事实源；不能用 System Status 页面冒充 Timeline（`.trellis/spec/frontend/index.md:17-18`; `web/src/api/system-status.ts:142-215`）。

Web cache 注意事项：全站 SSE store 已有 workspace query families、恢复顺序和定向失效（`web/src/events/event-store.tsx:70-132`; `web/src/events/event-store.tsx:147-240`），但 Timeline 投影由 Worker 异步完成。收到原始业务 SSE 后立刻 invalidate Timeline 可能早于 `ops.knowledge_event` 提交，随后长期保留旧列表。最小 Slice A 可以使用显式刷新/重新进入查询；若要求实时更新，必须在 projector 成功提交后发布可恢复的 `timeline` invalidation，再把 `['timeline', workspaceId]` 加入 SSE recovery。不要简单对所有业务 SSE 做一次立即刷新。

#### Slice B：owner-backed 只读 Impact（建议与 Slice A 分开提交）

Review 是最接近可安全接入的 owner：

- Review Card 直接绑定 Claim（`internal/review/domain/model.go:176-197`; `migrations/00034_learning_ops_auth.sql:138-162`）。
- 已有 `learning.review_card_evidence_selector`，按 Workspace/Claim/Source Version/Span 建有索引并限制证据展开 128 项（`migrations/00058_review_invalidation_batch_projection.sql:6-39`; `migrations/00058_review_invalidation_batch_projection.sql:63-97`）。
- 现有 invalidation SQL 已证明可以用这些 selector 做有界查找（`internal/review/adapter/postgres/invalidation.go:13-136`）。

因此可新增 `REVIEW_CARD` ImpactObject，按 changed Claim 直接命中，按 Relation 端点/Relation evidence 做有界关联；报告只引用 Card ID + version，并保持 `requires_proposal=false`，直到 Review owner 定义正式更新 Proposal。现有 Review invalidation 会投影成 `REVIEW_INVALIDATED` Health Issue，再生成泛化 Health Timeline（`migrations/00049_review_invalidation_observability.sql:3-15`; `migrations/00049_review_invalidation_observability.sql:130-164`）；它不能代替一等 `REVIEW_CARD_INVALIDATED` 事件。

Artifact 已有 owner 和不可变 Revision，但关联事实仍不足：

- Artifact Citation 只绑定 Source Version + Source Span，不直接绑定 Claim/Relation（`internal/artifact/domain/model.go:79-106`）。
- Revision 的 citation 存在 `learning.artifact_revision.sections` JSON 中，当前没有可 seek 的 citation selector projection（`internal/artifact/adapter/postgres/codec.go:21-27`; `internal/artifact/adapter/postgres/codec.go:144-148`）。
- Claim/Relation 到来源的正式 provenance 已存在于 `core.claim_source` 与 `core.relation_evidence`（`migrations/00017_knowledge_domain.sql:81-99`; `migrations/00017_knowledge_domain.sql:169-203`）。

最小安全做法是由 Artifact owner 新增 additive `artifact_revision_citation_selector`（具体命名由设计冻结），在写入不可变 Revision 的同一事务维护 `(workspace_id, artifact_id, revision_id, selector_kind, selector_id)`，索引 Source Version/Span，并对历史 v1 Revision 做有界、可重复 backfill。Impact 只能通过 Claim/Relation provenance 与该 selector 的精确 Source tuple 连接；不得每次分析无界扫描 JSON，也不得凭文本相似度声称 Artifact 依赖。报告应冻结 Artifact ID、Artifact version 和当前 Revision identity；仅 `id + version` 若无法证明 Revision 不漂移，则需要 owner-specific binding 扩展。

`ImpactObject`/OpenAPI 目前只公开 `RELATION,CONFLICT,HEALTH_ISSUE`，扩展 Artifact/Review 必须同步 Domain validation、canonical sort/dedupe、DB codec、HTTP/OpenAPI strict enum、checker 和前端 decoder（`api/openapi/check.mjs:1456-1463`）。所有查询继续满足同 Workspace、最多 500 个对象、稳定排序和参数化 SQL。

#### Slice C：一等 Timeline source（owner 已存在后可做）

- `ARTIFACT_GENERATED`：只从 durable Artifact Revision/generation terminal success 的同事务 source enqueue；aggregate 应绑定 Artifact，correlation 绑定 Revision，不能从 Worker 日志或模型调用开始事件推断成功。
- `REVIEW_CARD_INVALIDATED`：只在 Review Card 首次进入 `INVALIDATED` 的持久事务 enqueue，source identity 至少绑定 Card ID + version；重复 delivery 必须复用同一 Event。
- 新迁移必须 forward-safe additive，保留 `00037` 不动；Down 在存在新 Event/Outbox/selector 事实时以 `55000` 拒绝。
- 如本任务宣称补齐完整产品事件目录，还需单独处理 Source import 与 Claim created；当前用 `VERSION_PUBLISHED` 代替 Claim creation、且没有 Source import event，不能在验收文案中忽略。

### 5. 正式 downstream Proposal 不能伪造

现有 Change Control 只支持 `file_patch`、`knowledge_change`、`publish_artifact` 三种 typed Proposal（`internal/changecontrol/domain/typed_proposal.go:16-31`）。`PUBLISH_ARTIFACT` 的语义是把已批准 Artifact 发布为正式知识，冻结 Artifact/Revision/content hash，并明确不会创建普通 Document；它不是“刷新受影响 Artifact”（`internal/artifact/adapter/changecontrol/publication_creator.go:21-74`）。Health repair Proposal 端点也仍显式 unavailable（`internal/health/http/handler.go:312-326`）。

因此不能：

- 把现有 `ImpactProposalDraft` 当作已创建 Proposal；
- 新增一个没有 owner/executor 的泛型 `impact_action`；
- 用 `publish_artifact` 冒充 Artifact refresh；
- 用 Review 直接 invalidate 命令冒充经过 Change Control 审批的 update Proposal。

若本任务必须交付正式 Proposal，需先为每个支持对象冻结 typed owner contract，至少包含：source report ID/version/fingerprint、source event ID/version、target ID/base version、owner-specific immutable binding、request hash/idempotency、风险等级、审批与一次性 Write Authorization、批准后的 executor/receipt/replay/conflict/补偿语义。建议新建严格的“从报告选择对象创建 Proposal”命令端点，但 Application 必须委派给明确注册的 owner factory；未注册类型返回稳定 unavailable，不能生成孤儿 Proposal。

这是独立的高风险 Change Control 工作包，会影响 typed union、迁移、Approval/Workflow/Safe Writeback、Auth capability、OpenAPI discriminator 和 Web Proposal UI。没有这些证据时，本次只能交付只读 Impact 和显式不可用动作。

### 6. 既有报告的版本化迁移风险

这是扩展 Impact 时最容易遗漏的兼容性问题。`ops.impact_report` 当前对 `(workspace_id, source_event_id)` 唯一（`migrations/00034_learning_ops_auth.sql:249-258`），Report append-only（`migrations/00037_timeline_impact_hardening.sql:185-187`）；Application 一旦找到旧报告就直接 replay，不会重新执行新的 Artifact/Review 查询（`internal/knowledge/application/impact.go:96-117`）。

所以只修改 `ListImpactObjects` 只会影响部署后的新 Event，历史已分析 Event 永远缺少新增对象。实施必须明确二选一：

1. 保持 v1 历史不可变，只承诺新事件使用新 reader，并在 UI 标明旧报告的 analysis version；不能宣称历史覆盖已补齐。
2. 引入 additive versioned report 模型，例如 analysis policy/version + supersedes binding，允许同一 source event 生成新的不可变报告，并让 POST 的幂等/最新选择语义显式化。不得 UPDATE/DELETE v1 报告或偷偷覆盖 fingerprint。

若要真正关闭 Artifact/Review 历史影响，推荐方案 2；它需要新的 schema/version、唯一键、API/OpenAPI、Audit 与 Timeline source 设计，不能当作单条 SQL UNION 扩展。

### 7. 明确留给 M10/M11 的范围

- AI Eval：仓库没有 production Evaluation domain/table/repository/workflow/owner；只有未来数据库设计（`docs/architecture/database-design.md:735-763`）和 contract-only Tool，后者明确生产 unavailable（`.trellis/tasks/archive/2026-07/07-19-tool-registry-security/prd.md:17-20`; `.trellis/tasks/archive/2026-07/07-19-tool-registry-security/prd.md:80-83`）。父计划把真实 AI Eval 放在 M11-02（`.trellis/tasks/07-16-product-delivery/implement.md:68`）。本任务不得添加假 Eval Impact row。
- 版本比较：产品要求双批准 Revision 的 Markdown/Claim/Relation/Topic/Citation diff（`docs/product/PRD.md:2204-2213`），当前只有 Proposal review diff，没有 pair-revision domain/API。它应作为 M11 seam 独立设计，不塞入 Timeline UI 组件。
- 全局 Audit/observability：M7 spec 只拥有 `IMPACT_ANALYZED` 及最小 append-only/redaction 基础（`.trellis/spec/backend/timeline-impact.md:5-8`; `.trellis/spec/backend/timeline-impact.md:61-65`）。全业务 Audit、OTel/Metrics、保留、告警和 POISONED 恢复仍是 M10-01/M10-04（`.trellis/tasks/07-16-product-delivery/implement.md:63`; `.trellis/tasks/07-16-product-delivery/implement.md:66`）。Timeline UI 不能扩成 Audit UI。
- Document Impact：当前可导航对象是 Source Version，而产品要求正式 Document。未找到可供 Impact 冻结的 Document owner/identity；不能把 Source Version 改名为 Document。需由 Document lifecycle seam 落地后再接入。

### 8. 预计影响文件

仅 Slice A 的聚焦文件：

- `web/src/api/timeline.ts`、`web/src/api/timeline.test.ts`（新增）
- `web/src/features/timeline/**`（新增 page/query keys/hooks/tests）
- `web/src/routes/AppRoutes.tsx`、`web/src/routes/AppRoutes.test.tsx`
- `web/src/app/AppShell.tsx`、`web/src/app/AppShell.test.tsx`
- `web/src/styles.css`
- 若交付 projector-complete SSE：`web/src/events/event-store.tsx`、相关 tests、服务端 SSE schema/producer；否则不要为形式修改 Event Store。

Slice B/C 后端扩展：

- `internal/knowledge/domain/timeline.go` 及 tests
- `internal/knowledge/application/impact.go` 及 tests
- `internal/knowledge/adapter/postgres/timeline.go` 及 PostgreSQL integration tests
- `internal/knowledge/http/handler.go` 及 tests
- `internal/artifact/adapter/postgres/codec.go` 与 owner integration tests（citation selector 同事务维护）
- Review read projection/测试；优先复用 `learning.review_card_evidence_selector`，不要复制第二份 Card 事实
- 下一条 additive migration 与 `internal/platform/migration/**` tests；不要改写 `00037`
- `api/openapi/openapi.json`、`api/openapi/check.mjs`
- 若新增 mutation：`internal/auth/http/handler.go`、capability tests、`cmd/api` integration
- 若新增生产依赖 seam：`cmd/api/main.go`、`internal/app/router.go` 和 readiness tests

正式 Proposal 工作包还会触及 `internal/changecontrol/**`、Artifact/Review owner executor、Workflow/Approval、Proposal Web discriminated union 和对应迁移；应与 UI/read-only Impact 分开评审。

已知共享脏文件由父会话报告包括 `api/openapi/openapi.json`、`api/openapi/check.mjs`、`web/src/api/business.ts`、`web/src/features/graph/graph.css`、redaction 和 Worker integration 文件。实现必须基于当前内容做 additive hunk，不能覆盖 M9/M10 变更；Timeline UI 没有理由修改 `business.ts` 或 `graph.css`。

### 9. 验证建议

聚焦单测/静态门禁：

```bash
go test -race -count=1 -timeout 60s ./internal/knowledge/... ./internal/artifact/... ./internal/review/... ./internal/changecontrol/... ./internal/auth/http ./cmd/api
make openapi-check
npm run test --prefix web -- --run src/api/timeline.test.ts src/features/timeline src/routes/AppRoutes.test.tsx src/app/AppShell.test.tsx
npm run lint --prefix web
npm run typecheck --prefix web
npm run build --prefix web
git diff --check
```

真实 PostgreSQL/Worker 门禁（使用 disposable `ZHIXU_TEST_DATABASE_URL`）：

```bash
make timeline-impact-integration
make timeline-impact-fault-smoke
make timeline-impact-worker-smoke
```

新增 PostgreSQL integration 必须额外证明：

- Artifact selector fresh/upgrade/repeat/backfill/guarded Down；selector 与 Revision 同事务、Workspace 隔离、索引可 seek、无 JSON 全表扫描。
- Claim/Relation -> Artifact/Review 查询的精确 provenance、去重、500 上限、稳定排序、跨 Workspace 和 stale version。
- 新 Timeline source 的 owner 事务回滚、exact replay、并发 projector、POISONED binding drift。
- v1 已有 Impact Report 在新 analysis policy 下的明确兼容行为，不能只测全新 Event。
- 正式 Proposal 若纳入：同 key replay、不同 binding conflict、审批前零副作用、stale report/object 拒绝、批准后 owner receipt 与 response-loss recovery。

Web 需要 component 测试覆盖无 Workspace、capability unavailable、空列表、非法 wire、filter 重置 cursor、分页错误重试、详情 404、分析 201/200 replay/409/503、未知网络结果复用幂等键、仅为已证明对象渲染链接。若用户要求浏览器验收，再跑桌面与 390x844 真实页面 smoke；本研究阶段未启动服务。

按项目规则，涉及 Go/共享契约须执行 `go-review`，迁移/Repository/selector 必须追加 SQL review，跨前后端整体再做 `code-review-and-quality`。

## Files Found

- `.trellis/tasks/07-28-m7-timeline-impact-residuals/prd.md` — 当前残余任务占位 PRD，范围尚未冻结。
- `.trellis/spec/backend/timeline-impact.md` — 已交付 M7 后端的不变量、错误矩阵和 deferred 边界。
- `.trellis/spec/frontend/index.md` — 明确 Timeline/Impact 页面尚未交付。
- `.trellis/tasks/archive/2026-07/07-25-m7-timeline-impact/prd.md` — 原 M7 验收与遗留项的权威记录。
- `.trellis/tasks/07-16-product-delivery/implement.md` — M10/M11 owner 与里程碑边界。
- `docs/product/PRD.md` — Timeline 展示、版本比较、Impact 覆盖与处理的产品要求。
- `internal/knowledge/domain/timeline.go` — Event、filter、Impact object/report/draft 领域契约。
- `internal/knowledge/application/impact.go` — 报告生成、replay、Audit 与 Draft 派生逻辑。
- `internal/knowledge/adapter/postgres/timeline.go` — 当前 Relation/Conflict/Health Impact SQL。
- `internal/knowledge/http/handler.go` — 四端点 strict HTTP wire 与 query/body bounds。
- `migrations/00034_learning_ops_auth.sql` — Artifact/Review/Impact 初始表与 Impact 单报告唯一约束。
- `migrations/00037_timeline_impact_hardening.sql` — append-only Event/Report、Outbox 与现有 Timeline sources。
- `migrations/00039_artifact_persistence.sql` — Artifact v1 immutable Revision 与持久 owner。
- `migrations/00049_review_invalidation_observability.sql` — Review invalidation 到 Health/泛化 Timeline 的兼容投影。
- `migrations/00058_review_invalidation_batch_projection.sql` — Review evidence selector 索引与 bounded projection。
- `internal/artifact/domain/model.go` — Artifact Citation 的 Source Version/Span 绑定。
- `internal/artifact/adapter/postgres/codec.go` — Artifact Revision sections/citations 的 JSON persistence。
- `internal/review/domain/model.go` — Review Card 到 Claim/Evidence 的 owner 契约。
- `internal/review/adapter/postgres/invalidation.go` — 已验证的 bounded selector 查询模式。
- `internal/changecontrol/domain/typed_proposal.go` — 当前仅有三种 typed Proposal 的事实源。
- `internal/artifact/adapter/changecontrol/publication_creator.go` — `PUBLISH_ARTIFACT` 的真实语义与冻结 binding。
- `internal/auth/http/handler.go` — fail-closed HTTP capability 映射。
- `api/openapi/openapi.json` / `api/openapi/check.mjs` — strict Timeline/Impact wire 与 checker。
- `web/src/app/AppShell.tsx` / `web/src/routes/AppRoutes.tsx` — Timeline UI 的导航和 route 接入点。
- `web/src/events/event-store.tsx` — Workspace cache recovery 与 SSE 定向失效模式。
- `Makefile` — 现有 Timeline/Impact PostgreSQL、fault 和 Worker smoke targets。

## Code Patterns

- 严格客户端：API boundary 解码 wire，feature query key 必须包含 Workspace；现有模式见 `web/src/features/health/query-keys.ts:1-7` 与 `web/src/features/health/queries.ts:28-37`。
- opaque cursor：绑定 canonical request，不把 cursor 当可解释业务数据；服务端 binding 规则见 `.trellis/spec/backend/timeline-impact.md:58-60`。
- owner projection：Review selector 只保存 identity，Card 仍是唯一事实源（`migrations/00058_review_invalidation_batch_projection.sql:3-6`）；Artifact selector 应沿用同一原则。
- append-only extension：正式事实事务只 enqueue stable source，projector 后置恢复，不能让投影失败回滚 owner 事实（`.trellis/spec/backend/timeline-impact.md:54-57`）。
- fail-closed auth：未知 mutation 默认要求 `WRITE_KNOWLEDGE`，新端点必须显式加入 capability map 和测试（`internal/auth/http/handler.go:159-177`）。
- strict OpenAPI：新 route/schema/enum 不仅编辑 JSON，还要扩展 checker 的 success/error/security/bounds 断言（`api/openapi/check.mjs:267-306`; `api/openapi/check.mjs:987-1004`）。

## Related Specs

- `.trellis/spec/backend/timeline-impact.md`
- `.trellis/spec/backend/index.md`
- `.trellis/spec/frontend/index.md`
- `.trellis/spec/backend/artifact-contract.md`
- `.trellis/spec/backend/auth-security.md`
- `docs/product/PRD.md` section 10.16, AC-22, AC-36

## External References

无。本结论只依赖仓库内产品、规格、代码、迁移和测试事实；未使用外部文档或版本假设。

## Caveats / Not Found

- 当前 task PRD 是 `TBD`；研究给出建议边界，不代表产品已批准取舍。
- 未找到 production Evaluation owner、Document aggregate/Impact binding、Commit detail route/API、Review Card detail route，或从 Impact Report 创建正式 typed Proposal 的端点/执行器。
- 未找到 Artifact citation selector；直接扫描 `artifact_revision.sections` JSON 不满足有界查询要求。
- 未找到 projector 成功后的 Timeline SSE invalidation；原始业务 SSE 与异步 Timeline 投影存在刷新竞态。
- 现有 Event wire 不含 operator，现有 correlation 也不足以覆盖所有产品导航。要满足产品原文需先扩展后端投影/wire，不能由前端猜测。
- 现有单报告唯一键与 append-only 约束使历史报告不会自动获得新 object 类型；必须冻结 analysis version/supersession 方案。
- 本研究未运行 Git 命令、测试、构建、数据库或浏览器；只进行了只读文件/代码搜索，并仅写入本任务 `research/` 目录。
