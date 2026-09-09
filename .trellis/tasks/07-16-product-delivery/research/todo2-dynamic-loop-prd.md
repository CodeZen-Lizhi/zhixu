# TODO2 原始动态工具循环与本轮开发收口

## 授权与目标

2026-09-09 用户在确认原优化清单的 TODO2 仍缺少动态工具循环后明确要求继续开发：补齐未完成开发，完成已实现部分的必要验证，核对任务与文档并关闭已交付开发任务，最后重新部署本机 Docker 并证明部署成功。用户所称“AC11 测试模块”按现有项目语境对应 M11 最终 E2E、AI Eval 与发布包，本轮暂缓。

沿用已有 `07-16-product-delivery` 任务执行，不创建未经同意的新任务。此次明确实施与部署授权取代旧收尾记录中针对旧会话的“禁止部署”；不包含提交或推送 Git。TODO4 由其现有活跃任务负责，最后纳入统一开发交付核验，不重复实现或覆盖其代码。

## 已确认事实

- 原始 TODO2 要求模型在每次工具结果之后决定下一只读工具或最终回答：原清单 `source-requirement-optimization-list.md:65,80,101`。
- 当前 `workspace-analysis@1` 固定执行六阶段，节点工具和操作序列由服务端写死；既有首期已交付记录只证明该范围，不证明原始动态循环完成。
- 生产基线为 Eino v0.9.13、Gin、GORM、Atlas、PostgreSQL/River 与生成 OpenAPI 客户端。项目拥有权限、预算、Workflow、Model/Tool 事实、Evidence、Approval 与恢复，Eino 拥有通用模型/工具协议；禁止引入第二 Runtime 或生产 Eino checkpoint。
- 当前数据库、Definition、Operation slot、发布 proof 与 Web 时间线均冻结 v1 合同，扩展必须保留 v1 历史可读与可恢复，不原地篡改历史 Hash/迁移。
- 本轮初始 Docker 状态为 degraded：API 和 PostgreSQL 停止，Worker 与两个 relay 不健康；保存的 Workspace 为 `/Users/zhenglizhi/Documents/files/zhixu`。部署成功尚未证明。

## 功能与交付要求

- R1：`/chat` 显式工作区分析执行真正动态的受限只读工具循环。模型根据已取得的 Git、检索与来源结果选择下一操作或结束；不以固定流程、动态查询文本或测试脚本代替模型决策。
- R2：复用已有四类能力并保留服务端授权：Git 状态、受证据约束的知识检索、Source 读取与 Citation 校验。工具名、参数、来源短引用均须通过冻结目录与作用域校验；模型不得提供权限、路径、实体身份、lease/fence 或写工具。
- R3：模型决策、工具执行及其可恢复结果有版本化持久事实；Worker 重投递或崩溃后从相同 logical operation 恢复，已成功操作不重复调用、不重复计费或生成分叉时间线；无法证明的外部结果明确终止为 Unknown。
- R4：步骤、工具、模型、Token、时间、并发及已配置费用预算由服务端硬限制；取消和 budget exhaustion 可在真实动态循环到达，不能仅保留无实际路径的枚举。
- R5：正式答案发布仍要求有效来源、引用与独立忠实度校验；无依据、引用失败、模型/工具失败与未知结果均有可恢复或明确终止状态。正式文件/Git 变化继续由 Proposal/Approval/Safe Writeback 唯一拥有，Agent 不直接写入。
- R6：页面真实展示动态工具调用顺序、安全摘要、Token 草稿、等待和最终答案；刷新/断连恢复从服务端事实读取。普通 RAG、历史 v1 分析与 Workspace 隔离保持兼容。
- R7：TODO4 与本轮已有收尾分别取得实现和必要验证证据后再关闭；取消大型验收不能替代缺失实现或失败用例的修复。
- R8：任务、需求、路线图、规范和运维文档准确区分原始需求、首期历史、当前已交付行为及 M11 暂缓范围，不能再用静态首期完成标签代表动态需求完成。
- R9：完成开发集成后，基于用户保存的 Workspace/配置/数据执行受支持的 Docker 升级流程；先保护可恢复数据，再验证 migration、API、Worker、Web、relay 与 namespace anchors ready，固定浏览器入口可用且当前功能随部署更新。

## 验收

