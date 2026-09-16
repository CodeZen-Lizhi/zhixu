import { useEffect, useRef, useState } from "react";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { decideAnchorProposals, listAnchorProposals, listKnowledgeAnchors, type AnchorDecisionInput, type AnchorProposal, type KnowledgeAnchor, type SynthesisNote } from "../../api/synthesis";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, ErrorState } from "../../shared/ui";
import { AnchorSetupPanel } from "./AnchorSetupPanel";
import { AnchorFusionPanel } from "./AnchorFusionPanel";
import { synthesisQueryKeys } from "./queries";

const firstPage = (): string | null => null;
export const AnchorReviewPanel = ({ noteId, note }: { noteId: string; note?: SynthesisNote }) => {
  const workspaceId = useActiveWorkspaceId();
  const [expanded, setExpanded] = useState(false);
  const anchors = useInfiniteQuery({ queryKey: ["synthesis", workspaceId, "anchors", noteId],
    queryFn: ({ signal, pageParam }) => listKnowledgeAnchors(workspaceId, noteId, pageParam, signal),
    initialPageParam: firstPage(), getNextPageParam: (page) => page.nextAfterId ?? undefined,
    enabled: expanded && workspaceId !== "", retry: false });
  return <section className="synthesis-list-section" aria-label="主笔记维护范围">
    <Button variant="secondary" aria-expanded={expanded} aria-controls="anchor-review" onClick={() => { setExpanded(!expanded); }}>维护范围与来源审核</Button>
    {expanded ? <div id="anchor-review">
      {anchors.isPending ? <p role="status">正在读取维护范围…</p> : null}
      {anchors.isError ? <ErrorState description={anchors.error.message} onRetry={() => { void anchors.refetch(); }} /> : null}
      {anchors.data?.pages[0]?.items.length === 0 ? <><p className="synthesis-help">这份笔记还没有确认持续维护的范围。</p>{note ? <AnchorSetupPanel key={`${note.id}:${String(note.version)}`} note={note} /> : null}</> : null}
      {anchors.data?.pages.flatMap((page) => page.items).map((anchor) => <div className="synthesis-item" key={anchor.id}>
        <h3>{anchor.title}</h3><p>{anchor.scope.description}</p><p>主题：{anchor.scope.topics.join("、")} · 用途与受众：{anchor.scope.audiences.join("、")}</p>
        <ProposalReview key={`${anchor.id}:associations`} anchor={anchor} kind="SOURCE_ASSOCIATION" />
        <ProposalReview key={`${anchor.id}:scope`} anchor={anchor} kind="SCOPE_ADJUSTMENT" />
        <AnchorFusionPanel anchor={anchor} />
      </div>)}
      {anchors.hasNextPage ? <Button disabled={anchors.isFetchingNextPage} onClick={() => { void anchors.fetchNextPage(); }}>加载更多维护方向</Button> : null}
    </div> : null}
  </section>;
};

