/** Suggested Material Set 与整理模板的唯一网络边界。 */

import { authFetch } from "./auth";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";

export type OrganizingDraftStatus = "EDITING" | "CONFIRMED";
export type OrganizingMaterialKind = "SOURCE_VERSION" | "DOCUMENT_REVISION" | "CLAIM" | "SMART_COLLECTION";
export type OrganizingMaterialStatus = "AVAILABLE" | "STALE" | "UNAVAILABLE";
export type OrganizingMaterialOrigin = "SUGGESTED" | "USER";
export type OrganizingReasonCode =
  | "HYBRID_MATCH"
  | "PROFILE_MATCH"
  | "ALIAS_MATCH"
  | "FORMAL_KNOWLEDGE"
  | "COLLECTION_MEMBER"
  | "USER_ADDED";
export type OrganizingTemplateKind = "TOPIC_ARTICLE" | "MERGE_DOCUMENTS" | "KNOWLEDGE_REPORT" | "INTERVIEW_REVIEW";
export type OrganizingRunStatus = "QUEUED" | "STARTED" | "WAITING_FOR_HUMAN" | "SUCCEEDED" | "FAILED";
export type OrganizingDispatchStatus = "PENDING" | "STARTED" | "POISONED";
export type OrganizingWorkflowStatus = "pending" | "running" | "waiting_for_human" | "retry_wait" | "paused" | "succeeded" | "failed" | "cancelled";
export type OrganizingResultKind = "MERGE_PROPOSAL" | "ARTIFACT";

export interface OrganizingEvidence {
  indexVersionId: string;
  chunkId: string;
  sourceVersionId: string;
  sourceSpanId: string;
  contentHash: string;
  excerptHash: string;
}

export interface SourceVersionReference {
  kind: "SOURCE_VERSION";
  sourceVersionId: string;
  version: 0;
  contentHash: string;
  profileRevisionId: string | null;
  originCollectionId: string | null;
}

export interface DocumentRevisionReference {
  kind: "DOCUMENT_REVISION";
  documentId: string;
  articleRevisionId: string;
  revisionNo: number;
  contentHash: string;
  originCollectionId: string | null;
}

export interface ClaimReference {
  kind: "CLAIM";
  claimId: string;
  claimVersion: number;
  contentHash: string;
  originCollectionId: string | null;
}

export interface SmartCollectionReference {
  kind: "SMART_COLLECTION";
  collectionId: string;
  collectionVersion: number;
  queryHash: string;
  readModelRevision: string;
}

export type OrganizingMaterialReference =
  | SourceVersionReference
  | DocumentRevisionReference
  | ClaimReference
  | SmartCollectionReference;

export type OrganizingMaterialSearchReference =
  | { kind: "SOURCE_VERSION"; sourceVersionId: string }
  | { kind: "DOCUMENT_REVISION"; documentId: string; articleRevisionId: string }
  | { kind: "CLAIM"; claimId: string }
  | { kind: "SMART_COLLECTION"; collectionId: string };

export interface OrganizingMaterial {
  id: string;
  draftId: string;
  workspaceId: string;
  kind: OrganizingMaterialKind;
  title: string;
  reasons: OrganizingReasonCode[];
  origin: OrganizingMaterialOrigin;
  availability: OrganizingMaterialStatus;
  score: number;
  selected: boolean;
  position: number;
  reference: OrganizingMaterialReference;
  evidence: OrganizingEvidence[];
  createdAt: string;
}

/** 供整理草稿选择的 Workspace 受限材料搜索结果。 */
export interface OrganizingMaterialSearchResult {
  workspaceId: string;
  kind: OrganizingMaterialKind;
  title: string;
  availability: OrganizingMaterialStatus;
  reference: OrganizingMaterialSearchReference;
}

export interface OrganizingDraft {
  id: string;
  workspaceId: string;
  intent: string;
  status: OrganizingDraftStatus;
  templateRevisionId: string | null;
  confirmedSnapshotId: string | null;
  version: number;
  materials: OrganizingMaterial[];
  createdAt: string;
  updatedAt: string;
}

export interface TemplateSection {
  key: string;
  title: string;
  required: boolean;
}

export interface OrganizingTemplateDeclaration {
  schemaVersion: "organizing-template/v1";
  kind: OrganizingTemplateKind;
  name: string;
  description: string;
  materials: {
    allowedKinds: OrganizingMaterialKind[];
    minMaterials: number;
    maxMaterials: number;
  };
  sections: TemplateSection[];
  presentation: {
    audience: string;
    language: string;
    tone: string;
    length: "SHORT" | "MEDIUM" | "LONG";
    includeCode: boolean;
    includeExamples: boolean;
    includeFaq: boolean;
  };
  output: {
    directory: string;
    filenamePattern: string;
  };
  additionalInstructions: string;
}

export interface OrganizingTemplateRevision {
  id: string;
  templateId: string;
  workspaceId: string | null;
  revisionNo: number;
  kind: OrganizingTemplateKind;
  schemaVersion: "organizing-template/v1";
  canonicalHash: string;
  declaration: OrganizingTemplateDeclaration;
  createdAt: string;
}

export interface OrganizingTemplate {
  id: string;
  workspaceId: string | null;
  key: string;
  name: string;
  description: string;
  builtIn: boolean;
  kind: OrganizingTemplateKind;
  currentRevisionId: string;
  version: number;
  currentRevision: OrganizingTemplateRevision;
  createdAt: string;
  updatedAt: string;
}

export interface OrganizingSnapshotMaterial {
  position: number;
  reference: OrganizingMaterialReference;
  evidence: OrganizingEvidence[];
}

export interface OrganizingSnapshot {
  id: string;
  workspaceId: string;
  draftId: string;
  draftVersion: number;
  templateId: string;
  templateRevisionId: string;
  templateHash: string;
  intent: string;
  canonicalHash: string;
  materials: OrganizingSnapshotMaterial[];
  createdAt: string;
}

export interface OrganizingRun {
  snapshotId: string;
  dispatchStatus: OrganizingDispatchStatus;
  workflowStatus: OrganizingWorkflowStatus | null;
  workflowRunId: string | null;
  status: OrganizingRunStatus;
  resultKind: OrganizingResultKind | null;
  resultRef: string | null;
  resultHash: string | null;
  errorCode: string | null;
  retryable: boolean;
  attemptCount: number;
  updatedAt: string;
}

