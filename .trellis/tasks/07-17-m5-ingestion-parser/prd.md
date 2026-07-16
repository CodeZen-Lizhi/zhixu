# M5-02C/D/E/F Ingestion Attempt、Parser 与 Canonical Chunk

## Goal

把不可变 Content Artifact 转换为可追溯、可复现的 Markdown/TXT Parse Projection、Source Span 和 canonical Chunk，并冻结 Ingestion Attempt 状态边界。

## Requirements

- 新增持久化 Ingestion Attempt，分离 `status`、`security_status`、失败阶段、Parser/Config/Schema/Chunk 版本和警告。
- `quarantined` 只能表达为 `status=validating + security_status=quarantined`；`retry_wait` 属于 Workflow；`chunked` 不等于 `indexed/ready`。
- Parser 只接收不可变字节，不读取路径；Markdown 使用 CommonMark + 显式 Table 扩展，TXT 按纯文本处理。
- BOM、Frontmatter、非法 UTF-8、NUL/伪二进制、空代码块和 CRLF 必须有确定性行为和 Warning/Failure。
- Source Span 使用原始字节 0-based 半开区间和 1-based 闭区间行号；Canonical Chunk 引用 Span，不伪造模型 Token 数。
- 标题/段落/列表/引用/表格/代码块等结构优先保持原子性；原子块超限保留完整 Chunk 并产生 `ATOMIC_BLOCK_OVERSIZED`。
- Parse Projection 和 Chunk 以内容、Parser/Config/Schema/Strategy 版本幂等复用；不同 Source Version 共享投影但保留 Provenance 映射。

## Acceptance Criteria

- [x] 空库迁移可重复执行，Attempt/Projection/Span/Chunk 外键、唯一键、状态 CHECK 和不可变约束成立。
- [x] Markdown/TXT parser 单测覆盖正常、边界、错误、原始位置和 frontmatter warning。
- [x] 分块单测覆盖确定性 fingerprint、原子结构、oversized warning 和空输入。
- [x] 非法 UTF-8/NUL/超限/取消请求不会产生成功投影。
- [x] 同 Artifact + 同版本重复解析复用 Parse Projection/Chunk；Chunk Strategy/Parser 版本变化不会污染既有投影。
- [x] PostgreSQL 集成测试和最小 API/Workflow smoke 通过。
