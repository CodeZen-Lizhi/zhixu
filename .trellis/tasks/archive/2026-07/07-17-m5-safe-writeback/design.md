# M5-04 Safe Writeback 技术设计

## 1. Architecture Boundary

- `internal/changecontrol/domain`：Writeback Execution 状态机、不可变绑定、幂等身份、Commit Mapping 和稳定错误。
- `internal/changecontrol/application`：编排双授权、Durable Operation、文件/Git Saga、补偿和发布；不直接调用 pgx/os/exec。
- `internal/changecontrol/adapter/localfs`：Workspace ID → Root，目标锁、temp、Markdown 校验、最终 CAS、原子替换和基线恢复。
- `internal/platform/gitcli`：固定参数 Git Inspect/Diff/Commit/Verify/Reverse；不承载 Proposal 状态机。
- `internal/changecontrol/adapter/postgres`：Execution/Proposal 状态、Commit Mapping 与 Index Outbox 的数据库事务。
- M6 Retrieval：消费 `retrieval.revision.reindex_requested`，完成 Index/Regression 后回写 verifying→completed；M5-04 不实现假 Indexer。
- 当前运行时：已构造 Safe Writeback Node 与 API/Worker Composition，但仓库没有 River dispatcher、Job Registry 或 retry runner；直接 Node 集成烟测只证明 Saga 可运行，不能宣称自动异步领取或重试。

## 2. Core Flow

```mermaid
sequenceDiagram
    participant O as Trusted Orchestrator
    participant S as Safe Writeback Service
    participant DB as PostgreSQL
    participant FS as Workspace Store
    participant G as Git Repository
    O->>S: Begin(identity, two credentials, idempotency)
    S->>DB: Atomic Begin: lock/validate/consume both + lease + create/replay Execution
    DB-->>S: Execution=prepared, Proposal=applying
    S->>DB: validate running lease
    S->>FS: lock + prepare temp/backup + validate
    S->>DB: checkpoint file_prepared (durable intent)
    S->>FS: Resume/CommitCAS + atomic replace
    S->>DB: checkpoint file_applied
    S->>G: Diff check and persist git_prepared (Diff/Blob/Mode)
    S->>G: exact Trailer lookup; only NotFound may Commit
    S->>DB: checkpoint git_committed / Proposal=applied
    S->>DB: publish Mapping + Reindex Outbox atomically
    DB-->>S: Proposal=verifying, index=pending
    S->>FS: cleanup temp/backup evidence
    S->>DB: cleanup finalize (retryable)
```

双授权必须在 Atomic Begin 阶段同一事务内完成绑定和消费；文件替换后目标 Base Hash 已变化，不能再安全签发 Git Authorization。Credential 只在该事务内短暂存在，不进入 Execution、Node、Outbox、日志或 API 响应。

## 3. Data Model

### Proposal / Approval

- `proposal.version bigint`：每次状态变化 `old+1`。
- `approval.approved_git_head text nullable`：新审批必填 40/64 hex；历史 NULL 不可 Apply。

### Writeback Execution

关键字段：Workspace/Run/Node/Proposal/Revision/Approval、两个 Authorization ID、Target Path、Base/New/Change Hash、Approved/Result Git HEAD、Status、Idempotency Key、Failure Code、Manual Recovery、temp/backup locator、file byte size/mode/lock binding、Base/Result Blob ID/Base Mode、cleanup marker、Version、时间戳。

状态按可恢复检查点收敛为：

```text
prepared → file_prepared → file_applied → git_prepared → git_committed → verifying
prepared | file_prepared → needs_revision | apply_failed | manual_recovery_required
file_applied | git_prepared → compensating_file → compensated | manual_recovery_required
git_committed → publish_recovery_required | verifying
verifying → completed | verify_failed | rolled_back   (M6/后续任务)
```

数据库只记录安全摘要和定位 ID；临时/备份文件使用受控相对 Locator，不暴露绝对路径。

### Proposal Commit Mapping

唯一绑定 `workspace + git_commit`、`proposal + revision`、`writeback_execution`，保存 parent commit、target path、diff hash、新内容 hash和时间。

### Reindex Outbox

沿用 `workflow.outbox_event`，类型 `retrieval.revision.reindex_requested`，幂等键：

```text
reindex:<workspace>:<proposal>:<revision>:<commit>
```

Payload 只含稳定引用、target path、result hash、commit、parser/chunk schema 请求版本；不含正文、Credential 或 Embedding 维度。

## 4. Filesystem Contract

```go
type WorkspaceStore interface {
    AcquireTarget(context.Context, foundation.ID, string) (TargetLock, error)
    ResumeTarget(context.Context, foundation.ID, string, ResumeWrite) (TargetLock, PreparedWrite, *AppliedWrite, error)
}

type TargetLock interface {
    Prepare(context.Context, PrepareWrite) (PreparedWrite, error)
    CommitCAS(context.Context, PreparedWrite) (AppliedWrite, error)
    RestoreCAS(context.Context, AppliedWrite) (RestoreResult, error)
    Cleanup(context.Context, AppliedWrite) error
    Close() error
}
```

