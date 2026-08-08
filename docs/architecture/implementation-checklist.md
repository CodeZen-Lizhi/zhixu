# 架构落地任务清单

## Foundation

- [x] 初始化 Go Module、API、Worker。
- [x] 初始化 React/TypeScript。
- [x] Docker Compose PostgreSQL + pgvector。
- [x] 配置与 Secret。
- [x] 统一 ID、Clock、Error。
- [x] CI 基线。

## Workspace

- [x] Workspace 创建校验。
- [x] 安全路径解析。
- [x] Source Version Hash。
- [x] 文件扫描。
- [x] Git CLI Adapter。
- [x] 一致性检查。

## Database

- [x] Core Migration。
- [x] Workflow/Outbox Migration。
- [x] Retrieval Migration。
- [x] Review/Health Migration（核心 schema 与后续硬化迁移均纳入项目迁移序列）。
- [x] PostgreSQL Repository Query（采用手写 pgx Repository；不采用 sqlc）。
- [x] 可丢弃 PostgreSQL 集成测试环境（通过 `ZHIXU_TEST_DATABASE_URL` 注入；不采用 Testcontainers）。

## Workflow

- [x] Definition Registry（最小版本化 Definition）。
- [x] Run/Node 状态机（最小持久化状态与约束）。
- [x] Lease/Heartbeat（领取、续租和过期回收）。
- [x] Retry/Backoff。
- [x] Human Task（单次、版本校验与过期拒绝）。
- [x] Idempotency（Definition、Node Completion 与 Outbox event key）。
- [x] Side Effect 安全恢复/人工恢复判定（通用业务补偿继续按各领域实现）。

## Ingestion/Retrieval

- [x] Markdown/TXT Parser。
- [ ] PDF Parser Adapter。
- [ ] Web Parser Adapter。
- [x] Chunk Strategy。
- [x] FTS。
- [x] Embedding Batch。
- [ ] pgvector HNSW。
- [x] RRF/Dedup。
- [x] Rerank Adapter。
- [x] Index Version。

## Knowledge/Change

- [x] Topic/Claim。
- [x] Relation/Evidence。
- [x] Conflict。
- [x] Proposal Revision。
- [x] Approval Change Hash。
- [x] Atomic Write。
- [x] Git Commit。
- [x] Index 恢复/回归门禁。

## Agent/RAG

- [x] ChatModel Adapter。
- [x] Structured Output。
- [x] Schema Repair。
- [x] Relation Classification。
- [x] Citation Validation。
- [x] RAG Refusal/Conflict、Clarification 与 v2 Answer。
- [x] Conversation/Question/Answer/Feedback 与 retrieval-first Workflow。
- [ ] Memory（M8-03 Candidate/Confirm、编辑/暂停/恢复/删除/到期与 Interview scoped effective-context 已完成；通用 Agent 注入、`last_used_at`、最近使用展示和 `EPISODIC`→`PREFERENCE` 转换仍属后续产品范围）。

## Features

- [ ] Article Optimization。
- [x] Artifact（M8-01 Outline→Section generation、Revision/Citation、Markdown export、Publish Proposal 与可恢复 reservation/receipt 已交付；正式知识写回仍由 Change Control 拥有）。
- [x] Graph（M7-01 Topic/Claim canonical read projection）。
- [x] Semantic Links（M7-02 Candidate→typed Proposal→Approval→Relation，Topic scan）。
- [x] Smart Collection（M7-03 Query AST、统一 read model、列表/表格/卡片）。
- [x] Knowledge Health（M7-03 Issue、持久扫描、决策、修复与调度）。
- [x] Timeline/Impact（M7-04 与遗留收口已交付 Timeline UI、v1/v2 报告、Artifact/Review Card impact、一等 owner event 与正式 `downstream_update` Proposal 意图；批准保持零执行副作用，下游 executor、Document/Eval impact 与全局 Audit 仍属后续范围）。
- [x] Review/FSRS（M8-02 Review-only Session、Card/Schedule、可信评分、冻结 Scorer/FSRS version、API-only 共享/派生 HMAC `question_ref`、失效与 legacy quarantine）。
- [x] Interview Simulation（M8-03 独立 Interview 会话、共享基表上的 INTERVIEW origin Path、难度/deadline/连续追问、Completion reservation/digest hidden hold、ABANDONED→ORPHANED 维护与服务端 INTERVIEW Candidate）。
- [x] Review-derived Shared Learning Path 运行闭环（领域/Repository/HTTP/Web、`00060`、Artifact hidden hold、真实 Service composition 与 24 小时 reservation maintenance 已接线，并通过 fresh PostgreSQL/API/Worker/Vite/浏览器主链路验证）。

## Application

- [x] REST/OpenAPI（Workspace、Workflow、Ingestion、Search/Evidence、Proposal/Knowledge、Conversation/RAG 等已实现范围）。
- [x] 持久 Server Event Store、Last-Event-ID 恢复与 Workspace 唯一 SSE Owner（M9-04）。
- [x] Dashboard（M9-01 真实 Workspace/依赖/待办投影）。
- [x] Inbox/资料版本（M9-01 Source Version 列表与详情；正式 Document 聚合仍未交付）。
- [x] Diff/Approval（M9-02 File/Relation Diff、批准/驳回、Hash 冲突与 Apply Preflight）。
- [x] Graph UI（Global/Local/Path、Evidence 与 Candidate panel）。
- [x] Review/Interview/Memory UI（M8-02 `/review`、M8-03 `/interviews` 与 `/memories`，含两种 origin 的 Path 严格 decoder、Workspace cache/SSE 恢复和倒计时；真实后端主链路已完成桌面与 390x844 浏览器验证）。
- [x] Workflow UI（M9-01 列表/详情与 pause/resume/cancel）。
- [x] Settings UI（M9-01 只展示和操作已有服务端契约）。
- [x] RAG Conversation UI（`/chat`、阶段恢复、Citation/Feedback、桌面/移动）。

## Quality

- [x] Structured Logs 与平台级 Trace/Metrics 基线（slog、API→River consumer OTel Trace、
  API/Worker 独立 Prometheus Registry 与 `/metrics`）。
- [ ] 产品级 Model/Tool/Token/成本可观测、外部历史存储、告警与 Dashboard。
- [ ] Audit。
- [ ] Security Tests。
- [x] Agent 确定性 Eval 基线（真实 Provider 质量阈值仍归 M11）。
- [ ] Capacity Test。
- [ ] Backup/Restore Drill。
- [ ] Consistency Recovery Drill。
- [x] Docker Smoke（基础、Workspace、Search、Tool 与 RAG Conversation 独立闭环）。

> 边界：上述勾选只表示对应已实现切片已有代码和门禁，不表示项目整体完成。PDF/Web Parser、HNSW/容量、
> Timeline/Impact 下游 executor、Document/Eval impact 与 POISONED 运维入口、上述 Memory 后续产品缺口、
> Review Path reservation/hold 专门并发与 ABANDONED 重开、Export 的 Evaluation/Audit/CSV/XLSX 等
> 未覆盖格式、正式 Document/Article Revision、M10 后续安全、产品级 Model/Tool/Token/成本观测、
> 外部历史/告警 Dashboard、备份恢复，以及 M11 全量 E2E/发布交付仍未完成。

> M8-02/M8-03 已补验 Review/Interview/Memory PostgreSQL、M8/Review migration race、前端 746 条测试，以及真实 API/Worker/Vite 的桌面与 390x844 浏览器主链路；业务数据 Down guard、Review Path reservation/hold 专门并发与 ABANDONED 重开仍未直接覆盖。
