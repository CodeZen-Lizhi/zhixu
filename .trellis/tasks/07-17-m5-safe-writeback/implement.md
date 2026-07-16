# M5-04 实施清单

## Child Task Map

| 子任务 | 内容 | 前置 | 验收 |
|---|---|---|---|
| M5-04A | Approval Git HEAD、Proposal/Execution/Mapping/Outbox 数据模型与状态机 | M5-03 | 领域 + migration + PostgreSQL race integration |
| M5-04B | Workspace target lock、temp validation、最终 CAS、atomic replace、restore | M5-04A 契约 | Filesystem contract/fault/concurrency tests |
| M5-04C | Git inspect/diff/commit/trailer/recovery/reverse adapter | M5-04A 契约 | 临时 Git repo contract/security tests |
| M5-04D | 双授权 Safe Writeback Saga、checkpoint、补偿、publish 与 smoke | A/B/C | fault injection + duplicate delivery + Docker smoke |

## Ordered Checklist

1. [x] M5-04A：扩展 Approval Git HEAD、Proposal version/status state machine；新增 Writeback Execution、Proposal Commit Mapping 和 Reindex Outbox 迁移。
2. [x] M5-04A：实现领域模型、Repository 契约、PostgreSQL 幂等/乐观锁/交叉绑定/发布事务与真实数据库测试。
3. [x] M5-04B：实现 WorkspaceStore/TargetLock 领域端口与 localfs Adapter；覆盖 unsafe path/symlink/special file/temp/fsync/CAS/restore。
4. [x] M5-04C：扩展 Git CLI Adapter 的 clean snapshot、HEAD verify、path diff、fixed commit/trailer、unknown-result lookup 和 reverse commit。
5. [ ] M5-04D：实现 Application Saga：预签双授权、先建 Execution、分步消费授权、checkpoint、文件补偿、Commit 恢复、DB publish。
6. [ ] M5-04D：实现 Workflow Node/Composition Root 最小接线；不得增加直接文件/Git HTTP 写接口。
7. [ ] 同步 OpenAPI 状态查询（若暴露）、架构/数据库/工具安全/恢复/产品文档和父任务状态。
8. [ ] 执行 domain/application/FS/Git/PostgreSQL unit+race、故障注入、全仓 `go test -race ./...`、`go vet ./...`、`make test`、重复迁移和 Compose readiness smoke。
9. [ ] 使用 `go-review` + `sql-code-review` + Trellis full-scope check，修复 P0/P1 后提交、归档与 journal。

## Validation Commands

```bash
go test -race ./internal/changecontrol/... ./internal/platform/filesystem ./internal/platform/gitcli
go vet ./...
make test
node api/openapi/check.mjs
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
git diff --check -- . ':!vendor/github.com/yuin/goldmark/README.md'
```

真实数据库测试使用 disposable PostgreSQL，空库执行全部 Up 两次；数据库包按顺序执行，避免共享测试 Workspace 的锁竞争。

## Rollback Points

- A：迁移只向前；停用 Safe Writeback 入口，历史审批可读但不 Apply。
- B：CAS 前失败删除 temp；CAS 后 Git 前失败必须 RestoreCAS 或进入 manual recovery。
- C：未知 Commit 结果先 Trailer/HEAD 查证；禁止盲目重试和破坏性 Git 命令。
- D：Commit 前失败可恢复文件；Commit 后 DB 失败保留 Commit并走 reconcile，不能恢复文件制造 Git/FS 分叉。
