import {
  proposalRiskLevels,
  type ProposalRiskLevel,
  type ProposalStatus,
  type ProposalType,
  type SourceIndexStatus,
  type SourceIngestionStatus,
  type SourceSecurityStatus,
  type WorkflowStatus,
} from "../../api/business";
import { canonicalLocalDate } from "../../shared/time";

const single = (parameters: URLSearchParams, name: string): string | undefined => {
  const values = parameters.getAll(name);
  return values.length === 1 ? values[0] : undefined;
};

const pick = <T extends string>(value: string | undefined, allowed: readonly T[]): T | "" =>
  value !== undefined && allowed.includes(value as T) ? value as T : "";

const writeValue = (parameters: URLSearchParams, name: string, value: string): void => {
  if (value !== "") parameters.set(name, value);
};

const sourceSecurityStatuses: readonly SourceSecurityStatus[] = ["pending", "passed", "quarantined"];
const sourceIngestionStatuses: readonly SourceIngestionStatus[] = ["validating", "parsing", "parsed", "chunking", "chunked", "parse_failed", "cancelled"];
const sourceIndexStatuses: readonly SourceIndexStatus[] = ["included", "excluded"];
const sourceMimeTypes = ["text/markdown", "text/plain", "text/html", "application/pdf"] as const;

export const workflowStatusOptions = [
  ["pending", "等待"],
  ["running", "运行中"],
  ["waiting_for_human", "等待人工"],
  ["retry_wait", "等待重试"],
  ["paused", "已暂停"],
  ["succeeded", "成功"],
  ["failed", "失败"],
  ["cancelled", "已取消"],
] as const satisfies readonly (readonly [WorkflowStatus, string])[];

const workflowStatuses = workflowStatusOptions.map(([value]) => value);

export const proposalStatusOptions = [
  ["draft", "草稿"],
  ["validating", "校验中"],
  ["ready_for_review", "待审"],
  ["approved", "已批准"],
  ["applying", "写回中"],
  ["applied", "已写回"],
  ["verifying", "验证中"],
  ["completed", "已完成"],
  ["rejected", "已驳回"],
  ["needs_revision", "需修订"],
  ["deferred", "已暂缓"],
  ["apply_failed", "写回失败"],
  ["verify_failed", "验证失败"],
  ["rolled_back", "已回滚"],
  ["cancelled", "已取消"],
] as const satisfies readonly (readonly [ProposalStatus, string])[];

const proposalStatuses = proposalStatusOptions.map(([value]) => value);
const proposalTypes: readonly ProposalType[] = ["file_patch", "knowledge_change"];

export interface InboxUrlState {
  securityStatus: SourceSecurityStatus | "";
  ingestionStatus: SourceIngestionStatus | "";
  workflowStatus: WorkflowStatus | "";
  indexStatus: SourceIndexStatus | "";
  mimeType: (typeof sourceMimeTypes)[number] | "";
}

export interface ProposalUrlState {
  status: ProposalStatus | "";
  type: ProposalType | "";
  risk: ProposalRiskLevel | "";
  createdDate: string;
}

export interface WorkflowUrlState {
  status: WorkflowStatus | "";
}

export const parseInboxUrlState = (parameters: URLSearchParams): InboxUrlState => ({
  securityStatus: pick(single(parameters, "security_status"), sourceSecurityStatuses),
  ingestionStatus: pick(single(parameters, "ingestion_status"), sourceIngestionStatuses),
  workflowStatus: pick(single(parameters, "workflow_status"), workflowStatuses),
  indexStatus: pick(single(parameters, "index_status"), sourceIndexStatuses),
  mimeType: pick(single(parameters, "mime_type"), sourceMimeTypes),
});

export const writeInboxUrlState = (state: InboxUrlState): URLSearchParams => {
  const parameters = new URLSearchParams();
  writeValue(parameters, "security_status", state.securityStatus);
  writeValue(parameters, "ingestion_status", state.ingestionStatus);
  writeValue(parameters, "workflow_status", state.workflowStatus);
  writeValue(parameters, "index_status", state.indexStatus);
  writeValue(parameters, "mime_type", state.mimeType);
  return parameters;
};

export const parseProposalUrlState = (parameters: URLSearchParams): ProposalUrlState => ({
  status: pick(single(parameters, "status"), proposalStatuses),
  type: pick(single(parameters, "proposal_type"), proposalTypes),
  risk: pick(single(parameters, "risk"), proposalRiskLevels),
  createdDate: canonicalLocalDate(single(parameters, "created_after") ?? null) ?? "",
});

export const writeProposalUrlState = (state: ProposalUrlState): URLSearchParams => {
  const parameters = new URLSearchParams();
  writeValue(parameters, "status", state.status);
  writeValue(parameters, "proposal_type", state.type);
  writeValue(parameters, "risk", state.risk);
  writeValue(parameters, "created_after", state.createdDate);
  return parameters;
};

export const parseWorkflowUrlState = (parameters: URLSearchParams): WorkflowUrlState => ({
  status: pick(single(parameters, "status"), workflowStatuses),
});

export const writeWorkflowUrlState = (state: WorkflowUrlState): URLSearchParams => {
  const parameters = new URLSearchParams();
  writeValue(parameters, "status", state.status);
  return parameters;
};
