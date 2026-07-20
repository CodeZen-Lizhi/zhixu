# ZHIXU 产品级建设实施清单

> 本文件是复杂任务的执行顺序和验证门禁。规划已获批准并进入持续实施；状态以代码、子任务归档和实际验证为准，不以早期计划表中的默认值推断完成度。

## 1. Milestones

| 里程碑 | 目标 | 退出条件 |
|---|---|---|
| M0 | 需求、规范、契约与版本基线 | PRD/design/implement 审查通过；Spec 不再是占位模板；未决冲突有 ADR/研究任务 |
| M1 | 项目基础与技术骨架 | API/Worker/Web/DB/Compose/CI 可启动并有 readiness |
| M2 | Eino 技术验证 | 16 项 PoC 有报告，采用或回退路径明确 |
| M3 | 数据库与核心领域模型 | 迁移、约束、sqlc、聚合状态机和测试 Fixture 可重复 |
| M4 | 持久化 Workflow 与基础服务 | River 投递、Node Lease、Outbox、Human、重试、幂等、补偿可恢复 |
| M5 | 核心知识变更闭环 | Markdown 导入 → Proposal → Approval → Git → Reindex → 验证通过 |
| M6 | RAG、Agent、Tool Calling | 引用、拒答、冲突、结构化输出、权限和评测闭环 |
| M7 | Graph/Collection/Health/Timeline | 图谱可操作、候选可确认、问题可追踪、集合不复制事实 |
| M8 | Artifact/Review/Interview/Memory | 产物、复习、评分、FSRS、记忆生命周期可用 |
| M9 | 前端业务页面与表格能力 | 主要页面、Diff、图谱、表格、SSE、错误和空状态可用 |
| M10 | 安全、性能、可观测与部署 | 安全负测、容量基线、审计、备份恢复、Docker Smoke 通过 |
| M11 | 全量测试、文档和发布验收 | AC-01..AC-36、11 步演示、六条 seam 和交付包全部通过 |

## 2. Ordered Task Table

