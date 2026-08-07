# Eino 分层迁移执行计划

## 开发环境

- 基线与 PR 目标：`dev`。
- 开发分支：`codex/eino-layered-migration`。
- 独立 worktree：`/Users/zhenglizhi/GolandProjects/zhixu-eino-layered-migration`。
- 用户已明确要求继续开发至任务完成，任务状态为 `in_progress`；阶段 2-5 按既定门禁和决策点持续推进，未通过门禁的可选能力不得强行落地或宣称完成。

## 阶段 0：基线与合同冻结（2-3 人日）

- [x] 新增分层采用 ADR，明确 supersede ADR-0013 的“不正式采用”结论，并保留其中 Domain/Workflow/Permission/Writeback 边界。
- [x] 固定当前 direct Adapter 的 request/response fixture、错误分类、usage、模型回显、Schema 三阶段和并发行为。
- [x] 复核 PoC 中已验证的 Chat Graph、Callback、ToolsNode 样例；生产 contract tests 独立复用项目 fixture，不把 PoC contract 引入 Domain。
- [x] 确认目标 Eino core/OpenAI extension 版本、许可证、间接依赖和 `vendor` 体积；Embedding extension 暂不锁入主模块。
- [x] 为 `agentapplication.ChatModel` 增加实现无关的 contract tests，保证 direct/Eino 两种 Adapter 结果可比较。

门禁状态：现有相关主模块测试、`go vet` 和 PoC race/vet 基线已通过；默认仍为 `direct`，未改变既有默认行为。

## 阶段 1：Eino Chat Adapter（6-9 人日）

- [x] 在 `internal/platform/models` 增加 Eino-backed Chat Adapter，复用现有 model transport、安全解析和 Contract 结构。
- [x] 实现项目消息 ↔ `schema.Message` 映射、动态 JSON Schema payload modifier、response metadata/model/usage 校验和错误分类。
- [x] 更新 `NewConfiguredChatModel`/ModelRuntime 的受控选择；保留 direct Adapter 回退路径。
- [x] 增加 httptest 覆盖 `/v1/chat/completions` 请求体、严格 Schema、响应大小、缺 usage、model mismatch、429/5xx/401、取消、deadline、重定向和并发 Schema 隔离。
- [x] API、Worker 与 model settings connection test 继续走同一 factory/runtime；Credential 只保留在认证请求所需的私有 Adapter/SDK client，不进入 Runtime contract、日志、错误或格式化输出。
- [x] 更新根 `go.mod`/`go.sum`/`vendor` 并记录版本与许可证；现有 CI/Docker 已消费根 `go.sum` 与 `vendor`，无需新增构建路径。

门禁状态：direct/Eino 离线 fixture、相关 Go race/vet、vendor 构建与 Compose 合同已通过，失败可切回 direct；真实 OpenAI-Compatible Provider smoke 因当前无凭据仍为待办，因此不默认启用 `eino`。

## 阶段 2：Callback/Trace 适配（3-5 人日）

- [x] 将 Eino callback 的 start/end/error 映射到现有 correlation/telemetry context。
- [x] 复用现有 Redactor，禁止记录 API Key、Authorization、Cookie、Prompt 正文和响应全文。
- [x] 明确 callback 只是观测旁路；Model Run/Call、Audit 和 Workflow Progress 继续由项目事务写入。
- [x] 增加并发、异常、取消和敏感字段回归测试。

门禁状态：受影响包测试与 `go vet`、Callback 20 轮压力测试、模型/可观测性/runtime `-race`、
全局 handler 与 Eino 类型边界静态扫描均通过；Trace/Metrics 只含稳定摘要，telemetry error/panic 不改变 Chat 结果。
当前没有外部 exporter 和真实 Provider 凭据，未把对应 smoke 写成通过，`direct` 继续为默认。

阶段 2 后二次决策：只有至少两个现有/近期流程需要相同分支编排，或对照结果证明 Graph 在测试、可观测性或维护成本上有净收益，才进入阶段 3；否则正式采用范围停在 Chat + Callback。

## 阶段 3：Structured Output 短 Graph（8-12 人日，可选）

二次决策：GO。关系评估、RAG、Artifact、Capture、Organizing 五个生产消费者复用同一三阶段调度，满足复用门槛；实现范围只限 `StructuredRunner` 内部调度器，详见 `research/stage2-gate-and-stage3-stage4-decisions.md`。

