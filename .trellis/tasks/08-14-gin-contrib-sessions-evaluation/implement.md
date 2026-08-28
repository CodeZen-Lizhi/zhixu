# 实施计划

## 1. 固化研究证据

- 写入 `research/session-framework-coverage.md`：版本、许可证、模块依赖、核心/Store 源码行为和加权覆盖矩阵。
- 逐项关联当前 `internal/auth`、认证规格、真实 PostgreSQL 测试、`research/current-session-contract.md` 和 TODO 10 状态。

## 2. 记录正式决策

- 在开始实施前取得用户对“TODO 10 从本次关闭前置改为未来重评触发条件”的明确确认。
- 新增 ADR-0024，按 ADR-0019 七项要求记录不采用结论、用户确认、最小自研边界、维护责任、退出路径和重评触发条件。
- 更新 ADR 索引与 `.trellis/spec/backend/auth-security.md`，明确评估结论和禁止的第二 Store 路径。

## 3. 建立可重复合同检查

- 增加项目内静态合同测试，只读取 ADR 中的结构化决策/版本标记以及 `go.mod`、`go.sum`、`vendor/modules.txt`，验证未采用依赖没有进入 module/vendor。
- 合同检查不读取 Router 源码文本，不把内部 Composition 形状写成对外契约，也不以整文件快照代替行为测试。
- 增加真实 PostgreSQL 并发登录共存/单独撤销测试。
- 不修改 OpenAPI；将既有的全局 Auth `409`/OpenAPI 漂移记录为本任务不改变的已知缺口，避免只修一组 Auth 管理端点造成假对齐。

## 4. 回归验证

```bash
go test ./internal/auth/... ./internal/app/... ./cmd/api/...
go test -race -count=1 -timeout 60s ./internal/auth/...
make openapi-check
go vet ./internal/auth/... ./internal/app/... ./cmd/api/...
git diff --check
```

- 设置 `ZHIXU_TEST_DATABASE_URL` 时运行 `make auth-integration`；否则记录未覆盖原因，不伪装通过。
- 运行新增合同检查并确认 `go.mod`、`go.sum`、vendor 无未采用依赖。

## 5. 审查和收口

- 使用 `go-review` 检查 Go 合同测试；使用 `sql-code-review` 复核现有 SQL 依赖不变量未被削弱；使用 `code-review-and-quality` 检查跨层文档/契约一致性。
- 修复范围内问题并重新验证。
- 同步 `docs/roadmap.md` 和 2026-08-01 需求优化清单，准确标记 TODO 11“评估完成，不采用”，并记录 TODO 10 仍未完成、但其语义经用户确认改为未来重评触发条件。
- 更新任务结果、规格和 journal，提交并归档任务。

## 回滚点

- 本任务没有生产行为和 Schema 变化。ADR、规格、合同检查和状态文档可作为一个独立提交整体 revert。
