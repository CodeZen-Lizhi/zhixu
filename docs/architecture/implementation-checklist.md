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
- [ ] Workflow/Outbox Migration。
- [ ] Retrieval Migration。
- [ ] Review/Health Migration。
- [ ] sqlc Query。
- [ ] Testcontainers。

## Workflow

- [ ] Definition Registry。
- [ ] Run/Node 状态机。
- [ ] Lease/Heartbeat。
- [ ] Retry/Backoff。
- [ ] Human Task。
- [ ] Idempotency。
- [ ] Compensation。

## Ingestion/Retrieval

- [ ] Markdown/TXT Parser。
- [ ] PDF Parser Adapter。
- [ ] Web Parser Adapter。
- [ ] Chunk Strategy。
- [ ] FTS。
- [ ] Embedding Batch。
- [ ] pgvector HNSW。
- [ ] RRF/Dedup。
- [ ] Rerank Adapter。
- [ ] Index Version。

## Knowledge/Change

- [ ] Topic/Claim。
- [ ] Relation/Evidence。
- [ ] Conflict。
- [ ] Proposal Revision。
- [ ] Approval Change Hash。
- [ ] Atomic Write。
- [ ] Git Commit。
- [ ] Index Compensation。

## Agent/RAG

- [ ] ChatModel Adapter。
- [ ] Structured Output。
- [ ] Schema Repair。
- [ ] Relation Classification。
- [ ] Citation Validation。
- [ ] RAG Refusal/Conflict。
- [ ] Memory。

## Features

- [ ] Article Optimization。
- [ ] Artifact。
- [ ] Graph。
- [ ] Semantic Links。
- [ ] Smart Collection。
- [ ] Knowledge Health。
- [ ] Timeline/Impact。
- [ ] Review/FSRS。
- [ ] Interview Simulation。

## Application

- [x] REST/OpenAPI（Workspace 创建、详情和扫描；其余领域接口待后续阶段）。
- [ ] SSE。
- [x] Dashboard（M1 系统状态与 M3 Workspace 页面基础）。
- [ ] Inbox/Document。
- [ ] Diff/Approval。
- [ ] Graph UI。
- [ ] Review UI。
- [ ] Workflow UI。

## Quality

- [ ] Structured Logs/Trace/Metrics。
- [ ] Audit。
- [ ] Security Tests。
- [ ] AI Eval Suites。
- [ ] Capacity Test。
- [ ] Backup/Restore Drill。
- [ ] Consistency Recovery Drill。
- [x] Docker Smoke（M1 基线与 M3 Workspace 业务烟测）。
