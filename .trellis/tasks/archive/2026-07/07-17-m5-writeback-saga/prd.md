# M5-04D Safe Writeback Saga

## Goal

把已批准 Proposal 通过唯一受控应用 Saga 接入真实文件与 Git Adapter：审批时由服务端记录严格 clean Git HEAD；开始写回时在一个 PostgreSQL 事务内消费两份授权并创建 Durable Execution；文件与 Git 副作用使用可重启检查点恢复；最终原子发布 Commit Mapping 与 Reindex Outbox，并只推进到 `verifying/index_pending`。

## Background

- M5-04A 已提供 Approval Git HEAD 字段、Writeback Execution/Mapping/Outbox、状态机和 PostgreSQL 事务；M5-04B/C 已提供真实 LocalFS CAS 与 Git Commit/Trailer/Reverse Adapter。
- 当前产品路径仍不可运行：`DecideProposal` 没有观察 Git，API 创建的 Approved Approval 的 `approved_git_head` 为 NULL，而数据库禁止用 NULL 创建 Writeback Execution。
- 旧父设计的 `create execution → 分步消费两份 credential` 与现有实现冲突：创建 Execution 会把 Proposal 改为 `applying`，但授权消费只接受 `approved`；明文 Credential 又不可持久化，跨事务崩溃会永久丢失继续执行凭据。
- 当前 LocalFS 在随机 backup 创建与 rename 后才把 locator 交给调用方；若进程在 rename 成功、`file_applied` checkpoint 前崩溃，新进程无法重建 AppliedWrite。
- 当前 Git Adapter 内部可恢复命令 unknown result，但若 Commit 返回成功后、`git_committed` checkpoint 前崩溃，Execution 未保存 Diff/Blob/Mode，无法构造 exact Trailer lookup。
- 当前仓库没有 River dispatcher/Job Registry；`cmd/worker` 只是数据库健康循环。M5-04D 只能提供真实 Saga Node 契约、最小 Composition 和直接集成烟测，不得宣称异步 Worker 已自动领取任务。

## Requirements

### R1. 审批 Git 基线

- Approved 决策必须在写数据库前通过服务端 Workspace ID 读取 canonical Git top-level，要求仓库存在、attached HEAD、author identity 可用、无 tracked filter/hidden index flag/in-progress operation，且全仓 clean。
- Application 只保存 Adapter 返回的当前 HEAD；客户端不能提交 expected HEAD。Rejected 决策不要求 Git。
- Approval/Proposal 查询响应暴露只读 `approved_git_head`；历史 NULL 继续可查但不得 Begin Safe Writeback。

### R2. 原子 Begin 与双授权

- Begin 命令只接受服务端 Workflow/Proposal 身份、两份一次性 Credential 和写回幂等键；正文、目标、Hash、Git HEAD 和 Authorization ID 必须从持久化事实派生。
- PostgreSQL 必须在同一事务内按固定锁顺序：锁/校验两份 Authorization、使用数据库可信时间检查过期、验证 running Node lease、消费两份 Authorization、创建或重放 prepared Execution、将 Proposal `approved → applying`。
- `consumed` Authorization 只允许在已存在完整绑定的 Execution 时做幂等重放；若旧消费路径已消费授权但没有创建 Execution，Atomic Begin 必须拒绝，不能再次启动副作用。
- 两份授权必须分别绑定 `WRITE_KNOWLEDGE/ApplyApprovedPatch` 与 `GIT_WRITE/CreateGitCommit`，且 Workspace/Run/Node/Proposal/Revision/Approval/Scope/Change Hash/Target Version 完全一致。
- 事务失败不得留下单份已消费授权或无 Execution 的已消费授权；完全相同 Begin 可重放，不同绑定返回冲突。Credential 不写入 Execution、Workflow、Outbox、日志或 API 响应。

### R3. 可重启文件检查点

- 文件流程扩展为 `prepared → file_prepared → file_applied`。Prepare 必须在目标替换前确定并返回 temp/backup locator、Result Hash、Byte Size、Mode 和内部 lock binding；`file_prepared` checkpoint 持久化恢复所需摘要。
- durable file intent 必须包含原始目标、Result temp/应用后目标和 Base backup 的 opaque inode identity token；即使内容与 mode 相同，只要任一受控文件已被替换为新 inode，就不得自动 Commit、Restore 或 Cleanup。
- `CommitCAS` 只能使用已持久化的 PreparedWrite/预留 backup locator；进程在 rename 前后任一点崩溃时，新进程必须能重新获取目标锁、校验 temp/backup/target，并恢复为同一 Prepared/Applied binding。
- `file_prepared` 恢复时：目标仍为 Base 则继续 CAS；目标为 Result 且 backup 完整则识别已应用并进入 `file_applied`；其他内容、locator 篡改或结果未知进入 Manual Recovery。
- 只允许在 Git 明确未提交时 RestoreCAS；恢复不得覆盖用户后续编辑。Publish 确认后才清理 temp/backup 恢复证据。

### R4. 可重启 Git 检查点

