# Eino RAG Graph 正式迁移设计

## 当前状态

本地生产组合已完成 Eino Graph 迁移：`RAGExecutionScheduler` 编译固定 Graph，Graph 负责节点连接与分支，`RAGExecutor` 只执行项目领域事实。ChatModelAgent、只读 Tool、tool-free final Stream、metadata 和 Finalizer 已由父任务的 Compose smoke 覆盖；真实 Provider 和发布观察仍是外部门禁。

## 1. 调用链

```text
River -> Runtime Claim -> RAGWorkflowExecutor
                            |
                            v
              RAGExecutionScheduler Port
                            |
                            v
                   compiled Eino Graph
             / project nodes and Eino model nodes \
                            |
                            v
                      RAGProposal
                            |
                            v
                 project atomic finalizer
```

外层拥有恢复和发布，内层 Eino 拥有本次运行的节点连接、branch、loop 和 stream。

## 2. Graph State

Graph state 是 adapter 私有值，持有项目 `RAGRunInput`、冻结检索 tuple、Evidence refs、模型中间结果、预算和终态 Proposal。它不得包含数据库连接、Credential、绝对路径或可跨 Attempt 恢复的 Eino checkpoint。

## 3. 节点

1. Validate Input：复用项目请求/Scope/预算校验。
2. Query Plan：通过 Eino ChatModel 生成并进入项目严格 Schema 校验。
3. Clarification Branch：满足项目条件时直接形成 clarification proposal。
4. Retrieval：调用现有 ScopedRetrieval/Search 服务，保持 FTS/pgvector/RRF/active-index 语义。
5. Eligibility/Evidence：投影 approved evidence、provenance、conflict 和 topic allowlist。
6. Generation：由 Eino AgentRuntime 执行顺序只读 Tool loop；完成后由 AnswerStreamRuntime 调用不绑定工具的 `ChatModel.Stream`，再由结构化 scheduler 生成 hash-bound metadata envelope。
7. Citation/Faithfulness：调用现有项目 publisher/validator。
8. Terminal Branch：只返回 completed/refused/clarification proposal，由外层 finalizer 发布。

## 4. 迁移方式

`RAGExecutor` 内的领域步骤已收口为五个显式节点方法，由 Eino Graph 组合并按 route outcome 分支。测试以项目输入输出和持久事实为准，不以 Graph 节点数量为准；生产不再编译或保留旧 direct scheduler，历史版本通过 Git 发布记录恢复。

所有阶段共享 NodeAttempt 级 `RunBudgetLedger`。Agent 轮次、单次 ANSWER 流、metadata envelope 和 REVIEW 都在执行前按 phase 预留调用次数及输入/输出 Token 额度。Agent 调用先预占、再按 Provider usage 结算；usage 缺失时保守扣除预占上限。

## 5. 失败与重试

Graph 内不自动 retry/failover。节点错误映射为项目稳定错误；是否由 River 建立新 Attempt 由项目 Workflow 决定。最终 Answer 和同一活跃 Attempt 内保存完整结果的 ToolCall 通过项目 receipt replay；lease reclaim 后的新 Attempt 允许重新执行只读调用并建立新审计事实。只保存 hash/usage 的 ModelCall 在响应正文丢失后按现有 UNKNOWN/人工恢复语义处理，不能靠 Eino checkpoint 或 hash 伪造响应。
