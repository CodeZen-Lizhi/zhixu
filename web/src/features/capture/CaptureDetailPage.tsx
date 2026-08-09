import { Check, CircleDashed, CircleOff, CircleX, ExternalLink, FileText, Link2, RefreshCw, RotateCcw, TriangleAlert } from "lucide-react";
import { Link, useParams } from "react-router-dom";

import {
  CaptureApiError,
  type Capture,
  type CaptureKind,
  type CaptureStageStatus,
  type DocumentKnowledgeProfile,
  type KnowledgeProfileCandidate,
  type KnowledgeProfilePoint,
} from "../../api/captures";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, EmptyState, PageHeader, UnavailableState } from "../../shared/ui";
import { SourceSpanViewer } from "../source-spans";
import { useCapture, useKnowledgeProfile, useRetryCapture, useRetryKnowledgeProfile } from "./queries";

const stageMeta: Record<CaptureStageStatus, { label: string; tone: "neutral" | "success" | "warning" | "danger" | "info"; icon: typeof Check }> = {
  PENDING: { label: "等待", tone: "neutral", icon: CircleDashed },
  RUNNING: { label: "进行中", tone: "info", icon: CircleDashed },
  READY: { label: "完成", tone: "success", icon: Check },
  FAILED: { label: "失败", tone: "danger", icon: CircleX },
  CAPABILITY_UNAVAILABLE: { label: "能力不可用", tone: "warning", icon: CircleOff },
  STALE: { label: "已过期", tone: "warning", icon: TriangleAlert },
  NOT_APPLICABLE: { label: "不适用", tone: "neutral", icon: CircleOff },
};

const captureKindLabels: Record<CaptureKind, string> = {
  TEXT: "文字",
  URL: "链接",
  FILE: "文件",
  IMAGE: "图片",
};

const errorText = (error: unknown): string => {
  if (error instanceof CaptureApiError) return `${error.message}（${error.errorCode}）`;
  return error instanceof Error ? error.message : "请求未完成。";
};

const StageRow = ({ label, status, note }: { label: string; status: CaptureStageStatus; note: string }) => {
  const meta = stageMeta[status];
  const Icon = meta.icon;
  return <li className={`capture-stage capture-stage--${status.toLowerCase()}`}>
    <span className="capture-stage__icon" aria-hidden="true"><Icon size={16} /></span>
    <div><strong>{label}</strong><span>{note}</span></div>
    <Badge tone={meta.tone}>{meta.label}</Badge>
  </li>;
};

const EvidenceRefs = ({ workspaceId, sourceVersionId, sourceSpanIds }: { workspaceId: string; sourceVersionId: string; sourceSpanIds: string[] }) => <details className="profile-evidence">
  <summary>{sourceSpanIds.length} 个来源片段</summary>
  <div>{sourceSpanIds.map((sourceSpanId, index) => <SourceSpanViewer
    key={sourceSpanId}
    className="profile-evidence__button"
    label={`证据 ${String(index + 1)}`}
    reference={{ workspaceId, sourceVersionId, sourceSpanId }}
  />)}</div>
</details>;

const CandidateList = ({ title, items, workspaceId, sourceVersionId }: { title: string; items: KnowledgeProfileCandidate[]; workspaceId: string; sourceVersionId: string }) => <section className="profile-candidate-section">
  <h3>{title}</h3>
  {items.length === 0 ? <p className="muted">没有足够证据形成此类候选。</p> : <div className="profile-candidate-list">{items.map((item) => <article key={`${item.label}:${item.sourceSpanIds.join(":")}`}>
    <div><strong>{item.label}</strong>{item.aliases.length === 0 ? null : <p>别名：{item.aliases.join("、")}</p>}</div>
    <EvidenceRefs workspaceId={workspaceId} sourceVersionId={sourceVersionId} sourceSpanIds={item.sourceSpanIds} />
  </article>)}</div>}
</section>;

const PointList = ({ title, items, workspaceId, sourceVersionId }: { title: string; items: KnowledgeProfilePoint[]; workspaceId: string; sourceVersionId: string }) => <section className="profile-candidate-section">
  <h3>{title}</h3>
  {items.length === 0 ? <p className="muted">没有足够证据形成此类候选。</p> : <ol className="profile-point-list">{items.map((item, index) => <li key={`${String(index)}:${item.text}`}><p>{item.text}</p><EvidenceRefs workspaceId={workspaceId} sourceVersionId={sourceVersionId} sourceSpanIds={item.sourceSpanIds} /></li>)}</ol>}
</section>;

