import { useEffect, useRef, useState, type ComponentType } from "react";
import type { MonacoTextEditorProps } from "../../shared/MonacoTextEditor";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { resumeCandidateRemerge, applyCandidateRemerge, beginCandidateRemerge, readCandidateRemerge, getCandidateRemergeTarget, validRemergeKey, CandidateRemergeApiError, type CandidateRemergeApply, type CandidateRemergeBegin, type CandidateRemergeReview } from "../../api/synthesis-candidate-remerge";
import { validManuscriptText } from "../../api/synthesis-manuscript";
import { isAbortError } from "../../shared/codec";

import { MarkdownPreview } from "../authoring/MarkdownPreview";
import { Button } from "../../shared/ui";
import { synthesisQueryKeys } from "./queries";
import "./manuscript-review.css";

// 加载只读笔记不得初始化 Monaco。模块加载失败时，
// 所属草稿仍保持未就绪，同时保留其文本。
const RemergeEditor = (props: MonacoTextEditorProps) => {
  const [Editor, setEditor] = useState<ComponentType<MonacoTextEditorProps>>();
  const [failed, setFailed] = useState(false);
  const onError = useRef(props.onError); onError.current = props.onError;
  useEffect(() => {
    let active = true;
    void import("../../shared/MonacoTextEditor").then((m) => { if (active) setEditor(() => m.MonacoTextEditor); }, () => { if (active) { setFailed(true); onError.current?.(new Error("正文编辑器加载失败")); } });
    return () => { active = false; };
  }, []);
  return Editor ? <Editor {...props} /> : failed ? null : <p role="status">正在加载正文编辑器…</p>;
};

