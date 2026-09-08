# Retrieval / Ingestion Final 收口记录（2026-09-08）

## 授权与范围

Final owner 在各模块核心实库门禁完成后，授权本分支清理 `internal/retrieval/**` 与 `internal/ingestion/**` 的 legacy 业务 pgx。未修改 `cmd/**`、其他 owner 的实现、Atlas Schema、部署状态或 active-task pointer；未 commit/push。

## 最终接口与原生边界

- Ingestion 保留 `NewGORMRepository(*gorm.DB)`：调用者从共享 `platformpostgres.Pool.GORM()` 取得 root。删除旧 pgx Repository，仅保留 GORM 使用的纯映射/scanner。
- Retrieval 保留 `NewGORMRepository(*platformpostgres.Pool)`、`NewGORMSearchRepository`、`NewGORMDeliveryRepository`、独立 `NewGORMCompletionRepository` 与 `NewGORMDispatcher` 的已交接签名。
- Application/River 只保留 `TransactionScope` / scoped Dispatcher / scoped producer。Workflow 已删除的 `Options.EnqueueFence` 检查同步移除。
- 普通数据库错误分类通过 `platformpostgres.SQLState`；native no-row 通过 `sql.ErrNoRows` 错误链匹配。没有普通业务 pgconn import。

Retrieval 生产 pgx 精确 allowlist：

| 文件 | 能力与不变量 |
| --- | --- |
| `internal/retrieval/adapter/postgres/native_capabilities.go` | 唯一同 Pool capability 构造；普通 Repository 仅见 Manifest/Snapshot/Refresh 三个私有端口。 |
| `internal/retrieval/adapter/postgres/native_manifest.go` | Index metadata 与 Manifest COPY 同一原生事务；重复请求在相同事务中锁定和校验已持久化 Manifest。 |
| `internal/retrieval/adapter/postgres/native_snapshot.go` | 专用 session、Workspace advisory lock、Repeatable Read、临时表、分页与 COPY 共同物化 Snapshot；原 session 解锁，失败关闭连接。 |
| `internal/retrieval/adapter/postgres/native_source_refresh_lock.go` | Refresh lease 跨多个业务事务持有独立 session；重试释放未锁连接，Release 幂等，unlock 失败 hijack-close。 |

Ingestion 生产代码没有 pgx allowlist。上述边界不扩大到普通 CRUD、Activation、Dispatcher、Completion、Delivery、Vector Build、Regression 或 Evidence 查询。

## 既有测试迁移

- 保留所有既有场景与业务断言，将双实现变体收敛到最终 GORM；没有新增测试文件，没有通过 skip 或删断言隐藏回归。
- 对比 HEAD 与当前改动文件的 Test 函数集合：91 个既有 Test 函数全部保留。
- Retrieval 公共 fixture 从同一个真实 Pool 构造 GORM Store 与独立 Completion。Change Control / Workflow / Model Settings owner 通过已批准 scoped 协作者参与同一 UoW。
- Dispatcher 故障插入先真实写 River，再报错，验证 River / Delivery / Outbox 全回滚；duplicate 回执、并发、FIFO、retry 和提交响应丢失仍有原断言。
- Completion 原 SQL mutation 阶段故障/延迟迁至 fixture 独立 GORM root callback；每次检查确实命中目标阶段。owner 回滚在两次 Change Control CAS 后注错；response loss 在真实 UoW 提交后注错。
- Evidence unit mock 改用标准 `database/sql/driver`，仍断言完整 JOIN、实际绑定参数、非法输入不查询、not-found 和损坏元数据 fail closed。
- Trigram 测试使用单连接 Pool，在 GORM 查询前后核对相同 PostgreSQL PID 和 session 阈值，保留阈值不影响检索结果的断言，并验证 SET LOCAL 不泄漏。
- Capacity 方法池改为平台 Pool，从 PostgreSQL startup 参数设置固定 ANN 参数，真实 GORM 与 EXPLAIN 共用物理池；容量基准未运行。
- HTTP fixture 使用 Testcontainers 工厂、最终 Retrieval / Workspace GORM 构造；旧 `test` Workspace seed 修为合法 `inactive`。

## 已验证

