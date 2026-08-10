# Research: 产品交付任务 M7-M9 状态漂移审计

- Query: 核对活跃产品交付任务中 M7-04、M8-03、M9-02、M9-03 的残留项和完成状态，检查 AC 数量及来源漂移，并给出可直接落入文档的修正文案。
- Scope: internal
- Date: 2026-08-10

## Findings

### 1. 总体结论

| 项目 | 当前判断 | 依据摘要 | 应如何记录 |
| --- | --- | --- | --- |
| M7-04 Timeline / Impact v2 | 当前合同已交付；执行器和 Document/Eval 影响类型不属于当前合同 | v2 API、Artifact/Review 绑定、下游提案和 UI 均存在；规范明确 `downstream_update` 仅审批、不执行，Document/Eval 延后 | 保持“当前范围完成”，把“执行器未交付”改为产品边界，不列为当前阻塞 |
| M7-04 中的 OTel / Metrics | 已交付，父任务记录陈旧 | 08-08 归档任务已完成；API 与 Worker 均暴露 Prometheus，OTel exporter 已接入 | 从“待补”中删除；M10-01 改为“部分完成” |
| M7-04 中的全局 Audit | 部分交付，仍有真实缺口 | append-only store 及 Impact/Export/ModelSettings 生产者已存在；没有统一查询入口，关键业务/安全决策覆盖和留存演练仍不完整 | 留在 M10-01，明确剩余范围，不再与 OTel/Metrics 捆绑描述 |
| M8-03 三项残留 | 已由 07-28 子任务交付 | Guarded Down、Review Path 并发/ABANDONED、Conversation RAG Memory 均有代码和测试 | 改为完成；全局 Agent 注入是明确禁止的实现方式，不是缺口 |
| M9-02 Proposal 编辑/三方合并 | 仍缺失，是当前 AC-13 真缺口 | 当前仅有双向 Diff 与 hash 冲突阻断；无 Revision 编辑/merge API；稳定需求仍要求三方合并 | M9-02 改为“部分完成”，新建明确子任务或纳入 M11 阻塞项 |
| M9-03 Eval/Audit JSON | 未实现，但已明确移出当前产品范围 | 代码只接受三种现行格式；稳定需求和用户手册均明确其他格式不在当前范围 | M9-03 保持“当前范围完成”；不要再把 Eval/Audit JSON 写成当前残留 |
| AC 数量与验收来源 | 父任务陈旧 | 父任务仍指向已删除 `docs/product/PRD.md` 和 AC-01..36；现行 `docs/requirements.md` 为 AC-01..41 | 活跃父任务统一改为 `docs/requirements.md`、AC-01..41；M11 最终演示采用 14 步 |

### 2. 活跃父任务状态不等于产品整体完成

- `.trellis/tasks/07-16-product-delivery/task.json:6` 仍是 `in_progress`。
- 同文件 `:21-54` 挂载的 33 个子任务均已归档，因此任务工具显示 `33/33 done`。这个比例只表示“已挂载子任务全部归档”，不能推出 M10/M11 或全部产品 AC 已完成。
- 当前没有覆盖 M10-03、M10-04 和 M11 最终验收的子任务；相反，`.trellis/tasks/07-16-product-delivery/implement.md:63` 的 M10-01 仍写“待开始”，但 08-08 OTel/Prometheus 子任务已经完成。
- 建议不要把 `33/33` 复制到面向用户的项目完成度说明。应以稳定需求 AC 和代码验证为完成口径，并为尚未完成的 M9-02、M10、M11 建立可追踪项。

### 3. M7-04：交付状态与有意边界

#### 已交付

- `.trellis/tasks/07-16-product-delivery/implement.md:55` 已记录 Timeline/Impact v2、Artifact/Review 影响绑定和下游提案能力。
- `.trellis/spec/backend/timeline-impact.md:5-8` 将 Timeline Web、Artifact/Review 影响分析、Owner Events 和 `downstream_update` 提案列为已实现能力。
- `internal/knowledge/http/handler.go:64-67` 注册 Timeline 列表、事件详情、影响分析和报告读取接口。
- `internal/changecontrol/http/handler.go:70` 注册下游更新 Proposal 创建接口。
- `internal/knowledge/domain/timeline.go:164-177` 定义当前影响对象类型；`:193-230` 实现 Artifact/ReviewCard 的 owner binding。
- `web/src/features/timeline/TimelinePage.test.tsx:328-342` 覆盖 v2 报告读取和 Artifact 下游提案创建。

