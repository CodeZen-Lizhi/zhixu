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
- [ ] Review/Health Migration。
- [ ] sqlc Query。
- [ ] Testcontainers。

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
- [ ] Memory。

## Features

- [ ] Article Optimization。
- [ ] Artifact。
- [x] Graph（M7-01 Topic/Claim canonical read projection）。
- [x] Semantic Links（M7-02 Candidate→typed Proposal→Approval→Relation，Topic scan）。
- [ ] Smart Collection。
- [ ] Knowledge Health。
- [ ] Timeline/Impact。
- [ ] Review/FSRS。
- [ ] Interview Simulation。

## Application

- [x] REST/OpenAPI（Workspace、Workflow、Ingestion、Search/Evidence、Proposal/Knowledge、Conversation/RAG 等已实现范围）。
- [x] 持久 Server Event Store、Last-Event-ID 恢复与 Workspace 唯一 SSE Owner（M9-04）。
- [x] Dashboard（M9-01 真实 Workspace/依赖/待办投影）。
- [x] Inbox/资料版本（M9-01 Source Version 列表与详情；正式 Document 聚合仍未交付）。
- [x] Diff/Approval（M9-02 File/Relation Diff、批准/驳回、Hash 冲突与 Apply Preflight）。
- [x] Graph UI（Global/Local/Path、Evidence 与 Candidate panel）。
- [ ] Review UI。
- [x] Workflow UI（M9-01 列表/详情与 pause/resume/cancel）。
- [x] Settings UI（M9-01 只展示和操作已有服务端契约）。
- [x] RAG Conversation UI（`/chat`、阶段恢复、Citation/Feedback、桌面/移动）。

## Quality

- [ ] Structured Logs/Trace/Metrics。
- [ ] Audit。
- [ ] Security Tests。
- [x] Agent 确定性 Eval 基线（真实 Provider 质量阈值仍归 M11）。
- [ ] Capacity Test。
- [ ] Backup/Restore Drill。
- [ ] Consistency Recovery Drill。
- [x] Docker Smoke（基础、Workspace、Search、Tool 与 RAG Conversation 独立闭环）。

> 边界：上述勾选只表示对应已实现切片已有代码和门禁，不表示项目整体完成。PDF/Web Parser、HNSW/容量、
> M7-04 Timeline/Impact、M8、M9-03 异步导出/脱敏/结果追踪、正式 Document/Article Revision、M10 Auth/安全/
> 可观测/备份恢复，以及 M11 全量 E2E/发布交付仍未完成。
