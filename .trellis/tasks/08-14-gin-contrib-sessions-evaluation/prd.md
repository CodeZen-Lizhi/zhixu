# 评估并接入 gin-contrib/sessions

## Goal

完成需求优化清单 TODO 11：按成熟框架门禁评估 `gin-contrib/sessions` v1.1.0 是否能在不削弱现有认证安全、数据一致性和部署边界的前提下接管通用 Session 机制。满足全部强制约束且加权覆盖率达到 80% 时接入；否则形成可复核的不采用 ADR，保留最小现有实现并明确重新评估条件和退出路径。原清单将 TODO 10 列为评估前置；本任务若要在 TODO 10 完成前以“正式评估后不采用”关闭 TODO 11，必须先获得用户对这项依赖语义变更的明确确认并记入 ADR。

## Background And Confirmed Facts

- Gin v1.12.0 已是唯一生产 HTTP Router，TODO 5 已完成。
- TODO 10 的 GORM 数据访问全面迁移尚未完成；当前认证 Repository 使用项目共享 `pgxpool`，没有 GORM transaction boundary。
- 原始需求因此把 TODO 10 视为 TODO 11 的前置。当前提前评估只能在用户明确批准后改写为：TODO 10 不再阻止本次“不采用”结论，而是未来重评估的必要非充分条件。
- 浏览器 Cookie 只保存 32-byte 随机 Session Token；PostgreSQL `auth.session` 只保存 SHA-256 摘要以及 CSRF 摘要、Scope、数据库时钟生命周期、撤销状态和 last-seen。
- 每次 Session 请求都通过单条 PostgreSQL `UPDATE ... WHERE revoked_at IS NULL AND expires_at > CURRENT_TIMESTAMP RETURNING` 原子认证；轮换用一条 CTE 原子撤销旧 Session 并创建新 Session，并发轮换只允许一个成功。
- 当前 Session 没有独立可变 `version` 字段；版本代际由不可变 Session UUID 和轮换创建的新行表达。TODO 11 中的“版本”按该现行契约收敛，不为引入框架新增第二套可变版本状态。
- `gin-contrib/sessions` v1.1.0 是 Gin/Gorilla Session Values/Store 中间件。内置 PostgreSQL Store 使用独立 `database/sql` + `pgstore` 表；GORM Store 使用独立 `gormstore`。其核心抽象不拥有项目的摘要认证、数据库时钟、即时撤销、原子轮换、Capability、CSRF/Origin 或写授权语义。
- 该模块采用 MIT License，支持 Go 1.25 和 Gin v1.12.0；版本与许可证本身满足项目基线。

## Requirements

### R1. 成熟框架覆盖门禁

- 对 Cookie 属性、Session ID 传输、Store 生命周期、创建、读取、轮换、撤销、过期、并发登录、CSRF、Origin、API Token、Capability、Workspace 权限和 Approval Write Authorization 建立带强制项和权重的覆盖矩阵。
- 任一安全、权威状态、事务、部署、许可证、运行时兼容或可测试性强制项不满足即不得接入；强制项满足后才计算 80% 加权覆盖率。
- 调研项目已有实现、标准库、`gin-contrib/sessions` 核心、自带 Cookie/PostgreSQL/GORM Store 与自定义 Store 路径。

### R2. 权威状态与安全边界不变

- PostgreSQL 继续作为 Session ID、摘要、撤销、版本、Scope、用户绑定、数据库时钟和 last-seen 的唯一事实源。
- Cookie 继续只携带不可预测明文 Token；数据库、日志、错误、Audit、Metric 和 Trace 不保存或回显明文 Token、Cookie 或 Session ID。
- Session 认证、撤销和轮换的原子性及 fail-closed 行为不得降级；无效 Bearer 不得回退 Cookie。
- CSRF、Origin、API Token、Capability、Workspace 数据权限和 Approval Write Authorization 保持独立门禁，不进入 Session Values 或框架全局状态。
- 不创建第二连接池、第二 Session 表、第二迁移事实源或并行 Store 生命周期。

