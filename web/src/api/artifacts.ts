/** Artifact 的唯一网络边界：严格解码隔离产物及其不可变 Revision。 */

import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isAbortError, isRecord } from "../shared/codec";
import { ArtifactsApi as GeneratedArtifactsApi } from "./generated/apis/ArtifactsApi";
import {
  generatedConfiguration,
  generatedRawResponse,
  generatedRequestInit,
} from "./generated-client";

const artifactsApi = new GeneratedArtifactsApi(generatedConfiguration);

export type ArtifactStatus = "PLANNING" | "OUTLINE_REVIEW" | "GENERATING" | "DRAFT" | "APPROVED" | "EXPORTED" | "PUBLISH_PROPOSED" | "PUBLISHED" | "ARCHIVED";
export type CoverageStatus = "COVERED" | "PARTIAL" | "GAP";
export type ArtifactCreator = "HUMAN" | "AGENT";
export type ArtifactSectionGenerationStatus = "PENDING" | "COMPLETED" | "FAILED" | "CANCELLED" | "RECOVERY_REQUIRED";
export type ArtifactSectionGenerationPersistedStatus = Exclude<ArtifactSectionGenerationStatus, "COMPLETED">;

export interface ArtifactGap { code: string; description: string; }
export interface ArtifactCoverage { sectionKey: string; status: CoverageStatus; gaps: ArtifactGap[]; }
export interface ArtifactCitation { sourceVersionId: string; sourceSpanId: string; verifiedContentHash: string; excerpt: string; verified: true; }
export interface ArtifactDocumentSource { documentId: string; articleRevisionId: string; revisionNo: number; verifiedContentHash: string; verified: true; }
export interface ArtifactOutlineSection { key: string; title: string; }
export interface ArtifactSection { key: string; title: string; content: string; citations: ArtifactCitation[]; documentSources: ArtifactDocumentSource[]; coverage: ArtifactCoverage; }
export interface ArtifactRevision {
  id: string; artifactId: string; revisionNo: number; outline: ArtifactOutlineSection[]; sections: ArtifactSection[];
  createdBy: ArtifactCreator; metadata?: { promptVersion: string; modelVersion: string; workflowDefinitionVersion: string; schemaVersion: string; };
  contentHash: string; createdAt: string;
}
export interface Artifact {
  id: string; workspaceId: string; type: string; title: string; status: ArtifactStatus; scopeDefinition: string;
  sourceCoverage: ArtifactCoverage[]; currentRevisionId: string; version: number; createdAt: string; updatedAt: string; revision: ArtifactRevision;
}
export interface ArtifactPage { workspaceId: string; items: Artifact[]; nextCursor?: string; }
export interface ArtifactExport {
  id: string; workspaceId: string; artifactId: string; revisionId: string; artifactVersion: number; revisionNo: number;
  revisionHash: string; outputHash: string; outputSize: number; exportedAt: string;
}
export interface ArtifactPublication {
  workspaceId: string; artifactId: string; revisionId: string; artifactVersion: number; revisionNo: number;
  contentHash: string; proposalId: string; createdAt: string;
}
export interface ArtifactCommandResult { artifact: Artifact; export?: ArtifactExport; publication?: ArtifactPublication; replayed: boolean; }
export interface PlanArtifactInput { workspaceId: string; type: string; title: string; scopeDefinition: string; idempotencyKey: string; }
export interface SubmitOutlineInput { workspaceId: string; artifactId: string; expectedVersion: number; outline: ArtifactOutlineSection[]; idempotencyKey: string; }
export interface RevisionCommandInput { workspaceId: string; artifactId: string; expectedVersion: number; idempotencyKey: string; }
export interface RecordGapSectionInput extends RevisionCommandInput { sectionKey: string; title: string; gapCode: string; gapDescription: string; }
export interface GenerateArtifactSectionInput extends RevisionCommandInput { sectionKey: string; }
interface ArtifactSectionGenerationBase {
  generationId: string;
  workspaceId: string;
  artifactId: string;
  sourceRevisionId: string;
  sourceRevisionNo: number;
  sourceArtifactVersion: number;
  sectionKey: string;
  workflowRunId: string;
  nodeRunId: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  statusUrl: string;
}
export type ArtifactSectionGeneration = ArtifactSectionGenerationBase & { status: ArtifactSectionGenerationPersistedStatus };
export type ArtifactSectionGenerationAcceptance = ArtifactSectionGenerationBase & { status: ArtifactSectionGenerationStatus; replayed: boolean };
export interface ArtifactSectionGenerationPage { workspaceId: string; artifactId: string; items: ArtifactSectionGeneration[]; }

