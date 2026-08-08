# Stage 2 门禁与 Stage 3/4 二次决策

日期：2026-08-06

## Stage 2 Callback/Trace 门禁

结论：PASS，正式采用范围可从 Chat Adapter 扩展到调用级 Callback/Trace 旁路。

- 每次 `Chat` 通过 Context 的 `callbacks.InitCallbacks` 注入独立 handler；没有进程级可变 handler。
- Handler 不读取 Eino input/output/raw error，只把固定 component、Model Call phase、result、稳定 error code、耗时和现有 correlation 写入项目 Trace/Metrics。
- `RecordingChatModel`、Model Run/Call、Audit、Workflow Progress 和 PostgreSQL 事实均未移动到 callback。
- Telemetry 关闭、返回错误或 panic 不改变 Chat response/error；单个 Span 方法 panic 后仍尝试结束 Span。
- 受影响包测试、Callback 压力测试、race、vet、vendor 构建、`go mod tidy -diff`、Compose 合同、PoC race/vet 和 `git diff --check` 均通过。
- Stage 2 当时没有真实外部 exporter 和 OpenAI-Compatible Provider 凭据；对应 smoke 当时记录为 SKIP。Provider Chat 门禁已在 2026-08-07 补充关闭，外部 exporter 仍未宣称通过。

## Stage 3 Structured Output 短 Graph

结论：GO，但范围严格限制为 `StructuredRunner` 内部三阶段调度器。

依据：

- 关系评估、RAG、Artifact、Capture Profile、Organizing 五个生产消费者均复用同一 `INITIAL -> REPAIR -> REDUCED` 分支序列，满足“至少两个流程需要相同编排”的进入门槛。
- Graph 只负责调用/校验节点与提前结束分支；严格 decoder、原始响应字节闭包、Schema snapshot、token/byte/time budget、稳定错误和最多三次模型调用继续由项目 Application 拥有。
- 所有模型调用仍进入注入的项目 `ChatModel`，因此 `RecordingChatModel` 与 Model Run/Call 审计边界不变。
- Eino Graph 不使用 checkpoint、自动 retry、持久 state、Tool 或外部副作用。PostgreSQL/River 仍是持久工作流唯一事实源。
- 保留 direct scheduler，并按固定 consumer ID 独立配置；先验证 RAG，再依次验证其他四个消费者。

实施门禁：PASS。

- `StructuredPhaseScheduler` Port 与私有 `StructuredPhaseRun` state handle 已落地；Application 不导入 Eino，Eino 类型只存在于 `internal/platform/models` 与 `internal/agent/adapter/eino`。
- Worker 为 Relation、RAG Answer、Artifact、Capture Profile、Organizing 分别提供独立 `direct|eino` selector，默认均为 direct。RAG selector 不改变 Query Plan 与 Faithfulness Review 的项目编排。
- 五个消费者测试均以计数包装器委托真实 Eino scheduler，并断言 `Schedule` 恰好执行一次；Artifact 与 Capture 还覆盖 `INITIAL -> REPAIR`，防止仅验证依赖字段而未实际进入 Graph。
- direct/Eino 合同覆盖 INITIAL、REPAIR、REDUCED、exhaustion、Provider/取消/deadline、请求/响应/token budget、Graph 内部错误、32 路并发复用及 Model Call persistence unknown；Eino scheduler 20 轮 race 压力通过。
- 真实 PostgreSQL 18 + pgvector 集成在 direct/Eino 两个子用例中贯穿公共 HTTP、River、Retrieval、Knowledge、RecordingChatModel 与 Answer finalizer。完成后模拟同一 Node 的下一 River transport attempt，Claim 返回 stale，Provider 调用仍为 3，Model Call 仍为 `PLAN,INITIAL,REVIEW` 且全部成功，Workflow/Node/Attempt 与 Answer/Knowledge 终态不漂移。
- 真实集成首次暴露旧 fixture 使用已不合法的 Workspace 状态 `test`；按当前 `workspace_status_lifecycle` 约束修正为 `active` 后，两种 scheduler 均通过，说明失败发生在迁移 fixture 而非 Eino 路径。

最终验证包括受影响包 `go test -race`、Eino scheduler `-race -count=20`、相关 `go vet`、vendor API/Worker 构建、`go mod tidy -diff`、`make compose-check`、PoC race/vet、Python 合同编译和 `git diff --check`。阶段收口时 production Eino live smoke 尚为 SKIP；2026-08-07 已补充真实 Ollama Provider 门禁，direct 仍按灰度策略保持默认并保留全部回退路径。

最终复核还通过了 `make openapi-check`、integration tag 编译和 `make agent-eval`；离线 fixture 的五类关系 precision/recall/F1、citation precision/coverage、faithfulness、conflict disclosure 与 appropriate refusal 均为 `1`。独立代码 Review 重新检查五个计数包装器、Worker Composition Root 和 PostgreSQL/River 重投断言，未发现 P0/P1/P2 问题。后续 live smoke 使用真实模型执行，未复用 Fake 或把 SKIP 包装成通过。