#### 有意不在当前合同内

- `.trellis/spec/backend/timeline-impact.md:69-71` 明确 `downstream_update` 是 approval-only：批准/拒绝只持久化决策，不派发工作流，也不进行真实写入。
- `internal/changecontrol/application/writeback_service.go:200-201` 对 `downstream_update` apply fail closed。
- `migrations/00062_m7_timeline_impact_v2.sql:1058` 起的数据库约束同样拒绝执行该类型。
- `web/src/features/business/ProposalsPage.tsx:559-560` 对已批准的下游更新显示“执行能力不可用”。
- `docs/requirements.md:133-137` 与 `docs/user-guide.md:167-173` 都把它定义为影响建议和审批意图，而不是自动执行器。因此“没有执行器”不是当前 Must 缺口。
- `.trellis/spec/backend/timeline-impact.md:8,97-98,138-139` 明确 Document/Evaluation 专用 impact owner 延后；`internal/knowledge/domain/timeline.go:164-177` 也没有这两类对象。现行稳定需求未要求其成为独立影响对象，因此应归为未来扩展，而不是 M7 未完成项。

#### OTel / Metrics 已交付，Audit 仅部分交付

- `.trellis/tasks/archive/2026-08/08-08-otel-prometheus-observability/task.json:6,22` 标记 OTel/Prometheus 任务完成。
- `internal/platform/observability/otel.go:85-123` 创建并安装 OTLP exporter/provider。
- `internal/platform/observability/prometheus.go:43-97` 创建独立 Prometheus registry 和指标采集器。
- `internal/app/router.go:157-159` 暴露 API `/metrics`；`cmd/worker/main.go:2550-2557` 暴露 Worker `/metrics`。
- `internal/audit/application/ports.go:11-20` 和 `internal/audit/adapter/postgres/store.go:69-125` 已形成 append-only Audit 基础。
- 已知生产者包括 Impact（`internal/knowledge/adapter/audit/impact.go:40-102`）、Export Download（`internal/export/adapter/postgres/download.go:86-128`）和 Model Settings（`internal/modelsettings/adapter/postgres/audit.go:74-92`）。
- 当前未找到 Audit HTTP 查询路由或前端 Audit 模块。
- `docs/architecture/quality.md:127-135` 要求覆盖 Auth/Session/Token、Approval、Tool Auth、File/Git、Settings/Secret、Memory、Rollback 和 Security Block；`.trellis/spec/backend/logging-guidelines.md:154-158` 仍把统一生产者覆盖、查询/留存和自托管演练列为未完成工作。

结论：父任务里“global Audit/OTel/Metrics 待补”的合并描述已失真。OTel/Metrics 已完成；Audit 是“基础和若干切片完成，覆盖/查询/留存仍未完成”。

### 4. M8-03：历史残留已收口

`.trellis/tasks/07-16-product-delivery/implement.md:58` 仍把以下三项写为待补：业务数据 Down guard、Review Path 并发及 ABANDONED reopen、通用 Agent/RAG Memory。该记录已被后续子任务和代码事实覆盖。

#### 归档任务证据

- `.trellis/tasks/archive/2026-07/07-28-m8-learning-residuals/task.json:4-6,22` 显示任务“收口 M8 Learning 动态盲区与通用 Memory”已完成，且父任务正是 `07-16-product-delivery`。
- 该任务 PRD 的目标和范围位于 `.trellis/tasks/archive/2026-07/07-28-m8-learning-residuals/prd.md:3-15`，三类残留要求位于 `:19-50`。
- 该历史 PRD 的 AC 复选框在 `:59-72` 仍未勾选，与 task.json 的完成状态不一致。这是归档记录的 bookkeeping 漂移；不建议重写历史文件，应以任务状态、实现和测试共同判定。

#### 代码与测试证据

