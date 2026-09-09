# W3 模型执行接口

公共持久化合同与语义 receipt 的事实源为 `internal/organizing/application/synthesis_model_execution.go`；状态校验在同目录的 `synthesis_model_execution_validation.go`。Workflow 和 PostgreSQL adapter 依赖 application 合同，不依赖具体模型 adapter。

模型实现位于 `internal/organizing/adapter/agent/synthesis*.go`，包名 `agent`；构造依赖定义在 `synthesis_contract.go`。

## 构造与执行

生产先调用 `RegisterSynthesisRuntimeCatalog(catalog)` 注册两个精确 Prompt/Schema，再冻结 Catalog；调用 `NewSynthesisModel(SynthesisModelDependencies)`。Model、Scheduler、Catalog、ModelRunRepository、Store、ProfileRef、IDs、Clock 和 Budget 由 Composition 注入。Scheduler 复用真实 Eino fixed structured graph；零值 Budget 采用既有 `DefaultRunBudget()`。未配置模型通过 `NewUnavailableSynthesisModel()` 返回 `SYNTHESIS_MODEL_CAPABILITY_UNAVAILABLE`，不会返回空成功。

Workflow 使用以下显式入口：

```go
GenerateSynthesisForExecution(context.Context, workflowapp.ExecutionContext,
    organizingapp.SynthesisGenerationInput) (organizingapp.SynthesisGenerationResult, error)
ValidateSynthesisSemanticsForExecution(context.Context, workflowapp.ExecutionContext,
    organizingapp.SynthesisGenerationInput, organizingapp.SynthesisGenerationResult,
) (organizingapp.SynthesisSemanticReceipt, error)
```

生成节点要求 ExecutionContext 的 Workspace/Run/Node/Attempt 与 input 匹配。语义节点保留原生成 input，要求同 Workspace/Run、独立 NodeRun/NodeAttempt，不能把 input 改成语义节点身份。现有 Workflow `openGeneration` 使用持久 generation 的 NodeRunID/NodeAttemptID 恢复 input。

同时实现原 `SynthesisGenerator` / `SynthesisSemanticValidator` 端口；调用它们时先用 `WithSynthesisExecution(ctx, execution)` 绑定可信执行，语义 receipt 可由 `ValidateSynthesisSemanticsWithReceipt` 取得。

语义 receipt 保存独立 ModelRunID、原始 RequestHash、GenerationModelRunID/OutputHash、语义输出 OutputHash、CheckCount、Accepted。否定结论仍是已成功取得的模型结果（READY + ModelRun SUCCEEDED）；返回 `SYNTHESIS_SEMANTIC_REJECTED` 阻止应用，恢复不重复调用模型。

## 持久化与重放

`SynthesisModelStore` 使用 `LookupReady / Prepare / BindModelRun / Complete / Fail`，确切签名见 application 类型文件。`internal/organizing/adapter/synthesispostgres` 实现同池 PostgreSQL adapter；`Complete` 必须在一个事务内保存 Provider 原始 accepted bytes、已绑定的 generation（含首次服务器分配的 Item ID）或 semantic receipt，并用 scoped ModelRun finalizer 完成 ModelRun。恢复读取该已绑定结果，不能重新分配 Item ID。

`00096` 的 `output_document` 使用 bytea 保留原始空白与 key 顺序；generation 和 semantic receipt 分别使用 JSONB。`SynthesisGenerationResult` 保留当前默认 Go JSON 键 `ModelRunID/RequestHash/OutputHash/Notes`，不可只在单端添加 snake_case tags；semantic receipt 已有明确 snake_case tags。

每个 step 绑定 Processing/Workflow/Node/Attempt、Stage、RequestHash、InputRequestHash 和可空 GenerationOutputHash；RequestHash 包含可信输入身份、来源元组、冻结 candidate 和实际 Provider input hash，不依赖 Provider 回显。READY 按同 Node/Stage/hash 跨 transport attempt 重放，保留原始 ModelSettingsRevision、ModelRun、Attempt 和 Item ID。

同一 Attempt 在尚无 ModelCall 时可以继续创建或绑定原 ModelRun；已有调用、非终态调用或无法证明的提交不允许再次付费。Provider 完成后业务提交响应丢失，先按完整 binding 回查 READY；不能精确证明时返回 `SYNTHESIS_MODEL_FINALIZATION_UNKNOWN` / manual recovery。取消与自定义 cause 保持 `errors.Is`，已有 UNKNOWN 分类不被取消覆盖。

恢复时模型 adapter 重绑 raw output 并核对完整 generation 或 semantic receipt，同时验证成功 ModelRun、1–3 次连续 INITIAL/REPAIR/REDUCED ModelCall、原始 ResponseHash 与 ResponseBytes。语义调用开始前还须读回并验证真实已持久 generation，不能接受调用者自制或篡改的结果。

## Provider 与语义边界