- [x] 保留 `StructuredRunner` Facade 和现有 direct 实现；新增项目自有 `StructuredPhaseScheduler` Port，让 Eino Graph Adapter 实现该 Port，Application 不导入 Eino 类型。
- [x] 在 Composition Root 按固定 consumer ID 选择 direct/Eino scheduler，使 RAG、关系评估、Artifact、Capture、Organizing 能逐个灰度和回滚。
- [x] Graph 只编排三个固定 call+validate 节点；校验成功提前结束，失败最多消费三次模型响应。
- [x] 严格 decoder、原始响应字节闭包、Schema version、错误码及 token/byte/time budget 继续由项目代码掌握；Graph 不启用 checkpoint 或自动 retry。
- [x] 所有模型节点调用注入的项目 `ChatModel`，`RecordingChatModel` 继续记录每次 Model Call，不绕过 Model Run/Call 审计。
- [x] 五个固定 selector 均默认 direct，可按 RAG、关系评估、Artifact、Capture、Organizing 顺序逐个启用或回滚，禁止一个全局开关同时切换。
- [x] 用固定 fixture 对照 direct 与 Graph 的调用阶段/次数、accepted bytes、RuntimeRefs、usage/budget、错误分类、取消/超时和 exhaustion 行为。
- [x] 五个消费者均有 Eino scheduler 注入测试，并执行现有 Relation、RAG、Artifact、Capture、Organizing 包回归；详细命令和最终门禁记录在阶段 5 收口结果。
- [x] 在临时 PostgreSQL 18 + pgvector 数据库中分别运行 direct/Eino RAG 公共 HTTP→River 全链路，并模拟已完成 Node 的下一 River transport attempt；两种模式均保持 3 条 `PLAN/INITIAL/REVIEW` 成功 Model Call、单一成功 Attempt、同一 Answer/Knowledge 终态且不重复 Provider 调用。
- [x] 以 `ZHIXU_CHAT_IMPLEMENTATION=eino ZHIXU_STRUCTURED_SCHEDULER_RAG=eino make compose-rag-smoke` 运行真实容器闭环；Host Controller Coordinator/ComposeDriver 建立 exact-root Grant 后，公开 Scan/Ingestion/Approval/Reindex、Conversation/River/RAG、Citation、SSE、Feedback 与 exact replay 全部通过，trap 清理后无 project 容器、volume、network 或镜像残留。

门禁：StructuredRunner 合同等价、Model Run/Call 审计完整，上述五类消费者各自测试/评测不低于 direct 基线，且都可单独回滚。8-12 人日包含五类消费者的灰度与现有门禁执行，不包含新增真实模型数据集或修复无关历史失败。

## 阶段 4：只读 Tool Calling（6-10 人日，可在阶段 2 后独立决策）

- [x] 复核现有 Tool Registry、ExecutionService、Worker Tool Node、生产 Executor 和可信身份来源。
- [x] 复核现有 Eino Chat 合同，确认其禁止 `ToolCalls` 且当前没有独立 Tool Calling model Port。
- [x] 完成 Go/No-Go：因没有可提供持久 Attempt、lease/fence 与确定性 `call_no` 的生产消费者，本任务 No-Go，不创建未被运行路径使用的 bridge。
- [x] 冻结未来边界：只读工具仍须经 Registry、Policy、Capability、lease/fence、幂等、receipt 与 `UNKNOWN/manual_recovery`；写工具只走 Proposal/Approval/Safe Writeback。

门禁结果：NO-GO。安全实现需要新的持久 Workflow Tool-loop Node、独立严格 Tool Calling model Adapter 和顺序 ToolsNode，属于后续独立任务；当前没有注册 Eino ToolsNode，也没有新增运行时回滚面。

## 后续独立任务：检索组件桥接（预计 5-8 人日，不纳入本次实施）

当前批准的短 Graph 没有 Retriever 消费者，因此本任务不创建无调用方的 Eino Retriever/Embedder bridge。只有新的 RAG Graph 获得单独批准、Eino Embedding extension 有稳定可锁版本且选定真实 Rerank Provider 后，才创建独立任务；届时仍须保留 Active Index、Workspace/Approved Evidence、RRF、provenance、降级和 exact-result validator。

## 阶段 5：收口（2-4 人日）

- [x] 根据已通过门禁的实际范围更新 README、后端规范、部署/测试文档和分层采用 ADR，不提前宣称未落地能力。
- [x] 执行旧实现删除门禁：因真实 Provider smoke 仍为 SKIP，本次不删除 direct Adapter、direct scheduler 或独立回退开关，也不默认启用 Eino。
- [x] 保留版本升级说明、五个独立 scheduler selector、回滚步骤和面试可解释的边界决策。

