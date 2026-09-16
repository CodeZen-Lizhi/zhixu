# 实现计划

状态：用户已批准按任务清单实施；以下未完成项继续执行，无需重复审批。

- [x] 2026-09-16 后续授权：按 8 个实际功能分别设置思考强度已实现；真实PG/HTTP/浏览器保存刷新、两代配置协议请求、实际Worker文件简介/融合、130迁移恢复均通过。BGE-M3兼容修复与真实两条向量调用通过。见 research/reasoning-effort-functions.md。
- [ ] 实际本地运行服务更新后启用BGE。已保存desired revision7，active revision2仍关闭；聊天真实验收受本地网关503阻塞。未部署或迁移实际用户数据库。

## 规划补全项

- [x] 2026-09-16 用户授权增量：统一模型思考强度及 GPT-6 已知参数兼容已实现；真实 PG/HTTP/页面保存刷新、生产模型请求、本地运行时绑定和独立检查通过。没有按任务自动覆盖；未做真实外部模型和完整双进程热激活验收。具体证据见 research/reasoning-effort.md。

- [x] 核实片段知识目录、初版生成、来源关系与当前合成发布链路：已有 Profile/SourceSpan、SynthesisRevision/SourceRef 和证据打开；缺少独立目录实体与主笔记片段到知识点关系投影。
- [x] 补齐主笔记关系、来源图谱、集中阅读及更新提醒边界：现有前端仅有 synthesis 列表/详情/证据，需新增主笔记中心、关系/来源图谱和提醒投影。
- [x] 明确锚点范围演化及已追问的历史建议：范围调整需确认、纯重复只记来源、未裁决冲突可审核发布、局部更新保护人工内容、来源失效复核、上游发布触发下游候选。
- [x] 核实上游发布事件、引用版本和影响分析链路：现有 Impact 主要覆盖 Topic/Claim/Relation，未绑定 synthesis note；需新增主笔记依赖传播和去重。
- [x] 将主笔记中心设为默认产品入口，补齐主笔记列表、当前版本、更新提醒、冲突和来源图谱入口（各项实际验证范围见后文）。
- [x] 实现并定向验证主笔记片段到知识点、来源文件、来源版本和原文片段的追溯链及历史快照；无历史知识点绑定的旧资料明确 UNRECORDED。
- [x] 已补齐具体文件、数据契约、验证证据和上下文清单；当前收口结论见 research/remaining-acceptance.md，后文按时间保留历史进度。

1. 梳理现有笔记导入、知识主题、溯源、合成运行和发布版本接口，确定复用点。
2. 设计并实现来源元数据与片段知识目录，绑定来源版本和原文片段。
3. 设计并实现锚点/汇总目标、关联候选、范围调整候选、融合候选和发布版本的数据契约。
4. 接入笔记保存后的异步元数据分析，生成简介、标签、主题和解释依据。
5. 实现主笔记创建、主笔记中心、来源追溯和主笔记关系/来源图谱。
6. 实现增量融合候选、局部差异/冲突展示、来源失效提醒和版本发布指针。
7. 实现上游发布触发下游影响检查，按实际依赖生成候选并去重。
8. 为新增公共接口补充最小行为测试，覆盖幂等、来源溯源、候选确认和版本发布。
9. 执行后端类型/编译检查、相关测试和一次真实流程验证。

## 风险点

- 现有不可变知识模型的约束可能要求通过新版本记录表达更新。
- 模型输出 schema 必须与现有 agent 运行契约兼容。
- 相关性误判需要保留人工拒绝路径，不能自动污染已发布版本。

## 当前进度记录

- [x] 领域层区分正文变化与来源证据变化：`SynthesisDeltaResult.SourcesChanged`。
- [x] 定向合成领域、应用、适配器、工作流测试及 `go vet ./internal/organizing/...` 通过。
- [x] 前端 `typecheck` 与 `lint` 通过。
- [x] 来源证据独立持久化、锚点关联审批基础和主笔记中心默认阅读入口。
- [x] 知识目录可查询投影、持久 AI 推荐执行与审批后融合调度、共享来源图谱、历史画像版本绑定和画像就绪调度（具体验证边界见下文）。
- [x] 目标式初版生成与单来源提升：持久目录选择、生产 River 四阶段生成、固定 Provider 实库流程（00109–00113，证据见后文）。
- [x] 自动发现新增/修改/恢复来源，A→B→A 保留正确当前版本（00114）；来源失效提醒与历史原文读取已有定向实库证据。
- [x] 真实历史发布事件、已发布片段包含与精确追溯（00115–00116）；不等同于发布后的下游候选传播。
- [x] 上游发布影响持久提醒、精确版本链接与真实数据库/HTTP/组件联调（00117，证据见后文）。
- [x] 上游发布后的局部候选自动生成：00118 真实 River、实际片段许可修复及定向审查通过；完整多轮审批传播仍待验收。
- [x] 列表集中更新摘要及 P/C 精确版本入口，真实 PG/HTTP/浏览器刷新及手机显示通过。
- [x] 固定 Provider 基础产品 Compose 流程通过：Capture、审批/Git、增量/重放、历史/来源、桌面/手机复习。
- [x] 人工全文 v2、两阶段合并与裁决、不可变捕获/凭据、审阅和双基线发布接线；真实PG/River/HTTP/浏览器验证通过，边界见后文。
- [x] 文件自动发现失败的持久可见状态与恢复入口：真实FS/PG/HTTP/浏览器重扫恢复通过，跨binding分页P2已修复。
- [x] 多轮正文传播：A→B→C及A/B相互引用，在真实审批/Git、River/模型账本下验证局部更新与重复扫描收敛（固定Provider、v1图）。
- [x] 人工编辑及候选生成后文件再次变化的重合并：真实浏览器、隔离审批/Git、刷新恢复与正文逐字保真已验收。
- [x] 人工改写后的当前全文补源：首次复核、LOCAL_FILE实际发布后手改、证据读取/失效/历史原文、127重核与跨终态恢复均已联调；4项审查问题已修复，实际浏览器丢响应后刷新与同键恢复通过。固定Provider流程不替代下项真实模型质量。
- [x] 历史主笔记恢复为当前发布内容：128 owner、HTTP/Web、原审批/Git、来源/profile/body继承、后续增量及实际浏览器311.78s通过；最终128迁移/Schema恢复通过。同正文独立Git发布、无证明拒绝、丢响应找回原commit、旧125兼容与最终独立窄审均完成。
- [ ] 真实外部模型语义质量验收（当前没有可用配置，尚未调用）。
- 历史全仓检查曾因 `cmd/api/TestAPIHasAllToolContractsWithoutExecutors` 的基线数量断言失败（期望 17、既有目录返回 21）。本任务采用受影响范围验证，不以此要求重复全仓测试，也不声明全仓通过。


## 2026-09-14 实施证据与下一步

已落地：主笔记首页与独立导航；默认阅读已发布版本、显式审阅候选；独立补充来源账本、查询/打开 API、后补证据面板；后续生成冻结和使用补证；锚点持久身份、范围版本、关联/范围审核、7 个 API 接线及前端审核面板；冲突“尚未裁决”标记。

当前工作区验证：Web 定向测试、类型检查、lint/构建；OpenAPI 与路由一致性；隔离 PostgreSQL 重放 101 个迁移并导出 Schema；`TestSynthesisSupplementPersistence`、257 条账本预算回归、`TestAnchorPostgreSQLLifecycle` 通过。独立 Go 审查发现补证累计上限会永久阻断生成，已修复并加实库/冻结重开回归。既有 `cmd/api/TestAPIHasAllToolContractsWithoutExecutors` 的固定数量断言仍期望 17，而未改动的 toolcatalog 返回 21；不据此宣称全包通过。

下一步仍属原批准范围：

1. 将真实 AI 锚点初始范围/关联推荐接入，完成模型证明 verifier。
2. source-ready 和 apply-time 的当前范围/精确片段准入已接入；仍需接受关联驱动独立的后续融合请求，不能只保存审批记录。
3. 从已有普通笔记/主题目标创建锚点的完整入口与自动生成初版。
4. 知识目录读取投影已接入；仍需历史知识点快照绑定、主笔记来源图谱、关系/正文依赖与发布触发下游局部候选。
5. 来源失效的持久影响提醒、人工编辑保护的完整发布验收及真实模型/浏览器闭环。

以上未完成项不得由组件/接口存在替代验收；任务保持 in_progress，不重复请求需求或实施批准。

补充验证：`AnchorReviewPanel.test.tsx` 覆盖按需加载、批量接受、丢响应后原请求/原幂等键重试、成功后持久重读，以及范围变化后旧建议禁止接受。与 API、主笔记页面、路由合计 68 个定向测试通过；Web 类型检查与 lint 通过。`CONTEXT.md` 与 ADR-0031 已记录确认的术语和版本/审批决策。准入工作中曾出现接口签名同步期间的编译失败，需待切片完成后复验，不记为通过。

知识目录读取切片已接线：Capture ProfileReader → organizing owner projection → 两条 READ_LOCAL HTTP → OpenAPI/generated Raw → 严格 Web decoder → 原文弹层按需知识点。后端 owner/HTTP 与 API/Web 行为测试通过；主笔记历史版本对 ProfileRevision 的固定绑定尚待实施，不能由当前目录投影代替。锚点准入子任务已报告 organizing、worker、隔离 PG lifecycle 和定向 vet 通过，先前接口签名临时失败已修复。锚点 prompt 行为版本升级及持久证明兼容仍在处理。

本轮收口证据：主笔记 API、目录弹层、批量审核、路由共 70 个 Web 定向测试通过；目录 HTTP、路由库存/Auth/API composition 定向测试通过；OpenAPI lint、222 条 tag 清单、contract check 与生成客户端漂移检查通过。提示词兼容修复保留无锚点 v1，新锚点生成/语义审核用 v2，持久 proof 从冻结输入精确推导版本；agent/organizing/worker 测试和定向 vet 通过。提示词改动后额外执行隔离 PostgreSQL/River `TestSynthesisSourceReadyRunsThroughRiver` 通过（12.198s）。真实浏览器与外部真实模型的完整新流程仍未验证。

来源图谱读取切片已落地：所读不可变版本 → 精确来源文件/片段 → 共享文件的其他已发布主笔记。后端只读事务验证真实发布，前端提供按需展开、来源选择、分页和各自历史版本的原文深链；共享文件关系没有冒充正文依赖或包含关系。隔离 PostgreSQL `TestSynthesisSourceGraphUsesPublishedPeersAndExactHistory` 通过（13.043s），覆盖实际生成/发布、草稿排除、分页、工作区/版本隔离、删除来源后的历史保持。HTTP/路由定向测试、Web API+主笔记页面 35 个测试及图谱分页 1 个测试通过，Web 类型/lint、OpenAPI 223 条 tags/lint/contract/generated drift 检查通过。真实浏览器图谱流程尚未运行。

审批后融合子任务新增 00102 与持久请求/dispatcher/限定准入，尚待真实 River 闭环测试；审查发现 processing trigger 在 DISPATCHED 后更新的状态条件需专项核实。AI 推荐子任务新增 00103 请求冻结和 request/get/list/retry 仓储，真实模型 executor、proof verifier、source-ready 独立消费仍未完成。全量需求保持 in_progress，不因上述切片存在即勾选全部验收。

本轮审批后融合进度接线：新增 READ_LOCAL `listRecentAnchorFusionRequests`、严格响应投影及 Web decoder；维护范围面板可按需查看最近 20 条请求。PENDING 自动回查，DISPATCHED 再读取真实 processing，STALE 提示范围/来源变化；手动刷新同时更新列表和任务状态。HTTP 生命周期/作用域测试、真实路由/OpenAPI 224 项一致性、Web 融合反馈及既有审批面板测试通过。发现测试文件的 TypeScript ESLint 违规后已修复，定向 lint 和类型检查通过。生成客户端漂移检查通过。

已完成 Chromium 组件验收（固定测试资料，临时页面已清理）：桌面 1440 和手机 390 像素，来源图谱展开、共享笔记精确引用链接、融合请求等待→实际结果、手动刷新；两种布局均无横向溢出。截图留于 `output/playwright/anchor-flow-desktop.png`、`anchor-flow-mobile.png`。这不是生产模型或完整真实后端浏览器闭环。

融合后端 worker 已完成 `TestAcceptedAnchorFusionRunsThroughRiverWithoutReplayingSourceReady` 和 `TestAnchorPostgreSQLLifecycle`：实际关联接受、持久调度、River 后仅目标笔记生成第二修订，旧source-ready未重放且重复调度无新run。修复 00102 guard 的 INSERT/UPDATE 状态分支、trigger JSON NULL 和 allowed_sources 精确绑定，以及冻结事件的值比较；执行前范围/来源准入失效会报 SYNTHESIS_INPUT_STALE。验证使用临时迁移副本的 Atlas 校验和，主迁移清单/schema 仍需统一刷新。

AI 推荐新增共享 `DecodeAnchorModelOutput` 与 `RegisterAnchorRecommendationRuntimeCatalog` 并通过定向测试。该部分只定义严格输出，不代表模型执行已完成；PG 必须从原始输出重新绑定冻结证据及业务范围后才能接受，模型 hash 相等本身不能授权调用者任意 scope/reason。Runner/source-ready/用户确认初始范围入口仍待落地。

本轮收口检查：HTTP/application/agent/worker 的 Anchor、Catalog、Synthesis 定向测试通过；5 个 Web 文件共 41 个测试通过。模型输出严格解码与 catalog 的测试另覆盖三类推荐、无建议联合、重复键、额外权限字段、越界和重复证据标签。PG recommendation verifier 仍需核对 raw 输出与业务结果一致、模型绑定发生在调用前，并通过 initial/无建议终态完整验证；当前不将 verifier 文件存在算作自动推荐完成。

AI 推荐执行切片已完成（2026-09-14）：`AnchorModel.Recommend` 使用共享 `InitialStructuredRequest` 生成真实 ChatRequest，再以其 JSON SHA-256 领取冻结请求；实际调用经 RecordingChatModel 和 Eino 三阶段调度记录。`BuildAnchorRecommendationInput` 校验原文哈希、版本、当前范围及证据预算，模型只接收临时标签；输出标签重新绑定精确来源。数据库完成 proof 同时检查首请求 hash、末响应 hash/字节数及原始输出。已有锚点的标题显式传入并保持，模型结果只创建待审批建议。

