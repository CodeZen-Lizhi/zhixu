# Change Control 最终 GORM fixture 收敛

日期：2026-09-08。范围：本任务 TODO 10 的 Change Control 测试，以及追加授权的 M9 单文件迁移测试。产品代码由主会话维护。

## 变更

- 全部既有 CC / Approval Dispatch / Application fixture 改用最终 GORM 构造器和同一个平台 Pool。
- 数据库统一使用 `testdb.Require` / `FailWhenUnavailable`；配置 `ZHIXU_TEST_DATABASE_URL` 时将其作为 admin URL 创建隔离数据库，不对该 URL 指向的数据库直接执行迁移或清理。
- 删除仅为清理数据而包住旧 Repository 的 pgx 外层事务。SQL seed、约束负测和查询事实继续使用同 Pool 的原生连接；业务事务由 GORM UoW 创建。
- Publish 冲突和 Approval Dispatch 提交响应丢失使用真实 UoW 包装，保留事务回滚、原子状态和精确重放断言。
- Workflow 使用真实 Model Settings enqueue fence 和 Change Control scoped cancellation hook；Tools 使用 Workflow scoped policy/recovery 能力。
- HTTP/River/Reindex smoke 使用真实 scoped Outbox、CC binding verifier / completion participant。保留 4 个 checkpoint、Vector、Ready、Completion 的响应丢失注入，以及唯一 Active/Activation、不可变 Git 内容和 payload 洁净断言。
- 两个原产品 SQL INSERT 常量迁到测试，补齐既有 raw 负测遗漏的 `target_mode` 参数。非法 Workspace fixture 状态改为 `inactive`。
- M9 在原 33 版本回填断言完成后，为专门测试“无 revision 风险回填”的样例补一个历史 revision，再升级当前 Schema；使用 `openMigrationRuntimePool` 关闭迁移 Pool 并切换到 GORM runtime Pool。
- 未新增测试文件、测试入口或 build tag；原断言保留。新增的函数仅用于现有 fixture 接线和错误诊断。

## 修改文件

- `internal/changecontrol/adapter/postgres/repository_integration_test.go`
- `internal/changecontrol/adapter/postgres/repository_writeback_integration_test.go`
- `internal/changecontrol/adapter/postgres/typed_proposal_integration_test.go`
- `internal/changecontrol/adapter/postgres/restore_document_integration_test.go`
- `internal/changecontrol/adapter/postgres/list_integration_test.go`
- `internal/changecontrol/adapter/approvaldispatchpostgres/repository_integration_test.go`
- `internal/changecontrol/application/writeback_smoke_integration_test.go`
- `internal/changecontrol/application/writeback_fault_smoke_integration_test.go`
- `internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go`
- `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go`
- `internal/platform/migration/m9_business_contract_hardening_integration_test.go`

## 验证命令与结果

所有实库分组使用 `-count=1 -timeout=60s`，除特别注明的 `-count=2`。CC 实库采用独立 Testcontainers PostgreSQL 16 / pgvector。未运行全仓测试或全量构建。

