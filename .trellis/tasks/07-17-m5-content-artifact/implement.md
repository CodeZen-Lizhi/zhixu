# 实施与验证

1. [x] Domain、Filesystem、Repository、Migration、API 和前端契约实现。
2. [x] 补充正常、边界、失败、安全和并发测试。
3. [x] 执行 Go、Web、OpenAPI、Compose 和真实 PostgreSQL 验证。
4. [ ] 主 Agent 完成最终 diff Review、归档和提交。

## 回滚

代码回滚使用前向修复；数据库迁移只允许在 disposable 环境执行 Down，生产通过后续 Expand/Backfill/Cutover 迁移，不删除历史 SourceVersion 或 Artifact。
