# Journal - lizhi (Part 1)

> AI development session journal
> Started: 2026-07-16

---



## Session 1: M1 可运行骨架交付

**Date**: 2026-07-16
**Task**: M1 可运行骨架交付
**Branch**: `dev`

### Summary

完成 Go API/Worker、React Web、PostgreSQL+pgvector、OpenAPI、Compose、CI 与真实烟测；修复 PostgreSQL 18 数据目录、错误分类、405 Problem、DSN 密码转义、迁移幂等和请求日志，并归档 M1。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `2e5610a` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 2: M2 Eino 隔离采用门禁

**Date**: 2026-07-16
**Task**: M2 Eino 隔离采用门禁
**Branch**: `dev`

### Summary

完成 Eino Chat Graph、ToolsNode、Callback、权限/错误/限流/脱敏 PoC；独立 Review 修复副作用误重试和 Secret 泄漏，并对未经过真实 Eino/River/Provider 的门禁明确判定 FAIL/SKIP，最终不正式采用 Eino。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `91b42ce` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 3: 完成 M3 Workspace 事实源闭环

**Date**: 2026-07-16
**Task**: 完成 M3 Workspace 事实源闭环
**Branch**: `dev`

### Summary

完成 Workspace/Source/SourceVersion PostgreSQL 迁移与幂等仓储、Git/文件安全适配器、Create/Open/Scan 应用服务、REST/OpenAPI、React Workspace 页面和真实 Compose 业务烟测；修复运行镜像缺少 Git CLI，并验证重复扫描复用版本、同路径变更创建新版本和 Source Version 不可变。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `3546ef1` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 4: 完成 M4 Workflow 持久化闭环

**Date**: 2026-07-16
**Task**: 完成 M4 Workflow 持久化闭环
**Branch**: `dev`

### Summary

完成 Workflow Definition/Run/Node/Human Task/Outbox 持久化、合法状态、租约与过期回收、节点幂等完成、人工任务版本校验和跨 Workspace 事件作用域校验；接入 202 Run API、查询和 Human Decision API，加入 Idempotency-Key，完成真实 Compose smoke 和独立 PostgreSQL race 集成测试。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `fa5f7a7` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 5: 完成 M5 Proposal 审批与安全预检

**Date**: 2026-07-16
**Task**: 完成 M5 Proposal 审批与安全预检
**Branch**: `dev`

### Summary

实现 Proposal/Revision/Approval 持久化、版本化 Change Hash、服务端真实文件基线预检、稳定 HTTP/OpenAPI 契约和 Compose/PostgreSQL 烟测；未执行正式文件写回。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `315494e` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 6: 加固 M5 幂等与失效状态

**Date**: 2026-07-16
**Task**: 加固 M5 幂等与失效状态
**Branch**: `dev`

### Summary

补齐 Proposal 创建 Idempotency-Key 持久化与重放、审批相同决定重放、审批前真实基线检查，以及目标不可用转 needs_revision；完成 PostgreSQL、Compose 和 API 烟测。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `abc9f5a` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 7: 完成 M5 摄取解析与规范化分块

**Date**: 2026-07-17
**Task**: 完成 M5 摄取解析与规范化分块
**Branch**: `dev`

### Summary

实现不可变 Artifact 两阶段读取、Ingestion Attempt 状态机、Markdown/TXT Parser、Source Span、Canonical Chunk、API/Workflow Adapter 与 PostgreSQL 约束；通过 race、vet、OpenAPI、真实 PostgreSQL、Compose 和 API 幂等烟测。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `2a8e681` | (see git log) |
| `584ec5d` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 8: M5 Write Authorization

**Date**: 2026-07-17
**Task**: M5 Write Authorization
**Branch**: `dev`

### Summary

实现服务端短期 Write Authorization：审批与 Workflow 严格绑定、数据库可信时钟、过期/撤销、完整绑定前置校验、并发一次性幂等消费和 PostgreSQL 约束；明确 M5-04 在文件写入点执行最终 Target Version CAS。完成真实 PostgreSQL、全仓 race/vet、Web/Eino、OpenAPI、Compose build/readiness smoke 与 Go Quality Gate。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `87ea888` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 9: M5-04A Safe Writeback Persistence

**Date**: 2026-07-17
**Task**: M5-04A Safe Writeback Persistence
**Branch**: `dev`

### Summary

完成 Approval Git HEAD、Proposal 乐观锁、Durable Writeback Execution、Commit Mapping 与 Reindex Outbox；真实 PostgreSQL、全仓 race/vet、make test、Compose readiness 均通过。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `39565ea` | (see git log) |
| `f53e95b` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 10: M5-04B Filesystem CAS

**Date**: 2026-07-17
**Task**: M5-04B Filesystem CAS
**Branch**: `dev`

### Summary

实现本地 POSIX Safe Writeback：device/inode 跨进程锁、真实 Markdown Prepare、最终 Base/identity CAS、独立备份、原子替换、RestoreCAS/Cleanup；补齐路径/特殊文件/篡改/故障注入/helper subprocess 与 20 轮 race，并同步错误与安全规范。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `b0a6484` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 11: M5 Git writeback adapter

**Date**: 2026-07-17
**Task**: M5 Git writeback adapter
**Branch**: `dev`

### Summary

实现受限 Git 写回 Adapter：clean/HEAD/单目标 Diff、raw blob 与 immutable tree/ref CAS Commit、Trailer 幂等恢复、严格 Reverse、安全边界及真实 Git 测试；同步父任务与恢复规范。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `9894844` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 12: 完成 M5-04D 可恢复安全写回 Saga

**Date**: 2026-07-17
**Task**: 完成 M5-04D 可恢复安全写回 Saga
**Branch**: `dev`

### Summary

完成原子双授权 Begin、可重启 LocalFS/Git 检查点、发布与补偿、真实 PostgreSQL/LocalFS/Git smoke；修复 response-loss 同内容换 inode 恢复误判，并通过全量门禁与独立复验。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `ecadffb` | (see git log) |
| `f4dd50a` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 13: 完成 M4-A River Runtime Foundation 并纳入 fault smoke

**Date**: 2026-07-17
**Task**: 完成 M4-A River Runtime Foundation 并纳入 fault smoke
**Branch**: `dev`

### Summary

纳入 fault smoke 集成测试文件；完成 River/Goose 迁移、legacy FS 兼容、Definition/Executor Registry、River Adapter、PostgreSQL+River Start UoW 与 deterministic smoke；修复单连接池迁移锁阻塞并移除伪 ingest 定义；通过 race/count20/integration/vendor/Makefile/OpenAPI/Compose 门禁；归档 M4-A。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `ecadffb` | (see git log) |
| `8cc284d` | (see git log) |
| `c4df8af` | (see git log) |
| `bf33c56` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 14: 收口 M4-B Workflow Runtime State Machine

