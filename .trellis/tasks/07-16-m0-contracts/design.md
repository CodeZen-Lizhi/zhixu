# M0 契约收敛设计

## 冲突处理原则

1. `docs/product/PRD.md` 是功能范围；accepted ADR 是已决架构约束。
2. 当 PRD 的表现层示例与架构公共契约冲突时，保留用户体验语义，统一技术契约并同步两处文档。
3. 新增 Eino 和认证决策通过新 ADR 表达，不修改已有 ADR 的历史结论。

## 变更范围

- 产品：分页、SSE、表格和导出边界。
- API：明确 cursor、SSE 和认证契约。
- 数据库：补齐缺失实体和安全/版本字段；本任务不锁最终 SQL 字段长度。
- AI：Eino 只能位于 Agent/Application/Adapter/Infrastructure，PoC 通过后采用。
- 安全：Web 使用 Cookie Session，API 自动化使用受限 Token；Approval Write Authorization 仍独立于登录凭据。

## ADR

- ADR-0013：Eino 采用必须通过 PoC，且只位于 Adapter/短流程层。
- ADR-0014：单用户认证采用 Web Session + 可撤销 API Token，本地模式默认 localhost。

## 验证

- 关键词冲突扫描。
- Markdown 相对链接检查。
- `git diff --check`。
- Trellis 上下文校验。
