# gin-contrib/sessions 覆盖矩阵

## 结论

评估版本固定为 `github.com/gin-contrib/sessions v1.1.0`。它兼容 Go 1.25/Gin 1.12，采用 MIT License，但四条可用路径均未通过强制约束门禁；无需用加权平均掩盖失败。为便于复核，仍对最有利的“核心 middleware + 自定义 Store”路径计算反事实覆盖率：**6/100**，远低于 80%。

`gin-contrib/sessions` 不进入生产依赖。当前最小实际可替换范围为零：标准库已经完整覆盖两处设置 Cookie 和一处删除 Cookie；请求身份、权威状态与所有生命周期仍必须由项目 Auth Application/Repository 拥有。

## 强制约束和加权覆盖

评分只计算框架完整接管且可删除项目实现的能力。每行二元计分：整行契约全部满足则得该行全部权重，任一子约束未满足则得 0 分；不使用未事先定义的主观“部分分”。由现有代码继续实现、或把现有代码包入 custom Store，不计框架覆盖。

| 需求 | 强制 | 权重 | v1.1.0 + custom Store 覆盖 | 得分 | 项目保留/证据 |
|---|---:|---:|---|---:|---|
| Cookie 只携带 opaque 随机 Token，PostgreSQL 只存摘要 | 是 | 12 | 无；现成 Store 改变 wire/store，自定义 Store 只能复用现有逻辑 | 0 | `internal/auth/application/service.go`、`auth.session` |
| 唯一 PostgreSQL Session 表、共享连接池和正式迁移入口 | 是 | 10 | 无；PostgreSQL/GORM Store 自带连接和表，自定义 Store 不接管 | 0 | `cmd/api/main.go`、`migrations/00034_learning_ops_auth.sql` |
| 数据库时钟、原子认证和 `last_seen_at` | 是 | 10 | 无 | 0 | `AuthenticateSession` 单条 `UPDATE ... RETURNING` |
| 即时撤销与提交后 fail closed | 是 | 8 | 无 | 0 | Repository 撤销/认证竞态测试 |
| 原子轮换、并发只允许一个赢家 | 是 | 8 | 无 | 0 | CTE rotate 与 20 轮并发测试 |
| 明文 Token/CSRF、Cookie、Session ID 不进入日志/Store payload | 是 | 8 | 无；框架懒加载失败会自行 `slog.Error`，需额外适配 | 0 | Auth 安全规格、`sessions.go` |
| 稳定 Problem、Bearer 优先且失败不回退 Cookie | 是 | 7 | 无 | 0 | Auth HTTP middleware |
| Cookie `Path/HttpOnly/Secure/SameSite/MaxAge/Expires` 完全兼容 | 是 | 7 | 否；Options 没有数据库权威 `Expires`，整行不满足 | 0 | `session_options_go1.11.go`、Auth Handler |
| Origin 与 CSRF 继续独立且精确校验 | 是 | 7 | 无 | 0 | Auth HTTP/Application |
| API Token、Capability、Workspace/Write Authorization 独立 | 是 | 7 | 无 | 0 | Auth、RootGrant、Change Control |
| Go/Gin 兼容、许可证、维护与测试基线 | 是 | 6 | 完整：Go 1.25、Gin 1.12、MIT、官方 CI | 6 | v1.1.0 `go.mod`、LICENSE、release |
| 删除重复代码并降低净复杂度 | 否 | 10 | 否；仅请求内 accessor，最多替代薄 Cookie 调用，但增加 Gorilla/Store/error/config | 0 | 当前 HTTP Cookie 代码与框架依赖图 |
| **合计** |  | **100** |  | **6** |  |

强制项在唯一事实源、数据库时钟、原子认证/轮换/撤销、错误语义和 Cookie `Expires` 上失败，因此即使总分达到 80 也不能采用；反事实二元计分也只有 6 分。

## 候选路径比较

| 路径 | 结果 | 硬阻塞 |
|---|---|---|
| Cookie Store | 拒绝 | 客户端 Values 成为第二状态；不能即时撤销或使用数据库权威过期 |
| 官方 PostgreSQL Store | 拒绝 | 独立 `database/sql`/`pgstore`、`http_sessions` 与应用时钟，形成第二连接/表/迁移 |
| 官方 GORM Store | 拒绝 | TODO 10 未完成；`gormstore` 自有表和 blob 生命周期仍不是项目 `auth.session` |
| 自定义 Store | 拒绝 | 必须保留全部现有认证链，只新增 Gorilla 接口、懒加载错误、Save 时序和 wire 兼容工作 |

## 依赖与供应链

- v1.1.0 顶层模块直接声明全部后端依赖，包括 `pgstore`、Redis、Mongo、Memcached、GORM/SQLite 与 Gorilla；Go MVS 虽只编译被导入包，但 module graph、校验与更新面仍扩大。
- 项目当前 `go.mod`、`go.sum` 和 vendor 均没有 `gin-contrib/sessions`、`gorilla/sessions`、`pgstore` 或 `gormstore`。
- License 是 MIT，Go 版本为 1.25.0，Gin 依赖为 v1.12.0；兼容性不是拒绝原因。

## 最小自研边界与维护责任

- 标准库 `net/http` 只负责读写固定 Cookie 属性；不实现通用 Session Values、签名 Cookie 格式或多后端 Store。
- Auth Application 生成 CSPRNG Token/CSRF、哈希并编排生命周期；PostgreSQL Repository 拥有数据库时钟与原子状态。
- HTTP Adapter 拥有 Origin/CSRF、Bearer 优先、Capability 和安全 Problem；业务层只接收项目 Principal。
- 维护者必须继续运行 Auth HTTP/应用/真实 PostgreSQL/race/OpenAPI/Composition 门禁，并保持日志/错误无凭据。

## 退出路径与重新评估触发条件

只有同时出现以下变化时重新评估：

1. TODO 10 已完成且候选能复用统一 GORM transaction boundary，不自建表、迁移或连接池；
2. 候选原生支持 opaque-token digest lookup、数据库时钟、即时撤销、原子 rotate CAS 和稳定错误传播；
3. 能删除实质 Session 生命周期代码，而不是把现有 Service/Repository 包进 Store；
4. 模块依赖可裁剪，且安全/并发/兼容测试证明净收益。

未来若候选满足门禁，先以 HTTP Adapter behind-interface 试点，保持现有 Cookie wire 双读或明确全员登出边界；逐批行为对等后再删除旧投影。任一门禁失败时直接移除 Adapter/依赖，不需要数据回滚。

## 证据

- 本仓库所有权审计：`research/current-session-contract.md`
- 项目门禁：`docs/architecture/adr/0019-mature-framework-first.md`
- 官方核心：`github.com/gin-contrib/sessions@v1.1.0/sessions.go`
- 官方 Options：`session_options_go1.11.go`
- 官方 PostgreSQL/GORM/Cookie adapter：v1.1.0 对应子目录
- Gorilla Store：`github.com/gorilla/sessions@v1.4.0/store.go`
