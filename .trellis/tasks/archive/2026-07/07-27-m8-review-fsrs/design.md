# M8-02 技术设计

## Boundaries

`review/domain` 保持 Deck/Card/Schedule/Session/Answer 的不变量和稳定错误；`review/application` 编排证据校验、评分、状态转换和调度；PostgreSQL 是全部学习状态与命令 receipt 的唯一事实源。HTTP、Web 与 Worker 只能调用 Application 合同。Review Session 只接受 `REVIEW` 壳并绑定 Deck；`INTERVIEW` 壳、Question、Turn、Report 与 Learning Path 由 M8-03 独立拥有，不能调用 Review Answer 或 FSRS 路径。

评分通过窄 `Scorer` port 接入。它只接收已验证 Card、用户答案和受控上下文，返回版本化 `Score`；不得接受浏览器传入的 Score，也不得直接写数据库。评分输出必须在 Application 层再校验证据为 Card Evidence 子集后，才可与 Answer/Schedule 同事务持久化。

## Data Flow

1. 创建/编辑 Card 时冻结 Claim/Evidence binding 与 fingerprint；审批时再次验证 Claim/Evidence，原子创建初始 Schedule。
2. Due 查询在 PostgreSQL 以 Card 状态、Deck 状态、Schedule 状态、Claim 可用性和 Deck daily limit 计算，不由客户端拼装；服务端用 API-only HMAC 签发绑定当前 Session/Card/Schedule 快照的 `question_ref`。显式 key 或 `required` 模式的域隔离派生 key 可跨重启稳定；local `disabled` 缺省时才使用进程随机 key，多实例必须显式共享同一 key。legacy evidence 的 JSON UUID 仅经安全转换，无法解析的行 fail closed，不能使整页 due 查询报错。
3. 提交答案先查询 idempotency receipt；未命中时验签 `question_ref`、加载可用 Session/Card/Schedule，调用 Scorer，验证 score，调用冻结版本的 Scheduler，并在一个事务写入 Answer（含 Scorer version）、Schedule 与 receipt。
4. Knowledge/Health 生命周期通过窄失效应用入口改变 Card/Schedule；任何 Claim 状态离开 `CONFIRMED`（含 `DISPUTED`）和 legacy evidence quarantine 都会持久化失效原因、删除 Schedule，重放不重复改变状态。公开失效命令通过 Claim partial index 或带 Claim 的 evidence selector 双索引，以最多 200 张 Card 的单 SQL 批次推进，只返回 count/has_more 摘要；多个 selector 按 Card 级 AND 组合，不能错误收紧为同一 Evidence item，Source-only 与 Source+Claim 各走匹配排序的静态计划。调用方用新 key 继续直到完成。`00058` 让一条 Card 语句只调用一次集合化 Health 聚合，批量处理全部受影响 Workspace/Claim；`00049` 仍只把结果投影给既有 Health/Timeline 兼容链路，不能反向拥有或恢复 Card 生命周期。
5. Web 通过严格 API client + Query 状态展示，SSE 仅做相关 key 的失效；提交前不请求或渲染答案要点/证据正文。

## Card 状态映射

- 生成结果只有通过正式 Claim/Evidence 校验后才落库；持久 `DRAFT` 对应产品 `READY_FOR_REVIEW`，不保存未经校验的 `GENERATED` 中间态。
- 持久 `APPROVED` 在 Deck `ACTIVE` 且 Schedule 未暂停时投影为产品 `ACTIVE`；Deck `PAUSED` 或 Schedule paused 时投影为 `SUSPENDED`，Card 行不重复保存该派生状态。
- `INVALIDATED` 是同名事实状态，必须同时保存原因/时间并移除 Schedule；只有编辑回 `DRAFT`、重新验证和审批才能恢复。
- `REJECTED` 是审批前驳回，不是产品 `RETIRED`。M8-02 不提供单 Card 退休命令，整组退休由 Deck `ARCHIVED` 表达；未来新增单卡退休必须使用前向迁移，不能重解释历史 `REJECTED`。
- 既有 `DRAFT/APPROVED/INVALIDATED/REJECTED` 记录不改写；产品有效状态由 Card、Deck、Schedule 联合读取，不另建第二事实源。

## Compatibility And Rollback

- 迁移只前向增加状态/原因/receipt 所需字段、索引和约束；旧 Card 通过显式 quarantine 或读模型兼容处理，不能让 unverifiable APPROVED Card 保留 ACTIVE Schedule。
- Scorer unavailable 时返回稳定可重试 Problem，不写 Answer 或 Schedule。
- key 轮换、不同 key 实例或 local `disabled` 随机 key 的 API 重启会使未提交 `question_ref` fail closed，并由 Web 刷新 due；显式/派生 key 的同 key 重启保持有效。已完成命令从 receipt exact replay，不重新验旧签名。
- 回滚禁用新路由/前端和 Worker 订阅即可；已有 Answer 与 Schedule 是不可变学习历史，保留读取兼容。
