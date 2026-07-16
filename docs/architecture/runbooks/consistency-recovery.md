# Runbook：数据库、文件与 Git 不一致恢复

## 1. 触发

- Published Revision 无对应文件。
- Git HEAD 与数据库不符。
- Commit 成功但 DB Publish 失败。
- 文件被外部修改且映射缺失。
- Compensation 失败。

## 2. 第一响应

1. 系统进入 READ_ONLY_RECOVERY。
2. 停止 Worker 领取 Side Effect。
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
| Git Commit 存在，文件缺失 | 从 Git 恢复文件 |
| DB 显示 Applied，无 Commit | 验证文件；创建恢复 Proposal 或回退状态 |
| Commit 后索引旧 | 重建索引 |
| 临时文件未知 | 比较 Hash，不自动覆盖 |

## 5. 恢复步骤

1. 导出诊断报告。
2. 选择 Last Consistent Commit。
3. 比较 Working Tree。
4. 为外部编辑创建 Source/Revision。
5. 重建 Published Revision 映射。
6. 重放 Knowledge Event Projection。
7. 标记 Stale Proposal。
8. 重建受影响索引。
9. 运行回归评测。
10. 人工确认退出只读。

## 6. 禁止

- reset --hard。
- 强制覆盖用户文件。
- 删除未知临时文件。
- 手工修改 DB 状态但不留审计。

## 7. 验证

- 所有 Published Revision 有文件和 Commit。
- Git Working Tree 状态已解释。
- 默认检索只返回当前版本。
- Proposal/Workflow 无假 Completed。
- Audit 记录恢复操作。

