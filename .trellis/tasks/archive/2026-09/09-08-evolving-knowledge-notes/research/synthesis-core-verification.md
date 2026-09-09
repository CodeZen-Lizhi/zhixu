# W2 数据、发布与来源验证

日期：2026-09-09。范围为 W2 SynthesisService/GORM store/原始来源 reader、Authoring scoped generated read、00095 及对应测试；不代表 W3 模型运行、W4 面试、W5 页面或整体项目已完成。

## 文件与行为

- `internal/organizing/application/synthesis_store.go`、`synthesis_service.go`、`synthesis_processing.go`：候选事务合同、真实 Authoring publication 完成、immutable receipt 恢复、公开读取与 processing page。
- `internal/organizing/adapter/postgres/synthesis_models.go`、`synthesis_store.go`、`synthesis_read.go`：短 Workspace lock、真实 semantic journal/source fence、Generated ArticleRevision/投影/来源/预约/receipt 原子提交、NO_CHANGE、同作用域 receipt、批量摘要和 published proof 查询。
- `internal/organizing/adapter/owner/synthesis_source.go`：managed Artifact 原始字节/派生 parser excerpt，精确 tuple/hash/span/Workspace、原始来源 current/security fence、服务端 generated provenance 排除、数据库传输前 excerpt 大小限制。
- `internal/authoring/application/generated.go` 与 `internal/authoring/adapter/postgres/gorm_generated_read.go`：扩展最窄 scoped generated-document 状态读取，复用 Authoring origin/document 锁顺序。
- `atlas/migrations/00095_synthesis_notes.sql`：Note/Revision 与 generated origin 双向约束、来源 tuple、Workflow/ModelRun/Workspace 绑定、不可变历史、同事务 topic/source/receipt/reservation 闭合及历史 child 成员限制。
- `internal/organizing/adapter/owner/synthesis_source_test.go`、`internal/organizing/adapter/postgres/synthesis_integration_test.go`：来源边界单元测试和隔离 PostgreSQL 业务/约束回归。
- `internal/organizing/adapter/postgres/synthesis_publication_integration_test.go`：主会话追加交给 W2 的临时 Workspace/Git 发布正向组合验证，仅操作 `t.TempDir()`。

当前 00095 SHA-256：`ee31436419cfe08281f43d7f9f84a9d4e92257604468572cef03358d21cf57af`。`atlas.sum` 由主会话统一刷新，W2 不修改它。

## 已执行门禁

```sh
go test -mod=vendor ./internal/organizing/application ./internal/organizing/adapter/postgres ./internal/organizing/adapter/owner ./internal/authoring/adapter/postgres -timeout=60s
go test -mod=vendor -race ./internal/organizing/application ./internal/organizing/adapter/postgres ./internal/organizing/adapter/owner ./internal/authoring/adapter/postgres -timeout=60s
go vet -mod=vendor ./internal/organizing/application ./internal/organizing/adapter/postgres ./internal/organizing/adapter/owner ./internal/authoring/adapter/postgres
go build -mod=vendor ./internal/organizing/application ./internal/organizing/adapter/postgres ./internal/organizing/adapter/owner ./internal/authoring/adapter/postgres
make persistence-check
```

以上均 PASS。来源新测试覆盖 Artifact owner/hash/长度/原始字节漂移、原始 span 范围、parser evidence 与完整 Artifact 范围、未知 evidence kind、16 KiB 边界及 UTF-8 半字符，超限或失真不返回替代证据。数据访问门禁在该检查时扫描 30 个 owner、1900 个 Go 文件。

已使用 `go-review` 按接口/数据/并发边界自检，并修正：exact replay 在 Authoring reconciliation 之前恢复；scoped receipt 避免 retry 持锁时新借连接；旧待审候选与 Approval 竞争交由 Authoring/Change Control 同 scope 原语；来源/alias 初始闭合后禁止事后追加投影外 child；超大 parser excerpt 不先整体传入进程。

## 实库命令与当前结果

```sh
go test -mod=vendor -race -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisPostgreSQLCandidateLifecycle$' -count=1 -timeout=60s -v
```

最终 00095 版本 PASS（14.298s，含 race），全部 9 个子项通过。本组使用真实 PostgreSQL/UoW/Authoring/Change Control service/repository/source fence，覆盖：创建候选/真实 Proposal、未批准拒绝 published snapshot、来源打开与 Workspace 隔离、幂等冲突、补充来源/退役旧 Proposal、历史不可变、摘要/history cursor、NO_CHANGE 零版本/预约、来源删除后 exact replay、semantic/source/reservation 失败原子回滚、提交响应丢失恢复、Approval/退役竞争、生成 Markdown 回流排除。新增子项还验证 scoped receipt 的事务内可见与作用域寿命、缺少 receipt 拒绝整体提交、历史来源/alias 成员约束、超长 parser 证据失败。

刷新 checksum 后的首轮发现超长 parser 测试造数与基础 raw span 复用了同一范围，违反既有 `uq_source_span_range`；已改为同一 Artifact 内另一合法范围，再完成上述最终 race 验证。未修改生产逻辑或 00095。

```sh
go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres -run '^TestGeneratedAuthoringPostgreSQL(PublicationRetirementAtomic|RetirementWinsApprovalRace|ApprovalWinsRetirementRace)$' -count=1 -timeout=60s -v
```

