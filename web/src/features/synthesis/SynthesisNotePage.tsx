import { SourceReviewEvidence } from "./SourceReviewEvidence";
import { ArrowLeft, FileCheck2, History, RefreshCw } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { synthesisSourceIdentity } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { canonicalUuidPattern } from "../../shared/codec";
import { Badge, Button, EmptyState, ErrorState, PageHeader } from "../../shared/ui";
import { AnchorReviewPanel } from "./AnchorReviewPanel";
import { HistoricalRepublishWorkbench, historicalRestoreKeys } from "./HistoricalRepublishWorkbench";
import { CandidateRemergeWorkbench } from "./CandidateRemergeWorkbench";
import { NoteContent } from "./NoteContent";
import { NoteInterviewPanel } from "./NoteInterviewPanel";
import { SourceGraph } from "./SourceGraph";
import { BodyImpactPanel } from "./BodyImpactPanel";
import { SourceImpactPanel } from "./SourceImpactPanel";
import { SupplementalSources } from "./SupplementalSources";
import { ProcessingRecord } from "./ProcessingRecord";
import { synthesisStatus } from "./SynthesisNotesPage";
import { synthesisQueryKeys, useSynthesisNote, useSynthesisRevision, useSynthesisRevisions } from "./queries";
import { resolveSynthesisSourceLink } from "./source-link";
import "./synthesis.css";

export const SynthesisNotePage = () => {
  const { noteId = "" } = useParams();
  const workspaceId = useActiveWorkspaceId();
  const [params] = useSearchParams();
  if (params.has("restore_workspace") && (params.getAll("restore_workspace").length !== 1 || params.get("restore_workspace") !== workspaceId)) return <ErrorState title="历史恢复链接不属于当前工作区" description="请切回原工作区后重新打开。" />;
  if (params.has("source_review_workspace") && (params.getAll("source_review_workspace").length !== 1 || params.get("source_review_workspace") !== workspaceId)) return <ErrorState title="补源核验链接不属于当前工作区" description="请切回对应工作区后重新打开。" />;
  if (!canonicalUuidPattern.test(noteId)) return <ErrorState title="笔记地址无效" description="请从主笔记列表重新打开。" />;
  return <SynthesisNoteDetail key={`${workspaceId}:${noteId}`} noteId={noteId} />;
};

