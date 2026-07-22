import { AlertTriangle, ArrowRight, Eye, HeartPulse, RefreshCw, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState, type RefObject } from "react";
import { Link, useSearchParams } from "react-router-dom";

import {
  HealthApiError,
  workspaceHealthScope,
  type HealthIssue,
  type HealthIssueListItem,
  type HealthIssueStatus,
  type HealthIssueType,
  type HealthSeverity,
  type HealthTrendPoint,
} from "../../api/health";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, Dialog, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { useClearHealthIssueDetail, useDecideHealthIssue, useHealthIssue, useHealthIssues, useHealthScan, useHealthSummary, useStartHealthScan } from "./queries";
import { parseHealthUrlState, writeHealthUrlState, type HealthUrlState } from "./url-state";

const issueStatuses: HealthIssueStatus[] = ["OPEN", "REOPENED", "ACKNOWLEDGED", "DEFERRED", "PROPOSAL_CREATED", "IGNORED", "FALSE_POSITIVE", "RESOLVED"];
const severities: HealthSeverity[] = ["CRITICAL", "HIGH", "MEDIUM", "LOW"];
const issueTypes: HealthIssueType[] = ["ORPHAN", "DUPLICATE", "CONFLICT", "STALE", "MISSING_SOURCE", "LOW_CONFIDENCE", "BROKEN_REFERENCE", "INDEX_ERROR", "SUPERSEDED_USAGE", "REVIEW_INVALIDATED"];
const formatDate = (value: string | null): string => value === null ? "—" : new Date(value).toLocaleString("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const newKey = (prefix: string): string => `${prefix}-${crypto.randomUUID()}`;
const severityTone = (severity: HealthSeverity) => severity === "CRITICAL" || severity === "HIGH" ? "danger" : severity === "MEDIUM" ? "warning" : "info";
const statusTone = (status: HealthIssueStatus) => status === "OPEN" || status === "REOPENED" ? "danger" : status === "RESOLVED" ? "success" : "warning";

interface IdempotentAttempt { signature: string; key: string }

const attemptKey = (attempt: { current: IdempotentAttempt | null }, prefix: string, signature: string): string => {
  if (attempt.current?.signature !== signature) attempt.current = { signature, key: newKey(prefix) };
  return attempt.current.key;
};

const StateMessage = ({ title, description }: { title: string; description: string }) => <div className="ui-state" role="status"><strong>{title}</strong><p>{description}</p></div>;
const updateState = (state: HealthUrlState, params: URLSearchParams, setParams: ReturnType<typeof useSearchParams>[1]) => setParams(writeHealthUrlState(state, params), { replace: true });

const TrendList = ({ trend }: { trend: HealthTrendPoint[] }) => <div className="health-trend-list" aria-label="最近 7 天 Health 趋势">{trend.map((point) => <div key={point.date} className="health-trend-point"><time dateTime={point.date}>{point.date.slice(5)}</time><strong>{String(point.detectedCount)}</strong><small>新增</small><strong>{String(point.resolvedCount)}</strong><small>已解决</small></div>)}</div>;

const CoverageList = ({ coverage }: { coverage: { detectorId: string; detectorVersion: string; status: string; counters: { processed: number; failed: number }; unavailableReason: string | null; lastError: { code: string; retryable: boolean } | null }[] }) => <div className="health-coverage health-coverage-list">{coverage.map((item) => <div className={`health-coverage__item health-coverage__item--${item.status.toLowerCase()}`} key={`${item.detectorId}:${item.detectorVersion}`}><div><strong>{item.detectorId}</strong><small>{item.detectorVersion}</small></div><Badge tone={item.status === "SUCCEEDED" ? "success" : item.status === "UNAVAILABLE" || item.status === "PARTIAL" ? "warning" : item.status === "FAILED" ? "danger" : "info"}>{item.status}</Badge><span>{String(item.counters.processed)} 已处理 · {String(item.counters.failed)} 失败</span>{item.unavailableReason ? <p>{item.unavailableReason}</p> : item.lastError ? <p>{item.lastError.code}{item.lastError.retryable ? " · 可重试" : ""}</p> : null}</div>)}</div>;

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

  if (summary.isPending) return <Card><StateMessage title="正在读取健康概览" description="读取 open count、severity 分布、真实趋势和最近一次扫描。" /></Card>;
  if (summary.isError) return <ErrorState title="健康概览不可用" description={summary.error.message} onRetry={() => void summary.refetch()} />;
  const data = summary.data;
  return <div className="health-overview health-metrics">
    <Card className="health-overview__hero"><CardHeader eyebrow="Knowledge Health" title={`${String(data.openCount)} 个打开 Issue`} description={data.lastScan ? `最近扫描 ${formatDate(data.lastScan.updatedAt)} · ${data.lastScan.status}` : "还没有扫描记录。"} action={<Button onClick={startWorkspaceScan} disabled={startScan.isPending}><HeartPulse size={15} />{startScan.isPending ? "正在启动…" : "启动扫描"}</Button>} />
      <div className="health-severity-grid">{severities.map((severity) => <button key={severity} type="button" className={`health-severity health-severity--${severity.toLowerCase()}`} onClick={() => onChange({ ...state, severity })}><span>{severity}</span><strong>{data.openBySeverity[severity]}</strong></button>)}</div>{startScan.isError ? <p role="alert" className="form-error">{startScan.error.message}</p> : null}</Card>
    <Card><CardHeader eyebrow="趋势" title="最近 7 天新增与已解决" description="来自已终止 Health Scan 的持久化计数；缺失日期按 0 展示。" /><TrendList trend={data.trend} /></Card>
    <Card><CardHeader eyebrow="Coverage" title="最近扫描覆盖" description={data.lastScan ? `${String(data.lastScan.counters.processed)} 已处理 · ${data.lastScan.status}` : "等待首次扫描"} />{data.lastScan ? <CoverageList coverage={data.lastScan.coverage} /> : <EmptyState title="暂无覆盖信息" description="启动扫描后会显示 detector 覆盖和部分完成状态。" />}</Card>
    {data.unavailable.length > 0 ? <Card><CardHeader eyebrow="Coverage" title="未启用检查项" /><div className="capability-list">{data.unavailable.map((item) => <div key={item.code}><span>{item.code}</span><Badge tone="warning">{item.reason}</Badge></div>)}</div></Card> : null}
  </div>;
};

