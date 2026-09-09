# 2026-08-01 需求优化清单

> 状态同步（2026-08-30）：TODO 1、TODO 3、TODO 5、TODO 6、TODO 7、TODO 8、TODO 9、TODO 12 已收口；TODO 2、TODO 10 进行中，TODO 4 待规划，TODO 11 规划中但延后至 TODO 10 收口后。本文件继续保留原始需求与验收边界，详细实现证据以任务产物和 `docs/roadmap.md` 为准。

> 当前核对（2026-09-09）：TODO 2 的动态工具循环与 TODO 4 均已完成开发、必要数据库及真实桌面/窄屏验证，TODO 4 已归档；TODO 2、4、10 已随本轮统一部署到本机 Docker，Runtime ready，原数据与密钥保留。TODO 11 已评估并正式决定不采用。下列用户诉求与验收标准保留原始边界，任务归档本身不证明满足全部原始需求。
>
> 交付边界：本清单当前没有剩余的已约定开发项，但不代表最终发布验收完成。M11 全产品 E2E、AI Eval 与发布包仍按用户要求暂缓；本机原有模型配置尚未成功激活，Chat / Embedding 保持 disabled。随后要求处理的 OpenAPI 21 个错误、5 个警告已修复，固定原基线为 0 / 0；兼容修复代码尚未重新部署，见 [修复与版本边界](../../../../07-16-product-delivery/research/openapi-compatibility-fix.md)。首次部署证据仍见 [统一交付记录](../../../../07-16-product-delivery/research/final-integration-2026-09-09.md)。

## TODO 概览

> 人天为单名熟悉项目的工程师完成实现与重点验证的初步预估，不含需求澄清、外部协调和发布观察。

- [x] TODO 1. 固定 Docker 页面入口并取消一次性控制凭证（P0，已完成，10-15 人天）：固定 `8080` 页面入口，并让 Workspace 选择与浏览器会话解耦。
- [x] TODO 2. 打通受限 Workspace Agent 的真实用户链路（P1，开发及本机部署完成，20-30 人天）：在保留安全边界和 Proposal 审批的前提下，支持可审计的只读工具循环。
- [x] TODO 3. 全量从 Goose 迁移到 Atlas（P0，已完成，5-8 人天）：统一数据库 Schema 迁移与管理能力，并在 GORM 最终切换前建立唯一 Schema 事实源。
- [x] TODO 4. 自动合成可持续演进的知识笔记（P0，开发及本机部署完成，25-35 人天）：持续整合多篇资料，去重补全、保留冲突和缺口，并附可核验来源。
- [x] TODO 5. 将后端 HTTP 层从 Chi 迁移到 Gin（P0，已完成，15-25 人天）：采用国内使用广泛的 Gin 和 validator，保持现有 HTTP、安全与 SSE 契约兼容。
- [x] TODO 6. 使用 Eino 收敛 AI 通用基础设施（P0，已完成，20-30 人天）：以 Eino/eino-ext 承担模型、Tool Calling、流式输出和 Agent 通用编排，并作为 TODO 2 的基础设施前置。
- [x] TODO 7. 使用 Spectral 和 oasdiff 建立 OpenAPI 契约门禁（P1，已完成，3-5 人天）：用成熟标准工具替换通用手写校验，只保留项目专属断言。
- [x] TODO 8. 生成前端 OpenAPI 客户端并接入 Zod（P1，已完成，8-12 人天）：生成类型和请求代码，逐步删除重复的手写 Transport 与 DTO 解析。
- [x] TODO 9. 使用 Testcontainers-Go 管理数据库集成测试（P0，已完成，5-8 人天）：自动创建、迁移和销毁 PostgreSQL/pgvector 测试环境，并作为 Atlas、GORM 与 River 数据库门禁。
- [x] TODO 10. 将应用数据访问层全面迁移到 GORM（P0，开发及本机部署完成，80-120 人天）：用 GORM 统一 Repository 和事务入口，复杂 PostgreSQL 查询继续通过 GORM Raw/Exec 执行。
- [x] TODO 11. 评估并接入 gin-contrib/sessions（P2，评估完成、不采用，5-8 人天）：候选均未满足强制约束，按原始采用规则保留现有 Session 边界，结论见 ADR-0030。
- [x] TODO 12. 优先使用浏览器原生 EventSource（P2，已完成，3-5 人天）：替换可由 Web 标准覆盖的 SSE 解析与重连代码，保留必要的最小 Fetch 适配器。

依赖顺序：TODO 6 支撑已交付的 TODO 2 动态循环；TODO 7 → TODO 8 已按依赖顺序完成；数据库方向的 TODO 9 → TODO 3 → TODO 10 开发已交付，Atlas 是唯一 Schema 迁移事实源；TODO 5 已完成，TODO 11 已按约束评估并决定不采用。

## TODO 1. 固定 Docker 页面入口并取消一次性控制凭证

- [x] 状态：已收口（2026-08-08）
- 优先级：P0（已完成）
- 预估工期：10-15 人天（仅作已完成事项的规模参考）

### 用户诉求