- `internal/platform/migration/m8_guarded_down_integration_test.go:16` 验证 Interview/Memory migration 的 Guarded Down 保留业务数据并可恢复。
- 同文件 `:45` 验证 Review Shared Learning Path 的相同行为。
- `internal/review/learningpath/adapter/postgres/review_learning_path_integration_test.go:21` 覆盖同键/不同键并发；`:142` 覆盖 maintenance 对迟到 hold/complete 的 fencing；`:309` 覆盖 complete response-loss replay；`:357` 覆盖 ABANDONED reopen 与 attempt fencing。
- `internal/agent/adapter/workflow/rag_executor.go:18-36` 声明 RAG executor 的 Memory/Snapshot 依赖；`:43-52` 缺依赖时 fail closed；`:101-152` 在 provider 调用前完成 attempt-scoped snapshot claim、Memory 加载、canonical context 构造以及 snapshot/ModelRun 原子落库。
- `cmd/worker/main.go:2058-2068` 只为 Conversation RAG executor 注入 Memory loader，符合显式消费而不是全局注入的设计。
- `.trellis/spec/backend/quality-guidelines.md:666-670,684-711` 已记录 Conversation RAG Memory、00059/00060/00061 迁移及“不得全局注入共享 ChatModel”的约束。

#### 不应重新列为当前残留的项目

- 全局 Agent Memory 注入是明确禁止的架构方式；当前交付的是 Conversation RAG 的非证据 Memory 上下文。
- `last_used_at`、recent-use UI、EPISODIC 到 PREFERENCE 的自动转化在归档 PRD `:52-57,74-77` 中被排除。
- 现行 `docs/requirements.md:139-143` 只要求类型、来源/状态/作用域/过期、确认和编辑/暂停/恢复/删除；`docs/user-guide.md:193-199` 与之匹配。上述排除项不应作为当前 M8 完成阻塞，除非产品重新纳入范围。

### 5. M9-02：历史任务完成，但当前产品 AC-13 未完成

`.trellis/tasks/07-16-product-delivery/implement.md:60` 将 M9-02 标为完成，同时写明“批准后 Proposal Revision 编辑与完整三方合并显式不可用”。这可以描述当时子任务的缩减范围，但不能表示现行产品需求已满足。

#### 历史任务范围

- `.trellis/tasks/archive/2026-07/07-22-m9-business-frontend/prd.md:45-53` 只要求 current/proposal diff 和 hash 漂移阻断，并明确完整三方合并与 Proposal revision edit 显示不可用。
- 同文件 `:83-96` 的 AC 对这一缩减范围判定完成；`:105-110` 把完整三方合并和 revision edit 排除在任务外。

#### 当前实现

- `internal/changecontrol/http/handler.go:66-75` 只有创建、列表、详情、current-content、approve 和 preflight；没有 Proposal Revision 更新或 merge API。
- `web/src/features/business/ProposalsPage.tsx:366-372` 要求 base hash 与 current hash 相同才允许批准。
- 同文件 `:474` 在漂移时仅显示 current/base hash 并要求重新生成 Revision。
- 同文件 `:477-489` 的 Diff 输入只有 current 与 proposed，是双向比较，不是 base/current/proposed 三方合并。
- `api/openapi/openapi.json:2296,2494,2562,2660,5487` 对应现有 Proposal 操作；未找到 update revision 或 merge operation。
- Document History 的两版本比较不能替代该能力：`.trellis/spec/frontend/document-history-workbench.md:36-43` 明确是 left/right Commit/WORKTREE 比较；`internal/documenthistory/http/handler.go:59-62` 只注册 history、compare、restore preview/proposal。

#### 当前稳定需求仍要求三方合并

- `docs/requirements.md:211` 要求展示“原始目标、当前目标、Proposal”的三方差异。
- `docs/requirements.md:262` 的 AC-13 要求目标文件变化时阻断直接覆盖并进入三方合并流程。
- `docs/user-guide.md:82,303` 仍按该目标行为描述冲突处理。

结论：M9-02 是唯一一个本次重点项中“历史子任务已完成，但现行稳定需求仍存在真实功能缺口”的项目。必须在活跃父任务中标为部分完成，并由明确子任务或 M11 阻塞项追踪。

### 6. M9-03：额外导出格式未实现，但不在当前范围

