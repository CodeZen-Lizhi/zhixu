# Model Settings 规划审查

## Resolved design findings

1. Model Settings 不能直接改写 legacy Repository：Audit、Local Runtime 和 Workflow fence 当前都是 caller-owned pgx transaction。设计采用 sibling GORM/scoped contracts并保留 legacy 至 Final。
2. GORM Bootstrap 不能接 `BootstrapDB` 或直接组合 Audit pgx DB；它接完整 platform Pool，并从同池构造三个 GORM owner。
3. State -> ordered Runtime -> ordered Participant、DB time、Snapshot isolation、RETURNING no-row 与 credential AAD 均作为硬合同写入设计。
4. Foundation scope 没有 Pool identity；foreign active scope 无法由本 child 检测，必须作为 Composition/TODO 9 同池约束记录。
5. GORM constructor 显式提供 SecretSealer、scoped Audit 和 scoped Local Runtime options，拒绝 typed nil/重复配置；真实 Bootstrap 使用同池依赖，TODO 9 可注入 Audit 故障实现证明整体回滚。
6. “当前不改测试”限定在 staged 实施；TODO 9 只允许原位参数化既有 integration fixture，不新增测试文件。

关键大规范会被 Trellis context injection 截断，因此 `baseline.md`、`sql-schema.md`、`test-wiring.md`、`quality-gates.md` 与本文件共同保存已核验的锁序、三条 scoped 原子链、取消/commit ambiguity 和 TODO 9 边界；实现与审查清单均显式引用这些摘要。

## Approval boundary

独立规划 reviewer 关闭 P0-P2 后才运行 `task.py start`。TODO 9、PRD AC、生产切线和任务归档继续未完成。