生成 Prompt `synthesis-delta/v1`、Schema `agent.synthesis-delta/v1`；语义 Prompt `synthesis-semantic-review/v1`、Schema `agent.synthesis-semantic-review/v1`。三个 StructuredRunner phase 使用相同严格 Schema，REDUCED 只能压缩表达，不能删掉验证或伪造空成功。

Schema 复用 `$defs/$ref` 保持在现有 runtime 的结构深度限制内；没有放宽共享 Schema 深度或 decoder。Provider 文档严格拒绝缺失、未知、重复、大小写变体、尾随内容、错误 nullable 与 operation 联合形状。

Provider 只得到本次 N001/I001/S001 短标签、已有条目文本与精确原始片段，不含可信 UUID/hash/path/permission。未打开的历史来源只显示数量，不能作为新来源引用。服务端恢复完整 Source/Version/Projection/Span/hash 元组，并分配新增 Item ID。

五种封闭操作为 `ADD_FACT / ADD_CONFLICT / ADD_GAP / ADD_SUPPORT / RESOLVE_GAP`。既有笔记只返回 `note` 与 `operations`；新增笔记还必须有规范 `topic_key/title/aliases`。`ADD_SUPPORT.alternative` 是必须显式提供的 null 或 0 起始整数。所有目标均来自该笔记的既有条目。

独立语义请求逐项检查事实、冲突各方、新增支持、缺口上下文及解决结论，并额外检查冲突关系和中立主题。每项、每个精确引用均须返回对应顺序的 `SUPPORTED / UNSUPPORTED / UNCERTAIN`；只在所有必要 verdict 为 SUPPORTED 时由服务端派生 Accepted。空 delta 仍发起 NO_CHANGE 检查，提供已有笔记核对是否遗漏知识。没有用 `verified` 布尔或单一整体自证替代模型判断。

每次最多 512 个语义检查，每项最多 32 个来源；accepted output 上限 256 KiB，完整请求上限为共享 `MaxStructuredInputBytes`（512 KiB）。生成结果落为 READY 前还检查能否装入独立语义请求；源片段不被截断成无法定位的新内容。

W3 的成功类型为 `synthesis_delta`、`synthesis_semantic_review`；原始来源由 ParseProjection/Span/hash 绑定，不伪造 Retrieval Index。此次共享 Agent allowlist 另按 W4 接口增加 `agent.synthesis-note-interview-plan` / `synthesis_note_interview_plan`。Go 合同由模型代理增量修改，迁移 allowlist 与真实恢复门禁由 runtime / interview owner 同步；不放宽其他 Schema。

## 当前验证证据

2026-09-09，以下命令实际执行通过：

| 命令 | 结果 |
| --- | --- |
| `go test -mod=vendor ./internal/organizing/adapter/agent ./internal/organizing/application ./internal/agent/domain -count=1 -timeout=60s` | PASS，依次 0.892s / 0.547s / 0.555s |
| `go test -race -mod=vendor ./internal/organizing/adapter/agent ./internal/organizing/application ./internal/agent/domain -count=1 -timeout=60s` | PASS，依次 1.737s / 1.370s / 1.514s |
| `go vet -mod=vendor ./internal/organizing/adapter/agent ./internal/organizing/application ./internal/agent/domain` | PASS |
| `go build -mod=vendor ./internal/organizing/adapter/agent ./internal/organizing/application ./internal/agent/domain` | PASS |
| `go build -mod=vendor ./internal/organizing/workflow ./internal/organizing/adapter/synthesispostgres` | PASS，验证实际调用者与 Store 合同可编译 |
| `gofmt -l` 对本工作包 Go 文件 | PASS，无输出 |
| `git diff --check` | PASS，无输出 |
| 逐文件 `git diff --no-index --check /dev/null <owned path>` 与共享文件定向检查 | PASS，14 个新增文件与 2 个共享 Agent 文件无空白错误 |

测试使用真实 StructuredRunner、RecordingChatModel 和计数包装的真实 Eino scheduler，Provider 为确定性测试替身，Store 为有锁内存 fixture。覆盖严格联合与引用、五种增量的语义计划、每个来源 verdict、独立执行身份、篡改拒绝、语义否定持久重放、提交响应丢失、UNKNOWN、三个修复阶段、请求/响应/token/deadline、取消 cause、NO_CHANGE、32 并发和输入漂移。

已按 go-review 自检错误分类、并发状态、短标签来源隔离、精确 raw bytes/hash、Item ID 重放及 ModelRun/ModelCall 审计关系。此次修复 Schema 内联深度超限，使用局部 `$defs/$ref`，没有修改共享上限。

本工作包没有执行实库事务/Worker/River、真实 Provider 内容质量、审批文件/Git 写回或浏览器闭环验收。上述证据只交付 W3 的模型生成和独立语义校验子范围，不代表 W3 整体、TODO4 或整个项目完成；其余验收由对应 owner 与主会话补齐。没有提交、推送、部署或运行 M11。
