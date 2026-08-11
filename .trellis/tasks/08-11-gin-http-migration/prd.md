# 将后端 HTTP 层从 Chi 迁移到 Gin

## Goal

将生产 API、领域 HTTP Handler、Middleware 和测试辅助从 Chi 完整迁移到 Gin，使用项目已采用的
`go-playground/validator` 收敛适合声明式表达的请求校验，同时保持现有业务、安全、流式传输和错误契约。
迁移完成后，维护者只需理解一套 Router/Middleware 机制，仓库不再依赖 Chi，也不长期保留兼容 Router。

## Background

- 路线图 [`docs/roadmap.md`](../../../docs/roadmap.md) TODO 5 将 Gin 迁移列为 P0，并要求先冻结行为基线、按路由组迁移、
  每批行为对等后删除 Chi 路径。
- 当前 `go.mod` 使用 Go `1.25.4`、Chi `v5.3.1` 和 validator `v10.30.3`；2026-08-11 通过 Go Module
  Proxy 核实 Gin 最新稳定版为 `v1.12.0`，其最低 Go 版本为 `1.25.0`。
- 当前有 59 个非 vendor Go 文件直接导入 Chi、26 个路由注册入口；`api/openapi/openapi.json` 包含 154 个 path、
  182 个 operation。生产 Composition Root 位于 `internal/app/router.go`。
- 迁移前基线 `go test ./internal/app ./internal/auth/http ./internal/events/http ./internal/httpapi` 和
  `make openapi-check` 已通过。
- 当前工作区已有与本任务重叠的 `internal/modelsettings/http/*`、`api/openapi/*` 未提交改动；它们属于用户现有工作，
  本任务必须在其上增量集成，不得覆盖或回滚。

## Requirements

### R1. 冻结并保持外部 HTTP 契约

- 迁移前记录 OpenAPI 基线，并用可重复测试覆盖 runtime route method/path inventory。
- 保持全部现有 API、`/livez`、`/readyz`、可选 `/metrics`、静态资源 fallback 的路径、方法、状态码、Header、
  Content-Type 和响应体语义。
- 保持 404、405、重定向、尾斜杠和路径参数解码行为；不得因 Gin 默认配置产生未批准的 301/307、自动正文或错误格式。

### R2. 保持安全与隔离语义

- Session、API Token、CSRF、Origin、Capability、Workspace 隔离、写授权、限流和审计语义保持兼容。
- Middleware 顺序保持为 no-store、request ID、trace、request log、panic recovery，再进入 API auth/domain route group。
- 路由日志继续只记录低基数模板，不记录实际 path、凭据、请求体或其他敏感值。
- Gin Context 只允许出现在 HTTP/Composition 边界，不得进入 Domain、Application、Repository、Workflow 或持久化契约。

### R3. 保持严格请求与稳定错误契约

- 未知 JSON 字段、无效 Unicode、多个 JSON document、超出大小上限和错误 Content-Type 继续 fail closed。
- Problem Details 的状态映射、`error_code`、`message`、`retryable`、可选 `workflow_run_id` 和 bounded `details`
  保持兼容；不得使用 Gin 自动 bind error response 覆盖项目 Problem。
- 使用 validator 处理适合声明式表达且已有测试锁定的通用字段约束；跨字段安全规则、领域不变量和错误分类继续由项目代码负责。

### R4. 保持流式与文件传输契约

- SSE 的 `text/event-stream`、no-store、heartbeat、flush、Last-Event-ID 优先级、Workspace replay/reset/end 和断连处理保持兼容。
- Capture multipart 大小限制、文件名/MIME 处理，以及 Export/Attachment 下载状态、Header、流式写入和 Content-Disposition 保持兼容。
- panic recovery 不得在响应已经开始后追加 Problem，也不得泄露 panic 值、完整路径、请求正文或凭据。

### R5. 单一 Gin 生产入口与分批迁移

