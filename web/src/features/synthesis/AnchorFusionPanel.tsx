import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getSynthesisProcessing, listRecentAnchorFusionRequests, type AnchorFusionRequest, type KnowledgeAnchor } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, ErrorState } from "../../shared/ui";
import { ProcessingRecord } from "./ProcessingRecord";
import { synthesisQueryKeys } from "./queries";

export const AnchorFusionPanel = ({ anchor }: { anchor: KnowledgeAnchor }) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [expanded, setExpanded] = useState(false);
  const query = useQuery({ queryKey: ["synthesis", anchor.workspaceId, "anchor-fusion", anchor.id],
    queryFn: ({ signal }) => listRecentAnchorFusionRequests(anchor, signal), enabled: expanded && workspaceId === anchor.workspaceId, retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data?.some((request) => request.status === "PENDING") ? 3000 : false });
  const refresh = async () => {
    const result = await query.refetch();
    await Promise.all((result.data ?? []).flatMap((request) => request.processingId === null ? [] : [queryClient.invalidateQueries({ queryKey: synthesisQueryKeys.process(anchor.workspaceId, request.processingId), exact: true })]));
  };
  return <section aria-label="来源融合进度">
    <Button variant="ghost" aria-expanded={expanded} onClick={() => { setExpanded(!expanded); }}>查看来源融合进度</Button>
    {expanded ? <>
      <p className="synthesis-help">最近 20 条已接受来源的融合记录。生成的正文更新仍需审阅后发布。</p>
      <Button variant="ghost" size="sm" onClick={() => { void refresh(); }}>刷新融合记录</Button>
      {query.isPending ? <p role="status">正在读取融合记录…</p> : null}
      {query.isError ? <ErrorState description={query.error.message} onRetry={() => { void query.refetch(); }} /> : null}
      {query.data?.length === 0 ? <p>暂无来源融合记录。</p> : null}
      {query.data?.map((request) => <article key={request.id} className="synthesis-item">
        <h4>{[...new Set(request.sources.map((ref) => ref.title))].join("、")}</h4>
        {request.status === "PENDING" ? <Badge tone="neutral">已接受，等待安排整理</Badge> : null}
        {request.status === "STALE" ? <p>维护范围或来源已变化，本次融合请求已失效，请重新检查关联建议。</p> : null}
        {request.status === "DISPATCHED" ? <FusionProcessing request={request} /> : null}
      </article>)}
    </> : null}
  </section>;
};
const FusionProcessing = ({ request }: { request: AnchorFusionRequest }) => {
  const workspaceId = useActiveWorkspaceId();
  const query = useQuery({ queryKey: synthesisQueryKeys.process(request.workspaceId, request.processingId ?? ""),
    queryFn: async ({ signal }) => {
      if (request.processingId === null) throw new Error("融合任务尚未分配。");
      const processing = await getSynthesisProcessing(request.workspaceId, request.processingId, signal);
      if (processing.sourceVersionId !== request.sources[0]?.source.sourceVersionId) throw new Error("融合任务与审批来源不一致。");
      return processing;
    }, enabled: workspaceId === request.workspaceId && request.processingId !== null, retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data && ["PENDING", "RUNNING"].includes(query.state.data.status) ? 3000 : false });
  const mismatched = query.data !== undefined && query.data.sourceVersionId !== request.sources[0]?.source.sourceVersionId;
  return <>
    {mismatched ? <ErrorState description="融合任务与审批来源不一致。" /> : null}
    {query.isPending ? <p role="status">已安排整理，正在读取实际进度…</p> : null}
    {query.isError ? <ErrorState description={query.error.message} onRetry={() => { void query.refetch(); }} /> : null}
    {query.data && !mismatched ? <ProcessingRecord processing={query.data} /> : null}
  </>;
};
