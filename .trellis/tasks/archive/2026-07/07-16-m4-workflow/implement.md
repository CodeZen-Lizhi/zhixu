# M4 实施清单

1. [x] 读取 Workflow、错误、数据库规范并冻结最小模型。
2. [x] 新增 Workflow/Node/Human/Outbox migration 和约束。
3. [x] 实现 domain/application/repository 与状态、租约、幂等测试。
4. [x] 接入 API/OpenAPI 和 Contract Test。
5. [x] Compose SQL smoke、race/vet、Review、文档和任务收口。

## Verification

```bash
go test ./internal/workflow/... ./internal/app
go test -race ./internal/workflow/...
go vet ./internal/workflow/...
node api/openapi/check.mjs
docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait
```