| 任务ID | 阶段 | 任务 | 影响文件或模块 | 前置任务 | 验收方式 | 风险 | 执行者 | 状态 |
|---|---|---|---|---|---|---|---|---|
| M0-01 | M0 | 收敛 PRD、分页、SSE、表格边界和 Eino 决策记录 | `docs/product/PRD.md`, `docs/architecture/*`, ADR | 无 | 文档链接检查；需求追踪无孤儿；冲突表归零 | 文档与实现分叉 | 主 Agent | 已完成 |
| M0-02 | M0 | 完成 Trellis 后端规范 | `.trellis/spec/backend/**` | M0-01 | 无 `TBD/To be filled`；含事实引用和质量门禁，真实代码示例待 M1 回填 | 规范过度理想化 | 子 Agent，主 Agent 审核 | 已完成（待 M1 代码链接回填） |
| M0-03 | M0 | 完成 Trellis 前端规范 | `.trellis/spec/frontend/**` | M0-01 | 无 `TBD/To be filled`；含事实引用和可访问性门禁，真实代码示例待 M1 回填 | 与未来代码不一致 | 子 Agent，主 Agent 审核 | 已完成（待 M1 代码链接回填） |
| M0-04 | M0 | 确定 Go/Node/数据库/依赖版本与 License | `go.mod`, `web/package.json`, `docs/architecture/technology-stack.md` | M0-01 | manifest/lockfile 可复现；License 明确 | 版本选择过早 | 主 Agent | 待开始 |
| M1-01 | M1 | 初始化 Go module、API/Worker composition root、统一 ID/Clock/Error/Config | `go.mod`, `cmd/**`, `internal/platform/**` | M0-02,M0-04 | `go test ./...`, `go vet ./...`, readiness 单测 | 骨架形成错误依赖 | 子 Agent | 已完成 |
| M1-02 | M1 | 初始化 React/Vite/TS/Query/Router/Vitest；Playwright 由 M11 E2E 正式接入 | `web/**` | M0-03,M0-04 | `npm ci`, lint/typecheck/test/build；首页非空 | 工具链版本漂移 | 子 Agent | React/Vite/TS/Query/Router/Vitest 已完成；Playwright 尚未安装 |
| M1-03 | M1 | 建立显式 Compose、PostgreSQL+pgvector、配置样例和 CI | `deploy/compose.yml`, `Dockerfile*`, `.env.example`, `.github/workflows/**` | M1-01 | `docker compose -f deploy/compose.yml config`; Docker smoke | 误读父目录 Compose | 子 Agent | 已完成 |
| M1-04 | M1 | 建立 OpenAPI-first、Problem Details、cursor、ETag、Idempotency、SSE envelope | `api/openapi/**`, `internal/presentation/**`, `web/src/api/**` | M1-01,M1-02 | contract test、生成客户端无漂移 | 概念契约字段遗漏 | 主 Agent + 子 Agent | 已完成当前已发布 API 基线：OpenAPI/Problem、稳定 cursor、ETag、幂等与持久 SSE envelope/replay；后续领域接口继续 additive 演进 |
| M2-01 | M2 | Eino 16 项最小 PoC（模型、Embedding、Retriever、Streaming、Schema、Tool、Callback、River） | 独立 `internal/platform/eino/**`, `research/eino-poc/**` | M1-01,M1-03 | `make test-eino-poc` 逐项报告；race/资源关闭 | 框架类型侵入领域 | 子 Agent，主 Agent 复核 | 已完成采用门禁，部分验证未通过 |
| M2-02 | M2 | 根据 PoC 锁定或拒绝 Eino，建立 Adapter/Fake/ADR | `docs/architecture/adr/0013*`, `internal/agentadapter/**` | M2-01 | Adapter Contract；替换路径测试 | 锁定不可替换 | 主 Agent | 已完成：主模块不正式采用 Eino |
| M3-01 | M3 | 设计并实现 Core/Change/Workflow/Retrieval/Learning/Ops 迁移 | `migrations/**`, `sqlc.yaml`, `queries/**` | M0-01,M1-01 | 空库/升级/重复迁移/回滚前向策略测试 | 漏实体或版本字段 | 子 Agent | 待开始 |
| M3-02 | M3 | 落实部分唯一索引、FK/多态引用策略、CHECK、乐观锁和幂等作用域 | `migrations/**`, `internal/platform/postgres/**` | M3-01 | 并发约束测试、重复消息测试 | DB 约束弱于领域规则 | 主 Agent + 子 Agent | 待开始 |
| M3-03 | M3 | 建立领域 ID、状态机、聚合命令、事件和错误契约 | `internal/shared/**`, `internal/{workspace,knowledge,changecontrol,workflow,review}/**` | M0-01,M1-01 | 纯函数/状态机/非法转移测试 | 第三方类型渗透 | 主 Agent | 待开始 |
| M3-04 | M3 | 建立固定 Workspace、Fake Model/Parser/Git/FS/Retrieval、AI Gold Set 和容量生成器 | `testdata/**`, `internal/testkit/**`, `eval/**` | M3-03 | fixture 校验、确定性复现、无 Secret | 测试数据与生产数据混淆 | 子 Agent | 待开始 |
| M4-01 | M4 | 实现 Workflow Definition/Run/Node、River job 映射和租约 | `internal/workflow/**`, `cmd/worker/**` | M3-01,M3-03 | Worker claim/heartbeat/complete 集成测试 | 双状态机 | 子 Agent | 已完成：M4-A/B/C/D 子任务与真实 River/PostgreSQL smoke 已归档 |
| M4-02 | M4 | 实现 Outbox、重试/退避、暂停/恢复/取消、Human Task | `internal/workflow/**`, `migrations/**` | M4-01 | crash、lease expire、duplicate event、double submit | 重复副作用 | 子 Agent | 已完成：重试/后继/Human/控制面、duplicate/lease reclaim 与故障恢复已验证 |
| M4-03 | M4 | 实现 Side Effect 执行判定、补偿和 `MANUAL_RECOVERY_REQUIRED` | `internal/workflow/**`, `internal/audit/**` | M4-02 | 未知结果不自动重试；恢复演练 | 状态误判 | 主 Agent + 子 Agent | 已完成 Workflow/Writeback 安全判定；通用 append-only Audit 仍归 M10-01 |
| M5-01 | M5 | 实现 Workspace 安全路径、扫描、Source/Version Hash 和 Git CLI Adapter | `internal/workspace/**`, `internal/platform/{filesystem,gitcli}/**` | M3-03 | path/symlink/dirty workspace/commit failure 测试 | 覆盖用户文件 | 子 Agent | 已完成 |
| M5-02 | M5 | 实现 Markdown/TXT 解析、Source Span、分块和隔离 | `internal/ingestion/**`, `internal/platform/parser/**` | M5-01,M3-04 | 重复导入、异常编码、代码/表格完整性、隔离测试 | 引用错位 | 主 Agent + 子 Agent | 已完成：Attempt/Projection/Span/Chunk、API/Workflow Node、Compose/API smoke 与 PostgreSQL/race 验证通过 |
| M5-03 | M5 | 实现 Proposal/Revision/Approval/Write Authorization/Change Hash | `internal/changecontrol/**`, `internal/tools/**` | M4-01,M5-01,M3-03 | 未批准写入拒绝、版本冲突、过期授权、双审批 | 绕过唯一写入 seam | 主 Agent | 已完成：Proposal/Approval/Hash/preflight、服务端短期授权、绑定校验、过期/撤销、幂等消费；实际 Safe Writeback 副作用留 M5-04 |
| M5-04 | M5 | 实现原子写回、Git Commit、DB mapping、增量索引请求和反向 Commit | `internal/changecontrol/**`, `internal/retrieval/**` | M5-03 | file/git/db/index/regression 故障注入；知识变更 E2E | Saga 半完成 | 主 Agent + 子 Agent | 已完成：真实 Safe Writeback、Git/DB mapping、Reindex/Regression/Activation/Completion 及 response-loss 恢复闭环由 M6 收口 |
| M5-05 | M5 | 实现 Topic/Claim/Relation/Evidence/Conflict 领域规则 | `internal/knowledge/**`, `migrations/**` | M3-03,M5-02 | 五分类关系、条件冲突、证据可达、对称去重 | 关系第二事实源 | 主 Agent + 子 Agent | 已完成：统一 Domain/Application/PostgreSQL、00017 约束、receipt-first/CAS/批量锁与真实 PG/Compose 门禁通过 |
| M6-01 | M6 | 实现 FTS/pgvector/Embedding/Index Version/RRF/Dedup/Rerank Adapter | `internal/retrieval/**`, `queries/**` | M5-02,M3-01,M2-02 | 检索 contract、过滤正确、降级显式、index switch | 向量维度/中文分词 | 子 Agent | 已完成：M6-A/B/C/D、Search/Evidence HTTP/OpenAPI、top-100 Cursor、真实 PostgreSQL/River/Compose 闭环及全量质量门禁通过；ANN/P95 归 M10 |
| M6-02 | M6 | 实现 Agent 结构化输出、引用校验、拒答、冲突和模型版本记录 | `internal/agent/**`, `internal/platform/models/**`, `internal/{knowledge,retrieval}/**`, `eval/**` | M2-02,M5-05,M6-01 | RAG eval、schema repair、citation/refusal test | 幻觉与假成功 | 主 Agent + 子 Agent | 已完成：严格四类 Schema、三阶段 Repair、完整 Citation tuple、Eligibility/Formal Claim、Conflict disclosure、Model Run/Call、真实 PostgreSQL/Workflow/Compose 与两轮审查通过；真实 Provider 质量阈值归 M11 |
| M6-03 | M6 | 实现 Tool Registry、Schema、Capability、SSRF、命令和输出安全 | `internal/tools/**`, `internal/platform/**` | M5-03,M2-02 | permission matrix、prompt injection、SSRF、command injection | 模型文本越权 | 主 Agent + 子 Agent | 已完成：版本化 Registry/Schema、持久 Workflow Policy/Tool Call、SSRF/命令/路径/脱敏、trusted write audit 与真实 PG/River/Compose fault smoke 全部通过 |
| M6-04 | M6 | 实现 RAG API、会话、SSE 流式展示和反馈评测 | `internal/app/**`, `web/src/features/rag/**` | M1-04,M6-02,M6-03 | RAG E2E、SSE reconnect、拒答样本 | 流式草稿被误当完成 | 主 Agent + 子 Agent | 已完成：T01-T17 已交付 Conversation/Question/Answer/Feedback、RAG v2、持久阶段/SSE、`/chat`、真实 PostgreSQL/River integration、disposable Compose smoke、文档同步、全量审查和归档 |
| M7-01 | M7 | 实现 Graph Projection、局部/全局/路径查询和证据延迟加载 | `internal/graph/**`, `web/src/features/graph/**` | M5-05,M6-01 | 分页、路径正确、超时降级、非全量渲染 | 图谱毛线球 | 子 Agent | 待开始 |
| M7-02 | M7 | 实现 Semantic Link Candidate、fingerprint、确认/忽略 Proposal | `internal/graph/**`, `internal/changecontrol/**` | M7-01,M5-03 | 忽略候选不重复；内容变化再评估 | 候选噪声 | 子 Agent | 待开始 |
| M7-03 | M7 | 实现 Smart Collection Query AST、列表/表格/卡片和健康扫描 | `internal/collection/**`, `internal/health/**` | M3-01,M5-05,M1-04 | cursor、列/过滤/分组、fingerprint、Issue 幂等 | 查询注入/第二事实源 | 子 Agent | 待开始 |
| M7-04 | M7 | 实现 Knowledge Event、Timeline、Impact Analysis | `internal/knowledge/**`, `internal/health/**`, `internal/audit/**` | M5-04,M7-03 | Commit/Proposal/Conflict/下游互查；只生成报告/Proposal | 级联误写 | 主 Agent + 子 Agent | 待开始 |
| M8-01 | M8 | 实现 Artifact 大纲、分章生成、来源覆盖、Markdown 导出和入库 Proposal | `internal/artifact/**`, `web/src/features/artifacts/**` | M6-02,M7-03 | 先大纲后正文、缺口显式、导出可追踪 | 产物污染正式知识 | 子 Agent | 待开始 |
| M8-02 | M8 | 实现 Review Deck/Card、Evidence 绑定、多维评分和 FSRS Adapter | `internal/review/**`, `internal/platform/scheduler/**` | M5-05,M6-02,M8-01 | 卡片失效、答题幂等、评分解释、schedule 同事务 | 复习错误知识 | 子 Agent | 待开始 |
| M8-03 | M8 | 实现 Interview Session、知识缺口、Learning Path 和 Memory 生命周期 | `internal/review/**`, `internal/memory/**` | M8-02,M7-04 | 面试报告、只记录明确确认 Memory、可编辑删除 | 隐式长期记忆 | 子 Agent | 待开始 |
| M9-01 | M9 | 实现 Dashboard/Inbox/Documents/Proposals/Workflows/Settings | `web/src/features/**`, `web/src/routes/**` | M1-02,M1-04,M5-02,M5-03 | route integration、空/错/恢复状态、a11y | 页面先于契约漂移 | 子 Agent | 待开始 |
| M9-02 | M9 | 实现 Diff、证据、审批、冲突和版本合并 UI | `web/src/features/optimization/**`, `web/src/features/proposals/**` | M5-03,M5-04 | 逐项 accept/reject/edit、ETag 冲突合并 | 误导性 Diff | 子 Agent | 待开始 |
| M9-03 | M9 | 实现 Collection 表格、导出任务、脱敏和结果追踪 | `web/src/features/collections/**`, `internal/export/**` | M7-03,M1-04 | 表格视图、分页/筛选/列、Markdown/JSON 导出、权限/过期 | 擅自引入 XLSX/CSV | 子 Agent | 待开始 |
| M9-04 | M9 | 实现统一 SSE Event Store、重连、查询失效和异步 UX | `web/src/events/**`, `internal/presentation/sse/**` | M1-04,M4-02 | Last-Event-ID、超窗重查、页面刷新恢复 | SSE 被当事实源 | 子 Agent | 待开始 |
| M10-01 | M10 | 实现 slog/OTel/Metrics/append-only Audit/Secret Redaction | `internal/audit/**`, `internal/observability/**` | M4-01,M5-03,M6-03 | correlation、重试不重复审计、敏感字段扫描 | 日志泄密 | 子 Agent | 待开始 |
| M10-02 | M10 | 完成认证、Session/Token、CSRF/Origin、Capability 和安全负测 | `internal/auth/**`, `internal/tools/**`, `web/**` | M1-04,M6-03 | auth/security suite 全通过 | 自托管越权 | 主 Agent + 子 Agent | 待开始 |
| M10-03 | M10 | 完成 50 万容量、EXPLAIN、图谱和前端性能基线 | `bench/**`, `testdata/capacity/**`, `web/**` | M6-01,M7-01,M9-03 | `make benchmark-capacity`; P95 或 ADR 记录 | 性能预算不达标 | 子 Agent | 待开始 |
| M10-04 | M10 | 完成 Docker、Readiness、Migration Job、备份/恢复/一致性演练 | `deploy/**`, `scripts/ops/**`, `docs/architecture/runbooks/**` | M1-03,M5-04,M10-01 | `docker compose -f deploy/compose.yml up -d --wait`; restore/consistency drill | 灾难不可恢复 | 子 Agent | 待开始 |
| M11-01 | M11 | 完成六条 seam、PRD 最终演示和 Playwright E2E | `e2e/**`, `testdata/**` | M5–M10 | 固定 Fixture 11 步场景通过 | E2E 只测假页面 | 主 Agent + 子 Agent | 待开始 |
| M11-02 | M11 | 完成 AI Eval、关系/文章/图谱/Review 回归门禁 | `eval/**`, `docs/architecture/testing-and-evaluation.md` | M2-02,M6-02,M8-02,M11-01 | 指标基线、高风险下降非零退出 | 质量只看通过率 | 子 Agent | 待开始 |
| M11-03 | M11 | 完成全量 review、文档同步、SBOM、运行/部署/回滚说明和交付包 | `README.md`, `docs/**`, `deploy/**`, `Makefile` | 全部前置 | `make verify`; 文档与行为逐项核对 | 漏交付物 | 主 Agent | 待开始 |

