# M8-03 技术设计

## Boundaries

`review` 拥有 Review Answer 与 Interview Session、题目、Turn、报告等学习事实；`memory` 独立拥有 Candidate/Confirmed Memory 生命周期。Review 与 Interview 共用 `learning.learning_path(_step)` 聚合并复用 Artifact 模块的 `LEARNING_PATH` 类型、Revision 与 Citation；旧 Interview relation 只是兼容视图，不是第二持久化 owner。两条 origin 只能经窄 DTO/port 读取正式知识和冻结 Score。Interview Turn 不属于 Review Answer，绝不推进 FSRS。

## Data Flow

1. Start Interview 冻结配置与 Scope，题目选择器从 eligible Claim/Evidence 产生稳定问题；难度 policy 只要求证据中已有的核心、条件或边界。`review_session` 只作为会话壳，Question/Turn/Report 用独立事实表持久化；deadline 从 `started_at + duration_minutes` 派生。
2. Submit Interview Answer 使用受控评分器；持久化 Turn、最新缺口驱动的连续追问和下一题决策。Complete 先在 Session 行锁下 Begin reservation 冻结 snapshot，Prepare 固定完整 Artifact digest；Artifact PLAN receipt 与带 digest hidden hold 在同一事务写入，数据库按 `session → reservation → hold` 锁序核对 PENDING、digest、类型、角色和 stage key。最终事务写 Report/Path/steps/receipt 并只 release 精确两个 hold。Worker 将 24 小时超时 reservation/hold 有界转为 ABANDONED/ORPHANED，失败产物继续隐藏且可按稳定 identity 恢复。
3. Review-derived Path 只能从不可变 Review Answer 的服务端评分缺口与仍可验证 Citation 创建；`review-gap/v1` 决定 actionable gap，客户端不提交 gap、Score、Evidence 或 Artifact binding。创建 reservation、command receipt、hidden hold 与 digest fence 保护唯一 Answer→Path 绑定，Path/Step 仅通过 version CAS 推进。Artifact digest 在 Prepare 前由 source snapshot、完整规范化 Draft（Scope、正文、Citation）、renderer 元数据和 `attempt_no` 共同计算；只有已持久化且能从同一 snapshot 精确重算的 legacy v1 digest 可恢复原 PENDING attempt，空 digest/ABANDONED 重开一律使用 v2。旧 ORPHANED hold 不可复用。`PENDING` 是 Step 只读初始态，更新 target 仅允许 `IN_PROGRESS|COMPLETED|SKIPPED`。API Composition Root 已组装真实 Repository/Artifact bridge/Service/Handler，Worker 启动与周期路径已调用 Application-owned 维护策略；`00060` 与 Repository 契约已在 fresh PostgreSQL、生产 API/Worker/Vite 和浏览器主链路中动态验证，reservation/hold 专门并发与 ABANDONED 重开仍待独立覆盖。
4. 用户 HTTP Suggest 固定写 `USER/user:manual` Candidate；服务端 Interview Suggest 只从已持久化、`origin_type=INTERVIEW` 的 Learning Path 步骤派生 `INTERVIEW` 来源 Candidate。客户端 key 先绑定完整 Memory command request，随后结构化 session/path/step provenance 与部分唯一索引收敛同一步骤；等价新 key 复用原始 Candidate snapshot 并写各自 receipt。Confirm 在同一 Memory 事务验证 Candidate、用户主体和 scope，映射为 ACTIVE Memory 并追加与 aggregate version 一一对应的审计。读取只返回 ACTIVE、confirmed、effective 且未过期的 Memory。
5. Memory effective context 只在 Interview Service 提交 Turn、执行评分前通过专用 loader 注入；通用 Agent/Conversation/RAG 没有生产接线。事件仅失效 Web 查询；PostgreSQL 保留状态、版本和 idempotency receipt，Worker 负责已接线的 Memory expiry 与 Interview Completion orphan maintenance。页面按服务端 started_at 恢复倒计时，归零禁答并保留 manual completion。

## Compatibility And Rollback

- 用 `00045`–`00060` 前向迁移扩展 `learning` schema；`00059` 增加结构化 Interview Memory provenance、audit/version 与 keepalive guard，`00060` 将 Path 升格为共享基表并保留 Interview 可更新兼容视图。现有 `learning.memory` 与 Interview Path 只能按显式迁移/兼容关系读取，不允许应用自行猜测回填。
- 删除是业务逻辑 tombstone/不可见，不做静默物理抹除；恢复以新状态转换表达。
- 回滚关闭新 API/Worker/UI，保留历史 Interview/Memory/Path 读取能力；存在 Review origin Path、receipt、reservation 或 hold 时 `00060` Down 必须 fail closed。
