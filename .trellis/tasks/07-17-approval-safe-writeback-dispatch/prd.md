# M4-C Approval Safe Writeback Dispatch

## Goal

在 M4-A/B 的 River、Registry、UoW 和状态机之上，把用户 Approved Proposal 原子转换为唯一 Workflow Run/Node/River Job，由同一 lease owner 完成瞬时双授权、Atomic Begin/Execution 恢复、现有 Safe Writeback Node Execute 和 Workflow Complete，形成真实自动异步写回闭环。

## Scope

包含 Approval 自动 dispatch、Proposal→Run binding、Safe Writeback Bootstrap、Execution exact lookup、真实 River/PG/FS/Git 崩溃恢复。排除通用 Runtime 状态机、M4-D 运维、M6 Reindex consumer 与 `verifying → completed`。

## Conflicts Resolved

- 产品 PRD 要求批准后系统自动写回；当前 HTTP 只有 Approval 与 preflight。采用 Approved 自动 dispatch，不新增第二套 Apply 命令。
- Atomic Begin 要求 Node 已 running/lease owner 匹配；现有 Node 又要求 ExecutionID。采用 Job 先 Claim，再 bootstrap Begin/lookup，再构造 Node Input。
- Credential 不可持久化且 replay 不重新返回。Bootstrap 只在进程内瞬时签发；Begin 响应丢失通过稳定 Execution key 恢复。
- 当前 Approve 和 Workflow Start 各自事务。采用跨 Change Control/Workflow/River 的单一 UoW，禁止串联多个 Repository 冒充原子。

## Requirements

### R1. Approval Safety Gate

- 保留现有 Revision/Change Hash、Target Base Hash、strict-clean attached Git snapshot 和 `approved_git_head` 校验。
- 外部文件/Git I/O 在 DB 事务外完成；UoW 内重新锁定并验证 Proposal 仍 ready、Revision/Base/Change Hash 未变。
- snapshot 后到 Begin 的变化由 Atomic Begin/File CAS/Git HEAD 再验证，不静默更新审批基线。
- 已有完整 Approval→Run→Node→Job binding 的相同请求不能重跑 Target/Git preflight，必须直接进入 exact dispatch replay；只有首次审批或缺少完整 dispatch 的历史补建才重新执行安全门。不同 decision/binding 冲突。

### R2. Atomic Approval Dispatch

- Approved 在一个 pgx transaction 内持久化/replay Approval、Proposal approved + `workflow_run_id`、固定 Safe Writeback Definition/Run/Node、业务 Outbox 和唯一 River Job。
- Rejected 只保存决定，不创建 Workflow/Job。
- 任一步失败全部回滚；响应丢失重放返回同一 Approval/Run/Node/Job。
- 锁序：Proposal → Revision → Approval → Definition → Run → Node → Outbox → River Job；不得锁 Authorization。

### R3. Binding And API

- 新增 `proposal.workflow_run_id` nullable unique，只允许 NULL→唯一 Run；旧 Proposal 保持 NULL。
- Run idempotency key 由 Approval ID 派生；Node input 仅含 schema/proposal/revision/approved change hash，禁止正文/路径/Credential/Git 参数。
- Approved 新建返回 201，完全重放 200，包含 `workflow_run_id/workflow_status_url/dispatch_status`；Rejected 201/200 不返回 Workflow 字段。
- 历史 Approved 只有在 `approved_git_head` 有效且重新通过当前安全门时才允许补建 dispatch；NULL baseline 拒绝。
- 本任务唯一项目迁移为 `00013_approval_writeback_dispatch.sql`，只增加 Proposal→Run binding/约束，不重复 M4-A/B Workflow 字段。

### R4. Pre-Begin Bootstrap

- Worker 先通过 M4-B Claim 获得 DB-time lease，再查询 `safe-writeback:<node_run_id>` 对应 Execution。
- Execution 存在时验证 Workspace/Run/Node/Proposal/Revision/Approval/Change Hash/HEAD 全绑定并直接 Resume。
- 不存在时由受信 Bootstrap 瞬时签发 `WRITE_KNOWLEDGE` 和 `GIT_WRITE` 短 TTL Credential，使用稳定 Begin idempotency key 调用 Atomic Begin，随后清除明文引用。
- Job/Node/Outbox/日志/Attempt/Trace 不得保存 Credential。

### R5. Crash And Replay

- 第一/第二 Authorization 后崩溃：旧记录过期/撤销，新 delivery 使用新签发 key；不复用不可恢复 Credential。
- Begin commit 前崩溃：全部回滚，下一 delivery重新 bootstrap。
- Begin commit 后响应丢失：按 exact key找到唯一 Execution，不再次签发/Begin。
- Execution lookup binding 冲突进入 Manual，不创建第二 Execution。
- Saga 副作用后 Workflow Complete 前崩溃：现有 Node/Saga checkpoint 重放，只产生一个 Commit/Mapping/Reindex Outbox。

### R6. Node And Workflow Result

- Bootstrap 构造现有 `changecontrolworkflow.Input` 并调用 `Node.Execute`，lease owner 只作瞬时参数。
- Node 成功必须为 `verifying/index_pending` 且 cleanup 完成；Runtime 原子 Complete Workflow Node/Run。
- Workflow Run succeeded 不等于 Proposal completed；M6 Retrieval 前 Proposal/Execution保持 verifying。
- Retryable/Manual/LeaseLost 使用 M4-B 唯一错误分类，不让 River 状态覆盖领域事实。

### R7. Security

- 客户端不能提交 expected Git HEAD、ExecutionID、Authorization ID/Credential、路径、正文或 Git 参数。
- Job payload不是权限凭证；执行前重新加载 Approval/Run/Node/Definition/权限事实。
- Secret 扫描覆盖 River 表、Workflow/Change Control 表、Outbox、日志和错误。

## Acceptance Criteria

- [ ] Approved 与 Approval/Proposal binding/Run/Node/Outbox/River Job 原子提交；故障注入全有或全无。
- [ ] Rejected 无 Run/Node/Job；Approved 新建/重放返回 201/200 和同一 status URL。
- [ ] 首次审批或历史不完整 dispatch 不得绕过 Target/Git 安全门，dirty/detached/base drift/NULL baseline 均拒绝；已有完整绑定的 exact replay 即使写回后文件/HEAD 已变化也不访问 FS/Git，仍返回原 Approval/Run/Node/Job。
- [ ] 真实 River delivery 完成 Claim→Authorization→Begin/lookup→Node Execute→Workflow Complete。
- [ ] 两次 Authorization、Begin、lookup、Resume、Complete 的全部 crash/response-loss 窗口可恢复。
- [ ] 并发/duplicate/kill-9 只有一个 Execution、Git Commit、Mapping、Reindex Outbox 和 Workflow completion。
- [ ] exact lookup 绑定冲突进入 Manual，不签发新 Credential、不开始副作用。
- [ ] River Args/Node/Attempt/Outbox/日志/DB 全库 Secret 扫描无 Credential、正文、路径、Git 参数和 lock token。
- [ ] 结果稳定为 Workflow succeeded + Proposal/Execution `verifying/index_pending`，不伪造 completed/published。
- [ ] 真实 PostgreSQL/River/LocalFS/Git smoke、`go test -race`、关键 `-count=20`、OpenAPI、go-review/sql-code-review/Trellis check、`git diff --check` 通过。

## Stop Gate

只有 Approved HTTP 到真实 River Worker/Safe Writeback 的崩溃恢复闭环全部通过，才能进入 M4-D；不得以直接 Service 调用或 fake Resumer 代替。
