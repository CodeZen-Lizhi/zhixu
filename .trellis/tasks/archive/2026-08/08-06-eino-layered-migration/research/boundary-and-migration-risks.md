# 所有权矩阵与迁移风险

> 状态：本文件最初用于实施前风险评估。最终范围以 ADR-0019、`implement.md` 和
> `stage2-gate-and-stage3-stage4-decisions.md` 为准；Stage 4 已判定 No-Go，本任务没有注册 ToolsNode 或 Tool bridge。

## 所有权矩阵

| 能力 | 目标归属 | 是否替换 | 约束 |
|---|---|---|---|
| OpenAI-Compatible Chat transport | Eino OpenAI extension + 项目 Adapter | 是，第一阶段 | 保留项目请求/响应、安全、错误和 model contract；旧 HTTP Adapter 暂留回滚 |
| Message 转换 | 项目 Adapter | 否，适配 | Eino `schema.Message` 不得穿透 Application/Domain |
| INITIAL/REPAIR/REDUCED | 项目 `StructuredRunner` | 否 | 三次上限、预算、Schema 原文校验和错误码属于业务契约 |
| Structured Output 内部阶段调度 | Eino Graph + 项目 `StructuredRunner` Facade | 分阶段 | 只编排 `INITIAL/REPAIR/REDUCED`；严格解码、预算和错误契约仍归项目 |
| Query Plan/Answer/Review 与完整 RAG 编排 | 项目 `RAGExecutor` | 保留 | 拒答、Evidence、Citation/Faithfulness 和 terminal proposal 不进入本次迁移 |
| Embedding Provider | 现有项目 Adapter | 第一阶段不替换 | 严格维度/归一化/批量/版本契约；Eino embedding ext 为 pseudo-version |
| SQL/FTS/pgvector 检索、RRF、Active Index | 项目 `SearchService`/PostgreSQL | 不替换 | Eino Retriever 只能包装 SearchService，不得直接查库或改变排名事实 |
| Rerank | 项目 Port/现状降级 | 不替换 | 当前无生产 Provider；未来 Eino provider 也必须经过 exact-result validator |
| Tool schema/模型 Tool Call 路由 | 项目 Tool Calling Port；未来可由 Eino ToolsNode 适配 | 本任务 No-Go | 当前没有可信持久 Attempt/lease/fence 与独立 Tool Calling model contract；未来也只能解析/路由，服务端 Registry、Capability、lease/fence、幂等和审计仍由项目执行服务拥有 |
| Tool 副作用与 Safe Writeback | 项目 Change Control/Tool Executor | 不替换 | Eino Context/Graph 不能构造 Write Authorization |
| Token StreamReader | Eino | 仅内部可选 | 当前产品只推送持久阶段 SSE；要新增 Token SSE 需另立需求和协议门禁 |
| River/PostgreSQL Workflow | 项目 | 不替换 | 状态、租约、重试、Outbox、Human Task、补偿、恢复唯一事实源 |
| Checkpoint/Interrupt | 项目 Workflow 为主；Eino 可辅助 | 不替换 | 防止双账；Eino checkpoint 不承担外部副作用恢复 |
| Callback/Tracing | Eino callback + 项目 telemetry bridge | 增强 | 必须脱敏；不能替换 Model Run/Call、Audit 或 OTel correlation 事实 |

## 主要风险

### 1. 严格 Schema 和动态 ResponseFormat

现有 Adapter 每次请求都发送冻结 Schema，并拒绝多 choice、非 stop、Tool Call、缺 usage 或 model mismatch。Eino OpenAI 扩展把 ResponseFormat 作为构造配置，动态 Schema 需要 request payload modifier 或自定义组件。必须先用 httptest 覆盖每个阶段和并发调用，再切生产工厂。

### 2. 错误分类与重试边界

Eino/extension 的错误包装可能改变 HTTP 状态、Context cancellation 和 unknown result 的可识别性。Adapter 必须把错误映射回现有 `MODEL_CHAT_*`/Agent 错误分类；不能开启 Eino/ADK 自动重试，因为 River 已拥有投递重试，模型调用结果未知时要进入原有人工恢复路径。

### 3. 双持久化状态

若启用 Eino checkpoint 又让 River 继续恢复，会出现两套 checkpoint。规划要求第一阶段不启用 Eino checkpoint；第二阶段若需要 Interrupt，只把 checkpoint ID 作为短流程辅助信息，业务状态仍由 PostgreSQL 事务归约。

### 4. 工具越权与重复副作用

Eino ToolsNode 的 `InvokableRun` 只表示框架调用。生产工具仍必须走项目 Registry/Policy/Capability/lease/fence/Tool Call receipt；写工具只经过 Proposal → Approval → Write Authorization → Safe Writeback。任何 Eino tool result 都不能直接标记 Workflow 成功。

### 5. 依赖与部署

根模块采用 `vendor` 构建；Eino 会增加大量间接依赖。必须在独立分支完成 license、`go mod tidy`、vendor、Docker 和 CI 复验，并保持旧 Adapter 可编译到删除门禁。

## 回滚原则

- 第一阶段只替换 Composition Root 的 Chat Adapter，保持 `agentapplication.ChatModel`、数据库和 API 契约不变；出现差异时切回旧 Adapter，不需要数据迁移。
- Structured Output Graph 的五个消费者均保留独立 direct 路径和固定输入 fixture；Tool Calling 本任务没有生产运行路径，未来独立任务必须另设回滚门禁。
- 删除旧 Adapter 的前提是 Provider contract、Schema/usage、取消/超时、错误分类、并发、Model Run/Call、River response-loss 和真实 Provider smoke 全部通过，并保留一条可重放回归集。
