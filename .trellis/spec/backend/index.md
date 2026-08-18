# 后端开发规范

本目录是 Go API、Worker、领域模块、数据库、Adapter、Workflow 和可观测性实现的规范入口。M1 已建立可运行骨架；后续业务模块必须继续遵守这里的边界，并以真实代码和任务验收结果为准。

ADR-0027 将 Eino 设为正式生产 AI Runtime：Chat、OpenAI-Compatible/Ollama Embedding 和五个 Structured
Scheduler 固定使用 Eino。新 `/chat` 使用 RAG v2，由 Eino Graph、classic `ChatModelAgent`、冻结只读 ToolsNode
和无工具 final-answer Stream 承担通用内层编排；草稿经 PostgreSQL/SSE 传递，Finalizer 仅在正式 Answer 原子提交时
标记 `PUBLISHED`。项目继续拥有领域 Workflow、权限、审批、Evidence、PostgreSQL/River、Model/Tool 事实和恢复。
生产不提供 direct 实现选择器或自动 fallback；Checkpoint 仍是同进程、同活跃 Attempt 的隔离 PoC。具体契约见
`eino-chat-adapter.md`、`eino-embedding-adapter.md`、`eino-structured-scheduler.md` 和
`eino-runtime-adoption-gates.md`。

M7-01 已补充 Graph canonical read projection、公共 HTTP/真实进程 smoke、容量 benchmark 与跨层质量门禁；
首版只读 Topic/Claim，500,000 Relation/FPS 与正式认证仍归 M10。
M7-02 已补充 Semantic Link Candidate、typed Relation Proposal、Approval 后 Knowledge apply、durable Topic scan、
严格故障隔离与执行计划门禁；Candidate 仍不是正式 Relation。
M7-03 已补充版本化 Smart Collection、统一 read model/cursor、持久 Health Scan/Issue/Schedule、SMART_COLLECTION
Candidate scope、affected-change outbox、真实 PostgreSQL/River/API/浏览器门禁；Tag/Review/Directory owner、Artifact/
Review 批量动作、认证和 M10 最终容量仍未实现。
M7-04 已交付 append-only Knowledge Timeline、durable Outbox/Worker 恢复、Workspace-bound HMAC cursor、Timeline UI、
`impact-analysis/v2` Artifact/Review Card impact、一等 owner event、正式 `downstream_update` Proposal 意图与
`IMPACT_ANALYZED` Audit 原子事务；下游 owner executor 与 Document/Eval impact 仍不在当前合同。OTel exporter 与
API/Worker Prometheus 已交付；跨领域 Audit 生产者覆盖、查询/留存/恢复和 POISONED 运维入口仍未完成。
M9 已补充 Workspace Source Version/Proposal/Workflow 列表、资源绑定 cursor 与 Active Index 选择投影；
Proposal 等级由不可变 `proposal.risk_level` 唯一拥有，Source Version 使用受复合约束的 Workspace 镜像键支撑
有界 keyset 查询；Proposal detail 的 Approval 为必需 nullable，Summary 保持 optional non-null。三条真实
PostgreSQL 列表、迁移契约与严格执行计划已在最终迁移工作树复验；M10 继续负责最终容量门禁。
M9 Export 已交付两个显式 scope：Smart Collection `MARKDOWN|METADATA_JSON` 与 Workspace
`ATTACHMENTS_ZIP`。PostgreSQL 保存 tagged binding、幂等、租约、prepared result、持久 capability gate、
TTL/cleanup 与下载 Audit；附件 ZIP 从固定 `attachments/` 生成，使用 fd-relative 安全遍历、确定性 manifest 和
验证后的流式下载。`EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX 与通用字段映射仍为后续独立范围；
AC-33 已由 Markdown、领域 Metadata JSON 和真实附件字节闭环关闭。
M10-02 已交付单用户 Auth、Cookie Session、受限 API Token、CSRF/Origin、Capability Middleware 和 Compose
启动预检；认证与 Write Authorization 保持两道独立边界，具体可执行契约见 `auth-security.md`。
Docker Web/API 固定通过 IPv4 loopback 发布，宿主机不再运行常驻网页控制进程。Workspace Root/Docker mutation
由启动器调用的一次性原生命令执行；精确 bind、quiescence、lease、失败回滚和 Workspace ID 隔离保持不变。
M8-01 已交付 Workspace 隔离的 Artifact/Revision、证据复核章节生成、受控 Markdown 导出和
`PUBLISH_ARTIFACT` Proposal；外部副作用由持久 reservation 与 receipt/side-fact 原子闭合保护。
M8-02 补充 Review Deck/Card、服务端评分、冻结 FSRS/Scorer version、Review-only Session 边界、严格 due 投影和
Card/Schedule 失效收敛。due 查询必须携带活动 Review Session，`question_ref` 使用 API-only HMAC 绑定该 Session 与
Card/Schedule 快照；显式 key 或 `required` 模式域隔离派生 key 可跨重启稳定，
多实例必须显式共享同一 key。key 轮换、不同 key 实例或 local `disabled` 随机 key 重启后未提交题目必须刷新，已落库 Answer
仍可 exact replay。`00052` 将 `DISPUTED` 等非 CONFIRMED Claim 与不可验证 legacy evidence 的 APPROVED Card quarantine；
`00058` 以 Source-only/Source+Claim 双 selector 索引支撑有界失效，并让每条 Card 语句只调用一次集合化 Health helper；
`00049` 只将失效结果投影到既有 Health/Timeline 兼容链路，`review_card` 仍是生命周期唯一事实源，不能将其宣称为
完整 Review Health/Impact。
M8-03 补充独立 Interview Question/Turn/Report、Review/Interview 共享 Learning Path、Memory Candidate/Confirm 生命周期与
effective-context 过滤。`00059` 用结构化 session/path/step provenance、复合 FK、每步唯一 Candidate、
Audit aggregate/version 和 no-keepalive guard 强化 Memory/Completion；`00060` 将 `learning.learning_path(_step)` 设为
唯一基表，保留 Interview 可更新兼容视图，并以 `INTERVIEW|REVIEW` origin/source shape、Review Answer 唯一绑定、command/
reservation/hidden hold 与 retained-history trigger 保护共享 Path。
Interview 已交付难度 policy、`started_at + duration` deadline、连续追问与 SKIPPED gap/path；用户 HTTP 固定 USER provenance，INTERVIEW
Candidate 只能来自服务端已持久化 Path 步骤。Completion 采用 Begin/Prepare/Complete reservation：冻结 Session snapshot、
pending reservation 阻止 Submit、在 Artifact hidden hold 创建事务内绑定 digest，final 事务核对并写 Report/Path/receipt 后
release 精确 hold；Worker 在 24h 有界维护中将超时 reservation 转为 ABANDONED、hold 转为继续隐藏的 ORPHANED。本范围不
物理删除恢复/审计资产。Interview Candidate 同时绑定客户端 key 的完整请求与服务端 provenance 的语义 identity。
Conversation RAG 现通过 Agent-owned loader 显式读取 global/Conversation-scoped effective Memory；`00061` 先按
`node_attempt_id` 创建唯一 `PREPARING` snapshot claimant，再把 canonical non-evidence digest 与 Model Run 在同一事务闭合，
正文不进入 snapshot、Model Run、Workflow input、事件或日志。Memory 只注入 `agent.rag-answer` Conversation 节点，Relation
Assessment、Artifact Generation 和共享模型 factory 保持不注入；`last_used_at`、最近使用 UI 与类型转换仍未交付。
Quick Capture 已交付 TEXT/URL/FILE/IMAGE 的不可变 Capture、Workspace-owned Source/Artifact/Version、durable outbox/
Worker、独立 Ingestion/Index/Profile 状态和 `document-knowledge-profile/v1`。Profile Revision/Evidence append-only，
候选不进入正式知识；模型/Embedding disabled 时保留 Keyword 与基础资料，具体契约见 `capture-profile-contract.md`。
Authoring 已交付可恢复 Working Draft、每次 Freeze 追加不可变 Article Revision、Document Draft 列表，以及
`CREATE_ONLY|REPLACE` Proposal Reservation/Binding/Commit 最终化。数据库拒绝绕过 Proposal Commit 伪造 Published，
确定性发布前失败使用独立 ABANDONED 事实；具体契约见 `authoring-contract.md`。
Organizing 已交付可恢复 Suggested Material Set、显式确认、不可变 Workflow Input Snapshot、四个固定 Workflow 和
受约束自定义 Template Revision。确认在同一 Serializable 事务重验 owner facts；Human Task 读取与提交均 fail closed
复投影 Evidence/GAP/Diff，结果只经 Artifact/Proposal 与 Safe Writeback；具体契约见 `organizing-contract.md`。
Document History 已交付当前分支、当前 canonical path 的有界 Git 时间线、managed/external/current-change 区分、
严格版本比较和 `restore_document` Proposal。恢复只追加 Safe Writeback Commit 与 Article Revision，崩溃恢复重新校验
Authoring owner，禁止 reset/checkout/history rewrite；具体契约见 `document-history-contract.md`。
Git Remote Sync 已交付单 Workspace 单 HTTPS Remote、write-only AES-GCM Token、SSRF/DNS pinning、受控
AskPass、持久 Run/Attempt/Outbox、same/Fast-forward/non-force Push/post-check、result unknown 恢复、外部变化捕获和
独立索引状态。自动候选同时绑定 writeback Commit 与配置 Revision，并在应用层和 PostgreSQL 事务内双重栅栏；
与 Safe Writeback 复用 Workspace Git operation lock，具体契约见 `git-sync-contract.md`。
Review-derived Path 已统一 `00060`、Repository 与 Domain/Application 契约；ABANDONED 仅允许原 key 按新 `attempt_no`
重开，Artifact digest 冻结 source snapshot、完整规范化 Draft、renderer 元数据与 attempt，Prepare/Complete 和精确 hold
release 都受 attempt fence 保护。API 在数据库依赖可用时组装真实
Repository/Artifact bridge/Service/Handler，System Status 仅在 Review 与 Learning Path Handler 均可用时标记 ready；Worker
在启动和周期维护中调用 Application-owned 24 小时有界策略。`00059/00060/00061` guarded Down、Review Path 并发/
response-loss/ABANDONED 重开、Agent snapshot 原子绑定和 API/Worker production composition 已用隔离 PostgreSQL 动态验证；
Go race/vet/tidy、OpenAPI 与 Web lint/typecheck/test/build 门禁通过。

## 规范索引

| 规范 | 内容 | 当前状态 |
|---|---|---|
| [目录与模块结构](./directory-structure.md) | 进程入口、领域模块、Adapter 和依赖方向 | M1 入口与依赖边界已验证；领域模块待后续任务补充 |
| [认证与安全契约](./auth-security.md) | Session、API Token、CSRF、Capability、配置与 Compose 门禁 | M10-02 已锁定 API/DB/env 契约、失败矩阵与真实 PostgreSQL/Compose 验证 |
| [进程配置加载契约](./config-loading.md) | 实例化 Viper、validator、YAML/env 严格边界、process profile、Secret 与 Rollout 归属 | defaults/YAML/env 迁移已锁定；[维护者总览](../../../docs/architecture/application-contracts.md) 已同步，无全局 Viper、热更新或数据库 Rollout 状态读取 |
| [Gin HTTP 边界规范](./http-boundary.md) | 唯一 Engine、stdlib bridge、路由/OpenAPI 对等、Middleware、recovery 与 SSE flush | Gin v1.12.0 已迁移；operation 集合由可执行 inventory 精确对等，Chi 已移除 |
| [宿主机 Workspace 精确授权契约](./workspace-root-grant.md) | 一次性 Workspace Control、不可变 Root identity、单 Grant 状态机、exact bind 与 Docker 固定入口 | Root/Docker 控制由本机命令保护；运行时只允许 API/Worker 精确 source=target 授权，Web 以 Active Workspace API 为事实源 |
| [模型设置与热运行时契约](./model-settings-runtime.md) | desired/active/applied revision、AEAD、generation lease、双进程热激活与 Docker 生命周期 | managed Settings 无重启生效、冻结任务绑定和真实容器身份门禁 |
| [Eino Chat Adapter 契约](./eino-chat-adapter.md) | Eino Chat、wire/响应、安全传输、调用级 Callback telemetry、错误矩阵与升级门禁 | 生产固定 Eino，真实 Provider/发布验收独立记录 |
| [Eino Embedding Adapter 契约](./eino-embedding-adapter.md) | Eino OpenAI-Compatible/Ollama、wire 向量交叉校验、安全传输、错误矩阵与升级门禁 | 生产固定 Eino，真实 Provider/发布验收独立记录 |
| [Eino Structured Scheduler 契约](./eino-structured-scheduler.md) | Application phase-scheduler Port、固定短 Graph、五消费者编排、错误/预算/审计门禁 | 生产固定 Eino；构建失败 fail closed |
| [Eino 生产 AI Runtime 契约](./eino-runtime-adoption-gates.md) | RAG v2 Agent/短引用只读工具/final Stream、draft SSE、Finalizer 与稳定性门禁 | Eino 是唯一部署路径；Checkpoint 仍仅 PoC，真实 Provider 与稳定性证据独立记录 |
| [Workspace Analysis 合同](./workspace-analysis-contract.md) | 显式模式、六阶段 Definition、精确只读 Tool、Operation/receipt/预算恢复、终态发布与时间线 | 本地实现、真实 PostgreSQL/River、旧/新四组合、post-fact Worker-only 回滚、进程 kill/reclaim、固定 RAG、Worker OTLP 演练与桌面/移动浏览器链路已验证；目标环境 Migration、Canary、OTLP 观察窗口与扩量仍需发布授权 |
| [Timeline 与 Impact 契约](./timeline-impact.md) | append-only Event/Report、Outbox 状态机、Impact/Audit 原子事务、API/Worker/Web 门禁 | M7-04 与遗留收口已验证；下游 owner executor、Document/Eval impact 与全局 Audit 保持 deferred |
| [Artifact 产物闭环契约](./artifact-contract.md) | Revision、Citation、generation、receipt/reservation、导出与 Publish Proposal 边界 | M8-01 后端、迁移、API/Worker 和真实浏览器闭环已验证；Proposal 批准后的正式写回保持 Change Control owner |
| [快速记录与画像契约](./capture-profile-contract.md) | Capture、Source/Version、Outbox、独立阶段、Profile Revision/Evidence 与降级恢复 | TEXT/URL/FILE/IMAGE、Profile v1、真实 PostgreSQL 与桌面/移动 Capture 链已验证 |
| [主动创作与 Document Draft 契约](./authoring-contract.md) | Working Draft、Freeze、Revision、发布预留、CREATE_ONLY 与 Commit 最终化 | Domain/Application、真实 PostgreSQL、Change Control/Git、HTTP/OpenAPI、前端构建与桌面/移动浏览器门禁已验证 |
| [材料确认与整理模板契约](./organizing-contract.md) | Suggested Material、Serializable Snapshot、Template Revision、四 Workflow、Human Task review 与结果治理 | Domain/Application、真实 PostgreSQL、Workflow/Artifact/Proposal、HTTP/OpenAPI、前端构建及父任务桌面/移动浏览器门禁已验证 |
| [文档文件历史与受控恢复契约](./document-history-contract.md) | 当前 path Git 时间线、映射、cursor、compare、restore Proposal、Safe Writeback 与 Authoring closure | Domain/Application/Git/PostgreSQL/Change Control/Authoring、HTTP/OpenAPI、Web 构建、并发恢复及父任务桌面/移动浏览器门禁已验证 |
| [Git 远端同步契约](./git-sync-contract.md) | HTTPS Remote、AEAD Token、SSRF/AskPass、Run/Attempt/Outbox、受控 Fetch/Fast-forward/Push、post-check、自动调度与索引分离 | Domain/Application/Git/PostgreSQL/API/Worker、安全与 migration 门禁已验证；真实浏览器已覆盖桌面/390x844 配置、持久运行恢复、分叉冲突、有界预览和重试 |
| [可恢复异步 Export 契约](./export-contract.md) | Tagged Job、Collection scan、附件 ZIP、prepared result、流式下载、Audit 与清理 | Collection Markdown/Metadata JSON 和 Workspace Attachments ZIP 已交付并关闭 AC-33；Evaluation/Audit 内容导出保持 deferred |
| [数据库开发规范](./database-guidelines.md) | pgx/Goose/River、参数化查询、事务、迁移和约束 | 已记录 M7-03 Collection/Health、M8 Review/Interview/Shared Path/Memory、M9 Workspace 列表的持久化、receipt、可见性与 PG/EXPLAIN 门禁 |
| [错误处理规范](./error-handling.md) | 领域错误、Retry 分类、Problem Details、SSE 错误 | 已记录 Tool 稳定错误、M9 Proposal detail/summary Approval 空值契约与 M10 Auth 的稳定 Problem Details 边界 |
| [日志与审计规范](./logging-guidelines.md) | slog JSON、OTel/OTLP Trace/Metrics、Prometheus、脱敏和 Audit | 显式 Provider、双 signal 启动探针、API 本地 `/metrics`、API/Worker OTLP Metrics、River consumer span 与环境隔离合同已验证；目标环境后端观察仍需发布授权 |
| [质量与交付规范](./quality-guidelines.md) | 禁止模式、测试金字塔、安全、Review 和门禁 | 已记录 M7 Graph/Collection/Health、M8 Review/Interview/Shared Path/Memory 和 M9 Export 的跨层、fault、浏览器与独立审查门禁 |

## 开发前检查清单

开始修改后端代码前，必须：

1. 读取本目录与任务的 `prd.md`、`design.md`、`implement.md`，再读取对应 `docs/architecture/` 文档；不得凭经验猜接口、数据库或命令。
2. 确认任务处于 `in_progress`，明确影响模块、公共契约、数据流、兼容性、测试范围和恢复路径。
3. 先搜索现有领域术语、错误码、配置字段、查询和工具；共享规则只能有一个事实源。
4. 对跨层变更阅读 `.trellis/spec/guides/cross-layer-thinking-guide.md`；发现重复实现时阅读 `code-reuse-thinking-guide.md`。
5. 只有 Composition Root 读取配置并构造 Adapter；领域模块不自行创建数据库、模型或 Git 客户端。
6. 修改 Export Job、结果文件、下载、清理或 Collection Export UI 时，先阅读 [`export-contract.md`](./export-contract.md)；
   不把附件、`EVALUATION_JSON`、`AUDIT_JSON` 或 AC-33 全量完成推入 M9-03。
7. 修改 Docker Workspace 路径、一次性 Workspace control、Registry/Active/runtime grant 或启动器时，先阅读
   [`workspace-root-grant.md`](./workspace-root-grant.md)；禁止恢复父目录映射、`/workspace` target 或 Docker socket。
8. 修改 Chat Adapter、Eino/OpenAI extension 或模型 vendor 时，先阅读
   [`eino-chat-adapter.md`](./eino-chat-adapter.md)，并保持 Eino 合同、错误和安全边界。
9. 修改 Embedding Adapter/factory、Eino Embedding extension、Ollama SDK 或模型 vendor 时，先阅读
   [`eino-embedding-adapter.md`](./eino-embedding-adapter.md)，并保持两个 Provider 的 Eino 合同等价。
10. 修改 StructuredRunner、Eino Graph、五个消费者 scheduler 或对应配置时，先阅读
   [`eino-structured-scheduler.md`](./eino-structured-scheduler.md)；Graph 不得获得预算、持久化或副作用所有权。
11. 修改完整 RAG 编排、Retriever bridge、Tool/ReAct、Token Streaming、Checkpoint/Interrupt 或 ADK 时，先读取
   [`eino-runtime-adoption-gates.md`](./eino-runtime-adoption-gates.md)；未满足重开条件不得新增生产入口或 selector。

## 实现边界

- API 与 Worker 可独立运行，但共享领域 Interface 和 Composition Root。
- API、Worker 与 model settings 的 Chat capability 必须由同一 Configured Chat Factory 构造；生产构造固定为 Eino，
  不得改变 Provider/Model/Adapter 身份。
- API 与 Worker 的 Embedding Adapter 必须由同一 Configured Embedder Factory 构造，禁止两套配置转换。
- Eino Chat/Embedding/Provider SDK 类型只允许位于 `internal/platform/models`，Eino Graph 类型只允许位于
  `internal/agent/adapter/eino`；领域层不依赖 HTTP、pgx/sqlc、River、模型 SDK、文件系统实现或具体 Git 命令。
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

- 产品不变量、状态和验收：[`docs/requirements.md`](../../../docs/requirements.md)。
- 领域术语：[`docs/architecture/domain-and-data.md`](../../../docs/architecture/domain-and-data.md)。
- 模块边界：[`docs/architecture/system-design.md`](../../../docs/architecture/system-design.md)。
- API、分页、SSE 和错误：[`docs/architecture/application-contracts.md`](../../../docs/architecture/application-contracts.md)。
- 数据库、事务、索引和备份：[`docs/architecture/domain-and-data.md`](../../../docs/architecture/domain-and-data.md)。
- Workflow、租约、重试和补偿：[`docs/architecture/ai-runtime.md`](../../../docs/architecture/ai-runtime.md)。
- Adapter 与错误分类：[`docs/architecture/system-design.md`](../../../docs/architecture/system-design.md)。
- 安全、日志、Trace、Metrics、审计、性能和测试：[`docs/architecture/quality.md`](../../../docs/architecture/quality.md)。
- 测试与评测：[`docs/architecture/quality.md`](../../../docs/architecture/quality.md)。

## M1 真实实现入口

- API 入口与健康契约：[`cmd/api`](../../../cmd/api)、[`internal/app`](../../../internal/app)、[`api/openapi/openapi.json`](../../../api/openapi/openapi.json)。
- Worker 与数据库边界：[`cmd/worker`](../../../cmd/worker)、[`internal/platform/postgres`](../../../internal/platform/postgres)、[`migrations`](../../../migrations)。
- 配置、日志与部署：[`internal/platform/config`](../../../internal/platform/config)、[`internal/platform/observability`](../../../internal/platform/observability)、[`deploy`](../../../deploy)、[`Makefile`](../../../Makefile)。
- M1 Canonical Gate：`make test`、`make openapi-check`、`make compose-check`、`make compose-up`。
- Eino 历史 PoC 保存在 [`poc/eino`](../../../poc/eino)。当前正式路径以 ADR-0027 为准：Chat、Embedding、
  五个 scheduler 和 `/chat` RAG v2 使用 Eino；River Node 与 PostgreSQL 持久 Workflow 不由 Eino 替代，Checkpoint
  只保留隔离 PoC。旧 direct 实现不进入部署；历史版本由 Git 发布记录恢复。
- M6-02 Agent 门禁：完整 Citation tuple、Knowledge Eligibility/FormalClaimReader、严格 Schema Repair、Model Run/Call
  与 REVIEW 持久化必须按 [`database-guidelines.md`](./database-guidelines.md) 的专项契约验证；Active Index 不等于
  Approved Evidence，Agent 不得直接查询 Knowledge SQL。

## 当前明确待验证项

- License 和 50 万 Chunk ANN/P95 必须在对应后续任务中通过仓库文件和测试锁定；M6-D 的 exact vector
  scan/EXPLAIN 只作为正确性基线。
- 定向合同和组合测试不代替真实 Provider smoke、浏览器端到端、Compose overlay 与稳定发布观察；尚未
  执行的验收不得标记 PASS。2026-08-11 当前树已分别通过外部 HTTPS OpenAI-Compatible Chat/Embedding 与
  原生 Ollama Embedding live gate，以及 host-relay 外部 Chat + 本地 Ollama Embedding 的 metadata、REVIEW 和
  桌面/移动浏览器终态。host-relay 不证明容器直连外部 HTTPS 网络路径；真实稳定观察仍未完成。Checkpoint
  即使 PoC 通过也不得推断为跨进程恢复已采用。
- 本规范不提供伪造的实现代码、版本号、数据库字段长度或不存在的测试结果；M1 完成后应将真实文件链接补入各专题规范。
