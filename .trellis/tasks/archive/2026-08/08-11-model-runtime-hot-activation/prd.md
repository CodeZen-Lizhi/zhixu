# 模型运行时无容器重启热切换

## Goal

管理员保存并应用 Chat/Embedding 模型配置时，不需要重启 API 或 Worker 容器；系统仍保证跨进程版本一致、进行中工作语义稳定、失败可恢复，并明确展示已保存版本、当前生效版本和应用进度。

常用路径为一个主操作“保存并应用”，同时保留次级“仅保存”。两种交互都必须忠实反映“配置已保存”和“配置已生效”是两个不同事实。

## Confirmed Background

- 当前模型设置采用 `desired`、`active`、单进程 `applied` 三层版本。保存只推进 `desired`，API/Worker 普通启动只加载 `active`。
- 当前实机状态为 desired revision 6、active revision 2、API/Worker applied revision 2；revision 2 的 Chat/Embedding 均为 disabled，revision 6 均为 openai-compatible。
- 页面“已关闭”徽标投影当前 active capability，而不是尚未应用的 desired，因此当前展示与实际运行态一致。
- 现有 API、Worker 与模型相关的 Handler、Executor、Search、Vector Builder 在进程启动时捕获同一不可变 runtime；现有 PostgreSQL runtime 投影每个 role 也只有一个槽位。
- Workflow 排队 Job 不携带模型 revision；Claim 创建的 `node_attempt` 会冻结 `model_settings_revision` 与 runtime binding，已开始 Attempt 不允许中途换模型。
- Embedding/Index Version 已持久化模型、维度、归一化、距离度量和 settings provenance，禁止跨不兼容向量空间混写或混查。
- 历史 launcher 曾执行受控 rollout，但当前 launcher 已缺失完整应用步骤；即使恢复旧流程，它仍是排空并替换进程的 restart 方案，不能满足本任务的进程内切换目标。
- 用户明确要求：安装本功能后，普通模型配置应用不重启 API/Worker 容器；Docker Desktop、PostgreSQL 和整机重启都不是配置生效步骤。

## Requirements

- R1：保存与生效继续是可验证的独立版本事实；不得把仅持久化 desired 描述为已经应用。
- R2：模型配置应用必须在现有 API/Worker 进程内完成，不停止、重建或重启其容器。
- R3：应用必须冻结用户指定的 exact desired revision。API 与 Worker 针对同一个 target 都准备成功后才能提交 active；不得在异步执行时改用更新的 desired。
- R4：target 准备期间，旧 active runtime 继续服务；候选解密、构造或预检失败不得中断旧能力。
- R5：提交 active 后，新默认 Chat 工作和新 Claim 的 Workflow Attempt 使用新 revision；已经开始的请求或 Attempt 继续使用其冻结 runtime，单次执行不得观察到跨 revision 的模型、协议、Secret 或 Contract。由持久 Index/Embedding Contract 控制的 Retrieval 继续遵循 R8。
- R6：切换只允许短暂阻止新的模型工作/Claim，不等待所有在途工作排空。旧 runtime 在持有者释放前不得关闭；释放后可回收，并可在持久化历史工作需要时按正 revision 重建。
- R7：同一 settings revision 中的 Chat、Embedding 及其 role-specific 依赖图必须作为一个完整 generation 原子准备和切换；任何一项失败都不得形成部分生效。
- R8：Embedding 模型、维度、归一化或距离度量变化不得把新向量混入旧 Index Version；查询和 Reindex 必须按持久化 Embedding/Index Contract 取得兼容 runtime。
- R9：应用状态至少区分已保存待应用、准备中、切换中、已生效和失败；页面同时展示 target、API/Worker 各自状态及旧 active 是否仍在服务。
- R10：并发保存、重复应用、进程或协调者中断、心跳 stale 和响应丢失必须通过 PostgreSQL lease/version/CAS 自动收敛；浏览器轮询只观察状态，不推进状态机。
- R11：提交 active 前失败时保留旧 active 并清理候选；提交后禁止自动回滚，因为可能已有新工作绑定 target，系统必须向前恢复直至两端 applied 一致。
- R12：每次 Apply 都基于 target revision 在 API/Worker 两端执行有界的最小生产路径预检。编辑阶段的显式连接测试只提供提前反馈，不能替代应用时预检。
- R13：主操作“保存并应用”先保存 immutable desired，再启动 exact-target activation。若保存成功而应用请求失败、超时或预检失败，配置仍保持已保存，页面明确显示尚未生效并允许重试。
- R14：次级“仅保存”只推进 desired，不启动 activation；之后页面提供“应用配置”操作。
- R15：Settings/Activation 继续仅允许 Cookie Session，unsafe mutation 必须通过 Origin/CSRF；Secret 不进入 activation 请求、响应、URL、日志、DOM 文本、Browser Storage 或 Query cache。
- R16：现有 static Env/YAML 模式、revision 0 canonical disabled、Secret keep/replace/clear、append-only revision、审计、受限模型 Transport 和连接测试保持兼容。static/unmanaged 配置不承诺历史热重建。
- R17：`./zhixu restart` 继续作为软件升级、进程故障和运维级重建手段，但不再是正常模型配置生效的必需步骤。