**Date**: 2026-07-17
**Task**: 收口 M4-B Workflow Runtime State Machine
**Branch**: `dev`

### Summary

完成 DB-time Claim/Heartbeat、Attempt reclaim/fencing、Retry/Fail/Manual/Complete、唯一后继、Human wait/submit、Pause/Resume/Cancel checkpoint、River Worker transport/Human outcome、00012 迁移与契约文档；fault smoke 保持历史提交 ecadffb。通过 make test、go test -race、go vet、OpenAPI、PostgreSQL/River integration。M4-C/M4-D 尚未实施。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `63f0a83` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 15: 完成 M4-C Approval Safe Writeback Dispatch

**Date**: 2026-07-17
**Task**: 完成 M4-C Approval Safe Writeback Dispatch
**Branch**: `dev`

### Summary

完成 Approved HTTP 到 River/Safe Writeback 原子闭环、Proposal-Run 复合绑定、Bootstrap exact recovery、transport lease retry 修复、真实双 Worker smoke、文档与全量门禁；kill-9、可观测性和 side-effect cancel 归 M4-D。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `cda2192` | (see git log) |
| `db2376f` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 16: 完成 River Runtime M4-D 可运维交付

**Date**: 2026-07-17
**Task**: 完成 River Runtime M4-D 可运维交付
**Branch**: `dev`

### Summary

纳入 Safe Writeback fault/normal、Approval River 与独立数据库 SIGKILL rescue smoke；完成 Worker 配置、readiness、lifecycle、可观测性、取消安全、Docker/Compose、文档与全量门禁；归档 M4-D 及 River Runtime 父任务。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `08f0cc8` | (see git log) |
| `d756e16` | (see git log) |
| `bf1750d` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 17: 完成 M6-A Retrieval Index Foundation

**Date**: 2026-07-18
**Task**: 完成 M6-A Retrieval Index Foundation
**Branch**: `dev`

### Summary

完成版本化 Embedding/Index、Manifest、FTS/Vector Projection、原子激活回滚、pgvector Pool 注册、真实 PostgreSQL/race/EXPLAIN 门禁与文档同步。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `a88a5ce` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 18: 完成 M6-B Reindex Consumer

**Date**: 2026-07-19
**Task**: 完成 M6-B Reindex Consumer
**Branch**: `dev`

### Summary

实现 Safe Writeback Reindex Outbox 消费、committed source 捕获、完整 Workspace FTS Snapshot、Delivery/River 恢复、原子 Completion 与真实 fault smoke，并通过全量门禁和独立复验。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `b765075` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 19: 完成 M6-C Embedding 与混合检索

**Date**: 2026-07-19
**Task**: 完成 M6-C Embedding 与混合检索
**Branch**: `dev`

### Summary

完成直接 OpenAI-Compatible/Ollama Embedding Adapter、可恢复批量向量构建、Active-only Keyword/Semantic/Hybrid Search、RRF/去重/Rerank、显式降级、真实 PostgreSQL/River fault smoke、规格同步与全量质量门禁，并归档 M6-C。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `03b199c` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 20: 完成 M6-D Search API 与 M6 Retrieval 收口

**Date**: 2026-07-19
**Task**: 完成 M6-D Search API And Integration，并归档 M6 Retrieval 父任务
**Branch**: `dev`

### Summary

交付 Workspace-scoped Keyword/Semantic/Hybrid Search HTTP/OpenAPI、top-100 HMAC Cursor、可打开的不可变
Source Version/Span Evidence、API/Worker 共享 Embedder Factory、严格 Web Decoder，以及真实 PostgreSQL/River/
Compose 的 Approval→Writeback→Reindex→Search→Evidence→Completion 闭环；M6-01 已完成并归档。

### Main Changes

- 新增 `POST /api/v1/search` 与两个 Evidence GET 资源，严格拒绝未知字段、非法 UTF-8、孤立 surrogate、
  显式 null/错误类型、空 mode/cursor，并保持稳定 Problem 分类。
- Cursor 绑定规范请求、Active Index、完整 top-100 结果 Hash 与 offset；篡改、跨请求和结果漂移 fail closed。
- Evidence 通过 PostgreSQL 全绑定与不可变 Artifact Reader 复核，最多返回 4 KiB UTF-8 excerpt，不泄漏路径/locator。
- 新增真实生产 SQL JSON EXPLAIN、River response-loss fault smoke、disposable Compose 黑盒 smoke 与安全输出 canary。
- OpenAPI、Web Decoder、产品/架构/部署/安全/测试文档及 backend/frontend code-spec 已同步。

### Git Commits

| Hash | Message |
|------|---------|
| `0cee7b1` | `feat: 完成 Search API 与集成闭环` |
| `5290713` | `chore(task): 归档 M6 Retrieval` |

### Testing

- `go test -race ./internal/retrieval/... ./internal/httpapi ./internal/app ./cmd/api ./cmd/worker ./internal/platform/models`
- `go test -race -count=20 ./internal/retrieval/domain ./internal/retrieval/application ./internal/retrieval/http`
- 全新迁移 disposable PostgreSQL：`go test -race -tags=integration -count=1 -p 1 ./...`
- `go vet ./...`、`make test`、`go mod tidy -diff`
- Web lint/typecheck/test/build：5 files、41 tests 通过
- `node api/openapi/check.mjs`、Compose config、`make compose-search-smoke`、`make docker-build`、`git diff --check`
- 主 `go-review`、`sql-code-review`、`code-review-and-quality` 与两路独立复验均无剩余 P0/P1/P2。

### Status

[OK] **Completed**

### Next Steps

- 按依赖先实施 M5-05 Knowledge Domain，再进入 M6-02 Agent 结构化输出、引用校验、拒答与冲突处理。


## Session 21: 完成 M5-05 Knowledge Domain

**Date**: 2026-07-19
**Task**: 完成 Topic、Claim、Relation、Evidence、Conflict 统一领域事实边界
**Branch**: `dev`

### Summary

新增 Knowledge Domain/Application/PostgreSQL 深模块与 `00017_knowledge_domain.sql`，冻结 Relation Assessment、
Applicability v1、Provenance、确认、状态机、端点兼容、Conflict 和幂等收据契约；完成真实 PostgreSQL、
全仓 integration、race、Web/Go 构建与 Compose readiness 门禁。

### Main Changes

- 九个写命令均 receipt-first，重放不再调用 Provenance/Confirmation/ID/Clock；CAS、固定锁序和 response-loss 可恢复。
- Topic alias、Relation Evidence、Conflict Member 批量写入；Conflict Transition 按 NodeType 固定两批加锁，消除成员级 N+1。
- Relation Evidence 区分 owner-bound 持久 Hash 与业务语义 Hash，支持不同幂等键的 fingerprint replay。
- 迁移新增九张表、Provenance/生命周期/deferred constraints、命令-聚合矩阵、索引和有数据 `55000` Down guard。
- 同步领域、数据库、模块、测试文档与 backend spec；两轮独立复验发现的 P1/P2 均已修复并关闭。

