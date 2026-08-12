# Gin HTTP Migration Implementation Plan

## Preconditions

- [x] 用户持续执行指令确认按最终规划进入实现阶段，并已运行 `task.py start`。
- [x] 重新读取 `prd.md`、`design.md`、本文件和 manifests，确认任务状态为 `in_progress`。
- [x] 保存当前用户脏改文件清单和 `api/openapi/openapi.json` 基线 hash；任何重叠修改只做增量合并。
- [x] 记录当前分支 `dev` 到任务元数据，禁止把无关脏改加入任务提交。

## 2026-08-12 实施状态（以本节为准）

- [x] Phase 1 的 route inventory、Gin engine 默认差异和迁移相关基线测试已落地；当前 inventory 精确覆盖 183 个
  OpenAPI operation，初始规划中的 182 是模型 activation endpoint 加入前的历史基线。
- [x] Phase 2 的 Gin dependency、标准库 Handler bridge、validator 与边界测试已落地。
- [x] Phase 3-5 的唯一 Gin composition、middleware/auth、领域 route batches、strict JSON、SSE、上传下载和
  response-started recovery 已落地，并完成定向 race/vet/OpenAPI 验证。
- [~] Phase 6 的 Chi 清理、tidy、vendor 解析已完成；生产代码无 Chi，且生产 Domain/Application/Repository/Workflow
  不依赖 Gin。一个 Application integration test 仍 import Gin，所以原“测试也无 Gin import”的严格 AC 不关闭。
- [ ] Phase 7 的全仓 Go/race/vet、聚合 Compose 与浏览器 smoke 未作为本次定向收口执行，见 `prd.md` 的证据和盲区。
- [ ] Phase 8 的文档/Trellis 同步由 `08-11-docs-trellis-sync` 独立任务执行；本任务不因实现提交而自动完成或归档。

## Phase 1. Freeze Baseline

- [ ] 记录 25 个 Handler、179 个 Handler operation、3 个 app operation（livez/readyz/system status）、154 OpenAPI path/
  182 operation 和可选 `/metrics`。
- [ ] 增加 runtime route inventory 测试，规范化 Gin `:param` 为 OpenAPI `{param}`，精确比较 method/path 集合。
- [ ] 增加 Engine 默认差异测试：尾斜杠/双斜杠/编码参数不漂移、404/405 使用 Problem、unsupported method、`Allow` Header、
  trusted proxy spoof、静态 fallback、可选 metrics。
- [ ] 先运行现有 Router/Auth/SSE/strict JSON/OpenAPI 基线，失败时区分迁移回归与用户并行改动。

Validation:

```bash
go test ./internal/app ./internal/auth/http ./internal/events/http ./internal/httpapi
make openapi-check
```

Rollback point: 仅删除新增基线测试和任务自有 shared helper，不触碰业务 Handler。

## Phase 2. Shared Gin Boundary And Dependency

- [ ] 将 `github.com/gin-gonic/gin v1.12.0` 加入 `go.mod`；暂不删除 Chi，待所有调用方迁移后统一 tidy/vendor。
- [ ] 在 `internal/httpapi` 增加 Gin Handler bridge：复制 route params 到标准 `Request.PathValue` 后调用官方 `gin.WrapF`。
- [ ] 增加 validator helper 和并发/边界测试；只为已锁定通用约束添加 `validate` tag 和原错误映射。
- [ ] 验证 bridge 保留 Header、status、`http.Flusher`、request context 和 path value。

Validation:

```bash
go test ./internal/httpapi
go test -race ./internal/httpapi
```

Rollback point: 删除 Gin bridge/helper 和 Gin direct dependency，现有 Chi Router 不受影响。

## Phase 3. Composition, Middleware And Auth

- [ ] 用 `gin.New` 重写 `internal/app/router.go`，显式配置 redirect、raw path、405、proxy、NoRoute/NoMethod 和 static fallback；
  不使用 `gin.Default`、Gin Recovery 或 Gin StaticFS。
- [ ] 将 no-store、request ID、trace、request log、panic recovery 改为 Gin Middleware；用 Gin-aware response-start tracker
  替换 Chi `statusWriter`，保留 Flusher/Hijacker/Pusher/Unwrap。
- [ ] 保持路由日志 `{param}` 模板格式和敏感信息边界。
- [ ] 将 `internal/auth/http` 的 routes 和 Middleware 改为 Gin，认证成功仍通过标准 request context 传 Principal。
- [ ] 更新 app/auth/recovery tests，覆盖 Middleware 顺序、Abort、无输出与 200/204/Write/Flush/NoRoute response-started panic、
  编码 PathValue 和 `http.ErrAbortHandler`。

Validation:

```bash
go test ./internal/app ./internal/auth/http
go test -race ./internal/app ./internal/auth/http
```

Rollback point: app/auth/shared boundary 可作为一个整体回退；不保留可运行的双 Router fallback。

## Phase 4. Domain Route Batches

