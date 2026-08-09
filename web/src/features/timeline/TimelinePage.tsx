import {
  Activity,
  ArrowLeft,
  ArrowRight,
  Box,
  CalendarClock,
  ExternalLink,
  FileClock,
  Filter,
  GitCommitHorizontal,
  RefreshCw,
  RotateCcw,
  ShieldAlert,
  Sparkles,
} from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";

import { useActiveWorkspaceId } from "../../app/active-workspace";
import {
  TimelineApiError,
  timelineAggregateTypes,
  timelineEventTypes,
  type ArtifactImpactObject,
  type ImpactAction,
  type ImpactObject,
  type ImpactObjectType,
  type ImpactReport,
  type ReviewCardImpactBinding,
  type ReviewCardImpactObject,
  type TimelineAggregateType,
  type TimelineEvent,
  type TimelineEventType,
  type TimelineOperatorType,
} from "../../api/timeline";
import { Badge, Button, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import {
  canonicalTimelineFilter,
  useAnalyzeImpact,
  useCreateDownstreamUpdateProposal,
  useImpactReport,
  useTimelineEvent,
  useTimelinePage,
} from "./queries";
import {
  emptyTimelineUrlState,
  parseTimelineUrlState,
  serializeTimelineUrlState,
  timelineFilterFromUrlState,
  type TimelineUrlState,
} from "./url-state";
import "./timeline.css";

const formatUtc = (value: string): string => new Intl.DateTimeFormat("zh-CN", {
  dateStyle: "medium",
  timeStyle: "medium",
  timeZone: "UTC",
}).format(new Date(value));

const shortId = (value: string): string => `${value.slice(0, 8)}…${value.slice(-4)}`;
const newKey = (prefix: string): string => `${prefix}-${crypto.randomUUID()}`;

const timelineEventTypeLabels: Record<TimelineEventType, string> = {
  PROPOSAL_CREATED: "提案已创建",
  APPROVAL_GRANTED: "审批已批准",
  APPROVAL_REJECTED: "审批已驳回",
  GIT_COMMITTED: "Git 已提交",
  RELATION_CONFIRMED: "关系已确认",
  RELATION_DEPRECATED: "关系已废弃",
  CONFLICT_OPENED: "已发现冲突",
  CONFLICT_TRANSITIONED: "冲突状态已变更",
  CONFLICT_RESOLVED: "冲突已解决",
  VERSION_PUBLISHED: "版本已发布",
  VERSION_SUPERSEDED: "版本已被替代",
  HEALTH_ISSUE_DETECTED: "发现健康问题",
  HEALTH_ISSUE_RESOLVED: "健康问题已解决",
  IMPACT_ANALYZED: "已完成影响分析",
  ARTIFACT_GENERATED: "产物已生成",
  REVIEW_CARD_INVALIDATED: "复习卡已失效",
  CORRECTIVE_EVENT: "修正事件",
};

const aggregateTypeLabels: Record<TimelineAggregateType, string> = {
  PROPOSAL: "提案",
  APPROVAL: "审批",
  GIT_COMMIT: "Git 提交",
  TOPIC: "主题",
  CLAIM: "知识点",
  RELATION: "关系",
  CONFLICT: "冲突",
  DOCUMENT: "文档",
  ARTICLE_REVISION: "文章修订版本",
  HEALTH_ISSUE: "健康问题",
  IMPACT_REPORT: "影响报告",
  ARTIFACT: "产物",
  REVIEW_CARD: "复习卡",
};

const impactObjectTypeLabels: Record<ImpactObjectType, string> = {
  TOPIC: "主题",
  CLAIM: "知识点",
  RELATION: "关系",
  CONFLICT: "冲突",
  HEALTH_ISSUE: "健康问题",
  PROPOSAL: "提案",
  ARTICLE_REVISION: "文章修订版本",
  AUDIT_EVENT: "审计事件",
  ARTIFACT: "产物",
  REVIEW_CARD: "复习卡",
};

const impactActionLabels: Record<ImpactAction, string> = {
  REVIEW: "需要审阅",
  REINDEX: "重新索引",
  RESOLVE_CONFLICT: "解决冲突",
  REFRESH_HEALTH: "刷新健康检查",
  REGENERATE_ARTIFACT: "重新生成产物",
  REVALIDATE_REVIEW_CARD: "重新校验复习卡",
  NO_ACTION: "无需操作",
};

const impactReportStatusLabels: Record<ImpactReport["status"], string> = {
  READY: "就绪",
  STALE: "已失效",
  FAILED: "失败",
};

const reviewCardStatusLabels: Record<ReviewCardImpactBinding["status"], string> = {
  DRAFT: "草稿",
  APPROVED: "已批准",
  INVALIDATED: "已失效",
  REJECTED: "已驳回",
};

const operatorTypeLabels: Record<TimelineOperatorType, string> = {
  USER: "用户",
  API_TOKEN: "API Token",
  SYSTEM: "系统",
  UNKNOWN: "未知",
};

const errorDescription = (error: Error): string => {
  if (!(error instanceof TimelineApiError)) return error.message;
  if (error.status === 404) return "资源不存在，或不属于当前 Workspace。";
  if (error.status === 409) return `${error.message} 当前版本可能已经变化，请刷新后重新发起。`;
  if (error.status === 503) return `${error.message} 时间线 / 影响分析依赖暂不可用。`;
  if (error.code === "INVALID_RESPONSE") return "服务端响应未通过严格契约校验，页面没有展示部分可信数据。";
  if (error.code === "NETWORK_ERROR") return "请求结果未知。重试时将复用原 Idempotency-Key。";
  return error.message;
};

const isUnknownResult = (error: Error | null): boolean =>
  error instanceof TimelineApiError && (error.code === "NETWORK_ERROR" || error.status === 503 && error.retryable);

const Failure = ({ error, title, onRetry, onReset }: { error: Error; title: string; onRetry: () => void; onReset?: () => void }) => {
  if (error instanceof TimelineApiError && error.status === 503) {
    return <div className="timeline-failure"><UnavailableState title="时间线 / 影响分析暂不可用" description={errorDescription(error)} /><Button variant="secondary" onClick={onRetry}>重试</Button></div>;
  }
  return <div className="timeline-failure"><ErrorState title={title} description={errorDescription(error)} onRetry={onRetry} />{onReset ? <Button variant="ghost" onClick={onReset}><RotateCcw size={15} />回到首屏</Button> : null}</div>;
};

const eventTone = (event: TimelineEvent): "neutral" | "success" | "warning" | "danger" | "info" => {
  if (event.eventType.includes("REJECTED") || event.eventType.includes("CONFLICT")) return "danger";
  if (event.eventType.includes("INVALIDATED") || event.eventType.includes("SUPERSEDED")) return "warning";
  if (event.eventType.includes("GRANTED") || event.eventType.includes("RESOLVED") || event.eventType.includes("GENERATED")) return "success";
  return "info";
};

const operatorLabel = (event: TimelineEvent): string => {
  if (event.schemaVersion === "knowledge-event/v1") return "未记录";
  if (event.operator.id === undefined) return operatorTypeLabels[event.operator.type];
  return `${operatorTypeLabels[event.operator.type]} · ${shortId(event.operator.id)}`;
};

const AggregateRef = ({ event }: { event: TimelineEvent }) => {
  const label = aggregateTypeLabels[event.aggregateType];
  if (event.aggregateId === undefined) return <span>{label}</span>;
  if (event.aggregateType === "ARTIFACT") return <Link to={`/artifacts/${event.aggregateId}`}>{label} · {shortId(event.aggregateId)}<ExternalLink size={13} /></Link>;
  if (event.aggregateType === "PROPOSAL") return <Link to={`/proposals/${event.aggregateId}`}>{label} · {shortId(event.aggregateId)}<ExternalLink size={13} /></Link>;
  return <span>{label} · {shortId(event.aggregateId)}</span>;
};

const CorrelationLinks = ({ event }: { event: TimelineEvent }) => <div className="timeline-correlation" aria-label="结构化关联">
  {event.correlation.proposalId ? <Link to={`/proposals/${event.correlation.proposalId}`}><ExternalLink size={13} />提案 {shortId(event.correlation.proposalId)}</Link> : null}
  {event.correlation.workflowRunId ? <Link to={`/workflows/${event.correlation.workflowRunId}`}><ExternalLink size={13} />Workflow {shortId(event.correlation.workflowRunId)}</Link> : null}
  {event.correlation.approvalId ? <span>审批 {shortId(event.correlation.approvalId)}</span> : null}
  {event.correlation.auditEventId ? <span>审计 {shortId(event.correlation.auditEventId)}</span> : null}
  {event.correlation.gitCommitRef ? <span><GitCommitHorizontal size={13} />提交 {event.correlation.gitCommitRef}</span> : null}
  {Object.keys(event.correlation).length === 0 ? <span>没有结构化关联</span> : null}
</div>;

const EventRow = ({ event }: { event: TimelineEvent }) => <article className="timeline-row">
  <div className="timeline-row__time"><time dateTime={event.occurredAt}>{formatUtc(event.occurredAt)}</time><span>UTC</span></div>
  <div className="timeline-row__body"><div className="timeline-row__heading"><Badge tone={eventTone(event)}>{timelineEventTypeLabels[event.eventType]}</Badge><span>v{String(event.eventVersion)}</span></div><h3><Link to={`/timeline/${event.id}`}>{event.summary || "无摘要事件"}</Link></h3><div className="timeline-row__meta"><AggregateRef event={event} /><span>{operatorLabel(event)}</span><span title={event.sourceEventRef}>来源 · {event.sourceEventRef}</span></div></div>
  <Link className="timeline-row__open" to={`/timeline/${event.id}`} aria-label={`打开事件 ${event.summary || event.id}`}><ArrowRight size={17} /></Link>
</article>;

const utcInput = (value: string): string => value === "" ? "" : new Date(value).toISOString().slice(0, 16);
const utcValue = (value: string): string => value === "" ? "" : `${value}:00Z`;

const Filters = ({ state, onChange }: { state: TimelineUrlState; onChange: (state: TimelineUrlState) => void }) => {
  const [draft, setDraft] = useState(state);
  useEffect(() => setDraft(state), [state]);
  const update = <K extends keyof TimelineUrlState>(key: K, value: TimelineUrlState[K]): void => setDraft((current) => ({ ...current, [key]: value }));
  return <form className="timeline-filters" onSubmit={(event) => { event.preventDefault(); onChange(draft); }}>
    <div className="timeline-section-heading"><div><span><Filter size={15} />筛选条件</span></div><div><Button type="button" variant="ghost" onClick={() => { setDraft(emptyTimelineUrlState); onChange(emptyTimelineUrlState); }}>清除</Button><Button type="submit">应用筛选</Button></div></div>
    <div className="timeline-filter-grid">
      <label>事件类型<select multiple value={draft.eventTypes} onChange={(event) => update("eventTypes", [...event.currentTarget.selectedOptions].flatMap((option) => { const resolved = timelineEventTypes.find((item) => item === option.value); return resolved === undefined ? [] : [resolved]; }))}>{timelineEventTypes.map((type) => <option key={type} value={type}>{timelineEventTypeLabels[type]}</option>)}</select></label>
      <label>聚合类型<select value={draft.aggregateType} onChange={(event) => { const resolved = timelineAggregateTypes.find((item) => item === event.target.value); update("aggregateType", resolved ?? ""); }}><option value="">全部</option>{timelineAggregateTypes.map((type) => <option key={type} value={type}>{aggregateTypeLabels[type]}</option>)}</select></label>
      <label>聚合 ID<input value={draft.aggregateId} onChange={(event) => update("aggregateId", event.target.value)} placeholder="规范 UUID" /></label>
      <label>来源事件引用<input value={draft.sourceEventRef} onChange={(event) => update("sourceEventRef", event.target.value)} placeholder="精确来源引用" /></label>
      <label>开始时间（UTC）<input type="datetime-local" value={utcInput(draft.occurredAfter)} onChange={(event) => update("occurredAfter", utcValue(event.target.value))} /></label>
      <label>结束时间（UTC）<input type="datetime-local" value={utcInput(draft.occurredBefore)} onChange={(event) => update("occurredBefore", utcValue(event.target.value))} /></label>
    </div>
  </form>;
};

export const TimelinePage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [parameters, setParameters] = useSearchParams();
  const state = parseTimelineUrlState(parameters);
  const filter = timelineFilterFromUrlState(state);
  const scope = `${workspaceId}:${canonicalTimelineFilter(filter)}`;
  const [pageState, setPageState] = useState<{ scope: string; cursors: string[] }>({ scope, cursors: [] });
  const cursors = pageState.scope === scope ? pageState.cursors : [];
  const cursor = cursors.at(-1);
  useEffect(() => setPageState((current) => current.scope === scope ? current : { scope, cursors: [] }), [scope]);
  const timeline = useTimelinePage(filter, cursor, 25);

  if (workspaceId === "") return <div className="timeline-workbench"><UnavailableState title="先连接 Workspace" description="时间线只读取当前 Workspace 的持久化事件与影响报告。" /><Link className="ui-button ui-button--primary" to="/workspace">连接 Workspace</Link></div>;

  return <div className="timeline-workbench">
    <header className="timeline-hero"><div><h1>时间线</h1><p>查看知识事件及其影响。</p></div><Button variant="secondary" onClick={() => void timeline.refetch()} disabled={timeline.isFetching}><RefreshCw size={16} />{timeline.isFetching ? "正在刷新" : "刷新"}</Button></header>
    <Filters state={state} onChange={(next) => setParameters(serializeTimelineUrlState(next), { replace: true })} />
    <section className="timeline-stream" aria-labelledby="timeline-stream-title"><div className="timeline-section-heading"><div><h3 id="timeline-stream-title"><FileClock size={15} />事件流</h3></div>{cursor ? <Badge tone="info">后续窗口</Badge> : <Badge>首屏</Badge>}</div>
      {timeline.isPending ? <div className="timeline-loading" aria-live="polite"><Activity size={18} /><span>正在读取时间线事件…</span></div> : timeline.isError ? <Failure error={timeline.error} title="事件流读取失败" onRetry={() => void timeline.refetch()} {...(cursor === undefined ? {} : { onReset: () => setPageState({ scope, cursors: [] }) })} /> : timeline.data.items.length === 0 ? <EmptyState title="没有匹配事件" description="当前筛选没有持久化时间线事件；这不代表服务不可用。" /> : <div className="timeline-list">{timeline.data.items.map((event) => <EventRow key={event.id} event={event} />)}</div>}
      {timeline.isSuccess ? <div className="timeline-pagination">{cursors.length > 0 ? <Button variant="ghost" onClick={() => setPageState((current) => current.scope === scope ? { scope, cursors: current.cursors.slice(0, -1) } : { scope, cursors: [] })}><ArrowLeft size={15} />上一页</Button> : <span />}{timeline.data.nextCursor ? <Button variant="secondary" onClick={() => { const nextCursor = timeline.data.nextCursor; if (nextCursor !== undefined) setPageState((current) => current.scope === scope ? { scope, cursors: [...current.cursors, nextCursor] } : { scope, cursors: [nextCursor] }); }}>下一页<ArrowRight size={15} /></Button> : null}</div> : null}
    </section>
  </div>;
};