`00103` 补全初始正向/无建议/失败/恢复必需终态，原始输出用 bytea 保持字节不变；重试清理旧 claim 并支持同幂等键回读。新增 ModelRun 数据库准入保留旧契约，推荐模型只能绑定同 workspace/node/attempt 的 RUNNING 请求。隔离 PG 的 `TestAnchorModelRecordsActualCallsAndRecommendationOutcomes` 使用实际 Eino + RecordingChatModel + 固定 Provider 响应，验证初始推断不自动建锚点、无建议、关联不自动融合、失败重试、持久原始字节与真实请求哈希；与原 proof/伪造/旧 scope 用例合计通过（19.228s）。agent application/domain、organizing application/agent/postgres/workflow、worker 测试和定向 vet 通过。新空隔离库已由项目迁移器重放 103 个迁移并导出 schema；旧隔离库存在手工表与迁移记录不一致，未修改其历史。

下一接线点：独立持久 Workflow/dispatcher 调用 AnchorModel，source-ready 生成各锚点推荐请求，HTTP/Web 的初始分析和确认入口；目前生产 composition 尚未调用 AnchorModel，不能将上述模型执行测试宣称为自动扫描闭环。历史知识点快照、来源失效持久提醒、主笔记正文依赖与发布传播、按用户主题生成初版、人工编辑与完整浏览器/外部模型验收仍未完成。任务保持 in_progress。

## 2026-09-15 异步推荐调度与初始确认入口

已接入初始分析 request/list/get/retry 四条 HTTP，权限与 OpenAPI/generated 客户端同步为 228 项。Web 主笔记维护范围面板支持启动异步分析、轮询、无建议/失败/恢复提示、编辑 AI 推断范围并确认创建锚点；旧基准不允许确认，丢响应保留原幂等命令重试。API/Setup/Review/Pages 40 个定向测试及类型检查、lint 通过（前轮证据）。Chromium 固定资料组件流程验证分析等待→自动出现建议→确认后重读锚点，390 像素布局无横向溢出；截图 `output/playwright/anchor-setup-mobile.png`、`anchor-setup-confirmed.png`。浏览器未验证编辑输入；此检查不是生产后端或外部真实模型闭环。临时页面、专用浏览器和 5179 服务已清理。

00104 为推荐请求绑定独立持久 Workflow；worker 静态/热模型组装与启动/周期 dispatcher 已接线。每次明确重试使用请求版本生成新工作流幂等键，旧任务不能复用新请求。执行前核验冻结基准、锚点范围和精确可用来源；配置缺失和其他调用前失败持久化，取消上下文下用有限独立上下文收尾。终态巡检将未领取请求标为可重试 FAILED，将已领取但未保存结果的请求标为 RECOVERY_REQUIRED，避免重复 provider 调用。

真实 PostgreSQL/River 验证发现两个接线缺陷并修复：GORM 忽略未导出嵌入字段导致请求证据未读取；启动 JSON 按字节比较与 jsonb 读回格式不兼容。启动输入现复用 strictjson，允许对象空白/顺序变化且拒绝重复/额外字段。`TestAnchorRecommendationDispatchRunsThroughRiver` 通过（11.350s）：实际入队、模型调用、结果持久化、不自动建锚点、失败后不同工作流重试、无建议、重复派发，以及取消前后巡检的区分和幂等性。取消后已领取状态用持久 claim 模拟结果丢失边界，不表示该用例实际发起了 provider 调用。application/workflow/worker 测试与限定 Go vet 通过。隔离库通过项目迁移器升级到 104，Atlas validate 通过并更新 schema。

仍待完成：source-ready 自动产生各锚点推荐请求，普通笔记/主题目标创建主笔记，历史知识点快照固定，来源失效持久提醒，正文依赖及发布传播，人工编辑完整发布验收与真实后端/模型浏览器闭环。全任务保持 in_progress。

独立审查发现真实执行取消路径仍会写成不可重试 FAILED，且取消与成功结果竞争时缺少持久化前的执行状态锁。已修复：AnchorModel 对执行 context 取消/超时转入恢复必需，并在成功路径检查取消；推荐落盘事务复用 Workflow owner 的 scoped execution fence，锁定 run/node/attempt 并按数据库时间核验运行状态、取消/暂停标志及租约。推荐写入与取消串行化，失去执行资格的输出不生成建议。新增真实阻塞 Provider 的 River 用例，分别覆盖心跳取消调用，以及取消命令后 Provider 返回成功结果，两者均成为 RECOVERY_REQUIRED 且无 proposal；先前仅模拟 claim 的测试不再作为这两条路径的证明。与模型执行、proof 既有用例合计通过（26.302s），agent/postgres/workflow/worker 包测试与限定 vet 通过。重试后旧 workflow 的迟到失败写入也已验证被拒绝。

上述取消修复已由同一 reviewer 复核通过：初始完成和关联/范围建议均在落盘事务调用执行 fence，真实 River 取消竞态测试覆盖相应路径，未发现本次修复范围内遗留的明确缺陷。

## 2026-09-15 来源画像到自动锚点关联发现

新增 `AnchorDiscoveryDispatcher`：从已有 source-ready 处理事实派生来源/锚点范围订阅，先读取 Capture 知识目录，再由 owner 仅解析目录所列 span 的元数据；此阶段不读取整篇原文，也不调用模型。冻结不可变 ProfileRevision 与精确证据后按每批 32 条生成持久 recommendation 请求；画像指针后续变化不改写已冻结批次，中途失败通过固定 discovery/batch 幂等键续跑。worker 启动和周期处理均在 recommendation 派发前执行发现。关联请求创建新增 source fence；尚未接受时不进入融合。

00105 持久订阅按 workspace/source_version/parse_projection/anchor/scope_version 去重，保留处理来源、画像版本、精确证据和各批请求 ID；来源或范围过期可记 STALE，暂缺画像/暂时失败使用有限延迟重查，不阻塞保存。metadata-only resolver 仍核验当前来源、精确投影和非生成来源，不能把同名标签当作授权。

验证：workflow 单元用例覆盖画像未就绪不读来源、33 个片段分两批、首批成功后失败的冻结重放、工作区/画像/片段漂移拒绝。真实 PG/River `TestAnchorRecommendationDispatchRunsThroughRiver` 通过（11.284s）：source-ready 实际派发与 processing 事实、持久 Profile owner 读取、混合 Redis/Cooking 文件中只选 Redis span、实际模型记录/推荐落盘、重复发现无重复请求、未批准无融合且正文不变，并保留取消竞态用例。Profile 模型生成在该测试中是显式持久夹具，不据此宣称外部模型画像生成已验证。application/workflow/owner/worker 范围测试和限定 vet 通过；项目迁移器已将独立验证库升级到 105，Atlas validate 通过并导出 schema。store 独立并发/状态测试仍在执行。

新确认的既有能力缺口：Capture 导入路径会生成画像，直接从工作区目录扫描得到的旧来源不一定有 Capture/Profile 记录。本切片会保持这些来源等待画像；仍需接入来源画像回填，不能将等待队列存在当作完整本地知识库自动分析完成。后续历史知识点快照固定、持久来源失效提示、正文依赖发布传播、目标生成初版和完整浏览器/外部模型验收仍在原批准范围内。

持久发现子任务已完成独立实库检查：`TestAnchorSourceDiscoveryPersistsFrozenProfileAndRequestBatches` 通过，覆盖冻结赢家、请求绑定、完成幂等回读及终态不回退；这是交错持有旧读快照的验证，不等同于多进程压力测试。最终 00105 还在数据库 trigger 中核验 REQUESTED 的请求身份、anchor/scope/source 元组和有序证据批次闭包。最终约束下混合文章完整 River 用例再次通过（22.591s）。新空验证库 `zhixu_anchor_schema_final_105` 由项目迁移器重放全部 105 个迁移并用于最终 schema 导出；未改写此前隔离库的迁移历史。发现完成收据写入失败时不计为已完成，新增重试用例证明已创建的请求继续幂等复用。

回归补充：新执行 fence 揭示旧 `createSuccessfulAnchorRecommendationModelRun` 夹具的租约只剩约 99 毫秒（共享基准已是一小时前），会随机被当作失效执行。已只修正该夹具租期为剩余一小时，保持生产 fence 不变；模型证明/伪造/旧范围用例复验通过（9.524s），限定 vet 与 diff whitespace 检查通过。

## 2026-09-15 目录文件画像回填（实施中）

目录扫描已保存的来源将复用 Capture outbox/Workflow/ProfileGenerator，通过有界后台回填补齐 Capture 和待分析画像；原始文件与来源身份保持，文件修改触发新来源版本的分析，历史画像不覆盖。worker 启动/周期入口和真实扫描→实际模型记录→画像读取→再次修改→历史保留的验证已开始接线，尚未完成实库验证。

00105 独立审查发现已分析画像的 parse projection 与发现记录不匹配时会永久等待。已将此分支持久标记为 STALE（ANCHOR_PROFILE_PROJECTION_STALE），与真正尚未分析分开；定向 AnchorDiscovery 用例通过。

目录回填已接入 worker 启动与周期调度，复用 Capture owner 的 GORM 事务、既有 outbox/Workflow 和 ProfileGenerator，没有新增迁移或第二套模型执行链。首次回填原子保存 Capture、PENDING Profile 和事件；已有文件内容更新仅在原任务全部终结后推进同一 Capture 的 latest source version，旧画像/证据保持。重复扫描不自动重试失败模型。候选查询在 LIMIT 前排除已有画像、已排队和仍在执行的来源，避免后续文件饥饿；source 锁后用新语句重读当前版本，避免锁等待前快照导致错误入队。

实际验证：`TestWorkerScannedSourcesBackfillProfilesAndPreserveHistory` 最终通过（21.479s），使用真实工作区扫描、生产 composition、PostgreSQL/River、RecordingChatModel 和固定 Provider，未预写画像。覆盖两次并发扫描只排一个请求、生成有简介/主题/知识点/证据的画像、同一文件修改后生成第二个版本且第一版读回完全相同、重复扫描零新调用、分页 2→1→0。此前首次实库运行发现 CTE 列别名缺失并已修复；分页和锁后重读修复后复验通过。Capture application/profile/workflow/postgres 原有测试及 worker 定向测试通过，限定 vet 与 diff whitespace 通过。该测试不证明外部真实模型语义质量，也不证明主笔记历史版本已经固定了知识点快照。

本切片完成代码与实库验证；00105 已有独立审查，新增 Capture 回填的独立审查尚未收口。主任务仍 in_progress，下一项优先补齐主笔记历史知识点快照绑定；其后仍有目标生成初版、来源失效持久提醒、正文依赖与发布传播和完整交互验收。

## 2026-09-15 主笔记历史知识点绑定

新增 00106，将 ProfileRevision identity 固定在不可变 synthesis_revision_source 行；已有引用沿父修订继承，新增引用只绑定同一 source version / parse projection 的 READY 或 STALE 画像。Capture 新增精确 GetProfileRevision owner 接口；历史知识点 HTTP 改为读取主笔记 owner 的保存绑定再读取该不可变画像。旧版本没有快照时返回 UNRECORDED，页面不再用当前画像冒充历史。OpenAPI 和生成客户端同步该状态，操作数量仍为 228。

实库 TestSynthesisKnowledgeSnapshotSurvivesProfileRebuildAndSourceRemoval 通过（8.307s，后续增加删除后重读与 NULL 绑定不借用当前画像断言正在复验）：真实生成主笔记/下一修订、画像 current 指针切换后旧知识点不变、引用继承、删除来源仍可读、工作区与来源隔离、拒绝修改历史绑定。Profile 数据在本用例为显式合法夹具，画像模型本身由上一切片 worker 实际生成测试证明。Capture/organizing owner/application/postgres 包测试、历史知识点 HTTP 测试及限定 vet 通过；Web 类型检查、2 个文件 37 个定向测试、定向 ESLint、OpenAPI lint/tags 通过。隔离库由 105 升到 106，Atlas validate 通过，schema 已从该库导出。

剩余重要缺口：现有 source-ready 合成可能早于 Capture 画像完成，导致新版本没有可保存的画像快照。当前不会伪造该绑定，但仍需在调度中补齐“先画像，后正文生成”的衔接，且等待一个来源不能阻塞同工作区其他已就绪来源。新增 Capture 回填和 00106 的独立审查尚待收口；主笔记目标生成初版、来源失效持久提醒、正文依赖发布传播、人工编辑和完整交互验收仍未完成。任务保持 in_progress。

历史绑定补充断言复验通过（8.432s）：来源删除后通过 service 重读仍返回原绑定；即使当前画像存在，历史 NULL 绑定也始终返回 UNRECORDED，不借用当前知识点。

## 2026-09-15 画像就绪后领取正文生成

生产 Worker 新增 ProfiledSynthesisOutbox 读取投影，在原 outbox 的 LIMIT 前筛选已有精确 source version / parse projection 画像的事件；未分析来源等待，其他已就绪来源继续。Workflow owner 提供精确事件领取并继续持有发布权；既有 processing 重放和派生来源排除不受画像等待限制。00107 的快照冻结读取已提交不可变画像，避免 mutable profile 重建/失败影响新引用绑定；父修订精确引用仍继承原绑定。未新增队列或模型执行路径。

验证：生产 composition 的 TestWorkerSynthesisCompositionImportsContinuouslyAndReplaysWithoutNewResults 通过（27.293s），实际阻塞首个 Profile Provider 时 processing 为零，放行后完成分析/合成，各轮保存引用的 profile 绑定均非空，重复内容仍不生成新正文。真实 PG 的 TestProfiledSynthesisClaimSkipsUnanalyzedSourcesInSameWorkspace 与历史快照测试通过（合计 17.797s），覆盖同工作区先到未分析事件不阻塞后到就绪事件、排除工作区、RUNNING 画像仍可使用不可变版本及历史保持。新增测试首次编译中误用 fact.ID，已修正为公开字段 EventID 后通过。owner/workflow postgres/worker 单元包与限定 vet 通过；隔离库由 106 升到 107，Atlas validate 通过并导出 schema。

本切片不证明每个生成片段都有知识点映射，也不证明外部模型质量；这两项仍待完整验收。新增回填与 106/107 独立审查正在进行。原批准任务继续 in_progress；目标生成初版、来源失效持久提醒、正文依赖及发布传播、人工编辑和完整交互验收尚未完成。

独立审查已收口：reviewer 核对 Capture 回填锁序/当前版本复验、00106/00107 历史绑定、LIMIT 前画像就绪筛选及 Workflow 精确 claim/publish 边界，未发现明确缺陷；未重复执行已完成实库测试，未覆盖手工篡改数据库后的恢复。最终限定 vet、Atlas migrate validate 和 diff whitespace 检查通过。

## 2026-09-15 来源失效持久复核提醒（部分完成）

