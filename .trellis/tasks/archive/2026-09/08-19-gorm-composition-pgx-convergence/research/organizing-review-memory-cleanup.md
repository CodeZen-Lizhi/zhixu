# Organizing / Review / Memory Final cleanup

日期：2026-09-08。Owner：`/root/gorm_organizing`。本记录对应 Final 已放行的 legacy 删除，不改写历史 child 的 staged 阶段证据。

## 范围与结果

独占修改 `internal/organizing/**`、`internal/review/**`（Core / Interview / LearningPath）和 `internal/memory/**`。未修改 cmd、平台事务实现、Atlas/Schema、依赖文件或 active-task pointer；未提交或部署。

| Owner | Final 变更 | 当前验证 |
| --- | --- | --- |
| Organizing | 删除旧 Repository / Template / Runtime / Generation、旧 owner transaction fence 与 Workflow dispatcher / terminal 实现；迁出纯 codec、校验及终态 receipt；`ConfirmRecord.Fence` 和 material resolver 统一 scoped 契约 | unit / vet 通过；Final 删除后 Draft / Template / Runtime 生命周期实库复验通过 |
| Review Core | 删除旧 repository / session / invalidation / deck_schedule；保留 scanner、receipt 与领域校验；错误分类通过平台 SQLState / ConstraintName；测试固定 GORM，EXPLAIN 直接使用最终 GORM SQL | unit / vet / TODO9 实库 gate 通过 |
| Review Interview | 删除旧 repository / commands 和 DB codec，纯命令校验与 receipt codec 迁至 `command_helpers.go`；reservation 常量/校验继续共享；Session 错误用例原位对接最终 GORM 读取分类 | unit / vet / Report + Path 完成主流程及提交响应丢失、取消、TxDone 实库通过 |
| Review LearningPath | 删除旧 Repository 和 DB codec；保留 snapshot/digest/aggregate 校验；所有 fixture 与 Artifact bridge 使用同一平台 Pool 的 GORM 构造；原 pgx 并发 barrier 与响应丢失包装迁到 GORM callback / UoW | unit / vet 通过；Final create/read、并发幂等、提交响应丢失、Complete/维护竞争实库复验通过 |
| Memory | 删除旧 Repository 和 DB command loader，迁出纯 audit/command/lifecycle 校验；固定 GORM fixture；保留 owner/workspace/task/expiry 过滤、双幂等和原始 Candidate receipt | unit / vet / 三组实库通过 |

非测试生产文件搜索未发现 pgx import、旧 `NewRepository`、旧 `Repository` 类型或双实现 selector。测试中的 pgx 仅用于 seed、锁观测、Schema 断言和数据库状态检查。

## 保持的构造与事务契约

- Review Core、Interview、LearningPath：`NewGORMRepository(*platformpostgres.Pool)`。
- Memory：从同一 `Pool` 取得 `GORM()` 与 `UnitOfWork()`，调用 `NewGORMRepository(root, unitOfWork)`；签名不变。
- Organizing：`NewGORMRepository(pool)`、`NewGORMFrozenMaterialFence(sourceReferences, claims)`、`NewGORMGenerationRepository(pool, agentFinalizer)`、`NewGORMStartRepository(pool, workflowStarter, workflowBindings)`、`NewScopedTerminalHook(repository)`、`NewScopedDispatcher(dependencies)`。
- 没有第二连接池、运行时 SQL 翻译、双写或 pgx 兼容 Repository。Application/Domain 只使用 opaque `foundation.TransactionScope`。
- Review invalidation 保留 200 上限、batch + 1、稳定 card 锁序和 selector AND 语义。Interview 保留 Begin / Prepare / Complete 的 reservation、digest、Report / Path 双 hold 原子提交。LearningPath 保留 Answer 锁、不可变来源快照与 attempt fence。

## Go / SQL 审查修复

按已加载 `go-review`、`sql-code-review` 进行人工差异审查：检查同一 Pool/UoW、Rows 关闭、错误链、锁序、CAS、受限分页、批量和 Workspace / owner 条件。

- Interview、LearningPath 的唯一约束分类原先创建新的文字原因而丢失 PostgreSQL cause；改为保留原始 cause，Kind/Code/Retryable 不变。
- Memory 取消分类原先只保留 `ctx.Err()`；现使用 `context.Cause` 并在必要时与 sentinel join。已有取消断言原位验证两者。
- LearningPath 构造失败和包装过的 context sentinel 保留原始错误链。
- LearningPath 原并发 barrier 的单行 SQL 匹配已改为最终多行 GORM SQL 中的稳定表片段；两个并发请求通过各自 context 选择已有 barrier，仍先进入独立 UoW 再观察 Workspace 锁。
- 未新增测试文件、skip 或临时测试程序。旧 fixture 独有的并发、commit-response-loss、维护和历史保护断言继续保留。