const OwnerBinding = ({ object }: { object: ArtifactImpactObject | ReviewCardImpactObject }) => object.type === "ARTIFACT"
  ? <dl className="timeline-binding"><div><dt>产物版本</dt><dd>{String(object.artifactBinding.artifactVersion)}</dd></div><div><dt>修订版本</dt><dd>{shortId(object.artifactBinding.revisionId)} · #{String(object.artifactBinding.revisionNo)}</dd></div><div><dt>内容哈希</dt><dd><code>{object.artifactBinding.contentHash}</code></dd></div></dl>
  : <dl className="timeline-binding"><div><dt>卡片版本</dt><dd>{String(object.reviewCardBinding.cardVersion)} · {reviewCardStatusLabels[object.reviewCardBinding.status]}</dd></div><div><dt>知识点</dt><dd>{shortId(object.reviewCardBinding.claimId)}</dd></div><div><dt>证据绑定</dt><dd><code>{object.reviewCardBinding.evidenceBindingFingerprint}</code></dd></div></dl>;

const objectCanCreate = (report: ImpactReport, object: ImpactObject): object is ArtifactImpactObject | ReviewCardImpactObject =>
  report.schemaVersion === "impact-report/v2" && report.status === "READY" && report.supersededByReportId === null && (object.type === "ARTIFACT" || object.type === "REVIEW_CARD") && object.requiresProposal;

