# Eino 迁移面试口径

## 30 秒介绍

我没有把知序整体重写成 Eino 项目，而是把 Eino 放在项目自己的接口后面。第一层用 Eino 的
OpenAI-Compatible ChatModel 替换可选的模型 SDK 实现；第二层接入请求级 Callback 做脱敏 Trace/Metrics；第三层
用一个无状态短 Graph 调度结构化输出的 INITIAL、REPAIR、REDUCED 三个阶段。RAG、证据与引用校验、模型调用审计、
PostgreSQL 持久状态、River 重试和安全写回仍由项目代码负责。这样既复用框架能力，也不会让框架变成第二套业务和恢复事实源。

## 简历可写

- 在自有 `ChatModel`/`StructuredRunner` Port 后渐进接入 Eino `v0.9.13`，实现 direct/Eino 双 Chat Adapter、请求级脱敏 Callback，以及可按五类消费者独立灰度的三阶段 Structured Output 短 Graph。
- 保留 PostgreSQL + River 作为 Workflow 与 Model Run/Call 唯一事实源，通过 direct/Eino 合同、并发 race 和真实 PostgreSQL/River 重投递测试，验证模型调用审计、错误语义、终态与回滚路径不漂移。
- 对 Tool Calling 执行 No-Go 门禁：在缺少可信持久 Attempt、lease/fence 和独立 Tool Calling 模型合同的情况下，不注册 Eino ToolsNode，避免绕过现有权限、幂等和安全写回链路。

不要写“Eino 已成为默认生产实现”或“真实模型效果已验证”。当前真实 Provider smoke 没有凭据，默认仍是 direct。

## 为什么用了框架，还保留自研代码

大白话回答：框架擅长的是模型接入、Graph 调度和 Callback 这些通用工作；它不知道知序里什么证据能用、一次 River
任务怎么恢复、谁有权限调用工具、文件写坏后怎么补偿。前一类交给 Eino 可以少维护通用代码，后一类如果也交出去，
反而会出现两套状态、两套重试和说不清的审计。所以我的做法不是“框架和自研二选一”，而是让框架待在它擅长的层。

## 为什么一开始没有直接用 Eino

当时 PoC 只证明了部分 Chat Graph、Callback 和 ToolsNode 示例，真实 Provider、严格结构化输出、River 恢复边界等门禁
还没有闭合。我前期选择先冻结项目接口和业务合同，这个方向偏保守，也增加了自维护代码。后续重新评估后，我没有继续
用“框架不适用”解释，而是按风险拆层接入：先 Chat，再 Callback，再在五个消费者复用点接短 Graph；没有可靠入口的
Tool Calling 明确 No-Go。这个调整说明我会根据新证据修正技术决策，而不是为了维护旧结论拒绝框架。

## 为什么不让 Eino Graph 接管完整 RAG 或 Workflow

完整 RAG 不只是几个模型节点，它还包含 Active Index、Evidence Eligibility、Citation closure、Faithfulness、拒答和原子
发布。持久 Workflow 还包含 River 至少一次投递、Attempt、lease/fence、暂停恢复和人工恢复。Eino Graph 可以组织一次
进程内短调用，但它不会自动替我保证这些业务和持久化不变量。当前最合适的边界是：Eino 调度短的、无副作用的模型阶段；
PostgreSQL/River 继续管理跨进程、可重试的长期状态。

## 怎么证明不是“配置写了 Eino，实际还在走 direct”

每个消费者的测试都用一个计数包装器包住真实 Eino scheduler：包装器先把调用次数加一，再委托 Eino Graph。测试不只看
依赖字段，还要求 `Schedule` 恰好执行一次，并检查 Model Call 的 call_no、phase、status、bytes、usage 和业务 finalizer。
RAG 还在真实 PostgreSQL/River 中分别跑 direct/Eino，完成后模拟下一次 River transport attempt，确认它被识别为 stale，
不会再次调用 Provider，也不会多写 Model Call、Attempt 或 Answer。

## 常见追问

**用 Eino 的收益是什么？**

统一了 Chat SDK、Callback 和 Graph 的通用机制，后续增加同类模型节点时不必继续扩写自有调度基础设施；同时保留项目
Port，供应商和框架升级仍可通过合同测试控制。

**代价是什么？**

依赖图和 vendor 体积明显增加，框架错误包装、动态 JSON Schema、并发请求隔离和 Provider 兼容性都需要额外合同测试；
因此不能因为“用了成熟框架”就减少项目自己的验证。

**为什么保留 direct？**

真实 Provider smoke 还没有执行，直接删除旧实现或默认切换会失去已验证的回滚路径。五个 scheduler selector 独立存在，
某一类消费者有兼容问题时可以只切回它，不需要改数据库或回放其他工作流。

**Tool Calling 为什么不顺手做了？**

模型发出工具名不等于它有权限执行。当前普通 Chat/RAG 没有可信的持久 Attempt、lease/fence 和确定性 call_no，现有 Chat
合同也拒绝 ToolCalls。此时接 ToolsNode 只能做出一个看起来能跑、但绕不开安全缺口的演示，所以本任务选择 No-Go。

**成熟框架是不是一定比自研少 Bug？**

框架能减少通用组件里的重复 Bug，但它不能替代项目边界测试。Eino 自己的 Graph 可能成熟，项目把 Schema、错误、审计和
重试接错仍然会出问题。正确收益来自“框架实现通用机制 + 项目合同锁住边界”，而不是简单把代码行数换成依赖。
