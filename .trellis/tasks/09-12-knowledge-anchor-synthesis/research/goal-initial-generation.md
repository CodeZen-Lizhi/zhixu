# 目标驱动初版生成的实现衔接

用户已批准的行为：只提出“整理数据库知识”等目标，即可从工作区已有知识目录选择相关片段、读取证据并形成初版主笔记；不要求逐篇分类、逐篇选文件或先创建空锚点。初版仍为待审阅候选，后续 AI 提炼维护范围沿用现有确认入口。

## 已核查的复用点与硬边界

- `GORMProfileRepository.ListProfileDirectory` 与 `SynthesisGoalCatalogReader.ReadSynthesisGoalCatalog` 已提供分页元数据候选。按 Source ID 游标向前推进；同一页的 Profile 当前指针/Revision/evidence 通过 repeatable-read 一致读取。不加载 canonical chunk content 或 Artifact bytes。删除、隔离和旧 SourceVersion 在 LIMIT 前排除；Source owner 在返回目录前再核验新旧与派生来源，剔除后仍保留原页游标。
- 目录是候选，不能由标题/标签相同直接准入。工作流应让 AI 根据用户目标、简介、主题/用途线索和逐知识点文本选择相关片段；模型只能返回本批局部标签，服务端再绑定不可变 ProfileRevision/point locator。
- 选中后复用 `ResolveAnchorDiscoverySources` 将对应 SourceSpan IDs 解析为精确 SourceRef；随后由 `OpenSynthesisSource` 或相应当前读取验证实际原文。历史 SnapshotText 不进入新生成。应用时重验 Source fence；不能因画像有效就跳过原文证据检查。
- `SynthesisGenerationInput` 目前只允许一个 source-ready 主来源及已有笔记已引用的其他来源（`synthesis_contract.go:95` 附近）；`SynthesisExecutor.prepareInput` 以该事件读取当前候选和原文。因此不可把多来源目标请求伪造为普通 source-ready，或只用目标关键词筛一个文件后宣称完成全库整理。
- `AnchorFusionDispatcher` 展示如何在原 UnitOfWork/Workflow runtime 中建立独立触发身份，而不重放旧 source-ready 去重键。目标请求需要独立持久幂等身份、冻结目标与选择结果，并扩展原生成/语义审查/应用链。不能绕过真实 ModelRun、输入/输出哈希、执行 fence 和语义验证。
- 现有 `AnchorSetupPanel` 在已有 SynthesisNote 上执行 INITIAL_SCOPE 推荐和确认。它不能创建普通来源笔记或从零生成主笔记；两者依然是未完成要求。

## 下一步实现顺序

1. 建立用户目标请求的持久身份、原请求哈希、分页进度和不可变目录批次。只有真正完成分页分析时才结束选择；目录超过单次模型预算时继续分批，不静默截断为“最全”。临时无可用画像与没有相关资料必须区分，失败/等待不能假报成功。
2. 在现有 StructuredRunner/RecordingChatModel/Eino 路径中加入选择模型步骤，严格绑定局部知识点标签；保存实际调用证明。未选中的混合主题片段不读入正文生成。
3. 扩展原 Synthesis processing 的目标触发及多来源冻结契约。生成只产生该目标的一份初版候选；审查/应用验证精确来源、知识点快照及目标身份，仍使用原 ArticleRevision/发布机制。重复请求/重试不能创建多个笔记。
4. 主笔记中心提供目标输入、真实异步状态/重试及候选链接；普通笔记转主笔记以指定来源作为种子，经相同证据路径生成候选，再进入维护范围确认。
5. 用真实目录生成、混合 Redis/Oracle/无关模块、已有同类主笔记、幂等重试及无资料/画像未就绪场景验证完整链路。当前只有目录读取及安全筛选的证据，不代表以上执行步骤已完成。

## 已实现的选择输入契约

`ReadSynthesisGoalCatalogSnapshot` 按 00109 保存的 ProfileRevision identities 重建元数据，不读取 mutable current pointer 或原文字节。`BuildGoalSelectionInputs` 对每个来源的知识点/例子连续分片，每片最多 32 点且编码后不超过 256 KiB；按数量和实际 JSON 字节预算继续分批，覆盖全部最多 512 点。模型输入保留 user_goal、source_title、source_summary 与逐点文字和局部标签；目录中的完整主题/术语集合仍可检索，但不把整套标签/别名反复复制进每个选择请求。权威 Source/Profile/point/span 身份只在服务端分片绑定中保留。

`DecodeGoalSelectionOutput` / `BindGoalSelectionOutput` 拒绝未知字段、重复键/标签、越界或该分片不存在的 P 标签，允许明确空选择。选择结果是待打开原文的知识点，不能被当成已经核验的正文结论。`RegisterGoalSelectionRuntimeCatalog` 定义相应 v1 prompt/schema，但尚未注册到生产 ModelRun 执行路径。