const ScanRecovery = ({ scanId }: { scanId: string }) => {
  const scan = useHealthScan(scanId);
  if (scanId === "") return null;
  if (scan.isPending) return <Card><StateMessage title="正在恢复扫描" description="根据 URL 中的 scan ID 读取持久化状态。" /></Card>;
  if (scan.isError) return <ErrorState title="扫描状态不可用" description={scan.error.message} onRetry={() => void scan.refetch()} />;
  return <Card className="scan-recovery"><CardHeader eyebrow="Scan recovery" title={`Scan ${scan.data.status}`} description={`${String(scan.data.counters.processed)} 已处理 · ${String(scan.data.counters.created)} 新发现 · ${String(scan.data.counters.failed)} 失败`} action={<Button variant="ghost" onClick={() => void scan.refetch()}><RefreshCw size={15} />刷新</Button>} /><CoverageList coverage={scan.data.coverage} />{scan.data.lastError ? <p role="alert" className="form-error">{scan.data.lastError.stage}: {scan.data.lastError.code}{scan.data.lastError.retryable ? " · 可重试" : ""}</p> : null}</Card>;
};

const IssueFilters = ({ state, onChange }: { state: HealthUrlState; onChange: (state: HealthUrlState) => void }) => <Card><CardHeader eyebrow="Issue filters" title="Issue 列表" /><div className="filter-bar"><label>状态<select value={state.status} onChange={(event) => onChange({ ...state, status: event.target.value as HealthIssueStatus | "" })}><option value="">全部状态</option>{issueStatuses.map((status) => <option key={status}>{status}</option>)}</select></label><label>严重度<select value={state.severity} onChange={(event) => onChange({ ...state, severity: event.target.value as HealthSeverity | "" })}><option value="">全部严重度</option>{severities.map((severity) => <option key={severity}>{severity}</option>)}</select></label><label>类型<select value={state.type} onChange={(event) => onChange({ ...state, type: event.target.value as HealthIssueType | "" })}><option value="">全部类型</option>{issueTypes.map((type) => <option key={type}>{type}</option>)}</select></label></div></Card>;

