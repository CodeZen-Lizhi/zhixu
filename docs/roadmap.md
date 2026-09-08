# 知序开发路线图

## 1. 边界

本路线图只维护未交付方向、优先级、依赖和不可破坏的迁移边界。实际拆分、负责人、状态、验收证据与发布时间在 `.trellis/tasks/` 管理；人天是熟悉项目的单人初估，不含需求澄清、外部协调和发布观察。

当前架构事实以 [架构文档](architecture/README.md) 为准。Gin、Eino、前端 OpenAPI 生成客户端、TODO 9 的 Testcontainers 测试工厂与 Atlas 唯一迁移事实源均已交付；TODO 10 的 GORM 实现、入口接线与本地定向验收已完成，代码已提交并推送到 `origin/dev`。部署与历史升级限制见 [GORM 发布与回滚](architecture/runbooks/gorm-persistence-rollout.md)。`gin-contrib/sessions` 仍待评估。

## 当前交付收口（未完成）

当前产品主体已有大量实现与局部验收资产，但发布收口仍缺 Proposal Revision 编辑和真正三方合并、跨领域 Audit 完整覆盖、目标环境容量/FPS 证据、可执行备份恢复/一致性演练，以及统一六 seam、AI Eval、SBOM 和交付包门禁。M10-02 已完成；M9-03 已满足当前导出范围，Evaluation/Audit JSON 不再作为当前缺口。

最终完成以 [需求文档](requirements.md) AC-01..AC-41、生产装配、直接自动化证据和 14 步最终演示为准。详细状态见 [产品交付父任务](../.trellis/tasks/07-16-product-delivery/)；代码对照依据见 [M9 漂移审计](../.trellis/tasks/archive/2026-08/08-10-docs-consolidation/research/product-task-drift-audit.md)、[M10 审计](../.trellis/tasks/archive/2026-08/08-10-docs-consolidation/research/m10-code-status-audit.md) 与 [M11 审计](../.trellis/tasks/archive/2026-08/08-10-docs-consolidation/research/m11-code-audit.md)。

## 工程质量收口（未完成）

已完成可重复静态质量基线和 Health 历史数据有界化；步骤 0 的 CI/迁移运行时观察仍待补。路由错误恢复、Fast/Selected Integration/Full 分层 CI、完整 E2E 接线、Go HTTP 边界 primitives、热点与 Domain 边界拆分、性能预算、全量路由契约和 SBOM 治理尚未完成或未获批准；共享 TypeScript codec、手写 OpenAPI checker 等局部基础不代表对应工作包完成。

精确批准范围和验收见 [架构质量父任务](../.trellis/tasks/08-05-architecture-quality-optimization/)；当前代码证据见 [架构质量状态审计](../.trellis/tasks/archive/2026-08/08-10-docs-consolidation/research/architecture-quality-code-status-audit.md)。

## 2. 依赖顺序

```mermaid
flowchart LR
    Eino["Eino 通用 AI 能力（已交付）"] --> WorkspaceAgent["受限 Workspace Agent"]
    Spectral["Spectral + oasdiff（已交付）"] --> GeneratedClient["OpenAPI Generator + Zod（已交付）"]
    Testcontainers["Testcontainers-Go（已交付）"] --> Atlas["Atlas 唯一 Schema 迁移（已交付）"]
    Atlas --> GORM["GORM 数据访问迁移（开发完成）"]
    Testcontainers --> GORM
    Gin["Gin HTTP 基线（已交付）"] --> Sessions["gin-contrib/sessions 评估（deferred）"]
    GORM --> Sessions
```

数据库方向的 Testcontainers 与 Atlas 前置均已交付，GORM 全部 30 个子任务已完成本地开发与定向验收。OpenAPI 门禁与生成客户端已按依赖顺序交付。受限 Workspace Agent 依赖已交付的 Eino AI Runtime 基线，并继续拥有自身产品与安全门禁。Gin 已交付；Session 框架评估仍待单独推进。

## 3. 产品方向

### 3.1 TODO 2：打通受限 Workspace Agent 的真实用户链路

