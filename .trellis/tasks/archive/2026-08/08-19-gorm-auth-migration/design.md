# Auth GORM Repository 设计

## 1. 目标与阶段边界

本 child 先实现一个未接入生产 Composition 的 Auth GORM Repository，保持现有 `application.Repository`、领域类型、HTTP 契约与 pgx Repository 可用。TODO 9 真实 PostgreSQL 门禁通过前：

- 不修改 `cmd/api` 的 Auth 构造；
- 不删除或重命名现有 pgx `Repository` / `NewRepository`；
- 不勾选 PRD Acceptance Criteria，不完成或归档 child；
- 不新增 migration、`AutoMigrate` 或 GORM Migrator 调用。

## 2. Schema 与安全事实源

`migrations/00034_learning_ops_auth.sql` 是 `auth.session` 与 `auth.api_token` 的唯一 Schema、约束和索引事实源。GORM persistence model 仅负责显式列映射，不生成或修改 Schema。

必须保持：

- UUID 主键、64 位小写 SHA-256 hash、canonical JSONB scopes；
- 所有生命周期时间由 PostgreSQL `CURRENT_TIMESTAMP` 产生；
- `revoked_at` 是可读取的业务状态，不使用 `gorm.DeletedAt`；
- API Token 列表按 `(created_at DESC, id DESC)` 做 `limit+1` keyset 分页；
- 明文 Bootstrap、Session、CSRF 与 API Token 永不进入 Repository、日志或错误。

## 3. 文件与类型

### `internal/auth/adapter/postgres/model.go`

定义私有 `sessionRecord` 与 `apiTokenRecord`，显式声明 schema-qualified `TableName`、列名、UUID/JSONB 和关闭自动时间。record 只在 Adapter 内存在，并通过统一 mapper 转成 Domain；ID、scopes 与时间不变量仍由 `foundation.ParseID`、`decodeScopes` 和 Domain validator fail closed 校验。

现有 pgx scanner 与新 GORM scanner 共享 record mapper，避免凭据校验出现两个事实源。

### `internal/auth/adapter/postgres/queries.go`

集中保存新旧实现语义相同的参数化 SQL：双表 readiness、Session/API Token 创建、Session 单语句轮换和 Session/API Token 原子认证。无参数语句直接复用；有参数语句将 pgx `$n` 与 GORM `?` 版本并列保存，因为 GORM Raw 只解析 `?`/命名参数。两种版本只允许占位符语法不同，避免安全关键语句分散漂移。

### `internal/auth/adapter/postgres/gorm_repository.go`

新增 `GORMRepository` 与 `NewGORMRepository(*gorm.DB)`，实现 `application.Repository`。构造参数只在 Adapter 边界暴露 GORM，Application/Domain 不出现 GORM、database/sql 或 pgx。

## 4. 查询与事务策略

| 操作 | GORM 路径 | 必须保持的语义 |
| --- | --- | --- |
| Check | `Raw` | 同时读取 `auth.session` 与 `auth.api_token`，任一表不可读即 unavailable |
| Create Session/API Token | `Raw ... RETURNING` | DB time、微秒 TTL、持久事实回读、23505 conflict |
| Rotate Session | 单条 `Raw` CTE | 旧 Session 撤销与新 Session 插入全成全败；并发仅一个赢家 |
| Authenticate Session/API Token | 单条 `Raw UPDATE ... RETURNING` | 认证与 last-seen/last-used 更新原子，未过期未撤销，时间不倒退 |
| Revoke | GORM `Table/Where/Updates` + `gorm.Expr` | `COALESCE(revoked_at,CURRENT_TIMESTAMP)`，重复撤销幂等；0 行错误保持区别 |
| List API Token | GORM `Table/Select/Where/Order/Limit` | 固定列、固定排序、UUID tie-breaker、`limit+1`，无 Offset |

Auth 当前没有跨语句业务事务：Rotate 已由单条 CTE 保证原子性，其余命令均为单条 SQL。因此本 child 不把 `foundation.UnitOfWork` 泄漏进 Auth Application；未来若出现多语句不变量，只能由 GORM transaction scope 持有并在该 transaction 上执行同一 SQL。

## 5. 错误与资源语义

- `database/sql.ErrNoRows`、`pgx.ErrNoRows` 与 GORM record-not-found 统一映射为既有 `AUTH_UNAUTHORIZED`，不得区分凭据不存在、过期或撤销。
- `*pgconn.PgError` SQLSTATE `23505` 保持 `AUTH_CREDENTIAL_CONFLICT`；Foundation GORM 配置维持 `TranslateError:false`。
- 其余数据库错误，包括 context cancel/timeout，保持 retryable `AUTH_DEPENDENCY_UNAVAILABLE` 并用 `%w` 保留 cause；错误文本不得包含 SQL 参数、hash 或 DSN。
- `Rows()` 必须关闭并检查迭代错误；列表始终有界。

## 6. 验证与回滚

静态阶段运行现有 Auth 测试、race、vet、受影响命令编译、禁止 API 泄漏/AutoMigrate 检查与 `git diff --check`。现有快速测试仍锁定 pgx 基线；由于 `ZHIXU_TEST_DATABASE_URL` 未配置且不新增测试文件，GORM 的真实 SQL、并发、回滚与错误分类必须记录为 TODO 9 阻断证据。

回滚仅移除 GORM Repository、persistence model/共享 SQL 提取并还原本 child 的 scanner 小改；不涉及 Schema、数据和生产 Composition 回滚。
