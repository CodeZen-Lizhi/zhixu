# 合成笔记 API / 面试 Worker 构造接线

本记录只覆盖 composition 子工作包。主会话后续授权本代理接管 `cmd/api/main.go` 的 TODO4 接线；API 生产入口现已实际接入两个 handler、终态钩子与注册器。随后又授权修复 `cmd/api/workspace_analysis.go` 的模型热更新缺口，并新增独立的 Agent replay helper。`cmd/worker/main.go`、Router/Auth 和其他 owner 文件仍由主会话与各实现 owner 持有。本记录不把构造测试当作实库验收。

## 已实现入口

- `cmd/api/synthesis_components.go`：`newAPISynthesisRuntimeComponents(pool)`、`newAPISynthesisComponents(persistence, dependencies)`、API contract/definition 注册。
- `cmd/api/main.go`：生产工厂预构造/统一 hook 安装、旧签名 wrapper、实际 handler 注入以及 Authoring typed-nil 依赖拒绝。保留主会话已有 source-ready 与 conversation V2 ctor 修改。
- `cmd/worker/synthesis_interview_components.go`：`newWorkerSynthesisInterviewPersistence(pool, modelRuns)`、`newWorkerSynthesisInterviewComponents(persistence, runs, agentComponents)`、Worker executor/definition 注册与独立冻结模型目录。
- 对应 `*_test.go`：无需连接数据库的构造/缺依赖测试、明确模型不可用、不同模型 generation 的目录与 executor 隔离。

API 依赖从一个 `*platformpostgres.Pool` 取得 GORM/UoW、ModelRunRepository、Workflow fence、bindings、Synthesis runtime store、Authoring repository、来源 reader 与候选 store。既有 Authoring Service 通过 `Publications` 注入；没有配置模型、创建第二物理 Pool、后台 scheduler 或新的 River 队列。

## API 主入口的依赖顺序

1. `runAPI` 创建 fallback handler 时增加 `organizinghttp.NewSynthesisHandler(nil, nil, cfg.GraphQueryTimeout)` 与 `interviewhttp.NewNotePreparationHandler(nil)`。这样依赖异常返回明确 unavailable，路由仍存在。
2. `database` 和同池仓库可用后，`runAPI` 在任何 Runtime 构造前创建并保留 factory 的持久组件：

   ```go
   factory, err := newAPIRuntimeRepositoryFactory(database, cfg, modelEnqueueFences...)
   synthesisRuntime = factory.synthesis
   ```

   factory 内调用 `newAPISynthesisRuntimeComponents`，其 terminal 顺序固定为 source-processing store、note preparation repository。两者只用独立读/fence/model-run 端口；此时不持有尚未构造的 Runtime，不调用模型。
3. `factory.newRepository` 统一调用 `withSynthesisRuntimeHooks`，在既有 Artifact / WorkspaceAnalysis terminal 后追加上述两个 owner，保留原 CancellationSafety、Control 和 runtime freshness。`newWorkflowComponentsWithFactory` 回退路径也通过这一入口，没有只给有模型路径安装的分支。生产调用 `newAPIArtifactWorkflowComponentsWithFactory`；旧 `newAPIArtifactWorkflowComponents` 签名保留，创建 factory 后委托给新入口。
4. `registerAPIWorkflowExecutors` 末尾调用 `registerAPISynthesisWorkflowContracts`；`registerAPIWorkflowDefinitions` 末尾调用 `registerAPISynthesisWorkflowDefinitions`。现有 `newWorkflowServiceWithRuntime` 在两个 Freeze 前调用这些入口，同时注册 Synthesis 四节点与 NOTE_REVISION 面试单节点，**不受 `chatEnabled` 条件控制**。调用方不能重复追加注册。
5. `configuredAuthoringService` 构造成功后，独立于旧 `newOrganizingHandler` 成败和 Embedding 状态执行：

   ```go
   synthesis, err := newAPISynthesisComponents(synthesisRuntime, apiSynthesisDependencies{
       Workspaces: workspaceRepository,
       Files: fileScanner,
       Publications: configuredAuthoringService,
       Retirements: changeControlRepository,
       Runtime: workflowRuntime,
       Timeout: cfg.GraphQueryTimeout,
   })
   // 成功后：synthesisHandler = synthesis.handler
   //         synthesisInterviewHandler = synthesis.interviewHandler
   ```

   此 helper 按来源 reader → 同池 Authoring repository/retirer → 候选 store → SynthesisService → 冻结 definition registry → ProcessingService → 两个 HTTP handler 的顺序构造。全部准备完成后才对 preparation repository 调用一次 `BindWorkflowStarter(workflowRuntime)`。调用方不再重复绑定。
