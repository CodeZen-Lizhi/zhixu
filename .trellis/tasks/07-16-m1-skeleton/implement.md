# M1 骨架实施计划

1. [x] 核对官方/registry 文档，锁定当前可验证的 Go、Node、React/Vite、pgx/chi/YAML 和 Compose 镜像版本。
2. [x] 初始化 Go module、配置、日志、PostgreSQL Adapter、API/Worker 和单元测试。
3. [x] 初始化 React/Vite/TypeScript/TanStack Query/Router、状态页面和前端测试。
4. [x] 建立 OpenAPI、Makefile、Dockerfile、Compose、迁移、配置样例和 CI。
5. [x] 集成前端静态资源、运行 Go/前端检查和 Compose Smoke。
6. [x] 执行 Go/通用 Review，修复问题并同步 README/Spec 真实代码链接。

## 并行边界

- 后端 Agent：`go.mod`, `go.sum`, `cmd/**`, `internal/app/**`, `internal/platform/**`, `internal/webassets/**`。
- 前端 Agent：仅 `web/**`。
- 部署/契约 Agent：`api/**`, `deploy/**`, `migrations/**`, `.github/**`, `Makefile`, `.env.example`。
- 主 Agent 负责版本、接口、集成、冲突和最终验证。

## Verification

```bash
go test ./...
go vet ./...
npm ci --prefix web
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
docker compose -f deploy/compose.yml config
docker compose -f deploy/compose.yml up -d --build --wait
curl --fail http://127.0.0.1:8080/livez
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8080/api/v1/system/status
docker compose -f deploy/compose.yml down -v
```

## Rollback

删除/回退本任务新建的骨架文件即可；不修改用户 Workspace、Git 历史或生产数据库。Compose 验证使用独立 Volume，并在测试后显式 `down -v`。
