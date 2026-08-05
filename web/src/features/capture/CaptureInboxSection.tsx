import { FileImage, FileText, Link2, RefreshCw, Type } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { type Capture, type CaptureKind, type CaptureStatus } from "../../api/captures";
import { Badge, Button, Card, CardHeader, EmptyState, UnavailableState } from "../../shared/ui";
import { useCaptures } from "./queries";

const kindMeta = {
  TEXT: { label: "文字", icon: Type },
  URL: { label: "链接", icon: Link2 },
  FILE: { label: "文件", icon: FileText },
  IMAGE: { label: "图片", icon: FileImage },
} satisfies Record<CaptureKind, { label: string; icon: typeof Type }>;

const statusLabel: Record<CaptureStatus, string> = {
  RECEIVED: "已接收",
  SOURCE_SAVED: "原件已保存",
  FETCHING: "正在抓取",
  PROCESSING: "正在处理",
  READY: "可检索",
  READY_DEGRADED: "降级可用",
  FETCH_FAILED: "抓取失败",
  PROCESSING_FAILED: "处理失败",
};

const statusTone = (status: CaptureStatus): "neutral" | "success" | "warning" | "danger" | "info" => {
  if (status === "READY") return "success";
  if (status === "READY_DEGRADED") return "warning";
  if (status === "FETCH_FAILED" || status === "PROCESSING_FAILED") return "danger";
  return "info";
};

const sourceSummary = (capture: Capture): string => {
  if (capture.originalUrl !== undefined) {
    const parsed = new URL(capture.originalUrl);
    return `${parsed.hostname}${parsed.pathname === "/" ? "" : parsed.pathname}`;
  }
  if (capture.kind === "IMAGE" && capture.ingestionStatus === "CAPABILITY_UNAVAILABLE") return "原图已保存，仅保留原件";
  return capture.latestSourceVersionId === undefined ? "正在创建资料版本" : `资料版本 ${capture.latestSourceVersionId.slice(0, 8)}`;
};

export const CaptureInboxSection = ({ workspaceId }: { workspaceId: string }) => {
  const [kind, setKind] = useState<CaptureKind | "">("");
  const [status, setStatus] = useState<CaptureStatus | "">("");
  const [cursor, setCursor] = useState("");
  const query = useCaptures({
    limit: 30,
    ...(kind === "" ? {} : { kind }),
    ...(status === "" ? {} : { status }),
    ...(cursor === "" ? {} : { cursor }),
  });
  const nextCursor = query.data?.nextCursor;

  useEffect(() => setCursor(""), [workspaceId]);

  return <Card className="capture-inbox">
    <CardHeader
      eyebrow="Quick Capture"
      title="快速记录"
      description="文字、链接和上传原件会立即留下 Capture 事实；后台阶段分别更新，不会覆盖原始输入。"
      action={<Button variant="ghost" size="sm" onClick={() => void query.refetch()} disabled={query.isFetching}><RefreshCw size={15} />刷新</Button>}
    />
    <div className="capture-inbox__filters" aria-label="快速记录筛选">
      <label>类型<select value={kind} onChange={(event) => { setKind(event.target.value as CaptureKind | ""); setCursor(""); }}><option value="">全部</option><option value="TEXT">文字</option><option value="URL">链接</option><option value="FILE">文件</option><option value="IMAGE">图片</option></select></label>
      <label>状态<select value={status} onChange={(event) => { setStatus(event.target.value as CaptureStatus | ""); setCursor(""); }}><option value="">全部</option><option value="RECEIVED">已接收</option><option value="SOURCE_SAVED">原件已保存</option><option value="FETCHING">正在抓取</option><option value="PROCESSING">正在处理</option><option value="READY">可检索</option><option value="READY_DEGRADED">降级可用</option><option value="FETCH_FAILED">抓取失败</option><option value="PROCESSING_FAILED">处理失败</option></select></label>
    </div>
    {query.isError ? <UnavailableState title="快速记录暂时无法读取" description={query.error.message} />
      : query.isPending ? <div className="capture-inbox__loading" role="status">正在读取快速记录…</div>
        : query.data.items.length === 0 ? <EmptyState title="还没有快速记录" description="使用顶部的快速记录入口后，真实保存状态会出现在这里。" />
          : <div className="capture-inbox__list">{query.data.items.map((capture) => {
            const meta = kindMeta[capture.kind];
            const Icon = meta.icon;
            return <article className="capture-inbox__row" key={capture.id}>
              <span className="capture-inbox__kind" aria-label={meta.label}><Icon size={17} /></span>
              <div className="capture-inbox__identity">
                <Link to={`/captures/${capture.id}`}>{capture.displayName}</Link>
                <span>{sourceSummary(capture)}</span>
              </div>
              <div className="capture-inbox__state"><Badge tone={statusTone(capture.status)}>{statusLabel[capture.status]}</Badge>{capture.errorCode === undefined ? null : <code>{capture.errorCode}</code>}</div>
              <time dateTime={capture.capturedAt}>{new Date(capture.capturedAt).toLocaleString("zh-CN")}</time>
            </article>;
          })}</div>}
    <div className="pagination-row">{cursor === "" ? <span /> : <Button variant="ghost" onClick={() => setCursor("")}>返回首屏</Button>}{nextCursor === undefined ? null : <Button variant="secondary" onClick={() => setCursor(nextCursor)}>下一页</Button>}</div>
  </Card>;
};