新增 00108 / SynthesisSourceImpactReader 与有界 Reconciler，按已保存主笔记引用消费 core.source.removed_at 和相关 ingestion quarantine 事实，唯一键防重复；不可变观察绑定 workspace/note/source/version/reason。Worker 启动和周期接线，在模型排空前执行。新增 READ_LOCAL getSynthesisSourceImpacts，按所读 revision 映射完整引用及受影响 item IDs。Web 严格校验精确来源与条目集合，在主笔记所读版本上方默认显示原因、片段和历史引用链接；来源恢复后历史观察仍保留，刷新查询当前状态。

真实 PostgreSQL/文件发布的 TestSynthesisSourceImpactsPersistWithoutChangingPublishedContent 通过：实际生成并发布主笔记、共享来源两个笔记、并发有界对账、重复不新增、删除后发布正文和候选正文不变、刷新保留、恢复显示当前可用、工作区隔离、不可变记录和安全隔离不同原因。加入等待安全重查不能显示已恢复的断言后通过（14.119s）；此后又收紧历史 quarantine 的观察时间条件，最终复验结果待补。API/路由/worker 测试通过，Web 四文件 40 个测试通过，类型检查与定向 ESLint、Go vet 通过。OpenAPI 更新至 229 项，新增唯一数组使 generator manifest uniqueItems 从 50 调整为 51；初次路由固定数量断言和生成 manifest 漂移已同步修正。隔离库迁移到 108、Atlas validate 通过，schema 已导出。

本轮仅证明对已确认删除/隔离事实的持久提醒，未完成普通本地文件物理删除自动检测、短暂删除事件捕获、Artifact 缺失原因、主笔记列表汇总提醒、复核完成动作、补充来源账本的失效提示，以及删除后历史字节打开；不勾选整项来源生命周期验收。主任务仍 in_progress，目标生成初版、完整知识点覆盖、正文依赖发布传播、人工编辑和完整交互验收仍未完成。

本轮最终复验：加入 quarantine 发生时间不得早于该笔记首次引用的筛选后，实库用例通过（12.989s）；等待安全复查仍不可显示“已恢复”。API/路由/worker 定向测试通过，最终 Web typecheck、229 项 tags、Atlas 校验、diff whitespace 检查通过。真实 Chromium 固定资料核验桌面 1440 和手机 390：删除提醒→刷新后恢复仍待复核、受影响片段 hash 精确跳转、历史引用完整深链，手机无横向溢出；截图为 output/playwright/source-impacts-desktop.png 与 source-impacts-mobile.png。临时页面、专用浏览器与 5179 Vite 服务已清理。这不是生产文件删除/外部模型闭环。Go 自检已完成；本轮 source-impact 新切片尚无独立 reviewer 结论，不借用上一轮画像审查。

## 2026-09-15 删除或更新后读取历史原文

已将当前来源可用性与历史副本读取分离：SynthesisSourceView 新增可选 SnapshotText / snapshot_text；只从原受控 ContentArtifact 读取，复核完整元组、原 Artifact hash 和精确 excerpt hash。Source 已删除或新版本出现时保留 UNAVAILABLE/STALE 标记，同时可展示已验证历史片段；隔离来源、缺失或篡改副本仍不返回内容。来源正文与后补证据 HTTP、OpenAPI/生成客户端、Web 严格解码和两个弹层已同步。现有当前 Text 联合保持，历史内容不进入当前生成输入。

真实 PG + 真实受控副本 TestHistoricalSourceReadsManagedBytesAfterOriginalRemoval 通过：从主笔记 service 精确打开、物理删除原文件后仍返回旧原文、重复读不漂移、原文件恢复并注册新版本后仍返回旧片段、篡改副本拒绝、隔离后拒绝、主笔记 revision 不变。删除 tombstone 在该用例显式写入，不能据此认定自动本地删除检测已完成。与持久提醒用例合计通过（17.525s）。application/owner/http/agent/worker/app 定向包测试通过；Web API+主笔记页 40 个测试、类型检查和定向 ESLint 通过；OpenAPI lint/tags（229 项）及 Go vet 通过。两次验证命令曾因 cwd 错误找不到路径，已从正确目录重跑成功。

真实 Chromium 固定资料检查显示历史片段与来源不可用提示并存，390 像素无横向溢出；截图 output/playwright/historical-source-mobile.png。这项页面检查使用固定响应，真实文件读取由上述实库用例证明。临时页面/浏览器已清理。自检另修正 00108 读取提醒的当前隔离判断，使其与来源 owner 的“该精确版本存在隔离记录则禁止读取”一致，不能仅凭后续检查结果宣称该历史版本已恢复。

仍待：普通本地路径缺失检测及并发恢复保护、短暂删除事件、列表汇总提醒/补充来源失效提示，以及本切片独立审查。目标生成初版、完整知识点覆盖、正文依赖发布传播、人工编辑和完整产品流程仍在原任务范围；主任务保持 in_progress。

## 2026-09-15 普通本地文件缺失检查

已补齐 Worker 启动/周期中的有界本地来源存在检查，运行在 Capture 回填及来源影响对账之前。Workspace owner 负责 Source tombstone，filesystem port 只检查授权根目录下的已登记相对路径；不读取正文、不新增基础设施或迁移。每批最多 100，keyset 越过仍存在或被排除的路径，避免后页饥饿。Source 行锁内重新检查最新版本与原路径，锁与正常登记精确重放互斥；根不可用、权限失败、符号链接均不被当作文件删除。

真实历史来源测试已移除显式 SQL tombstone，改为实际删除原文件并调用生产 Workspace presence owner。自动标记→持久提醒→历史片段读取→新版本及同内容恢复链路通过；同内容重新登记在检查持锁期间超时，释放锁后正常登记清除 tombstone，复核提醒仍保留。该版本测试通过（10.949s），额外加入“第一页仍存在、下一页删除”的分页断言后正在复验。filesystem 边界测试覆盖正常/不存在/不存在父目录、断链/越界链接、非目录父路径、保留目录、URL/越界输入和根目录缺失。worker/workspace/runtimegrant 包测试、限定 vet 与 diff whitespace 检查通过。

00108 与历史快照读取已由 recommendation_review 独立审查，未发现明确缺陷；该结论不覆盖本次新加的 presence 检查。仍保留短暂删除事件、来源恢复的自动重新扫描、列表汇总提醒、补充来源失效提示的缺口；未将这些推断为已验收。原任务其他未完成项保持，task.status 仍为 in_progress。

本切片最终验证：加入首个已存在来源和后续缺失来源的实际 keyset 分页后，完整历史来源/并发恢复实库测试通过（11.232s）；filesystem、Workspace postgres/runtimegrant、worker 包测试再次通过。历史原文 Web 类型检查的遗留运行也已确认 exit 0。没有更改本切片以外的用户规范文件，无提交或部署。

## 2026-09-15 目标初版生成的元数据候选目录

新增 Capture owner `ProfileDirectoryReader.ListProfileDirectory`，每页最多 32，按 Source ID keyset 获取当前已分析版本；Source 删除/隔离/新版本及精确解析证明在 LIMIT 前筛选。复用 GetProfiles 的三条集合查询，在同一 repeatable-read 只读事务中读取 Profile、不可变 Revision 和 evidence。正在重建的画像可以使用已经提交的同版本 Revision。没有加载原文或 canonical chunk content。

Organizing `SynthesisGoalCatalogReader` 从该只读 owner 生成带不可变知识点 locator 的目录项，复用 Source metadata resolver 再核验当前性及派生来源；剔除后的空页仍保留上游游标，真实查询失败不伪装成空结果。只有后续 AI 根据目标选择了知识点，才解析对应精确原文证据。没有引入标签直接归属或关键词代替 AI 判断。

`TestGoalCatalogPagesCurrentMetadataWithoutOpeningOriginals` 实库通过（9.901s）：移除全部测试 Artifact bytes 后仍可分页读取 Redis/Oracle 简介与知识点，跨工作区隔离，画像重建保留原点位，删除早期来源不阻挡后续页，新版本未画像不使用旧画像，隔离排除，历史 locator 仍可精确读取。首次夹具以 NULL Artifact 注册新版本被实际 Schema 拒绝，已改为真实合法 Artifact/Version 绑定后复验；生产约束未修改。

Capture/Organizing 相关 application/postgres/owner 测试通过；元数据查询及 owner 自检已完成，新增 skip/cursor/错误与精确证据测试和定向 vet 正在收口。下一步见 research/goal-initial-generation.md：独立持久目标请求、AI 知识点选择、多来源冻结与原合成流水线、页面入口仍待实施，不能将目录读取算作初版生成功能完成。任务仍 in_progress；其他已批准范围不缩减。

本目录切片最终检查：`TestGoalCatalogSkipsChangedSourceWithoutLosingCursor` 通过，验证失效/派生来源剔除后游标保留、基础设施失败传播、替换证据拒绝；Capture application/postgres 和 Organizing application/owner 的定向 vet 通过，diff whitespace 通过。本轮没有 Web/API wire 或迁移变更，因此未重复无关的前端/全仓验证。

## 2026-09-15 目标请求与不可变目录批次

新增 00109 synthesis_goal_request / synthesis_goal_catalog_batch / synthesis_goal_catalog_item。请求固定用户目标和 workspace+幂等键哈希；同键不同目标拒绝，响应丢失可重读同一身份。目录按批固定精确 SourceVersion/Artifact/ParseProjection/ProfileRevision/title，不保存原文字节；并发枚举返回首次提交的批次。批次、条目闭包和父请求游标在同一事务完成，延迟约束拒绝只提交孤立批次头，历史行拒绝更新/删除/截断。

生产 Worker 已接 metadata-only SynthesisGoalCatalogDispatcher，复用当前 RootGrant 的 Active Workspace 和现有周期。每次最多推进 4 个目标，各读取最多 32 个目录项；失败保存稳定错误码与 30 秒退避，其他目标继续。目录完成只标记 CATALOG_READY，绝不当作已生成/已发布。恢复使用持久游标；过期失败写入不倒退已推进请求。

真实 PG TestSynthesisGoalPersistsIdempotentCatalogAndResumes 覆盖并发幂等创建/冻结、不同目标键冲突、跨工作区隔离、重新构造 store 后续接、错误画像绑定回滚、删除来源后冻结批次不变、不可变表保护、暂时失败退避且后续目标继续、恢复清除错误及迟到失败拒绝。更新失败退避后通过（8.888s）；再补孤立批次 deferred constraint 断言的最终结果见下文。第一次 dispatcher 用例的夹具工作区为 inactive，已显式激活目标工作区后通过；未绕过生产 Active Workspace 检查。application/postgres/workflow/worker 包测试及定向 vet 通过。

独立验证库 zhixu_anchor_schema_final_105 已由项目迁移器升级到 109，schema.sql 已从实际库导出并保留既有受控 runtime 权限尾部，Atlas migrate validate 通过。导出后多余 EOF 空行已修正。此批没有 HTTP/UI 或模型 Schema 变更；目标请求仍是内部能力，不能据此宣称用户已能生成初版。AI 知识点选择、ModelRun/工作流证明、多来源正文生成、目标输入和普通来源转主笔记入口继续待实施。新增 presence/catalog/00109 尚缺独立 reviewer 结论，旧 00108 快照审查不覆盖它们。任务保持 in_progress。

00109 最终实库复验通过（10.485s），包含孤立批次不能提交的延迟约束断言；最终 diff whitespace 检查通过。没有提交、push 或部署。

## 2026-09-15 目标选择的冻结输入与严格输出

新增按 00109 冻结 ProfileRevision 重读目录的 owner 接口；新增目标选择输入分片与局部 P 标签映射、严格输出解码和服务端知识点绑定。每片最多 32 点、实际 JSON 不超过 256 KiB，按预算继续拆分全部知识点；模型只收到目标、标题、简介和知识点文字。点位/Source/Profile identities 不作为模型控制字段发送，返回值不允许指定 UUID、来源 ID 或发布字段。明确空选择允许，重复/未知/超出本批标签拒绝。prompt/schema 的 RuntimeCatalog 定义已完成，但生产 ModelRun/Workflow 执行尚未接线。

验证：application/owner/agent 包测试通过；512 个知识点（含知识点与例子）的普通及极端 JSON 转义字节用例均完整覆盖，无截断/重复；调用方修改 span slice 不改变分片，严格输出校验拒绝伪造控制字段/重复键/未知标签。真实 PG 目标恢复测试增加 Profile current pointer 更新后，从旧目录批次重建旧知识点且原文字节不可用的断言，通过（7.861s）；原并发/恢复/退避/不可变约束断言继续通过。定向 vet 正在完成，未改 Web、OpenAPI 或迁移，不重复无关检查。

仍待生产选择请求、真实 RecordingChatModel/Eino 调用证明、选择结果落库、所有分片汇总、多来源原文验证与初版生成，及用户页面入口。该切片只证明选择契约和历史输入还原，不证明 AI 已执行选择或其语义质量。主任务保持 in_progress，其他已批准验收不缩减。

选择契约最终限定 vet 和 diff whitespace 检查通过。没有提交、push 或部署。


## 2026-09-15 持久知识点选择与真实模型调用证明

新增未发布迁移 00110：每个目录批次拥有一次性封存的 selection manifest；分片绑定批次/来源序号/点位 offset/count 与实际 payload hash，目录中所有知识点必须连续且完整覆盖才能提交。数据库延迟约束校验条目总数和画像知识点/例子总数；封存后不能追加或修改分片身份。Prepare 并发读取首次提交身份，失败不留下半个 manifest。选择状态 PENDING/RUNNING/SUCCEEDED/FAILED/RECOVERY_REQUIRED 独立于目标目录完成、正文生成和发布。

新增 GORMGoalSelectionStore 与 GoalSelectionModel，执行输入由 owner 重读不可变 ProfileRevision，模型只收到本分片元数据；调用前 CAS 领取并固定实际 ChatRequest hash 与 Workflow node attempt。复用 RecordingChatModel/Eino StructuredRunner，新增独立 goal-point-selection Schema/ResultType 和 ModelRun 数据库精确 running selection 约束。完成时重建冻结点位、严格绑定 P 标签，锁定 Workflow 执行和有效租约，核对真实 ModelRun、所有 ModelCall、首请求 hash、末响应 hash/长度及原始输出字节。取消后的迟到结果不能变成有效选择；不创建或发布正文。

本轮实库首次运行发现画像 examples 可以合法保存为 JSON null，覆盖约束直接取数组长度会失败。已在尚未发布的 00110 中改为按空数组处理，不修改画像事实。重新创建隔离验证库 zhixu_anchor_schema_final_110，从项目迁移器完整应用最终 110 并导出 schema.sql，保留原 runtime grants；旧隔离库 final_105 不再作为该切片 schema 导出来源。Atlas hash/validate 通过。

