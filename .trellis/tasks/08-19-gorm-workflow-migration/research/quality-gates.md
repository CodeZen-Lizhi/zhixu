# Workflow GORM 实施质量门禁

本摘要从 backend quality guidelines 提取本任务必须完整注入的规则；主 agent 规划时已完整读取原 spec。

- 公开 Application/Domain API 不含 pgx、GORM、database/sql 或 `any`；legacy allowlist 不扩张。
- 一个业务动作只有一个事务 owner；caller-owned scoped 方法不 begin/commit/rollback，不 fallback root。
- 事务内锁、状态写、Outbox、River job 和 Hook 使用同一 live scope；固定锁序、DB time、CAS 与 exact replay 不重排。
- Tool policy 只以单条 join 读取并 `FOR SHARE OF run,definition,node,attempt`；Tool recovery 固定 binding preflight -> Node Run -> Node Attempt 的 `FOR UPDATE SKIP LOCKED` 顺序，禁止锁 Run。
- Tool scoped Port 只返回 Workflow-owned raw facts，不访问 Agent/Tool Call 表、不解释 Tools policy；missing、skipped、stale 必须可区分，durable replay 禁止重入 live policy/fence。
- root/scoped 入口校验 nil context、typed-nil dependency、非法/失效 scope；context 保留 sentinel 与 custom cause。
- Raw SQL 全参数化；JSONB carrier、nullable scan、Rows Close/Err、no-row 与 SQLSTATE 显式处理。
- 未知错误不泄漏 SQL、参数、JSON、owner、内部路径或凭据；日志使用项目 logger 且不输出 bind values。
- 不使用 AutoMigrate、Migrator、Preload、Association、Save，不引入 N+1、Offset 或无界列表。
- 代码修改后执行局部 unit/race/vet、integration compile、consumer compile、vendor/module、task validate、gofmt/diff check。
- 真实 PostgreSQL 不可用时明确盲区，不能勾 PRD AC、切生产、删 legacy、完成或归档。
- Tool candidate 公平性、Runtime/River worker/response-loss、EXPLAIN 大数据计划、最终 Composition 和跨 owner 原子性缺少任一真实证据时，task 必须保持 `in_progress`。