PASS（21.364s），隔离 Schema 停在 00094。覆盖真实 repository 的安全退役原子性和确定性两个竞争顺序。

```sh
go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres -run '^TestGeneratedAuthoringPostgreSQL(PublicationMissingParent|PublishedBaselineAndHistoricalReplay)$' -count=1 -timeout=60s -v
go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres -run '^TestGeneratedAuthoringPostgreSQL(AtomicReplayAndScope|OriginAndMutationGuards)$' -count=1 -timeout=60s -v
go test -mod=vendor -tags=integration ./internal/ingestion/adapter/postgres -run '^TestGORMSourceReadyTransactions$' -count=1 -timeout=60s
```

Authoring 两组分别 PASS（15.619s、15.826s），覆盖缺失 parent、published baseline/历史幂等恢复、scope 生命周期、取消/超时以及 origin/document/article/parent/Workspace 不可变约束。Source-ready 最终 PASS（13.322s），使用默认生产全量迁移到最新 00099，验证同事务追加/失败回滚、重解析幂等、精确 publish binding 与无效/隔离来源不通知。

Source-ready 的上一轮曾在 migration 阶段失败（6.770s）；主会话定位为 00098 contract SQL CASE 语法错误，由对应 owner 修复并刷新 checksum 后完成上述标准命令复跑。

此前曾受共享 checksum 未同步阻断；`MigrateAtlasToVersion(94/95)` 同样会先验证整个 Atlas 目录。主会话统一刷新并确认 `make atlas-migrate-validate` PASS 后才开始本轮实库，未把旧的迁移启动失败计为业务行为通过。

曾将全部 `TestGeneratedAuthoringPostgreSQL` 放在一个 `-timeout=60s` 进程执行，累计容器启动超过 60s；超时点在最后测试的容器等待，后拆成以上小组，不增加单组时间上限。

## 真实 Git 发布组合验证

`TestSynthesisPostgreSQLRealGitPublication` 最终 PASS（17.398s，含 race）：

```sh
go test -mod=vendor -race -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisPostgreSQLRealGitPublication$' -count=1 -timeout=60s -v
```

采用现有 Safe Writeback smoke 的 direct-node 夹具方式，只预置固定 running Workflow/lease；Proposal 审批、双授权、原子 Begin、工具审计、LocalFS、Git 操作锁、真实 Git commit、WritebackService、Authoring PublicationFinalizer 均使用真实 owner。覆盖首次含 FACT/CONFLICT/GAP 的候选发布、未批准无文件/commit、重复执行不增 commit、ADD_SUPPORT 后续替换、历史/稳定 item 不变、published pointer 与精确文件字节/Git blob/Proposal commit/快照逐项一致，以及批准后人工修改导致 needs_revision 且保留文件/旧 published snapshot。

修正 parser 测试造数后，`go vet -mod=vendor -tags=integration ./internal/organizing/adapter/postgres`、两个 W2 integration 文件的 `gofmt -l`、空白及冲突标记检查均 PASS；重新计算的 00095 SHA-256 与上述冻结值一致。Git helper 清除继承的 `GIT_*` 仓库/index 环境并隔离全局配置，所有文件与 Git commit 均限制在测试临时仓库。

为 W4 跨层验证提供 `newSynthesisGitFixtureAtVersion(t,97)`，默认 W2 fixture 仍停在 95。W4 独占新的 `synthesis_note_interview_integration_test.go`，可在同包复用真实发布后的 `f.service.ReadPublishedSynthesisNote`，不复制 published SQL fixture。

## 验证边界与后续组合要求

- 提交恢复还有独立的跨 owner 证据：W3 执行的 `TestSynthesisExplicitPublicationRecoveryKeepsCommittedProposal` 已在实库 race 下 PASS（15.62s）。它让真实 Authoring 成功创建 Proposal 后丢失响应，移除 Artifact 可读数据并禁用模型，再显式 Retry 新 Workflow；恢复相同 Proposal/Revision，保留一份 apply receipt/Proposal 且不增加原有两次模型执行。该结果由 W3 的 `synthesis-runtime-verification.md` 记录，补充 W2 在候选事务提交后、首次 Publication 调用前丢失响应的场景。
- 此 W2 fixture 的 semantic validator 是明确测试替身；它不证明真实 Provider 请求/语义支持质量。Runtime 的 production store 提供同 scope `VerifyValidatedSynthesisGenerationScoped`，真实 runtime journal/模型接线由 W3/W6 的独立组合证据验收。
- 基础 CandidateLifecycle 组的 Publication 创建使用真实 Authoring/Change Control service/repository；目标文件预检和 Git snapshot inspector 在该基础组为测试 seam。RealGitPublication 组已用真实 Git 文件写回与 published snapshot 证明补齐批准后的正向验证。既有 Authoring `PublishedBaselineAndHistoricalReplay` 使用 `seedAuthoringProposalCommit` 的 SQL fixture，不能代替此真实 Git 链路。
- Source fixture 注入 managed Artifact bytes reader；production 接线必须复用 `retrievalworkspace.NewReader(workspaceRepo, files)`。UI 只能展示记录的 availability，不以 canonical chunk 或当前版本替换失效历史证据。
- W2 不运行 commit/push/部署，不修改公共 API/Web/spec、其他迁移或 `atlas/schema.sql`；整体交付与用户授权的后续动作由主会话统一核对。
