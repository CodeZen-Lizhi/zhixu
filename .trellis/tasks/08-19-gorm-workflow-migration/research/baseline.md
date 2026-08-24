# Workflow GORM 迁移基线

## 公开面

- `domain.Repository`：Run/Node/Human 基础读写；另有 Run List 与 Pending Human Task 读取。
- `RuntimeRepository`：Start/StartTx、Claim、Heartbeat、TransitionDelivery、Control、WaitForHuman、SubmitHuman、Runtime Output。
- Application 的 `RuntimeStatePort` 已隔离数据库实现，但 `StartTx`、Cancellation/Terminal/Control Hook 仍直接绑定 `pgx.Tx`/`any`。

## 生产构造

- Worker 在 `cmd/worker/main.go` 组合 cancellation guard、Artifact/Organizing/Conversation Hook 后构造 legacy Runtime，并单独构造 legacy Repository。
- API 在 `cmd/api/main.go` 的 runtime factory 中构造 legacy pgx/River Runtime。
- TODO 9 前两处均保持不变。

## caller-owned Start

`StartTx(pgx.Tx)` 的直接 owner：Artifact、Conversation、Change Control approval dispatch、Health scan、Graph scan。它们必须各自在自己的 GORM child 中迁移到 `ScopedRuntimeStarter`，本 child 不改签旧接口。

## pgx-only Hook

- Cancellation：Change Control、Graph、Health；
- Terminal：Artifact、Conversation Workspace Analysis、Organizing；
- Control：Conversation Workspace Analysis；
- 这些实现都依赖 pgx transaction。GORM Runtime 不能复用它们，需 scoped sibling。

## 阶段结论

Workflow 的 staged GORM 实现可以编译并作为行为基线存在，但在 scoped enqueue fence 与跨 owner Hook/Start 未迁移前不能替换生产 Runtime。Execution fence 是可独立先交付的最小闭环。
