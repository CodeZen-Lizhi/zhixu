# 实施清单

1. [x] 新增 Ingestion Attempt/Projection/Span/Chunk migration、领域模型和 Repository；包含 Attempt 乐观锁、Projection/Span/Chunk 不可变和跨 Workspace/Artifact 约束。
2. [x] 新增 MIME/UTF-8/NUL/大小校验与逐文件错误分类；超限在扫描/重读/Parser 三层受控。
3. [x] 引入锁定版本 goldmark，完成 Markdown/TXT Parser Adapter 和原始位置映射；补齐 BOM、CRLF、Frontmatter、短表格、缩进代码块和取消检查。
4. [x] 完成确定性结构分块、oversized Warning、fingerprint 和按 Chunk Strategy/Schema 的幂等投影。
5. [x] 接入 Application Service、事务持久化、最小 API 和 Workflow Node Adapter；支持状态检查点恢复，并以“元数据→Attempt→受限文件读取”的顺序持久化读取失败、超限和取消。
6. [x] 执行 unit、race、PostgreSQL、Compose、API smoke、Go Review、SQL Review；验证命令与结果记录在任务日志和交付摘要中。

## 回滚

只新增前向迁移；Parser/Chunk 版本变化通过新 Projection/Chunk Strategy，不覆盖历史投影。goldmark 只在 Adapter 层可替换。