- **优先级/初估**：P1，20–30 人天。
- **目标**：让用户在 Workspace 内提出分析目标，由系统执行多步、可审计的只读工具循环，并把需要写入的结果收敛为 Proposal。
- **当前实现状态**：`/chat` 已具备独立的 `workspace_analysis` 模式、六阶段持久流程、受限时间线和恢复合同；默认 `rag` 路径、历史哈希和既有 RAG Definition 保持不变。真实旧/新 API/Worker 四组合、含新事实后的 current-API/legacy-Worker 回滚、固定 RAG 浏览器和 Worker 恢复演练已通过。该能力仍默认关闭，只有 API、Worker、Definition 与精确 Tool Registry 同时 ready 时才能接受新请求；在完成目标环境灰度和 OTLP 观察前，本条目仍不标记为路线图已交付。
- **首期范围**：只开放 `ReadGitStatus`、受证据约束的 RAG、`ReadSource` 和 `ValidateCitation`；其他自由查询或正文工具先完成 Receipt、输入上限、敏感信息处理和循环契约。
- **边界**：工具仅服务端授权；步骤数、单工具超时、总时限、并发、Token 和费用预算有硬限制；最终事实必须经过 Evidence/Citation 校验。模型不得直接写文件、提交 Git 或扩大 Capability。
- **验收门禁**：一次对话至少完成两次有依赖的只读调用；时间线区分 Token、工具请求/结果、等待和最终回答，刷新或 Worker 重投递能从持久事实恢复；各种上限以稳定错误终止。
- **关闭与回退**：先停止新提交，再让存量 Run 在安全检查点完成或稳定终止；保留 additive schema、receipt、candidate 与历史 Answer，确认没有可运行的新 Definition 节点后只回退 Worker，current API 继续承担兼容读取。不得把工作区分析请求改投固定 RAG。执行步骤见 [发布 Runbook](architecture/runbooks/workspace-analysis-rollout.md)。
- **依赖**：Eino AI Runtime 基线已交付；固定 RAG 与受限 Workspace Agent 模式仍需拥有独立产品入口、能力门禁和 Git 发布恢复路径，Eino 类型不得进入外部 API、领域对象或前端业务类型。

### 3.2 TODO 4：自动合成可持续演进的知识笔记