### R3. 采用或不采用的收口规则

- 若采用，框架只进入 Gin/HTTP Adapter 边界，并删除被其完整覆盖的重复实现；Domain/Application/Repository 不依赖框架类型。
- 若自定义 Store 只是把现有完整认证链包进 Gorilla Store 接口，不能删除实质代码或降低维护复杂度，则视为未覆盖，不以“能够适配”冒充“值得采用”。
- 若不采用，新增 ADR 记录候选、覆盖率、硬阻塞、最小自研边界、维护责任、退出路径和重新评估触发条件；不得把依赖加入 `go.mod`/vendor 或保留未使用适配器。
- 现有认证行为有直接测试证据时复用；对覆盖矩阵和决策关键事实增加可重复的静态合同检查，防止文档与依赖状态漂移。
- 补充并发创建多个 Session 且互不失效的真实 PostgreSQL 专项测试；多会话共存是当前产品语义，不能只从 Schema 推断。

### R4. 文档和任务状态一致

- 同步 ADR 索引、认证规格、路线图和 2026-08-01 需求优化清单。
- TODO 11 只有在用户明确批准 TODO 10 依赖语义变更，且覆盖矩阵、ADR、合同检查、安全回归和 Review 全部通过后才标记完成；“完成”可以是证据充分的正式不采用结论。
- TODO 10 仍保持独立未完成，不得因本任务更改为 GORM 已交付。

## Acceptance Criteria

- [ ] AC1：覆盖矩阵逐项给出强制性、权重、二元计分规则、框架覆盖、项目保留和证据位置，并得出可复算结论。
- [ ] AC2：明确验证 `gin-contrib/sessions` v1.1.0 的核心、PostgreSQL/GORM Store、自定义 Store、Go/Gin 兼容性、许可证和依赖影响。
- [ ] AC3：最终实现不改变 Session 创建、读取、轮换、撤销、过期、并发登录、Cookie 属性、Bearer 优先级、CSRF/Origin、Capability 和既有运行时错误响应；并发登录获得直接的真实 PostgreSQL 测试证据。
- [ ] AC4：PostgreSQL 仍是唯一 Session 事实源，不新增连接池、表、迁移、客户端权威状态或 Session Values 业务状态。
- [ ] AC5：若采用，删除被覆盖的重复代码且真实 PostgreSQL/安全测试证明兼容；若不采用，ADR 完整满足 ADR-0019 自研例外的七项记录要求，依赖清单保持无该框架。
- [ ] AC6：增加可重复静态合同检查，只锁定 ADR 的结构化决策标记与 `go.mod`/`go.sum`/vendor 依赖状态；关键安全边界继续由 Auth 单元、HTTP、race、真实 PostgreSQL、OpenAPI 与相关 Composition 行为测试锁定。
- [ ] AC7：Go、SQL 和跨层 Review 无未处理高严重度问题，`git diff --check` 通过，所有未执行验证及原因明确记录。
- [ ] AC8：在用户批准依赖语义变更后，路线图与需求优化清单把 TODO 11 标记为“评估完成，不采用”，同时明确 TODO 10 仍未完成且是未来重评估触发条件。

## Out Of Scope

- 实施 TODO 10 的全仓 GORM Repository 迁移，或为本任务单独引入 GORM。
- 改变认证产品流程、登录方式、Cookie 名称、公开 API、前端 UX 或 Session/API Token 的权限模型。
- 把 CSRF、Origin、API Token、Capability、Workspace 权限或写授权交给 `gin-contrib/sessions`。
- 新增 Redis/Mongo/Memcached/File 等 Session 后端，或建立客户端自包含权威 Session。
- 修正全部受保护路由在损坏认证行下可能返回 `409` 、但 OpenAPI 未全量声明的既有全局契约漂移。该问题影响范围超过 Auth 管理端点，不在本次 Session 框架选型中做局部修复；在 ADR/任务结果中保留为已知契约缺口。