真实容器门禁也以 `ZHIXU_CHAT_IMPLEMENTATION=eino ZHIXU_STRUCTURED_SCHEDULER_RAG=eino make compose-rag-smoke` 通过。该 smoke 不恢复 legacy `/workspace` 或父目录 bind，而是通过真实 Host Controller Coordinator/ComposeDriver 完成 candidate probe、`source == target` exact-root mount、Grant generation 和 API/Worker runtime registration；随后只用公开业务 API 完成 Scan/Ingestion/Approval/Reindex、Conversation/River/RAG、Citation、SSE、Feedback 与 exact replay。确定性模型 fixture 使用独立镜像 stage，不再把仓库源码 bind 给 sidecar。

该门禁在修复过程中暴露并锁定了 Host Controller 改造后的 smoke 漂移：运行时 `config/build` 必须显式启用 `workspace-runtime` profile；共享模型 network namespace 的 Worker 必须在测试 overlay 清空 `extra_hosts`；canary 必须覆盖唯一 ingress published port；Workspace-scoped 请求必须携带 `X-Workspace-ID`；trap 必须先按固定 runtime service 集 stop/rm，再执行 project 级 volume/image 清理。最终无调试模式重复通过，且无 project 容器、volume、network 或镜像残留。

## Live Provider 补充门禁（2026-08-07）

结论：PASS，但只关闭 Chat 协议兼容门禁，不代表真实模型业务质量或生产灰度完成。

- 临时 Ollama `0.32.6` 仅绑定 `127.0.0.1:11434`，生产 `EinoOpenAIChatModel` 通过 `/v1/chat/completions` 调用真实 `qwen3:0.6b`；同一 live test 连续通过两次。
- 首次失败有两个独立原因：通用 live fixture 的 256 token 上限被 Qwen3 thinking 消耗，导致空 content/`finish_reason=length`；提高到 512 后，项目严格 decoder 又正确暴露了 Provider 的已知 `message.reasoning` 扩展未在白名单内。
- 最终修复没有关闭 `DisallowUnknownFields`：只把 `reasoning` 与 `reasoning_content` 定义为可选 string/null，严格解码后立即丢弃，不进入 `ChatResponse`、Model Run/Call、日志、Audit 或 telemetry；未知 message 字段和错误 reasoning 类型仍有 direct/Eino 回归测试。
- `qwen2.5:0.5b` 的无 reasoning 响应也曾连续通过两次，用于区分“基础 OpenAI-Compatible 路径正常”和“reasoning 映射缺口”；最终准入证据仍以修复后 qwen3 路径为准。
- `direct` 保持默认是发布策略而非未完成门禁：后续先显式灰度 `eino` 并保留一键切回，不删除 direct 或五个独立 scheduler selector。

## Stage 4 只读 Tool Calling

结论：NO-GO，本任务不实现；另立任务补齐可信生产入口后再评估。

依据：

- 当前生产只读工具只能从持久 Worker Tool Node 的 `workflow.ExecutionContext` 构造 `TrustedExecutionIdentity`；普通 Chat/RAG 没有合法的 Attempt、lease owner/fence 和确定性 `call_no` 来源。
- 现有 Eino Chat Adapter 的合同明确拒绝响应中的 `ToolCalls`，也没有项目自有的 Tool Calling 模型 Port。直接复用会破坏已冻结的 Chat 合同。
- 安全接入至少需要新的持久 Workflow Tool-loop Node/Port、独立严格 Tool Calling model Adapter，以及顺序 ToolsNode/确定性调用编号。这是独立产品与持久化行为，不是一个可安全落地的薄 Adapter。
- 当前直接新增未被生产路径使用的 Eino Tool bridge 既不能验证权限链，也会扩大框架表面，因此不满足“有明确消费者和净收益”的采用门槛。
- 未来任务仍必须只暴露已冻结且有生产 Executor 的只读工具，并继续经过项目 Registry、Policy、Capability、lease/fence、幂等、receipt 和 `UNKNOWN/manual_recovery`；写工具只走 Proposal/Approval/Safe Writeback。
- 静态防漂移检查确认 `git diff -- internal/tools` 为空，生产 Eino import 只存在于 `internal/platform/models` 与 `internal/agent/adapter/eino`；任务设计和所有权矩阵均明确标记 Stage 4 No-Go。

## 回滚结论

- Chat/Callback：保持 `ZHIXU_CHAT_IMPLEMENTATION=direct` 为默认；切回 direct 不改变数据库或工作流状态。
- Structured scheduler：每个 consumer 可单独切回 direct。
- Tool Calling：当前未注册 Eino ToolsNode，无新增运行时回滚动作。