## 3. Parallelization Rules

- M0-02 与 M0-03 可并行，但不得修改同一文件。
- M1-01、M1-02 可在公共 API 最小契约冻结后并行；M1-03 依赖后端端口和数据库选择。
- M2、M3 的研究/测试可并行，生产领域模型和迁移必须由主 Agent 先冻结。
- M6-01 与 M6-02 只有在 Retrieval/Agent 契约冻结后才能并行。
- M7、M8、M9 可按文件边界并行；所有公共 OpenAPI、迁移、领域模型和生成客户端由主 Agent 统一整合。
- 任何高风险迁移、写回、认证和恢复任务不允许多个 Agent 同时修改同一文件。

## 4. Per-Task Definition of Done

每项任务关闭前必须：

1. 更新本任务状态和对应需求追踪。
2. 读取目标模块 `.trellis/spec` 与相关 docs。
3. 添加正常、边界、失败路径测试。
4. 执行单测、集成/契约测试、lint/typecheck/compile 和受影响构建。
5. 执行最小业务烟测，页面必须实际打开并操作。
6. 检查 diff，确认无第二事实源、静默 fallback、吞异常或未授权写入。
7. 运行适用的 `go-review`、`code-review-and-quality`、`sql-code-review` 或前端质量审查。
8. 同步 API、迁移、配置、产品/架构/运行文档。

## 5. Canonical Verification Commands

最终应提供并持续维护以下命令。当前 `make test`、`rag-integration`、`openapi-check`、Compose
Search/Tool/RAG smoke 等入口已存在；下列尚不存在的最终发布命令仍由 M10/M11 补齐，不能视为已通过：

```bash
make verify
go test -race ./...
go vet ./...
npm ci --prefix web
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
docker compose -f deploy/compose.yml config
docker compose -f deploy/compose.yml up -d --wait
make migrate-test
make contract-test
make integration-test
make e2e
make eval-regression
make security-test
make benchmark-capacity
make backup
make restore-drill
make consistency-drill
docker compose -f deploy/compose.yml down -v
```

## 6. Rollback Points

- M1：只删除未被用户数据使用的骨架/Compose。
- M3：迁移只前进；应用保持旧 Schema 兼容，禁止默认 down migration。
- M4：停 Worker、释放租约、重放 Outbox；未知副作用进入人工恢复。
- M5：使用 reverse Git Commit，重新构建投影，不重写 Git 历史。
- M6–M8：切回上一 Active Prompt/Model/Workflow/Index Version。
- M10–M11：保留上一镜像、上一迁移兼容版本、上一评测基线和备份 Marker。
