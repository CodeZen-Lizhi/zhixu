# Local Model Runtime 规划审查

## Resolved findings

1. 不能直接把 `TxLifecycle/WithTx(pgx.Tx)` 改成 opaque scope：Model Settings 当前在同一 pgx transaction 中调用它。设计改为新增 scoped `WithScope`/GORM Store，保留 legacy 到 Model Settings child/Final。
2. credential-init 的管理员 pgx、`ALTER ROLE`、角色属性校验和 0600 credential file 是父任务批准的 Final security allowlist，不属于本 child 的 GORM adapter。
3. `ReadDemand` 必须保持 RepeatableRead+ReadOnly 单快照；test probe 的 operation CAS 与 hold release 必须保持同一 owned UoW；runtime role 只能调用固定 SECURITY DEFINER functions。
4. 当前 platform scope 没有 Pool identity；active foreign-Pool scope 无法由 child runtime 检测，必须作为 Composition/TODO 9 同池不变量记录，不伪造 fail-closed 能力。

## Approval boundary

复杂 child 的 `design.md`/`implement.md` 已补齐后才可 `task.py start`。TODO 9、PRD AC 和任务归档保持未完成，直到真实 PostgreSQL 等价证据可执行。
