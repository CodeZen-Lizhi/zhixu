# 后端目录与模块结构

## 适用范围

本规范适用于 Go API、Worker、领域模块、应用层和基础设施 Adapter。仓库已进入 M5/M6 实施阶段；目录存在性和示例以当前代码为准，下面的边界仍是后续模块的约束。

## 已确认事实

- 系统采用模块化单体；API 与 Worker 是两个可独立运行的进程，共享领域模块和 PostgreSQL（依据 [`system-design.md`](../../../docs/architecture/system-design.md)）。
- 依赖方向是 `presentation → application → domain modules`，Workflow 只能依赖 Agent、Tools 和领域接口，Adapter 实现领域接口（依据 [`system-design.md`](../../../docs/architecture/system-design.md)）。
- 领域模块必须隐藏实现复杂度，对外暴露小而稳定的 Interface；事务由维护不变量的模块控制，HTTP Handler 不得发起跨模块事务。
- API 负责同步查询、命令提交和 Human Decision；Worker 负责租约、长任务和副作用。预计超过 3 秒的任务通过持久化 Workflow 异步执行（依据 [`application-contracts.md`](../../../docs/architecture/application-contracts.md) 与 [`ai-runtime.md`](../../../docs/architecture/ai-runtime.md)）。
- 当前已有 Go module、API/Worker、Workspace、Workflow、Ingestion、Change Control 和 Retrieval 实现；新增模块必须复用相同的 domain/application/adapter 分层与 Composition Root 模式。

## 目标代码落点（M1 起）

```text
cmd/
  api/                         # API 进程入口与 composition root
  worker/                      # Worker 进程入口与 composition root
internal/
  app/                         # Command/Query 编排、HTTP/SSE 边界所需应用服务
  workspace/                   # Workspace、路径和文件版本边界
  ingestion/                   # Source、解析、分块和导入流程
  retrieval/                   # FTS、向量、融合排序和索引版本
  knowledge/                   # Topic、Claim、Relation、Conflict、Provenance
  changecontrol/               # Proposal、Approval、Safe Writeback、补偿
  graph/                       # 图谱查询、候选关联和路径
  collection/                  # Query AST、集合和视图配置
  export/                      # Smart Collection 异步导出 Job、结果与恢复
  artifact/                    # Artifact 大纲、章节和导出
  review/                      # Deck、Card、Session、评分和调度
  health/                      # Health Issue 检测与修复 Proposal
  workflow/                    # Definition、Run、Node、Human Task、Outbox
  agent/                       # 结构化 Agent 编排和模型端口
  tools/                       # Tool Registry、授权、执行和审计
  memory/                      # 用户确认的长期/情景 Memory
  audit/                       # 独立审计写入与查询边界
  platform/
    postgres/                  # pgx/sqlc/River/Goose 适配实现
    filesystem/                # WorkspaceStore 本地文件实现
    gitcli/                    # Git CLI 适配实现
    models/                    # Chat/Embedding/Reranker 适配实现
    parser/                    # Markdown/PDF/HTML 解析适配实现
migrations/                    # Goose 前向迁移
web/                           # React 构建产物或嵌入边界；不放领域逻辑
```

该布局来源于 [`system-design.md`](../../../docs/architecture/system-design.md)。实际包名、是否使用 `web/` 嵌入以及数据库逻辑 Schema 需在 M1 manifest、迁移和构建配置落地后再以代码为准。

## 模块组织规则

1. 每个领域模块拥有一个对外稳定的领域 Interface；内部实现、持久化细节和策略放在模块内部。
2. `internal/platform/*` 只实现 Adapter，不承载业务规则；Adapter 不得依赖 Application Command Handler。
3. `cmd/api` 与 `cmd/worker` 只负责读取配置、构造连接池和 Adapter、注入模块、注册 Workflow/Tool、启动进程。模块不得自行读取环境变量或创建 SDK Client。
4. Presentation 只调用 Application；禁止直接访问 Repository、pgx、sqlc 生成类型或文件/Git Adapter。
5. 模块间通过公开 Interface、领域类型或事件协作；禁止引用另一个模块的内部包。
6. Shared Kernel 仅保留真正共享的 ID、时间、分页和通用错误等概念；不能借此放置业务服务或跨模块数据库模型。
7. 文件/Git/数据库的一致性通过 Change Control 的有序 Saga、Outbox 和补偿处理；不得试图把文件或 Git 纳入数据库事务。

### M6-04 RAG Production Composition

- API 的 Conversation Repository、Question Dispatcher、Feedback Service 与 SSE Handler 必须共享同一个 PostgreSQL pool 和同一个 `events` Store/Appender；Question Dispatcher 复用 API 唯一 Workflow RuntimeRepository。
- Chat 显式 disabled 时，Conversation create/read、Answer/Turn read、Feedback 与 SSE 仍可用，但不得注册 RAG Definition/Executor，也不得注入 Question Dispatcher。
- Chat enabled 时，API 只有在 Workflow、Workspace/Retrieval 和 Conversation/Event 全部组装成功后才注入 Question Dispatcher；`/readyz` 对任何缺失依赖 fail closed，`system/status` 只暴露稳定、脱敏的 RAG 状态。
- Worker 的 Relation 与 RAG Executor 共享冻结 Chat contract、Runtime Catalog、Agent Repository 和预算；RAG 另注入真实 Conversation Context/Finalizer、Retrieval、Knowledge Topic、Event Progress，禁止从 Tool runtime 借用 Search 或受 Tool disabled 状态影响。
- Worker ExecutorRegistry 与 DefinitionRegistry 必须同时可解析 Relation 和 RAG；任一半注册状态都不能 ready，Safe Writeback、Tool 与 Reindex 的既有注册保持独立。

