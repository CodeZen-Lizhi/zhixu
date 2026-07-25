# 后端开发规范

本目录是 Go API、Worker、领域模块、数据库、Adapter、Workflow 和可观测性实现的规范入口。M1 已建立可运行骨架；后续业务模块必须继续遵守这里的边界，并以真实代码和任务验收结果为准。

M7-01 已补充 Graph canonical read projection、公共 HTTP/真实进程 smoke、容量 benchmark 与跨层质量门禁；
首版只读 Topic/Claim，500,000 Relation/FPS 与正式认证仍归 M10。
M7-02 已补充 Semantic Link Candidate、typed Relation Proposal、Approval 后 Knowledge apply、durable Topic scan、
严格故障隔离与执行计划门禁；Candidate 仍不是正式 Relation。
M7-03 已补充版本化 Smart Collection、统一 read model/cursor、持久 Health Scan/Issue/Schedule、SMART_COLLECTION
Candidate scope、affected-change outbox、真实 PostgreSQL/River/API/浏览器门禁；Tag/Review/Directory owner、Artifact/
Review 批量动作、认证和 M10 最终容量仍未实现。
M9 已补充 Workspace Source Version/Proposal/Workflow 列表、资源绑定 cursor 与 Active Index 选择投影；
Proposal 等级由不可变 `proposal.risk_level` 唯一拥有，Source Version 使用受复合约束的 Workspace 镜像键支撑
有界 keyset 查询；Proposal detail 的 Approval 为必需 nullable，Summary 保持 optional non-null。三条真实
PostgreSQL 列表、迁移契约与严格执行计划已在最终迁移工作树复验；M10 继续负责最终容量门禁。
M10-02 已交付单用户 Auth、Cookie Session、受限 API Token、CSRF/Origin、Capability Middleware 和 Compose
启动预检；认证与 Write Authorization 保持两道独立边界，具体可执行契约见 `auth-security.md`。

## 规范索引

| 规范 | 内容 | 当前状态 |
|---|---|---|
| [目录与模块结构](./directory-structure.md) | 进程入口、领域模块、Adapter 和依赖方向 | M1 入口与依赖边界已验证；领域模块待后续任务补充 |
| [认证与安全契约](./auth-security.md) | Session、API Token、CSRF、Capability、配置与 Compose 门禁 | M10-02 已锁定 API/DB/env 契约、失败矩阵与真实 PostgreSQL/Compose 验证 |
| [数据库开发规范](./database-guidelines.md) | pgx/Goose/River、参数化查询、事务、迁移和约束 | 已记录 M7-03 Collection/Health 持久化、read-model revision、schedule/outbox，以及 M9 Workspace 列表、cursor kind/version、Active Index included/excluded 与 PG/EXPLAIN 门禁 |
| [错误处理规范](./error-handling.md) | 领域错误、Retry 分类、Problem Details、SSE 错误 | 已记录 Tool 稳定错误、M9 Proposal detail/summary Approval 空值契约与 M10 Auth 的稳定 Problem Details 边界 |
| [日志与审计规范](./logging-guidelines.md) | slog JSON、OTel correlation、脱敏和 Audit | M6-03 Tool/Observability 共享脱敏与受限 Tool Call 事实已记录；通用 append-only Audit/真实 exporter 归 M10 |
| [质量与交付规范](./quality-guidelines.md) | 禁止模式、测试金字塔、安全、Review 和门禁 | M7-01 Graph、M7-02 Semantic Link 与 M7-03 Collection/Health 的跨层、fault、性能、浏览器和独立审查门禁均已记录 |

## 开发前检查清单

开始修改后端代码前，必须：

1. 读取本目录与任务的 `prd.md`、`design.md`、`implement.md`，再读取对应 `docs/architecture/` 文档；不得凭经验猜接口、数据库或命令。
2. 确认任务处于 `in_progress`，明确影响模块、公共契约、数据流、兼容性、测试范围和回滚路径。
3. 先搜索现有领域术语、错误码、配置字段、查询和工具；共享规则只能有一个事实源。
4. 对跨层变更阅读 `.trellis/spec/guides/cross-layer-thinking-guide.md`；发现重复实现时阅读 `code-reuse-thinking-guide.md`。
5. 只有 Composition Root 读取配置并构造 Adapter；领域模块不自行创建数据库、模型或 Git 客户端。

