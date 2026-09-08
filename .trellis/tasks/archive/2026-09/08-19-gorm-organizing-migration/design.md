# Organizing GORM 迁移设计

## 边界与依据

用户于 2026-09-08 明确要求完成 TODO 10 的全部未完成子任务。本设计细化父任务等价迁移，不改变材料确认、Workflow、Artifact/Proposal 的产品语义或 Schema。

当前 PostgreSQL Repository 位于 `internal/organizing/adapter/postgres/{repository,template,runtime,generation}.go`；材料冻结验证位于 `internal/organizing/adapter/owner/transaction_fence.go`，直接构造 Knowledge/Retrieval pgx Repository；Workflow 终态写入位于 `internal/organizing/workflow/terminal.go`。这些事务路径必须一并迁移。

## 目标实现

- 从共享 `platformpostgres.Pool` 获取 GORM 与 UnitOfWork。显式 Model 映射现有 Draft/Version/Material、Template/Revision、Snapshot、CommandReceipt、StartOutbox、RunBinding、Generation、RunResult。
- 保持 command advisory lock、expected-version CAS、不可变版本、幂等绑定与精确 replay。材料确认继续在 Serializable 事务中冻结并重验 owner facts；只读快照保持 Repeatable Read + ReadOnly。
- 为 FrozenMaterialFence 使用 `foundation.TransactionScope`。Repository 调用稳定 Port；owner adapter 复用 Knowledge/Retrieval 的 scoped 只读能力，跨 owner 不能创建第二事务或把具体 PostgreSQL Repository 带入公共层。缺失能力只增加到真实 owner，保持现有批量上限和锁顺序。
- Runtime start 使用 Workflow `ScopedRuntimeStarter`/`ScopedRuntimeBindingReader`，原子闭合 Outbox 与 RunBinding；Generation 使用 Agent `ScopedModelRunStore` 原子终结模型事实。
- Workflow terminal hook 只拥有领域解码和校验，RunResult 的 SQL 移至 Organizing PostgreSQL Adapter 并由稳定 scoped writer 注入，清除 workflow 层 pgx 依赖。
- 常规 CRUD/批量使用 GORM，已有复杂锁/状态转换 SQL 保留参数化 Raw/Exec；复用 codec/验证，不自研 SQL 转译器或 pgx 兼容层。

## 验证与回滚

原位复用 Repository/Generation/Workflow 既有测试和 Testcontainers 工厂，证明 Draft/Confirm/Start 主路径及 Serializable fence/终态回滚的一条关键不变量。权限、Workspace、数据状态和错误语义保持。Final 才切生产并删除 legacy；不修改 Atlas Schema，回滚按 Adapter/接线版本边界进行。
