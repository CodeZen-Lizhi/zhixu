import { useEffect, useRef, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { SourceReviewApiError, readPendingSourceReviewCommand, sourceReviewClient, sourceReviewIsActive, writePendingSourceReviewCommand } from "../../api/synthesis-source-review";
import type { PendingSourceReviewCommand, SourceReview, SourceReviewClient, SourceReviewEvidence as Evidence, SourceReviewOperation, SourceReviewScope } from "../../api/synthesis-source-review";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Button, Dialog, ErrorState } from "../../shared/ui";
import "./source-review-evidence.css";

const recoveryStatusLabel = (status: NonNullable<SourceReview["recoveryStatus"]>): string => ({ pending: "等待执行", running: "正在应用已保存结果", waiting_for_human: "等待人工处理", retry_wait: "等待重试应用", paused: "已暂停", succeeded: "执行已结束，以当前核验结果为准", failed: "恢复执行失败", cancelled: "恢复已取消" })[status];
const SourceReviewLinks = ({ review, targets = true }: { review: SourceReview; targets?: boolean }) => <div className="source-review-links">
  <Link to={`/authoring/notes?source_review_processing=${review.originProcessingId}&source_review_workspace=${review.workspaceId}`}>查看原处理记录</Link>
  {targets ? review.targets.map((target) => <Link key={target.noteId} to={`/authoring/notes/${target.noteId}?revision_id=${target.baseRevisionId}&source_review_workspace=${review.workspaceId}`}>{target.targetKind === "LOCAL_FILE" ? "查看基线版本" : "查看对应版本"}</Link>) : null}
</div>;

export const sourceReviewStatus = (review: SourceReview): string => {
  if (review.completed && review.latest && review.effectiveStatus === "CURRENT") return "当前正文补充来源已完成";
  if (!review.latest && review.status === "SUCCEEDED") return "历史核验通过";
  if (review.status === "SUCCEEDED") return "当时核验通过，当前需重新核验";
  if (review.status === "REJECTED") return "尚未证实来源支持当前正文，补源未完成";
  if (review.status === "STALE") return "正文、来源或维护范围已变化，补源未完成";
  if (review.status === "FAILED") return "核验未完成";
  if (review.status === "RECOVERY_REQUIRED") return "核验结果尚未确认，需要恢复检查";
  if (review.status === "REVIEWED") return "模型核验已记录，等待完成确认";
  return "正在核验当前正文与来源";
};

