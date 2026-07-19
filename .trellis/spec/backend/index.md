# 后端开发规范

本目录是 Go API、Worker、领域模块、数据库、Adapter、Workflow 和可观测性实现的规范入口。M1 已建立可运行骨架；后续业务模块必须继续遵守这里的边界，并以真实代码和任务验收结果为准。

## 规范索引

| 规范 | 内容 | 当前状态 |
|---|---|---|
| [目录与模块结构](./directory-structure.md) | 进程入口、领域模块、Adapter 和依赖方向 | M1 入口与依赖边界已验证；领域模块待后续任务补充 |
| [数据库开发规范](./database-guidelines.md) | pgx/Goose/River、参数化查询、事务、迁移和约束 | 已记录 M6-04 Conversation/Answer/Feedback、PLAN/RAG v2、SSE 投影、CAS 与 guarded Down；Repository/HTTP 闭环按任务继续验证 |
| [错误处理规范](./error-handling.md) | 领域错误、Retry 分类、Problem Details、SSE 错误 | 已记录 Tool Context/Permission/receipt/UNKNOWN/Web Policy 稳定错误；正式 Auth/CSRF 仍归 M10 |
| [日志与审计规范](./logging-guidelines.md) | slog JSON、OTel correlation、脱敏和 Audit | M6-03 Tool/Observability 共享脱敏与受限 Tool Call 事实已记录；通用 append-only Audit/真实 exporter 归 M10 |
| [质量与交付规范](./quality-guidelines.md) | 禁止模式、测试金字塔、安全、Review 和门禁 | 已记录 M6-03 真实 River Tool、SSRF/命令/路径、Safe Writeback audit 与 Docker smoke 门禁；结果以任务最终记录为准 |

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

- License、认证实现和 50 万 Chunk ANN/P95 必须在对应后续任务中通过仓库文件和测试锁定；M2 已明确
  不采用 Eino 主模块依赖，M6-D 的 exact vector scan/EXPLAIN 只作为正确性基线。
- 本规范不提供伪造的实现代码、版本号、数据库字段长度或不存在的测试结果；M1 完成后应将真实文件链接补入各专题规范。
