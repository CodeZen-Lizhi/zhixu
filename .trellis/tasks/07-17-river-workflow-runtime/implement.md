# River Workflow Runtime 实施清单

1. [x] 完成 River 隔离 PoC：锁定 `v0.40.0`，真实验证 Go 1.25.4/pgx 5.10.0、7 个 migration、自定义 `workflow` Schema、typed Workers、`InsertTx` rollback、ByArgs unique、ScheduledAt、Start/Stop/StopAndCancel；主仓库尚未引入依赖。
2. [x] M4-A 只收敛 RegisteredDefinition、Executor、稳定 Job identity 和 tx-scoped UoW 基础；Run/Node/Attempt 状态、Failure/Retry 与控制面由 M4-B 独占，Proposal binding 由 M4-C 独占。
3. [x] 按字段 owner 拆分前向迁移：M4-A `00011_river_runtime_foundation.sql` 负责 Run/Node identity/schema/dispatch 与 Outbox identity；M4-B `00012_workflow_runtime_state_machine.sql` 负责状态、Attempt/retry/failure/control；M4-C `00013_approval_writeback_dispatch.sql` 负责 `proposal.workflow_run_id`。每个子任务分别验证 legacy/Down 安全门，禁止重复定义列。
4. [x] 集成 Goose + River 官方迁移：新增唯一 `cmd/migrate`/`zhixu-migrate`，按项目 Goose Up → `rivermigrate` Up → Validate 执行，显式使用 `workflow` Schema；在本阶段验证二进制顺序、重复执行、升级、单实例门禁和版本检查，不复制 River 内部 SQL。Dockerfile/Compose 接线归 M4-D，不在 M4-A 建第二套入口。
5. [x] 实现 Definition Registry：注册 canonical DAG、Schema、Retry/权限和后继；覆盖重复、未知、缺失 Executor/权限的启动失败。
6. [x] 实现 Executor Registry 与 Deterministic Test Node：提供项目自有接口、稳定输入输出校验和未知 Node 非重试失败；不得 import River 到 Domain/Executor。
7. [x] 重构 Workflow Start：只接受注册 Definition；保留旧 Graph 字段时要求 canonical hash/size 一致并标记废弃。实现 Run `idempotency_key/request_hash` 查询，相同请求返回同一 Run，不同绑定冲突；定义历史未知 Definition 的只读错误。
8. [x] 正式精确引入 River/riverpgxv5 `v0.40.0` 并同步 `go.mod/go.sum/vendor`；实现 UoW 与 River Adapter，提供稳定 `NodeJobArgs(schema_version,node_run_id,dispatch_no)`、typed Worker、ByArgs unique、`InsertTx`、duplicate result和 transport error 映射。M4-A 只做 Client transport smoke，完整 Start/Stop/SoftStopTimeout lifecycle 归 M4-D。
9. [x] 实现 DB-time Claim/Heartbeat/Attempt：区分 business retry 与 River delivery/lease reclaim；活动 lease 不可抢占、过期可回收、相同 delivery 不重复 Attempt、旧 owner/version/attempt CAS 失败；长任务 heartbeat 失败取消 Context。
10. [x] 实现 Retry/Fail/Manual Recovery Repository 与 Runtime：原子结束当前 Attempt，持久化错误分类/next_attempt_at，并为下一 execution `InsertTx` 定时 Job；实现退避/jitter/Retry-After/`max_retries`，区分 River 传输重试，覆盖事务回滚和响应丢失重放。
11. [x] 实现 Complete/后继/Run 归约事务：Output、Node、Attempt、唯一后继、River `InsertTx`、Outbox 和 Run 状态原子提交；覆盖多前驱和重复 delivery。
12. [x] 实现 Human Resume 与控制面：持久 `pause_requested_at/cancel_requested_at`，版本化 Pause/Resume/Cancel Repository/Application/HTTP/OpenAPI；新建后继 Node 的首个 Job 固定 `dispatch_no=1`，Human/Pause Resume 对既有 NodeRun 创建新 generation 时才 `dispatch_no+1`，均不递增 retry_no。旧 Job delivery 必须被 Claim 识别为 stale 并返回 nil 结束，不 snooze/transport retry；阻止非法领取/副作用，已提交 Git 保持诚实恢复语义。
13. [x] 实现 Approval Durable Dispatch：保留 Target Hash 与 strict-clean Git snapshot 前置安全门，通过 Cross-Schema UoW 原子创建/replay Approval、Proposal→Run binding、固定 Safe Writeback Run/Node、冻结 Outbox 事件和 River Job；已有相同 Approval 也必须走 replay。Rejected 无 Job。按明确 OpenAPI schema 新建/重放返回 201/200，同一 workflow 状态 URL，覆盖锁顺序、竞态和响应丢失。
14. [x] 实现 Safe Writeback Bootstrap Executor：新增按 workspace+stable key exact lookup 和完整 binding 校验；Claim 后不存在时瞬时签发双 Credential并 Atomic Begin，存在时直接构造 Node Input Resume；覆盖两次授权、Begin、binding conflict 和所有崩溃窗口。
15. [x] 注册现有 Safe Writeback Node：真实 River Job 执行 Node `Execute`，Complete 后 Workflow succeeded、Proposal/Execution 保持 `verifying/index_pending`；重复/并发/kill -9 只产生一个 Commit、Mapping 和 Reindex Outbox。
16. [x] 改造 `cmd/worker` Composition：构造 River Client、Workflow Runtime、Registry、Heartbeat、graceful shutdown 和独立 `:8081/livez|readyz` health server；Compose healthcheck 实际探测 readiness，删除 `workflow_dispatcher=not_configured` 假运行路径。
17. [x] 增加可观测性：关联 Run/Node/Attempt/River Job/Workspace/Proposal 的脱敏日志、Trace seam 和队列/重试/lease/manual recovery 指标；覆盖重复 delivery 不重复领域成功审计。
18. [x] 完成真实 PostgreSQL/River 集成测试：事务型 enqueue、两 Worker、duplicate、DB 短断、lease expiry、retry/nonretry/manual、后继、graceful stop 和 restart recovery。
19. [x] 完成 Docker Compose 业务烟测：批准 Proposal 后 Worker 自动执行 Safe Writeback，HTTP 可查询 Workflow，最终文件/Git/Mapping/Reindex Outbox 一致且 Worker readiness 通过。
20. [x] 同步 `docs/product/PRD.md`、Workflow/模块/数据库/安全/可观测性/部署/恢复文档、ADR、OpenAPI、`.env.example` 和 backend spec；明确 M6 Retrieval 与前端 Workflow Center 仍未完成。
21. [x] 执行全量门禁：`go test -race ./...`、关键并发包 `-count=20`、`go vet ./...`、`make test`、迁移/OpenAPI/Compose/Docker smoke、Secret 扫描、go-review、sql-code-review 和 Trellis full-scope check；修复当前范围内问题后再申请提交。

