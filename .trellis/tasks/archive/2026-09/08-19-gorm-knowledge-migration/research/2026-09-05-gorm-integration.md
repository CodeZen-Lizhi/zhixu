# Knowledge GORM 精简实库证据

## 范围

本轮只在现有 `timeline_integration_test.go` 中新增
`TestGORMKnowledgeLeanMainPathAndScopedAuditRollback`。测试通过
`testdb.Require` 和 `FailWhenUnavailable` 建立独立 migrated PostgreSQL，并由
同一个 `platformpostgres.Pool` 提供 pgx seed/assertion、Knowledge GORM
Repository、UnitOfWork 与 Audit GORM Store。没有新增测试文件、修改 migration、
接入生产 Composition 或删除 legacy。

## 已验证

- Claim Suggest 首次写入与相同 command 精确重放，Confirm 在一次提交中写入
  SUPPORTS provenance 并通过 deferred constraint。
- `BatchGetClaims` 返回完整 confirmed Claim 和单条 Source；使用另一 Workspace
  查询同一 Claim ID 返回空集合。
- Impact 保存先插入 Report 并触发 Timeline outbox，再由同 scope 的真实 GORM
  Audit Recorder 成功 append；collaborator 随后返回注入错误。
- 事务失败后 `ops.impact_report`、`ops.timeline_projection_outbox` 和
  `ops.audit_event` 均无对应行，证明跨 owner 原子回滚。
- 首轮运行捕获 `gormSaveImpactReport` 的占位错位：多出的普通占位使
  `generated_at` 被绑定为 JSONB，PostgreSQL 返回 `42804`。修正 SELECT 的
  JSONB/时间占位顺序后，同一 Testcontainers 场景通过。

## 命令与结果

```text
go test -mod=vendor ./internal/knowledge/... -count=1 -timeout 60s          PASS
go vet -mod=vendor ./internal/knowledge/... ./internal/platform/postgres \
  ./internal/events/... ./internal/audit/...                                PASS
go test -v -mod=vendor -tags=integration \
  -run '^TestGORMKnowledgeLeanMainPathAndScopedAuditRollback$' \
  ./internal/knowledge/adapter/postgres -count=1 -timeout 120s              PASS
python3 .trellis/scripts/task.py validate \
  .trellis/tasks/08-19-gorm-knowledge-migration                             PASS
gofmt -d internal/knowledge/adapter/postgres/gorm_timeline.go \
  internal/knowledge/adapter/postgres/timeline_integration_test.go          PASS
git diff --check                                                             PASS
```

## Review 与规范同步

- Trellis/Go/SQL 轻量复核覆盖当前完整 diff、legacy Impact 列映射、参数化、
  Workspace 隔离、同池 scoped UoW、回滚断言和 production wiring，未发现剩余
  P0-P2。
- 提交前复核发现 `design.md` 仍把旧完整 PostgreSQL 矩阵表述为当前强制门禁；
  已补充 2026-09-01 父任务精简政策的覆盖说明，完整矩阵仅保留为 Final 与直接
  风险触发清单。
- `gormSaveImpactReport` 修正后的 16 个 `SELECT` 表达式与目标列、参数列表逐项
  对齐；真实 PostgreSQL 已验证 JSONB 与时间绑定。
- backend database spec 新增复杂 `INSERT ... SELECT` 类型占位错位规则，要求
  修改后执行至少一条真正触发 SQL 的精简 PostgreSQL 测试。

## 未覆盖门禁

按 2026-09-01 精简政策，本轮未运行完整 legacy/GORM command/read、Relation
Apply、Timeline Projection、response-loss、SQLSTATE/cancel/连接、integration
race 或 EXPLAIN 矩阵。当前直接风险已由主读写/查询与真实 Audit 写入后回滚覆盖；
生产 Composition 切换、Organizing caller-owned transaction 迁移与 legacy 删除
继续由后续 child/Final 负责。
