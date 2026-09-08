# Workflow 测试与接线盘点

## 可复用测试

- `repository_integration_test.go`、`list_integration_test.go`：基础 Repository 与分页；
- `runtime_start_integration_test.go`：Start replay/conflict/rollback/commit response-loss；
- `runtime_state_integration_test.go`：claim/heartbeat/lease/retry/join/control/human/Hook；
- `runtime_worker_integration_test.go`：Runtime + River Worker；
- `adapter/river/worker_real_integration_test.go`、`worker_kill_smoke_integration_test.go`：真实 pgx Worker/listener/rescue。

## TODO 9 fixture

`newRuntimeTestPlatformDatabase` 已统一使用 `testdb.Require`，固定 `FailWhenUnavailable`、`MaxConns: 8`。
每个测试独立容器/数据库，工厂执行 Atlas/River migration 并负责唯一 platform Pool 的清理；不要求
`ZHIXU_TEST_DATABASE_URL`。legacy seed 使用 `DB()`，被测 GORM 使用同 Pool 的 GORM/UoW/scoped River。

2026-09-08 在现有文件原位切换的实际 GORM 场景：

- `runtime_start_integration_test.go`：并发唯一 Start、caller-owned StartScoped replay/rollback、Settings 锁。
- `runtime_state_integration_test.go`：Claim/Heartbeat/Delivery/terminal replay、Hook rollback、并发 lease reclaim。
- `runtime_worker_integration_test.go`：GORM Runtime + database/sql insert + pgx Runtime Worker。

`newGORMRuntimeTestRepository` 只组合真实 Settings/Audit/Sealer/Runtime，不提供 no-op fence、fake River
或额外数据库生命周期。Repository 与两个 scoped fence 文件继续使用既有 fixture；定向命令已通过，
见 `../final-handoff.md`。

## 生产门禁

- API/Worker 继续 legacy；
- 无 Hook 的 GORM Runtime 测试不能替代带 Hook 生产路径；
- Final 汇总 Artifact、Conversation、Change Control、Health、Graph、Organizing 各自的 scoped 组合证据；
- Model Settings scoped enqueue fence 已交付并验证，Final 必须注入该真实实现；
- Docker 不可用时测试明确失败，compile-only 仍不能作为实库证据。
