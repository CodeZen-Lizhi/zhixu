import { useQuery } from "@tanstack/react-query";
import {
  ArrowRight,
  ArrowUpRight,
  ChevronDown,
  FileText,
  FolderOpen,
  RotateCw,
  ShieldCheck,
} from "lucide-react";
import { useId, useState } from "react";
import { Link } from "react-router-dom";

import type { SourceVersionItem } from "../../api/business";
import { listProposals, listSourceVersions, listWorkflows } from "../../api/business";
import { getWorkspace } from "../../api/workspace";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge } from "../../shared/ui";
import {
  formatDashboardTimestamp,
  indexStatusDisplay,
  ingestionStatusDisplay,
  securityStatusDisplay,
  selectDashboardFocus,
  sourceFileName,
  type DashboardStatusDisplay,
} from "./dashboard-view-model";

const errorMessage = (error: unknown): string =>
  error instanceof Error ? error.message : "读取失败";

const KnowledgeLineage = () => {
  const labelId = useId();
  const descriptionId = useId();
  const mobileLabelId = useId();
  const mobileDescriptionId = useId();

  return <div className="dashboard-entry__lineage" aria-hidden="false">
    <svg className="dashboard-lineage dashboard-lineage--desktop" viewBox="0 0 1362 834" preserveAspectRatio="xMidYMid meet" role="img" aria-labelledby={`${labelId} ${descriptionId}`}>
      <title id={labelId}>从本地资料到待审提案的知识脉络</title>
      <desc id={descriptionId}>本地资料形成知识主题，经过证据关系，最终成为需要人工审阅的提案。</desc>
      <path className="dashboard-lineage__hint" d="M800 365 C928 248 1116 237 1240 305" />
      <path className="dashboard-lineage__thread" d="M560 610 C659 589 708 431 800 365 C876 310 930 465 1035 500 C1136 534 1142 347 1240 305" />

      <g className="dashboard-lineage__node">
        <circle className="dashboard-lineage__ring" cx="560" cy="610" r="8" />
        <circle className="dashboard-lineage__core" cx="560" cy="610" r="3" />
        <text x="530" y="648">本地资料</text>
      </g>
      <g className="dashboard-lineage__node dashboard-lineage__node--delay-1">
        <circle className="dashboard-lineage__ring" cx="800" cy="365" r="25" />
        <circle className="dashboard-lineage__core" cx="800" cy="365" r="6" />
        <text x="768" y="417">知识主题</text>
      </g>
      <g className="dashboard-lineage__node dashboard-lineage__node--delay-2">
        <circle className="dashboard-lineage__ring" cx="1035" cy="500" r="15" />
        <circle className="dashboard-lineage__core" cx="1035" cy="500" r="5" />
        <text x="1002" y="543">证据关系</text>
      </g>
      <g className="dashboard-lineage__node dashboard-lineage__node--delay-3">
        <circle className="dashboard-lineage__terminal" cx="1240" cy="305" r="21" />
        <circle className="dashboard-lineage__terminal-core" cx="1240" cy="305" r="5" />
        <text x="1195" y="356">待审提案</text>
      </g>
    </svg>

    <svg className="dashboard-lineage dashboard-lineage--mobile" viewBox="0 0 338 335" preserveAspectRatio="xMidYMid meet" role="img" aria-labelledby={`${mobileLabelId} ${mobileDescriptionId}`}>
      <title id={mobileLabelId}>从本地资料到待审提案的知识脉络</title>
      <desc id={mobileDescriptionId}>本地资料形成知识主题，经过证据关系，最终成为需要人工审阅的提案。</desc>
      <path className="dashboard-lineage__hint" d="M120 160 C185 91 259 79 307 105" />
      <path className="dashboard-lineage__thread" d="M24 275 C74 260 82 181 120 160 C160 137 180 215 226 230 C266 243 273 121 307 105" />

      <g className="dashboard-lineage__node">
        <circle className="dashboard-lineage__ring" cx="24" cy="275" r="6" />
        <circle className="dashboard-lineage__core" cx="24" cy="275" r="2.5" />
        <text x="16" y="304">本地资料</text>
      </g>
      <g className="dashboard-lineage__node dashboard-lineage__node--delay-1">
        <circle className="dashboard-lineage__ring" cx="120" cy="160" r="19" />
        <circle className="dashboard-lineage__core" cx="120" cy="160" r="5" />
        <text x="92" y="200">知识主题</text>
      </g>
      <g className="dashboard-lineage__node dashboard-lineage__node--delay-2">
        <circle className="dashboard-lineage__ring" cx="226" cy="230" r="12" />
        <circle className="dashboard-lineage__core" cx="226" cy="230" r="4" />
        <text x="198" y="265">证据关系</text>
      </g>
      <g className="dashboard-lineage__node dashboard-lineage__node--delay-3">
        <circle className="dashboard-lineage__terminal" cx="308" cy="91" r="17" />
        <circle className="dashboard-lineage__terminal-core" cx="308" cy="91" r="4" />
        <text x="257" y="132">待审提案</text>
      </g>
    </svg>
  </div>;
};

