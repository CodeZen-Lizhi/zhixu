# GORM 平台与事务基础实施清单

## Phase 1：任务与依赖

1. [x] 激活本 child，加载后端数据库、错误、日志和质量规范。
2. [x] 固定 GORM、PostgreSQL Driver、River database/sql Driver 版本，同步 `go.mod/go.sum/vendor`，核对许可证与 River 版本一致性。
3. [x] 保留现有 `go.sum` 用户改动，仅审查本任务新增依赖差异。

## Phase 2：共享连接与 GORM 根

1. [x] 扩展 `internal/platform/postgres.Pool`，由 `postgres.Open` 创建共享 `*sql.DB` facade 与 GORM root。
2. [x] `OpenMigration` 保持 pgx-only；`DB()`、`Begin()` 和所有现存调用方保持兼容。
3. [x] 集中实现显式、安全 GORM Config；禁止自动 Ping、prepared statements、自动 migration 与错误泛化。
4. [x] 固定初始化失败清理和 `sql.DB facade -> pgxpool` 关闭顺序。

## Phase 3：opaque transaction scope

1. [x] 在 `internal/foundation` 定义无数据库依赖的 isolation、options、scope、callback 与 Unit of Work Port。
2. [x] 在 `internal/platform/postgres` 实现 GORM Unit of Work，唯一解包 `*gorm.DB/*sql.Tx`。
3. [x] 校验 nil context/callback、未知 isolation、未知/过期 scope；回调成功提交、错误回滚交给 GORM 正式事务实现。
4. [x] 不修改现存 `transaction any`/`pgx.Tx` 契约；由 28 个模块 child 按 owner 迁移。

## Phase 4：River database/sql producer

1. [x] 新增仅供 GORM 路径使用的 typed River inserter，事务参数为 opaque scope。
2. [x] 复用现有参数校验、queue、unique、scheduled、trace metadata、receipt 和错误语义；新 fence 只接 opaque scope。
3. [x] 保留 `riverpgxv5` Client、Worker、listener、migrator 和旧 pgx inserter，不修改生产 Composition。
4. [x] 将 Workflow、Export、Retrieval、Model Settings 调用点及迁移 owner 记录为后续任务门禁。

## Phase 5：局部验证与审查

1. [x] 运行受影响包现有测试、vendor 模式编译、`go vet`、`go mod verify` 和 `git diff --check`。
2. [x] 使用 go-review 审查 Context、事务生命周期、错误包装、资源关闭与公共契约。
3. [x] 使用 sql-code-review 审查同事务 River 路径、参数化、安全日志、连接池与错误分类。
4. [x] 使用 trellis-check 复核规范、任务边界、依赖与差异。
5. [x] 运行真实 PostgreSQL/River 原子性、Worker 消费、取消、ACK 丢失重放和池生命周期验收；将网络层 COMMIT response loss、目标环境容量与业务 owner 错误矩阵记录为剩余门禁，保持任务 `in_progress`、不得归档。