- [x] AC-D1：至少两个真实模型响应 fixture 导致不同的调用顺序/次数，且一次成功请求有至少两次依赖调用；后一步决策输入含前一步受控结果。测试 Provider 明确标作确定性行为验证，不冒充真实 Provider 质量评测。
- [x] AC-D2：模型请求第二轮检索/追加 Source 阅读及自主结束均能通过相同生产链路执行；伪造/未知/写工具和跨 Workspace 引用在执行前被拒绝。
- [x] AC-D3：隔离 PostgreSQL/River 验证决策后响应丢失、已完成工具重放、旧 lease/fence、取消与预算耗尽，记录调用和计费不重复。
- [x] AC-D4：成功答案引用与忠实度校验通过；引用失败、模型失败、工具失败和 Unknown 不发布成功；Token 草稿不能成为最终事实。
- [x] AC-D5：桌面/窄屏真实页面显示动态时间线，刷新恢复和停止可用；固定 RAG 与 v1 历史读取通过受影响回归。
- [x] AC-D6：Schema 前向升级、受影响 Go/Web/契约/持久化门禁及独立检查通过，必要问题已修复；每项证据对应实际代码状态。
- [x] AC-C1：TODO4 与其他已授权开发尾项有明确完成证据；活跃开发任务关闭，M11 作为暂缓验收保留，未执行项不写 PASS。
- [x] AC-C2：文档与任务逐项核对完成，原始清单中的未满足行为已对应实际实现，历史证据保留。
- [x] AC-C3：本机 Docker 重新构建与受控部署成功；`./zhixu status` 为 ready，必要 HTTP/浏览器、新功能接口与模型依赖状态已核验，已有 Workspace/数据库/密钥保留。现场 AI 准入受原有禁用模型配置限制，未将其记为模型闭环通过，具体边界见下表。

## 排除范围

M11 全产品演示/E2E、全域 AI Eval/质量基线、SBOM/发布包与大型容量/灾备矩阵；新增无关产品需求；未获授权的 Git commit/push；全局 Docker 重启或其他项目容器变更；绕过权限/预算/审批的 Agent；以减少验收标准或关闭记录代替实现。

## 协作与证据归属

- 当前任务负责 TODO2 动态循环、跨任务最终核对和本机 Docker 部署。
- 既有 TODO4 活跃任务继续拥有其实现、迁移、接线、必要测试和自身文档。
- 既有非 TODO4 收尾任务继续完成其已有验证与交付记录；最后复用证据，不重复运行无关全仓矩阵。
- `todo2-dynamic-loop-foundation.md` 记录精确复用/Schema/恢复研究，后续 design/implement 以该研究与版本锁定源码为依据。

## 2026-09-09 开发验收证据

| AC | 实际证据 |
| --- | --- |
| D1/D2 | [第四轮 Compose](todo2-v2-compose-verification.md)：默认 5 次决策、追加检索 6 次决策；不同顺序/次数、两次来源读取与自主结束；Tools 白名单与绑定拒绝回归 |
| D3 | [持久化与终态实库](todo2-v2-foundation-verification.md)；[Tools 恢复](todo2-dynamic-tools-verification.md)；真实预算/Stop/重放 |
| D4 | 独立 Candidate/Citation/Review/Finalizer 及成功/拒答/失败/UNKNOWN 实库；Compose 只发布校验后的答案 |
| D5 | 桌面 1440 px/窄屏 390 px 实际时间线、引用、Stop、刷新；v1/RAG 受影响 Go 回归保留原边界 |
| D6 | Atlas 99 文件与 schema drift、各 owner race/vet、最终 persistence-check（30 owner / 1969 Go 文件）、Web 1281 项、标准 OpenAPI/生成客户端、独立接线及 Tools 审查；breaking gate 限制单列 |
| C1 | TODO4 AC1–AC10 和真实 Compose/browser 完成并归档；其他开发尾项已归档；父任务保留 M11 |
| C2 | 原始 12 项清单、路线图、PRD/实施表、任务 metadata 与发布 runbook 同步；保留原首期及失败轮次证据，见 [统一记录](final-integration-2026-09-09.md) |
| C3 | 受支持 launcher 重建/部署 exit 0，Runtime ready、Atlas 00099；原业务 ID、Root/身份/密钥保留；认证 API 与真实桌面/窄屏页面通过。本机模型 active revision 2 仍为升级前的 disabled，revision 6 原有激活失败未变；AI 准入尚未开启，不宣称现场 Provider 闭环通过 |

本轮按用户原 Workspace/配置完成部署；为保留既有模型状态，未修改模型选型或触发付费调用。TODO2/TODO4 的实际执行闭环来自已通过的隔离 Compose；现场模型启用需要在模型设置中成功应用有效配置。M11、真实 Provider 质量与未执行的 OTLP/Worker kill/完整回归矩阵不标为通过。首次部署时的 OpenAPI 失败保留为历史事实；随后通过显式 HTTP v2 修复，原基线复查 0 / 0，修复尚未重新部署，见 [公共契约记录](public-contract-upgrade.md)。
