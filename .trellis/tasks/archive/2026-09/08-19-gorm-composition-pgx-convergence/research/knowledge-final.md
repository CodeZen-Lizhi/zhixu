# Knowledge Final 收口

## 2026-09-08 状态

主会话确认全部模块核心门禁通过后，将 `internal/knowledge/**` 的最终旧实现清理转交给本 owner。Knowledge 的生产旧 pgx Repository、旧 Appender 和 Impact `transaction any` 审计桥已删除；单一 GORM/scoped 路径的 unit、vet 和三组定向实库均通过。主会话新增的 `ScopedClaimReader` / `BatchGetClaimsScoped` 保留，Organizing 继续在调用方快照中读取 Claim 与 Sources。

### 实现与契约

- 删除旧 `Repository` / `NewRepository(DB)`、`ApprovedRelationApplyRepository` / `NewApprovedRelationApplyRepository`、pgx transaction/query helpers；`claim_commands.go`、`eligibility.go`、`evidence_topic.go` 的整份旧实现删除。保留 GORM 复用的纯校验、codec、明确 select 列、receipt 与状态值，不复制业务 SQL。
- `repository.go`、`read.go`、`scans.go`、`relation_commands.go`、`conflict_commands.go`、`relation_apply.go`、`timeline.go`、`timeline_projection.go` 仅保留仍使用的纯 helpers。Timeline scanner 的 no-row 分支使用 `database/sql.ErrNoRows`；原 SQLSTATE 分类矩阵通过平台 `SQLState` 读取，未扩大 57014 或约束错误的重试范围。
- 保留所有正式 GORM 构造及实现。`NewGORMApprovedRelationApplyRepository` 仍通过同 Pool 的 UoW、ScopedAppender 写入 Approval / Proposal / Relation / Evidence / Receipt / Event；Candidate-before-Proposal 锁序、唯一 canonical Relation、baseline stale 仅提交 needs_revision 分支保持。
- Impact 删除 `ImpactAuditPort`、`NewImpactServiceWithAudit`、`SaveImpactReportWithAudit` 和 `RecordImpactAnalysisTx`。正式服务使用 `NewScopedImpactServiceWithAudit`；无审计的既有 `NewImpactService` 保留。首次报告、trigger Outbox 和 Audit 同 scope 写入；已持久报告 replay 在可变 v2 readiness gate 前处理。
- 原位迁移 Audit / Impact 单元 stub 和 Knowledge integration 构造。旧/GORM 对照 fixture 收敛到最终 GORM；共享业务断言全部保留，删除仅重复运行同一场景的 legacy wrapper 和无调用的环境 skip fixtures；没有新增测试文件、临时测试程序或 skip。
- Graph owner 的两个 Knowledge commit response-loss 场景迁入现有 `repository_integration_test.go`：`TestApprovedCandidateApplyRecoversCommitResponseLoss` 与 `TestCandidateApprovalAndRelationApplyRecoverCommitResponseLoss`。真实 `delegate.Within` 成功提交后首次注入错误，后续用全新仓储 replay；保留原 Approval ID、APPLIED/version 4、Approval/Relation/Receipt 各一条、Evidence confirmation/applicability 与 approved/applied Event 各一条，追加 commit stage 错误码及收据 hash/aggregate 绑定检查。Graph owner 保留其余跨 owner 回滚/锁序/并发业务断言。

### 验证命令与结果

以下均已通过，所有实库用例使用项目 Testcontainers 的 fail-on-unavailable 配置，每命令显式 60 秒上限：

```bash
go test -mod=vendor -count=1 -timeout=60s ./internal/knowledge/...
go test -mod=vendor -tags=integration -run '^$' -timeout=60s ./internal/knowledge/...
go vet -mod=vendor ./internal/knowledge/...
go vet -mod=vendor -tags=integration ./internal/knowledge/...
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^TestGORMKnowledgeLeanMainPathAndScopedAuditRollback$' ./internal/knowledge/adapter/postgres
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^(TestApprovedCandidateApplyRecoversCommitResponseLoss|TestCandidateApprovalAndRelationApplyRecoverCommitResponseLoss)$' ./internal/knowledge/adapter/postgres
go test -mod=vendor -tags=integration -count=1 -timeout=60s -run '^(TestRepositoryKnowledgeLifecycleReplayAndEvidenceHistory|TestTimelineImpactProjectionGORMIntegration)$/(gorm|append_page_and_impact_replay|impact_concurrent_replay|projection_poison|projection_skip_locked)$' ./internal/knowledge/adapter/postgres
git diff --check -- internal/knowledge internal/platform/migration/timeline_impact_integration_test.go internal/platform/migration/review_invalidation_observability_integration_test.go
```

实库结果分别为：核心 Claim + 真实 Audit 写后 rollback 11.947s；两个 Apply commit response-loss 15.820s；完整知识生命周期与历史证据 + Timeline 重放、Impact 并发、poison、SKIP LOCKED 17.016s。

### Go / SQL review

按 go-review 与 sql-code-review 自审：纯 helper 保留数据验证；GORM 的 batch 500 上限、RR/read-only、数组单参数、批量 child hydration、Workspace 条件、固定排序、Rows 关闭、CAS/锁序、错误 cause 与事务生命周期保持。实际 Audit 写入后故障验证了 Report / trigger Outbox / Audit 全部回滚；真实物理 commit 后的 response loss 验证了跨 owner 重放不重复事实。未发现剩余范围内明确缺陷。

### Migration fixture 跟进

主会话追加独占两份既有 fixture：`internal/platform/migration/timeline_impact_integration_test.go` 和 `review_invalidation_observability_integration_test.go`。

- 复用主会话新增的 `openMigrationRuntimePool(t, ctx, pool)`：关闭 raw migration pool 后在同一隔离数据库打开 application platform Pool，Knowledge 与后续 seed/assertion 共用它，调用方先 `defer platform.Close()` 再由既有 cleanup 删除数据库。
- 最终构造为 `NewGORMRepository(platform)`；`projectTimelineSource` 接收纯 `TimelineProjectionPort`。
- Review fixture 保留 through-49 的 backfill/lifecycle/assertions，之后按当前 projector 列契约升级至 62 再投影，和既有 Timeline fixture 保持一致；不修改 Atlas Schema 源文件。
- 迁移包 compile-only 首次被其他 owner 的 Agent / Tools / Conversation / ChangeControl 旧构造阻断；最终全部 fixture 已恢复编译。
- 主会话已在专用临时 PostgreSQL 串行执行三个原用例，均使用 `go test -mod=vendor -tags=integration ./internal/platform/migration -run '<精确测试名>' -count=1 -timeout=60s`：`TestTimelineImpactMigrationBackfillsAndProjectsHealthLifecycle` PASS 10.151s；`TestTimelineImpactMigrationBackfillsAndProjectsSourceDirectory` PASS 10.285s；`TestReviewInvalidationObservabilityMigrationUsesHealthAndTimelineOwners` PASS 10.747s。历史回填、owner 事实与当前 GORM 投影断言均保留。

### 回滚与边界

无 Schema、HTTP wire、状态机、历史数据、依赖升级、生产部署或 git 操作。回滚按 Final Composition 与 scoped Event/Audit/Organizing 调用方的依赖闭包恢复代码，不能单独恢复旧 pgx 任意事务接口。主会话负责最终 cmd / 全仓静态门禁 / 其他 owner 的集成；未执行全仓测试、生产容量或浏览器验证。
