import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { prepareNoteInterview, type NoteInterviewOptions, type NoteInterviewPreparation, type SynthesisRevision } from "../../api/synthesis";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, ErrorState } from "../../shared/ui";
import { synthesisQueryKeys, useNoteInterviewPreparation, useNoteInterviewPreparations } from "./queries";

const statusLabel: Record<NoteInterviewPreparation["status"], string> = {
  QUEUED: "等待准备", GENERATING: "正在生成题目", READY: "可以开始面试", FAILED: "准备失败", CAPABILITY_UNAVAILABLE: "模型暂不可用", RECOVERY_REQUIRED: "需要人工恢复",
};

export const NoteInterviewPanel = ({ noteId, publishedRevision }: { noteId: string; publishedRevision: SynthesisRevision | null }) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [params, setParams] = useSearchParams();
  const [options, setOptions] = useState<NoteInterviewOptions>({ role: "知识复习", difficulty: "INTERMEDIATE", durationMinutes: 30, questionCount: 6, maxFollowUps: 3 });
  const preparationId = params.get("preparation_id") ?? "";
  const selected = useNoteInterviewPreparation(noteId, preparationId);
  const recent = useNoteInterviewPreparations(noteId);
  const pending = useRef<{ fingerprint: string; key: string; controller: AbortController } | null>(null);
  useEffect(() => () => { pending.current?.controller.abort(); }, [workspaceId, noteId]);
  const mutation = useMutation({
    mutationFn: ({ options, retryId, key, signal }: { options: NoteInterviewOptions; retryId?: string; key: string; signal: AbortSignal }) => prepareNoteInterview(workspaceId, noteId, options, key, retryId, signal),
    retry: false,
    onSuccess: (preparation) => {
      if (getActiveWorkspaceId() !== workspaceId) return;
      queryClient.setQueryData(synthesisQueryKeys.preparation(workspaceId, noteId, preparation.id), preparation);
      void queryClient.invalidateQueries({ queryKey: synthesisQueryKeys.preparations(workspaceId, noteId) });
      setParams((current) => { const next = new URLSearchParams(current); next.set("preparation_id", preparation.id); return next; });
      pending.current = null;
    },
    onError: () => { if (getActiveWorkspaceId() === workspaceId) void recent.refetch(); },
  });
  const prepare = (retry?: NoteInterviewPreparation) => {
    if (mutation.isPending) return;
    const requestOptions = retry?.options ?? options;
    const fingerprint = JSON.stringify([workspaceId, noteId, retry?.noteRevision.revisionId ?? publishedRevision?.id, retry?.id, requestOptions]);
    if (pending.current?.fingerprint !== fingerprint) pending.current = { fingerprint, key: crypto.randomUUID(), controller: new AbortController() };
    const operation = pending.current;
    mutation.mutate({ options: requestOptions, ...(retry === undefined ? {} : { retryId: retry.id }), key: operation.key, signal: operation.controller.signal });
  };
  const preparation = selected.data ?? (preparationId === "" ? recent.data?.[0] : undefined);
  const isPreparing = preparation?.status === "QUEUED" || preparation?.status === "GENERATING";
  const minimumQuestions = publishedRevision === null ? 1 : new Set(publishedRevision.items.map((item) => item.kind)).size;
  return <section className="synthesis-interview" aria-labelledby="synthesis-interview-heading">
    <h2 id="synthesis-interview-heading">用这篇笔记面试</h2>
    <p className="synthesis-help">问题围绕已发布版本的事实、冲突和缺口。面试开始后，题目引用的版本保持不变。</p>
    {publishedRevision === null ? <p>批准并发布笔记后，可以准备面试。</p> : <form onSubmit={(event) => { event.preventDefault(); prepare(); }}>
      <div className="synthesis-interview-fields">
        <label>面试方向<input value={options.role} maxLength={256} required onChange={(event) => { setOptions({ ...options, role: event.target.value }); }} /></label>
        <label>难度<select value={options.difficulty} onChange={(event) => {
          const value = event.target.value; if (value === "FOUNDATION" || value === "INTERMEDIATE" || value === "ADVANCED") setOptions({ ...options, difficulty: value });
        }}><option value="FOUNDATION">基础</option><option value="INTERMEDIATE">进阶</option><option value="ADVANCED">深入</option></select></label>
        <label>时长（分钟）<input type="number" min={1} max={240} value={options.durationMinutes} required onChange={(event) => { setOptions({ ...options, durationMinutes: event.target.valueAsNumber }); }} /></label>
        <label>题目数<input type="number" min={minimumQuestions} max={20} value={options.questionCount} required onChange={(event) => { setOptions({ ...options, questionCount: event.target.valueAsNumber }); }} /></label>
        <label>最多追问<input type="number" min={0} max={20} value={options.maxFollowUps} required onChange={(event) => { setOptions({ ...options, maxFollowUps: event.target.valueAsNumber }); }} /></label>
      </div>
      <Button type="submit" disabled={mutation.isPending || isPreparing}>{mutation.isPending ? "正在提交…" : isPreparing ? "正在准备题目…" : "准备 AI 面试"}</Button>
    </form>}
    {mutation.isError ? <ErrorState title="面试准备请求未完成" description={`${mutation.error.message} 下方会重新查询已创建的准备任务。`} /> : null}
    {selected.isError && preparationId !== "" ? <ErrorState description={selected.error.message} onRetry={() => { void selected.refetch(); }} /> : null}
    {recent.isError ? <ErrorState description={recent.error.message} onRetry={() => { void recent.refetch(); }} /> : null}
    {preparation ? <div className="synthesis-preparation" role="status"><div className="synthesis-item-heading"><strong>{preparation.options.role}</strong><Badge tone={preparation.status === "READY" ? "success" : preparation.failure === null ? "info" : "danger"}>{statusLabel[preparation.status]}</Badge></div>
      <p>基于笔记版本 {preparation.noteRevision.revisionNo}，{preparation.options.questionCount} 道题。</p>
      {preparation.failure ? <p className="synthesis-failure">{preparation.status === "RECOVERY_REQUIRED" ? "上次模型调用结果尚不能确认，请先查看处理过程。" : "题目尚未准备好，可检查模型设置后按允许状态重试。"}<span>错误编号：{preparation.failure.code}</span></p> : null}
      <div className="synthesis-actions">{preparation.sessionId !== null ? <Button asChild><Link to={`/interviews/${preparation.sessionId}`}>继续面试</Link></Button> : null}
        <Button asChild variant="ghost"><Link to={`/workflows/${preparation.workflowRunId}`}>查看准备过程</Link></Button>
        {preparation.failure?.retryable && preparation.status !== "RECOVERY_REQUIRED" ? <Button variant="secondary" disabled={mutation.isPending} onClick={() => { prepare(preparation); }}>重试准备</Button> : null}</div>
    </div> : null}
    {recent.data && recent.data.length > 1 ? <details><summary>最近面试准备</summary><ul>{recent.data.map((item) => <li key={item.id}><button type="button" className="synthesis-history-button" onClick={() => { setParams((current) => { const next = new URLSearchParams(current); next.set("preparation_id", item.id); return next; }); }}>{item.options.role} · 版本 {item.noteRevision.revisionNo} · {statusLabel[item.status]}</button></li>)}</ul></details> : null}
  </section>;
};
