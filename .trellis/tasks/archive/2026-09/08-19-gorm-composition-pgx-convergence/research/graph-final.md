# Graph Final 收敛记录

日期：2026-09-08。范围为 `internal/graph/**`。Final 主会话确认全部模块门禁后已放行本地 legacy 删除；`cmd/**` 接线、平台 helper、全仓 allowlist 和发布记录由主会话拥有。本记录覆盖清理后的实际结果，不以先前 Graph child 的证据代替本轮验证。

## 生产实现

- 删除旧 `Repository`、`CandidateConfirmRepository`、`SemanticLinkScanRepository`、Topic/Smart planner/page、Discovery writer 及旧事务构造；`scan_planner.go`、`scan_cancellation.go` 删除。保留仍被最终 GORM 实现使用的固定 SQL、record、codec、scanner、领域校验、BFS、排序、CAS 与 v1/v2 receipt replay。
- 删除 exported pgx-shaped `DB`、`gormDB`、完整 pgx.Rows 伪实现、CommandTag 桥和 `withinDB/readSnapshot` 适配入口。Candidate/Confirm 的共享 helper 直接使用 `*gorm.DB`，事务仍由平台 `UnitOfWork` 和 opaque scope 拥有。
- **删除 `renderGORMPositional` 与全部 lexer/运行时参数转换。** 查询原样交给 GORM 原生 NamedExpr；调用点显式 `sql.Named`，数组使用成熟的 `pq.Array`，JSONB 使用已有校验 carrier。`(@pN)::uuid` 的括号是 GORM 命名参数与 PostgreSQL cast 的明确边界，重复或乱序参数由 GORM 自身处理。
- `gorm_adapter.go` 仅保留中性的 `database/sql` scanner/Rows 与 Raw/Exec helper；单行查询会保留 GORM callback 错误。没有重新引入 SQL 转译器、第二物理池或生产测试注入接口。
- 无结果统一检查标准 `sql.ErrNoRows` / `gorm.ErrRecordNotFound`；vendor 的 pgx sentinel 包装标准 sentinel，既有单测验证兼容。错误分类使用平台 `SQLState` / `IsStatementTimeout`，保持 `57014` 下 statement timeout 与显式取消的不同 kind/code/retryable 和原始 cause。
- Smart planner 直接执行既有 Collection durable-read 合同，不再委托旧 planner type。Graph Application/Domain 和外部 API 未改变。

主要修改文件组：`gorm_core.go`、`gorm_adapter.go`、`gorm_candidate*.go`、`gorm_query*.go`、`gorm_scan*.go`、`gorm_discovery_candidate_writer.go`；同目录旧 `repository.go`、`candidate_*.go`、Query/Topic/Smart/Discovery 文件删去 legacy receiver，只保留共享事实和纯逻辑。

## 现有测试迁移

- `repository_integration_test.go` 的 fixture 改用 `testdb.Require(FailWhenUnavailable)` 提供的完整平台 Pool。Seed/assertion 与 GORM 共用同一个物理池，测试不再向 Repository 注入未提交 pgx.Tx；显式 seed 事务先提交，Pool seed 使用真实提交的数据。
- 修正旧 fixture 中从未经过提交约束的确认态数据：Claim 在自动提交前具备支持来源，Relation 按 Suggested → Evidence → Confirmed 构造；历史 Proposal seed 同事务写入 `current_revision_id` 与 Revision。没有禁用生产约束来使这些用例通过。
- Candidate/Confirm、Scan/River、Smart Collection、Approval/Apply、Topic exclusions 和 SQL plan 的现有测试全部改到最终 GORM 构造。Workflow 的 ModelSettings enqueue fence、Graph cancellation guard、Collection 与 Events 使用同池 scoped 组合；River Worker/listener 仍使用官方 pgx driver。
- Query count、SQL fault、路径 barrier、statement timeout 和锁序使用实际 GORM callbacks。成功事件仍委托真正的 scoped Events Store；真实 response-loss 在内部 UoW 完成 commit 后才注入错误。未新增测试文件、临时测试代码、build tag 或 skip。
- Knowledge 私有 UoW 的两条 commit-response-loss 用例移到其现有 `repository_integration_test.go`，同名为 `TestApprovedCandidateApplyRecoversCommitResponseLoss` 和 `TestCandidateApprovalAndRelationApplyRecoverCommitResponseLoss`。Knowledge owner 已反馈两用例实库 PASS（15.820s），保留原 Approval ID、APPLIED/version 4、Relation/Receipt/Evidence/Event 数量和精确 replay 断言；见 [Knowledge Final](knowledge-final.md)。Graph 原两函数随迁移删除，其他业务断言保留。
- 旧构造拒绝外部 Tx 的运行时测试改为完整 Pool 构造下外部事务独立可用；无法再向最终构造传入 pgx.Tx。旧 renderer 实现细节测试原位改为 GORM 原生命名参数/array/JSON carrier 检查，不再保留已删除 DSL 的 malformed-SQL 契约。
- 容量 benchmark 改成平台单池构造与原生命名参数。GORM 语句 callback 加真实 UoW begin/commit-or-rollback 边界计数，保留每次 Neighborhood **6 条数据库语句**的不变量。