- 用户启动项目后应始终通过 `http://127.0.0.1:8080` 打开页面，不依赖一次性链接或额外常驻宿主机网页进程。
- 第一次指定一个较大的 Workspace Root 后，后续启动应自动复用；低频切换不能造成 A/B 数据混合或丢失。
- 关闭页面、换浏览器、系统休眠或长时间不操作，不应让 Docker 仍在运行时出现 `ERR_CONNECTION_REFUSED`。

### 产品判断

- 默认只使用一个大 Workspace，子文件夹承担日常分类；切换是低频高级能力。
- 页面可用性与宿主机目录授权应解耦。Docker 固定提供 Web/API，目录授权由本机一次性命令在启动或切换时完成。
- A 与 B 共享同一套数据库基础设施，但所有业务数据按稳定 Workspace ID 隔离；切到 B 完全看不到 A，切回 A 恢复原数据。
- 取消的是旧宿主机控制凭证，不是业务登录、CSRF、API Token 或模型 API Key。

### 安全边界

- 只把用户选择的 canonical Root 精确 bind 给 API/Worker，不挂载父目录，不把 Docker Socket 交给业务容器。
- 切换继续执行 validate、quiesce、revoke、prepare、verify、commit、activate，并在失败时恢复 A 或 fail closed。
- 浏览器只读取 `GET /api/v1/workspaces/active`，不选择宿主机路径、不触发 Docker mutation，也不从 localStorage 恢复 Workspace 身份。

### 验收标准

- 首次 `./zhixu up --workspace <absolute-root>` 后可直接打开固定 `8080`；后续 `up`/`restart` 自动复用。
- `down` 保留 Workspace 选择、数据库和模型密钥；经确认的 `reset` 不删除宿主机文件。
- A -> B -> A 切换期间页面先清理旧 Workspace 缓存，B 看不到 A，切回 A 后原索引和历史可用。
- 仓库不再包含旧宿主机 HTTP 代理、控制会话、一次性 fragment、PID/log 或对应前端入口。

## TODO 2. 打通受限 Workspace Agent 的真实用户链路

- [x] 状态：开发及本机部署完成（2026-09-09；保留固定 v1 历史，v2 动态循环、追加检索/阅读、预算、重放、停止及桌面/窄屏已通过，见 [实际验证](../../../../07-16-product-delivery/research/todo2-v2-compose-verification.md)；本机 AI 准入仍受原有禁用模型配置限制）
- 优先级：P1（下一阶段）
- 预估工期：20-30 人天

### 用户诉求

- 用户应当可以在现有对话入口提交一个包含多个步骤的工作区任务，而不必手动在对话、Dashboard、文档历史和 Proposal 页面之间拼接只读操作。
- Agent 应当能够根据中间结果，在明确的工具白名单和预算内决定下一步只读操作，例如先读取 Git 状态，再检索知识、打开证据并校验引用。
- 用户应当能够看到 Agent 正在调用什么工具、得到什么受控结果，以及最终结论所依据的引用，而不是只看到固定阶段提示和一次性完整回答。
- 当任务涉及正式知识或 Git 写入时，Agent 只能生成结构化 Proposal；用户仍需审阅 Diff 并明确批准，模型不得直接修改文件或创建 Commit。

### 当前问题

- `/chat` 当前执行固定的 Query Plan、Retrieval、Answer 和 Faithfulness 流程；模型提示词明确禁止调用工具或启动 Tool Loop。
- 页面当前展示的是 Workflow 阶段事件，不是模型 Token Streaming，也没有展示工具调用时间线。
- 项目已经具备 Tool Registry、真实只读 Executor、Tool Receipt、权限审计、持久 Workflow、Proposal 和 Safe Writeback，但这些能力尚未接入同一个对话 Agent 循环。
- 当前需要由用户先查看 Dashboard 的 Git 状态，再进入对话查询资料，最后手动进入文档历史和 Proposal 页面完成比较、恢复与审批。

### 目标体验

- 在现有 `/chat` 中提供边界清晰的“工作区分析”模式；普通证据问答继续保留固定 RAG 模式。
- 用户可以提交类似“检查当前 Git 状态，查找发布流程资料并给出带引用的恢复建议，但不要直接修改”的复合任务。
- Agent 可以在同一次执行中调用获准的只读能力，将每次工具结果重新交给模型，再由模型决定继续调用工具或生成最终回答。
- 对话时间线展示工具名称、执行状态、安全摘要、引用和最终结论，并提供真正端到端的 Token Streaming；刷新页面后仍能恢复已持久化的执行事实。
- 需要修改时，Agent 只创建或建议创建 Proposal；后续审批、版本检查、Safe Writeback、Git Commit、Reindex 和回归验证复用现有正式链路。

### 首期能力范围

