# Workspace 实施与 Review 规范路由

## 实施前完整读取

- `.trellis/spec/backend/index.md`：后端模块边界与质量门禁。
- `.trellis/spec/backend/database-guidelines.md`：事务、锁、查询、分页、SQLSTATE 与数据库验证。
- `.trellis/spec/backend/workspace-root-grant.md`：root identity、control/runtime lock order、DB time、capability 与真实 PostgreSQL 门禁。
- `.trellis/spec/backend/capture-profile-contract.md`：Capture/Source/Artifact/Version/outbox/receipt 的共同事务与 owner 边界。
- `.trellis/spec/backend/logging-guidelines.md`：root path、SQL 参数、DSN、Secret 和 error 脱敏。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：Workspace、Capture、Audit、rootgrant、cmd Composition 跨层数据流。
- `.trellis/spec/guides/code-reuse-thinking-guide.md`：复用 legacy validation/scanner/SQL事实，避免双实现漂移。

`get_context.py` 注入只用于定位，不替代完整文件读取。实施开始时使用 `trellis-before-dev` 重新读取适用规范，并按具体修改加载 Go Review 的 API/data、concurrency/performance/security 与 quality-gate references。

## Review 必查不变量

- Application/Domain 不新增 GORM、database/sql、pgx 或弱类型 transaction。
- 所有 transaction advisory/row locks、DB time和 Audit append 使用同一个 live scope。
- legacy Capture writer和生产 Composition未被提前切换。
- rootgrant managed failure不 fallback direct；root path必须经 capability resolver重新验证。
- Source/Control/Runtime/Git 的 CAS、exact replay、constraint-name和 retryability没有因 GORM默认行为改变。
- TODO 9 不可用时 task保持 `in_progress`、PRD AC不勾选、不归档。