## 已执行验证

每条 Go test 均显式 `-timeout=60s`，使用项目 Testcontainers 工厂。

| 命令 | 结果 |
| --- | --- |
| `go test -timeout=60s ./internal/organizing/...` | PASS（Final legacy 删除后） |
| `go vet ./internal/organizing/...` | PASS |
| `go test -tags=integration -timeout=60s ./internal/organizing/adapter/postgres -run '^TestRepositoryPostgreSQLDraftTemplateSnapshotAndRuntimeLifecycle$' -count=1` | PASS，8.887s；Final legacy 删除及跨 owner 清理后复验 |
| `go test -timeout=60s ./internal/review/adapter/postgres` | PASS，0.445s |
| `go test -timeout=60s ./internal/review/application ./internal/review/domain ./internal/review/http` | PASS |
| `go vet ./internal/review/adapter/postgres` | PASS |
| `go test -tags=integration -timeout=60s ./internal/review/adapter/postgres -run '^TestReviewRepositoryPostgreSQLTODO9Gate$' -count=1` | PASS，12.588s；due/invalidation 批量与索引、Rows 取消/连接归还、SQL TxDone |
| `go test -timeout=60s ./internal/review/interview/...` | PASS |
| `go vet ./internal/review/interview/...` | PASS |
| `go test -tags=integration -timeout=60s ./internal/review/interview/adapter/postgres -run '^TestInterviewRepositoryPersistsTurnsReportsPathsAndNeverWritesFSRS$' -count=1` | PASS，6.899s |
| `go test -tags=integration -timeout=60s ./internal/review/interview/adapter/postgres -run '^TestInterviewRepositoryCommitResponseLossCancellationAndTxDone$' -count=1` | PASS，11.999s |
| `go test -timeout=60s ./internal/review/learningpath/...` | PASS |
| `go vet ./internal/review/learningpath/...` | PASS（最终错误链修复后复验） |
| `go test -tags=integration -timeout=60s ./internal/review/learningpath/adapter/postgres -run '^(TestReviewLearningPathPostgreSQLCreateReadAndReplay\|TestReviewLearningPathPostgreSQLConcurrentSameAndDifferentKeys\|TestReviewLearningPathPostgreSQLCompleteResponseLossReplay)$' -count=1` | PASS，25.766s；Final Artifact bridge 与 GORM callback / UoW 故障注入全部跑通 |
| `go test -tags=integration -timeout=60s ./internal/review/learningpath/adapter/postgres -run '^TestReviewLearningPathPostgreSQLCompleteWinsBeforeMaintenance$' -count=1` | PASS，7.145s；复验本轮迁移的另一处 SQL barrier |
| `go test -timeout=60s ./internal/memory/...` | PASS |
| `go vet ./internal/memory/...` | PASS |
| `go test -tags=integration -timeout=60s ./internal/memory/adapter/postgres -run '^(TestRepositoryPostgreSQLLifecycleReceiptScopeAndAudit\|TestRepositoryPostgreSQLConcurrentConfirmReplaysExactlyOnce\|TestRepositoryPostgreSQLInterviewCandidateDualIdempotency)$' -count=1` | PASS，21.503s |
| `git diff --check -- internal/organizing internal/review internal/memory` | PASS |
| `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-composition-pgx-convergence` | PASS；两个较大 spec 有 injection truncation 提示，相关内容已分段人工读取 |

Organizing staged 全 GORM 实库及 Generation 门禁详见原 child 的 `research/final-handoff.md`；这些是 Final 删除前的证据，不冒充删除后的复验。

## 曾遇阻断、跨 owner 调用方与验证边界

- LearningPath 的 create/read、原并发 barrier、原 commit-response-loss 的 Final 实库命令首次被 Artifact `generation.go` / `generation_terminal.go` 引用已删 `ModelRunTxFinalizer` / `WorkflowTerminalHook` 阻断。Artifact owner 完成清理后已重跑通过，阻断解除。
- Organizing Final 实库最初还受到 ModelSettings / Knowledge / Retrieval 旧跨 owner 接口的同步删除中间态阻断；跨 owner 稳定后的最终主路径已复验通过，未重复已通过的旧矩阵。
- 已通知父代理：`internal/platform/migration/m8_interview_history_hardening_integration_test.go` 的 Memory / Interview 构造需要由平台 owner 迁移；cmd Worker 的 tool/rag fixture 和 API 的 Review LearningPath fixture 由父代理统一接线。
- 只读 Final Composition 审查见 `research/composition-review.md`。未重复主会话负责的 cmd build，也未启动 API / Worker 做进程级运行或部署验证。

回滚只涉及本轮 Adapter / scoped Port / Composition 依赖闭包，不撤回 Atlas 迁移或历史数据。外部发布、生产启动顺序、全仓 allowlist 与 cmd 验证由 Final 主会话收口。
