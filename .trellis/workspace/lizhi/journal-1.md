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


## Session 20: 完成 M6-02 Agent 结构化输出与引用闭环

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
