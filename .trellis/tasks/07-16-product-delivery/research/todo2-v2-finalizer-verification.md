# TODO2 v2 Finalizer、终态 Hook 与取消审计验证

日期：2026-09-09。Owner：`dynamic_contract`。这是主会话追加的 Conversation Finalizer 范围，接续 [公共 Answer / Timeline 合同记录](todo2-dynamic-contract-verification.md)。只记录本范围的实现和实际检查，不作为 TODO2 整体交付、真实 Provider 质量或部署成功的证明。

## 实现与兼容边界

- Finalizer 从持久 Run / Workflow Definition 验证版本、Policy 与 Graph Hash。调用方传入版本不能覆盖持久版本；旧 Lookup 的零值只兼容 v1。
- v2 成功使用独立的公开 `v2` builder，私有 Candidate 使用 schema `2`。Git 没有调用时公开 `git_status: null`，调用过则必须引用最后一次成功 Git receipt，不伪造默认 Git 状态。
- 成功发布必须有完成的决策链、Source 证据、独立 `validate_citations` 的 ValidateCitation@4 与独立 Review。loop Citation receipt 不能替代最终校验；Candidate / receipt / Review hash 漂移被拒绝。
- 同一不可变 Citation tuple 经不同 Search 得到 E1 / E6 等别名时，正文及全部私有短引用保留，公开 Citations 与 Proposal citation IDs 按首次引用去重；同 ID 对应不同 tuple、同 tuple 对应不同 ID 均拒绝。
- v2 无证据拒答在 `synthesize_answer` 引用前置 finish decision。Citation invalid 必须证明独立最终校验的精确绑定，不能用缺少有效校验来代替拒答证据。拒答与失败正文仍使用既有 `v1` Schema，Workflow publication output 按持久 Definition 使用 1 / 2。
- Budget proof 核对真实准入额度：第 13 个决策、为 synthesis / review 保留模型与 Token、为独立 Citation 保留 Tool、8 次 Source read 和 E1–E32 namespace。终态之前只关闭完整成功 prefix 的 RUNNING decision ModelRun，在同一事务写齐 proof / Run / Answer / event / audit 后再强制验证 deferred constraints，验证通过才提交。
- Terminal / cancellation audit Hook 识别 v1 / v2 精确节点类型，并验证持久版本。v2 真正取消或 Runtime failure 可保留最后一个没有 Call / reservation 的 PENDING journal entry；不能掩盖 STARTED / FAILED / UNKNOWN。无 Attempt 的 v2 Runtime failure 另须证明 node attempt 为 0 且不存在任何 Attempt。
- v1 的冻结 Definition、公开 bytes、完整原 SQL 约束及 `00091` Runtime-failure 的互斥触发器路由保留。

## 文件

新增：

- `internal/conversation/adapter/postgres/gorm_workspace_analysis_finalizer_v2.go`
- `internal/conversation/adapter/postgres/workspace_analysis_finalizer_v2_contract.go`
- `internal/conversation/adapter/postgres/workspace_analysis_finalizer_v2_test.go`
- `internal/conversation/application/workspace_analysis_finalizer_v2_test.go`
- [SQL 交付片段](todo2-v2-finalizer-sql.sql)

修改：`application/workspace_analysis_finalizer.go`，Adapter 的 `gorm_workspace_analysis_finalizer.go`、`workspace_analysis_finalizer_contract.go`、`gorm_workspace_analysis_terminal_hook.go`、`workspace_analysis_terminal_hook_contract.go`、`gorm_workspace_analysis_control_audit_hook.go`、`workspace_analysis_control_audit_hook_contract.go`，以及 `gorm_models.go` 中可空 Git proof 字段。

SQL 片段由 `dynamic_foundation` 并入 `atlas/migrations/00098_workspace_analysis_dynamic.sql`；已检查片段在该迁移中逐字匹配且只出现一次。checksum 与声明 Schema 由主会话统一管理。本代理未直接编辑迁移或 checksum。

