# 将后端 HTTP 层从 Chi 迁移到 Gin

## Goal

将生产 API、领域 HTTP Handler、Middleware 和测试辅助从 Chi 完整迁移到 Gin，使用项目已采用的
`go-playground/validator` 收敛适合声明式表达的请求校验，同时保持现有业务、安全、流式传输和错误契约。
迁移完成后，维护者只需理解一套 Router/Middleware 机制，仓库不再依赖 Chi，也不长期保留兼容 Router。

## Background

- 任务启动时，路线图 TODO 5 将 Gin 迁移列为 P0，并要求先冻结行为基线、按路由组迁移、每批行为对等后删除 Chi 路径；
  当前路线图已同步为“Gin HTTP 基线（已交付）”。
- 启动时的基线是 Go `1.25.4`、Chi `v5.3.1`、182 个 OpenAPI operation。实现提交
  `9bb5b939d84fe52993a549216a36e6de831c2f71` 已将生产入口改为唯一 `*gin.Engine`，锁定 Gin `v1.12.0` 并删除 Chi。
- 当前 runtime/OpenAPI inventory 为 183 个 operation：相较初始 182，额外的模型 activation endpoint 是随后受控加入的
  独立契约；`internal/app/router_inventory_test.go` 对当前集合做精确比较。生产 Composition Root 仍位于
  `internal/app/router.go`。
- 当前工作区不再有非 vendor Go 的 Chi import/symbol 命中；Application integration smoke 已改经生产
  `app.NewRouter` 发起公开 `/api/v1` 请求，不再直接 import Gin。框架依赖只停留在 Composition/HTTP adapter 边界。

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

- [x] AC-01：Gin runtime route inventory 与当前冻结的 183 个 OpenAPI operation 对等；健康检查、可选 metrics 和静态 fallback
  由专项测试覆盖。初始 182→183 仅对应已受控的模型 activation endpoint，OpenAPI 无其他未批准 path/method 漂移。
- [x] AC-02：所有 API 通过唯一 Gin Engine 提供；404/405、尾斜杠、路径参数、静态 fallback 和 Middleware 顺序测试通过，
  生产入口未挂载 Chi。
- [x] AC-03：Auth、Session、CSRF、Origin、API Token、Capability、Workspace 与写授权测试通过，路由日志保持模板化且不泄露实际 path。
- [x] AC-04：严格 JSON、Content-Type、body limit 和 validator 测试通过；Gin 默认 binding 不会输出或替换项目 Problem。
- [x] AC-05：SSE、multipart upload、Export/Attachment download、response-started panic recovery 的单元/集成测试通过。
- [x] AC-06：`rg 'github.com/go-chi/chi|chi\\.' --glob '!vendor/**'` 在生产和测试代码中无命中，`go.mod`、`go.sum`、`vendor/`
  无 Chi，且 Domain/Application/Repository/Workflow 无 Gin import。
- [x] AC-07：全仓 race/vet、API build、OpenAPI、vendor/tidy 和全部受影响 HTTP 包通过；全仓普通/race 的两个既有基线失败、
  Compose launcher 端口竞态、数据库与浏览器边界均已提供可复核原因和剩余风险，见下方最终证据。
- [x] AC-08：架构文档、后端规格、路线图和任务记录与最终实现一致，包含明确的整体 revert 回滚点和 TODO 11 延后边界。

## 2026-08-14 最终实施证据

- 隔离于并行 Managed Ollama 改动的工作树中，app、`cmd/api`、auth/http、httpapi、Change Control Application 及全部
  迁移领域 HTTP package 的普通测试和 `-race -count=1 -timeout 60s` 全部通过。全仓
  `go test -race -count=1 -timeout 60s ./...` 除下述两个基线外其余包通过；`go vet ./...` 全部通过。
- `make openapi-check`、`go build ./cmd/api`、`go mod tidy -diff`、`go list -mod=vendor ./...`、integration-tag
  compile-only 和 `git diff --check` 通过；runtime 与 OpenAPI 当前 183 条 operation 精确相等，仅 `/metrics` 可选。
- `internal/platform/config` 的 Compose fake 在 `compose-netns-check` 返回错误 project name；同一测试已在迁移前提交
  `9bb5b939^` 复现。`internal/platform/gitcli` 在全仓 60 秒阈值末尾超时，单包普通与 race 使用 120 秒均通过。
- `make compose-check` 的 runtime/netns/workspace、cleanup 和 image-cleanup 子合同通过；launcher 子合同在动态端口
  A→B→A 切换时因 fixture 端口释放竞态返回 `EADDRINUSE`，且迁移前提交可同样复现。单独的
  `model-secrets-init-contract` 通过；本任务未修改相关部署脚本。
- 当前未设置 `ZHIXU_TEST_DATABASE_URL`，真实 PostgreSQL integration 只完成带 integration tag 的编译验证。该迁移不改变
  Schema/Repository；既有 production-router 数据库 smoke 仍保留。后端无用户可见 UI 变化，且用户未要求浏览器运行，
  因此未启动本地服务做浏览器 smoke；HTTP/OpenAPI/Compose 合同是本任务的可执行边界证据。
- 路线图和系统设计已由 `ab6029c4` 同步；新增 `http-boundary.md` 固化 Gin engine、route parity、panic recovery 与
  SSE 底层 flush 契约。独立审查发现的不可比较 panic 值二次 panic和伪 Flusher 两项缺陷已修复并通过普通/race 回归。
- 回滚点：整体 revert 核心迁移提交 `9bb5b939` 及本任务最终收口提交后重新构建 Chi artifact；本任务没有数据库或数据回滚步骤。

## Planning Status

- 用户已批准创建 Trellis 任务；持续执行指令已确认按本规划进入实现阶段，任务状态为 `in_progress`。
- 规划、实现、独立审查和最终门禁已完成；技术选择和分批方案记录在 `design.md`、`implement.md`，最终结果记录在
  `outcome.md`。
