# 2026-08-01 需求优化清单

## TODO 1. 固定 Docker 页面入口并取消一次性控制凭证

- [x] 状态：已收口（2026-08-08）

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

- [ ] 状态：待规划（2026-08-08 记录，当前不实施）

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
