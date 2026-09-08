# Collection Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

在不改变 Collection 对外行为和生产接线的前提下，新增完整的 staged GORM Repository，并建立不暴露 pgx/GORM/database/sql/`any` 的 durable scan scoped transaction Port。TODO 9 真实 PostgreSQL 门禁通过后，才允许由后续 owner child 与 Final 切换生产 Composition。

## Requirements

### R1. 阶段边界与兼容性

- 迁移 owner 范围为 `internal/collection/adapter/postgres`；仅为 opaque durable scan Port 对 `internal/collection/application` 做最小增量。
- 保留现有 `Repository`、`NewRepository(DB)`、`VerifyDurableScanBinding(ctx, pgx.Tx, ...)` 和所有 pgx 生产/测试调用点。
- 新增 `GORMRepository`，完整实现主 Repository、Search、Query、Preview、Durable Scan 与 Revision Verifier 能力，但 TODO 9 前不接入 `cmd/**`、Graph、Health、Export 或 Organizing。
- 禁止运行时 selector、双读、双写、静默 fallback、独立连接池和半迁移 Composition。

### R2. 写事务与幂等契约

- Create、Update、Archive 必须由同一个平台 `UnitOfWork` 拥有事务，保持 Workspace 锁、immutable receipt 查找、aggregate `FOR UPDATE`、version CAS、aggregate 写入、receipt 写入和 commit 的既有顺序。
- 精确重放必须返回 receipt 中的原始 aggregate snapshot；同 key 不同 request/type/aggregate 必须 fail closed。
- 保持 active name 部分唯一约束、query version 递增、archive 不可变和数据库 trigger/SQLSTATE 语义；禁止 delete 和 AutoMigrate。

### R3. 快照、分页与 durable scan

- List、Results、Preview、Plan/Read/Revision Verify 继续使用 `REPEATABLE READ READ ONLY`，并在同一事务内执行 `SET LOCAL statement_timeout='1500ms'`。
- 保持 Workspace 隔离、精确 count、revision vector、稳定 keyset、`NULLS LAST`、Limit+1、同快照 hydration 和 cursor/binding 校验。
- Durable binding 继续固定 `CollectionVersion + QueryHash + ReadModelRevision + ExactCount`；checkpoint 必须仍是成员，definition/revision/count drift 必须返回 stale。
- 新增 scoped binding verifier，只从 active `foundation.TransactionScope` 解出 caller transaction，不 commit/rollback、不从 root DB fallback；legacy pgx verifier继续服务未迁移的 Graph/Health。

### R4. 动态 SQL 与参数安全

- 动态字段、操作符和排序仅来自现有 Registry/Compiler 白名单；不得新增用户可控 identifier 拼接。
- GORM 路径必须把最终完整 SQL 的 PostgreSQL `$n` markers 转为 `?`，按每次 marker 出现顺序复制对应参数；支持重复 marker 和多位序号，并拒绝越界、零值、缺失或未消费参数。
- `ANY(?::text[]/uuid[])`、`unnest(?::text[],?::uuid[])` 必须使用单个 `driver.Valuer` array carrier；禁止把裸 slice 交给 GORM 展开。
- JSONB 写入使用返回 string 的私有 `driver.Valuer`；读取使用显式 text/scanner 并继续执行 canonical query/view/receipt 校验。

### R5. 错误、取消、资源与安全

- 保持现有 Collection error kind/code/retryability 和精确 SQLSTATE 集合，不扩大可重试范围。
- no-row 同时识别 pgx、database/sql 和 GORM；cancel/deadline 保留标准 sentinel 与 `context.Cause`，`sql.ErrTxDone` 安全分类。
- 所有 Row/Rows 路径 fail closed；Rows 必须 Close 并检查 Err，损坏 JSON/array/ID/revision/hydration 不得返回部分结果。
- 错误、日志与 Review 证据不得泄漏 SQL 参数、查询正文、receipt、DSN、Secret 或绝对路径。

### R6. 验证与完成门禁

- 使用现有单测、race、vet、integration compile、受影响 Composition compile、vendor/module 和静态接线检查。
- TODO 9 必须在 disposable PostgreSQL 上，从同一 `platformpostgres.Pool` 构造 pgx、GORM root 与 Unit of Work，比较 legacy/GORM 的事务、查询、分页、revision、durable scan、SQLSTATE、取消和执行计划。
- TODO 9 不可用时只能交付未接入 Composition 的 staged 实现，不得勾选本 PRD AC、完成或归档 child；TODO 3 仅阻断 Final。

## Acceptance Criteria

- [x] staged `GORMRepository` 实现 Collection 全部 Repository 能力，生产仍使用 legacy pgx。
- [x] Create/Update/Archive 的锁、CAS、immutable receipt、精确重放、archive 与 SQLSTATE 契约在真实 PostgreSQL 上等价。
- [x] 全部注册字段/操作符、重复 `$n`、array/JSONB carrier、稳定 keyset、NULL tail、cursor/revision stale 和 bounded hydration 在真实 PostgreSQL 上等价。
- [x] durable scan 的 plan/read/restart/checkpoint/definition/revision/count drift 与 caller-owned scoped verification 在真实 PostgreSQL 上等价。
- [x] context、timeout、rollback/commit、连接释放、错误脱敏和目标索引/有界执行计划通过门禁。
- [x] 局部 test/race/vet/compile、module/vendor、Trellis validate、gofmt 与 `git diff --check` 通过。

## Out Of Scope

- 生产 Composition 切换、删除 pgx、迁移 Graph/Health 的 transaction Port 或其他 owner Repository。
- 修改 Query Registry、Domain 语义、HTTP/API、cursor wire format、Schema、migration、trigger 或索引。
- AutoMigrate、ORM association/preload、缓存、物化结果、双写、数据回填和新的 cleanup/delete 行为。
- 在本 child 修复 Foundation scope 缺少 Pool identity 的共享限制。

## Dependencies

- 前置：`gorm-platform-transaction-foundation` 提供共享 Pool、GORM root、Unit of Work 和 opaque transaction scope。
- 下游：Graph、Health 后续 child 使用新 scoped verifier；Export、Organizing 继续消费既有 Collection Application 能力；Final 统一切 Composition 和移除 legacy。
