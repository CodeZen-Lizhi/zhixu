# ZHIXU 产品级建设与交付

## Goal

将当前仅包含产品与架构文档的 ZHIXU 仓库，按 `docs/product/PRD.md`、已接受 ADR 和本任务确认的补充约束，建设为可运行、可测试、可维护、可部署、可恢复、可审计、可交付的本地优先个人知识工作台。

产品核心价值是让用户在保有 Markdown 与 Git 数据所有权的前提下，完成“资料进入 → 解析与索引 → 证据检索与关系判断 → Proposal → 人工审批 → 安全写回 → Git 版本 → 重新索引与验证 → 图谱/健康/复习”的完整闭环。

## Authoritative Sources

事实优先级：

1. 用户当前明确要求与本任务附加目标。
2. `AGENTS.md` 与 `.trellis/workflow.md`。
3. 本任务 `prd.md`、`design.md`、`implement.md`。
4. `docs/product/PRD.md` 与已接受 ADR。
5. 其他架构文档、运行手册和实现清单。
6. 当前代码和实际运行行为。

当前仓库不存在 `doc/`，只有 `docs/`；本任务统一以 `docs/` 为需求和架构事实目录。

## Confirmed Current State

- 当前分支为 `dev`，基线提交 `7ffef09`。
- `README.md` 明确项目处于产品与架构设计阶段。
- 仓库没有业务源码、`go.mod`、`package.json`、SQL/迁移、OpenAPI、测试、Dockerfile、项目 Compose 或 CI。
- `go test ./...` 和 `go vet ./...` 因缺少 Go module 失败；`npm test` 因缺少 `package.json` 失败。
- `docs/product/PRD.md` 定义正式 v1.0 全量范围和 AC-01..AC-36；当前 36 项均无运行证据。
- `.trellis/spec/backend/` 与 `.trellis/spec/frontend/` 仍为占位模板，现有 `00-bootstrap-guidelines` 任务未完成。
- Eino 在仓库文档、依赖和代码中均不存在；当前文档只定义 OpenAI-Compatible/Ollama Adapter。
- 表格明确需求是 Smart Collection 的列表/表格/卡片视图、列选择、排序、过滤、分组、分页和空状态；导出明确为 Markdown、附件、领域元数据 JSON、评测和审计摘要，没有 Excel/CSV 导入导出要求。

## Product Scope

### P0 — 产品运行与安全闭环

- 项目骨架、配置、数据库迁移、API/Worker、前端和 Docker Compose。
- Workspace、Source Version、Markdown/Git 事实源和一致性检查。
- 持久化 Workflow、River 投递、Outbox、Human Task、重试、取消、幂等和补偿。
- Proposal、Approval、Tool Permission、Safe Writeback、Git Commit、索引更新和只读恢复。
- Markdown/TXT 摄取、结构化分块、FTS/pgvector、RRF、版本化索引和搜索。
- 可观测性、审计、认证、安全负测、备份恢复和最小业务烟测。

### P1 — 正式 v1 核心体验

- PDF/网页摄取、文章优化、Claim/Topic/Relation/Conflict。
- Eino PoC 通过后的 AI Adapter、结构化输出、Tool Calling、引用校验、拒答与评测。
- RAG、图谱、语义关联、Smart Collection、知识健康、时间线与影响分析。
- Artifact、Review Card、FSRS、面试模拟和 Memory。
- 对应前端页面、OpenAPI 契约、SSE 状态和端到端测试。

### P2 — 重要增强

- 容量基线压测、图谱聚类/布局缓存、全量索引蓝绿切换。
- 可选本地模型 Profile、OTel/Prometheus 导出、评测趋势和更丰富的恢复演练。

### Out of Scope

沿用正式 PRD：多用户/RBAC、SaaS 计费、微服务、Kubernetes、独立图数据库、独立向量数据库、Redis/Kafka 必需依赖、通用低代码数据库、通用拖拽工作流、Agent Swarm、移动原生 App、WYSIWYG、Canvas、音视频处理、自动互联网发布和未经审批修改外部系统。

