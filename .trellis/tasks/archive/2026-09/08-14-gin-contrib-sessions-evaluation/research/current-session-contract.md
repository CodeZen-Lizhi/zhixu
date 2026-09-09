> 本文是 2026-08-14 的调研快照。2026-09-08 GORM 前置已完成，现行实现见 Auth GORMRepository；Session 状态所有权与不采用结论见 ADR-0030。

# Research: current session contract

- Query: 现有 Auth/Session 契约与所有权审计；为 TODO 11 评估 `gin-contrib/sessions` 可替换的最小通用部分
- Scope: mixed
- Date: 2026-08-14

## Findings

### 结论

当前实现不是“Cookie 内 Session”，而是“Cookie 携带一次生成的不透明随机凭据，PostgreSQL 保存其 SHA-256 哈希并权威决定生命周期”。`session_id` 是独立的公开 UUID 元数据，不是 Cookie 值。创建、读取、轮换、撤销、过期与并发冲突均由 `internal/auth` 应用层编排，并由单条 PostgreSQL 语句或事务语义落地；HTTP 层只负责凭据提取、Cookie 属性、Origin/CSRF、Capability 路由策略和稳定 Problem 映射。

`gin-contrib/sessions` 的现成 Cookie/PostgreSQL/GORM store 都不能直接替换该契约：它们拥有自己的 Cookie 编码、表结构、应用时钟和 CRUD 语义，缺少哈希凭据、撤销状态、`last_seen_at`、数据库时钟、原子轮换以及项目错误码。若强制接入，唯一可控边界是自定义一个“仅传输单个不透明 lookup credential”的 Store，同时继续使用现有 PostgreSQL Repository；但这仍需处理框架吞掉 `Get` 错误、缺少数据库 `Expires` Cookie 语义及存量 Cookie 兼容，几乎不减少现有代码。按 ADR 0019 的强制约束优先规则，当前证据支持“不接入”；最小实际可替换部分为零，现有两处 `http.SetCookie` 和一处删除 Cookie 已由标准库覆盖。

### 当前请求与数据流

```text
Set-Cookie: zhixu_session=<43-char base64url random token>
                         |
                         v
HTTP middleware -- SHA-256 --> auth.session.token_hash
       |                            |
       | Origin + CSRF              | revoked_at / expires_at / last_seen_at
       | route capability           | PostgreSQL CURRENT_TIMESTAMP
       v                            v
request.Context Principal <--- canonical authenticated Session row
       |
       +--> Workspace RootGrant / write authorization 再独立校验
```

Cookie 中不包含 `session_id`、用户、scope、CSRF 或生命周期状态；服务端也不持久化明文 Session token/CSRF token。明文 token 只经 `Set-Cookie` 返回，明文 CSRF 只在创建或轮换响应中返回。

### 所有权矩阵

