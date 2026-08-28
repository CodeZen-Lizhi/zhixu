# Learning Path GORM staged handoff

状态：`in_progress`

## 本 child 已完成

- integration fixture 已切换为 `internal/platform/testdb` 的共享工厂；legacy pgx 与 staged GORM 变体都使用其唯一的 `platformpostgres.Pool`。GORM 变体显式验证同一 Pool 提供 `DB()`、`GORM()` 和 `UnitOfWork()`。
- 真实 PostgreSQL/Testcontainers 已覆盖 legacy/GORM 的 create/read/status/step parity、ABANDONED reopen 与 old-attempt fence，以及 maintenance 先于 late Artifact hold 或 Complete 的两个确定性 winner 场景。
- GORM 专项实测覆盖同/异 key 并发结果、history trigger `23514` rollback、context cancel/deadline、receipt `23505` rollback、共享 UoW 的真实 Workspace lock wait/cleanup，以及 Complete 后 response-loss 的 replay。
- 未修改生产 Composition、`cmd/**`、迁移、共享平台、依赖或 legacy pgx 路径。

## 验证

下列门禁已通过：

```text
go test -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/review/learningpath/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/review/learningpath/...
go test -mod=vendor -tags=integration -run '^$' -count=1 -p 1 -timeout 60s ./internal/review/learningpath/adapter/postgres
go test -mod=vendor -tags=integration -count=1 -p 1 -timeout 180s ./internal/review/learningpath/adapter/postgres
go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 180s ./internal/review/learningpath/adapter/postgres
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-review-learningpath-migration
gofmt -d <Learning Path integration files>
git diff --check -- <child files>
```

最终完整 Testcontainers 普通套件通过（108.194s），`-race` 套件通过（106.384s）。Go、SQL 和独立 Trellis review 均未发现 P0/P1 缺陷；`task.py validate` 仅保留既有 32 KiB context-injection 警告。详见 `research/static-validation.md`。

## TODO 9 阻断

- `Complete wins before maintenance` 的交易可见 `SKIP LOCKED` CTE barrier 仍只覆盖 legacy；尚未建立 legacy/GORM 成对 barrier。
- 尚缺 FK SQLSTATE、Rows 生命周期与额外 commit-failure 的 GORM 故障注入。
- 尚缺 evidence/maintenance 的 statement-count、索引使用和 `EXPLAIN` 证据。

因此全部 PRD AC 继续未勾选，任务不得完成或归档；生产仍使用 legacy。后续只可在 `internal/review/learningpath/**` 与本 child 工件中继续，不能切换生产 Composition 或删除 legacy。