### M9-03 Smart Collection Export Module Boundary

- `internal/export/domain` 拥有 Export Kind、Job、生命周期、字段白名单、脱敏与 prepared-result 不变量；它不导入
  HTTP、pgx、River、文件系统或 Audit Adapter。公开正式 kind 仅为 `MARKDOWN|METADATA_JSON`。
- `internal/export/application` 编排授权、Collection durable scan、lease、staging/promote、恢复、过期、cleanup 与
  下载统计/Audit binding。prepared 后不得重新读取 Collection 或生成第二个结果。
- `internal/export/adapter/{collection,localfs,postgres,river,auth}` 分别实现 snapshot、受控 Workspace 文件、
  PostgreSQL Job/Event/Audit、River transport 和能力检查；Adapter 不拥有业务状态机，River Args 只携带
  Workspace ID 与 Export ID。
- `internal/export/http` 只承担严格 HTTP wire、Problem、下载 header 与当前 principal 到 Audit actor 的映射；
  `cmd/api`、`cmd/worker` 负责组装并运行 API、Worker、recovery 与 cleanup。Collection 页面通过 API/Query 恢复，
  不直接读取文件或 PostgreSQL。
- Artifact Export 与 Smart Collection Export 是不同领域事实。不得让 `ops.export_job` 充当 Artifact 状态源，
  也不得把附件、`EVALUATION_JSON`、`AUDIT_JSON` 伪装成 M9-03 已实现的 Export kind。

## 命名约定

- Go 包名使用小写单词，不使用下划线或复数缩写；公开类型和方法按领域术语命名，例如 `Workspace`、`WorkflowRun`、`CreateProposal`。
- 模块目录使用架构领域名；`changecontrol` 是已确认的目录名，不能在代码中另起 `change_control` 形成第二套称呼。
- 入口目录使用 `cmd/<process>`；平台实现放在 `internal/platform/<adapter>`。
- 文件名采用 Go 工具链习惯（小写、必要时下划线），不以 HTTP、数据库表或页面名称替代领域概念。
- 真实分层示例见 `internal/ingestion/{domain,application,adapter}`、`internal/retrieval/{domain,application,adapter}`；新模块应遵循相同依赖方向，不复制其业务语义。

## 禁止模式

- 在 Handler 中实现领域状态机、写文件、创建 Git Commit 或同步执行长 Workflow。
- 在领域包中导入 HTTP 框架、pgx、sqlc、River、模型 SDK、React 或文件系统实现。
- 通过模块内部包、全局变量或循环依赖共享业务状态。
- 以数据库表、前端页面或 Chunk 名称冒充 Source、Document、Claim、Proposal 等领域对象。
- 将 Agent 逻辑直接绑定文件/Git Adapter，绕过 Change Control、Approval 和 Tool 权限边界。
- 为了“方便”在 `internal/platform` 中新增业务规则或隐式降级。

## 验证方式

### M0 当前（仅规范）

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

### M1 代码落地后

- `go list ./...` 能枚举 `cmd/api`、`cmd/worker` 和各领域包。
- `go test ./...` 覆盖模块 Interface 及依赖方向相关测试。
- 对新增跨层依赖执行静态检查；发现 Presentation→Repository、Domain→Adapter 等违规必须阻断合并。
- API 与 Worker 使用同一 composition root 组装，启动和最小 readiness 烟测均通过。

## 当前待验证

- 尚未实施模块的实际包名和 Interface 文件位置。
- 新模块是否继续满足 domain 不依赖 Adapter、Presentation 不直连 Repository。
- 是否需要额外的 `internal/shared` 包；只有出现至少两个真实调用方且概念确属共享时才能新增。
- 项目尚无独立循环依赖 lint target，当前通过 `go list/go test/go vet` 和 import review 检查。

## M5-05 Knowledge Module Contract

- `internal/knowledge/domain` 拥有 Topic、Claim、Claim Source、Relation、Relation Evidence、Conflict、
  Applicability 与 Relation Assessment；只能依赖 `internal/foundation`。
- `internal/knowledge/application` 拥有短事务命令、Provenance/Confirmation Port、幂等和批量查询上限；
  不调用模型、不读取文件路径、不直接依赖 Retrieval DTO。
- `internal/knowledge/adapter/postgres` 是唯一 SQL/UoW 实现；Graph、Collection、Agent 不得直接写知识表。
- Relation 是 Topic–Claim `BELONGS_TO` 的唯一正式事实；不得再建可独立写入的 `topic_claim`。
- Claim Source、Relation Evidence、Search Evidence、Proposal Evidence 保持不同语义；共享的只有
  Workspace → Source Version → Source Span Provenance 绑定规则。
- 当前文件型 Change Control Proposal 不能用假路径/Hash/正文复用为 Knowledge Proposal；typed proposal
  由后续任务以向后兼容方式扩展。