export interface OrganizingCommandResult {
  draft: OrganizingDraft;
  replayed: boolean;
}

export interface CreateOrganizingDraftInput {
  workspaceId: string;
  intent: string;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export interface UpdateOrganizingDraftInput extends CreateOrganizingDraftInput {
  draftId: string;
  expectedVersion: number;
  templateRevisionId: string;
}

export interface OrganizingDraftCommandInput {
  workspaceId: string;
  draftId: string;
  expectedVersion: number;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export type AddOrganizingMaterialInput = OrganizingDraftCommandInput & (
  | { kind: "SOURCE_VERSION"; sourceVersionId: string }
  | { kind: "DOCUMENT_REVISION"; documentId: string; articleRevisionId: string }
  | { kind: "CLAIM"; claimId: string }
  | { kind: "SMART_COLLECTION"; collectionId: string }
);

export interface SearchOrganizingMaterialsInput {
  workspaceId: string;
  query: string;
  kind: OrganizingMaterialKind;
  limit?: number;
  signal?: AbortSignal;
}

export interface RemoveOrganizingMaterialInput extends OrganizingDraftCommandInput {
  materialId: string;
}

export interface SetOrganizingMaterialSelectionInput extends RemoveOrganizingMaterialInput {
  selected: boolean;
}

export interface ConfirmOrganizingDraftInput extends OrganizingDraftCommandInput {
  templateRevisionId: string;
}

export interface ConfirmOrganizingDraftResult {
  draft: OrganizingDraft;
  snapshot: OrganizingSnapshot;
  dispatchStatus: "PENDING";
  replayed: boolean;
}

export interface CloneOrganizingTemplateInput {
  workspaceId: string;
  templateId: string;
  name: string;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export interface ReviseOrganizingTemplateInput {
  workspaceId: string;
  templateId: string;
  expectedVersion: number;
  declaration: OrganizingTemplateDeclaration;
  idempotencyKey: string;
  signal?: AbortSignal;
}

type OrganizingApiErrorKind = "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";

export class OrganizingApiError extends Error {
  readonly code: OrganizingApiErrorKind;
  readonly errorCode: string;
  readonly status: number | null;
  readonly retryable: boolean;
  readonly workflowRunId: string | undefined;
  readonly details: Readonly<Record<string, unknown>> | undefined;

  constructor(
    code: OrganizingApiErrorKind,
    errorCode: string,
    message: string,
    status: number | null = null,
    retryable = false,
    options: { cause?: unknown; workflowRunId?: string; details?: Readonly<Record<string, unknown>> } = {},
  ) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = "OrganizingApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.status = status;
    this.retryable = retryable;
    this.workflowRunId = options.workflowRunId;
    this.details = options.details;
  }
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const calendarDatePattern = /^(\d{4})-(\d{2})-(\d{2})T/;
const errorCodePattern = /^[A-Z][A-Z0-9_]{0,127}$/;
const keyPattern = /^[a-z0-9]+(?:[._-][a-z0-9]+)*$/;
const sectionKeyPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const mandatoryGovernanceSections = ["conflicts", "gaps", "sources"] as const;
const controlPattern = /[\u0000-\u001f\u007f]/;
const encoder = new TextEncoder();

const materialKinds = ["SOURCE_VERSION", "DOCUMENT_REVISION", "CLAIM", "SMART_COLLECTION"] as const;
const materialStatuses = ["AVAILABLE", "STALE", "UNAVAILABLE"] as const;
const reasonCodes = ["HYBRID_MATCH", "PROFILE_MATCH", "ALIAS_MATCH", "FORMAL_KNOWLEDGE", "COLLECTION_MEMBER", "USER_ADDED"] as const;
const templateKinds = ["TOPIC_ARTICLE", "MERGE_DOCUMENTS", "KNOWLEDGE_REPORT", "INTERVIEW_REVIEW"] as const;
const materialOrigins = ["SUGGESTED", "USER"] as const;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const invalidRequest = (field: string, cause?: unknown): OrganizingApiError =>
  new OrganizingApiError("INVALID_REQUEST", "INVALID_REQUEST", `整理请求字段无效：${field}`, null, false, { cause });

const invalidResponse = (field: string, status: number | null = null, cause?: unknown): OrganizingApiError =>
  new OrganizingApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `整理响应字段无效：${field}`, status, false, { cause });

const exact = (value: unknown, required: readonly string[], field: string, optional: readonly string[] = []): Record<string, unknown> => {
  if (!isRecord(value)) throw invalidResponse(field);
  const allowed = new Set([...required, ...optional]);
  if (Object.keys(value).some((key) => !allowed.has(key)) || required.some((key) => !Object.hasOwn(value, key))) {
    throw invalidResponse(field);
  }
  return value;
};

const text = (value: unknown, field: string, maxBytes: number, allowEmpty = false): string => {
  if (typeof value !== "string" || encoder.encode(value).byteLength > maxBytes || value.includes("\u0000") || (!allowEmpty && value.trim() === "")) {
    throw invalidResponse(field);
  }
  return value;
};

const uuid = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !uuidPattern.test(value)) throw invalidResponse(field);
  return value;
};

const nullableUuid = (value: unknown, field: string): string | null => value === null ? null : uuid(value, field);
const optionalUuid = (row: Record<string, unknown>, key: string, field: string): string | null =>
  row[key] === undefined ? null : uuid(row[key], field);

const hash = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !hashPattern.test(value)) throw invalidResponse(field);
  return value;
};

const integer = (value: unknown, field: string, minimum = 0): number => {
  if (!Number.isSafeInteger(value) || (value as number) < minimum) throw invalidResponse(field);
  return value as number;
};

const finiteNumber = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isFinite(value)) throw invalidResponse(field);
  return value;
};

const boolean = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const timestamp = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !timestampPattern.test(value) || !Number.isFinite(Date.parse(value))) throw invalidResponse(field);
  const calendar = calendarDatePattern.exec(value);
  if (calendar === null) throw invalidResponse(field);
  const year = Number(calendar[1]);
  const month = Number(calendar[2]);
  const day = Number(calendar[3]);
  const candidate = new Date(0);
  candidate.setUTCHours(0, 0, 0, 0);
  candidate.setUTCFullYear(year, month - 1, day);
  if (candidate.getUTCFullYear() !== year || candidate.getUTCMonth() !== month - 1 || candidate.getUTCDate() !== day) throw invalidResponse(field);
  return value;
};

