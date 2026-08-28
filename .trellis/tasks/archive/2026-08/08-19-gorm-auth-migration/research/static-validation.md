# Auth GORM 静态交付与验证记录

## 当前结论

- 已在 `internal/auth/adapter/postgres` 内新增并行 `GORMRepository`，实现现有 `application.Repository`，并复用统一的 persistence record、Domain 校验与错误分类。
- `cmd/api/main.go` 仍构造现有 pgx `Repository`；没有切换生产 Composition、删除旧实现或修改 migration。
- 2026-08-27 已将既有集成测试改为使用 `testdb.Require`：每个场景从同一个 `platformpostgres.Pool` 构造 legacy pgx 与 GORM 路径，并在 Testcontainers PostgreSQL 上成对通过。PRD AC 已更新，主会话复核后任务已归档为 `completed`；生产 Composition、legacy 和 migration 保持不变。

## 已交付范围

1. 新增私有 `sessionRecord` / `apiTokenRecord`，显式映射 schema-qualified 表、UUID、JSONB、数据库时间和 nullable 撤销时间；没有 `gorm.DeletedAt` 或自动时间写入。
2. pgx 与 GORM scanner 复用同一 record -> Domain mapper，继续严格验证 UUID、canonical scope JSON、hash、时间和撤销不变量。
3. 安全关键 SQL 集中保存：pgx 使用 `$n`，GORM 使用 `?` 并由 PostgreSQL Dialector 生成 `$n`。两种版本的语句结构一致，仅占位符语法不同。
4. GORM Raw 保留数据库时间创建、单 CTE Session 轮换和单语句认证；GORM builder 实现参数化撤销与 `limit+1` keyset 列表。
5. 构造器和每次调用都拒绝 nil/零值/失效 GORM root，避免依赖注入错误从稳定 unavailable 变成 panic。

## 行为对等矩阵

| 行为 | 静态实现 | 2026-08-27 真实 PostgreSQL 证据 |
| --- | --- | --- |
| Session/API Token 创建 | `CURRENT_TIMESTAMP`、微秒 TTL、`RETURNING`、canonical scopes | DB time 精度、摘要落库和明文不落库均通过；23505 由 rotate conflict 覆盖 |
| Session 轮换 | 单条 `WITH revoked ... INSERT ... RETURNING` | 并发仅一赢家；新 hash 冲突返回 `AUTH_CREDENTIAL_CONFLICT` 且旧 Session 未撤销 |
| 认证 | active predicate + `GREATEST` + `RETURNING` | authenticate/revoke 竞态提交后 fail closed；并发 last-seen 更新通过 |
| 撤销 | schema-qualified builder + `COALESCE` + RowsAffected | Session/API Token 撤销后拒绝；缺失 API Token 保持 stable not-found |
| API Token 列表 | 固定 projection/order、tuple cursor、UUID cast、limit+1 | 同时间 101 行无重复、无遗漏且 UUID DESC |
| 损坏/敏感数据 | 统一 record mapper 与 stable errors；GORM logger 由 Foundation 丢弃 | expired/revoked/corrupt scope fail closed；明文不落库 |

## 已执行验证

以下命令通过：

```text
go test -mod=vendor ./internal/auth/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/auth/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/auth/... ./internal/platform/postgres
go test -race -mod=vendor -tags=integration -run '^$' ./internal/auth/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api -count=1 -timeout 60s
go list -mod=vendor ./internal/auth/... ./cmd/api
go mod verify
python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-auth-migration
git diff --check
```

其中带 `integration` 的命令仅编译现有集成测试，输出明确为 `no tests to run`；它不算真实 PostgreSQL 通过。

静态检查确认：

- `internal/auth/application` 与 `internal/auth/domain` 不导入 GORM、database/sql 或 pgx。
- Auth production Composition 仍调用 `authpostgres.NewRepository(database.DB())`。
- Auth 范围没有 `AutoMigrate`、GORM Migrator、`gorm.DeletedAt` 或 Offset 分页。
- migration 与 `cmd/api` 均未由本 child 修改；集成测试文件仅改为复用共享 Testcontainers fixture 并成对运行 legacy/GORM 路径。

## Review 结果

- Go review：无 P0/P1。发现 `NewGORMRepository(&gorm.DB{})` 会在后续 `WithContext` panic；已校验 Config、Statement、两个 ConnPool 与既有 Error，并重新通过 race/vet/API 编译。
- SQL review：无 P0/P1。确认 GORM `?` 会渲染为 PostgreSQL `$n`、Raw 参数数量一致、CTE 原子结构/DB time/keyset/RowsAffected 保持；未发现 SQL 注入、动态排序、N+1、软删或 Credential 日志路径。
- 2026-08-27 Go/SQL/Trellis 复核未发现 P0/P1/P2：新增 fixture 只复用 `testdb.Require` 的生命周期，legacy/GORM 都来自同一个平台 Pool，所有新 SQL 断言保持参数化且由真实 PostgreSQL 执行。

实现中曾发现并修复一个 SQL 绑定问题：pgx `$n` SQL 不能直接传给 GORM Raw，否则 GORM 不会消费参数；现在 GORM 使用并列的 `?` 版本并将 JSON 参数先转为 string，避免 slice 展开。

## `go mod tidy -diff`

只读执行 `go mod tidy -diff` 返回非零：它建议清理工作区原有的大量 checksum-only `go.sum` 条目，并补充 GORM sqlite 测试 driver 的 checksum。该 diff 未应用，以免覆盖本任务开始前的用户 `go.sum` 改动。当前 vendor 模式 test/list、`go mod verify` 均通过；依赖收口留在 Foundation/Final 的独立所有权中处理。

## TODO 9 已关闭门禁

现有 Auth integration suite 已在 migrated disposable PostgreSQL 上分别实际构造 legacy 与 `GORMRepository`，并已通过：

1. Session/API Token 的数据库时间、TTL 微秒精度与明文不落库。
2. authenticate/revoke 竞态在撤销提交后严格拒绝。
3. concurrent Session rotate 仅一赢家；duplicate new hash 返回稳定 23505 映射且整个 CTE 回滚。
4. 101 个相同 `created_at` API Token 的 UUID keyset 无重复、无遗漏。
5. expired/revoked/corrupt scopes 均 fail closed，no-row 与 23505 保持稳定错误码。

取消传播、schema/权限错误和日志脱敏仍由既有 static/unit 验证覆盖；本次没有扩大到测试文件原本未包含的故障注入场景。`cmd/api` 接线和 legacy 删除仍归最终 Composition 收口任务，不属于本 child。

## 回滚边界

当前阶段回滚只涉及 `gorm_repository.go`、`model.go`、`queries.go` 及 `repository.go` 的共享 mapper/SQL 提取；不需要 Schema、数据或生产 Composition 回滚。
