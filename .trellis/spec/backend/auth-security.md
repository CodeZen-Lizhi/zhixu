# 后端认证与安全契约

## Scenario: M10 单用户认证、Cookie Session 与受限 API Token

### 1. Scope / Trigger

- 修改 `internal/auth/**`、认证路由、中间件、认证配置、`00034_learning_ops_auth.sql`、Compose 或 OpenAPI 时，必须应用本契约。
- 认证只建立调用者身份和普通 Capability；Proposal、Approval 与一次性 Write Authorization 仍是正式写入的独立服务端边界。

### 2. Signatures

- Bootstrap 交换：`POST /api/v1/auth/sessions`，唯一 `Authorization: Bearer <bootstrap>`，响应 `201`、Session Cookie 与 `{session_id, csrf_token, expires_at}`。
- 受保护管理端点：`GET|DELETE /api/v1/auth/session`、`POST /api/v1/auth/session/rotate`、`POST|GET /api/v1/auth/api-tokens`、`DELETE /api/v1/auth/api-tokens/{token_id}`。
- 持久化事实：`auth.session(token_hash, csrf_hash, scopes, expires_at, revoked_at)` 与 `auth.api_token(token_hash, scopes, expires_at, revoked_at)`；摘要为 64 位小写 SHA-256 十六进制串。
- 配置入口：`ZHIXU_AUTH_MODE`、`ZHIXU_AUTH_BOOTSTRAP_TOKEN`、`ZHIXU_AUTH_SESSION_TTL`、`ZHIXU_AUTH_API_TOKEN_TTL`、`ZHIXU_AUTH_SECURE_COOKIE`、`ZHIXU_AUTH_ALLOWED_ORIGINS`。
- 就绪依赖：`authpostgres.Repository.Check(context.Context)` 必须查询 `auth.session` 与 `auth.api_token`；`app.Dependencies.AuthCheck` 在 `/readyz` 与 `/api/v1/system/status` 复用该探针。

### 3. Contracts

- Cookie 必须使用 `HttpOnly`、`SameSite=Strict`、`Path=/`；生产/非 loopback 使用 Secure Cookie。Cookie 的不安全方法同时要求精确的单个 Origin 与 `X-CSRF-Token`，两者均不得依赖前端猜测。
- 若请求携带 Bearer，Bearer 是唯一认证来源：无效 Bearer 不能退回 Cookie；重复 Authorization Header 必须拒绝。Bearer 不要求 CSRF，但每条路由仍由显式 Capability 策略检查。
- Bootstrap 只能交换 Session，不能作为业务 API 的通用凭据；Session 只能创建、列出或撤销 API Token，API Token 的 Scope 不得超过创建它的 Session。
- 明文 Bootstrap、Session 与 API Token 不进入数据库、日志、错误响应或 Token 列表。API Token 明文只在 `POST /auth/api-tokens` 的创建响应中出现一次。
- `POST /auth/sessions`、`POST /auth/session/rotate`、`DELETE /auth/session` 与 `DELETE /auth/api-tokens/{token_id}` 不声明 `requestBody`；固定长度或 chunked 的非空 body 都必须在签发、轮换或撤销前以 `400 AUTH_REQUEST_INVALID` 拒绝，OpenAPI 同步声明 `Problem`。
- Repository 必须在单条数据库语句中把未撤销且未过期的匹配摘要认证为主体并更新时间戳；损坏或非 canonical 的 Scope JSON 必须 fail closed。
- `disabled` 与 `Secure=false` 都只允许 development 的 API 进程 loopback 监听。官方 Compose 由无 Workspace 的
  `zhixu-app-netns` anchor 发布 host-loopback TCP 端口；anchor 每次启动先以 `NET_ADMIN` 安装只允许 Docker bridge gateway 与
  loopback 的 peer firewall，再仅以 `SETUID`/`SETGID` 完成降权，成为非特权零 capability 长期进程。不得用环境变量声明替代监听地址、最终端口模型或运行时隔离验证。