- 首期只开放当前具备稳定输入边界的只读能力：`ReadGitStatus`、现有受证据约束的 RAG 能力、`ReadSource` 和 `ValidateCitation`。
- `SearchKnowledge`、`CalculateDiff` 等携带正文或自由查询的工具，必须先完成安全 request receipt、输入上限、敏感信息处理和进程内循环契约，再决定是否进入模型工具目录。
- Eino 和 eino-ext 负责通用模型调用、Tool Calling、流式传输和 Agent 编排；通过 Adapter 转换为项目内部请求、结果和错误类型，领域层不得依赖 Eino 类型。
- 每次执行必须设置最大步骤数、单工具超时、总时限、Token/费用预算、并发限制和终止原因，并记录可审计的 Model Call 与 Tool Call 事实。

### 安全边界

- Agent 不获得任意 Shell、任意文件系统、任意网络访问或未经登记的工具能力。
- 模型不能直接调用 `ApplyApprovedPatch`、`CreateGitCommit` 或其他可信 Workflow 专用写工具。
- 工具名称、版本、Workspace、权限、参数、结果大小、幂等键和 Attempt/Fence 必须由服务端校验；模型输出不能充当授权事实。
- 正式知识唯一写入路径继续保持 Proposal → Evidence Validation → Approval → Version Check → Atomic Write → Git Commit → Reindex → Regression Validation。
- Agent 失败、超时、预算耗尽、工具结果漂移或证据不足时必须明确停止并给出可解释状态，不得静默降级为无证据回答或绕过审批。

### 验收标准

- 用户只在对话中提交一次复合任务，即可看到 Agent 至少完成两次有依赖关系的只读工具调用，并使用前一次结果决定后一次操作。
- `ReadGitStatus`、RAG、`ReadSource` 和 `ValidateCitation` 的调用均经过现有权限、契约、Receipt、审计和 Workspace 绑定，工具结果能够安全返回同一 Agent 循环。
- 页面能够区分 Token 增量、工具调用、工具结果、等待状态和最终回答；中断、刷新或 Worker 重投递不会重复产生不可控副作用。
- 达到步骤、时间、Token 或费用上限时，执行可预测地终止，并展示稳定错误码和用户可理解的原因。
- 最终事实性结论只有在证据和引用校验通过后才能发布；证据不足时继续保持澄清或拒答能力。
- 涉及写入的任务最多形成 Proposal，模型无法直接触发文件修改或 Git Commit；批准后的写回继续通过现有可信 Workflow 完成。
- 固定 RAG 模式和 Agent 模式可独立启用、灰度和回滚，Eino 实现细节不进入对外 API、持久领域对象或前端业务类型。

## TODO 3. 全量从 Goose 迁移到 Atlas

- [x] 状态：已收口（2026-08-30；Atlas 已成为唯一 Schema 迁移事实源，Trellis 任务已归档）
- 优先级：P0（数据库迁移前置）
- 预估工期：5-8 人天

### 用户诉求

- 将项目中的 Goose 全量替换为 Atlas，统一数据库 Schema 迁移与管理能力。

### 实施边界

- 迁移前盘点 Goose 的依赖、迁移文件、启动入口、测试、Docker/CI 配置及运维文档，形成可执行迁移方案。
- 保持现有数据库 Schema、迁移顺序和已部署环境的兼容性；迁移过程中不得出现 Goose 与 Atlas 双轨执行同一迁移的情况。
- Atlas 继续作为唯一 Schema 定义、版本迁移和漂移检查入口；TODO 10 引入 GORM 后，生产、测试和命令入口均禁止调用 GORM `AutoMigrate`、`Migrator` 或根据 Model 自动修改 Schema。
- GORM Persistence Model 和字段标签只能映射 Atlas 已批准的表、列、约束和索引，不得反向成为第二份 Schema 定义。
- 完成迁移、回滚策略和验证后，移除 Goose 相关依赖、配置、脚本与文档引用。

### 验收标准

- 新环境和已有数据库均通过 Atlas 完成 Schema 校验与迁移，结果与迁移前预期一致。
- 应用、Docker/CI、测试和运维文档均不再依赖 Goose。
- GORM 启动和 Repository 测试不会隐式创建、修改或删除表、列、约束和索引，Schema 漂移只由 Atlas 门禁报告和处理。
- 迁移失败时能够依据已定义的回滚或恢复流程安全处理，不造成 Schema 版本漂移。

## TODO 4. 自动合成可持续演进的知识笔记

- [x] 状态：开发完成并归档，已部署本机（2026-09-09；自动连续录入、审批/Git、NO_CHANGE/重放、历史来源及桌面/窄屏笔记面试均通过，见 [实际验证](../../../2026-09/09-08-evolving-knowledge-notes/research/synthesis-compose-verification.md)；本机新页面与列表接口正常，模型生成仍受原有禁用配置限制）
- 优先级：P0（高优先级）
- 预估工期：25-35 人天

### 产品定位

- 这是一个主动整理知识的工作台，而不是只根据提问返回片段的普通 RAG：用户持续录入散落文档，系统围绕知识点把多篇资料综合为一份属于用户的合成笔记。
- 合成笔记应可持续演进，并作为后续 AI 面试、复习和知识检验的可靠上下文。

### 用户诉求

