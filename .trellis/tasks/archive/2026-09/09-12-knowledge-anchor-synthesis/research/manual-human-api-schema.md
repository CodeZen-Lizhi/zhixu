# Human review 接口协调（2026-09-15）

channel send 被沙箱拒绝写 ~/.trellis/channels 锁；此文件为 main / manuscript-resolution 的共享协调内容。

应用接口在 application/synthesis_manuscript_review_service.go：Read 返回 binding+ready+submitted+targets 身份列表；ReadNote 仅返回所选 note 三方 preview；Decide 幂等保存一阶段，全部 receipts ready 后才提交同一通用 task。

建议 HTTP：GET processing/{processingID}/manuscript-review；GET 同路径/{noteID}；POST 同路径/{noteID}/decisions。POST 接完整不可变 binding + note/attempt/capture + idempotency_key + resolution(stage,preview_fingerprint,acknowledged_ordinals,final_content)。路径 workspace/processing/note 必须与 body 一致。每调用从真实 principal capabilities 创建具名不可变 Caller；零值拒绝。无 Prepared、模型日志、authority 输出。具体 Go DTO 为唯一字段依据，HTTP 尚待接线。

HumanTask schema 仅 version/processing_id/workflow_run_id/note_ids/result_hash，完整 note 集合 enum 绑定；result_hash 是按 note 排序的真实 receipt identities hash。不能直接授权全文。

124 配合：ReadManifest 应精确过滤冻结 workflow_run_id（processing retry 共享 processing ID）；同一冻结模型全 changed-existing targets 重算，不用查到的 rows 代替期望集合。runtime 会准备全部 targets，再返回真实 HumanWaitResult。

现有 processing retry 仅允许 FAILED&&retryable，RecoveryRequired 不可重试。当前 pending HumanTask stale 可走现有 workflow cancel → terminal hook FAILED/retryable → 显式 synthesis retry 新 run/attempt；须实际验证 cancel 待审节点路径，不展示无后端支持的 reprepare 按钮。
