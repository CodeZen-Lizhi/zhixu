# 文档文件历史与受控恢复：技术设计

## Module

新增 `internal/documenthistory` 查询模块。它通过窄端口组合 Git History、Article Revision 和 Proposal Commit，不拥有这些事实，也不写 Knowledge Timeline。

## Read Flow

1. 服务端以 Workspace + Document ID 解析当前 canonical relative path。
2. GitHistoryReader 校验 canonical top-level、attached current branch、path 和有界 limit/cursor。
3. 读取当前分支 path history、commit metadata 和 blob identity。
4. Repository 批量查询 commit → Article Revision/Proposal Commit 映射。
5. Application 验证 Workspace/path/commit、稳定排序与唯一性，投影 managed/external union。
6. 单独读取 worktree status/diff，作为 `CURRENT_CHANGE`，不写历史。

Cursor 绑定 Workspace、Document、path、branch/head、limit 和最后 commit position；HEAD 或 path 变化返回 stale，客户端重开首屏。

## Git Adapter

在 `internal/platform/gitcli` 增加独立只读 History client，不扩宽 Change Control GitRepository。允许的操作仅包括 bounded log、show/cat-file、diff 和 ancestry/object validation。固定禁 Hook、replace、pager/editor、签名与调用方 Git args。

Diff/Blob 设单项和整页 byte limit；超限返回稳定错误，不截断后伪装完整。Commit OID 按仓库 object format 校验。

## Restore Flow

Restore preview 读取目标 Commit Blob 与当前 approved HEAD Blob，生成稳定 diff/hash。Create command 提交 Workspace、Document、target commit、expected current head/document version、preview hash 和 Idempotency-Key。

Change Control 新增强类型 `RESTORE_DOCUMENT` Proposal，target/base/result binding 均来自服务端读取。批准后仍走既有 Approval → Safe Writeback → Git Commit → Reindex。外部目标 Commit 只作为 result source。

## API And UI

- `GET .../documents/{id}/history`
- `GET .../documents/{id}/history/compare?left=...&right=...`
- `POST .../documents/{id}/restore-previews`
- `POST .../documents/{id}/restore-proposals`

前端 `web/src/api/document-history.ts` 是唯一 strict decoder。Document 页面增加 History view，Query key 绑定 Workspace/Document/head/cursor；Monaco DiffViewer 复用现有安全卸载模式。

## Persistence And Compatibility

历史优先实时组合现有 Git/DB 事实，不建第二份 Commit 正文表。必要的 cursor projection 可重建。Restore Proposal 使用 additive schema/type migration；现有 Proposal 和 Timeline 不变。

## Risks

- 大历史/Diff：分页、输出上限、按需详情。
- Git/DB 漂移：Application 验证映射；缺映射只标 external，不猜测。
- TOCTOU：preview hash + expected HEAD/version，审批和写回继续 strict clean。
- 外部修改：dirty 时只读可用，恢复禁用。
