# Knowledge staged GORM 静态验证

- 日期：2026-08-20；独立会话复核：2026-08-31
- 范围：Knowledge core commands/reads、Timeline/Impact/Projection、Approved Relation Apply、scoped Impact Audit 及本任务工件
- 结论：staged 实现和局部静态门禁通过；真实 PostgreSQL TODO 9、生产 Composition 切换和 legacy 删除保持未完成。

## 实现边界

- `GORMRepository` 只从一个 `platformpostgres.Pool` 取得 GORM root 与 `foundation.UnitOfWork`；没有独立连接池、Schema 管理、双读、双写或 fallback。
- Domain Repository 的 Topic、Claim、Relation、Conflict command 与三类 bounded read、Evidence、Timeline/Impact 和 Projection 端口已由 staged sibling 覆盖。
- `GORMApprovedRelationApplyRepository` 在单一 scope 内保持 Candidate-before-Proposal、Approval/Proposal、Knowledge Relation/Evidence/receipt 与 optional scoped Event 原子性。
- `NewScopedImpactServiceWithAudit` 只接受 scoped Repository/Audit；首次保存和已有报告 replay 都经 `SaveImpactReportWithScopedAudit`，在同一 UoW 追加脱敏 Audit。
- legacy Repository、`any` transaction Port、migration、测试文件以及 API/Worker/Organizing production wiring 均未修改或切换。

## Review 发现与修复

1. **P1**：初始 scoped Impact replay 仍走 legacy `recordAudit` 分支，因 scoped 构造没有 legacy Audit 而漏记 replay Audit。现由 Repository 在 opaque scope 内完成 report exact replay 与 Audit。
2. **P1**：首轮 staged Repository 未覆盖 `OpenConflict` / `TransitionConflict`。恢复 `domain.Repository` 编译期断言后暴露缺口，现已补齐稳定 Topic/Claim 锁、member 批量写入、Claim disputed 闭包、CAS 与 receipt。
3. **P1**：scoped Impact replay 统一调用 `gormSaveImpactReport` 后，会对已持久化报告重新执行可变的 V2 selector readiness 门禁；这与 legacy Service 的 existing-report exact replay 语义不等价，现有 `TestImpactAnalyzeReplaysCurrentV2BeforeReadinessAndHistoricalGetRemainsAvailable` 也明确要求 replay 不调用 readiness/save。现改为同一 UoW 内先读取并 exact compare 已有报告，仅首次写入调用 `gormSaveImpactReport`；事务前仍执行 canonical time 与完整性校验，Audit 原子性不变。
4. **P2**：scoped/legacy Impact 构造器和运行时检查只用 `== nil` 判断 `IDGenerator` / `Clock`，会接受 typed-nil 接口并在 Analyze 时触发 panic。现统一使用项目 `isNil` fail closed。
5. **P2**：checklist 要求的 Claim Query 与 Evidence Topic 编译期断言缺失。现分别补充 `ClaimQueryRepository` 与 `EvidenceTopicRepository` 断言。
6. **P1**：`classifyGORMKnowledge` 把 caller cancel 归为 `DependencyUnavailable`，且 Knowledge/Relation Apply 分类器在 UoW 包装已有 `foundation.Error` 后返回外层 wrapper，破坏调用方对项目错误 envelope 的直接契约。现按 canceled=`NonRetryableFailure`、deadline=`RetryableFailure` 分类，并返回原始 `foundation.Error`；已在现有 `errors_test.go` 增加回归断言。
7. **P1**：migration `00082` 已引入 `proposal.current_revision_id`，但 staged Relation Apply 只按 Proposal/Revision ID 关联，旧 revision 可在指针推进后继续 Approval/Apply。现按 Change Control GORM adapter 的扩容期兼容语义，在 Candidate 定位、Proposal/Revision 锁定读取、approved binding 与全部状态 CAS 同时约束 current revision；Candidate-before-Proposal 锁序不变。

P0 为零；以上 P1/P2 均已修复，未发现其他 P1/P2。SQL Review 未发现可静态确认的缺陷。参数顺序、Workspace 条件、锁序、CAS、receipt、JSONB/array carrier、RR+ReadOnly 批读、Timeline `SKIP LOCKED`、Impact+Audit 和 Relation Apply+Event 均完成静态对照。Trellis Check 确认任务范围、生产未接线和 TODO 9 状态门禁一致。

## 已执行门禁