const ImpactReportView = ({ report }: { report: ImpactReport }) => {
  const navigate = useNavigate();
  const createProposal = useCreateDownstreamUpdateProposal();
  const submit = (object: ArtifactImpactObject | ReviewCardImpactObject, reuse = false): void => {
    const previous = createProposal.variables;
    const input = reuse && previous !== undefined
      ? previous
      : { workspaceId: report.workspaceId, reportId: report.id, targetType: object.type, targetId: object.id, action: object.action, idempotencyKey: newKey("downstream-update") };
    createProposal.mutate(input, {
      onSuccess: (proposal) => void navigate(`/proposals/${proposal.id}`),
    });
  };
  const createError = createProposal.isError ? createProposal.error : null;
  return <section className="impact-report" aria-labelledby="impact-report-title"><div className="timeline-section-heading"><div><span><Sparkles size={15} />影响报告</span><h3 id="impact-report-title">{report.analysisVersion} · {impactReportStatusLabels[report.status]}</h3></div><div><Badge tone={report.status === "READY" ? "success" : report.status === "STALE" ? "warning" : "danger"}>{report.schemaVersion}</Badge>{report.schemaVersion === "impact-report/v2" && report.supersededByReportId !== null ? <Badge tone="warning">已被替代</Badge> : null}</div></div>
    <dl className="timeline-report-meta"><div><dt>报告 ID</dt><dd>{report.id}</dd></div><div><dt>指纹</dt><dd><code>{report.fingerprint}</code></dd></div><div><dt>来源事件</dt><dd>{shortId(report.sourceEventId)} · v{String(report.sourceEventVersion)}</dd></div><div><dt>生成时间</dt><dd>{formatUtc(report.generatedAt)} UTC</dd></div>{report.schemaVersion === "impact-report/v2" ? <><div><dt>替代的报告</dt><dd>{report.supersedesReportId ?? "无"}</dd></div><div><dt>被替代为</dt><dd>{report.supersededByReportId ?? "无"}</dd></div></> : null}</dl>
    {report.status === "STALE" ? <UnavailableState title="报告已失效" description={report.staleReason ?? "归属绑定已变化。"} /> : null}{report.status === "FAILED" ? <ErrorState title="影响分析失败" description={report.errorCode ?? "服务端未返回错误码。"} /> : null}
    {report.objects.length === 0 ? <EmptyState title="没有下游影响" description="当前分析策略没有找到与该事件精确关联的对象。" /> : <div className="impact-object-list">{report.objects.map((object) => <article key={`${object.type}:${object.id}`} className="impact-object"><div className="impact-object__main"><div><Badge tone={object.requiresProposal ? "warning" : "neutral"}>{impactObjectTypeLabels[object.type]}</Badge><span>v{String(object.version)}</span></div><h4>{impactActionLabels[object.action]}</h4><p>{object.reason || "没有附加原因"}</p><code>{object.id}</code>{object.type === "ARTIFACT" || object.type === "REVIEW_CARD" ? <OwnerBinding object={object} /> : null}</div><div className="impact-object__action">{object.type === "ARTIFACT" ? <Link to={`/artifacts/${object.id}`}>打开产物<ExternalLink size={13} /></Link> : null}{objectCanCreate(report, object) ? <Button size="sm" disabled={createProposal.isPending} onClick={() => submit(object)}><ShieldAlert size={14} />创建提案</Button> : <span>只读影响</span>}</div></article>)}</div>}
    {createError ? <div className="timeline-command-error" role="alert"><strong>{createError instanceof TimelineApiError && createError.status === 409 ? "提案创建冲突" : createError instanceof TimelineApiError && createError.status === 503 ? "提案归属服务暂不可用" : "提案创建未完成"}</strong><p>{errorDescription(createError)}</p>{isUnknownResult(createError) ? <Button variant="secondary" onClick={() => { const variables = createProposal.variables; const object = variables?.reportId === report.id ? report.objects.find((item): item is ArtifactImpactObject | ReviewCardImpactObject => objectCanCreate(report, item) && item.id === variables.targetId && item.type === variables.targetType && item.action === variables.action) : undefined; if (object !== undefined) submit(object, true); }}>重试原创建请求</Button> : null}</div> : null}
  </section>;
};