export class ArtifactApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(code: ArtifactApiError["code"], errorCode: string, message: string, retryable: boolean, status: number | null = null, options?: ErrorOptions) {
    super(message, options);
    this.name = "ArtifactApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/;
const generationSectionKeyPattern = /^[a-z0-9-]+$/;
const statuses: readonly ArtifactStatus[] = ["PLANNING", "OUTLINE_REVIEW", "GENERATING", "DRAFT", "APPROVED", "EXPORTED", "PUBLISH_PROPOSED", "PUBLISHED", "ARCHIVED"];
const coverageStatuses: readonly CoverageStatus[] = ["COVERED", "PARTIAL", "GAP"];
const generationStatuses: readonly ArtifactSectionGenerationStatus[] = ["PENDING", "COMPLETED", "FAILED", "CANCELLED", "RECOVERY_REQUIRED"];
const persistedGenerationStatuses: readonly ArtifactSectionGenerationPersistedStatus[] = ["PENDING", "FAILED", "CANCELLED", "RECOVERY_REQUIRED"];
const generationWireKeys = ["generation_id", "workspace_id", "artifact_id", "source_revision_id", "source_revision_no", "source_artifact_version", "section_key", "workflow_run_id", "node_run_id", "status", "version", "created_at", "updated_at", "status_url"] as const;

const invalidResponse = (field: string, status: number | null = null): ArtifactApiError => new ArtifactApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `Artifact 响应字段无效：${field}`, false, status);
const invalidRequest = (field: string): ArtifactApiError => new ArtifactApiError("INVALID_REQUEST", "INVALID_REQUEST", `Artifact 请求字段无效：${field}`, false);
const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => { if (!hasOnlyKeys(value, keys)) throw invalidResponse(field); };
const stringValue = (value: unknown, field: string, allowEmpty = false): string => { if (typeof value !== "string" || (!allowEmpty && value.trim() === "")) throw invalidResponse(field); return value; };
const uuid = (value: unknown, field: string): string => { const result = stringValue(value, field); if (!uuidPattern.test(result)) throw invalidResponse(field); return result; };
const hash = (value: unknown, field: string): string => { const result = stringValue(value, field); if (!hashPattern.test(result)) throw invalidResponse(field); return result; };
const integer = (value: unknown, field: string, minimum = 0): number => { if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) throw invalidResponse(field); return value; };
const bool = (value: unknown, field: string): boolean => { if (typeof value !== "boolean") throw invalidResponse(field); return value; };
const timestamp = (value: unknown, field: string): string => { const result = stringValue(value, field); if (!timestampPattern.test(result) || !Number.isFinite(Date.parse(result))) throw invalidResponse(field); return result; };
const enumValue = <T extends string>(value: unknown, allowed: readonly T[], field: string): T => { if (typeof value !== "string" || !allowed.includes(value as T)) throw invalidResponse(field); return value as T; };
const requireUuid = (value: string, field: string): string => { if (!uuidPattern.test(value)) throw invalidRequest(field); return value; };
const requireVersion = (value: number): number => { if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest("expectedVersion"); return value; };
const requireKey = (value: string): string => { if (value.trim() === "" || value.length > 128) throw invalidRequest("idempotencyKey"); return value; };
const requireText = (value: string, field: string, maximum: number, allowEmpty = false): string => { if (typeof value !== "string" || (!allowEmpty && value.trim() === "") || value.length > maximum) throw invalidRequest(field); return value; };
const generationSectionKey = (value: unknown, field: string): string => { const result = stringValue(value, field); if (result.length > 128 || !generationSectionKeyPattern.test(result)) throw invalidResponse(field); return result; };
const requireGenerationSectionKey = (value: string): string => { if (typeof value !== "string" || value.length > 128 || !generationSectionKeyPattern.test(value)) throw invalidRequest("sectionKey"); return value; };