- Lock 使用 `.knowledge/locks/<target-hash>.lock` + OS advisory lock；锁目录拒绝 symlink。
- temp/backup 与目标同文件系统，`O_EXCL 0600`，写入后 file sync；rename 后 parent directory sync。
- 写目标拒绝 symlink 和非普通文件；保留原权限位。
- `Prepare` 必须预留同目录 temp/backup locator；`file_prepared` 把 locator、byte size、mode 和 lock binding 持久化后才能进入替换。
- `ResumeTarget` 在进程重启后重新获取锁并验证 locator 归属、regular file、owner/device、hash、size、mode；Base 仍存在时继续 CAS，Result+完整 backup 时识别已应用，其余情况进入人工恢复。
- `CommitCAS` 在持锁状态下最后一次读取/Hash/文件身份校验。外部非协作编辑仍可能在 rehash→rename 极小窗口发生；结果不确定时必须进入人工恢复，不宣称内核级 strict CAS。
- Publish 确认前保留 temp/backup；清理通过可重试的 `FinalizeWritebackCleanup` 完成，不能因清理失败返回假完成。

## 5. Git Contract

```go
type GitRepository interface {
    Inspect(context.Context, foundation.ID, string) (GitSnapshot, error)
    DiffApproved(context.Context, GitDiffRequest) (GitDiff, error)
    CommitApproved(context.Context, GitCommitRequest) (GitCommit, error)
    FindWritebackCommit(context.Context, GitCommitLookup) (GitCommit, error)
    CreateReverseCommit(context.Context, ReverseCommitRequest) (GitCommit, error)
}
```

- 写前：repo present、branch 非空、HEAD==approved head、无 dirty/staged/untracked。
- 写后：唯一 changed path 是 target；`git diff --check -- target`；Blob/new hash 与 Revision 一致。
- Commit 禁用 Hook，参数数组化；批准 raw blob 通过受控 NUL index record stage，`write-tree` 固化 immutable tree，再以 `commit-tree` + `update-ref expected-old` 发布，固定 Trailer 支持 crash-after-commit 恢复；不执行普通 `git add`/`git commit`。
- Application 在 Commit 前先持久化 `git_prepared` Diff/Blob/Mode binding；重放时先 `FindWritebackCommit`，只有明确 NotFound 且 HEAD/index 安全才调用 `CommitApproved`，unknown/conflict 不恢复文件。
- 自动 Reverse 只在 HEAD==produced commit 且 clean；使用受控 `revert --no-commit --no-edit` 生成反向结果并创建固定 Commit，禁止 reset/checkout/push。

## 6. Transaction And Recovery

- 文件/Git 不进入 PostgreSQL 事务；每个外部步骤前后持久化 Execution checkpoint。每个新副作用前再次校验 running Node lease，失去 lease 时停止开始下一副作用。
- `prepared → file_prepared → file_applied → git_prepared → git_committed` 是可重启检查点；`git_prepared` 先保存完整 Diff/Blob/Mode binding。
- 文件成功、Git 失败：恢复旧文件后再记录 compensated；恢复不确定 → manual recovery。
- Commit 成功、DB Publish 失败：不恢复文件；从 Trailer 找 exact Commit，再原子补 Mapping/Outbox。Commit lookup 明确 NotFound 之外的 unknown/conflict 一律人工恢复。
- DB Publish 事务同时写 Commit Mapping、Execution=verifying、Proposal=verifying 和 Reindex Outbox。
- 相同 Execution 重试先检查 checkpoint、文件 hash、HEAD 和 Mapping，已完成步骤返回既有结果，不重复副作用；Publish 后仅为 `verifying/index_pending`，清理证据失败保持可重试。

## 7. Compatibility And Rollback

- 新迁移只向前添加列/表/触发器；历史 Approval 的 Git HEAD 为 NULL，保持可查询但不可 Apply。
- 停止 Worker 可阻止新写回；已有 `file_applied` Execution 必须先恢复或继续，不能删除记录。
- 回滚代码版本时保留新表和 nullable 字段；旧代码仍可读 Proposal/Approval，不触发写回。
- M6 未部署时系统停在 `verifying/index_pending`，明确展示 degraded，不进入 completed。

## 8. Technical Decisions

1. 正式写回要求 clean Git 基线，即使 Workspace 创建允许 dirty；原因是 path-scoped Commit 必须证明没有夹带用户改动。
2. Approval 绑定 Git HEAD，而不是让 Apply 调用方提交 expected HEAD；原因是写入安全事实必须由服务端审批快照拥有。
3. 两份授权在 Atomic Begin 同一事务内消费；原因是旧的“创建 Execution→分步 Consume”会和 `approved/applying` 状态约束冲突，且明文 Credential 无法跨崩溃恢复。
4. `file_prepared`/`git_prepared` 是 durable intent；原因是 rename 或 Commit 成功后 checkpoint 前崩溃时，恢复必须依赖数据库事实而非内存对象。
5. M5-04 只持久化真实 Reindex Request，Proposal 停在 verifying；原因是 Retrieval 生产实现尚不存在，禁止假完成。
