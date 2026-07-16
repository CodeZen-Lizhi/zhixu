# 技术栈与依赖选择

## 1. 原则

- 选择项目初始化时的稳定版本并锁定。
- 依赖服务数量最小。
- 核心领域逻辑不绑定框架。
- 优先成熟、可测试、Go 生态自然的库。
- 外部工具通过 Adapter。

## 2. 后端

| 能力 | 选择 | 用途 |
|---|---|---|
| 语言 | Go | API、Worker、领域模块 |
| HTTP | net/http + chi | REST、Middleware、SSE |
| PostgreSQL Driver | pgx | 连接池、事务、COPY |
| SQL | sqlc | 类型安全查询，保留 SQL 控制 |
| Migration | Goose | 前向迁移与版本管理 |
| Job Queue | River | PostgreSQL Job、重试、Worker |
| Logging | slog | 结构化日志 |
| Telemetry | OpenTelemetry | Trace/Metrics |
| Config | 环境变量 + YAML | 本地与自托管配置 |

## 3. 数据

| 能力 | 选择 |
|---|---|
| 主数据库 | PostgreSQL |
| 向量 | pgvector |
| 全文 | PostgreSQL FTS |
| 向量索引 | HNSW |
| 图谱 | Relation 表 + PostgreSQL 查询投影 |
| 任务 | River 表 + Workflow 领域表 |

## 4. 内容处理

| 类型 | 选择 |
|---|---|
| Markdown | goldmark |
| HTML 正文 | go-readability Adapter |
| PDF | pdftotext/Poppler Adapter |
| Hash | 标准库 SHA-256 |
| Git | Git CLI Adapter |

PDF 抽取作为 Adapter，因为纯 Go PDF 文本库质量和布局兼容性可能变化。

## 5. AI

| 能力 | 选择 | 采用约束 |
|---|---|---|
| Chat | OpenAI-Compatible HTTP Adapter | 默认可替换实现 |
| Local Model | Ollama Adapter | 通过统一 Model Interface 接入 |
| Embedding | OpenAI-Compatible/Local Adapter | 批量调用并记录模型版本 |
| Rerank | HTTP Adapter，可禁用 | 失败必须显式标记 degraded |
| Structured Output | JSON Schema + 领域校验 | 框架输出仍需领域校验 |
| Prompt | 版本化模板 | 运行记录保存实际版本 |
| Agent 编排 | Eino（PoC 通过后可选采用） | 仅限 Agent/Application/Adapter/Infrastructure |

Eino 不是当前已锁定依赖。只有 [ADR-0013](adr/0013-eino-adoption-gate.md) 定义的 PoC 全部通过后，才在项目 Manifest、Lockfile 和 CI 中锁定经验证版本；PoC 失败则继续使用直接 OpenAI-Compatible Adapter。

领域模型、Workflow 持久化与状态机、Proposal/Approval、Tool Permission 和 Write Authorization 不得依赖 Eino 类型或运行时。核心同样不依赖 LangChain；第三方 AI Framework 只能位于 Agent/Application 编排边缘或 Adapter/Infrastructure，且必须通过项目 Interface 隔离。

## 6. 前端

| 能力 | 选择 |
|---|---|
| UI | React + TypeScript |
| Build | Vite |
| Server State | TanStack Query |
| Routing | React Router |
| Graph | Cytoscape.js | 知识图谱、路径与聚类展示 |
| Diff | Monaco Diff Editor |
| Realtime | EventSource/SSE |
| Test | Vitest + Testing Library + Playwright |

Graph 库属于可替换展示实现，不进入领域模型。

## 7. 测试

| 能力 | 选择 |
|---|---|
| Go | testing、httptest |
| DB Integration | Testcontainers-Go |
| Fake | 手写领域 Fake 为主 |
| E2E | Playwright |
| AI Eval | 自建数据集与 Runner |
| Load | Go Benchmark + k6（可选） |

## 8. 部署

| 能力 | 选择 |
|---|---|
| Local/Self-host | Docker Compose |
| Reverse Proxy | Caddy 或 Nginx |
| Image | Multi-stage、non-root |
| Frontend Static | Go embed 或 Proxy |
| Backup | pg_dump + Workspace/Git 备份 |

## 9. 不引入

- Kafka。
- Kubernetes。
- Redis 必需依赖。
- 独立向量数据库。
- 独立图数据库。
- Temporal。
- Elasticsearch。
- 通用 Agent Framework 作为核心。

## 10. 选型验证

- pgvector 50 万向量检索。
- River Node Job 与事务插入。
- pdftotext 引用页码。
- Cytoscape.js 5,000 可视节点的聚类/按需展开。
- Monaco 大 Diff。
- Eino Chat、Embedding、Streaming、Structured Output、Tool Calling、Callback/Trace、取消、限流、错误映射和 River Node 集成 PoC；未通过不得成为正式依赖。
