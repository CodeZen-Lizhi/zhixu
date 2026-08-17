import {
  AlertTriangle,
  BadgeCheck,
  Circle,
  Clock3,
  FileText,
  GitBranch,
  LoaderCircle,
  Search,
  ShieldCheck,
  Square,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import type {
  Answer,
  WorkspaceAnalysisTimelineItem,
  WorkspaceAnalysisTimelinePhase,
} from "../../api/conversation";
import { Tooltip } from "../../shared/ui";
import { useCancelWorkspaceAnalysisCommand } from "./commands";
import { ragQueryKeys } from "./query-keys";
import { useWorkspaceAnalysisTimeline } from "./queries";

const phaseLabels: Record<WorkspaceAnalysisTimelinePhase, string> = {
  inspect_workspace: "检查工作区",
  retrieve_evidence: "检索证据",
  read_evidence: "读取证据",
  synthesize_answer: "生成回答",
  validate_citations: "校验引用",
  review_publish: "审查发布",
};

const statusLabels: Record<WorkspaceAnalysisTimelineItem["status"], string> = {
  pending: "等待",
  waiting: "等待",
  started: "进行中",
  succeeded: "完成",
  failed: "失败",
  refused: "已拒绝",
  unknown: "结果未知",
  cancelled: "已取消",
};

const runStatusLabels = {
  queued: "等待开始",
  running: "分析中",
  succeeded: "已完成",
  refused: "未发布",
  clarification_required: "需要澄清",
  failed: "失败",
  cancelled: "已取消",
} as const;

const statusIcons: Record<WorkspaceAnalysisTimelineItem["status"], LucideIcon> = {
  pending: Circle,
  waiting: Clock3,
  started: LoaderCircle,
  succeeded: BadgeCheck,
  failed: AlertTriangle,
  refused: ShieldCheck,
  unknown: AlertTriangle,
  cancelled: Square,
};

const phaseIcons: Record<WorkspaceAnalysisTimelinePhase, LucideIcon> = {
  inspect_workspace: GitBranch,
  retrieve_evidence: Search,
  read_evidence: FileText,
  synthesize_answer: FileText,
  validate_citations: ShieldCheck,
  review_publish: BadgeCheck,
};

const formatDuration = (durationMs: number | null): string | null => {
  if (durationMs === null) return null;
  return durationMs < 1_000 ? `${String(durationMs)} ms` : `${(durationMs / 1_000).toFixed(1)} s`;
};

const TimelineSummary = ({ item }: { item: WorkspaceAnalysisTimelineItem }) => {
  const summary = item.summary;
  if (summary === null) return null;
  if (summary.kind === "git") return <span>
    {summary.git.clean ? "工作树干净" : `暂存 ${String(summary.git.stagedCount)} · 未暂存 ${String(summary.git.unstagedCount)} · 未跟踪 ${String(summary.git.untrackedCount)} · 冲突 ${String(summary.git.conflictCount)}`}
    {summary.git.branch === "" ? " · detached" : ` · ${summary.git.branch}`} · {summary.git.head.slice(0, 10)}
  </span>;
  if (summary.kind === "search") return <span>
    命中 {summary.search.hitCount}
    {summary.search.degradationCodes.length === 0 ? "" : ` · 退化 ${summary.search.degradationCodes.join("、")}`}
  </span>;
  if (summary.kind === "source") return <span>
    {summary.source.evidenceRef} · {summary.source.contentHash.slice(0, 10)}{summary.source.truncated ? " · 已截断" : ""}
  </span>;
  if (summary.kind === "citation_validation") return <span>
    有效 {summary.citationValidation.validCount} · 无效 {summary.citationValidation.invalidCount}
    {summary.citationValidation.reasonCodes.length === 0 ? "" : ` · ${summary.citationValidation.reasonCodes.join("、")}`}
  </span>;
  return <span>输入 {summary.modelUsage.inputTokens} · 输出 {summary.modelUsage.outputTokens} tokens</span>;
};

const TimelineItem = ({ item }: { item: WorkspaceAnalysisTimelineItem }) => {
  const StatusIcon = statusIcons[item.status];
  const PhaseIcon = phaseIcons[item.phase];
  const duration = formatDuration(item.durationMs);
  return <li className={`rag-analysis-item rag-analysis-item--${item.status}`}>
    <span className="rag-analysis-item__marker"><StatusIcon size={16} aria-hidden="true" /></span>
    <div className="rag-analysis-item__body">
      <div className="rag-analysis-item__heading">
        <span><PhaseIcon size={16} aria-hidden="true" />{phaseLabels[item.phase]}</span>
        <small>{statusLabels[item.status]}{duration === null ? "" : ` · ${duration}`}</small>
      </div>
      {item.toolRef === null ? null : <p className="rag-analysis-item__tool"><Wrench size={14} aria-hidden="true" />{item.toolRef.name}@{item.toolRef.version}</p>}
      <TimelineSummary item={item} />
      {item.errorCode === null ? null : <code>{item.errorCode}</code>}
    </div>
  </li>;
};

const activeWorkflowStatuses = new Set<Answer["workflow"]["status"]>([
  "pending", "running", "paused", "waiting_for_human", "retry_wait",
]);

export const WorkspaceAnalysisTimeline = ({
  workspaceId,
  conversationId,
  answer,
  autoRefresh,
}: {
  workspaceId: string;
  conversationId: string;
  answer: Answer;
  autoRefresh: boolean;
}) => {
  const active = answer.publicationStatus === "pending";
  const [expanded, setExpanded] = useState(autoRefresh);
  const query = useWorkspaceAnalysisTimeline(workspaceId, answer.id, autoRefresh || expanded, autoRefresh || (expanded && active));
  const queryClient = useQueryClient();
  const refreshedTerminal = useRef("");
  const refreshingTerminal = useRef("");
  const refreshedTimelineForTerminalAnswer = useRef("");
  const refreshingTimelineForTerminalAnswer = useRef("");
  const [syncRetry, setSyncRetry] = useState(0);
  const [syncFailed, setSyncFailed] = useState(false);
  useEffect(() => {
    if (!active || query.data === undefined || query.data.runStatus === "queued" || query.data.runStatus === "running") return;
    const key = `${query.data.runStatus}:${String(query.data.latestServerEventSequence)}`;
    if (refreshedTerminal.current === key || refreshingTerminal.current === key) return;
    refreshingTerminal.current = key;
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: ragQueryKeys.answer(workspaceId, answer.id), exact: true }, { throwOnError: true }),
      queryClient.invalidateQueries({ queryKey: ragQueryKeys.turns(workspaceId, conversationId) }, { throwOnError: true }),
    ]).then(() => {
      if (refreshingTerminal.current === key) {
        refreshedTerminal.current = key;
        setSyncFailed(false);
      }
    }).catch(() => {
      setSyncFailed(true);
    }).finally(() => {
      if (refreshingTerminal.current === key) refreshingTerminal.current = "";
    });
  }, [active, answer.id, conversationId, query.data, queryClient, syncRetry, workspaceId]);
  useEffect(() => {
    if (active || !expanded) return;
    const key = `${answer.publicationStatus}:${String(answer.version)}:${String(answer.workflow.version)}`;
    if (refreshedTimelineForTerminalAnswer.current === key || refreshingTimelineForTerminalAnswer.current === key) return;
    refreshingTimelineForTerminalAnswer.current = key;
    void queryClient.invalidateQueries({
      queryKey: ragQueryKeys.analysisTimeline(workspaceId, answer.id),
      exact: true,
    }, { throwOnError: true }).then(() => {
      if (refreshingTimelineForTerminalAnswer.current === key) {
        refreshedTimelineForTerminalAnswer.current = key;
        setSyncFailed(false);
      }
    }).catch(() => {
      setSyncFailed(true);
    }).finally(() => {
      if (refreshingTimelineForTerminalAnswer.current === key) refreshingTimelineForTerminalAnswer.current = "";
    });
  }, [active, answer.id, answer.publicationStatus, answer.version, answer.workflow.version, expanded, queryClient, syncRetry, workspaceId]);
  const cancel = useCancelWorkspaceAnalysisCommand();
  const canStop = active && activeWorkflowStatuses.has(answer.workflow.status);
  const stop = () => cancel.mutate({
    workspaceId,
    conversationId,
    answerId: answer.id,
    workflowRunId: answer.workflow.runId,
    expectedVersion: answer.workflow.version,
  });

  return <section className="rag-analysis" aria-label="工作区分析进度">
    <div className="rag-analysis__header">
      <div><strong>工作区分析</strong><small>{query.data === undefined ? (autoRefresh ? "正在同步" : active ? "等待查看" : "过程已归档") : runStatusLabels[query.data.runStatus]}</small></div>
      {canStop ? <Tooltip content="停止工作区分析"><button
        type="button"
        className="rag-analysis-stop"
        aria-label="停止工作区分析"
        disabled={cancel.isPending || cancel.isSuccess}
        onClick={stop}
      ><Square size={16} aria-hidden="true" /></button></Tooltip> : null}
    </div>
    {!expanded ? <button type="button" className="rag-analysis__expand" onClick={() => setExpanded(true)}>{active ? "查看当前进度" : "查看分析过程"}</button> : null}
    {cancel.isSuccess ? <p className="rag-analysis__status" role="status">停止请求已提交</p> : null}
    {cancel.isError ? <p className="rag-analysis__error" role="alert">停止请求未完成</p> : null}
    {query.isError || syncFailed ? <div className="rag-analysis__error" role="alert">
      <span>分析进度同步未完成</span>
      <button type="button" onClick={() => {
        setSyncFailed(false);
        setSyncRetry((attempt) => attempt + 1);
        void query.refetch();
      }}>重试读取进度</button>
    </div> : null}
    {!expanded ? null : query.data === undefined ? <p className="rag-analysis__status">正在读取权威进度…</p> : <>
      <dl className="rag-analysis-budget">
	        <div><dt>模型调用</dt><dd>{query.data.budget.modelCalls.used}/{query.data.budget.modelCalls.max}</dd></div>
	        <div><dt>工具调用</dt><dd>{query.data.budget.toolCalls.used}/{query.data.budget.toolCalls.max}</dd></div>
	        <div><dt>证据读取</dt><dd>{query.data.budget.sourceReads.used}/{query.data.budget.sourceReads.max}</dd></div>
	        <div><dt>输入 tokens</dt><dd>{query.data.budget.inputTokens.used}/{query.data.budget.inputTokens.max}</dd></div>
        <div><dt>输出 tokens</dt><dd>{query.data.budget.outputTokens.used}/{query.data.budget.outputTokens.max}</dd></div>
      </dl>
      <ol className="rag-analysis-list">{query.data.items.map((item) => <TimelineItem key={item.sequence} item={item} />)}</ol>
    </>}
  </section>;
};