const EventDetail = ({ event }: { event: TimelineEvent }) => <section className="timeline-event-detail"><div className="timeline-event-detail__title"><div><Badge tone={eventTone(event)}>{timelineEventTypeLabels[event.eventType]}</Badge><h1>{event.summary || "无摘要事件"}</h1></div><time dateTime={event.occurredAt}>{formatUtc(event.occurredAt)} UTC</time></div><dl className="timeline-report-meta"><div><dt>事件 ID</dt><dd>{event.id}</dd></div><div><dt>结构版本</dt><dd>{event.schemaVersion}</dd></div><div><dt>聚合对象</dt><dd><AggregateRef event={event} /></dd></div><div><dt>操作者</dt><dd>{operatorLabel(event)}</dd></div><div><dt>来源事件引用</dt><dd>{event.sourceEventRef}</dd></div><div><dt>来源引用</dt><dd>{event.sourceRef}</dd></div></dl><CorrelationLinks event={event} />{event.schemaVersion === "knowledge-event/v2" && event.ownerBinding ? <div className="timeline-owner-snapshot"><Box size={15} /><strong>归属快照</strong><span>{"artifact" in event.ownerBinding ? `产物修订版本 ${shortId(event.ownerBinding.artifact.revisionId)}` : `复习卡 ${shortId(event.ownerBinding.reviewCard.cardId)} · ${reviewCardStatusLabels[event.ownerBinding.reviewCard.status]}`}</span></div> : null}</section>;

