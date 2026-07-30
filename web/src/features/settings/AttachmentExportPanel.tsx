import { Archive, Download, Plus, RefreshCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { downloadAttachmentExport, type AttachmentExportCreateInput, type AttachmentExportJob } from "../../api/attachment-exports";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { attachmentExportQueryKeys } from "./attachment-export-query-keys";
import { useAttachmentExports, useCreateAttachmentExport } from "./attachment-export-queries";

const statusTone = (status: AttachmentExportJob["status"]): "neutral" | "success" | "warning" | "danger" => status === "SUCCEEDED" ? "success" : status === "FAILED" || status === "EXPIRED" ? "danger" : status === "CANCELLED" ? "neutral" : "warning";
const statusText: Record<AttachmentExportJob["status"], string> = { PENDING: "等待处理", RUNNING: "正在归档", SUCCEEDED: "可以下载", FAILED: "归档失败", EXPIRED: "结果已过期", CANCELLED: "已取消（历史）" };
const formatTime = (value: string): string => new Date(value).toLocaleString("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const formatBytes = (value: number): string => value < 1024 ? `${String(value)} B` : value < 1_048_576 ? `${(value / 1024).toFixed(1)} KB` : value < 1_073_741_824 ? `${(value / 1_048_576).toFixed(1)} MB` : `${(value / 1_073_741_824).toFixed(2)} GB`;
const createKey = (): string => `attachment-export-${crypto.randomUUID()}`;

interface ExportAttempt { workspaceId: string; key: string; }
interface DispatchPendingJob { workspaceId: string; jobId: string; }

const triggerDownload = (blob: Blob, filename: string): void => {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.style.display = "none";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
};

const AttachmentExportJobRow = ({ job, onCreateNew, onJobChanged }: { job: AttachmentExportJob; onCreateNew: () => void; onJobChanged: (job: AttachmentExportJob) => Promise<void> }) => {
  const workspaceId = useActiveWorkspaceId();
  const [downloading, setDownloading] = useState(false);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const download = async (): Promise<void> => {
    setDownloading(true);
    setDownloadError(null);
    try {
      const result = await downloadAttachmentExport(workspaceId, job);
      triggerDownload(result.blob, result.filename);
    } catch (error: unknown) {
      setDownloadError(error instanceof Error ? error.message : "附件归档下载失败。");
    } finally {
      setDownloading(false);
    }
    await onJobChanged(job);
  };
  const preparedSummary = job.entryCount === null || job.totalUncompressedBytes === null
    ? null
    : `${String(job.entryCount)} 个文件，原始数据 ${formatBytes(job.totalUncompressedBytes)}${job.archiveSize === null ? "" : `，ZIP ${formatBytes(job.archiveSize)}`}`;
  return <article className="attachment-export-job">
    <div className="attachment-export-job__summary">
      <div><strong>Workspace 附件 ZIP</strong><span className="mono">{job.id.slice(0, 8)}…</span></div>
      <Badge tone={statusTone(job.status)}>{statusText[job.status]}</Badge>
    </div>
    <p>{job.status === "PENDING" ? "任务已持久化，正在等待 Worker 领取。" : job.status === "RUNNING" ? `第 ${String(Math.max(1, job.attemptCount))} 次归档正在执行。` : job.status === "SUCCEEDED" ? `${preparedSummary ?? "归档已经完成"}；结果将在 ${formatTime(job.expiresAt)} 过期。` : job.status === "FAILED" ? `${job.errorCode ?? "EXPORT_FAILED"}：${job.errorMessage ?? "服务端未返回失败说明。"}` : job.status === "EXPIRED" ? "生成的 ZIP 已到期；附件源目录未被清理，可创建新的导出。" : "这是历史兼容记录，当前版本不提供取消操作。"}</p>
    <div className="attachment-export-job__meta"><span>创建于 {formatTime(job.createdAt)}</span><span>已下载 {String(job.downloadCount)} 次</span><span>策略 {job.contentPolicy}</span></div>
    <div className="button-row">
      {job.status === "SUCCEEDED" ? <Button size="sm" onClick={() => void download()} disabled={downloading}><Download size={14} />{downloading ? "正在下载…" : "下载 ZIP"}</Button> : null}
      {job.status === "FAILED" || job.status === "EXPIRED" ? <Button size="sm" variant="secondary" onClick={onCreateNew}><Plus size={14} />新建附件导出</Button> : null}
    </div>
    {downloadError !== null ? <p className="form-error" role="alert">{downloadError}</p> : null}
  </article>;
};

export const AttachmentExportPanel = () => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const scope = workspaceId;
  const [page, setPage] = useState<{ scope: string; cursors: (string | undefined)[]; index: number }>({ scope, cursors: [undefined], index: 0 });
  const cursor = page.scope === scope ? page.cursors[page.index] : undefined;
  useEffect(() => setPage((current) => current.scope === scope ? current : { scope, cursors: [undefined], index: 0 }), [scope]);
  const exports = useAttachmentExports(cursor);
  const create = useCreateAttachmentExport();
  const attempt = useRef<ExportAttempt | null>(null);
  const activeWorkspace = useRef(workspaceId);
  activeWorkspace.current = workspaceId;
  const [dispatchPending, setDispatchPending] = useState<DispatchPendingJob | null>(null);
  useEffect(() => {
    setDispatchPending((current) => current?.workspaceId === scope ? current : null);
  }, [scope]);
  useEffect(() => {
    if (dispatchPending?.workspaceId === workspaceId && exports.data?.items.some((job) => job.id === dispatchPending.jobId && job.status !== "PENDING")) setDispatchPending(null);
  }, [dispatchPending, exports.data?.items, workspaceId]);

  const createExport = (forceNew = false): void => {
    if (workspaceId === "") return;
    if (forceNew || attempt.current?.workspaceId !== workspaceId) attempt.current = { workspaceId, key: createKey() };
    const input: AttachmentExportCreateInput = { workspaceId, idempotencyKey: attempt.current.key };
    create.mutate(input, {
      onSuccess: (result) => {
        if (attempt.current?.workspaceId === input.workspaceId && attempt.current.key === input.idempotencyKey) attempt.current = null;
        setDispatchPending((current) => activeWorkspace.current !== input.workspaceId
          ? current
          : result.dispatchPending ? { workspaceId: input.workspaceId, jobId: result.job.id } : null);
      },
    });
  };
  const refreshJobs = async (job: AttachmentExportJob): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: attachmentExportQueryKeys.all(workspaceId) });
    await queryClient.invalidateQueries({ queryKey: attachmentExportQueryKeys.detail(workspaceId, job.id), exact: true });
    await exports.refetch();
  };
  const nextPage = (): void => {
    const nextCursor = exports.data?.nextCursor;
    if (nextCursor === null || nextCursor === undefined) return;
    setPage((current) => current.scope !== scope ? { scope, cursors: [undefined, nextCursor], index: 1 } : current.cursors[current.index + 1] === nextCursor ? { ...current, index: current.index + 1 } : { ...current, cursors: [...current.cursors.slice(0, current.index + 1), nextCursor], index: current.index + 1 });
  };
  const previousPage = (): void => setPage((current) => current.scope === scope && current.index > 0 ? { ...current, index: current.index - 1 } : current);

  if (workspaceId === "") return <UnavailableState title="尚未连接 Workspace" description="连接 Workspace 后才能读取固定 attachments/ 目录并创建可恢复归档。" />;
  return <Card className="attachment-export-panel">
    <CardHeader eyebrow="Data portability" title="Workspace 附件导出" description="将固定 attachments/ 目录中的普通文件生成确定性 ZIP。任务与下载历史来自服务端，可在刷新或重启后恢复。" action={<Button variant="ghost" size="sm" onClick={() => void exports.refetch()} disabled={exports.isFetching} aria-label="刷新附件导出历史"><RefreshCw size={16} /></Button>} />
    <div className="attachment-export-create">
      <div><strong>ATTACHMENTS_ZIP</strong><p>内容策略为 <span className="mono">RAW_USER_OWNED</span>；ZIP 包含原始附件字节和版本化 manifest。</p></div>
      <Button onClick={() => createExport(false)} disabled={create.isPending}><Archive size={15} />{create.isPending ? "正在创建…" : "创建附件导出"}</Button>
    </div>
    {create.isError ? <ErrorState title="附件导出任务未创建" description={create.error instanceof Error ? create.error.message : "无法创建附件导出任务。"} onRetry={() => createExport(false)} /> : null}
    {dispatchPending?.workspaceId === workspaceId ? <div className="ui-state ui-state--warning" role="status"><strong>任务已保存，等待重新调度</strong><p>持久任务已经创建；当前 River 投递尚未确认，服务端恢复流程会继续处理。</p></div> : null}
    <div className="attachment-export-history" aria-live="polite">
      <h3>附件导出历史</h3>
      {exports.isError ? <ErrorState title="附件导出历史不可用" description={exports.error instanceof Error ? exports.error.message : "无法读取附件导出历史。"} onRetry={() => void exports.refetch()} /> : exports.isPending ? <div className="ui-state" role="status"><strong>正在恢复附件导出任务</strong><p>刷新后仍从服务端读取当前 Workspace 的任务状态。</p></div> : exports.data.items.length === 0 ? <EmptyState title="还没有附件导出" description="创建任务后，等待、运行、成功、失败和过期状态都会保留在这里。" /> : <div className="attachment-export-job-list">{exports.data.items.map((job) => <AttachmentExportJobRow key={job.id} job={job} onCreateNew={() => createExport(true)} onJobChanged={refreshJobs} />)}</div>}
      <div className="pagination-row"><span className="sidebar-note">当前页 {String(exports.data?.items.length ?? 0)} 个任务</span><div className="button-row">{page.scope === scope && page.index > 0 ? <Button size="sm" variant="ghost" onClick={previousPage}>上一页</Button> : null}{exports.data?.nextCursor ? <Button size="sm" variant="secondary" onClick={nextPage}>下一页</Button> : null}</div></div>
    </div>
  </Card>;
};