export const SourceReviewEvidence = ({ scope, client = sourceReviewClient }: { scope: SourceReviewScope; client?: SourceReviewClient }) => {
  const workspaceId = useActiveWorkspaceId();
  // 作用域键在工作区或版本变化时重新挂载全部证据选择状态。
  return workspaceId === scope.workspaceId ? <SourceReviewPanel key={`${workspaceId}:${scope.processingId ?? `${scope.noteId}:${scope.revisionId}`}`} scope={scope} client={client} /> : null;
};
const SourceReviewPanel = ({ scope, client }: { scope: SourceReviewScope; client: SourceReviewClient }) => {
  const [params, setParams] = useSearchParams();
  let pending: PendingSourceReviewCommand | null = null, invalidCommand = false;
  try { pending = readPendingSourceReviewCommand(params, scope); } catch { invalidCommand = true; }
  const [sending, setSending] = useState(false), [message, setMessage] = useState("");
  const [rejected, setRejected] = useState(false);
  const busy = useRef(false), live = useRef(true), controller = useRef<AbortController | null>(null);
  useEffect(() => { live.current = true; return () => { live.current = false; controller.current?.abort(); }; }, []);
  const [selection, setSelection] = useState<{ reviewId: string; evidence: Evidence } | null>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const query = useInfiniteQuery({
    queryKey: ["synthesis", scope.workspaceId, "source-reviews", scope.processingId, scope.noteId, scope.revisionId],
    queryFn: ({ signal, pageParam }) => client.list(scope, pageParam, signal),
    initialPageParam: null as string | null, getNextPageParam: (page) => page.nextAfterId, retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : (scope.processingId !== undefined && query.state.data?.pages.every((page) => page.items.length === 0)) || query.state.data?.pages.some((page) => page.items.some(sourceReviewIsActive)) ? 5000 : false,
  });
  const commandRead = useQuery({
    queryKey: ["synthesis", scope.workspaceId, "source-review-command", scope.processingId, scope.noteId, scope.revisionId, pending?.command.reviewId, pending?.command.idempotencyKey, pending?.operation, pending?.resultReviewId],
    queryFn: async ({ signal }) => {
      if (!pending) throw new Error("未选择核验命令");
      const c = pending.command;
      const original = await client.get(c.workspaceId, c.reviewId, signal);
      if (original.originProcessingId !== c.originProcessingId || scope.noteId !== undefined && !original.targets.some((t) => t.noteId === scope.noteId && t.baseRevisionId === scope.revisionId)) throw new SourceReviewApiError();
      if (pending.resultReviewId === null || pending.resultReviewId === original.id) return original;
      const result = await client.get(c.workspaceId, pending.resultReviewId, signal);
      if (pending.operation !== "recheck" || result.originProcessingId !== c.originProcessingId || result.supersedesId !== original.id) throw new SourceReviewApiError();
      return result;
    },
    enabled: pending !== null && !invalidCommand && !sending, retry: false, staleTime: 0,
    refetchInterval: (query) => query.state.status !== "error" && query.state.data && sourceReviewIsActive(query.state.data) ? 5000 : false,
  });
  const source = useQuery({
    queryKey: ["synthesis", scope.workspaceId, "source-review-evidence", selection?.reviewId, selection?.evidence.id],
    queryFn: ({ signal }) => { if (!selection) throw new Error("未选择来源"); return client.open(scope.workspaceId, selection.reviewId, selection.evidence, signal); },
    enabled: selection !== null && !query.isError, retry: false, staleTime: 0,
  });
  const refresh = () => { setSelection(null); void query.refetch(); if (pending) void commandRead.refetch(); };
  const send = async (request: PendingSourceReviewCommand) => {
    if (busy.current || invalidCommand) return;
    busy.current = true; setSending(true); setMessage(""); setRejected(false); setSelection(null);
    setParams((old) => writePendingSourceReviewCommand(old, scope, request), { replace: true });
    const abort = new AbortController(); controller.current = abort;
    try {
      const result = await client[request.operation](request.command, abort.signal);
      if (!live.current) return;
      setParams((old) => writePendingSourceReviewCommand(old, scope, { ...request, resultReviewId: result.id }), { replace: true });
      setMessage("请求已记录，正在读取当前核验进度。");
      void query.refetch();
    } catch (e) {
      if (!live.current) return;
      const knownRejection = e instanceof SourceReviewApiError && e.status !== null && e.status >= 400 && e.status < 500 && e.code !== "SYNTHESIS_HTTP_RESULT_INCONSISTENT" && !e.code.endsWith("RESULT_INVALID");
      setRejected(knownRejection);
      setMessage(knownRejection ? e.message : "请求结果尚未确认。原请求已保存在当前链接中；请先重新读取，必要时重试同一请求。");
    } finally { busy.current = false; controller.current = null; if (live.current) setSending(false); }
  };
  const begin = (review: SourceReview, operation: SourceReviewOperation) => {
    if (params.has("sr_key") || query.isFetching || (operation === "recheck" ? !review.canRecheck : !review.canRecover)) return;
    void send({ operation, resultReviewId: null, command: { workspaceId: scope.workspaceId, reviewId: review.id, originProcessingId: review.originProcessingId, expectedVersion: review.version, idempotencyKey: crypto.randomUUID() } });
  };
  return <section className="source-review-evidence" aria-label="当前正文补充来源">
    <h2>当前正文补充来源</h2>
    <p>独立核对完整正文与来源；历史来源的待复核提示仍保留。</p>
    {invalidCommand ? <ErrorState title="补源操作恢复链接无效" description="恢复参数不完整或不匹配，未发送任何操作。" /> : null}
    {pending ? <div className="source-review-command" aria-label="补源操作进度">
      <h3>{pending.operation === "recheck" ? "重新核验当前全文" : "恢复已保存的核验结果"}</h3>
      <p role="status">{sending ? "正在提交操作…" : message || (pending.resultReviewId === null ? "正在恢复读取原请求；刷新不会自动重跑模型。" : "已恢复此操作的核验记录。")}</p>
      {commandRead.isError ? <ErrorState title="核验进度读取失败" description={commandRead.error.message} onRetry={refresh} /> : null}
      {!commandRead.isError && commandRead.data ? <>
        <p>{commandRead.isFetching ? "正在重新检查当前有效性…" : sourceReviewStatus(commandRead.data)}</p>
        {commandRead.data.recoveryStatus ? <p>恢复执行：{recoveryStatusLabel(commandRead.data.recoveryStatus)}</p> : null}
        {commandRead.data.recoveryCompletedAt ? <p>核验结果已保存于 <time dateTime={commandRead.data.recoveryCompletedAt}>{new Date(commandRead.data.recoveryCompletedAt).toLocaleString("zh-CN")}</time>；当前是否完成以上方有效性为准。</p> : null}
        <SourceReviewLinks review={commandRead.data} />
      </> : null}
      <div className="source-review-actions">
        <Button variant="secondary" disabled={sending || commandRead.isFetching} onClick={refresh}>重新读取操作结果</Button>
        {pending.resultReviewId === null && !rejected ? <Button variant="secondary" disabled={sending || !commandRead.data || commandRead.isError || commandRead.isFetching} onClick={() => { void send(pending); }}>重试原请求</Button> : null}
        {rejected || pending.resultReviewId !== null && !commandRead.isError && commandRead.data && !sourceReviewIsActive(commandRead.data) ? <Button variant="ghost" disabled={sending || commandRead.isFetching} onClick={() => { setParams((old) => writePendingSourceReviewCommand(old, scope, null), { replace: true }); setMessage(""); setRejected(false); refresh(); }}>返回核验记录</Button> : null}
      </div>
    </div> : null}
    {query.isPending ? <p role="status">正在读取补源核验记录…</p> : query.isError ? <ErrorState title="补源核验记录读取失败" description={query.error.message} onRetry={refresh} /> : <>
      {query.data.pages.every((p) => p.items.length === 0) ? <p>{scope.processingId !== undefined ? "等待当前正文补源核验记录，可刷新查看进度。" : "暂无此版本的当前正文补源核验记录。"}</p> : null}
      {query.data.pages.flatMap((p) => p.items).map((review) => <article key={review.id}>
        <h3>{query.isRefetching ? "正在重新检查当前有效性…" : sourceReviewStatus(review)}</h3>
        <p>已核验 {review.supportedCount} / {review.obligationCount} 项来源支持关系 · <time dateTime={review.createdAt}>{new Date(review.createdAt).toLocaleString("zh-CN")}</time></p>
        <p>第 {review.attemptNo} 次核验 · {review.latest ? "最新核验记录" : "历史核验记录"}</p>
        {review.status === "STALE" && review.completed ? <p>原失效记录保留，已恢复的核验结果当前有效。</p> : null}
        {review.recoveryStatus ? <p>恢复执行：{recoveryStatusLabel(review.recoveryStatus)}</p> : null}
        {review.recoveryCompletedAt ? <p>恢复结果保存时间：<time dateTime={review.recoveryCompletedAt}>{new Date(review.recoveryCompletedAt).toLocaleString("zh-CN")}</time></p> : null}
        <SourceReviewLinks review={review} targets={false} />
        {review.supersedesId ? <details><summary>查看前次核验身份</summary><code>{review.supersedesId}</code></details> : null}
        {review.canRecheck || review.canRecover ? <div className="source-review-actions">
          {review.canRecheck ? <div><p>重新核验会由 AI 重新分析当前全文与来源。</p><Button variant="secondary" disabled={sending || query.isFetching || params.has("sr_key") || invalidCommand} onClick={() => begin(review, "recheck")}>重新核验当前全文</Button></div> : null}
          {review.canRecover ? <div><p>恢复会复用已保存的核验结果，不再次调用模型。</p><Button variant="secondary" disabled={sending || query.isFetching || params.has("sr_key") || invalidCommand} onClick={() => begin(review, "recover")}>恢复已保存的结果</Button></div> : null}
          {params.has("sr_key") ? <p>请先确认上一次补源操作的结果。</p> : null}
        </div> : null}
        {review.status === "SUCCEEDED" && !review.completed ? <p>原成功记录保留；当前正文或来源已无法按原依据确认。{review.effectiveStatus.includes("SOURCE") ? "来源更新或不可用不代表结论已被证伪。" : ""}</p> : null}
        {review.failure ? <details><summary>查看核验状态</summary><code>{review.failure}</code></details> : null}
        {review.targets.map((target) => <div key={target.noteId}>
          <h4>{target.targetKind === "LOCAL_FILE" ? "当前文件的独立核验快照" : "主笔记版本的核验快照"}</h4>
          {target.targetKind === "LOCAL_FILE" ? <p>此快照包含文件中的人工修改，尚未作为该基线版本发布。下面的来源仅对应此快照。</p> : null}
          <p><Link to={`/authoring/notes/${target.noteId}?revision_id=${target.baseRevisionId}&source_review_workspace=${scope.workspaceId}`}>{target.targetKind === "LOCAL_FILE" ? "查看基线版本" : "查看对应版本"}</Link></p>
          <details><summary>查看精确版本与全文标识</summary><p>基线版本：<code>{target.baseRevisionId}</code></p><p>核验全文 SHA-256：<code>{target.fullContentHash}</code></p></details>
          <details><summary>查看当时核验的完整正文</summary><pre>{target.fullContent}</pre></details>
          {target.evidence.length === 0 ? <p>尚无已完成的段落补充来源。</p> : target.paragraphs.filter((p) => target.evidence.some((e) => e.paragraph === p.label)).map((paragraph) => <div key={paragraph.label}>
            <h5>核验段落 {paragraph.ordinal}</h5><blockquote>{paragraph.text}</blockquote>
            <details><summary>查看段落精确位置</summary><p>UTF-8 字节 [{paragraph.startByte}, {paragraph.endByte}) · SHA-256 <code>{paragraph.hash}</code></p></details>
            <ul>{target.evidence.filter((e) => e.paragraph === paragraph.label).map((e) => <li key={e.id}><Button variant="ghost" disabled={query.isRefetching} onClick={(event) => { trigger.current = event.currentTarget; setSelection({ reviewId: review.id, evidence: e }); }}>打开来源：{e.source.title}</Button>{e.obligations.length > 1 ? <span>支持此段落的 {e.obligations.length} 项来源关系</span> : null}</li>)}</ul>
          </div>)}
        </div>)}
      </article>)}
      {query.hasNextPage ? <Button variant="secondary" disabled={query.isFetching} onClick={() => { void query.fetchNextPage(); }}>加载更多核验记录</Button> : null}
    </>}
    <Button variant="ghost" disabled={query.isFetching} onClick={refresh}>刷新补源核验</Button>
    <Dialog open={selection !== null} onOpenChange={(open) => { if (!open) setSelection(null); }} title={selection?.evidence.source.title ?? "补充来源"} description="查看该次核验保存的精确来源版本与原文片段。" restoreFocusRef={trigger} contentClassName="synthesis-source-dialog">
      {source.isPending ? <p role="status">正在读取来源…</p> : null}
      {source.isError || query.isError ? <ErrorState title="来源读取失败" description={source.error?.message ?? "请先重新读取核验记录。"} onRetry={() => { void source.refetch(); }} /> : null}
      {!source.isError && !query.isError && source.data ? <>
        <p>来源版本：<code>{source.data.reference.source.sourceVersionId}</code></p>
        {source.data.availability !== "AVAILABLE" ? <p role="status">{source.data.availability === "STALE" ? "来源已更新" : "来源当前不可用"}，该来源当前支持关系需重新核验；不表示结论已被证伪。</p> : null}
        {source.data.text !== null ? <pre className="synthesis-source-excerpt">{source.data.text}</pre> : null}
        {source.data.snapshotText ? <><h3>已保存的历史原文</h3><pre className="synthesis-source-excerpt">{source.data.snapshotText}</pre></> : null}
      </> : null}
    </Dialog>
  </section>;
};