const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string): T => {
  if (typeof value !== "string" || !values.includes(value as T)) throw invalidResponse(field);
  return value as T;
};

const array = <T>(value: unknown, field: string, max: number, decode: (item: unknown, index: number) => T): T[] => {
  if (!Array.isArray(value) || value.length > max) throw invalidResponse(field);
  return value.map(decode);
};

const assertUnique = (values: readonly string[], field: string): void => {
  if (new Set(values).size !== values.length) throw invalidResponse(field);
};

const decodeOutputDefaults = (value: unknown, field: string): OrganizingTemplateDeclaration["output"] => {
  const row = exact(value, ["directory", "filename_pattern"], field);
  const directory = text(row.directory, `${field}.directory`, 1024).trim();
  const filenamePattern = text(row.filename_pattern, `${field}.filename_pattern`, 256).trim();
  const parts = directory.split("/");
  const expanded = filenamePattern.replaceAll("{slug}", "x").replaceAll("{date}", "x");
  if (directory.startsWith("/") || directory.includes("\\") || parts.some((part) => part === "" || part === "." || part === "..")
    || filenamePattern.includes("/") || filenamePattern.includes("\\") || filenamePattern.includes("..")
    || !filenamePattern.toLowerCase().endsWith(".md") || expanded.includes("{") || expanded.includes("}")) {
    throw invalidResponse(field);
  }
  return { directory, filenamePattern };
};

const decodeEvidence = (value: unknown, field: string): OrganizingEvidence => {
  const row = exact(value, ["index_version_id", "chunk_id", "source_version_id", "source_span_id", "content_hash", "excerpt_hash"], field);
  return {
    indexVersionId: uuid(row.index_version_id, `${field}.index_version_id`),
    chunkId: uuid(row.chunk_id, `${field}.chunk_id`),
    sourceVersionId: uuid(row.source_version_id, `${field}.source_version_id`),
    sourceSpanId: uuid(row.source_span_id, `${field}.source_span_id`),
    contentHash: hash(row.content_hash, `${field}.content_hash`),
    excerptHash: hash(row.excerpt_hash, `${field}.excerpt_hash`),
  };
};

export const decodeOrganizingMaterialReference = (value: unknown, field = "material.reference"): OrganizingMaterialReference => {
  if (!isRecord(value)) throw invalidResponse(field);
  const kind = enumValue(value.kind, materialKinds, `${field}.kind`);
  switch (kind) {
    case "SOURCE_VERSION": {
      const row = exact(value, ["kind", "source_version_id", "version", "content_hash"], field, ["origin_collection_id", "profile_revision_id"]);
      const version = integer(row.version, `${field}.version`);
      if (version !== 0) throw invalidResponse(`${field}.version`);
      return { kind, sourceVersionId: uuid(row.source_version_id, `${field}.source_version_id`), version: 0, contentHash: hash(row.content_hash, `${field}.content_hash`), profileRevisionId: optionalUuid(row, "profile_revision_id", `${field}.profile_revision_id`), originCollectionId: optionalUuid(row, "origin_collection_id", `${field}.origin_collection_id`) };
    }
    case "DOCUMENT_REVISION": {
      const row = exact(value, ["kind", "document_id", "article_revision_id", "version", "content_hash"], field, ["origin_collection_id"]);
      return { kind, documentId: uuid(row.document_id, `${field}.document_id`), articleRevisionId: uuid(row.article_revision_id, `${field}.article_revision_id`), revisionNo: integer(row.version, `${field}.version`, 1), contentHash: hash(row.content_hash, `${field}.content_hash`), originCollectionId: optionalUuid(row, "origin_collection_id", `${field}.origin_collection_id`) };
    }
    case "CLAIM": {
      const row = exact(value, ["kind", "claim_id", "version", "content_hash"], field, ["origin_collection_id"]);
      return { kind, claimId: uuid(row.claim_id, `${field}.claim_id`), claimVersion: integer(row.version, `${field}.version`, 1), contentHash: hash(row.content_hash, `${field}.content_hash`), originCollectionId: optionalUuid(row, "origin_collection_id", `${field}.origin_collection_id`) };
    }
    case "SMART_COLLECTION": {
      const row = exact(value, ["kind", "collection_id", "version", "query_hash", "read_model_revision"], field);
      return { kind, collectionId: uuid(row.collection_id, `${field}.collection_id`), collectionVersion: integer(row.version, `${field}.version`, 1), queryHash: hash(row.query_hash, `${field}.query_hash`), readModelRevision: hash(row.read_model_revision, `${field}.read_model_revision`) };
    }
  }
};

const decodeMaterial = (value: unknown, field: string): OrganizingMaterial => {
  const row = exact(value, ["id", "draft_id", "workspace_id", "kind", "title", "reasons", "origin", "availability", "score", "selected", "position", "reference", "evidence", "created_at"], field);
  const kind = enumValue(row.kind, materialKinds, `${field}.kind`);
  const reference = decodeOrganizingMaterialReference(row.reference, `${field}.reference`);
  const reasons = array(row.reasons, `${field}.reasons`, 8, (item, index) => enumValue(item, reasonCodes, `${field}.reasons[${String(index)}]`));
  const evidence = array(row.evidence, `${field}.evidence`, 100, (item, index) => decodeEvidence(item, `${field}.evidence[${String(index)}]`));
  const score = finiteNumber(row.score, `${field}.score`);
  if (reference.kind !== kind || reasons.length === 0 || score < 0 || score > 1) throw invalidResponse(`${field}.binding`);
  assertUnique(reasons, `${field}.reasons`);
  assertUnique(evidence.map((item) => `${item.indexVersionId}:${item.chunkId}:${item.sourceSpanId}`), `${field}.evidence`);
  return {
    id: uuid(row.id, `${field}.id`),
    draftId: uuid(row.draft_id, `${field}.draft_id`),
    workspaceId: uuid(row.workspace_id, `${field}.workspace_id`),
    kind,
    title: text(row.title, `${field}.title`, 512),
    reasons,
    origin: enumValue(row.origin, materialOrigins, `${field}.origin`),
    availability: enumValue(row.availability, materialStatuses, `${field}.availability`),
    score,
    selected: boolean(row.selected, `${field}.selected`),
    position: integer(row.position, `${field}.position`),
    reference,
    evidence,
    createdAt: timestamp(row.created_at, `${field}.created_at`),
  };
};

