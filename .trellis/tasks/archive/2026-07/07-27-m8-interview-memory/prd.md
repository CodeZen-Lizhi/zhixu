# M8-03 Interview、Learning Path 与 Memory

## Goal

交付以正式知识和证据驱动的面试模拟、基于评分缺口的学习路径，以及只能由用户显式确认后进入长期上下文的 Memory 生命周期。用户能完成一段面试、查看可解释报告与下一步学习动作，并完全控制其长期 Memory。

## Confirmed Facts

- M8-02 的 Deck/Card/Review Answer/FSRS 是稳定公开学习合同；M8-03 只消费其可解释结果，不能复制 Card/Answer 状态，也不能把 Interview Turn 写入 Review Answer 或 Schedule。
- `internal/memory/**` 提供 Candidate、Confirm、编辑、暂停/恢复、删除、到期维护、审计和 receipt。只有 `ACTIVE`、已确认、未过期且 scope 匹配的记录可进入 effective context；它们始终是偏好/上下文，不是 Citation 或知识事实。
- `review_session.session_type=INTERVIEW` 只作为壳；Question、Turn、Score、Report、Learning Path 和步骤状态是独立持久事实。Interview 评分与追问不会推进 FSRS。
- Review 与 Interview 共用 `learning.learning_path` / `learning_path_step` 聚合和 `LEARNING_PATH` Artifact；旧 `learning.interview_learning_path*` 只保留为可更新兼容视图。`origin_type` 严格区分 `INTERVIEW|REVIEW`，Review origin 唯一绑定一条不可变 Review Answer，gap、Score、Evidence 与 Artifact 绑定只能由服务端从已持久化事实派生。
- Interview Completion 通过 Begin/Prepare/Complete reservation 冻结 Session snapshot 与 Artifact digest；Artifact Draft 在成功绑定前保持 hidden hold，Worker 将超时尝试有界转为 ABANDONED/ORPHANED，ORPHANED 继续隐藏。Review-derived Path 已统一领域、Repository、HTTP、Web 与 `00060` 契约；Artifact digest 冻结 source snapshot、完整规范化 Draft、renderer 元数据和 attempt，API 在数据库依赖可用时注入真实 Service/Artifact bridge，Worker 在启动和周期路径调用 Application-owned 的 24 小时有界维护。归档后已用 fresh PostgreSQL、真实 API/Worker/Vite 和浏览器验证该生产链路。
- 用户 HTTP 创建 Candidate 固定为 `USER/user:manual`，不得接收 source type/ref、owner 或 confirmation 主体；`INTERVIEW` 来源仅能由服务端从已持久化 Learning Path 步骤派生，仍必须由用户 Confirm 才能变为 ACTIVE。
- Interview Candidate 保留客户端 Idempotency-Key 绑定完整请求，并以 stable owner + `INTERVIEW` source_ref 绑定同一步骤的语义 identity；同 key 异请求冲突，不同 key 等价请求复用同一 Candidate 与创建 Audit、各自保存 receipt。
- 难度由证据受限的服务端 prompt policy 区分核心复述、适用条件与已有边界；`started_at + duration_minutes` 派生不可变 deadline，过期后新 Submit fail closed，但 exact receipt replay 与 manual completion 保持可恢复。
- 连续追问消费最新评分缺口并受全局 `max_follow_ups` 约束；报告按主问题链最终状态汇总，SKIPPED 链同样形成一个可解释 gap 和 Learning Path step。
- Memory effective context 当前只由 Interview 的 `adapter/memory/loader.go` 在答题评分前读取；`internal/agent/**`、Conversation/RAG/Chat 尚无通用 Memory loader 生产接线，Memory 也不成为 Citation、Evidence 或 Turn 持久事实。

## In Scope

- 面试会话：岗位、范围、难度、时长、题目数和追问配置；以已批准 Claim/Evidence 选择题目，持久化问题、答案、评分、追问/切题决策和会话状态。
- 面试报告：主题覆盖、正确点、薄弱点、表达问题、可打开证据、推荐知识、后续复习动作和基于缺口生成的 Learning Path。
- Learning Path：Review Answer 与 Interview gap 共用 Path 聚合和 `LEARNING_PATH` Artifact，只引用正式知识与可验证评分结果，允许用户暂停/恢复/完成 Path 或推进步骤；不能直接修改正式知识或建立第二套文档存储。
- Memory Candidate、Confirm、列表、详情、编辑、暂停/恢复、删除与到期处理；类型覆盖偏好、情景、反馈和目标，范围限定 Workspace/任务。
- Interview 服务端上下文只能读取当前会话任务范围内 ACTIVE、已确认、未过期的 Memory，并明确标记为偏好/上下文，绝不作为 Citation 或知识事实。
- REST/OpenAPI、认证/Capability、SSE、Review/Interview/Memory Web 页面、真实 PostgreSQL、浏览器 smoke 和生命周期测试。

