# ZHIXU 产品级建设实施清单

> 当前增量 [OpenAPI 兼容修复](research/openapi-compatibility-fix.md) 已完成：Interview/Conversation、公共路由、OpenAPI 与 Web 已集成，固定原基线 0 error / 0 warning，必要验证与独立检查通过。修复尚未重新部署；M11 继续暂缓。源码已按后续授权提交为 `789692e2`，本批次推送目标为 `origin/dev`。

> **2026-09-09 本轮执行结果**：原始 TODO2 的动态工具调用循环已按 [本轮 PRD](research/todo2-dynamic-loop-prd.md) 补齐并通过真实整栈验证；TODO4 与已有开发收尾已核对，已交付任务关闭，本机 Docker 统一重建部署成功。下表旧首期的“已完成”只证明当时范围，动态需求有新的独立证据。M11 保持暂缓；实际迁移、HTTP/页面、数据保留与本机原有模型禁用限制见 [统一交付记录](research/final-integration-2026-09-09.md)。

> 本文件是复杂任务的执行顺序和验证门禁。规划已获批准并进入持续实施；状态以代码、子任务归档和实际验证为准，不以早期计划表中的默认值推断完成度。

## 1. Milestones

| 里程碑 | 目标 | 退出条件 |
|---|---|---|
| M0 | 需求、规范、契约与版本基线 | PRD/design/implement 审查通过；Spec 不再是占位模板；未决冲突有 ADR/研究任务 |
| M1 | 项目基础与技术骨架 | API/Worker/Web/DB/Compose/CI 可启动并有 readiness |
| M2 | Eino 技术验证 | 16 项 PoC 有报告，采用或回退路径明确 |
| M3 | 数据库与核心领域模型 | 迁移、约束、sqlc、聚合状态机和测试 Fixture 可重复 |
| M4 | 持久化 Workflow 与基础服务 | River 投递、Node Lease、Outbox、Human、重试、幂等、补偿可恢复 |
| M5 | 核心知识变更闭环 | Markdown 导入 → Proposal → Approval → Git → Reindex → 验证通过 |
| M6 | RAG、Agent、Tool Calling | 引用、拒答、冲突、结构化输出、权限和评测闭环 |
| M7 | Graph/Collection/Health/Timeline | 图谱可操作、候选可确认、问题可追踪、集合不复制事实 |
| M8 | Artifact/Review/Interview/Memory | 产物、复习、评分、FSRS、记忆生命周期可用 |
| M9 | 前端业务页面与表格能力 | 主要页面、Diff、图谱、表格、SSE、错误和空状态可用 |
| M10 | 安全、性能、可观测与部署 | 现有安全/运行基线、容量工具、审计查询和最小停写备份恢复交付，必要验证通过；完整矩阵移出本轮门禁 |
| M11 | 全量测试、文档和发布验收 | `docs/requirements.md` AC-01..AC-42、14 步演示、六条 seam 和交付包全部通过 |

## 当前开发与发布状态

- 当前登记 35 个 child；计数只表示各 child 的约定交付范围，不表示父任务或 M11 完成。
- 2026-09-08 至 09-09 按用户要求完成非 GORM、非 M11 精简收尾；具体实现/验证见 [收尾记录](research/lean-closeout-2026-09-08.md)。完整测试矩阵、长期观察与大型重构不再阻塞开发归档。
- M10-02 已完成；M9-03 已满足当前导出范围，`EVALUATION_JSON`/`AUDIT_JSON` 不再作为缺口。
- TODO2/TODO4 与统一部署已完成：Atlas 00099、Runtime ready，新 API/页面上线，原 Workspace/数据/密钥保留。本机 Chat/Embedding 保持升级前的 disabled 状态，现场 AI 准入尚未启用。随后 OpenAPI 兼容修复已清零原 21/5 报告，修复代码尚未重新部署。
- 最终验收以 `docs/requirements.md` AC-01..AC-42 和 `prd.md#final-demonstration` 的 14 步演示为准。

## 2. Ordered Task Table

