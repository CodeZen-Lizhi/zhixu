# M4-C Approval Safe Writeback Dispatch 实施清单

1. [ ] 新增 `00013_approval_writeback_dispatch.sql`，只实现 Proposal→Workflow Run binding、partial unique/FK/immutable trigger 与历史兼容测试。
2. [ ] 定义 ApprovalDispatchCommand/Result 和 Approved/Rejected HTTP/OpenAPI 201/200/409/503 契约。
3. [ ] 重构 DecideProposal：首次审批/历史不完整 dispatch 保留 Target Hash/Git snapshot 安全门；完整 Approval→Run→Node→Job binding 直接 exact replay，不访问已被写回改变的 FS/Git。
4. [ ] 实现跨 Change Control/Workflow/River Approval UoW，冻结锁序和事务故障注入。
5. [ ] 实现固定 Safe Writeback Definition/Node input、Run/Node/Outbox/Job 幂等绑定。
6. [ ] 新增 Execution exact lookup port/adapter 与全绑定校验/冲突测试。
7. [ ] 实现 Safe Writeback Bootstrap Executor：Claim后 lookup或瞬时签发双 Credential并 Atomic Begin。
8. [ ] 接入现有 `changecontrolworkflow.Node.Execute` 与 M4-B Complete/Retry/Manual 分类。
9. [ ] 覆盖 Authorization/Begin/lookup/Saga/Complete 全 crash/response-loss 矩阵。
10. [ ] 运行真实 River+PostgreSQL+LocalFS+Git 双 Worker/duplicate/kill-9 smoke，断言唯一 Execution/Commit/Mapping/Outbox。
11. [ ] 执行全库/日志/Job Secret 扫描；验证首次/补建路径的 NULL Git baseline/dirty/detached/drift 拒绝，以及写回后的完整绑定 replay 不访问 FS/Git并返回原 Run/Job。
12. [ ] 同步产品 PRD、OpenAPI、Workflow/Tool Security/Database/Recovery/Testing 文档和 Spec。
13. [ ] 执行 `go test -race`、关键 `-count=20`、OpenAPI、go-review/sql-code-review/Trellis check 与 `git diff --check`。

## Dependencies

M4-A Registry/UoW/River 和 M4-B lease/Attempt/result transaction 必须先完成；不得在本任务内复制 Runtime 或自制 polling。