### Testing

- `go test -race -count=20 ./internal/knowledge/domain ./internal/knowledge/application ./internal/knowledge/adapter/postgres`
- 全新 PostgreSQL：`go test -race -tags=integration -count=1 -p 1 ./...`
- `go test -race ./...`、`go vet ./...`、`make test`、`go mod tidy -diff`、`git diff --check`
- `make docker-build`；Compose migrate/API/Worker healthy；`/readyz` 与首页 smoke 通过并清理卷。

### Status

[OK] **Completed**

### Next Steps

- 归档 M5-05 后创建并实施 M6-02 Agent：结构化输出、Schema Repair、引用校验、拒答、冲突与模型版本记录。


## Session 22: 完成 M6-02 Agent 结构化输出与引用闭环

**Date**: 2026-07-19
**Task**: 完成 M6-02 Agent 结构化输出与引用闭环
**Branch**: `dev`

### Summary

交付严格四类 Schema、INITIAL/REPAIR/REDUCED、完整 Citation tuple、Knowledge Eligibility 与 Formal Claim、Disputed Conflict disclosure、Faithfulness Review、可查询 Model Run/Call 实际版本、真实 PostgreSQL/Workflow/Eval/Docker 门禁；两轮独立复验关闭全部 P1/P2，并归档 M6-02。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `b56cd32` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 23: 完成 M6-03 Tool Registry 与安全闭环

**Date**: 2026-07-19
**Task**: 完成 M6-03 Tool Registry 与安全闭环
**Branch**: `dev`

### Summary

交付版本化 Tool Registry、canonical Capability、严格 Tool Schema、持久 Workflow Policy/Tool Call、SSRF/命令/路径与输出安全、Safe Writeback audit bridge，以及真实 PostgreSQL/River/Compose fault smoke；主审查与独立两轮复验关闭全部当前范围 P0/P1/P2。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `648b0d4` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 24: M6-04 Conversation 与 SSE 持久层检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T04-T05）
**Branch**: `dev`

### Summary

完成 Conversation create/list/read/turn/answer Repository、8 Turn/32 KiB Published Context、JSONB canonical
readback、持久 Server Event Store、caller-owned AppendTx 与 SSE replay/heartbeat/cancel。主审修复了 Conversation
exact replay 对 24 小时事件投影的错误依赖；事件清理后幂等创建仍以 Conversation 权威事实恢复。

### Main Changes

- Conversation 创建与 `conversation.created` 首次写入保持同一事务；event append failure 全部回滚。
- commit response-loss、并发同键、不同请求冲突、事件投影清理后的 exact replay 均有真实 PostgreSQL 证据。
- Conversation/Turn 使用稳定 keyset pagination；Turn/Answer/Workflow 与 Published Context 均为有界批量查询。
- Server Event 只公开稳定 ID、状态、版本和计数；Last-Event-ID 支持 invalid/future/expired、窗口内补发和无游标水位起步。
- 两轮独立只读审查无剩余问题；生产 `/api/v1/events` Router/Composition Root 接线按计划归 T11。

### Testing

- `go test ./internal/conversation/... ./internal/events/... ./internal/platform/migration -count=1`
- `go test -race ./internal/conversation/... ./internal/events/... -count=1`
- 真实 PostgreSQL：`go test -race -tags=integration -count=1 -p 1 ./internal/conversation/... ./internal/events/...`
- `go vet ./internal/conversation/... ./internal/events/...`、`go mod tidy -diff`、`gofmt`、`git diff --check`

### Status

[IN PROGRESS] M6-04 T01-T05 completed; T06-T17 pending.

### Next Steps

- 实施 T06 Question → pending Answer → Workflow/Outbox/River Job/Event 的原子派发与 response-loss recovery。


## Session 25: M6-04 Question 原子派发检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T06）
**Branch**: `dev`

### Summary

完成 Question、pending Answer、Workflow Run/Node/Outbox、River Job 与 `answer.pending` Server Event 的单事务派发；同键精确重放、异键活动 Workflow 冲突、归档语义、failed/cancelled 后继续及 response-loss 恢复均由真实 PostgreSQL 验证。

### Main Changes

- 复用 `BuildRuntimeStartRequest` 与 `RuntimeRepository.StartTx`，未复制 Workflow/River SQL。
- `00021` 以 Conversation 锁和非终态 Run 约束替代 pending Answer 唯一索引，并提供稳定 trigger constraint 与 guarded Down。
- Workflow Input 只保存 ID、ordinal 和 context hash；Question/历史正文不进入 Workflow 或 Server Event。
- 修复 exact replay 与 Worker Claim 并发时的旧 Run 快照误判；锁后重读最新 Answer/Workflow 投影。

### Testing

- 新并发重放真实 PostgreSQL 回归：修复前稳定失败，修复后 `-race -count=20` 通过。
- Conversation 与 Migration 真实 PostgreSQL integration `-race` 通过。
- `go test -race -count=1 ./...`、`go vet ./...`、`go mod tidy -diff`、`make test`、`git diff --check` 通过。
- 主 Agent go/SQL/通用五轴审查与独立两轮复验通过，全部 P1/P2 已关闭。

### Status

[OK] M6-04 T06 completed; T07-T17 pending.

### Next Steps

- 实施 T07 Query Plan/RAG Workflow output receipt、严格 catalog 注册与 Conversation execution-context loader。


## Session 26: M6-04 RAG Workflow 契约与执行上下文检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T07）
**Branch**: `dev`

### Summary

完成 Conversation RAG Workflow 的严格发布回执、PLAN/RAG v2/Clarification Runtime Catalog 注册，以及从持久 Question/Answer/Workflow 事实恢复冻结执行上下文的窄端口。

### Main Changes

- Workflow output 只保存六字段 canonical receipt；Answer 发布状态到结果类型由 Conversation Domain 唯一维护，不复制正文、检索摘要或 Provider 数据。
- Runtime Catalog 保留全部既有 v1 Schema，并精确新增 `agent.rag-query-plan/v1`、`agent.rag-answer/v2` 与 `conversation.clarification/v1`；每个 envelope 的 `schema_version` 绑定精确 `SchemaRef.Version`。
- `QuestionExecutionContextLoader` 使用一次 Question/Answer/Run 联合查询和一次有界历史查询，恢复 scope、depth、format、当前 Question 与最多 8 Turn/32 KiB 历史。
- Loader 校验 Workspace/Conversation/Question/Answer/Run/ordinal/hash 完整绑定，并重算实际持久历史 hash；字段彼此一致但历史被篡改时仍 fail closed。

