# 实施计划

## 1. 测试先行

- Application：默认/最大 limit、cursor binding/tamper、跨 kind/issue/workspace/limit、repository over-return。
- HTTP：详情新字段、两条 GET history route、严格 query、404/400/503 和既有 POST decisions 不冲突。
- PostgreSQL integration：>256 历史、同时间戳、末页、固定 statement 数、evidence sentinel 不加载、当前 fingerprint 精确绑定、EXPLAIN。
- Web API/query/page：严格 decoder、URL、Query Key、AbortSignal、加载更多、错误重试、Issue 切换清理。

## 2. 后端契约与存储

- 修改 `internal/health/application/read.go`，增加 history request/page/position、cursor 和 service 方法。
- 修改 `internal/health/adapter/postgres/read_repository.go`，以 `limit+1` 和 keyset 替换无界读取。
- 删除或改造 `loadObservations`/`loadDecisions`，确保不存在可被详情调用的无界路径。
- 当前 Issue lookup 按 fingerprint 精确加载非空 observation；损坏的空当前历史 fail closed，不从历史排序首项推断。

## 3. HTTP 与 OpenAPI

- 注册 observation/decision GET routes，复用现有严格 query/ID/limit 解析。
- 增加 page wire 与 detail cursor metadata。
- 更新 `api/openapi/openapi.json` 和 `api/openapi/check.mjs` 的精确契约检查。

## 4. Web

- 更新 `web/src/api/health.ts` 类型、decoder 和 history request 函数。
- 更新 query keys 与 `useInfiniteQuery` hooks。
- 在 `HealthPage.tsx` 的 Evidence Dialog 增加历史 tabs、分页和错误恢复；保持现有 decision mutation。
- 增加最小 CSS，保持 Dialog 内容可滚动、移动端无横向溢出。

## 5. 验证

```bash
go test -timeout=60s ./internal/health/application ./internal/health/http
go test -race -tags=integration -count=1 -p 1 -timeout=60s ./internal/health/adapter/postgres
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- --run src/api/health.test.ts src/features/health
npm run build --prefix web
make openapi-check
ZHIXU_TEST_DATABASE_URL='<test-db>' make collection-health-integration
git diff --check
```

真实浏览器 smoke 只有用户明确要求页面验证时才运行；本任务未获得该授权，以组件测试、typecheck、lint 和 production build 作为 Web 门禁。

## Review Gate

- `go-review`：context、cursor、error、handler/service/repository 边界、并发与资源释放。
- `sql-code-review`：参数化、keyset 条件、索引方向、limit+1、batch hydration、Workspace 隔离和 EXPLAIN。
- `code-review-and-quality`：跨层字段、前端 Query Key、失败恢复、兼容性和测试完整性。

## Rollback

- 后端 application/repository、HTTP/OpenAPI、Web 各自独立提交检查点。
- 任何回滚都保留硬 limit；禁止恢复旧无界 SQL。