## Go 检查

在本次 Finalizer / Hook Go 实现完成后执行以下完整四包检查，均退出 0：

```bash
go test -mod=vendor -race \
  ./internal/conversation/domain \
  ./internal/conversation/application \
  ./internal/conversation/adapter/postgres \
  ./internal/conversation/workflow \
  -count=1 -timeout=90s

go vet -mod=vendor \
  ./internal/conversation/domain \
  ./internal/conversation/application \
  ./internal/conversation/adapter/postgres \
  ./internal/conversation/workflow
```

race 四包结果依次为 1.454s / 1.434s / 1.658s / 1.412s。先前的 `^TestWorkspaceAnalysisV2` 与 Finalizer 定向测试同样通过。这些命令没有 `integration` tag，不计作实库验证。

新增单测覆盖可空 Git、版本漂移、Citation 别名、loop receipt 不能替代最终校验、合法但 invalid 的最终 Citation、Candidate hash 漂移、真实预算保留规则、v2 no-attempt 终态事件合同与持久版本审计。按 `go-review` 检查了同池事务、nullable GORM 映射、精确重放和数据库约束；owned Go 的 `gofmt -l` 无输出、tracked files 的 `git diff --check` 通过，新增 SQL / 验证记录无行尾空白且保留结尾换行。

## 隔离 PostgreSQL：迁移红绿与 v1 回归

第一次 integration 批次没有进入业务：所有 fixture 在迁移 `00098` 的 `guard_answer_mutation` 初始化时遇到 `42601 syntax error at end of input`。原因是 PL/pgSQL IF 比较中的裸 CASE。已修复 Answer schema 比较与 preoperation node / kind 比较三处括号，并由 foundation 同步迁移、主会话刷新 checksum。该失败不计作业务回归失败，也不计 PASS。

修复后，foundation 的标准 no-evidence Finalizer probe 通过（10.685s），本代理使用默认 Testcontainers `pgvector/pgvector:pg16`、标准 `00001`–`00099` 迁移与当前 checksum 完成下面 8 项。未使用临时 overlay，未关闭数据库约束。

公共命令前缀为 `go test -mod=vendor -tags=integration ./internal/conversation/adapter/postgres`；每组追加下表 `-run`、`-failfast -count=1 -timeout=60s -v`，避免把全部容器初始化塞进一个 60 秒批次。

| 组 | `-run` | 结果 |
| --- | --- | --- |
| 1 | `^TestWorkspaceAnalysisFinalizerClarificationAcceptsPlannerResultWithoutSubjectCandidateAndReplays$` | PASS，12.563s |
| 2 | `^TestWorkspaceAnalysis(FinalizerCancellationCommitsBundleAndRecoversExactResponseLoss\|CancellationTerminalHookDirectRuntimeCancelClosesPublicationAndReplays\|RuntimeFailureHookClosesPendingAnswerWithoutFabricatingCall)$` | 3 项 PASS，38.609s |
| 3 | `^TestWorkspaceAnalysis(CancellationTerminalHookAuditFailureRollsBackRuntimeAndPublication\|CancellationControlAuditIsSingleSafeAndRollbackAtomic)$` | 2 项 PASS，39.022s |
| 4 | `^TestWorkspaceAnalysisCancellationTerminalHook(CheckpointCancelClosesAttemptBoundPublication\|AcceptsExactActiveLeasePublication)$` | 2 项 PASS，28.206s |

覆盖 clarification、取消 bundle、提交响应丢失精确重放、直接 Runtime cancel、Runtime failure、审计失败原子回滚、控制审计安全及唯一性、checkpoint 与精确 active lease。容器均由 test factory 清理，命令均退出 0。

上述四组前后迁移文件 Hash 一致：