下一步需要：持久选择请求按 catalog_batch_id/source_ordinal/point_offset 去重；在调用前冻结实际 ChatRequest hash 及 Workflow node attempt；复用 RecordingChatModel/Eino 执行，依据原始输出字节和模型证明绑定选点。迁移需同步 agent ModelRun schema/result allowlist 与执行 fence，不能借用 anchor recommendation 的身份伪装目标选择。分片元数据必须从上述不可变目录重新构造并校验输入 hash；失败/取消/丢响应重试遵循已有推荐路径的语义。所有选择分片完成后，才进入多来源原文验证与初版生成。


## 00110 选择执行事实及继续入口

已实现 `SynthesisGoalSelectionStore`（application/synthesis_goal_execution.go）与 `NewGORMGoalSelectionStore(pool, goals, snapshots, runs)`；PrepareGoalSelections 按 goal request / batch number 完整封存所有分片，ReadGoalSelectionInput 只重建该分片所在来源的旧画像。`GoalSelectionModel.Select(ctx, ExecutionContext, selection)` 走实际 RecordingChatModel/Eino 并保存 ModelRun/原始响应证明；需要先读取已绑定 ScheduledWorkflowID 的 selection。空 selections 是合法选点完成，不表示整份目标无资料。

持久调度预留 `ClaimPendingGoalSelectionScoped(ctx, scope, workspaceID)` 和 `BindGoalSelectionWorkflowScoped(ctx, scope, workspaceID, selectionID, workflowID)`，必须与 Workflow Start 同事务使用。00110 schedule guard 要求 Workflow definition key `organizing.goal-point-selection`、version 1，以及 input 中精确 `selection_id`、`expected_version`。生产 Definition/Dispatcher/Executor、RuntimeCatalog 注册、manifest producer 当前尚未接线，不能把已测 Model.Select 描述为生产自动分析已执行。请求失败/工作流取消后的 reconcile 与显式重试仍需补齐；当前 RUNNING/RECOVERY_REQUIRED 不能自动重发模型。

后续调度必须使用当前授权 Workspace、有界分页/防饥饿与既有 Runtime/Model Settings fence；成功分片只读复用，全部批次和分片完成后才进入原文核验与多来源初版。真实外部模型质量、空目录与未画像区别、来源转主笔记及 UI 仍待验收。独立 schema 验证库已改为 zhixu_anchor_schema_final_110。


## 00111 已接入生产的调度

上节未接线项中，RuntimeCatalog/Definition/Dispatcher/Executor、manifest producer 和显式 retry/reconcile 现已完成。稳定 definition key/version 为 organizing.goal-point-selection@1，node kind 为 organizing.goal-point-selection.run，原 Workflow/River 入队事务和 model settings fence 保持。生产 owners 新增 goalSelections，启动及 managed 重建均注册模型适配器，缺模型时执行器保存可重试能力失败。Prepare 每轮最多 4 批，调度每轮最多 8 个选择；准备失败独立 30 秒退避。111 的 retry receipt 保留精确 selection/version/命令键，FAILED+retryable 且旧 workflow terminal 才能重置执行；RECOVERY_REQUIRED 不自动重发。

后续优先：目标所有 batch 的 manifest 已封存且所有 slice SUCCEEDED 才能生成；目标状态 CATALOG_READY 仍只是目录完成，不能展示为正文完成。汇总全部选择必须按不可变输入绑定 Source/Profile/point/span，保留来源用途，读取原文时复验当前性；重复 selected spans 可按精确身份合并，知识点 locator 不能丢失。目标的一份初版候选需要稳定目标身份去重，不能借 source-ready 自动主题拆分或新造虚拟源绕过多来源契约。空目录/无选择与尚未画像的文件应有不同可解释状态。API/UI 尚未接入，普通来源转主笔记仍待实施。验证库名保持 final_110，实际 schema version 已为 111。

## 当前生成接入边界（2026-09-15 修正）

不要使用曾添加的 Coordinator/GenerateGoal/BuildSynthesisGoalGenerationInput；它们已删除。当前使用 application.SynthesisGoalBinding 和原 SynthesisExecutor 的 prepareGoalInput 分支，原四节点图和独立语义审查保持。GoalBinding 冻结精确选点来源，Provider 使用 v3 user_goal，旧 v1/v2 不变。