| 行为/数据 | 当前所有者 | 可验证契约 |
|---|---|---|
| Session Cookie 名称与属性 | `internal/auth/http.Handler` | 名称 `zhixu_session`；`Path=/`、`HttpOnly`、配置化 `Secure`、`SameSite=Strict`、数据库返回的 `Expires`、按 TTL 计算 `MaxAge`。Cookie 值直接是凭据 token，没有额外签名/序列化层（`internal/auth/http/handler.go:25-30`, `:366-403`）。 |
| Session token 编码 | `internal/auth/application.Service` | 32 字节 CSPRNG，经 `base64.RawURLEncoding` 编为固定 43 字符；数据库只保存小写 SHA-256 hex（`internal/auth/application/service.go:22-32`, `:332-370`）。 |
| Session ID | Domain/Application | 独立 UUID，由应用层生成并作为元数据返回；不进入 Cookie，Repository 校验回写与签发一致（`internal/auth/application/service.go:341-393`；`internal/auth/domain/model.go:52-73`）。 |
| CSRF token | Application + HTTP | 与 Session token 独立随机生成，数据库只保存哈希；HTTP 对不安全 Cookie 请求做恒定时间比较（`internal/auth/application/service.go:177-197`, `:341-370`; `internal/auth/http/handler.go:616-655`）。 |
| 权威 Session 状态 | PostgreSQL `auth.session` | `token_hash`、`csrf_token_hash`、用户、scope、创建/最后访问/过期/撤销时间均在表中；没有 Cookie 内状态，也没有进程内 Session store（`migrations/00034_learning_ops_auth.sql:308-324`）。 |
| 创建 | Application 生成凭据；Repository 用数据库时钟落库；HTTP 发 Cookie | `INSERT ... CURRENT_TIMESTAMP ... RETURNING`，TTL 由数据库时间计算；应用校验 Repository 返回值（`internal/auth/adapter/postgres/repository.go:53-74`; `internal/auth/application/service.go:341-393`）。 |
| 读取/认证 | HTTP 提取凭据；Application 哈希；Repository 原子更新 | 单条 `UPDATE` 仅命中未撤销、未过期 token，同时更新 `last_seen_at=CURRENT_TIMESTAMP` 并返回规范行（`internal/auth/http/handler.go:616-655`; `internal/auth/application/service.go:177-197`; `internal/auth/adapter/postgres/repository.go:107-116`）。 |
| 轮换 | Application 限定 Session principal 并重新认证；Repository 原子替换；HTTP 替换 Cookie/CSRF | 单个 CTE 先撤销仍有效的旧行，再仅在成功撤销时插入新行；并发轮换只能一个成功（`internal/auth/application/service.go:153-175`; `internal/auth/adapter/postgres/repository.go:76-105`; `internal/auth/http/handler.go:425-452`）。 |
| 撤销 | Application 限定当前 Session；Repository 先撤销；HTTP 成功后删除 Cookie | PostgreSQL 更新 `revoked_at`；存在但已撤销仍幂等成功，不存在返回未授权；Cookie 删除不能代替数据库撤销（`internal/auth/application/service.go:303-311`; `internal/auth/adapter/postgres/repository.go:118-136`; `internal/auth/http/handler.go:454-470`）。 |
| 过期 | PostgreSQL 数据与数据库时钟 | 认证和轮换用 `expires_at > CURRENT_TIMESTAMP`；应用时钟漂移不能改变权威结果（`internal/auth/adapter/postgres/repository.go:76-116`; `internal/auth/adapter/postgres/repository_integration_test.go:27-104`）。 |
| 并发登录 | 当前语义允许多个独立 Session 共存 | `auth.session` 仅对 `token_hash` 唯一，没有用户唯一约束或“登录即撤销其他 Session”逻辑。集成测试连续创建两个 Session 并验证均存在，但没有专门的并发登录测试（`migrations/00034_learning_ops_auth.sql:308-324`, `:388-393`; `internal/auth/adapter/postgres/repository_integration_test.go:42-90`）。 |

Repository 边界明确承担原子生命周期操作（`internal/auth/adapter/postgres/repository.go:21-29`），Application Repository 接口没有通用 key/value Session 抽象（`internal/auth/application/service.go:49-59`）。这意味着框架 store 不能成为第二事实源。

### PostgreSQL Schema 与一致性

`auth.session` 字段为：`id`、`token_hash`、`csrf_token_hash`、`user_label`、`scopes`、`created_at`、`last_seen_at`、`expires_at`、`revoked_at`；哈希长度、时间顺序、scope 非空均有约束（`migrations/00034_learning_ops_auth.sql:308-324`）。`auth.api_token` 是独立表，字段包含名称、前缀、哈希、scope、过期/使用/撤销时间（同文件 `:325-337`）。两个表均由项目 migration 管理，索引位于同文件 `:388-393`。

读取后的行还要经过 UUID、64 位 hex 哈希和规范 scope 校验；损坏行失败关闭为依赖/一致性错误，不会被当成匿名或空 Session（`internal/auth/adapter/postgres/repository.go:233-282`）。`pgx.ErrNoRows` 映射未授权，唯一冲突映射 `AUTH_CREDENTIAL_CONFLICT`，其他数据库错误映射依赖不可用（同文件 `:292-332`）。

数据库接入复用应用已有 `database.DB`，启动时同时检查 `auth.session` 和 `auth.api_token`，没有第二连接池（`cmd/api/main.go:305-330`, `:650-683`; `internal/auth/adapter/postgres/repository.go:39-51`）。

