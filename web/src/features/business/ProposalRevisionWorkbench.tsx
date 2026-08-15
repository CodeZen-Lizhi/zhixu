import { lazy, Suspense, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { AlertTriangle, Check, GitCompareArrows, RotateCcw, Save, X } from "lucide-react";

import {
  appendProposalRevision,
  createProposalRevisionIdempotencyKey,
  previewProposalRevision,
  proposalRevisionMaxBytes,
  proposalRevisionMetadataMaxBytes,
  type AppendProposalRevisionInput,
  type AppendProposalRevisionResult,
  type ProposalRevisionMergePreview,
  type ProposalRevisionPreviewBinding,
} from "../../api/business-revisions";
import { BusinessApiError, type ProposalRiskLevel } from "../../api/business";
import { MonacoTextEditor } from "../../shared/MonacoTextEditor";
import {
  Badge,
  Button,
  Dialog,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "../../shared/ui";
import "./proposal-revision.css";

const MonacoDiffViewer = lazy(() => import("../../shared/MonacoDiffViewer").then((module) => ({ default: module.MonacoDiffViewer })));
const utf8Encoder = new TextEncoder();

export type ProposalRevisionWorkbenchPhase =
  | "preparing"
  | "preview_error"
  | "active_clean"
  | "active_conflicts"
  | "submitting"
  | "delivery_unknown"
  | "stale"
  | "restart_review"
  | "success";

export type ProposalRevisionComparisonTab = "base-current" | "base-proposed" | "conflicts";
export type ProposalRevisionDraftField = "content" | "evidenceSummary" | "risk" | "rollbackPlan";

export interface ProposalRevisionDraft {
  content: string;
  evidenceSummary: string;
  risk: string;
  rollbackPlan: string;
  resolvedConflictIds: string[];
}

export interface ProposalRevisionSubmission {
  idempotencyKey: string;
  input: AppendProposalRevisionInput;
}

export interface ProposalRevisionWorkbenchState {
  phase: ProposalRevisionWorkbenchPhase;
  selectedTab: ProposalRevisionComparisonTab;
  preview: ProposalRevisionMergePreview | undefined;
  draft: ProposalRevisionDraft | undefined;
  initialDraft: ProposalRevisionDraft | undefined;
  previousDraft: ProposalRevisionDraft | undefined;
  freshDraft: ProposalRevisionDraft | undefined;
  submission: ProposalRevisionSubmission | undefined;
  editorStatus: "loading" | "ready" | "error";
  message: string;
  errorCode: string | undefined;
  success: AppendProposalRevisionResult | undefined;
  successSyncStatus: "idle" | "syncing" | "ready" | "failed";
}

export type ProposalRevisionWorkbenchAction =
  | { type: "PREPARE"; restart: boolean }
  | { type: "PREVIEW_SUCCESS"; preview: ProposalRevisionMergePreview; metadata: Omit<ProposalRevisionDraft, "content" | "resolvedConflictIds"> }
  | { type: "PREVIEW_FAILURE"; message: string; errorCode?: string }
  | { type: "SELECT_TAB"; tab: ProposalRevisionComparisonTab }
  | { type: "EDIT"; field: ProposalRevisionDraftField; value: string }
  | { type: "TOGGLE_CONFLICT"; conflictId: string; resolved: boolean }
  | { type: "EDITOR_READY" }
  | { type: "EDITOR_ERROR"; message: string }
  | { type: "SUBMIT"; submission: ProposalRevisionSubmission }
  | { type: "KNOWN_ERROR"; message: string; errorCode?: string; focusConflictId?: string }
  | { type: "DELIVERY_UNKNOWN"; message: string; errorCode?: string }
  | { type: "STALE"; message: string; errorCode: string }
  | { type: "RESUME_AFTER_UNKNOWN" }
  | { type: "ADOPT_FRESH" }
  | { type: "KEEP_PREVIOUS" }
  | { type: "SUCCESS"; result: AppendProposalRevisionResult }
  | { type: "SUCCESS_SYNC_START" }
  | { type: "SUCCESS_SYNC_DONE" }
  | { type: "SUCCESS_SYNC_FAILURE"; message: string };

export const initialProposalRevisionWorkbenchState = (): ProposalRevisionWorkbenchState => ({
  phase: "preparing",
  selectedTab: "base-current",
  preview: undefined,
  draft: undefined,
  initialDraft: undefined,
  previousDraft: undefined,
  freshDraft: undefined,
  submission: undefined,
  editorStatus: "loading",
  message: "",
  errorCode: undefined,
  success: undefined,
  successSyncStatus: "idle",
});

const activePhase = (preview: ProposalRevisionMergePreview): ProposalRevisionWorkbenchPhase =>
  preview.conflictCount === 0 ? "active_clean" : "active_conflicts";

const draftForPreview = (
  preview: ProposalRevisionMergePreview,
  metadata: Omit<ProposalRevisionDraft, "content" | "resolvedConflictIds">,
): ProposalRevisionDraft => ({
  content: preview.candidate.content,
  evidenceSummary: metadata.evidenceSummary,
  risk: metadata.risk,
  rollbackPlan: metadata.rollbackPlan,
  resolvedConflictIds: [],
});

const editableState = (state: ProposalRevisionWorkbenchState): boolean =>
  state.phase === "active_clean" || state.phase === "active_conflicts";

export const proposalRevisionWorkbenchReducer = (
  state: ProposalRevisionWorkbenchState,
  action: ProposalRevisionWorkbenchAction,
): ProposalRevisionWorkbenchState => {
  switch (action.type) {
    case "PREPARE":
      return {
        ...state,
        phase: "preparing",
        message: "",
        errorCode: undefined,
        submission: undefined,
        ...(action.restart && state.draft !== undefined ? { previousDraft: state.draft } : { previousDraft: undefined }),
      };
    case "PREVIEW_SUCCESS": {
      const freshDraft = draftForPreview(action.preview, action.metadata);
      if (state.previousDraft !== undefined) {
        return {
          ...state,
          phase: "restart_review",
          preview: action.preview,
          freshDraft,
          initialDraft: freshDraft,
          selectedTab: "base-current",
          editorStatus: "loading",
          message: "",
          errorCode: undefined,
        };
      }
      return {
        ...state,
        phase: activePhase(action.preview),
        preview: action.preview,
        draft: freshDraft,
        initialDraft: freshDraft,
        previousDraft: undefined,
        freshDraft: undefined,
        selectedTab: action.preview.conflictCount === 0 ? "base-current" : "conflicts",
        editorStatus: "loading",
        message: "",
        errorCode: undefined,
      };
    }
    case "PREVIEW_FAILURE":
      return { ...state, phase: "preview_error", message: action.message, errorCode: action.errorCode };
    case "SELECT_TAB":
      return { ...state, selectedTab: action.tab };
    case "EDIT":
      if (!editableState(state) || state.draft === undefined) return state;
      return { ...state, draft: { ...state.draft, [action.field]: action.value }, message: "", errorCode: undefined };
    case "TOGGLE_CONFLICT":
      if (!editableState(state) || state.draft === undefined) return state;
      return {
        ...state,
        draft: {
          ...state.draft,
          resolvedConflictIds: action.resolved
            ? [...new Set([...state.draft.resolvedConflictIds, action.conflictId])]
            : state.draft.resolvedConflictIds.filter((id) => id !== action.conflictId),
        },
        message: "",
        errorCode: undefined,
      };
    case "EDITOR_READY":
      return { ...state, editorStatus: "ready", message: state.errorCode === "EDITOR_LOAD_FAILED" ? "" : state.message, errorCode: state.errorCode === "EDITOR_LOAD_FAILED" ? undefined : state.errorCode };
    case "EDITOR_ERROR":
      return { ...state, editorStatus: "error", message: action.message, errorCode: "EDITOR_LOAD_FAILED" };
    case "SUBMIT":
      if (!editableState(state)) return state;
      return { ...state, phase: "submitting", submission: action.submission, message: "", errorCode: undefined };
    case "KNOWN_ERROR": {
      if (state.preview === undefined) return state;
      const selectedTab = action.focusConflictId === undefined ? state.selectedTab : "conflicts";
      return { ...state, phase: activePhase(state.preview), selectedTab, submission: undefined, message: action.message, errorCode: action.errorCode };
    }
    case "DELIVERY_UNKNOWN":
      return { ...state, phase: "delivery_unknown", message: action.message, errorCode: action.errorCode };
    case "STALE":
			return { ...state, phase: "stale", submission: undefined, message: action.message, errorCode: action.errorCode };
    case "RESUME_AFTER_UNKNOWN":
      if (state.preview === undefined) return state;
      return { ...state, phase: activePhase(state.preview), submission: undefined, message: "", errorCode: undefined };
    case "ADOPT_FRESH":
      if (state.preview === undefined || state.freshDraft === undefined) return state;
      return {
        ...state,
        phase: activePhase(state.preview),
        draft: state.freshDraft,
        previousDraft: undefined,
        freshDraft: undefined,
        selectedTab: state.preview.conflictCount === 0 ? "base-current" : "conflicts",
        editorStatus: "loading",
        message: "",
        errorCode: undefined,
      };
    case "KEEP_PREVIOUS":
      if (state.preview === undefined || state.previousDraft === undefined) return state;
      return {
        ...state,
        phase: activePhase(state.preview),
        draft: { ...state.previousDraft, resolvedConflictIds: [] },
        previousDraft: undefined,
        freshDraft: undefined,
        selectedTab: state.preview.conflictCount === 0 ? "base-current" : "conflicts",
        editorStatus: "loading",
        message: "",
        errorCode: undefined,
      };
    case "SUCCESS":
      return {
        ...state,
        phase: "success",
        submission: undefined,
        success: action.result,
        successSyncStatus: "syncing",
        message: "",
        errorCode: undefined,
      };
    case "SUCCESS_SYNC_START":
      return state.phase === "success" ? { ...state, successSyncStatus: "syncing", message: "" } : state;
    case "SUCCESS_SYNC_DONE":
      return state.phase === "success" ? { ...state, successSyncStatus: "ready", message: "" } : state;
    case "SUCCESS_SYNC_FAILURE":
      return state.phase === "success" ? { ...state, successSyncStatus: "failed", message: action.message } : state;
  }
};

export const proposalRevisionDraftChanged = (state: ProposalRevisionWorkbenchState): boolean => {
  const { draft, initialDraft } = state;
  if (draft === undefined || initialDraft === undefined) return false;
  return draft.content !== initialDraft.content
    || draft.evidenceSummary !== initialDraft.evidenceSummary
    || draft.risk !== initialDraft.risk
    || draft.rollbackPlan !== initialDraft.rollbackPlan
    || draft.resolvedConflictIds.length !== initialDraft.resolvedConflictIds.length;
};

const riskLevelLabel = (riskLevel: ProposalRiskLevel): string => ({
  CRITICAL: "严重",
  HIGH: "高",
  MEDIUM: "中",
  LOW: "低",
})[riskLevel];

const shortHash = (value: string): string => `${value.slice(0, 10)}...`;

const conflictIDsFromProblem = (error: BusinessApiError): string[] => {
  const value = error.details?.conflict_ids;
  if (!Array.isArray(value)) return [];
  const ids: string[] = [];
  for (const item of value as unknown[]) {
    if (typeof item === "string") ids.push(item);
  }
  return ids;
};

const proposalRevisionAuthorityConflictCodes = new Set([
	"PROPOSAL_REVISION_STALE",
	"PROPOSAL_REVISION_NOT_EDITABLE",
	"PROPOSAL_REVISION_SIDE_EFFECT_STARTED",
	"PROPOSAL_REVISION_WORKFLOW_ACTIVE",
	"PROPOSAL_REVISION_AUTHORIZATION_CONFLICT",
]);

export interface ProposalRevisionWorkbenchProps {
  binding: ProposalRevisionPreviewBinding;
  authorityEditable: boolean;
  evidenceSummary: string;
  risk: string;
  rollbackPlan: string;
  riskLevel: ProposalRiskLevel;
  onClose: () => void;
  onRevisionCreated: (result: AppendProposalRevisionResult) => Promise<void> | void;
  onAuthorityStale: () => Promise<void> | void;
}

export const ProposalRevisionWorkbench = ({
  binding,
  authorityEditable,
  evidenceSummary,
  risk,
  rollbackPlan,
  riskLevel,
  onClose,
  onRevisionCreated,
  onAuthorityStale,
}: ProposalRevisionWorkbenchProps) => {
  const [state, dispatch] = useReducer(proposalRevisionWorkbenchReducer, undefined, initialProposalRevisionWorkbenchState);
  const [closeDialogOpen, setCloseDialogOpen] = useState(false);
  const [abandonDialogOpen, setAbandonDialogOpen] = useState(false);
  const [diffError, setDiffError] = useState("");
  const previewControllerRef = useRef<AbortController | undefined>(undefined);
  const previewRequestTokenRef = useRef(0);
  const errorRef = useRef<HTMLDivElement>(null);
  const conflictRefs = useRef(new Map<string, HTMLElement>());
  const metadata = useMemo(() => ({ evidenceSummary, risk, rollbackPlan }), [evidenceSummary, risk, rollbackPlan]);

  const loadPreview = (restart: boolean): void => {
    if (restart && !authorityEditable) return;
    previewControllerRef.current?.abort();
    const controller = new AbortController();
    const requestToken = previewRequestTokenRef.current + 1;
    previewRequestTokenRef.current = requestToken;
    previewControllerRef.current = controller;
    dispatch({ type: "PREPARE", restart });
    void previewProposalRevision(binding, controller.signal).then(
      (preview) => {
        if (previewRequestTokenRef.current === requestToken) {
          dispatch({ type: "PREVIEW_SUCCESS", preview, metadata });
        }
      },
      (error: unknown) => {
        if (previewRequestTokenRef.current !== requestToken || error instanceof Error && error.name === "AbortError") return;
        dispatch({
          type: "PREVIEW_FAILURE",
          message: error instanceof Error ? error.message : "三方合并预览读取失败",
          ...(error instanceof BusinessApiError && error.errorCode !== undefined ? { errorCode: error.errorCode } : {}),
        });
      },
    );
  };

  useEffect(() => {
    loadPreview(false);
    return () => {
      previewRequestTokenRef.current += 1;
      previewControllerRef.current?.abort();
    };
  }, []);

  const previewBindingChanged = state.preview !== undefined && (
    state.preview.proposalVersion !== binding.expectedProposalVersion
    || state.preview.sourceRevisionId !== binding.sourceRevisionId
    || state.preview.sourceChangeHash !== binding.sourceChangeHash
  );
  useEffect(() => {
    if (
      authorityEditable && !previewBindingChanged
      || state.phase !== "active_clean" && state.phase !== "active_conflicts"
    ) return;
    dispatch({
      type: "STALE",
		errorCode: authorityEditable ? "PROPOSAL_REVISION_STALE" : "PROPOSAL_REVISION_NOT_EDITABLE",
      message: authorityEditable
        ? "Proposal 已产生新的权威版本；上次编辑仍保留，但旧绑定不能提交。"
        : "Proposal 当前已不可编辑；上次编辑仍保留，但旧绑定不能提交。",
    });
  }, [authorityEditable, previewBindingChanged, state.phase]);

  const draftChanged = proposalRevisionDraftChanged(state);
  const createsRevision = state.preview !== undefined && state.draft !== undefined && (
    draftChanged || state.preview.base.hash !== state.preview.current.hash || state.preview.candidate.hash !== state.preview.proposed.hash
  );
  const unsaved = state.phase === "delivery_unknown" || state.phase === "stale" || state.phase === "restart_review" || createsRevision;
  useEffect(() => {
    if (!unsaved) return undefined;
    const handleBeforeUnload = (event: BeforeUnloadEvent): void => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", handleBeforeUnload);
    return () => window.removeEventListener("beforeunload", handleBeforeUnload);
  }, [unsaved]);

  useEffect(() => {
    if (state.message === "") return;
    errorRef.current?.focus();
  }, [state.message]);

  const active = state.phase === "active_clean" || state.phase === "active_conflicts";
  const unresolvedConflictIds = state.preview === undefined || state.draft === undefined
    ? []
    : state.preview.conflicts.filter((conflict) => !state.draft?.resolvedConflictIds.includes(conflict.id)).map((conflict) => conflict.id);
  const draftByteSize = useMemo(
    () => state.draft === undefined ? 0 : utf8Encoder.encode(state.draft.content).byteLength,
    [state.draft?.content],
  );
  const draftValid = state.draft !== undefined
    && state.draft.content.trim() !== ""
    && draftByteSize <= proposalRevisionMaxBytes
    && [state.draft.evidenceSummary, state.draft.risk, state.draft.rollbackPlan].every((value) => value.trim() !== "" && utf8Encoder.encode(value).byteLength <= proposalRevisionMetadataMaxBytes);
  const canSubmit = active && state.editorStatus === "ready" && diffError === "" && draftValid && createsRevision && unresolvedConflictIds.length === 0;

  const syncCreatedRevision = async (result: AppendProposalRevisionResult): Promise<void> => {
    dispatch({ type: "SUCCESS_SYNC_START" });
    try {
      await onRevisionCreated(result);
      dispatch({ type: "SUCCESS_SYNC_DONE" });
    } catch (error: unknown) {
      dispatch({
        type: "SUCCESS_SYNC_FAILURE",
        message: error instanceof Error ? error.message : "最新 Proposal 事实刷新失败",
      });
    }
  };

  const runSubmission = async (submission: ProposalRevisionSubmission): Promise<void> => {
    let result: AppendProposalRevisionResult;
    try {
      result = await appendProposalRevision(binding.workspaceId, binding.proposalId, submission.idempotencyKey, submission.input);
    } catch (error: unknown) {
			if (error instanceof BusinessApiError && error.errorCode !== undefined && proposalRevisionAuthorityConflictCodes.has(error.errorCode)) {
			dispatch({ type: "STALE", message: error.message, errorCode: error.errorCode });
        try {
          await onAuthorityStale();
        } catch {
				dispatch({ type: "STALE", message: `${error.message}；最新权威事实读取失败，请稍后重试。`, errorCode: error.errorCode });
        }
        return;
      }
      if (error instanceof BusinessApiError && error.errorCode === "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED") {
        const conflictIds = conflictIDsFromProblem(error);
        dispatch({
          type: "KNOWN_ERROR",
          message: error.message,
          errorCode: error.errorCode,
          ...(conflictIds[0] === undefined ? {} : { focusConflictId: conflictIds[0] }),
        });
        if (conflictIds[0] !== undefined) queueMicrotask(() => conflictRefs.current.get(conflictIds[0] ?? "")?.focus());
        return;
      }
      if (error instanceof BusinessApiError && (error.code === "NETWORK_ERROR" || error.status !== undefined && error.status >= 500)) {
        dispatch({
          type: "DELIVERY_UNKNOWN",
          message: "请求结果尚未确认。只能使用同一正文和 Idempotency-Key 重试，或明确放弃本次命令。",
          ...(error.errorCode === undefined ? {} : { errorCode: error.errorCode }),
        });
        return;
      }
      dispatch({
        type: "KNOWN_ERROR",
        message: error instanceof Error ? error.message : "创建 Revision 失败",
        ...(error instanceof BusinessApiError && error.errorCode !== undefined ? { errorCode: error.errorCode } : {}),
      });
      return;
    }
    dispatch({ type: "SUCCESS", result });
    void syncCreatedRevision(result);
  };

  const submit = (): void => {
    if (!canSubmit || state.preview === undefined || state.draft === undefined) return;
    const submission: ProposalRevisionSubmission = {
      idempotencyKey: createProposalRevisionIdempotencyKey(),
      input: {
        expectedProposalVersion: state.preview.proposalVersion,
        sourceRevisionId: state.preview.sourceRevisionId,
        sourceRevisionNo: state.preview.sourceRevisionNo,
        sourceChangeHash: state.preview.sourceChangeHash,
        expectedCurrentHash: state.preview.current.hash,
        mergeFingerprint: state.preview.mergeFingerprint,
        mergeAlgorithm: state.preview.mergeAlgorithm,
        mergeAlgorithmVersion: state.preview.mergeAlgorithmVersion,
        content: state.draft.content,
        evidenceSummary: state.draft.evidenceSummary,
        risk: state.draft.risk,
        rollbackPlan: state.draft.rollbackPlan,
        resolvedConflictIds: state.preview.conflicts.map((conflict) => conflict.id),
      },
    };
    dispatch({ type: "SUBMIT", submission });
    void runSubmission(submission);
  };

  const requestClose = (): void => {
    if (unsaved) setCloseDialogOpen(true);
    else onClose();
  };

  if (state.phase === "preparing") {
    return <section className="proposal-revision-workbench proposal-revision-workbench--loading" aria-live="polite">
      <p className="eyebrow">Proposal Revision</p>
      <h2>{state.previousDraft === undefined ? "正在生成三方合并预览..." : "正在读取最新权威版本..."}</h2>
      <Button variant="ghost" onClick={requestClose}><X size={16} />取消</Button>
    </section>;
  }

  if (state.phase === "preview_error") {
    return <section className="proposal-revision-workbench">
      <div className="proposal-revision-alert proposal-revision-alert--error" role="alert" tabIndex={-1} ref={errorRef}>
        <AlertTriangle size={18} /><div><strong>无法准备 Revision</strong><p>{state.message}</p></div>
      </div>
      <div className="proposal-revision-actions">
        <Button variant="secondary" onClick={requestClose}><X size={16} />返回审阅</Button>
        <Button onClick={() => loadPreview(false)}><RotateCcw size={16} />重试预览</Button>
      </div>
    </section>;
  }

  const createdRevision = state.success;
  if (state.phase === "success" && createdRevision !== undefined) {
    return <section className="proposal-revision-workbench">
      <div className="proposal-revision-alert proposal-revision-alert--success" role="status">
        <Check size={19} /><div><strong>Revision #{createdRevision.proposal.revision.revisionNo} 已创建</strong><p>新版本已回到待审状态，需要重新审批；尚未执行 Preflight、Workspace 写入或 Git 写回。</p></div>
      </div>
      {state.successSyncStatus === "failed" ? <div className="proposal-revision-alert proposal-revision-alert--warning" role="alert">
        <AlertTriangle size={18} /><div><strong>页面事实刷新失败</strong><p>{state.message}。Revision 已确定创建，不要再次提交创建命令。</p></div>
      </div> : null}
      <div className="proposal-revision-actions">
        {state.successSyncStatus === "failed" ? <Button variant="secondary" onClick={() => void syncCreatedRevision(createdRevision)}><RotateCcw size={16} />重新读取最新事实</Button> : null}
        <Button onClick={onClose} disabled={state.successSyncStatus === "syncing"}><Check size={16} />{state.successSyncStatus === "syncing" ? "正在刷新最新事实..." : "返回最新审阅"}</Button>
      </div>
    </section>;
  }

  const preview = state.preview;
  const draft = state.draft;
  if (preview === undefined || draft === undefined) return null;

  if (state.phase === "restart_review" && state.previousDraft !== undefined && state.freshDraft !== undefined) {
    const identity = `${preview.mergeFingerprint}:restart-review`;
    return <section className="proposal-revision-workbench">
      <header className="proposal-revision-header">
        <div><p className="eyebrow">重新合并核对</p><h2>上次编辑与最新候选</h2></div>
        <Badge tone="warning">需明确选择</Badge>
      </header>
      <div className="proposal-revision-diff-shell">
        <Suspense fallback={<p className="proposal-revision-loading">正在加载差异...</p>}>
          <MonacoDiffViewer
            identityKey={identity}
            original={state.freshDraft.content}
            modified={state.previousDraft.content}
            originalModelPath={`inmemory://zhixu/workspaces/${binding.workspaceId}/proposals/${binding.proposalId}/revisions/${preview.sourceRevisionId}/${preview.mergeFingerprint}/fresh-candidate.md`}
            modifiedModelPath={`inmemory://zhixu/workspaces/${binding.workspaceId}/proposals/${binding.proposalId}/revisions/${preview.sourceRevisionId}/${preview.mergeFingerprint}/previous-draft.md`}
            language="markdown"
            height="440px"
            onReady={() => setDiffError("")}
            onError={(error) => setDiffError(error.message)}
          />
        </Suspense>
      </div>
      {diffError !== "" ? <div className="proposal-revision-alert proposal-revision-alert--error" role="alert"><AlertTriangle size={18} /><p>{diffError}</p></div> : null}
      <div className="proposal-revision-actions">
        <Button variant="secondary" onClick={() => dispatch({ type: "KEEP_PREVIOUS" })}>保留上次编辑</Button>
        <Button onClick={() => dispatch({ type: "ADOPT_FRESH" })}><RotateCcw size={16} />采用最新候选</Button>
      </div>
    </section>;
  }

  const comparison = state.selectedTab === "base-current"
    ? { label: "基线到当前 Workspace", modified: preview.current, role: "current" }
    : { label: "基线到原提案", modified: preview.proposed, role: "proposed" };
  const diffIdentity = `${preview.mergeFingerprint}:${comparison.role}`;
  const editorDisabled = !active;
  const deliverySubmission = state.phase === "delivery_unknown" ? state.submission : undefined;
	const errorTitle = state.phase === "delivery_unknown"
		? "请求结果未知"
		: state.phase !== "stale"
			? "无法创建 Revision"
			: state.errorCode === "PROPOSAL_REVISION_STALE"
				? "版本已变化"
				: "当前版本不可替换";

  return <section className="proposal-revision-workbench" aria-label="Proposal Revision 合并工作台">
    <header className="proposal-revision-header">
      <div><p className="eyebrow">Proposal Revision</p><h2>Revision #{preview.sourceRevisionNo} -&gt; #{preview.sourceRevisionNo + 1}</h2></div>
      <div className="proposal-revision-header__status">
        <Badge tone={preview.conflictCount === 0 ? "success" : "danger"}>{preview.conflictCount === 0 ? "无冲突" : `${String(preview.conflictCount)} 处冲突`}</Badge>
        {draftChanged ? <Badge tone="warning">本地已编辑</Badge> : null}
      </div>
    </header>

    {state.message !== "" ? <div
      className={`proposal-revision-alert ${state.phase === "delivery_unknown" || state.phase === "stale" ? "proposal-revision-alert--warning" : "proposal-revision-alert--error"}`}
      role="alert"
      tabIndex={-1}
      ref={errorRef}
    >
      <AlertTriangle size={18} />
			<div><strong>{errorTitle}</strong><p>{state.message}</p></div>
    </div> : null}

    {deliverySubmission !== undefined ? <div className="proposal-revision-recovery">
      <Button onClick={() => void runSubmission(deliverySubmission)}><RotateCcw size={16} />原样重试</Button>
      <Button variant="secondary" onClick={() => setAbandonDialogOpen(true)}>放弃本次命令</Button>
    </div> : null}
    {state.phase === "stale" ? <div className="proposal-revision-recovery">
      {authorityEditable ? <Button onClick={() => loadPreview(true)}><GitCompareArrows size={16} />基于最新内容重新合并</Button> : null}
      <Button variant="secondary" onClick={requestClose}>返回审阅</Button>
    </div> : null}

    <ol className="proposal-revision-provenance" aria-label="Revision 来源轨迹">
      <li><span>基线</span><strong title={preview.base.hash}>{shortHash(preview.base.hash)}</strong><small>{String(preview.base.byteSize)} bytes</small></li>
      <li><span>当前 Workspace</span><strong title={preview.current.hash}>{shortHash(preview.current.hash)}</strong><small>{preview.base.hash === preview.current.hash ? "与基线一致" : "已发生变化"}</small></li>
      <li><span>原提案 #{preview.sourceRevisionNo}</span><strong title={preview.sourceChangeHash}>{shortHash(preview.sourceChangeHash)}</strong><small>{String(preview.proposed.byteSize)} bytes</small></li>
      <li><span>待提交 Revision #{preview.sourceRevisionNo + 1}</span><strong>{draftChanged ? "本地已编辑" : "服务端候选"}</strong><small>{String(draftByteSize)} bytes</small></li>
    </ol>

    <Tabs value={state.selectedTab} onValueChange={(value) => dispatch({ type: "SELECT_TAB", tab: value as ProposalRevisionComparisonTab })}>
      <TabsList className="proposal-revision-tabs" aria-label="Revision 比较视图">
        <TabsTrigger value="base-current">基线 -&gt; 当前</TabsTrigger>
        <TabsTrigger value="base-proposed">基线 -&gt; 原提案</TabsTrigger>
        <TabsTrigger value="conflicts">冲突 ({preview.conflictCount})</TabsTrigger>
      </TabsList>
      <TabsContent value="base-current" />
      <TabsContent value="base-proposed" />
      <TabsContent value="conflicts" />
    </Tabs>

    {state.selectedTab === "conflicts" ? <div className="proposal-revision-conflicts" aria-label="冲突列表">
      {preview.conflicts.length === 0 ? <div className="proposal-revision-empty"><Check size={18} /><p>服务端没有返回冲突区域。</p></div> : preview.conflicts.map((conflict) => {
        const checked = draft.resolvedConflictIds.includes(conflict.id);
        return <article
          className="proposal-revision-conflict"
          key={conflict.id}
          tabIndex={-1}
          ref={(element) => { if (element === null) conflictRefs.current.delete(conflict.id); else conflictRefs.current.set(conflict.id, element); }}
        >
          <header><strong>冲突 {conflict.ordinal}</strong><Badge tone={checked ? "success" : "danger"}>{checked ? "已确认处理" : "待处理"}</Badge></header>
          <div className="proposal-revision-conflict__sources">
            <div><span>Base</span><pre>{conflict.base || "(空)"}</pre></div>
            <div><span>Current</span><pre>{conflict.current || "(空)"}</pre></div>
            <div><span>Proposed</span><pre>{conflict.proposed || "(空)"}</pre></div>
          </div>
          <label className="proposal-revision-conflict__confirm"><input
            type="checkbox"
            checked={checked}
            disabled={editorDisabled}
            onChange={(event) => dispatch({ type: "TOGGLE_CONFLICT", conflictId: conflict.id, resolved: event.target.checked })}
          />我已在待提交正文中处理此冲突</label>
        </article>;
      })}
    </div> : <div className="proposal-revision-diff-shell" aria-label={comparison.label}>
      <Suspense fallback={<p className="proposal-revision-loading">正在加载差异...</p>}>
        <MonacoDiffViewer
          identityKey={diffIdentity}
          original={preview.base.content}
          modified={comparison.modified.content}
          originalModelPath={`inmemory://zhixu/workspaces/${binding.workspaceId}/proposals/${binding.proposalId}/revisions/${preview.sourceRevisionId}/${preview.mergeFingerprint}/base.md`}
          modifiedModelPath={`inmemory://zhixu/workspaces/${binding.workspaceId}/proposals/${binding.proposalId}/revisions/${preview.sourceRevisionId}/${preview.mergeFingerprint}/${comparison.role}.md`}
          language="markdown"
          height="440px"
          onReady={() => setDiffError("")}
          onError={(error) => setDiffError(error.message)}
        />
      </Suspense>
    </div>}
    {diffError !== "" ? <div className="proposal-revision-alert proposal-revision-alert--error" role="alert"><AlertTriangle size={18} /><p>{diffError}</p></div> : null}

    <div className="proposal-revision-editor-heading">
      <div><p className="eyebrow">待提交正文</p><h3>Revision #{preview.sourceRevisionNo + 1}</h3></div>
      <span>{String(draftByteSize)} / {String(proposalRevisionMaxBytes)} bytes</span>
    </div>
    <div className="proposal-revision-editor-shell">
      <MonacoTextEditor
        ariaLabel="待提交 Revision 正文"
        value={draft.content}
        modelPath={`inmemory://zhixu/workspaces/${binding.workspaceId}/proposals/${binding.proposalId}/revisions/${preview.sourceRevisionId}/${preview.mergeFingerprint}/candidate.md`}
        language="markdown"
        height="440px"
        disabled={editorDisabled}
        onChange={(value) => dispatch({ type: "EDIT", field: "content", value })}
        onReady={() => dispatch({ type: "EDITOR_READY" })}
        onError={(error) => dispatch({ type: "EDITOR_ERROR", message: error.message })}
      />
    </div>

    <div className="proposal-revision-form">
      <label><span>Evidence Summary</span><textarea rows={3} maxLength={proposalRevisionMetadataMaxBytes} value={draft.evidenceSummary} disabled={editorDisabled} onChange={(event) => dispatch({ type: "EDIT", field: "evidenceSummary", value: event.target.value })} /></label>
      <div className="proposal-revision-form__risk">
        <label><span>风险等级</span><output><Badge tone={riskLevel === "CRITICAL" || riskLevel === "HIGH" ? "danger" : riskLevel === "MEDIUM" ? "warning" : "neutral"}>{riskLevelLabel(riskLevel)}</Badge></output></label>
        <label><span>风险说明</span><textarea rows={3} maxLength={proposalRevisionMetadataMaxBytes} value={draft.risk} disabled={editorDisabled} onChange={(event) => dispatch({ type: "EDIT", field: "risk", value: event.target.value })} /></label>
      </div>
      <label><span>回滚计划</span><textarea rows={3} maxLength={proposalRevisionMetadataMaxBytes} value={draft.rollbackPlan} disabled={editorDisabled} onChange={(event) => dispatch({ type: "EDIT", field: "rollbackPlan", value: event.target.value })} /></label>
    </div>

    {!createsRevision && active ? <p className="proposal-revision-note" role="status">正文和元数据与当前 Revision 相同，暂无需要创建的新版本。</p> : null}
    {unresolvedConflictIds.length > 0 && active ? <p className="proposal-revision-note proposal-revision-note--warning" role="status">仍有 {unresolvedConflictIds.length} 处冲突未确认处理。</p> : null}
    <div className="proposal-revision-actions">
      <Button variant="secondary" onClick={requestClose} disabled={state.phase === "submitting"}><X size={16} />取消</Button>
      <Button onClick={submit} disabled={!canSubmit || state.phase === "submitting"}><Save size={16} />{state.phase === "submitting" ? "正在检查并创建..." : `检查并创建 Revision #${String(preview.sourceRevisionNo + 1)}`}</Button>
    </div>

    <Dialog
      open={closeDialogOpen}
      onOpenChange={setCloseDialogOpen}
      title="放弃未提交的 Revision？"
      description="关闭后本次候选正文和元数据不会保存在浏览器中。"
    >
      <div className="dialog-actions"><Button variant="secondary" onClick={() => setCloseDialogOpen(false)}>继续编辑</Button><Button variant="danger" onClick={onClose}>放弃并关闭</Button></div>
    </Dialog>
    <Dialog
      open={abandonDialogOpen}
      onOpenChange={setAbandonDialogOpen}
      title="放弃结果未知的命令？"
      description="继续编辑会生成新的逻辑命令；原命令仍可能已被服务端接收，页面不会推断其失败。"
    >
      <div className="dialog-actions"><Button variant="secondary" onClick={() => setAbandonDialogOpen(false)}>保留原样重试</Button><Button variant="danger" onClick={() => { setAbandonDialogOpen(false); dispatch({ type: "RESUME_AFTER_UNKNOWN" }); }}>放弃并继续编辑</Button></div>
    </Dialog>
  </section>;
};
