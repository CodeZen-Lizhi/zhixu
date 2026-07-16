# M5-04B Filesystem CAS

## Goal

为 Safe Writeback 提供真实的本地文件系统副作用边界：在 Workspace 内串行协作写入者，安全准备已批准 Markdown 内容，执行最终 Base Hash/文件身份检查和原子替换，并在 Git 失败时通过受控备份做恢复 CAS；任何无法证明的结果必须显式进入人工恢复语义。

## Background

- M5-04A 已持久化 Writeback Execution 的 target/base/result/change hash、temp/backup locator 和恢复状态，但不执行文件副作用。
- 现有 `internal/platform/filesystem` 已有 `os.OpenRoot`、O_EXCL/0600 临时文件、流式 hash、file/directory sync 和安全 Artifact Capture，可复用底层能力。
- 现有 `Root.Resolve/Hash` 会跟随 Workspace 内 symlink，适合受控读取，不足以证明写目标本身不是 symlink/特殊文件。
- Go 1.25 `os.Root.Rename` 可在根内执行 rename，但 advisory flock、rehash→rename、fsync 与底层文件系统不能构成文件/Git/DB 的单一原子事务。

## Requirements

### R1. Workspace 与目标安全边界

- 只允许服务端 Workspace ID 解析出的 canonical root；目标必须是规范相对 POSIX 路径、现存 `.md`/`.markdown` 普通文件。
- 拒绝空路径、绝对路径、NUL、反斜杠、`.`/`..`、父目录 symlink、目标 symlink、目录、FIFO、socket、设备文件、跨 device/mount 目标和非当前进程 owner 的目标。
- 使用 `os.OpenRoot` 限定根，并对父链与目标显式 `Lstat`；不能把 `EvalSymlinks` 或 `os.Stat` 当成写安全证明。

### R2. 跨进程目标锁

- 使用 `.knowledge/locks/<device-inode>.lock` 持久锁文件与 `flock` advisory exclusive lock；锁目录逐级拒绝 symlink/非目录并使用 0700，锁文件使用 0600。
- 以目标 `device+inode` 生成锁身份，避免 macOS 大小写/Unicode 别名指向同一文件却获得不同锁。
- 获取锁必须响应 context 取消/超时；同一目标跨进程串行，不同目标可并行；释放锁不删除锁文件，进程退出可释放内核锁。
- 产品只承诺本地 POSIX 文件系统上的协作写入者串行，不承诺阻止绕过锁的本地进程或网络文件系统一致性。

### R3. Prepare 与内容验证

- Prepare 使用结构化请求绑定 Execution ID、Expected Base Hash、Approved Change Hash 和内容；Adapter 生成 locator，调用方不能指定任意 temp/backup 路径。
- 临时文件与目标同目录，随机不可预测，`O_EXCL|0600`；流式写入并限制最大 10 MiB。
- 校验 UTF-8/伪二进制/Markdown Parser、Result Hash，以及 `ComputeChangeHash(target,base,content)==approved_change_hash`。
- 顺序固定为写入 → `fchmod` 保留原 POSIX mode bits → `File.Sync` → close；只承诺保留已测试的 mode bits，不承诺 ACL/xattr/owner/group 全量复制。
- 临时/备份命名使用稳定 Git exclude pattern，避免 Git clean 检查将内部文件视为用户改动。

### R4. CommitCAS 与原子替换

- 持锁后重新 `Lstat/Open/Fstat`，要求目标 device/inode 与 Acquire 时一致，并重新读取/复制/Hash 验证 Expected Base Hash。
- 备份使用同目录独立副本，不使用 hardlink；备份写入、hash、`File.Sync` 后才能替换目标。
- 使用同文件系统 rename 原子替换，替换后同步父目录并重新验证目标为普通文件且 hash 等于 Result Hash。
- CAS 前冲突不得替换正式文件；rename/sync/结果验证无法证明时返回 `ManualRecoveryRequired`，不得静默成功。
- 不宣称阻止 rehash→rename 极小窗口内的非协作外部编辑，也不宣称断电级 durability。

### R5. RestoreCAS、Cleanup 与幂等

- Restore 仅在当前目标仍为系统 Result Hash 且受控 backup 仍为 Base Hash/普通文件/同 Execution locator 时执行；用户后续编辑时拒绝覆盖。
- Restore 使用 backup 的独立副本或受控 rename 恢复，随后同步父目录并验证 Base Hash；重复 Restore 在目标已为 Base Hash 时返回 replay，不产生第二次副作用。
- Cleanup 只删除当前锁生成的受控 temp/backup，并同步父目录；缺失文件保持幂等，篡改/symlink/特殊文件不得删除。
- Close 释放 advisory lock 与 Root/FD；未 Commit 的 temp 可清理，已 Applied 的 backup 必须保留到 Git Commit 与 DB Publish 已确认。

## Acceptance Criteria

- [x] Domain 定义 `WorkspaceStore`、`TargetLock`、Prepare/Applied 类型与稳定错误语义，不依赖 os/syscall/parser 实现。
- [x] LocalFS Adapter 复用 Workspace Repository 与 `os.OpenRoot`，实现目标安全检查、device/inode 锁、context-aware flock 和不同目标并行。
- [x] Prepare 使用真实 Markdown Validator，覆盖 UTF-8/二进制/空内容/大小/Change Hash/Result Hash，temp 权限与目标 mode 保留。
- [x] CommitCAS 覆盖最终 hash+identity 检查、独立备份、原子 rename、file/parent sync、结果复核和错误分类。
- [x] RestoreCAS/Cleanup 覆盖成功、重放、用户并发编辑、backup 缺失/篡改、symlink/特殊文件和清理失败。
- [x] 测试覆盖绝对/穿越/NUL/反斜杠、父/目标 symlink、非 Markdown、目录/FIFO/socket、跨 device（可注入）、owner/mode 边界。
- [x] helper subprocess 证明跨进程锁竞争、取消和进程退出释放；20 轮 race 并发无死锁或资源泄漏。
- [x] fault injection 覆盖 temp sync、backup sync、rename、parent sync、结果验证失败；不确定结果明确为 `ManualRecoveryRequired`。
- [x] `go test -race ./internal/changecontrol/adapter/localfs ./internal/changecontrol/domain ./internal/platform/filesystem`、`go vet ./...`、`make test` 和 `git diff --check` 通过。

## Out of Scope

- Git clean/HEAD/Diff/Commit/Reverse 与 Application Saga；分别由 M5-04C/D 完成。
- 严格内核 compare-and-swap rename、阻止任意本地进程、NFS/SMB 锁保证、断电级 durability。
- ACL、xattr、所有者/组、Finder metadata、Windows ACL 全量保留。
- 多文件事务、自动三方合并和跨节点分布式锁。
