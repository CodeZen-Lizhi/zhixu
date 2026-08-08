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
| SQL | pgx 参数化手写 SQL；sqlc 尚未配置 | 当前沿用现有 Repository 风格，后续引入需单独迁移门禁 |
| Migration | Goose | 前向迁移与版本管理 |
| Job Queue | River | PostgreSQL Job、重试、Worker |
| Logging | slog | 结构化日志 |
| Telemetry | 项目自有接口 + OpenTelemetry Adapter seam | Trace/Metrics；真实 exporter 尚未接入 Composition |
| Config | Viper v1 + validator v10 + YAML v3 AST 预检 | 实例化合并默认值/YAML/环境覆盖，基础字段校验与严格输入契约 |

M4-A 已在主模块精确锁定 River/riverpgxv5 `v0.40.0` 与 Goose `v3.27.0`。River 使用 MPL-2.0，Goose 使用 MIT；当前 Go/Docker 基线为 `1.25.4`，因此不采用要求 Go `1.25.7` 的 Goose `v3.27.2`。迁移、依赖 License、升级与退出门禁见 [ADR-0015](adr/0015-river-goose-runtime.md)。项目自身采用仓库根目录 `LICENSE` 中的 MIT License。

M4-D 已提供项目自有 Logger/Metrics/Tracer/Provider 接口、bounded label 与
`traceparent` 异步传播，并定义 `disabled/optional/required`。当前生产 Composition
没有注入 OpenTelemetry exporter factory：optional 明确 degraded，required
fail-fast；在真实 Adapter 和部署 smoke 完成前不得宣称外部 Telemetry 已启用。

进程配置精确锁定 `github.com/spf13/viper v1.21.0`、`github.com/go-playground/validator/v10 v10.30.3`、
`github.com/go-viper/mapstructure/v2 v2.4.0` 与 `go.yaml.in/yaml/v3 v3.0.4`。每次加载都创建独立
`viper.New()`，不使用 Viper 全局单例、`AutomaticEnv`、`BindEnv`、watch 或 remote provider。显式 registry 保留
`env > YAML > defaults`、API/non-API 读取范围以及 disabled Provider/Telemetry 的 Secret lookup gate；
validator 只承载局部字段约束，跨字段、安全、Secret 脱敏和 Model Settings Rollout 仍由项目逻辑拥有。
完整实现边界见 [进程启动配置架构](configuration.md)。

M6-A 已锁定官方 `pgvector-go/pgx v0.4.0`（MIT）作为 pgx 向量编解码与连接类型注册实现；
迁移入口使用不注册扩展类型的专用 Pool，避免空库创建 `vector` 扩展前启动失败。当前不锁定
全局 HNSW 参数，固定维度部分索引必须在真实模型与容量评测后单独落地。

## 3. 数据

| 能力 | 选择 |
|---|---|
| 主数据库 | PostgreSQL |
| 向量 | pgvector |
| 全文 | PostgreSQL FTS |
| 向量索引 | exact scan 基线；固定维度容量评测后使用部分表达式 HNSW |
| 图谱 | Relation 表 + PostgreSQL 查询投影 |
| 任务 | River 表 + Workflow 领域表 |

## 4. 内容处理

| 类型 | 选择 |
|---|---|
| Markdown | goldmark |
| HTML 正文 | go-readability Adapter |
| Tool Web HTML 安全文本 | `golang.org/x/net/html v0.56.0` token parser |
| PDF | pdftotext/Poppler Adapter |
| Hash | 标准库 SHA-256 |
| Git | Git CLI Adapter |

PDF 抽取作为 Adapter，因为纯 Go PDF 文本库质量和布局兼容性可能变化。

M6-03 使用 BSD-3-Clause 的 `golang.org/x/net/html v0.56.0` 解析受控 Web Tool HTML 文本；
不使用正则清理 HTML。parser 只位于 SSRF-safe Web Adapter，script/style/事件内容和超出节点/深度/
解压后字节预算的响应必须拒绝或截断为受控输出，不能把第三方 DOM 类型带入领域层。

## 5. AI

| 能力 | 选择 | 采用约束 |
|---|---|---|
| Chat | OpenAI-Compatible HTTP Adapter | 默认可替换实现 |
| Local Model | Ollama OpenAI-Compatible Endpoint | 通过统一 ChatModel Interface 接入，不维护第二套原生 Chat 协议 |
| Embedding | OpenAI-Compatible/Local Adapter | 批量调用并记录模型版本 |
| Rerank | HTTP Adapter，可禁用 | 失败必须显式标记 degraded |
| Structured Output | JSON Schema + 领域校验 | 框架输出仍需领域校验 |
| Prompt | 版本化模板 | 运行记录保存实际版本 |
| Agent 编排 | 项目自有 Application | 直接编排稳定 Interface，不采用 Eino 主模块依赖 |

Eino 不是当前依赖。[ADR-0013](adr/0013-eino-adoption-gate.md) 定义的采用门禁已由 M2 执行，因关键项未全部通过，主模块正式选择项目自有 Application + 直接 OpenAI-Compatible Adapter。

M2 已在独立 `poc/eino` module 中验证 Eino `v0.9.12` 的 Chat Graph、ToolsNode、Callback 以及 OpenAI 扩展 `v0.1.13` 的编译/配置边界，但 Streaming、Structured Output、Embedding/Retriever/Rerank、River Node 和真实 Provider Smoke 门禁尚未全部通过。因此当前结论为“不正式采用”，主模块继续保留直接 OpenAI-Compatible Adapter 路线。详见 [`poc/eino/report.md`](../../poc/eino/report.md)。

领域模型、Workflow 持久化与状态机、Proposal/Approval、Tool Permission 和 Write Authorization 不得依赖 Eino 类型或运行时。核心同样不依赖 LangChain；第三方 AI Framework 只能位于 Agent/Application 编排边缘或 Adapter/Infrastructure，且必须通过项目 Interface 隔离。

## 6. 前端

| 能力 | 选择 |
|---|---|
| UI | React + TypeScript |
| Build | Vite |
| Server State | TanStack Query |
| Routing | React Router |
| Graph | M7-01 使用有界 SVG/CSS + 语义列表；Cytoscape.js 仅作后续候选 |
| Diff | Monaco Diff Editor |
| Realtime | EventSource/SSE |
| Test | Vitest + Testing Library + Playwright |

前端运行时和工具链由 `web/package.json`、`web/package-lock.json` 与 `deploy/Dockerfile` 共同锁定：Node.js `24.18.0`、npm `11.7.0`、React `19.2.7`、TypeScript `5.9.3`、Vite `8.1.5`、Vitest `4.1.10`、Playwright `1.61.1`。依赖升级必须同步 lockfile、镜像基线和回归门禁。

Graph 展示实现不进入领域模型。当前 `web/package.json` 未引入 Cytoscape.js；只有 M10 的 500,000 Relation
容量与前端 FPS/交互证据证明有必要时，才单独评估 Cytoscape.js/Web Worker 并锁定依赖。

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
- Graph 在 500,000 Relation 正式资源预算下的有界渲染与 FPS/交互；若当前 SVG/CSS + 列表方案不达标，
  再验证 Cytoscape.js/Web Worker 候选，不预设 5,000 节点全量渲染。
- Monaco 大 Diff。
- Eino Chat、Embedding、Streaming、Structured Output、Tool Calling、Callback/Trace、取消、限流、错误映射和 River Node 集成 PoC；未通过不得成为正式依赖。
