import type {
  ProposalRiskLevel,
  ProposalSummary,
  SourceIndexStatus,
  SourceIngestionStatus,
  SourceSecurityStatus,
  WorkflowSummary,
} from "../../api/business";

export type DashboardFocus =
  | {
      kind: "workflow_waiting" | "workflow_failed";
      label: string;
      title: string;
      description: string;
      href: string;
      actionLabel: string;
      updatedAt: string;
    }
  | {
      kind: "proposal";
      label: string;
      title: string;
      description: string;
      href: string;
      actionLabel: string;
      updatedAt: string;
      riskLevel: ProposalRiskLevel;
      riskLabel: string;
    }
  | {
      kind: "empty";
      label: string;
      title: string;
      description: string;
      href: string;
      actionLabel: string;
    };

export interface DashboardStatusDisplay {
  label: string;
  tone: "neutral" | "success" | "warning" | "danger" | "info";
}

const riskRank: Record<ProposalRiskLevel, number> = {
  CRITICAL: 4,
  HIGH: 3,
  MEDIUM: 2,
  LOW: 1,
};

const proposalTypeLabels: Record<ProposalSummary["type"], string> = {
  file_patch: "文件修改",
  restore_document: "文档恢复",
  knowledge_change: "知识变更",
  publish_artifact: "发布产物",
  downstream_update: "下游更新",
};

const proposalRiskLabels: Record<ProposalRiskLevel, string> = {
  CRITICAL: "严重",
  HIGH: "高",
  MEDIUM: "中",
  LOW: "低",
};

const safeTimestamp = (value: string): number => {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : Number.NEGATIVE_INFINITY;
};

const newestWorkflow = (items: readonly WorkflowSummary[]): WorkflowSummary | undefined =>
  [...items].sort((left, right) => safeTimestamp(right.updatedAt) - safeTimestamp(left.updatedAt))[0];

const highestRiskProposal = (items: readonly ProposalSummary[]): ProposalSummary | undefined =>
  [...items].sort((left, right) => {
    const riskDifference = riskRank[right.riskLevel] - riskRank[left.riskLevel];
    return riskDifference === 0
      ? safeTimestamp(right.updatedAt) - safeTimestamp(left.updatedAt)
      : riskDifference;
  })[0];

export const selectDashboardFocus = ({
  waitingWorkflows,
  failedWorkflows,
  proposals,
}: {
  waitingWorkflows: readonly WorkflowSummary[];
  failedWorkflows: readonly WorkflowSummary[];
  proposals: readonly ProposalSummary[];
}): DashboardFocus => {
  const waitingWorkflow = newestWorkflow(waitingWorkflows);
  if (waitingWorkflow !== undefined) {
    return {
      kind: "workflow_waiting",
      label: "需要你的处理",
      title: waitingWorkflow.definitionKey,
      description: "流程正在等待人工决定",
      href: `/workflows/${waitingWorkflow.id}`,
      actionLabel: "继续流程",
      updatedAt: waitingWorkflow.updatedAt,
    };
  }

  const failedWorkflow = newestWorkflow(failedWorkflows);
  if (failedWorkflow !== undefined) {
    return {
      kind: "workflow_failed",
      label: "流程需要检查",
      title: failedWorkflow.definitionKey,
      description: "流程已失败，请检查失败阶段与恢复方式",
      href: `/workflows/${failedWorkflow.id}`,
      actionLabel: "检查流程",
      updatedAt: failedWorkflow.updatedAt,
    };
  }

  const proposal = highestRiskProposal(proposals);
  if (proposal !== undefined) {
    const riskLabel = proposalRiskLabels[proposal.riskLevel];
    return {
      kind: "proposal",
      label: "等待审阅",
      title: proposal.target,
      description: `${proposalTypeLabels[proposal.type]} · ${riskLabel}风险`,
      href: `/proposals/${proposal.id}`,
      actionLabel: "开始审阅",
      updatedAt: proposal.updatedAt,
      riskLevel: proposal.riskLevel,
      riskLabel,
    };
  }

  return {
    kind: "empty",
    label: "今日已收束",
    title: "今天没有等待处理的事项",
    description: "现在适合从一篇新文章或一次材料整理开始。",
    href: "/authoring/new",
    actionLabel: "新建文章",
  };
};

export const sourceFileName = (path: string): string => {
  const segments = path.split(/[\\/]/).filter(Boolean);
  return segments.at(-1) ?? "未命名资料";
};

export const formatDashboardTimestamp = (value: string): string => {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "时间未知";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "long",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
};

export const securityStatusDisplay = (status: SourceSecurityStatus): DashboardStatusDisplay => ({
  pending: { label: "待检查", tone: "warning" },
  passed: { label: "已通过", tone: "success" },
  quarantined: { label: "已隔离", tone: "danger" },
} satisfies Record<SourceSecurityStatus, DashboardStatusDisplay>)[status];

export const ingestionStatusDisplay = (status: SourceIngestionStatus | undefined): DashboardStatusDisplay => {
  if (status === undefined) return { label: "未开始", tone: "neutral" };
  return ({
    validating: { label: "安全校验", tone: "info" },
    parsing: { label: "解析中", tone: "info" },
    parsed: { label: "已解析", tone: "success" },
    chunking: { label: "分块中", tone: "info" },
    chunked: { label: "已分块", tone: "success" },
    parse_failed: { label: "解析失败", tone: "danger" },
    cancelled: { label: "已取消", tone: "warning" },
  } satisfies Record<SourceIngestionStatus, DashboardStatusDisplay>)[status];
};

export const indexStatusDisplay = (status: SourceIndexStatus | undefined): DashboardStatusDisplay => {
  if (status === undefined) return { label: "未选择", tone: "neutral" };
  return ({
    included: { label: "已纳入", tone: "success" },
    excluded: { label: "已排除", tone: "warning" },
  } satisfies Record<SourceIndexStatus, DashboardStatusDisplay>)[status];
};
