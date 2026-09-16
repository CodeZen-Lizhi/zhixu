# 00121 人工全文发布的 F/P 双基线

2026-09-15；Authoring 发布切片。没有新增生产 generation/merge_review/HumanWait，没有 USER 编辑入口，没有放宽 AGENT parent、Git clean 或外部 Safe Writeback 协议。

## 实现

- `organizing.synthesis_publication_merge_baseline` 只读 owner 投影按候选 Article 精确关联 119 receipt、attempt、capture、synthesis v2、generated Article 和真实历史 publication/ProposalCommit。核对最终全文/hash/projection、目标路径、L/generated parent、Document 版本和 P 的完整发布身份。查询不接受客户端 P/F/hash；无 v2 的普通 Article 仍走原分支。
- `PublicationReservation.MergeBaseline` 是服务器返回字段，包含 receipt/capture、旧 P Article/hash。`ReservePublicationRecord` 和 Publish HTTP/request hash **没有新增可任意填写的基线参数**。同一 Article 的 immutable receipt 决定唯一 F/P；旧请求/hash 和普通预约不变。
- 新预约的 `BaseVersion` 是捕获 F.hash（无 P 时保持 absence token），数据库正式版本 CAS 使用 P.ID/hash。预约、恢复和绑定完成核对 Document/latest/P 与存储基线；字段不可变，SQL 校验精确 receipt/capture/Article 闭包。
- Authoring Finalizer 复用 `ValidateWritebackPreparation`，在既有文件锁内、实际写入前核对新 manuscript 预约的当前 Document/latest/P。普通文件提案保持原行为。实际字节 CAS、审批和 Git 提交仍由原 Change Control/Safe Writeback 实施。
- 00121 更新 `validate_article_revision_mutation` 和 `verify_publication_terminal_state` 的旧 P supersede 条件，使用分支函数 `publication_replaces_revision`。`publication_is_exact_current` 的 `Proposal.base_hash=reservation.base_version` 原封保留。00073:256 的旧 P 比较属于当次迁移的一次性 DO 历史检查，不在当前函数内，无需改写历史迁移。

## 实际验证

正式主目录（包含 119/120/121），没有 overlay：

- `go test -mod=vendor -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisManuscriptStorageCaptureReceiptAndV2$' -count=1 -timeout=60s`：PASS 11.386s，`/tmp/manuscript-121-publish.log`。
- `go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres -run '^TestRepositoryPostgreSQLPublication(ReservationCompletionReplayAndReads|FinalizerPublishesAndMarksRecovery)$' -count=1 -timeout=60s`：PASS 17.893s，`/tmp/manuscript-121-ordinary-publish.log`。
- `go test -mod=vendor ./internal/authoring/adapter/changecontrol ./internal/authoring/application ./internal/authoring/adapter/postgres`：PASS，`/tmp/manuscript-121-unit.log`。
- 相关四包 `go vet -mod=vendor -tags=integration` 与限定 `git diff --check`：通过，`/tmp/manuscript-121-vet.log`。

实库测试复用 119 的明确模型/权限 fixture，P/ProposalCommit/Git 改为真实记录；v2 candidate 仍由测试 owner 事务创建，尚无生产生成接线。测试覆盖 F 变动拒绝创建 Proposal；P pointer 漂移在回滚事务内拒绝 exact reservation 恢复；真实审批/授权/Safe Writeback/Git/旧 P SUPERSEDED；发布后重放与全文重读。

## 真实边界

1. Git 审批当前要求全仓 clean（`writeback_inspect.go:CaptureApprovalSnapshot`）。未提交 F 人工编辑确实返回 `GIT_REPOSITORY_DIRTY`；测试先断言拒绝，再**仅在隔离测试仓**提交用户 F 后批准候选。此时 F 仍不同于 Authoring 的 P，双基线实际生效。这不能证明未提交人工文件可直接发布；后续产品需明确此约束，不以测试提交行为掩盖。
2. 119 BaselineReader/Proof 的真实执行授权、model/source/scope/RootGrant 生产 composition 仍未接线；本批不以 SQL hash 投影代替这些 owner 证明。
3. 当前 Organizing runtime fixture 下限随读取契约提升至 121。Authoring 的 `newGeneratedAuthoringFixture` 保留 94 的真实 owner 约束，追加明确的依赖 fixture：四个 nullable reservation 字段被 CHECK 强制为 NULL，typed empty synthesis proof view 永远不返回证明。未修改生产缺表行为、历史迁移或关闭 FK/trigger。该组仅证明普通 generated Authoring owner 的 atomic/scope/AGENT/retirement 合同，不证明完整 121 合并 SQL；后者由上述正式主目录实库流程证明。原夹具因新增列失配的 P2 已修复。
4. schema.sql 和 atlas.sum 由 root 统一生成；本切片未自行改动它们。无提交、push、部署。

## Generated owner 夹具兼容修复复验

仅修改 `internal/authoring/adapter/postgres/generated_revision_integration_test.go` 的依赖 setup；原测试断言全部保留。命令共同前缀 `go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres`，各组 `-count=1 -timeout=60s`：

- `-run '^TestGeneratedAuthoringPostgreSQL(AtomicReplayAndScope|OriginAndMutationGuards)$'`：PASS 14.299s，`/tmp/manuscript-121-generated-owner.log`。
- `-run '^TestGeneratedAuthoringPostgreSQL(PublicationRetirementAtomic|RetirementWinsApprovalRace|ApprovalWinsRetirementRace)$'`：PASS 25.857s，`/tmp/manuscript-121-generated-retirement.log`。
- `-run '^TestGeneratedAuthoringPostgreSQL(PublicationMissingParent|PublishedBaselineAndHistoricalReplay)$'`：PASS 18.332s，`/tmp/manuscript-121-generated-history.log`。

相关 postgres 包 `go vet -mod=vendor -tags=integration` 与限定 diff check 通过，日志 `/tmp/manuscript-121-generated-vet.log`。此收尾没有修改 00121 SQL、atlas.sum 或 schema.sql；SQL 仍为已完成全文实库验证的版本。
