# Organizing 执行计划

- [x] 核对父任务、既有 Repository/Owner/Workflow 边界，记录用户完成 TODO 10 的授权。
- [x] 读取业务 specs 和直接调用方，落实 scoped owner 能力与文件分工。
- [x] 实现 GORM Draft/Template/Snapshot/Receipt/Outbox/RunBinding Repository，保持隔离级别、锁序和 CAS。
- [x] 实现 scoped FrozenMaterialFence、Agent Generation 终态和 Workflow Start/Result writer；通过稳定 Port 移除具体 pgx 协作者。
- [x] 原位复用既有 Testcontainers fixture，完成主路径与代表性 Serializable/终态事务验证；不新增测试文件或临时测试代码。
- [x] 运行 `go test -timeout=60s ./internal/organizing/...`、`go vet ./internal/organizing/...`、选定 `-tags=integration` 用例、`git diff --check` 和 task validate。
- [x] Go/SQL 审查无遗留阻断，记录真实验证证据、Final 构造/legacy 清单及回滚边界。

测试范围以父任务 `research/lean-test-policy-2026-09-01.md` 为准。Knowledge/Retrieval/Workflow/Agent 若需新增 scoped 方法，先与 owner 协调；不并发编辑同一文件。Final 负责生产接线与旧路径删除。

2026-09-08 验证、构造顺序、剩余 legacy 依赖详见 `research/final-handoff.md`。
