import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getSynthesisSourceKnowledgePoints, type SynthesisRevision, type SynthesisSourceRef } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Button, ErrorState } from "../../shared/ui";

export const SourceKnowledgePoints = ({ revision, reference }: { revision: SynthesisRevision; reference: SynthesisSourceRef }) => {
  const workspaceId = useActiveWorkspaceId();
  const [expanded, setExpanded] = useState(false);
  const query = useQuery({ queryKey: ["synthesis", revision.workspaceId, "source-knowledge", revision.noteId, revision.id, reference],
    queryFn: ({ signal }) => getSynthesisSourceKnowledgePoints(revision.workspaceId, revision.noteId, revision.id, reference, signal),
    enabled: expanded && workspaceId === revision.workspaceId && workspaceId === reference.source.workspaceId, retry: false });
  return <section aria-label="片段知识目录">
    <Button variant="ghost" aria-expanded={expanded} onClick={() => { setExpanded(!expanded); }}>查看片段对应知识点</Button>
    {expanded ? <>
      {query.isPending ? <p role="status">正在读取知识目录…</p> : null}
      {query.isError ? <ErrorState description={query.error.message} onRetry={() => { void query.refetch(); }} /> : null}
      {query.data?.directory.status === "UNRECORDED" ? <p>此主笔记版本未记录知识目录快照，原文引用仍可追溯。</p> : null}
      {query.data?.directory.status === "UNANALYZED" ? <p>该来源版本尚未完成知识分析。</p> : null}
      {query.data?.directory.status === "UNAVAILABLE" ? <p>该来源版本的知识分析暂不可用，原有引用保持不变。</p> : null}
      {query.data?.directory.status === "ANALYZED" ? <>
        <p className="synthesis-help">来自此主笔记版本保存的 AI 知识目录快照，供定位与复核。</p>
        {query.data.points.length === 0 ? <p>保存的目录没有与此原文片段精确对应的知识点。</p> : <ul>{query.data.points.map((point) =>
          <li key={`${point.profileRevisionId}:${point.kind}:${String(point.index)}`}>{point.text}</li>)}</ul>}
      </> : null}
    </> : null}
  </section>;
};
