# TODO2 v2 动态只读工具验证

2026-09-09，owner：dynamic_tools。本文只记录 Tools 及其 Agent 同池参与者的实现和直接证据，不代表整个 TODO2、M11 或部署完成。精确接口与版本见 [工具接口](todo2-v2-tools-interface.md)。

## 实现范围

- 追加 `ReadGitStatus@3`、`SearchKnowledge@3`、`ReadSource@4`、`ValidateCitation@4`，只服务 `workspace-analysis@2`；旧 tuple、Definition Hash、receipt decoder 与 SQL guard 保留。
- 动态调用必须经过已持久 Decision、Workflow lease/fence、Operation、预算授权。通用 `ExecutionService.Execute` 拒绝 v2 工具，避免绕过动态预算。
- Search receipt 保留局部 E1–E5；成功结算与 Run 全局 E1–E32 追加在同一事务中完成。全局引用绑定 exact Search operation/receipt/hash/local ref/immutable tuple。
- Read 只打开已绑定的不可变 Source；模型输出不包含私有 ID、路径和 content hash。Git 的模型输出只保留 clean 与计数。
- Citation 校验必须有已成功 Read 的完整证据链，使用真实 EvidenceReference 和 KnowledgeEligibility；不同 historical index 分批打开。循环校验的 candidate ID/hash 为 null，正式校验绑定服务端候选及其 hash。
- 预算/截止时间拒绝持久化真实 PENDING；提交响应丢失恢复只认可已经存在的精确 operation，不补建拒绝证明。CanonicalRequest/Arguments 的 JSON、fmt 和 slog 输出均脱敏。
- `00099` 扩展严格 receipt、依赖和全局引用 guard；缺少 v2 Run 或 private binding 时显式拒绝，避免 SQL NULL 漏校验。

主要变更位于 `internal/tools/{domain,application,adapter}`、Agent 的 scoped tool/evidence participant 和 `atlas/migrations/00099_workspace_analysis_dynamic_tools.sql`。未修改 `atlas.sum`、声明 Schema、OpenAPI/Web、生产 main 或其他 owner 的迁移。

## 单元、并发与静态证据

| 验证 | 实际结论 |
| --- | --- |
| Tools catalog/domain/real executor tests | PASS；包含旧 tuple/hash 回归、严格 JSON、重复键、完整 tuple/hash 漂移、跨 Workspace/Workflow、E1–E32、跨 index Citation 和模型安全投影 |
| Tools application race | PASS；新增通用 Execute 预算绕过拒绝后已单独重跑 |
| Tools 其他包、Agent domain/postgres race | 组合命令中这些包 PASS；不是把整个组合命令记为 PASS |
| Agent application synthesis/global-ref/replay | 组合命令原有一项 fixture 失败；runtime 修复后，`TestWorkspaceAnalysisV2SynthesisPreservesGlobalRefsAndReplays` 定向 race PASS（1.412s） |
| `go vet -mod=vendor ./internal/tools/... ./internal/agent/domain ./internal/agent/application ./internal/agent/adapter/postgres` | PASS；最新通用 Execute guard 后已执行 |
| `go run -mod=vendor ./cmd/persistencecheck` | PASS；执行时覆盖 30 owners / 1939 Go files |
| integration build | PASS；新增工具集成 helper 已编译并实际运行 |
| 本 owner 已追踪文件 `git diff --check` | PASS；新增 Go 文件已 gofmt；新 SQL、集成测试及两份接口/验证文档的单独空白检查 PASS |

组合 race 命令为：

```sh
go test -race -mod=vendor ./internal/tools/... ./internal/agent/domain ./internal/agent/application ./internal/agent/adapter/postgres -count=1 -timeout=60s
```

其中 application 的单项失败不能当成整个命令通过。后续独立执行且通过：

```sh
go test -race -mod=vendor ./internal/tools/application -count=1 -timeout=60s
go test -race -mod=vendor ./internal/agent/application -run '^TestWorkspaceAnalysisV2SynthesisPreservesGlobalRefsAndReplays$' -count=1 -timeout=60s
```

## 隔离 PostgreSQL 证据

默认测试入口位于 `internal/agent/adapter/postgres/workspace_analysis_dynamic_tools_integration_test.go`。复用 foundation 的真实 v2 Run/Decision fixture；Search 排名结果和 Artifact bytes 是明确的确定性 fake，以下部分是真实生产实现：

