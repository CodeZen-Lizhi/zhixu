import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { decideManuscript, resumeManuscript, getManuscriptDetail, getManuscriptSummary, manuscriptBindingKey, ManuscriptApiError, validManuscriptText, type ManuscriptBinding, type ManuscriptDecision, type ManuscriptDetail, type ManuscriptSummary, type ManuscriptTarget } from "../../api/synthesis-manuscript";
import { isAbortError } from "../../shared/codec";
import { MonacoTextEditor } from "../../shared/MonacoTextEditor";
import { Button, ErrorState, Tabs, TabsContent, TabsList, TabsTrigger } from "../../shared/ui";
import { manuscriptSummaryKey, synthesisQueryKeys } from "./queries";

import "./manuscript-review.css";

const MonacoDiffViewer = lazy(() => import("../../shared/MonacoDiffViewer").then((m) => ({ default: m.MonacoDiffViewer })));
interface Draft { detail: ManuscriptDetail; content: string; acknowledged: number[]; phase: "active" | "saving" | "unknown" | "stale"; command?: ManuscriptDecision | undefined; message: string; editorStatus: "loading" | "ready" | "error"; editorError: string; diffError: string; editorEpoch: number }
const targetKey = (t: ManuscriptTarget) => [t.noteId, t.attemptId, t.captureId, t.stage, t.previewFingerprint, t.ready].join(":");

