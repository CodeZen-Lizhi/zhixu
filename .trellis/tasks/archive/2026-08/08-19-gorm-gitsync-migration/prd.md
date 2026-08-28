# Git Sync Repository 迁移到 GORM

## Goal

在不改变 Git Remote Sync 领域契约、Schema、事务原子性、并发控制和错误语义的前提下，为
`internal/gitsync/adapter/postgres` 提供使用共享 GORM Root 与 Unit of Work 的完整 Repository 实现，减少普通业务
Repository 对 pgx 的直接依赖，并为 TODO 10 最终切换保留可独立审查、验证和回滚的模块边界。

## Background And Confirmed Facts

- 现有 PostgreSQL Adapter 实现 23 个 Repository 方法，覆盖 Remote Config、Credential、Run、Attempt、Outbox、
  Index Follow-up 和自动同步候选；生产实现仍由 `NewRepository(pgx, sealer)` 构造。
- `migrations/00076_git_remote_sync.sql` 是八张 Git Sync 持久表、触发器、约束和索引的结构事实源；本任务不改迁移。
- 当前实现包含 11 个显式数据库事务，并依赖 Workspace 级 `pg_advisory_xact_lock`、数据库时间、CAS、
  `FOR UPDATE`、`SKIP LOCKED`、唯一活动 Run 和 result-unknown 收敛。
- Git Sync Outbox 是模块自有持久投递事实，不是 River Job；本任务不引入 River 依赖或跨 Repository 写事务。
- 自动同步候选会只读 Change Control 的已完成 writeback；该查询保持现有有界 LATERAL SQL，不引入具体 Adapter 依赖。
- Token 只以 AES-GCM envelope 持久化，AAD 绑定 Workspace、配置 Revision、Remote URL 和 Branch；明文销毁、
  `keep|replace|clear` 与 exact replay 顺序必须保持。
- GORM Foundation 已提供共享 GORM Root、`foundation.UnitOfWork` 和 opaque transaction scope；模块不得另建事务机制。
- TODO 9 Testcontainers-Go 工厂已由 `109d2cb4` 交付。本 child 已在既有 integration fixture 中使用该工厂完成
  legacy/GORM 真实 PostgreSQL 回归；生产 Composition 仍由 Final child 收口。

## Requirements

### R1. Adapter Boundary

- 新增独立 `GORMRepository`，实现现有 `application.ConfigStore`、`RunStore`、`AutoSyncCandidateSource`、
  `FollowupStore` 和 `OutboxStore` 契约。
- 构造依赖为共享 `*gorm.DB`、与其匹配的 `foundation.UnitOfWork` 和既有 `CredentialSealer`；事务只通过
  Unit of Work 进入，并由平台唯一的 `GORMTransaction(scope)` 解包。
- GORM 仅存在于 PostgreSQL Adapter；Domain/Application/Public API 不导入或暴露 GORM、database/sql 或 pgx 类型。
- 旧 pgx `Repository`、构造器和生产 Composition 保持不变；不增加运行时 selector、双写或双读。

### R2. Persistence Models And Schema Fidelity

- 为 `ops.git_remote_config`、`git_remote_config_revision`、`git_remote_credential`、
  `git_remote_command_receipt`、`git_sync_run`、`git_sync_attempt`、`git_sync_outbox` 和
  `git_sync_index_retry_receipt` 定义显式 Persistence Model 和 `TableName()`。
- 显式声明 schema/table、列名、UUID、JSONB、bytea、nullable、时间和版本映射；禁止 `gorm.Model`、软删除、
  自动时间戳、关联自动保存、Hook 和任何 `AutoMigrate`/`Migrator` 调用。
- Migration 和现有 PostgreSQL 约束仍是唯一结构与状态机事实源；本任务不新增、修改或回写 Schema。

### R3. Query And Transaction Equivalence

- 23 个现有 Repository 方法均提供 GORM 等价实现。复杂 CTE、RETURNING、LATERAL、锁、租约和多表状态收敛
  保留参数化 GORM Raw/Exec，不为了形式改写已验证 SQL。
- 11 个现有事务保持同样的起止边界、默认隔离级别、锁顺序、提交/回滚和上下文取消语义；禁止模块直接调用
  `*gorm.DB.Transaction`、自行构造第二个 Unit of Work 或解包 `*sql.Tx`。
- Workspace advisory transaction lock、`FOR UPDATE`、`SKIP LOCKED`、数据库时间、稳定分页、RowsAffected/CAS、
  唯一活动 Run 和 Outbox lease fence 行为必须保持。