const materialReferenceIdentity = (reference: OrganizingMaterialReference | OrganizingMaterialSearchReference): string => {
  switch (reference.kind) {
    case "SOURCE_VERSION": return `${reference.kind}:${reference.sourceVersionId}`;
    case "DOCUMENT_REVISION": return `${reference.kind}:${reference.documentId}:${reference.articleRevisionId}`;
    case "CLAIM": return `${reference.kind}:${reference.claimId}`;
    case "SMART_COLLECTION": return `${reference.kind}:${reference.collectionId}`;
  }
};

const decodeMaterialSearchReference = (value: unknown, field: string): OrganizingMaterialSearchReference => {
  if (!isRecord(value)) throw invalidResponse(field);
  const kind = enumValue(value.kind, materialKinds, `${field}.kind`);
  switch (kind) {
    case "SOURCE_VERSION": {
      const row = exact(value, ["kind", "source_version_id"], field);
      return { kind, sourceVersionId: uuid(row.source_version_id, `${field}.source_version_id`) };
    }
    case "DOCUMENT_REVISION": {
      const row = exact(value, ["kind", "document_id", "article_revision_id"], field);
      return { kind, documentId: uuid(row.document_id, `${field}.document_id`), articleRevisionId: uuid(row.article_revision_id, `${field}.article_revision_id`) };
    }
    case "CLAIM": {
      const row = exact(value, ["kind", "claim_id"], field);
      return { kind, claimId: uuid(row.claim_id, `${field}.claim_id`) };
    }
    case "SMART_COLLECTION": {
      const row = exact(value, ["kind", "collection_id"], field);
      return { kind, collectionId: uuid(row.collection_id, `${field}.collection_id`) };
    }
  }
};

const decodeMaterialSearchResult = (value: unknown, workspaceId: string, field: string): OrganizingMaterialSearchResult => {
  const row = exact(value, ["workspace_id", "kind", "title", "availability", "reference"], field);
  const kind = enumValue(row.kind, materialKinds, `${field}.kind`);
  const reference = decodeMaterialSearchReference(row.reference, `${field}.reference`);
  const resultWorkspaceId = uuid(row.workspace_id, `${field}.workspace_id`);
  if (resultWorkspaceId !== workspaceId || reference.kind !== kind) throw invalidResponse(`${field}.binding`);
  return {
    workspaceId: resultWorkspaceId,
    kind,
    title: text(row.title, `${field}.title`, 512),
    availability: enumValue(row.availability, materialStatuses, `${field}.availability`),
    reference,
  };
};

export const decodeOrganizingDraft = (value: unknown): OrganizingDraft => {
  const row = exact(value, ["id", "workspace_id", "intent", "status", "template_revision_id", "confirmed_snapshot_id", "version", "materials", "created_at", "updated_at"], "draft");
  const id = uuid(row.id, "draft.id");
  const workspaceId = uuid(row.workspace_id, "draft.workspace_id");
  const status = enumValue(row.status, ["EDITING", "CONFIRMED"] as const, "draft.status");
  const templateRevisionId = nullableUuid(row.template_revision_id, "draft.template_revision_id");
  const confirmedSnapshotId = nullableUuid(row.confirmed_snapshot_id, "draft.confirmed_snapshot_id");
  const materials = array(row.materials, "draft.materials", 100, (item, index) => decodeMaterial(item, `draft.materials[${String(index)}]`));
  const createdAt = timestamp(row.created_at, "draft.created_at");
  const updatedAt = timestamp(row.updated_at, "draft.updated_at");
  if (materials.some((item, index) => item.draftId !== id || item.workspaceId !== workspaceId || item.position !== index)
    || Date.parse(updatedAt) < Date.parse(createdAt)
    || (status === "CONFIRMED") !== (templateRevisionId !== null && confirmedSnapshotId !== null)) throw invalidResponse("draft.binding");
  assertUnique(materials.map((item) => item.id), "draft.materials.id");
  return { id, workspaceId, intent: text(row.intent, "draft.intent", 4096), status, templateRevisionId, confirmedSnapshotId, version: integer(row.version, "draft.version", 1), materials, createdAt, updatedAt };
};

const decodeTemplateDeclaration = (value: unknown, field: string): OrganizingTemplateDeclaration => {
  const row = exact(value, ["schema_version", "kind", "name", "description", "materials", "sections", "presentation", "output", "additional_instructions"], field);
  if (row.schema_version !== "organizing-template/v1") throw invalidResponse(`${field}.schema_version`);
  const kind = enumValue(row.kind, templateKinds, `${field}.kind`);
  const materialsRow = exact(row.materials, ["allowed_kinds", "min_materials", "max_materials"], `${field}.materials`);
  const allowedKinds = array(materialsRow.allowed_kinds, `${field}.materials.allowed_kinds`, materialKinds.length, (item, index) => enumValue(item, materialKinds, `${field}.materials.allowed_kinds[${String(index)}]`));
  const minMaterials = integer(materialsRow.min_materials, `${field}.materials.min_materials`, 1);
  const maxMaterials = integer(materialsRow.max_materials, `${field}.materials.max_materials`, minMaterials);
  if (allowedKinds.length === 0 || maxMaterials > 500) throw invalidResponse(`${field}.materials`);
  assertUnique(allowedKinds, `${field}.materials.allowed_kinds`);
  const sections = array(row.sections, `${field}.sections`, 24, (item, index): TemplateSection => {
    const section = exact(item, ["key", "title", "required"], `${field}.sections[${String(index)}]`);
    const key = text(section.key, `${field}.sections[${String(index)}].key`, 64);
    if (!sectionKeyPattern.test(key)) throw invalidResponse(`${field}.sections[${String(index)}].key`);
    return { key, title: text(section.title, `${field}.sections[${String(index)}].title`, 256), required: boolean(section.required, `${field}.sections[${String(index)}].required`) };
  });
  if (sections.length === 0) throw invalidResponse(`${field}.sections`);
  assertUnique(sections.map((section) => section.key), `${field}.sections.key`);
  for (const key of mandatoryGovernanceSections) {
    if (!sections.some((section) => section.key === key && section.required)) throw invalidResponse(`${field}.sections.${key}`);
  }
  const presentationRow = exact(row.presentation, ["audience", "language", "tone", "length", "include_code", "include_examples", "include_faq"], `${field}.presentation`);
  const output = decodeOutputDefaults(row.output, `${field}.output`);
  return {
    schemaVersion: "organizing-template/v1",
    kind,
    name: text(row.name, `${field}.name`, 128),
    description: text(row.description, `${field}.description`, 2048),
    materials: { allowedKinds, minMaterials, maxMaterials },
    sections,
    presentation: {
      audience: text(presentationRow.audience, `${field}.presentation.audience`, 512, true),
      language: text(presentationRow.language, `${field}.presentation.language`, 64),
      tone: text(presentationRow.tone, `${field}.presentation.tone`, 128),
      length: enumValue(presentationRow.length, ["SHORT", "MEDIUM", "LONG"] as const, `${field}.presentation.length`),
      includeCode: boolean(presentationRow.include_code, `${field}.presentation.include_code`),
      includeExamples: boolean(presentationRow.include_examples, `${field}.presentation.include_examples`),
      includeFaq: boolean(presentationRow.include_faq, `${field}.presentation.include_faq`),
    },
    output,
    additionalInstructions: text(row.additional_instructions, `${field}.additional_instructions`, 8192, true),
  };
};