真实 PG + RecordingChatModel/Eino 的 TestGoalSelectionPersistsActualModelProofAndRejectsLateResults 已通过，覆盖并发准备、工作区隔离、冻结字段与封存保护、实际 Provider 字节证明、有效/空选择、超出本分片的标签拒绝、伪造输出不能冒用真实调用、Workflow 取消后输出不写入选择、重复完成与成功读取零新调用。补充两执行者并发领取后，与原目标目录恢复测试合计通过（17.556s），只有一个执行者调用 Provider。分片漏点导致事务回滚的额外断言正在最终复验。

独立审查覆盖上一批 presence、目录读取和 00109，发现一项：目录超时后使用已取消 ctx 写 Defer 导致退避丢失。已改为 5 秒 bounded WithoutCancel 专用恢复上下文；结束批次前检查取消，避免继续污染尚未处理目标。真实目录测试新增实际 deadline 后 error_code/版本/30 秒退避落库断言，包含在上述通过结果中。该审查不覆盖新 00110 与选择模型；新切片已按 Go Review 做自检，尚无新切片独立 reviewer 结论。

相关 Go domain/application/agent/postgres/workflow 包测试与定向 vet、diff whitespace 通过。当前选择执行由生产 adapter 和 owner 在实库测试调用，Workflow 行是合法持久执行夹具；不是生产 River 调度或外部真实 Provider 验收。下一步仍须注册生产 RuntimeCatalog/Workflow definition、持久 manifest 生产调度、失败/终结工作流对账和显式重试，然后汇总所有分片选择、读取多来源原文并创建初版候选，以及目标输入/普通来源转主笔记页面。主任务仍 in_progress，其他批准验收不缩减，无提交、push 或部署。

本切片最终实库复验通过（9.541s）：加入漏掉首个知识点的 manifest 必须整事务回滚、之后正常 Prepare 可以恢复的断言；并发模型领取、取消、原始证明等仍全部通过。收紧 adapter 的领取返回值检查为精确 version+1 和 scheduled identity 后，agent 包测试、限定 vet 与 diff whitespace 通过。当前没有遗留测试进程。


## 2026-09-15 目标选择生产调度与显式重试

生产 Worker 已注册 goal-point-selection 的独立 Workflow definition、executor 和 RuntimeCatalog；静态启动和 managed model generation 重建均接入同一 owner。周期调度先完整准备不可变目录批次，再在原 UoW 中领取分片、Workflow StartScoped/River 入队和 schedule binding。启动/周期均经过已有 modelDrain producer gate，并使用独立 DB timeout，避免前面的 source-ready 工作耗尽本步预算。readiness 同时核验实际注册的目标选择 executor/definition。

新增 00111 保存准备失败的有界退避和显式 retry receipt。目录批次失败 30 秒退避且不截断其他目标；已完成/封存批次不重复准备。工作流终结后的对账将从未开始模型的 PENDING 转为可重试 FAILED，将执行过但没有确切结果的 RUNNING 转为 RECOVERY_REQUIRED。显式重试仅接受已知 FAILED+retryable 且旧工作流已终结的精确版本，保留同一 selection 身份、清除旧 schedule/attempt，要求同事务不可变 receipt；同键不同命令拒绝，旧工作流迟到失败不能改写新执行。未新增自动重跑模型机制。

真实 PostgreSQL/River TestGoalSelectionDispatchRunsThroughRiverAndRetriesExplicitly 通过（10.173s）：内部创建目标→生产 catalog dispatcher→自动准备 manifest→真实 Workflow/队列；启动前通过真实 Coordinator 取消→对账；显式重试后 Provider 失败→持久失败且不自动重试；再次显式重试成功→重复调度/响应丢失读取均零新调用。全流程只发生两次预期 Provider 调用，未生成任何正文。Scope/profile 为现有合法数据库夹具，模型为固定 Provider，不能据此声称外部模型质量或用户页面初版生成已验收。

Worker/organizing application/workflow/postgres/agent 包测试、限定 vet、Atlas validate 和 diff whitespace 已通过；生产 composition 的实际启动/原 Capture+Synthesis 流程正在最终复验。隔离验证库 zhixu_anchor_schema_final_110 已通过项目迁移器升级至 111，schema 已重新导出并保留 runtime grants。当前仍缺新选择切片的独立 reviewer 结论，未借用之前 presence/catalog 审查。

下一步是将所有成功分片汇总为目标来源/知识点集合，验证多来源原文并接入原合成初版候选与发布审批；目标输入/普通来源转主笔记入口、无资料与待画像区分、实际正文依赖传播、完整人工编辑保护和最终交互验收仍待完成。task.status 保持 in_progress，无提交或部署。

本轮最终生产 composition 复验通过（23.737s）：使用真正 newWorkerComponentsWithModels 构造器和注册表，readiness 包含目标选择 definition/executor，原 Capture/Profile/Synthesis 实际流程继续通过。该检查证明静态生产组装与原链路兼容；managed generation 重建路径已接线并通过编译/vet，未在本轮重新进行热切换实跑。无遗留测试会话，最终 diff whitespace 通过。

## 2026-09-15 选择结果汇总与来源材料准备

新增目标选择结果只读投影：Progress 在所有目录批次都封存且所有 selection slice 为 SUCCEEDED 后才 Ready；PENDING、FAILED、RECOVERY_REQUIRED 和明确空选择保持可区分。结果按 selection ID 游标分页，空 selections 也占用游标，不会因为首个分片没有选点而丢失后续来源。读取时从冻结 ProfileRevision 重建输入并严格绑定 P 标签，读取不会重新调用模型。

新增目标材料 owner：按完整选择结果分页，按 SourceRef 精确合并共享原文片段，同时保留每个 selection/model run/knowledge-point locator/reason 的绑定；每批最多 256 个 span，超出继续分批，并拒绝重复、替换或缺失的 resolver 结果。新增 preparer 会在生成前逐一 OpenSynthesisSource，重新校验当前来源、文件版本、解析投影、内容 hash 和 excerpt hash；来源不可用或完整材料超过输入预算会显式失败，不能用部分材料生成。

真实 PG 验证 TestGoalResultsWaitForAllSelectionsAndPreserveEmptyPageCursor 通过：目录完成或 manifest 准备不提前暴露生成输入；三个选择分片全部成功后，空选择页、后续选点、精确来源/span/profile 绑定和跨工作区/伪造游标拒绝均通过；读取结果不产生额外 Provider 调用，仍未创建主笔记。owner 单元测试覆盖共享 span、257 个证据分批、空选择游标、未就绪短路、来源错误及 resolver 缺漏/替换拒绝。

独立审查发现的 P1 已修复：模型调用前上下文取消造成的 PENDING selection 现在保存为 retryable FAILED，可通过显式 retry receipt 恢复；已进入模型调用的 RUNNING 任务仍保持 RECOVERY_REQUIRED 语义。真实 River 调度测试加入取消前领取回归，证明取消时 Provider 调用数为零、显式重试后继续原有失败/成功流程。Organizing application/owner/postgres/agent/workflow 定向 test、vet、integration test 和 diff check 通过。

仍待：将准备好的多来源材料接入现有 SynthesisGenerationInput 的独立目标触发与一次稳定目标初版候选，完整应用/语义审查/发布绑定；普通来源转主笔记、正文依赖传播、HTTP/UI 入口及真实外部模型验收未完成。

## 2026-09-15 目标生成边界修正与真实验证

重新检查中断后的代码发现此前记录存在高估：仅增加 GoalRequestID 仍会被旧的多来源校验拒绝；目标文本没有发送给生成器；额外 Coordinator 没有调用独立语义审查器；持久 store 的 LoadSynthesisExecution 也尚未装载目标身份。之前普通包测试通过不能证明这些能力已实现。已移除未使用的 Coordinator/GenerateGoal 端口和 BuildSynthesisGoalGenerationInput 捷径，改为扩展原有四阶段 executor。

当前已实现：SynthesisGoalBinding 保存目标请求 ID、目标文本、全部精确 SourceRef 和 selection/model-run/ProfileRevision/point/reason 绑定。输入必须与材料白名单完全一致；同一片段跨多个选择分片的知识点可全部保留（最多 512），不再以单分片 32 点截断。工作流冻结哈希包含目标与知识点绑定，nil goal 省略，原有输入哈希保持兼容。目标初版结果限制为一份新主笔记，不能静默输出多篇或空正文结果。

SynthesisExecutor 新增目标 prepare 分支：调用完整材料 preparer，冻结无原文字节的绑定；重读目标输入时只 OpenSynthesisSource 打开选中的精确片段，不读取其他模块，历史 SnapshotText 不用于新生成。prepare 成功仍不允许绕过原 generate→validate→apply 图；队列 GoalRequestID 与加载结果/冻结绑定不符时直接拒绝，显式 null/空/非法 ID 也拒绝。

Provider 新增独立 v3 生成与审查 prompt，v1/v2 原文不变。生成器和独立审查器都接收 user_goal，所有选中来源均为 incoming；控制身份和原文哈希不发送给模型。生成和审查的 ModelRun proof 按冻结 goal 要求 v3，不能冒用旧 prompt。生产 owners 已构造 goal material reader/preparer，静态及 managed executor 组装复用该实例。

已验证：application 多来源白名单、缺失/替换证据、跨工作区、空选择、多篇结果、40 个共享片段知识点绑定测试通过。Workflow JSON 冻结/重建后仅重读两个选中原文、重复 prepare 不重做分析、目标变化使哈希失效、历史快照拒绝，以及没有独立语义审查不应用候选的测试通过；此项使用测试 store，不是数据库持久目标调度验收。实际 RecordingChatModel/Eino 测试中生成与审查各有独立 ModelRun，均收到同一目标和两来源，合成一份候选并按 v3 记录；Provider 为固定响应，不能证明外部模型语义质量。

真实 PG TestGoalResultsWaitForAllSelectionsAndPreserveEmptyPageCursor 已扩展到实际 preparer：原始 Artifact 副本缺失时返回错误且无部分材料；恢复副本后按已选结果打开两个原文，校验 hash 与知识点绑定，并构建完整 goal binding。通过（10.307s）。相关 application/workflow/owner/agent/synthesispostgres/worker 测试与定向 vet 通过。生产 composition 的原 Capture/Synthesis 流程正在复验。

仍待：目标到 processing 的独立持久关联/去重键、完整选择证明在 freeze/apply 的持久复核、加载/重试身份延续、生产目标调度器和候选结果链接。当前只在测试提供 GoalRequestID 的执行事实；生产 Store 尚不能装载该身份，不能宣称目标初版已可由用户发起。后续仍须完成普通来源转主笔记、HTTP/UI、正文依赖传播、人工编辑保护及完整产品验收；任务保持 in_progress。

本轮最终生产 composition 复验通过（28.415s）：真实 Worker owners 构造了目标材料 reader/preparer，原 Capture/Profile/Synthesis/River 导入与重放流程继续通过。该结果证明生产组装和原链路兼容，不证明目标 processing 已持久启动。补充 v3 模型证明必须与冻结目标 prompt 匹配的测试；最终定向 vet 和 diff whitespace 检查通过。没有迁移、提交、push 或部署。

## 2026-09-15 目标生成持久身份与选择证明

新增 00112：synthesis_processing 通过 workspace 外键关联 goal_request_id，并对 goal 唯一；目标与 fusion 互斥。目标 processing_key 固定为 workspace+goal 的 SHA-256，不依赖来源种子；原 source/fusion 键保持兼容。processing 仍引用真实 source-ready 事件，数据库要求目录完成、全部 manifest 封存、所有 selection 成功且种子来自实际选中来源。目标初版不允许 SKIPPED/NO_CHANGE 或多份 revision。队列输入、processing、冻结目标 ID/文本由数据库再次交叉检查，重试不能改换身份；现有唯一 apply receipt 复用同一 processing，无额外重复 goal 外键。

SynthesisProcessing、加载、创建和显式重试已贯穿 GoalRequestID；ApplyGeneration 使用目标独立键。新增只读 GoalSelectionResultReader 和独立历史 profile snapshot reader，避免证明器构造依赖正文 writer/文件读取的循环。API/Worker runtime 构造时均接入完整选择证明器。freeze 前和 apply 的既有验证 fence 中，逐页读取不可变成功选择，精确双向核对目标文本、selection/model-run/source/profile/point/reason/span；缺失、替换、额外片段或不完整游标都拒绝。证明过程最多 16,384 个分片，超限明确报错，不截断。原文可用性和 excerpt hash 继续由原文 owner 与 apply SourceFence 负责，选择证明本身不替代原文核验。

实库 TestGoalResultsWaitForAllSelectionsAndPreserveEmptyPageCursor 升级至 112：实际选择模型证明完成后，通过生产 Store 创建/读取目标 processing、检查跨工作区查询、冻结/重读完整 GoalBinding。改动合法 reason 并重算 frozen hash 仍被持久 proof 拒绝；重复冻结返回原快照，goal 身份不能清空。通过（12.541s）。此处 Workflow/attempt 是合法数据库夹具，不代表目标生产调度或 River 目标生成已验收。

生产 Worker 原 Capture/Profile/Synthesis/River composition 回归通过（29.537s），包含新只读证明器的生产构造。目标 key 的不同种子/不同目标/普通 source 兼容测试、application 证明器边界测试、相关包测试与 vet 通过。隔离数据库 zhixu_anchor_schema_final_110 已通过项目迁移器从 111 升级至 112，schema.sql 从该库重导出并保留 runtime grants；Atlas hash/validate 与 diff whitespace 通过。独立审查正在进行。

仍待目标生成生产 dispatcher/状态查询及候选链接，目标失败后真实 retry→River→独立审查→候选的完整实跑，以及目标输入与普通来源转主笔记页面。正文依赖传播、人工编辑保护等原 PRD 要求保持未完成，不因该持久化切片通过而缩小验收范围。无提交、push 或部署。

独立 Go/SQL 审查发现 1 个快照容量问题：合法的 512 点理由集合可能超过数据库 JSONB 的 1 MiB 上限。已在目标 FrozenInput.Validate 增加完整 JSON 序列化预算检查；采用含缩进 JSON 作为 JSONB 分隔符膨胀的保守上界，超限返回不可重试的 SYNTHESIS_MODEL_INPUT_TOO_LARGE，并让 Freeze 原样保留该错误码。没有删减知识点。定向测试证明 512×2048-byte 合法理由集合明确拒绝，32 点版本可正常冻结；实库测试补充相同错误在生产 Store 边界不会变成通用约束错误。审查其余范围未发现已证实的问题。旧普通来源的显式 Retry/River 幂等实跑通过（8.679s）；目标专用重试完整实跑仍待 dispatcher 落地后验收。

修复后的最终实库复验通过（11.871s），包括 Store 返回明确且不可重试的容量错误、合法输入仍可冻结及重放。相关定向 vet 与 diff whitespace 通过；当前无遗留验证进程，任务保持 in_progress。