## Out Of Scope

- 未经用户确认自动写入长期 Memory、跨 Workspace Memory、从 Conversation 自动提取正式 Memory。
- 通用 Agent、Conversation、RAG 或 Chat 的 Memory 注入，以及把 Memory 作为检索证据、Citation 或知识事实。
- 把 Interview/Path 的模型文本当作正式 Claim 或直接写回 Markdown/Git。
- 语音面试、多人协作、通用职业档案或外部招聘平台集成。

## Requirements

1. 面试题、评分、追问和报告都必须关联已批准 Claim/Evidence；模型/评分不可用时明确失败或允许用户继续手动结束，不伪造报告。
2. 面试 Session 的问题/答案顺序、幂等提交、恢复和最终报告必须持久化；重复请求不能重复计分或重复生成不同报告。
3. Learning Path 只从服务端冻结的 Review Answer 或 Interview Score gap、相关 Topic/Claim/Source 构建；共享聚合必须保留 origin/source binding，复用 `LEARNING_PATH` Artifact 的版本与 Citation，且不创建正式知识写入。
4. Memory Candidate 不是正式 Memory。Confirm 是唯一写入 ACTIVE Memory 的路径，必须记录用户主体、confirmation/source、审计与版本；编辑或删除保持历史/审计可追溯。
5. 到期、暂停和删除的 Memory 不得被新的 Interview 上下文读取，也不得作为 Citation 或 Evidence 暴露；通用 Agent/RAG 读取不属于本任务已交付范围。

## Acceptance Criteria

- [x] 用户能配置并恢复一段面试，逐题回答并获得有证据的追问/评分；完成后报告显示覆盖、强弱项、表达问题、来源和下一步动作。
- [x] 同一面试答题/结束请求以 receipt/reservation 精确重放；PENDING completion 冻结 snapshot 并阻止新 Submit，最终只写一份报告和路径。
- [x] Interview origin 已能从可解释 gap 或 SKIPPED 主问题链生成 Path；Review origin 的共享持久化、服务、HTTP、Web、API Composition Root、SQL 契约和 reservation maintenance 已完成生产代码与静态契约收口。
- [x] Interview 服务端或用户可创建 Memory Candidate，但只有确认操作能创建 ACTIVE Memory；Candidate 未确认、暂停、删除或过期后均不进入 Interview 上下文。
- [x] 用户可按类型/状态查看、编辑、暂停、恢复和删除自己的 Memory；跨 Workspace/越权访问、非法状态转换和过期读取均 fail closed。
- [x] Production Go build/vet、Web lint/typecheck/build、OpenAPI、JSON/路由/Capability 与格式门禁通过；归档后动态补验进一步通过 Interview/Memory PostgreSQL、Memory 真实 provenance、`00059/00060` 前向迁移、Review Answer→Path，以及真实 API/Worker/Vite 的桌面与 390x844 Playwright smoke。
- [ ] `00059/00060` 存在业务数据时的 Down guard，以及 Review Path reservation/hold 的专门并发与 ABANDONED 重开仍未直接覆盖；不得由 fresh migration、Repository 全包或浏览器 smoke 推断为已通过。

## Risks And Deferred Items

- Interview 题目选择和评分经可替换服务端 Adapter 进入；Provider 不可用时不伪造证据或报告。
- Memory 作为上下文可见性有较高隐私风险，默认最小 scope、最短情景 TTL，读取必须显式过滤。
- Artifact 与 Interview 分属模块，不使用跨模块物理事务；`00057` 以 reservation、PLAN/hold digest fence 和精确 release 保证可见性。24 小时 Worker 维护只转为继续隐藏的 ORPHANED，不物理删除恢复/审计资产。
- 产品要求的 Memory `last_used_at`、最近使用展示和 `EPISODIC` 转为 `PREFERENCE` 尚未纳入本切片，不得因 M8-03 生命周期范围而降级或误报为已交付。
- 通用 Agent/RAG Memory 注入仍是明确缺口。Review-derived Learning Path 的生产接线、迁移/Repository SQL 对齐、24 小时维护调度与真实 PostgreSQL/API/Worker/浏览器主链路已通过；剩余风险限定为上述 Down guard、reservation/hold 并发及 ABANDONED 重开专门用例。
- 面试与 Memory 的真实模型质量阈值/Gold Set 由 M11 承接；M8-03 交付确定性生命周期与证据边界。
