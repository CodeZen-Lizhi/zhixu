import { Database, Download, FileText, Plus, RefreshCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { downloadExport, type ExportCreateInput, type ExportJob, type ExportKind } from "../../api/exports";
import type { Collection } from "../../api/collections";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { useCollectionExports, useCreateCollectionExport } from "./export-queries";
import { collectionExportQueryKeys } from "./export-query-keys";

const defaultFields = ["object_type", "id", "title", "summary", "status", "topic", "source", "relations", "health", "confidence", "created_at", "updated_at"] as const;
const formatLabel: Record<ExportKind, string> = { MARKDOWN: "Markdown", METADATA_JSON: "领域元数据 JSON" };
const statusTone = (status: ExportJob["status"]): "neutral" | "success" | "warning" | "danger" | "info" => status === "SUCCEEDED" ? "success" : status === "FAILED" || status === "EXPIRED" ? "danger" : status === "CANCELLED" ? "neutral" : "warning";
const statusText: Record<ExportJob["status"], string> = { PENDING: "等待处理", RUNNING: "正在生成", SUCCEEDED: "可以下载", FAILED: "生成失败", EXPIRED: "结果已过期", CANCELLED: "已取消（历史）" };
const keyFor = (signature: string): string => `collection-export-${crypto.randomUUID()}-${String(signature.length)}`;
const formatTime = (value: string): string => new Date(value).toLocaleString("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

interface ExportAttempt { signature: string; key: string; }

const triggerDownload = (blob: Blob, filename: string): void => {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url; anchor.download = filename; anchor.style.display = "none";
  document.body.append(anchor); anchor.click(); anchor.remove(); URL.revokeObjectURL(url);
};

const ExportJobRow = ({ job, onNewExport, onJobChanged }: { job: ExportJob; onNewExport: (kind: ExportKind) => void; onJobChanged: (job: ExportJob) => Promise<void> }) => {
  const workspaceId = useActiveWorkspaceId();
  const [downloading, setDownloading] = useState(false);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const download = async () => {
    setDownloading(true); setDownloadError(null);
    try { const result = await downloadExport(workspaceId, job); triggerDownload(result.blob, result.filename); }
    catch (error: unknown) { setDownloadError(error instanceof Error ? error.message : "下载 Export 结果失败。"); }
    finally { setDownloading(false); }
    await onJobChanged(job);
  };
  const requiresNewExport = job.status === "FAILED" || job.status === "EXPIRED";
  return <article className="collection-export-job">
    <div className="collection-export-job__summary"><div><strong>{formatLabel[job.kind]}</strong><span className="mono">{job.id.slice(0, 8)}…</span></div><Badge tone={statusTone(job.status)}>{statusText[job.status]}</Badge></div>
    <p>{job.status === "PENDING" ? "任务已接受，正在等待调度。" : job.status === "RUNNING" ? `第 ${String(Math.max(1, job.attemptCount))} 次生成正在进行。` : job.status === "SUCCEEDED" ? `结果将在 ${formatTime(job.expiresAt)} 过期。已下载 ${String(job.downloadCount)} 次。` : job.status === "FAILED" ? `${job.errorCode ?? "EXPORT_FAILED"}：${job.errorMessage ?? "服务端未返回失败说明。"}` : job.status === "EXPIRED" ? "结果文件已到期并不可下载，请创建新的导出。" : "该任务是历史兼容记录，当前版本不提供取消操作。"}</p>
    <div className="collection-export-job__meta"><span>创建于 {formatTime(job.createdAt)}</span><span>尝试 {String(job.attemptCount)} 次</span></div>
    <div className="button-row">{job.status === "SUCCEEDED" ? <Button size="sm" onClick={() => void download()} disabled={downloading}><Download size={14} />{downloading ? "正在下载…" : "下载结果"}</Button> : null}{requiresNewExport ? <Button size="sm" variant="secondary" onClick={() => onNewExport(job.kind)}><Plus size={14} />新建导出</Button> : null}</div>
    {downloadError !== null ? <p className="form-error" role="alert">{downloadError}</p> : null}
  </article>;
};

export const CollectionExportPanel = ({ collection }: { collection: Collection }) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<ExportKind>("MARKDOWN");
  const scope = `${workspaceId}:${collection.id}`;
  const [page, setPage] = useState<{ scope: string; cursors: (string | undefined)[]; index: number }>({ scope, cursors: [undefined], index: 0 });
  const cursor = page.scope === scope ? page.cursors[page.index] : undefined;
  useEffect(() => setPage((current) => current.scope === scope ? current : { scope, cursors: [undefined], index: 0 }), [scope]);
  const attempt = useRef<ExportAttempt | null>(null);
  const exports = useCollectionExports(collection.id, cursor);
  const create = useCreateCollectionExport();
  const [dispatchPendingId, setDispatchPendingId] = useState<string | null>(null);
  const archived = collection.status === "ARCHIVED";
  useEffect(() => {
    if (dispatchPendingId !== null && exports.data?.items.some((job) => job.id === dispatchPendingId && job.status !== "PENDING")) setDispatchPendingId(null);
  }, [dispatchPendingId, exports.data?.items]);
  const createExport = (requestedKind = kind) => {
    if (archived) return;
    const signature = JSON.stringify({ workspaceId, collectionId: collection.id, collectionVersion: collection.version, queryHash: collection.queryHash, kind: requestedKind, fields: defaultFields });
    if (attempt.current?.signature !== signature) attempt.current = { signature, key: keyFor(signature) };
    const input: ExportCreateInput = { workspaceId, collectionId: collection.id, collectionVersion: collection.version, queryHash: collection.queryHash, kind: requestedKind, fields: [...defaultFields], idempotencyKey: attempt.current.key };
    create.mutate(input, { onSuccess: (result) => { attempt.current = null; setDispatchPendingId(result.dispatchPending ? result.job.id : null); } });
  };
  const beginNewExport = (requestedKind: ExportKind) => { attempt.current = null; setKind(requestedKind); createExport(requestedKind); };
  const refreshJobs = async (job: ExportJob): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: collectionExportQueryKeys.all(workspaceId) });
    await queryClient.invalidateQueries({ queryKey: collectionExportQueryKeys.detail(workspaceId, job.id), exact: true });
    await exports.refetch();
  };
  const nextPage = () => {
    const nextCursor = exports.data?.nextCursor;
    if (nextCursor === null || nextCursor === undefined) return;
    setPage((current) => current.scope !== scope ? { scope, cursors: [undefined, nextCursor], index: 1 } : current.cursors[current.index + 1] === nextCursor ? { ...current, index: current.index + 1 } : { ...current, cursors: [...current.cursors.slice(0, current.index + 1), nextCursor], index: current.index + 1 });
  };
  const previousPage = () => setPage((current) => current.scope === scope && current.index > 0 ? { ...current, index: current.index - 1 } : current);
  return <Card className="collection-export-panel">
    <CardHeader eyebrow="异步导出" title="Collection Export" description="使用当前 Collection 版本和范围指纹创建可恢复的服务端任务。" action={<Button variant="ghost" size="sm" onClick={() => void exports.refetch()} disabled={exports.isFetching} aria-label="刷新导出历史"><RefreshCw size={16} /></Button>} />
    {archived ? <UnavailableState title="归档 Collection 不可创建导出" description="归档定义仍保留历史任务，但不能再冻结新的导出范围。" /> : <div className="collection-export-create"><label htmlFor="collection-export-kind">导出格式<select id="collection-export-kind" value={kind} onChange={(event) => setKind(event.target.value === "METADATA_JSON" ? "METADATA_JSON" : "MARKDOWN")}><option value="MARKDOWN">Markdown</option><option value="METADATA_JSON">领域元数据 JSON</option></select></label><Button onClick={() => createExport()} disabled={create.isPending}><span aria-hidden="true">{kind === "MARKDOWN" ? <FileText size={15} /> : <Database size={15} />}</span>{create.isPending ? "正在创建…" : `创建 ${formatLabel[kind]} 导出`}</Button><p className="sidebar-note">默认脱敏，仅导出安全字段；重新尝试同一请求会复用同一个任务。</p></div>}
    {create.isError ? <ErrorState title="导出任务未创建" description={create.error instanceof Error ? create.error.message : "无法创建 Export 任务。"} onRetry={createExport} /> : null}
    {dispatchPendingId !== null ? <div className="ui-state ui-state--warning" role="status"><strong>任务已保存，等待重新调度</strong><p>导出事实已创建；当前投递尚未确认，服务端恢复流程会继续处理。</p></div> : null}
    <div className="collection-export-history" aria-live="polite"><h3>导出历史</h3>{exports.isError ? <ErrorState title="导出历史不可用" description={exports.error instanceof Error ? exports.error.message : "无法读取该 Collection 的 Export 历史。"} onRetry={() => void exports.refetch()} /> : exports.isPending ? <div className="ui-state" role="status"><strong>正在恢复导出任务</strong><p>刷新后仍从服务端读取当前 Collection 的任务状态。</p></div> : exports.data.items.length === 0 ? <EmptyState title="还没有导出任务" description="创建 Markdown 或领域元数据 JSON 导出后，任务会显示在这里。" /> : <div className="collection-export-job-list">{exports.data.items.map((job) => <ExportJobRow key={job.id} job={job} onNewExport={beginNewExport} onJobChanged={refreshJobs} />)}</div>}<div className="pagination-row"><span className="sidebar-note">当前页 {String(exports.data?.items.length ?? 0)} 个任务</span><div className="button-row">{page.scope === scope && page.index > 0 ? <Button size="sm" variant="ghost" onClick={previousPage}>上一页</Button> : null}{exports.data?.nextCursor ? <Button size="sm" variant="secondary" onClick={nextPage}>下一页</Button> : null}</div></div></div>
  </Card>;
};
