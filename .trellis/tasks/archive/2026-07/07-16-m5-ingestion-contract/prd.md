# M5-02 摄取契约与状态基线

## Goal

收敛 Ingestion Run、Source Span、canonical Chunk 和 Source Version 不可变边界，为 Markdown/TXT 解析落地建立稳定契约。

## Requirements

- `SourceVersion` 继续作为不可变原始内容事实，不再承担后置安全、解析和索引状态的唯一事实源。
- Source Version 必须引用可重读的不可变内容捕获；仅保存用户可变路径和旧哈希不足以支持重解析。
- 新增持久化 `Ingestion Run/Attempt` 契约，记录安全、解析、分块状态、Parser/Chunk 版本、警告与失败。
- `Source Span` 建立为稳定实体，支持后续 Claim、Relation、RAG 引用和引用失效检测。
- canonical `Chunk` 属于 Ingestion 解析投影；Retrieval 的 FTS/Embedding/Index Version 只引用 Chunk，不复制解析生命周期。
- Markdown/TXT 的 Source Span 行号采用 1-based 闭区间，byte offset 采用原始字节 0-based 半开区间。
- 结构完整性优先于 Token 上限；超大代码块或表格保持完整并产生 Warning。
- 隔离、解析失败、索引失败和 Workflow 重试状态必须分离，UI 可以组合展示但数据库不得混成单一状态。
- 本任务仅冻结契约和同步文档，不直接实现 Parser、数据库迁移或业务 API。

## Acceptance Criteria

- [x] PRD、领域模型、数据库、检索和摄取流程文档对 Source Version、Span、Chunk 归属无冲突。
- [x] 状态机明确 `CHUNKED` 不等于 `INDEXED/READY`，`RETRY_WAIT` 归 Workflow。
- [x] 明确重复内容、单文件失败、隔离解除、超大结构块和 Parser 版本变化的行为。
- [x] 明确 managed source store 的事实归属、扫描排除、原子 create-only 和重建语义。
- [x] 后续数据库、Parser、Application、API 和测试任务均有可执行验收标准。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