6. `app.Dependencies.Synthesis` 与 `.SynthesisInterview` 已注入实际 handler。主会话已有路由与 12 条认证 capability 保持不变。新增的 `newAuthoringService` typed-nil 检查避免 ChangeControl 构造失败后，把接口内的空 service 传入 Synthesis publication。

`newInterviewService` 的 Artifact CommandService 同时接入同池 Authoring repository 和 `artifactauthoring.NewVerifier`，通过 `Documents` 验证 NOTE_REVISION 报告的不可变文档来源；原有 `Evidence` verifier 保持。此处缺失会使已准备好的笔记面试在完成报告时失败，W4 owner 复核后已由本子工作包补齐。

`SynthesisService.RecoverAppliedGeneration` 依赖持久候选 receipt 和既有 Authoring `PublishArticleRevision`，没有模型/Embedding 条件。`ProcessingService` 的 `Applied` 直接使用 W2 Store 的 `LookupSynthesisApplyResultScoped`，与 retry 的 `StartScoped` 保持同一 UoW，不在事务内借第二连接。

API helper 的小型 definition registry 只持有相同的服务器固定 graph，供 retry 命令 Resolve；它不创建 Runtime、scheduler 或队列。主 Workflow service 已按第 4 步加入相同 definition。

## Worker 初始构造

在 `newWorkerComponentsWithModels`：

1. 取得 `artifactAgentRepository` 后、`terminalHookComponents` composite 前：

   ```go
   interviewPersistence, err := newWorkerSynthesisInterviewPersistence(db, artifactAgentRepository)
   interviewTerminal, err := interviewPersistence.terminalHook()
   terminalHookComponents = append(terminalHookComponents, interviewTerminal)
   ```

   同时按 runtime owner 的 helper 创建 `synthesisStore := newSynthesisRuntimeStore(db, artifactAgentRepository)`，将 store 本身加入 terminal composite。
2. 唯一 `runtimeRepository` 构造成功后，调用一次 `interviewPersistence.bindRuntime(runtimeRepository)`。保持已有 `ModelRuntimeFreshWithin`、managed enqueue fence 和 cancellation hook。
3. `workflowRepository`、`agentComponents` 完成后：

   ```go
   synthesisInterview, err := newWorkerSynthesisInterviewComponents(interviewPersistence, workflowRepository, agentComponents)
   err = registerWorkerSynthesisInterviewExecutor(executors, synthesisInterview)
   ```

   必须在 `executors.Freeze()` 前。`agentComponents.model` 缺失时仍注册真正 NotePreparationExecutor，模型适配返回 `INTERVIEW_NOTE_PLAN_UNAVAILABLE`，使 preparation 持久呈现 `CAPABILITY_UNAVAILABLE`，而非悬空 QUEUED 或假成功。模型存在而 catalog/scheduler/绑定不完整则构造失败。
4. 在 `definitions.Freeze()` 前调用 `registerWorkerSynthesisInterviewDefinition(definitions)`。这个 definition 的模型节点不自动重试付费请求；已知失败由用户显式重试，未知结果保留恢复现场。
5. 自动笔记执行使用 runtime owner 的 `newSynthesisWorkerOwners(...)`、`newSynthesisWorkerExecutor(...)`、`registerSynthesisWorkerExecutors(...)` 与 `registerSynthesisWorkerDefinitions(...)`。Owner 的 publication service 来自现有 ChangeControl 和 Authoring，同池生成投影；没有 Approval/Git 的旁路。

