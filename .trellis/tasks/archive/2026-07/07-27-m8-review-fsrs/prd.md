# M8-02 Review Deck/Card 与 FSRS

## Goal

交付从正式 Claim 与可定位证据创建卡片，到用户复习、服务端可信评分、解释性反馈和幂等 FSRS 调度更新的完整闭环。用户能够在 Web 中管理 Deck、审阅卡片并完成每日复习；任何未确认知识、失效证据或重复提交都不能产生错误的学习状态。

## Confirmed Facts

- `internal/review/**`、`internal/platform/scheduler/**`、Review HTTP/OpenAPI 与 `web/src/features/review/**` 共同提供 Deck/Card/Schedule、服务端评分、命令 receipt、严格 due 投影和 Workspace-bound SSE 回查；评分、Answer 与 Schedule 的权威事实仍在 PostgreSQL。
- Review Session 只能使用 `session_type=REVIEW`，且必须绑定 Deck；提交和完成均验证 Session、Deck、Card 与 due question 的同一 Workspace 绑定。`INTERVIEW` 会话拥有独立事实，绝不能通过 Review Answer 推进 FSRS。
- `00052_m8_review_legacy_card_quarantine.sql` 将离开 `CONFIRMED` 的 Claim（包括 `DISPUTED`）关联的 APPROVED Card 显式失效，并隔离无法按 `review-evidence/v1` 校验的 legacy evidence；due SQL 对 JSON UUID 转换 fail closed，畸形旧数据不能中断 Workspace 查询。
- `00058_review_invalidation_batch_projection.sql` 用带 Claim 的 APPROVED evidence selector 双索引与 Claim partial index 支撑最多 200 张 Card 的失效批次，并把同一语句涉及的全部 Workspace/Claim 交给一次集合化 Health 聚合；receipt 只冻结 count/has_more 摘要。
- Card 的持久状态仍为 `DRAFT/APPROVED/INVALIDATED/REJECTED`；产品 `ACTIVE/SUSPENDED` 只由 Card、Deck 与 Schedule 联合投影，不能另建状态事实源。
- Due Question 使用 API-only HMAC `question_ref` 绑定 Session/Card/Schedule 快照；显式 key 或 `required` 模式从 Bootstrap Token 派生的 key 可跨重启稳定，多实例必须显式共享同一 key。只有 local `disabled` 缺省生成的进程随机 key、显式轮换或不同 key 实例会使未提交题目失效；已落库 Answer 的 exact receipt replay 不受影响。
- 每条 Answer 同时冻结 Scheduler 与 Scorer version；历史评分实现身份不可因算法升级被重写。

## In Scope

- Review Deck 的创建、列表、详情、每日限额、暂停/恢复/重置与 Workspace 隔离。
- 仅用 CONFIRMED Claim 和可验证 Source Span/Evidence 创建候选卡；去重、用户审批/驳回/编辑后重新校验证据，并明确 Card 状态映射。
- 服务端评分边界：用户只提交答案与自评难度；评分器以卡片的正式 Claim、答案要点与 Evidence 产生五维 Score、错误、遗漏和证据引用。客户端不得提交可信评分结果。
- Score 与 Card Evidence 的绑定校验、Review/Scoring Adapter 的不可用降级、Answer + Score + FSRS Schedule 的单事务提交、幂等重放与 CAS。
- Claim 离开 CONFIRMED（包括 superseded、deprecated、invalid 或 DISPUTED）、来源失效或 legacy evidence 不可验证时，Card 显式进入 INVALIDATED、从 due 队列移除，并建立 Health/Timeline 兼容的失效事实。
- Review Web：Deck 与今日复习入口、答案前隐藏答案/引用、提交后展示多维评分/遗漏/错误/引用、暂停/重置与错误恢复；桌面和移动端可用。
- REST/OpenAPI、鉴权能力映射、SSE 定向失效、真实 PostgreSQL、API/Worker/Vite/Playwright 验收和确定性评分/调度测试。

## Out Of Scope

- 语音回答、通用题库、第三方学习平台同步。
- Interview、Learning Path 与长期 Memory（由 M8-03 交付）。
- 以客户端传入的模型评分或模型输出直接更新 Schedule。
- 把 Review Card、评分或学习路径写入正式知识；正式知识修改仍使用 Proposal/Approval。

## Requirements

1. Card 必须绑定一个 CONFIRMED Claim、对应 Source Version/Span、hash 与可回查引用；审批和重新编辑后均重新验证绑定。
2. 每次 Answer 使用服务器端评分器；评分结构包含正确性、覆盖度、边界、清晰度、置信度、错误、遗漏和证据。评分器/证据校验不可用时 fail closed，不更新 Schedule。
3. 相同 `Idempotency-Key` 和完整请求精确重放同一个 Answer/Schedule；同 key 不同请求稳定冲突；并发提交只推进一次调度。
4. 初始调度、暂停、恢复、重置、失效以及每日限额由服务端事实决定；FSRS 与 Scorer version 冻结在历史 Answer/Schedule 中，算法升级不得重写历史。
5. Claim 或证据失效时 Card 状态、Schedule 和可见 due 结果保持一致，不留“隐藏但仍 ACTIVE”的第二事实源。
6. 前端以服务端资源和 SSE 定向失效恢复状态，答案与引用仅在提交后渲染；页面不保留答案正文到提交前可见 DOM。

## Acceptance Criteria

- [x] 用户能在同一 Workspace 创建、查看、暂停、恢复和重置 Deck；每日 due 查询不超过 Deck 的 `daily_limit`，跨 Workspace 资源不可见。
- [x] 只能创建并审批绑定 CONFIRMED Claim 和有效 Evidence 的 Card；重复 Card、证据漂移、编辑后失效和未批准 Card 均不能进入复习队列。
- [x] Web 用户提交文本答案和自评难度后，服务端返回可解释的五维评分、错误/遗漏和可打开证据；客户端无法伪造 Score 或直接推进 Schedule。
- [x] PostgreSQL 中 Answer、Score 快照和 FSRS Schedule 在一个事务中写入；响应丢失、重试和并发请求均只形成一个 Answer 与一次调度推进。
- [x] Card 的 Claim/Source/Conflict 失效后变为 INVALIDATED、从 due 队列移除，并可观察到原因；恢复必须重新审批并重新验证证据。
- [x] Production Go build/vet、OpenAPI、JSON/路由/Capability 与格式门禁通过；归档后动态补验进一步通过 Review PostgreSQL 全包、并发 race、M8/Review 迁移 race、前端 69 个测试文件共 746 条测试，以及真实 API/Worker/Vite 的桌面与 390x844 Playwright smoke。

## Risks And Deferred Items

- 首版评分器必须是服务端可替换 Adapter；若真实模型评分未通过可用性/证据门禁，返回明确不可用，不用客户端 score 或伪造成功降级。
- Card 状态以现有持久化兼容迁移为前提；旧记录需要明确的状态映射和前向迁移，不能静默改变历史含义。
- `00049_review_invalidation_observability.sql` 只把 Review Card 失效投影到既有 `REVIEW_INVALIDATED` Health Issue 与既有 Timeline outbox；它不是完整 Review Health/Impact 分析，也不替代 Card/Schedule 的学习生命周期事实。
- 评分 Gold Set 与真实 Provider 质量阈值属于 M11，但 M8-02 必须交付确定性边界与回归样本。
- 多 API 副本必须显式共享同一 `question_ref` HMAC key；轮换或混用不同 key 会使在途未提交题目 fail closed，发布时必须配合 due 刷新，不能影响已持久化 Answer/Schedule。