### 独立安全门

| 安全门 | 与 Session 的关系 | 证据 |
|---|---|---|
| Origin | Cookie 不安全请求必须“恰好一个”且精确匹配启动时规范化的允许列表；不是 Session store 职责。Bootstrap 仅在带 Origin 时校验，允许无 Origin 的非浏览器交换。 | `internal/auth/http/handler.go:78-94`, `:366-383`, `:616-655` |
| CSRF | 每个 Session 独立 secret/hash；只对 Cookie 不安全请求要求“恰好一个” `X-CSRF-Token`。Bearer 请求不走 CSRF，但仍走 Capability。 | `internal/auth/application/service.go:177-197`; `internal/auth/http/handler.go:616-655` |
| API Token | 独立 `auth.api_token` 表和认证路径；仅 Session principal 可创建、列举、撤销，scope 必须是当前 Session scope 子集。任何单个 `Authorization` 都独占认证路径，Bearer 失败不能回落 Cookie。 | `internal/auth/application/service.go:200-300`; `internal/auth/http/handler.go:478-614`, `:616-663` |
| Capability | HTTP 层对业务路由维护显式 method/path 策略；Application 再检查 principal scope。未知不安全路由默认要求 `WRITE_KNOWLEDGE`。 | `internal/auth/http/handler.go:111-184`, `:200-318`; `internal/auth/application/service.go:270-300` |
| Workspace RootGrant | Workspace 激活状态和本机 mount grant 是独立权威边界；托管运行时禁止浏览器/API 选择或创建宿主根目录。Repository 每次解析 active workspace + grant，不能由 Session scope 代替。 | `.trellis/spec/backend/workspace-root-grant.md:12-37`, `:83-90`; `internal/platform/rootgrant/grant.go:15-27`, `:50-129`; `internal/workspace/adapter/postgres/repository.go:75-90`, `:136-160`, `:379-402` |
| 正式写授权 | `change_control.tool_authorization` 是一次性、强绑定、可消费/撤销/过期的独立能力，只支持 `WRITE_KNOWLEDGE`/`GIT_WRITE`；绑定 workspace/workflow/node/proposal/revision/approval/tool/scope/change hash/target/version。Session Capability 只允许进入审批/预检路由，不等于写权限。 | `internal/changecontrol/domain/authorization.go:17-53`, `:73-180`; `internal/changecontrol/application/service.go:977-1105`; `internal/changecontrol/http/handler.go:66-74`, `:751-760`; `migrations/00008_write_authorization.sql:1-170` |

Capability 枚举为 `READ_LOCAL`、`READ_EXTERNAL`、`WRITE_PROPOSAL`、`WRITE_KNOWLEDGE`、`GIT_WRITE`、`INDEX_MAINTENANCE`、`EVALUATION_RUN`、`MANAGE_SYSTEM_SETTINGS`（`internal/capability/capability.go:9-29`）。即使 Session 有写类 scope，仍必须满足 workspace grant、审批状态、目标校验和一次性 write authorization；这些边界不能被 Session 框架接管或弱化。

### HTTP 与 OpenAPI 公开契约

Handler 注册以下端点（`internal/auth/http/handler.go:96-109`）：

| Endpoint | 认证/输入 | 成功契约 |
|---|---|---|
| `POST /api/v1/auth/sessions` | Bootstrap Bearer；无 body；Origin 若存在必须允许 | `201`，设置 HttpOnly Session Cookie；JSON 返回 `session_id`、43 字符 `csrf_token`、`expires_at` |
| `GET /api/v1/auth/session` | Session Cookie only | 当前 `SessionInfo`；不回显 token/CSRF/hash |
| `POST /api/v1/auth/session/rotate` | Session Cookie + Origin + CSRF；无 body | 原子撤销旧 Session、发新 Cookie；返回新 ID/CSRF/过期时间 |
| `DELETE /api/v1/auth/session` | Session Cookie + Origin + CSRF；无 body | 数据库撤销成功后删除 Cookie，`204` |
| `GET /api/v1/auth/api-tokens` | Session Cookie only | 严格 cursor 分页元数据；不返回 token/hash |
| `POST /api/v1/auth/api-tokens` | Session Cookie + Origin + CSRF；严格 JSON | `201`，明文 API token 仅本次响应返回 |
| `DELETE /api/v1/auth/api-tokens/{id}` | Session Cookie + Origin + CSRF | `204`；不存在为 `404`，不影响当前 Session |

