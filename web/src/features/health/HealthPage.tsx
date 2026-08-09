import { AlertTriangle, ArrowRight, Eye, HeartPulse, RefreshCw, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState, type RefObject } from "react";
import { Link, useSearchParams } from "react-router-dom";

import {
  HealthApiError,
  workspaceHealthScope,
  type HealthDecision,
  type HealthDecisionRecordAction,
  type HealthCoverageStatus,
  type HealthIssue,
  type HealthIssueListItem,
  type HealthIssueStatus,
  type HealthIssueType,
  type HealthObjectType,
  type HealthObservation,
  type HealthScanStatus,
  type HealthSeverity,
  type HealthTrendPoint,
} from "../../api/health";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, Dialog, EmptyState, ErrorState, Tabs, TabsContent, TabsList, TabsTrigger, UnavailableState } from "../../shared/ui";
import { useClearHealthIssueDetail, useDecideHealthIssue, useHealthIssue, useHealthIssueDecisions, useHealthIssueObservations, useHealthIssues, useHealthScan, useHealthSummary, useStartHealthScan } from "./queries";
import { parseHealthUrlState, writeHealthUrlState, type HealthUrlState } from "./url-state";

const issueStatuses: HealthIssueStatus[] = ["OPEN", "REOPENED", "ACKNOWLEDGED", "DEFERRED", "PROPOSAL_CREATED", "IGNORED", "FALSE_POSITIVE", "RESOLVED"];
const severities: HealthSeverity[] = ["CRITICAL", "HIGH", "MEDIUM", "LOW"];
const issueTypes: HealthIssueType[] = ["ORPHAN", "DUPLICATE", "CONFLICT", "STALE", "MISSING_SOURCE", "LOW_CONFIDENCE", "BROKEN_REFERENCE", "INDEX_ERROR", "SUPERSEDED_USAGE", "REVIEW_INVALIDATED"];
const formatDate = (value: string | null): string => value === null ? "—" : new Date(value).toLocaleString("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const newKey = (prefix: string): string => `${prefix}-${crypto.randomUUID()}`;
const severityTone = (severity: HealthSeverity) => severity === "CRITICAL" || severity === "HIGH" ? "danger" : severity === "MEDIUM" ? "warning" : "info";
const statusTone = (status: HealthIssueStatus) => status === "OPEN" || status === "REOPENED" ? "danger" : status === "RESOLVED" ? "success" : "warning";
const severityLabels: Record<HealthSeverity, string> = { CRITICAL: "严重", HIGH: "高", MEDIUM: "中", LOW: "低" };
const issueStatusLabels: Record<HealthIssueStatus, string> = { OPEN: "待处理", REOPENED: "再次打开", ACKNOWLEDGED: "已确认", DEFERRED: "已暂缓", PROPOSAL_CREATED: "已创建提案", IGNORED: "已忽略", FALSE_POSITIVE: "误报", RESOLVED: "已解决" };
const issueTypeLabels: Record<HealthIssueType, string> = { ORPHAN: "孤立对象", DUPLICATE: "重复内容", CONFLICT: "内容冲突", STALE: "内容过期", MISSING_SOURCE: "缺少来源", LOW_CONFIDENCE: "置信度低", BROKEN_REFERENCE: "引用失效", INDEX_ERROR: "索引错误", SUPERSEDED_USAGE: "使用已替代内容", REVIEW_INVALIDATED: "复习记录失效" };
const scanStatusLabels: Record<HealthScanStatus, string> = { PENDING: "等待处理", RUNNING: "正在扫描", SUCCEEDED: "扫描完成", PARTIAL: "部分完成", FAILED: "扫描失败", CANCELLED: "已取消" };
const coverageStatusLabels: Record<HealthCoverageStatus, string> = { ...scanStatusLabels, UNAVAILABLE: "不可用" };
const objectTypeLabels: Record<HealthObjectType, string> = { TOPIC: "主题", CLAIM: "知识点", RELATION: "关系", CONFLICT: "冲突", SOURCE_VERSION: "资料版本", INDEX_VERSION: "索引版本" };
const decisionActionLabels: Record<HealthDecisionRecordAction, string> = { ACKNOWLEDGE: "确认", IGNORE: "忽略", FALSE_POSITIVE: "标记为误报", DEFER: "暂缓", CREATE_REPAIR_PROPOSAL: "创建修复提案" };

interface IdempotentAttempt { signature: string; key: string }
type IssueDetailTab = "current" | "observations" | "decisions";

const attemptKey = (attempt: { current: IdempotentAttempt | null }, prefix: string, signature: string): string => {
  if (attempt.current?.signature !== signature) attempt.current = { signature, key: newKey(prefix) };
  return attempt.current.key;
};

const isIssueDetailTab = (value: string): value is IssueDetailTab => value === "current" || value === "observations" || value === "decisions";
const uniqueHistoryItems = <T extends { id: string }>(pages: { items: T[] }[] | undefined): T[] => {
  const seen = new Set<string>();
  return (pages ?? []).flatMap((page) => page.items).filter((item) => {
    if (seen.has(item.id)) return false;
    seen.add(item.id);
    return true;
  });
};

const StateMessage = ({ title, description }: { title: string; description: string }) => <div className="ui-state" role="status"><strong>{title}</strong><p>{description}</p></div>;
const updateState = (state: HealthUrlState, params: URLSearchParams, setParams: ReturnType<typeof useSearchParams>[1]) => setParams(writeHealthUrlState(state, params), { replace: true });

const TrendList = ({ trend }: { trend: HealthTrendPoint[] }) => <div className="health-trend-list" aria-label="最近 7 天健康趋势">{trend.map((point) => <div key={point.date} className="health-trend-point"><time dateTime={point.date}>{point.date.slice(5)}</time><strong>{String(point.detectedCount)}</strong><small>新增</small><strong>{String(point.resolvedCount)}</strong><small>已解决</small></div>)}</div>;

const CoverageList = ({ coverage }: { coverage: { detectorId: string; detectorVersion: string; status: HealthCoverageStatus; counters: { processed: number; failed: number }; unavailableReason: string | null; lastError: { code: string; retryable: boolean } | null }[] }) => <div className="health-coverage health-coverage-list">{coverage.map((item) => <div className={`health-coverage__item health-coverage__item--${item.status.toLowerCase()}`} key={`${item.detectorId}:${item.detectorVersion}`}><div><strong>{item.detectorId}</strong><small>{item.detectorVersion}</small></div><Badge tone={item.status === "SUCCEEDED" ? "success" : item.status === "UNAVAILABLE" || item.status === "PARTIAL" ? "warning" : item.status === "FAILED" ? "danger" : "info"}>{coverageStatusLabels[item.status]}</Badge><span>{String(item.counters.processed)} 已处理 · {String(item.counters.failed)} 失败</span>{item.unavailableReason ? <p>{item.unavailableReason}</p> : item.lastError ? <p>{item.lastError.code}{item.lastError.retryable ? " · 可重试" : ""}</p> : null}</div>)}</div>;

const HealthOverview = ({ state, onChange }: { state: HealthUrlState; onChange: (state: HealthUrlState) => void }) => {
  const summary = useHealthSummary();
  const startScan = useStartHealthScan();
  const workspaceId = useActiveWorkspaceId();
  const key = useRef(newKey("health-scan"));
  const startWorkspaceScan = () => {
    startScan.mutate({ scope: workspaceHealthScope(workspaceId), maxItems: 5000, preventScopeConcurrency: true, idempotencyKey: key.current }, {
      onSuccess: (accepted) => { key.current = newKey("health-scan"); onChange({ ...state, scanId: accepted.healthScanId }); },
    });
  };

  if (summary.isPending) return <Card><StateMessage title="正在读取健康概览" description="读取待处理数量、严重度分布、真实趋势和最近一次扫描。" /></Card>;
  if (summary.isError) return <ErrorState title="健康概览不可用" description={summary.error.message} onRetry={() => void summary.refetch()} />;
  const data = summary.data;
  return <div className="health-overview health-metrics">
    <Card className="health-overview__hero"><CardHeader eyebrow="知识健康" title={`${String(data.openCount)} 个待处理问题`} description={data.lastScan ? `最近扫描 ${formatDate(data.lastScan.updatedAt)} · ${scanStatusLabels[data.lastScan.status]}` : "还没有扫描记录。"} action={<Button onClick={startWorkspaceScan} disabled={startScan.isPending}><HeartPulse size={15} />{startScan.isPending ? "正在启动…" : "启动扫描"}</Button>} />
      <div className="health-severity-grid">{severities.map((severity) => <button key={severity} type="button" className={`health-severity health-severity--${severity.toLowerCase()}`} onClick={() => onChange({ ...state, severity })}><span>{severityLabels[severity]}</span><strong>{data.openBySeverity[severity]}</strong></button>)}</div>{startScan.isError ? <p role="alert" className="form-error">{startScan.error.message}</p> : null}</Card>
    <Card><CardHeader eyebrow="趋势" title="最近 7 天新增与已解决" description="来自已结束健康扫描的持久化计数；缺失日期按 0 展示。" /><TrendList trend={data.trend} /></Card>
    <Card><CardHeader eyebrow="覆盖情况" title="最近扫描覆盖" description={data.lastScan ? `${String(data.lastScan.counters.processed)} 已处理 · ${scanStatusLabels[data.lastScan.status]}` : "等待首次扫描"} />{data.lastScan ? <CoverageList coverage={data.lastScan.coverage} /> : <EmptyState title="暂无覆盖信息" description="启动扫描后会显示检测器覆盖和部分完成状态。" />}</Card>
    {data.unavailable.length > 0 ? <Card><CardHeader eyebrow="覆盖情况" title="未启用检查项" /><div className="capability-list">{data.unavailable.map((item) => <div key={item.code}><span>{item.code}</span><Badge tone="warning">{item.reason}</Badge></div>)}</div></Card> : null}
  </div>;
};

const ScanRecovery = ({ scanId }: { scanId: string }) => {
  const scan = useHealthScan(scanId);
  if (scanId === "") return null;
  if (scan.isPending) return <Card><StateMessage title="正在恢复扫描" description="根据 URL 中的 scan ID 读取持久化状态。" /></Card>;
  if (scan.isError) return <ErrorState title="扫描状态不可用" description={scan.error.message} onRetry={() => void scan.refetch()} />;
  return <Card className="scan-recovery"><CardHeader eyebrow="扫描恢复" title={scanStatusLabels[scan.data.status]} description={`${String(scan.data.counters.processed)} 已处理 · ${String(scan.data.counters.created)} 新发现 · ${String(scan.data.counters.failed)} 失败`} action={<Button variant="ghost" onClick={() => void scan.refetch()}><RefreshCw size={15} />刷新</Button>} /><CoverageList coverage={scan.data.coverage} />{scan.data.lastError ? <p role="alert" className="form-error">{scan.data.lastError.stage}: {scan.data.lastError.code}{scan.data.lastError.retryable ? " · 可重试" : ""}</p> : null}</Card>;
};

const IssueFilters = ({ state, onChange }: { state: HealthUrlState; onChange: (state: HealthUrlState) => void }) => <Card><CardHeader eyebrow="问题筛选" title="问题列表" /><div className="filter-bar"><label>状态<select value={state.status} onChange={(event) => onChange({ ...state, status: event.target.value as HealthIssueStatus | "" })}><option value="">全部状态</option>{issueStatuses.map((status) => <option key={status} value={status}>{issueStatusLabels[status]}</option>)}</select></label><label>严重度<select value={state.severity} onChange={(event) => onChange({ ...state, severity: event.target.value as HealthSeverity | "" })}><option value="">全部严重度</option>{severities.map((severity) => <option key={severity} value={severity}>{severityLabels[severity]}</option>)}</select></label><label>类型<select value={state.type} onChange={(event) => onChange({ ...state, type: event.target.value as HealthIssueType | "" })}><option value="">全部类型</option>{issueTypes.map((type) => <option key={type} value={type}>{issueTypeLabels[type]}</option>)}</select></label></div></Card>;

const IssueRow = ({ issue, selected, onSelect }: { issue: HealthIssueListItem; selected: boolean; onSelect: (button: HTMLButtonElement) => void }) => <article className={selected ? "health-issue-row health-issue-row--selected" : "health-issue-row"}><div className="health-issue-row__marker"><AlertTriangle size={18} /></div><div><h3>{issueTypeLabels[issue.type]}</h3><p>{issue.evidenceSummary || "没有摘要"}</p><div className="collection-item__meta"><Badge tone={severityTone(issue.severity)}>{severityLabels[issue.severity]}</Badge><Badge tone={statusTone(issue.status)}>{issueStatusLabels[issue.status]}</Badge><span>{objectTypeLabels[issue.target.type]}:{issue.target.id.slice(0, 8)}…</span><time>{formatDate(issue.updatedAt)}</time></div></div><Button variant={selected ? "primary" : "secondary"} onClick={(event) => onSelect(event.currentTarget)}><Eye size={15} />查看证据</Button></article>;

const IssueList = ({ state, onChange, returnFocus }: { state: HealthUrlState; onChange: (state: HealthUrlState) => void; returnFocus: RefObject<HTMLButtonElement | null> }) => {
  const workspaceId = useActiveWorkspaceId();
  const issueScope = `${workspaceId}:${state.status}:${state.severity}:${state.type}`;
  const [issuePage, setIssuePage] = useState<{ scope: string; cursor?: string }>({ scope: issueScope });
  const cursor = issuePage.scope === issueScope ? issuePage.cursor : undefined;
  useEffect(() => setIssuePage({ scope: issueScope }), [issueScope]);
  const issues = useHealthIssues({ ...(state.status ? { statuses: [state.status] } : {}), ...(state.severity ? { severities: [state.severity] } : {}), ...(state.type ? { types: [state.type] } : {}), ...(cursor ? { cursor } : {}), limit: 25 });
  if (issues.isPending) return <Card><StateMessage title="正在读取问题" description="读取当前 Workspace 的问题列表。" /></Card>;
  if (issues.isError) return <ErrorState title="问题列表不可用" description={issues.error.message} onRetry={() => void issues.refetch()} />;
  return <Card>{issues.data.items.length === 0 ? <EmptyState title="没有匹配问题" description="当前筛选没有待处理问题；不可用检测器仍在覆盖情况中显示。" /> : <div className="health-issue-list">{issues.data.items.map((issue) => <IssueRow key={issue.id} issue={issue} selected={state.issueId === issue.id} onSelect={(button) => { returnFocus.current = button; onChange({ ...state, issueId: issue.id }); }} />)}</div>}<div className="pagination-row">{cursor !== undefined ? <Button variant="ghost" onClick={() => setIssuePage({ scope: issueScope })}>回到首屏</Button> : <span />}{issues.data.nextCursor ? <Button variant="secondary" onClick={() => { const nextCursor = issues.data.nextCursor; if (nextCursor) setIssuePage({ scope: issueScope, cursor: nextCursor }); }}>下一页<ArrowRight size={15} /></Button> : null}</div></Card>;
};

const IssueDetail = ({ issue }: { issue: HealthIssue }) => <div className="health-issue-detail"><div><Badge tone={severityTone(issue.severity)}>{severityLabels[issue.severity]}</Badge><Badge tone={statusTone(issue.status)}>{issueStatusLabels[issue.status]}</Badge></div><h3>{issueTypeLabels[issue.type]}</h3><p>{issue.evidenceSummary}</p><dl className="detail-grid"><div><dt>目标</dt><dd>{objectTypeLabels[issue.target.type]}:{issue.target.id}</dd></div><div><dt>检测器</dt><dd>{issue.detectorId}@{issue.detectorVersion}</dd></div><div><dt>指纹</dt><dd><code>{issue.fingerprint.slice(0, 16)}…</code></dd></div><div><dt>版本</dt><dd>{issue.version}</dd></div></dl></div>;

const ObservationRecord = ({ observation }: { observation: HealthObservation }) => <article className="health-history-record">
  <header><div><strong>{formatDate(observation.observedAt)}</strong><small>{observation.detectorVersion}</small></div><Badge tone={severityTone(observation.severity)}>{severityLabels[observation.severity]}</Badge></header>
  <dl className="health-history-meta"><div><dt>问题版本</dt><dd>{observation.issueVersion}</dd></div><div><dt>扫描</dt><dd><code>{observation.scanId.slice(0, 8)}…</code></dd></div></dl>
  {observation.evidence.length === 0 ? <p className="sidebar-note">该次观测没有证据。</p> : <ul className="health-history-evidence">{observation.evidence.map((evidence) => <li key={`${evidence.ref.type}:${evidence.ref.id}:${evidence.hash}`}><strong>{objectTypeLabels[evidence.ref.type]}:{evidence.ref.id.slice(0, 8)}…</strong><span>{evidence.summary || "—"}</span></li>)}</ul>}
</article>;

const DecisionRecord = ({ decision }: { decision: HealthDecision }) => <article className="health-history-record">
  <header><div><strong>{decisionActionLabels[decision.action]}</strong><small>{formatDate(decision.createdAt)}</small></div><Badge tone="info">v{decision.issueVersion}</Badge></header>
  <p>{decision.reason || "没有填写原因"}</p>
  {decision.deferredUntil ? <small>暂缓至 {formatDate(decision.deferredUntil)}</small> : null}
  {decision.proposalId ? <small>提案 {decision.proposalId}</small> : null}
</article>;

const ObservationHistory = ({ query }: { query: ReturnType<typeof useHealthIssueObservations> }) => {
  if (query.isPending) return <StateMessage title="正在读取观测历史" description="读取该问题的观测记录。" />;
  if (query.isError && query.data === undefined) return <ErrorState title="观测历史不可用" description={query.error.message} onRetry={() => void query.refetch()} />;
  const items = uniqueHistoryItems(query.data.pages);
  return <div className="health-history-panel">
    {items.length === 0 ? <EmptyState title="没有观测历史" description="该问题尚未产生观测记录。" /> : <div className="health-history-list">{items.map((observation) => <ObservationRecord key={observation.id} observation={observation} />)}</div>}
    {query.isFetchNextPageError ? <div className="ui-state ui-state--error" role="alert"><strong>加载更多观测失败</strong><p>{query.error.message}</p><Button variant="secondary" onClick={() => void query.fetchNextPage()} disabled={query.isFetchingNextPage}>重试加载更多观测</Button></div> : null}
    {query.hasNextPage && !query.isFetchNextPageError ? <div className="pagination-row"><span /><Button variant="secondary" onClick={() => void query.fetchNextPage()} disabled={query.isFetchingNextPage}>{query.isFetchingNextPage ? "加载中…" : "加载更多观测"}</Button></div> : null}
  </div>;
};

const DecisionHistory = ({ query }: { query: ReturnType<typeof useHealthIssueDecisions> }) => {
  if (query.isPending) return <StateMessage title="正在读取决策历史" description="读取该问题的决策记录。" />;
  if (query.isError && query.data === undefined) return <ErrorState title="决策历史不可用" description={query.error.message} onRetry={() => void query.refetch()} />;
  const items = uniqueHistoryItems(query.data.pages);
  return <div className="health-history-panel">
    {items.length === 0 ? <EmptyState title="没有决策历史" description="该问题尚未产生决策记录。" /> : <div className="health-history-list">{items.map((decision) => <DecisionRecord key={decision.id} decision={decision} />)}</div>}
    {query.isFetchNextPageError ? <div className="ui-state ui-state--error" role="alert"><strong>加载更多决策失败</strong><p>{query.error.message}</p><Button variant="secondary" onClick={() => void query.fetchNextPage()} disabled={query.isFetchingNextPage}>重试加载更多决策</Button></div> : null}
    {query.hasNextPage && !query.isFetchNextPageError ? <div className="pagination-row"><span /><Button variant="secondary" onClick={() => void query.fetchNextPage()} disabled={query.isFetchingNextPage}>{query.isFetchingNextPage ? "加载中…" : "加载更多决策"}</Button></div> : null}
  </div>;
};

const EvidenceDialog = ({ issueId, onClose, returnFocus }: { issueId: string; onClose: () => void; returnFocus: RefObject<HTMLButtonElement | null> }) => {
  const detail = useHealthIssue(issueId);
  const decide = useDecideHealthIssue();
  const clearIssueDetail = useClearHealthIssueDetail();
  const [tabState, setTabState] = useState<{ issueId: string; tab: IssueDetailTab }>({ issueId, tab: "current" });
  const activeTab = tabState.issueId === issueId ? tabState.tab : "current";
  const [reason, setReason] = useState("");
  const [deferredUntil, setDeferredUntil] = useState("");
  const decisionAttempt = useRef<IdempotentAttempt | null>(null);
  const observations = useHealthIssueObservations(issueId, activeTab === "observations");
  const decisions = useHealthIssueDecisions(issueId, activeTab === "decisions");
  useEffect(() => {
    setReason("");
    setDeferredUntil("");
    decisionAttempt.current = null;
  }, [issueId]);
  const submitDecision = (issue: HealthIssue, action: "ACKNOWLEDGE" | "IGNORE" | "FALSE_POSITIVE" | "DEFER") => {
    const payload = { issueId: issue.id, expectedVersion: issue.version, action, ...(reason.trim() ? { reason: reason.trim() } : {}), ...(action === "DEFER" && deferredUntil ? { deferredUntil: new Date(deferredUntil).toISOString() } : {}) };
    const signature = JSON.stringify(payload);
    decide.mutate({ ...payload, idempotencyKey: attemptKey(decisionAttempt, "health-decision", signature) }, { onSuccess: () => { decisionAttempt.current = null; setReason(""); setDeferredUntil(""); void detail.refetch(); } });
  };
  const close = () => {
    const closingIssueId = issueId;
    setTabState({ issueId: "", tab: "current" });
    setReason("");
    setDeferredUntil("");
    decisionAttempt.current = null;
    clearIssueDetail(closingIssueId);
    onClose();
  };
  const decisionError = decide.isError
    ? decide.error instanceof HealthApiError && decide.error.status === 409
      ? "问题已被其他操作更新，请关闭后重新打开并基于最新版本重试。"
      : `${decide.error.message}${decide.error instanceof HealthApiError && decide.error.retryable ? "（可重试；相同操作将复用幂等键。）" : ""}`
    : "";

  return <Dialog open={issueId !== ""} onOpenChange={(open) => { if (!open) close(); }} restoreFocusRef={returnFocus} contentClassName="health-evidence-dialog" title="问题详情" description="查看当前证据、观测记录和决策记录。">
    {detail.isPending ? <StateMessage title="正在读取证据" description="读取问题当前证据与可用修复选项。" /> : detail.isError ? <ErrorState title="证据不可用" description={detail.error.message} onRetry={() => void detail.refetch()} /> : <div className="health-evidence-detail">
      <IssueDetail issue={detail.data.issue} />
      <Tabs value={activeTab} onValueChange={(value) => { if (isIssueDetailTab(value)) setTabState({ issueId, tab: value }); }}>
      <TabsList aria-label="问题详情分组"><TabsTrigger value="current">当前证据</TabsTrigger><TabsTrigger value="observations">观测历史</TabsTrigger><TabsTrigger value="decisions">决策历史</TabsTrigger></TabsList>
        <TabsContent value="current">
          <div className="health-evidence-list evidence-list">
            {detail.data.issue.evidence.length === 0 ? <EmptyState title="没有证据" description="该问题当前没有可展示的证据摘要。" /> : detail.data.issue.evidence.map((evidence) => <article key={`${evidence.ref.type}:${evidence.ref.id}:${evidence.hash}`}><strong>{objectTypeLabels[evidence.ref.type]}:{evidence.ref.id.slice(0, 8)}…</strong><p>{evidence.summary || "—"}</p><code>{evidence.hash.slice(0, 16)}…</code></article>)}
          </div>
          <div className="health-decision-panel health-decision-form">
            <label>原因<textarea value={reason} onChange={(event) => setReason(event.target.value)} rows={2} /></label>
            <label>暂缓到<input value={deferredUntil} onChange={(event) => setDeferredUntil(event.target.value)} type="datetime-local" /></label>
            <div className="button-row"><Button variant="secondary" disabled={decide.isPending} onClick={() => submitDecision(detail.data.issue, "ACKNOWLEDGE")}><ShieldCheck size={15} />确认</Button><Button variant="secondary" disabled={decide.isPending || reason.trim() === ""} onClick={() => submitDecision(detail.data.issue, "IGNORE")}>忽略</Button><Button variant="secondary" disabled={decide.isPending || reason.trim() === ""} onClick={() => submitDecision(detail.data.issue, "FALSE_POSITIVE")}>误报</Button><Button variant="secondary" disabled={decide.isPending || reason.trim() === "" || deferredUntil === ""} onClick={() => submitDecision(detail.data.issue, "DEFER")}>暂缓</Button></div>
            {decisionError ? <p role="alert" className="form-error">{decisionError}</p> : null}
          </div>
          <div className="health-repair-options repair-options">
            <h3>修复选项（不可用）</h3>
            {detail.data.issue.repairOptions.length === 0 ? <p className="sidebar-note">修复提案端点尚未启用；知识健康不会直接修改正式知识。</p> : detail.data.issue.repairOptions.map((option) => <div key={option.code}><div><strong>{option.title}</strong><small>{option.unavailableReason ?? "修复提案端点尚未启用"}</small></div><Button variant="ghost" disabled>创建提案（不可用）</Button></div>)}
          </div>
        </TabsContent>
        <TabsContent value="observations"><ObservationHistory query={observations} /></TabsContent>
        <TabsContent value="decisions"><DecisionHistory query={decisions} /></TabsContent>
      </Tabs>
    </div>}
  </Dialog>;
};

export const HealthPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params, setParams] = useSearchParams();
  const state = parseHealthUrlState(params);
  const returnFocus = useRef<HTMLButtonElement>(null);
  const onChange = (next: HealthUrlState) => updateState(next, params, setParams);
  if (!workspaceId) return <div className="page-stack"><UnavailableState title="先连接 Workspace" description="知识健康只能读取当前 Workspace 的持久化问题与扫描。" /><Link className="ui-button ui-button--primary" to="/settings">前往设置</Link></div>;
  return <div className="page-stack"><div className="page-intro page-intro--split"><div><h1>知识健康</h1><p>发现并处理知识质量问题。</p></div><div className="folio-mark"><HeartPulse size={20} /><strong>健康</strong><span>知识</span></div></div><HealthOverview state={state} onChange={onChange} /><ScanRecovery scanId={state.scanId} /><IssueFilters state={state} onChange={onChange} /><IssueList state={state} onChange={onChange} returnFocus={returnFocus} /><EvidenceDialog issueId={state.issueId} onClose={() => onChange({ ...state, issueId: "" })} returnFocus={returnFocus} /></div>;
};