## 清理后验证

所有实库命令均为 `go test -mod=vendor -tags=integration ./internal/graph/adapter/postgres -run '<下列精确测试名组成的表达式>' -count=1 -timeout=60s -v`。每组命令都设置 60 秒超时，均通过 Docker/Testcontainers 使用真实 PostgreSQL，没有 skip。

| 现有用例 | 最终结果 |
| --- | --- |
| `TestGORMCandidateRepositoryCreatesAndReplaysWithBatchEvidence` | PASS 9.48s，含 Confirm 跨 owner rollback/commit |
| `TestRepositoryGlobalSearchAndDetailsUseCanonicalKnowledgeFacts` | PASS 9.92s |
| `TestGORMSemanticLinkScanCommitsAndRollsBackWorkflowAndRiver` | PASS 6.85s |
| `TestRepositoryGlobalFiltersRecomputeClusterFromEligibleFacts` | PASS 8.48s |
| `TestRepositoryFindPathIsDeterministicAndDirectionAware` | PASS 7.72s |
| `TestRepositoryFindPathReturnsFrontierCancellationAndTimeoutWithoutPartial` | PASS 7.87s，含取消、deadline、statement timeout、显式 PostgreSQL cancel |
| `TestCandidateApprovalAndRelationApplyRollBackTogetherOnEvidenceFailure` | PASS 6.91s |
| `TestCandidateApprovalAndRelationApplyLocksCandidateBeforeProposal` | PASS 6.84s |
| `TestCandidateApprovalAndRelationApplyReusesConcurrentSuggestedWinner` | PASS 11.21s |
| `TestGraphAdjacencyAndEvidencePlansUseKnowledgeIndexes` | 修正后 PASS 9.98s |
| `TestCandidateConfirmExactlyReplaysLegacyV1ReceiptAndProposal` | PASS 5.78s |
| `TestSemanticLinkCandidateRepositoryLifecycleAndBatchHydration` | PASS 11.52s，保留列表恰好两条查询断言 |
| `TestSemanticLinkTopicScanRunsThroughRealRiverToCandidate` | PASS 23.92s，实际 Worker 执行并持久化 Candidate |

首组总耗时 26.632s；Query/BFS/timeout 组中最初 EXPLAIN 断言失败，其余三个 PASS；修正后的 Plan/Approval 组总耗时 35.683s；Lifecycle/V1/River 组总耗时 41.840s。

EXPLAIN 失败原因已核实：Atlas `00026_smart_collection_health_query_indexes.sql` 增加了 `idx_knowledge_relation_workspace_source_status_type` / `idx_knowledge_relation_workspace_target_status_type`，前缀与 `00017` 的对应 endpoint 索引一致并覆盖 status。PostgreSQL 选择这些索引时旧测试误判失败。测试只新增这两个明确等价选项，Relation/Evidence 禁止 Seq Scan、所需索引与 Topic 每 source Limit 断言保留；没有更改 Schema 或为了固定计划禁用索引。

| 静态命令 | 结果 |
| --- | --- |
| `go build -mod=vendor ./internal/graph/...` | PASS |
| `go test -mod=vendor ./internal/graph/... -count=1 -timeout=60s` | PASS，六包 |
| `go test -mod=vendor -tags=integration ./internal/graph/... -run '^$' -timeout=60s` | PASS，含 testfixture/命令共九包 |
| `go vet -mod=vendor -tags=integration ./internal/graph/...` | PASS；最后的 fixture deadline 顺序修正后再执行一次亦 PASS |
| `gofmt` 与 `git diff --check -- internal/graph` | PASS |
| Graph 非测试、非 testfixture 的 pgx/pgconn/pgxpool/旧构造/renderer 搜索 | 无匹配 |

## Go / SQL Review 与边界

按照已加载 Go Review、SQL Review 自检最终 diff。核对命名参数与 SQL casts、数组和 JSONB 单值绑定、workspace 谓词、唯一约束、CAS、分页顺序与上限、BFS 完整层、批量 Evidence、行锁先后、只读 Repeatable Read、timeout 与 cause、跨 owner 同 scope、River 生命周期；本轮明确问题已修复并由上述实库场景覆盖。普通仓储不再依赖 pgx；测试 Seed/assertion、显式容量 fixture 和官方 River Worker 仍允许使用底层驱动。

未运行全量 integration、`-race`、500k 容量 benchmark、浏览器或生产发布/回滚演练。已有 benchmark 和全部测试保留且可编译；Final 主会话负责全仓组合、allowlist 与最终发布门禁。Graph 不再有待迁移 legacy 生产入口。本轮未修改 Schema、`cmd/**`、其他 owner 或 task pointer，未 commit/push/部署。回滚需由 Final 按 Graph 与相关 Composition 的依赖闭包恢复代码，数据库版本与数据格式保持不变。