## Source-ready DispatchBatch

`cmd/worker/synthesis_components.go` 由 runtime owner 持有，其当前入口是：

```go
synthesisSources, err := newSynthesisSourceDispatcher(db, synthesisOwners, synthesisStore, runtimeRepository, definitions)
```

在 `definitions.Freeze()` 后构造，保存在 `workerComponents`，与现有 `captureOutbox` / `organizingOutbox` 共用主循环；不创建新 goroutine、ticker 或 queue。`dispatchSynthesisSources(ctx, logger, synthesisSources, phase)` 内部批量上限为 20。

- 启动：`startWorkerRuntime` 成功后，在 capture/organizing 首次 dispatch 附近调用一次，使用 `processContext` 加 `cfg.DatabasePingTimeout`。
- 周期：现有 `captureTicker.C` 分支内，在 `modelDrain.ProducersEnabled()` 为 true 时调用，独立短 context，沿用主线程取消/排空语义。
- 启动调用也检查 `modelDrain.ProducersEnabled()`，避免 rollout 排空期间增加新工作。
- Dispatcher 失败保留 durable outbox，由后续轮次恢复；日志只带稳定 code 和数量。不得把失败写成 ingestion 失败，也不得丢弃未知 source-ready 事件。
- NOTE_REVISION 面试准备在 API 的 preparation UoW 内直接 `StartScoped`，无需额外 DispatchBatch。

## Managed 模型热更新

给 `newWorkerRuntimeGenerationBuilder` 增加 `interviewPersistence` 参数（同一次启动已绑定的持久对象）。在 closure 每次 `newAgentWorkflowComponentsWithToolsAndMetrics` 完成后、该 generation 的 `executors.Freeze()` 前，执行同样的 `newWorkerSynthesisInterviewComponents(...)` 和 `registerWorkerSynthesisInterviewExecutor(...)`。

同样把 `synthesisOwners`、`synthesisStore` 作为长期持久依赖传入 closure；每次用 **该 closure 本次的** `agentComponents.model` / `contract` / `organizingScheduler` 构造和注册 `newSynthesisWorkerExecutor`。不要捕获启动时的 model 或 executor。

面试目录由 `newSynthesisInterviewRuntimeCatalog` 注册精确 Note Prompt/Schema、实际 Chat contract 和 `agentworkflow.DefaultProfileRef()`，再 Freeze。StructuredRunner 仍经既有 Eino scheduler；ModelRun/Call 由 owner 的 RecordingChatModel 保存。`workerRuntimeGeneration` 现有 registry 持有新 executor，RuntimeHost lease 保护其对应 Models 直到 Attempt 完成，不需新增 generation 生命周期或单独模型 client。

禁止在 generation rebuild 内重复创建/绑定 preparation store 或更换 terminal hook；它们只持久化 facts，不依赖当前模型。`ExecutionContext.ModelSettingsRevision` 从现有 River/Runtime acquisition 注入，由 owner 逐次校验，composition 不手工重写。

## 主会话追加：TODO2 v2 能力随模型热更新

已核实旧 `newWorkerWorkspaceAnalysisCapability` 只看启动时模型和可选组件：初始 disabled 会使维护对象为 nil，永久没有 heartbeat；初始 enabled 后停用模型则会继续给原广告续租。`HotRuntimeController` 更新的是角色 runtime/participant，未自动维护 Agent 的业务能力广告。启动时的全局 definition registry 同样不应成为后续启用模型的固定障碍。

本子工作包追加修改 `cmd/worker/workspace_analysis_capability.go`、`model_runtime_hot.go` 与新的 hot capability 测试：