- `.trellis/tasks/07-16-product-delivery/implement.md:61` 已将当前三种格式视为完成，但把 Evaluation/Audit JSON 写作“后续范围”。
- `internal/export/domain/model.go:12-18` 只定义 `MARKDOWN`、`METADATA_JSON`、`ATTACHMENTS_ZIP`。
- `internal/export/domain/model_test.go:104-114` 明确拒绝 `EVALUATION_JSON` 和 `AUDIT_JSON`。
- `internal/export/http/handler.go:145-146` 的 Collection 导出只接受 Markdown/Metadata JSON；附件包走独立 Workspace 导出路径。
- `api/openapi/openapi.json:23863-23868,24029-24034,24525-24529` 与上述三种格式一致。
- `web/src/api/exports.ts:6,88` 只暴露 Collection 的两种格式，Workspace attachment export 独立处理。
- `docs/requirements.md:176-182` 明确 Evaluation/Audit JSON、CSV/XLSX 等不在当前范围；`:282` 的 AC-33 只验收 Collection Markdown、Metadata JSON 和 Workspace ZIP。
- `docs/user-guide.md:126-131` 和 `docs/architecture/domain-and-data.md:234-236` 与现行范围一致。
- 历史差距分析 `.trellis/tasks/archive/2026-07/07-28-m9-export-residuals/research/m9-gap-analysis.md:9-16,53-80` 已区分 AC-33 与额外格式；但活跃父 PRD `.trellis/tasks/07-16-product-delivery/prd.md:109-114` 仍保留旧的 Evaluation/Audit JSON 要求。

结论：M9-03 已满足当前稳定需求。Evaluation/Audit JSON 是未实现的候选扩展，但不应继续叫“当前 M9 残留”。如果产品未来需要，应单独建立 roadmap 项，不应让现有完成状态持续含糊。

### 7. AC 数量、来源与 M11 演示步骤漂移

- 活跃父 PRD `.trellis/tasks/07-16-product-delivery/prd.md:5,16,28,133` 仍将已删除的 `docs/product/PRD.md` 作为来源，并使用 AC-01..AC-36。
- `.trellis/tasks/07-16-product-delivery/implement.jsonl:1` 和 `check.jsonl:1` 也仍引用旧路径。
- `.trellis/tasks/07-16-product-delivery/implement.md:20` 的 M11 验收仍写 AC-01..AC-36 和 11 步演示；`:26` 再次引用旧 PRD。
- 现行 `docs/requirements.md:250-290` 包含 AC-01..AC-41，是当前稳定验收来源。
- 同一 docs consolidation 任务的 M11 审计 `.trellis/tasks/08-10-docs-consolidation/research/m11-code-audit.md:23-28,101-107` 已确认旧 Git PRD 的最终演示为 14 步，而不是父任务当前的 11 步。

建议：只修活跃父任务和当前 docs，不回写已归档任务。活跃父任务应统一声明：

> 产品范围与验收清单以 `docs/requirements.md` 的 AC-01..AC-41 为准；用户行为以 `docs/user-guide.md` 为准；M11 使用 Trellis 中维护的最终 14 步演示脚本执行验收。

## Proposed Wording Corrections

以下文案可以直接用于活跃父任务的 milestone 表或当前开发路线图。

### M7-04

> **当前范围完成。** Timeline/Impact v2、Artifact/Review 影响绑定、owner events 和 approval-only `downstream_update` Proposal 已交付。下游真实执行器与 Document/Evaluation 专用影响类型不属于当前合同。OTel/Prometheus 已由 08-08 子任务交付；Audit 已有 append-only 基础及 Impact/Export/ModelSettings 切片，统一生产者覆盖、查询、留存和自托管演练继续由 M10-01 收口。

### M8-03

> **已完成。** 07-28 残留任务已交付真实 PostgreSQL Guarded Down 验证、Review Learning Path reservation/hold 并发与 ABANDONED reopen/fencing，以及 Conversation RAG 的 attempt-scoped 非证据 Memory snapshot。全局 Agent/ChatModel Memory 注入是明确禁止的架构方式；`last_used_at`、recent-use UI 和自动类型转化不在当前稳定需求范围。

### M9-02

> **部分完成，AC-13 未关闭。** File/Relation 双向 Diff、Evidence/Rollback 展示、approve/reject、base-hash 冲突阻断和 preflight 已交付。Proposal Revision 编辑和真正的 base/current/proposed 三方合并尚未实现；当前 UI 只会阻断批准并要求重新生成 Revision。应创建明确子任务或将其列为 M11 阻塞项。

### M9-03

> **当前范围完成。** Collection 支持 Markdown 与 Metadata JSON，Workspace 支持 Attachments ZIP。Evaluation/Audit JSON 及 CSV/XLSX 不在当前产品范围；如未来恢复，应作为独立 roadmap 项，而不是 M9 当前残留。

### M10-01

