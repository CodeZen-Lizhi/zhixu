# M5-04 Safe Writeback Saga

## Goal

将已批准 Proposal 通过唯一受控写入 seam 真实应用到 Workspace Markdown，并创建可反查的 Git Commit；整个过程必须具备目标版本 CAS、幂等恢复、数据库映射、增量索引请求和明确补偿，禁止把授权消费、文件替换或索引请求伪装成完整成功。

## Background

- M5-03 已实现 Proposal/Revision/Approval/Change Hash 与两类短期 Write Authorization：`WRITE_KNOWLEDGE/ApplyApprovedPatch` 和 `GIT_WRITE/CreateGitCommit`。
- 当前文件系统只支持安全读取、Hash 和不可变 Artifact Capture；Git Adapter 只支持 Status/Initialize；数据库没有 Writeback Execution、Commit Mapping、Compensation 或 Index Request。
- 当前仓库没有生产 Retrieval 模块、Embedding 维度或 Index Version 实现，不能创建猜测向量列或宣称真实索引已完成。
- 产品文档要求：目标锁与版本校验 → 临时写入与校验 → 原子替换 → Diff 校验 → Git Commit → DB Mapping → Reindex → Regression；文件、Git 和数据库不能放入单一事务，必须使用 Saga。
- 历史决策允许 Workspace 创建时存在 Git Dirty 警告，但正式写回属于更高风险边界，必须在审批/写回时使用可解释的干净 Git 基线，避免把用户无关改动夹带进 Commit。

### M5-04D 已确认的实现边界

- Approved 决策由服务端通过 Git Adapter 捕获严格 clean、attached 的当前 HEAD，并将其写入 `approved_git_head`；客户端不能提交或替换该基线。
- Safe Writeback 的 Begin 在一个 PostgreSQL 事务内校验并消费两份授权、校验 running Node lease、创建或重放 Durable Execution，并推进 Proposal `approved → applying`。不能使用“先创建 Execution、再分两次 Consume”的旧顺序，因为 Credential 不可恢复且会与状态约束冲突。
- 文件和 Git 都有持久化意图检查点：`file_prepared` 保存 temp/backup locator、byte size、mode 和 lock binding；`git_prepared` 保存 Diff/Blob/Mode。进程重启必须从这些事实恢复，而不是依赖内存对象。
- Git 恢复先按完整 Trailer/binding 查找已有 Commit；只有明确 `NotFound` 且 HEAD/index 安全时才允许 Commit。结果未知或绑定不一致时进入人工恢复，不能恢复文件后盲目重试。
- Commit Mapping 与 `retrieval.revision.reindex_requested` Outbox 在同一事务中发布，成功终态是 Proposal `verifying` / `index_pending`；M6 Retrieval 尚未完成，不能宣称 `completed`。
- 本仓库目前只有 Safe Writeback Node 和 API/Worker Composition；尚未实现 River dispatcher、Job Registry 或 retry runner，因此本期不得宣称自动异步领取和重试。

## Requirements

### R1. 审批与 Git 基线绑定

- Approval 必须记录服务端观察到的 Git HEAD；Git 不存在、detached、工作区或 index 非干净时不得批准正式写回。
- 写回必须重新校验 Proposal/Revision/Approval/Change Hash、目标文件 Base Hash、批准 Git HEAD 和持久化 Workflow Run/Node。
- 历史 Approval 缺少 Git HEAD 时不得降级 Apply，必须重新审批。

### R2. 双授权与 Durable Operation

- 文件和 Git 使用两份独立授权，且必须在任何文件副作用前完成签发；两份授权必须绑定同一 Workspace/Workflow/Proposal/Revision/Approval/Target。
- 授权消费不等于副作用完成。任何文件副作用前必须先创建或重放 Durable Writeback Execution。
- 同一 Workspace + Idempotency Key 和同一 Proposal Revision 只能有一个逻辑写回；完全相同请求重放已有执行，不同绑定返回冲突。

### R3. Workspace 锁、临时写与最终 CAS

- 写入目标仅允许 Workspace 内现有普通 Markdown 文件；拒绝绝对路径、路径穿越、symlink、设备文件、FIFO 和越界目标。
- 使用跨进程目标锁串行同一文件的系统写入；同目录创建随机临时文件，限制内容大小，校验 UTF-8、Markdown Parser、目标路径和 Change Hash。
- 临时文件写入、`fsync`、最终重新读取并比较 Base Hash 后，才可 `rename` 原子替换并同步父目录。
- 保留受控基线备份直到 Git Commit 与 DB Mapping 已确认；恢复也必须 CAS，不能覆盖用户后续编辑。
- POSIX 文件锁和最终 rehash 不能把外部非协作编辑变成内核级跨存储事务；任何无法证明结果的情况进入 `manual_recovery_required`，不得返回假成功。