const ProfileProjection = ({ profile }: { profile: DocumentKnowledgeProfile }) => {
  const revision = profile.revision;
  if (revision === undefined) return null;
  return <div className="knowledge-profile">
    <div className="knowledge-profile__summary"><p className="eyebrow">摘要</p><p>{revision.summary}</p></div>
    <div className="knowledge-profile__columns">
      <CandidateList title="候选主题" items={revision.topics} workspaceId={profile.workspaceId} sourceVersionId={profile.sourceVersionId} />
      <CandidateList title="术语与别名" items={revision.terms} workspaceId={profile.workspaceId} sourceVersionId={profile.sourceVersionId} />
    </div>
    <PointList title="关键知识点" items={revision.knowledgePoints} workspaceId={profile.workspaceId} sourceVersionId={profile.sourceVersionId} />
    <PointList title="关键示例" items={revision.examples} workspaceId={profile.workspaceId} sourceVersionId={profile.sourceVersionId} />
    <dl className="knowledge-profile__revision"><div><dt>知识配置修订版本</dt><dd className="mono">{revision.id}</dd></div><div><dt>结构版本</dt><dd>{revision.schemaVersion}</dd></div><div><dt>提示词版本</dt><dd>{revision.promptVersion}</dd></div><div><dt>生成时间</dt><dd>{new Date(revision.createdAt).toLocaleString("zh-CN")}</dd></div></dl>
  </div>;
};

const ProfilePanel = ({ capture }: { capture: Capture }) => {
  const sourceVersionId = capture.latestSourceVersionId;
  const shouldRead = sourceVersionId !== undefined && capture.profileStatus !== "PENDING";
  const query = useKnowledgeProfile(shouldRead ? { workspaceId: capture.workspaceId, captureId: capture.id, sourceVersionId } : undefined);
  const retry = useRetryKnowledgeProfile();
  const profile = query.data;
  const rebuildableWithoutRetryFlag = profile?.status === "CAPABILITY_UNAVAILABLE" || profile?.status === "STALE";

  const startRetry = (): void => {
    if (profile === undefined) return;
    retry.mutate({
      workspaceId: profile.workspaceId,
      captureId: profile.captureId,
      sourceVersionId: profile.sourceVersionId,
      expectedVersion: profile.version,
      idempotencyKey: `profile-retry-${profile.id}-${String(profile.version)}-${crypto.randomUUID()}`,
    });
  };

  return <Card className="capture-profile-card">
    <CardHeader
      eyebrow="候选投影"
      title="文档知识画像"
      description="这些摘要、标签和知识点是可重建候选，不是已确认的主题、主张或关系。"
      action={profile?.retryable || rebuildableWithoutRetryFlag ? <Button variant="secondary" size="sm" disabled={retry.isPending} onClick={startRetry}><RotateCcw size={15} />{retry.isPending ? "正在重试…" : rebuildableWithoutRetryFlag ? "重新生成画像" : "重试画像"}</Button> : undefined}
    />
    {capture.profileStatus === "PENDING" ? <div className="capture-profile-state" role="status"><CircleDashed size={17} /><div><strong>等待基础解析</strong><p>画像只会从已验证的来源片段生成。</p></div></div> : null}
    {shouldRead && query.isPending ? <div className="capture-profile-state" role="status"><CircleDashed size={17} /><div><strong>正在读取画像状态</strong><p>基础资料与索引状态不依赖这次读取。</p></div></div> : null}
    {!query.isPending && !query.isError && (capture.profileStatus === "RUNNING" || profile?.status === "RUNNING") ? <div className="capture-profile-state" role="status"><CircleDashed size={17} /><div><strong>正在生成候选画像</strong><p>基础检索不会等待这个阶段。</p></div></div> : null}
    {capture.profileStatus === "CAPABILITY_UNAVAILABLE" && profile?.revision === undefined ? <UnavailableState title="当前仅保留基础资料" description="模型能力不可用，没有伪造摘要或候选知识。" /> : null}
    {query.isError ? <div className="ui-state ui-state--error" role="alert"><strong>画像状态无法读取</strong><p>{errorText(query.error)}</p><Button variant="secondary" onClick={() => void query.refetch()}><RefreshCw size={15} />重新读取</Button></div> : null}
    {profile?.status === "FAILED" ? <div className="capture-profile-warning" role="alert"><TriangleAlert size={17} /><div><strong>最近一次画像生成失败</strong><p>{profile.errorCode}</p></div></div> : null}
    {profile?.status === "CAPABILITY_UNAVAILABLE" ? <div className="capture-profile-warning"><CircleOff size={17} /><div><strong>模型能力不可用</strong><p>{profile.errorCode}</p></div></div> : null}
    {profile?.status === "STALE" ? <div className="capture-profile-warning"><TriangleAlert size={17} /><div><strong>当前画像已过期</strong><p>来源或处理依赖已经变化；旧修订版本仍可复核。</p></div></div> : null}
    {profile === undefined ? null : <ProfileProjection profile={profile} />}
    {retry.isError ? <div className="ui-state ui-state--error" role="alert"><strong>画像重试未确认</strong><p>{errorText(retry.error)}</p><Button variant="secondary" onClick={() => retry.mutate(retry.variables)}>重试原请求</Button></div> : null}
  </Card>;
};