- PostgreSQL、GORM 单物理池和事务、Workflow policy/fence/recovery。
- Model Decision 授权/结算、Tool Operation/Call/receipt、预算、Events 与 Audit。
- Source/Version/Artifact/Projection/Span/Chunk/Index manifest 的持久绑定及 SQL guard。
- EvidenceReference 的 immutable tuple 打开，以及 KnowledgeEligibility 的正式 Claim 绑定检查。

新版三个正式测试函数已在完整 SQL 和临时 checksum overlay 下同批通过（29.764s），包含最新有资格/无资格 Claim fixture；不只依赖早期 smoke helper 的结果。

| 场景 | 实际证据 |
| --- | --- |
| Search → Read → Search → Read → Citation | 正式测试函数 PASS；第二次 Search 分配 E6–E10，E7 绑定第二个 Search 的局部 E2；Citation 对已确认 Claim 的 E1 返回 OK，对没有正式知识绑定的 E7 返回 EVIDENCE_INELIGIBLE；models=5/tools=5/reads=2/aliases=10，reserved=0 |
| 成功工具重放与替换 Attempt | 同一 Read 返回相同 Call/Receipt，不重复 executor；更换 Attempt/fence 后重放仍不重复执行；旧 fence 与真实 Coordinator cancellation 拒绝执行 |
| 作用域与 SQL 约束 | 跨 Workspace authority 读取被拒；直接 SQL 插入伪造 content hash 返回 23514，错误确认为 dynamic evidence alias guard |
| 引用容量耗尽 | 正式测试函数 PASS；七次 Search 的 limit 为 5/5/5/5/5/5/2，追加到 E32；第八个请求产生真实 PENDING BudgetExhausted，重放保持同一 OperationID，无第八次 Tool executor；models=8/tools=7/aliases=32/pending=1/reserved=0；历史 Search 的容量仍按当时操作前缀计算 |
| Source 读取预算耗尽 | 正式测试函数 PASS；第九次 Read 产生同一 PENDING 拒绝，无第九次读取；models=11/tools=10/reads=8/aliases=10/pending=1/reserved=0；已有八条全局引用及其两组 Search receipts 仍可汇总，包含同物理 tuple 的不同全局别名 |
| v1 participant/refusal/receipt parity | 四条既有正式测试函数 PASS（34.493s）：拒绝事实原子且不占预算、拒绝提交响应丢失恢复、工具同池授权/结算/receipt/authority 重放、Events 失败全 owner 回滚；构建使用临时 checksum overlay，不关闭生产 SQL guard |

开发 smoke 使用 Atlas MemDir 完整复制并单独计算迁移 checksum，真实执行至 `00099`，不修改仓库的 `atlas.sum`。首轮 Search 曾因本测试 Source 与 Source Version 路径不一致而失败，已修复 fixture；生产 guard 保持。新增 Citation 首次也正确拒绝未绑定正式知识的 Source，现 fixture 明确覆盖有资格与无资格两种来源。

正式测试函数使用的临时 overlay 仅替换本次构建嵌入的 `atlas/migrations/atlas.sum`，内容由同一 SQL 快照按 Atlas 正式算法计算；原仓库文件不变，测试仍校验 checksum 并真实迁移。两个包分别执行并通过：

```sh
go test -overlay <temporary-checksum-overlay.json> -mod=vendor -tags=integration ./internal/agent/adapter/postgres -run '^TestWorkspaceAnalysisDynamicTools(SearchReadAliasReplay|EvidenceCapacityDenial|SourceReadBudgetDenial)Integration$' -timeout=60s -count=1
go test -overlay <temporary-checksum-overlay.json> -mod=vendor -tags=integration ./internal/tools/adapter/postgres -run '^TestWorkspaceAnalysisTool(RefusalAudit.*|OperationParticipantAndAuthorityParityIntegration|OperationEventFailureRollsBackAllOwnersIntegration)$' -timeout=60s -count=1
```

## 待整合验证与边界

- 初次交付时 foundation 的 `00098` 尚在整合，未使用 overlay 的默认入口曾停在 migration fixture 初始化。主会话整合后，本页下方记录了正式迁移、无 overlay 的 Search/Read/alias/replay 实库复验；另外两条预算耗尽测试仍以此前 overlay 证据为准，最终迁移汇总由主会话维护。
- 旧 SourceRead count 接口仅由 v1 synthesis 调用；v2 使用独立 successful-read 查询，PENDING 第九次 Read 不污染成功证据集合。
- 完整 Eino/River 编排、候选/忠实度审核/答案发布、公共时间线、真实 Provider 质量、浏览器与 Docker 部署由对应 owner 和主会话整合。本文的确定性 fixture 不证明真实 Provider 质量，也不代表 M11 完成。
- 本 owner 未执行 commit、push 或部署。

