import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, BookUp, CalendarDays, Check, Clock3, FileDiff, GitMerge, RotateCcw, X } from "lucide-react";
import { Component, lazy, Suspense, useEffect, useRef, useState, type ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";

import {
  BusinessApiError,
  decideProposal,
  getProposal,
  getProposalCurrentContent,
  listProposals,
  preflightProposal,
  proposalRiskLevels,
  type ProposalDetail,
  type ProposalRiskLevel,
} from "../../api/business";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import {
  Badge,
  Button,
  Card,
  CardHeader,
  Dialog,
  EmptyState,
  ErrorState,
  UnavailableState,
} from "../../shared/ui";
import { canonicalLocalDate, formatElapsedDuration, localDateStartRfc3339 } from "../../shared/time";
import { BusinessWorkspaceGate } from "./BusinessWorkspaceGate";
import { useScopedCursor } from "./pagination";
import { parseProposalUrlState, proposalStatusOptions, writeProposalUrlState, type ProposalUrlState } from "./url-state";

const MonacoDiffViewer = lazy(() => import("./MonacoDiffViewer").then((module) => ({ default: module.MonacoDiffViewer })));
type ProposalDecisionAction = "approved" | "rejected" | "redispatch";
type DiffLoadStatus = "idle" | "loading" | "ready" | "error";
interface ProposalDecisionSnapshot {
  action: ProposalDecisionAction;
  proposalType: ProposalDetail["type"];
  revisionId: string;
  changeHash: string;
  riskLevel: ProposalRiskLevel;
}
type FilePatchProposal = Extract<ProposalDetail, { type: "file_patch" }>;
interface DiffLoadState {
  identity: string;
  status: DiffLoadStatus;
  message: string;
}
interface DiffViewerErrorBoundaryProps {
  identity: string;
  onError: (error: Error) => void;
  children: ReactNode;
}
interface DiffViewerErrorBoundaryState {
  identity: string;
  error: Error | null;
}

const currentContentPrefix = (workspaceId: string, proposalId: string) =>
  ["business", workspaceId, "proposal-current-content", proposalId] as const;

const currentContentKey = (workspaceId: string, proposalId: string, proposal: FilePatchProposal) =>
  [...currentContentPrefix(workspaceId, proposalId), proposal.revision.id, proposal.targetPath, proposal.revision.baseHash] as const;

const readCurrentContent = (workspaceId: string, proposalId: string, proposal: FilePatchProposal, signal?: AbortSignal) =>
  getProposalCurrentContent(workspaceId, proposalId, {
    targetPath: proposal.targetPath,
    baseHash: proposal.revision.baseHash,
  }, signal);

class DiffViewerErrorBoundary extends Component<DiffViewerErrorBoundaryProps, DiffViewerErrorBoundaryState> {
  override state: DiffViewerErrorBoundaryState = { identity: this.props.identity, error: null };

  static getDerivedStateFromProps(props: DiffViewerErrorBoundaryProps, state: DiffViewerErrorBoundaryState): Partial<DiffViewerErrorBoundaryState> | null {
    return props.identity === state.identity ? null : { identity: props.identity, error: null };
  }

  static getDerivedStateFromError(error: unknown): Partial<DiffViewerErrorBoundaryState> {
    return { error: error instanceof Error ? error : new Error("Diff Viewer 加载失败") };
  }

  override componentDidCatch(error: Error): void {
    this.props.onError(error);
  }

  override render() {
    return this.state.error === null ? this.props.children : null;
  }
}

const readText = (value: unknown, fallback = "未提供"): string =>
  typeof value === "string" && value.trim() !== "" ? value : fallback;

const riskLevelLabel = (riskLevel: ProposalRiskLevel): string => ({
  CRITICAL: "严重",
  HIGH: "高",
  MEDIUM: "中",
  LOW: "低",
})[riskLevel];

const riskLevelTone = (riskLevel: ProposalRiskLevel): "danger" | "warning" | "neutral" =>
  riskLevel === "CRITICAL" || riskLevel === "HIGH" ? "danger" : riskLevel === "MEDIUM" ? "warning" : "neutral";

const isHighRiskLevel = (riskLevel: ProposalRiskLevel): boolean => riskLevel === "CRITICAL" || riskLevel === "HIGH";

const proposalTypeLabel = (type: ProposalDetail["type"]): string => ({
  file_patch: "文件变更",
  knowledge_change: "知识关系变更",
  publish_artifact: "Artifact 发布",
  downstream_update: "下游更新",
})[type];

export const ProposalsPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params, setParams] = useSearchParams();
  const urlState = parseProposalUrlState(params);
  const canonicalSearch = writeProposalUrlState(urlState).toString();
  useEffect(() => {
    if (params.toString() !== canonicalSearch) setParams(new URLSearchParams(canonicalSearch), { replace: true });
  }, [canonicalSearch, params, setParams]);
  const { status, type, risk, createdDate } = urlState;
  const createdAfter = createdDate === "" ? undefined : localDateStartRfc3339(createdDate);
  const [cursor, setCursor] = useScopedCursor([
    workspaceId,
    status,
    type,
    risk,
    createdAfter ?? "",
  ]);
  const query = useQuery({
    queryKey: ["business", workspaceId, "proposals", status, type, risk, createdAfter, cursor],
    queryFn: ({ signal }) => listProposals(workspaceId, { limit: 30, ...(cursor ? { cursor } : {}), ...(status ? { status } : {}), ...(type ? { type } : {}), ...(risk ? { risk } : {}), ...(createdAfter ? { createdAfter } : {}) }, signal),
    enabled: Boolean(workspaceId),
    retry: false,
  });
  const updateFilter = (updates: Partial<ProposalUrlState>) => {
    setCursor("");
    setParams(writeProposalUrlState({ ...urlState, ...updates }));
  };

  if (!workspaceId) return <BusinessWorkspaceGate description="Proposal 队列必须绑定已连接 Workspace；未连接时不会发出列表请求或显示无限加载。" />;

  return <div className="page-stack">
    <div className="page-intro">
      <h1>提案</h1>
      <p>审阅并决定待处理变更。</p>
    </div>
    <Card>
      <div className="filter-bar" aria-label="Proposal 筛选">
        <label>状态<select value={status} onChange={(event) => updateFilter({ status: event.target.value as ProposalUrlState["status"] })}><option value="">全部</option>{proposalStatusOptions.map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label>
        <label>类型<select value={type} onChange={(event) => updateFilter({ type: event.target.value as ProposalUrlState["type"] })}><option value="">全部</option><option value="file_patch">文件变更</option><option value="knowledge_change">知识关系</option><option value="publish_artifact">Artifact 发布</option><option value="downstream_update">下游更新</option></select></label>
        <label>风险等级<select value={risk} onChange={(event) => updateFilter({ risk: event.target.value as ProposalUrlState["risk"] })}><option value="">全部</option>{proposalRiskLevels.map((riskLevel) => <option value={riskLevel} key={riskLevel}>{riskLevelLabel(riskLevel)}</option>)}</select></label>
        <label>创建日期起<input type="date" value={canonicalLocalDate(createdDate) ?? ""} onChange={(event) => updateFilter({ createdDate: canonicalLocalDate(event.target.value) ?? "" })} /></label>
      </div>
      <CardHeader eyebrow="待审队列" title={query.isPending ? "读取中…" : `${String(query.data?.items.length ?? 0)} 条 Proposal`} />
      {query.isError
        ? <UnavailableState title="Proposal 列表不可用" description={query.error.message} />
        : query.data?.items.length === 0
          ? <EmptyState title="没有可审变更" description="真实 Proposal 会按更新时间倒序出现在这里。" />
          : <div className="proposal-list">{query.data?.items.map((item) => <Link to={`/proposals/${item.id}`} className="proposal-row" key={item.id}>
            <div className="proposal-row__kind">
              <span className={`kind-mark kind-mark--${item.type}`}>{item.type === "file_patch" ? <FileDiff size={16} /> : item.type === "knowledge_change" ? <GitMerge size={16} /> : item.type === "publish_artifact" ? <BookUp size={16} /> : <RotateCcw size={16} />}</span>
              <div><strong>{item.type === "publish_artifact" ? "Artifact 发布" : item.type === "downstream_update" ? "下游更新" : item.target}</strong><small>{proposalTypeLabel(item.type)} · <CalendarDays size={12} /> {new Date(item.createdAt).toLocaleString("zh-CN")} · <Clock3 size={12} /> {item.status === "ready_for_review" ? `等待 ${formatElapsedDuration(item.createdAt)}` : `存在 ${formatElapsedDuration(item.createdAt)}`}</small></div>
            </div>
            <div className="proposal-row__meta">
              <Badge tone={item.status === "ready_for_review" ? "warning" : item.status === "needs_revision" ? "danger" : "neutral"}>{item.status}</Badge>
              <Badge tone={riskLevelTone(item.riskLevel)}>风险等级 {item.riskLevel}</Badge>
              <span className="risk-copy" title={item.risk}>风险说明：{item.risk}</span>
              {item.approval ? <Badge tone={item.approval.decision === "approved" ? "success" : "danger"}>{item.approval.decision}</Badge> : null}
              {item.approval?.workflowRunId ? <span className="mono">Workflow {item.approval.workflowRunId.slice(0, 8)}…</span> : null}
              {item.approval?.writebackState === "pending_dispatch" ? <Badge tone="warning">待恢复写回</Badge> : null}
              {item.approval?.writebackState === "legacy_unrecoverable" ? <Badge tone="danger">历史审批缺少 Git 基线</Badge> : null}
            </div>
          </Link>)}</div>}
      <div className="pagination-row">{cursor ? <Button variant="ghost" onClick={() => setCursor("")}>返回首屏</Button> : <span />}{query.data?.nextCursor ? <Button variant="secondary" onClick={() => setCursor(query.data.nextCursor ?? "")}>下一页</Button> : null}</div>
    </Card>
  </div>;
};

