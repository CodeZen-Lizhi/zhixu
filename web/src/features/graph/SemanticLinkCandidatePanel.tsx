import { type KeyboardEvent, type SyntheticEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import "./semantic-link.css";

import {
  SemanticLinkApiError,
  type RelationType,
  type SemanticLinkCandidate,
  type SemanticLinkCandidateDecisionInput,
  type SemanticLinkCandidateStatus,
  type SemanticLinkDecisionAction,
} from "../../api/semantic-links";
import type { GraphNodeRef } from "../../api/graph";
import {
  createSemanticLinkIdempotencyKey,
  useDecideSemanticLinkCandidate,
  useSemanticLinkCandidates,
  useSemanticLinkScan,
  useStartSemanticLinkScan,
} from "./semantic-link-queries";
import {
  compatibleSemanticLinkRelationTypes,
  groupSemanticLinkCandidates,
  semanticLinkActionLabels,
  semanticLinkCandidateActions,
  semanticLinkDiscoveryLabels,
  semanticLinkRelationLabels,
  semanticLinkRelationTypes,
  semanticLinkStatusLabels,
} from "./semantic-link-view-model";

type CandidateStatusFilter = "ACTIVE" | "DEFERRED" | "ALL";
type CandidateView = "cards" | "list";

interface DecisionDraft {
  candidate: SemanticLinkCandidate;
  action: SemanticLinkDecisionAction;
  idempotencyKey: string;
  relationType: RelationType;
  reason: string;
  deferredUntil: string;
}

interface CandidateNotice {
  candidateId: string;
  message: string;
}

export interface SemanticLinkCandidatePanelProps {
  workspaceId: string;
  nodeScope?: GraphNodeRef | null;
  idempotencyKeyFactory?: () => string;
  scanIdempotencyKeyFactory?: () => string;
  persistedScanId?: string | null;
  onPersistedScanIdChange?: (scanId: string | null) => void;
}

const focusableSelector = [
  "button:not([disabled])",
  "a[href]",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "summary",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

const errorSummary = (error: unknown): { title: string; message: string; conflict: boolean; retryable: boolean; cursorStale: boolean } => {
  if (error instanceof SemanticLinkApiError) {
    const conflict = error.status === 409 || error.errorCode.includes("CONFLICT") || error.errorCode.includes("STALE");
    return {
      title: conflict ? "候选已变化" : "候选操作失败",
      message: `${error.message} (${error.errorCode})`,
      conflict,
      retryable: error.retryable,
      cursorStale: error.errorCode.includes("CURSOR"),
    };
  }
  return {
    title: "候选操作失败",
    message: error instanceof Error ? error.message : "发生未知错误。",
    conflict: false,
    retryable: false,
    cursorStale: false,
  };
};

const normalizeReason = (value: string): string => value.normalize("NFC").trim().replace(/\s+/gu, " ");

const formatConfidence = (value: number): string => `${String(Math.round(value * 100))}%`;

const formatTimestamp = (value: string): string => new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
}).format(new Date(value));