- 采用 Gin `v1.12.0` 的单一 `gin.Engine`/route group 作为生产 Router；不使用 `gin.Default` 隐式安装第二套 logger/recovery。
- 按核心 Router/Middleware、认证、领域路由组、SSE/文件边界、最终清理分批迁移，每批完成定向行为对等验证。
- 允许迁移期间在同一开发分支保留尚未切换的 Chi 文件，但任一可运行生产入口不得同时挂载 Chi 和 Gin；最终不得保留双 Router、
  Chi adapter 或 Chi Middleware。

### R6. 依赖、测试和文档收口

- 生产代码、测试辅助、`go.mod`、`go.sum` 和 `vendor/` 不再包含 `github.com/go-chi/chi/v5`；Gin 依赖被锁定并 vendor 化。
- 所有既有 Go 单元/集成测试继续通过，并补充 Gin 配置、runtime route inventory、validator、Middleware 顺序、404/405、SSE、
  upload/download 和安全回归测试。
- API 构建、Go test/race/vet、OpenAPI、Compose 门禁和关键浏览器链路通过；无法在当前环境执行的外部依赖门禁必须明确记录盲区，
  不得伪装通过。
- 当前架构文档和后端规格更新为 Gin 当前事实，路线图 TODO 5 改为已完成并记录验证证据；TODO 11 仍保持独立后续任务。

## Out Of Scope

- 不引入 `gin-contrib/sessions`，不改变 PostgreSQL Session 权威状态；该工作仍属于 TODO 11。
- 不迁移 Goose/pgx 到 Atlas/GORM，不改变数据库 Schema、Repository 或事务边界。
- 不实施 OpenAPI Generator/Zod、Spectral/oasdiff 或前端 transport 重写。
- 不改变业务路由、DTO、领域规则、错误码、Capability 表、SSE wire format 或用户可见功能。
- 不为迁移进行无关目录重构、热点拆分、依赖升级或性能优化。

## Acceptance Criteria

- [ ] AC-01：Gin runtime route inventory 与冻结的 182 个 OpenAPI operation 对等；健康检查、可选 metrics 和静态 fallback
  由专项测试覆盖，OpenAPI 文件无未批准 path/method 漂移。
- [ ] AC-02：所有 API 通过唯一 Gin Engine 提供；404/405、尾斜杠、路径参数、静态 fallback 和 Middleware 顺序测试通过，
  生产入口未挂载 Chi。
- [ ] AC-03：Auth、Session、CSRF、Origin、API Token、Capability、Workspace 与写授权测试通过，路由日志保持模板化且不泄露实际 path。
- [ ] AC-04：严格 JSON、Content-Type、body limit 和 validator 测试通过；Gin 默认 binding 不会输出或替换项目 Problem。
- [ ] AC-05：SSE、multipart upload、Export/Attachment download、response-started panic recovery 的单元/集成测试通过。
- [ ] AC-06：`rg 'github.com/go-chi/chi|chi\\.' --glob '!vendor/**'` 在生产和测试代码中无命中，`go.mod`、`go.sum`、`vendor/`
  无 Chi，且 Domain/Application/Repository/Workflow 无 Gin import。
- [ ] AC-07：`go test ./...`、`go test -race ./...`、`go vet ./...`、API build、`make openapi-check`、适用 Compose 门禁和关键浏览器 smoke
  通过，或对不可执行门禁提供可复核的环境原因与剩余风险。
- [ ] AC-08：架构文档、后端规格、路线图和任务记录与最终实现一致，包含明确的迁移回滚点和 TODO 11 延后边界。

## Planning Status

- 用户已批准创建 Trellis 任务；持续执行指令已确认按本规划进入实现阶段，任务状态为 `in_progress`。
- 当前没有阻塞规划的产品、范围、兼容性或风险决策；技术选择和分批方案记录在 `design.md`、`implement.md`。