### Testing

- `go test -race -count=1 ./internal/agent/... ./internal/conversation/...`
- 真实 PostgreSQL：`go test -race -tags=integration -count=1 -p 1 ./internal/conversation/... ./internal/agent/adapter/workflow`
- `go vet ./internal/conversation/... ./internal/agent/...`、`go mod tidy -diff`、`git diff --check`
- 主 Agent 执行 Trellis、Go、SQL 与通用五轴审查，修复状态/结果类型重复映射并补齐三终态回执、精确两次数据库调用断言；独立只读审查未发现 P0-P2。

### Status

[OK] M6-04 T07 completed; T08-T17 pending.

### Next Steps

- 实施 T08 retrieval-first Query Plan 与 RAG Executor，保持 Search 直接走 Retrieval Application seam，并把终态原子发布留给 T09 finalizer。

## Session 27: M6-04 RAG Executor 与原子发布检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T08-T09）
**Branch**: `dev`

### Summary

完成 retrieval-first RAG Executor、真实阶段事件、deferred Retrieval 与 Model Run/Answer/Conversation/Event 原子 finalizer；Provider 后持久化不确定状态统一禁止自动重试。

### Main Changes

- 单次严格 PLAN、1..3 rewrite、scoped Search、跨 rewrite 去重、Eligibility/Conflict/Topic allowlist、RAG v2 与 Citation/Faithfulness gate。
- Workflow 只从 `QuestionExecutionContextLoader` 构建不持久化模型输入；terminal receipt 在 Provider 前恢复。
- `00022` 允许 RAG RUNNING 延迟绑定 Retrieval，终态一次性绑定；旧 Relation 行为保持。
- Finalizer 单事务完成 Model Run、Answer、Conversation activity 与 terminal event，覆盖 CAS、并发和 response-loss。
- PLAN/retrieval/validation 阶段事件使用真实时钟，按 source ref advisory lock 精确重放且 payload 脱敏。

### Testing

- affected packages `go test -race`、`go vet`、`go mod tidy -diff`、`git diff --check` 全通过。
- 真实 PostgreSQL agent/conversation/knowledge/events/migration integration 通过；Migration legacy adoption 已更新到版本 22。
- 独立三轮审查关闭 progress 假时间、Reduced Refusal 未闭合、post-provider 重试、progress replay 等 P1/P2，最终无剩余 P0-P2。

### Status

[OK] M6-04 T08-T09 completed; T10-T17 pending.

### Next Steps

- 实施 T10 Conversation/Answer/Feedback HTTP 与 OpenAPI，再进入 T11 生产 API/Worker composition。

## Session 28: M6-04 Conversation HTTP 与 OpenAPI 检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T10）
**Branch**: `dev`

### Summary

完成 Conversation、Question、Turn、Answer 与 Feedback 的 REST 边界、Feedback append-only PostgreSQL 持久化，以及 Conversation/RAG/SSE OpenAPI 契约；生产真实依赖组装继续由 T11 负责。

### Main Changes

- 7 条 Conversation/Answer/Feedback REST 路径支持严格 JSON、`application/json`、Idempotency-Key、稳定 scoped cursor、分页、202/200 replay、ETag/304、Problem 与跨 Workspace 防枚举。
- Feedback Application/Repository 校验五类反馈、Citation 闭包与 canonical hash；真实 PostgreSQL 覆盖创建、exact replay、异请求冲突、跨 Workspace 和 Answer/Knowledge 不变。
- Answer ETag 同时绑定 Answer/Workflow version，避免 Workflow 独立推进时错误 304。
- OpenAPI 新增 Conversation/Question/Turn/Answer/Feedback/SSE 契约和结构 gate；RAG result Citation 与外层可打开 Citation 分离，数量/字节上限与领域一致。

### Testing

- `make openapi-check`
- `go test -race -count=1 ./internal/conversation/... ./internal/app ./cmd/api`
- 真实 PostgreSQL：`go test -race -tags=integration -count=1 -p 1 ./internal/conversation/adapter/postgres ./internal/app`
- `go vet ./internal/conversation/... ./internal/app ./cmd/api`、`go mod tidy -diff`、`git diff --check`
- 主 Agent Go/SQL/通用五轴审查修复 ETag、wire schema、媒体类型与公开上限漂移；独立两轮复验最终无剩余 P0-P2。

### Status

[OK] M6-04 T10 completed; T11-T17 pending.

### Next Steps

- 实施 T11 API/Worker production composition 与 readiness，确保真实 RAG definition/executor 依赖缺失时 fail closed。

## Session 29: M6-04 RAG Production Composition 检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T11）
**Branch**: `dev`

### Summary

完成 API/Worker 的真实 RAG 依赖组装、SSE 生产路由和 capability readiness；显式关闭保持只读能力，启用但依赖不完整时禁止接收 Question 或启动半注册 Worker。

### Main Changes

- API 使用同一 PostgreSQL Event Store 组装 Conversation 写事件与 SSE replay，并复用唯一 Workflow Runtime 原子派发 Question。
- API 注册冻结 RAG contract/definition；完整 RAG composition 失败时保留 read/feedback/SSE，但 Question Dispatcher 不注入，readiness 返回脱敏 503。
- Worker 真实组装 Chat/Catalog、Agent PG、Conversation Context/Finalizer、Event Progress、Retrieval、Knowledge Eligibility/Topic，并同时注册 Relation 与 RAG Executor/Definition。
- Worker readiness 校验 Relation/RAG 双 Executor 与双 Definition 可达；Chat disabled 不注册 Agent，Tool/Safe Writeback/Reindex 保持独立。
- System Status/OpenAPI 新增 strict RAG capability 状态。

### Testing

- `make openapi-check`
- API/Worker/App 与 Agent/Conversation/Knowledge/Retrieval/Events/Workflow 定向 `go test -race`
- 真实 PostgreSQL：Worker 测试创建临时数据库、执行全量迁移、Ping/关键表检查，再 Resolve RAG/Relation/SafeWriteback Executor 与 Definition。
- `go vet`、`go mod tidy -diff`、`git diff --check`
- 主 Agent Go/SQL/通用五轴审查；独立审查修复 init failure 仍可提交 Question 和伪真实 PG composition 两项问题。

### Status

[OK] M6-04 T11 completed; T12-T17 pending.

### Next Steps

- 实施 T12 typed frontend Conversation/RAG clients 与唯一 SSE owner，再进入 T13 真实 RAG 页面。

## Session 30: M6-04 Typed Conversation 与 SSE 恢复检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T12）
**Branch**: `dev`

### Summary

