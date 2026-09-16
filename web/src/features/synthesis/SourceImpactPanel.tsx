import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { getSynthesisSourceImpacts, type SynthesisRevision } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Button, ErrorState } from "../../shared/ui";
import { synthesisSourceHref } from "./source-link";

export const SourceImpactPanel = ({ revision }: { revision: SynthesisRevision }) => {
  const workspaceId = useActiveWorkspaceId();
  const query = useQuery({ queryKey: ["synthesis", workspaceId, "source-impacts", revision.noteId, revision.id],
    queryFn: ({ signal }) => getSynthesisSourceImpacts(revision, signal), enabled: workspaceId === revision.workspaceId, retry: false });
  if (workspaceId !== revision.workspaceId) return null;
  if (query.isPending) return <p role="status">正在检查来源复核提醒…</p>;
  if (query.isError) return <ErrorState title="来源复核提醒读取失败" description={query.error.message} onRetry={() => { void query.refetch(); }} />;
  if (query.data.length === 0) return null;
  return <section className="synthesis-source-impacts" aria-label="来源待复核">
    <h2>来源待复核</h2>
    <p>以下片段的来源曾发生变化。正文与历史引用仍保留，来源不可用不代表结论错误。</p>
    <ul>{query.data.map((impact) => <li key={`${impact.id}:${impact.reference.sourceSpanId}`}>
      <strong>{impact.reference.title}</strong>
      <p>{impact.reason === "SOURCE_REMOVED" ? (impact.currentlyUnavailable ? "来源已删除，请复核相关内容。" : "来源曾被删除，当前已恢复，相关内容仍待复核。") : (impact.currentlyUnavailable ? "来源存在隔离记录，尚未恢复可用，请复核相关内容。" : "来源曾被隔离，当前已恢复，相关内容仍待复核。")}</p>
      <div className="synthesis-impact-links">{impact.itemIds.map((id) => <a key={id} href={`#synthesis-item-${id}`}>查看受影响片段 {revision.items.findIndex((item) => item.id === id) + 1}</a>)}
        <Link to={synthesisSourceHref(revision.noteId, revision.id, impact.reference)}>查看保存的原始引用</Link></div>
      <time dateTime={impact.detectedAt}>发现于 {new Date(impact.detectedAt).toLocaleString("zh-CN")}</time>
    </li>)}</ul>
    <Button variant="ghost" size="sm" disabled={query.isFetching} onClick={() => { void query.refetch(); }}>刷新来源状态</Button>
  </section>;
};
