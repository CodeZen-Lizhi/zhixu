# M0 产品与架构契约收敛

## Goal

在创建业务代码前，解决正式 PRD、API、数据库、AI 框架和认证设计之间已识别的冲突与缺口，使后续 OpenAPI、迁移、领域接口和测试只有一个可执行事实来源。

## Requirements

- PRD 和 API 统一使用 cursor + limit 分页；页面可做页码表现层，但公共契约不出现 offset/page 双事实源。
- 实时状态统一使用 SSE + Last-Event-ID；删除“WebSocket 或 SSE”的模糊表述。
- 明确 Smart Collection 表格范围和导出格式：本期没有 Excel/CSV 导入导出，不建设通用低代码数据库。
- 在数据库设计中补齐 Smart Collection、Artifact/Revision、Memory、Knowledge Event、Audit、Evaluation、Index Version、Human Task、Compensation、Tool Authorization 和 Node 运行版本字段。
- 记录 Eino 为 PoC 后采用的可替换 Adapter；PoC 通过前不能锁为核心依赖。
- 记录本地/自托管单用户认证边界：本地 localhost、Web Cookie Session、受限 API Token、CSRF/Origin、轮换和审计。
- 更新 ADR 索引和需求追踪，保持已有 accepted ADR 不被静默推翻。

## Acceptance Criteria

- [x] `docs/product/PRD.md` 不再出现公共 API `page` 或“WebSocket 或 SSE”冲突表述。
- [x] 表格/导出范围明确，不含无依据的 Excel/CSV 实现承诺。
- [x] `database-design.md` 覆盖 PRD 的持久化实体、关键版本字段和约束责任。
- [x] 新增 Eino 采用门禁 ADR 和单用户认证 ADR，ADR 索引同步。
- [x] 需求追踪矩阵覆盖 Eino、认证、导出和补齐的数据实体。
- [x] 文档相对链接、Mermaid、Markdown 和 git diff 检查通过。
- [x] 不创建业务源码、依赖或迁移。

## Out of Scope

- 不执行 Eino PoC，不锁定 Eino 版本。
- 不选择具体 Embedding 模型、中文 tokenizer 或 License。
- 不创建 OpenAPI/SQL/Go/React 文件；这些属于 M1/M2/M3。