完成前端 Conversation/RAG 严格传输边界、唯一 SSE fetch-stream owner，以及刷新后从持久事件恢复 Answer 当前阶段的后端投影。

### Main Changes

- Conversation client 从 `unknown` 严格解码 Conversation、Question、Turn、四态 Answer、RAG v2、Refusal、Clarification、Retrieval Summary、Problem 与 ETag/304。
- SSE owner 支持 CRLF/heartbeat/UTF-8/frame bound、Last-Event-ID、409 权威回查后恢复、400 停止、有界退避、异步事件提交与 Abort 资源释放。
- Answer/Turn Query 从同 Workspace、同 Answer 的最新持久 RAG Event 恢复六态 `current_stage`；Answer ETag 同时绑定 stage。
- 新增 `00023` 部分索引与真实 PostgreSQL Down/Up、EXPLAIN、跨 Workspace、终态和非法绑定门禁；OpenAPI 与 System Status 前端同步。

### Testing

- Go focused race、vet、tidy 和 OpenAPI gate 通过。
- 真实 PostgreSQL Conversation/Events/Migration/API integration 通过。
- 前端 lint、typecheck、93 tests 与 production build 通过。
- 主 Agent Go/SQL/通用五轴审查未发现当前范围内明确问题；阶段投影与前端最终独立复验均无 P0-P2。

### Status

[OK] M6-04 T12 completed; T13-T17 pending.

### Next Steps

- 实施 T13 `/chat` 与 `/chat/:conversationId` 真实 RAG 页面、Query owner、阶段/轮询恢复和桌面/移动端浏览器烟测。

## Session 31: M6-04 真实 RAG 页面检查点

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T13）
**Branch**: `dev`

### Summary

完成 `/chat` 与 `/chat/:conversationId` 的真实证据研究台、Workspace 单一 owner、分页/最新 Turn 恢复、SSE/轮询状态层和桌面/移动端交互。

### Main Changes

- 三栏 Conversation rail、Turn timeline、Evidence panel；移动端单列及 Citation 聚焦 drawer。
- Composer 支持 Scope、Retrieval Mode、Depth、Format、Enter/Shift+Enter 与幂等重试；Clarification/Follow-up 可回填下一问题。
- 四态 Answer 穷尽展示，包含真实阶段、Refusal、Conflict、Citation、Retrieval Summary、Related Topic、Follow-up 与 Feedback；Pending 不展示草稿。
- Conversation/Turn Infinite Query 接入 cursor；新增 `latest=true` 单条 Turn 投影保证超过 50 Turn 时仍恢复最新 Answer；断线轮询失败也计入 12 次预算。
- Workspace 页面增加已有 ID 恢复入口，避免浏览器存储丢失后被单 Workspace 限制封死。

### Testing

- `go test -race ./internal/conversation/...`、`go vet`、`go mod tidy -diff`、真实 PostgreSQL latest Turn integration 与 OpenAPI gate 通过。
- 前端 lint、typecheck、105 tests、production build 与 diff check 通过。
- 临时空 PostgreSQL + 真实 API 浏览器烟测：创建 Workspace、Conversation、Chat disabled 明确失败；1280 桌面三栏和 390 移动单列均无横向溢出，console 无应用错误。
- 独立审查发现并关闭 Clarification 假反馈、分页/latest恢复、失败无界轮询、移动Citation、旧cursor与旧latest投影问题。

### Status

[OK] M6-04 T13 completed; T14-T17 pending.

### Next Steps

- 实施 T14 `make rag-integration` 与 `make compose-rag-smoke`，贯通公开 API、River Worker、Retrieval、Agent、Answer、SSE 和 Feedback。

## Session 32: M6-04 真实 RAG 集成与 Compose 门禁

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T14）
**Branch**: `dev`

### Summary

完成公共 Conversation HTTP→River Worker→Retrieval/Knowledge→PLAN/Answer/Review→原子 Answer→SSE→Feedback 的真实 PostgreSQL 与 disposable Compose 门禁。

### Main Changes

- 新增 request-driven OpenAI-compatible fixture，严格 Bearer canary、Schema 三元组和单 JSON 文档，不依赖调用顺序。
- `make rag-integration` 与 `make compose-rag-smoke` 验证 Citation、Related Topic、Follow-up、检索摘要、SSE/Feedback/Question exact replay，并证明首次恰好三次 Model Call、重放零新增。
- 修复 PLAN/REVIEW 缺少 `model_run_ref`、空 Degradation/Citation slice 退化为 nil、Markdown snippet/source excerpt 首尾空白导致 Evidence/Citation gate 失败。
- Compose seed 仅通过 Knowledge Domain/Repository 补无公开 API 的正式资格，不创建或修改 Conversation/Question/Answer/Workflow。

### Testing

- focused Go race/vet、`go mod tidy -diff`、OpenAPI、`make test` 与 106 个前端测试/构建通过。
- `make rag-integration` 真实 PostgreSQL通过。
- `make compose-rag-smoke` 真实 Compose 全链路通过，退出自动清理容器、网络、volume 与临时目录。
- 主 Agent Go/SQL/通用五轴审查无 P0/P1；独立审查两项 P2 中 Compose 调用计数已修，PLAN 输入项确认总预算以服务端绑定后输入为准并同步规范。

### Status

[OK] M6-04 T14 completed; T15-T17 pending.

### Next Steps

- 实施 T15 文档/规范同步与全量门禁，再进行 T16 独立跨层复审和 T17 提交归档。

## Session 33: M6-04 文档同步与全量门禁

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T15）
**Branch**: `dev`

### Summary

把 M6-04 的真实 HTTP、数据库、Worker、模型、SSE 与前端行为同步到 README、架构、测试、部署和父任务状态，并执行全仓质量门禁。

### Main Changes

- README 增加 Conversation/RAG API、`/chat`、Chat fail-closed 配置和 RAG 两条真实门禁说明。
- API/Agent/Workflow/Frontend/Testing/Deployment 文档同步 RAG v2、四态 Answer、阶段恢复、三次 Model Call、SSE 重放和明确 M9/M10/M11 边界。
- Implementation Checklist 与父任务更新已实现切片，保留 Graph/Collection/Artifact/Review/Auth/容量/最终发布未完成事实。

### Testing

- `go test -race -count=1 ./...` 与 `go vet ./...` 通过。
- `make test`、前端 lint/typecheck/106 tests/build、OpenAPI、tidy、task validate 与 diff check 通过。
- `make rag-integration` 与 `make compose-rag-smoke` 通过；T13 已完成 1280/390 真实浏览器烟测，无横向溢出或应用 console error。

### Status

[OK] M6-04 T15 completed; T16 in progress, T17 pending.

### Next Steps

- 对 M6-04 全任务提交范围执行独立跨层/Go/SQL/frontend 审查，修复后归档任务。

