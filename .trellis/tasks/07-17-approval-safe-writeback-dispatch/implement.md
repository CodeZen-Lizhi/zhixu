# M4-C Approval Safe Writeback Dispatch 实施清单

1. [x] 新增 `00013_approval_writeback_dispatch.sql`，只实现 Proposal→Workflow Run binding、partial unique/FK/immutable trigger 与历史兼容测试。
2. [x] 定义 ApprovalDispatchCommand/Result 和 Approved/Rejected HTTP/OpenAPI 201/200/409/503 契约。
3. [x] 重构 DecideProposal：首次审批/历史不完整 dispatch 保留 Target Hash/Git snapshot 安全门；完整 Approval→Run→Node→Job binding 直接 exact replay，不访问已被写回改变的 FS/Git。
4. [x] 实现跨 Change Control/Workflow/River Approval UoW，冻结锁序和事务故障注入。
5. [x] 实现固定 Safe Writeback Definition/Node input、Run/Node/Outbox/Job 幂等绑定。
6. [x] 新增 Execution exact lookup port/adapter 与全绑定校验/冲突测试。
7. [x] 实现 Safe Writeback Bootstrap Executor：Claim后 lookup或瞬时签发双 Credential并 Atomic Begin。
8. [x] 接入现有 `changecontrolworkflow.Node.Execute` 与 M4-B Complete/Retry/Manual 分类。
9. [x] 覆盖 Authorization/Begin/lookup/Saga/Complete 全 crash/response-loss 矩阵。
10. [x] 运行真实 River+PostgreSQL+LocalFS+Git 双 Worker/duplicate 与持久 fault/response-loss smoke，断言唯一 Execution/Commit/Mapping/Outbox；真实 OS `kill -9` 与 River rescue/lifecycle 由 M4-D 验证。
11. [x] 执行持久 Runtime payload/Job Secret 扫描；验证首次/补建路径的 NULL Git baseline/dirty/detached/drift 拒绝，以及写回后的完整绑定 replay 不访问 FS/Git并返回原 Run/Job。运行时日志/metrics/trace 扫描由 M4-D 验证。
12. [x] 同步产品 PRD、OpenAPI、Workflow/Tool Security/Database/Recovery/Testing 文档和 Spec。
13. [x] 执行 `go test -race`、关键 `-count=20`、OpenAPI、go-review/sql-code-review/Trellis check 与 `git diff --check`。

## Dependencies

M4-A Registry/UoW/River 和 M4-B lease/Attempt/result transaction 必须先完成；不得在本任务内复制 Runtime 或自制 polling。

## Verification Record

- `make test`：PASS（Go test/vet、Web lint/typecheck/test/build、Eino、OpenAPI、Compose config）。
- `go test -race ./...`：PASS。
- Approval Dispatch PostgreSQL integration：`-race -count=20` PASS；覆盖并发唯一 binding、Rejected、全事务回滚和 commit response-loss replay。
- Runtime active-lease transport retry：`TestRuntimeStateHigherRiverAttemptWaitsForActiveLeaseThenReclaims -race -count=20` PASS。
- Atomic Begin Proposal→Run mismatch：PostgreSQL failure-path PASS；Bootstrap binding conflict `-race -count=20` PASS。
- 真实 HTTP→双 River Worker→PostgreSQL→LocalFS/Git smoke：PASS；唯一 Execution/Commit/Mapping/Reindex Outbox，exact HTTP replay 200。
- Migration `00013`：fresh DB Up/Down/guard PASS；复合 FK 阻止绑定后 Run Workspace 变化。
- 主 Agent `go-review`、`sql-code-review`、`trellis-check`：未发现剩余 P0/P1。
- 独立审查两轮：首轮 4 项风险/门禁；修复 transport retry、Proposal→Run 完整绑定、同 Workspace 复合 FK，优化 kill-9/observability 归属；复验通过。
- 已知限制：用户 Cancel/forced cancel 与真实 OS `kill -9` 必须在 M4-D 完成前保持非生产开放；M4-D PRD/AC/implement 已加入禁止 terminal Workflow + 非终态 Writeback Execution 孤儿组合。