- 每录入一篇文档，系统自动分析其知识点，并与已有聚合笔记进行对比。
- 对重复内容进行去重，对已有笔记缺失但新资料能够支持的内容进行补全；已充分覆盖的部分不应被无意义地重复改写。
- 当资料之间存在事实、结论或观点冲突时，保留冲突及各自依据，不得静默选择其一覆盖另一方。
- 识别资料未覆盖或证据不足的知识缺口，并在合成笔记中清楚标注。
- 合成笔记中的事实、观点和冲突应附上可回溯、可核验的来源，用户能够定位到原始文档及相关片段。
- 用户能够基于合成笔记让 AI 进行面试，通过追问检验理解和暴露知识盲区。

### 核心流程

- 文档录入后自动抽取知识点、结论、证据和适用上下文。
- 将新增资料与对应知识点的现有合成笔记比较，生成去重、补全、冲突保留和缺口标注的更新提案。
- 仅将确认缺失或需要显式呈现冲突的内容写入新版本；保持已有有效内容和来源链可追溯。
- 合成笔记更新后，根据其知识点、冲突和缺口生成可控的面试题与追问上下文。

### 验收标准

- 连续录入两篇具有重复、互补和冲突内容的资料后，系统生成一份按知识点组织的合成笔记：重复内容不重复出现，互补内容得到补全，冲突及其来源被并列保留。
- 合成笔记能标出至少一个证据不足或资料未覆盖的知识缺口，并能从每项事实、观点或冲突定位到支持它的原始来源。
- 录入与已有笔记高度重复的第三篇资料时，系统只更新新出现的知识点、证据、冲突或缺口，不重写已充分覆盖的部分。
- 用户可以从合成笔记发起 AI 面试；问题和追问基于笔记内容、已标注的冲突及缺口，并能在回答后指出需要回看的来源。

## TODO 5. 将后端 HTTP 层从 Chi 迁移到 Gin

- [x] 状态：已收口（2026-08-14）
- 优先级：P0（已完成）
- 预估工期：15-25 人天（仅作已完成事项的规模参考）

### 当前问题

- API Router 和多个领域 HTTP Handler 直接依赖 Chi，路由注册、参数读取、Middleware 组合和测试辅助代码均与 Chi 类型绑定。
- 请求严格解码、Problem Details、SSE、鉴权、CSRF、Origin、Capability 和请求关联信息包含大量既有安全语义，不能通过简单替换 Router 完成迁移。
- 项目后续开发更偏向采用国内使用广泛、团队经验更容易复用的成熟框架，因此需要把 HTTP 通用能力收敛到 Gin 生态。

### 实施边界

- 迁移前冻结全部路由、方法、状态码、Header、Problem Details、严格 JSON、分页、下载、SSE 和 Middleware 顺序，形成可重复的行为基线。
- Gin 只进入 HTTP/Composition 边界，领域实体、Application Service、Repository、Workflow 和持久化对象不得依赖 Gin 类型。
- 使用 `go-playground/validator` 处理适合声明式表达的通用请求校验；未知字段拒绝、跨字段安全规则和领域校验继续由项目代码负责。
- 按路由组分批迁移并保持单一生产入口；完成行为对等验证后删除 Chi 依赖和兼容代码，不长期维护两套路由体系。

### 验收标准

- 全部既有 API、健康检查、`/metrics`、文件上传/下载和 SSE 契约在 Gin 上通过回归测试，对外 OpenAPI 契约不发生未批准变化。
- Auth、Session、CSRF、Origin、API Token、Capability、Workspace 绑定和写授权测试保持通过，严格 JSON 不因 Gin 默认绑定行为而变弱。
- 生产代码、测试辅助和 `go.mod` 不再依赖 Chi；Gin 类型没有进入领域层和持久化契约。
- API 构建、Go test/race/vet、OpenAPI、Compose 和关键浏览器链路通过，迁移过程具有按路由组回退的明确路径。

### 可直接执行的任务描述

> 将当前后端 HTTP 层从 Chi 迁移到 Gin，并使用 go-playground/validator 处理通用请求校验，保持全部路由、Middleware、SSE、Problem 错误格式、严格 JSON 和鉴权行为兼容，补齐回归测试后删除 Chi 依赖。

## TODO 6. 使用 Eino 收敛 AI 通用基础设施

- [x] 状态：已收口（2026-08-12；真实稳定观察独立进行）
- 优先级：P0（已完成）
- 预估工期：20-30 人天（仅作已完成事项的规模参考）
- 发布说明：生产替换、真实 Provider/浏览器闭环和旧 direct 删除已经完成；连续 7 个自然日、至少 100 个终态的稳定观察仍是独立发布质量证据，不属于本清单开头定义的实现工期或完成判定。

### 当前问题

- 当前模型调用、Embedding、结构化输出修复、Tool Calling、流式输出和 Agent 通用编排主要由项目代码分别实现，升级协议和扩展 Provider 的维护成本较高。
- TODO 2 需要真正的多步 Tool Loop 和 Token Streaming；如果继续扩展现有通用实现，会进一步重复成熟 AI 框架已经提供的能力。
- 领域 Workflow、权限、审批、证据链、Receipt、幂等和 PostgreSQL/River 恢复语义是项目核心差异，不能为了采用框架而交给 Eino 管理。