OpenAPI 对应定义位于 `api/openapi/openapi.json:5738-6061`；安全方案位于 `:11480-11498`；`SessionCredential`、`SessionInfo`、API Token 请求/响应 schema 位于 `:13318-13550`。全局业务 API 允许 Session Cookie 或 API Bearer，Auth 管理端点逐个覆盖为 Bootstrap 或 Session-only。无 body 端点在运行时拒绝常规和 chunked 非空 body（`internal/auth/http/handler.go:684-710`）。

稳定服务端错误码：

- `AUTH_UNAUTHORIZED`: `401`，并设置 `WWW-Authenticate`。
- `AUTH_CAPABILITY_DENIED`: `403`。
- `AUTH_CSRF_REJECTED`: `403`，同时覆盖 Origin/CSRF 拒绝。
- `AUTH_REQUEST_INVALID`: `400`。
- `AUTH_DEPENDENCY_UNAVAILABLE`: 常规依赖故障为 `503`；若 Repository 发现损坏/非规范数据库行，则同一 code 搭配 consistency-violation kind 映射为 `409`、`retryable=false`。
- `AUTH_API_TOKEN_NOT_FOUND`: `404`。
- `AUTH_CREDENTIAL_CONFLICT`: Repository 唯一冲突的罕见内部映射，Foundation 会映射 `409`。当前 Auth OpenAPI responses 未声明任何 `409`，因此它也没有覆盖上一项的损坏行响应。

定义与映射见 `internal/auth/application/service.go:34-47`、`internal/auth/http/handler.go:666-682`、`internal/auth/adapter/postgres/repository.go:292-332`。Problem body 只公开稳定 `error_code`、安全 `message`、`retryable` 等字段，不包含内部 cause；基础错误模型见 `internal/foundation/error.go:5-47`。

所有 Auth 响应设置 `Cache-Control: no-store`（`internal/auth/http/handler.go:684-686`）。HTTP middleware 将规范化 `Principal` 写入标准 `request.Context`，不要求业务层依赖 Gin context（同文件 `:111-145`）。Router 先挂全局安全中间件，再开放健康/认证端点，业务组统一先认证；Auth 初始化缺失时业务路由失败关闭（`internal/app/router.go:117-136`, `:175-192`）。Readiness 同时检查 Auth 表并以 `AUTH_DEPENDENCY_UNAVAILABLE` 返回，不泄露内部错误（同文件 `:140-155`, `:473-488`）。

### 前端依赖的行为契约

前端只通过 `authFetch` 访问 API：默认 `credentials: "include"`，Cookie 由浏览器管理；不安全 Cookie 请求从 localStorage 取 CSRF 并加 `X-CSRF-Token`，Bearer 请求不加 CSRF（`web/src/api/auth.ts:245-265`）。CSRF 存储不可用时失败关闭为本地 `AUTH_STORAGE_UNAVAILABLE`；跨 tab `storage` 事件会使认证状态失效并重取（同文件 `:77-169`）。

Bootstrap 和 rotate 分别写入/替换 CSRF；成功 revoke 清除；受保护请求 `401` 也清除认证状态（`web/src/api/auth.ts:330-350`, `:245-265`）。Auth Context 用 system status 区分 disabled/unavailable/required；required 且无 CSRF 时视为匿名，登出网络失败不会假装已经退出（`web/src/app/auth-context.tsx:62-133`）。`SessionInfo.id` 是前端可见的会话元数据，替换 Session 会清理查询缓存；迁移时不能把它误作 Cookie credential 或删除响应字段。

前端本地错误 `AUTH_STORAGE_UNAVAILABLE`、兼容 fallback `AUTH_UNAVAILABLE`、`INVALID_RESPONSE`、`NETWORK_ERROR` 不是服务端 Problem 契约。相关行为测试位于 `web/src/api/auth.test.ts:85-211` 与 `web/src/app/auth-context.test.tsx:68-197`。