export const decodeOrganizingTemplateRevision = (value: unknown, field = "template_revision"): OrganizingTemplateRevision => {
  const row = exact(value, ["id", "template_id", "workspace_id", "revision_no", "kind", "schema_version", "canonical_hash", "declaration", "created_at"], field);
  const kind = enumValue(row.kind, templateKinds, `${field}.kind`);
  if (row.schema_version !== "organizing-template/v1") throw invalidResponse(`${field}.schema_version`);
  const declaration = decodeTemplateDeclaration(row.declaration, `${field}.declaration`);
  if (declaration.kind !== kind) throw invalidResponse(`${field}.binding`);
  return { id: uuid(row.id, `${field}.id`), templateId: uuid(row.template_id, `${field}.template_id`), workspaceId: nullableUuid(row.workspace_id, `${field}.workspace_id`), revisionNo: integer(row.revision_no, `${field}.revision_no`, 1), kind, schemaVersion: "organizing-template/v1", canonicalHash: hash(row.canonical_hash, `${field}.canonical_hash`), declaration, createdAt: timestamp(row.created_at, `${field}.created_at`) };
};

export const decodeOrganizingTemplate = (value: unknown): OrganizingTemplate => {
  const row = exact(value, ["id", "workspace_id", "key", "name", "description", "built_in", "kind", "current_revision_id", "version", "current_revision", "created_at", "updated_at"], "template");
  const id = uuid(row.id, "template.id");
  const workspaceId = nullableUuid(row.workspace_id, "template.workspace_id");
  const builtIn = boolean(row.built_in, "template.built_in");
  const currentRevision = decodeOrganizingTemplateRevision(row.current_revision);
  const kind = enumValue(row.kind, templateKinds, "template.kind");
  const currentRevisionId = uuid(row.current_revision_id, "template.current_revision_id");
  const key = text(row.key, "template.key", 128);
  const createdAt = timestamp(row.created_at, "template.created_at");
  const updatedAt = timestamp(row.updated_at, "template.updated_at");
  if (currentRevision.templateId !== id || currentRevision.id !== currentRevisionId || currentRevision.workspaceId !== workspaceId ||
    currentRevision.kind !== kind || builtIn !== (workspaceId === null) || !keyPattern.test(key) || Date.parse(updatedAt) < Date.parse(createdAt)) throw invalidResponse("template.binding");
  return { id, workspaceId, key, name: text(row.name, "template.name", 128), description: text(row.description, "template.description", 2048), builtIn, kind, currentRevisionId, version: integer(row.version, "template.version", 1), currentRevision, createdAt, updatedAt };
};

const decodeSnapshotMaterial = (value: unknown, field: string): OrganizingSnapshotMaterial => {
  const row = exact(value, ["position", "reference", "evidence"], field);
  const reference = decodeOrganizingMaterialReference(row.reference, `${field}.reference`);
  const evidence = array(row.evidence, `${field}.evidence`, 100, (item, index) => decodeEvidence(item, `${field}.evidence[${String(index)}]`));
  return { position: integer(row.position, `${field}.position`), reference, evidence };
};

export const decodeOrganizingSnapshot = (value: unknown): OrganizingSnapshot => {
  const row = exact(value, ["id", "workspace_id", "draft_id", "draft_version", "template_id", "template_revision_id", "template_hash", "intent", "materials", "canonical_hash", "created_at"], "snapshot");
  const materials = array(row.materials, "snapshot.materials", 500, (item, index) => decodeSnapshotMaterial(item, `snapshot.materials[${String(index)}]`));
  if (materials.length === 0 || materials.some((item, index) => item.position !== index)) throw invalidResponse("snapshot.materials");
  return { id: uuid(row.id, "snapshot.id"), workspaceId: uuid(row.workspace_id, "snapshot.workspace_id"), draftId: uuid(row.draft_id, "snapshot.draft_id"), draftVersion: integer(row.draft_version, "snapshot.draft_version", 1), templateId: uuid(row.template_id, "snapshot.template_id"), templateRevisionId: uuid(row.template_revision_id, "snapshot.template_revision_id"), templateHash: hash(row.template_hash, "snapshot.template_hash"), intent: text(row.intent, "snapshot.intent", 4096), canonicalHash: hash(row.canonical_hash, "snapshot.canonical_hash"), materials, createdAt: timestamp(row.created_at, "snapshot.created_at") };
};