## Session 34: M6-04 全量审查与恢复边界收口

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation API And SSE（T16-T17）
**Branch**: `dev`

### Summary

完成 M6-04 独立跨层第二轮审查，关闭本地 SSE cursor 污染、nil Context 和验收追踪问题，进入提交归档。

### Main Changes

- RAG SSE Hook 对浏览器损坏 cursor 先回查权威 Query，成功后清理并无游标重连；回查失败保留 cursor、关闭状态并显式报告错误。
- RAG Executor 对 nil Context 返回稳定 invalid input，不再用 Background 绕过调用方取消链路。
- PRD AC-01..13、父子任务状态和前端测试数同步到真实结果。

### Testing

- `go test -race -count=1 ./...`、`go vet ./...`、`go mod tidy -diff`、`make test` 通过。
- 前端 lint、typecheck、108 tests、production build 通过。
- OpenAPI、Trellis task validate 与 `git diff --check` 通过。
- 独立审查第二轮 P0/P1/P2 均为 0；主 Agent Go 与通用五轴审查未发现当前范围内明显问题。

### Status

[OK] M6-04 T01-T16 completed; T17 commit/archive in progress.

### Next Steps

- 提交当前任务改动，记录 Trellis session 并归档 M6-04；随后返回父任务选择下一项依赖就绪工作。


## Session 24: M6-04 RAG Conversation 交付归档

**Date**: 2026-07-20
**Task**: M6-04 RAG Conversation 交付归档
**Branch**: `dev`

### Summary

完成 RAG SSE 恢复边界、nil Context fail-closed、文档验收同步、全量质量门禁与独立复验，并归档 M6-04。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `338e041` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 25: 完成 Graph M7-01 收口

**Date**: 2026-07-21
**Task**: 完成 Graph M7-01 收口
**Branch**: `dev`

### Summary

完成 Graph Topic/Claim Global、Local、Path、Relation Evidence 查询与真实前端；补齐 cursor window metadata stale 绑定、Evidence 生命周期缓存清理和 Workspace cache boundary。通过 Go race/vet/tidy、make test、OpenAPI、真实 PostgreSQL integration/smoke/benchmark、前端 230 tests/lint/typecheck/build、桌面移动浏览器 smoke 与独立跨层复验；M7-02/M7-03/M7-04、M8-M11 和正式 Auth 仍未完成。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `7a83de3` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 26: 交付 M7-02 Semantic Link Candidates

**Date**: 2026-07-21
**Task**: 交付 M7-02 Semantic Link Candidates
**Branch**: `dev`

### Summary

完成 Candidate/Decision/fingerprint、typed Relation Proposal、Approval 后 Knowledge Relation apply、durable Topic scan/River、严格前端面板、五项离线评测、fault/browser/full gates；修复 Topic pair 无界执行与 PostgreSQL 微秒时间精度导致的 Scan/Run 终态不一致，并通过主审与独立后端 SQL/前端复验。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `fccd262` | (see git log) |
| `6075ea6` | (see git log) |
| `1a9a37d` | (see git log) |
| `0eefb99` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 27: 完成语义关联候选复验与归档

**Date**: 2026-07-21
**Task**: 完成语义关联候选复验与归档
**Branch**: `dev`

### Summary

修复评测忽略样本预排除与跨 Topic scan 恢复问题，完成 Approval 到正式 Relation、持久 Topic scan/River、fault/eval/browser smoke、全量门禁和两轮独立复审，并同步最终契约后归档。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `13bf907` | (see git log) |
| `f452525` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 28: M7-03 Smart Collection 与健康扫描交付

**Date**: 2026-07-22
**Task**: M7-03 Smart Collection 与健康扫描交付
**Branch**: `dev`

### Summary

完成版本化 Smart Collection Query AST、统一动态结果三视图、持久 Health Scan/Issue 生命周期、SMART_COLLECTION Candidate scope、Schedule/affected-change outbox、严格 Collection/Health API 与 Web 页面。主 Agent 执行 go-review、code-review-and-quality、sql-code-review；独立 backend/frontend 复验关闭 Health hydration cursor P2。暂存边界保持 M7-only，工作提交 9ac2a9d、归档提交 b0078bb，未 push；并行 M9 改动继续留在工作树。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `9ac2a9d` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 29: 收口 M7-03 归档后修正

**Date**: 2026-07-22
**Task**: 收口 M7-03 归档后修正
**Branch**: `dev`

### Summary

完成 Collection page/scan 双 revision 的归档后修正、clean-checkout vendor 补漏与确定性 cursor 篡改回归；在隔离 worktree 和 fresh PostgreSQL 上重跑全量门禁及主审/独立复审。任务保持已归档，不纳入 M9 改动，不 push。

### Main Changes

- 修复归档后真实 browser smoke 暴露的 `HEALTH_SCAN_SCOPE_STALE`：保留 `revision_hash` 页面/Health hydration 语义，新增 `scan_revision_hash` 绑定 durable membership。
- 修复 clean checkout 缺失的 `golang.org/x/text` 两个必需 vendor coverage 源文件。
- 修复 Collection list cursor 篡改集成测试的 RawURL Base64 尾部等价脆弱性，改为确定性篡改 HMAC 签名段首字符。
- 验证：目标 PostgreSQL 回归 `-race -count=50`；fresh DB integration、fault smoke、benchmark、真实 API/Worker/Vite browser smoke；全仓 `go test -race -count=1 ./...`、`make test`、`go mod tidy -diff`、npm audit high、secret scan、Trellis validate、diff check 全部通过。
- Review：主 Agent 使用 `go-review` 五轴门禁未发现问题；独立 reviewer 复跑 `-race -count=20`、application race、integration vet、gofmt/diff check，P0-P3 均为 0。
- Git 边界：提交 `2fb87ab`、`fd1ef44`、`8fcda37`、`be98708` 均为 scoped follow-up；现有 M9 暂存/未暂存改动保持原样。


### Git Commits

| Hash | Message |
|------|---------|
| `2fb87ab` | (see git log) |
| `fd1ef44` | (see git log) |
| `8fcda37` | (see git log) |
| `be98708` | (see git log) |

### Testing

- `go test -race -tags=integration -count=50 -run '^TestCollectionRepositoryLifecycleIdempotencyCASAndWorkspaceIsolation$' ./internal/collection/adapter/postgres` 通过。
- fresh migrated PostgreSQL 上 `make collection-health-integration`、`make collection-health-fault-smoke`、`make collection-health-benchmark`、`make collection-health-browser-smoke` 通过。
- clean worktree 上 `go test -race -count=1 ./...`、`make test`、`go mod tidy -diff`、npm audit high、secret scan、归档任务 validate 与 `git diff --check` 通过。

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 30: 完成 M9 业务前端与统一 SSE 交付