### 现有测试锁定的生命周期语义

- 应用时钟大幅漂移时，创建/过期仍由数据库时钟决定，且明文凭据不落库（`internal/auth/adapter/postgres/repository_integration_test.go:27-104`）。
- Authenticate 与 revoke 并发后，一旦 revoke 提交，后续认证全部失败（同文件 `:106-171`）。
- 20 轮并发 rotate 每轮恰好一个成功、一个失败；旧凭据失效，新凭据有效（同文件 `:174-243`）。
- 过期、撤销、损坏 scope 的 Session/API token 均失败关闭（同文件 `:327-483`）。
- 重复/失败轮换会回滚旧 Session 撤销；并发 `last_seen_at` 更新不破坏状态（同文件 `:503-580`）。
- 非规范 scope 行被拒绝，readiness 检查两个 Auth 表（`internal/auth/adapter/postgres/repository_test.go:18-32`, `:70-88`）。
- Migration 创建约束、可重复 up/down，非空 Auth 表阻止 down（`internal/platform/migration/auth_integration_test.go:14-135`）。
- HTTP 测试锁定 Cookie 属性、Origin/CSRF、Bearer 优先级、Session-only 管理端点、未知 body 拒绝和安全错误响应（`internal/auth/http/handler_test.go:158-303`, `:366-570`）。
- Router 测试锁定业务保护、公开健康端点、Auth 初始化失败关闭和 readiness 不泄漏内部详情（`internal/app/router_test.go:505-685`）。

### `gin-contrib/sessions` 能力与差距

截至 2026-08-14，官方最新版本为 `v1.1.0`（2026-03-28 发布，MIT，模块要求 Go 1.25）。核心 API 是 Gin middleware + 懒加载 `Session`，公开 `ID/Get/Set/Delete/Clear/Flashes/Options/Save`；`Store` 嵌入 `gorilla/sessions.Store` 并增加 Cookie Options。项目当前 `go.mod` 未引入 `gin-contrib/sessions` 或 GORM。

直接 store 评估：

| 候选 | 可复用点 | 与当前契约冲突 |
|---|---|---|
| CookieStore | Cookie 编码、签名/可选加密、属性、Gin accessor | Session 值在客户端成为事实源；无法由 PostgreSQL 即时撤销、轮换或权威过期，直接违反强制约束。 |
| 官方 PostgreSQL Store | Cookie session ID、`database/sql` 持久化、过期清理 | 使用 `antonlindstrom/pgstore` 自动创建 `http_sessions`，保存原始生成 key 和编码数据，使用应用 `time.Now`；没有项目 migration、哈希凭据、CSRF hash、scope、last-seen/revoked、原子轮换及稳定错误码。会形成第二表/第二状态语义。 |
| 官方 GORM Store | ORM 持久化、Cookie transport | 使用 `wader/gormstore/v2` 自迁移 `sessions` 表；底层仍基于旧 `github.com/jinzhu/gorm` API，应用时钟和通用 Session blob 语义同样不满足生命周期约束。TODO 10 的 GORM 接入目前也尚未落地。 |
| 自定义 Store | 理论上可只解析/写入单个 opaque credential，并复用 Gin accessor/options | 必须重新实现 `gorilla.Store.Get/New/Save` 适配、保留 raw 43-char wire token 或做双读迁移、绕过/补足明确 `Expires`、把 store 错误映射回稳定 Problem。框架 `Session()` 在 `Get` 失败时只记录错误而不向调用方返回，天然不适合当前失败关闭和错误区分。数据库生命周期仍必须全部走现有 Repository。 |

框架 Options 有 `Path/Domain/MaxAge/Secure/HttpOnly/SameSite`，但没有由数据库权威时间直接传入的 `Expires` 字段；Gorilla 会根据 `MaxAge` 与应用时钟构造过期时间。当前 Handler 直接使用 Repository 返回的 `expires_at`，这不是等价替换。