## Milestone Stop Gates

- M4-A（任务 1-8）：River/迁移/Registry/UoW/幂等基础。必须通过事务入队与 Deterministic Node 真实 River smoke。
- M4-B（任务 9-12）：lease/Attempt/retry/fail/complete/后继/Human/控制面。必须通过两 Worker、duplicate、retry/manual 和 Pause/Cancel 测试。
- M4-C（任务 13-15）：Approval + Safe Writeback。必须通过 pre-Begin 全崩溃矩阵和唯一 Execution/Commit/Mapping/Outbox 烟测。
- M4-D（任务 16-21）：可观测性、readiness、Compose、文档和全量门禁。任一前置 gate 失败不得进入下一里程碑或开放自动写回。

实际 Trellis 子任务按以下顺序执行和归档：

1. `.trellis/tasks/07-17-river-runtime-foundation`
2. `.trellis/tasks/07-17-workflow-runtime-state-machine`
3. `.trellis/tasks/07-17-approval-safe-writeback-dispatch`
4. `.trellis/tasks/07-17-river-operability-delivery`

父任务只负责跨子任务契约、最终集成审查和状态汇总，不直接混合实现四个里程碑。

## Validation Commands

```bash
go test -race ./internal/workflow/... ./internal/changecontrol/... ./cmd/worker
go test -race -count=20 ./internal/workflow/runtime ./internal/workflow/adapter/river ./internal/changecontrol/workflow
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -count=1 ./internal/workflow/... ./internal/changecontrol/application
go vet ./...
make test
node api/openapi/check.mjs
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait
curl -fsS http://127.0.0.1:8080/readyz
docker compose -f deploy/compose.yml --env-file .env.example exec -T worker wget -q -O - http://127.0.0.1:8081/readyz
docker compose -f deploy/compose.yml --env-file .env.example down -v
git diff --check
```

## Risk And Rollback Points

- 正式 Goose/River 迁移与现有数据库升级不兼容时，停止在任务 4/8，不提前写自制队列表；以已通过的 v0.40.0 PoC 为基线修正集成，不更换到 master 伪版本。
- Approval 事务与 River `InsertTx` 接线是跨模块高风险点；必须先通过故障注入证明全有或全无，再开放 HTTP 自动 dispatch。
- 状态拆分和 Attempt 迁移必须保持旧 Run/Node 可读；若兼容测试失败，回退应用代码但保留前向 schema，不删除活动 Job/lease/checkpoint。
- Safe Writeback 已出现 Git Commit 时任何回滚均不得 Restore 文件；停止 Worker并按 Execution/Trailer/Mapping 恢复。
- M6 不在本任务范围；不得为了演示把 Reindex Outbox 标 published 或把 Proposal 标 completed。