Excel/CSV 导入导出当前不在正式范围；除非后续文档明确新增，不得因“表格能力”自行扩展。

## Requirements

### R-01 项目基础与规范

- 完成后端、前端、数据库、错误、日志、测试和安全规范，所有规范有真实代码示例后方可关闭 Bootstrap Guidelines。
- 锁定 Go、Node、PostgreSQL、pgvector、River、Eino（如采用）和前端依赖版本。
- 提供统一的 `make`/脚本入口，禁止依赖开发者记忆零散命令。

### R-02 架构与依赖边界

- 采用 Go 模块化单体，API 与 Worker 两进程共享领域模块和 PostgreSQL。
- 领域层不得依赖 HTTP、pgx/sqlc、River、Eino、模型 SDK、Git、文件系统或 React 类型。
- 第三方能力通过 Adapter 接入，公共契约只使用领域类型和稳定错误模型。

### R-03 事实源与数据一致性

- 原始 Source Version 不可变；正式知识由 Markdown + Git 持有。
- PostgreSQL 保存投影、关系、工作流、审批、审计、评测和学习状态。
- 文件/Git/数据库/索引通过可恢复 Saga 协作；不一致时进入明确只读恢复状态。

### R-04 Workflow、Proposal 与审批

- PostgreSQL 是业务 Workflow 事实源，River 只负责可执行 Job 投递。
- 所有副作用有幂等键、可判定执行结果和补偿/人工恢复路径。
- Agent 只能输出结构化结果或 Tool Request，不得直接操作文件、数据库或 Git。
- 所有正式知识和关系修改必须经过 Proposal → Evidence Validation → Approval → Version Check → Safe Writeback。

### R-05 摄取、检索与 RAG

- 支持 Markdown、TXT、PDF、网页和粘贴文本的可追踪摄取；不安全内容隔离。
- 支持 Source Span、结构化分块、FTS、pgvector、RRF、可选 Rerank 和版本化索引。
- 默认只检索最新批准 Revision；降级必须显式。
- RAG 回答必须提供可打开引用、冲突说明、推断标识和拒答能力。

### R-06 知识与学习功能

- 支持 Article Revision、Claim、Topic、Relation、Conflict、Graph、Semantic Link、Collection、Health、Timeline、Artifact、Review、Interview 和 Memory。
- AI 生成的关系、卡片、文章修改和知识产物必须有证据、版本、状态和人工控制。

### R-07 API 与前端

- REST 承载命令/查询，SSE 承载单向状态事件；SSE 不是事实源。
- 写命令要求 Idempotency-Key，修改要求 Version/ETag，列表统一 cursor + limit。
- OpenAPI 是前后端契约事实源，生成前端客户端并做 breaking-change 检查。
- 所有异步页面可刷新恢复；错误展示阶段、影响、可重试性和错误编号。

### R-08 表格与数据能力

- Smart Collection 支持列表、表格、紧凑卡片三种视图。
- 表格支持固定领域字段的列选择、排序、过滤、分组、分页、空状态和虚拟滚动；不建设通用公式字段或数据库设计器。
- 导出支持 Markdown、附件、领域元数据 JSON、评测结果和审计摘要，包含 schema_version 与 Workspace ID。
- 导出权限、敏感字段脱敏、文件生命周期、幂等、可追踪性和公式注入防护需有设计与测试；Excel/CSV 保持未实现。

### R-09 安全、性能与运维

- 本地模式绑定 localhost；自托管必须单用户认证、HTTPS、CSRF/Origin 与 Session/Token 生命周期控制。
- 防止路径穿越、Symlink Escape、SSRF、Prompt Injection、XSS、SQL/命令注入和 Secret 泄漏。
- 审计为 append-only 业务记录，普通日志不得替代审计。
- 达到 50 万 Chunk/Relation 容量基线下的检索、图谱和页面指标，或提供可复现的未达标证据与架构升级决策。
- 提供非 root 多阶段镜像、显式 Compose、备份恢复、一致性恢复和升级/回滚说明。