- 自动同步候选继续按完成时间与 Commit ID 稳定排序并受批量上限约束，不引入 N+1 或逐行写入退化。

### R4. Credential And Recovery Safety

- `ReplayConfig` 继续先于 URL/DNS/CAS 校验返回 exact receipt；`keep` 不得把旧密文重新绑定到新的 URL/Branch。
- Token 明文、密文、nonce、key ID、AAD digest 和密钥路径不得进入日志、错误或公共投影；所有临时明文 buffer
  和 Token 生命周期保持现有销毁行为。
- Run/Attempt/Outbox、lease/checkpoint、Index Follow-up、Poison 和 result-unknown 恢复语义保持不变；只读阶段失败
  不得被提升为 `MANUAL_RECOVERY_REQUIRED`。

### R5. Errors And Compatibility

- GORM/database/sql 的 no-row、context 和底层 `pgconn.PgError` 必须映射到现有稳定 Foundation kind/code/retryable；
  SQLSTATE `40001|40P01|55P03|57014|53300|23503|23505|23514|55000` 的既有分类不变。
- 损坏的 UUID、JSON、枚举、nullable shape、时间或 Credential envelope 继续 fail closed，不返回部分领域对象。
- Application/Domain 接口、HTTP/OpenAPI、事件、Worker 和外部 Git 行为不变。

### R6. Scope Isolation And Verification

- 产品代码范围限于 `internal/gitsync/adapter/postgres`；规划和证据仅写本任务目录。
- 禁止修改 `cmd/**`、`go.mod`、`go.sum`、`vendor/**`、`internal/platform/**`、迁移、父任务公共设计和其他模块。
- 不新增测试文件或临时测试代码。运行现有 Git Sync 单元/集成门禁、局部 vet、静态 import 检查和
  `git diff --check`；无法由现有测试覆盖的新 GORM 路径必须记录为 TODO 9 验收盲区。

## Acceptance Criteria

- [x] AC1：`GORMRepository` 完整实现五个现有 Application persistence interface，旧 pgx Repository 和生产构造不变。
- [x] AC2：八张表均有显式 Persistence Model，且不存在隐式迁移、自动时间戳、软删除、Hook 或关联写入。
- [x] AC3：23 个方法的事务、锁、数据库时间、CAS、稳定分页、Outbox lease 和自动候选查询语义与旧路径一致。
- [x] AC4：配置 exact replay、Credential AAD/销毁、Run/Attempt/Index result-unknown 和 Poison 收敛保持不变。
- [x] AC5：GORM/database/sql 错误映射不泄漏底层类型或敏感数据，Application/Domain 不新增 GORM/pgx 依赖。
- [x] AC6：无 N+1、无界查询、逐条写入或动态 SQL 标识符；复杂 SQL 均绑定参数并保留索引使用形状。
- [x] AC7：现有相关测试、局部 vet、静态检查和 `git diff --check` 通过；Go/SQL Review 的范围内缺陷已修复。
- [x] AC8：TODO 9 工厂已可用并完成本模块真实 PostgreSQL 双路径门禁；生产 Composition 切换、legacy 删除和最终全仓
  收口仍只属于 Final child。

## Key Decisions

- 采用并行 `GORMRepository`，而不是原地改写旧 Repository。
- 使用 Foundation Unit of Work 和平台唯一的 GORM transaction scope；模块不建立第二事务边界。
- 保留已验证的 PostgreSQL 专属 SQL；迁移目标是让连接、事务和 Repository 执行归 GORM 管理，不做机械 ORM 化。
- TODO 9 前只交付 staged implementation 和静态/既有门禁证据，测试缺口显式保留。

## Out Of Scope

- 修改 Migration、Schema、触发器、约束、索引或采用 GORM AutoMigrate。
- 修改 Remote Git、SSRF/DNS、AskPass、Source Capture、Change Control 或 Workspace Git operation 实现。
- 修改 API/Worker/CLI Composition、删除旧 pgx 路径或增加运行时 GORM/pgx selector。
- 实现 TODO 9 Testcontainers-Go、TODO 3 Atlas 或 TODO 10 Final Composition 收口。

## Dependencies And Deferred Gates

- 直接依赖 `08-19-gorm-platform-transaction-foundation` 已实现的共享 GORM Root 与 Unit of Work。
- TODO 9 是模块最终真实 PostgreSQL 验收和完成门禁；TODO 3 只阻断 TODO 10 Final。
- Blocking product/scope questions: none.
