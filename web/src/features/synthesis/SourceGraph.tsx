import { useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { getSynthesisSourceGraph, synthesisSourceIdentity, type SynthesisRevision } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Button, ErrorState } from "../../shared/ui";

import { synthesisSourceHref } from "./source-link";

export const SourceGraph = ({ revision }: { revision: SynthesisRevision }) => {
  const workspaceId = useActiveWorkspaceId();
  const [expanded, setExpanded] = useState(false);
  const [selectedSourceId, setSelectedSourceId] = useState<string | null>(null);
  const query = useInfiniteQuery({ queryKey: ["synthesis", revision.workspaceId, "source-graph", revision.noteId, revision.id],
    queryFn: ({ signal, pageParam }) => getSynthesisSourceGraph(revision, pageParam, signal), initialPageParam: null as string | null,
    getNextPageParam: (page) => page.nextAfterNoteId, enabled: expanded && workspaceId === revision.workspaceId, retry: false });
  const references = query.data?.pages[0]?.sources ?? [];
  const files = [...new Map(references.map((ref) => [ref.source.sourceId, ref.title])).entries()];
  const sourceId = selectedSourceId ?? files[0]?.[0];
  const peers = [...new Map(query.data?.pages.flatMap((page) => page.sharedNotes).map((note) => [note.noteId, note])).values()];
  const shared = peers.filter((note) => note.sources.some((ref) => ref.source.sourceId === sourceId));
  return <section className="synthesis-source-graph" aria-label="来源图谱">
    <Button variant="secondary" aria-expanded={expanded} onClick={() => { setExpanded(!expanded); }}>查看来源图谱</Button>
    {expanded ? <>
      <p className="synthesis-help">查看版本 {revision.revisionNo} 的正文来源，以及共享这些来源文件的已发布主笔记。</p>
      {query.isPending ? <p role="status">正在读取来源关系…</p> : null}
      {query.isError ? <ErrorState description={query.error.message} onRetry={() => { void query.refetch(); }} /> : null}
      {query.data ? <>
        {files.length === 0 ? <p>这个版本没有正文来源引用。</p> : <div className="synthesis-source-flow">
          <section className="synthesis-graph-node"><h3>{revision.title}</h3><p>所读版本 {revision.revisionNo}</p><span className="synthesis-help">引用 →</span></section>
          <section className="synthesis-graph-node" aria-label="来源文件"><h3>来源文件</h3>
            <div className="synthesis-source-choices">{files.map(([id, title]) => <Button key={id} variant={id === sourceId ? "secondary" : "ghost"} aria-pressed={id === sourceId} onClick={() => { setSelectedSourceId(id); }}>{title}</Button>)}</div>
            <ul>{references.filter((ref) => ref.source.sourceId === sourceId).map((ref, index) => <li key={synthesisSourceIdentity(ref)}><Link to={synthesisSourceHref(revision.noteId, revision.id, ref)}>查看原文片段 {index + 1}</Link></li>)}</ul>
          </section>
          <section className="synthesis-graph-node" aria-label="共享来源的主笔记"><h3>← 共享来源</h3>
            {shared.length === 0 ? <p>{query.hasNextPage ? "已加载的主笔记中暂无共享此来源的记录。" : "暂无其他已发布主笔记共享此来源。"}</p> : <ul>{shared.map((note) => <li key={note.noteId}>
              <Link to={`/authoring/notes/${note.noteId}?revision_id=${note.revisionId}`}>{note.title} · 已发布版本 {note.revisionNo}</Link>
              <ul>{note.sources.filter((ref) => ref.source.sourceId === sourceId).map((ref, index) => <li key={synthesisSourceIdentity(ref)}><Link to={synthesisSourceHref(note.noteId, note.revisionId, ref)}>查看它引用的片段 {index + 1}</Link></li>)}</ul>
            </li>)}</ul>}
          </section>
        </div>}
        <p className="synthesis-help">共享文件可被用于不同主题，也可能引用不同版本或片段。这里展示共享来源关系。</p>
        {query.hasNextPage ? <Button variant="ghost" disabled={query.isFetchingNextPage} onClick={() => { void query.fetchNextPage(); }}>加载更多共享主笔记</Button> : null}
      </> : null}
    </> : null}
  </section>;
};