## 2026-09-15 目标初版生产调度与真实 River 闭环

新增 GoalGenerationDispatcher，复用原 organizing.synthesis-note 四阶段图。先按活动 Workspace 获取现有 intake advisory lock，再领取完整筛选且尚无 goal processing 的目标；StartScoped/River 入队与 CreateSynthesisProcessingScoped 同事务提交。目标生成只保存独立 goal 身份和一个真实选中来源事件作为 provenance，完整材料仍由 prepare 重读；不发布或重放该来源 outbox。领取 SQL 在 LIMIT 前排除未封存、未成功、全空选择和已有 processing 的目标，按目标 created_at/id 与目录批次/来源序号稳定选择。

Worker 启动与周期 producer gate 内接入目标生成，并给予独立 DB timeout；用户目标先于普通 source-ready 自动整理获得当前空闲的生成槽位。新增 readiness 要求实际 goal dispatcher 已构造。dispatcher 不保存模型实例，managed 模型重建继续复用原 owner 和 executor 组装路径。没有新增工作流系统、迁移或发布行为。

TestGoalGenerationDispatchesThroughRiverAndRetriesOneCandidate 在真实 PostgreSQL/River/Workflow/RecordingChatModel/Eino 上贯穿：创建目标→生产 catalog dispatcher→生产 selection dispatcher→全部成功→生产 generation dispatcher→prepare/generate/独立 validate/apply。最早的目标有两个明确空选择，不生成 processing，也不阻挡后续有效目标。有效目标来自两个真实文件；模型暂不可用时保存 FAILED/retryable，重复调度不重试；显式 RetryProcessing 及同键重放保留同一 processing/goal 身份，新执行完成后只保存一份含两来源引用、独立审查已通过但尚未发布的候选。两次并发 generation dispatcher 合计只启动一个工作流；完成后的多次调度及读取没有额外调用，processing/apply receipt 各一条，两个原始 source-ready outbox 仍未被目标调度消费。Provider 固定响应，画像为合法实库夹具，不能声称外部模型语义质量或用户 UI 已验收。

独立审查未发现该生产调度切片的新 P1/P2。主 agent 的额外锁顺序复核发现：普通 source consumer 先锁 outbox 再等 workspace，目标持有 workspace 后插入 processing 时的外键可能反向等 outbox。已在实际 provenance seed 查询增加 FOR KEY SHARE OF event SKIP LOCKED，以非阻塞共享锁保护后续 FK；不修改 outbox 内容。上述 River 测试加入真实事务独占锁住来源事件的交错，在事件锁释放前目标调度立即返回零启动，之后正常生成。最终实库复验通过（12.118s）。

生产 Worker 原导入/画像/合成/River composition 回归通过（27.972s），包含新增 dispatcher/readiness 组装。相关 Go 包测试、vet 和 diff whitespace 通过。没有遗留验证进程。

下一步仍为目标 HTTP/UI 入口、可解释的阶段/失败/无相关资料与待画像状态、候选结果链接及用户重试操作；普通来源转主笔记、正文真实依赖传播、完整人工编辑保护和最终产品验收仍待完成。目标初版已具备后台真实闭环，尚不能称为用户可用的完整产品功能。任务继续 in_progress，无提交、push 或部署。

## 2026-09-15 目标创建、进度、显式重试与候选入口

主笔记中心已接入目标创建与服务端历史目标列表。用户只描述希望整理的内容，不填写标签、逐篇简介或手工纳入规则。新增五个 HTTP operation：创建/分页列出/读取目标，分页列出一个目标的全部筛选记录，以及按精确 selection/version/幂等键重试。生成失败继续使用已有 processing retry，保持同一目标和候选身份。API 生产构造器已接入真实 goal store、只读 view reader 和 selection store；认证 capability、运行路由清单、OpenAPI 与生成客户端同步完成。

GORMGoalViewReader 在同一 repeatable-read 只读事务中批量读取请求、完整选择进度、成功选点数和 goal processing，避免逐目标查询。候选链接只从 SUCCEEDED processing 的唯一 revision_ids 和同工作区 synthesis_revision 精确关联，不能用笔记当前版本替代。读取不打开原文、不请求模型、不重启生成。HTTP 仅输出可见进度和稳定身份，不暴露模型输入输出、节点尝试或选择 payload。目标列表使用创建时间/ID 的签名 keyset cursor；筛选记录使用完整 after_id 分页，空成功选择不会阻挡后页失败重试。

目录冻结前新增 readiness 检查：当前原始来源尚未完成解析/画像时延后，不将 ProfileDirectory 中缺失的条目认定为无匹配。当前 SourceVersion 已登记但 Capture 尚未追上时同样等待；不要求先有 Capture 才纳入检查。已删除、隔离、生成笔记来源继续复用 owner 排除规则。解析失败、画像失败和画像能力不可用保存不同延后码；PENDING/RUNNING 画像仍保留已提交同版本 revision 时允许使用。readiness 放在目录读取之前，避免“先读到空目录，画像紧接着完成，随后判断 ready”造成假空结果。两个 owner 查询仍不是统一事务快照；并发新增/替换来源的严格快照边界尚未做完整验收，不宣称全文件监听已完成。

前端新增 synthesis-goals.ts 严格边界和 SynthesisGoalsPanel。创建响应丢失时保留原文本与幂等键，读取列表恢复事实；错误文案不断言命令未保存。筛选失败只对 FAILED+retryable 展示显式重试，RECOVERY_REQUIRED 保留人工恢复说明。候选链接带确切 revision_id；列表显示“候选已生成”，不据此推断其当前发布状态。修复“选择已就绪、processing 尚未出现”时停止轮询的问题；失败/人工恢复终态不自动重做模型。资料待分析和已知分析失败有不同文案及收件箱入口。

验证证据：

- 实际 PostgreSQL + River 的 TestGoalGenerationDispatchesThroughRiverAndRetriesOneCandidate 已从真实 HTTP 创建目标（含幂等重放），经过生产后台筛选/生成/独立审查；HTTP 读取首次失败、HTTP 显式 retry、读取最终唯一候选及刷新重复读取、筛选分页与跨工作区/跨目标拒绝均通过。真实 Provider 调用仍为预期 6 次。Provider 为固定响应，不证明外部模型内容质量。
- 目录实库验证覆盖等待首版画像、保留 revision 的画像重建、画像失败、解析失败，以及新 SourceVersion 尚无匹配 Capture。最新改动的两条实库流程已安排复验，结果见后续记录。
- 新 HTTP 严格输入/错误绑定/空选择完成/精确 retry 测试及 organizing HTTP race 通过；app/httpapi/auth HTTP race 通过。API 目标构建及限定 vet 通过。OpenAPI lint、项目检查、234 个 operation 的精确路由和 tag 清单、客户端生成通过；前端 typecheck、generated typecheck、相关 ESLint 与目标 API/组件测试通过。
- Playwright 在真实 Chromium 中使用临时固定接口数据，操作创建→异步更新→候选→刷新恢复，核验精确候选 href；1440/390 像素截图已人工查看，390 像素 viewport 下 scrollWidth=375，无横向溢出。只出现临时页 favicon 404，无组件异常。此项证明组件交互，不能替代真实 API/Worker/Web 同场联机验收。临时页面与 Vite/浏览器进程已清理；截图在 output/playwright/goal-entry-{desktop,mobile}.png。

整体任务仍为 in_progress，无提交、push、发布或归档。仍待：普通来源转主笔记、实际正文依赖随上游发布传播、完整人工编辑保护、文件新增/恢复的持续发现、完整产品联机与外部模型质量验收。目录批次准备失败目前已有后端退避，但目标页面尚未单独展示这一细分错误，后续需补齐；本轮不能据此声称所有失败状态和 PRD 验收已完成。

本切片最终实库复验通过（19.333s）：包含新版本尚无 Capture 的等待修正、解析失败分类，以及 HTTP→River→唯一候选/重试全流程。最终限定 vet 与 git diff --check 通过。当前没有本切片遗留的浏览器、Vite 或测试进程；整体目标保持 active。

## 2026-09-15 单来源建立主笔记与准备失败可见性

资料详情的已完成知识画像增加“从这份笔记建立主笔记”。用户无需填写主题、标签或纳入规则；服务器根据原标题构造保留主题、用途、适用条件与模块语境的整理目标。此入口以原笔记为来源生成可审阅的新主笔记候选，保留原文件；不是原地将任意原文无损改成主笔记。候选之后沿用现有发布审阅与 AI 锚点范围设置。混合模块是否完整保留、完整范围审阅到持续融合的产品流程仍待最终验收。

新增 00113 synthesis_source_promotion receipt。来源版本、ProfileRevision、目标请求和唯一目录批次在一个事务内绑定：先验证当前原始来源，再创建目标、冻结恰好一个来源的目录并记录不可变 receipt。后台不会先看到一个尚未冻结、可能扩展到全库的普通目标。ProfileDirectory 与 readiness 复用查询新增可选精确 SourceVersion 过滤，提升单篇笔记不会等待无关来源的分析。重复同键命令首先读取持久 receipt；即使原文件之后删除，也返回原目标；同键换来源或跨工作区不能重放。

API 新增 POST /synthesis/sources/{source_version_id}/promote，严格空对象请求与 Idempotency-Key。生产 API composition、能力鉴权、OpenAPI、235 个 operation 的路由与 tag 清单、生成客户端同步完成。前端保留一次操作的幂等键，响应丢失时提示“整理请求尚未确认”，重试原命令；保存后提供主笔记中心进度入口。

独立审查发现内部目标键与普通目标共享命名空间，可预测映射可能被普通请求预占。已改为事务内随机 UUID 内部键，重放由 promotion receipt 负责；回归测试先用原可预测键及完全相同目标文本创建普通目标，再并发提升同一来源，证明普通目标不被误用且不阻塞提升。随机键生成失败仍回滚整个事务。

同时补齐目录准备失败的只读进度：未封存批次的 preparation_failures / preparation_error_code 从真实 preparation 记录汇总。封存后旧错误不再显示；页面明确提示“资料目录准备受阻”，保持原有后端有界退避，不自动重跑失败模型。

验证证据：

- TestSourcePromotionFreezesOneSourceAndReplaysWithoutReanalysis 在真实 PostgreSQL 上通过（修复后 13.138s）：并发去重、普通目标预占回归、HTTP 严格请求与 202 重放、单一来源冻结、删除后重放、换来源/跨工作区拒绝。目标 2 条分别对应普通预占夹具和实际提升，提升 receipt 仅 1 条。
- TestGoalGenerationDispatchesThroughRiverAndRetriesOneCandidate 扩展至迁移 113，通过（11.182s）。在原 HTTP 多来源创建/失败/显式重试闭环之后追加 production promotion service→单来源 selection→真实 River 四阶段生成→未发布候选。目录、材料、冻结执行输入、最终正文证据均绑定同一个指定 SourceVersion；重复 promotion 和完成后重复调度不新增模型调用。总 Provider 调用 9 次，其中提升 3 次。固定 Provider 验证流程与绑定，不证明外部模型语义质量。
- 准备失败→封存后清除错误、既有目录幂等和冻结回归的实库检查通过（本切片前段合并运行 25.776s）。相关 application/owner/capture postgres 编译与测试通过；organizing HTTP/app/auth race、限定 Go vet、前端 typecheck/generated typecheck、目标 API/组件测试和相关 ESLint 通过。
- 真实 Chromium 通过组件交互核验：模拟第一次响应丢失→错误提示→点击重试→保存成功，两次请求均使用同一个幂等键和空对象 body；成功后只有主笔记中心链接，不能重复提交。390 像素截图已检查，见 output/playwright/source-promotion-mobile.png。此项使用临时固定接口数据，不能替代真实 API/Worker/Web 同场联机验收；控制台仅临时页 favicon 404。
- 隔离数据库 zhixu_anchor_schema_final_110 已由项目迁移器升至 113；atlas/schema.sql 从该库重新导出并保留既有 runtime grants，Atlas hash/validate、diff whitespace 通过。

整体任务保持 in_progress / goal active。仍待：主笔记之间实际正文依赖随上游发布产生局部候选、完整人工编辑保护、文件新增/恢复的持续发现、默认主笔记中心与全部提醒的综合验收、单来源入口后续范围审核/融合的完整联机流程，以及真实模型语义质量评估。当前不将新候选等同于已发布主笔记或已完成锚点设置。无提交、push、部署或归档。

## 2026-09-15 新增、修改与原样恢复文件的持续发现

后台新增 localSourceDiscovery，启动和既有 Capture producer 周期调用。只扫描当前 Active Workspace；每页最多推进 100 个目录项，游标按目录遍历的分量顺序前进（不能直接按整个路径字符串排序）。完成一轮后间隔 30 秒再从头检查；进程重启重复扫描由原 Source/Version 唯一约束与 Capture receipt 去重。复用 Go 标准库、现有 filesystem.Scanner 的格式/大小策略与 Workspace 注册能力，未引入新 watcher、队列或第三方依赖。

Scanner 将目录枚举与单文件内容观察分开：os.Root 限定目录边界，不进入 .git/.knowledge/tmp/.tmp 或目录符号链接；仅观察支持格式的常规文件，按大小上限读取 hash，读取期间大小/mtime 改变时拒绝当前观察。Service 逐文件重验 RootGrant，调用原不可变 Capture + Source/Artifact/Version 注册，成功后推进游标；单文件错误计数并继续，超时中断的文件在下一完整轮次重试。Worker 保存本轮进度，日志只记录稳定错误码/计数，不输出文件内容或底层错误。文件级失败目前只在后台日志可见，尚无持久逐文件发现错误页面；该限制不等于失败文件已经进入知识库。

新文件版本登记后复用原 Capture backfill、Outbox、River、解析与画像流水线；无需用户逐篇手动扫描或填写分类。已有手动 ScanWorkspace 保持批量事务行为，仅提取共用的 capture/registration helper。Worker 静态及 managed source processing composition 均配置相同 Scanner。

验证：既有 TestWorkerScannedSourcesBackfillProfilesAndPreserveHistory 改为真正调用生产 components.localSourceDiscovery.DiscoverBatch，不再用手动 ScanWorkspace 代替后台发现。真实临时文件新增/修改后，经真实生产 Worker/River 与固定 Provider 完成两次画像，保留 Source/Capture 身份与历史 ProfileRevision。补充删除→presence 标记→原样恢复后 removed_at 清除、原 SourceVersion/画像复用且没有新的 backfill；首部超限文件计为失败，后续三个新文件仍进入有界 backfill（2/1/0）。最终扩展实库验证通过（23.581s）。此处主动清空 nextPass 模拟后续周期，不依靠测试实际等待 30 秒；生产定时循环接线已检查，但不是长时间运行的 watcher 稳定性验收。

