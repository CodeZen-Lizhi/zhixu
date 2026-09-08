# Conversation 执行计划

- [x] 核对父任务范围、现有 PostgreSQL 文件和 Agent/Workflow scoped 契约；用户授权完成全部剩余开发。
- [x] 逐文件冻结读写、分页、幂等、错误、锁序、草稿和终态恢复行为。
- [x] 实现同池 GORM Repository、Question Dispatcher、Execution Context、Draft Stream 和 Finalizer。
- [x] 完成 Workspace Analysis proof、timeline、audit、control/terminal scoped hooks，保持跨 owner 原子性。
- [x] 原位适配既有 integration fixture，验证主读写与代表性回滚/精确重放场景；不新增测试文件或临时测试代码。
- [x] 运行 `go test -timeout=60s ./internal/conversation/...`、`go vet ./internal/conversation/...`、选定 `-tags=integration` 场景及 `git diff --check`，真实命令与结果写入 research/验证记录。
- [x] 执行 Go/SQL 审查、task validate；补充 Final 构造、legacy 文件及回滚交接。
- [x] 按主会话 Final 授权清除本模块旧 pgx 实现和 legacy fixtures；共享纯 helpers 迁入 `*_contract.go`，原断言全部保留。

门禁遵循父任务 `research/lean-test-policy-2026-09-01.md`，不把历史穷举矩阵重新设为前置；不得以 skip 或 compile-only 代替实库验收。生产接线由主会话负责，本模块旧实现已在 Final 调度下清除。验证见 `research/validation-2026-09-08.md`，构造与回滚交接见 `final-handoff.md`。
