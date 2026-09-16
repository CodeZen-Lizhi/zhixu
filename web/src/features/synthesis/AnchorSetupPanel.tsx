import { useEffect, useRef, useState } from "react";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { confirmInitialAnchor, listAnchorRecommendations, requestAnchorRecommendation, retryAnchorRecommendation, type AnchorRecommendation, type SynthesisNote } from "../../api/synthesis";
import { Button, ErrorState } from "../../shared/ui";
import { synthesisQueryKeys } from "./queries";

export const AnchorSetupPanel = ({ note }: { note: SynthesisNote }) => {
  const client = useQueryClient();
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => { controller.current?.abort(); }, []);
  const history = useInfiniteQuery({ queryKey: ["synthesis", note.workspaceId, "anchor-analysis", note.id],
    queryFn: ({ pageParam, signal }) => listAnchorRecommendations(note.workspaceId, note.id, pageParam, signal),
    initialPageParam: null as string | null, getNextPageParam: (page) => page.nextAfterId ?? undefined, retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data?.pages.some((page) => page.items.some((item) => item.status === "PENDING" || item.status === "RUNNING")) ? 3000 : false });
  const invalidate = async () => { await client.invalidateQueries({ queryKey: synthesisQueryKeys.all(note.workspaceId) }); };
  const start = useMutation({ mutationFn: requestAnchorRecommendation, retry: false, onSuccess: invalidate });
  const retry = useMutation({ mutationFn: retryAnchorRecommendation, retry: false, onSuccess: invalidate });
  const items = history.data?.pages.flatMap((page) => page.items).filter((item) => item.kind === "INITIAL_SCOPE").sort((a, b) => b.createdAt.localeCompare(a.createdAt)) ?? [];
  const active = items.some((item) => item.status === "PENDING" || item.status === "RUNNING");
  const signal = () => { controller.current?.abort(); controller.current = new AbortController(); return controller.current.signal; };
  return <section aria-label="AI 维护范围分析" className="synthesis-list-section">
    <h3>让 AI 识别维护范围</h3>
    <p className="synthesis-help">根据这份笔记的最新内容和来源，识别主题与用途。确认后持续维护，正文更新仍需审阅。</p>
    <Button disabled={note.currentRevisionId === null || active || start.isPending || start.isError || retry.isPending || retry.isError || history.isPending || history.isError} onClick={() => { start.mutate({ note, idempotencyKey: crypto.randomUUID(), signal: signal() }); }}>分析维护范围</Button>
    <Button variant="ghost" onClick={() => { void history.refetch(); }}>刷新范围分析</Button>
    {history.isPending ? <p role="status">正在读取分析记录…</p> : null}
    {history.isError ? <ErrorState description={history.error.message} onRetry={() => { void history.refetch(); }} /> : null}
    {start.isPending ? <p role="status">正在提交分析…</p> : null}
    {start.isError ? <ErrorState description={start.error.message} onRetry={() => { start.mutate({ ...start.variables, signal: signal() }); }} /> : null}
    {retry.isError ? <ErrorState description={retry.error.message} onRetry={() => { retry.mutate({ ...retry.variables, signal: signal() }); }} /> : null}
    {items.map((item) => <article key={item.id} className="synthesis-item">
      <time dateTime={item.createdAt}>{new Date(item.createdAt).toLocaleString("zh-CN")}</time>
      {item.status === "PENDING" || item.status === "RUNNING" ? <p role="status">{item.status === "PENDING" ? "已排队，等待分析…" : "AI 正在分析主题与用途…"}</p> : null}
      {item.status === "NO_RECOMMENDATION" ? <p>本次没有足够依据提出维护范围。可补充笔记内容后重新分析。</p> : null}
      {item.status === "FAILED" ? <div><p role="alert">分析未完成：{item.errorCode}</p>{item.retryable ? <Button disabled={retry.isPending || retry.isError || start.isPending || start.isError} onClick={() => { retry.mutate({ request: item, idempotencyKey: crypto.randomUUID(), signal: signal() }); }}>重试这次分析</Button> : <p>请检查笔记和来源后重新分析。</p>}</div> : null}
      {item.status === "RECOVERY_REQUIRED" ? <p role="alert">这次分析的执行结果需要复核，系统不会自动重复调用。请检查任务状态。</p> : null}
      {item.status === "SUCCEEDED" && item.recommendation?.scope ? <ScopeConfirmation key={`${item.id}:${String(note.version)}`} note={note} request={item} /> : null}
    </article>)}
    {history.hasNextPage ? <Button variant="ghost" disabled={history.isFetchingNextPage} onClick={() => { void history.fetchNextPage(); }}>加载更早的分析</Button> : null}
  </section>;
};

