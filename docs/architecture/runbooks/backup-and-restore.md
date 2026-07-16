# Runbook：备份与恢复

## 1. 适用

- 定期备份。
- 应用升级前。
- 迁移机器。
- 灾难恢复。

## 2. 备份内容

- Workspace。
- .git。
- PostgreSQL。
- 非敏感配置。

## 3. 一致备份

1. 在 UI 开启 Maintenance Mode。
2. 停止新 Proposal Apply 和新 Side Effect。
3. 等待当前写回到安全检查点。
4. 记录 Backup Marker：
   - 时间。
   - Git HEAD。
   - DB Schema Version。
   - Active Index Version。
5. 备份 Workspace 和 Git。
6. 执行 PostgreSQL 逻辑或物理备份。
7. 验证备份文件大小和校验和。
8. 关闭 Maintenance Mode。

## 4. 验证

- Git fsck/status。
- 随机打开 Markdown/PDF。
- 数据库恢复到临时实例。
- 查询 Backup Marker。
- 检查 Proposal/Workflow/Relation/Review 数量。

## 5. 恢复顺序

1. 停止 API/Worker。
2. 恢复 Workspace/Git 到新目录。
3. 恢复 PostgreSQL。
4. 配置新路径。
5. 启动 Migration Check，不执行写入。
6. 运行 Consistency Check。
7. 检查 Active Index。
8. 执行 Smoke：
   - 浏览文档。
   - Keyword Search。
   - Proposal 查询。
   - Workflow 查询。
9. 启动 Worker。
10. 关闭 Read Only。

## 6. 仅文件/Git恢复

- 重建 Document/Chunk/FTS/Vector。
- Confirmed Relation、Approval、Review 等需要 DB 备份或元数据导出。
- 在恢复完成前明确标记历史不完整。

## 7. 停止条件

- Git HEAD 与 Backup Marker 不同。
- DB Schema 高于应用支持。
- Workspace 缺少正式文件。
- Consistency Check 出现不可解释 Commit。

出现停止条件，保持只读并进入 [一致性恢复](consistency-recovery.md)。