```text
00098  3a461f1a3552373f5403953f88c924389728592062ce95a6f9fa54d22f1e5f85
00099  9a7f86856c1212f7c2655fc2e5ae21af9c27fcb3a649b0b7f4f8b0fa230fed8d
```

## v2 正式集成结果与验证边界

v2 production Finalizer fixture 由 `dynamic_foundation` 拥有。以下结果已与 [foundation 最终验证记录](todo2-v2-foundation-verification.md) 的“标准 PostgreSQL 入口”及永久测试中的断言核对；复用其实际执行证据，本次文档同步没有重跑矩阵。各批次均使用标准迁移目录、共享 `atlas.sum` 和完整 `00001`–`00099` 约束，没有 checksum overlay 或关闭约束。

| 范围 | foundation 实际结果 |
| --- | --- |
| no-evidence finish → synthesize 节点 refusal、exact replay 与终态 Timeline | PASS，10.685s |
| 无 Git 的完整成功发布，以及第 13 个决策 budget 终态与 ModelRun 关闭 | PASS，同批 25.091s |
| deadline 授权前与真实 PENDING 两条路径，以及 replacement UNKNOWN Finalizer 与全额记账 | PASS，同批 19.036s |
| 成功决策前缀取消、Decision 13 PENDING 后取消、成功前缀 Runtime failure；生产 RuntimeCoordinator、原子终态及 hook replay | PASS，同批 25.599s |
| CitationInvalid、ReviewRejected、ModelFailed，以及 E1/E6 同来源元组的独立读取与公开 Citation 去重 | PASS，同批 37.400s |
| Decision 提交回执丢失恢复、成功前缀 replacement、Search → Read → Search → Read 的 journal / Timeline 顺序 | PASS，同批 32.031s |
| ToolFailed / ToolUnknown；Executor 只执行一次，Tool / Finalizer exact replay、前缀关闭与精确预算 | PASS，同批 21.684s |

CitationInvalid 实际在 `review_publish` 跨节点引用独立最终 ValidateCitation；loop receipt 替代被拒绝，失败引用也不能开始 Review。ReviewRejected 不能发布成功。E1/E6 场景保留 2 次 Source read、8 次模型调用和 6 次工具调用，仅公开 1 条 Citation；Finalizer replay 不重复 Search / Read / Citation 执行。取消场景由 Cancel → Complete checkpoint 同事务关闭 Run、Node、Attempt、Answer 与 proof，公开终态 Timeline 从已持久事实读取。

新增结果对应以下永久测试的定向复验入口；这些命令用于定位上述测试范围，本次只核对入口和证据，没有再次执行：

```bash
go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^TestWorkspaceAnalysisDynamicRuntime(Cancellation|Failure)ClosesDecisionPrefixIntegration$' \
  -count=1 -timeout=60s

go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^TestWorkspaceAnalysisDynamic(CitationRejectionFinalization|ReviewRejectionFinalization|ModelFailureFinalization|PublicationDeduplicatesSourceAliases)Integration$' \
  -count=1 -timeout=60s

go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^TestWorkspaceAnalysisDynamic(DecisionCommitRecovery|DecisionCompletedPrefixReplacement|TimelineReadsJournalOrder)Integration$' \
  -count=1 -timeout=60s

go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^TestWorkspaceAnalysisDynamicToolFailureFinalizationIntegration$' \
  -count=1 -timeout=60s
```

v2 Runtime failure 的 `node attempt = 0` 且不存在任何 Attempt 行这一精确分支，尚未单独执行实库测试。已通过的 Runtime failure 带有成功决策前缀及真实 Attempt；事件合同单测不能替代该数据库分支的验证，也不能据此声称穷尽全部终止时机。

上述结果证明协议、事务和恢复行为；确定性模型 fixture 不代表实际 Provider 的回答质量，也不代表完整 Compose / River、HTTP、页面或部署验证通过。这些整合结论由主会话继续汇总。M11、Git commit / push 和部署未由本代理执行。

