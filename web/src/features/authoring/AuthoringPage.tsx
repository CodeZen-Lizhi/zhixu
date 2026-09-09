import { Activity, ArrowRight, CheckCircle2, Clock3, FileCheck2, FilePlus2, FileText, Layers3, RefreshCw, TriangleAlert } from "lucide-react";
import { Link } from "react-router-dom";

import type { PublicationStatus } from "../../api/authoring";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, EmptyState, ErrorState, PageHeader } from "../../shared/ui";
import { useAuthoringOverview } from "./queries";
import "./authoring.css";

const errorText = (error: unknown): string => error instanceof Error ? error.message : "创作台暂时无法读取。";

const publicationMeta: Record<PublicationStatus, { label: string; tone: "info" | "danger" | "success" }> = {
  PENDING: { label: "等待确认", tone: "info" },
  PUBLISHED: { label: "已完成", tone: "success" },
  RECOVERY_REQUIRED: { label: "待恢复", tone: "danger" },
  CLOSED: { label: "已关闭", tone: "danger" },
};

const formatTime = (value: string): string => new Intl.DateTimeFormat("zh-CN", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
}).format(new Date(value));

export const AuthoringPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const overview = useAuthoringOverview();

  if (workspaceId === "") return <div className="page-stack authoring-page"><PageHeader title="创作" /><EmptyState title="先连接 Workspace" description="连接工作区后才能创建和恢复文章。" action={<Button asChild><Link to="/workspace">连接 Workspace</Link></Button>} /></div>;

  return <div className="page-stack authoring-page">
    <PageHeader title="创作" description="从空白文章开始，或用已有材料整理成文。" />

    <section className="authoring-paths" aria-label="创作方式">
      <Link className="authoring-path" to="/authoring/notes">
        <span className="authoring-path__icon"><FileText size={21} /></span>
        <span><strong>合成笔记</strong><small>持续整理知识点与原始依据</small></span>
        <ArrowRight size={18} />
      </Link>
      <Link className="authoring-path" to="/authoring/new">
        <span className="authoring-path__icon"><FilePlus2 size={21} /></span>
        <span><strong>新建文章</strong><small>空白 Markdown</small></span>
        <ArrowRight size={18} />
      </Link>
      {overview.data?.organizing.available && overview.data.organizing.href !== null ? <Link className="authoring-path" to={overview.data.organizing.href}>
        <span className="authoring-path__icon"><Layers3 size={21} /></span>
        <span><strong>整理成文</strong><small>选择材料并生成草稿</small></span>
        <ArrowRight size={18} />
      </Link> : <div className="authoring-path authoring-path--disabled" aria-disabled="true" title={overview.data?.organizing.reason ?? "整理能力正在准备"}>
        <span className="authoring-path__icon"><Layers3 size={21} /></span>
        <span><strong>整理成文</strong><small>{overview.data?.organizing.reason ?? "暂不可用"}</small></span>
      </div>}
    </section>

    {overview.isPending ? <div className="authoring-overview-state" role="status"><Clock3 size={18} />正在读取创作状态…</div> : null}
    {overview.isError ? <ErrorState description={errorText(overview.error)} onRetry={() => { void overview.refetch(); }} /> : null}

    {overview.data ? <div className="authoring-overview">
      <section className="authoring-list-section" aria-labelledby="authoring-recent-heading">
        <header><h2 id="authoring-recent-heading">最近草稿</h2><span>{overview.data.recentDrafts.length}</span></header>
        {overview.data.recentDrafts.length === 0 ? <p className="authoring-list-empty">还没有草稿。</p> : <div className="authoring-list">{overview.data.recentDrafts.map((draft) => <Link className="authoring-row" key={draft.id} to={`/authoring/new?draft=${draft.id}`}>
          <span><strong>{draft.title.trim() === "" ? "未命名文章" : draft.title}</strong><small>{draft.targetPath.trim() === "" ? "尚未设置路径" : draft.targetPath}</small></span>
          <span className="authoring-row__meta"><time dateTime={draft.updatedAt}>{formatTime(draft.updatedAt)}</time><Badge tone={draft.status === "EDITING" ? "info" : "neutral"}>{draft.status === "EDITING" ? `v${String(draft.version)}` : "已归档"}</Badge></span>
        </Link>)}</div>}
      </section>

      <section className="authoring-list-section" aria-labelledby="authoring-pending-heading">
        <header><h2 id="authoring-pending-heading">待确认</h2><span>{overview.data.pendingPublications.length}</span></header>
        {overview.data.pendingPublications.length === 0 ? <p className="authoring-list-empty">没有等待处理的发布。</p> : <div className="authoring-list">{overview.data.pendingPublications.map((publication) => {
          const meta = publicationMeta[publication.status];
          return <Link className="authoring-row" key={publication.id} to={publication.proposalHref}>
            <span><strong>{publication.targetPath}</strong><small>提案 {publication.proposalId}</small></span>
            <span className="authoring-row__meta">{publication.status === "RECOVERY_REQUIRED" ? <TriangleAlert size={16} /> : <RefreshCw size={16} />}<Badge tone={meta.tone}>{meta.label}</Badge></span>
          </Link>;
        })}</div>}
      </section>

      <section className="authoring-list-section" aria-labelledby="authoring-completed-heading">
        <header><h2 id="authoring-completed-heading">已完成</h2><span>{overview.data.completedDocuments.length}</span></header>
        {overview.data.completedDocuments.length === 0 ? <p className="authoring-list-empty">还没有已发布文章。</p> : <div className="authoring-list">{overview.data.completedDocuments.map((document) => <Link className="authoring-row" key={document.id} to={`/authoring/documents/${document.id}/history`}>
          <span><strong>{document.title}</strong><small>{document.canonicalPath}</small></span>
          <span className="authoring-row__meta"><CheckCircle2 size={16} /><Badge tone="success">已发布</Badge><ArrowRight size={16} /></span>
        </Link>)}</div>}
      </section>
    </div> : null}

    <nav className="authoring-record-navigation" aria-label="创作高级记录">
      <span>高级记录</span>
      <Link to="/proposals"><FileCheck2 size={15} />审批记录</Link>
      <Link to="/workflows"><Activity size={15} />执行记录</Link>
      <Link to="/artifacts"><FileText size={15} />生成产物</Link>
    </nav>
  </div>;
};