export const decodeOrganizingRun = (value: unknown, expectedWorkspaceId?: string): OrganizingRun => {
  const row = exact(value, ["snapshot_id", "dispatch_status", "workflow_status", "retryable", "attempt_count", "last_error_code", "binding", "result", "updated_at"], "run");
  const snapshotId = uuid(row.snapshot_id, "run.snapshot_id");
  const dispatchStatus = enumValue(row.dispatch_status, ["PENDING", "STARTED", "POISONED"] as const, "run.dispatch_status");
  const workflowStatus = row.workflow_status === null ? null : enumValue(row.workflow_status, ["pending", "running", "waiting_for_human", "retry_wait", "paused", "succeeded", "failed", "cancelled"] as const, "run.workflow_status");
  const retryable = boolean(row.retryable, "run.retryable");
  const attemptCount = integer(row.attempt_count, "run.attempt_count");
  const errorCode = row.last_error_code === null ? null : text(row.last_error_code, "run.last_error_code", 128);
  if (errorCode !== null && !errorCodePattern.test(errorCode)) throw invalidResponse("run.last_error_code");

  let workflowRunId: string | null = null;
  let bindingId: string | null = null;
  let bindingWorkspaceId: string | null = null;
  if (row.binding !== null) {
    const binding = exact(row.binding, ["id", "workspace_id", "snapshot_id", "workflow_run_id", "definition_key", "definition_version", "created_at"], "run.binding");
    bindingId = uuid(binding.id, "run.binding.id");
    if (uuid(binding.snapshot_id, "run.binding.snapshot_id") !== snapshotId ||
      !["organizing.topic-article", "organizing.merge-documents", "organizing.knowledge-report", "organizing.interview-review"].includes(text(binding.definition_key, "run.binding.definition_key", 128))) throw invalidResponse("run.binding");
    bindingWorkspaceId = uuid(binding.workspace_id, "run.binding.workspace_id");
    if (expectedWorkspaceId !== undefined && bindingWorkspaceId !== expectedWorkspaceId) throw invalidResponse("run.binding.workspace_id");
    workflowRunId = uuid(binding.workflow_run_id, "run.binding.workflow_run_id");
    integer(binding.definition_version, "run.binding.definition_version", 1);
    timestamp(binding.created_at, "run.binding.created_at");
  }

  let resultKind: OrganizingResultKind | null = null;
  let resultRef: string | null = null;
  let resultHash: string | null = null;
  if (row.result !== null) {
    const result = exact(row.result, ["id", "workspace_id", "run_binding_id", "snapshot_id", "workflow_run_id", "node_run_id", "kind", "result_ref", "result_hash", "created_at"], "run.result");
    uuid(result.id, "run.result.id");
    const resultWorkspaceId = uuid(result.workspace_id, "run.result.workspace_id");
    uuid(result.node_run_id, "run.result.node_run_id");
    if (bindingId === null || resultWorkspaceId !== bindingWorkspaceId || uuid(result.run_binding_id, "run.result.run_binding_id") !== bindingId ||
      uuid(result.snapshot_id, "run.result.snapshot_id") !== snapshotId || uuid(result.workflow_run_id, "run.result.workflow_run_id") !== workflowRunId) throw invalidResponse("run.result.binding");
    resultKind = enumValue(result.kind, ["MERGE_PROPOSAL", "ARTIFACT"] as const, "run.result.kind");
    resultRef = uuid(result.result_ref, "run.result.result_ref");
    resultHash = hash(result.result_hash, "run.result.result_hash");
    timestamp(result.created_at, "run.result.created_at");
  }

  if (dispatchStatus === "PENDING" && (workflowStatus !== null || workflowRunId !== null || row.result !== null || !retryable)
    || dispatchStatus === "POISONED" && (workflowStatus !== null || workflowRunId !== null || row.result !== null || retryable || errorCode === null)
    || dispatchStatus === "STARTED" && (workflowStatus === null || workflowRunId === null || retryable)
    || resultKind !== null && workflowStatus !== "succeeded") throw invalidResponse("run.binding");

  const status: OrganizingRunStatus = dispatchStatus === "PENDING"
    ? "QUEUED"
    : dispatchStatus === "POISONED" || workflowStatus === "failed" || workflowStatus === "cancelled"
      ? "FAILED"
      : workflowStatus === "waiting_for_human"
        ? "WAITING_FOR_HUMAN"
        : workflowStatus === "succeeded" && resultKind !== null ? "SUCCEEDED" : "STARTED";
  return { snapshotId, dispatchStatus, workflowStatus, workflowRunId, status, resultKind, resultRef, resultHash, errorCode, retryable, attemptCount, updatedAt: timestamp(row.updated_at, "run.updated_at") };
};

const requireUuid = (value: string, field: string): string => {
  if (!uuidPattern.test(value)) throw invalidRequest(field);
  return value;
};

const requireVersion = (value: number): number => {
  if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest("expectedVersion");
  return value;
};

const requireLimit = (value: number | undefined): number => {
  const limit = value ?? 25;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 25) throw invalidRequest("limit");
  return limit;
};

const requireText = (value: string, field: string, maxBytes: number, allowEmpty = false): string => {
  if (typeof value !== "string" || encoder.encode(value).byteLength > maxBytes || value.includes("\u0000") || (!allowEmpty && value.trim() === "")) throw invalidRequest(field);
  return value;
};

const requireKey = (value: string): string => {
  if (value.trim() === "" || value !== value.trim() || encoder.encode(value).byteLength > 128 || controlPattern.test(value)) throw invalidRequest("idempotencyKey");
  return value;
};

const withSignal = (signal?: AbortSignal): RequestInit => signal === undefined ? {} : { signal };
const isAbortError = (value: unknown): boolean => value instanceof DOMException ? value.name === "AbortError" : isRecord(value) && value.name === "AbortError";

const decodeHttpProblem = (value: unknown, status: number): OrganizingApiError => {
  try {
    const problem = decodeProblem(value);
    if (!errorCodePattern.test(problem.errorCode)) throw invalidResponse("problem.error_code", status);
    return new OrganizingApiError("HTTP_ERROR", problem.errorCode, problem.message, status, problem.retryable, {
      ...(problem.workflowRunId === undefined ? {} : { workflowRunId: problem.workflowRunId }),
      ...(problem.details === undefined ? {} : { details: problem.details }),
    });
  } catch (error: unknown) {
    if (error instanceof OrganizingApiError && error.code === "HTTP_ERROR") return error;
    return invalidResponse("problem", status, error);
  }
};