对每个 Handler：将 `Routes` 改为 `gin.IRouter`、route method 改为 Gin 大写 API、动态参数统一为 OpenAPI snake_case 后
把 `{param}` 改为 `:param`、注册 `httpapi.GinHandler`、把 `chi.URLParam` 改为对应 canonical `request.PathValue`，同步迁移该模块测试 Router。

### Batch A

- [ ] Workspace、Workflow、Change Control、Collection。
- [ ] Health、Ingestion、Retrieval、Graph/Candidate。

### Batch B

- [ ] Conversation、Events、Knowledge、Artifact、Authoring。
- [ ] Capture、Organizing、Document History、Git Sync。
- [ ] Model Settings 由主会话在用户现有未提交改动上增量迁移。

### Batch C

- [ ] Export/Attachment。
- [ ] Review、Learning Path、Memory、Interview。
- [ ] Review no-store group Middleware 改为 Gin，保持所有 route group Header。

每批完成后运行受影响模块测试；不得用删除断言或放宽错误响应换取通过。

Validation:

```bash
go test ./internal/workspace/http ./internal/workflow/http ./internal/changecontrol/http ./internal/collection/http
go test ./internal/health/http ./internal/ingestion/http ./internal/retrieval/http ./internal/graph/http
go test ./internal/conversation/http ./internal/events/http ./internal/knowledge/http ./internal/artifact/http ./internal/authoring/http
go test ./internal/capture/http ./internal/organizing/http ./internal/documenthistory/http ./internal/gitsync/http ./internal/modelsettings/http
go test ./internal/export/http ./internal/review/http ./internal/review/learningpath/http ./internal/memory/http ./internal/review/interview/http
```

Rollback point: 每批目录互不重叠，可按批次回退；共享 app/auth/httpapi 不在领域子代理内修改。

## Phase 5. Contract And Edge Verification

- [ ] `NewRouter` 注册不 panic，runtime Gin routes 与 OpenAPI 182 operation 精确相等；只排除不属于 OpenAPI 的可选 `/metrics`，
  `/livez` 和 `/readyz` 必须参与对比。
- [ ] Auth/CSRF/Origin/API Token/Capability/Workspace/write authorization 回归通过。
- [ ] strict JSON、unknown fields、Unicode、multiple document、body/content-type limit 和 validator error mapping 回归通过。
- [ ] SSE Last-Event-ID、heartbeat、flush、replay/reset/end、断连和 response-started panic 回归通过。
- [ ] Capture multipart 和 Export/Attachment 下载 Header/字节/stream 回归通过。
- [ ] cmd/api composition 与相关真实 PostgreSQL/Router 集成测试可编译并在环境可用时通过。

Validation:

```bash
go test ./cmd/api ./internal/app ./internal/auth/http ./internal/events/http ./internal/capture/http ./internal/export/http
make openapi-check
```

## Phase 6. Remove Chi And Normalize Dependencies

- [ ] 全仓生产/测试 import 和 symbol 删除 Chi；删除临时迁移代码。
- [ ] 从 `go.mod`/`go.sum` 删除 Chi，锁定 Gin；执行 tidy 和 vendor 更新。
- [ ] 验证 Domain/Application/Repository/Workflow 无 Gin import。
- [ ] 检查 vendor license/manifest 一致且 vendor mode 可构建。

Validation:

```bash
rg -n 'github\.com/go-chi/chi|\bchi\.' --glob '!vendor/**' .
rg -n 'github\.com/gin-gonic/gin' internal --glob '**/domain/**' --glob '**/application/**' --glob '**/adapter/**'
go mod tidy -diff
go mod vendor
go list -mod=vendor ./...
git diff --check
```

Expected: 两个边界 `rg` 都无输出；`go mod tidy -diff`、`go list`、`git diff --check` 成功。

## Phase 7. Full Quality Gate

- [ ] 运行 focused 和全仓 Go tests；失败先定位是否依赖外部数据库/Docker，再按项目规则重试一次或记录盲区。
- [ ] 运行 race、vet、API build、OpenAPI 和适用 Compose 门禁。
- [ ] 使用 `go-review` 和 `code-review-and-quality`，并由独立 reviewer 复核共享 HTTP、安全、SSE、依赖和测试覆盖。
- [ ] 修复当前范围内所有已验证缺陷后重新运行受影响门禁。

Validation:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/api
make openapi-check
make compose-check
git diff --check
```

## Phase 8. Documentation, Commit And Finish

- [ ] 更新 `.trellis/spec/backend` 对应 HTTP/错误/质量事实和 `docs/architecture/system-design.md` 当前技术栈。
- [ ] 将 `docs/roadmap.md` TODO 5 更新为已完成，记录实际范围、Gin 版本和验证证据；TODO 11 保持 deferred。
- [ ] 对照 PRD AC-01..AC-08 做逐项完成审计，任何弱证据均视为未完成。
- [ ] 只 stage 本任务文件和与用户脏改重叠文件中的本任务增量；不纳入其他未提交改动。
- [ ] 创建中文简洁提交，记录 task commit，执行 Trellis finish/archive 和开发日志收尾。

Release rollback: revert 本任务最终提交并重新构建原 Chi artifact；本任务没有 Schema 或数据回滚步骤。