- `go test -mod=vendor ./internal/knowledge/... -count=1 -timeout 60s`：PASS
- `go test -race -mod=vendor ./internal/knowledge/... -count=1 -timeout 60s`：PASS
- `go vet -mod=vendor ./internal/knowledge/... ./internal/platform/postgres ./internal/events/... ./internal/audit/...`：PASS
- `go test -mod=vendor -tags=integration -run '^$' ./internal/knowledge/adapter/postgres ./internal/knowledge/adapter/audit ./internal/knowledge/application -count=1 -timeout 60s`：PASS（仅编译）
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/organizing/... -count=1 -timeout 60s`：PASS（仅编译）
- `go list -mod=vendor`（Knowledge、Platform、Events、Audit、API、Worker、Organizing）：PASS
- `go mod verify`：PASS
- `go mod tidy -diff`：非零，仅显示任务开始前已有的全仓 `go.sum` 规范化漂移及 SQLite checksum；未应用输出
- 禁用模式与 production wiring 扫描：PASS；未发现 pgx import、AutoMigrate/Migrator、Save/Preload/Association、独立 `gorm.Open` 或 staged 生产构造
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-knowledge-migration`：PASS，仅有大规格文件注入截断警告
- 受影响 Go 文件 `gofmt -d`、已跟踪及未跟踪文件 whitespace 检查、全局 `git diff --check`：PASS

## 当时未完成门禁

`ZHIXU_TEST_DATABASE_URL` 未配置，编译和现有 pgx 测试不能证明 GORM 在真实 migrated PostgreSQL 上的行为。TODO 9 仍需在现有 integration 文件内以 legacy/GORM 独立 disposable database 验证：

- UUID/JSONB/text 与 uuid arrays 的 driver binding、strict decode、真实 SQLSTATE 和 deferred constraints；
- receipt/fingerprint/source-ref exact replay、CAS、锁竞争、deadlock/serialization 分类和 commit response loss；
- 500 条 bounded RR 快照与完整 hydration、连接释放和目标索引 EXPLAIN；
- Timeline projection claim/project/poison、Impact V1/V2 readiness/supersedes 与 Report+Audit 回滚；
- Relation Apply Approval/Candidate/Proposal/Relation/Evidence/receipt/Event 全有或全无，以及 baseline stale 唯一提交分支；
- canceled/deadline/custom cause、`sql.ErrTxDone`、scope 失效和跨模块 rollback 可见性。

Foundation scope 当前不携带可验证的 Pool identity，因此 staged adapters 无法单独拒绝来自另一 active Pool 的 scope。Final Composition 和 TODO 9 fixture 必须从同一 `platformpostgres.Pool` 派生 Knowledge、Events、Audit 与 UoW。

以上是 2026-08-20/2026-08-31 静态阶段的历史状态；当时任务保持
`in_progress`，PRD AC 与 TODO 9 未勾选，不归档，也不切生产 Composition。

## 2026-08-31 独立会话复核证据

- `go test -mod=vendor ./internal/knowledge/... -count=1 -timeout 60s`：PASS
- `go test -race -mod=vendor ./internal/knowledge/... -count=1 -timeout 60s`：PASS
- `go vet -mod=vendor ./internal/knowledge/... ./internal/platform/postgres ./internal/events/... ./internal/audit/...`：PASS
- `go test -mod=vendor -tags=integration -run '^$' ./internal/knowledge/adapter/postgres ./internal/knowledge/adapter/audit ./internal/knowledge/application -count=1 -timeout 60s`：PASS（仅编译）
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/organizing/... -count=1 -timeout 60s`：PASS（仅编译）
- scoped `go list -mod=vendor` 与 `go mod verify`：PASS
- `task.py validate`：PASS，仅有既有大规格文件注入截断警告；`gofmt -d`、全局 `git diff --check` 与 scoped trailing-whitespace 扫描：PASS
- 禁用模式扫描未发现 AutoMigrate/Migrator、独立 `gorm.Open`、root `.Transaction`、Save/Preload/Association；动态 SQL 片段仅来自包内固定列、条件和锁文本，外部值均绑定。
- `ZHIXU_TEST_DATABASE_URL` 未配置；未运行真实 migrated PostgreSQL，故 TODO 9 的 deferred constraint、真实 driver binding、并发锁/response-loss、跨 owner rollback 与 Pool affinity 仍未验证。

## 2026-09-05 状态更新

父任务的精简测试政策已取代上述全矩阵完成要求。当前已通过 GORM-only
Testcontainers 主读写/查询场景和 Impact/Audit 真实 scoped 写入后回滚场景；
首轮暴露并修复的 Impact JSONB/时间占位错位、最终命令与剩余按风险专项见
`2026-09-05-gorm-integration.md`。生产 Composition、Organizing caller-owned
transaction 与 legacy 删除仍未改变，继续由后续 child/Final 负责。
