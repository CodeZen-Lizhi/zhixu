# 实施结果

## 交付结果

- Health Issue 详情中的 observation 与 decision 历史均固定为首屏 25 条，并返回各自 `has_more`/`next_cursor`。
- 新增两类 Workspace-scoped 历史端点，支持默认 25、最大 100 的 HMAC opaque cursor 分页；cursor 绑定 Workspace、Issue、history kind、limit 与位置。
- 当前 `latest_observation` 改为按 Issue 当前 fingerprint 精确读取的非空快照，不再从历史排序首项推断。
- PostgreSQL 使用 `(timestamp DESC, id ASC)` keyset、`limit+1` 与单次 evidence batch hydration；未新增 migration 或索引。
- OpenAPI、Go HTTP wire、TypeScript runtime decoder 与前端历史 Tabs 同步升级；详情首屏直接注入 infinite-query cache，避免首次切换 Tab 重复请求。
- 阶段 0 的架构质量基线脚本、测试、Make target 与规范场景保持通过。

## 验证证据

- `go test ./cmd/api ./internal/health/...`：通过。
- `go vet ./cmd/api ./internal/health/...`：通过。
- `go test -race ./internal/health/application ./internal/health/adapter/postgres ./internal/health/http`：通过。
- `go test -tags=integration -run '^$' ./internal/health/adapter/postgres`：integration tag 编译通过。
- 真实 PostgreSQL 定向 integration 连续执行两次：均通过；fixture 为 261 条 observation、260 条 decision，覆盖跨页去重/无遗漏、固定 statement 数、当前 fingerprint 与既有索引 EXPLAIN。隔离数据库已删除并确认不存在。
- `npm --prefix web test -- src/api/health.test.ts src/features/health/queries.test.tsx src/features/health/HealthPage.test.tsx`：3 个文件、20 个用例通过。
- `npm --prefix web run typecheck`、`npm --prefix web run lint -- --quiet`、`npm --prefix web run build`：通过；production build 仅保留仓库已有的大 chunk 警告。
- `node api/openapi/check.mjs`：通过。
- `python3 -m unittest deploy.architecture_quality_baseline_test`：10 个用例通过。
- `make architecture-quality-baseline`：通过。

## Review 结果

- 修复 PostgreSQL `SELECT id::text ... ORDER BY id` 按文本别名排序的问题，改为表限定的原生 UUID 排序列。
- 为混合方向 keyset 增加等价的时间范围条件，使深页 cursor 进入既有索引 `Index Cond`。
- evidence 查询改为参数侧 `text[]::uuid[]` 转换，避免对索引列 cast。
- 当前 observation 的读取与写侧回填统一改为精确 fingerprint，消除“最新时间即当前状态”的错误假设。
- 修复前端严格 decoder 绑定、Dialog 宽度选择器和历史首屏重复请求，并补齐 limit 边界、cursor 漂移、坏 repository projection、同时间戳分页和缓存复用测试。

## 边界与回滚

- 按项目规则，未在用户未明确授权时启动服务或执行真实浏览器 smoke；Web 由组件测试、类型检查、lint 和 production build 覆盖。
- 未执行全仓测试或全量 integration；验证聚焦于 `cmd/api`、Health 跨层和阶段 0 基线。
- 回滚可以按后端、HTTP/OpenAPI、Web 分层进行，但必须保留服务端硬上限，不能恢复无界历史 SQL。