阶段 5 结果：代码、离线、真实 PostgreSQL/River 和 Eino Compose 门禁已收口；唯一未完成的激活门禁是需要外部凭据的真实 OpenAI-Compatible Provider smoke。该门禁不影响 direct 默认路径，但在通过前禁止删除回退实现或将 `eino` 设为默认。

## 影响文件地图

| 阶段 | 已知影响文件/包 | 计划改动 |
|---|---|---|
| 0 | `docs/architecture/adr/`、`poc/eino`、`internal/agent/application` contract tests | 新 ADR、冻结 direct 基线、补齐采用门禁 |
| 1 | `internal/platform/models/chat_openai.go`、`chat_http.go`、`model_transport.go`、`runtime.go`、Chat factory、`cmd/api/main.go`、`cmd/worker/main.go` | 新 Eino Chat Adapter、共享安全 client、双实现选择与 Composition Root 注入 |
| 1 | `go.mod`、`go.sum`、`vendor/`、`deploy/Dockerfile`、`.github/workflows/ci.yml` | 锁版本、同步 vendor/镜像/CI 与许可证记录 |
| 2 | `internal/platform/observability`、Eino Adapter 包及相关测试 | Callback bridge、correlation、trace 与脱敏 |
| 3 | `internal/agent/application/runner.go`、新 Eino scheduler Adapter、五类消费者及其现有测试 | 项目 scheduler Port、Graph 实现、逐消费者灰度和合同/评测 |
| 4 | `internal/tools`、Worker Tool runtime 与 Chat contract（只读复核） | Go/No-Go 为 NO-GO；不新增未被生产路径消费的 bridge |
| 5 | README、ADR、后端规范、运维/回滚文档 | 按实际落地范围收口，不提前删除回退路径 |

新文件的最终名称和最小包位置在实施阶段读取对应 `.trellis/spec/` 后确定；上表不授权移动既有 Domain/Application 所有权。

## 验证命令

```bash
go test ./internal/platform/models ./internal/agent/application ./internal/agent/adapter/workflow ./internal/retrieval/application
go test -race ./internal/platform/models ./internal/agent/adapter/workflow ./internal/tools/...
go vet ./internal/platform/models ./internal/agent/... ./internal/retrieval/...
go test ./cmd/api ./cmd/worker
make openapi-check
make compose-check
ZHIXU_CHAT_IMPLEMENTATION=eino ZHIXU_STRUCTURED_SCHEDULER_RAG=eino make compose-rag-smoke
cd poc/eino && go test -race ./... && go vet ./...
```

真实 PostgreSQL/River integration 与确定性 Provider Compose smoke 已执行；有真实 Provider 凭据后再执行 live smoke，不把跳过的 live smoke 写成通过。

## 风险点与回滚点

| 回滚点 | 触发条件 | 回滚动作 |
|---|---|---|
| Chat Adapter 切换 | Schema/usage/错误/Provider smoke 任一不等价 | Composition Root 选择 direct Adapter；不回滚数据库 |
| Callback | 脱敏或 trace 语义变化 | 切回 `direct`，或将 telemetry 设为 disabled/noop；模型调用仍正常 |
| Structured Output Graph | accepted bytes、预算、调用次数、错误或审计漂移 | 对应消费者切回现有 direct `StructuredRunner` 实现 |
| Tool bridge | 权限、幂等或 UNKNOWN 处理失败 | 不注册 Eino ToolsNode；保留现有 Tool Executor |

## 预计工作量

以下累计口径包含阶段 0 的基线工作和阶段 5 的收口，不把它们藏在“迁移成本”之外：

- 最小正式采用（Chat + Callback）：约 13-21 人日。
- 再加 Structured Output 短 Graph：约 21-33 人日累计。
- 若未来另立任务补齐可信 Tool-loop 入口并实现只读 Tool Calling：再加约 6-10 人日，累计约 27-43 人日。
- 后续若另行批准检索桥接：再加约 5-8 人日，累计约 32-51 人日。

按单人每月约 18-20 个有效开发日计算，全范围约 1.6-2.8 个月；预留真实 Provider 联调、返工和简历材料整理后，按三个月安排合理。真实 Provider、vendor 冲突和集成数据库可用性是区间上限的主要不确定性；“让 Eino 接管整个 Workflow”不在本计划内，也不建议估算为可接受方案。
