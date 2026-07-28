# 收口 M8 Learning 动态盲区与通用 Memory

## Goal

在不重复实现既有 M8 Learning 状态机的前提下，补齐三类可复核遗留项：用真实 PostgreSQL 证明 `00059/00060` 的业务数据 Down guard；用可控并发窗口证明 Review Answer 到共享 Learning Path 的 reservation、hidden hold、Complete、maintenance 与 ABANDONED 重开不变量；仅为 Conversation RAG 显式接入当前用户已确认且有效的 Memory，并保持 Memory 永远是非证据上下文。

完成后，M8-03 已有实现与新增能力必须由动态证据区分：已有 guard/reservation 逻辑不得因为新增测试被描述成“本轮新实现”，而 Conversation RAG Memory 接线属于本轮真实功能增量。

## Confirmed Baseline

- `00059_m8_interview_memory_integrity.sql` 和 `00060_review_shared_learning_path.sql` 已包含 SQLSTATE `55000` 的 guarded Down；当前缺口是存在业务事实时的直接迁移测试。
- Review Learning Path 已有 Answer-scoped reservation、attempt fence、`LEARNING_PATH_CREATE` hidden hold、原子 Complete 和 24 小时 maintenance；当前缺口是专门的真实 PostgreSQL 并发与 ABANDONED 重开证明。
- Memory `LoadEffective` 已在 SQL/Application 双层过滤 ACTIVE、confirmed、unexpired、Workspace、stable owner 和 task scope；当前唯一生产消费者是 Interview loader。
- Conversation RAG 当前模型输入没有 Memory，Worker 也没有为 RAG Executor 注入 Memory loader；Relation Assessment 和 Artifact Section Generation 不得继承本轮能力。
- Review Path 前端旧契约漂移若已在当前 HEAD 修复，本任务只验证并记录结果，不重复修改。

## Requirements

### R1. `00059/00060` guarded Down 动态证明

- 分别在迁移版本 59 和 60 建立代表性的受保护业务事实，再执行 `DownTo(58)` / `DownTo(59)`。
- 必须断言 PostgreSQL code 为 `55000`、Goose 当前版本不变、触发测试的业务行仍存在，不能只检查错误字符串。
- seed helper 必须显式返回其建立的依赖行，避免因隐式依赖先触发另一 guard 分支而把测试误报为单分支覆盖。
- 清理夹具后继续证明空数据 Down -> Up 可恢复；不得修改已发布的 `00059/00060` 来配合测试。

### R2. Review Learning Path 并发与重开矩阵

- 使用真实 PostgreSQL 的独立连接、事务锁和测试 barrier 控制竞争窗口，不使用 `sleep` 猜测时序。
- 覆盖同 Answer 同 key/hash、同 Answer 不同 key、maintenance 与 late Artifact hold/Create、maintenance 与 Complete、Complete response-loss replay。
- 任一竞争结果最多只有一个逻辑 Path、一个当前 reservation attempt、一个完成 Artifact binding；receipt 和 hold 必须与终态闭合，不得出现公开 Artifact 与 ABANDONED reservation、COMPLETED reservation 与 ORPHANED 当前 hold 等偏态。
- `ABANDONED` 只允许原 key/hash 重开，`attempt_no` 精确加一；新 attempt 使用新的 v2 digest 和 Artifact identity，旧 hold 保持 ORPHANED，旧 attempt 的 late Prepare/Complete 必须被 attempt/digest fence 拒绝。
- 不同 key 或同 key 不同 hash 不得替换或重开既有 reservation。
- 若动态测试暴露生产缺陷，只做维持现有 `00060` 契约所需的最小根因修复；不得另建 Path 表或第二套状态机。

### R3. Conversation RAG 显式 Memory 注入

- 在 Agent Application 定义窄的 effective Memory loader；正式 Adapter 调用 Memory Application Service，不向 Agent 暴露 Memory Repository、SQL 类型或 Interview DTO。
- 只在 Conversation RAG Executor 注入该 loader。查询固定使用 stable single-user owner，`ConversationID` 作为 `TaskScopeID`，同时允许 global Memory。
- 只有 ACTIVE、confirmed、unexpired、Workspace/owner 匹配且 task scope 为 global 或当前 Conversation 的 Memory 可以进入模型输入；Candidate、Paused、Expired、Deleted、其他 Workspace/owner/task scope 全部排除。
- RAG 模型输入升级为显式版本，新增 `non_evidence_context`，至少区分 `user_preferences` 与 `task_context`，并保持有界条数、总字节、稳定顺序和 `untrusted_data` 标记。
- Memory 只可影响偏好、表达方式和当前任务意图；不得进入 Search query 的正式 Evidence 集、Citation、related topics、Eligibility、Faithfulness 的批准证据或知识事实。
- Memory loader 缺失或查询失败时 RAG capability fail closed；真实空结果才编码为空的非证据上下文，禁止静默 fallback。

