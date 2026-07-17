# Runbook：数据库、文件与 Git 不一致恢复

## 1. 触发

- Published Revision 无对应文件。
- Git HEAD 与数据库不符。
- Commit 成功但 DB Publish 失败。
- 文件被外部修改且映射缺失。
- Compensation 失败。

## 2. 第一响应

1. 系统进入 READ_ONLY_RECOVERY。
2. 向 Worker 发送 SIGTERM 并确认独立 `/readyz` 进入
   `WORKER_SHUTTING_DOWN`；等待 graceful `Stop` 到安全 checkpoint。若已超过 hard
   deadline 或 Worker 崩溃，保持 River Job、Workflow lease/Attempt 和 Writeback
   checkpoint，按 Workflow 恢复 Runbook 处理，不能用自制 polling 或手工完成 Job
   替代正式 River Runtime。
3. 保留所有临时文件和日志。
4. 记录：
   - Workspace。
   - Git HEAD。
   - Dirty Files。
   - Proposal/Workflow。
   - Last Known Consistent Marker。

## 3. 权威顺序

正式内容：

1. 用户当前文件。
2. Git 历史。
3. DB Revision 映射。

运行历史：

1. PostgreSQL。
2. Audit Export。

不得用 DB Chunk 覆盖用户文件。

## 4. 诊断矩阵

| 情况 | 恢复 |
|---|---|
| 文件与 Git 一致，DB 落后 | 重建 Revision/Commit 映射 |
| 文件变化未 Commit | 作为外部编辑新基线 |
| Git Commit 已确认存在，文件缺失 | 先验证完整 Trailer、parent、target、Blob/Diff 和工作区；仅在目标未被用户后续编辑时按受控恢复流程补回文件，否则进入人工恢复。DB Publish 失败时保留 Commit，禁止先恢复旧文件制造分叉 |
| DB 显示 Applied，无 Commit | 验证文件；创建恢复 Proposal 或回退状态 |
| Commit 后索引旧 | 重建索引 |
| 临时文件未知 | 比较 Hash，不自动覆盖 |
| Writeback Execution 无 Commit Mapping，但历史存在 exact `Zhixu-Writeback-ID` Commit | 验证完整 Trailer、parent、target、Blob/Result/Diff 后补 Mapping/Outbox；不得创建第二个 Commit |
| `update-ref` 结果未知且 exact Commit 是 HEAD | 标记 recovered，保留 Commit，继续 DB reconcile；Reverse 还需校验并 `revert --quit` 清理预期 REVERT_HEAD |
| `update-ref`/Commit 结果未知且 HEAD 仍为 approved、无 exact Commit | 结果仍未知，不得 Restore 文件、删除 backup 或盲目重试；保留 `git_prepared`/file intent，进入 ManualRecoveryRequired，由人工确认 Git、index 和工作区现场 |
| target index 已被用户再次 stage、存在其他 staged path 或 HEAD 已漂移 | 不覆盖 index/文件，进入 ManualRecoveryRequired 与 READ_ONLY_RECOVERY |
| Commit 存在但 parent/path/blob/diff/trailer 不符或候选不唯一 | 视为一致性破坏；保留 ref/index/worktree 现场，禁止自动选择或补偿 |

## 5. 恢复步骤

1. 导出诊断报告。
2. 选择 Last Consistent Commit。
3. 比较 Working Tree。
4. 使用 `git log --no-show-signature` 在当前分支有界历史内按 Writeback ID + Operation 查找候选，并逐个验证 raw Commit 对象；禁止 replace/graft 语义。
5. 对 `git_prepared` 只接受 exact Commit 或明确 NotFound；unknown、冲突候选、HEAD 漂移和 index 不安全都保持 READ_ONLY_RECOVERY，不执行文件补偿或新 Commit。
6. Git Commit 已确认存在但 DB 落后时，原子重建 Commit Mapping 与 `retrieval.revision.reindex_requested` Outbox；不得创建第二 Commit。
7. 为外部编辑创建 Source/Revision。
8. 重放 Knowledge Event Projection。
9. 标记 Stale Proposal。
10. 重建受影响索引。
11. 运行回归评测。
12. 清理 temp/backup 仅在 Publish/恢复证据确认后执行 cleanup finalize；清理失败保留 `verifying` 并重试。
13. 人工确认退出只读。

## 6. 禁止

- reset --hard。
- checkout/switch、强制 update-ref、删除未知 Commit/backup/index 证据。
- 强制覆盖用户文件。
- 删除未知临时文件。
- 手工修改 DB 状态但不留审计。

## 7. 验证

- 所有 Published Revision 有文件和 Commit。
- Git Working Tree 状态已解释。
- 默认检索只返回当前版本。
- Proposal/Workflow 无假 Completed。
- Audit 记录恢复操作。
