import { useEffect, useRef } from "react";
import { Link } from "react-router-dom";
import type { SynthesisProcessing, SynthesisProcessingStatus } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, ErrorState } from "../../shared/ui";
import { useRetrySynthesis } from "./queries";

const labels: Record<SynthesisProcessingStatus, string> = {
  PENDING: "等待整理", RUNNING: "正在整理", SUCCEEDED: "已生成更新", NO_CHANGE: "已覆盖，无需更新", SKIPPED: "无需合成", FAILED: "整理失败", RECOVERY_REQUIRED: "需要人工恢复",
};

export const ProcessingRecord = ({ processing }: { processing: SynthesisProcessing }) => {
  const workspaceId = useActiveWorkspaceId();
  const retry = useRetrySynthesis();
  const pending = useRef<{ version: number; key: string; controller: AbortController } | null>(null);
  useEffect(() => () => { pending.current?.controller.abort(); }, [workspaceId]);
  const startRetry = () => {
    if (retry.isPending) return;
    if (pending.current?.version !== processing.version) pending.current = { version: processing.version, key: crypto.randomUUID(), controller: new AbortController() };
    const operation = pending.current;
    retry.mutate({ workspaceId, processingId: processing.id, expectedVersion: processing.version, idempotencyKey: operation.key, signal: operation.controller.signal });
  };
  const canRetry = processing.status === "FAILED" && processing.failure?.retryable === true;
  const failed = processing.status === "FAILED" || processing.status === "RECOVERY_REQUIRED";
  return <article className="synthesis-processing-record">
    <div className="synthesis-item-heading"><Link to={`/documents/${processing.sourceVersionId}`}>资料 {processing.sourceVersionId.slice(0, 8)}</Link><Badge tone={failed ? "danger" : processing.status === "SUCCEEDED" || processing.status === "NO_CHANGE" ? "success" : "neutral"}>{labels[processing.status]}</Badge></div>
    <div className="synthesis-meta"><time dateTime={processing.updatedAt}>{new Date(processing.updatedAt).toLocaleString("zh-CN", { hour12: false })}</time>
      {processing.workflowRunId !== null ? <Link to={`/workflows/${processing.workflowRunId}`}>查看处理过程</Link> : null}</div>
    {processing.failure ? <p className="synthesis-failure">{processing.failure.code === "SYNTHESIS_MODEL_CAPABILITY_UNAVAILABLE" ? "模型暂不可用。请在设置中完成模型配置后重试。" : processing.status === "RECOVERY_REQUIRED" ? "上次执行结果尚不能确认，请先检查处理过程。" : "这份资料的整理未完成，原始资料仍然保留。"}<span>错误编号：{processing.failure.code}</span></p> : null}
    {canRetry ? <Button variant="secondary" disabled={retry.isPending} onClick={startRetry}>{retry.isPending ? "正在提交重试…" : "重试整理"}</Button> : null}
    {retry.isError ? <ErrorState title="重试未完成" description={retry.error.message} /> : null}
  </article>;
};
