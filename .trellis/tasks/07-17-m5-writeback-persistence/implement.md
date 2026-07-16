# M5-04A 实施清单

1. [x] 定义 `WritebackStatus`、`WritebackExecution`、`ProposalCommit`、Create/Checkpoint/Publish 命令与状态机测试。
2. [x] 扩展 Approval Git HEAD 与 Proposal Version 的领域模型/Repository 查询映射。
3. [x] 新增 `00009_safe_writeback.sql`：列、表、索引、状态/不可变/交叉绑定 Trigger。
4. [x] 实现 PostgreSQL create/replay、checkpoint/failure/manual、publish/replay 与查询。
5. [x] 补真实 PostgreSQL migration/race/constraint/transaction tests。
6. [x] 同步父任务 checklist、数据库/错误 code-spec，运行全范围门禁与 review。

## Validation

```bash
go test -race ./internal/changecontrol/...
go vet ./...
make test
git diff --check -- . ':!vendor/github.com/yuin/goldmark/README.md'
```

## Rollback

停用 Safe Writeback 入口；保留新增表和 nullable 字段。不得删除执行历史、Mapping 或 Outbox。