type Phase = "idle" | "loading" | "active" | "saving" | "begin-unknown" | "apply-unknown" | "stale" | "applied";
interface Draft { beginKey: string; review: CandidateRemergeReview; content: string; acknowledged: number[]; editor: "loading" | "ready" | "error"; epoch: number }
export const CandidateRemergeWorkbench = (props: { workspaceId: string; noteId: string; eligible: boolean; currentRevisionId: string }) => <CandidateRemergeSession key={`${props.workspaceId}:${props.noteId}`} {...props} />;
const CandidateRemergeSession = ({ workspaceId, noteId, eligible, currentRevisionId }: { workspaceId: string; noteId: string; eligible: boolean; currentRevisionId: string }) => {
  const [params, setParams] = useSearchParams();
  const keys = params.getAll("remerge_key");
  const beginKey = keys[0] ?? "";
  const invalidLink = keys.length > 1 || keys.length === 1 && !validRemergeKey(beginKey);
  const client = useQueryClient();
  const [phase, setPhase] = useState<Phase>("idle");
  const [draft, setDraft] = useState<Draft>();
  const [message, setMessage] = useState("");
  const [beginCommand, setBeginCommand] = useState<CandidateRemergeBegin>();
  const [applyCommand, setApplyCommand] = useState<CandidateRemergeApply>();
  const live = useRef(true), busy = useRef(false), controllers = useRef(new Set<AbortController>());
  const activeKey = useRef(beginKey), currentRevision = useRef(currentRevisionId);
  activeKey.current = beginKey; currentRevision.current = currentRevisionId;
  const commands = useRef<{ begin?: CandidateRemergeBegin; apply?: CandidateRemergeApply }>({});
  const phaseRef = useRef(phase); phaseRef.current = phase;
  useEffect(() => { live.current = true; return () => { live.current = false; for (const c of controllers.current) c.abort(); }; }, []);
  const invalidate = () => { void client.invalidateQueries({ queryKey: synthesisQueryKeys.note(workspaceId, noteId) }); void client.invalidateQueries({ queryKey: synthesisQueryKeys.revisions(workspaceId, noteId) }); void client.invalidateQueries({ queryKey: synthesisQueryKeys.notes(workspaceId) }); };
  const draftRef = useRef(draft); draftRef.current = draft;
  const install = (review: CandidateRemergeReview) => {
    const previous = draftRef.current;
    if (phaseRef.current === "stale" && review.state !== "APPLIED") { setMessage("此预览已过期，旧编辑保留供查看。请重新打开最新主笔记。"); return; }
    if (previous && (previous.beginKey !== activeKey.current || previous.review.attemptId !== review.attemptId || previous.review.previewFingerprint !== review.previewFingerprint)) { setPhase("stale"); setMessage("预览身份已变化。保留旧编辑供查看，不会覆盖新任务。"); return; }
    setDraft(previous ? { ...previous, review } : { beginKey: activeKey.current, review, content: review.review?.candidate ?? review.candidate ?? "", acknowledged: [], editor: "loading", epoch: 0 });
    if (review.state === "APPLIED") { setPhase("applied"); invalidate(); }
    else setPhase("active");
  };
  // URL 恢复是只读操作：不能凭空创建或重建 Begin 命令。
  useEffect(() => {
    if (invalidLink || beginKey === "" || commands.current.begin?.idempotency_key === beginKey) return;
    const controller = new AbortController(); let active = true;
    if (!busy.current) { setPhase("loading"); setMessage(""); }
    if (busy.current) return;
    void readCandidateRemerge(workspaceId, noteId, beginKey, controller.signal).then((r) => { if (active) install(r); }, (e: unknown) => {
      if (!active || isAbortError(e)) return;
      setPhase(commands.current.begin ? "begin-unknown" : "idle");
      setMessage("尚未确认此请求。请稍后按同一恢复链接重读；不会自动创建另一份预览。");
    });
    return () => { active = false; controller.abort(); };
  }, [workspaceId, noteId, beginKey, invalidLink]);
  useEffect(() => {
    if (!draft || draft.review.state === "APPLIED" || draft.content === (draft.review.review?.candidate ?? draft.review.candidate ?? "") && phase !== "apply-unknown") return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault(); window.addEventListener("beforeunload", warn); return () => window.removeEventListener("beforeunload", warn);
  }, [draft, phase]);
  const begin = async () => {
    if (busy.current || invalidLink || beginCommand !== undefined && beginCommand.idempotency_key !== beginKey || (!eligible && !beginCommand) || beginKey !== "" && !beginCommand) return;
    busy.current = true; setMessage(""); setPhase("loading");
    const controller = new AbortController(); controllers.current.add(controller);
    let command = beginCommand;
    let requestKey = beginKey;
    try {
      if (!command) {
        const target = await getCandidateRemergeTarget(workspaceId, noteId, controller.signal);
        if (!live.current || target.expected_revision_id !== currentRevision.current) { if (live.current) { setPhase("stale"); setMessage("候选已变化，请刷新主笔记后重新打开。"); } return; }
        command = { ...target, idempotency_key: crypto.randomUUID() }; requestKey = command.idempotency_key;
        commands.current.begin = command; setBeginCommand(command);
        activeKey.current = requestKey;
        setParams((old) => { const p = new URLSearchParams(old); p.set("remerge_key", requestKey); return p; }, { replace: true });
      }
      const review = await beginCandidateRemerge(command, controller.signal);
      if (!live.current || activeKey.current !== requestKey) return;
      if (currentRevision.current !== command.expected_revision_id && currentRevision.current !== review.result?.revisionId) { setPhase("stale"); setMessage("当前候选已变化，请保留原恢复链接并刷新主笔记。"); return; }
      install(review);
    } catch (e) {
      if (!live.current || isAbortError(e) || requestKey !== "" && activeKey.current !== requestKey) return;
      const unknown = command !== undefined && (!(e instanceof CandidateRemergeApiError) || e.status === null || e.status >= 500);
      setPhase(unknown ? "begin-unknown" : command ? "stale" : "idle");
      setMessage(unknown ? "建立预览的结果未知。原命令已冻结，请按同一 key 重读或精确重试。" : e instanceof Error ? e.message : "预览读取失败。");
    } finally { controllers.current.delete(controller); busy.current = false; }
  };
  const reread = async () => {
    if (busy.current || invalidLink || beginKey === "") return;
    busy.current = true; const controller = new AbortController(); controllers.current.add(controller);
    const requestKey = beginKey;
    try {
      const r = await readCandidateRemerge(workspaceId, noteId, requestKey, controller.signal);
      if (!live.current || activeKey.current !== requestKey) return;
      if (phaseRef.current === "apply-unknown" && r.state !== "APPLIED") { setMessage("原提交结果仍未确认，请使用已冻结的原命令精确重试。"); return; }
      setMessage(""); install(r);
    } catch (e) {
      if (live.current && activeKey.current === requestKey && !isAbortError(e)) {
        setMessage(e instanceof Error ? e.message : "状态读取失败，保留原命令与编辑。");
        if (draft && e instanceof CandidateRemergeApiError && [403, 404, 409].includes(e.status ?? 0) && phaseRef.current !== "apply-unknown") setPhase("stale");
      }
    } finally { controllers.current.delete(controller); busy.current = false; }
  };
  const save = async () => {
    if (draft?.beginKey !== beginKey || busy.current || invalidLink || beginKey === "" || !["active", "apply-unknown", "applied"].includes(phase)) return;
    const review = draft.review.review;
    const retry = phase === "apply-unknown" || phase === "applied";
    if (retry && !applyCommand || !retry && (draft.review.state === "CONFLICTS" && (draft.editor !== "ready" || draft.acknowledged.length !== review?.conflicts.length || !validManuscriptText(draft.content)))) return;
    const command: CandidateRemergeApply = applyCommand ?? { workspace_id: workspaceId, note_id: noteId, attempt_id: draft.review.attemptId, idempotency_key: crypto.randomUUID(), ...(review ? { resolution: { stage: "WORKSPACE_MANUAL_CONTENT", preview_fingerprint: draft.review.previewFingerprint, acknowledged_ordinals: [...draft.acknowledged].sort((a, b) => a - b), final_content: draft.content } } : {}) };
    const requestKey = beginKey; busy.current = true; setPhase("saving"); setMessage(""); setApplyCommand(command); commands.current.apply = command;
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const result = await applyCandidateRemerge(command, controller.signal);
      if (!live.current || activeKey.current !== requestKey) return;
      install(result);
    } catch (e) {
      if (!live.current || activeKey.current !== requestKey || isAbortError(e)) return;
      const unknown = !(e instanceof CandidateRemergeApiError) || e.status === null || e.status >= 500;
      const stale = e instanceof CandidateRemergeApiError && [403, 404, 409].includes(e.status ?? 0);
      setPhase(unknown ? "apply-unknown" : stale ? "stale" : "active");
      if (!unknown) { setApplyCommand(undefined); delete commands.current.apply; }
      setMessage(unknown ? "提交结果未知。完整编辑与原命令已冻结，请精确重试或按原 key 重读。" : stale ? "文件、候选或权限已变化，旧编辑仅供查看，不能继续应用。请刷新主笔记后建立新的预览。" : e instanceof Error ? e.message : "提交失败，编辑已保留。");
    } finally { controllers.current.delete(controller); busy.current = false; }
  };
  const resume = async () => {
    if (busy.current || invalidLink || draft?.beginKey !== beginKey || draft.review.state !== "APPLIED" || draft.review.result.proposalId) return;
    const requestKey = beginKey, attemptId = draft.review.attemptId;
    busy.current = true; setPhase("saving"); setMessage("");
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const result = await resumeCandidateRemerge(workspaceId, noteId, attemptId, controller.signal);
      if (!live.current || activeKey.current !== requestKey) return;
      install(result);
    } catch (e) {
      if (!live.current || activeKey.current !== requestKey || isAbortError(e)) return;
      setPhase("applied");
      setMessage("更新提案创建结果尚未确认。请按原请求重新读取，或继续创建更新提案；不会重新合并正文。");
    } finally { controllers.current.delete(controller); busy.current = false; }
  };
  const editorEvent = (epoch: number, status: "ready" | "error") => {
    if (!live.current) return;
    setDraft((old) => old?.epoch === epoch && old.beginKey === activeKey.current && old.review.attemptId === draft?.review.attemptId && (status === "error" || old.editor === "loading") ? { ...old, editor: status } : old);
  };
  if (!eligible && beginKey === "" && !draft) return null;
  const review = draft?.review.review, result = draft?.review.result;
  const locked = phase !== "active" || invalidLink || draft?.beginKey !== beginKey;
  return <section className="synthesis-list-section manuscript-review" aria-label="候选重新合并">
    <h2>基于当前文件重新合并</h2><p>将最新文件修改与已有候选的完整正文合并。保留原历史，新候选仍需审核发布。</p>
    {invalidLink ? <p role="alert">恢复链接无效或重复，请从主笔记详情重新打开。</p> : null}
    {beginKey === "" ? <Button disabled={phase === "loading" || !eligible || invalidLink} onClick={() => { void begin(); }}>基于当前文件重新合并</Button> : <Button variant="secondary" disabled={phase === "saving" || phase === "loading" || invalidLink} onClick={() => { void reread(); }}>按原请求重新读取</Button>}
    {phase === "begin-unknown" && beginCommand ? <Button onClick={() => { void begin(); }}>使用原命令重试建立预览</Button> : null}
    {phase === "loading" ? <p role="status">正在读取重新合并预览…</p> : null}
    {message ? <p role="alert">{message}</p> : null}
    {phase === "stale" ? <a href={`/authoring/notes/${noteId}`}>保留所需正文后，重新打开最新主笔记</a> : null}
    {draft?.review.state === "READY" ? <div><h3>待合并预览</h3><MarkdownPreview markdown={draft.review.candidate} /></div> : null}
    {review ? <div>
      <h3>三方内容比较</h3><div className="manuscript-conflict-text">{(["base", "current", "proposed"] as const).map((side) => <details key={side}><summary>{side === "base" ? "Base · 候选生成时的文件" : side === "current" ? "Current · 最新文件" : "Proposed · 原候选完整正文"}</summary><pre>{review[side]}</pre></details>)}</div>
      {review.conflicts.map((c) => <fieldset key={c.ordinal} disabled={locked}><legend>冲突 {c.ordinal}</legend><div className="manuscript-conflict-text">{(["base", "current", "proposed"] as const).map((side) => <details key={side}><summary>{side}</summary><pre>{c[side]}</pre></details>)}</div><label><input type="checkbox" checked={draft.acknowledged.includes(c.ordinal)} onChange={(e) => setDraft({ ...draft, acknowledged: e.target.checked ? [...draft.acknowledged, c.ordinal] : draft.acknowledged.filter((n) => n !== c.ordinal) })} />已在完整正文中处理冲突 {c.ordinal}</label></fieldset>)}
      <h3>完整合并结果</h3><RemergeEditor key={`${draft.review.attemptId}:${String(draft.epoch)}`} ariaLabel="重新合并完整正文" preserveWhitespace value={draft.content} language="markdown" height="400px" modelPath={`candidate-remerge://${workspaceId}/${noteId}/${draft.review.attemptId}/${String(draft.epoch)}`} disabled={locked} onChange={(content) => { setDraft((old) => old?.epoch === draft.epoch && old.beginKey === activeKey.current && old.review.attemptId === draft.review.attemptId && phaseRef.current === "active" ? { ...old, content } : old); }} onReady={() => editorEvent(draft.epoch, "ready")} onError={() => editorEvent(draft.epoch, "error")} />
      {draft.editor === "loading" ? <p role="status">编辑器就绪后才能提交新裁决。</p> : null}
      {draft.editor === "error" ? <div role="alert">编辑器加载失败，已禁止新提交。<Button onClick={() => setDraft({ ...draft, editor: "loading", epoch: draft.epoch + 1 })}>重新加载编辑器</Button></div> : null}
      <p>{new TextEncoder().encode(draft.content).length} / 1048576 UTF-8 字节 · 已确认 {draft.acknowledged.length} / {review.conflicts.length} 项</p>
      {!validManuscriptText(draft.content) ? <p role="alert">正文必须是有效 UTF-8、不含 NUL，且最多 1 MiB。</p> : null}
    </div> : null}
    {draft && !["APPLIED"].includes(draft.review.state) ? <Button disabled={phase === "apply-unknown" ? !applyCommand || draft.beginKey !== beginKey : locked || !!review && (draft.editor !== "ready" || draft.acknowledged.length !== review.conflicts.length || !validManuscriptText(draft.content))} onClick={() => { void save(); }}>{phase === "apply-unknown" ? "使用原命令精确重试" : phase === "saving" ? "正在保存…" : "确认合并并创建新候选"}</Button> : null}
    {result ? <div role="status"><p>重新合并结果已保存。</p><Link to={`/authoring/notes/${noteId}?revision_id=${result.revisionId}&remerge_key=${encodeURIComponent(beginKey)}`}>查看新候选</Link>{result.proposalId ? <Link to={`/proposals/${result.proposalId}`}>查看新候选的更新提案</Link> : <><p>更新提案仍待确认，可继续创建，无需重新裁决。</p><Button disabled={phase === "saving" || invalidLink || draft.beginKey !== beginKey} onClick={() => { void resume(); }}>继续创建更新提案</Button></>}</div> : null}
  </section>;
};
