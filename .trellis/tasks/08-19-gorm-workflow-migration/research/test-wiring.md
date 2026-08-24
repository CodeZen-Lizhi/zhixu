# Workflow 测试与接线盘点

## 可复用测试

- `repository_integration_test.go`、`list_integration_test.go`：基础 Repository 与分页；
- `runtime_start_integration_test.go`：Start replay/conflict/rollback/commit response-loss；
- `runtime_state_integration_test.go`：claim/heartbeat/lease/retry/join/control/human/Hook；
- `runtime_worker_integration_test.go`：Runtime + River Worker；
- `adapter/river/worker_real_integration_test.go`、`worker_kill_smoke_integration_test.go`：真实 pgx Worker/listener/rescue。

## TODO 9 fixture

现有 Runtime Start fixture 已能创建临时数据库并运行 migration。参数化时每个 legacy/GORM 子测试使用独立数据库；migration pool 完成后关闭，再构造唯一 platform Pool。legacy 用 `DB()`，GORM 用同一个 Pool 的 GORM/UoW/scoped River producer。

## 生产门禁

- API/Worker 继续 legacy；
- 无 Hook 的 GORM Runtime 测试不能替代带 Hook 生产路径；
- Artifact、Conversation、Change Control、Health、Graph、Organizing 未提供 scoped 端口前保持 legacy；
- Model Settings scoped enqueue fence 未交付前保持 legacy；
- `ZHIXU_TEST_DATABASE_URL` 缺失时 integration compile 不是真实验收。