- `workerRuntimeGeneration.workspaceAnalysis` 保存本代可选组合状态与冻结 Tool contract/executor registries；`newWorkerWorkspaceAnalysisGeneration(status, tools)` 构造该元数据，不保存模型配置或 Secret。
- 构造器保留旧 4 参数，managed 调用另传现有 `modelHost`；static 的可选 typed-nil Host 视为缺省。managed 初始 disabled 仍创建维护对象，固定 v2 definition/catalog 在独立 contract registry 冻结，不读取启动时模型或 definition registry。
- 每次 `Advertise`/`Renew` 调用 `Acquire(ctx, CurrentRuntime())`；在 lease 内验证真实 Models revision/Chat、当前 v2 Workflow/Tool executor 完整性、精确 catalog hash 与模型 timeout 对 Worker budget 的覆盖。错误、disabled、unknown、closed/gated Host 都不会执行广告写入；lease 在成功和失败路径均释放。
- 不可用时停止续租，由现有 30 秒 DB lease 到期关闭 API 的 capability gate。临时停用不执行永久 `Release`，因为已退役实例不能再次 Advertise；后续启用使用同 worker/同不可变 v2 合同恢复。只有进程结束才退役广告。写入曾发生但响应未知时仍保留关闭时清理的机会。
- advertised contract 继续固定 `workspace-analysis@2`、v2 catalog hash 和 policy 2；不增加 Host、连接池、后台调度或数据库状态源。

主会话已在 `cmd/worker/main.go` 完成三处接线，本代理随后只读核对：初始 generation 的 `newWorkerWorkspaceAnalysisGeneration(components.workspaceAnalysisCapability, components.tools)`；每代 builder 返回值的 `newWorkerWorkspaceAnalysisGeneration(agentComponents.workspaceAnalysisCapability, toolComponents)`；能力构造器的 `modelHost` 参数。初始 disabled 的维护对象非 nil，原有 heartbeat ticker 分支即可覆盖。builder 现传入收紧后的 `agentConfig`，保留静态终态/控制钩子失败时的禁用结论。

Worker 初始 definitions 仍在 `relation != nil` 时注册 Agent 合同，但这不是 managed 后续启用的阻碍：`RuntimeNodeWorker` 从当前/Attempt 的 Host lease 取得该代 ExecutorRegistry，`GORMRuntimeRepository.Claim` 从持久 `workflow.definition` 读取 Definition；执行链不读取初始 Worker definitions。Conversation dispatcher 按服务端固定 v2 合同构造 `BuildRuntimeStartRequest` 并在原 scope 内 `StartScoped`，也不依赖初始 Worker registry。

## 追加：API 初始禁用后的模型热更新

生产组合复核发现 API 仍只按启动时 `configuredModels` 构造 analysis starter：初始 disabled 会永久省略 analysis dispatcher，初始 enabled 后修改 timeout 也会继续使用旧预算。主会话授权本代理修复该入口：

- `cmd/api/main.go` 保留 `WorkspaceAnalysisAPIEnabled && dispatchReady && workspaceAnalysisRuntimeHooks != nil` 的静态门禁，向原构造器追加现有 `modelRuntimeHost`。
- `cmd/api/workspace_analysis.go` 的 managed 构造不再依赖初始 Chat。每次新的 `StartWorkspaceAnalysisRunScoped` 都 Acquire `CurrentRuntime()`，校验 API role、managed binding、revision 与实例身份，再用当代 Chat timeout 的私有配置副本构造 v2 starter。lease 覆盖同一个 caller-owned scope 内的 Worker readiness 和 Run insert；无模型、无 lease、错误 binding、预算不覆盖或取消时均拒绝新 Run，并释放已取得的 lease。
- factory 只保留同池 Repository、固定 definition/catalog/policy/config revision 和 duration/token 限制，不保留旧 Models、完整配置或 Secret，不创建额外 Host、Pool、模型 client 或事务。
- 新增 `internal/agent/application/workspace_analysis_replay.go` 的 `ScopedWorkspaceAnalysisRunReplayService`，仅接受 `Replayed=true`，复用 owner 既有 command/binding/domain 校验，只读取原 scope 的持久 v1/v2 Run。managed replay 在 Acquire 前直接委托给它，不要求当前模型、当代 timeout 或新广告；既有 foundation 文件未修改。
- static 同样先保留独立 replay 入口。`apiWorkspaceAnalysisStaticRunStarter` 只在新建时校验进程已有的冻结 Models 并构造 delegate；Chat disabled 时历史 v1/v2 同键重放仍能读取持久事实，新建拒绝且不查询 readiness、不插入 Run。它没有 runtime/Acquire，不伪造 live Host。主入口传入的 typed-nil Host 被识别为缺省；managed 缺 Host、静态模式携带真实 Host 或多个 runtime 参数均拒绝。

