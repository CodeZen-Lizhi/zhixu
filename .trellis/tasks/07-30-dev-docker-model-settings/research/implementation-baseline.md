# W0 实施基线

记录时间：2026-07-30（Asia/Shanghai）。本文件只记录开始 W0-W7 实施时已经验证的事实，不表示任何后续门禁完成。

## Git 与任务

- Branch：`dev`
- HEAD：`54b83788507e9b05b3bad8615668a9009733f52a`
- Active task：`.trellis/tasks/07-30-dev-docker-model-settings`
- 工作树包含大量其他任务的已修改和未跟踪文件；本任务必须遵守 `research/worktree-overlap.md`，逐文件增量合并，不回滚用户改动。
- `task.py validate dev-docker-model-settings` 通过。现有 context manifest 对三个大型 spec 文件报告 32 KiB 注入截断警告；implement/check agent 仍必须按 manifest 和任务文档读取必要上下文。

## 工具链

- Go：`go1.25.4 darwin/arm64`
- Node.js：`v25.2.0`
- npm：`11.7.0`
- Docker Engine：`29.4.0`
- Docker Compose：`v5.1.2`

## 局部代码基线

以下命令在开始并行实施前通过：

```bash
go test ./internal/modelsettings/... ./internal/platform/models ./cmd/modelctl
npm run test --prefix web -- model-settings SettingsPage
```

前端结果为 2 个 test files、11 个 tests 通过。该结果不覆盖 PostgreSQL integration、API/Worker Composition Root、OpenAPI、Compose、真实 rollout 或浏览器验收。

## Docker 保护快照

`deploy` project 当前状态：

| Service | State | Evidence |
| --- | --- | --- |
| app | running | healthy，image `deploy-app` |
| postgres | running | healthy，image `pgvector/pgvector:0.8.5-pg18-bookworm` |
| proxy | running | healthy，image `deploy-proxy` |
| worker | running | healthy，image `deploy-worker` |

- PostgreSQL volume：`deploy_zhixu-postgres`，local driver。
- 主机同时运行 Aether 和多个知序测试 PostgreSQL 等其他容器；它们不属于本任务清理范围。
- Docker 总量：463 images / 33.06 GB，41 containers，107 local volumes；BuildKit cache 不纳入清理。

目标 smoke images：

| Namespace | Image rows | Unique IDs |
| --- | ---: | ---: |
| `zhixu-auth-smoke-*` | 165 | 165 |
| `zhixu-rag-smoke-*` | 75 | 75 |
| `zhixu-search-smoke-*` | 24 | 24 |
| `zhixu-tool-smoke-*` | 18 | 18 |
| Total | 282 | 282 |

开始实施时，282 个目标 Image ID 中被任意 container 引用的数量为 0。该事实必须在实际删除前重新计算，不能复用本快照直接删除。

## 并行文件所有权

- W1 agent：`internal/modelsettings/application/**`、Model Runtime loader/models、`internal/platform/models/**`。
- W2 agent：migration、modelsettings PostgreSQL adapter、crypto。
- W4 frontend agent：model settings API 与 Settings UI 文件。
- W5/W6 deploy agent：deploy scripts/Compose、root launcher 及必要文档入口；不执行一次性删除。
- W3 research agent：只写 `research/runtime-fence-audit.md`。
- Root：跨工作包整合、API/Worker Composition、HTTP/OpenAPI、modelctl、验证、Review、实际 Docker/浏览器门禁。

共享文件在同一时刻只允许一个 owner 修改；发现用户或其他 agent 的新改动时，以当前文件为事实源重新合并。
