# Authoring GORM 实施规范路由

## 必须保持的项目契约

- `backend/authoring-contract.md`：Working Draft 可恢复、Freeze 才追加正式 Revision、Publication 只在 exact `proposal_commit` 后最终化、Restore 只追加历史。
- `backend/database-guidelines.md`：显式列、全部参数化、稳定 cursor、有界批量、数据库约束优先、事务 owner 清晰、禁止无界查询/N+1/改写历史。
- `backend/error-handling.md`：稳定 Foundation kind/code/retryability，内部 cause 可诊断但外部不泄漏实现或敏感值。
- `backend/directory-structure.md`：GORM 仅位于 PostgreSQL Adapter；Composition Root 构造依赖，Application/Domain 不依赖实现。
- `backend/logging-guidelines.md`：SQL 参数、正文、路径、凭据、DSN 和 Secret 不进入日志或错误；沿用平台安全 logger。
- `backend/quality-guidelines.md`：真实 PostgreSQL、并发、回滚、fault、migration/trigger 和独立 Review 证据缺一不可。
- `guides/cross-layer-thinking-guide.md`：核对 API/Worker -> Authoring -> Change Control/Artifact/Organizing 全链路和 Final 切换面。
- `guides/code-reuse-thinking-guide.md`：优先复用 Foundation GORM/UoW 和既有纯 validation/scanner；避免复制领域规则，也不为复用引入弱类型大抽象。

## 本 child 的解释

- 成熟框架选择已由父任务锁定为 GORM + 官方 postgres driver + Foundation shared Pool，不在模块内引入替代 ORM/生成器。
- GORM 只拥有连接/事务与 Raw execution；Authoring 的锁、状态机、receipt、Proposal proof 和 Restore closure 仍由项目代码与 PostgreSQL constraint/trigger 拥有。
- 本阶段只新增未接 Composition 的 Adapter。TODO 9 真实库证据缺失时保持 `in_progress`，不得以 unit/compile 代替验收。
