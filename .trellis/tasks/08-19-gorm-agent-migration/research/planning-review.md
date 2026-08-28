# Agent GORM 规划审查记录

## 审查范围

- Go/API/调用链：Model Run scoped consumer、Workspace Analysis scoped starter、生产构造兼容；
- SQL/事务：Run/Call trigger、Recovery、Memory、Workspace Analysis 锁序/CAS/deferred constraints、RAG Progress；
- Trellis：Owner 边界、依赖 producer、验收门禁、任务状态与上下文注入。

## 已发现并修复

1. Workspace Analysis readiness 与 Run starter 原先缺少不可拆分的 scoped 组合入口。现已冻结 `ScopedWorkspaceAnalysisCapabilityCheckedRunStarter`，同一 scope 内固定 readiness -> Run，且不管理事务。
2. Capture 仅需三方法 Model Run Finalizer，而 Artifact/Conversation 还需要按 Attempt 查询。现已拆分窄 `ScopedModelRunFinalizer`、`ScopedModelRunAttemptFinder` 与组合 `ScopedModelRunStore`。
3. Legacy Agent 直接锁定 Workflow owner 表。新规划改为 Workflow `ScopedWorkspaceAnalysisExecutionFence`，由 owner 在同一 scope 内锁 Run -> Node Run -> Node Attempt；Agent GORM 不直接访问 `workflow.*`。
4. Workflow child 原先没有 fence producer 交付项。其 PRD 已加入 Application Port、GORM Adapter、锁序、错误合同和 AC，作为 Agent Model Operation 的明确前置。
5. Fence 错误合同原先不完整。现以 `(snapshot, found, error)` 区分缺失/父子 binding drift，并冻结 Agent 对 conflict、invalid、scope、context、SQLSTATE 和依赖错误的翻译，禁止透传 Workflow code。

## 结论

- 独立 Go 与 SQL 规划复核在修订后未发现 P0/P1/P2；最终 Trellis 复核提出的两个 P2 已按 owner 交付和错误合同修复。
- Agent 与 Workflow child 均保持 `planning`；Agent PRD AC、实现、Review 和 TODO 9 项均未勾选。
- 当前环境无 `ZHIXU_TEST_DATABASE_URL`。真实 PostgreSQL 的锁竞争、trigger、deferred constraint、commit-loss、scope 可见性和查询计划仍是 TODO 9，不得由静态结果替代。
- 下一步仅在用户审批本方案后启动 Agent child；Workflow fence 未交付时，只能实施 Model Runtime、Recovery、Memory 与 Run/Capability staged 阶段。
