# Health Testcontainers 验证证据

日期：2026-08-28

## 运行方式

所有新增或迁移的真实 PostgreSQL 测试均通过 `internal/platform/testdb.Require` 获取同一个共享 `platform/postgres.Pool`，再从该 Pool 派生 GORM、Workflow River 和 Events 依赖。测试未自行创建容器、数据库生命周期或第二连接池。

使用的构建标签为 `integration testcontainers`。为避开当前工作树已知的 vendor 清单不一致，命令显式使用 `GOFLAGS=-mod=mod`；未修改 vendor、模块或依赖文件。

## 已通过

| 验证 | 结果 | 覆盖重点 |
| --- | --- | --- |
| Health 默认测试、race、vet | 通过 | 受影响包的单元与并发检查 |
| Testcontainers integration compile | 通过 | Health PostgreSQL integration 测试可编译 |
| GORM composition、Scan、Schedule、Issue | 通过，37.684s | 同一 Pool、事务桥、River/Events enqueue、lease、CAS、missing-set |
| Affected-change dispatcher | 通过，42.668s | claim、回滚、poison、response-loss、降级 |
| Scan/River | 通过，55.931s | worker、replay、取消、retry/exhaustion、checkpoint |
| Fact reader cancellation、Read trend | 通过，15.408s | 表锁等待取消后的连接释放、七日 UTC 趋势聚合 |

## 未满足项与根因

组合执行 Collection 相关真实测试时，以下两个用例在 Health 代码运行前失败：

- `TestFactReaderEvidenceDetectorsDeduplicateAndPageByTypedKey`
- `TestTopicScopeReaderAndMissingSetUseConfirmedMembership`

根因是 `internal/graph/testfixture.SeedFunctional` 插入 `workspace.status='test'`，而当前数据库迁移的 `workspace_status_lifecycle` 仅接受当前合法状态，PostgreSQL 返回 SQLSTATE `23514`。该 fixture 属于 `internal/graph/**`，超出 child 模块范围，因此本 child 不修改它，也不把这两个用例标为通过。

`issue_repository_batch_integration_test.go` 和 `read_repository_integration_test.go` 仍有连接级 pgx tracer 的固定语句数/并发屏障断言。共享 factory 不暴露 tracer 注入；在本 child 另建 pool 会违反约束，而扩展 `internal/platform/testdb` 属于共享平台范围，故保留为未满足门禁。