const request = async (path: string, init: RequestInit, statuses: readonly number[]): Promise<{ payload: unknown; status: number }> => {
  let response: Response;
  try {
    response = await authFetch(path, init);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new OrganizingApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接整理服务。", null, true, { cause: error });
  }
  const contentType = response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase();
  if (contentType !== "application/json") throw invalidResponse("content_type", response.status);
  let payload: unknown;
  try {
    payload = strictJson(await response.text());
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw invalidResponse("json", response.status, error);
  }
  if (!response.ok) throw decodeHttpProblem(payload, response.status);
  if (!statuses.includes(response.status)) throw invalidResponse("status", response.status);
  return { payload, status: response.status };
};

const mutationInit = (idempotencyKey: string, body: Readonly<Record<string, unknown>>, signal?: AbortSignal, method = "POST"): RequestInit => ({
  method,
  headers: { "Idempotency-Key": requireKey(idempotencyKey) },
  body: JSON.stringify(body),
  ...withSignal(signal),
});

const decodeDraftResult = (value: unknown, workspaceId: string, draftId?: string): OrganizingCommandResult => {
  const row = exact(value, ["draft", "replayed"], "draft_command");
  const draft = decodeOrganizingDraft(row.draft);
  if (draft.workspaceId !== workspaceId || draftId !== undefined && draft.id !== draftId) throw invalidResponse("draft_command.binding");
  return { draft, replayed: boolean(row.replayed, "draft_command.replayed") };
};

const assertReplayStatus = (status: number, replayed: boolean): void => {
  if ((status === 200) !== replayed) throw invalidResponse("command.status", status);
};

export const createOrganizingDraft = async (input: CreateOrganizingDraftInput): Promise<OrganizingCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const result = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts`, mutationInit(input.idempotencyKey, { intent: requireText(input.intent, "intent", 4096) }, input.signal), [200, 201]);
  const decoded = decodeDraftResult(result.payload, workspaceId);
  assertReplayStatus(result.status, decoded.replayed);
  return decoded;
};

export const getOrganizingDraft = async (workspaceIdValue: string, draftIdValue: string, signal?: AbortSignal): Promise<OrganizingDraft> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const draftId = requireUuid(draftIdValue, "draftId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts/${encodeURIComponent(draftId)}`, withSignal(signal), [200]);
  const row = exact(payload, ["draft"], "draft_detail");
  const draft = decodeOrganizingDraft(row.draft);
  if (draft.workspaceId !== workspaceId || draft.id !== draftId) throw invalidResponse("draft_detail.binding");
  return draft;
};

export const updateOrganizingDraft = async (input: UpdateOrganizingDraftInput): Promise<OrganizingCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts/${encodeURIComponent(draftId)}`, mutationInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion), intent: requireText(input.intent, "intent", 4096), template_revision_id: requireUuid(input.templateRevisionId, "templateRevisionId") }, input.signal, "PUT"), [200]);
  return decodeDraftResult(payload, workspaceId, draftId);
};

export const suggestOrganizingMaterials = async (input: OrganizingDraftCommandInput): Promise<OrganizingCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts/${encodeURIComponent(draftId)}/suggestions`, mutationInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion) }, input.signal), [200]);
  return decodeDraftResult(payload, workspaceId, draftId);
};

export const searchOrganizingMaterials = async (input: SearchOrganizingMaterialsInput): Promise<OrganizingMaterialSearchResult[]> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const query = requireText(input.query, "query", 256).trim();
  if (encoder.encode(query).byteLength < 2 || controlPattern.test(query)) throw invalidRequest("query");
  const kind = enumValue(input.kind, materialKinds, "kind");
  const params = new URLSearchParams({ q: query, kind, limit: String(requireLimit(input.limit)) });
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/materials/search?${params.toString()}`, withSignal(input.signal), [200]);
  const row = exact(payload, ["workspace_id", "items"], "material_search");
  if (uuid(row.workspace_id, "material_search.workspace_id") !== workspaceId) throw invalidResponse("material_search.workspace_id");
  const items = array(row.items, "material_search.items", 25, (item, index) => decodeMaterialSearchResult(item, workspaceId, `material_search.items[${String(index)}]`));
  assertUnique(items.map((item) => materialReferenceIdentity(item.reference)), "material_search.items");
  return items;
};

export const addOrganizingMaterial = async (input: AddOrganizingMaterialInput): Promise<OrganizingCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const selector: Record<string, string> = (() => {
    switch (input.kind) {
      case "SOURCE_VERSION": return { source_version_id: requireUuid(input.sourceVersionId, "sourceVersionId") };
      case "DOCUMENT_REVISION": return { document_id: requireUuid(input.documentId, "documentId"), article_revision_id: requireUuid(input.articleRevisionId, "articleRevisionId") };
      case "CLAIM": return { claim_id: requireUuid(input.claimId, "claimId") };
      case "SMART_COLLECTION": return { collection_id: requireUuid(input.collectionId, "collectionId") };
      default: throw invalidRequest("kind");
    }
  })();
  const result = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts/${encodeURIComponent(draftId)}/materials`, mutationInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion), kind: input.kind, ...selector }, input.signal), [200, 201]);
  const decoded = decodeDraftResult(result.payload, workspaceId, draftId);
  assertReplayStatus(result.status, decoded.replayed);
  return decoded;
};

export const removeOrganizingMaterial = async (input: RemoveOrganizingMaterialInput): Promise<OrganizingCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const materialId = requireUuid(input.materialId, "materialId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts/${encodeURIComponent(draftId)}/materials/${encodeURIComponent(materialId)}`, mutationInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion) }, input.signal, "DELETE"), [200]);
  return decodeDraftResult(payload, workspaceId, draftId);
};

export const setOrganizingMaterialSelection = async (input: SetOrganizingMaterialSelectionInput): Promise<OrganizingCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const materialId = requireUuid(input.materialId, "materialId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts/${encodeURIComponent(draftId)}/materials/${encodeURIComponent(materialId)}`, mutationInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion), selected: input.selected }, input.signal, "PATCH"), [200]);
  const result = decodeDraftResult(payload, workspaceId, draftId);
  if (!result.draft.materials.some((material) => material.id === materialId && material.selected === input.selected)) throw invalidResponse("material_selection.binding");
  return result;
};

