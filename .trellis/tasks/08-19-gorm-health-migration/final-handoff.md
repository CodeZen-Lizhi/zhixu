# Health GORM 迁移交接

状态：`in_progress`

## 本 child 已完成

- Health GORM Scan、Schedule、Issue/Read 等 staged adapter 继续复用共享 Pool、GORM root 和 UnitOfWork。
- Smart Collection 的 GORM scoped verifier 通过 opaque `foundation.TransactionScope` 复核 durable binding；Issue missing-set 的最终回查也走同一 verifier。
- 旧 pgx durable binding bridge 从 staged `membership.go` 移到 `legacy_membership.go`，保留现有生产装配行为。
- Health Testcontainers 测试改用 `internal/platform/testdb`，并增加同一真实 Pool 的 GORM/Workflow/River/Events 集成覆盖。

## 验证

默认 Health `go test`、`go test -race`、`go vet`、integration compile、真实 Testcontainers Scan/Schedule/Issue/River/Events/Affected-change 以及 `git diff --check` 均已执行并通过。具体命令、用例与耗时见 `research/testcontainers-evidence.md` 和 `implement.md`。

## 保留阻断

Collection 相关的两个真实测试受 Graph fixture 写入废弃 workspace 状态阻断（SQLSTATE `23514`），失败发生在 Health 断言前。另有 `issue_repository_batch_integration_test.go` 和 `read_repository_integration_test.go` 的连接级 pgx tracer 断言尚未接入共享 factory。前者必须由 `internal/graph/**` owner 修复，后者需要共享 testdb 设计变更；在此之前不得完成或归档本 child。

## 明确保留的 Final 范围

未修改生产 Composition、`cmd/**`、Schema/migration、共享平台或 `go.mod`；未删除 legacy pgx 路径。回滚本 child 仅需撤回 Health adapter/collection 文件和本 child 工件，不影响现有生产构造。