框架能通用化的表面仅是：Cookie 属性写入、从 Gin context 获取 Session、调用 Store Save。现实现相应代码只有两处设置 Cookie 和一处删除 Cookie，且标准库完整支持所需 `Expires` 与 Cookie 属性。引入框架反而增加 Gorilla/securecookie/store、签名 key 配置和错误适配，并可能改变 Cookie wire format。

因此最小决策是：

1. **推荐边界：不接入。** 保留 `net/http` Cookie transport 和现有 Auth Repository；这符合 ADR 0019 对“小而稳定、标准库已覆盖”的例外，也避免低收益依赖。
2. **若产品明确要求必须出现该依赖：** 只允许自定义 transport-only Store，值中仅放现有 opaque token；不得使用现成 Cookie/PostgreSQL/GORM store，不得新建表/连接池/migration，不得把 scope/用户/CSRF/过期/撤销放进框架 Session。认证后仍由现有 Service/Repository 返回 Principal。必须补双读或明确全员登出迁移、显式错误通道、数据库 `Expires` 保真及日志脱敏测试。
3. **不得替换：** `auth.session`/`auth.api_token` Schema、Application lifecycle、Repository 原子 SQL、CSRF/Origin、Capability 路由策略、API token、Workspace RootGrant、正式 write authorization、Problem 错误码和 OpenAPI/前端契约。

TODO 11 尚未给出加权需求表，不能诚实计算“覆盖 80%”。但 ADR 0019 规定先检查强制约束，任一失败即淘汰、无需进入评分；三个现成 store 都已在权威 PostgreSQL 状态、无第二存储、数据库时钟、原子轮换/撤销和错误契约上失败（`docs/architecture/adr/0019-mature-framework-first.md:21-34`）。Roadmap 也明确只允许替换通用 Cookie/Session transport，且禁止第二连接池、migration 或 Session store（`docs/roadmap.md:124-130`）。

### Files found

- `internal/auth/domain/model.go` — Principal、Session、API Token 领域模型与规范校验。
- `internal/auth/application/service.go` — 凭据生成/哈希、Auth 生命周期编排、Capability/API Token 规则和错误码。
- `internal/auth/adapter/postgres/repository.go` — PostgreSQL 权威状态、数据库时钟、原子认证/轮换/撤销与错误归一。
- `internal/auth/http/handler.go` — Cookie、Origin/CSRF、Bearer 优先级、路由 Capability 和公开 Problem。
- `internal/app/router.go` — Gin middleware 组合、公开/受保护路由与 readiness。
- `cmd/api/main.go` — 复用现有数据库构造 Auth Repository/Service/Handler。
- `migrations/00034_learning_ops_auth.sql` — `auth.session`/`auth.api_token` Schema、索引和 down guard。
- `migrations/00008_write_authorization.sql` — 独立正式写授权 Schema 与不可变/状态转换约束。
- `api/openapi/openapi.json` — Auth endpoints、security schemes、schemas 和 Problem responses。
- `web/src/api/auth.ts` — Cookie/CSRF 前端 transport、localStorage、跨 tab 失效和 API decoder。
- `web/src/app/auth-context.tsx` — 前端 Auth 状态机和 query invalidation。
- `internal/capability/capability.go` — Canonical capability 枚举。
- `internal/platform/rootgrant/grant.go` — Workspace 本机 RootGrant 边界。
- `internal/workspace/adapter/postgres/repository.go` — Active workspace 与 RootGrant 解析。
- `internal/changecontrol/domain/authorization.go` — 一次性正式写授权不变量。
- `internal/changecontrol/application/service.go` — 正式写授权签发/消费流程。
- `internal/changecontrol/http/handler.go` — 对外只开放审批/预检，不暴露内部写凭据。
- `internal/auth/adapter/postgres/repository_integration_test.go` — 生命周期、数据库时钟和并发语义集成测试。
- `internal/auth/http/handler_test.go` — HTTP/Cookie/Origin/CSRF/Bearer 契约测试。
- `internal/app/router_test.go` — 路由组合与 fail-closed 测试。
- `web/src/api/auth.test.ts`、`web/src/app/auth-context.test.tsx` — 前端 transport/状态契约测试。

### External references