### R4. Git Diff、Commit 与反向 Commit

- 写回前要求 Git 仓库存在、非 detached、HEAD 等于 Approval 基线、工作树和 index 干净。
- 文件替换后只允许目标路径发生变化；执行 path-scoped `git diff --check`，验证目标新 Blob Hash/内容与批准 Revision 一致。
- 只将批准 raw blob 通过受控 index record stage，使用 immutable tree + `commit-tree` + `update-ref expected-old` CAS 发布固定 Commit Message/Trailer（Proposal/Approval/Workflow/Writeback ID）；禁止仓库 filter、shell 拼接、Hook、push、reset、checkout 和任意参数。
- Commit 超时或结果未知时必须先按 Trailer/HEAD/Commit 内容判定，禁止盲目重复 Commit。
- 自动反向 Commit 只允许在 HEAD 仍等于系统生成 Commit 且工作区干净时执行；否则进入人工恢复。用户主动回滚仍需新 Proposal + Approval。

### R5. 数据库映射、状态与索引请求

- Proposal 增加乐观锁版本和受约束状态迁移：`approved → applying → applied → verifying`；Safe Writeback Execution 细化为 `prepared → file_prepared → file_applied → git_prepared → git_committed → verifying`，失败可进入 `needs_revision/apply_failed/compensating_file/compensated/publish_recovery_required/manual_recovery_required`。
- 持久化 Writeback Execution、Proposal Commit Mapping、步骤状态、Base/New Hash、批准/结果 Git HEAD、错误码、补偿与人工恢复标志；不保存 Credential、正文或绝对临时路径。
- Git Commit 映射与 `retrieval.revision.reindex_requested` Outbox 必须在同一 PostgreSQL 事务内发布，重复请求只产生一个事件；Publish 成功后仅表示 `verifying/index_pending`，清理 temp/backup 通过独立 cleanup finalize 完成且可重试。
- M5-04 成功终态是 `Proposal=verifying` 且 `index_request=pending`；M6 Retrieval 真正完成解析/索引/回归后才能进入 `completed`。

### R6. 故障恢复

- CAS 前冲突：无文件/Git/映射副作用，Proposal 进入 `needs_revision`。
- 临时写失败：删除临时文件，正式文件不变，Execution/Proposal 记录 `apply_failed`。
- 文件成功但 Git 失败：CAS 恢复旧文件并验证 Git/工作区回到基线；失败进入人工恢复并阻止该目标后续写入。
- Git 成功但 DB Publish 失败：保留 Commit，不恢复文件；使用 Commit Trailer 恢复 Mapping/Outbox，Workspace 进入只读恢复语义，禁止重复 Commit。
- 索引失败：不撤销 Commit；保持 `verifying/index_pending|stale`，继续使用旧 Active Index 并重试。
- 内容回归失败：满足严格 HEAD/clean 前提时创建反向 Commit，否则进入人工恢复。

## Acceptance Criteria

- [x] Approval Git HEAD、Proposal 乐观锁、Writeback Execution、Commit Mapping、Index Request/Outbox 和状态约束有前向迁移与真实 PostgreSQL 集成测试。
- [x] Filesystem Adapter 覆盖跨进程目标锁、temp/validate/fsync、最终 CAS、原子替换、备份恢复、路径/symlink/特殊文件和并发外部编辑。
- [x] Git Adapter 覆盖 clean/dirty/staged/untracked/detached、HEAD drift、path-scoped Diff、固定 Commit/Trailer、未知结果判定、重复提交和安全反向 Commit。
- [ ] Application/Saga 在任何副作用前建立 Durable Operation，严格消费双授权，并对重复投递、崩溃恢复和补偿保持幂等。
- [ ] Commit 与 Proposal/Revision/Approval/Workflow/Writeback 双向可查，Git Commit 后的 DB Publish 与索引 Outbox 原子落库。
- [ ] M5-04 不创建猜测 Retrieval Schema，不把 `index_pending` 返回为完成；Proposal 只推进到 `verifying`。
- [ ] 单元、Filesystem/Git/PostgreSQL 集成、故障注入、`go test -race ./...`、`go vet ./...`、`make test`、OpenAPI/Compose 与 Docker smoke 全通过。
- [ ] 架构、数据库、工具安全、故障恢复和产品文档同步说明本期范围与 M6 完成条件。

## Out of Scope

- FTS/Embedding/Rerank 的真实索引构建、Embedding 维度和 Active Index 切换；由 M6 Retrieval 实现。
- 用户主动回滚 UI、认证/Audit 完整产品、跨节点分布式锁和任意多文件事务。
- 自动三方合并；冲突只生成 Needs Revision，合并需新 Proposal Revision 并重新审批。
