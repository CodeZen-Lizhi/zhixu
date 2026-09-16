import { lazy, Suspense } from "react";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, ArrowRight, BookOpen, RefreshCw } from "lucide-react";
import { Link, useSearchParams } from "react-router-dom";
import { getSynthesisUpdateSummaries, type SynthesisNoteStatus } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, EmptyState, ErrorState, PageHeader } from "../../shared/ui";
import { canonicalUuidPattern } from "../../shared/codec";
import { ProcessingRecord } from "./ProcessingRecord";
import { synthesisQueryKeys, useSynthesisNotes, useSynthesisProcessing, useSynthesisProcessingList } from "./queries";
import { SynthesisGoalsPanel } from "./SynthesisGoalsPanel";
import "./synthesis.css";

const ManuscriptReviewWorkbench = lazy(() => import("./ManuscriptReviewWorkbench").then((module) => ({ default: module.ManuscriptReviewWorkbench })));

export const synthesisStatus: Record<SynthesisNoteStatus, { label: string; tone: "neutral" | "info" | "success" | "warning" | "danger" }> = {
  QUEUED: { label: "等待整理", tone: "neutral" }, GENERATING: { label: "正在整理", tone: "info" }, PENDING_APPROVAL: { label: "更新待审批", tone: "warning" },
  READY: { label: "已就绪", tone: "success" }, FAILED: { label: "整理失败", tone: "danger" }, CONFLICT: { label: "版本冲突", tone: "warning" },
  CAPABILITY_UNAVAILABLE: { label: "模型暂不可用", tone: "warning" }, RECOVERY_REQUIRED: { label: "需要人工恢复", tone: "danger" },
};