| 命令 / 场景 | 结果 |
| --- | --- |
| `go test -mod=vendor ./internal/retrieval/... ./internal/ingestion/... -timeout=60s` | PASS，Retrieval / Ingestion 各现有 unit 包；Ingestion Adapter 无 unit 文件。 |
| `go vet -mod=vendor ./internal/retrieval/... ./internal/ingestion/...` | PASS。 |
| `go vet -mod=vendor -tags=integration ./internal/retrieval/... ./internal/ingestion/adapter/postgres` | PASS，包含全部现有 integration fixture 的编译。 |
| `go test -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres -run '^(TestRepositoryAttemptProjectionLifecycle\|TestRepositorySaveProjectionRollsBackFailedChunkBatch)$' -count=1 -timeout=60s` | PASS，17.340s。 |
| `go test -mod=vendor -tags=integration ./internal/retrieval/http -run '^TestPostgresRouterSearchEvidenceCursorAndExplain$' -count=1 -timeout=60s` | PASS，10.490s；检索、Evidence、cursor、隔离、EXPLAIN。 |
| `git diff --check -- internal/retrieval internal/ingestion` | PASS。 |
| `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-composition-pgx-convergence` | PASS；大规范文件超过自动注入大小的既有警告，不影响路径校验。 |

Ingestion 首次代表用例只在 cleanup 失败：`t.Context()` 在 `t.Cleanup` 前取消，外层 SQL 事务自动回滚与显式 rollback 竞态。改为 scenario 返回前 defer rollback 后，两条场景均通过；没有忽略 rollback 错误。

## Final PostgreSQL 验证

Change Control owner 完成 legacy 清理后，Retrieval PostgreSQL 集成编译恢复。以下均使用最终构造和真实 Testcontainers PostgreSQL，每条命令 `-count=1 -timeout=60s`：

| `go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run ...` | 结果 |
| --- | --- |
| `^(TestRepositoryFTSOnlyBuildReadyActivateAndReplay\|TestRepositorySourceRefreshLeaseBlocksSameWorkspaceUntilRelease)$` | PASS，14.994s。Manifest COPY、FTS、Ready、Activation、replay、跨 session lease 与增量 Snapshot。 |
| `^(TestDispatcherInsertFailureRollsBackDeliveryAndPublishedAt\|TestDispatcherFirstDispatchReplaysExactPendingAndResponseLoss\|TestDispatcherRejectsDuplicateJobReceiptAndRollsBack)$` | PASS，26.600s。实际 River insert 后的原子回滚、提交响应丢失与 duplicate receipt。 |
| `^(TestCompleteReindexTxAtomicallyActivatesAndReplaysAfterResponseLoss\|TestCompleteReindexTxRechecksLeaseAtFinalMutation\|TestCompleteReindexTxFaultInjectionRollsBackEveryMutationStage)$/(gorm\|proposal_completed\|gorm_owner_rollback)$` | PASS，31.065s。原子完成/历史重放、最后 mutation lease、真实 Proposal update callback 与 owner 双 CAS 后全回滚。 |
| `^(TestSearchRepositoryActiveFiltersAndBoundedProvenance\|TestRepositoryVectorBuildPageCacheCommitAndResponseLossReplay)$` | PASS，17.494s。同一物理 session 的阈值隔离、完整 filter/provenance、Vector 分页/cache/提交重放。 |

所有既有 mutation 阶段仍保留并编译；本轮按风险选择以上代表场景，没有运行整个故障矩阵或容量 benchmark。Final owner 继续负责跨模块入口和全仓门禁。

## 审查与回滚

使用 Go Review 与 SQL Review 检查直接调用方、错误链、取消、锁/session 生命周期、事务原子性、参数化、Workspace 隔离、幂等和批量访问。Final 改动不引入 Schema 变更或双写。回滚必须按本次 Adapter/Composition 的依赖闭包恢复，不能仅恢复 legacy 构造而遗漏 scoped owner；所有组件保持同一版本。

容量规模性能、生产发布和真实外部 Provider 不在本次已执行验证内。模块上一阶段的 PostgreSQL 证据保存在 Retrieval child `research/static-validation.md` 与 `final-handoff.md`，不把这些历史结果冒充 Final 重接线后的验证。
