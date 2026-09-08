# Agent 测试与接线研究

## 可复用测试

- `internal/agent/adapter/postgres/repository_integration_test.go`：caller transaction、Run/Call replay/CAS/recovery；
- `memory_snapshots_integration_test.go`：claimant 竞争、READY+Run 原子绑定、FAILED；
- `workspace_analysis_model_operations_integration_test.go`：授权、成功/候选/Review/失败/拒绝/UNKNOWN、replacement 与 commit recovery；
- `workspace_analysis_concurrency_fault_integration_test.go`：双授权单 reservation、cancel-vs-authorize 无孤儿；
- `rag_progress_integration_test.go`：阶段顺序、脱敏与 Event replay；
- `internal/platform/migration/workspace_analysis_*_integration_test.go`：Schema/trigger/proof/capability/receipt/terminal hardening；
- Worker/Capture integration：跨 owner 原子事务与端到端恢复。

## Fixture 方案

当前 Agent fixture 只返回 `*pgxpool.Pool`。TODO 9 时每个 legacy/GORM 子测试使用独立临时数据库：raw migration pool 完成迁移并关闭，随后从同一 URL 创建唯一 `platformpostgres.Pool`。legacy 使用 `DB()`；GORM 使用同一 Pool 的 `GORM()`/`UnitOfWork()`、Workflow scoped execution fence 及 Events scoped Store。cleanup 先关闭平台 Pool 再 drop 数据库。

Workspace Analysis GORM 用例必须证明 Workflow fence 与 Agent Repository 收到同一个 active scope，按 Workflow Run -> Node Run -> Node Attempt -> Agent rows -> DB clock 的顺序持锁；另以 nil/失效 scope 验证 fail closed。Foundation 尚无 Pool affinity 时，active foreign-Pool scope 只能作为已知限制记录，不能伪造“已拒绝”的验收结果。

当前环境没有 `ZHIXU_TEST_DATABASE_URL`，integration compile-only 不能证明 GORM SQL、锁、trigger、并发或原子性，也不能勾选 AC。
