# M5-04C Git Writeback Adapter

## Goal

为 Safe Writeback 提供真实、可恢复且不夹带用户改动的 Git 副作用边界：在文件 CAS 成功后验证唯一目标 Diff，创建可从稳定 Trailer 反查的单路径 Commit；Commit 超时或结果未知时先查证而非重复提交；回归失败时只在严格 HEAD/clean 前提下创建反向 Commit。

## Background

- M5-04A 已持久化 approved Git HEAD、Writeback Execution、Git Commit/Parent/Diff Hash 和 Proposal Commit Mapping；M5-04B 已实现文件锁、最终 Base CAS、backup 与 RestoreCAS。
- 当前 `internal/platform/gitcli` 只实现 Workspace Status/Initialize，命令通过参数数组执行，但尚无 Change Control Git 领域端口、Diff/Commit/Trailer/Recovery/Reverse。
- 正式写回要求 Workspace 根本身就是 Git top-level、branch 非 detached、HEAD 等于审批基线、工作树与 index 干净；Workspace 创建阶段允许 dirty 的历史行为不能放宽本边界。
- 产品 PRD 要求 Commit Metadata 包含 Source Version，但当前 Proposal/Writeback 模型没有 Source Version 绑定；本任务以父任务已收敛的 Proposal/Revision/Approval/Workflow/Writeback 绑定为依据，不伪造 Source Version Trailer。后续若领域模型新增真实 Source Version 关联，再版本化扩展 Trailer。

## Requirements

### R1. Workspace 与 Git 基线

- 所有写操作只接受服务端 Workspace ID；Adapter 解析 canonical root，并要求 Git top-level 与 Workspace root 完全一致，拒绝嵌套在父仓库中的 Workspace。
- Inspect 必须区分 repo 缺失、unborn HEAD、detached HEAD、HEAD drift、staged、unstaged、untracked 和冲突状态；正式审批/Apply 只接受 branch 非空且全仓 clean。
- 拒绝 merge/rebase/cherry-pick/revert/bisect 等进行中状态；目标必须是 approved HEAD 中已跟踪的普通 blob，拒绝 symlink blob和 gitlink/submodule。全仓任一 tracked path 使用 assume-unchanged 或 skip-worktree 都会隐藏真实 dirty，v1 一律拒绝（即不支持 sparse checkout 写回）。
- 支持 SHA-1 与 SHA-256 Git object format（40/64 hex）；不假设固定 40 位 Commit。
- Git author identity 必须在副作用前可用；不在代码中硬编码或伪造用户身份。

### R2. 单目标 Diff 与内容绑定

- 文件 CAS 后 HEAD 仍必须等于 approved head，index 必须 clean，worktree 唯一变更必须是规范 target path；拒绝 rename、delete、untracked、冲突和任何其他路径变化。
- 所有 path 命令使用参数数组、全局 literal pathspec 与 `--` 分隔；支持以 `-` 开头、空格和 Unicode 的合法 Markdown 文件名，不允许 pathspec 注入。为保证 Trailer 单行可恢复，Git 写回额外拒绝 CR/LF、TAB 和其他控制字符路径。
- 执行 path-scoped `git diff --check` 和禁用 external diff/textconv 的 binary/full-index Diff；Diff Hash 为该稳定 Diff 字节的 SHA-256。
- 在任何会读取 worktree 的 Git 命令前扫描 tracked content filter，并对目标拒绝 filter/ident/text/eol/working-tree-encoding/diff 等会改变内容或 Diff 表示的属性；目标原始字节只用 `hash-object --no-filters` 绑定，不执行仓库 filter。

### R3. 安全、固定且可验证的 Commit

- Commit 前再次确认 HEAD、唯一 target Diff、Result/Diff/Blob 绑定；将批准原始字节通过 `hash-object -w --no-filters` 写成已验证 Blob，再用受控 NUL `update-index --index-info` 只 stage target，禁止 `git add` 执行仓库 clean filter。
- Commit 使用固定 subject 和稳定 Trailer：Operation、Writeback Execution、Proposal、Revision、Approval、Workflow Run、Workflow Node、Target Path、Result SHA-256、Diff SHA-256；不接受调用方提供任意参数或自由 Commit Message。Operation 区分同一 Execution 的 apply/revert，避免恢复查询把合法反向 Commit 当作重复冲突。
- 禁用 Hook、GPG signing、external editor、interactive prompt 和 pager；不执行 shell、push、fetch、remote、reset、checkout 或任意 Git 配置命令。
- staged target 先通过 `write-tree` 固化为 immutable tree，并再次验证 tree 相对 approved head 只改变 target、mode/blob/result/diff 全部匹配；随后用 `commit-tree -p approved` 创建固定 Commit 对象，最后以 `update-ref <branch> <new> <approved>` old-value CAS 发布，禁止普通 `git commit` 在并发 ref/index 漂移时夹带内容。
- 发布成功后验证新 Commit parent、HEAD、唯一 target、Commit Diff/Result/Trailer 和 repo clean；任何发布后的不确定或验证失败都进入 ManualRecoveryRequired，不返回假成功。
- stage 后若能证明 ref 未发布且 HEAD 仍为 approved，Adapter 只在当前 target index 仍为系统 Result Blob（或已为 Base）时恢复 approved mode/blob；发现用户新 staged blob、其他 staged path 或 HEAD 漂移时进入 ManualRecoveryRequired。

