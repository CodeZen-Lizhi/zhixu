# TODO 10 按风险精简测试门禁

生效日期：2026-09-01。适用范围：TODO 10 中尚未归档的 child。已归档任务保留当时的完整验证记录，不回写、不降级。

## 目标

保留 GORM 迁移最容易出现的真实数据库风险，同时移除对每个模块重复、低收益的测试矩阵。此政策不是生产切换授权：所有 child 仍保持 legacy Composition，只有 Final 才能修改 `cmd/**`、删除 legacy 或收口 pgx allowlist。

## 每个模块的最低完成证据

1. GORM 实现符合该 child 的功能、领域边界、Schema 禁止项和单一 Pool 约束。
2. 受影响包既有 `go test`、`go vet`、`git diff --check` 和 Trellis task validate 通过。
3. 一个复用既有 fixture 的 Testcontainers integration 场景覆盖该模块的主读写或主查询路径。可行时在同一场景构造 legacy/GORM 对照；不可行时使用冻结行为基线和明确断言，不新增平行 fixture。
4. 若本 child 直接改变事务、锁、幂等、River/队列或跨 owner 原子性，再额外验证一条最关键的提交/回滚、冲突或并发不变量。
5. 轻量 Go/SQL Review 覆盖当前改动；权限、Workspace 隔离、敏感数据、状态机和错误边界不能以“测试精简”为由跳过。

## 不再默认要求

- 全部 legacy/GORM 场景逐一成对执行；
- 每个模块都跑 response-loss、取消、连接释放、故障注入或跨模块端到端；
- 每个模块都跑 EXPLAIN/容量基准或整包 integration `-race`；
- 重复的 compile-only、`go mod tidy -diff`、全仓/无关模块构建。

## 按风险触发的专项

| 直接改动 | 仍需的代表性专项 |
| --- | --- |
| 事务、写入幂等、乐观锁 | 一条提交/回滚或冲突/replay 场景 |
| `FOR UPDATE`、`SKIP LOCKED`、lease、advisory lock | 一条并发竞争或锁释放场景 |
| River/Outbox/跨 owner scope | 一条同事务写入与队列/协作者原子性场景 |
| 查询形状、索引或分页规模 | 只对该查询跑 EXPLAIN 或容量检查 |
| commit unknown、取消、资源释放实现 | 只对改动的错误/资源路径做专项验证 |

## 完成与回滚边界

child 在完成自身实现、直接依赖和上述最低证据后即可归档；这不代表生产已改用 GORM。Final 仍独占 Composition 切换、legacy 删除、全仓 allowlist 和发布回滚。任何发现的核心不变量、权限或数据一致性缺口仍阻断该 child 完成，不能用该政策豁免。