const ScopeConfirmation = ({ note, request }: { note: SynthesisNote; request: AnchorRecommendation }) => {
  const suggested = request.recommendation;
  const client = useQueryClient();
  const [title, setTitle] = useState(suggested?.title ?? "");
  const [topics, setTopics] = useState(suggested?.scope?.topics.join("\n") ?? "");
  const [audiences, setAudiences] = useState(suggested?.scope?.audiences.join("\n") ?? "");
  const [description, setDescription] = useState(suggested?.scope?.description ?? "");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => { controller.current?.abort(); }, []);
  const mutation = useMutation({ mutationFn: confirmInitialAnchor, retry: false, onSuccess: async () => { await client.invalidateQueries({ queryKey: synthesisQueryKeys.all(note.workspaceId) }); } });
  if (!suggested?.scope) return null;
  const stale = request.basisRevisionId !== note.currentRevisionId;
  const disabled = stale || mutation.isPending || mutation.isError || mutation.isSuccess;
  const lines = (value: string) => [...new Set(value.split("\n").map((line) => line.trim()).filter(Boolean))];
  return <div>
    <p>{suggested.reason}</p>
    <ul>{suggested.evidence.map((ref) => <li key={ref.sourceSpanId}><Link to={`/authoring/notes/${note.id}?${new URLSearchParams({ revision_id: request.basisRevisionId, workspace_id: ref.source.workspaceId, source_id: ref.source.sourceId, source_version_id: ref.source.sourceVersionId, content_artifact_id: ref.source.contentArtifactId, parse_projection_id: ref.source.parseProjectionId, source_span_id: ref.sourceSpanId, content_hash: ref.source.contentHash, excerpt_hash: ref.excerptHash }).toString()}`}>查看依据：{ref.title}</Link></li>)}</ul>
    {stale ? <p role="alert">笔记内容已更新。这份建议基于旧内容，请重新分析后确认。</p> : null}
    <form className="synthesis-anchor-form" onSubmit={(event) => { event.preventDefault(); controller.current = new AbortController(); mutation.mutate({ note, request, title: title.trim(), scope: { topics: lines(topics), audiences: lines(audiences), description: description.trim() }, idempotencyKey: crypto.randomUUID(), signal: controller.current.signal }); }}>
      <label>维护方向名称<input value={title} onChange={(event) => { setTitle(event.target.value); }} maxLength={512} required disabled={disabled} /></label>
      <label>主题（每行一个）<textarea value={topics} onChange={(event) => { setTopics(event.target.value); }} rows={3} required disabled={disabled} /></label>
      <label>用途与受众（每行一个）<textarea value={audiences} onChange={(event) => { setAudiences(event.target.value); }} rows={2} required disabled={disabled} /></label>
      <label>纳入范围<textarea value={description} onChange={(event) => { setDescription(event.target.value); }} rows={3} maxLength={2048} required disabled={disabled} /></label>
      <Button type="submit" disabled={disabled || title.trim() === "" || lines(topics).length === 0 || lines(audiences).length === 0 || description.trim() === ""}>确认并持续维护</Button>
    </form>
    {mutation.isPending ? <p role="status">正在保存维护范围…</p> : null}
    {mutation.isError ? <ErrorState description={mutation.error.message} onRetry={() => { controller.current = new AbortController(); mutation.mutate({ ...mutation.variables, signal: controller.current.signal }); }} /> : null}
    {mutation.isSuccess ? <p role="status">已确认维护范围，可在来源审核中处理后续更新。</p> : null}
  </div>;
};