export const CaptureDetailPage = () => {
  const { captureId = "" } = useParams();
  const workspaceId = useActiveWorkspaceId();
  const query = useCapture(captureId);
  const retry = useRetryCapture();

  if (workspaceId === "") return <div className="page-stack"><PageHeader title="快速记录" /><Card><EmptyState title="先连接 Workspace" description="连接工作区后才能读取快速记录。" action={<Button asChild><Link to="/workspace">连接或切换 Workspace</Link></Button>} /></Card></div>;
  if (query.isPending) return <div className="page-stack"><PageHeader title="快速记录" description="正在恢复服务端状态。" /><Card><p role="status">正在读取记录…</p></Card></div>;
  if (query.isError) return <div className="page-stack"><PageHeader title="快速记录" /><UnavailableState title="快速记录详情不可用" description={errorText(query.error)} /></div>;
  const capture = query.data;

  const startRetry = (): void => retry.mutate({
    workspaceId,
    captureId: capture.id,
    expectedVersion: capture.version,
    idempotencyKey: `capture-retry-${capture.id}-${String(capture.version)}-${crypto.randomUUID()}`,
  });

  return <div className="page-stack capture-detail">
    <PageHeader title={capture.displayName} description="原始输入与各处理阶段分别保留；刷新后仍以服务端状态为准。" action={<Button asChild variant="ghost"><Link to="/inbox">返回收件箱</Link></Button>} />
    {capture.errorCode === undefined ? null : <div className="capture-detail__failure" role="alert"><TriangleAlert size={18} /><div><strong>{capture.failureStage ?? "处理阶段"}未完成</strong><p>{capture.errorCode}</p></div>{capture.retryable && capture.failureStage !== "PROFILE" ? <Button variant="secondary" disabled={retry.isPending} onClick={startRetry}><RotateCcw size={15} />{retry.isPending ? "正在重试…" : "重试处理"}</Button> : null}</div>}
    {retry.isError ? <div className="ui-state ui-state--error" role="alert"><strong>重试结果尚未确认</strong><p>{errorText(retry.error)}</p><Button variant="secondary" onClick={() => retry.mutate(retry.variables)}>重试原请求</Button></div> : null}

    <div className="capture-detail__grid">
      <Card>
        <CardHeader eyebrow="来源快照" title="原始来源" description="抓取、OCR、摘要和整理都不会覆盖这些来源事实。" />
        <dl className="detail-grid"><div><dt>类型</dt><dd>{captureKindLabels[capture.kind]}</dd></div><div><dt>记录 ID</dt><dd className="mono">{capture.id}</dd></div><div><dt>来源 ID</dt><dd className="mono">{capture.sourceId}</dd></div><div><dt>捕获时间</dt><dd>{new Date(capture.capturedAt).toLocaleString("zh-CN")}</dd></div></dl>
        {capture.originalUrl === undefined ? null : <a className="capture-original-url" href={capture.originalUrl} target="_blank" rel="noreferrer"><Link2 size={15} aria-hidden="true" /><span>{capture.originalUrl}</span><ExternalLink size={14} /></a>}
        {capture.latestSourceVersionId === undefined ? <p className="muted">URL 原件尚未生成资料版本；记录与原始链接仍已保存。</p> : <Button asChild variant="secondary"><Link to={`/documents/${capture.latestSourceVersionId}`}><FileText size={15} />查看资料版本</Link></Button>}
      </Card>
      <Card>
        <CardHeader eyebrow="处理进度" title="处理阶段" description="单个能力失败不会把其他已完成阶段改成失败。" />
        <ol className="capture-stage-list">
          <StageRow label="抓取原件" status={capture.fetchStatus} note={capture.kind === "URL" ? "受限网络抓取并保留原始 URL" : "本地输入无需网络抓取"} />
          <StageRow label="解析内容" status={capture.ingestionStatus} note={capture.kind === "IMAGE" && capture.ingestionStatus === "CAPABILITY_UNAVAILABLE" ? "仅保存原图，没有伪造 OCR 正文" : "生成可追溯片段与文本块"} />
          <StageRow label="建立索引" status={capture.indexStatus} note="关键词检索可独立于向量检索使用" />
          <StageRow label="候选画像" status={capture.profileStatus} note="只生成非正式候选投影" />
        </ol>
      </Card>
    </div>
    <ProfilePanel capture={capture} />
  </div>;
};
