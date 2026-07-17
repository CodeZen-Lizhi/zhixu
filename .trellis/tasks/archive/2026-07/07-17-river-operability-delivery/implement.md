# M4-D River Operability And Delivery 实施清单

1. [x] 扩展 Worker/Workflow 配置模型和交叉字段校验，补默认、边界、非法组合和 Secret 输出测试。
2. [x] 实现 Worker readiness snapshot 与独立 `/livez|readyz` health Adapter，覆盖 DB/River/Registry/Dependency/shutdown 状态。
3. [x] 重构 `cmd/worker` lifecycle：按顺序启动 Registry/Runtime/Health/River，直接调用 v0.40.0 `Start`；正常 Stop+SoftStopTimeout 与紧急 StopAndCancel 两条路径互斥，增加 hard shutdown deadline、非零退出和资源关闭测试；写权限 Node 只有在领域恢复/补偿/Manual 事实持久化后才允许 terminal cancel。
4. [x] 扩展项目 observability 接口与 OpenTelemetry Provider seam，加入脱敏 correlation、bounded metrics 和 trace propagation；覆盖 disabled/optional/required，且不把 noop/in-memory 冒充真实 exporter。
5. [x] 将 M4-A 已实现的 `zhixu-migrate` 打包进 Dockerfile并改造 Compose migrate service，保留非 root 运行和单实例迁移门禁；未重建第二套迁移入口。
6. [x] 为 Compose Worker 增加容器内 `/readyz` healthcheck，验证 migrate/API/Worker 的 depends_on 和 `up --wait` 行为。
7. [x] 增加 graceful stop、用户 Cancel、forced cancel、真实子进程 SIGKILL/River rescue、DB 短断、双 Worker恢复 smoke，断言不重复领域副作用，且不存在 terminal Workflow + 非终态 Writeback Execution 孤儿组合。
8. [x] 增加日志/metrics/trace/River metadata/health Secret 扫描和高基数 label 静态测试。
9. [x] 同步 README、`.env.example`、Workflow/Observability/Deployment/Testing/Recovery/Database 文档和相关 ADR/Spec。
10. [x] 执行全量门禁与 go-review/sql-code-review/Trellis check，修复当前范围内明确问题。

实施补充：fault/normal/Approval River smoke 已纳入；SIGKILL smoke 改为独立临时数据库，
避免常驻 Compose Worker 的 River maintenance leader 覆盖测试 rescue 周期。生产
Runtime Worker 已接通 trace/correlation、业务 metrics 与严格 allowlist fatal channel，
transition replay 不重复发射结果指标。

## Validation

```bash
go test -race ./cmd/worker ./internal/workflow/... ./internal/platform/config ./internal/platform/observability
go test -race -count=20 ./internal/workflow/runtime ./cmd/worker
go vet ./...
make test
docker build -f deploy/Dockerfile -t zhixu:local .
docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait
curl -fsS http://127.0.0.1:8080/readyz
docker compose -f deploy/compose.yml --env-file .env.example exec -T worker wget -q -O - http://127.0.0.1:8081/readyz
docker compose -f deploy/compose.yml --env-file .env.example down -v
git diff --check
```

## Stop Gate

只有 API、Worker、Migrate、PostgreSQL 全部通过 Compose readiness，真实 Job 与 Safe Writeback crash/restart smoke 通过，Secret 扫描和全量门禁为绿，才能归档 River Runtime 父任务并进入 Retrieval。