const CandidateCard = ({
  candidate,
  compact,
  pending,
  notice,
  onAction,
}: {
  candidate: SemanticLinkCandidate;
  compact: boolean;
  pending: boolean;
  notice: CandidateNotice | null;
  onAction: (candidate: SemanticLinkCandidate, action: SemanticLinkDecisionAction, trigger: HTMLButtonElement) => void;
}) => {
  const titleId = `semantic-link-candidate-${candidate.id}`;
  const actions = semanticLinkCandidateActions(candidate.status);
  return <article className={`semantic-link-card${compact ? " semantic-link-card--compact" : ""}`} aria-labelledby={titleId}>
    <div className="semantic-link-card__meta">
      <span className="semantic-link-card__kind">Candidate · 未进入正式图</span>
      <span className={`semantic-link-status semantic-link-status--${candidate.status.toLowerCase()}`}>
        {semanticLinkStatusLabels[candidate.status]}
      </span>
    </div>
    {candidate.reopenedReason === "CONTENT_CHANGED" ? <p className="semantic-link-reopened">
      因端点内容变化重新评估
    </p> : null}
    <div className="semantic-link-endpoints">
      <div><span>{candidate.source.type}</span><strong id={titleId}>{candidate.source.summary}</strong>{candidate.source.excerpt === "" ? null : <p>{candidate.source.excerpt}</p>}<code>v{candidate.source.version} · {candidate.source.id}</code></div>
      <span className="semantic-link-endpoints__arrow" aria-label="建议关联到">→</span>
      <div><span>{candidate.target.type}</span><strong>{candidate.target.summary}</strong>{candidate.target.excerpt === "" ? null : <p>{candidate.target.excerpt}</p>}<code>v{candidate.target.version} · {candidate.target.id}</code></div>
    </div>
    <dl className="semantic-link-card__facts">
      <div><dt>建议关系</dt><dd>{semanticLinkRelationLabels[candidate.proposedRelationType]} <code>{candidate.proposedRelationType}</code></dd></div>
      <div><dt>置信度</dt><dd><meter min="0" max="1" value={candidate.confidence}>{formatConfidence(candidate.confidence)}</meter><span>{formatConfidence(candidate.confidence)}</span></dd></div>
      <div><dt>更新时间</dt><dd>{formatTimestamp(candidate.updatedAt)}</dd></div>
    </dl>
    <p className="semantic-link-card__reason">{candidate.reason}</p>
    <ul className="semantic-link-methods" aria-label="发现方式">
      {candidate.discoveryMethods.map((method) => <li key={method}>{semanticLinkDiscoveryLabels[method]}</li>)}
    </ul>
    <details className="semantic-link-evidence">
      <summary>查看候选证据 ({candidate.evidence.length})</summary>
      {candidate.evidence.length === 0 ? <p>当前响应没有候选证据。</p> : <ol>
        {candidate.evidence.map((evidence) => <li key={evidence.id}>
          <blockquote>{evidence.excerpt}</blockquote>
          <p>{evidence.reason}</p>
          <div><a href={evidence.sourceSpanHref} target="_blank" rel="noreferrer">打开来源片段</a><code>{evidence.semanticHash.slice(0, 12)}…</code></div>
        </li>)}
      </ol>}
    </details>
    {candidate.proposalId === null ? null : <p className="semantic-link-proposal">
      <span>独立 Proposal</span><code>{candidate.proposalId}</code><strong>尚未写入正式图</strong>
    </p>}
    {notice?.candidateId === candidate.id ? <p className="semantic-link-card__success" role="status">{notice.message}</p> : null}
    {actions.length === 0 ? null : <div className="semantic-link-actions" aria-label="候选决策">
      {actions.map((action) => <button
        key={action}
        type="button"
        className={action === "CONFIRM" ? "semantic-link-actions__primary" : "semantic-link-actions__secondary"}
        disabled={pending}
        onClick={(event) => onAction(candidate, action, event.currentTarget)}
      >{pending ? "提交中" : semanticLinkActionLabels[action]}</button>)}
    </div>}
  </article>;
};