## 2026-09-09：已脱敏 canonical receipt 重验修复

第三轮真实 Compose 在两个 Decision、Git@3 和 Search@3 已成功持久化后，返回 `TOOL_RESULT_REPLAY_UNAVAILABLE`，尚未生成 Candidate。Search snippet 中的 `Evidence token: durable-rag-<fixture marker>` 命中 Foundation 敏感赋值规则，首次处理将该字段替换为 `[REDACTED]`，保存 `redacted_field_count=1`；canonical receipt 回读再次运行 sanitizer 得到计数 0，完整摘要比较误拒绝合法回执。首次 Finalize 后和后续重放共用此重验路径，因此两者都会失败。

生产修复仅位于 `internal/tools/application/execution.go` 的 `validatePersistedReceiptResult`：

- 保留原始摘要中的脱敏次数作为首次调用的持久事实，要求字段存在、非 null、整数、非负且不大于已验证输出字节数；不从已脱敏文本反推原次数。
- 仍执行 Domain 的精确 Call/Definition/receipt/private binding 校验，以及输出 Schema、大小和敏感内容检查；重新处理的输出必须保持原 hash/bytes。
- 依据已验证输出和原次数重建摘要，精确核对其余字段；首次 Finalize 的 `sameCanonicalFinalizationCall` 继续拒绝原次数或其他 Call 事实漂移。
- 不改变旧 `ResultReceiptLoader` 分支、sanitizer、Definition/Schema/hash、SQL/checksum 或 Compose fixture。

永久回归文件：`internal/tools/application/canonical_receipt_redaction_test.go`。

| 验证 | 实际证据 |
| --- | --- |
| 原触发 RED | 改生产代码前，用原 `Approved recovery requires durable replay without duplicate provider work. Evidence token: durable-rag-a7c0c6a6e6b9` 文本走 `ExecuteWorkspaceAnalysisTool`；v1/v2 首次返回和已持久 receipt 重放均稳定失败，错误为 `TOOL_RESULT_REPLAY_UNAVAILABLE` |
| 修复后 GREEN | 同一测试的 6 个 v1/v2 正例通过：原敏感文本次数保留为 1；普通历史文本和原文就是 `[REDACTED]` 的次数保持 0。首调/重放返回同一 canonical 输出、Call、Operation、receipt，不重复 executor 或 finalization |
| 36 个 v1/v2 负例 | 拒绝缺失/null/负数/字符串/小数/超范围/溢出的次数、摘要 hash/bytes/schema/额外字段漂移、正文篡改、即使同步改写 hash/bytes 仍未脱敏的正文、私有绑定与 Schema/Workspace/Call 漂移；均无输出、不重执行 |
| 首次 finalization 与旧 loader | 持久层把首次次数从 1 改为 0 仍被拒绝；旧 raw loader 重放原敏感输入成功，无法复现原摘要时继续拒绝 |
| Tools 全包 race | `go test -mod=vendor -race ./internal/tools/... -count=1 -timeout=60s` PASS，含上述新回归与原有安全/历史版本测试 |
| Tools 全包 vet | `go vet -mod=vendor ./internal/tools/...` PASS |
| 正式迁移实库重放 | 下列无 overlay 命令 PASS（12.008s）；使用真实 PostgreSQL/GORM/冻结 catalog，复验 Search→Read→Search→Read→Citation、固定预算与别名、替换 Attempt、旧 fence、取消和作用域拒绝；本条沿用普通文本 fixture，敏感文本 RED→GREEN 来自前述 Application 回归 |
| Go 自检与空白 | `go-review` 定向核对数据来源、序列化、只读重验和失败路径；新增文件已 gofmt，相关 `git diff --check` PASS |

```sh
go test -mod=vendor -tags=integration ./internal/agent/adapter/postgres \
  -run '^TestWorkspaceAnalysisDynamicToolsSearchReadAliasReplayIntegration$' \
  -count=1 -timeout=60s
```

已通知 runtime 继续第四轮 Compose。完整动态循环、候选/Review/发布与页面是否通过，以 runtime 后续实际结果为准；本修复不声明整栈或部署完成。