const IssueRow = ({ issue, selected, onSelect }: { issue: HealthIssueListItem; selected: boolean; onSelect: (button: HTMLButtonElement) => void }) => <article className={selected ? "health-issue-row health-issue-row--selected" : "health-issue-row"}><div className="health-issue-row__marker"><AlertTriangle size={18} /></div><div><h3>{issue.type}</h3><p>{issue.evidenceSummary || "没有摘要"}</p><div className="collection-item__meta"><Badge tone={severityTone(issue.severity)}>{issue.severity}</Badge><Badge tone={statusTone(issue.status)}>{issue.status}</Badge><span>{issue.target.type}:{issue.target.id.slice(0, 8)}…</span><time>{formatDate(issue.updatedAt)}</time></div></div><Button variant={selected ? "primary" : "secondary"} onClick={(event) => onSelect(event.currentTarget)}><Eye size={15} />Evidence</Button></article>;

const IssueList = ({ state, onChange, returnFocus }: { state: HealthUrlState; onChange: (state: HealthUrlState) => void; returnFocus: RefObject<HTMLButtonElement | null> }) => {
  const workspaceId = useActiveWorkspaceId();
  const issueScope = `${workspaceId}:${state.status}:${state.severity}:${state.type}`;
  const [issuePage, setIssuePage] = useState<{ scope: string; cursor?: string }>({ scope: issueScope });
  const cursor = issuePage.scope === issueScope ? issuePage.cursor : undefined;
  useEffect(() => setIssuePage({ scope: issueScope }), [issueScope]);
  const issues = useHealthIssues({ ...(state.status ? { statuses: [state.status] } : {}), ...(state.severity ? { severities: [state.severity] } : {}), ...(state.type ? { types: [state.type] } : {}), ...(cursor ? { cursor } : {}), limit: 25 });
  if (issues.isPending) return <Card><StateMessage title="正在读取 Issue" description="读取当前 Workspace 的问题列表。" /></Card>;
  if (issues.isError) return <ErrorState title="Issue 列表不可用" description={issues.error.message} onRetry={() => void issues.refetch()} />;
  return <Card>{issues.data.items.length === 0 ? <EmptyState title="没有匹配 Issue" description="当前筛选没有打开的问题；不可用 detector 仍在 Coverage 中显示。" /> : <div className="health-issue-list">{issues.data.items.map((issue) => <IssueRow key={issue.id} issue={issue} selected={state.issueId === issue.id} onSelect={(button) => { returnFocus.current = button; onChange({ ...state, issueId: issue.id }); }} />)}</div>}<div className="pagination-row">{cursor !== undefined ? <Button variant="ghost" onClick={() => setIssuePage({ scope: issueScope })}>回到首屏</Button> : <span />}{issues.data.nextCursor ? <Button variant="secondary" onClick={() => { const nextCursor = issues.data.nextCursor; if (nextCursor) setIssuePage({ scope: issueScope, cursor: nextCursor }); }}>下一页<ArrowRight size={15} /></Button> : null}</div></Card>;
};

const IssueDetail = ({ issue }: { issue: HealthIssue }) => <div className="health-issue-detail"><div><Badge tone={severityTone(issue.severity)}>{issue.severity}</Badge><Badge tone={statusTone(issue.status)}>{issue.status}</Badge></div><h3>{issue.type}</h3><p>{issue.evidenceSummary}</p><dl className="detail-grid"><div><dt>Target</dt><dd>{issue.target.type}:{issue.target.id}</dd></div><div><dt>Detector</dt><dd>{issue.detectorId}@{issue.detectorVersion}</dd></div><div><dt>Fingerprint</dt><dd><code>{issue.fingerprint.slice(0, 16)}…</code></dd></div><div><dt>Version</dt><dd>{issue.version}</dd></div></dl></div>;

