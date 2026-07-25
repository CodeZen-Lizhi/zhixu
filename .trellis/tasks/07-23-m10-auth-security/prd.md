# M10 认证与安全闭环

## Goal

为本地优先与自托管部署建立可验证的单用户认证边界：本地 loopback 开发模式可显式关闭认证，自托管/生产模式必须启用认证；浏览器使用安全 Cookie Session，自动化调用使用最小权限 API Token，并保持 Proposal/Approval/Write Authorization 仍是正式写入的第二道业务授权边界。

## Requirements

- Bootstrap Bearer Token 只能来自运行时配置，用于首次换取 Session 或创建受限 API Token，不持久化明文。
- Session 使用不可预测的明文 Token；数据库只保存 SHA-256 摘要，支持过期、撤销和原子认证/last-used 更新。
- 浏览器 Session Cookie 必须为 `HttpOnly`、`SameSite=Strict`，生产可配置且默认要求 `Secure`；Cookie 状态修改请求同时校验允许的 Origin 与 `X-CSRF-Token`。
- API Token 明文只在创建成功时返回一次，数据库只保存摘要；每个 Token 具有名称、Capability Scope、过期时间、撤销时间和最后使用时间。
- API Token Scope 使用项目现有 canonical Capability；认证只证明调用方身份和能力，不能绕过 Proposal、Approval 或一次性 Write Authorization。
- 中间件必须区分匿名公共端点、已认证读请求、需要 Capability 的请求以及 Cookie 状态修改请求；错误使用稳定 Problem Details，禁止静默降级。
- 生产/自托管模式若认证关闭、Bootstrap Token 缺失或 Cookie/Origin 配置不安全，API 必须拒绝启动；仅 loopback 开发模式可显式 `disabled`。
- 配置、`.env.example`、Compose、API composition root、Router、OpenAPI 和前端调用契约必须一致。
- `00034_learning_ops_auth.sql` 中认证表、约束、原子查询与回滚顺序必须有真实 PostgreSQL 集成测试。
- 安全测试至少覆盖：缺失/错误凭据、过期、撤销、Scope 越权、损坏 Scope JSON、CSRF、Origin、Cookie 属性、Bearer 优先级、明文不落库和并发撤销竞态。

## Acceptance Criteria

- [ ] Bootstrap Token 可换取 Session，响应 Cookie 与 CSRF Token 满足安全属性；Bootstrap Token 不落库、不进入日志或响应回显。
- [ ] Session 与 API Token 的创建、认证、过期、撤销和 last-used 更新均通过单元、HTTP 与 PostgreSQL 集成测试。
- [ ] Cookie 修改请求缺少或伪造 Origin/CSRF 时稳定拒绝；Bearer 请求不依赖 CSRF，但严格执行 Capability Scope。
- [ ] API Token 明文仅创建时可见，数据库与后续列表响应只包含摘要不可逆信息。
- [ ] 自托管/生产的不安全配置在启动前失败；loopback 开发禁用认证必须显式配置且有测试。
- [ ] Router 覆盖所有 `/api/v1` 业务端点，健康检查/静态资源等明确公共端点除外；正式写入仍需原业务授权。
- [ ] OpenAPI、配置样例、Compose 和运行文档与实际行为一致。
- [ ] `go test ./internal/auth/...`、受影响包测试、真实 PostgreSQL 认证集成测试、`go test -race ./...`、`go vet ./...`、OpenAPI 检查及安全扫描通过。
- [ ] 主 Agent 完成 `go-review` 与 `sql-code-review`；独立审查复验高风险认证和 SQL 改动，当前范围内无未处理的高严重度问题。

## Notes

- 本任务只完成 M10-02 认证与直接相关安全闭环；通用 Audit/OTel、容量与备份恢复分别由后续 M10 子任务完成。
- 共享迁移 `00034_learning_ops_auth.sql` 同时预建后续 Learning/Ops 表；本任务只对认证相关表及迁移整体可逆性负责，不提前实现 M8 业务。