### R4. Commit 幂等与未知结果恢复

- 完全相同的重放先按稳定 Trailer 和完整绑定查找既有 Commit；找到且 parent/target/result/diff/trailers 全部匹配时返回 Recovered/Replayed，不创建第二个 Commit。
- Commit 进程超时、取消、信号或返回错误但可能已落盘时，Adapter 必须用独立短时恢复上下文查询 HEAD/Trailer/Commit 内容；确认成功则返回既有 Commit，无法证明时返回 `ManualRecoveryRequired`。
- 直接查询时，同一 Writeback Execution Trailer 指向不同绑定、存在多个匹配 Commit 或找到 Commit 但内容不符，返回 ConsistencyViolation；发布结果未知的恢复阶段无法 exact 证明时统一返回 ManualRecoveryRequired，禁止调用方误判为“未提交”。
- 查询范围只限当前 Workspace 当前分支可达历史的有界窗口，不扫描 remote、不切换 branch。

### R5. 严格反向 Commit

- 自动 Reverse 只允许 HEAD 仍等于系统生成 Commit、branch 非 detached、repo/index/worktree clean，且原 Commit 完整绑定当前 Writeback。
- 使用固定 `git revert --no-edit <commit>` 或等价安全序列，并应用与正式 Commit 相同的 Hook/signing/prompt 禁用策略；禁止 reset/checkout/history rewrite。
- Reverse 后验证 parent 等于被反向 Commit、只改变 target、目标 SHA-256 回到 Base Hash，并写入稳定 Revert Trailer。
- precondition conflict 不产生副作用；revert 结果未知或产生冲突现场时进入 ManualRecoveryRequired，不自动执行破坏性 abort/reset。

### R6. 错误、资源与兼容性

- Adapter 把 Git exit code/stderr 映射为稳定 `InvalidInput/NotFound/VersionConflict/PermissionDenied/DependencyUnavailable/RetryableFailure/ConsistencyViolation/ManualRecoveryRequired`，不向 API 泄露绝对路径或完整 stderr。
- 所有命令响应 context；恢复查询有固定上限；stdout/stderr 有大小上限，避免恶意仓库输出造成无界内存。
- 保持现有 Workspace `Status/Initialize` API 与行为兼容；新 Change Control Git 端口不得让 Agent/Eino 绕过 Proposal、Approval、Authorization 或 Durable Execution。

## Acceptance Criteria

- [x] Domain 定义 GitRepository、Inspect/Diff/Commit/Lookup/Reverse 值对象、不变量和稳定 sentinel，不依赖 os/exec/Git CLI。
- [x] GitCLI 通过 Workspace Repository 解析 canonical root，覆盖 repo missing/root mismatch/unborn/detached/HEAD drift 与 clean/dirty/staged/untracked/conflict。
- [x] DiffApproved 证明唯一 changed path、pathspec 安全、diff --check、Result Hash、Diff Hash、Blob ID 和 filter/attribute 安全。
- [x] CommitApproved 只 raw-stage target，禁用 hook/signing/editor/prompt，以 immutable tree + commit-tree + update-ref old-value CAS 发布固定 subject/trailers，并验证 parent/HEAD/path/blob/result/diff。
- [x] FindWritebackCommit 覆盖完全重放、commit-after-timeout、无匹配、多个/冲突 Trailer、内容不符和有界历史查询。
- [x] CreateReverseCommit 覆盖成功、HEAD drift、dirty/staged/untracked、内容冲突、revert conflict/unknown 和 Result 回到 Base。
- [x] 临时真实 Git repo 测试覆盖 SHA-1，并在本机支持时覆盖 SHA-256 object format；覆盖空格/Unicode/前导 `-` 路径和恶意 Hook/filter 配置。
- [x] fault injection 证明 Commit/Revert 已成功但命令返回错误时可恢复；无法证明时明确 `ManualRecoveryRequired`，不重复副作用。
- [x] `go test -race -count=20 ./internal/platform/gitcli ./internal/changecontrol/domain`、`go vet ./...`、`make test` 和 `git diff --check` 通过。

## Out of Scope

- 文件 CAS、数据库 checkpoint/publish、双授权消费和 Workflow Saga；由 M5-04B/D 负责。
- remote fetch/push、branch 创建/切换、merge/rebase、submodule/LFS、多文件 Commit 和用户手动回滚 UI。
- 阻止拥有本地仓库写权限的任意进程直接运行 Git；v1 只保证系统 Adapter 不执行未授权命令并在每一步复核事实。
