import { useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, ArrowRight, BookOpen, RefreshCw } from "lucide-react";
import { Link } from "react-router-dom";
import type { SynthesisNoteStatus } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, EmptyState, ErrorState, PageHeader } from "../../shared/ui";
import { ProcessingRecord } from "./ProcessingRecord";
import { synthesisQueryKeys, useSynthesisNotes, useSynthesisProcessingList } from "./queries";
import "./synthesis.css";

export const synthesisStatus: Record<SynthesisNoteStatus, { label: string; tone: "neutral" | "info" | "success" | "warning" | "danger" }> = {
  QUEUED: { label: "等待整理", tone: "neutral" }, GENERATING: { label: "正在整理", tone: "info" }, PENDING_APPROVAL: { label: "更新待审批", tone: "warning" },
  READY: { label: "已就绪", tone: "success" }, FAILED: { label: "整理失败", tone: "danger" }, CONFLICT: { label: "版本冲突", tone: "warning" },
  CAPABILITY_UNAVAILABLE: { label: "模型暂不可用", tone: "warning" }, RECOVERY_REQUIRED: { label: "需要人工恢复", tone: "danger" },
};

export const SynthesisNotesPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const notes = useSynthesisNotes();
  const processing = useSynthesisProcessingList();
  const refresh = () => { void queryClient.resetQueries({ queryKey: synthesisQueryKeys.all(workspaceId) }); };
  const items = notes.data?.pages.flatMap((page) => page.items) ?? [];
  const records = processing.data?.pages.flatMap((page) => page.items) ?? [];
  return <div className="page-stack synthesis-page">
    <Link to="/authoring" className="synthesis-back"><ArrowLeft size={16} aria-hidden="true" />返回创作</Link>
    <PageHeader title="合成笔记" description="资料持续积累，笔记围绕知识点补充内容、保留冲突，并留下原始依据。" action={<Button variant="secondary" onClick={refresh}><RefreshCw size={16} aria-hidden="true" />刷新</Button>} />
    {workspaceId === "" ? <EmptyState title="先连接工作区" description="连接后可查看资料整理状态和合成笔记。" /> : <>
      <section className="synthesis-list-section" aria-labelledby="synthesis-notes-heading"><h2 id="synthesis-notes-heading">知识笔记</h2>
        {notes.isPending ? <p role="status">正在读取合成笔记…</p> : null}
        {notes.isError ? <ErrorState description={notes.error.message} onRetry={refresh} /> : null}
        {notes.isSuccess && items.length === 0 ? <EmptyState title="还没有合成笔记" description="新资料解析完成后，会自动整理知识点。首次整理失败的记录可在下方查看。" action={<Button asChild><Link to="/inbox">导入资料</Link></Button>} /> : null}
        <div className="synthesis-note-list">{items.map((item) => {
          const status = synthesisStatus[item.note.status];
          return <article className="synthesis-note-row" key={item.note.id}>
            <Link className="synthesis-note-main" to={`/authoring/notes/${item.note.id}`}><BookOpen size={20} aria-hidden="true" /><span><strong>{item.note.title}</strong><small>{item.itemCount} 项知识内容{item.conflictCount > 0 ? ` · ${String(item.conflictCount)} 项冲突` : ""}{item.openGapCount > 0 ? ` · ${String(item.openGapCount)} 项待补充` : ""}</small></span><ArrowRight size={17} aria-hidden="true" /></Link>
            <div className="synthesis-note-meta"><Badge tone={status.tone}>{status.label}</Badge><span>{item.publishedRevision === null ? "尚未发布" : `已发布版本 ${String(item.publishedRevision.revisionNo)}`}</span>
              {item.publication !== null ? <Link to={`/proposals/${item.publication.proposalId}`}>查看提案</Link> : null}</div>
          </article>;
        })}</div>
        {notes.hasNextPage ? <Button variant="secondary" disabled={notes.isFetchingNextPage} onClick={() => { void notes.fetchNextPage(); }}>{notes.isFetchingNextPage ? "正在读取…" : "加载更多笔记"}</Button> : null}
      </section>
      <section className="synthesis-list-section" aria-labelledby="synthesis-processing-heading"><div className="synthesis-section-heading"><h2 id="synthesis-processing-heading">资料整理进度</h2><p>每份新资料的自动处理记录，独立于已生成的笔记。</p></div>
        {processing.isPending ? <p role="status">正在读取整理进度…</p> : null}
        {processing.isError ? <ErrorState description={processing.error.message} onRetry={refresh} /> : null}
        {processing.isSuccess && records.length === 0 ? <p className="synthesis-help">还没有新资料的整理记录。</p> : null}
        <div className="synthesis-processing-list">{records.map((record) => <ProcessingRecord processing={record} key={`${workspaceId}:${record.id}`} />)}</div>
        {processing.hasNextPage ? <Button variant="secondary" disabled={processing.isFetchingNextPage} onClick={() => { void processing.fetchNextPage(); }}>{processing.isFetchingNextPage ? "正在读取…" : "加载更多记录"}</Button> : null}
      </section>
    </>}
  </div>;
};