export const SynthesisNotesPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params] = useSearchParams();
  if (params.has("source_review_workspace") && (params.getAll("source_review_workspace").length !== 1 || params.get("source_review_workspace") !== workspaceId)) return <ErrorState title="补源核验链接不属于当前工作区" description="请切回对应工作区后重新打开。" />;
  return <SynthesisNotesContent key={workspaceId} />;
};
const SynthesisNotesContent = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params] = useSearchParams();
  const selectedProcessing = params.getAll("processing");
  const sourceReviewProcessing = params.getAll("source_review_processing");
  const sourceReviewProcessingId = sourceReviewProcessing.length === 1 && canonicalUuidPattern.test(sourceReviewProcessing[0] ?? "") && params.get("source_review_workspace") === workspaceId ? sourceReviewProcessing[0] : undefined;
  const processingId = selectedProcessing.length === 1 && canonicalUuidPattern.test(selectedProcessing[0] ?? "") ? selectedProcessing[0] : undefined;
  const queryClient = useQueryClient();
  const notes = useSynthesisNotes();
  const processing = useSynthesisProcessingList();
  const refresh = () => { void queryClient.resetQueries({ queryKey: synthesisQueryKeys.all(workspaceId) }); };
  const items = notes.data?.pages.flatMap((page) => page.items) ?? [];
  const records = processing.data?.pages.flatMap((page) => page.items) ?? [];
  const batches = Array.from({ length: Math.ceil(items.length / 50) }, (_, index) => items.slice(index * 50, (index + 1) * 50));
  const summaries = useQueries({ queries: batches.map((batch) => ({
    queryKey: [...synthesisQueryKeys.all(workspaceId), "update-summaries", batch.map((item) => [item.note.id, item.note.currentRevisionId, item.currentRevision?.id, item.publishedRevision?.id])],
    queryFn: ({ signal }: { signal: AbortSignal }) => getSynthesisUpdateSummaries(workspaceId, batch, signal),
    enabled: workspaceId !== "", retry: false,
  })) });
  return <div className="page-stack synthesis-page">
    <Link to="/authoring" className="synthesis-back"><ArrowLeft size={16} aria-hidden="true" />返回创作</Link>
    <PageHeader title="主笔记中心" description="阅读当前发布版本，审阅新资料带来的更新，并追溯每个知识片段的来源。" action={<Button variant="secondary" onClick={refresh}><RefreshCw size={16} aria-hidden="true" />刷新</Button>} />
    {workspaceId === "" ? <EmptyState title="先连接工作区" description="连接后可查看资料整理状态和合成笔记。" /> : <>
      {selectedProcessing.length > 0 && processingId === undefined ? <ErrorState description="处理记录定位无效。" /> : null}
      {sourceReviewProcessing.length > 0 && sourceReviewProcessingId === undefined ? <ErrorState description="来源核验处理记录定位无效。" /> : null}
      {sourceReviewProcessingId ? <SelectedSourceReviewProcessing processingId={sourceReviewProcessingId} /> : null}
      {processingId ? <Suspense fallback={<p role="status">正在加载裁决工作台…</p>}><ManuscriptReviewWorkbench workspaceId={workspaceId} processingId={processingId} /></Suspense> : null}
      <SynthesisGoalsPanel />
      <section className="synthesis-list-section" aria-labelledby="synthesis-notes-heading"><h2 id="synthesis-notes-heading">主笔记</h2>
        {notes.isPending ? <p role="status">正在读取合成笔记…</p> : null}
        {notes.isError ? <ErrorState description={notes.error.message} onRetry={refresh} /> : null}
        {notes.isSuccess && items.length === 0 ? <EmptyState title="还没有主笔记" description="新资料解析完成后，会自动整理知识点。首次整理失败的记录可在下方查看。" action={<Button asChild><Link to="/inbox">导入资料</Link></Button>} /> : null}
        <div className="synthesis-note-list">{items.map((item, index) => {
          const status = synthesisStatus[item.note.status];
          const summary = summaries[Math.floor(index / 50)];
          const reviews = summary?.data?.find((entry) => entry.noteId === item.note.id)?.items;
          return <article className="synthesis-note-row" key={item.note.id}>
            <Link className="synthesis-note-main" to={`/authoring/notes/${item.note.id}`}><BookOpen size={20} aria-hidden="true" /><span><strong>{item.note.title}</strong><small>最新整理：{item.itemCount} 项知识内容{item.conflictCount > 0 ? ` · ${String(item.conflictCount)} 项冲突` : ""}{item.openGapCount > 0 ? ` · ${String(item.openGapCount)} 项待补充` : ""}</small></span><ArrowRight size={17} aria-hidden="true" /></Link>
            <div className="synthesis-note-meta"><Badge tone={status.tone}>{status.label}</Badge><span>{item.publishedRevision === null ? "尚未发布" : `已发布版本 ${String(item.publishedRevision.revisionNo)}`}</span>
              {item.publication !== null ? <Link to={`/proposals/${item.publication.proposalId}`}>查看提案</Link> : null}</div>
            <div className="synthesis-note-meta">
              {summary?.isPending ? <span role="status">正在读取更新提醒…</span> : null}
              {summary?.isError ? <ErrorState description="更新提醒读取失败或版本已变化，请刷新列表重试。" onRetry={refresh} /> : null}
              {summary?.isSuccess && reviews?.every((review) => review.sourceReviewCount === 0 && review.bodyReviewCount === 0) ? <span>暂无待复核观察</span> : null}
              {summary?.isSuccess ? reviews?.filter((review) => review.sourceReviewCount > 0 || review.bodyReviewCount > 0).map((review) => {
                const published = review.revisionId === item.publishedRevision?.id;
                const version = published ? item.publishedRevision : item.currentRevision;
                return <Link key={review.revisionId} to={`/authoring/notes/${item.note.id}?revision_id=${review.revisionId}`}>
                  {published ? "已发布" : "候选"}版本 {String(version?.revisionNo)}：{review.sourceReviewCount > 0 ? `来源待复核 ${String(review.sourceReviewCount)} 条` : ""}{review.sourceReviewCount > 0 && review.bodyReviewCount > 0 ? " · " : ""}{review.bodyReviewCount > 0 ? `正文引用待复核 ${String(review.bodyReviewCount)} 条` : ""}
                </Link>;
              }) : null}
            </div>
          </article>;
        })}</div>
        {notes.hasNextPage ? <Button variant="secondary" disabled={notes.isFetchingNextPage} onClick={() => { void notes.fetchNextPage(); }}>{notes.isFetchingNextPage ? "正在读取…" : "加载更多笔记"}</Button> : null}
      </section>
      <section className="synthesis-list-section" aria-labelledby="synthesis-processing-heading"><div className="synthesis-section-heading"><h2 id="synthesis-processing-heading">资料整理进度</h2><p>每份新资料的自动处理记录，独立于已生成的笔记。</p></div>
        {processing.isPending ? <p role="status">正在读取整理进度…</p> : null}
        {processing.isError ? <ErrorState description={processing.error.message} onRetry={refresh} /> : null}
        {processing.isSuccess && records.length === 0 ? <p className="synthesis-help">还没有新资料的整理记录。</p> : null}
        <div className="synthesis-processing-list">{records.filter((record) => record.id !== sourceReviewProcessingId).map((record) => <ProcessingRecord processing={record} key={`${workspaceId}:${record.id}`} />)}</div>
        {processing.hasNextPage ? <Button variant="secondary" disabled={processing.isFetchingNextPage} onClick={() => { void processing.fetchNextPage(); }}>{processing.isFetchingNextPage ? "正在读取…" : "加载更多记录"}</Button> : null}
      </section>
    </>}
  </div>;
};

const SelectedSourceReviewProcessing = ({ processingId }: { processingId: string }) => {
  const query = useSynthesisProcessing(processingId);
  return <section aria-label="补源核验的原处理记录">
    <h2>原处理记录</h2>
    {query.isPending ? <p role="status">正在读取原处理记录…</p> : query.isError ? <ErrorState title="原处理记录读取失败" description={query.error.message} onRetry={() => { void query.refetch(); }} /> : <ProcessingRecord processing={query.data} />}
  </section>;
};