const EvidenceDialog = ({ issueId, onClose, returnFocus }: { issueId: string; onClose: () => void; returnFocus: RefObject<HTMLButtonElement | null> }) => {
  const detail = useHealthIssue(issueId);
  const decide = useDecideHealthIssue();
  const clearIssueDetail = useClearHealthIssueDetail();
  const [reason, setReason] = useState("");
  const [deferredUntil, setDeferredUntil] = useState("");
  const decisionAttempt = useRef<IdempotentAttempt | null>(null);
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
    setReason("");
    setDeferredUntil("");
    decisionAttempt.current = null;
    clearIssueDetail(closingIssueId);
    onClose();
  };
  const decisionError = decide.isError
    ? decide.error instanceof HealthApiError && decide.error.status === 409
      ? "Issue 已被其他操作更新，请关闭后重新打开并基于最新版本重试。"
      : `${decide.error.message}${decide.error instanceof HealthApiError && decide.error.retryable ? "（可重试；相同操作将复用幂等键。）" : ""}`
    : "";

  return <Dialog open={issueId !== ""} onOpenChange={(open) => { if (!open) close(); }} restoreFocusRef={returnFocus} title="Issue Evidence" description="Evidence 按需读取，关闭后焦点返回触发按钮。">
    {detail.isPending ? <StateMessage title="正在读取 Evidence" description="读取 Issue 当前证据与可用修复选项。" /> : detail.isError ? <ErrorState title="Evidence 不可用" description={detail.error.message} onRetry={() => void detail.refetch()} /> : <div className="health-evidence-detail">
      <IssueDetail issue={detail.data.issue} />
      <div className="health-evidence-list evidence-list">
        {detail.data.issue.evidence.length === 0 ? <EmptyState title="没有 Evidence" description="该 Issue 当前没有可展示的证据摘要。" /> : detail.data.issue.evidence.map((evidence) => <article key={`${evidence.ref.type}:${evidence.ref.id}:${evidence.hash}`}><strong>{evidence.ref.type}:{evidence.ref.id.slice(0, 8)}…</strong><p>{evidence.summary || "—"}</p><code>{evidence.hash.slice(0, 16)}…</code></article>)}
      </div>
      <div className="health-decision-panel health-decision-form">
        <label>原因<textarea value={reason} onChange={(event) => setReason(event.target.value)} rows={2} /></label>
        <label>暂缓到<input value={deferredUntil} onChange={(event) => setDeferredUntil(event.target.value)} type="datetime-local" /></label>
        <div className="button-row"><Button variant="secondary" disabled={decide.isPending} onClick={() => submitDecision(detail.data.issue, "ACKNOWLEDGE")}><ShieldCheck size={15} />确认</Button><Button variant="secondary" disabled={decide.isPending || reason.trim() === ""} onClick={() => submitDecision(detail.data.issue, "IGNORE")}>忽略</Button><Button variant="secondary" disabled={decide.isPending || reason.trim() === ""} onClick={() => submitDecision(detail.data.issue, "FALSE_POSITIVE")}>误报</Button><Button variant="secondary" disabled={decide.isPending || reason.trim() === "" || deferredUntil === ""} onClick={() => submitDecision(detail.data.issue, "DEFER")}>暂缓</Button></div>
        {decisionError ? <p role="alert" className="form-error">{decisionError}</p> : null}
      </div>
      <div className="health-repair-options repair-options">
        <h3>修复选项（不可用）</h3>
        {detail.data.issue.repairOptions.length === 0 ? <p className="sidebar-note">Repair Proposal 端点尚未启用；Health 不会直接修改 Knowledge。</p> : detail.data.issue.repairOptions.map((option) => <div key={option.code}><div><strong>{option.title}</strong><small>{option.unavailableReason ?? "Repair Proposal 端点尚未启用"}</small></div><Button variant="ghost" disabled>创建 Proposal（不可用）</Button></div>)}
      </div>
    </div>}
  </Dialog>;
};

export const HealthPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params, setParams] = useSearchParams();
  const state = parseHealthUrlState(params);
  const returnFocus = useRef<HTMLButtonElement>(null);
  const onChange = (next: HealthUrlState) => updateState(next, params, setParams);
  if (!workspaceId) return <div className="page-stack"><UnavailableState title="先连接 Workspace" description="Knowledge Health 只能读取当前 Workspace 的持久化 Issue 与扫描。" /><Link className="ui-button ui-button--primary" to="/settings">前往 Settings</Link></div>;
  return <div className="page-stack"><div className="page-intro page-intro--split"><div><p className="eyebrow">Ops / Knowledge Health</p><h2>持续发现知识质量问题。</h2><p>Issue、Evidence、Decision 和 Scan 状态均来自持久化结果；系统依赖状态仍在独立页面展示。</p></div><div className="folio-mark"><HeartPulse size={20} /><strong>Health</strong><span>knowledge</span></div></div><HealthOverview state={state} onChange={onChange} /><ScanRecovery scanId={state.scanId} /><IssueFilters state={state} onChange={onChange} /><IssueList state={state} onChange={onChange} returnFocus={returnFocus} /><EvidenceDialog issueId={state.issueId} onClose={() => onChange({ ...state, issueId: "" })} returnFocus={returnFocus} /></div>;
};
