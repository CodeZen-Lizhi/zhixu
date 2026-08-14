# Eino 迁移面试口径

> 当前代码状态：Eino/eino-ext 已成为 Chat、OpenAI-Compatible/Ollama Embedding、五类结构化调度器和
> `/chat` RAG v2 的默认生产实现。Eino Graph、classic ChatModelAgent/ToolsNode 与最终 Answer Stream 已进入
> 正式链路；项目继续拥有领域 Workflow、权限、审批、证据链及 PostgreSQL/River 持久化。真实外部 Provider、
> 浏览器实机、回滚演练和稳定发布观察仍是上线门禁，不要说成已经完成生产发布。

## 30 秒介绍

我做的是一个带严格证据门禁的知识管理和 RAG 系统。通用 AI Runtime 已正式迁到 Eino：模型接入和 Embedding
使用 eino-ext，结构化阶段和 RAG 内层路由使用 Eino Graph，只读工具循环使用 classic ChatModelAgent/ToolsNode，
最终答案通过 Eino Stream 写入短期 PostgreSQL 草稿，再由 SSE 展示。项目自己的代码继续负责检索、Evidence、
Citation/Faithfulness、权限、审批、幂等、ModelCall/ToolCall 审计，以及 River 的重投递、lease/fence 和最终原子发布。
简单说，Eino 负责通用 AI 编排，项目负责业务规则和可恢复的生产事实。

## 简历可写

- 主导 Go AI Runtime 的分层迁移，将 Eino `v0.9.13` 与 eino-ext 接入项目 Port，使 Chat、
  OpenAI-Compatible/Ollama Embedding、五类 Structured Scheduler 和 `/chat` RAG v2 默认走 Eino，构造或运行
  失败 fail closed，禁止静默回退到 legacy direct。
- 使用 Eino Graph 编排 Query Plan、Retrieval、Evidence、Generation、Publication 五个显式领域节点；使用 classic
  ChatModelAgent/ToolsNode 完成有界只读 ReAct 循环，所有工具仍经过项目 `ExecutionService`、allowlist、Schema、
  Capability、幂等 receipt 与审计，写工具和可信写回对模型不可达。
- 打通 tool-free Eino final-answer Stream、PostgreSQL generation/sequence/fence 草稿状态机、Answer SSE 与前端独立
  draft state；最终 Answer 仍在 metadata hash、Citation、Faithfulness 和 Finalizer 门禁后原子 `PUBLISHED`，草稿
  不进入 Answer、Evidence 或持久事件事实。
- 建立 NodeAttempt 级共享 `RunBudgetLedger` 和 ModelCall 事实，冻结
  `PLAN -> AGENT* -> ANSWER -> INITIAL/REPAIR/REDUCED -> REVIEW` 调用序列，关闭 Eino 内部 retry/failover，
  保留 PostgreSQL/River 作为跨进程恢复唯一事实源。
- 为 eino-ext Adapter 保留项目 hardened transport，补齐响应大小、Content-Type、SSRF/redirect、错误脱敏以及
  OpenAI index/model/order、Ollama `truncate=false` 等框架未承诺的 Provider 合同；通过全量 Go、race、真实
  PostgreSQL/River、Compose 多轮 Tool/Stream/replay、OpenAPI 和前端测试门禁。

不要写“Eino 替代了全部 Workflow”或“已经完成真实模型效果验证”。准确说法是：Eino 已成为代码和 Compose 的
生产默认 AI Runtime；River/PostgreSQL、领域门禁和权限仍归项目；真实外部 Provider、浏览器实机、回滚演练与稳定
发布观察尚未完成。

## 为什么用了框架，还保留项目代码

大白话回答：框架擅长模型接入、Graph、Agent loop、Tool dispatch 和 Stream，这些通用机制交给 Eino，能少维护一套
基础设施。但 Eino 不知道知序里哪些证据可以引用、谁有权调用工具、一次 River 任务怎样重放、租约丢了以后谁还能写、
最终答案怎样和审计事实一起提交。这些业务规则如果也交给框架，就会出现两套状态和两套重试。因此不是“框架或自研”
二选一，而是 Eino 做通用 Runtime，项目 Port/Adapter 锁住业务边界。

## 为什么一开始没有直接用 Eino

一开始我对框架能力估计得偏保守，只验证了 Chat Graph、Callback 和 ToolsNode 的 PoC，担心严格结构化输出、Provider
兼容和 River 恢复边界没有闭合，所以先写了项目自己的接口和部分实现。后来重新查官方文档和源码后确认，Eino 已能
承担 Chat、Embedding、Graph、classic Agent、ToolsNode、Stream 和 HITL 的大部分通用能力。于是我修正了决策：不再
用“框架不适合”笼统解释，而是把生产不变量列出来，能由 Eino 覆盖的正式迁入，框架不拥有的权限、证据和持久恢复留在
项目层。这个过程体现的是根据证据修正方案，而不是维护旧结论。