Filesystem 实际临时目录测试覆盖分页跨目录、a/b.md 与 a.txt 的遍历排序、排除管理目录和目录 symlink、读取修改后的新 hash、拒绝超限文件和管理路径。filesystem/workspace application 包测试、Worker 编译和限定 vet、diff whitespace 通过。独立审查正在进行。无前端/API/数据库迁移变更，不重复无关前端检查。

新发现的既有版本边界仍待修复：core.source_version 唯一键为 (source_id,content_hash)，重现旧内容 A 时注册会复用旧行；Capture/知识目录等 owner 按 captured_at/id 取最新。因而 A→B→A 的当前文件内容与“最新版本”可能不一致。静态证据位于 gorm_source_writer.go 的 source/hash 重放和 gorm_source_backfill.go 的 latest 查询。不可通过修改历史版本时间戳解决，需要明确当前来源版本事实并同步相关 owner；本轮原样恢复的是最新 B，不证明回退到历史 A 正确。

整体仍为 in_progress / goal active；上述历史内容回退边界、发现失败可见性、主笔记正文依赖传播、人工编辑保护及完整产品联机/真实模型验收仍未完成。没有提交、push 或部署。

本轮独立审查确认两项：上述 A→B→A 是待修复的 P2；扫描/捕获之间按路径重新打开根可能摄入被替换目录，为 P1。P1 已修正：SourceDiscoverySession 在打开物理目录后执行 RootGrant 校验，并检查授权时路径与已打开句柄为同一目录。枚举、观察和不可变捕获持续借用该句柄，提交元数据前再次 Revalidate；关闭由整页 session 负责。根被替换时，已有操作只能接触原授权物理目录，不能读写替换后的目录，且不能通过最后的登记校验。

为避免持有句柄的捕获又通过 Git ignore 初始化重新解析路径，发现流程在受控 .knowledge 内以 create-only 方式写入仅含星号的自忽略文件。旧手动 Capture 的 .git/info/exclude 逻辑保持兼容。现有非预期 ignore 文件拒绝覆盖。补充实际根 rename/替换回归：观察和 Artifact 副本仍来自原目录，替换目录没有 .knowledge 写入，Revalidate 拒绝登记。定向 filesystem 测试和限定 vet 已通过；修复后的完整 Worker 实库复验结果见后续记录。

修复后的最终生产 Worker 实库流程通过（26.347s），涵盖自动发现→新增/修改画像、历史保留、删除与原样恢复、超限文件继续推进。临时 Git 仓库验证自忽略文件和 Artifact 都不会出现在 untracked 状态。最终 diff whitespace 通过，无本轮遗留验证进程。P1 已修复；A→B→A 当前版本语义 P2 保持未完成，将作为下一步优先处理，不将发现切片标记为完整文件更新验收。

## 2026-09-15 A→B→A 来源版本修复

修复上一轮确认的 P2。新增 00114，将普通 source/hash 的 canonical 唯一性保留为部分唯一索引；本地内容再次出现时允许新建带 observation_predecessor_id 的不可变版本。前驱外键绑定同 Source，唯一前驱和触发器要求其为当时当前版本、内容发生变化、时间严格推进且原 canonical Artifact/元数据存在。历史版本、时间戳、内容副本和引用均不修改。

手动扫描和后台发现均通过 CurrentObservation 显式进入观察分支。持有 Source 注册锁后读取当前版本：相同内容/元数据直接返回当前版本；新内容按原流程注册；回到已有内容时创建新的出现记录并复用 Artifact。旧观察晚到时返回 SOURCE_OBSERVATION_STALE，不能把 B 覆盖到当前 A。普通命令/指定 Commit 注册仍返回原 canonical identity；其 SQL 的冲突目标与读取显式限定 canonical 行。时间先截断到 PostgreSQL 的微秒精度，防止仅纳秒较新但落库同时间后按随机 ID 排错。

已验证：真实生产 Worker/River 的自动发现测试扩展为 A→B→A，三个观察均完成真实画像阶段；第三个 SourceVersion 不等于原 A，但复用同一 Artifact，知识目录按每次实际观察版本更新，历史画像保持不变。随后删除并原样恢复当前 A 不产生新版本/画像请求，超限文件仍不阻挡后续文件。通过（25.234s，固定 Provider，不代表外部模型质量）。

真实 Workspace Repository 生命周期测试新增两执行者并发恢复 A：只有一次新版本、同一返回身份且 Artifact 复用；普通旧命令重放仍返回最早 canonical A；迟到 B 观察和过期前驱 SQL 插入被拒绝，当前版本保持新 A。旧 Source/Artifact 不可变、同 hash 不同 Source 及原注册幂等检查继续通过（21.704s）。补充微秒精度修正后的最后复验进行中。

限定 Go vet、相关编译/应用测试、Atlas hash/validate 已通过。隔离 schema 库由项目迁移器升至 114，并重新导出 atlas/schema.sql、保留 runtime grants。没有前端或 OpenAPI 变更，未重复无关检查。整体 task/goal 仍未完成：正文依赖传播、人工编辑保护、发现失败的持久可见性和完整产品/模型验收继续保留；无提交、push、部署。

微秒精度修正后的并发生命周期复验通过（21.873s）。随后 Go/SQL 自检发现新观察读取当前行时会提前拒绝缺失 ContentArtifact 的历史版本，影响原有受限补绑定路径；已将仅该观察分支的行读取允许暂缺 Artifact，并继续交给原 canonical 注册 helper 补绑定。普通来源读取保持严格，不允许缺失 Artifact 的版本进入 Parser。旧数据测试使用现有模拟迁移前异常数据夹具，增加当前 legacy 行被补绑定且身份不变的断言；与并发生命周期一起最终复验中。

最终复验通过（56.054s）：并发 A→B→A、canonical 命令重放、过期观察/前驱拒绝，以及 legacy Artifact 补绑定均通过；相关 Go vet 与 diff whitespace 通过。已修复本轮版本判定 P2 和自检发现的兼容问题，无本轮遗留验证进程。整体目标继续 active，后续优先推进已批准的主笔记正文依赖传播与人工编辑保护。新注册 SQL 与迁移 114 配套，尚未部署到正式运行库。


## 2026-09-15 主笔记历史发布事件基础

经调用链核验，Writeback 的 reindex outbox 写入早于 Authoring finalizer，不能单独证明主笔记已经进入发布态；当前发布指针也会漏掉两次扫描间被替代的历史版本。本轮先落实依赖传播的真实触发事实，不把已有共享来源图谱当作正文引用。

新增 00115：synthesis_proven_publication 只读投影以真实 publication binding、ProposalCommit、generated ArticleRevision 和 SynthesisRevision 的完整身份/hash 映射证明历史发布（含 SUPERSEDED）。新增历史部分索引。GORMSynthesisStore.ReconcileSynthesisPublicationEvents 每批最多 100 条，使用现有 workflow.outbox_event；唯一键绑定 immutable revision，原子 INSERT SELECT 支持重复及并发补录，不保存易漏历史的内存水位。数据库触发器拒绝未发布/伪造事件、绑定修改、删除和已有发布事件时的 TRUNCATE；消费确认单向保存。Worker 启动与既有 capture 周期接线，模型排空不关闭发布事实观察。

复用 TestSynthesisPostgreSQLRealGitPublication 升至迁移 115：真实 Approval→Safe Writeback→Git→Authoring finalizer 连续发布两次，期间不扫描；随后按每批 1 条补录，旧 SUPERSEDED 版本与新版本都进入事件。重放扫描为零；事件身份/hash 精确，未审批候选、人工编辑导致写回失败均无新发布事件；直接伪造未发布事件和历史事件修改/删除被数据库拒绝。首次定向实库通过（13.363s）。该测试保留既有历史版本与人工改动保护断言；模型内容由已有夹具提供，不证明真实模型质量。

相关包编译、限定 Go vet 与 diff whitespace 已通过。正在同步隔离 schema 数据库与导出、执行独立 Go/SQL 审查；最终结果追加下文。没有前端/API 变更，不重复无关前端检查。

整体 task 保持 in_progress、goal active。此切片只生产可靠发布事实；真实下游片段依赖绑定、影响语义分析、局部候选消费、防循环，以及完整人工编辑保护、发现失败持久可见性和全产品/真实模型验收仍未完成。无提交、push 或正式部署。


并发补录、单向消费确认与禁止撤销确认的实库扩展通过（22.147s）。独立审查发现 inactive workspace 过滤会丢失停机期间切换/移除工作区的已发布事实；已移除该过滤，补录只读取数据库中的历史发布证明，不访问本地目录。审查复核未再发现可证实的 P1/P2。新增 inactive 状态回归最初因夹具未按 registry 契约推进 workspace version 而被数据库拒绝，已修正夹具为 version+1，最后复验中。

触发器声明从 view 的 %ROWTYPE 改为 record，避免 pg_dump 将函数排列在 view 之前导致恢复依赖问题；证据查询使用 UUID 条件，不对 revision 主键转 text。最终 115 已在新隔离库 zhixu_anchor_schema_115_final 经项目迁移器完整应用；atlas/schema.sql 从该库导出并保留原 runtime grants。整个导出 Schema 已在另一空隔离库恢复通过；Atlas validate、限定 vet 和 diff whitespace 通过。旧的临时 schema 库不是此次最终迁移基线。

最终定向实库复验通过（19.938s）：非活动工作区仍完整补录连续两次真实发布，并发/重复扫描不重复，历史绑定与消费确认不可改写，未审批及人工编辑导致失败的候选不产生发布事件。最终 Schema 空库恢复和静态检查通过。没有本轮遗留验证进程；整体仍在实施，下一切片继续真实正文片段依赖绑定与局部候选消费，不将发布事实基础等同于完整传播功能。


## 2026-09-15 主笔记已发布片段的显式包含与追溯

新增 00116 与 body_reference 契约，跨主笔记引用绑定真实 publication、不可变 revision、item 和 projection hash。v4 模型协议通过 INCLUDE_ITEM 标签指令复制完整事实、冲突或缺口（含已有结论、条件及原始证据），独立语义审查覆盖全部复制内容。冻结与应用事务再次验证历史发布证据，数据库自动投影不可变正文引用台账；伪造身份、缺失片段、不完整复制被拒绝。已 supersede 的原发布版本仍可作为已冻结证据，未发布当前草稿不冒用旧版发布身份。v1/v2/v3 请求协议保持兼容，v4 生成采用 v2 schema，语义审查仍为 v1。

OpenAPI、生成客户端、严格前端解码与阅读页同步接入可选引用字段。用户可从下游片段跳到原发布版本并聚焦原片段，再展开原始来源；版本哈希、工作区、片段不匹配的链接明确报错。原始来源按钮继续保留。真实浏览器发现同级组件 key 重复，已给后补来源组件使用独立 key。

独立 Go/SQL 审查发现 P2：显式引用遇到同文普通条目时，旧语义去重会丢弃引用关系。已将完整引用身份加入去重键，普通条目与显式引用不再互相吞并，同一精确引用仍去重。实库回归先创建同文普通条目，再纳入三种已发布条目，确认候选升为版本 2、保留原条目、落库三条引用；审查复核确认问题已解决。

验证证据：

- TestSynthesisPostgreSQLRealGitPublication 升至迁移 116，最终通过（12.729s）。覆盖真实 Git/审批发布历史、精确三类内容包含、已有同文条目回归、旧版本发布证据、草稿不提供包含身份、幂等重放、台账不可变与伪造 publication 拒绝。此夹具的生成验证器为固定验证实现；不将它等同于生产 River + v4 模型持久记录的同场联机验收。
- organizing domain/application/agent/synthesispostgres/workflow 相关包测试通过；v4 生成与语义审查的不同 schema、旧提示版本拒绝及精确模型响应证明定向回归通过。相关 Go vet 通过。
- OpenAPI 235 个 operation 清单、lint 和生成通过；前端 typecheck/generated typecheck、相关 ESLint、两个 API/页面文件共 42 项测试通过，包括引用元数据解码、精确版本定位及哈希漂移拒绝。
- 隔离 schema 库由项目迁移器成功升至 116，atlas/schema.sql 已重新导出并保留 runtime grants；Atlas validate 通过。

整体 task/goal 仍为 in_progress/active。当前完成的是显式正文依赖的保存和读取；00115 发布事件尚未驱动下游受影响片段的局部候选。后续仍需传播去重与终止、人工修改保留/合并、发现失败可见性和完整产品/模型验收。无提交、push、部署或归档。


浏览器最终核验通过：真实 Chromium 的 iPhone 13（390 CSS 像素）环境，点击下游引用进入原发布版本，焦点确切落在所引用的 item，scrollWidth=390，无横向溢出；截图已查看，保留于 output/playwright/body-reference-mobile-device.png。继续打开原始来源按钮可查看固定接口提供的原文。当前组件控制台仅临时 favicon 404；最初发现的同级 key 重复已消除。本项使用临时固定 API 数据，不替代真实 API/Worker/Web 同场验收。

追加严格链接校验：revision_id 为空时不能回退到当前发布版本冒充精确引用；页面回归 26 项和相关 ESLint 通过。最后的 diff whitespace 通过。

116 的导出 Schema 已在新建空隔离库 zhixu_anchor_schema_116_restore 完整恢复，恢复命令退出 0；恢复后查询确认 synthesis_revision_body_reference 表与 verify_synthesis_published_bindings(jsonb) 函数存在。该证据覆盖当前 116 导出，不代表正在实施的 117/118 已完成迁移或恢复验证。

## 2026-09-15 上游真实发布的正文影响提醒

新增 00117 不可变 synthesis_body_impact，绑定下游确切版本/item、原上游引用、上游新发布与真实发布事件。Worker 启动和周期对账只处理显式正文依赖；共享来源、未引用内容变化、未发布草稿不会产生提醒。比较忽略纯 body_reference 身份变化。每批有界，冲突去重，其他消费者确认 outbox 不会导致影响遗漏。

审查发现默认读已发布 P、当前候选却是 C 时，原查询只给 C 保存提醒，P 页面会漏报。已将可见基线限定为当前候选和经 Document 当前发布指针、synthesis_proven_publication 精确证明的正式版本；不扩展到任意历史草稿。插入校验按既有 note → document 顺序加共享锁，再核验同一可见集合。Authoring finalizer 不锁 synthesis_note，因此 Document 锁不可省略。并发 LIMIT 1 扫描允许先处理同一条记录，再有界补齐另一条；最终记录数与重复扫描结果必须精确。

新增 GET /synthesis/notes/{note_id}/revisions/{revision_id}/body-impacts，分页默认 20、上限 100，严格参数、作用域、排序、返回游标与旧引用绑定。前端默认读取所看版本的提醒，提供刷新、分页、失败重试、受影响片段、原引用片段和新发布版本链接。原链接保持旧 revision/hash/item；新版本只携带其真实 revision_id，不套用旧 hash。提醒本身不表示候选已经生成或正文已发布。

