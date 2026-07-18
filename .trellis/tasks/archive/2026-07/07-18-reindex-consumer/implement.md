# M6-B Reindex Consumer 实施清单

1. [x] 建立 `internal/retrieval/contract` v1 DTO/strict codec，替换 Writeback 匿名编码与私有重复校验；补字段、版本、canonical 与跨绑定测试。
2. [x] 新增 `00015_reindex_consumer.sql`：Index source snapshot 字段、`index_manifest_source`、`reindex_delivery`、append-only `reindex_delivery_attempt`、复合 FK、状态/lease/checkpoint、deferred completion invariant 和 Down `55000`。
3. [x] 扩展 Retrieval Domain/PostgreSQL 基础：included/excluded Source Manifest canonical hash/count、legacy Index 兼容、BuildStatus/Ready 三层闭包与集合化 Chunk FK。
4. [x] 新增 Git Commit Blob Reader 与 committed bytes Content Artifact capture；实现 `CaptureCommittedSourceVersion` 及工作树漂移/路径/hash/大小/响应丢失测试。
5. [x] 实现 Snapshot Resolver 与 `BeginWorkspaceSnapshot`：按当前处理契约和稳定排序冻结 included/excluded Source/Attempt，增量复制 Active Source Manifest 并替换目标，分页计算 Hash/Count、分批 COPY、推导去重 Chunk Manifest、精确 replay；补容量分页/超限测试。
6. [x] 实现 Delivery 状态模型与 PostgreSQL 持久化：dispatch/status/checkpoint/manual、版本、终态和精确恢复查询。
7. [x] 实现 Delivery Attempt Runtime：claim/heartbeat/retry/fail、DB-time lease、Ingestion retry generation、旧 owner fence 和 append-only 历史。
8. [x] 扩展现有 Workflow River transport 的受限共享 Worker 注册/tx typed insertion seam；实现 Reindex Args、Inserter、Worker 与“业务已归约返回 nil/事务未知才 transport retry”语义，复用既有两个 Client和同一 queue。
9. [x] 实现 Outbox Dispatcher 两条 `SKIP LOCKED` 路径：首次 Delivery+InsertTx+published_at，以及 due retry generation；补 legacy Runtime tuple、并发与响应丢失重放。
10. [x] 实现 Dispatcher Runner 配置、readiness/fatal/backoff/start-stop 生命周期，并接入现有 Worker shutdown 控制器。
11. [x] 实现 Reindex Processor：Capture→Ingestion→Snapshot→Begin→Lexical→`SNAPSHOT_STRUCTURE_V1`→Ready，每个 checkpoint 支持重启恢复。
12. [x] 抽取 M6-A `activateTx`，实现 `CompleteReindexTx`：先 exact terminal replay、首次 lease/attempt fence、cleanup/Workflow gate、Activation、Delivery、Execution、Proposal 单事务、完整 deferred invariant 与历史 replay。
13. [x] 接入 `cmd/worker` Composition、配置、readiness/metrics/trace；Reindex 不新增第三 River Client或第二 queue。
14. [x] 增加真实 PostgreSQL/River 集成与 fault smoke：派发回滚、各 checkpoint kill/restart、双 Worker、完成故障注入、上游 response-loss 并发和完整 Safe Writeback→Completed 闭环。
15. [x] 同步父任务、产品 PRD、Workflow/Retrieval/Database/Testing/Deployment、backend Spec，统一“回归失败不自动反向 Commit”的本期语义。
16. [x] 执行全量门禁、go-review、sql-code-review、独立审查、提交、归档和 journal。

## Dependency Order

```text
Contract -> Migration -> Source Manifest -> Committed Capture -> Snapshot
         -> Delivery/Dispatcher/River -> Processor -> Complete UoW -> Composition/Smoke
```

Migration 和公共契约先完成；Processor 不得在 Source Manifest、Delivery lease 与 completion guard
未通过数据库测试前接入 Worker。`CompleteReindexTx` 未证明原子性前不得把任何状态返回 completed。

## Validation

```bash
go test -race ./internal/retrieval/contract ./internal/workspace/... ./internal/retrieval/...
go test -race -count=20 ./internal/retrieval/domain ./internal/retrieval/application
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration ./internal/platform/migration ./internal/retrieval/adapter/postgres
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration ./internal/retrieval/adapter/river ./cmd/worker
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 ./...
go vet ./...
make test
go mod tidy -diff
git diff --check
```

## Risk And Rollback Points

- Source Snapshot：单文件 Manifest 或 silent omission 会丢文档；任何闭包不成立立即失败并保留旧 Active。
- Committed Capture：只读 Commit Blob，禁止退化为工作树扫描；Git 结果未知进入 manual recovery。
- Delivery：Outbox published 与业务成功分离；InsertTx/状态/ack 任一步失败必须事务回滚。
- Completion：cleanup/Workflow/Regression 任一 gate 失败不得切 Active；故障注入不通过不得提交。
- Rollback 应保留 00015 数据和旧 Active；只有空表允许 Down，应用可停 Dispatcher 回退。