const decodeGap = (value: unknown, field: string): ArtifactGap => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["code", "description"], field); return { code: stringValue(value.code, `${field}.code`), description: stringValue(value.description, `${field}.description`) }; };
const decodeCoverage = (value: unknown, field: string): ArtifactCoverage => {
  if (!isRecord(value)) throw invalidResponse(field); exact(value, ["section_key", "status", "gaps"], field);
  if (!Array.isArray(value.gaps)) throw invalidResponse(`${field}.gaps`);
  const status = enumValue(value.status, coverageStatuses, `${field}.status`);
  const gaps = value.gaps.map((item, index) => decodeGap(item, `${field}.gaps[${String(index)}]`));
  if ((status === "COVERED" && gaps.length !== 0) || ((status === "PARTIAL" || status === "GAP") && gaps.length === 0)) throw invalidResponse(`${field}.gaps`);
  return { sectionKey: stringValue(value.section_key, `${field}.section_key`), status, gaps };
};
const decodeCitation = (value: unknown, field: string): ArtifactCitation => {
  if (!isRecord(value)) throw invalidResponse(field); exact(value, ["source_version_id", "source_span_id", "verified_content_hash", "excerpt", "verified"], field);
  if (!bool(value.verified, `${field}.verified`)) throw invalidResponse(`${field}.verified`);
  return { sourceVersionId: uuid(value.source_version_id, `${field}.source_version_id`), sourceSpanId: uuid(value.source_span_id, `${field}.source_span_id`), verifiedContentHash: hash(value.verified_content_hash, `${field}.verified_content_hash`), excerpt: stringValue(value.excerpt, `${field}.excerpt`), verified: true };
};
const decodeDocumentSource = (value: unknown, field: string): ArtifactDocumentSource => {
  if (!isRecord(value)) throw invalidResponse(field); exact(value, ["document_id", "article_revision_id", "revision_no", "verified_content_hash", "verified"], field);
  if (!bool(value.verified, `${field}.verified`)) throw invalidResponse(`${field}.verified`);
  return {
    documentId: uuid(value.document_id, `${field}.document_id`),
    articleRevisionId: uuid(value.article_revision_id, `${field}.article_revision_id`),
    revisionNo: integer(value.revision_no, `${field}.revision_no`, 1),
    verifiedContentHash: hash(value.verified_content_hash, `${field}.verified_content_hash`),
    verified: true,
  };
};
const decodeOutline = (value: unknown, field: string): ArtifactOutlineSection[] => {
  if (!Array.isArray(value)) throw invalidResponse(field);
  const keys = new Set<string>();
  return value.map((item, index) => { const itemField = `${field}[${String(index)}]`; if (!isRecord(item)) throw invalidResponse(itemField); exact(item, ["key", "title"], itemField); const key = stringValue(item.key, `${itemField}.key`); if (keys.has(key)) throw invalidResponse(`${itemField}.key`); keys.add(key); return { key, title: stringValue(item.title, `${itemField}.title`) }; });
};
const decodeSection = (value: unknown, field: string): ArtifactSection => {
  if (!isRecord(value)) throw invalidResponse(field); exact(value, ["key", "title", "content", "citations", "document_sources", "coverage"], field);
  if (!Array.isArray(value.citations) || !Array.isArray(value.document_sources)) throw invalidResponse(field);
  const coverage = decodeCoverage(value.coverage, `${field}.coverage`);
  const citations = value.citations.map((item, index) => decodeCitation(item, `${field}.citations[${String(index)}]`));
  const documentSources = value.document_sources.map((item, index) => decodeDocumentSource(item, `${field}.document_sources[${String(index)}]`));
  const documentSourceIdentities = documentSources.map((source) => `${source.documentId}\u0000${source.articleRevisionId}`);
  const content = stringValue(value.content, `${field}.content`, true);
  const supportCount = citations.length + documentSources.length;
  if (coverage.sectionKey !== stringValue(value.key, `${field}.key`)
    || new Set(documentSourceIdentities).size !== documentSourceIdentities.length
    || (coverage.status === "GAP" && (content !== "" || supportCount !== 0))
    || (coverage.status !== "GAP" && supportCount === 0)) throw invalidResponse(field);
  return { key: coverage.sectionKey, title: stringValue(value.title, `${field}.title`), content, citations, documentSources, coverage };
};
const decodeRevision = (value: unknown, field: string): ArtifactRevision => {
  if (!isRecord(value)) throw invalidResponse(field); exact(value, ["id", "artifact_id", "revision_no", "outline", "sections", "created_by", "metadata", "content_hash", "created_at"], field);
  const createdBy = enumValue(value.created_by, ["HUMAN", "AGENT"] as const, `${field}.created_by`);
  let metadata: ArtifactRevision["metadata"];
  if (value.metadata !== undefined) { if (!isRecord(value.metadata)) throw invalidResponse(`${field}.metadata`); exact(value.metadata, ["prompt_version", "model_version", "workflow_definition_version", "schema_version"], `${field}.metadata`); metadata = { promptVersion: stringValue(value.metadata.prompt_version, `${field}.metadata.prompt_version`), modelVersion: stringValue(value.metadata.model_version, `${field}.metadata.model_version`), workflowDefinitionVersion: stringValue(value.metadata.workflow_definition_version, `${field}.metadata.workflow_definition_version`), schemaVersion: stringValue(value.metadata.schema_version, `${field}.metadata.schema_version`) }; }
  if ((createdBy === "HUMAN") !== (metadata === undefined)) throw invalidResponse(`${field}.metadata`);
  const outline = decodeOutline(value.outline, `${field}.outline`);
  if (!Array.isArray(value.sections)) throw invalidResponse(`${field}.sections`);
  const sections = value.sections.map((item, index) => decodeSection(item, `${field}.sections[${String(index)}]`));
  const outlineKeys = new Set(outline.map((item) => item.key));
  if (sections.some((section) => !outlineKeys.has(section.key)) || new Set(sections.map((section) => section.key)).size !== sections.length) throw invalidResponse(`${field}.sections`);
  return { id: uuid(value.id, `${field}.id`), artifactId: uuid(value.artifact_id, `${field}.artifact_id`), revisionNo: integer(value.revision_no, `${field}.revision_no`, 1), outline, sections, createdBy, ...(metadata === undefined ? {} : { metadata }), contentHash: hash(value.content_hash, `${field}.content_hash`), createdAt: timestamp(value.created_at, `${field}.created_at`) };
};