真实浏览器联调发现时间契约错误：Postgres 读出的 detected_at 使用本地 +08:00，严格前端只接受 UTC。已在 Store 读取层统一 UTC，并在真实发布回归中加入规范时区断言；没有放宽前端契约。

已验证：

- TestSynthesisBodyImpactsFromRealPublications 最终通过（20.713s）：真实 PostgreSQL、审批、Git 发布，P/C 双基线、无关变化/草稿排除、精确旧新绑定、并发补齐/重放、不可变与工作区隔离、正式指针与文件不变、UTC 时间。Go vet（含 integration）通过。
- HTTP、权限与路由检查、OpenAPI 生成、前端两项类型检查、相关 ESLint 和 45 项定向 Web 测试由 UI 实施代理验证通过。
- 专用只读 HTTP 入口直接连接上述真实 PostgreSQL/Git 夹具，真实 Chromium 运行 BodyImpactPanel、NoteContent 和严格 API decoder。刷新按钮和整页重开后提醒仍在；下游 item 定位与焦点正确；旧版本保持未补充内容，新发布版本显示新增结论。iPhone 13 的 viewport/scrollWidth 均为 390，无 alert 或最终控制台错误；截图 output/playwright/body-impact-live-mobile.png 已查看。临时 HMR 入口出现过重复 createRoot 警告，已加卸载并重开后消失；临时入口、专用浏览器、服务与容器均已清理。本项未经过完整 App 认证入口或生产 River 模型执行，不代替全产品联机验收。
- 当前 schema 基线库由 116 升至 117；atlas/schema.sql 已导出并保留原 runtime grants，在新空库 zhixu_anchor_schema_117_restore 完整恢复成功。由于 118 同时在写入，117 最终实库验证与迁移使用精确复制的 1–117 SQL 目录和单独 Atlas 校验和，通过临时 Go overlay 仅替换 MigrationDir 的读取位置；SQL 和业务代码未替换为模拟实现。该冻结目录的 Atlas validate 通过，不据此声称工作区未完成的 118 已校验。

整体 task/goal 保持 in_progress/active。00118 局部候选、人工正文合并、完整产品与模型验收仍继续实施。没有提交、push、正式部署或归档。


## 2026-09-15 主笔记列表的集中更新提醒

新增 GET /synthesis/update-summaries，显式传入当前已加载的 1–50 个唯一 note IDs。每批在 repeatable-read 内固定查询，返回确切 current/published revision 的来源及正文引用观察数；同版本去重，不累计任意历史版本。读取错误或列表身份变化显示刷新入口。列表保留默认当前发布版阅读链接，提醒另链接到确切 revision_id。

实施代理完成真实 PG/Git 117 集成（P/C、历史排除、两笔记共享来源、详情计数一致、重复读取、工作区隔离）、HTTP/权限/路由、44 项 Web 定向测试、前后端类型/生成/lint/vet/OpenAPI 检查。root 复核跨层读取与链接契约，并在专用只读 HTTP 上连接真实 PG/Git 数据运行完整 SynthesisNotesPage：列表同时显示发布 v1 和候选 v2 的独立来源/正文提醒；刷新按钮与整页重新加载后保留；两个提醒分别打开其实际版本，来源删除提示和正文更新提示均正常读取。390px 手机 viewport 与 scrollWidth 均为 390，alert 为 0，截图 output/playwright/main-note-update-summary-mobile.png 已查看。临时入口最初 favicon 404 已通过入口图标修正，最后重新加载无控制台错误。

浏览器夹具复用真实业务数据与正式 notes/summary/source-impact/body-impact HTTP，非目标的 goals/processing 面板仅提供空夹具；没有验证创建目标、认证、生产 River 或真实外部模型。夹具运行最终通过（192.855s，包含浏览器等待）；临时 Go/Web 文件、两个浏览器会话、Vite/HTTP 与该夹具数据库均已清理。

root 发现 source-impact 同样未把 GORM detected_at 规范化 UTC，会被实际前端 timestamp decoder 拒绝；已修复并在既有 TestSynthesisSourceImpactsPersistWithoutChangingPublishedContent 添加 UTC 断言，真实 PG/Git 回归通过（13.150s）。随后浏览器实际确认来源提醒正常加载。另在既有 TestSynthesisRevisionHashSnapshotAndSafeMarkdown 固定 v1 projection/content golden hash，定向检查通过，为后续完整稿版本保留历史契约。

人工保留设计已收窄：现有 generated document 没有保存 USER Article 的可达编辑入口；不新增 WorkingDraft 功能或人工父版本入口。保留现有 AGENT owner 链，对可达历史恢复产生的 SYSTEM drift 继续拒绝。机器增量以最新候选 L 为基线，磁盘合并必须以实际发布全文 P 为共同祖先；不能把尚未发布的候选缺席文件当人工删除。详见 research/manual-content-review.md 的补充复核。完整稿与映射开始独立实施，尚未接入持久化、合并审阅或发布，不视为 PRD 已完成。


## 2026-09-15 118 局部候选执行与待修复审查

118 已接独立 BodyRefreshRequestID、四阶段 River 执行、v5 生成协议和独立语义记录。实施代理完成真实 PG/Git/River（初始来源/关联使用既有owner夹具，自刷新调度起启用实际模型账本/执行fence），并发只启动一次、调用生成和语义各一次、局部候选维持无关条目与正式指针。root 在 UTC 请求规范化修复后复跑 TestBodyRefreshGroupsPublishedAndCandidateImpacts 通过（59.145s）。这不证明任意真实模型响应质量，也不等于下游候选经过完整 UI 审批后再次传播。

当前118通过项目迁移器应用到隔离 schema 基线库；atlas/schema.sql 已导出并保留 runtime grants，在空隔离库 zhixu_anchor_schema_118_restore 恢复通过，实际存在 refresh_request 与 pending_body_impact；Atlas validate 通过。

独立审查发现 P2：prepare 许可读取错误绑定上游 provenance SourceEvent 的单一来源，可能漏掉受影响片段引用的其他已获许可资料，误报范围不足。正在按实际片段的证据集合修复，并补事件A/证据B的真实River场景；上述59.145s通过不覆盖此修复，118切片暂不标最终完成。基础全应用 smoke 已在独立 Compose project启动，日志 /tmp/zhixu-compose-synthesis-0915.log；其构建快照早于此项修复，仅作为基础产品流程核验。

### 118 定向复核与基础产品烟测完成

实际片段许可 P2 已修复：按更新片段真实来源逐一冻结/重验，不借用 SourceEvent 来源或旧引用扩大 scope。三场景真实 PG + River 通过（113.180s），独立审查 seq9484 关闭；许可缺失/scope 变化的负向证据来自事务故障注入下的 owner fence，不是新增撤销交互验收。Schema 未变化，118 导出与空库恢复证据仍适用。

基础 Compose smoke 首次因固定模型夹具缺少既有 v4 published-note schema 而失败。已给 cmd/rag-model-fixture 增加 v4/v2 精确 schema 路由和 published 字段解码，保留严格请求匹配；TestFixtureSynthesis 和 vet 通过。第二次完整 smoke 退出 0，验证 Capture→生成→审批/Git、增量/NO_CHANGE/精确重放、来源和历史、桌面/手机已发布主笔记 Interview。日志 /tmp/zhixu-compose-synthesis-0915-retry.log。该项目的所有容器和卷已实际核对清理；未操作用户运行实例。使用固定 Provider，不证明真实外部模型质量，也未覆盖正在实施的人工全文合并发布或多轮正文传播。

人工全文 Mapper 独立审查发现跨项语境 P2：块内人工句子也能否定全篇，AST 局部结构不能证明语义作用域。采用保守整篇待复核，不用关键词过滤；原字节保留，已改全文不能继续作为已验证语义来源。v2 可信 Items 允许为空，历史来源仍需与当前证明明确区分。人工全文接线继续，整体未完成。

### 人工全文与纯合并/裁决基础完成

新增 v2 Manuscript envelope 与独立 renderer/hash schema，SynthesisRevision/Snapshot 使用 Content() 读取精确完整正文；v1 可选字段省略，黄金 hash 不变。顶层可信 Items 可为空且必须严格等于映射子集，机器审计 Items 不作为 fallback；snapshot 完整深拷贝。全文上限 1 MiB，拒绝 NUL/非法 UTF-8。当前任意人工全文变化整篇待复核，原文和历史引用保留，不声称局部 AST 能证明句子语义作用域。

PreviewSynthesisManuscript 复用真实 Git merge-file，先保留 L 中人工内容，再基于 P/F 合并；ResolveSynthesisManuscript 逐阶段验证 fingerprint 和全部冲突 ordinal，内层解决后出现外层冲突须独立裁决。无文件/数据库/审批副作用。历史排除完整并集保留已移除 ID，三代同 ID 重现不能复活；上限沿现有 MaxSynthesisItems=128，超限明确人工复核而不丢弃历史。1024 为 Git 冲突块上限，不是排除集上限。

实施代理最终 application 1.718s / Mapper 1.496s / domain 1.567s 定向测试通过（真实 Git 未发布内容/人工备注、内外冲突、两次裁决、漂移拒绝、三代排除与超限、NUL、上下文和 v1 golden），三包 vet 与 diff 检查通过。独立 impact-check seq14851 复核确认两个 P2 修复，未发现新增信任边界缺陷，证据在 research/manual-merge-review.md。

119 文件捕获/attempt/clean receipt 存储已实现，生产 Baseline/Proof owner、冲突持久裁决/HumanWait、双基线发布与前端尚未接入。以上不代表人工编辑完整 PRD 已完成。发现失败的持久可见性开始独立实施（预留120），同属既有任务清单。

### 119 正式迁移目录验收与并发修复

当前 runtime 仓储夹具最低迁移版本提升到119，历史迁移专用夹具不变。独立审查发现 Prepare 提前分类数据库错误会丢失23505，导致并发同键恢复分支不可达；已保留原始错误到恢复之后，并增加有界并发同键实际数据库验收（同 attempt/capture、只落一套事实、异参拒绝）。

使用正式迁移目录及主 checksum（含119+120），无 overlay：两项 manuscript 存储测试 PASS15.211s，日志 /tmp/manuscript-store-119-review-fixed.log；BodyRefreshGroupsPublishedAndCandidateImpacts PASS48.762s，日志 /tmp/manuscript-store-119-body-refresh.log；两包 integration vet 与定向 diff 检查通过。此前临时目录证据已由本次正式目录结果补足。生产 Baseline/Proof、当前执行许可分离、HumanWait、双基线发布仍未验证，不因存储 fixture 通过而视为完整功能。

### 120 扫描失败可见性完成

WALK/OBSERVE/REGISTER实际失败按workspace/binding/安全相对路径持久化；只由实际读目录或文件登记成功恢复。页尾ReadDir二次回调不会误恢复/占双份额度，根替换/授权变化拒绝登记。真实文件系统权限、PG/HTTP重读、存储故障检查通过；正式目录含121的最终实库7.705s。跨页binding变化时清空可见旧分页并从首页刷新，P2复核关闭；最终2文件11项Web测试、类型/lint通过。浏览器真实PG+扫描+HTTP验证失败→手动重扫→已恢复→页面刷新保持；实际rebind竞态仅组件序列验证。保留DOM快照，无PNG截图；临时入口/服务已清理。详见research/discovery-failure-implementation.md。

### 121 双基线发布完成与当前限制

Authoring从不可变候选Article/receipt/capture推导F文件CAS与P正式指针CAS，不接受客户端新hash字段。真实PG/Git完成全文存储→预约→审批→Git→P supersede→全文重读，PASS11.386s；普通发布两项17.893s。审查发现94 generated owner夹具缺新依赖，已用强制NULL列+typed empty view隔离依赖修复，保留真实94约束及原7项atomic/scope/AGENT/retirement/审批竞争断言，全部通过；该夹具不证明121跨owner闭包。审查P2关闭。证据见research/manual-publication-implementation.md。

真实边界：现有Git clean门禁仍拒绝未提交的人工F；发布测试先证明拒绝，再仅在隔离仓提交F完成F≠P发布。不宣称用户无需Git提交即可发布。生产Baseline/Proof、版本化Workflow/HumanWait和读取UI仍在接线，整体PRD未完成。

root使用正式项目迁移器将独立schema库从118升至121，重新导出atlas/schema.sql并保留runtime grants；在新空库zhixu_anchor_schema_121_restore恢复成功，实际确认receipt/失败表/双基线view存在，Atlas validate通过。当前schema证据截至121，后续122需另验。

### 人工全文读取完成，生产与多轮验收继续

v2 通过受控 display 返回精确全文，允许零可信 Items；原始来源独立 HTTP 响应明确 HISTORICAL_REVIEW，历史目录未记录不会请求当前 Profile 冒充。v1 shape 保持。主会话指出片段索引缺少可辨认文本、冲突共享来源产生重复按钮，已修复并补回归；最终48项 Web 测试、HTTP/应用授权、类型/lint/vet/OpenAPI/generated检查通过。独立 Chromium 验证人工全文重载、历史来源、390px布局，以及索引实际焦点和共享来源只显示一次。浏览器使用实际组件/handler的固定owner夹具，未接真实PG/生产模型；不替代完整发布验收。详见 research/manual-reading-implementation.md，frontend spec已同步。

生产接线122与复习快照兼容123仍在验证。root已接worker真实Resolver组合点，并发现无锚点兼容及零可信项重复更新的prepare/apply基线分歧，两项交实施修复，未关闭。另开独立多轮正文传播验收；剩余成功/失败条件见 research/remaining-acceptance.md。整体任务保持in_progress。

root已使用正式迁移目录将独立schema库121→123升级（/tmp/zhixu-schema-123-migrate.log），导出atlas/schema.sql并保留runtime权限尾段，在新空库zhixu_anchor_schema_123_restore完整恢复成功（/tmp/zhixu-schema-123-restore.log），Atlas validate通过。122 checksum rG2MEAZuiW90oVUW7g74NS0J3b060P3umqnwXU3MiOs=，123 checksum b4BuVDtPlEelmYS3eLwu1+PnEvOKVByh9ZnAFemdFPc=；这是结构证据，业务实库仍由各切片验收。

123 Interview正式目录实库通过54.418s：真实v2发布/准备/River出题及v1生命周期，零可信项在preparation/ModelRun之前拒绝，篡改snapshot拒绝，v1/v2精确重放无额外调用。合成Baseline/Proof仍为明确fixture，完整生产人工合并不能借用此结论。backend spec已补充完整snapshot与十字段题目身份契约；独立读取/Interview审查正在进行。