const EntryDashboard = () => {
  const [boundaryExpanded, setBoundaryExpanded] = useState(false);
  const boundaryId = useId();

  return <section className="dashboard-entry" aria-labelledby="dashboard-entry-title">
    <div className="dashboard-entry__copy">
      <h1 id="dashboard-entry-title">知序</h1>
      <h2>让每一条知识，<br />都看得见来路。</h2>
      <p>连接本地 Markdown，把资料、关系与 AI 建议放回一条可追溯的脉络。</p>
      <div className="dashboard-entry__actions">
        <Link className="ui-button ui-button--primary dashboard-entry__primary" to="/workspace">
          <FolderOpen size={16} />
          连接知识目录
          <ArrowRight size={16} />
        </Link>
        <button
          type="button"
          className="dashboard-entry__boundary-trigger"
          aria-expanded={boundaryExpanded}
          aria-controls={boundaryId}
          onClick={() => setBoundaryExpanded((expanded) => !expanded)}
        >
          <ShieldCheck size={15} />
          数据边界
          <ChevronDown className={boundaryExpanded ? "is-expanded" : undefined} size={15} />
        </button>
      </div>
      <p className="dashboard-entry__boundary" id={boundaryId} hidden={!boundaryExpanded}>
        只连接你选择的本地目录；AI 建议经确认后才写回。
      </p>
    </div>
    <KnowledgeLineage />
  </section>;
};

const InlineError = ({
  title,
  description,
  onRetry,
}: {
  title: string;
  description: string;
  onRetry: () => void;
}) => <div className="dashboard-inline-error" role="alert">
  <div><strong>{title}</strong><p>{description}</p></div>
  <button type="button" onClick={onRetry}><RotateCw size={15} />重新读取</button>
</div>;

const SourceStatusStep = ({ label, display }: { label: string; display: DashboardStatusDisplay }) => <div className="dashboard-lineage-step">
  <span className="dashboard-lineage-step__marker" aria-hidden="true" />
  <strong>{label}</strong>
  <Badge tone={display.tone}>{display.label}</Badge>
</div>;

const LeadSource = ({ source }: { source: SourceVersionItem }) => {
  const security = securityStatusDisplay(source.securityStatus);
  const ingestion = ingestionStatusDisplay(source.ingestionStatus);
  const index = indexStatusDisplay(source.indexStatus);

  return <>
    <p className="dashboard-object-label">最近捕获资料</p>
    <h2>{sourceFileName(source.path)}</h2>
    <p className="dashboard-source-path" title={source.path}>{source.path}</p>
    <p className="dashboard-source-meta">捕获于 {formatDashboardTimestamp(source.capturedAt)} · {source.mimeType}</p>
    <div className="dashboard-source-lineage" aria-label="资料处理状态">
      <SourceStatusStep label="安全" display={security} />
      <SourceStatusStep label="解析" display={ingestion} />
      <SourceStatusStep label="索引" display={index} />
    </div>
  </>;
};