| 范围 | 命令 / 选择器 | 结果 |
| --- | --- | --- |
| CC 集成编译 | `go test -mod=vendor -tags=integration ./internal/changecontrol/... -run '^$' -count=1 -timeout=60s` | PASS，全部包 |
| M9 集成编译 | `go test -mod=vendor -tags=integration ./internal/platform/migration -run '^$' -count=1 -timeout=60s` | PASS，1.078s |
| CC unit | 下方脚本选择 254 个既有非 integration Test 入口 | PASS，全部包 |
| CC vet | `go vet -mod=vendor -tags=integration ./internal/changecontrol/...` | PASS |
| 授权 / 写回 / 取消 | `go test -mod=vendor -tags=integration ./internal/changecontrol/adapter/postgres -run '^Test(RepositoryWriteAuthorizationLifecycle\|RepositoryBeginWritebackAtomicDoubleConsumeAndReplay\|RepositoryWritebackCheckpointAndPublish\|WorkflowCancellationWaitsForWritebackRecoveryCheckpoint)$' -count=1 -timeout=60s -v` | 4 / 4 PASS，31.129s |
| Approval Dispatch 原分组 | `go test -mod=vendor -tags=integration ./internal/changecontrol/adapter/approvaldispatchpostgres -run '^TestApprovalDispatch(AtomicallyCreatesAndReplaysWorkflowRiverBinding\|RollsBackDecisionWhenRuntimeFails\|RejectedNeverCreatesWorkflow\|RecoversCommitResponseLossByExactReplay)$' -count=1 -timeout=60s -v` | rollback / commit-response-loss 2 项 PASS；并发与拒绝暴露下述产品缺陷，随后定向复验通过 |
| Approval Dispatch 修复复验 | `go test -mod=vendor -tags=integration ./internal/changecontrol/adapter/approvaldispatchpostgres -run '^TestApprovalDispatch(AtomicallyCreatesAndReplaysWorkflowRiverBinding\|RejectedNeverCreatesWorkflow)$' -count=2 -timeout=60s -v` | 2 场景各 2 次全部 PASS，32.651s |
| PostgreSQL / Git / LocalFS | `go test -mod=vendor ./internal/changecontrol/application -run '^Test(SafeWritebackWorkflowNodePostgreSQLGitFilesystemSmoke\|WritebackSagaRealFaultSmoke)$' -count=1 -timeout=60s -v` | 2 / 2 PASS，15.254s |
| HTTP → 双 River worker → Writeback → Reindex → Hybrid Search | `go test -mod=vendor -tags=integration ./internal/changecontrol/application -run '^TestApprovalDispatchRealRiverSafeWritebackSmoke$' -count=1 -timeout=60s -v` | PASS，19.106s；7 次 Reindex 故障均被消费 |
| List / Restore / Typed Knowledge | `go test -mod=vendor -tags=integration ./internal/changecontrol/adapter/postgres -run '^TestRepository(ListProposalsWithPostgres\|CreatesReplaysAndReadsTypedRestoreDocumentProposal\|KnowledgeChangeProposalRoundTripAndReplay)$' -count=1 -timeout=60s -v` | 3 / 3 PASS，34.409s；EXPLAIN 的第一页和游标页均命中 `idx_proposal_workspace_updated_id`，根节点为 Limit |
| SQL 约束 / Begin rollback / 并发重放 | `go test -mod=vendor -tags=integration ./internal/changecontrol/adapter/postgres -run '^Test(WritebackSQLConstraints\|RepositoryBeginWritebackRollsBackBothAuthorizations\|RepositoryBeginWritebackConcurrentReplayCreatesOneExecution)$' -count=1 -timeout=60s -v` | 3 / 3 PASS，34.272s |
| M9 最终实库 | `go test -mod=vendor -tags=integration ./internal/platform/migration -run '^TestM9BusinessContractHardeningMigrationBackfillConstraints$' -count=1 -timeout=60s -v` | FAIL，8.896s；独立临时 PostgreSQL，触发下述 82 历史回填缺陷，已交主会话 |
| diff 格式 | `git diff --check` | PASS |

unit 选择脚本与实际执行一致，避免未加 build tag 的既有数据库 fixture 被普通 unit 分组带入：

```python
from pathlib import Path
import re
import subprocess

names = []
for path in Path('internal/changecontrol').rglob('*_test.go'):
    if not path.name.endswith('_integration_test.go'):
        names.extend(re.findall(r'^func (Test\w+)\(', path.read_text(), re.M))
pattern = '^(' + '|'.join(sorted(set(names))) + ')$'
result = subprocess.run(['go', 'test', '-mod=vendor', './internal/changecontrol/...',
                         '-run', pattern, '-count=1', '-timeout=60s'])
raise SystemExit(result.returncode)
```

## Review 与已修问题

按照 `go-review` 与 `sql-code-review` 检查同 Pool 接线、scope 生命周期、错误分类、回滚、锁顺序、参数化及原断言保留情况。

1. Approval Dispatch 拒绝 INSERT 硬编码 `REJECTED`，与 domain / SQL 的 `rejected` 不符。主会话改为绑定 `domain.DecisionRejected`。
2. 并发批准时 `LEFT JOIN proposal_revision_dispatch` 与 `FOR UPDATE p,r` 共用一个 READ COMMITTED statement，等锁后 nullable 侧可能保持旧快照，导致 `23505 / proposal_revision_dispatch_pkey`。主会话改为先锁 p/r，再独立读取 immutable dispatch 绑定。原并发测试各次严格要求一个首次结果、一个 replay，复验通过。
3. M9 的 `noRevisionID` 专用历史 fixture 在原 M9 断言后升级到 82 时被严格回填拒绝；仅在 M9 断言完成后补齐样例，保留原无 revision 风险升级断言。
4. 原 raw 授权 / 写回 SQL fixture 缺少 `target_mode` 参数；已补齐，避免负测仅因占位符参数数量不符而通过。
5. M9 补齐历史 revision 后，82 的 `UPDATE proposal SET current_revision_id=...` 被 62 版 `validate_proposal_transition` 拒绝，错误 `23514 / proposal immutable field or version violation`。旧 guard 同时要求 version 递增和合法 status 迁移；只增加 version 仍不足以允许纯指针回填。82 到后半部分才替换该函数。这是当前历史升级路径的产品迁移问题，已报告主会话；未在测试中禁用触发器或绕过升级。

## 限制与后续

- 既有未选中的实库矩阵已完成最终 API 编译，未声称全部实库用例执行过。
- M9 最终实库尚未通过，原因是 82 的历史指针回填与当时仍生效的旧 transition guard 冲突；需主会话处理后仅复验该测试。
- PostgreSQL 容器与所有业务数据均为临时隔离资源，未修改用户现有数据库；无 commit / push。
- 业务产品改动和全仓整合由主会话最终确认。
