import { useEffect, useRef, useState, type ComponentType } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { applyHistoricalRepublish, beginHistoricalRepublish, getHistoricalRepublishTarget, decodeHistoricalRepublishTarget, readHistoricalRepublish, resumeHistoricalRepublish, HistoricalRepublishApiError, validRemergeKey, type HistoricalRepublishApply, type HistoricalRepublishBegin, type HistoricalRepublishReview, type HistoricalRepublishTarget } from "../../api/synthesis-historical-republish";
import { strictJson } from "../../api/exports";
import { canonicalUuidPattern, isAbortError } from "../../shared/codec";
import type { MonacoDiffViewerProps } from "../../shared/MonacoDiffViewer";
import { Button } from "../../shared/ui";
import { synthesisQueryKeys } from "./queries";
import "./manuscript-review.css";
import "./historical-republish.css";

export const historicalRestoreKeys = ["restore_begin", "restore_key", "restore_workspace", "restore_selected", "restore_apply_key", "restore_attempt", "restore_fingerprint", "restore_confirm", "restore_retire"];
const ExactDiff = ({ original, candidate, identity, label }: { original: string; candidate: string; identity: string; label: string }) => {
 const [Viewer, setViewer] = useState<ComponentType<MonacoDiffViewerProps>>();
 const [failed, setFailed] = useState(false);
 useEffect(() => { let active = true; void import("../../shared/MonacoDiffViewer").then((m) => { if (active) setViewer(() => m.MonacoDiffViewer); }, () => { if (active) setFailed(true); }); return () => { active = false; }; }, []);
 return <section aria-label={label}><h3>{label}</h3>{Viewer && !failed ? <Viewer identityKey={identity} original={original} modified={candidate} originalModelPath={`historical-restore://${identity}/original`} modifiedModelPath={`historical-restore://${identity}/selected`} language="markdown" onReady={() => { /* 只读；下方仍提供完整字节对照。 */ }} onError={() => setFailed(true)} /> : <p role="status">{failed ? "差异视图加载失败，请使用下方完整原文对照。" : "正在加载只读差异…"}</p>}<div className="manuscript-conflict-text"><details><summary>恢复前完整原文</summary><pre>{original}</pre></details><details><summary>所选历史版本完整原文</summary><pre>{candidate}</pre></details></div></section>;
};
interface Props { workspaceId: string; noteId: string; selectedId: string }
export const HistoricalRepublishWorkbench = (props: Props) => <HistoricalRepublishSession key={`${props.workspaceId}:${props.noteId}:${props.selectedId}`} {...props} />;
const HistoricalRepublishSession = ({ workspaceId, noteId, selectedId }: Props) => {
 const [params, setParams] = useSearchParams();
 const beginKey = params.get("restore_key") ?? "";
 let savedBegin: HistoricalRepublishBegin | undefined;
 let invalidSavedBegin = false;
 if (params.has("restore_begin")) {
  try { const raw = params.get("restore_begin") ?? ""; if (raw.length > 16384 || !validRemergeKey(beginKey)) throw new Error("invalid saved Begin"); savedBegin = { ...decodeHistoricalRepublishTarget(strictJson(raw), workspaceId, noteId, selectedId), idempotency_key: beginKey }; } catch { invalidSavedBegin = true; }
 }
 const invalidLink = invalidSavedBegin || historicalRestoreKeys.some((k) => params.getAll(k).length > 1) || beginKey !== "" && (!validRemergeKey(beginKey) || params.get("restore_workspace") !== workspaceId || params.get("restore_selected") !== selectedId);
 const client = useQueryClient();
 const [target, setTarget] = useState<HistoricalRepublishTarget>();
 const [review, setReview] = useState<HistoricalRepublishReview>();
 const [message, setMessage] = useState("");
 const [pending, setPending] = useState(false);
 const [confirmed, setConfirmed] = useState(false), [retired, setRetired] = useState(false);
 const [beginCommand, setBeginCommand] = useState<HistoricalRepublishBegin | undefined>(savedBegin);
 const [applyCommand, setApplyCommand] = useState<HistoricalRepublishApply>();
 const [stale, setStale] = useState(false);
 const live = useRef(true), busy = useRef(false), activeKey = useRef(beginKey), controllers = useRef(new Set<AbortController>());
 activeKey.current = beginKey;
 const invalidate = () => { void client.invalidateQueries({ queryKey: synthesisQueryKeys.note(workspaceId, noteId) }); void client.invalidateQueries({ queryKey: synthesisQueryKeys.revisions(workspaceId, noteId) }); void client.invalidateQueries({ queryKey: synthesisQueryKeys.notes(workspaceId) }); };
 useEffect(() => { live.current = true; return () => { live.current = false; for (const c of controllers.current) c.abort(); }; }, []);
 const install = (r: HistoricalRepublishReview) => { setReview((old) => { if (old && (old.attemptId !== r.attemptId || old.previewFingerprint !== r.previewFingerprint)) { setStale(true); setMessage("预览身份已变化，请保留原恢复链接重新读取。"); return old; } return r; }); if (r.state === "APPLIED") invalidate(); };
 // 重新加载仅读取原尝试记录，绝不在 effect 中执行 POST。
 useEffect(() => {
  if (invalidLink || busy.current) return;
  const c = new AbortController(); let active = true;
  setPending(true);
  const work = beginKey ? readHistoricalRepublish(workspaceId, noteId, selectedId, beginKey, c.signal).then((r) => { if (active) install(r); }) : getHistoricalRepublishTarget(workspaceId, noteId, selectedId, c.signal).then((t) => { if (active) setTarget(t); });
  void work.catch((e: unknown) => { if (active && !isAbortError(e)) setMessage(e instanceof Error ? e.message : "无法读取此版本的恢复许可。"); }).finally(() => { if (active) setPending(false); });
  return () => { active = false; c.abort(); };
 }, [workspaceId, noteId, selectedId, beginKey, invalidLink]);
 const frozenApply = (): HistoricalRepublishApply | undefined => {
  if (applyCommand) return applyCommand;
  if (!params.has("restore_apply_key")) return undefined;
  const key = params.get("restore_apply_key") ?? "", attempt = params.get("restore_attempt") ?? "", fingerprint = params.get("restore_fingerprint") ?? "";
  if (!validRemergeKey(key) || !canonicalUuidPattern.test(attempt) || !/^[0-9a-f]{64}$/.test(fingerprint) || params.get("restore_confirm") !== "true" || !["true", "false"].includes(params.get("restore_retire") ?? "")) return undefined;
  return { workspace_id: workspaceId, note_id: noteId, attempt_id: attempt, idempotency_key: key, preview_fingerprint: fingerprint, confirm_exact_restore: true, retire_current_candidate: params.get("restore_retire") === "true" };
 };
 const frozen = frozenApply();
 const invalidApply = params.has("restore_apply_key") && !frozen || frozen !== undefined && review !== undefined && (frozen.attempt_id !== review.attemptId || frozen.preview_fingerprint !== review.previewFingerprint);
 const run = async (operation: (signal: AbortSignal) => Promise<HistoricalRepublishReview>, requestKey: string, command: boolean) => {
  if (busy.current || invalidLink) return;
  busy.current = true; setPending(true); setMessage(""); const c = new AbortController(); controllers.current.add(c);
  try { const r = await operation(c.signal); if (live.current && activeKey.current === requestKey) install(r); }
  catch (e) { if (!live.current || activeKey.current !== requestKey || isAbortError(e)) return; setMessage(e instanceof Error ? e.message : "结果尚未确认，请按原请求重读或重试。"); if (command && e instanceof HistoricalRepublishApiError && [403, 404, 409].includes(e.status ?? 0)) setStale(true); }
  finally { controllers.current.delete(c); busy.current = false; if (live.current && activeKey.current === requestKey) setPending(false); }
 };
 const begin = () => {
  if (busy.current || invalidLink || beginKey && (beginCommand?.idempotency_key !== beginKey) || !target && !beginCommand || stale) return;
  if (!beginCommand && !target) return;
  const c = beginCommand ?? (target ? { ...target, idempotency_key: crypto.randomUUID() } : undefined);
  if (!c) return;
  setBeginCommand(c); activeKey.current = c.idempotency_key;
  setParams((old) => { const p = new URLSearchParams(old); const { idempotency_key: originalKey, ...originalTarget } = c; p.set("restore_begin", JSON.stringify(originalTarget)); p.set("restore_key", originalKey); p.set("restore_workspace", workspaceId); p.set("restore_selected", selectedId); return p; }, { replace: true });
  void run((signal) => beginHistoricalRepublish(c, signal), c.idempotency_key, true);
 };
 const apply = () => {
  if (review?.state !== "READY" || invalidApply || stale || !frozen && (!confirmed || review.target.requires_retirement && !retired)) return;
  const c: HistoricalRepublishApply = frozen ?? { workspace_id: workspaceId, note_id: noteId, attempt_id: review.attemptId, idempotency_key: crypto.randomUUID(), preview_fingerprint: review.previewFingerprint, confirm_exact_restore: true, retire_current_candidate: review.target.requires_retirement && retired };
  setApplyCommand(c);
  setParams((old) => { const p = new URLSearchParams(old); p.set("restore_apply_key", c.idempotency_key); p.set("restore_attempt", c.attempt_id); p.set("restore_fingerprint", c.preview_fingerprint); p.set("restore_confirm", "true"); p.set("restore_retire", String(c.retire_current_candidate)); return p; }, { replace: true });
  void run((signal) => applyHistoricalRepublish(c, selectedId, signal), beginKey, true);
 };
 const restart = () => { setParams((old) => { const p = new URLSearchParams(old); for (const k of historicalRestoreKeys) p.delete(k); return p; }); setReview(undefined); setTarget(undefined); setBeginCommand(undefined); setApplyCommand(undefined); setStale(false); setConfirmed(false); setRetired(false); };
 return <section className="synthesis-list-section manuscript-review historical-republish" aria-label="历史版本重新审阅"><h2>重新审阅此版本</h2><p>完整恢复所选版本的正文与旧来源快照，不调用 AI、不改变锚点范围。生成新候选后仍需审核发布。</p>
  {invalidLink || invalidApply ? <p role="alert">恢复链接的身份或原命令不完整，已禁止提交。请保留原链接并重新打开正确的工作区与版本。</p> : null}
  {message ? <p role="alert">{message}</p> : null}{pending ? <p role="status">正在核对历史恢复状态…</p> : null}
  {!beginKey ? <Button disabled={!target || pending || invalidLink} onClick={begin}>恢复此版本／建立审阅预览</Button> : <Button variant="secondary" disabled={pending || invalidLink} onClick={() => { void run((signal) => readHistoricalRepublish(workspaceId, noteId, selectedId, beginKey, signal), beginKey, false); }}>按原请求重新读取</Button>}
  {beginCommand && !review && !stale ? <Button disabled={pending || invalidLink} onClick={begin}>使用原命令重试建立预览</Button> : null}
  {review ? <><p><Link to={`/authoring/notes/${noteId}?revision_id=${selectedId}`}>查看所选历史版本及其原始来源</Link></p><p>范围和来源现状提示不代表对当前语义重新核验；旧来源丢失不等于旧结论已被证伪。</p>
   {review.currentScope ? <div><h3>本次预览冻结的锚点范围</h3><p>主题：{review.currentScope.topics.join("、")}</p><p>用途／受众：{review.currentScope.audiences.join("、")}</p><p>{review.currentScope.description}</p></div> : <p>本次预览未绑定锚点范围。</p>}
   {review.warnings.length ? <ul>{review.warnings.map((w, i) => <li key={`${w.code}:${String(i)}`}>{warningLabel(w.code)}{w.sourceId ? ` · 来源 ${w.sourceId}` : ""}</li>)}</ul> : null}
   <ExactDiff identity={`${workspaceId}/${noteId}/${review.attemptId}/published`} label="当前发布正文 → 所选历史正文" original={review.publishedContent} candidate={review.candidate} />
   <ExactDiff identity={`${workspaceId}/${noteId}/${review.attemptId}/file`} label="当前文件完整正文 → 所选历史正文" original={review.currentContent} candidate={review.candidate} />
   {review.target.requires_retirement ? <p>将替代尚未批准的候选：<Link to={`/authoring/notes/${noteId}?revision_id=${review.target.expected_revision_id}`}>查看该候选</Link>{review.target.expected_proposal_id ? <Link to={`/proposals/${review.target.expected_proposal_id}`}>查看待替代提案</Link> : null}</p> : null}
   {review.state === "READY" ? <><fieldset disabled={pending || !!frozen || stale || invalidLink || invalidApply}><legend>确认整篇恢复</legend><label><input type="checkbox" checked={frozen?.confirm_exact_restore ?? confirmed} onChange={(e) => setConfirmed(e.target.checked)} />我已审阅两份完整差异，确认恢复整篇历史正文，包括替换当前文件中的人工修改。</label>{review.target.requires_retirement ? <label><input type="checkbox" checked={frozen?.retire_current_candidate ?? retired} onChange={(e) => setRetired(e.target.checked)} />明确替代上述尚未批准的候选及提案。</label> : null}</fieldset><Button disabled={pending || stale || invalidLink || invalidApply || !frozen && (!confirmed || review.target.requires_retirement && !retired)} onClick={apply}>{frozen ? "使用原命令精确重试" : "确认整篇恢复并创建新候选"}</Button></> : <div role="status"><p>历史恢复候选已保存，尚需按提案状态审核发布。</p><Link to={`/authoring/notes/${noteId}?revision_id=${review.result?.revisionId ?? ""}`}>查看恢复候选</Link>{review.result?.proposalId ? <Link to={`/proposals/${review.result.proposalId}`}>审阅恢复提案</Link> : <><p>提案创建尚未完成，继续同一次恢复不会创建另一版本。</p><Button disabled={pending || invalidLink} onClick={() => { void run((signal) => resumeHistoricalRepublish(workspaceId, noteId, selectedId, review.attemptId, signal), beginKey, true); }}>继续创建原恢复提案</Button></>}<Link to={`/authoring/notes/${noteId}`} onClick={invalidate}>返回主笔记当前发布内容</Link></div>}
  </> : null}
  {stale && review?.state !== "APPLIED" ? <Button variant="secondary" onClick={restart}>基于当前文件重新预览此历史版本</Button> : null}
 </section>;
};
const warningLabel = (code: string) => ({ HISTORICAL_CONTENT_NOT_REVALIDATED_AGAINST_CURRENT_SCOPE: "当前锚点范围未重新语义核验", SOURCE_REMOVED: "原始来源当前已删除，保留旧来源快照", SOURCE_QUARANTINED: "原始来源当前已隔离", SOURCE_UNAVAILABLE: "原始来源当前不可用", SOURCE_CHANGED: "原始来源已有新版本" }[code] ?? `来源／范围复核提示：${code}`);