## 实现边界

- API 与 Worker 可独立运行，但共享领域 Interface 和 Composition Root。
- API 与 Worker 的 Embedding Adapter 必须由同一 Configured Embedder Factory 构造，禁止两套配置转换。
- 领域层不依赖 HTTP、pgx/sqlc、River、模型 SDK、文件系统实现或具体 Git 命令。
- 正式知识唯一写入路径是 Proposal → Evidence Validation → Approval → Version Check → Atomic Write → Git Commit → Reindex → Regression Validation。
- 长任务进入持久化 Workflow；River 只负责投递/领取可运行节点，不取代 Workflow 领域状态。
- 跨文件/Git/DB 的一致性通过有序 Saga、Outbox、幂等和补偿处理；失败必须可解释、可审计、可恢复。

## 质量检查

规范修改阶段执行：

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

代码落地后，至少执行 `go test ./...`、`go vet ./...`，并根据影响范围运行数据库/文件/Git/Workflow 集成、API Contract、安全、AI Eval、E2E、Docker Smoke 和恢复演练。未创建 `go.mod`、CI 或 Makefile 前，不把具体版本或工具参数写成已确认事实。

## 事实来源

- 产品不变量、状态和验收：[`docs/product/PRD.md`](../../../docs/product/PRD.md)。
- 领域术语：[`docs/architecture/CONTEXT.md`](../../../docs/architecture/CONTEXT.md)。
- 模块边界：[`docs/architecture/module-architecture.md`](../../../docs/architecture/module-architecture.md)。
- API、分页、SSE 和错误：[`docs/architecture/api-and-events.md`](../../../docs/architecture/api-and-events.md)。
- 数据库、事务、索引和备份：[`docs/architecture/database-design.md`](../../../docs/architecture/database-design.md) 与 [`data-architecture.md`](../../../docs/architecture/data-architecture.md)。
- Workflow、租约、重试和补偿：[`docs/architecture/workflow-engine.md`](../../../docs/architecture/workflow-engine.md)。
- Adapter 与错误分类：[`docs/architecture/interfaces-and-adapters.md`](../../../docs/architecture/interfaces-and-adapters.md)。
- 安全、日志、Trace、Metrics 和审计：[`security.md`](../../../docs/architecture/security.md)、[`observability.md`](../../../docs/architecture/observability.md)、[`tool-security.md`](../../../docs/architecture/tool-security.md)。
- 测试与评测：[`docs/architecture/testing-and-evaluation.md`](../../../docs/architecture/testing-and-evaluation.md)。

## M1 真实实现入口

- API 入口与健康契约：[`cmd/api`](../../../cmd/api)、[`internal/app`](../../../internal/app)、[`api/openapi/openapi.json`](../../../api/openapi/openapi.json)。
- Worker 与数据库边界：[`cmd/worker`](../../../cmd/worker)、[`internal/platform/postgres`](../../../internal/platform/postgres)、[`migrations`](../../../migrations)。
- 配置、日志与部署：[`internal/platform/config`](../../../internal/platform/config)、[`internal/platform/observability`](../../../internal/platform/observability)、[`deploy`](../../../deploy)、[`Makefile`](../../../Makefile)。
- M1 Canonical Gate：`make test`、`make openapi-check`、`make compose-check`、`make compose-up`。
- M2 Eino 隔离门禁：[`poc/eino`](../../../poc/eino)，当前门禁结论为不正式采用，主模块继续使用直接 Adapter 路线。
- M6-02 Agent 门禁：完整 Citation tuple、Knowledge Eligibility/FormalClaimReader、严格 Schema Repair、Model Run/Call
  与 REVIEW 持久化必须按 [`database-guidelines.md`](./database-guidelines.md) 的专项契约验证；Active Index 不等于
  Approved Evidence，Agent 不得直接查询 Knowledge SQL。

## 当前明确待验证项

- License 和 50 万 Chunk ANN/P95 必须在对应后续任务中通过仓库文件和测试锁定；M2 已明确
  不采用 Eino 主模块依赖，M6-D 的 exact vector scan/EXPLAIN 只作为正确性基线。
- 本规范不提供伪造的实现代码、版本号、数据库字段长度或不存在的测试结果；M1 完成后应将真实文件链接补入各专题规范。