- **优先级/初估**：P0，25–35 人天。
- **目标**：围绕知识点持续合并多篇资料，去重、补全、并列保留冲突、标注缺口，并让每个结论回到原始来源。
- **边界**：只更新新增知识、证据、冲突或缺口，不重复改写已充分覆盖内容；更新仍通过 Proposal/Approval。合成笔记可作为复习与 AI 面试的可信上下文。
- **产品验收**：详见 [需求文档的附加需求](requirements.md#附加需求自动合成可持续演进的知识笔记)。

## 4. 平台与契约迁移

### 4.1 已交付 TODO 3：全量从 Goose 迁移到 Atlas（2026-08-30）

- **结果**：91 个历史 Goose 文件已转换为 Atlas 前向迁移，并追加 00092 清理旧 history；`cmd/migrate`、Docker/CI、testdb fixture、Makefile 与运维入口统一使用 in-process Atlas，Goose 依赖和兼容层已删除。见 [ADR-0029](architecture/adr/0029-atlas-sole-schema-migration.md)。
- **兼容与恢复**：Goose 全量/部分 history、shell-runner 与 Eino 版本冲突均以 fail-closed 接管；单一 advisory lock、River Up/Validate 和 Compose 启动门禁保持不变。Atlas 为 forward-only，恢复使用每文件事务、断点续跑、fix-forward 与升级前备份。
- **验证**：92 文件 lint/hash/validate、PG16/PG18 空库迁移与声明基线 drift、接管矩阵、重复 Up、`txmode none` 失败续跑、testdb fixture、Compose/launcher 合约均通过。
- **后续边界**：GORM `AutoMigrate`/`Migrator` 在生产、测试和命令入口均禁止，Persistence Model 只能映射 Atlas 已批准结构。

### 4.2 已交付 5：后端 HTTP 已从 Chi 迁移到 Gin（2026-08-11）

- **结果**：生产 Router 已收敛为 Gin v1.12.0；严格解码、Problem Details、SSE、Session/API Token、CSRF/Origin、Capability、Workspace 隔离、限流和审计语义仍以 OpenAPI、实现与 Trellis 验收证据为准。
- **保留边界**：Domain/Application 不暴露 Gin Context；标准库 `http.Handler` 仅在 HTTP 适配边界互操作，不保留双 Router 或双 Middleware。
- **后续关系**：这只提供 Session 框架评估的前置 HTTP 基线，不代表 TODO 11 已采用或通过验收。

### 4.3 已交付 6：使用 Eino 收敛 AI 通用基础设施（2026-08-12）

- **优先级/初估**：P0，20–30 人天（已完成，仅作规模参考）。
- **结果**：Chat、OpenAI-Compatible/Ollama Embedding、五类 Structured Scheduler、RAG Graph、只读 Tool Agent 和最终正文 Stream 已固定使用 Eino/eino-ext；旧 direct 实现、selector 和运行时 fallback 已删除，见 [ADR-0027](architecture/adr/0027-eino-primary-ai-runtime.md)。
- **保留边界**：项目继续唯一拥有持久 Workflow、权限、Approval、Evidence/Citation、Model Run/Call、Receipt、Fence 与 PostgreSQL/River 状态机；Eino 类型不进入领域对象、外部 API 或持久化合同。
- **验收状态**：真实 Provider 六项 live gate 与 host-relay 桌面/移动浏览器闭环已通过，Eino 迁移任务已归档为 `completed`；容器直连外部 HTTPS 网络路径及连续 7 天/100 个终态的稳定观察仍是独立发布质量证据，不阻塞本任务归档，当前不得标记为已通过。

### 4.4 已交付 7：Spectral 和 oasdiff OpenAPI 契约门禁

- **结果**：`api/openapi` 已成为独立、锁定的工具目录：Spectral CLI `6.16.3` 与解析到
  `1.22.7` 的 rulesets 检查 OpenAPI 3.1；项目 checker 和 Gin runtime inventory 继续锁定
  Auth/Capability/Workspace/SSE/Router 不变量。oasdiff `v1.29.1` 以固定 Docker digest 对可信
  Git base 检查 WARN/ERR 级 breaking change，并固定 180 天弃用宽限期和无外部引用策略。
- **入口**：`make openapi-install`、`make openapi-check` 与带显式 40 位 SHA 的
  `make openapi-breaking-check`；PR 使用 base SHA、受保护分支 push 使用 before SHA。普通 lint
  不依赖历史 base，兼容性 gate 不允许可覆盖 snapshot、ignore 或自动接受。
- **保留边界**：历史 base 的极窄 `items: false` bootstrap 适配仅用于一个已验证旧 Schema，候选永不
  改写；批准 breaking change 仍需 API owner、弃用/迁移说明和受保护分支显式绕过。GitHub branch
  protection、required checks、CODEOWNERS 与 bypass audit 属于仓库外管理员核验。细节见
  [ADR-0028](architecture/adr/0028-openapi-contract-gates.md)。

### 4.5 已交付 8：生成前端 OpenAPI 客户端并接入 Zod（2026-08-19）

- **结果**：189 个 operation 已按 26 个稳定领域 tag 生成 `typescript-fetch` API；现有 26 个生产 API 模块的普通 JSON、multipart 与下载请求均使用对应 `*ApiRaw`，不再保留手写普通请求路径。生成目录与领域 UI Model 隔离，模块继续拥有 wire-to-domain 和严格不变量。
- **工具链**：本地 wrapper `2.40.1`、OpenAPI Generator `7.24.0` 与 Zod `4.4.3` 由 manifest/lock/config 固定；`make openapi-generate-check` 从权威契约重建、检查 drift、公共模型和严格编译，CI 同步执行。normalizer 只投影已登记的 generator 兼容差异，生成目录禁止手改。
- **保留边界**：共享 Transport 唯一处理 API base URL、Cookie/API Token、CSRF、401、Abort 和 header 合并；generated 参数使用当前页面 Origin 满足契约签名，实际线路 Origin 由浏览器控制。Zod 严格校验共享 Problem、Session 与 API Token，其他高风险领域沿用既有 strict owner。原生 SSE 与 Answer Draft stream 保留专用协议 owner；multipart/Blob 只保留进度、媒体、文件名和完整性薄 Adapter。
- **验证**：OpenAPI 契约/生成漂移、前端 lint/typecheck、1,233 项 Vitest、production build 与 `git diff --check` 通过；真实 Chromium 本地确定性 smoke 验证 Cookie/CSRF、生成 Search 请求、Workspace A→B URL/cache 收敛、迟到请求取消和严格联合类型页面，控制台无错误或警告。26 模块逐项证据见 [迁移矩阵](../.trellis/tasks/08-18-openapi-client-zod-integration/research/operation-migration-matrix.md)。

### 4.6 已完成 12：优先使用浏览器原生 EventSource（2026-08-10）

- **优先级/初估**：P2，3–5 人天。
- **目标**：让浏览器标准 API 接管可覆盖的 SSE 帧解析、连接状态和重连。
- **结果**：浏览器业务事件流已改用原生 `EventSource`；删除 `ReadableStream.getReader()`、`TextDecoder` 和手写 frame parser。
  服务端默认 legacy named-event wire 保持兼容，原生客户端用 `event_format=message` 与 `last_event_id` seed；Header 优先级、
  no-store、heartbeat 和 Workspace 重放语义由 Handler/OpenAPI 锁定。
- **保留边界**：项目继续拥有 strict Envelope、committed cursor、有界串行队列、generation 隔离与 fatal `CLOSED` 的最小
  Fetch probe。已建立连接的普通网络中断交给浏览器原生重连；probe 只分类 401/400/409/5xx/Content-Type，200 body
  立即取消，不解析 SSE。
- **验证**：Go Handler 单测与 integration 编译、OpenAPI check、前端 69 个 Event/Event Store 定向用例、lint、typecheck、build，
  以及 Chromium smoke（分块 CRLF、heartbeat、多行 data、EOF `Last-Event-ID`、页面刷新后恢复、Cookie、非法事件、
  fatal probe、A/B 切换）通过。

## 5. 数据访问与测试迁移

### 5.1 已交付 TODO 9：使用 Testcontainers-Go 管理数据库集成测试（2026-08-26）

- **结果**：提交 `109d2cb4` 已交付 `internal/platform/testdb` 的 Testcontainers-Go 工厂，归档任务 [`08-25-gorm-prerequisites`](../.trellis/tasks/archive/2026-08/08-25-gorm-prerequisites/) 记录了真实 PostgreSQL/pgvector、迁移、共享 `platformpostgres.Pool`、并行隔离和清理证据。
- **后续边界**：工厂只提供环境生命周期。TODO 10 各模块已在既有 fixture 中完成核心真实 PostgreSQL 与相关事务/锁/恢复验证，具体范围见下方验收记录；后续修改仍须按风险验证，不能用测试工厂交付代替业务回归。
- **边界**：单一工厂负责健康等待、正式迁移、数据库/端口/容器命名、并行隔离、日志摘要和失败销毁；业务测试不得复制容器管理。保留显式外部 DSN 用于性能、故障注入和远程 CI，但两种模式不能竞争同一数据库，并应执行同一迁移和核心断言。
- **失败语义**：Docker 不可用时由开发者选择的集成测试明确 skip 或报可操作错误，不得伪装数据库测试通过；日志不得包含密码或完整 DSN。

### 5.2 已完成开发 TODO 10：应用数据访问层全面迁移到 GORM（2026-09-08）

- **优先级/初估**：P0、高风险，80–120 人天；Testcontainers 与 Atlas 前置均已交付。
- **代码状态**：最终实现 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已提交并推送到 `origin/dev`；父任务和 30 个子任务均为 `completed`，已归档。尚未部署。
- **结果**：全部 30 个子任务（Foundation、28 个模块、Final）完成，覆盖 30 个持久化 owner。API、Worker 和六个相关 CLI 统一使用 GORM/批准的底层入口；业务事务经单一 Pool 与 `foundation.TransactionScope` 组合，legacy pgx 仓储和运行时过渡接口已清理。逐项证据见 [最终验收记录](../.trellis/tasks/archive/2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。
- **模型边界**：Persistence Model 与领域实体分离，显式表/列/nullable 映射；禁止 `gorm.Model`、隐式复数表名、软删除、自动时间戳、关联级联或 Hook 改变现有语义。
- **SQL 边界**：常规 CRUD/分页/批量使用 GORM；复杂 CTE、窗口、图、pgvector、`FOR UPDATE SKIP LOCKED`、advisory lock 和性能 SQL 使用受控 Raw/Exec/Clauses，但仍由 Repository 管理。
- **事务边界**：项目自有 Unit of Work 保持隔离级别、只读事务、Savepoint、跨 Repository 原子提交、Outbox 和 response-loss replay；GORM 类型不进入 Domain/Application。
- **pgx allowlist**：只允许 GORM PostgreSQL Driver、River、Atlas/迁移、连接级 lock、COPY/类型注册及 credential bootstrap 等经证据证明的底层能力；精确清单由 `cmd/persistencecheck` 保护，每个例外有接口、理由和验证依据。
- **配置边界**：Logger、NamingStrategy、PrepareStmt、SkipDefaultTransaction、NowFunc、连接池和错误翻译必须显式，不依赖会改变 SQL、事务、时间或脱敏语义的框架默认值。
- **兼容边界**：保持现有 Workspace/权限、分页、幂等、乐观锁、唯一约束和错误码；禁止 N+1、隐式预加载、无界查询、逐条写入退化及日志泄露 Credential、正文、完整 DSN 或高敏参数。
- **验证**：各模块核心 PostgreSQL 与相关事务/锁/River/恢复场景、受影响入口构建、定向 unit/race/vet、Go/SQL 与 credential 安全审查已完成；静态门禁通过 30 个 owner、1,788 个 Go 文件。完整容量、网络故障和外部发布矩阵未执行，具体覆盖以验收记录为准。
- **发布与回滚**：TODO 10 未改永久 Schema、历史迁移或依赖；后续 M9 以 `00093` 和 Atlas runner 兼容修复了旧库的指针回填/延迟约束冲突，原失败实库用例、历史保持、重复升级和失败回滚重试已通过。升级须使用包含兼容 runner 的构建，前 92 个迁移及其校验和不变；目标环境尚未部署。验收见 [M9 修复记录](../.trellis/tasks/07-16-product-delivery/research/m9-legacy-upgrade-2026-09-08.md)，步骤见 [Runbook](architecture/runbooks/gorm-persistence-rollout.md)。

### 5.3 TODO 11：评估并接入 gin-contrib/sessions

- **优先级/初估**：P2，5–8 人天；依赖 Gin 与 GORM。
- **目标**：只替换框架完整覆盖的 Cookie/Session 通用传输机制。
- **强制保留**：PostgreSQL 是 Session、撤销、版本和用户绑定的权威状态；CSRF、Origin、API Token、Capability、Workspace 权限和 Approval Write Authorization 保持独立门禁。
- **采用规则**：按 [ADR-0019](architecture/adr/0019-mature-framework-first.md) 比较 Cookie、Session ID、Store 生命周期、轮换、撤销、CSRF、Origin、API Token 和 Capability 的强制约束与 80% 加权覆盖。不满足则记录证据并保留最小现有实现；采用后删除被覆盖的重复代码，不创建第二连接池、迁移或 Store 事实源。
- **验收门禁**：形成框架接管/项目保留/未采用能力和退出路径的覆盖矩阵；创建、读取、轮换、撤销、过期、并发登录、Cookie 与错误响应保持兼容，日志和错误不泄露 Cookie 或 Session ID。

## 6. 已建立的前置基线

**TODO 1：固定 Docker 页面入口并取消一次性控制凭证**已经建立，初始规模估算为 P0、10–15 人天。它是后续方向的前置事实而非本路线图的任务状态：Web 固定在 `127.0.0.1:8080`，Root 只能由本机命令选择，浏览器会话与挂载权限分离，Workspace A/B 数据与索引隔离；`down` 保留选择、数据库和模型密钥，经确认的 `reset` 也不删除宿主机文件。当前实现细节见 [系统设计](architecture/system-design.md) 和 [运行手册](operations.md)。

## 7. 长期交付顺序

1. 保持 Workspace、文件/Git/数据库一致性和安全写回基线。
2. 完善持久 Workflow、摄取、检索和证据门禁。
3. 完成整理、文章优化、问答和合成笔记闭环。
4. 完善图谱、集合、健康、时间线、Artifact、Review 与 Interview。
5. 用固定评测、安全、容量、备份恢复和发布演练收口。

所有新基础设施、框架和第三方集成都必须先执行成熟方案门禁；历史理由记录到 ADR，当前任务状态只记录到 Trellis。