export const decodeArtifact = (value: unknown): Artifact => {
  if (!isRecord(value)) throw invalidResponse("artifact"); exact(value, ["id", "workspace_id", "type", "title", "status", "scope_definition", "source_coverage", "current_revision_id", "version", "created_at", "updated_at", "revision"], "artifact");
  if (!Array.isArray(value.source_coverage)) throw invalidResponse("artifact.source_coverage");
  const revision = decodeRevision(value.revision, "artifact.revision");
  const id = uuid(value.id, "artifact.id"); const workspaceId = uuid(value.workspace_id, "artifact.workspace_id"); const currentRevisionId = uuid(value.current_revision_id, "artifact.current_revision_id");
  const sourceCoverage = value.source_coverage.map((item, index) => decodeCoverage(item, `artifact.source_coverage[${String(index)}]`));
  const coverageBySection = new Map(sourceCoverage.map((coverage) => [coverage.sectionKey, coverage]));
  const matchesRevisionCoverage = revision.sections.every((section) => {
    const coverage = coverageBySection.get(section.key);
    if (coverage === undefined) return false;
    return coverage.status === section.coverage.status
      && coverage.gaps.length === section.coverage.gaps.length
      && coverage.gaps.every((gap, index) => {
        const sectionGap = section.coverage.gaps[index];
        if (sectionGap?.code !== gap.code) return false;
        return gap.description === sectionGap.description;
      });
  });
  if (revision.id !== currentRevisionId || revision.artifactId !== id || new Set(sourceCoverage.map((item) => item.sectionKey)).size !== sourceCoverage.length || sourceCoverage.length !== revision.sections.length || !matchesRevisionCoverage) throw invalidResponse("artifact.revision_binding");
  return { id, workspaceId, type: stringValue(value.type, "artifact.type"), title: stringValue(value.title, "artifact.title"), status: enumValue(value.status, statuses, "artifact.status"), scopeDefinition: stringValue(value.scope_definition, "artifact.scope_definition"), sourceCoverage, currentRevisionId, version: integer(value.version, "artifact.version", 1), createdAt: timestamp(value.created_at, "artifact.created_at"), updatedAt: timestamp(value.updated_at, "artifact.updated_at"), revision };
};