**Date**: 2026-07-23
**Task**: 完成 M9 业务前端与统一 SSE 交付
**Branch**: `dev`

### Summary

完成 M9 业务契约与安全读模型、业务前端、Proposal/Diff/审批、Workflow、统一 SSE Event Store 与 Search recovery；全量 Go/Web/OpenAPI/Trellis/数据库/浏览器门禁和主审查、独立复验通过。已完成两个 scoped commit 并归档 07-22-m9-business-frontend；保留并行 M7 未提交改动，不 push。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `d84829e` | (see git log) |
| `f89acec` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 31: M10 认证与安全闭环验证收口

**Date**: 2026-07-25
**Task**: M10 认证与安全闭环
**Branch**: `dev`

### Summary

完成 M10-02 单用户认证的最终收口：Cookie Session、受限 API Token、CSRF/Origin、Capability Middleware、
启动安全校验、Compose loopback 映射守卫、OpenAPI 与前端 Auth Boundary 均已验证。补充后端认证跨层契约、
前端认证状态所有权和任务验收记录；已完成 scoped 工作提交、父任务状态同步与子任务归档，其他里程碑改动保持未提交。

### Main Changes

- Compose 守卫现在在信任 loopback 端口映射时解析最终 Compose 模型，拒绝缺失 host IP、`0.0.0.0` 和非 loopback 绑定；独立审查已复验 IPv4/IPv6/localhost 正例与负例。
- 新增 `.trellis/spec/backend/auth-security.md`，锁定认证 API、配置、数据库摘要、失败矩阵、测试及 Write Authorization 独立边界。
- 前端状态规范记录 `AuthProvider`/`AuthBoundary`、401 清理、CSRF 及严格 decoder 的唯一所有权。

### Git Commits

| Hash | Message |
|------|---------|
| `1d2e341` | `feat: 完成单用户认证与安全边界` |
| `76becd8` | `chore(task): 完成 M10-02 状态` |

### Testing

- `make compose-auth-smoke` 通过：真实 required-auth Compose 栈验证匿名拒绝、Bootstrap Session、Cookie、Origin/CSRF、API Token 创建/撤销与登出。
- 使用一次性 loopback `pgvector/pgvector:0.8.5-pg18-bookworm` 数据库执行 `ZHIXU_TEST_DATABASE_URL=<temporary> make auth-integration` 通过：迁移、认证 PostgreSQL `-race`、重复 Up/非空 Down guard 均绿。
- index 独立快照的 `go test -race -count=1 -timeout 60s ./...`、`go vet ./...`、前端 lint/typecheck/test（52 files / 610 tests）/build、`make openapi-check`、`make compose-check`、Python 编译和 `git diff --cached --check` 全部通过；混合工作树全量前端门禁另通过 52 files / 612 tests。
- 桌面与 `390x844` 浏览器 smoke 已验证 development disabled 工作台/Settings 无 console error、空白或文本重叠；临时 Compose project、卷和数据库均已清理。

### Review

- 主审使用 `go-review`、`sql-code-review`、`code-review-and-quality` 进行五轴质量门禁：当前范围未发现 Critical/Required 问题。
- 同一独立只读审查 Agent 最终复验暂存区，确认请求体阻塞与 API `ReadTimeout` 修复已进入提交，且未混入 Timeline/Review/Export 后续覆盖层；P0/P1/P2 均为 0。

### Status

[OK] **Completed and archived**


## Session 31: 收口 M7-04 Knowledge Timeline 与 Impact Analysis

**Date**: 2026-07-26
**Task**: 收口 M7-04 Knowledge Timeline 与 Impact Analysis
**Branch**: `dev`

### Summary

完成 M7-only Knowledge Timeline、durable Outbox/Worker、只读 Impact Report 与原子 Audit 收口；隔离并提交 68 个 M7 文件，通过全量 Go/Web/OpenAPI/Compose、真实 PostgreSQL 门禁及独立复验，归档 M7-04，保留 M8/M9/M10 并行改动且未 push。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `5cd940b` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 32: 完成 M8-01 Artifact 产物闭环

**Date**: 2026-07-26
**Task**: 完成 M8-01 Artifact 产物闭环

### Summary

交付 Workspace 隔离的 Artifact Revision、服务端 Citation 复核、受控分章生成、Markdown 导出和 PUBLISH_ARTIFACT Proposal；完成真实 PostgreSQL、全仓 race、OpenAPI、前端、API/Worker/Vite 浏览器 smoke 及独立复审。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `0852a4f` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 33: 完成 M8-02 Review/FSRS 与 M8-03 Interview/Memory

**Date**: 2026-07-28
**Task**: 完成 M8-02 Review/FSRS 与 M8-03 Interview/Memory
**Branch**: `dev`

### Summary

完成 Review Deck/Card、可信评分与冻结 scorer/FSRS、HMAC question_ref、原子 Answer/Schedule、失效与 legacy quarantine；完成证据驱动 Interview、难度/deadline/连续追问、SKIPPED gap/path、Learning Path、Memory 生命周期，以及 Completion reservation/digest hidden hold 和 24h ABANDONED/ORPHANED Worker。本轮拆分为三笔 M8 工作提交，并补齐 PostgreSQL、race、前端测试及真实 API/Worker/Vite 浏览器动态验证；两个 M8 子任务已归档，M9/M10 与运行时并行改动保持未提交。

### Main Changes

- 交付 Review Deck/Card、服务端可信评分、冻结 scorer/FSRS version、HMAC `question_ref`、Answer/Schedule/receipt 原子推进、失效与 legacy quarantine。
- 交付证据驱动 Interview、难度/deadline/连续追问、SKIPPED gap/path、Learning Path、Memory 生命周期和 effective context。
- 以 Completion reservation、digest hidden hold 与 24 小时 ABANDONED/ORPHANED Worker 收口跨模块完成态，并同步 OpenAPI、Web、规范与任务文档。

### Git Commits

| Hash | Message |
|------|---------|
| `002c2b5` | `feat(m8): 完成 Review 与 FSRS 学习闭环` |
| `c66cfbf` | `feat(m8): 完成 Interview、Memory 与共享学习路径` |
| `35439b3` | `feat(m8): 接入学习 API、Worker 与 Web 路由` |

### Testing

- Review、Interview、Memory 的真实 PostgreSQL 集成验证通过；M8/Review 迁移 race 验证通过。
- Web 69/69 个测试文件、746/746 个测试通过；OpenAPI、类型检查、构建与 staged whitespace 校验通过。
- 真实 API、Worker、Vite 在桌面与 `390x844` 视口的浏览器主链路 smoke 通过。
- `00059/00060` 业务数据 Down guard、Review Path reservation/hold 专门并发和 ABANDONED 重开仍是动态验证盲区。

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 34: 收口 M7 Timeline 与 Impact 遗留