export const confirmOrganizingDraft = async (input: ConfirmOrganizingDraftInput): Promise<ConfirmOrganizingDraftResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const { payload, status } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/drafts/${encodeURIComponent(draftId)}/confirm`, mutationInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion), template_revision_id: requireUuid(input.templateRevisionId, "templateRevisionId") }, input.signal), [200, 202]);
  const row = exact(payload, ["draft", "snapshot", "dispatch_status", "replayed"], "confirm");
  const draft = decodeOrganizingDraft(row.draft);
  const snapshot = decodeOrganizingSnapshot(row.snapshot);
  const replayed = boolean(row.replayed, "confirm.replayed");
  if (row.dispatch_status !== "PENDING" || draft.workspaceId !== workspaceId || draft.id !== draftId ||
    draft.status !== "CONFIRMED" || draft.confirmedSnapshotId !== snapshot.id || draft.version !== snapshot.draftVersion + 1 ||
    snapshot.workspaceId !== workspaceId || snapshot.draftId !== draftId || snapshot.templateRevisionId !== input.templateRevisionId ||
    (status === 200) !== replayed) throw invalidResponse("confirm.binding", status);
  return { draft, snapshot, dispatchStatus: "PENDING", replayed };
};

export const getOrganizingSnapshot = async (workspaceIdValue: string, snapshotIdValue: string, signal?: AbortSignal): Promise<OrganizingSnapshot> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const snapshotId = requireUuid(snapshotIdValue, "snapshotId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/snapshots/${encodeURIComponent(snapshotId)}`, withSignal(signal), [200]);
  const row = exact(payload, ["snapshot"], "snapshot_detail");
  const snapshot = decodeOrganizingSnapshot(row.snapshot);
  if (snapshot.workspaceId !== workspaceId || snapshot.id !== snapshotId) throw invalidResponse("snapshot_detail.binding");
  return snapshot;
};

export const listOrganizingTemplates = async (workspaceIdValue: string, signal?: AbortSignal): Promise<OrganizingTemplate[]> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/templates`, withSignal(signal), [200]);
  const row = exact(payload, ["workspace_id", "items"], "template_list");
  if (uuid(row.workspace_id, "template_list.workspace_id") !== workspaceId) throw invalidResponse("template_list.workspace_id");
  const items = array(row.items, "template_list.items", 100, (item) => decodeOrganizingTemplate(item));
  if (items.some((item) => item.workspaceId !== null && item.workspaceId !== workspaceId)) throw invalidResponse("template_list.binding");
  assertUnique(items.map((item) => item.id), "template_list.items.id");
  return items;
};

export const getOrganizingTemplate = async (workspaceIdValue: string, templateIdValue: string, signal?: AbortSignal): Promise<OrganizingTemplate> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const templateId = requireUuid(templateIdValue, "templateId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/templates/${encodeURIComponent(templateId)}`, withSignal(signal), [200]);
  const row = exact(payload, ["template"], "template_detail");
  const template = decodeOrganizingTemplate(row.template);
  if (template.id !== templateId || template.workspaceId !== null && template.workspaceId !== workspaceId) throw invalidResponse("template_detail.binding");
  return template;
};

const decodeTemplateResult = (value: unknown, workspaceId: string, templateId?: string): { template: OrganizingTemplate; replayed: boolean } => {
  const row = exact(value, ["template", "replayed"], "template_command");
  const template = decodeOrganizingTemplate(row.template);
  if (template.workspaceId !== workspaceId || templateId !== undefined && template.id !== templateId) throw invalidResponse("template_command.binding");
  return { template, replayed: boolean(row.replayed, "template_command.replayed") };
};

const encodeTemplateDeclaration = (value: OrganizingTemplateDeclaration): Record<string, unknown> => ({
  schema_version: value.schemaVersion,
  kind: value.kind,
  name: value.name,
  description: value.description,
  materials: {
    allowed_kinds: value.materials.allowedKinds,
    min_materials: value.materials.minMaterials,
    max_materials: value.materials.maxMaterials,
  },
  sections: value.sections.map((section) => ({ key: section.key, title: section.title, required: section.required })),
  presentation: {
    audience: value.presentation.audience,
    language: value.presentation.language,
    tone: value.presentation.tone,
    length: value.presentation.length,
    include_code: value.presentation.includeCode,
    include_examples: value.presentation.includeExamples,
    include_faq: value.presentation.includeFaq,
  },
  output: { directory: value.output.directory, filename_pattern: value.output.filenamePattern },
  additional_instructions: value.additionalInstructions,
});

export const cloneOrganizingTemplate = async (input: CloneOrganizingTemplateInput): Promise<{ template: OrganizingTemplate; replayed: boolean }> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const templateId = requireUuid(input.templateId, "templateId");
  const result = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/templates/${encodeURIComponent(templateId)}/clone`, mutationInit(input.idempotencyKey, { name: requireText(input.name, "name", 128) }, input.signal), [200, 201]);
  const decoded = decodeTemplateResult(result.payload, workspaceId);
  assertReplayStatus(result.status, decoded.replayed);
  return decoded;
};

export const reviseOrganizingTemplate = async (input: ReviseOrganizingTemplateInput): Promise<{ template: OrganizingTemplate; replayed: boolean }> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const templateId = requireUuid(input.templateId, "templateId");
  const declaration = decodeTemplateDeclaration(encodeTemplateDeclaration(input.declaration), "declaration");
  const result = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/templates/${encodeURIComponent(templateId)}/revisions`, mutationInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion), declaration: encodeTemplateDeclaration(declaration) }, input.signal), [200, 201]);
  const decoded = decodeTemplateResult(result.payload, workspaceId, templateId);
  assertReplayStatus(result.status, decoded.replayed);
  return decoded;
};

export const getOrganizingRun = async (workspaceIdValue: string, snapshotIdValue: string, signal?: AbortSignal): Promise<OrganizingRun> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const snapshotId = requireUuid(snapshotIdValue, "snapshotId");
  const { payload } = await request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/organizing/runs/${encodeURIComponent(snapshotId)}`, withSignal(signal), [200]);
  const row = exact(payload, ["run"], "run_detail");
  const run = decodeOrganizingRun(row.run, workspaceId);
  if (run.snapshotId !== snapshotId) throw invalidResponse("run_detail.binding");
  return run;
};