export const SemanticLinkCandidatePanel = ({
  workspaceId,
  nodeScope = null,
  idempotencyKeyFactory = createSemanticLinkIdempotencyKey,
  scanIdempotencyKeyFactory = () => createSemanticLinkIdempotencyKey("candidate-scan"),
  persistedScanId,
  onPersistedScanIdChange,
}: SemanticLinkCandidatePanelProps) => {
  const [open, setOpen] = useState(false);
  const [statusFilter, setStatusFilter] = useState<CandidateStatusFilter>("ACTIVE");
  const [relationFilter, setRelationFilter] = useState<RelationType | "ALL">("ALL");
  const [minConfidence, setMinConfidence] = useState(0);
  const [view, setView] = useState<CandidateView>("cards");
  const [decision, setDecision] = useState<DecisionDraft | null>(null);
  const [validationError, setValidationError] = useState("");
  const [notice, setNotice] = useState<CandidateNotice | null>(null);
  const [localScanId, setLocalScanId] = useState("");
  const panelTriggerRef = useRef<HTMLButtonElement>(null);
  const decisionTriggerRef = useRef<HTMLButtonElement | null>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const persistedScanChangeRef = useRef(onPersistedScanIdChange);
  const decisionMutation = useDecideSemanticLinkCandidate();
  const scanMutation = useStartSemanticLinkScan();
  const scanWorkspaceRef = useRef(workspaceId);
  const workspaceChanged = scanWorkspaceRef.current !== workspaceId;
  const scanId = workspaceChanged ? "" : persistedScanId === undefined ? localScanId : persistedScanId ?? "";
  const setScanId = useCallback((value: string): void => {
    setLocalScanId(value);
    persistedScanChangeRef.current?.(value === "" ? null : value);
  }, []);
  const scanQuery = useSemanticLinkScan({ workspaceId, scanId }, scanId !== "");
  const refreshedScanRef = useRef("");
  const topicScope = nodeScope?.type === "TOPIC" ? nodeScope : null;
  const scanScopeMismatch = topicScope !== null
    && scanQuery.data?.scope.kind === "TOPIC"
    && scanQuery.data.scope.topicId !== topicScope.id;

  const queryInput = useMemo(() => ({
    workspaceId,
    ...(nodeScope === null ? {} : { nodeType: nodeScope.type, nodeId: nodeScope.id }),
    ...(statusFilter === "ALL" ? {} : { statuses: [statusFilter] as SemanticLinkCandidateStatus[] }),
    ...(relationFilter === "ALL" ? {} : { relationTypes: [relationFilter] }),
    ...(minConfidence === 0 ? {} : { minConfidence }),
    limit: 20,
  }), [minConfidence, nodeScope, relationFilter, statusFilter, workspaceId]);
  const candidateQuery = useSemanticLinkCandidates(queryInput, open);
  const candidates = useMemo(() => {
    const byId = new Map<string, SemanticLinkCandidate>();
    candidateQuery.data?.pages.forEach((page) => page.items.forEach((candidate) => byId.set(candidate.id, candidate)));
    return [...byId.values()];
  }, [candidateQuery.data]);
  const groups = useMemo(() => groupSemanticLinkCandidates(candidates), [candidates]);
  const currentMutationCandidateId = decisionMutation.isPending ? decisionMutation.variables.candidateId : null;
  const queryError = candidateQuery.isError ? errorSummary(candidateQuery.error) : null;

  useEffect(() => {
    if (decision === null) return;
    const frame = requestAnimationFrame(() => {
      const dialog = dialogRef.current;
      const preferred = dialog?.querySelector<HTMLElement>("[data-autofocus]");
      (preferred ?? dialog)?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, [decision?.action, decision?.candidate.id]);

  useEffect(() => {
    setDecision(null);
    setNotice(null);
    setValidationError("");
    decisionMutation.reset();
  }, [nodeScope?.id, nodeScope?.type, workspaceId]);

  useEffect(() => {
    persistedScanChangeRef.current = onPersistedScanIdChange;
  }, [onPersistedScanIdChange]);

  useEffect(() => {
    if (scanWorkspaceRef.current !== workspaceId) {
      scanWorkspaceRef.current = workspaceId;
      setScanId("");
    }
    scanMutation.reset();
    refreshedScanRef.current = "";
  }, [scanMutation.reset, setScanId, workspaceId]);

  useEffect(() => {
    if (scanScopeMismatch || scanQuery.data?.status !== "SUCCEEDED" || refreshedScanRef.current === scanQuery.data.id) return;
    refreshedScanRef.current = scanQuery.data.id;
    void candidateQuery.refetch();
  }, [candidateQuery, scanQuery.data, scanScopeMismatch]);

  useEffect(() => {
    if (!scanScopeMismatch) return;
    refreshedScanRef.current = "";
    setScanId("");
  }, [scanScopeMismatch, setScanId]);

  const restoreDecisionFocus = (): void => {
    const trigger = decisionTriggerRef.current;
    if (trigger?.isConnected === true) trigger.focus();
    requestAnimationFrame(() => {
      if (trigger?.isConnected === true) trigger.focus();
      else panelTriggerRef.current?.focus();
    });
    decisionTriggerRef.current = null;
  };

  const closeDecision = (): void => {
    if (decisionMutation.isPending) return;
    setDecision(null);
    setValidationError("");
    decisionMutation.reset();
    restoreDecisionFocus();
  };

  const openDecision = (
    candidate: SemanticLinkCandidate,
    action: SemanticLinkDecisionAction,
    trigger: HTMLButtonElement,
  ): void => {
    decisionTriggerRef.current = trigger;
    decisionMutation.reset();
    setValidationError("");
    setDecision({
      candidate,
      action,
      idempotencyKey: idempotencyKeyFactory(),
      relationType: candidate.proposedRelationType,
      reason: "",
      deferredUntil: "",
    });
  };

  const buildDecisionInput = (draft: DecisionDraft): SemanticLinkCandidateDecisionInput | null => {
    const base = {
      candidateId: draft.candidate.id,
      workspaceId,
      expectedVersion: draft.candidate.version,
      idempotencyKey: draft.idempotencyKey,
    };
    switch (draft.action) {
      case "CONFIRM":
        return { ...base, action: "CONFIRM" };
      case "CONFIRM_WITH_RELATION_TYPE":
        return { ...base, action: "CONFIRM_WITH_RELATION_TYPE", relationType: draft.relationType };
      case "IGNORE":
      case "FALSE_POSITIVE": {
        const reason = normalizeReason(draft.reason);
        if (reason === "") {
          setValidationError("请填写非空原因。");
          return null;
        }
        return { ...base, action: draft.action, reason };
      }
      case "DEFER": {
        const reason = normalizeReason(draft.reason);
        let deferredUntil: string | null = null;
        if (draft.deferredUntil !== "") {
          const parsed = new Date(draft.deferredUntil);
          if (!Number.isFinite(parsed.getTime())) {
            setValidationError("恢复时间格式无效。");
            return null;
          }
          deferredUntil = parsed.toISOString();
        }
        return { ...base, action: "DEFER", ...(reason === "" ? {} : { reason }), deferredUntil };
      }
      case "RESUME":
        return { ...base, action: "RESUME" };
    }
  };

  const submitDecision = (event: SyntheticEvent<HTMLFormElement>): void => {
    event.preventDefault();
    if (decision === null || decisionMutation.isPending) return;
    setValidationError("");
    const input = buildDecisionInput(decision);
    if (input === null) return;
    decisionMutation.mutate(input, {
      onSuccess: (receipt) => {
        const message = receipt.proposalId === null
          ? `已完成“${semanticLinkActionLabels[receipt.action]}”，服务端版本为 ${String(receipt.version)}。`
          : `已创建独立 Proposal ${receipt.proposalId}；审批应用前不会进入正式图。`;
        setNotice({ candidateId: receipt.candidateId, message });
        setDecision(null);
        restoreDecisionFocus();
      },
    });
  };

  const handleDialogKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    if (event.key === "Escape") {
      event.preventDefault();
      closeDecision();
      return;
    }
    if (event.key !== "Tab") return;
    const dialog = dialogRef.current;
    if (dialog === null) return;
    const focusable = [...dialog.querySelectorAll<HTMLElement>(focusableSelector)];
    const first = focusable[0];
    const last = focusable.at(-1);
    if (first === undefined || last === undefined) {
      event.preventDefault();
      dialog.focus();
      return;
    }
    if (event.shiftKey && (document.activeElement === first || !dialog.contains(document.activeElement))) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog)) {
      event.preventDefault();
      first.focus();
    }
  };

  const mutationError = decisionMutation.isError ? errorSummary(decisionMutation.error) : null;
  const scanError = scanMutation.isError
    ? errorSummary(scanMutation.error)
    : scanQuery.isError ? errorSummary(scanQuery.error) : null;
  const scan = scanScopeMismatch ? undefined : scanQuery.data;
  const scanActive = scan?.status === "PENDING" || scan?.status === "RUNNING";
  const compatibleRelationTypes = decision === null ? [] : compatibleSemanticLinkRelationTypes(decision.candidate);
  const pageCountLabel = candidateQuery.data === undefined
    ? ""
    : `${String(candidates.length)}${candidateQuery.hasNextPage ? "+" : ""}`;

  const startTopicScan = (reuseFailedRequest: boolean): void => {
    if (topicScope === null) return;
    const previous = scanMutation.variables;
    const input = reuseFailedRequest && previous?.workspaceId === workspaceId && previous.scope.kind === "TOPIC" && previous.scope.topicId === topicScope.id
      ? previous
      : {
        workspaceId,
        scope: { kind: "TOPIC" as const, topicId: topicScope.id },
        idempotencyKey: scanIdempotencyKeyFactory(),
      };
    scanMutation.mutate(input, { onSuccess: (accepted) => setScanId(accepted.scanId) });
  };

  return <section className={`semantic-link-panel${open ? " is-open" : ""}`} aria-labelledby="semantic-link-panel-title">
    <header className="semantic-link-panel__header">
      <div>
        <span className="graph-kicker">Review queue · Candidate only</span>
        <h2 id="semantic-link-panel-title">语义关系候选</h2>
        <p>{nodeScope === null ? "当前 Workspace" : `${nodeScope.type} · ${nodeScope.id}`} · 与正式图谱隔离</p>
      </div>
      <div className="semantic-link-panel__header-actions">
        {pageCountLabel === "" ? null : <output aria-label="当前已加载候选数">{pageCountLabel}</output>}
        {topicScope === null ? null : <button
          type="button"
          className="graph-secondary-command"
          disabled={scanMutation.isPending || scanActive}
          onClick={() => {
            setOpen(true);
            startTopicScan(false);
          }}
        >{scanMutation.isPending ? "正在启动扫描" : scanActive ? "扫描进行中" : "扫描当前 Topic"}</button>}
        <button
          ref={panelTriggerRef}
          type="button"
          className="graph-secondary-command"
          aria-expanded={open}
          aria-controls="semantic-link-panel-body"
          onClick={() => setOpen((current) => !current)}
        >{open ? "收起候选" : "审阅候选"}</button>
      </div>
    </header>
    {!open ? null : <div id="semantic-link-panel-body" className="semantic-link-panel__body">
      {topicScope === null ? <p className="semantic-link-scan-hint">选择正式 Topic 后可启动持久候选扫描；Claim 选择只过滤现有候选。</p> : null}
      {scan === undefined ? null : <section className="semantic-link-scan" aria-live="polite">
        <div><strong>Topic 扫描 · {scan.status}</strong><span>{String(scan.processedCount)} / {String(scan.totalCount)} 节点</span></div>
        <progress max={Math.max(scan.totalCount, 1)} value={scan.processedCount}>{String(scan.processedCount)}</progress>
        <p>候选 {String(scan.candidateCount)} · 忽略 {String(scan.ignoredCount)} · 失败 {String(scan.failedCount)}</p>
      </section>}
      {scan?.lastError === null || scan?.lastError === undefined ? null : <section className="semantic-link-error semantic-link-scan-error" role="alert">
        <strong>扫描失败 · {scan.lastError.code}</strong>
        <p>阶段：{scan.lastError.stage} · {scan.lastError.retryable ? "该故障可重试。" : "需修复依赖或输入后重新发起。"}</p>
        <button type="button" disabled={scanMutation.isPending} onClick={() => startTopicScan(false)}>重新发起扫描</button>
      </section>}
      {scanError === null ? null : <section className="semantic-link-error semantic-link-scan-error" role="alert">
        <strong>{scanError.title}</strong><p>{scanError.message}</p>
        <button type="button" onClick={() => {
          if (scanMutation.isError) startTopicScan(true);
          else if (scanQuery.isError && scanId !== "") void scanQuery.refetch();
          else startTopicScan(true);
        }}>{scanMutation.isError ? "重试扫描启动" : "重试扫描状态"}</button>
      </section>}
      <div className="semantic-link-toolbar" aria-label="候选显示控制">
        <label><span>处理状态</span><select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as CandidateStatusFilter)}>
          <option value="ACTIVE">待处理</option>
          <option value="DEFERRED">稍后处理</option>
          <option value="ALL">全部状态</option>
        </select></label>
        <label><span>关系类型</span><select value={relationFilter} onChange={(event) => setRelationFilter(event.target.value as RelationType | "ALL")}>
          <option value="ALL">全部关系</option>
          {semanticLinkRelationTypes.map((relationType) => <option key={relationType} value={relationType}>{semanticLinkRelationLabels[relationType]}</option>)}
        </select></label>
        <label className="semantic-link-confidence-filter"><span>最低置信度 <strong>{formatConfidence(minConfidence)}</strong></span><input
          type="range"
          aria-label="最低置信度"
          min="0"
          max="1"
          step="0.05"
          value={minConfidence}
          onChange={(event) => setMinConfidence(Number(event.target.value))}
        /></label>
        <div className="semantic-link-view-switch" aria-label="候选视图">
          <button type="button" aria-pressed={view === "cards"} onClick={() => setView("cards")}>卡片</button>
          <button type="button" aria-pressed={view === "list"} onClick={() => setView("list")}>列表</button>
        </div>
      </div>
      {candidateQuery.isPending ? <p className="semantic-link-state" role="status">正在加载语义候选…</p> : null}
      {queryError === null ? null : <section className="semantic-link-error" role="alert">
        <strong>{queryError.title}</strong><p>{queryError.message}</p>
        <button type="button" onClick={() => {
          if (queryError.cursorStale) void candidateQuery.resetToFirstPage();
          else void candidateQuery.refetch();
        }}>{queryError.cursorStale ? "从第一页重新加载" : "重新加载候选"}</button>
      </section>}
      {!candidateQuery.isPending && queryError === null && candidates.length === 0 ? <p className="semantic-link-state">
        当前筛选范围没有语义候选。
      </p> : null}
      <div className={`semantic-link-groups semantic-link-groups--${view}`}>
        {groups.map((group) => <section key={group.relationType} className="semantic-link-group" aria-labelledby={`semantic-link-group-${group.relationType}`}>
          <header><h3 id={`semantic-link-group-${group.relationType}`}>{semanticLinkRelationLabels[group.relationType]}</h3><span>{String(group.items.length)} 条</span></header>
          <div className="semantic-link-group__items">
            {group.items.map((candidate) => <CandidateCard
              key={candidate.id}
              candidate={candidate}
              compact={view === "list"}
              pending={currentMutationCandidateId === candidate.id}
              notice={notice}
              onAction={openDecision}
            />)}
          </div>
        </section>)}
      </div>
      {candidateQuery.hasNextPage ? <button
        type="button"
        className="semantic-link-load-more graph-secondary-command"
        disabled={candidateQuery.isFetchingNextPage}
        onClick={() => { void candidateQuery.fetchNextPage(); }}
      >{candidateQuery.isFetchingNextPage ? "正在加载" : "加载更多候选"}</button> : null}
    </div>}
    {decision === null ? null : <div className="semantic-link-dialog-backdrop">
      <div
        ref={dialogRef}
        className="semantic-link-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="semantic-link-dialog-title"
        tabIndex={-1}
        onKeyDown={handleDialogKeyDown}
      >
        <div className="semantic-link-dialog__heading">
          <div><span className="graph-kicker">Candidate decision</span><h3 id="semantic-link-dialog-title">{semanticLinkActionLabels[decision.action]}</h3></div>
          <button type="button" aria-label="关闭候选决策" title="关闭" disabled={decisionMutation.isPending} onClick={closeDecision}>×</button>
        </div>
        <p className="semantic-link-dialog__candidate">{decision.candidate.source.summary} → {decision.candidate.target.summary}</p>
        <form onSubmit={submitDecision}>
          {decision.action === "CONFIRM_WITH_RELATION_TYPE" ? <label><span>确认的关系类型</span><select
            data-autofocus
            value={decision.relationType}
            onChange={(event) => setDecision((current) => current === null ? null : { ...current, relationType: event.target.value as RelationType })}
          >{compatibleRelationTypes.map((relationType) => <option key={relationType} value={relationType}>{semanticLinkRelationLabels[relationType]} · {relationType}</option>)}</select></label> : null}
          {decision.action === "IGNORE" || decision.action === "FALSE_POSITIVE" || decision.action === "DEFER" ? <label><span>{decision.action === "DEFER" ? "备注（可选）" : "原因"}</span><textarea
            data-autofocus
            rows={4}
            maxLength={1024}
            aria-required={decision.action !== "DEFER"}
            value={decision.reason}
            aria-describedby={validationError === "" ? undefined : "semantic-link-dialog-validation"}
            onChange={(event) => setDecision((current) => current === null ? null : { ...current, reason: event.target.value })}
          /></label> : null}
          {decision.action === "DEFER" ? <label><span>恢复时间（留空表示手工恢复）</span><input
            type="datetime-local"
            value={decision.deferredUntil}
            onChange={(event) => setDecision((current) => current === null ? null : { ...current, deferredUntil: event.target.value })}
          /></label> : null}
          {decision.action === "CONFIRM" || decision.action === "RESUME" ? <p className="semantic-link-dialog__confirmation" data-autofocus tabIndex={-1}>
            {decision.action === "CONFIRM"
              ? `确认后将创建 ${semanticLinkRelationLabels[decision.candidate.proposedRelationType]} Proposal；审批前不会写入正式图。`
              : "恢复后，该候选将回到待处理队列。"}
          </p> : null}
          {validationError === "" ? null : <p id="semantic-link-dialog-validation" className="semantic-link-dialog__validation" role="alert">{validationError}</p>}
          {mutationError === null ? null : <section className={`semantic-link-dialog__error${mutationError.conflict ? " is-conflict" : ""}`} role="alert">
            <strong>{mutationError.title}</strong><p>{mutationError.message}</p>
            {mutationError.conflict ? <button type="button" onClick={() => {
              setDecision(null);
              restoreDecisionFocus();
              void candidateQuery.refetch();
            }}>刷新候选</button> : null}
          </section>}
          <div className="semantic-link-dialog__actions">
            <button type="button" className="graph-secondary-command" disabled={decisionMutation.isPending} onClick={closeDecision}>取消</button>
            <button type="submit" disabled={decisionMutation.isPending || mutationError?.conflict === true}>
              {decisionMutation.isPending ? "正在提交" : mutationError?.retryable === true ? "重试提交" : "提交决策"}
            </button>
          </div>
        </form>
      </div>
    </div>}
  </section>;
};