### 实施边界

- 先用行为测试冻结 Chat、Embedding、Tool Calling、结构化输出、流式事件、超时、重试和错误分类，再按能力逐项迁移到 Eino/eino-ext。
- Eino 仅位于 AI Adapter 和 Composition Root；通过项目接口转换请求、结果、流事件和稳定错误，领域层、对外 API 与数据库不得保存 Eino 类型。
- 保留项目自有 Workflow、权限、审批、证据校验、审计、Model Run/Call、Receipt、Fence 和 PostgreSQL/River 状态机。
- TODO 6 只收敛通用 AI 基础设施；TODO 2 继续拥有 Workspace Agent 的产品体验、工具白名单、预算、时间线和写入 Proposal 边界。
- 实施前同步更新当前“不正式采用 Eino 主模块”的旧 PoC/规范结论，记录新选型、迁移范围、版本锁定、回滚和退出路径。

### 验收标准

- 生产模型、Embedding、Tool Calling、流式输出和 Agent 通用执行路径通过 Eino/eino-ext Adapter，不再保留功能重复的自研协议与编排实现。
- Eino 类型只出现在 Adapter/Composition 允许范围，现有领域接口、持久 Workflow、审计和 OpenAPI 不需要理解 Eino。
- 模型禁用、Provider 故障、Schema 修复、流中断、工具超时、预算耗尽和 Worker 重投递保持稳定、可解释且可恢复。
- 现有 AI 单元/集成测试、Agent Eval、Secret 扫描、race/vet 和 TODO 2 的端到端基线通过；旧 direct 实现已按 2026-08-11 决策删除，需要恢复时使用 Git 发布记录，不在生产部署中保留第二套 Runtime。

### 可直接执行的任务描述

> 使用 Eino 和 eino-ext 替换当前自研的模型调用、Embedding、Tool Calling、流式输出和 Agent 通用编排，保留项目领域 Workflow、权限、审批、证据链及 PostgreSQL/River 持久化，并通过 Adapter 隔离 Eino 类型。

## TODO 7. 使用 Spectral 和 oasdiff 建立 OpenAPI 契约门禁

- [x] 状态：已收口（2026-08-18）
- 优先级：P1（已完成）
- 预估工期：3-5 人天（仅作已完成事项的规模参考）
- 发布说明：Spectral `6.16.3` 与 oasdiff `v1.29.1` 已锁定并接入 Makefile/CI；项目代码保留标准工具无法表达的专属契约断言，Breaking Change 基线与漂移门禁均可复现执行。

### 当前问题

- 当前 OpenAPI 检查包含项目手写的通用规范校验和契约断言，标准规则、破坏性变更判断与项目专属约束没有清晰分层。
- 手写通用校验需要持续跟进 OpenAPI 规范细节，也不便于在 CI 中稳定识别相对基线的 Breaking Change。

### 实施边界

- 锁定 Spectral 和 oasdiff 版本、规则集、基线来源和升级方式，禁止 CI 随环境自动漂移到新版本。
- Spectral 负责通用 OpenAPI lint，oasdiff 负责相对批准基线的破坏性变更检测；项目代码只保留标准工具无法表达的业务契约断言。
- 明确 API 基线更新审批流程，不通过覆盖快照或静默接受差异绕过 Breaking Change 门禁。
- 将 lint、Breaking Change 和后续生成代码漂移检查接入 Makefile 与 CI，并保留本地可复现命令。

### 验收标准

- 已知非法 OpenAPI、破坏性变更和项目专属契约错误均能被对应门禁稳定拒绝，并输出可定位的失败信息。
- 通用手写规则被删除，保留规则均有“为什么标准工具不能覆盖”的说明和回归测试。
- 工具版本、配置、基线和 CI/Makefile 入口进入仓库，开发机和 CI 对同一输入得到一致结果。
- 合法的兼容性变更能够通过，不要求开发者为无关格式差异手工更新大量快照。

### 可直接执行的任务描述

> 使用锁定版本的 Spectral 和 oasdiff 替换当前手写 OpenAPI 通用校验及破坏性变更检查，只保留标准工具无法表达的项目专属契约断言，并把校验、Breaking Change 和生成代码漂移检查接入 Makefile 与 CI。

## TODO 8. 生成前端 OpenAPI 客户端并接入 Zod

- [x] 状态：已收口（2026-08-19）
- 优先级：P1（已完成）
- 预估工期：8-12 人天（仅作已完成事项的规模参考）
- 发布说明：189 个 operation 已按 26 个稳定领域 tag 生成并完成生产接入；普通 JSON 使用对应 `*ApiRaw`，关键不可信响应保留 Zod/strict owner，SSE、multipart 和 Blob 仅保留必要 Adapter。生成漂移、前端 lint/typecheck/test/build 与 Chromium smoke 均已通过。

### 当前问题