API 定向用例验证同一个 starter 的 disabled→enabled→更换 timeout→disabled、旧 Run 不被新预算修改、v1/v2 历史重放不触碰 runtime/readiness、gate 超时、acquisition 返回 lease 同时出错、typed-nil、错误 binding、预算不覆盖、caller scope 保持和取消后零写入。另以真实 RuntimeHost 证明 scoped start 完成前模型不能退役，以及 closed Host 拒绝新 start。这里的生命周期转换使用可控 current-runtime 端口与生产 Models factory，不等同于真实 HotRuntimeController Apply 或数据库/浏览器联调。

独立复核发现 static 分支原先在构造时调用 `forModels`，Chat disabled 会让 main 省略 analysis starter，继而在 Dispatcher 读取幂等记录前拒绝历史请求。上述 static wrapper 是该 P2 的最小修复；没有改 Dispatcher、main 或 managed 执行逻辑。回归覆盖生产构造保留入口、static disabled 下 v1/v2 原 scope 重放与新建零写入。

## 本次核对出的集成缺陷

- 已通知并由 runtime owner 修正：Synthesis 固定 graph 原始节点顺序与 registry canonical 顺序不同，导致执行 fence 的 hash 恒不匹配；owner 在固定定义里排序并补相等性回归。
- 已通知 interview owner 修正：`MaxRetries=0` 却带非零 delay，不满足 registry 契约；改零值 RetryPolicy。PromptRef 的 Version 为 string，使用 `"1"`。

## 验证记录

2026-09-09 初次 compile-only：

```sh
go test -mod=vendor ./cmd/api -run '^$' -count=1 -timeout=60s
```

未完成：当时共享 `internal/agent/application/workspace_analysis_decision_runner.go:10` 的未使用 `time` 导入使构建失败。已通知主会话，未改其他 owner 文件。

面试 owner 的全部锁定构造器落盘后执行了新增构造测试：

```sh
go test -mod=vendor ./cmd/api ./cmd/worker -run '^(TestAPISynthesisComposition|TestSynthesisInterviewComposition)' -count=1 -timeout=60s
```

仍未执行到测试：共享 Agent PostgreSQL v2 正在实现的 `gormValidateWorkspaceAnalysisDecisionClosure`、`gormAuthorizePendingWorkspaceAnalysisDecision`、`recoverWorkspaceAnalysisAdmissionDenial`、`validateWorkspaceAnalysisV2ModelAdmission` 当时尚未定义。已通知主会话与 foundation owner，等待其可编译通知再重验，不重复执行相同半成品构建。

共享 Agent 编译恢复后，已取得以下实际结果；初期阻断不再作为当前构造验证结论：