> **部分完成。** OTel trace exporter 和 API/Worker Prometheus `/metrics` 已交付。Audit append-only store 及部分业务生产者已交付；全量关键决策覆盖、查询入口、留存/归档和自托管恢复演练仍待完成。

### M11

> **待验收。** 以 `docs/requirements.md` 的 AC-01..AC-41 和 Trellis 内最终 14 步演示脚本为唯一验收口径。M9-02 的 AC-13 三方合并缺口必须在最终验收前明确关闭或经产品决策从稳定需求中移除。

### 活跃父 PRD 的 R-08

> 当前导出范围为 Collection Markdown、Collection Metadata JSON 和 Workspace Attachments ZIP。Evaluation/Audit JSON、CSV/XLSX 及其他格式不在当前范围，未来如需要单独立项。

## Files Found

- `.trellis/tasks/07-16-product-delivery/{task.json,prd.md,implement.md,implement.jsonl,check.jsonl}` — 活跃产品交付任务、里程碑状态和陈旧来源。
- `.trellis/tasks/archive/2026-07/07-28-m8-learning-residuals/` — M8 三类残留的已完成归档任务。
- `.trellis/tasks/archive/2026-07/07-22-m9-business-frontend/` — M9-02 历史缩减范围及完成证据。
- `.trellis/tasks/archive/2026-07/07-28-m9-export-residuals/` — M9 导出 AC 与额外格式的历史差距分析。
- `.trellis/tasks/archive/2026-08/08-08-otel-prometheus-observability/` — OTel/Prometheus 已完成任务。
- `docs/requirements.md` — 当前 AC-01..AC-41 和范围事实源。
- `docs/user-guide.md` — 当前用户行为和范围说明。
- `docs/architecture/{quality.md,domain-and-data.md}` — Audit 质量目标与导出领域范围。
- `.trellis/spec/backend/{timeline-impact.md,quality-guidelines.md,logging-guidelines.md}` — Timeline/M8/Audit 可执行约束。
- `.trellis/spec/frontend/document-history-workbench.md` — Document History 双版本比较边界。
- `internal/{knowledge,changecontrol,memory,agent,review,export,audit,platform/observability}`、`cmd/worker`、`web/src/features` — 实现和测试证据。

## Verification Performed

- `go test -count=1 -timeout 60s ./internal/knowledge/... ./internal/changecontrol/...` — 通过。
- `go test -count=1 -timeout 60s ./internal/memory/... ./internal/agent/... ./internal/review/learningpath/...` — 通过；未启用 integration build tag 的包只执行普通单元测试。
- `go test -count=1 -timeout 60s ./internal/export/...` — 通过。
- `go test -count=1 -timeout 60s ./internal/platform/observability ./internal/observability ./internal/audit/... ./internal/app` — 通过。
- `npm run test --prefix web -- src/features/timeline/TimelinePage.test.tsx src/features/business/ProposalsPage.test.tsx src/features/collections/CollectionExportPanel.test.tsx src/api/attachment-exports.test.ts` — 4 个测试文件、58 个测试通过。

## Related Specs

- `.trellis/spec/backend/timeline-impact.md`
- `.trellis/spec/backend/quality-guidelines.md`
- `.trellis/spec/backend/logging-guidelines.md`
- `.trellis/spec/frontend/document-history-workbench.md`
- `docs/architecture/quality.md`
- `docs/architecture/domain-and-data.md`

## External References

无。本审计只使用仓库内任务、稳定文档、实现和测试证据。

## Caveats / Not Found

- 当前环境未配置 `ZHIXU_TEST_DATABASE_URL`，因此没有重新执行带 `-tags=integration` 的 PostgreSQL 集成测试；M8 的真实数据库结论来自已归档任务状态、测试源码和静态实现证据。
- 未找到 Proposal Revision 更新或三方 merge 的 HTTP/OpenAPI 入口；也未找到 Audit 查询 HTTP 路由或前端 Audit 模块。这里的“未找到”基于全仓符号/路由检索和相关模块检查。
- 归档任务中的未勾选 AC 不宜原地修改。它们是历史记录；当前状态应由活跃父任务、稳定需求、代码和可运行测试统一表达。
- M7 的 downstream executor、Document/Evaluation impact owner，以及 M9 的 Evaluation/Audit JSON 都是“代码不存在但当前范围有意不要求”；不能与 M9-02 的 AC-13 真缺口混为一类。