const RecentSourceLink = ({ source }: { source: SourceVersionItem }) => {
  const security = securityStatusDisplay(source.securityStatus);
  const ingestion = ingestionStatusDisplay(source.ingestionStatus);
  const index = indexStatusDisplay(source.indexStatus);

  return <Link className="dashboard-recent-row" to={`/documents/${source.id}`}>
    <time dateTime={source.capturedAt}>{formatDashboardTimestamp(source.capturedAt)}</time>
    <div>
      <strong>{sourceFileName(source.path)}</strong>
      <span title={source.path}>{source.path}</span>
      <small>{source.mimeType} · 安全{security.label} · 解析{ingestion.label} · 索引{index.label}</small>
    </div>
    <ArrowUpRight size={15} aria-hidden="true" />
  </Link>;
};

export const DashboardPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const enabled = workspaceId !== "";
  const workspace = useQuery({
    queryKey: ["workspace", workspaceId],
    queryFn: ({ signal }) => getWorkspace(workspaceId, signal),
    enabled,
    retry: false,
  });
  const waitingWorkflows = useQuery({
    queryKey: ["business", workspaceId, "workflows", "dashboard", "waiting_for_human"],
    queryFn: ({ signal }) => listWorkflows(workspaceId, { limit: 1, status: "waiting_for_human" }, signal),
    enabled,
    retry: false,
  });
  const failedWorkflows = useQuery({
    queryKey: ["business", workspaceId, "workflows", "dashboard", "failed"],
    queryFn: ({ signal }) => listWorkflows(workspaceId, { limit: 1, status: "failed" }, signal),
    enabled,
    retry: false,
  });
  const proposals = useQuery({
    queryKey: ["business", workspaceId, "proposals", "dashboard", "ready_for_review"],
    queryFn: ({ signal }) => listProposals(workspaceId, { limit: 5, status: "ready_for_review" }, signal),
    enabled,
    retry: false,
  });
  const sources = useQuery({
    queryKey: ["business", workspaceId, "sources", "dashboard"],
    queryFn: ({ signal }) => listSourceVersions(workspaceId, { limit: 4 }, signal),
    enabled,
    retry: false,
  });

  if (!enabled) return <EntryDashboard />;

  const today = new Date();
  const workflowAndProposalQueries = [waitingWorkflows, failedWorkflows, proposals] as const;
  const focusPending = workflowAndProposalQueries.some((query) => query.isPending);
  const focusErrors = workflowAndProposalQueries.filter((query) => query.isError);
  const focus = selectDashboardFocus({
    waitingWorkflows: waitingWorkflows.data?.items ?? [],
    failedWorkflows: failedWorkflows.data?.items ?? [],
    proposals: proposals.data?.items ?? [],
  });
  const focusUnavailable = !focusPending && focus.kind === "empty" && focusErrors.length > 0;
  const leadSource = sources.data?.items[0];
  const recentSources = sources.data?.items.slice(1) ?? [];
  const retryFocus = (): void => {
    for (const query of focusErrors) void query.refetch();
  };

  return <div className="dashboard-home">
    <header className="dashboard-day-header">
      <div className="dashboard-date-block">
        <strong>{new Intl.DateTimeFormat("zh-CN", { weekday: "long" }).format(today)}</strong>
        <time dateTime={today.toISOString()}>{new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(today)}</time>
      </div>
      <div className="dashboard-day-title">
        <h1>今天的知识桌面</h1>
        <p title={workspace.data?.rootPath}>
          {workspace.isPending ? "正在读取工作区…" : workspace.data
            ? <>{workspace.data.name}<span aria-hidden="true"> · </span>{workspace.data.git.present ? `${workspace.data.git.branch || "未命名分支"} · ${workspace.data.git.dirty ? "有未提交改动" : "工作树干净"}` : "未检测到 Git"}</>
            : "当前工作区信息暂不可用"}
        </p>
      </div>
      <Link className="dashboard-context-link" to="/workspace">工作区设置<ArrowUpRight size={14} /></Link>
    </header>

    {workspace.isError ? <InlineError title="工作区信息暂不可用" description={errorMessage(workspace.error)} onRetry={() => { void workspace.refetch(); }} /> : null}

    <section className="dashboard-focus" aria-labelledby="dashboard-focus-title">
      <div className="dashboard-focus__label">
        <span>{focusPending ? "正在读取" : focusUnavailable ? "读取受阻" : focus.label}</span>
        <strong id="dashboard-focus-title">先处理这一件</strong>
      </div>
      <div className="dashboard-focus__item">
        {focusPending ? <div className="dashboard-focus-skeleton" aria-label="正在读取待办"><i /><i /><i /></div>
          : focusUnavailable ? <><p className="dashboard-object-label">待办读取不完整</p><h2>暂时无法判断下一件事</h2><p>待办列表均未成功返回，请重新读取后再继续。</p></>
          : <><p className="dashboard-object-label">{focus.kind === "proposal" ? `提案 · ${focus.riskLabel}风险` : focus.kind === "empty" ? "工作区" : "流程"}</p><h2>{focus.title}</h2><p>{focus.description}</p></>}
      </div>
      <div className="dashboard-focus__action">
        {!focusPending && !focusUnavailable && focus.kind !== "empty" ? <time dateTime={focus.updatedAt}>更新于 {formatDashboardTimestamp(focus.updatedAt)}</time> : null}
        {focusUnavailable ? <button type="button" className="ui-button ui-button--primary" onClick={retryFocus}><RotateCw size={15} />重新读取待办</button>
          : focusPending ? <span className="dashboard-focus__action-placeholder" />
          : <Link className="ui-button ui-button--primary" to={focus.href}>{focus.actionLabel}<ArrowRight size={16} /></Link>}
      </div>
    </section>

    {focusErrors.length > 0 && !focusUnavailable ? <InlineError title="部分待办读取失败" description="当前焦点来自已成功返回的有界列表，结果可能不完整。" onRetry={retryFocus} /> : null}

    <div className="dashboard-source-grid">
      <section className="dashboard-source-panel" aria-labelledby="dashboard-lead-source-title">
        <div className="dashboard-panel-heading">
          <h2 id="dashboard-lead-source-title">继续最近的资料线索</h2>
          {leadSource ? <Link to={`/documents/${leadSource.id}`}>打开资料<ArrowRight size={14} /></Link> : null}
        </div>
        {sources.isPending ? <div className="dashboard-source-skeleton" aria-label="正在读取最近资料"><i /><i /><i /></div>
          : sources.isError ? <InlineError title="资料列表暂不可用" description={errorMessage(sources.error)} onRetry={() => { void sources.refetch(); }} />
          : leadSource ? <LeadSource source={leadSource} />
          : <div className="dashboard-source-empty"><FileText size={19} /><h3>还没有捕获资料</h3><p>连接目录后，从资料收件箱启动第一次扫描。</p><Link className="ui-button ui-button--secondary" to="/inbox">前往资料收件箱</Link></div>}
      </section>

      <section className="dashboard-source-panel dashboard-source-panel--recent" aria-labelledby="dashboard-recent-title">
        <div className="dashboard-panel-heading">
          <div><h2 id="dashboard-recent-title">最近捕获</h2><p>按服务端捕获时间排序</p></div>
          <Link to="/inbox">查看全部<ArrowRight size={14} /></Link>
        </div>
        {sources.isPending ? <div className="dashboard-recent-skeleton" aria-label="正在读取捕获记录"><i /><i /><i /></div>
          : sources.isError ? <p className="dashboard-recent-note">资料列表恢复后会在这里显示。</p>
          : recentSources.length > 0 ? <div className="dashboard-recent-list">{recentSources.map((source) => <RecentSourceLink key={source.id} source={source} />)}</div>
          : <p className="dashboard-recent-note">暂无更多捕获记录。</p>}
      </section>
    </div>

    <footer className="dashboard-footnote"><ShieldCheck size={15} /><span>资料保留在当前工作区；AI 建议确认后才写回。</span></footer>
  </div>;
};