### R-10 Eino 采用门禁

- Eino 仅可位于 Agent、Application、Adapter 或 Infrastructure 层。
- 先完成 16 项最小 PoC；PoC 通过后新增 ADR、锁版本、建立 Fake Adapter 和集成测试。
- Eino 不得成为持久化 Workflow、Proposal/Approval、Tool Permission、Evidence Validation 或 Safe Writeback 的事实源。
- PoC 失败时回退到 OpenAI-Compatible HTTP Adapter 或厂商 SDK Adapter，不修改领域契约。

## Acceptance Criteria

最终产品必须逐项证明 `docs/product/PRD.md` 的 AC-01..AC-36，并满足：

- [ ] 一条明确命令可启动 API、Worker、PostgreSQL/pgvector 和 Web，前端页面非空。
- [ ] 六条最高层业务 seam 均有确定性 E2E：知识变更、文章优化、RAG、Artifact、图谱/健康、Collection/Review。
- [ ] 正常路径、边界路径、失败路径、故障注入和恢复路径均有自动测试。
- [ ] 所有正式写入可关联 Proposal、Approval、Tool Authorization、Git Commit、Workflow Run 和 Audit。
- [ ] Eino 16 项 PoC 有逐项 PASS/FAIL 报告；采用与否有 ADR 和替换路径。
- [ ] PostgreSQL 迁移、sqlc 生成、关键约束、EXPLAIN、pgvector/FTS 和 50 万容量基线有可重复验证。
- [ ] OpenAPI、前端生成客户端、SSE 重连、cursor 分页、ETag/Idempotency 行为通过契约测试。
- [ ] 表格视图、分页、过滤、列选择、批量非破坏性操作和既定导出能力通过 UI/E2E 验收。
- [ ] 路径穿越、SSRF、Prompt Injection、越权工具、XSS、CSRF、SQL/命令注入和 Secret Redaction 测试通过。
- [ ] Docker Smoke、备份恢复演练、一致性恢复演练和最终 11 步演示场景通过。
- [ ] `go test -race ./...`、静态检查、前端 lint/typecheck/test/build、Playwright、AI Eval、漏洞扫描和镜像扫描全部通过。
- [ ] 所有文档、ADR、API、迁移、配置、运行和回滚说明与实际行为一致。

## Known Requirement Conflicts and Resolutions

1. `doc/` 与 `docs/`：仓库只有 `docs/`，采用 `docs/`。
2. `PRD-outline.md` 标记“待确认”，`PRD.md` 标记正式 v1.0 需求稿：采用后者；大纲仅作历史参考。
3. API 文档采用 Cursor Pagination，PRD Search 示例使用 `page`：公共 API 统一 cursor + limit，UI 可做页码表现层映射，并同步修订 PRD。
4. accepted ADR-0010 明确 SSE，PRD 存在“WebSocket 或 SSE”表述：统一 SSE。
5. 现有 AI 文档未提 Eino，用户新增 Eino 优先要求：执行 PoC，限定 Adapter 边界，PoC 后新增 ADR。
6. 文档没有 Excel/CSV：只实现既定表格视图和 Markdown/JSON 类导出，不扩大范围。

## Open Decisions That Block Specific Later Tasks

这些问题不阻塞 M0/M1 骨架，但在对应任务开始前必须以 PoC/ADR 或用户决策关闭：

- Eino PoC 后是否采用及锁定版本。
- 自托管认证使用 Cookie Session 还是 Bearer Token；推荐 Cookie Session + CSRF，CLI/自动化再提供受限 Token。
- 首个 Embedding 模型、维度与中文 FTS tokenizer；必须通过检索 PoC 与评测选择。
- 备份 RPO/RTO、频率、保留、加密和密钥管理；推荐个人本地默认 RPO 24h、RTO 2h，允许配置。
- 开源 License；正式公开分发前必须确定。
