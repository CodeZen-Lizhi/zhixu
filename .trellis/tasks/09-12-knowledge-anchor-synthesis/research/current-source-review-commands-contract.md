# 当前全文复核恢复命令合同（127 已实现）

2026-09-15，current-source-review-recovery owner；main 于22:08更新收口状态。实现、最终实库/HTTP/generated React、浏览器恢复与正式schema迁移/空库恢复均已通过，独立复审原4项P2已修复。下文带时间的小节保留实施历史，最终合同以 final-fixes 段为准；验证边界见 implementation 和 final-review 报告，外部模型质量尚未验证。

只新增两个命令：POST 原 source-reviews/:id/recheck 和 /recover。workspace 与 review ID 来自现有精确父路由；body 仅 expected_version（正整数）与 idempotency_key（1–128字符）。不接收模型、scope、source、proof、全文或 fence。首次自动 dispatcher 保留。

应用入口：SynthesisManuscriptSourceReviewCommandService，构造 NewSynthesisManuscriptSourceReviewCommandService(store SynthesisManuscriptSourceReviewCommandStore, caller SynthesisManuscriptCaller)。caller 来自真实认证 capabilities，要求 ReadLocal + WriteProposal；零值拒绝。Store 由 GORMSynthesisManuscriptSourceReviewStore 提供，运行启动依赖通过 ConfigureSourceReviewCommands(starter, definitions) 配置。API 无须创建 Provider。

DTO SynthesisManuscriptSourceReviewCommand：WorkspaceID、ReviewID foundation.ID；ExpectedVersion int64；IdempotencyKey string。方法 Recheck(ctx, command)、Recover(ctx, command)，返回 SynthesisSourceReviewView。服务也提供 Read(ctx, workspace, id)。既有安全读取仍由 SourceReviewView 提供，禁止直接序列化内部 record。

View 保留全部既有字段，新增 supersedes_id（可省略）、latest（bool）、can_recheck（bool）、can_recover（bool）、recovery_workflow_run_id（可省略）、recovery_status（可省略）；attempt_no 从持久值读取。status 是旧 review 历史；recovery_status 是独立恢复执行状态。completed 仅全义务证据闭合且当前全文/来源/scope/Root仍有效时为 true。SUCCEEDED 历史可与当前 STALE 并存。UI只根据 can_* 显示操作，服务端每次重新裁决。

Recheck：先核当前权限/Root及workspace；同键同参数先返回已持久 winner，异参冲突。新命令要求 expected_version、最新链、最多10次、无活动或未知旧调用。FAILED+retryable 可重试；STALE/REJECTED/SUCCEEDED仅实际 baseline改变可新复核。当前有效成功可返回原结果；同baseline拒绝不再次调用。新source version不改绑旧origin义务，应走原source-ready。冻结当前批准scope，历史双模型proof仍按旧scope验证。

Recover：只应用已持久accepted原字节+原ModelRun/Calls。旧run必须结束；创建独立恢复workflow，真实新fence授权写入，旧model/run/output身份不改且零Provider。未知/仅Call hash拒绝。已提交receipt精确重放返回原历史事实与最新有效性，不要求旧lease复活。

主要错误：SYNTHESIS_SOURCE_REVIEW_CONFLICT（版本/命令异参）、SYNTHESIS_SOURCE_REVIEW_NOT_LATEST、SYNTHESIS_SOURCE_REVIEW_ATTEMPT_LIMIT、SYNTHESIS_SOURCE_REVIEW_BASELINE_UNCHANGED、SYNTHESIS_SOURCE_REVIEW_RECOVERY_REQUIRED（未知不可重跑）、SYNTHESIS_SOURCE_REVIEW_NOT_RECOVERABLE、SYNTHESIS_SOURCE_REVIEW_EXECUTION_ACTIVE；权限用既有 foundation permission denied。刷新重读原列表与命令返回的review ID，不能把旧review状态覆盖为successor状态。

## 127 已实现接口与迁移交接（21:44）

上述 factory/service/DTO/字段已实际落盘并通过定向编译，cmd/api 与 HTTP 可使用该形状；恢复定义由 `workflow.SourceReviewDefinitions()` 一起注册。worker executor 需注册新增 `application.SynthesisSourceReviewRecover` kind，已接。