- Git 流程扩展为 `file_applied → git_prepared → git_committed`。`git_prepared` 必须在 Commit 前持久化 Diff Hash、Base/Result Blob ID 和 Base Mode。
- 重放 `git_prepared` 时必须先按完整 immutable binding 调用 `FindWritebackCommit`；找到 exact Commit 则恢复 `git_committed`，明确 NotFound 且 HEAD/index 安全时才允许调用 `CommitApproved`。
- Commit 成功或 Adapter 内部 Recovered/Replayed 后 checkpoint Git Commit/Parent/Diff；无法证明发布结果时进入 Manual Recovery，不执行文件补偿。

### R5. Saga、补偿与发布

- 每个新外部副作用前使用数据库可信时间验证 Node 当前为 running、lease owner 匹配且 lease 未过期；失去 lease 时停止开始新副作用，由新 owner 按 Execution checkpoint 恢复。
- `prepared/file_prepared` 的 Base/HEAD/审批冲突进入 `needs_revision` 或 `apply_failed`；`file_applied/git_prepared` 且 Git 明确未提交时进入 `compensating_file → compensated`。
- Git 已提交后任何数据库失败都不得 Restore 文件；Execution 保持 `git_committed/publish_recovery_required`，重试 `PublishWriteback` 并通过 Mapping/Outbox 完整绑定恢复。
- Publish 在同一事务内写 Proposal Commit Mapping、Reindex Outbox、Execution/Proposal=`verifying`。成功后清理文件恢复证据；清理失败保持可重试且不得把 Proposal 标记 completed。
- 输出只包含稳定 ID、状态、Commit、Result/Diff Hash、`index_status=pending` 和 recovery 标记；不包含正文、Credential、绝对路径、lock token 或原始 stderr。

### R6. Workflow Node 与诚实接线

- 新增固定 Safe Writeback Node，持久输入只包含 schema version、Execution ID 和稳定 Workflow 身份；Credential 只存在于 Begin 的瞬时服务端调用，不进入 Node input。
- Node 只调用 Application Saga，不 import pgx、localfs、gitcli 或 River 类型；完成 Node 只表示写回已发布到 `verifying/index_pending`，不表示 Retrieval/Regression completed。
- API/Worker Composition 分离最小权限：API 只需要审批 Git Inspector；Worker/Saga 需要 WritebackRepository、WorkspaceStore、GitRepository 和 Lease Guard。
- 本任务不新增同步文件/Git HTTP Apply Endpoint，也不使用自制 polling 冒充 River。真实 River dispatcher/registry/retry runner 作为 M4 独立后续任务。

## Acceptance Criteria

- [x] Approved 决策使用真实 clean Git snapshot 持久化 `approved_git_head`；dirty/detached/root mismatch/历史 NULL 拒绝写回，HTTP/OpenAPI 查询可见该字段。
- [x] PostgreSQL 原子 Begin 同事务消费两份授权、创建/replay Execution 和推进 Proposal；故障注入证明任一步失败全部回滚，Credential 未持久化。
- [x] LocalFS 提供 `file_prepared` durable intent 与重启恢复；覆盖 rename 前、rename 后、DB checkpoint 前崩溃、backup/temp 篡改、同内容不同 inode 替换和用户后续编辑。
- [x] `git_prepared` 持久化 Diff/Blob/Mode；覆盖 Commit 成功后 checkpoint 前崩溃、exact Trailer recovery、明确未提交重试和 unknown→manual。
- [x] 正常路径完成 `approved → applying → file_prepared → file_applied → git_prepared → git_committed → verifying`，只创建一个 Commit、Mapping 和 Reindex Outbox。
- [x] 重复 Begin、重复 Node delivery、lease 丢失、各状态 Resume、文件补偿、Publish rollback/replay 和 cleanup retry 均有测试。
- [x] Node 输入/输出、Execution、Outbox 和日志扫描不含 Credential、正文、绝对路径、任意 Git 参数或 lock token。
- [x] 真实 PostgreSQL + 临时 Workspace + 真实 Git 集成 smoke 验证 Proposal/Approval/Authorization/File/Git/Mapping/Outbox 全链路，结果为 `verifying/index_pending`。
- [x] `go test -race ./...`、`go vet ./...`、`make test`、重复迁移、OpenAPI、Compose、Docker readiness、go-review、sql-code-review 和 Trellis full-scope check 通过。
- [x] 父任务、产品/架构/数据库/工具安全/Workflow/恢复文档同步，明确 River dispatcher 与 M6 Retrieval 仍未完成。

## Out of Scope

- River Job/dispatcher/registry/publisher、通用 Workflow retry runner；另建 M4 任务实现，不能由临时轮询替代。
- Retrieval/Embedding/Index Version/Regression 的真实消费和 `verifying → completed`；由 M6 实现。
- 用户主动回滚 UI、直接 HTTP 写回、Audit 完整产品、跨节点分布式锁和多文件事务。
- 自动三方合并；任何基线冲突都需新 Proposal Revision 与重新审批。