const ProposalReview = ({ anchor, kind }: { anchor: KnowledgeAnchor; kind: AnchorProposal["kind"] }) => {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<string[]>([]);
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => { controller.current?.abort(); }, []);
  const proposals = useInfiniteQuery({ queryKey: ["synthesis", anchor.workspaceId, "anchor-proposals", anchor.id, kind],
    queryFn: ({ signal, pageParam }) => listAnchorProposals(anchor.workspaceId, anchor.id, kind, pageParam, signal),
    initialPageParam: firstPage(), getNextPageParam: (page) => page.nextAfterId ?? undefined, retry: false });
  const mutation = useMutation({ mutationFn: decideAnchorProposals, retry: false, onSuccess: async () => {
    setSelected([]);
    await queryClient.invalidateQueries({ queryKey: synthesisQueryKeys.all(anchor.workspaceId) });
  } });
  const items = proposals.data?.pages.flatMap((page) => page.items) ?? [];
  const selectedItems = items.filter((item) => selected.includes(item.id) && item.status === "PENDING" && item.scopeVersion === anchor.scopeVersion);
  const submit = (decision: AnchorDecisionInput["decision"]) => {
    controller.current?.abort(); controller.current = new AbortController();
    mutation.mutate({ workspaceId: anchor.workspaceId, anchor, kind, decision, items: selectedItems, idempotencyKey: crypto.randomUUID(), signal: controller.current.signal });
  };
  const retry = () => {
    const previous = mutation.variables;
    if (!previous) return;
    controller.current = new AbortController();
    mutation.mutate({ ...previous, signal: controller.current.signal });
  };
  return <section className="synthesis-list-section" aria-label={kind === "SOURCE_ASSOCIATION" ? "来源关联审核" : "范围调整审核"}>
    <h4>{kind === "SOURCE_ASSOCIATION" ? "来源关联审核" : "范围调整审核"}</h4>
    <p className="synthesis-help">{kind === "SOURCE_ASSOCIATION" ? "接受来源后才可用于融合；此操作不会扩大维护范围，也不会发布正文。" : "确认后才改变维护方向，正文仍需单独审阅发布。一次只确认一项范围调整。"}</p>
    {proposals.isPending ? <p role="status">正在读取建议…</p> : null}
    {proposals.isError ? <ErrorState description={proposals.error.message} onRetry={() => { void proposals.refetch(); }} /> : null}
    {proposals.isSuccess && items.length === 0 ? <p>暂无建议。</p> : null}
    {items.map((item) => <article className="synthesis-item" key={item.id}>
      <Badge tone={item.status === "ACCEPTED" ? "success" : "neutral"}>{item.status === "ACCEPTED" ? "已接受" : item.status === "REJECTED" ? "已拒绝" : item.scopeVersion !== anchor.scopeVersion ? "范围已变化，建议过期" : "待审核"}</Badge>
      {item.status === "PENDING" && item.scopeVersion === anchor.scopeVersion ? <label><input type="checkbox" checked={selected.includes(item.id)} disabled={mutation.isPending || mutation.isError}
        onChange={(event) => { setSelected(event.target.checked ? kind === "SCOPE_ADJUSTMENT" ? [item.id] : [...selected, item.id].slice(0, 32) : selected.filter((id) => id !== item.id)); }} />选择：{item.reason}</label> : <p>{item.reason}</p>}
      {item.suggested ? <p>建议范围：{item.suggested.description}（{item.suggested.topics.join("、")}；{item.suggested.audiences.join("、")}）</p> : null}
      <ul>{item.evidence.map((ref) => <li key={ref.sourceSpanId}><Link to={`/documents/${ref.source.sourceVersionId}`}>{ref.title}</Link></li>)}</ul>
    </article>)}
    {selectedItems.length > 0 ? <div className="synthesis-detail-status">
      <Button disabled={mutation.isPending || mutation.isError} onClick={() => { submit("ACCEPTED"); }}>接受所选（{selectedItems.length}）</Button>
      <Button variant="secondary" disabled={mutation.isPending || mutation.isError} onClick={() => { submit("REJECTED"); }}>拒绝所选</Button>
    </div> : null}
    {mutation.isPending ? <p role="status">正在保存审核结果…</p> : null}
    {mutation.isError ? <><ErrorState title="审核结果尚未确认" description={mutation.error.message} onRetry={retry} /><Button variant="ghost" onClick={() => { mutation.reset(); setSelected([]); void queryClient.invalidateQueries({ queryKey: synthesisQueryKeys.all(anchor.workspaceId) }); }}>重新读取后选择</Button></> : null}
    {mutation.isSuccess ? <p role="status">审核结果已保存，正式正文保持原样。</p> : null}
    {proposals.hasNextPage ? <Button disabled={proposals.isFetchingNextPage} onClick={() => { void proposals.fetchNextPage(); }}>加载更多建议</Button> : null}
  </section>;
};
