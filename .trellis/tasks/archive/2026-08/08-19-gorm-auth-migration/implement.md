# Auth Repository GORM 迁移实施清单

## Phase 1：任务与行为基线

1. [x] 激活本 child，加载 Auth、安全、数据库、错误、日志、质量与跨层规范。
2. [x] 盘点 `00034` 表/约束/索引、唯一生产构造点、pgx 测试桩和现有 PostgreSQL 集成门禁。
3. [x] 固定 TODO 9 前不切 `cmd/api`、不删除 pgx Repository、不修改 migration 的阶段边界。

## Phase 2：共享持久化映射

1. [x] 新增私有 Session/API Token persistence record，显式列、UUID/JSONB、数据库时间与非软删映射。
2. [x] 让 pgx 与 GORM scanner 共享 record -> Domain 的 ID、scope 和不变量校验。
3. [x] 提取双表 readiness、创建、轮换和认证的安全关键 SQL 常量，保持参数化与原语句完全一致。

## Phase 3：未接线 GORM Repository

1. [x] 新增 `GORMRepository` / `NewGORMRepository` 并实现 `application.Repository`，拒绝 nil root/context。
2. [x] 用 GORM Raw 保持 DB-time create、单 CTE rotate 和原子 authenticate；用 builder 实现 revoke 与有界 keyset list。
3. [x] 保持 no-row、23505、not-found、unavailable、corrupt scope、credential 脱敏与 context cause 语义。
4. [x] 不使用 `AutoMigrate`、GORM soft delete、Go 时钟、Offset、字符串拼接输入或默认 SQL logger。

## Phase 4：局部验证与审查

1. [x] 运行 Auth 现有 test/race、vet、vendor 模式编译、API compile-only 与 `git diff --check`。
2. [x] 静态确认 Application/Domain 无 GORM/sql/pgx 泄漏，生产 Composition 仍使用 pgx Repository。
3. [x] 使用 go-review 与 sql-code-review 审查事务原子性、并发、分页、错误分类、Credential 与资源关闭。
4. [x] 使用 trellis-check 验证任务边界、规范、差异与父任务依赖。

## Phase 5：TODO 9 真实 PostgreSQL 门禁

1. [x] 将既有 Auth integration fixture 接入 `testdb.Require`，由同一个 `platformpostgres.Pool` 分别构造 legacy 与 GORM Repository；没有复制容器、迁移或清理生命周期。
2. [x] 真实 PostgreSQL `-race` 回归成对验证 DB time、明文不落库、authenticate/revoke 竞态、并发 rotate、101 行 keyset、expired/revoked/corrupt scope、23505 conflict 与 CTE rollback。
3. [x] 重跑受影响包 test/vet、Trellis 校验、Go/SQL/Trellis review 与 `git diff --check`；证据见 `research/real-postgresql-validation-2026-08-27.md`。

## Phase 6：Final Handoff

1. [x] PRD Acceptance Criteria 与任务状态已按真实 PostgreSQL 证据更新；本 child 已完成。
2. [x] 生产 Composition 继续使用 legacy pgx Repository，legacy 文件和 Schema 均未修改；它们属于最终 Composition 收口任务，不被本 child 宣称完成。
3. [x] 主会话已复核本次 diff 和证据并完成归档；本 child 不自行提交。
