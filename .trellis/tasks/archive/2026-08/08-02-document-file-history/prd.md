# 文档文件历史与受控恢复

## Goal

让用户无需理解 Git 命令即可查看、比较和恢复一个正式 Document 的文件版本，同时完整区分知序写回、外部 Commit 和当前未提交改动。

## Dependencies

- 无 Quick Capture/Profile 硬依赖；需要现有 Document/Article Revision、Proposal Commit、Change Control 和 Git CLI。
- 与 `08-02-git-remote-sync` 共用 Git CLI Runner 安全基础，但不得共享可写远端权限；若并行实施，必须先冻结公共 Runner 扩展方案。

## Requirements

- 当前分支上影响 Document 当前 canonical path 的 Commit 形成有界、稳定分页的版本时间线。
- Safe Writeback Commit 展示时间、作者/发起者、摘要、Diff、Article Revision、Proposal、Approval、Workflow、Writeback 和 Commit。
- 没有应用映射的 Commit 明确标记“外部变更”；缺少关系保持为空，不伪造审批。
- 工作树未提交修改显示为顶部“当前未提交改动”，可与 HEAD 比较，但不算历史版本。
- 用户可比较任意两个可读取历史版本，或历史版本与当前工作树；Diff 展示版本身份和变更来源。
- 恢复前展示相对当前版本的反向 Diff 和影响；恢复创建新的 `RESTORE_DOCUMENT` Proposal。
- 批准恢复后由 Safe Writeback 创建新 Commit；不执行 reset、checkout、ref move 或历史重写。
- 外部 Commit 的内容可以作为恢复 Proposal 的目标快照，但不能反向补造旧审批关系。
- dirty、detached、HEAD/path/blob 漂移或证据不可读时恢复 fail closed，不覆盖当前改动。
- 首版只承诺当前分支和当前 canonical path 的可验证历史，不展示远端未拉取分支或通用 Git 图谱。

## Acceptance Criteria

- [x] Document 详情可打开历史时间线并稳定分页。
- [x] Safe Writeback 版本展示 Revision、Proposal、Approval、Workflow 和 Commit 关联。
- [x] 外部 Commit 可见且标记明确，缺失审批关系为空。
- [x] 未提交工作树变化只显示为当前改动，恢复按钮被阻止且不会覆盖它。
- [x] 任意两个可读版本可比较，Diff 不混淆左右版本与来源。
- [x] 恢复先创建 Proposal，批准后追加新 Commit；原历史和后续 Commit 均保留。
- [x] 相同恢复幂等命令不会创建重复 Proposal 或 Commit。
- [x] 路径、Commit、Workspace 越界和输出过大被稳定拒绝，不返回部分可信历史。
- [x] 桌面和 `390x844` 下长路径、Commit、Diff 与状态不会遮挡或横向撑破页面。

## Out Of Scope

- 跨分支/远端历史、通用 Git 图谱浏览、rename follow。
- 应用内 Merge、Rebase、cherry-pick、force、reset 或 checkout。
- 修改 Knowledge Timeline 的领域事件语义。