- 前端多个功能模块分别维护请求 Transport、DTO 类型和 `unknown` 响应解析，容易在 API 字段演进时产生类型与运行时行为漂移。
- TypeScript 编译期类型不能证明网络响应可信；完全依赖生成类型或在每个页面重复手写校验都无法形成稳定边界。

### 实施边界

- 在 TODO 7 稳定 OpenAPI 门禁后，锁定 OpenAPI Generator 版本和 `typescript-fetch` 模板，明确生成目录、再生成命令和禁止手工修改规则。
- 在生成客户端外保留一个项目 Transport Adapter，统一接入 Cookie/API Token、CSRF、Workspace、请求 ID、错误映射和 TanStack Query。
- Zod 只用于关键不可信响应、持久缓存恢复和高风险联合类型边界；普通已受 OpenAPI 契约保护的数据不重复维护整套平行 Schema。
- 按模块逐步迁移，在行为对等测试通过后删除对应手写 DTO/Transport/Decoder，不一次性替换全部前端调用。

### 验收标准

- OpenAPI 生成结果可由单一命令确定性重建，CI 能发现规格与生成代码漂移，生成目录不包含人工业务逻辑。
- 认证、CSRF、Workspace、Problem Details、取消、超时、文件上传/下载和 SSE 之外的普通 API 调用保持兼容。
- 关键不可信响应在进入 Store/组件前经过 Zod fail-closed 校验，错误映射为稳定前端状态且不泄露响应正文。
- 已迁移模块不再保留重复的手写 Transport、DTO 和字段解析，前端 lint、typecheck、test、build 和关键浏览器流程通过。

### 可直接执行的任务描述

> 使用锁定版本的 OpenAPI Generator `typescript-fetch` 从现有 OpenAPI 生成前端类型和请求客户端，接入当前认证、CSRF 和 TanStack Query，在关键不可信响应边界使用 Zod 校验，并在行为对等测试通过后逐步删除重复的手写 Transport 和 DTO 解析代码。

## TODO 9. 使用 Testcontainers-Go 管理数据库集成测试

- [x] 状态：已收口（2026-08-26；统一 Testcontainers-Go 工厂已交付并归档）
- 优先级：P0（GORM 迁移门禁）
- 预估工期：5-8 人天

### 当前问题

- PostgreSQL/pgvector 集成测试主要依赖开发者预先准备数据库和外部 DSN，初始化、迁移、隔离与清理步骤容易因本机状态不同而漂移。
- 缺少容器运行时或 DSN 时，部分数据库行为只能由单元测试覆盖，真实约束、事务、迁移和 SQL 兼容问题发现较晚。
- TODO 10 将全面改造 Repository 和事务入口；如果没有自动化真实数据库基线，无法证明 GORM 迁移没有破坏锁、隔离级别、幂等、约束和 PostgreSQL 专属查询。

### 实施边界

- 使用 Testcontainers-Go 启动与生产兼容的 PostgreSQL/pgvector 镜像，等待真实健康状态后通过仓库当前正式迁移入口执行迁移，再向测试暴露隔离 DSN；TODO 3 完成后，该入口固定为 Atlas。
- 封装单一测试环境工厂，负责容器创建、数据库命名、正式迁移、并行隔离、日志摘要和销毁，并从同一 DSN 构造 GORM 数据访问入口与 River/底层能力需要的 pgx 入口；业务测试不得各自复制容器管理代码。
- 保留显式外部 DSN 入口，用于性能、故障注入、远程 CI 和人工排障；自动容器与外部 DSN 不得同时竞争同一数据库。
- Docker 不可用时必须明确跳过仅由开发者选择的集成测试或给出可操作错误，不得伪装为数据库测试已通过。

### 验收标准

- 一条受支持命令可以从无数据库状态自动创建容器、执行仓库正式迁移（TODO 3 完成后由 Atlas 负责）、运行代表性 GORM、pgvector、River、事务与约束测试并销毁资源。
- 串行和允许并行的测试之间不存在 Schema、数据、端口或容器名称污染，失败后也能回收临时资源。
- 外部 DSN 模式继续可用，并由测试证明与 Testcontainers 模式执行相同迁移和核心断言。
- CI、本地文档和 Makefile 入口一致，容器日志和错误信息不包含数据库密码或完整 DSN。

### 可直接执行的任务描述

> 使用 Testcontainers-Go 替换当前手动管理的 PostgreSQL/pgvector 测试环境，让集成测试自动创建隔离容器、通过仓库正式迁移入口执行迁移（TODO 3 完成后固定为 Atlas），并从同一 DSN 验证 GORM Repository、pgvector、River 和事务约束，测试结束后销毁资源，同时保留外部 DSN 作为高级测试、性能测试和排障入口。

## TODO 10. 将应用数据访问层全面迁移到 GORM

- [x] 状态：开发及本机部署完成（2026-09-08 全部 30 个子任务及必要验证完成并归档；2026-09-09 随本轮升级至 Atlas 00099，原数据保留）
- 优先级：P0（高优先级、高风险，必须分阶段）
- 预估工期：80-120 人天

### 当前问题

