# Human review 接口协调（2026-09-15）

channel send 被沙箱拒绝写 ~/.trellis/channels 锁；此文件为 main / manuscript-resolution 的共享协调内容。

应用接口在 application/synthesis_manuscript_review_service.go：Read 返回 binding+ready+submitted+targets 身份列表；ReadNote 仅返回所选 note 三方 preview；Decide 幂等保存一阶段，全部 receipts ready 后才提交同一通用 task。

建议 HTTP：GET processing/{processingID}/manuscript-review；GET 同路径/{noteID}；POST 同路径/{noteID}/decisions。POST 接完整不可变 binding + note/attempt/capture + idempotency_key + resolution(stage,preview_fingerprint,acknowledged_ordinals,final_content)。路径 workspace/processing/note 必须与 body 一致。每调用从真实 principal capabilities 创建具名不可变 Caller；零值拒绝。无 Prepared、模型日志、authority 输出。具体 Go DTO 为唯一字段依据，HTTP 尚待接线。

HumanTask schema 仅 version/processing_id/workflow_run_id/note_ids/result_hash，完整 note 集合 enum 绑定；result_hash 是按 note 排序的真实 receipt identities hash。不能直接授权全文。

124 配合：ReadManifest 应精确过滤冻结 workflow_run_id（processing retry 共享 processing ID）；同一冻结模型全 changed-existing targets 重算，不用查到的 rows 代替期望集合。runtime 会准备全部 targets，再返回真实 HumanWaitResult。

现有 processing retry 仅允许 FAILED&&retryable，RecoveryRequired 不可重试。当前 pending HumanTask stale 可走现有 workflow cancel → terminal hook FAILED/retryable → 显式 synthesis retry 新 run/attempt；须实际验证 cancel 待审节点路径，不展示无后端支持的 reprepare 按钮。

## 当前集成点（供 main 独立派 HTTP/Web）

Go app DTO/方法已落地并编译：`application/synthesis_manuscript_review_service.go`。真实工厂为 `postgres.SynthesisManuscriptRuntime.HumanReview(caller, *RuntimeHumanCoordinator)`，每个 HTTP 请求单独调用；`caller := NewSynthesisManuscriptCaller(principal.Scopes)`，principal 仅来自 authhttp.PrincipalFromContext，缺失/disabled 默认拒绝，不传 nil 当特权。Read/ReadNote/Decide 均使用同一 per-call service。

我本轮继续负责生产权限/runtime与实库验证，不编辑 HTTP、cmd/api、OpenAPI/Web（用户允许这些由后续独立接线）。API 组合可复用 worker 的 RuntimeDependencies：同池 Candidates、runtime Store 同时 Models/Executions、RootGrantResolver+localfs 包装的 Roots 和 Files、GitMerge+Mapper+IDs/Clock。API Root Resolver 可由 workspaceRuntime.Resolver 提供；不重新创建进程根权能、不依赖 Provider。

GET processing 本身可增 `pending_human_task` identity（或前端从 workflow 状态看到 manuscript schema 后改走专用 GET），不要把内部 manifest 或通用 business workflow 两类 schema decoder 直接拿来解析本任务。当前 app Read 在无真实待审/已提交任务时返回 409 SYNTHESIS_MANUSCRIPT_STALE；未查到待审不能伪装审阅成功。

124 Manifest 查询提醒：GenerationInput 的 JSON 字段是 `WorkflowRunID`（大写 Go 名），不是 `workflow_run_id`；按该键过滤精确 run，避免 retry 历史 attempt 混入。

进度 11:02：真实 BodyRefresh conflict→HumanWait→owner Decide→SubmitHuman→apply 已在正式124通过；同批普通无anchor夹具未制造实际冲突，正在改成真实全文冲突后复验。production HumanAuthority 不伪造 ExecutionContext，已拆 immutable model history 与 readOwners；124 owner 已主动按精确 WorkflowRunID过滤历史attempt。HTTP/Web仍按上述边界独立接线。

## 已锁定 HTTP 实现（覆盖前述暂缓 HTTP 的说明）

按 main 后续分工，我已接 Go HTTP/cmd/api；UI 负责 OpenAPI/schema/tag/generated，勿改 Go。

- `GET /api/v1/workspaces/{workspace_id}/synthesis/processing/{processing_id}/manuscript-review` → 200 **直接** `SynthesisManuscriptReviewSummary`，无额外包装。
- `GET /api/v1/workspaces/{workspace_id}/synthesis/processing/{processing_id}/manuscript-review/{note_id}` → 200 **直接** `SynthesisManuscriptReviewDetail`。
- `POST /api/v1/workspaces/{workspace_id}/synthesis/processing/{processing_id}/manuscript-review/{note_id}/decisions` → body 为 `DecideSynthesisManuscript`（完整 binding/note_id/attempt_id/capture_id/idempotency_key/resolution），200 **直接** Summary。Idempotency-Key header 可省略；如提供必须唯一且与 body 相同。POST 严格未知字段/Unicode/JSON，8MiB encoded document、1MiB string、最多1024 ordinals；领域再校验UTF8/NUL/文本大小。
- 三个路径均拒绝 query 参数，READ_LOCAL + WRITE_PROPOSAL；从 `authhttp.PrincipalFromContext` 创建不可变 caller，缺 principal（含 disabled 开发入口）返回403，绝不默认特权。
- 非待审处理 GET 返回404 `SYNTHESIS_MANUSCRIPT_REVIEW_NOT_PENDING`；仅此码可以投影为“无待处理冲突”，其他权限/漂移/5xx正常展示。已 submitted 的原 task 仍可 GET/精确决定 replay（同processing尚未换run）。
- 已注册 routes、auth capability 与 cmd/api factory；API 复用 workspaceRuntime.Resolver，同池 owner 和 immutable模型日志，不需要模型Provider。