export const decodeArtifactPage = (value: unknown): ArtifactPage => {
  if (!isRecord(value)) throw invalidResponse("artifact_page"); exact(value, ["workspace_id", "items", "next_cursor"], "artifact_page"); if (!Array.isArray(value.items)) throw invalidResponse("artifact_page.items");
  const workspaceId = uuid(value.workspace_id, "artifact_page.workspace_id"); const items = value.items.map(decodeArtifact);
  if (items.some((item) => item.workspaceId !== workspaceId) || new Set(items.map((item) => item.id)).size !== items.length) throw invalidResponse("artifact_page.items");
  const nextCursor = value.next_cursor === undefined ? undefined : stringValue(value.next_cursor, "artifact_page.next_cursor");
  return { workspaceId, items, ...(nextCursor === undefined ? {} : { nextCursor }) };
};
const decodeExport = (value: unknown, field: string): ArtifactExport => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["id", "workspace_id", "artifact_id", "revision_id", "artifact_version", "revision_no", "revision_hash", "output_hash", "output_size", "exported_at"], field); return { id: uuid(value.id, `${field}.id`), workspaceId: uuid(value.workspace_id, `${field}.workspace_id`), artifactId: uuid(value.artifact_id, `${field}.artifact_id`), revisionId: uuid(value.revision_id, `${field}.revision_id`), artifactVersion: integer(value.artifact_version, `${field}.artifact_version`, 1), revisionNo: integer(value.revision_no, `${field}.revision_no`, 1), revisionHash: hash(value.revision_hash, `${field}.revision_hash`), outputHash: hash(value.output_hash, `${field}.output_hash`), outputSize: integer(value.output_size, `${field}.output_size`), exportedAt: timestamp(value.exported_at, `${field}.exported_at`) }; };
const decodePublication = (value: unknown, field: string): ArtifactPublication => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["workspace_id", "artifact_id", "revision_id", "artifact_version", "revision_no", "content_hash", "proposal_id", "created_at"], field); return { workspaceId: uuid(value.workspace_id, `${field}.workspace_id`), artifactId: uuid(value.artifact_id, `${field}.artifact_id`), revisionId: uuid(value.revision_id, `${field}.revision_id`), artifactVersion: integer(value.artifact_version, `${field}.artifact_version`, 1), revisionNo: integer(value.revision_no, `${field}.revision_no`, 1), contentHash: hash(value.content_hash, `${field}.content_hash`), proposalId: uuid(value.proposal_id, `${field}.proposal_id`), createdAt: timestamp(value.created_at, `${field}.created_at`) }; };
export const decodeArtifactCommandResult = (value: unknown): ArtifactCommandResult => {
  if (!isRecord(value)) throw invalidResponse("artifact_command"); exact(value, ["artifact", "export", "publication", "replayed"], "artifact_command");
  const artifact = decodeArtifact(value.artifact); const exported = value.export === undefined ? undefined : decodeExport(value.export, "artifact_command.export"); const publication = value.publication === undefined ? undefined : decodePublication(value.publication, "artifact_command.publication");
  if ((exported !== undefined && (exported.workspaceId !== artifact.workspaceId || exported.artifactId !== artifact.id || exported.revisionId !== artifact.revision.id || exported.artifactVersion !== artifact.version || exported.revisionNo !== artifact.revision.revisionNo || exported.revisionHash !== artifact.revision.contentHash)) || (publication !== undefined && (publication.workspaceId !== artifact.workspaceId || publication.artifactId !== artifact.id || publication.revisionId !== artifact.revision.id || publication.artifactVersion !== artifact.version || publication.revisionNo !== artifact.revision.revisionNo || publication.contentHash !== artifact.revision.contentHash))) throw invalidResponse("artifact_command.binding");
  return { artifact, ...(exported === undefined ? {} : { export: exported }), ...(publication === undefined ? {} : { publication }), replayed: bool(value.replayed, "artifact_command.replayed") };
};

