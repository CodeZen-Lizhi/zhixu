import type { HealthIssueStatus, HealthIssueType, HealthSeverity } from "../../api/health";

export interface HealthUrlState { issueId: string; scanId: string; status: HealthIssueStatus | ""; severity: HealthSeverity | ""; type: HealthIssueType | "" }
const values = <T extends string>(value: string | null, allowed: readonly T[]): T | "" => value !== null && allowed.includes(value as T) ? value as T : "";
const single = (params: URLSearchParams, key: string): string | null => { const items = params.getAll(key); return items.length === 1 ? items[0] ?? null : null; };
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const uuid = (value: string | null): string => value !== null && uuidPattern.test(value) ? value : "";
const statuses: readonly HealthIssueStatus[] = ["OPEN", "ACKNOWLEDGED", "DEFERRED", "PROPOSAL_CREATED", "RESOLVED", "IGNORED", "FALSE_POSITIVE", "REOPENED"];
const severities: readonly HealthSeverity[] = ["CRITICAL", "HIGH", "MEDIUM", "LOW"];
const types: readonly HealthIssueType[] = ["ORPHAN", "DUPLICATE", "CONFLICT", "STALE", "MISSING_SOURCE", "LOW_CONFIDENCE", "BROKEN_REFERENCE", "INDEX_ERROR", "SUPERSEDED_USAGE", "REVIEW_INVALIDATED"];
export const parseHealthUrlState = (params: URLSearchParams): HealthUrlState => ({ issueId: uuid(single(params, "issue")), scanId: uuid(single(params, "scan")), status: values(single(params, "status"), statuses), severity: values(single(params, "severity"), severities), type: values(single(params, "type"), types) });
export const writeHealthUrlState = (state: HealthUrlState, current: URLSearchParams): URLSearchParams => { void current; const next = new URLSearchParams(); const setOrDelete = (key: string, value: string) => { if (value) next.set(key, value); else next.delete(key); }; setOrDelete("issue", uuid(state.issueId)); setOrDelete("scan", uuid(state.scanId)); setOrDelete("status", state.status); setOrDelete("severity", state.severity); setOrDelete("type", state.type); return next; };