## 为什么 Eino Graph 没有替代 River/PostgreSQL Workflow

现在 Eino Graph 已经接管 RAG 进程内的节点连接和分支，不再由项目写通用 phase switch；但它没有接管跨进程持久
Workflow。原因是两者解决的问题不同：Eino Graph 管一次内存中的 AI 执行，River/PostgreSQL 管至少一次投递、
NodeAttempt、lease/fence、重试、outbox、Human Task 和响应丢失恢复。Eino checkpoint 也不是这套业务事实的替代品，
所以当前只保留隔离 PoC，不用于跨 Attempt 恢复。

## 怎么证明不是“配置写了 Eino，实际还走 direct”

我做了三层证据：第一，默认配置、`.env.example`、Compose、API 和 Worker composition 都选择 Eino，未知 selector 或
Eino capability 缺失直接启动失败；第二，生产组合测试实际构造 Eino Graph、ChatModelAgent、ToolsNode 和 Stream，
并断言 v2 Workflow、Tool contract 和 draft store 都被调用；第三，`make compose-rag-smoke` 用真实进程和 PostgreSQL
跑出 `PLAN,AGENT,AGENT,ANSWER,INITIAL,REVIEW`，同时断言 `ReadSource@2:SUCCEEDED`、draft `PUBLISHED` 和
Question exact replay 不新增 ModelCall。legacy direct 只允许显式选择 v1，不能自动接管在途 v2。

## 常见追问

**用 Eino 的收益是什么？**

主要收益不是少写几个 HTTP 请求，而是复用统一的 ChatModel、Embedding、Graph、Agent、ToolsNode 和 Stream 生命周期，
减少自有通用编排的维护面。项目再用稳定 Port 和合同测试把框架升级、Provider 差异与业务层隔开。

**成熟框架是不是一定比自研少 Bug？**

通用机制通常更成熟，但接入边界仍会出 Bug。实际迁移时就发现，Eino OpenAI 扩展把 `openai-request-id` 保存为命名
字符串类型，项目如果只按原生 `string` 断言，会把合法 Agent 响应拒掉。正确做法不是绕开框架，而是在 Adapter 边界
按官方真实类型修正，并补真实 ChatModelAgent 回归和 Compose 门禁。

**为什么还要 hardened transport？这是不是又在自研 SDK？**

不是。请求生成、SDK、Tool calling 和 Stream 都由 eino-ext 负责；项目 transport 只补自己的强制合同，例如 SSRF、
redirect、最大响应、错误脱敏，以及 Embedding 的 model/index/order 校验。这些是项目安全和数据一致性要求，框架接口
本来就不承诺全部覆盖。

**Tool Calling 如何保证安全？**

模型看到工具名不等于获得权限。Eino 负责生成 ToolCall 和顺序 dispatch，真正执行仍进入项目 `ExecutionService`，
再次校验冻结的 Workflow Definition、工具版本、Workspace、Attempt、lease/fence、Capability、Schema、幂等和 receipt。
首版只开放 `ReadSource@2`、`ValidateCitation@2` 等只读工具，写工具永远不进 Chat allowlist；需要修改数据时只能先生成
Proposal，由独立 Workflow 在批准后执行。

**为什么 Agent 结束后还要再发一次 Answer Stream？**

Agent 的中间轮可能在结尾产生 ToolCall，不能安全地把它一边生成一边发给用户，否则会泄露内部工具参数或半成品。
因此 Agent loop 只在 Worker 内消费；结束后再用不绑定工具的 Eino `ChatModel.Stream` 生成最终 Markdown。这个流可以
公开成草稿，但仍不是正式 Answer，必须继续通过 metadata hash、Citation、Faithfulness 和 Finalizer。

**为什么保留 direct？**

它只是迁移期的显式应急回滚，不是默认实现，也不是运行时 fallback。切回前必须暂停新 `/chat`、排空已分发的 v2，
再让 direct 只创建 v1；direct Worker 不能接管在途 v2。真实 Provider 和稳定发布观察完成后再删除 direct，避免在没有
回滚证据时把最后的恢复手段先删掉。

**这次迁移完成到什么程度？**

代码、默认配置、生产 composition、真实 PostgreSQL/River、Compose Tool/Stream/replay、Go race/vet、OpenAPI 和前端
单测/构建已经完成。尚未完成的是外部环境门禁：真实目标 Provider、桌面/移动浏览器实机、quiesce/direct 回滚演练和
一个稳定发布观察周期。面试时要把“实现完成”和“已生产发布”分开说。

**为什么按三个月规划？**

三个月包含的不只是写 Adapter，还包括官方能力复核、Provider 合同、Graph/Agent/Tool 安全、跨进程草稿流、数据库
状态机、真实环境联调、回滚演练和稳定发布观察。工程实现可以提前完成，但发布观察不能靠压缩代码时间跳过，所以用
9 到 13 周作为单人日历计划更诚实。