const decodeArtifactSectionGenerationFields = <T extends ArtifactSectionGenerationStatus>(value: Record<string, unknown>, field: string, allowedStatuses: readonly T[]): ArtifactSectionGenerationBase & { status: T } => {
  const workflowRunId = uuid(value.workflow_run_id, `${field}.workflow_run_id`);
  const statusUrl = stringValue(value.status_url, `${field}.status_url`);
  if (statusUrl !== `/api/v1/workflows/${workflowRunId}`) throw invalidResponse("artifact_generation.status_url");
  return {
    generationId: uuid(value.generation_id, `${field}.generation_id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    artifactId: uuid(value.artifact_id, `${field}.artifact_id`),
    sourceRevisionId: uuid(value.source_revision_id, `${field}.source_revision_id`),
    sourceRevisionNo: integer(value.source_revision_no, `${field}.source_revision_no`, 1),
    sourceArtifactVersion: integer(value.source_artifact_version, `${field}.source_artifact_version`, 1),
    sectionKey: generationSectionKey(value.section_key, `${field}.section_key`),
    workflowRunId,
    nodeRunId: uuid(value.node_run_id, `${field}.node_run_id`),
    status: enumValue(value.status, allowedStatuses, `${field}.status`),
    version: integer(value.version, `${field}.version`, 1),
    createdAt: timestamp(value.created_at, `${field}.created_at`),
    updatedAt: timestamp(value.updated_at, `${field}.updated_at`),
    statusUrl,
  };
};

export const decodeArtifactSectionGenerationAcceptance = (value: unknown): ArtifactSectionGenerationAcceptance => {
  if (!isRecord(value)) throw invalidResponse("artifact_generation");
  exact(value, [...generationWireKeys, "replayed"], "artifact_generation");
  return { ...decodeArtifactSectionGenerationFields(value, "artifact_generation", generationStatuses), replayed: bool(value.replayed, "artifact_generation.replayed") };
};

export const decodeArtifactSectionGenerationPage = (value: unknown): ArtifactSectionGenerationPage => {
  if (!isRecord(value)) throw invalidResponse("artifact_generation_page");
  exact(value, ["workspace_id", "artifact_id", "items"], "artifact_generation_page");
  if (!Array.isArray(value.items)) throw invalidResponse("artifact_generation_page.items");
  const workspaceId = uuid(value.workspace_id, "artifact_generation_page.workspace_id");
  const artifactId = uuid(value.artifact_id, "artifact_generation_page.artifact_id");
  const sectionKeys = new Set<string>();
  const generationIds = new Set<string>();
  const items = value.items.map((item, index) => {
    const field = `artifact_generation_page.items[${String(index)}]`;
    if (!isRecord(item)) throw invalidResponse(field);
    exact(item, generationWireKeys, field);
    const generation = decodeArtifactSectionGenerationFields(item, field, persistedGenerationStatuses);
    if (generation.workspaceId !== workspaceId || generation.artifactId !== artifactId || sectionKeys.has(generation.sectionKey) || generationIds.has(generation.generationId)) throw invalidResponse("artifact_generation_page.items");
    sectionKeys.add(generation.sectionKey);
    generationIds.add(generation.generationId);
    return generation;
  });
  return { workspaceId, artifactId, items };
};

const readProblem = (value: unknown, status: number): ArtifactApiError => {
  if (!isRecord(value)) return new ArtifactApiError("HTTP_ERROR", "HTTP_ERROR", `Artifact API 返回 HTTP ${String(status)}。`, status >= 500, status);
  const code = typeof value.code === "string" && value.code !== "" ? value.code : typeof value.error_code === "string" && value.error_code !== "" ? value.error_code : "HTTP_ERROR";
  const message = typeof value.detail === "string" && value.detail !== "" ? value.detail : typeof value.message === "string" && value.message !== "" ? value.message : `Artifact API 返回 HTTP ${String(status)}。`;
  return new ArtifactApiError("HTTP_ERROR", code, message, value.retryable === true, status);
};
const request = async (operation: Promise<Response>, expectedStatuses?: readonly number[]): Promise<unknown> => {
  let response: Response;
  try { response = await operation; } catch (error: unknown) { if (isAbortError(error)) throw error; throw new ArtifactApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接 Artifact API。", true, null, { cause: error }); }
  let payload: unknown; try { payload = await response.json(); } catch (error: unknown) { if (isAbortError(error)) throw error; throw new ArtifactApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Artifact API 返回了无效 JSON。", false, response.status, { cause: error }); }
  if (!response.ok) throw readProblem(payload, response.status);
  if (expectedStatuses !== undefined && !expectedStatuses.includes(response.status)) throw invalidResponse("http_status", response.status);
  return payload;
};
const command = (operation: Promise<Response>, expectedWorkspaceId: string, expectedArtifactId: string | undefined, expectedStatuses: readonly number[]): Promise<ArtifactCommandResult> => request(operation, expectedStatuses)
  .then(decodeArtifactCommandResult)
  .then((result) => {
    if (result.artifact.workspaceId !== expectedWorkspaceId || (expectedArtifactId !== undefined && result.artifact.id !== expectedArtifactId)) throw invalidResponse("artifact_command.binding");
    return result;
  });

export const listArtifacts = (workspaceId: string, input: { cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<ArtifactPage> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const limit = input.limit ?? 50;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100) throw invalidRequest("limit");
  if (input.cursor !== undefined && (input.cursor.trim() === "" || input.cursor.length > 1024)) throw invalidRequest("cursor");
  return request(generatedRawResponse(artifactsApi.listArtifactsRaw({
    workspaceId: expectedWorkspaceId,
    limit,
    ...(input.cursor === undefined ? {} : { cursor: input.cursor }),
  }, generatedRequestInit(signal))))
    .then(decodeArtifactPage)
    .then((page) => {
      if (page.workspaceId !== expectedWorkspaceId) throw invalidResponse("artifact_page.workspace_id");
      return page;
    });
};
export const getArtifact = (workspaceId: string, artifactId: string, signal?: AbortSignal): Promise<Artifact> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const expectedArtifactId = requireUuid(artifactId, "artifactId");
  return request(generatedRawResponse(artifactsApi.getArtifactRaw({
    workspaceId: expectedWorkspaceId,
    artifactId: expectedArtifactId,
  }, generatedRequestInit(signal))))
    .then(decodeArtifact)
    .then((artifact) => {
      if (artifact.workspaceId !== expectedWorkspaceId || artifact.id !== expectedArtifactId) throw invalidResponse("artifact.binding");
      return artifact;
    });
};
export const getArtifactSectionGenerations = (workspaceId: string, artifactId: string, signal?: AbortSignal): Promise<ArtifactSectionGenerationPage> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const expectedArtifactId = requireUuid(artifactId, "artifactId");
  return request(generatedRawResponse(artifactsApi.listArtifactSectionGenerationsRaw({
    workspaceId: expectedWorkspaceId,
    artifactId: expectedArtifactId,
  }, generatedRequestInit(signal))), [200])
    .then(decodeArtifactSectionGenerationPage)
    .then((page) => {
      if (page.workspaceId !== expectedWorkspaceId || page.artifactId !== expectedArtifactId) throw invalidResponse("artifact_generation_page.binding");
      return page;
    });
};
export const planArtifact = (input: PlanArtifactInput, signal?: AbortSignal): Promise<ArtifactCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  return command(generatedRawResponse(artifactsApi.planArtifactRaw({
    idempotencyKey: requireKey(input.idempotencyKey),
    artifactPlanRequest: {
      workspace_id: workspaceId,
      type: requireText(input.type, "type", 128),
      title: requireText(input.title, "title", 512),
      scope_definition: requireText(input.scopeDefinition, "scopeDefinition", 16_384),
    },
  }, generatedRequestInit(signal))), workspaceId, undefined, [200, 201]);
};
export const submitArtifactOutline = (input: SubmitOutlineInput, signal?: AbortSignal): Promise<ArtifactCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const artifactId = requireUuid(input.artifactId, "artifactId");
  const outline = input.outline.map((item) => ({ key: requireText(item.key, "outline.key", 128), title: requireText(item.title, "outline.title", 512) }));
  if (outline.length === 0 || new Set(outline.map((item) => item.key)).size !== outline.length) throw invalidRequest("outline");
  return command(generatedRawResponse(artifactsApi.submitArtifactOutlineRaw({
    artifactId,
    idempotencyKey: requireKey(input.idempotencyKey),
    artifactOutlineRequest: { workspace_id: workspaceId, expected_version: requireVersion(input.expectedVersion), outline },
  }, generatedRequestInit(signal))), workspaceId, artifactId, [200]);
};
export const approveArtifactOutline = (input: RevisionCommandInput, signal?: AbortSignal): Promise<ArtifactCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const artifactId = requireUuid(input.artifactId, "artifactId");
  return command(generatedRawResponse(artifactsApi.approveArtifactOutlineRaw({
    artifactId,
    idempotencyKey: requireKey(input.idempotencyKey),
    artifactRevisionRequest: { workspace_id: workspaceId, expected_version: requireVersion(input.expectedVersion) },
  }, generatedRequestInit(signal))), workspaceId, artifactId, [200]);
};
export const startArtifactRevision = (input: RevisionCommandInput, signal?: AbortSignal): Promise<ArtifactCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const artifactId = requireUuid(input.artifactId, "artifactId");
  return command(generatedRawResponse(artifactsApi.startArtifactRevisionRaw({
    artifactId,
    idempotencyKey: requireKey(input.idempotencyKey),
    artifactRevisionRequest: { workspace_id: workspaceId, expected_version: requireVersion(input.expectedVersion) },
  }, generatedRequestInit(signal))), workspaceId, artifactId, [200]);
};
export const recordArtifactGapSection = (input: RecordGapSectionInput, signal?: AbortSignal): Promise<ArtifactCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const artifactId = requireUuid(input.artifactId, "artifactId");
  const sectionKey = requireText(input.sectionKey, "sectionKey", 128);
  return command(generatedRawResponse(artifactsApi.recordArtifactSectionRaw({
    artifactId,
    idempotencyKey: requireKey(input.idempotencyKey),
    artifactRecordSectionRequest: {
      workspace_id: workspaceId,
      expected_version: requireVersion(input.expectedVersion),
      section: {
        key: sectionKey,
        title: requireText(input.title, "title", 512),
        content: "",
        citations: [],
        coverage: {
          section_key: sectionKey,
          status: "GAP",
          gaps: [{ code: requireText(input.gapCode, "gapCode", 128), description: requireText(input.gapDescription, "gapDescription", 4096) }],
        },
      },
    },
  }, generatedRequestInit(signal))), workspaceId, artifactId, [200]);
};
export const generateArtifactSection = (input: GenerateArtifactSectionInput, signal?: AbortSignal): Promise<ArtifactSectionGenerationAcceptance> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const artifactId = requireUuid(input.artifactId, "artifactId");
  const expectedVersion = requireVersion(input.expectedVersion);
  const sectionKey = requireGenerationSectionKey(input.sectionKey);
  return request(generatedRawResponse(artifactsApi.generateArtifactSectionRaw({
    artifactId,
    idempotencyKey: requireKey(input.idempotencyKey),
    artifactSectionGenerationRequest: { workspace_id: workspaceId, expected_version: expectedVersion, section_key: sectionKey },
  }, generatedRequestInit(signal))), [202]).then(decodeArtifactSectionGenerationAcceptance).then((generation) => {
    if (generation.workspaceId !== workspaceId || generation.artifactId !== artifactId || generation.sectionKey !== sectionKey || generation.sourceArtifactVersion !== expectedVersion) throw invalidResponse("artifact_generation.binding");
    return generation;
  });
};
const revisionCommand = (
  input: RevisionCommandInput,
  operation: (artifactId: string, workspaceId: string, idempotencyKey: string, expectedVersion: number, init: RequestInit) => Promise<Response>,
  signal?: AbortSignal,
): Promise<ArtifactCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const artifactId = requireUuid(input.artifactId, "artifactId");
  return command(operation(
    artifactId,
    workspaceId,
    requireKey(input.idempotencyKey),
    requireVersion(input.expectedVersion),
    generatedRequestInit(signal),
  ), workspaceId, artifactId, [200]);
};
export const approveArtifactDraft = (input: RevisionCommandInput, signal?: AbortSignal): Promise<ArtifactCommandResult> =>
  revisionCommand(input, (artifactId, workspaceId, idempotencyKey, expectedVersion, init) => generatedRawResponse(artifactsApi.approveArtifactDraftRaw({
    artifactId,
    idempotencyKey,
    artifactRevisionRequest: { workspace_id: workspaceId, expected_version: expectedVersion },
  }, init)), signal);
export const exportArtifactMarkdown = (input: RevisionCommandInput, signal?: AbortSignal): Promise<ArtifactCommandResult> =>
  revisionCommand(input, (artifactId, workspaceId, idempotencyKey, expectedVersion, init) => generatedRawResponse(artifactsApi.exportArtifactMarkdownRaw({
    artifactId,
    idempotencyKey,
    artifactRevisionRequest: { workspace_id: workspaceId, expected_version: expectedVersion },
  }, init)), signal);
export const createArtifactPublishProposal = (input: RevisionCommandInput, signal?: AbortSignal): Promise<ArtifactCommandResult> =>
  revisionCommand(input, (artifactId, workspaceId, idempotencyKey, expectedVersion, init) => generatedRawResponse(artifactsApi.createArtifactPublishProposalRaw({
    artifactId,
    idempotencyKey,
    artifactRevisionRequest: { workspace_id: workspaceId, expected_version: expectedVersion },
  }, init)), signal);