| 任务ID | 阶段 | 任务 | 影响文件或模块 | 前置任务 | 验收方式 | 风险 | 执行者 | 状态 |
|---|---|---|---|---|---|---|---|---|
| M0-01 | M0 | 收敛需求、分页、SSE、表格边界和 Eino 决策记录 | `docs/requirements.md`, `docs/user-guide.md`, `docs/architecture/*`, ADR | 无 | 文档链接检查；需求追踪无孤儿；冲突表归零 | 文档与实现分叉 | 主 Agent | 已完成 |
| M0-02 | M0 | 完成 Trellis 后端规范 | `.trellis/spec/backend/**` | M0-01 | 无 `TBD/To be filled`；含事实引用和质量门禁，真实代码示例已随实现回填 | 规范过度理想化 | 子 Agent，主 Agent 审核 | 已完成 |
| M0-03 | M0 | 完成 Trellis 前端规范 | `.trellis/spec/frontend/**` | M0-01 | 无 `TBD/To be filled`；含事实引用和可访问性门禁，真实代码示例已随实现回填 | 与未来代码不一致 | 子 Agent，主 Agent 审核 | 已完成 |
| M0-04 | M0 | 确定 Go/Node/数据库/依赖版本与 License | `go.mod`, `web/package.json`, `docs/architecture/system-design.md`, ADR, `LICENSE` | M0-01 | manifest/lockfile 可复现；MIT License 明确 | 版本选择过早 | 主 Agent | 已完成：Go/Node/数据库依赖版本已锁定，项目采用 MIT License；精确版本以 manifest/lockfile 为准 |
| M1-01 | M1 | 初始化 Go module、API/Worker composition root、统一 ID/Clock/Error/Config | `go.mod`, `cmd/**`, `internal/platform/**` | M0-02,M0-04 | `go test ./...`, `go vet ./...`, readiness 单测 | 骨架形成错误依赖 | 子 Agent | 已完成 |
| M1-02 | M1 | 初始化 React/Vite/TS/Query/Router/Vitest；Playwright 由 M11 E2E 正式接入 | `web/**` | M0-03,M0-04 | `npm ci`, lint/typecheck/test/build；首页非空 | 工具链版本漂移 | 子 Agent | 已完成：React/Vite/TS/Query/Router/Vitest 与 Playwright 基础设施、浏览器 smoke 已交付；M11 负责最终全量 E2E 验收 |
| M1-03 | M1 | 建立显式 Compose、PostgreSQL+pgvector、配置样例和 CI | `deploy/compose.yml`, `Dockerfile*`, `.env.example`, `.github/workflows/**` | M1-01 | `docker compose -f deploy/compose.yml config`; Docker smoke | 误读父目录 Compose | 子 Agent | 已完成 |
| M1-04 | M1 | 建立 OpenAPI-first、Problem Details、cursor、ETag、Idempotency、SSE envelope | `api/openapi/**`, `internal/presentation/**`, `web/src/api/**` | M1-01,M1-02 | contract test、生成客户端无漂移 | 概念契约字段遗漏 | 主 Agent + 子 Agent | 已完成当前已发布 API 基线：OpenAPI/Problem、稳定 cursor、ETag、幂等与持久 SSE envelope/replay；后续领域接口继续 additive 演进 |
| M2-01 | M2 | Eino 16 项最小 PoC（模型、Embedding、Retriever、Streaming、Schema、Tool、Callback、River） | 独立 `internal/platform/eino/**`, `research/eino-poc/**` | M1-01,M1-03 | `make test-eino-poc` 逐项报告；race/资源关闭 | 框架类型侵入领域 | 子 Agent，主 Agent 复核 | 已完成：采用门禁已执行；未通过项构成不正式采用 Eino 的证据，而非未完成工作 |
| M2-02 | M2 | 根据 PoC 锁定或拒绝 Eino，建立 Adapter/Fake/ADR | `docs/architecture/adr/0013*`, `internal/agentadapter/**` | M2-01 | Adapter Contract；替换路径测试 | 锁定不可替换 | 主 Agent | 已完成：主模块不正式采用 Eino |
| M3-01 | M3 | 设计并实现 Core/Change/Workflow/Retrieval/Learning/Ops 迁移 | `migrations/**`, `internal/platform/migration/**`, `internal/**/adapter/postgres/**` | M0-01,M1-01 | 空库/升级/重复迁移/前向迁移策略测试 | 漏实体或版本字段 | 子 Agent | 已完成：迁移与 PostgreSQL Repository 已采用项目内手写 pgx 实现，未采用 sqlc |
| M3-02 | M3 | 落实部分唯一索引、FK/多态引用策略、CHECK、乐观锁和幂等作用域 | `migrations/**`, `internal/**/adapter/postgres/**` | M3-01 | 并发约束测试、重复消息测试 | DB 约束弱于领域规则 | 主 Agent + 子 Agent | 已完成：数据库约束、CAS、幂等作用域与 PostgreSQL 集成回归已覆盖 |
| M3-03 | M3 | 建立领域 ID、状态机、聚合命令、事件和错误契约 | `internal/foundation/**`, `internal/{workspace,knowledge,changecontrol,workflow,retrieval}/**` | M0-01,M1-01 | 纯函数/状态机/非法转移测试 | 第三方类型渗透 | 主 Agent | 已完成：领域 ID、状态机、聚合命令和稳定错误契约已落地并由领域测试覆盖 |
| M3-04 | M3 | 建立固定 Workspace、Fake Model/Parser/Git/FS/Retrieval、AI Gold Set 和容量生成器 | `internal/**/{fake,testfixture}/**`, `eval/**`, `internal/capacity/**` | M3-03 | fixture 校验、确定性复现、无 Secret | 测试数据与生产数据混淆 | 子 Agent | 已完成：固定 Workspace、领域 Fixture/Fake、确定性评测集与容量生成入口已建立；最终容量门禁仍归 M10 |
| M4-01 | M4 | 实现 Workflow Definition/Run/Node、River job 映射和租约 | `internal/workflow/**`, `cmd/worker/**` | M3-01,M3-03 | Worker claim/heartbeat/complete 集成测试 | 双状态机 | 子 Agent | 已完成：M4-A/B/C/D 子任务与真实 River/PostgreSQL smoke 已归档 |
| M4-02 | M4 | 实现 Outbox、重试/退避、暂停/恢复/取消、Human Task | `internal/workflow/**`, `migrations/**` | M4-01 | crash、lease expire、duplicate event、double submit | 重复副作用 | 子 Agent | 已完成：重试/后继/Human/控制面、duplicate/lease reclaim 与故障恢复已验证 |
| M4-03 | M4 | 实现 Side Effect 执行判定、补偿和 `MANUAL_RECOVERY_REQUIRED` | `internal/workflow/**`, `internal/audit/**` | M4-02 | 未知结果不自动重试；恢复演练 | 状态误判 | 主 Agent + 子 Agent | 已完成 Workflow/Writeback 安全判定；通用 append-only Audit 仍归 M10-01 |
| M5-01 | M5 | 实现 Workspace 安全路径、扫描、Source/Version Hash 和 Git CLI Adapter | `internal/workspace/**`, `internal/platform/{filesystem,gitcli}/**` | M3-03 | path/symlink/dirty workspace/commit failure 测试 | 覆盖用户文件 | 子 Agent | 已完成 |
| M5-02 | M5 | 实现 Markdown/TXT 解析、Source Span、分块和隔离 | `internal/ingestion/**`, `internal/platform/parser/**` | M5-01,M3-04 | 重复导入、异常编码、代码/表格完整性、隔离测试 | 引用错位 | 主 Agent + 子 Agent | 已完成：Attempt/Projection/Span/Chunk、API/Workflow Node、Compose/API smoke 与 PostgreSQL/race 验证通过 |
| M5-03 | M5 | 实现 Proposal/Revision/Approval/Write Authorization/Change Hash | `internal/changecontrol/**`, `internal/tools/**` | M4-01,M5-01,M3-03 | 未批准写入拒绝、版本冲突、过期授权、双审批 | 绕过唯一写入 seam | 主 Agent | 已完成：Proposal/Approval/Hash/preflight、服务端短期授权、绑定校验、过期/撤销、幂等消费；实际 Safe Writeback 副作用留 M5-04 |
| M5-04 | M5 | 实现原子写回、Git Commit、DB mapping、增量索引请求和反向 Commit | `internal/changecontrol/**`, `internal/retrieval/**` | M5-03 | file/git/db/index/regression 故障注入；知识变更 E2E | Saga 半完成 | 主 Agent + 子 Agent | 已完成：真实 Safe Writeback、Git/DB mapping、Reindex/Regression/Activation/Completion 及 response-loss 恢复闭环由 M6 收口 |
| M5-05 | M5 | 实现 Topic/Claim/Relation/Evidence/Conflict 领域规则 | `internal/knowledge/**`, `migrations/**` | M3-03,M5-02 | 五分类关系、条件冲突、证据可达、对称去重 | 关系第二事实源 | 主 Agent + 子 Agent | 已完成：统一 Domain/Application/PostgreSQL、00017 约束、receipt-first/CAS/批量锁与真实 PG/Compose 门禁通过 |
| M6-01 | M6 | 实现 FTS/pgvector/Embedding/Index Version/RRF/Dedup/Rerank Adapter | `internal/retrieval/**`, `queries/**` | M5-02,M3-01,M2-02 | 检索 contract、过滤正确、降级显式、index switch | 向量维度/中文分词 | 子 Agent | 已完成：M6-A/B/C/D、Search/Evidence HTTP/OpenAPI、top-100 Cursor、真实 PostgreSQL/River/Compose 闭环及全量质量门禁通过；ANN/P95 归 M10 |
| M6-02 | M6 | 实现 Agent 结构化输出、引用校验、拒答、冲突和模型版本记录 | `internal/agent/**`, `internal/platform/models/**`, `internal/{knowledge,retrieval}/**`, `eval/**` | M2-02,M5-05,M6-01 | RAG eval、schema repair、citation/refusal test | 幻觉与假成功 | 主 Agent + 子 Agent | 已完成：严格四类 Schema、三阶段 Repair、完整 Citation tuple、Eligibility/Formal Claim、Conflict disclosure、Model Run/Call、真实 PostgreSQL/Workflow/Compose 与两轮审查通过；真实 Provider 质量阈值归 M11 |
| M6-03 | M6 | 实现 Tool Registry、Schema、Capability、SSRF、命令和输出安全 | `internal/tools/**`, `internal/platform/**` | M5-03,M2-02 | permission matrix、prompt injection、SSRF、command injection | 模型文本越权 | 主 Agent + 子 Agent | 已完成：版本化 Registry/Schema、持久 Workflow Policy/Tool Call、SSRF/命令/路径/脱敏、trusted write audit 与真实 PG/River/Compose fault smoke 全部通过 |
| M6-04 | M6 | 实现 RAG API、会话、SSE 流式展示和反馈评测 | `internal/app/**`, `web/src/features/rag/**` | M1-04,M6-02,M6-03 | RAG E2E、SSE reconnect、拒答样本 | 流式草稿被误当完成 | 主 Agent + 子 Agent | 已完成：T01-T17 已交付 Conversation/Question/Answer/Feedback、RAG v2、持久阶段/SSE、`/chat`、真实 PostgreSQL/River integration、disposable Compose smoke、文档同步、全量审查和归档 |
| M7-01 | M7 | 实现 Graph Projection、局部/全局/路径查询和证据延迟加载 | `internal/graph/**`, `web/src/features/graph/**` | M5-05,M6-01 | 分页、路径正确、超时降级、非全量渲染 | 图谱毛线球 | 子 Agent | 已完成：Graph 只读 Topic/Claim 投影、Global/Local/Path、Evidence lazy page、公共 HTTP、真实 PG/容量/浏览器 smoke 已归档；500,000 Relation/FPS 与正式认证仍归 M10 |
| M7-02 | M7 | 实现 Semantic Link Candidate、fingerprint、确认/忽略 Proposal | `internal/graph/**`, `internal/changecontrol/**` | M7-01,M5-03 | 忽略候选不重复；内容变化再评估 | 候选噪声 | 主 Agent + 子 Agent | 已完成：Candidate→typed Proposal→Approval→Relation、持久 Topic scan/River、fault/eval/browser smoke、全量门禁与两轮独立复审通过；目录 scope 仍等待稳定对象契约 |
| M7-03 | M7 | 实现 Smart Collection Query AST、列表/表格/卡片和健康扫描 | `internal/collection/**`, `internal/health/**` | M3-01,M5-05,M1-04 | cursor、列/过滤/分组、fingerprint、Issue 幂等 | 查询注入/第二事实源 | 主 Agent | 已完成：M7-only 工作提交 `9ac2a9d`，全量 Go/Web/PG/River/API/Worker/browser/secret/OpenAPI 门禁与独立复验通过；不含 M7-04、认证和 M10 最终容量，当前由 Trellis Finish 执行归档/journal |
| M7-04 | M7 | 实现 Knowledge Event、Timeline、Impact Analysis | `internal/knowledge/**`, `internal/health/**`, `internal/audit/**` | M5-04,M7-03 | Commit/Proposal/Conflict/下游互查；只生成报告/Proposal | 级联误写 | 主 Agent + 子 Agent | 当前范围完成：Timeline/Impact v2、Artifact/Review 影响绑定、owner events 和 approval-only `downstream_update` Proposal 已交付。下游真实执行器与 Document/Evaluation 专用影响类型不属于当前合同；OTel/Prometheus 已由 M10 观测子任务交付，全局 Audit 剩余范围归 M10-01。 |
| M8-01 | M8 | 实现 Artifact 大纲、分章生成、来源覆盖、Markdown 导出和入库 Proposal | `internal/artifact/**`, `web/src/features/artifacts/**` | M6-02,M7-03 | 先大纲后正文、缺口显式、导出可追踪 | 产物污染正式知识 | 子 Agent | 已完成：M8-only 工作提交 `0852a4f`，Artifact Revision/Citation/generation、Markdown 导出、Publish Proposal、API/Worker/UI、真实 PostgreSQL/浏览器 smoke、全量门禁与独立复验均通过并归档；正式知识写回仍由 Change Control 后续流程拥有。 |
| M8-02 | M8 | 实现 Review Deck/Card、Evidence 绑定、多维评分和 FSRS Adapter | `internal/review/**`, `internal/platform/scheduler/**` | M5-05,M6-02,M8-01 | 卡片失效、答题幂等、评分解释、schedule 同事务 | 复习错误知识 | 子 Agent | 已完成：Review-only Session、Deck/Card/Schedule、可信评分与冻结 Scorer/FSRS version、HMAC `question_ref`、Answer/Schedule/receipt 原子提交、legacy quarantine/失效投影及严格 API/Web/SSE 已交付；动态补验通过 Review PostgreSQL 全包、并发 race、M8/Review 迁移 race、前端 69/69 文件共 746/746 测试，以及真实 API/Worker/Vite 的桌面与 390x844 浏览器闭环。 |
| M8-03 | M8 | 实现 Interview Session、知识缺口、共享 Learning Path 和 Memory 生命周期 | `internal/review/**`, `internal/memory/**`, `internal/agent/**` | M8-02,M7-04 | 面试报告、只记录明确确认 Memory、可编辑删除 | 隐式长期记忆 | 子 Agent | 已完成：真实 PostgreSQL Guarded Down、Review Learning Path reservation/hold 并发与 ABANDONED reopen/fencing，以及 Conversation RAG attempt-scoped 非证据 Memory snapshot 均已交付并有代码/归档测试证据。全局 Agent/ChatModel Memory 注入是禁止的实现方式；`last_used_at`、recent-use UI 和自动类型转化不在当前稳定需求范围。 |
| M9-01 | M9 | 实现 Dashboard/Inbox/Documents/Proposals/Workflows/Settings | `web/src/features/**`, `web/src/routes/**` | M1-02,M1-04,M5-02,M5-03 | route integration、空/错/恢复状态、a11y | 页面先于契约漂移 | 子 Agent | 已完成：真实 Workspace 列表/API、响应式 shadcn open-code 工作台、严格状态与桌面/移动 smoke 已交付 |
| M9-02 | M9 | Proposal Revision 编辑、三方合并、历史和重新审批 | `internal/changecontrol/**`, `web/src/features/business/**`, `api/openapi/**` | M5-03,M5-04 | 复用已有 merge/domain/HTTP/组件验证；本轮定向修复旧库升级 | 历史数据升级失败 | 子 Agent | 功能已实现；旧库迁移兼容、历史保持、重复升级、失败回滚重试与非法回填拒绝已通过必要实库回归，见 [M9 修复记录](research/m9-legacy-upgrade-2026-09-08.md)。完整浏览器/资源/并发矩阵不在此次修复范围，未执行不记 PASS。 |
| M9-03 | M9 | 实现异步导出任务、脱敏、权限/过期和结果追踪 | `internal/export/**`, `web/src/features/{collections,settings}/**` | M7-03,M1-04 | Markdown/JSON/附件导出、权限/过期、任务恢复；复用已交付 Collection 三视图 | 擅自引入 XLSX/CSV | 子 Agent | 当前范围完成：Collection `MARKDOWN|METADATA_JSON` 与 Workspace `ATTACHMENTS_ZIP` 已交付，AC-33 已关闭。`EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX 已移出当前产品范围，不作为 M9 残留。 |
| M9-04 | M9 | 实现统一 SSE Event Store、重连、查询失效和异步 UX | `web/src/events/**`, `internal/presentation/sse/**` | M1-04,M4-02 | Last-Event-ID、超窗重查、页面刷新恢复 | SSE 被当事实源 | 子 Agent | 已完成：Workspace 唯一连接、Last-Event-ID、权威回查、定向失效、切换清理和 RAG 迁移已交付 |
| M10-01 | M10 | slog/OTel/Metrics/append-only Audit 与最小查询 | `internal/audit/**`, `cmd/audit/**`, `internal/platform/observability/**` | M4-01,M5-03,M6-03 | 有界查询、作用域与安全输出的必要验证 | 日志泄密 | 子 Agent | 已按精简范围交付：显式 Workspace/global、只读连接、有界分页/超时及固定安全摘要查询，镜像打包已补；Go 单测/vet、隔离实库和 Linux 构建通过。见 [审计记录](research/audit-closeout.md)；全域审计 UI/自动归档平台移出范围。 |
| M10-02 | M10 | 完成认证、Session/Token、CSRF/Origin、Capability 和安全负测 | `internal/auth/**`, `internal/tools/**`, `web/**` | M1-04,M6-03 | auth/security suite 全通过 | 自托管越权 | 主 Agent + 子 Agent | 已完成：工作提交 `1d2e341`；单用户 Auth、Cookie Session、受限 API Token、CSRF/Origin、Capability、真实 PostgreSQL/Compose smoke、全量 Go/Web/OpenAPI 门禁与独立复验均通过。 |
| M10-03 | M10 | 交付容量工具与有界查询基线 | `internal/capacity/**`, `internal/{graph,retrieval}/**`, `deploy/capacity-benchmark.sh` | M6-01,M7-01,M9-03 | 按实际性能问题选择已有工具 | 容量阈值未实测 | 子 Agent | 按精简范围交付：500k harness、P95/EXPLAIN runner 已有；完整目标环境运行与正式 Graph FPS 未执行，不保留为开发欠项，也不宣称阈值通过。 |
| M10-04 | M10 | Docker/readiness/migration 与最小停写备份恢复 | `deploy/**`, `cmd/migrate/**`, `docs/operations.md` | M1-03,M5-04,M10-01 | 参数/文件保护、隔离样例备份读取与恢复 | 停写/数据身份前提 | 子 Agent | 已按精简范围交付：`backup.py create/verify`、单 Root/Git＋整库、私有 marker/hash 和新目标还原步骤。10 条保护测试与隔离 PG18 基本恢复通过，见 [备份记录](research/backup-closeout.md)；完整应用一致性/灾备/跨机和自动修复未执行或未实现，移出本轮范围。 |
| M11-01 | M11 | 完成六条 seam、需求矩阵与用户指南最终演示和 Playwright E2E | `web/e2e/**`, `deploy/*smoke*.sh`, `testdata/**` | M5–M10 | 固定 Fixture 14 步场景通过 | E2E 只测假页面 | 主 Agent + 子 Agent | 按用户要求延后，未在本轮执行：Artifact、Graph/Health、Collection/Review/Interview/Export 有局部真实浏览器 smoke，Knowledge Change/RAG 有 Compose/API smoke；缺统一六 seam Playwright、若干完整用户链路和 14 步最终演示。 |
| M11-02 | M11 | 完成 AI Eval、关系/文章/图谱/Review 回归门禁 | `eval/**`, `docs/architecture/quality.md`, `docs/requirements.md` | M2-02,M6-02,M8-02,M11-01 | 指标基线、高风险下降非零退出 | 质量只看通过率 | 子 Agent | 按用户要求延后，未在本轮执行：Agent 与 Semantic Link 两套确定性门禁可运行；缺 Article、Artifact、Graph、Review/Interview 等版本化套件、真实 Provider 门禁、批准 baseline 比较和统一回归入口。 |
| M11-03 | M11 | 完成全量 review、文档同步、SBOM、运行/部署/回滚说明和交付包 | `README.md`, `docs/**`, `deploy/**`, `Makefile` | 全部前置 | `make verify`; 文档与行为逐项核对 | 漏交付物 | 主 Agent | 按用户要求延后，未在本轮执行：README、MIT License、运行/部署/回滚文档和 Docker 基线已存在；缺统一 `make verify`、SBOM、漏洞/镜像扫描、发布包/校验和、全量发布 CI 和最终 review 证据。 |

## 3. Parallelization Rules

- M0-02 与 M0-03 可并行，但不得修改同一文件。
- M1-01、M1-02 可在公共 API 最小契约冻结后并行；M1-03 依赖后端端口和数据库选择。
- M2、M3 的研究/测试可并行，生产领域模型和迁移必须由主 Agent 先冻结。
- M6-01 与 M6-02 只有在 Retrieval/Agent 契约冻结后才能并行。
- M7、M8、M9 可按文件边界并行；所有公共 OpenAPI、迁移、领域模型和生成客户端由主 Agent 统一整合。
- 任何高风险迁移、写回、认证和恢复任务不允许多个 Agent 同时修改同一文件。

## 4. Per-Task Definition of Done

本节按当前改动风险选用，不累加为每个开发任务的完整测试门禁；M11 与长期/跨环境矩阵按新版 PRD 分离。

每项任务关闭前必须：

1. 更新本任务状态和对应需求追踪。
2. 读取目标模块 `.trellis/spec` 与相关 docs。
3. 为行为变更与真实缺陷保留有意义的回归证据，低影响文档/状态修改不新增形式化测试。
4. 按受影响范围运行必要单测、集成/契约、lint/typecheck/compile 和构建，已有同版本有效证据可复用。
5. 对需要实际运行才能确认的行为补最小烟测，不把完整页面/浏览器矩阵作为每次开发关闭的统一前置。
6. 检查 diff，确认无第二事实源、静默 fallback、吞异常或未授权写入。
7. 运行适用的 Go 或前端/脚本质量审查；SQL 与数据边界并入同一次检查，不叠加同义流程。
8. 同步 API、迁移、配置、产品/架构/运行文档。

## 5. Canonical Verification Commands

本轮实际命令、结果和未执行边界集中在 [收尾记录](research/lean-closeout-2026-09-08.md)。必要检查覆盖路由组件、审计查询、历史迁移兼容与备份工具，未重跑全仓矩阵。

当前 `make test`、`rag-integration`、`openapi-check`、Compose Search/Tool/RAG smoke 等入口已存在。以下保留为 M11 的候选发布编排，不表示所有命令均已实现或本轮必须运行；最终采用范围由 M11 明确：

```bash
make verify
go test -race ./...
go vet ./...
npm ci --prefix web
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
docker compose -f deploy/compose.yml config
docker compose -f deploy/compose.yml up -d --wait
make migrate-test
make contract-test
make integration-test
make e2e
make eval-regression
make security-test
make benchmark-capacity
```

备份直接使用 `python3 deploy/backup.py create` / `verify`，新目标恢复步骤见运行手册；不为额外 Make 包装或大型灾备矩阵挂开发欠项。清理只允许针对验证自行创建的隔离资源，禁止把 `down -v` 当作用户安装的常规验收步骤。

## 6. Rollback Points

- M1：只删除未被用户数据使用的骨架/Compose。
- M3：迁移只前进；应用保持旧 Schema 兼容，禁止默认 down migration。
- M4：停 Worker、释放租约、重放 Outbox；未知副作用进入人工恢复。
- M5：使用 reverse Git Commit，重新构建投影，不重写 Git 历史。
- M6–M8：切回上一 Active Prompt/Model/Workflow/Index Version。
- M10–M11：保留上一镜像、上一迁移兼容版本、上一评测基线和备份 Marker。