const TimelineEventWorkbench = ({ workspaceId, eventId }: { workspaceId: string; eventId: string }) => {
  const event = useTimelineEvent(eventId);
  const analyze = useAnalyzeImpact();
  const [reportId, setReportId] = useState("");
  const report = useImpactReport(reportId);
  const startAnalysis = (reuse = false): void => {
    const variables = reuse && analyze.variables !== undefined ? analyze.variables : { workspaceId, eventId, idempotencyKey: newKey("impact-analysis") };
    analyze.mutate(variables, { onSuccess: (result) => setReportId(result.report.id) });
  };

  if (workspaceId === "") return <div className="timeline-workbench"><UnavailableState title="先连接 Workspace" description="事件详情不能跨 Workspace 读取。" /><Link className="ui-button ui-button--primary" to="/workspace">连接 Workspace</Link></div>;
  if (event.isPending) return <div className="timeline-workbench"><div className="timeline-loading"><Activity size={18} />正在读取事件详情…</div></div>;
  if (event.isError) return <div className="timeline-workbench"><Link className="timeline-back" to="/timeline"><ArrowLeft size={15} />返回时间线</Link><Failure error={event.error} title={event.error instanceof TimelineApiError && event.error.status === 404 ? "事件不存在" : "事件详情读取失败"} onRetry={() => void event.refetch()} /></div>;
  const analysisError = analyze.isError ? analyze.error : null;
  return <div className="timeline-workbench"><Link className="timeline-back" to="/timeline"><ArrowLeft size={15} />返回时间线</Link><EventDetail event={event.data} /><section className="timeline-analysis-command"><div><span><CalendarClock size={15} />影响分析</span><h3>分析当前事件的下游影响</h3><p>分析只生成不可变报告；不会修改知识、产物、复习卡、文件或 Git。</p></div><Button onClick={() => startAnalysis()} disabled={analyze.isPending}><Sparkles size={16} />{analyze.isPending ? "正在分析" : "发起分析"}</Button></section>
    {analysisError ? <div className="timeline-command-error" role="alert"><strong>{analysisError instanceof TimelineApiError && analysisError.status === 409 ? "影响分析版本冲突" : analysisError instanceof TimelineApiError && analysisError.status === 503 ? "影响分析暂不可用" : "影响分析请求未完成"}</strong><p>{errorDescription(analysisError)}</p><div>{isUnknownResult(analysisError) ? <Button variant="secondary" onClick={() => startAnalysis(true)}>重试原分析请求</Button> : null}<Button variant="ghost" onClick={() => startAnalysis(false)}>发起新的分析意图</Button></div></div> : null}
    {analyze.data ? <p className="timeline-command-success" role="status">{analyze.data.replayed ? "已恢复既有分析报告。" : "新的影响报告已持久化。"}</p> : null}
    {reportId !== "" ? report.isPending ? <div className="timeline-loading"><Activity size={18} />正在读取影响报告…</div> : report.isError ? <Failure error={report.error} title="影响报告读取失败" onRetry={() => void report.refetch()} /> : <ImpactReportView key={`${report.data.workspaceId}:${report.data.id}`} report={report.data} /> : null}
  </div>;
};

export const TimelineEventPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const eventId = useParams().eventId ?? "";
  return <TimelineEventWorkbench key={`${workspaceId}:${eventId}`} workspaceId={workspaceId} eventId={eventId} />;
};
