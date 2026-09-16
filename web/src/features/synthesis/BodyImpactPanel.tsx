import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { getSynthesisBodyImpacts, type SynthesisRevision } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Button, ErrorState } from "../../shared/ui";

export const BodyImpactPanel = ({ revision }: { revision: SynthesisRevision }) => {
  const workspaceId = useActiveWorkspaceId();
  const query = useInfiniteQuery({
    queryKey: ["synthesis", workspaceId, "body-impacts", revision.noteId, revision.id],
    queryFn: ({ signal, pageParam }) => getSynthesisBodyImpacts(revision, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.nextAfterId,
    enabled: workspaceId === revision.workspaceId, retry: false,
  });
  if (workspaceId !== revision.workspaceId) return null;
  if (query.isPending) return <p role="status">正在检查主笔记引用更新…</p>;
  if (query.isError && !query.isFetchNextPageError) return <ErrorState title="主笔记引用提醒读取失败" description={query.error.message} onRetry={() => { void query.refetch(); }} />;
  const items = query.data.pages.flatMap((page) => page.items);
  if (items.length === 0) return null;
  return <section className="synthesis-source-impacts" aria-label="主笔记引用待复核">
    <h2>主笔记引用待复核</h2>
    <p>引用的主笔记有更新，请复核相关片段；当前正文与历史引用仍保留。</p>
    <ul>{items.map((impact) => {
      const index = revision.items.findIndex((item) => item.id === impact.itemId);
      const ref = revision.items[index]?.bodyReference;
      if (!ref) return null;
      const oldHref = `/authoring/notes/${ref.noteId}?${new URLSearchParams({ revision_id: ref.revisionId, body_item_id: ref.itemId, body_workspace_id: ref.workspaceId, body_projection_hash: ref.projectionHash }).toString()}`;
      const newHref = `/authoring/notes/${impact.upstreamNoteId}?${new URLSearchParams({ revision_id: impact.publishedRevisionId }).toString()}`;
      return <li key={impact.id}>
        <strong>片段 {index + 1} 引用的主笔记有更新</strong>
        <p>{impact.reason === "ITEM_MISSING" ? "新发布版本中已无原引用片段，请复核；当前片段不会自动删除。" : "引用片段的发布内容发生变化，请对照原引用复核。"}</p>
        <div className="synthesis-impact-links">
          <a href={`#synthesis-item-${impact.itemId}`}>查看受影响片段 {index + 1}</a>
          <Link to={oldHref}>查看原引用片段</Link>
          <Link to={newHref}>查看新发布版本</Link>
        </div>
        <time dateTime={impact.detectedAt}>发现于 {new Date(impact.detectedAt).toLocaleString("zh-CN")}</time>
      </li>;
    })}</ul>
    {query.isFetchNextPageError ? <ErrorState title="更多主笔记引用提醒读取失败" description={query.error.message} onRetry={() => { void query.fetchNextPage(); }} /> : null}
    {query.hasNextPage && !query.isFetchNextPageError ? <Button variant="secondary" disabled={query.isFetching} onClick={() => { void query.fetchNextPage(); }}>{query.isFetchingNextPage ? "正在读取…" : "加载更多引用提醒"}</Button> : null}
    <Button variant="ghost" size="sm" disabled={query.isFetching} onClick={() => { void query.refetch(); }}>刷新引用提醒</Button>
  </section>;
};
