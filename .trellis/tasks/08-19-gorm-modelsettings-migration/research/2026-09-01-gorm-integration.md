# Model Settings GORM 本轮实库证据

## 范围

本轮在现有 `repository_integration_test.go` 中原位增加
`TestGORMRepositorySaveDesiredRollbackAndScopedEnqueueFence`，使用 TODO 9
`internal/platform/testdb` 工厂创建独立迁移数据库，并由一个完整
`*platformpostgres.Pool` 同时提供 GORM root、UnitOfWork、Audit GORM Store
与 Model Settings GORM Repository。没有修改生产 Composition、Schema 或
新增测试文件。

## 已验证

- GORM `SaveDesired` 成功写入 revision/state，并由同池 Audit GORM Store
  追加一条脱敏事件。
- 注入 scoped Audit 错误后，revision insert、state desired 更新和 Audit
  均回滚；数据库中仍只有首个 revision，desired revision 未变化。
- 回滚不会回退 PostgreSQL sequence；后续成功保存使用更高 revision，且
  Audit resource ref 与实际 revision 一致。
- GORM activation 从 idle 进入 preparing 后执行 pre-commit failure，active
  revision 保持不变并进入 failed 状态。
- caller-owned UnitOfWork 内的 `CheckEnqueue` 成功；事务结束后的 stale
  opaque scope 被拒绝。
- 同一 `foundation.TransactionScope` 内通过 scoped River inserter 入队：回调
  返回错误时 `workflow.river_job` 不留记录；正常提交返回有效 JobID，且提交后
  由 root Pool 新查询可见对应唯一记录。
- 使用同一 Pool 的 managed Local Runtime：注入 preparation 失败时 operation/hold
  与 activation 状态整体回滚；正常 Start/Fail 提交后 operation 进入 failed 且 hold
  被释放。
- `CheckEnqueue` 持有 singleton `FOR SHARE` 时，竞争事务的 `FOR UPDATE NOWAIT`
  返回 PostgreSQL `55P03`；事务结束后锁释放。

## 命令与结果

```text
go test -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s      PASS
go test -race -mod=vendor ./internal/modelsettings/... -count=1 -timeout 60s PASS
go vet -mod=vendor ./internal/modelsettings/... ./internal/audit/... \
  ./internal/localmodelruntime/... ./internal/platform/postgres             PASS
go test -v -mod=vendor -tags=integration \
  -run '^TestGORMRepositorySaveDesiredRollbackAndScopedEnqueueFence$' \
  ./internal/modelsettings/adapter/postgres -count=1 -timeout 120s          PASS
python3 ./.trellis/scripts/task.py validate \
  .trellis/tasks/08-19-gorm-modelsettings-migration                       PASS
git diff --check                                                            PASS
```

## 未覆盖门禁

按 2026-09-01 精简政策，本轮直接改动触发的硬风险已覆盖：主路径、Revision/Audit、
managed Local Runtime、scoped fence + River 同事务以及 GORM 锁竞争/释放均通过。
该必选场景使用 `testdb.FailWhenUnavailable`，容器 provider 不可用时不会以 skip
伪装成功。
双进程完整矩阵、DB-time lease、Snapshot 并发一致性、cancel/commit ambiguity、
连接释放和 EXPLAIN 作为低频专项按风险触发；child 可完成验收但生产 Composition
切换与 legacy 删除继续由 Final child 负责。