**Date**: 2026-07-29
**Task**: 收口 M7 Timeline 与 Impact 遗留
**Branch**: `dev`

### Summary

交付 Timeline Web、v2 Artifact/Review Card impact、一等 owner event 与 approval-only downstream_update Proposal；完成 PostgreSQL/API/Worker/Vite 动态验收及精确索引快照门禁。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `c5126dc` | (see git log) |
| `b341fc5` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 35: 收口 M9 附件导出与 AC-33

**Date**: 2026-07-30
**Task**: 收口 M9 附件导出与 AC-33
**Branch**: `dev`

### Summary

完成 Workspace 附件 ZIP 导出、安全本地文件处理、OpenAPI 与数据库迁移、前端设置页收口及端到端验证，关闭 AC-33。

### Git Commits

| Hash | Message |
|------|---------|
| `a7bf1c1` | (see git log) |

### Status

[OK] **Completed**


## Session 36: 开发环境一键启动与模型配置

**Date**: 2026-07-31
**Task**: 开发环境一键启动与模型配置
**Branch**: `dev`

### Summary

完成源码下载后一键 Docker 启动、受管模型配置与显式 restart 应用，并通过暂存快照全量验证。

### Main Changes

- 新增 ./zhixu up/status/logs/down/reset/restart 与 Compose 密钥卷、一次性迁移和运行时恢复契约。
- 新增模型设置 API、加密 revision、API/Worker rollout fence、前端配置页及执行 provenance。

### Git Commits

| Hash | Message |
|------|---------|
| `c14173e` | (see git log) |
| `5faeca9` | (see git log) |

### Testing

- [OK] 暂存树通过 go test ./...、integration 编译、OpenAPI/Compose/launcher contracts、Web lint/typecheck、845 tests 和 build。

### Status

[OK] **Completed**


## Session 37: 优化工作台首屏与导航流程

**Date**: 2026-07-31
**Task**: 优化工作台首屏与导航流程
**Branch**: `dev`

### Summary

完成 Workspace 首屏、分组导航、状态摘要与移动端 Playwright 验证。

### Git Commits

| Hash | Message |
|------|---------|
| `6598cfd81955f141878e9c0548d83c986ce6891f` | (see git log) |

### Status

[OK] **Completed**


## Session 38: 宿主机 Workspace 精确授权

**Date**: 2026-08-01
**Task**: 宿主机 Workspace 精确授权
**Branch**: `dev`

### Summary

实现 Docker 宿主机绝对路径的精确单目录授权、可恢复切换、控制页面与全链路验证。

### Git Commits

| Hash | Message |
|------|---------|
| `a6b5988` | (see git log) |

### Status

[OK] **Completed**


## Session 39: 重设计工作台导航与首页

**Date**: 2026-08-01
**Task**: 重设计工作台导航与首页
**Branch**: `dev`

### Summary

将首页收敛为白蓝知识脉络入口，重组导航与五类设置，并以真实有界数据投影已连接工作台。

### Git Commits

| Hash | Message |
|------|---------|
| `c1a7ca3` | (see git log) |
| `145deee` | (see git log) |

### Status

[OK] **Completed**


## Session 40: 首页公开访问与控制授权拆分

**Date**: 2026-08-02
**Task**: 首页公开访问与控制授权拆分
**Branch**: `dev`

### Summary

拆分普通业务访问与 Host 控制授权，完成多浏览器、认证模式、重启恢复和桌面移动端验收，并更新 8 月 1 日优化清单。

### Git Commits

| Hash | Message |
|------|---------|
| `e812c32` | (see git log) |
| `1a7add8` | (see git log) |
| `430f0f9` | (see git log) |

### Status

[OK] **Completed**


## Session 41: 笔记工作流与工作台交付

**Date**: 2026-08-05
**Task**: 笔记工作流与工作台交付
**Branch**: `dev`

### Summary

交付快速记录、文章创作、材料整理、文档历史与 Git 远端同步，完成工作台导航和系统状态重设计，并通过完整 Web/Go/浏览器复验。

### Git Commits

| Hash | Message |
|------|---------|
| `903ffbf` | (see git log) |

### Status

[OK] **Completed**


## Session 42: 架构质量优化阶段 0-1

**Date**: 2026-08-05
**Task**: 架构质量优化阶段 0-1
**Branch**: `dev`

### Summary

完成可重复架构质量基线与 Health Issue 有界历史跨层改造；阶段 0、1 均已验证并归档，阶段 2-6 保持未启动。

### Main Changes

- 新增确定性的 tracked-file 架构质量基线脚本、10 个回归测试与 Make 入口。
- Health 详情固定 25 条历史，新增 observation/decision HMAC cursor 分页端点及精确 current observation 契约。
- OpenAPI、Web 严格 decoder、TanStack infinite query 和历史 Tabs 同步升级。

### Git Commits

(No commits - planning session)

### Testing

- [OK] Go 受影响包单测、vet、race 与 integration-tag 编译通过。
- [OK] 真实 PostgreSQL 261/260 条 fixture 连续两次通过固定 statement、稳定分页与 EXPLAIN 索引断言。
- [OK] Web 20 个定向用例、typecheck、lint、production build、OpenAPI、基线 10 个用例通过。

### Status

[OK] **Completed**

### Next Steps

- 阶段 2-6 按用户要求保持未启动；后续需单独确认后再继续。


## Session 43: 完成 Eino 分层迁移

**Date**: 2026-08-08
**Task**: 完成 Eino 分层迁移
**Branch**: `codex/eino-layered-migration`

### Summary

分层接入 Eino Chat Adapter、调用级脱敏 Callback 与 Structured Output 短 Graph，保留 PostgreSQL/River、领域校验和 direct 回滚边界；完成真实 Compose/River 闭环及 Ollama qwen3 Provider smoke，并以 typed allowlist 兼容 reasoning 扩展。

### Git Commits

| Hash | Message |
|------|---------|
| `e51f5acc` | (see git log) |
| `e376ad01` | (see git log) |
| `4bcc916f` | (see git log) |
| `62e15152` | (see git log) |
| `675181e4` | (see git log) |

### Status

[OK] **Completed**


## Session 44: 收口 Eino 依赖账本

**Date**: 2026-08-08
**Task**: 收口 Eino 依赖账本
**Branch**: `codex/eino-layered-migration`

### Summary

完成 Eino 迁移的 go.sum tidy 收口，删除 63 条未使用的 go.mod 校验记录；依赖版本、go.mod 与 vendor 保持不变，并通过 tidy、verify、vendor 编译、API/Worker 构建、race 与 vet 门禁。

### Git Commits

| Hash | Message |
|------|---------|
| `9d1a34e` | (see git log) |

### Status

[OK] **Completed**