- 当前应用数据访问以 pgx 和参数化手写 SQL 为主。仓库约有 144 个生产 Go 文件包含 SQL 语句或查询片段，另有约 48 个 Repository/Store/PostgreSQL 实现文件；pgx 还进入大量事务辅助、Composition 和测试代码，维护与迁移成本已经跨越多个领域模块。
- Repository 普遍重复处理建模映射、CRUD、事务启动、错误转换和结果扫描；团队希望统一采用国内 Go 项目认知度较高的 GORM，降低常规数据访问的维护门槛。
- 项目大量使用逻辑 Schema、CTE、`FOR UPDATE`、`SKIP LOCKED`、advisory lock、pgvector、批量写入、严格隔离级别和跨 Repository 原子事务，不能把“全面使用 GORM”误解为只改 Struct Tag 或只迁移简单 CRUD。
- GORM PostgreSQL Driver 本身基于 pgx，River 也依赖 `riverpgxv5`；因此目标是消除应用 Repository 对 pgx 的直接耦合，而不是强行从依赖树删除 pgx。

### 实施边界

- 全部应用 PostgreSQL Adapter 和 Repository 分批迁移到统一的 GORM Composition/transaction boundary；领域和 Application Interface 继续使用项目类型，不暴露 `*gorm.DB`、GORM Model、Clause 或错误类型。
- Persistence Model 与领域实体分离，显式映射现有逻辑 Schema、表名、列名和可空类型；禁止直接嵌入 `gorm.Model`、隐式复数表名、隐式软删除、自动时间戳、关联级联写入或 Hook 副作用改变既有数据语义。
- 常规 CRUD、条件查询、分页和批量操作使用 GORM API；复杂 CTE、窗口函数、图查询、pgvector、`FOR UPDATE SKIP LOCKED`、advisory lock 和性能敏感 SQL 继续通过 GORM `Raw`、`Exec`、`Clauses` 或受控 Plugin 执行，但调用入口仍由 GORM Repository 统一管理。
- 定义项目自有 Unit of Work/transaction port，在 GORM Transaction 上保持现有隔离级别、只读事务、Savepoint、跨 Repository 原子提交、Outbox 和 response-loss replay；删除 Application/Domain 及普通 Repository 对 `pgx.Tx`、`pgxpool.Pool` 和 `any` transaction cast 的依赖。
- pgx 仅允许保留在 GORM PostgreSQL Driver、River/`riverpgxv5`、Atlas/迁移接入、连接级 advisory lock、COPY/类型注册等经验证无法由 GORM 完整覆盖的底层 allowlist；每个例外必须记录原因、接口和测试，业务模块不得直接取得 pgx Pool/Tx。
- Atlas 是唯一 Schema 迁移事实源，所有生产和测试入口禁止 GORM `AutoMigrate`/`Migrator` 修改 Schema；GORM Model 只能映射 Atlas 已批准结构。
- 显式配置 GORM Logger、NamingStrategy、PrepareStmt、SkipDefaultTransaction、NowFunc、连接池和错误翻译策略，禁止依赖可能改变 SQL、事务、时间或日志脱敏行为的框架默认值。
- 按“只读简单查询 → 单聚合写入 → 批量/分页 → 跨 Repository 事务 → Workflow/Graph/pgvector/锁”分阶段迁移；每一阶段行为对等后删除旧实现，不长期保留双写或两套生产 Repository。

### 验收标准

- 所有应用 PostgreSQL Repository 生产入口均通过 GORM，普通领域/应用/Repository 代码不再直接导入 pgx；残留 pgx 只存在于批准的 Driver、River、迁移和底层 allowlist，并由静态门禁检查。
- 现有 Schema、显式列、Workspace/权限范围、稳定分页、幂等、乐观锁、唯一约束、错误码和响应语义保持不变；GORM 不创建或修改任何生产 Schema。
- 跨 Repository 事务、隔离级别、行锁、advisory lock、Outbox、Workflow Claim/Heartbeat/Complete、response-loss replay 和并发冲突通过真实 PostgreSQL 测试及 race 验证。
- pgvector、FTS、复杂 CTE、Graph、Collection、Health、Workflow 和 River 查询保持正确并通过关键 `EXPLAIN`/容量门禁，不产生 N+1、隐式预加载、无界查询或逐条写入退化。
- GORM SQL/参数/慢查询日志经过项目统一脱敏，禁止输出 Credential、正文、完整 DSN、绝对路径或高敏业务参数。
- 每个迁移批次都有旧实现行为基线、Testcontainers 回归、性能对比和模块级回滚点；最终切换后删除重复 pgx Repository、过渡 Adapter 和失效测试辅助。

### 可直接执行的任务描述

> 将当前应用数据访问层从直接 pgx 和手写 Repository 全面迁移到 GORM，所有应用 Repository 与事务统一通过 GORM 边界执行，复杂 CTE、图查询、pgvector、锁和性能敏感 SQL 使用 GORM Raw/Exec/Clauses 保持原语义，Atlas 继续作为唯一 Schema 迁移工具并禁止 AutoMigrate，pgx 只保留给 GORM PostgreSQL Driver、River、迁移和经批准的底层能力，同时通过 Testcontainers、事务并发、EXPLAIN 和行为对等测试分阶段删除旧实现。

