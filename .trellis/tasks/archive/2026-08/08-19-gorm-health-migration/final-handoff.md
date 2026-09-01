# Health GORM 迁移交接

状态：`complete`（待归档）

## 本 child 已完成

- Health GORM Scan、Schedule、Issue/Read 等 staged adapter 继续复用共享 Pool、GORM root 和 UnitOfWork。
- Smart Collection 的 GORM scoped verifier 通过 opaque `foundation.TransactionScope` 复核 durable binding；Issue missing-set 的最终回查也走同一 verifier。
- 旧 pgx durable binding bridge 从 staged `membership.go` 移到 `legacy_membership.go`，保留现有生产装配行为。
- Health Testcontainers 测试改用 `internal/platform/testdb`，并增加同一真实 Pool 的 GORM/Workflow/River/Events 集成覆盖。

## 验证

默认 Health `go test`、`go test -race`、`go vet`、integration compile、28 个真实 Testcontainers PostgreSQL 顶层用例、模块一致性检查、命令入口编译、`git diff --check` 与 Trellis context gate 均已执行并通过。具体命令、覆盖与耗时见 `research/testcontainers-evidence.md` 和 `implement.md`。

## Review 结论

Graph fixture 和固定语句数 tracer 阻断均已闭环。Go/SQL/Trellis review 额外发现并修复 membership cache hit 未先复核 durable revision 的一致性问题；未发现剩余 P0-P2 问题。生产切换前必须继续由 Final 同时注入 scoped Collection verifier，且不得建立第二连接池或第二 membership 事实源。

## 明确保留的 Final 范围

未修改生产 Composition、`cmd/**`、Schema/migration、共享平台或 `go.mod`；未删除 legacy pgx 路径。回滚本 child 仅需撤回 Health adapter/collection 文件和本 child 工件，不影响现有生产构造。