## 后续独立复核：canonical receipt 脱敏计数

2026-09-09，主会话追加有界只读 Go / 安全复核。使用 `go-review` 检查 Tools owner 的 `internal/tools/application/execution.go::validatePersistedReceiptResult` 和新增 `canonical_receipt_redaction_test.go`，只追踪其直接相关的首次完成、旧 loader、Domain receipt 验证及 GORM 回执加载路径。未发现此次修复引入的具体缺陷；本代理没有修改 Go / SQL 或重跑测试矩阵。

原始输出触发脱敏后，canonical receipt 仅保留 `[REDACTED]`，无法从该文档重新推导首次脱敏次数。此次修复从已持久的权威 Call 摘要读取原次数，要求字段存在、非 null、可解析为整数、非负且不超过已验证的输出字节数。该值作为首次执行事实保留，不把摘要其余部分直接当作可信输出。

逐项核对后的完整性边界：

- 首次执行仍调用 `validateExecutorResult` 完成 Schema / 大小校验和 Foundation 脱敏，并验证私有绑定。持久化返回后，`sameCanonicalFinalizationCall` 仍比较原始完整摘要，包含原脱敏次数；Repository 把首次次数从 1 改为 0 仍会被拒绝。
- canonical 回读先执行 `domain.ValidateResultReceipt`，核对 Call / Definition / Workspace / Workflow / Node / Attempt / Tool / Schema 身份、时间、canonical bytes、输出 hash / 长度，以及私有绑定的 Schema / hash / bytes / exact 文档关系。这条调用没有被跳过或改为可选。
- canonical 输出仍经 `validateExecutorResult` 重新解码、检查敏感字段并脱敏；之后必须保持原 hash / bytes。即使同时改写存储 hash / bytes，尚未脱敏的正文也不能通过重验并返回。摘要的输出 hash / bytes / Schema 均重新计算，完整 JSON 比较继续拒绝额外或漂移字段；成功结果继续标记 `UntrustedData`，不会公开私有绑定。
- 旧 `ResultReceiptLoader` 不具备 canonical 持久承诺，继续从 raw receipt 重算包含脱敏次数的完整摘要。该分支和 sanitizer 均未因此次修复而放宽。

复用 [Tools owner 的修复与验证记录](todo2-dynamic-tools-verification.md)；本次核对了测试输入、变更注入位置和断言，没有将只读复核写成再次执行：

| 已有验证 | 独立核对 |
| --- | --- |
| 6 个 v1 / v2 正例 | 原敏感文本保留次数 1，普通文本及 literal `[REDACTED]` 保持次数 0；首次 / replay 的输出、Call、Operation、receipt 不变，Executor / finalization 不重复 |
| 36 个漂移负例 | 覆盖计数类型 / 范围、摘要 hash / bytes / Schema / 额外字段、正文、同步改 hash 的未脱敏正文、私有绑定及身份漂移；错误时无输出且不重新执行 |
| 首次 finalization 与旧 loader | 首次返回的计数漂移被拒绝；旧 raw loader 能复现原摘要时成功，改用已脱敏文本而无法复现次数时仍拒绝 |
| Tools 全包 race / vet | owner 的 `go test -mod=vendor -race ./internal/tools/... -count=1 -timeout=60s` 与 `go vet -mod=vendor ./internal/tools/...` 已通过，本代理未重跑 |
| 正式 Schema PostgreSQL 回归 | owner 的 `TestWorkspaceAnalysisDynamicToolsSearchReadAliasReplayIntegration` 已通过（12.008s），没有 checksum overlay |

实库的 12.008s 回归使用普通文本 fixture；原 `Evidence token:` 敏感文本的 RED → GREEN 证据来自 Application 回归，不能写成该敏感输入已完成 PostgreSQL 或 Compose 端到端验证。第四轮 Compose、候选 / Review / 发布及部署结果仍以 runtime 和主会话的实际记录为准。
