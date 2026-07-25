# M10 认证与安全闭环实施清单

## 1. 实现顺序

1. 审计并收尾现有 `internal/auth` domain/application/postgres/http 实现，先修复编译和单元测试。
2. 增加 HTTP 测试：Bootstrap、Cookie 属性、Origin/CSRF、Bearer、Scope、撤销和稳定错误。
3. 增加 PostgreSQL 集成测试：迁移、摘要落库、原子认证、过期/撤销、损坏 Scope JSON 与并发竞态。
4. 将认证配置接入 `internal/platform/config`、`.env.example`、Compose，并实现启动安全校验。
5. 接入 `cmd/api` 与 `internal/app/router`，明确公共端点和业务端点认证门禁，保持写授权 seam 不变。
6. 更新 OpenAPI、前端 API 调用/认证恢复逻辑及必要 UI 状态。
7. 执行安全负测、全量回归、Compose 浏览器/API smoke、文档同步。
8. 执行主 Agent `go-review`、`sql-code-review`，再由独立只读审查 Agent 复核并修复后复验。

## 2. 主要影响文件

- `internal/auth/**`
- `internal/platform/config/**`
- `internal/app/router.go`
- `cmd/api/main.go`
- `api/openapi/**`
- `web/src/api/**` 与认证相关 UI
- `.env.example`, `deploy/compose.yml`, `README.md`
- `migrations/00034_learning_ops_auth.sql` 及 migration integration tests

## 3. 验证命令

```bash
gofmt -w internal/auth
go test ./internal/auth/...
go test ./internal/platform/config/... ./internal/app/... ./cmd/api/...
go test -race ./...
go vet ./...
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
make openapi-check
docker compose -f deploy/compose.yml config
```

真实 PostgreSQL 集成与 Compose smoke 使用仓库现有测试入口；若缺少统一入口，本任务补充到 Makefile 并在任务验收记录实际命令与输出。

## 4. Review 门禁

- 认证绕过、Bootstrap Token 泛化、Cookie/Origin/CSRF 错误、Scope 扩权、明文泄漏、撤销竞态均视为阻断问题。
- SQL 必须参数化；认证与撤销的原子性必须由真实 PostgreSQL 测试证明。
- 不允许用认证替代 Proposal/Approval/Write Authorization。
- 不允许以开发默认值让非 loopback 部署静默处于未认证状态。

## 5. 回滚点

- 代码回滚到上一兼容镜像；保留新增表，不执行生产 Down。
- required 模式出现故障时只能通过受控配置恢复到 loopback-only disabled 开发模式，不能对外网监听关闭认证。

## 6. 验收记录（2026-07-23）

- [x] Bootstrap 只能交换 HttpOnly、SameSite=Strict Session；明文 Bootstrap、Session 与 API Token 不落库、不进入列表或日志边界。
- [x] Session/API Token 的签发、认证、过期、撤销、数据库时钟 last-used、损坏 Scope 与撤销竞态由单元、HTTP 和真实 PostgreSQL `-race` 测试覆盖。
- [x] Cookie unsafe 请求的精确 Origin/CSRF 校验、Bearer 优先级、重复 Authorization 与 Scope 扩权均有负测；认证不替代 Proposal/Approval/Write Authorization。
- [x] `required` 的 Token/Origin/Cookie 启动条件与 development loopback `disabled` 均由配置测试锁定；Compose 守卫拒绝缺失、wildcard 或非 loopback 的 host port mapping。
- [x] Router、OpenAPI、`.env.example`、Compose、README 与前端严格 decoder/Auth Boundary 已同步；健康、readiness、系统状态和 Bootstrap 交换保持明确公共边界。
- [x] 主审已按 `go-review`、`sql-code-review`、`code-review-and-quality` 完成五轴检查；独立只读审查两轮复验 Compose 端口守卫，未留下 Critical/Required 问题。

### 实际验证

```text
go test -race -timeout 60s ./...                         PASS
go vet ./...                                             PASS
npm run lint --prefix web                                PASS
npm run typecheck --prefix web                           PASS
npm run test --prefix web                                PASS (52 files / 612 tests)
npm run build --prefix web                               PASS
make openapi-check                                       PASS
make compose-check                                       PASS
python3 -m py_compile deploy/compose_auth_check.py       PASS
git diff --check                                         PASS
make compose-auth-smoke                                  PASS
ZHIXU_TEST_DATABASE_URL=<disposable pgvector DB> make auth-integration  PASS
```

Compose smoke 使用独立 project、临时卷和随机 loopback 端口，验证匿名拒绝、Bootstrap 交换、Session、
Origin/CSRF 拒绝、受限 API Token 创建/元数据/撤销与登出。浏览器烟测已在桌面和 `390x844` 确认 development
disabled 工作台与 Settings 正常渲染，console 无错误、无空白或文本重叠；临时 Compose 资源已清理。
