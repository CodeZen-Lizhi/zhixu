# M5-02 后续实施清单

1. [x] A：同步 PRD、领域、数据库、检索、流程和接口文档。
2. [ ] B：实现 Content Artifact 不可变捕获、安全重读、扫描排除和 Source Version ID 返回。
3. [ ] C：新增 Ingestion Attempt、Parsed Document、SOURCE Revision、Source Span、canonical Chunk migration 与 Repository。
4. [ ] D：实现 MIME/UTF-8/NUL/大小校验和逐文件结果。
5. [ ] E：引入锁定版本 goldmark，通过 Parser Adapter 实现 Markdown/TXT、Frontmatter Warning 和原始位置映射。
6. [ ] F：实现确定性结构分块、oversized Warning、fingerprint 和幂等投影。
7. [ ] G：实现 Application Service、事务持久化、批次隔离和 API/Workflow Node。
8. [ ] H：执行单元、race、PostgreSQL、Compose、真实文件烟测和 Review。
