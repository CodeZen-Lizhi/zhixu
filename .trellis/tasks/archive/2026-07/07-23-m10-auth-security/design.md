# M10 认证与安全闭环技术设计

## 1. 边界与不变量

- `internal/auth/domain` 定义 Session、API Token、Capability Scope 与稳定错误；不依赖 HTTP 或 pgx。
- `internal/auth/application` 负责编排 Bootstrap 交换、Token 生命周期和随机密钥生成；只依赖 Repository、Clock、Hasher/Random 等端口。
- `internal/auth/adapter/postgres` 使用摘要查询和 `UPDATE ... RETURNING` 原子完成认证、过期/撤销判断与 last-used 更新。
- `internal/auth/http` 只处理凭据提取、Cookie、Origin/CSRF、Problem Details 和请求上下文；业务权限继续由下游服务验证。
- 数据库永不保存 Bootstrap、Session 或 API Token 明文；日志、错误和审计不得包含明文。

## 2. 认证模式

配置提供显式模式：

- `disabled`：仅允许开发环境且监听地址为 loopback。
- `required`：所有业务 API 默认要求认证；Bootstrap Token 必填。

生产/自托管启动校验在 composition root 之前完成。Cookie `Secure=false` 只允许明确的 loopback HTTP 开发场景；允许 Origin 使用规范化的精确匹配列表，不接受通配符或字符串前缀匹配。

## 3. 请求流程

### 3.1 Bootstrap 换取 Session

客户端以 Bootstrap Bearer 调用 Session 创建端点。服务恒定时间比较配置摘要，创建随机 Session 与独立 CSRF Token，返回 CSRF Token 并设置安全 Cookie。Cookie 删除使用相同 Path/SameSite/Secure 属性。

### 3.2 Session 请求

中间件读取 Cookie，摘要后调用原子 Repository 认证。安全方法只需要有效 Session；状态修改方法还需精确 Origin 匹配和恒定时间 CSRF 比较。认证主体放入私有 context key。

### 3.3 API Token 请求

中间件读取 Bearer Token。Bootstrap Token 仅允许认证管理端点，不作为通用业务凭据；普通 API Token 经数据库原子认证后，把 Scope 写入认证主体。路由或 Handler 通过 `RequireCapability` 做最小权限校验。

## 4. 数据模型与并发

- `auth.session.token_hash`、`auth.api_token.token_hash` 唯一，且都是 SHA-256 十六进制摘要。
- 认证 SQL 在单条 `UPDATE ... WHERE revoked_at IS NULL AND expires_at > now() RETURNING ...` 中更新 `last_used_at`，避免先读后写竞态。
- Scope JSON 解码失败视为数据损坏并显式拒绝，不扩大权限。
- 撤销为幂等更新；已撤销或不存在统一返回无效凭据，避免对象枚举。

## 5. API 与兼容性

- 新增 Session 创建/删除、当前认证状态、API Token 创建/列表/撤销端点。
- OpenAPI 明确 Cookie 与 Bearer security scheme、一次性 Token 返回语义及 Problem Details。
- 现有业务端点路径和 DTO 保持不变，只增加认证门禁；健康、readiness、OpenAPI 与静态前端资源保持公共。

## 6. 验证与回滚

- 单元测试验证领域边界与应用编排；HTTP 测试验证 Cookie、Origin/CSRF、Bearer/Scope 和错误映射。
- PostgreSQL 集成测试验证迁移、摘要落库、原子认证、过期/撤销、损坏 JSON 与并发行为。
- Compose smoke 验证 required 模式和显式 loopback disabled 模式。
- 回滚应用可关闭新端点并恢复上一个兼容镜像；迁移采用前向兼容，默认不在用户库执行 Down。