## Acceptance Criteria

- [x] AC1：API/Worker 容器 ID 与启动时间均不变化时，管理员可把 desired revision 应用为 active；最终 desired=active=API applied=Worker applied，两个 role 均 fresh。
- [x] AC2：修改本地草稿后点击“保存并应用”，页面先得到已保存 revision，再进入持久 activation 进度；刷新、断网重连或请求响应丢失后仍能恢复并显示真实结果。
- [x] AC3：点击“仅保存”后 desired 推进、active/applied 保持旧值，页面显示“已保存，尚未应用”，随后可对该 exact revision 执行“应用配置”。
- [x] AC4：应用期间已经开始的 Chat/Search 请求或已 Claim Workflow Attempt 全程使用旧 generation；提交后的新默认 Chat/Workflow 工作统一使用 target generation，Retrieval 按 AC8 的持久向量空间绑定执行。
- [x] AC5：API 或 Worker 任一角色对 target 解密、完整构造、连接预检或准备失败时，active 和新工作入口继续使用旧 revision；页面显示脱敏 role/phase/error 并允许修正或重试。
- [x] AC6：两端 prepared/armed 后 active 只提交一次；提交后发生协调者或单进程中断时只向前恢复，最终两端 applied target，不形成自动反向切换。
- [x] AC7：Chat/Embedding Provider、模型、API style、Secret 或运行时限制变化后，新工作只观察到完整 target generation，不出现单能力或单 role 半生效。
- [x] AC8：Embedding Contract 变化不会向旧向量空间写入新维度/度量；旧 Active Index 的查询和历史 Reindex 使用兼容 runtime 或显式失败，不会静默使用新默认 Embedder。
- [x] AC9：并发 Save/Apply、重复 Start、stale owner takeover、pre/post-commit crash recovery 和 Claim/cutover 竞态由真实 PostgreSQL 测试证明收敛。
- [x] AC10：配置页不再要求执行 `./zhixu restart`；桌面与 390x844 均可清楚区分保存中、已保存待应用、准备中、切换中、失败、已生效和 runtime degraded，且无 Secret/Endpoint 泄漏。
- [x] AC11：现有模型设置、runtime provenance、Workflow replay、Embedding/Index、认证、Workspace、OpenAPI strict decoder 和 Docker 生命周期回归测试继续通过。
- [x] AC12：端到端验收全过程不重启 API/Worker/PostgreSQL，不删除 volume、Workspace、Secret、历史 revision、Attempt 或 Index Version。

## Out Of Scope

- 不为第三方 Provider 实现新的模型协议、自动 fallback 或流量分配策略。
- 不修改历史 Workflow Attempt、Model Run、Embedding Version 或 Index Version 的不可变业务事实。
- 不新增独立模型运行时微服务；本次沿用模块化单体、PostgreSQL、现有 Provider Adapter 与 Go 标准同步原语。
- 不自动把新的 Embedding 设置发布成新的 Active Index；索引构建/激活继续遵循既有独立版本流程。
- MVP 不新增用户主动取消或一键回滚 activation；提交前由超时/失败自动保留旧版本，提交后向前恢复。恢复旧配置通过保存并应用一个新 revision 完成。
- 不取消 `./zhixu restart` 的软件升级和运维恢复能力，也不承诺容器、宿主机或 Provider 故障时业务零中断。
- 安装这次代码和数据库迁移本身仍是一次正常软件升级；“无需重启”指升级完成后的模型配置应用流程。