- 认证表缺失、权限被收回或探针超时必须让 `/readyz` 返回 `503 AUTH_DEPENDENCY_UNAVAILABLE`，且系统状态 `auth.status=unavailable`；响应不泄漏底层数据库细节。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| 缺失、重复或错误身份凭据 | `401 AUTH_UNAUTHORIZED`，不得泄漏主体是否存在 |
| Cookie 不安全请求缺 Origin、Origin 不在白名单或缺 CSRF | `403 AUTH_CSRF_REJECTED` |
| Bearer Scope 不足或 API Token 调用仅允许 Session 的凭据管理端点 | `403`，不得扩大 Scope 或把 Token 当浏览器 Session |
| 无 `requestBody` 的凭据状态变更端点收到固定长度或 chunked 非空 body | `400 AUTH_REQUEST_INVALID`，不得签发、轮换或撤销凭据 |
| 过期、撤销、Scope JSON 损坏或并发撤销后的凭据 | fail closed，后续请求不得获得 Principal |
| `required` 缺 Bootstrap、`disabled` 携带 Bootstrap、生产 insecure Cookie/Origin | API 启动前拒绝 |
| disabled/insecure Cookie 的 API 非 loopback 监听、host networking、端口缺 host IP、为 `0.0.0.0` 或非 loopback、anchor 缺少 bridge peer firewall/运行时降权 | 配置校验或 `compose-auth-check` 在 `compose up` 前拒绝 |
| `auth.session` / `auth.api_token` 缺失、不可读或认证探针超时 | readiness `503 AUTH_DEPENDENCY_UNAVAILABLE`；系统状态标记 auth unavailable |

### 5. Good / Base / Bad Cases

- Good：浏览器用 Bootstrap 换取 HttpOnly Session；同源 unsafe 请求带 Origin 与 CSRF；自动化使用只含所需 Capability 的 Bearer API Token。
- Base：development loopback 可显式 `disabled`，前端仍从 `/api/v1/system/status` 读取模式，而不是把 localhost 当作身份。
- Bad：把 Bootstrap 作为所有 Bearer 的 fallback、在 Token 列表返回明文、只检查 SameSite、仅凭可伪造的环境变量放行公网端口，或以登录身份跳过 Approval/Write Authorization。

### 6. Tests Required

- `internal/auth` 的 domain/application/HTTP 单测必须覆盖 Cookie 属性、Bearer 优先级、重复 Header、Origin/CSRF、Scope 越权、过期/撤销、无 body 凭据变更端点的普通/chunked 请求体拒绝与明文不回显。
- `internal/auth/adapter/postgres` 的真实 PostgreSQL `-race` 测试必须覆盖摘要落库、数据库时钟、原子 last-used、损坏 Scope、keyset cursor 与撤销/认证竞态。
- `internal/platform/config` 与 `compose_contract_test.go` 必须覆盖 production 配置拒绝、loopback IPv4/IPv6 允许、disabled/insecure Cookie 的非 loopback、host-networking 或缺少 anchor firewall 拒绝、anchor 零 Workspace/secret/socket 与长期降权，以及 Secure Cookie 自托管端口允许。
- `internal/auth/adapter/postgres` 与 `internal/app` 测试必须覆盖双认证表探针、探针错误不回显、`/readyz` 503 与系统状态 auth unavailable。
- 发布前运行 `make auth-integration`（带 `ZHIXU_TEST_DATABASE_URL`）、`make compose-auth-smoke`（包含 host loopback 可达和 bridge peer 拒绝）、`make openapi-check`、全仓 `go test -race`/`go vet` 与前端 lint/typecheck/test/build。

### 7. Wrong vs Correct

```text
Wrong: Authorization Header 无效时继续尝试 Session Cookie；或用可伪造的环境变量声明绕过 disabled/insecure 的监听边界。
Correct: Bearer 出现即只验证 Bearer；API 进程本身绑定 loopback，Compose 解析最终端口模型并确认本地开发入口均为 loopback 后才启动。

Wrong: 仅数据库 Ping 成功就报告 ready。
Correct: `/readyz` 与系统状态都通过同一个认证表探针，缺表、权限错误或超时统一 fail closed。

Wrong: API Token 拥有 WRITE_KNOWLEDGE 就直接 Apply Knowledge。
Correct: Capability 只允许到达业务命令；正式写入仍必须通过 Proposal、Approval 和一次性 Write Authorization。

Wrong: OpenAPI 不声明 requestBody 的凭据端点静默忽略 JSON body 后继续签发或撤销。
Correct: 固定长度和 chunked 的非空 body 都在状态变更前稳定返回 `400 AUTH_REQUEST_INVALID`，并由 OpenAPI checker 锁定该契约。
```