### R4. Node attempt 冻结与审计

- 在调用 Memory loader 前，必须按 `ExecutionContext.NodeAttemptID` 原子创建持久 `PREPARING` snapshot reservation；唯一约束和 attempt 级锁保证只有创建成功的 claimant 可以调用 `LoadEffective`。并发执行、进程崩溃或响应丢失后，同一 attempt 不得再次加载 Memory。
- claimant 只加载一次并构造 canonical snapshot；随后在同一 PostgreSQL 事务把 reservation 归约为 `READY` 并创建绑定该 snapshot 的 Model Run。事务提交前不得调用 Provider；已有 `PREPARING|FAILED` reservation，或已有 `READY` 但缺少绑定 Model Run，均进入稳定 manual-recovery/fail-closed 路径，不重新查询 Memory。
- PLAN、INITIAL、REPAIR、REDUCED 和 REVIEW 均复用 claimant 一次构造的输入，不得在阶段间重新查询。在任何 Provider 调用前，Model Run 必须已绑定 canonical Memory snapshot 的稳定 SHA-256 digest、schema version、条数和字节数；不复制 Memory 正文到 snapshot reservation、Model Run、Model Call、Workflow input、事件或日志。
- digest 必须绑定 Workspace、stable owner、`ConversationID` task scope，以及稳定排序后的 Memory identity、version、type 和 canonical content，能够检测内容或版本漂移。
- 同一 node attempt 已存在 Model Run 时继续沿用既有 fail-closed replay 语义，不重新加载 Memory 或再次调用 Provider。新的 node attempt 可以读取新的 effective snapshot；该边界必须有测试和文档化断言。

### R5. 明确排除项

- 不把 Memory 注入 Relation Assessment、Artifact Section Generation、共享 ChatModel 装饰器或其他 Agent workflow。
- 不实现或更新 `last_used_at`，不新增最近使用 UI，不实现 `EPISODIC -> PREFERENCE` 转换。
- 不因本任务修改 Memory provenance、HTTP 生命周期、Review/Interview 前端业务范围或全局 Audit/OTel/Metrics。
- 不把 Memory 文本、Memory ID 或伪造 Citation ID 当作可引用来源。

## Acceptance Criteria

- [ ] `00059` 有代表性受保护业务数据时 Down 返回 SQLSTATE `55000`，版本保持 59 且数据仍在；清理后空数据 Down -> Up 成功。
- [ ] `00060` 有代表性受保护业务数据时 Down 返回 SQLSTATE `55000`，版本保持 60 且数据仍在；清理后空数据 Down -> Up 成功。
- [ ] Review Path 专门 PostgreSQL 测试覆盖同/异 key 竞争、hold/Create 与 maintenance、Complete 与 maintenance、response-loss replay，所有允许终态均满足 reservation/path/artifact/receipt/hold 闭包。
- [ ] ABANDONED 原 key/hash 重开后 `attempt_no + 1`、新 digest/Artifact identity 成立；旧 attempt 晚到请求失败且旧 hold 继续 ORPHANED。
- [ ] Conversation RAG 能加载 global 与 `task_scope_id=ConversationID` 的有效 Memory；所有状态、Workspace、owner、scope 反例均被排除。
- [ ] 一个 node attempt 的持久 claim 最多产生一个 loader claimant：获胜执行调用一次，所有并发/恢复执行调用零次；所有模型阶段复用同一 `non_evidence_context`，Model Run 在首个 Provider 调用前已与 READY snapshot 的稳定 digest/count/bytes 原子绑定。
- [ ] 相同 canonical snapshot 产生相同 digest，任一 identity/version/type/content 或 scope 变化会改变 digest；数据库不新增 Memory 正文副本。
- [ ] Memory 中包含貌似事实、工具指令或 Citation ID 时，也不能进入 approved Evidence/Citation；没有正式 Evidence 时仍按既有 refusal/clarification 规则 fail closed。
- [ ] Relation Assessment 与 Artifact Section Generation 输入契约和生产 composition 保持不变，并有回归证明没有全局注入。
- [ ] loader 缺失/失败明确使 RAG unavailable 或执行失败；空结果与依赖失败可区分。
- [ ] 当前 HEAD 的 Review Path 前端/OpenAPI 契约通过既有检查；若已经修复，不产生重复产品代码 diff。
- [ ] 定向 Go race、真实 PostgreSQL migration/Repository、Worker composition、OpenAPI/前端契约、`go vet` 与 `git diff --check` 通过；Go 与 SQL 改动完成适用审查。

## Notes

- 本任务不通过修改已发布迁移“修好” guard，也不以 mock 替代 SQLSTATE、锁顺序和 migration version 证据。
- 本任务完成不会自动补齐 Memory `last_used_at`、最近使用展示或类型转换，因此不得据此宣称 Memory 全产品范围完成。