下一个真正需要完成的持久边界：synthesis_processing 增加独立 goal_request_id（workspace FK/唯一身份），独立 processing key 含 goal 请求，不能再用 SourceEvent.ProcessingKey 单独去重；StartInput、加载、freeze、retry、apply receipt 必须一致。现有 migration 00102 展示 fusion 扩展模式，但 goal 不同于单来源 fusion；执行种子只能来自真实已选来源 outbox，不能伪造 event。SynthesisProcessing/processingModel/LoadSynthesisExecution 当前尚未保存或装载 GoalRequestID，直接写 queue 参数会被 executor 拒绝。

freeze/apply 必须从 SUCCEEDED、封存且完整的 selection 账本重新验证 GoalBinding：目标文本、每个已选 locator/span、selection ModelRun 证明与完整集合，不能仅凭传入的合法 UUID 允许扩充来源。可复用 GoalSelectionResultReader 的不可变分页；原文当前性由现有 Source fence 最终事务核验。输入的完整冻结哈希已含 GoalBinding，原 source/fusion nil goal hash 兼容。初版结果只接受一篇新笔记，稳定身份与结果回链还需完成。

## 00112 后的下一步（2026-09-15）

GoalRequestID 已由生产 processing Store 持久读取；SynthesisProcessingKey(sourceEvent, goalID) 对 goal 仅绑定 workspace+goal。数据库要求种子对应成功选择；队列/冻结绑定相符，完整选择证明器已接入 API/Worker runtime。只读 GORMGoalSelectionResultReader 与历史 SynthesisGoalCatalogSnapshotReader 可独立构造，勿再造空 SynthesisStore 或设置初始化后可变依赖。

下一步需要目标生成 dispatcher：读取全部选择 Ready 且尚无 goal processing 的请求，从选中来源取真实 outbox 事件；工作区 intake 锁后按 goal 查重、StartScoped 和 CreateSynthesisProcessingScoped 同事务。目标缺失原文/超过预算应进入可见失败状态，不能持续重新收费。继承原四阶段执行与失败恢复；目标初版只能有一份候选。当前尚无生产 dispatcher，也尚无用户可发起的目标页面。

## 生产目标调度已接通（2026-09-15）

上一节待建的 dispatcher 已由 workflow/synthesis_goal_generation.go 和 synthesispostgres/goal_generation.go 实现；生产 Worker 启动与周期均已接入。后台真实 River 验收覆盖两个来源的完整选择→生成→独立语义审查→一份未发布候选，以及能力失败后的显式重试/同键重放、空选择防阻塞、并发调度与原始 outbox 不消费。仍未接 HTTP/UI。

保留锁顺序要求：source-ready 消费者先以 FOR UPDATE 锁 outbox，再取 Workspace intake lock。目标 dispatcher 持有 intake lock 时，其 provenance 查询必须 FOR KEY SHARE OF event SKIP LOCKED；否则随后 processing 外键的隐式 key-share 与普通消费者形成反向等待。共享锁只保护引用，不发布 outbox。实库测试已持有来源独占锁验证目标非阻塞退出。

下一步前后端落点：internal/organizing/http/synthesis_handler.go、synthesis_wire.go 与 cmd/api/synthesis_components.go；web/src/api/synthesis.ts 为唯一合成 HTTP 边界，features/synthesis 的主笔记中心增加目标输入、真实处理阶段、显式重试及候选链接。不能把 SynthesisGoalRequest.Status=CATALOG_READY 显示成主笔记完成；应组合 selection progress、goal processing 与持久候选结果。全空选择与空目录/未完成画像的区分仍须补齐。保持当前一份候选与既有发布审批语义。

## 用户入口已接通（2026-09-15）

HTTP 在 synthesis_goals.go / synthesis_goal_selections.go，生产 newAPISynthesisComponents 使用 NewSynthesisHandlerWithGoals。目标模型仍全部由 Worker 执行；POST 只持久目标，GET 只读。独立 GORMGoalViewReader 通过 synthesispostgres.Store.ReadSynthesisGoalProcessingBatchScoped 复用处理状态投影；候选取 processing 的不可变 revision，不取 note.current_revision_id。

前端目标专用严格边界为 web/src/api/synthesis-goals.ts，复用既有 decodeSynthesisProcessing 与生成客户端；SynthesisGoalsPanel 组合进主笔记中心。创建、筛选重试使用原命令幂等键；目标 ready 而 processing 尚无记录时必须继续轮询，不能当终态。候选存在只证明曾经生成过，不证明当前未发布；精确版本链接由 candidate.note_id/revision_id 构成。

已知待补边界：selection_preparation 表的错误目前只后端退避，未单独进入 goal view；来源 readiness 和目录 owner 是先后只读查询，未建立整体文件系统快照。后台读取当前原始 SourceVersion 的未分析状态包括 Capture 尚未关联的情况，不能再用 inner Capture join 漏掉刚登记的文件。后续正式文件持续发现和完整产品验收继续按主任务推进。