`STALE` 终态且有 accepted bytes 时也可 Recover：前提当前 baseline 恢复为原快照；独立 recovery receipt 保存结果，旧 STALE/status/version/output 不变，View 可为 status=STALE + recovery_status=succeeded + completed=true（仅当前全部义务有效）。原 REVIEWED 非终态恢复正常推进 SUCCEEDED。读 completed 时不要只用 status===SUCCEEDED 在 Web 重算；以 owner 的 completed/effective_status 为准。

明确边界：RECHECK 总是新的实际复核（除当前同baseline成功可直接返回）；A→B→A 已验证每次显式复核各调用一次 Provider，并在 apply 全量复用 A 的已有真实 evidence/result manifest。没有实现“找到历史 A 就完全跳过本次模型”的额外快路径。部分复用沿相同逐义务闭包逻辑，但尚无多目标真实PG专项证据。

127 SQL 已就绪：`atlas/migrations/00127_synthesis_manuscript_source_review_recovery.sql`。本 owner 未编辑 atlas.sum/schema.sql；实库用仅替换 atlas.sum 的 `/tmp/source-review-127-overlay.json` 执行正式 SQL 文件。请 main 更新共享 sum 后正式迁移/导出/空库恢复。channel send 因沙箱写 `~/.trellis/channels/...lock` 返回 EPERM，无法发送命令通知，交接以本文件和最终回复为准。

## 2026-09-15 final-fixes 实际 DTO（覆盖上文“SQL已就绪”）

应用 DTO 已落盘：`SynthesisSourceReviewEvidenceView.Obligations []string json:"obligations"` 替代 singular Obligation；每个物理 ID 仅一条，集合来自当前 result.obligation，Source 来自物理 evidence 的完整 SourceRef。Open 复用此对象。新增 `SynthesisSourceReviewView.RecoveryCompletedAt *time.Time json:"recovery_completed_at,omitempty"`，精确来自独立 recovery_receipt.created_at；CompletedAt 保留原 review 历史时刻，不伪改 STALE 历史。

`recovery_status` 必须保持真实 workflow 状态，不能仅因 receipt 存在伪装 succeeded（事务已提交 receipt、节点最终化前可能仍 running）。完成判据：owner completed + effective_status=CURRENT +（历史 status=SUCCEEDED，或独立 receipt_hash 非空且 recovery_completed_at 非空并有 recovery_workflow_run_id）。STALE 恢复显示实际 recovery_completed_at；不能把任意 STALE 或模型 succeeded 当完成。后续漂移保留 receipt/时间但 completed=false。HTTP/Web owner 请同步此真实 receipt 谓词，不能继续要求 recovery_status=succeeded。

127 正在补 target 摘要独立验证及多次独立恢复并发守卫，尚未稳定；勿依据上文旧就绪声明复跑迁移。main 最后统一 sum/schema。

## final-fixes 稳定迁移交接

127 SQL 已停止编辑，SHA-256 `2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a`。main 可统一 atlas.sum/schema 导出；UI 可用最终 `TestSynthesisManuscriptSourceReviewRuntime/multi_obligation`、`file_drift`、`terminal_recovery` 模式作 HTTP/React overlay。当前最终版本 multi_obligation 已真实 PG PASS：1 evidence + 2 manifest、公开 reader/Open/refresh 均完整；其余三个窄专项正在本进程复验。测试入口没有改名。

target_hash 协议已实现为 frozen snapshot 原始 JSON target token 的 SHA256；SQL 使用 json 保留原始 token 次序/转义/空格，不能改成 jsonb::text。新增 evidence guard 独立重算；result guard 同时核旧 proof 的冻结 target hash。126 SQL/旧不可变 evidence 未修改。

channel 发送仍 EPERM；本共享合同即稳定信号，不等待额外审批。

final-fixes 核心收口：最终 SQL 四专项 multi_obligation/recovery_retry/file_drift 通过；target_hash 独立最终重跑 PASS 22.86s，包含错误 hash、冻结 target note_id、来源 SourceID 三个 SQLSTATE 23514 负例且正常 apply 成功。相关包 go test/vet、integration-tag vet 退出0。详见 current-source-review-recovery-implementation.md。SQL hash 不变，backend owner 已停止修改，main/UI 可按稳定版本完成外层验收。