export const ProposalDetailPage = () => {
  const { proposalId = "" } = useParams();
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState<ProposalDecisionSnapshot>();
  const [diffLoadState, setDiffLoadState] = useState<DiffLoadState>({ identity: "", status: "idle", message: "" });
  const [diffRetryNonce, setDiffRetryNonce] = useState(0);
  const decisionTriggerRef = useRef<HTMLButtonElement>(null);
  const proposalQueryKey = ["business", workspaceId, "proposal", proposalId] as const;
  const proposalQuery = useQuery({
    queryKey: proposalQueryKey,
    queryFn: ({ signal }) => getProposal(workspaceId, proposalId, signal),
    enabled: Boolean(workspaceId && proposalId),
    retry: false,
    gcTime: 0,
  });
  const proposal = proposalQuery.data;
  const confirmMatchesProposal = confirm !== undefined
    && confirm.proposalType === proposal?.type
    && confirm.revisionId === proposal.revision.id
    && confirm.changeHash === proposal.revision.changeHash
    && confirm.riskLevel === proposal.riskLevel;
  useEffect(() => {
    if (confirm !== undefined && !confirmMatchesProposal) setConfirm(undefined);
  }, [confirm, confirmMatchesProposal]);
  const type = proposal?.type ?? "file_patch";
  const fileProposal: FilePatchProposal | undefined = proposal?.type === "file_patch" ? proposal : undefined;
  const proposalCurrentContentKey = fileProposal === undefined
    ? [...currentContentPrefix(workspaceId, proposalId), "unbound"] as const
    : currentContentKey(workspaceId, proposalId, fileProposal);
  const currentContentQuery = useQuery({
    queryKey: proposalCurrentContentKey,
    queryFn: ({ signal }) => {
      if (fileProposal === undefined) throw new Error("File Patch Proposal 绑定不完整");
      return readCurrentContent(workspaceId, proposalId, fileProposal, signal);
    },
    enabled: Boolean(workspaceId && proposalId && fileProposal),
    retry: false,
    gcTime: 0,
  });
  const diffIdentity = fileProposal !== undefined && currentContentQuery.data !== undefined
    ? `${workspaceId}:${proposalId}:${fileProposal.revision.id}:${fileProposal.revision.baseHash}:${fileProposal.revision.changeHash}:${currentContentQuery.data.currentHash}`
    : "";
  useEffect(() => {
    setDiffLoadState((current) => {
      if (diffIdentity === "") return current.identity === "" && current.status === "idle" ? current : { identity: "", status: "idle", message: "" };
      if (current.identity === diffIdentity && current.status !== "idle") return current;
      return { identity: diffIdentity, status: "loading", message: "" };
    });
  }, [diffIdentity]);
  const refetchProposalFacts = async () => {
    const previousCurrentContentKey = proposalCurrentContentKey;
    const result = await proposalQuery.refetch();
    const freshProposal = result.data;
    if (result.isError || freshProposal?.type !== "file_patch") return;
    const freshCurrentContentKey = currentContentKey(workspaceId, proposalId, freshProposal);
    if (JSON.stringify(previousCurrentContentKey) !== JSON.stringify(freshCurrentContentKey)) {
      await queryClient.cancelQueries({ queryKey: previousCurrentContentKey, exact: true });
      queryClient.removeQueries({ queryKey: previousCurrentContentKey, exact: true });
    }
    await queryClient.fetchQuery({
      queryKey: freshCurrentContentKey,
      queryFn: ({ signal }) => readCurrentContent(workspaceId, proposalId, freshProposal, signal),
      staleTime: 0,
      gcTime: 0,
    });
  };
  const mutation = useMutation({
    mutationFn: async (snapshot: ProposalDecisionSnapshot) => {
      const decision = snapshot.action === "redispatch" ? "approved" : snapshot.action;
      return decideProposal(proposalId, {
        revisionId: snapshot.revisionId,
        changeHash: snapshot.changeHash,
        decision,
        proposalType: snapshot.proposalType,
      });
    },
    onSuccess: async (_result, snapshot) => {
      setConfirm(undefined);
      const queryKeys: (readonly unknown[])[] = [
        ["business", workspaceId, "proposals"],
        ["business", workspaceId, "workflows"],
      ];
      if (snapshot.action === "approved" && snapshot.proposalType === "knowledge_change") {
        queryKeys.push(
          ["graph", workspaceId],
          ["semantic-links", workspaceId],
          ["collections", workspaceId],
          ["knowledge-health", workspaceId],
        );
      }
      await Promise.all(queryKeys.map((queryKey) => queryClient.invalidateQueries({ queryKey })));
      await refetchProposalFacts();
    },
    onError: async (error) => {
      setConfirm(undefined);
      if (error instanceof BusinessApiError && error.status === 409) {
        await refetchProposalFacts();
      }
    },
  });
  const preflight = useMutation({
    mutationFn: async () => {
      const current = proposalQuery.data;
      if (!current || current.type !== "file_patch" || current.status !== "approved") {
        throw new Error("只有已批准的文件 Proposal 可以执行写回前检查");
      }
      return preflightProposal(proposalId, { revisionId: current.revision.id, changeHash: current.revision.changeHash });
    },
    onError: async (error) => {
      if (error instanceof BusinessApiError && error.status === 409) {
        await refetchProposalFacts();
      }
    },
  });

  if (!workspaceId) return <BusinessWorkspaceGate description="Proposal 详情必须绑定已连接 Workspace；未连接时不会读取 Revision、正文或 Approval。" />;
  if (proposalQuery.isError) {
    return <div className="page-stack"><ErrorState title="Proposal 读取失败" description={proposalQuery.error.message} onRetry={() => void proposalQuery.refetch()} /></div>;
  }
  if (proposalQuery.isPending || !proposal) {
    return <div className="page-stack"><Card><p>正在读取 Proposal 详情…</p></Card></div>;
  }

  const revision = proposal.revision;
  const fileRevision = proposal.type === "file_patch" ? proposal.revision : undefined;
  const knowledgeRevision = proposal.type === "knowledge_change" ? proposal.revision : undefined;
  const publishRevision = proposal.type === "publish_artifact" ? proposal.revision : undefined;
  const downstreamRevision = proposal.type === "downstream_update" ? proposal.revision : undefined;
  const versionBindingLabel = fileRevision ? "Base Hash" : publishRevision ? "Artifact Content Hash" : downstreamRevision ? "Target Base Version" : "版本来源";
  const versionBindingValue = fileRevision?.baseHash ?? publishRevision?.publication.contentHash ?? (downstreamRevision ? `v${String(downstreamRevision.update.baseVersion)}` : "结构化节点版本");
  const proposedContent = fileRevision?.content ?? "";
  const changeHash = revision.changeHash;
  const riskDescription = revision.risk;
  const ready = proposal.status === "ready_for_review";
  const baselineVerified = type !== "file_patch" || currentContentQuery.data?.baseHashMatch === true;
  const fileDiffReady = type !== "file_patch" || (diffIdentity !== "" && diffLoadState.identity === diffIdentity && diffLoadState.status === "ready");
  const fileDiffFailed = type === "file_patch" && diffIdentity !== "" && diffLoadState.identity === diffIdentity && diffLoadState.status === "error";
  const fileDiffPending = type === "file_patch" && currentContentQuery.data !== undefined && !fileDiffReady && !fileDiffFailed;
  const canApprove = ready && baselineVerified && !currentContentQuery.isError && fileDiffReady;
  const canReject = ready;
  const canPreflight = proposal.type === "file_patch" && proposal.status === "approved" && baselineVerified && !currentContentQuery.isError;
  const writebackState = proposal.type === "file_patch" && proposal.approval?.decision === "approved"
    ? proposal.approval.writebackState
    : undefined;
  const canRedispatch = writebackState === "pending_dispatch" && baselineVerified && !currentContentQuery.isError && fileDiffReady;
  const activeConfirm = confirmMatchesProposal ? confirm : undefined;
  const openConfirmation = (action: ProposalDecisionAction, trigger: HTMLButtonElement): void => {
    decisionTriggerRef.current = trigger;
    setConfirm({
      action,
      proposalType: proposal.type,
      revisionId: proposal.revision.id,
      changeHash: proposal.revision.changeHash,
      riskLevel: proposal.riskLevel,
    });
  };
  const preflightStillCurrent = proposal.type === "file_patch"
    && preflight.data !== undefined
    && !proposalQuery.isFetching
    && !currentContentQuery.isFetching
    && currentContentQuery.data?.baseHashMatch === true
    && preflight.data.revisionId === proposal.revision.id
    && preflight.data.changeHash === proposal.revision.changeHash
    && preflight.data.baseHash === proposal.revision.baseHash
    && currentContentQuery.data.baseHash === proposal.revision.baseHash;
  const retryDiffViewer = (): void => {
    if (diffIdentity === "") return;
    setDiffLoadState({ identity: diffIdentity, status: "loading", message: "" });
    setDiffRetryNonce((value) => value + 1);
  };

  return <div className="page-stack">
    <div className="page-intro page-intro--split">
      <div>
        <p className="eyebrow">审阅台 / {type}</p>
        <h1>{proposal.type === "file_patch" ? proposal.targetPath : proposal.type === "knowledge_change" ? "知识关系变更" : proposal.type === "publish_artifact" ? "Artifact 发布" : "下游更新意图"}</h1>
        <p>Proposal 状态：<Badge tone={proposal.status === "needs_revision" ? "danger" : ready ? "warning" : "neutral"}>{proposal.status}</Badge></p>
      </div>
      <div className="hash-card"><span>Change Hash</span><code>{changeHash.slice(0, 16)}…</code></div>
    </div>

    <div className="review-layout">
      <main>
        <Card>
          <CardHeader
            eyebrow="变更主体"
            title={type === "file_patch" ? "当前文件 → Proposal" : type === "knowledge_change" ? "Relation Diff" : type === "publish_artifact" ? "Artifact Publication Snapshot" : "Downstream Update Snapshot"}
            description={type === "file_patch" ? "当前正文由服务端安全读取；只有 current hash 与 Proposal base hash 一致时才允许批准。" : type === "knowledge_change" ? "结构化关系端点、版本和 Evidence，不伪装成 Markdown Diff。" : type === "publish_artifact" ? "冻结 Artifact、Revision、版本、内容哈希与来源覆盖；这里只审阅发布请求，不写入正式知识。" : "冻结 Impact Report、来源 Event、目标版本和 owner 绑定；Approval 只记录更新意图，不授予执行能力。"}
            action={type === "file_patch" ? <Button variant="ghost" size="sm" onClick={() => void currentContentQuery.refetch()} disabled={currentContentQuery.isFetching}><RotateCcw size={14} />重新读取</Button> : undefined}
          />
          {fileRevision ? <>
            {currentContentQuery.isPending ? <p className="skeleton-line">正在读取当前 Workspace 文件…</p> : null}
            {currentContentQuery.isError ? <ErrorState title="当前文件读取失败" description={currentContentQuery.error.message} onRetry={() => void currentContentQuery.refetch()} /> : null}
            {currentContentQuery.data && !currentContentQuery.data.baseHashMatch ? <div className="ui-state ui-state--error" role="alert"><strong>版本冲突：基线已经漂移</strong><p>当前 hash 为 <code>{currentContentQuery.data.currentHash}</code>，Proposal base hash 为 <code>{currentContentQuery.data.baseHash}</code>。批准已被禁用，请重新生成 Revision。</p></div> : null}
            {fileDiffPending ? <p className="sidebar-note" role="status">Diff Viewer 正在挂载；完成前文件批准和恢复派发保持禁用。</p> : null}
            {fileDiffFailed ? <ErrorState title="Diff Viewer 加载失败" description={diffLoadState.message || "无法加载本地 Monaco Diff Viewer；请重试或驳回该 Proposal。"} onRetry={retryDiffViewer} /> : null}
            {currentContentQuery.data && !fileDiffFailed ? <Suspense fallback={<p>正在加载 Diff Viewer…</p>}>
              <div className="monaco-diff-shell" aria-label="当前文件与 Proposal 内容差异">
                <DiffViewerErrorBoundary
                  identity={`${diffIdentity}:${String(diffRetryNonce)}`}
                  onError={(error) => setDiffLoadState({ identity: diffIdentity, status: "error", message: error.message })}
                >
                  <MonacoDiffViewer
                    identityKey={`${diffIdentity}:${String(diffRetryNonce)}`}
                    original={currentContentQuery.data.content}
                    modified={proposedContent}
                    originalModelPath={`inmemory://zhixu/workspaces/${workspaceId}/proposals/${proposalId}/revisions/${fileRevision.id}/original.md`}
                    modifiedModelPath={`inmemory://zhixu/workspaces/${workspaceId}/proposals/${proposalId}/revisions/${fileRevision.id}/modified.md`}
                    onReady={() => setDiffLoadState({ identity: diffIdentity, status: "ready", message: "" })}
                    onError={(error) => setDiffLoadState({ identity: diffIdentity, status: "error", message: error.message })}
                    language="markdown"
                  />
                </DiffViewerErrorBoundary>
              </div>
            </Suspense> : null}
          </> : knowledgeRevision ? <div className="relation-diff">
            <div><span>操作</span><strong>{knowledgeRevision.changeSet.operation}</strong></div>
            <div><span>关系类型</span><strong>{knowledgeRevision.changeSet.relationType}</strong></div>
            <div><span>来源节点</span><strong>{knowledgeRevision.changeSet.source.type}:{knowledgeRevision.changeSet.source.id} · v{knowledgeRevision.changeSet.source.version}</strong></div>
            <div><span>目标节点</span><strong>{knowledgeRevision.changeSet.target.type}:{knowledgeRevision.changeSet.target.id} · v{knowledgeRevision.changeSet.target.version}</strong></div>
            <div><span>Revision</span><strong>#{knowledgeRevision.revisionNo} · <code className="mono">{knowledgeRevision.id}</code></strong></div>
            {knowledgeRevision.targetRefs.map((targetRef, index) => <div key={`${targetRef.type}:${targetRef.id}:${targetRef.fingerprint}`}><span>目标引用 {index + 1}</span><strong>{targetRef.type}:{targetRef.id}<br /><code className="mono">fingerprint: {targetRef.fingerprint}</code></strong></div>)}
            {knowledgeRevision.baseVersions.map((baseVersion) => <div key={`${baseVersion.nodeType}-${baseVersion.nodeId}`}><span>基线版本</span><strong>{baseVersion.nodeType}:{baseVersion.nodeId} · v{baseVersion.version}</strong></div>)}
            {knowledgeRevision.evidenceRefs.map((evidence) => <div key={evidence.candidateEvidenceId}><span>Evidence</span><strong>{evidence.candidateEvidenceId}<br /><code className="mono">semantic hash: {evidence.semanticHash}</code></strong></div>)}
          </div> : publishRevision ? <div className="relation-diff">
            <div><span>Artifact</span><strong>{publishRevision.publication.artifactId}</strong></div>
            <div><span>Artifact Revision</span><strong>#{publishRevision.publication.revisionNo} · <code className="mono">{publishRevision.publication.revisionId}</code></strong></div>
            <div><span>Artifact Version</span><strong>v{publishRevision.publication.artifactVersion}</strong></div>
            <div><span>Content Hash</span><strong><code className="mono">{publishRevision.publication.contentHash}</code></strong></div>
            <div><span>Schema</span><strong>{publishRevision.publication.schemaVersion}</strong></div>
            <div><span>Change Control Revision</span><strong>#{publishRevision.revisionNo} · <code className="mono">{publishRevision.id}</code></strong></div>
            {publishRevision.publication.sourceCoverage.map((coverage) => <div key={coverage.sectionKey}>
              <span>章节覆盖 · {coverage.sectionKey}</span>
              <strong>{coverage.status} · {coverage.gaps.length === 0 ? "无知识缺口" : coverage.gaps.map((gap) => `${gap.code}：${gap.description}`).join("；")}</strong>
            </div>)}
          </div> : downstreamRevision ? <div className="relation-diff">
            <div><span>Impact Report</span><strong><code className="mono">{downstreamRevision.update.sourceReport.id}</code></strong></div>
            <div><span>Analysis Version</span><strong>{downstreamRevision.update.sourceReport.analysisVersion}</strong></div>
            <div><span>Report Fingerprint</span><strong><code className="mono">{downstreamRevision.update.sourceReport.fingerprint}</code></strong></div>
            <div><span>Source Event</span><strong><code className="mono">{downstreamRevision.update.sourceEvent.id}</code> · v{downstreamRevision.update.sourceEvent.eventVersion}</strong></div>
            <div><span>Target</span><strong>{downstreamRevision.update.targetType}:{downstreamRevision.update.targetId}</strong></div>
            <div><span>Target Base Version</span><strong>v{downstreamRevision.update.baseVersion}</strong></div>
            <div><span>Action</span><strong>{downstreamRevision.update.action}</strong></div>
            <div><span>Schema</span><strong>{downstreamRevision.update.schemaVersion}</strong></div>
            <div><span>Change Control Revision</span><strong>#{downstreamRevision.revisionNo} · <code className="mono">{downstreamRevision.id}</code></strong></div>
            {downstreamRevision.update.targetType === "ARTIFACT" ? <>
              <div><span>Artifact Owner</span><strong>{downstreamRevision.update.artifactBinding.artifactId} · v{downstreamRevision.update.artifactBinding.artifactVersion}</strong></div>
              <div><span>Artifact Revision</span><strong>#{downstreamRevision.update.artifactBinding.revisionNo} · <code className="mono">{downstreamRevision.update.artifactBinding.revisionId}</code></strong></div>
              <div><span>Artifact Content Hash</span><strong><code className="mono">{downstreamRevision.update.artifactBinding.contentHash}</code></strong></div>
            </> : <>
              <div><span>Review Card Owner</span><strong>{downstreamRevision.update.reviewCardBinding.cardId} · v{downstreamRevision.update.reviewCardBinding.cardVersion} · {downstreamRevision.update.reviewCardBinding.status}</strong></div>
              <div><span>Claim</span><strong><code className="mono">{downstreamRevision.update.reviewCardBinding.claimId}</code></strong></div>
              <div><span>Card Fingerprint</span><strong><code className="mono">{downstreamRevision.update.reviewCardBinding.fingerprint}</code></strong></div>
              <div><span>Evidence Binding</span><strong><code className="mono">{downstreamRevision.update.reviewCardBinding.evidenceBindingFingerprint}</code></strong></div>
            </>}
          </div> : null}
        </Card>
        <Card>
          <CardHeader eyebrow="证据与回滚" title="为什么要改" />
          <div className="prose-block">
            <p>{fileRevision ? readText(fileRevision.evidenceSummary, "该 Proposal 没有提供摘要。") : knowledgeRevision ? `结构化变更绑定 ${String(knowledgeRevision.evidenceRefs.length)} 条 Evidence。` : publishRevision ? `冻结 ${String(publishRevision.publication.sourceCoverage.length)} 个章节的来源覆盖；该快照仅供发布审批。` : downstreamRevision?.update.reason}</p>
            <p><strong>风险等级：</strong><Badge tone={riskLevelTone(proposal.riskLevel)}>{proposal.riskLevel} · {riskLevelLabel(proposal.riskLevel)}</Badge></p>
            <p><strong>风险说明：</strong>{riskDescription}</p>
            <p><strong>回滚计划：</strong>{readText(revision.rollbackPlan)}</p>
          </div>
        </Card>
      </main>

      <aside className="review-sidebar">
        <Card>
          <CardHeader eyebrow="Change Control" title="决策" />
          {proposal.status === "needs_revision" || (type === "file_patch" && currentContentQuery.data?.baseHashMatch === false)
            ? <UnavailableState title="基线已漂移" description="批准已被阻止；请重新生成 Proposal Revision。" />
            : ready ? <>
              <Button className="full-button" disabled={!canApprove || mutation.isPending} onClick={(event) => openConfirmation("approved", event.currentTarget)}><Check size={16} />批准</Button>
              <Button className="full-button" variant="danger" disabled={!canReject || mutation.isPending} onClick={(event) => openConfirmation("rejected", event.currentTarget)}><X size={16} />驳回</Button>
              {type === "file_patch" && !baselineVerified && !currentContentQuery.isError ? <p className="sidebar-note">等待当前文件基线校验完成后才能批准。</p> : null}
              {type === "file_patch" && baselineVerified && !fileDiffReady && !currentContentQuery.isError ? <p className="sidebar-note">等待 Diff Viewer 成功挂载后才能批准；驳回仍可提交。</p> : null}
            </> : proposal.type === "downstream_update" && proposal.status === "approved"
              ? <UnavailableState title="执行能力不可用" description="该 Approval 仅记录下游更新意图；当前没有目标执行器，页面不会显示执行、派发或写入成功状态。" />
              : writebackState === "pending_dispatch" ? <>
              <UnavailableState title="历史批准尚未派发" description="该 Approval 已有可信 Git 基线，但尚未绑定 Safe Writeback Workflow。恢复时服务端会重新检查当前文件与 Git。" />
              <Button className="full-button" variant="secondary" disabled={!canRedispatch || mutation.isPending} onClick={(event) => openConfirmation("redispatch", event.currentTarget)}><RotateCcw size={15} />恢复写回 Workflow</Button>
              {!baselineVerified && !currentContentQuery.isError ? <p className="sidebar-note">等待当前文件基线校验完成后才能恢复派发。</p> : null}
              {baselineVerified && !fileDiffReady && !currentContentQuery.isError ? <p className="sidebar-note">等待 Diff Viewer 成功挂载后才能恢复派发。</p> : null}
            </> : writebackState === "legacy_unrecoverable" ? <UnavailableState title="历史审批缺少 Git 基线" description="该记录可只读查看，但服务端禁止自动补建写回 Workflow；请按恢复手册人工处理或重新生成 Proposal。" />
              : <UnavailableState title="当前状态不可决策" description="该 Proposal 已有决定或已进入后续状态；页面不会重复显示批准/驳回操作。" />}
          {mutation.isError ? <p className="form-error" role="alert">{mutation.error.message}</p> : null}
          <p className="sidebar-note">暂缓、编辑后批准、批量审批与完整三方合并尚未交付。</p>
        </Card>
        {proposal.type === "file_patch" && proposal.status === "approved" ? <Card>
          <CardHeader eyebrow="Safe Writeback" title="写回前检查" />
          <p className="sidebar-note">Apply Preflight 只检查已批准 Revision 的当前基线，不写文件，也不代替首次审批。</p>
          <Button className="full-button" variant="secondary" disabled={!canPreflight || preflight.isPending} onClick={() => preflight.mutate()}>
            <RotateCcw size={15} />{preflight.isPending ? "检查中…" : "执行 Apply Preflight"}
          </Button>
          {preflightStillCurrent ? <div className="ui-state ui-state--success" role="status"><strong>写回前检查通过</strong><p>当前 Base Hash 与已批准 Revision 一致；本次检查没有执行写入。</p></div> : null}
          {preflight.isError ? <p className="form-error" role="alert">{preflight.error.message}</p> : null}
        </Card> : null}
        {proposal.type === "publish_artifact" ? <Card>
          <CardHeader eyebrow="知识隔离边界" title="尚未成为正式知识" />
          <p className="sidebar-note">该 Proposal 只冻结并审阅 Artifact 发布请求。即使 Approval 为 approved，本页也不表示已创建 Document、Git 写入或索引；正式知识状态必须由后续受控执行结果确认。</p>
        </Card> : null}
        <Card>
          <CardHeader eyebrow="决策快照" title="Approval" />
          {proposal.approval ? <dl className="stacked-meta">
            <div><dt>Decision</dt><dd><Badge tone={proposal.approval.decision === "approved" ? "success" : "danger"}>{proposal.approval.decision}</Badge></dd></div>
            <div><dt>Decided At</dt><dd>{new Date(proposal.approval.decidedAt).toLocaleString("zh-CN")}</dd></div>
            <div><dt>Approval ID</dt><dd className="mono">{proposal.approval.id}</dd></div>
            {proposal.approval.approvedGitHead ? <div><dt>Approved Git Head</dt><dd className="mono">{proposal.approval.approvedGitHead}</dd></div> : null}
            {proposal.approval.workflowRunId ? <div><dt>Writeback Workflow</dt><dd><Link className="table-link" to={`/workflows/${proposal.approval.workflowRunId}`}>{proposal.approval.workflowRunId}</Link></dd></div> : null}
          </dl> : <UnavailableState title="尚无 Approval" description="当前 Revision 尚未形成服务端批准或驳回决定。" />}
        </Card>
        <Card>
          <CardHeader eyebrow="版本绑定" title="Revision" />
          <dl className="stacked-meta">
            <div><dt>Revision ID</dt><dd className="mono">{readText(revision.id)}</dd></div>
            <div><dt>{versionBindingLabel}</dt><dd className="mono">{versionBindingValue}</dd></div>
            <div><dt>风险等级</dt><dd><AlertTriangle size={14} /><Badge tone={riskLevelTone(proposal.riskLevel)}>{proposal.riskLevel} · {riskLevelLabel(proposal.riskLevel)}</Badge></dd></div>
            <div><dt>风险说明</dt><dd>{riskDescription}</dd></div>
          </dl>
        </Card>
      </aside>
    </div>

    <Dialog
      open={activeConfirm !== undefined}
      onOpenChange={(open) => { if (!open) setConfirm(undefined); }}
      title={activeConfirm?.action === "redispatch" ? "确认恢复写回 Workflow？" : activeConfirm?.action === "approved" ? isHighRiskLevel(activeConfirm.riskLevel) ? "高风险变更：再次确认批准" : "确认批准这项变更？" : "确认驳回这项变更？"}
      description={activeConfirm?.action === "redispatch" ? "服务端会重做 Target Hash 与 strict-clean Git 安全门，再原子补建唯一 Workflow；前端不会把请求受理显示成写回完成。" : activeConfirm?.proposalType === "downstream_update" ? "提交后服务端会再次校验冻结的 Impact Report、Event、目标版本、owner 绑定与 Change Hash。批准只记录更新意图，不授予目标执行能力。" : activeConfirm?.proposalType === "publish_artifact" ? "提交后服务端会再次校验冻结的 Artifact、Revision、版本与 Change Hash。批准只形成 Approval，不表示已经创建 Document、Git 写入或索引。" : activeConfirm?.proposalType === "file_patch" ? "提交后服务端会再次校验 Proposal 状态、Revision、Change Hash 与当前文件基线；前端不会乐观显示成功。" : "提交后服务端会再次校验 Proposal 状态、Revision 与 Change Hash；前端不会乐观显示成功。"}
      restoreFocusRef={decisionTriggerRef}
    >
      <div className="dialog-actions">
        <Button variant="secondary" onClick={() => setConfirm(undefined)}>取消</Button>
        <Button variant={activeConfirm?.action === "rejected" ? "danger" : "primary"} onClick={() => { if (activeConfirm !== undefined) mutation.mutate(activeConfirm); }} disabled={mutation.isPending || activeConfirm === undefined}>{mutation.isPending ? "提交中…" : "确认提交"}</Button>
      </div>
    </Dialog>
  </div>;
};