多轮正文传播正式目录实库PASS38.19s（包41.812s，/tmp/body-roundtrip-final.log）：A草稿不传播，A实际发布后只更新B候选；B审核/Git发布后C及反向引用A分别局部更新并实际发布；再次完整扫描三次均零新增，3个请求/processing、6个实际模型调用、A/B/C版本4/2/2保持。无关条目结构与Markdown字节、历史引用、正式文件/指针均核对；root已读关键断言及日志。该具体环因B引用的A条目没有变化而终止，不推断任意模型必然收敛。初始源/关联使用owner fixture，传播真River/独立model journal，v1图、固定Provider。未引入生产修复；integration vet通过，见research/body-roundtrip-validation.md。

### 122 生产 clean 合并与独立审查完成

无锚点 admission 和 prepare/apply 基线分歧均已修复，生产真实 caller 及热构建组合已复核；独立审查无新增 P1/P2，原两项关闭。正式目录实库覆盖 clean/下一轮 v2（最终55.025s）、普通 fusion 与 source/root/owner/scope 漂移（120.019s）、无锚点真实 source-ready（19.169s）、旧 v1 图运行及重放（18.001s）、原119存储兼容（14.541s）；固定 Provider、实际 PG/River/Git，许可故障注入边界见 research/manual-runtime-independent-review.md。审计重复明确返回 SOURCE_REVIEW_REQUIRED，不生成 v1 候选、不丢人工全文，也不将历史机器项提升为可信来源。该分支可恢复产品入口及冲突 HumanWait 尚待接通。

v2 读取/Interview 独立审查亦已完成，无新增 P1/P2，见 research/manual-read-interview-review.md。真实外部模型的五场景质量验收工具已具备预算、超时和原始产物限制，离线协议与限额检查通过；当前有效 Chat disabled、测试环境未配置，尚未调用外部模型。已询问配置入口并继续独立开发，不将固定 Provider 的结果视为语义质量证明。

124 人工裁决 owner 已落盘并开始正式迁移及实库验收。生产 HumanWait、实际 CallerCapabilities 授权、owner receipt 后恢复 apply 与界面流程继续实施，完整 task/goal 保持 in_progress/active。

### 124 人工裁决存储与结构验收完成

真实PG、实际HumanTask、真实Git两阶段裁决通过12.41s；中间无receipt，最后阶段与receipt同事务，完整多目标缺失/未seal不Ready，幂等与漂移拒绝、重开恢复均有断言。独立审查无新增P1/P2。root随后发现同processing重试混入旧run，已按冻结PascalCase WorkflowRunID过滤，并以新旧run保留3条attempt的实库回归通过13.47s（包14.338s）；最终integration vet通过。生产权限在此存储测试仍是明确fixture，真实HumanWait/HTTP/UI/apply另验。详见 research/manual-resolution-implementation.md 与 manual-resolution-review.md。

main 将124纳入正式目录，删除一处行尾空白后最终checksum为guHqkRSKINQrGKldYXTlfe707MdYmG5cOiVa6v+sazo=（无业务SQL变化）。在新隔离库zhixu_anchor_schema_124_final从空库完整迁移，导出精确atlas/schema.sql并保留runtime权限尾段，另在zhixu_anchor_schema_124_final_restore空库恢复成功；日志 /tmp/zhixu-schema-124-final-migrate.log、/tmp/zhixu-schema-124-final-restore.log。Atlas validate和定向diff检查通过。仅操作隔离测试库，未迁移用户运行实例。

### 生产 HumanWait、恢复与浏览器发布闭环完成

生产 typed caller/Root/实际HumanTask授权已接真实两阶段裁决；全部目标包括后续clean项都准备齐备后才释放同task。实际generation/semantic日志、owner receipt与最终apply相互核验；无receipt的伪通用task结果被拒绝，zero candidate。两阶段、普通无anchor、跨workspace/伪capture/Root/Document/scope漂移、cancel→FAILED/retry新run、多个目标原子apply均通过真实PG+River回归，详见research/manual-human-runtime-implementation.md。

最后receipt已落而SubmitHuman前中断的缺口已由binding-only Resume补齐；真实重建服务/丢弃旧command→GET Ready未Submitted→Resume原task→apply及精确重放PASS23.487s。Web/OpenAPI真实owner schema与242操作接线完成，旧通用approval不会误接主笔记任务；unknown同命令跨阶段恢复、stale保存草稿、Monaco未ready/error禁新提交的14项定向组件测试、type/lint通过。通用decision schema的enum/pattern遗漏已修复；原跨域同包集成import cycle改为外部测试包、薄test导出复用后，正常包命令实际PG测试两项PASS20.683s及vet通过。

main使用正式组件+真实PG/River/认证HTTP loopback桥在Chromium操作两阶段完整编辑与冲突确认，检查候选与正式版本分离，再经原审批/Git写回并重新读取正式正文。最终opt-in集成PASS379.53s（包380.505s，/tmp/zhixu-manuscript-live-verified/test.log），操作仅隔离t.TempDir仓库，无用户文件提交。临时入口已清理，测试容器与Vite停止；最后integration vet通过。页面切换仍观察到Monaco释放/异步diff console错误，业务结果已通过但该UI生命周期问题待修复；不宣称零console错误或全仓通过。

### 剩余边界继续实施

原PRD“候选生成后F再变化”已确认缺专用重合并路径：旧候选会安全拒绝，普通Proposal revision不能重建generated绑定。125专用owner正在实现，Base=原capture F0、Current=最新F1、Proposed=候选完整人工正文，保留旧版本和证明、不新增模型调用，新候选仍原审批；HTTP/Web独立接线中，不标为已完成。

用户于2026-09-15明确选择：人工改写后，AI须重新核对当前全文，确实支持当前内容才算补源完成；历史待复核参考不能代替完成。该规则已补PRD，正在设计最小当前全文证据复核和存量SOURCE_REVIEW_REQUIRED恢复。真实外部模型配置仍待提供，质量验收未调用。任务继续in_progress。


### 125 候选重合并、恢复及编辑保真闭环完成（2026-09-15）

125 owner/API/Web与真实PG独立审查已完成。Target/Begin/Read/Apply/Resume五入口，真实ReadLocal+WriteProposal权限；原capture F0、当前F1、完整原候选R1生成R2/A2/P2，不调用新模型。R2已提交而P2尚未创建时可在刷新/丢失原Apply命令后按attempt Resume；重复不建额外revision/event。正式125从空库迁移、schema导出/新库恢复已过，见research/schema-125-validation.md。

main真实浏览器流程PASS407.38s（包410.802s），包括冲突预览刷新、完整手改、P2前故障、刷新恢复、实际候选读取、隔离Approval/Git与发布后重新读取。发现并修复Monaco自动缩进（输入3067字节被改成3169字节）和已保存结果仍警告离页；最终全文逐字一致，无关内容保留。共享只读Diff卸载gutter异常另有12轮冷启动生命周期验证并修复。证据见research/candidate-remerge-browser-verification.md、monaco-lifecycle-verification.md；没有声称完整登录AppShell或外部模型语义质量已验收。

### 126 当前全文独立复核与证据读取（2026-09-15，后续恢复继续）

首次纯ADD_FACT补源从真实旧SOURCE_REVIEW_REQUIRED恢复，新增固定Workflow与独立Recording ModelRun，仅写外置段落证据/receipt。core已通过支持、拒绝、缺义务、正文/来源漂移、accepted输出落盘丢响应；旧4次模型不变，新增复核1次，正文/版本/P不变，重复调度0次。只读owner/HTTP真实PG已验证按精确parent分页、Root拒绝、来源删除后历史快照与当前失效；正式Web/API接线仍在收口。

126正式迁移与schema导出、新空库恢复通过，见research/schema-126-validation.md。独立恢复审查发现已知模型失败终结、prepare重试终态以及取消/过期归约缺口；prepare临时失败已交core修正，其他与显式successor/recheck和accepted proof的新恢复执行由127接续，不把首次成功当完整可恢复功能。原成功/拒绝和模型证明均须保留，未知调用不得通过新模型尝试覆盖。见research/current-source-recovery-review.md。

## 2026-09-15 21:45 当前全文补源收口进展

实际LOCAL_FILE链路已通过：原v2候选实际批准/Git发布→手改磁盘全文→独立复核→React/generated/正式HTTP/真实PG读取→完整正文逐字比对→精确来源→来源失效刷新及整页重载→历史原文。手机390×844无横向溢出；纯补源不改正文/版本/P，3次调度不新增模型调用。完整测试PASS314.56s，固定Provider原4次+独立1次；证据与严格范围见research/current-source-review-browser-verification.md。

127首次已实现显式Recheck、独立Recover、known failure/terminal/cancel归约与A→B→A复用。独立审查仍有必须修复的边界：同次复核多义务共享证据、公开义务集合、DB target身份约束、恢复执行自身失败后的新执行；另需统一保留STALE历史但恢复成功的HTTP/UI谓词。详见research/source-review-127-proof-review.md和source-review-ui-coordination.md。未将这几项标为完成，正式schema导出仍126，待127最终稳定后统一迁移/恢复验证。

## 2026-09-15 22:12 当前全文补源与恢复链路已交付

127最终四项P2均已修复：同事务共享物理证据且逐义务闭合、公开obligations集合、DB独立重算冻结target身份、恢复执行失败后新建独立恢复。原STALE历史与真实recovery receipt/时间分开，后续正文或来源变化撤下完成提示。最终窄复审新增确定缺陷0，见research/source-review-127-final-review.md；三个SQL负例的新日志PASS22.86s且无旧探针panic假阳性。

正式127从空库迁移、atlas/schema.sql导出、另空库恢复及Atlas lint/validate全部通过，见research/schema-127-validation.md。真实PG/River/auth HTTP三场景PASS63.31s，generated Raw/React两场景PASS53.39s，253条OpenAPI/generated一致、受影响Go vet/Web typecheck/ESLint和49项既有定向回归通过。

main实际Chromium重核命令恢复PASS529.93s：真实POST成功后注入丢响应→完整reload保留原key/version→显式重试找回同一后继→打开精确来源→实际HTTP并发同键重放。原失败历史保留，旧生成/语义4次和独立核验2次不因恢复增加，正文/版本/P不变。临时Web入口、专用浏览器和测试服务已清理，见research/current-source-review-command-browser-verification.md。

当前批准功能链路已有实现及上述范围验证；完整需求质量仍不标完成。唯一尚未开展的验收类别是可用外部模型下的真实语义质量，包含用户本次确认的当前全文判断；现无可用配置，配置选择已询问而未回复。任务保持in_progress，未提交/推送/发布。今后接续先读research/remaining-acceptance.md，避免把本文件旧时间段待办当作当前未实现项。

## 2026-09-15 22:28 完成审计发现历史重发布遗漏，继续实施

逐项对照PRD发现：主笔记历史查看与通用Git文件恢复存在，但通用恢复创建SYSTEM ArticleRevision，不满足主笔记AGENT/generated/publication proof，因此“历史回滚后仍为当前正式主笔记”未完成。已纠正上节“唯一剩余模型”的结论，研究见research/history-republish-audit.md、history-republish-design.md。

采用专用历史重发布owner：复制选定的证明完整R1（含未发布旧候选）为新R3，parent指当前L，保留selected的完整内容/来源/profile NULL/body引用；以本次人工操作回执证明复制，复用原generated/reservation/Approval/Git，不重新调用AI或回拨旧revision。128由history-republish-core独占，HTTP/OpenAPI/Web由history-republish-ui接线；main最终统一schema、独立审查和真实浏览器。两片正在实施，不标为完成。

同时已补当前全文真实模型验收工具TestSynthesisLiveSourceReviewQuality：支持当前全文/旧段被全文其他段否定两例，独立开关、4次Chat/2048每次/5分钟、0600不可覆盖artifact，真实生产SourceReviewModel/Eino/Recording路径。离线实际请求包含完整全文及不同历史statement、两义务绑定及预算检查通过，定向test/vet退出0；真实入口明确Skip，未调用外部模型。main轻量复核修正为允许同时支持claim和context段，避免对合法输出误报；具体限制见research/synthesis-live-quality.md。

## 2026-09-15 历史恢复主流程验证完成，继续同字节发布

历史恢复128提供精确Target/Begin/Read/Apply/Resume，保存selected证据与人工选择回执，当前L作parent。已发布/未发布旧候选、v2人工全文、待审退役、无首次P/F、失效来源与Profile NULL、正文引用及后续增量/下游影响均有core实际PG/Git证据。main独立浏览器走完整SynthesisNotePage，预览两份完整差异→确认→候选提交而提案未创建中断→刷新Read→Resume同Proposal→原审批/Git→默认发布版与原文→再次刷新，PASS311.78s。见research/history-republish-browser-verification.md。

首轮浏览器确实发现完整Begin17字段被通用16字段限制拦截，已改成共享严格decoder的历史入口17字段上限并重验。原Begin命令跨刷新保存，Get404后仍只在明确重试时使用原Target/key；公开CurrentScope与historical provenance已接线。OpenAPI 258路由集合、相关HTTP/auth、类型/lint/generated及定向Web回归通过。

最终128 hash4501e97abe705788ade7de68451147b7265e67ad8c6e64f78c305fe4dace9054正式空库迁移、导出schema、另空库恢复和Atlas lint/validate通过，见research/schema-128-validation.md。独立初审未发现P1/P2，新增无P分支与普通Authoring兼容由限定复验跟进。

同字节历史操作生成候选后仍被Git空diff门禁拒绝，不能将已知失败的Boundary测试算发布完成。core继续沿既有Authoring receipt→SafeWriteback→Git owner做类型化精确授权，无用户HTTP布尔开关，不修改已冻结128。此项和真实外部模型语义质量仍未完成；不请求重复实施批准。

## 2026-09-15 历史恢复最终收口

同正文的独立历史发布已完成：Authoring从128不可变回执及实际执行身份重建类型化能力，SafeWriteback/Git仅对该精确历史操作支持空diff/不变tree，新commit带对应receipt，仍核全仓clean、目标blob/mode、HEAD/branch CAS及RootGrant。真实连续历史选择与独立commit、正式读取、Resume不推进HEAD已验证；无授权/错身份拒绝和update-ref成功丢响应找回原commit也通过，旧125+历史/同正文实库合跑50.531s。最终独立窄审未发现P1/P2，见research/history-republish-proof-review.md末节；该子项现可勾选。

当前剩余验收为真实外部模型语义质量；已有生产调用入口及离线输入/预算核验，但live BASE_URL/API_KEY/MODEL仍未配置。任务保持in_progress，未提交、推送或发布。已完成范围与未验证限制统一见research/remaining-acceptance.md，历史进度段的已关闭待办不重开。