const SynthesisNoteDetail = ({ noteId }: { noteId: string }) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const detail = useSynthesisNote(noteId);
  const history = useSynthesisRevisions(noteId);
  const resetHistory = () => { void queryClient.resetQueries({ queryKey: synthesisQueryKeys.revisions(workspaceId, noteId), exact: true }); };
  const [params, setParams] = useSearchParams();
  const selectedRevisionId = params.get("revision_id") ?? "";
  const selected = useSynthesisRevision(noteId, selectedRevisionId);
  const value = detail.data;
  const displayed = selectedRevisionId !== "" ? selected.data : value?.publishedRevision;
  const sourceLink = displayed ? resolveSynthesisSourceLink(params, displayed) : null;
  const linkedSource = sourceLink?.kind === "resolved" ? sourceLink.reference : null;
  const bodyKeys = ["body_item_id", "body_workspace_id", "body_projection_hash"];
  const hasBodyLink = bodyKeys.some((key) => params.has(key));
  const bodyItemId = params.get("body_item_id") ?? "";
  const bodyLinkValid = !hasBodyLink || (bodyKeys.every((key) => params.getAll(key).length === 1) && params.getAll("revision_id").length === 1 &&
    canonicalUuidPattern.test(selectedRevisionId) && displayed?.id === selectedRevisionId && canonicalUuidPattern.test(bodyItemId) && params.get("body_workspace_id") === workspaceId && displayed.projectionHash === params.get("body_projection_hash") && displayed.items.some((item) => item.id === bodyItemId));
  const chooseRevision = (revisionId: string | null) => { void detail.refetch(); resetHistory(); setParams((current) => { const next = new URLSearchParams(current); for (const key of [...bodyKeys, ...historicalRestoreKeys, "workspace_id", "source_id", "source_version_id", "content_artifact_id", "parse_projection_id", "source_span_id", "content_hash", "excerpt_hash"]) next.delete(key); if (revisionId === null) next.delete("revision_id"); else next.set("revision_id", revisionId); return next; }); };
  const displayedIsPublished = displayed !== undefined && displayed !== null && displayed.id === value?.publishedRevision?.id;
  const displayedIsCurrent = displayed !== undefined && displayed !== null && displayed.id === value?.currentRevision?.id;
  return <div className="page-stack synthesis-page">
    <Link to="/authoring/notes" className="synthesis-back"><ArrowLeft size={16} aria-hidden="true" />主笔记</Link>
    <PageHeader title={value?.note.title ?? "主笔记"} action={<Button variant="secondary" onClick={() => { void detail.refetch(); void queryClient.invalidateQueries({ queryKey: ["synthesis", workspaceId, "source-impacts", noteId] }); void queryClient.invalidateQueries({ queryKey: ["synthesis", workspaceId, "body-impacts", noteId] }); resetHistory(); if (selectedRevisionId !== "") void selected.refetch(); }}><RefreshCw size={16} aria-hidden="true" />刷新</Button>} />
    {detail.isPending ? <p role="status">正在读取笔记与版本…</p> : null}
    {detail.isError ? <ErrorState description={detail.error.message} onRetry={() => { void detail.refetch(); }} /> : null}
    {value ? <>
      <div className="synthesis-detail-status"><Badge tone={synthesisStatus[value.note.status].tone}>{synthesisStatus[value.note.status].label}</Badge>
        <span>{value.publishedRevision === null ? "尚未发布" : `正式版本 ${String(value.publishedRevision.revisionNo)}`}</span>
        {value.currentRevision && value.currentRevision.id !== value.publishedRevision?.id ? <Button variant="secondary" onClick={() => { chooseRevision(value.currentRevision?.id ?? null); }}>审阅候选版本 {value.currentRevision.revisionNo}</Button> : null}
        {value.publication !== null ? <Button asChild><Link to={`/proposals/${value.publication.proposalId}`}><FileCheck2 size={16} aria-hidden="true" />查看更新提案</Link></Button> : null}
      </div>
      {!value.currentRevision?.historicalRepublish ? <CandidateRemergeWorkbench workspaceId={workspaceId} noteId={noteId} currentRevisionId={value.currentRevision?.id ?? ""} eligible={value.currentRevision?.display !== undefined && value.currentRevision.id !== value.publishedRevision?.id} /> : null}
      {displayed && selectedRevisionId ? <HistoricalRepublishWorkbench workspaceId={workspaceId} noteId={noteId} selectedId={displayed.id} /> : null}
      {value.note.failure ? <div className="synthesis-failure" role="alert"><p>{value.note.status === "CONFLICT" ? "笔记或发布基线已变化。请检查最新内容与提案，避免覆盖现有修改。" : value.note.status === "RECOVERY_REQUIRED" ? "上次执行结果需要核实，请查看处理记录后恢复。" : "笔记更新尚未完成，已有内容和来源仍然保留。"}</p><span>错误编号：{value.note.failure.code}</span></div> : null}
      {displayed ? <BodyImpactPanel key={`body-${displayed.id}`} revision={displayed} /> : null}
      {displayed ? <SourceImpactPanel key={displayed.id} revision={displayed} /> : null}
      {displayed?.display ? <SourceReviewEvidence scope={{workspaceId: displayed.workspaceId, noteId: displayed.noteId, revisionId: displayed.id}} /> : null}
      <div className="synthesis-detail-layout"><section className="synthesis-reading" aria-label="笔记内容">
        <div className="synthesis-reading-heading"><h2>{displayed ? `版本 ${String(displayed.revisionNo)}` : "笔记内容"}</h2>
          {displayed ? <Badge tone={displayedIsPublished ? "success" : displayedIsCurrent ? "warning" : "neutral"}>{displayedIsPublished ? "已发布" : displayedIsCurrent ? "当前候选" : "历史版本"}</Badge> : null}
          {selectedRevisionId !== "" ? <Button variant="ghost" size="sm" onClick={() => { chooseRevision(null); }}>返回已发布内容</Button> : null}</div>
        {selectedRevisionId !== "" && selected.isPending ? <p role="status">正在读取所选历史版本…</p> : null}
        {selectedRevisionId !== "" && selected.isError ? <ErrorState description={selected.error.message} onRetry={() => { void selected.refetch(); }} /> : null}
        {sourceLink?.kind === "invalid" ? <ErrorState title="无法定位原始来源" description="来源链接的信息不完整、格式有误、存在重复参数，或与当前工作区不符。请从笔记中的来源按钮重新打开。" /> : null}
        {sourceLink?.kind === "missing" ? <ErrorState title="无法定位原始来源" description="所选笔记版本未保存这个精确引用。请选择对应的历史版本，或从笔记中的来源按钮重新打开。" /> : null}
        {displayed && !bodyLinkValid ? <ErrorState title="无法定位引用的主笔记片段" description="链接与所选版本、工作区或片段不匹配。请从原笔记的引用入口重新打开。" /> : null}
        {displayed?.historicalRepublish ? <p>恢复旧内容，未调用 AI；沿用所选版本当时的来源证明。<Link to={`/authoring/notes/${noteId}?revision_id=${displayed.historicalRepublish.selectedRevisionId}`}>查看所选历史版本与来源</Link></p> : null}
        {displayed?.remerge ? <p>基于最新文件重新合并，保留原候选与来源历史；本版本未再次生成 AI 知识。</p> : null}
        {displayed ? <NoteContent key={`${displayed.workspaceId}:${displayed.id}:${linkedSource === null ? sourceLink?.kind ?? "none" : synthesisSourceIdentity(linkedSource)}`} revision={displayed} initialSource={linkedSource} initialItemId={hasBodyLink && bodyLinkValid ? bodyItemId : null} /> : selectedRevisionId === "" ? <EmptyState title="这份主笔记尚未发布" description="生成内容需经审核发布后才成为正式笔记。已有候选可从上方进入审阅。" /> : null}
      </section><aside className="synthesis-history" aria-labelledby="synthesis-history-heading"><h2 id="synthesis-history-heading"><History size={17} aria-hidden="true" />版本记录</h2>
        {history.isPending ? <p role="status">正在读取历史…</p> : null}
        {history.isError ? <ErrorState description={history.error.message} onRetry={resetHistory} /> : null}
        <ol>{history.data?.pages.flatMap((page) => page.items).map((revision) => <li key={revision.id}><button type="button" className="synthesis-history-button" aria-current={displayed?.id === revision.id ? "true" : undefined} onClick={() => { chooseRevision(revision.id); }}><strong>版本 {revision.revisionNo}</strong>{revision.remergeSourceRevisionId ? <span>基于最新文件重新合并</span> : null}<time dateTime={revision.createdAt}>{new Date(revision.createdAt).toLocaleDateString("zh-CN")}</time>{value.publishedRevision?.id === revision.id ? <span>当前正式版本</span> : null}</button></li>)}</ol>
        {history.hasNextPage ? <Button variant="ghost" size="sm" disabled={history.isFetchingNextPage} onClick={() => { void history.fetchNextPage(); }}>更早版本</Button> : null}
        <Link to={`/authoring/documents/${value.note.documentId}/history`}>查看文件历史</Link>
      </aside></div>
      {value.latestProcessing ? <section aria-label="最近更新任务"><h2>最近整理记录</h2><ProcessingRecord key={`${value.workspaceId}:${value.latestProcessing.id}`} processing={value.latestProcessing} /></section> : null}
      {displayed ? <SourceGraph key={`${displayed.workspaceId}:${displayed.id}`} revision={displayed} /> : null}
      <AnchorReviewPanel key={`${workspaceId}:${noteId}:anchor`} noteId={noteId} note={value.note} />
      <SupplementalSources key={`${workspaceId}:${noteId}:supplements`} noteId={noteId} />
      <NoteInterviewPanel key={`${value.workspaceId}:${noteId}`} noteId={noteId} publishedRevision={value.publishedRevision} />
    </> : null}
  </div>;
};