- [`gin-contrib/sessions` v1.1.0 release](https://github.com/gin-contrib/sessions/releases/tag/v1.1.0) — 当前官方版本与发布日期。
- [`sessions.go` v1.1.0](https://github.com/gin-contrib/sessions/blob/v1.1.0/sessions.go) — middleware、Store/Session API、懒加载与 `Get` 错误处理。
- [`session_options_go1.11.go` v1.1.0](https://github.com/gin-contrib/sessions/blob/v1.1.0/session_options_go1.11.go) — Cookie Options 字段。
- [Official PostgreSQL adapter](https://github.com/gin-contrib/sessions/blob/v1.1.0/postgres/postgres.go) — `pgstore` 适配器构造。
- [Official GORM adapter](https://github.com/gin-contrib/sessions/blob/v1.1.0/gorm/gorm.go) — `wader/gormstore/v2` 适配器构造。
- [`antonlindstrom/pgstore` implementation](https://github.com/antonlindstrom/pgstore/blob/e3a6e3fed12a/pgstore.go) — 自动 Schema、key/data/expiry 与应用时钟实现。
- [`gorilla/sessions` Store contract](https://github.com/gorilla/sessions/blob/v1.4.0/store.go) — `Get/New/Save` 和 CookieStore 合约。

### Related specs

- `.trellis/spec/backend/auth-security.md` — Session/API Token、Origin/CSRF、Capability、数据库时钟和凭据日志红线。
- `.trellis/spec/backend/http-boundary.md` — Gin 仅在 HTTP 边界、标准 `request.Context` Principal、严格请求/响应。
- `.trellis/spec/backend/error-handling.md` — Problem、安全错误与稳定 error code。
- `.trellis/spec/backend/database-guidelines.md` — migration 权威、事务与一致性。
- `.trellis/spec/backend/workspace-root-grant.md` — Active workspace 与本机 grant 的独立所有权。
- `.trellis/spec/guide/cross-layer-contract-thinking.md` — 跨层契约核对要求。
- `.trellis/spec/frontend/api-client.md`、`.trellis/spec/frontend/component.md` — 前端统一 API transport 与 Auth 状态边界。
- `docs/architecture/adr/0019-mature-framework-first.md` — 强制约束、80% 评分和标准库例外。
- `docs/roadmap.md:124-130` — TODO 11 的边界、依赖与验收条件。

## Caveats / Not Found

- 本研究初稿生成时 TODO 11 PRD 尚未冻结；随后已在同一任务目录的 `prd.md` 和 `research/session-framework-coverage.md` 中补齐强制项、权重、二元计分规则、验收用例和回滚方案。实施时以后两者为冻结决策输入。
- Roadmap 写明 TODO 11 依赖 TODO 10（GORM），但当前 `go.mod` 没有 GORM，依赖尚未满足。即使未来引入 GORM，也不能据此授权框架自迁移 Session 表。
- Roadmap 要求 PostgreSQL 对 Session 的“版本”权威，但当前 `auth.session`、Domain Session 和 OpenAPI `SessionInfo` 均没有 `version` 字段。需澄清这是未来新契约、对轮换代际的泛称，还是文档漂移；不能在框架评估中假设已有版本语义。
- 没有找到专门的“并发登录”测试。现有 Schema/Service 清楚表达多 Session 可共存，但仍应补一个并发创建且互不失效的集成测试，以把该推断固定为契约。
- Auth OpenAPI 没有声明运行时可能出现的 `409`：既包括极罕见 UUID/token 唯一冲突的 `AUTH_CREDENTIAL_CONFLICT`，也包括损坏数据库行使用的 `AUTH_DEPENDENCY_UNAVAILABLE`。迁移前应统一运行时与规范，并决定损坏行究竟应保持 conflict 还是对外统一 dependency-unavailable。
- `session_id` 虽不是 credential，却已由公开响应和前端消费；同时安全规范禁止在日志/错误中记录 Session ID。任何框架默认 debug/logging 必须验证不会输出 Cookie、ID 或 Store payload。
- 外部源码结论基于 `gin-contrib/sessions` v1.1.0、其官方适配器及固定依赖源码；若实施时版本改变，需重新核对 API、Cookie wire format、底层 store 和 Go/Gin/GORM 兼容性。