pending task 发现：ProcessingRecord 可按 running/pending 的 processing_id GET 上述 summary；专用404表示尚未进入HumanWait，不需要改现有 processing 响应必需字段。真实 summary.binding 就是 pendingHumanTask identity。普通 workflow 页应仅链接该owner页，不能通用approve伪造 result_hash。

当前真实普通无 anchor conflict链 PASS33.247s，日志 `/tmp/manuscript-human-unanchored.log`；两阶段同task正在验收。

## UI 协调答复

已读取 manual-human-ui-coordination.md：conflicts 将在 HTTP 窄映射为 `{ordinal,base,current,proposed}` UTF8 string，不改内部ledger JSON/hash。

继续动作保持原任务第3条已实现语义：Decide 保存该阶段，全部 targets ready 后立即调用 SubmitHuman；响应丢失重放同一 Decide 会恢复已存结果后补交同 task。**当前无 /continue API，请 UI 不添加此未定义调用**。最后一份可标「确认并继续生成候选」，Submitted=true 后显示「已提交，正在生成候选」；如 main 明确改成用户额外显式 Continue 再统一协调。目前不增加一个原需求未要求的中间确认流程。

真实 cancel→FAILED/retryable→RetryProcessing→同processing新run/新task/新attempt/capture→裁决→apply 已通过；普通通用 HumanTask 伪造 result_hash 提交成功后，apply 因无真实 receipt 明确 FAILED，零候选。证据 `/tmp/manuscript-human-recovery.log` PASS37.249s。安全恢复 UI 可用既有 workflow cancel，然后重新读 processing 的version/Failure.Retryable，只有真实 FAILED/retryable 时启用既有 processing retry；RecoveryRequired不能使用此入口。

新增实际证据：HTTP 使用真实 PG auth repository ExchangeBootstrap→API Token→Auth Middleware→Principal→HumanReview，READ_LOCAL-only Token 403；真实 summary/detail GET和POST→后继apply，BodyRefresh冲突及两阶段同task均PASS40.359s（`/tmp/manuscript-human-http-integration.log`）。HTTP conflicts 已映射小写文本DTO；没有改内部receipt字段/hash。`/continue` 与main一致不新增。

实际 HumanAuthority 的 Document版本漂移、无anchor→新增真实anchor的scope漂移拒绝均PASS；多note生产用真实固定Provider生成两份变更（冲突项排第一、第二份clean）全部attempt/receipt齐备后原子生成两份候选，PASS。合并日志 `/tmp/manuscript-human-owner-multi.log` PASS56.409s。

供 UI 的真实 HTTP 报文采集正在运行：`go test -tags integration ./internal/organizing/adapter/postgres -run 'TestSynthesisManuscriptRuntimeBodyRefresh/human_two_stage$' -count=1 -timeout=5m`。成功后 `/tmp/zhixu-manuscript-http-fixtures/human_two_stage/` 将有 summary.json、detail-1.json、detail-2.json、decision-1-command.json/decision-1.json、decision-2-command.json/decision-2.json。它们来自真实 PG+River+Token Middleware+HTTP handler 的两阶段调用，不包含凭据。UI可用固定fixtures核对真实wire/组件；这不等于浏览器正在驱动活的PG。main下一步可在同个helper绑定loopback server复用真正owner，而非重造假model journal。原始测试文件 resolveRuntimeHuman / manuscriptAuthenticatedHTTP 是窄复用入口。

交接：上述两阶段报文现已全部生成，`/tmp/manuscript-human-http-captures.log` PASS25.360s；detail冲突字段确认为小写ordinal/base/current/proposed纯文本。实现报告 `manual-human-runtime-implementation.md` 已落盘，相关单测/vet/integration vet/diff检查已通过。无需等待本代理提供更多HTTP代码，UI可直接读取这套报文；原始第三方凭据未导出。

路由同步：UI的三条OpenAPI路径已落盘；Go inventory原期望238需随新增三路由更新为241，我只更新internal/app/router_inventory_test.go这一常量，继续保留实际method/path集合精确比较。OpenAPI文件未由我编辑。

## Resume 稳定契约（本轮新增）

`POST /api/v1/workspaces/{workspace_id}/synthesis/processing/{processing_id}/manuscript-review/resume`。body 严格为 `{"binding":完整SynthesisManuscriptHumanBinding}`，binding 含 workspace_id、processing_id、human_task_id、run_id、node_run_id、target_version。200 直接返回既有 `SynthesisManuscriptReviewSummary`（binding/ready/submitted/targets），成功 ready=true/submitted=true。无正文、resolution、hash、权限或 idempotency key；拒绝未知字段、query、路径与binding不一致。认证仍 READ_LOCAL+WRITE_PROPOSAL，逐请求真实caller/Root。

用于最后receipt已存而SubmitHuman前退出后的恢复：刷新GET为ready=true/submitted=false时，仅用其完整binding调用。owner只读并授权真实完整manifest，全部ready才构造owner result并提交原task；未ready/错task/workspace/篡改binding拒绝。已submitted同binding精确重放，无新模型/receipt。正常Decide最后一项仍自动Submit；Resume不新增确认步骤。此节覆盖此前“无恢复API”的限制；不新增/continue。
