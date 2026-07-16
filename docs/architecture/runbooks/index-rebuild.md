# Runbook：索引重建与切换

## 1. 触发

- Embedding/Dimensions 变化。
- Chunk Strategy 变化。
- 索引损坏。
- 大量 Stale Projection。
- 检索评测退化。

## 2. 前置

- Active Index 可继续服务。
- 确认磁盘容量。
- 确认 Model/Embedding 可用。
- 记录新 Index Version 和配置。

## 3. 增量重建

1. 计算受影响 Revision。
2. 创建 Index Build Workflow。
3. 解析/Chunk（必要时）。
4. 批量 Embedding。
5. 写入新版本投影。
6. 校验数量、Span、维度。
7. 运行检索评测。
8. 激活受影响范围。

## 4. 全量重建

1. 限制一个全量任务。
2. 创建独立 Index Version。
3. 分页读取当前 Published Revision。
4. 批量构建 FTS/Vector。
5. 记录断点。
6. 验证覆盖率。
7. 运行完整评测。
8. 原子切换 Active。
9. 观察。
10. 归档旧版本。

## 5. 失败

- Embedding Rate Limit：Retry Wait。
- Worker Crash：断点恢复。
- 维度不一致：失败整个版本。
- 评测失败：不激活。
- 磁盘不足：停止并保留 Active。

## 6. 回滚

- 切换 active_index_version 到上一 Ready。
- 不删除失败版本直到诊断完成。

## 7. 验证

- Published Revision 覆盖率 100%。
- 无 Draft/Historical 默认索引。
- Source Span 可打开。
- Recall/Citation 不低于门禁。
- P95 满足预算。