export const ManuscriptReviewWorkbench = ({ workspaceId, processingId }: { workspaceId: string; processingId: string }) => <ManuscriptReviewSession key={`${workspaceId}:${processingId}`} workspaceId={workspaceId} processingId={processingId} />;
const ManuscriptReviewSession = ({ workspaceId, processingId }: { workspaceId: string; processingId: string }) => {
  const client = useQueryClient();
  const summary = useQuery({ queryKey: manuscriptSummaryKey(workspaceId, processingId), queryFn: ({ signal }) => getManuscriptSummary(workspaceId, processingId, signal), retry: false, gcTime: 0, refetchOnWindowFocus: false });
  const [selected, setSelected] = useState("");
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [readError, setReadError] = useState("");
  const [loading, setLoading] = useState(false);
  const [reload, setReload] = useState(0);
  const [resume, setResume] = useState<{ phase: "idle" | "saving" | "unknown" | "failed"; binding?: ManuscriptBinding; message: string }>({ phase: "idle", message: "" });
  const live = useRef(true);
  const submitting = useRef(false);
  const controllers = useRef(new Set<AbortController>());
  useEffect(() => { live.current = true; return () => { live.current = false; for (const c of controllers.current) c.abort(); }; }, []);
  const current = summary.data?.targets.find((t) => t.noteId === selected);
  const draft = drafts[selected];
  const identity = summary.data ? manuscriptBindingKey(summary.data.binding) : "";
  const targetIdentity = current ? targetKey(current) : "";
  const latestBinding = useRef(identity);
  latestBinding.current = identity;
  const snapshot = `${identity}:${summary.data?.targets.map(targetKey).join("|") ?? ""}:${String(summary.data?.submitted)}`;
  const latestSnapshot = useRef(snapshot);
  latestSnapshot.current = snapshot;
  useEffect(() => {
    if (!summary.data || !current || current.ready || summary.data.submitted) return;
    const controller = new AbortController();
    let active = true;
    setLoading(true); setReadError("");
    void getManuscriptDetail(summary.data, current, controller.signal).then((detail) => {
      if (!active) return;
      setDrafts((old) => {
        const previous = old[selected];
        if (previous) {
          const same = manuscriptBindingKey(previous.detail.binding) === manuscriptBindingKey(detail.binding) && targetKey(previous.detail.target) === targetKey(detail.target);
          if (previous.phase === "unknown" && manuscriptBindingKey(previous.detail.binding) === manuscriptBindingKey(detail.binding) && previous.detail.target.attemptId === detail.target.attemptId && previous.detail.target.captureId === detail.target.captureId) return old;
          return same ? old : { ...old, [selected]: { ...previous, phase: "stale", message: "预览身份已变化。旧编辑保留供查看，不能提交到新阶段或任务。" } };
        }
        return { ...old, [selected]: { detail, content: detail.review?.candidate ?? "", acknowledged: [], phase: "active", message: "", editorStatus: "loading", editorError: "", diffError: "", editorEpoch: 0 } };
      });
    }, (error: unknown) => {
      if (!active || isAbortError(error)) return;
      const message = error instanceof Error ? error.message : "预览读取失败。";
      setReadError(message);
      if (error instanceof ManuscriptApiError && error.status === 409) setDrafts((old) => old[selected] ? { ...old, [selected]: { ...old[selected], phase: "stale", message: "当前绑定已失效，旧编辑保留供查看。" } } : old);
    }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; controller.abort(); };
  // 身份值绑定精确的摘要和目标；普通重新获取不会重置草稿。
  }, [identity, targetIdentity, selected, reload]);
  useEffect(() => {
    if (!Object.values(drafts).some((d) => d.content !== d.detail.review?.candidate || d.phase === "unknown")) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn); return () => window.removeEventListener("beforeunload", warn);
  }, [drafts]);
  const update = (change: Partial<Draft>) => setDrafts((old) => old[selected] ? { ...old, [selected]: { ...old[selected], ...change } } : old);
  // Monaco 回调归属于一个详情对象和一次显式加载尝试。
  // 已分离的编辑器绝不能授权替换后的草稿或清除后续失败。
  const updateMonaco = (change: Partial<Draft>, ready = false) => setDrafts((old) => {
    const active = old[selected];
    if (!live.current || !draft || active?.detail !== draft.detail || active.editorEpoch !== draft.editorEpoch || latestBinding.current !== manuscriptBindingKey(draft.detail.binding)) return old;
    if (ready && active.editorStatus !== "loading") return old;
    return { ...old, [selected]: { ...active, ...change } };
  });
  const sameBinding = draft && summary.data && current && manuscriptBindingKey(draft.detail.binding) === identity && targetKey(draft.detail.target) === targetIdentity;
  const canReplay = draft?.phase === "unknown" && draft.command !== undefined && current !== undefined && manuscriptBindingKey(draft.command.binding) === identity && draft.command.noteId === current.noteId && draft.command.attemptId === current.attemptId && draft.command.captureId === current.captureId;
  const locked = !sameBinding || draft.phase !== "active" || loading || readError !== "" || summary.isError;
  const review = draft?.detail.review;
  const newSubmissionBlocked = locked || draft.editorStatus !== "ready" || draft.diffError !== "";
  const save = async () => {
    if (!summary.data || !draft || !review || !sameBinding && !canReplay || submitting.current || draft.phase === "stale" || draft.phase === "saving") return;
    if (draft.phase === "unknown" ? !canReplay : newSubmissionBlocked || !validManuscriptText(draft.content) || draft.acknowledged.length !== review.conflicts.length) return;
    const command: ManuscriptDecision = draft.command ?? { binding: draft.detail.binding, noteId: selected, attemptId: draft.detail.target.attemptId, captureId: draft.detail.target.captureId, idempotencyKey: crypto.randomUUID(), resolution: { stage: review.stage, previewFingerprint: draft.detail.target.previewFingerprint, acknowledgedOrdinals: [...draft.acknowledged].sort((a, b) => a - b), finalContent: draft.content } };
    const noteId = selected;
    const startedSnapshot = snapshot;
    const controller = new AbortController(); controllers.current.add(controller); submitting.current = true;
    update({ phase: "saving", command, message: "" });
    try {
      const result: ManuscriptSummary = await decideManuscript(command, controller.signal, summary.data.targets);
      if (!live.current) return;
      if (latestBinding.current !== manuscriptBindingKey(command.binding) || latestSnapshot.current !== startedSnapshot) {
        setDrafts((old) => old[noteId] ? { ...old, [noteId]: { ...old[noteId], phase: "stale", message: "任务或阶段已变化。旧请求结果不会覆盖当前状态，编辑保留供查看。" } } : old);
        return;
      }
      setDrafts((old) => Object.fromEntries(Object.entries(old).filter(([key]) => key !== noteId)));
      client.setQueryData(manuscriptSummaryKey(workspaceId, processingId), result);
      setReload((value) => value + 1);
      void client.invalidateQueries({ queryKey: synthesisQueryKeys.processing(workspaceId) });
    } catch (error) {
      if (!live.current || isAbortError(error)) return;
      const bindingChanged = latestBinding.current !== manuscriptBindingKey(command.binding);
      const unknown = !bindingChanged && (!(error instanceof ManuscriptApiError) || error.status === null || error.status >= 500);
      const stale = bindingChanged || error instanceof ManuscriptApiError && [403, 404, 409].includes(error.status ?? 0);
      setDrafts((old) => old[noteId] ? { ...old, [noteId]: { ...old[noteId], phase: unknown ? "unknown" : stale ? "stale" : "active", command: unknown ? command : undefined, message: unknown ? "提交结果未知。编辑与原命令已冻结，请精确重试；不要另建裁决。" : stale ? "当前任务或预览已失效。编辑保留供查看，旧提交已停用。可查看处理过程，取消旧流程后按真实失败状态重试整理。" : error instanceof Error ? error.message : "保存失败，编辑已保留。" } } : old);
    } finally { controllers.current.delete(controller); submitting.current = false; }
  };
  const recover = async () => {
    if (!summary.data?.ready || summary.data.submitted || summary.isError || submitting.current) return;
    const b = resume.binding ?? summary.data.binding;
    if (manuscriptBindingKey(b) !== manuscriptBindingKey(summary.data.binding)) return;
    const controller = new AbortController(); controllers.current.add(controller); submitting.current = true;
    setResume({ phase: "saving", binding: b, message: "" });
    try {
      const result = await resumeManuscript(b, controller.signal);
      if (!live.current) return;
      if (latestBinding.current !== manuscriptBindingKey(b)) { setResume({ phase: "failed", binding: b, message: "任务已变化，旧恢复结果不会覆盖当前任务。" }); return; }
      client.setQueryData(manuscriptSummaryKey(workspaceId, processingId), result);
      setResume({ phase: "idle", message: "" });
      void client.invalidateQueries({ queryKey: synthesisQueryKeys.processing(workspaceId) });
    } catch (error) {
      if (!live.current || isAbortError(error)) return;
      const unknown = !(error instanceof ManuscriptApiError) || error.status === null || error.status >= 500;
      setResume({ phase: unknown ? "unknown" : "failed", binding: b, message: unknown ? "恢复结果未知，请使用同一绑定精确重试。" : error instanceof Error ? error.message : "恢复整理失败。" });
    } finally { controllers.current.delete(controller); submitting.current = false; }
  };
  return <section className="synthesis-list-section manuscript-review" aria-label="主笔记冲突裁决">
    <h2>主笔记冲突裁决</h2><p>逐份比较基线、当前内容和拟议内容，编辑完整合并结果。生成候选后仍须原审批才能发布。</p>
    {summary.isPending ? <p role="status">正在读取待裁决摘要…</p> : null}
    {summary.error instanceof ManuscriptApiError && summary.error.status === 404 && summary.error.code === "SYNTHESIS_MANUSCRIPT_REVIEW_NOT_PENDING" ? <p>当前没有待处理的主笔记冲突。</p> : summary.isError ? <ErrorState description={summary.error.message} onRetry={() => { void summary.refetch(); }} /> : null}
    {summary.data?.submitted ? <p role="status">裁决已保存，正在生成待审核候选。<Link to={`/workflows/${summary.data.binding.runId}`}>查看处理过程</Link></p> : null}
    {summary.data?.ready && !summary.data.submitted ? <div><p>所有裁决已保存，整理流程尚未恢复。无需重新输入正文。</p><Button disabled={summary.isError || resume.phase === "saving" || resume.binding !== undefined && manuscriptBindingKey(resume.binding) !== identity} onClick={() => { void recover(); }}>{resume.phase === "saving" ? "正在恢复整理…" : resume.phase === "unknown" ? "使用同一绑定重试恢复" : "继续生成候选 / 恢复整理"}</Button>{resume.message ? <p role="alert">{resume.message}</p> : null}</div> : null}
    {summary.data && !summary.data.submitted ? <>
      <div className="button-row">{summary.data.targets.map((t, index) => <Button key={t.noteId} variant="secondary" aria-pressed={selected === t.noteId} disabled={draft?.phase === "saving" || draft?.phase === "unknown"} onClick={() => { if (selected !== t.noteId) setDrafts((old) => { const previous = old[t.noteId]; return previous ? { ...old, [t.noteId]: { ...previous, editorStatus: "loading", editorEpoch: previous.editorEpoch + 1 } } : old; }); setSelected(t.noteId); }}>主笔记 {index + 1} · {t.noteId.slice(0, 8)} · {t.ready ? "已完成" : "待裁决"}</Button>)}</div>
      {selected === "" ? <p>请选择一份主笔记后读取全文预览。</p> : null}
      {current?.ready ? <p role="status">这份主笔记的裁决已保存。请处理其余待裁决笔记。</p> : null}
      {loading ? <p role="status">正在读取所选主笔记预览…</p> : null}
      {readError ? <ErrorState description={readError} onRetry={() => { void summary.refetch(); setReload((v) => v + 1); }} /> : null}
      {draft && review && (!current?.ready || draft.phase === "unknown") ? <div>
        <h3>{review.stage === "CANDIDATE_MANUAL_CONTENT" ? "阶段一：保留候选中的人工修改" : "阶段二：合并工作区中的人工修改"}</h3>
        {(!sameBinding && !canReplay || draft.phase === "stale") ? <p role="alert">旧编辑仅供查看。当前阶段或任务已变化，不能使用旧确认提交。</p> : null}
        <Tabs defaultValue="current" key={`${selected}:${review.stage}`}><TabsList aria-label="三方比较"><TabsTrigger value="current">Base / Current</TabsTrigger><TabsTrigger value="proposed">Base / Proposed</TabsTrigger><TabsTrigger value="conflicts">冲突逐项确认</TabsTrigger></TabsList>
          {(["current", "proposed"] as const).map((side) => <TabsContent key={side} value={side}><Suspense fallback={<p>正在加载差异…</p>}><MonacoDiffViewer key={`${selected}:${String(draft.editorEpoch)}:${side}`} identityKey={`${identity}:${targetIdentity}:${side}`} original={review.base} modified={review[side]} originalModelPath={`manuscript://${identity}/${targetIdentity}/${side}/base`} modifiedModelPath={`manuscript://${identity}/${targetIdentity}/${side}`} language="markdown" height="320px" onReady={() => { /* 差异视图就绪不代表允许提交。 */ }} onError={(error) => updateMonaco({ diffError: error.message })} /></Suspense></TabsContent>)}
          <TabsContent value="conflicts">{review.conflicts.map((conflict) => <fieldset key={conflict.ordinal} disabled={locked}><legend>冲突 {conflict.ordinal}</legend><div className="manuscript-conflict-text"><details><summary>Base</summary><pre>{conflict.base}</pre></details><details><summary>Current</summary><pre>{conflict.current}</pre></details><details><summary>Proposed</summary><pre>{conflict.proposed}</pre></details></div><label><input type="checkbox" checked={draft.acknowledged.includes(conflict.ordinal)} onChange={(event) => update({ acknowledged: event.target.checked ? [...draft.acknowledged, conflict.ordinal] : draft.acknowledged.filter((n) => n !== conflict.ordinal) })} />已在完整结果中处理冲突 {conflict.ordinal}</label></fieldset>)}</TabsContent>
        </Tabs>
        <h3>完整合并结果</h3><MonacoTextEditor key={`${selected}:${targetKey(draft.detail.target)}:${String(draft.editorEpoch)}`} ariaLabel="完整合并结果" preserveWhitespace value={draft.content} modelPath={`manuscript://${manuscriptBindingKey(draft.detail.binding)}/${targetKey(draft.detail.target)}/result`} language="markdown" height="400px" disabled={locked} onChange={(content) => { if (!locked) update({ content }); }} onReady={() => updateMonaco({ editorStatus: "ready" }, true)} onError={(error) => updateMonaco({ editorStatus: "error", editorError: error.message })} />
        {draft.editorStatus === "loading" ? <p role="status">正在准备编辑器，加载完成后才能提交新裁决。</p> : null}
        {draft.editorStatus === "error" || draft.diffError !== "" ? <div role="alert"><p>编辑器或差异加载失败，新裁决已停用。{draft.editorError || draft.diffError}</p><Button variant="secondary" onClick={() => update({ editorStatus: "loading", editorError: "", diffError: "", editorEpoch: draft.editorEpoch + 1 })}>重新加载编辑器与差异</Button></div> : null}
        <p>{new TextEncoder().encode(draft.content).length} / 1048576 UTF-8 字节 · 已确认 {draft.acknowledged.length} / {review.conflicts.length} 项冲突</p>
        {!validManuscriptText(draft.content) ? <p role="alert">正文必须为有效 UTF-8、不含 NUL，且不超过 1 MiB。</p> : null}
        {draft.message ? <p role="alert">{draft.message}</p> : null}
        {draft.phase === "stale" ? <Link to={`/workflows/${draft.detail.binding.runId}`}>查看处理过程与恢复操作</Link> : null}
        <Button disabled={draft.phase === "unknown" ? !canReplay || submitting.current : newSubmissionBlocked || !validManuscriptText(draft.content) || draft.acknowledged.length !== review.conflicts.length} onClick={() => { void save(); }}>{draft.phase === "unknown" ? "使用原命令精确重试" : draft.phase === "saving" ? "正在保存…" : summary.data.targets.filter((t) => !t.ready).length === 1 ? "保存裁决并继续整理" : "保存本阶段裁决"}</Button>
      </div> : null}
    </> : null}
  </section>;
};
