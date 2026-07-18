# 后端质量与交付规范

## 适用范围

适用于后端所有领域模块、API、Worker、数据库/文件/Git/模型 Adapter、迁移、Workflow 和安全边界。
仓库已进入 M6-D；本文件记录可执行质量门禁，但只有实际命令输出才能证明某项测试或 smoke 已通过。

## 已确认事实

- 模块必须是深模块：小 Interface、明确输入/输出/不变量/错误/性能约束，复杂度隐藏在实现中（依据 [`module-architecture.md`](../../../docs/architecture/module-architecture.md) 第 2 节）。
- 核心领域逻辑不能绑定框架；外部工具通过 Adapter，Composition Root 负责配置、构造和注入（依据 [`technology-stack.md`](../../../docs/architecture/technology-stack.md) 与 [`interfaces-and-adapters.md`](../../../docs/architecture/interfaces-and-adapters.md)）。
- 必须覆盖主路径和异常路径、审计、可观测、自动测试、AI Eval、文档和无未说明降级（依据 [`testing-and-evaluation.md`](../../../docs/architecture/testing-and-evaluation.md) 第 21 节）。
- 测试层级包括 Unit、Adapter Contract、PostgreSQL/Filesystem/Git/Workflow Integration、AI Evaluation、Playwright E2E、Docker Smoke 与恢复演练；Go 单元使用 `testing`/`httptest`，数据库集成采用 Testcontainers-Go。
- 安全门禁覆盖路径穿越、Symlink、SSRF、Prompt Injection、XSS、CSRF、SQL Injection、未授权 Tool 和 Secret Redaction。

## 目标代码落点（M1 起）

- 领域规则和状态机：各 `internal/<module>/`，通过纯函数/小 Interface 测试。
- Application/API：`internal/app/`、`cmd/api/`，通过 Handler/Contract/错误映射测试。
- Worker/Workflow：`internal/workflow/`、`cmd/worker/`，通过租约、崩溃恢复、幂等和补偿集成测试。
- Adapter：`internal/platform/*`，每个正式实现与 Fake/Fixture 共享 Contract Test。
- 数据库和迁移：`migrations/`、`internal/platform/postgres/`，通过空库升级、约束故障和 EXPLAIN 测试。
- 测试 Fixture、Gold Set、AI Eval、E2E 和部署验证路径由 M1 manifest/构建文件确定；不提前伪造目录或命令文件。

## 禁止模式

- 伪接口、硬编码假数据、静默 fallback、吞异常、假成功响应或把演示流程当作业务闭环。
- 在 Controller、页面边界、SQL 脚本中散落业务规则；共享校验、权限和状态机必须收敛到唯一领域事实源。
- 领域包依赖 HTTP、pgx/sqlc、River、Eino/模型 SDK、文件系统或具体 Git 命令。
- 无测试地修改公共 Interface、DTO、Schema、迁移、权限、错误码或幂等语义；禁止通过重命名绕过 Breaking Change 检查。
- 逐条查库/远程调用、无分页大集合、无界图谱、无版本 Embedding、非参数化 SQL、shell 拼接用户输入。
- 生产日志泄露 Secret、Prompt/Source 全文、用户回答或思维链；安全失败转成允许继续。
- 破坏性迁移、修改已应用迁移、`reset --hard`、任意 Git remote push、绕过 Proposal/Approval 的文件写入。

## 必须遵循的模式

1. 新行为先定义领域不变量和 Interface，再实现 Adapter/Handler；对外契约用 OpenAPI 和稳定错误码。
2. 所有有副作用命令使用 Idempotency-Key/作用域幂等键、Version/ETag/Change Hash，并记录 Audit 与 Trace 关联。
3. 长任务持久化为 Workflow；Node 有租约、心跳、重试分类、checkpoint、Outbox 和补偿；Human Task 等待时不占 Worker。
4. 通过参数化 SQL、Workspace Root/Symlink 检查、SSRF 逐跳校验、Tool Registry 权限和敏感信息脱敏保护边界。
5. Adapter Contract 必须验证 Success、Timeout、Retryable/NonRetryable、Idempotency、Version Conflict 和 Resource Cleanup。
6. 代码变更后按影响面运行 Unit → Contract → Integration → E2E/Smoke；模型、Prompt、Retrieval、Workflow 或 Schema 版本变更需要 AI Eval 和回归门禁。
7. Search API 的 Handler 只拥有严格 wire 解码、href 和 top-100 page slicing；Canonicalization、Active-only
   Search、RRF、Evidence binding 与降级属于 Domain/Application/Adapter，禁止复制到 HTTP。
