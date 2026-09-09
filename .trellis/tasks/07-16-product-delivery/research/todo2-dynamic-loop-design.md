# TODO2 v2 实现设计与分工

依据本目录 `todo2-dynamic-loop-prd.md`、`todo2-dynamic-loop-surface.md` 与 foundation 研究。用户已授权继续开发并在必要验证后部署；以下为实施合同，不能作为功能完成证据。

## 版本与模型执行

- 保留 `workspace-analysis@1` 的 Definition、Graph Hash、输入/结果、历史记录和恢复执行器，追加 `workspace-analysis@2`。新请求由服务器选择 v2；相同幂等键的旧请求按已持久 Workflow Definition 重放，不能按当前配置改绑版本。
- Workflow 仍是合法无环图；动态取证循环在受持久 journal 保护的 loop 节点内执行，完成取证后进入独立的候选生成、Citation 校验、Faithfulness Review 与发布门禁。模型结束取证不构成发布授权。
- Eino v0.9.13 继续承担模型/工具调用协议与通用循环；项目拥有每次模型决策、白名单工具授权、持久恢复、预算、取消、Fence 和发布证明。不启用生产 Eino checkpoint，不另造通用 Runtime 或第二连接池。
- 一次 loop Attempt 只建立一个 ModelRun，内部逐 ModelCall 授权和记账；不能重复调用 v1 的单槽 Planner 来绕过 `uq_agent_model_run_node_attempt`。候选生成与 Review 使用各自独立节点/ModelRun。
- 模型每次可决定继续已获准的 Git/检索/Source 取证或结束；下一次请求确实包含上一步受控结果。既有 Citation 工具和成功发布的独立验证不可省略。工具参数只允许受限查询/短引用等模型可控制字段，Workspace、权限、ID、receipt、attempt/fence 由服务器重建。

## 持久事实与预算

- v2 使用有稳定全局顺序的版本化决策/操作 journal；同一 logical operation 的授权、调用、结果和预算结算精确绑定。替换 Worker 从已持久事实恢复，已完成调用不重复执行、不新增计费或时间线行。
- 外部调用已经发出而结果无法证明时记录 Unknown 并稳定终止，不能重复调用并声称精确恢复。每次新授权都在同池事务核对 Run、lease/fence、取消、截止时间和剩余预算。
- 已接受模型决定只存严格有界的 canonical 决策及私有请求回执；来源正文不进入队列、日志、公开事件或 ModelRun/Call 元数据。工具重放以精确持久 receipt 和 immutable 来源元组恢复，不能读“最新资料”替代历史输入。
- 预算必须覆盖动态决策数、Model/Tool calls、Source reads、单工具 timeout、总时间、Token、并发以及已配置的费用。具体常量由 foundation 的唯一 v2 policy 实现冻结，所有 Go/OpenAPI/Web 共用同一合同；不得沿用 v1 固定次数冒充动态预算，耗尽路径必须实际可达。
- Run-global Evidence 短引用固定为 E1–E32，追加检索只增补映射，不能重新绑定已分配引用。每个引用仍有确切 Search/Read receipt、原始身份/hash 与 Workspace。
- v2 成功必须证明候选、至少一条有效来源、精确 Citation receipt 和独立 Review 已闭合；可不调用 Git，因此公开 `git_status` 必需但可为 null，不制造默认 Git 结果。

## 公共合同与接线

- 继续使用 `/chat` 的 `workspace_analysis` mode 和既有 Answer/Timeline/Stop/Draft endpoints；请求不接受客户端选择 Definition/policy。
- v1/v2 结果与 Timeline 使用独立严格版本分支。新增 `decide_next` phase，保留 node/model/tool kind；工具行按已授权工具推导 phase。
- 时间线按持久 journal 顺序输出，保持 Search → Read → Search，不按 phase 重排；刷新或重投递保持同一序位。公开摘要只含允许的计数、引用和错误，不展示模型 reasoning/参数/正文。
- API 和 Worker readiness 同时证明精确 v2 Definition、executor、catalog、policy 与配置；保留 v1 读取和恢复。普通 RAG 不改投分析，分析失败不静默改投 RAG。

## 所有权

| Owner | 修改范围 | 必须交付 |
| --- | --- | --- |
| dynamic_foundation | Agent v2 domain/application/GORM；Conversation finalizer；新增 00098 起迁移；v2 Definition 合同 | 唯一 policy、journal/模型与工具授权、恢复/预算/终态 proof、可供执行器调用的早期接口 |
| dynamic_runtime | Agent Eino/Workflow v2 executor、工具桥接及 v2 模型 runner；测试 Provider | 真实模型动态决策、工具结果反馈、停止/恢复、候选/校验/Review 全闭环 |
| dynamic_contract | Conversation Go 结果与 Timeline DTO/validator、读取与持久顺序投影 | v1 兼容、严格 v2、安全公共数据 |
| public_ui | TODO2/4 OpenAPI/生成客户端/Web；TODO4 HTTP | 动态时间线与完整合成工作台、来源/审批/重试/面试入口 |
| 主会话 | Dispatcher 与 readiness、API/Worker main 接线、Atlas checksum/声明 Schema、部署、共享文档与最终检查 | 跨 owner 集成、必要验证、真实部署和准确任务状态 |

TODO4 迁移已分配 00095（核心）、00096（自动执行）、00097（笔记面试），已有 00094 不改；TODO2 使用 00098 起，额外编号先协调。`atlas.sum` 和声明 Schema 仅由主会话统一更新。共享 `agent/domain/output.go` / `runtime.go` 已由 synthesis_model 追加三类合成 Schema/Result，后续扩展不得覆盖它们。

## 实施与验证顺序

1. foundation 先落 policy、journal 与调用端口，通知 runtime/contract/UI；各 owner 按实际字段补齐接口记录，不另开需求审批。
2. 实现持久授权/结算、真实 Eino 循环、候选与发布 proof，接上新请求派发和双版本 readiness。
3. 接 Go 公共数据、OpenAPI/生成客户端和页面；确定性 Provider 至少提供不同工具顺序，以及追加 Search/Read、自主结束与真实预算耗尽。
4. 使用隔离 PostgreSQL/River 验证幂等、结果丢失、过期 Fence、取消和历史 v1 replay；复用已有 v1/RAG 有效基线，只运行受影响回归。
5. 完成必要 Go unit/race/vet/build、Schema/persistence/OpenAPI/Web 门禁、真实桌面/窄屏页面与独立检查，修复发现的问题。
6. 与 TODO4 完整验收统一核对，保护原 Workspace/数据库/配置后重新构建并部署；M11 全产品 E2E、AI Eval、SBOM/发布包继续暂缓。