| 范围与命令 | 实际结果 |
| --- | --- |
| `go test -mod=vendor ./cmd/api ./cmd/worker -run '^(TestAPISynthesis\|TestSynthesisInterviewComposition\|TestNewAuthoringHandler\|TestAPIArtifactWorkflowComposition\|TestAPIWorkflowRegistration\|TestAPIRegistersAgentDefinition\|TestAPINeverExposesInternalToolWorkflow\|TestAPIWorkspaceAnalysisRuntimeHooks\|TestAPIWorkflowRuntimeHooks)' -count=1 -timeout=60s` | PASS，API 0.506s / Worker 0.483s |
| `go test -race -mod=vendor ./cmd/api -run '^(TestAPISynthesis\|TestNewInterview\|TestNewAuthoringHandler\|TestAPIArtifactWorkflowComposition\|TestAPIWorkflowRegistration\|TestAPIRegistersAgentDefinition\|TestAPINeverExposesInternalToolWorkflow\|TestAPIWorkspaceAnalysisRuntimeHooks\|TestAPIWorkflowRuntimeHooks)' -count=1 -timeout=60s` | PASS，2.316s；包含 NOTE completion Document verifier 接线后的构造 |
| `go test -race -mod=vendor ./cmd/worker -run '^(TestNewWorkerWorkspaceAnalysisCapability\|TestWorkerWorkspaceAnalysisCapability\|TestWorkerRuntimeExecutorAcquirer\|TestUnavailableInitialWorkerGeneration\|TestSynthesisInterviewComposition)' -count=1 -timeout=60s` | PASS，最终 2.764s；包含 ready 元数据但真实模型 disabled 的负测 |
| `go vet -mod=vendor ./cmd/api ./cmd/worker` | PASS，无输出 |
| `go build -mod=vendor ./cmd/api ./cmd/worker` | PASS，无输出 |
| `go test -race -mod=vendor ./internal/agent/application ./cmd/api -run '^(TestWorkspaceAnalysisReplayService\|TestAPIWorkspaceAnalysisStarter\|TestNewAPIWorkspaceAnalysisRunStarter\|TestAPIWorkspaceAnalysisRuntimeHooks\|TestAPIWorkflowRuntimeHooks\|TestNewConversation)' -count=1 -timeout=60s` | PASS，Agent application 1.606s / API 2.359s；包含 API 热更新、独立 replay 和 acquisition 中途取消后的最终版本 |
| `go vet -mod=vendor ./cmd/api ./internal/agent/application` | PASS，API 热更新与 replay helper 落盘后，无输出 |
| `go build -mod=vendor ./cmd/api ./internal/agent/application` | PASS，API 热更新与 replay helper 落盘后，无输出 |
| `go test -race -mod=vendor ./cmd/api -run '^(TestAPIWorkspaceAnalysisStaticStarterReplaysWhenChatDisabled\|TestNewAPIWorkspaceAnalysisRunStarterPreservesStaticAndManagedModes)$' -count=1 -timeout=60s` | PASS，2.018s；static P2 修复后的定向回归 |
| `go vet -mod=vendor ./cmd/api` | PASS，static P2 修复后，无输出；两份 Go 文件的 gofmt 与 whitespace 检查通过 |

另一次 Worker race/vet 曾被同期 Tools v3 的临时 `gitStatusInputError` 缺失阻断；依赖补齐后上述检查已实际通过。新增热更新测试首次因 fixture 未设置 managed 模式要求的 key-file 路径而未进入断言；补全只用于构造的绝对路径后通过，没有读取文件或凭据。

生命周期单测以生产 Models factory 构造不可变模型代次，使用冻结 Registry 和可控 current-runtime/广告持久端口验证同一维护对象的 disabled→enabled→disabled→恢复、变更模型 timeout、gate/unknown/缺少 executor 零续租；另使用真实 RuntimeHost 证明广告写入期间 generation lease 未提前释放。它们不调用模型或数据库，也不冒充真实模型 Apply/DB 广告联调。API fallback 两类终态 owner、原 hook 保留、Chat 无关注册、typed-nil publisher 和 starter 单次绑定都有构造回归覆盖。

`go-review` 自检核对同池组合、typed-nil、runtime 构造顺序、单次 starter 绑定、模型 unavailable、模型目录隔离、current lease 生存期、可恢复广告与永久 Release 区分。早期 8 个本子工作包 Go 文件的 `gofmt -l`、3 个已跟踪 Go 文件和 6 个新文件的空白检查均通过；新增文件使用逐文件 `git diff --no-index --check /dev/null <file>`，正常差异退出码 1 不表示 whitespace 错误。末次 Worker race 之后，Worker vet/build 再次通过。主会话持有的热更新 main.go 三处接线已实际落盘并完成调用链复核；API 后续追加热更新与独立 replay 的定向验证见上表，实库生效仍以统一联调证据为准。

没有执行提交、推送、部署、迁移或真实数据库业务验证；完整自动合成/审批/面试仍须由主会话按 AC 验证。
