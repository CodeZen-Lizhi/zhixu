# 架构落地任务清单

## Foundation

- [ ] 初始化 Go Module、API、Worker。
- [ ] 初始化 React/TypeScript。
- [ ] Docker Compose PostgreSQL + pgvector。
- [ ] 配置与 Secret。
- [ ] 统一 ID、Clock、Error。
- [ ] CI 基线。

## Workspace

- [ ] Workspace 创建校验。
- [ ] 安全路径解析。
- [ ] Source Version Hash。
- [ ] 文件扫描。
- [ ] Git CLI Adapter。
- [ ] 一致性检查。

## Database

- [ ] Core Migration。
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

- [ ] REST/OpenAPI。
- [ ] SSE。
- [ ] Dashboard。
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
- [ ] Docker Smoke。