## TODO 11. 评估并接入 gin-contrib/sessions

- [x] 状态：评估完成、不采用（2026-09-08；候选不满足强制约束，结论见 ADR-0030）
- 优先级：P2（低优先级）
- 预估工期：5-8 人天

### 当前问题

- 当前 Session 传输、Cookie 和安全状态包含项目自有实现；其中部分属于框架通用能力，部分则依赖 PostgreSQL 权威状态、撤销和写授权等项目不变量。
- 在 Gin 和 GORM 迁移前直接引入 `gin-contrib/sessions` 会形成 Chi/Gin、pgx/GORM 双轨和新的全局状态，因此只能在 TODO 5、TODO 10 收口后评估。

### 实施边界

- 按成熟框架 80% 覆盖规则逐项比较 Cookie 属性、Session ID 传输、Store 生命周期、轮换、撤销、CSRF、Origin、API Token 和 Capability 需求。
- 只把 `gin-contrib/sessions` 用于能够完整覆盖且不会削弱安全语义的通用传输机制；PostgreSQL 继续保存权威 Session、撤销、版本和用户绑定状态。
- 权威 PostgreSQL Session Repository 必须复用 TODO 10 的统一 GORM transaction boundary，不为 `gin-contrib/sessions` 新建第二套连接池、迁移或 Store 事实源。
- 不把 CSRF、Origin、API Token、Capability、Workspace 数据权限或写授权合并进 Session 框架，继续保持独立后端门禁。
- 如果强制安全项不满足或加权覆盖不足 80%，记录未采用理由并保留最小现有实现，不为了“用了框架”强行迁移。

### 验收标准

- 形成可复核的需求覆盖矩阵，明确框架接管项、项目保留项、未采用能力和退出路径。
- 接入时保持 Session 创建、读取、轮换、撤销、过期、并发登录、Cookie 属性和错误响应兼容，不接受客户端伪造权威状态。
- Auth、CSRF、Origin、API Token、Capability、写授权和真实 PostgreSQL 安全测试全部通过，日志与错误不泄露 Cookie 或 Session ID。
- 若完成替换，删除被框架覆盖的重复代码；若不替换，则用 ADR 记录证据而不是继续维护无说明的自研例外。

### 可直接执行的任务描述

> 对现有 Session 传输层执行成熟框架覆盖评估，仅使用 gin-contrib/sessions 替换其能够完整覆盖的 Cookie 和 Session 通用机制，保留 PostgreSQL 权威状态、撤销、轮换、CSRF、Origin、API Token、Capability 和写授权规则，权威 Session Repository 复用统一 GORM 事务边界，并以现有安全契约测试作为迁移门禁。

## TODO 12. 优先使用浏览器原生 EventSource

- [x] 状态：已收口（2026-08-10）
- 优先级：P2（已完成）
- 预估工期：3-5 人天（仅作已完成事项的规模参考）

### 当前问题

- 前端当前使用 Fetch `ReadableStream` 手工处理 SSE 帧、事件字段、游标和重连，协议解析与浏览器已经提供的 EventSource 能力存在重复。
- EventSource 不支持任意自定义 Header 和请求方法，现有认证、Workspace 绑定与恢复语义必须先验证，不能直接删除 Fetch 路径。

### 实施边界

- 盘点 `/api/v1/events` 的认证方式、Cookie、请求方法、Header、Last-Event-ID、Workspace 绑定、游标过期、错误响应和代理行为，建立真实浏览器基线。
- 默认使用浏览器原生 EventSource 承担 SSE 帧解析、连接状态和标准重连；项目代码继续负责严格事件解码、状态归约、鉴权失效和游标恢复策略。
- 如果自定义 Authorization、CSRF、Workspace Header 或非 GET 请求属于不可移除的强制契约，则保留一个最小 Fetch SSE Adapter，仅实现标准 EventSource 无法覆盖的差异。
- EventSource 与 Fetch Adapter 必须输出同一项目事件类型和连接状态，不允许页面组件感知两套底层协议实现。

### 验收标准

- Cookie/GET 可覆盖的生产场景使用原生 EventSource，不再自行解析底层 SSE 行、分隔符和多行 `data` 规则。
- Last-Event-ID、Workspace 切换、鉴权失效、游标过期、服务端断开、网络恢复和退避重连行为与现有契约一致。
- 必须保留 Fetch 时，其范围由自动化测试证明为标准 API 的真实缺口，且不复制 EventSource 已覆盖的通用状态机。
- 单元测试、前端构建和真实浏览器 smoke 覆盖正常事件、断线恢复、重复事件、非法事件和 Workspace A/B 隔离。

### 可直接执行的任务描述

> 使用浏览器原生 EventSource 替换当前低层 SSE 帧解析代码，保留 Last-Event-ID、Workspace 绑定、鉴权失效、游标过期恢复、退避重连和事件严格校验；若自定义 Header 或请求方式无法满足，再保留最小 Fetch 兼容适配器。
