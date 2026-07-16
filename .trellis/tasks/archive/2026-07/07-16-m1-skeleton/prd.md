# M1 可运行项目骨架

## Goal

建立第一个可运行、可测试、可容器化的 ZHIXU 工程：Go API/Worker、React Web、PostgreSQL+pgvector、配置、OpenAPI、统一错误、SSE 基础、Makefile 和 CI。该骨架不实现业务功能，但必须是真实可启动的基础设施，不使用假成功接口。

## Requirements

- Go module 使用仓库远程对应的模块路径 `github.com/CodeZen-Lizhi/zhixu`。
- API 与 Worker 为两个独立二进制，共享配置、日志、数据库和基础类型。
- API 提供 liveness、readiness 和 `/api/v1/system/status`；readiness 真实检查配置与 PostgreSQL，不得固定返回成功。
- Worker 启动时连接 PostgreSQL，周期检查依赖并响应进程信号；River 业务任务在 M4 实现。
- React/Vite/TypeScript 页面展示 API/DB/版本状态，包含 loading、ready、degraded、error，不使用静态假数据。
- 提供初始 OpenAPI 契约和生成/漂移检查入口；M1 可先手写最小客户端边界，但不能出现第二套 DTO。
- Docker Compose 显式包含 app、worker、postgres/pgvector；使用多阶段 non-root 镜像和健康检查。
- 配置支持环境变量和可选 YAML，Secret 不写默认配置或日志。
- 提供 Makefile canonical 命令和 CI：Go test/vet、前端 lint/typecheck/test/build、OpenAPI/Compose 检查、Docker build。
- 不实现 Workspace、Workflow、Proposal、检索、AI 或正式数据库模型。

## Acceptance Criteria

- [ ] `go test ./...` 和 `go vet ./...` 通过。
- [ ] 前端 install、lint、typecheck、test、build 通过。
- [ ] `docker compose -f deploy/compose.yml config` 通过。
- [ ] Compose 启动后 PostgreSQL、API、Worker 处于健康/运行状态。
- [ ] `/livez` 仅检查进程；`/readyz` 在 DB 不可用时返回失败，在 DB 可用时返回成功。
- [ ] Web 页面实际打开且展示真实 API/DB 状态。
- [ ] API 错误响应符合稳定 Problem Details 扩展字段。
- [ ] OpenAPI、README、本地运行、配置示例和 Docker 命令同步。
- [ ] 无硬编码 Secret、无静默 fallback、无与 M2/M3/M4 范围混杂的业务实现。

## Out of Scope

- 不实现领域迁移、River Workflow、Eino、Workspace、摄取、检索、Proposal 或认证业务。
- License 暂不决定，继续保持发布门禁。