8. API/Worker 的 Embedding 构造必须复用 Configured Embedder Factory；前端原始 Search JSON 只能通过
   `web/src/api/search.ts` 严格 Decoder 进入 Feature。

## 测试要求

每个新功能至少包含：

- 正常路径、边界条件和失败路径单元测试。
- 受影响 Adapter 的契约测试；涉及 DB/File/Git/Workflow 时补集成测试。
- 公共 API 的分页、幂等、版本冲突、错误映射和 SSE 重连测试（若适用）。
- 涉及 AI 的结构化输出、Schema Repair、Citation/Refusal、冲突披露和高风险指标回归。
- 涉及安全的负测；涉及文件/Git 的原子写、并发修改、Commit 失败、反向 Commit 和 Symlink Escape。
- 核心闭环的最小业务烟测与可追踪审计。

M6-D 已通过真实 PostgreSQL HTTP、River fault 与 Compose API smoke 一轮；以下仍是后续发布必须重复的
独立交付门禁，且不代表前端、全仓静态检查或独立审查已经完成：

- 真实 PostgreSQL HTTP integration：生产 Router/Repository、三模式/过滤、Cursor、Workspace isolation、
  Source Version/Span href、Artifact 完整性和生产 SQL `EXPLAIN (FORMAT JSON)`。
- 真实 PostgreSQL/River fault smoke：Completion 后通过 Router Search/Evidence，并在 response-loss/
  duplicate delivery 下断言唯一 Activation、Active 与 Completion。
- Compose API smoke：唯一 project + disposable Git Workspace，经公开 API 完成 Approval→Reindex→
  Hybrid-to-Keyword degraded Search→Evidence GET，成功/失败都清理 volume/临时目录。
- Web strict decoder 单测与 lint/typecheck/test/build；OpenAPI 三路径及关键 Schema drift check。
- go-review、sql-code-review 和独立只读审查。上述 gate 不能用单元 Fake、readiness 或文档描述替代。

架构文档给出的 CI 事实是：PR 运行 Unit、Lint/Static、Migration、Contract 和选定 Integration；主分支/发布运行 Full Integration、E2E、Security、Evaluation、Docker Smoke。具体 CI 配置在 M1 创建后以仓库文件为准。

## Code Review 清单

- 是否符合模块依赖方向，未把领域逻辑放进 Handler、Adapter 或 SQL？
- 是否更新 OpenAPI、DTO、数据库迁移、错误码、事件和对应契约测试？
- 是否有版本检查、幂等、超时、重试分类、补偿和未知副作用处理？
- 是否参数化 SQL、限制分页/大小/超时，避免 N+1、循环远程调用和无界结果？
- 是否有完整日志/Trace/Audit，且 Secret、Prompt、Source、用户回答脱敏？
- 是否覆盖主路径、边界、失败、安全负测、集成和最小烟测？
- 是否运行并记录受影响包测试、静态检查、构建和迁移验证？
- 是否同步更新真实文档，并明确待确认项，而不是用未经证实的版本、路径或 API？
- Cursor 是否绑定 canonical request、Active Index、完整 top-100 结果与 offset，并明确进程重启失效？
- Vector wire 是否为 `distance`；Evidence 是否只从不可变 Artifact 读取最多 4 KiB excerpt？
- 是否误把 Workspace 隔离/loopback 声称为 M10 Auth、CSRF 或 Capability 已完成？
- 是否误把 exact vector scan/小夹具 EXPLAIN 声称为 500,000 Chunk ANN/P95 已完成？

## 验证方式

### M0 当前（仅规范）

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

### M1 代码落地后

最小门禁为：

```bash
go test ./...
go vet ./...
```

并按影响范围补充迁移/集成、API Contract、E2E、Security、AI Eval、Docker Smoke 和恢复演练。`go.mod`、CI 和 Makefile 未创建前，不把具体 lint 工具、版本或命令参数当成既定事实。

## 当前后续门禁

- M6-D 前端、全仓静态/构建/测试、Review 与归档的最终运行结果；真实 PG HTTP/River/Compose smoke
  已完成一轮，后续发布仍需重跑。
- 统一覆盖率阈值、完整 E2E Fixture 和性能容量基准。
- License、发布门禁、SBOM 和镜像扫描配置；README 当前仍未确定许可证。
