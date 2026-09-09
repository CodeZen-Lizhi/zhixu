import { readFileSync } from "node:fs";

const openAPISource = readFileSync(new URL("./openapi.json", import.meta.url), "utf8");
const document = JSON.parse(openAPISource);
const exportHandlerSource = readFileSync(new URL("../../internal/export/http/handler.go", import.meta.url), "utf8");
const gitSyncHandlerSource = readFileSync(new URL("../../internal/gitsync/http/handler.go", import.meta.url), "utf8");
const authHandlerSource = readFileSync(new URL("../../internal/auth/http/handler.go", import.meta.url), "utf8");
const canonicalGinRoute = (path) => path.replaceAll(/:([A-Za-z0-9_]+)/g, "{$1}");
if (document.openapi !== "3.1.0") {
  throw new Error(`expected OpenAPI 3.1.0, got ${document.openapi}`);
}
if (JSON.stringify(document.servers) !== JSON.stringify([{ url: "/" }])) {
  throw new Error("OpenAPI servers must use the relative same-origin root");
}
for (const marker of [
  '    "/api/v1/workspaces/active": {',
  '      "ActiveWorkspace": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Active Workspace OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}
for (const marker of [
  '    "/api/v1/proposals/{proposal_id}/revision-merge-previews": {',
  '    "/api/v1/proposals/{proposal_id}/revisions": {',
  '    "/api/v1/proposals/{proposal_id}/revisions/{revision_id}": {',
  '      "ProposalRevisionCapability": {',
  '      "ProposalRevisionMergePreviewRequest": {',
  '      "AppendProposalRevisionRequest": {',
  '      "ProposalRevisionMergePreview": {',
  '      "ProposalRevisionHistoryApproval": {',
  '      "ProposalRevisionHistoryPage": {',
  '      "ProposalRevisionHistoryDetail": {',
  '      "ProposalRevisionAppendResponse": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Proposal Revision OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}
for (const marker of [
  '    "/api/v1/health/issues/{issue_id}/observations": {',
  '    "/api/v1/health/issues/{issue_id}/decisions": {',
  '      "HealthIssueObservationPage": {',
  '      "HealthIssueDecisionPage": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Health bounded-history OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}
for (const marker of [
  '    "/api/v1/workspaces/{workspace_id}/captures": {',
  '    "/api/v1/workspaces/{workspace_id}/capture-files": {',
  '    "/api/v1/workspaces/{workspace_id}/captures/{capture_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/captures/{capture_id}/retry": {',
  '      "CaptureKind": {',
  '      "CaptureStatus": {',
  '      "CaptureStageStatus": {',
  '      "Capture": {',
  '      "CapturePage": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Capture OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}
for (const marker of [
  '    "/api/v1/workspaces/{workspace_id}/authoring/working-drafts": {',
  '    "/api/v1/workspaces/{workspace_id}/authoring/documents": {',
  '    "/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}/freeze": {',
  '    "/api/v1/workspaces/{workspace_id}/documents/{document_id}/revisions/{revision_id}/publish-proposals": {',
  '    "/api/v1/workspaces/{workspace_id}/documents/{document_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/authoring/overview": {',
  '      "AuthoringEmptyCommand": {',
  '      "UpdateWorkingDraftRequest": {',
  '      "FreezeWorkingDraftRequest": {',
  '      "WorkingDraft": {',
  '      "WorkingDraftSummary": {',
  '      "WorkingDraftPage": {',
  '      "DocumentDraft": {',
  '      "DocumentDraftSummary": {',
  '      "DocumentDraftPage": {',
  '      "ArticleRevision": {',
  '      "PublicationBinding": {',
  '      "AuthoringOverview": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Authoring OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}
for (const marker of [
  '    "/api/v1/workspaces/{workspace_id}/documents/{document_id}/history": {',
  '    "/api/v1/workspaces/{workspace_id}/documents/{document_id}/history/compare": {',
  '    "/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-previews": {',
  '    "/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-proposals": {',
  '      "DocumentHistoryEntry": {',
  '      "DocumentHistoryPage": {',
  '      "DocumentHistoryCompare": {',
  '      "DocumentRestorePreview": {',
  '      "DocumentRestoreProposalResult": {',
  '      "RestoreDocumentProposal": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Document History OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}
for (const marker of [
  '    "/api/v1/workspaces/{workspace_id}/git-remote": {',
  '    "/api/v1/workspaces/{workspace_id}/git-remote/tests": {',
  '    "/api/v1/workspaces/{workspace_id}/git-sync": {',
  '    "/api/v1/workspaces/{workspace_id}/git-sync/runs": {',
  '    "/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}/retries": {',
  '      "GitSyncWorkspaceID": {',
  '      "GitSyncRunID": {',
  '      "GitSyncCursor": {',
  '      "GitSyncLimit": {',
  '      "GitSyncHTTPSRemoteInput": {',
  '      "GitSyncHTTPSRemoteURL": {',
  '      "GitSyncBranch": {',
  '      "GitSyncOID": {',
  '      "GitSyncKeepTokenAction": {',
  '      "GitSyncReplaceTokenAction": {',
  '      "GitSyncClearTokenAction": {',
  '      "GitSyncTokenAction": {',
  '      "GitSyncTestTokenAction": {',
  '      "SaveGitRemoteConfigRequest": {',
  '      "RemoveGitRemoteConfigRequest": {',
  '      "TestGitRemoteConfigRequest": {',
  '      "RetryGitSyncRunRequest": {',
  '      "GitRemoteConfig": {',
  '      "GitRemoteTestResult": {',
  '      "GitSyncFileChange": {',
  '      "GitSyncRun": {',
  '      "GitSyncStatus": {',
  '      "GitSyncRunPage": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Git Sync OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}
for (const marker of [
  '    "/api/v1/workspaces/{workspace_id}/organizing/drafts": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/suggestions": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/confirm": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/snapshots/{snapshot_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/templates": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/clone": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/revisions": {',
  '    "/api/v1/workspaces/{workspace_id}/organizing/runs/{snapshot_id}": {',
  '      "OrganizingAddMaterialRequest": {',
  '      "OrganizingDraft": {',
  '      "OrganizingSnapshot": {',
  '      "OrganizingTemplate": {',
  '      "OrganizingRun": {',
]) {
  if (openAPISource.split(marker).length !== 2) {
    throw new Error(`Organizing OpenAPI key must occur exactly once: ${marker.trim()}`);
  }
}

const requiredOperations = [
  ["/livez", "get", "200"],
  ["/readyz", "get", "200"],
  ["/api/v1/system/status", "get", "200"],
  ["/api/v1/auth/sessions", "post", "201"],
  ["/api/v1/auth/session", "get", "200"],
  ["/api/v1/auth/session", "delete", "204"],
  ["/api/v1/auth/session/rotate", "post", "200"],
  ["/api/v1/auth/api-tokens", "get", "200"],
  ["/api/v1/auth/api-tokens", "post", "201"],
  ["/api/v1/auth/api-tokens/{token_id}", "delete", "204"],
  ["/api/v1/settings/models", "get", "200"],
  ["/api/v1/settings/models", "put", "200"],
  ["/api/v1/settings/models/activations", "post", "202"],
  ["/api/v1/settings/models/test", "post", "200"],
  ["/api/v1/workspaces", "post", "201"],
  ["/api/v1/workspaces/active", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/scan", "post", "200"],
  ["/api/v1/workspaces/{workspace_id}/captures", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/captures", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/capture-files", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/captures/{capture_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/captures/{capture_id}/retry", "post", "202"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}", "put", "200"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}/freeze", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/revisions/{revision_id}/publish-proposals", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/history", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/history/compare", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-previews", "post", "200"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-proposals", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/authoring/overview", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/authoring/documents", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}", "put", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/suggestions", "post", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}", "patch", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}", "delete", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/confirm", "post", "202"],
  ["/api/v1/workspaces/{workspace_id}/organizing/snapshots/{snapshot_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/clone", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/revisions", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/organizing/runs/{snapshot_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/git-remote", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/git-remote", "put", "201"],
  ["/api/v1/workspaces/{workspace_id}/git-remote", "delete", "200"],
  ["/api/v1/workspaces/{workspace_id}/git-remote/tests", "post", "200"],
  ["/api/v1/workspaces/{workspace_id}/git-sync", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/git-sync/runs", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/git-sync/runs", "post", "202"],
  ["/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}/retries", "post", "202"],
  ["/api/v1/source-versions/{source_version_id}/ingestion-attempts", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/source-versions", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/workflows", "post", "202"],
  ["/api/v1/workspaces/{workspace_id}/workflows", "get", "200"],
  ["/api/v1/workflows/{run_id}", "get", "200"],
  ["/api/v1/workflows/{run_id}/human-tasks/{task_id}/decision", "post", "200"],
  ["/api/v1/workspaces/{workspace_id}/proposals", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/proposals", "get", "200"],
  ["/api/v1/proposals/{proposal_id}", "get", "200"],
  ["/api/v1/proposals/{proposal_id}/current-content", "get", "200"],
  ["/api/v1/proposals/{proposal_id}/revision-merge-previews", "post", "200"],
  ["/api/v1/proposals/{proposal_id}/revisions", "get", "200"],
  ["/api/v1/proposals/{proposal_id}/revisions", "post", "201"],
  ["/api/v1/proposals/{proposal_id}/revisions/{revision_id}", "get", "200"],
  ["/api/v1/proposals/{proposal_id}/approvals", "post", "201"],
  ["/api/v1/proposals/{proposal_id}/apply-preflight", "post", "200"],
  ["/api/v1/search", "post", "200"],
  ["/api/v1/graph/global", "post", "200"],
  ["/api/v1/graph/neighborhood", "post", "200"],
  ["/api/v1/graph/path", "post", "200"],
  ["/api/v1/graph/nodes", "get", "200"],
  ["/api/v1/graph/nodes/{node_type}/{node_id}", "get", "200"],
  ["/api/v1/graph/relations/{relation_id}", "get", "200"],
  ["/api/v1/graph/relations/{relation_id}/evidence", "get", "200"],
  ["/api/v1/graph/candidates", "get", "200"],
  ["/api/v1/graph/candidates/{candidate_id}", "get", "200"],
  ["/api/v1/graph/candidates/{candidate_id}/decisions", "post", "201"],
  ["/api/v1/graph/candidate-scans", "post", "202"],
  ["/api/v1/graph/candidate-scans/{scan_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}", "get", "200"],
  ["/api/v1/conversations", "post", "201"],
  ["/api/v1/conversations", "get", "200"],
  ["/api/v1/conversations/{conversation_id}", "get", "200"],
  ["/api/v1/conversations/{conversation_id}/questions", "post", "202"],
  ["/api/v1/conversations/{conversation_id}/turns", "get", "200"],
  ["/api/v1/answers/{answer_id}", "get", "200"],
  ["/api/v1/answers/{answer_id}/feedback", "post", "201"],
  ["/api/v1/events", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/timeline", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/timeline/{event_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}/impact-reports/{report_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/impact-reports/{report_id}/proposals", "post", "201"],
  ["/api/v1/exports", "post", "202"],
  ["/api/v1/exports/{export_id}", "get", "200"],
  ["/api/v1/exports/{export_id}/download", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/exports", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/attachment-exports", "post", "202"],
  ["/api/v1/workspaces/{workspace_id}/attachment-exports", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}/download", "get", "200"],
  ["/api/v1/artifacts", "get", "200"],
  ["/api/v1/artifacts", "post", "201"],
  ["/api/v1/artifacts/{artifact_id}", "get", "200"],
  ["/api/v1/artifacts/{artifact_id}/outline", "post", "200"],
  ["/api/v1/artifacts/{artifact_id}/outline/approve", "post", "200"],
  ["/api/v1/artifacts/{artifact_id}/sections", "post", "200"],
  ["/api/v1/artifacts/{artifact_id}/sections/generate", "post", "202"],
  ["/api/v1/artifacts/{artifact_id}/section-generations", "get", "200"],
  ["/api/v1/artifacts/{artifact_id}/draft/approve", "post", "200"],
  ["/api/v1/artifacts/{artifact_id}/exports/markdown", "post", "200"],
  ["/api/v1/artifacts/{artifact_id}/exports/{export_id}", "get", "200"],
  ["/api/v1/artifacts/{artifact_id}/publish-proposals", "post", "200"],
];
for (const [path, method, successResponse] of requiredOperations) {
  const operation = document.paths?.[path]?.[method];
  if (!operation) throw new Error(`missing operation ${method.toUpperCase()} ${path}`);
  for (const response of [successResponse, "405"]) {
    if (!operation.responses?.[response]) throw new Error(`missing ${response} response for ${method.toUpperCase()} ${path}`);
  }
}
const activeWorkspacePath = "/api/v1/workspaces/active";
const activeWorkspaceOperation = document.paths?.[activeWorkspacePath]?.get;
if (activeWorkspaceOperation?.operationId !== "getActiveWorkspace" ||
    activeWorkspaceOperation.security !== undefined ||
    activeWorkspaceOperation["x-required-capability"] !== "READ_LOCAL" ||
    activeWorkspaceOperation.requestBody !== undefined ||
    (activeWorkspaceOperation.parameters ?? []).length !== 0) {
  throw new Error("GET /api/v1/workspaces/active must remain a parameter-free READ_LOCAL business operation");
}
if (activeWorkspaceOperation.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ActiveWorkspace") {
  throw new Error("GET /api/v1/workspaces/active must use the dedicated ActiveWorkspace response schema");
}
const activeWorkspaceResponseStatuses = Object.keys(activeWorkspaceOperation.responses ?? {}).sort();
if (activeWorkspaceResponseStatuses.join(",") !== "200,401,403,404,405,409,503") {
  throw new Error("GET /api/v1/workspaces/active response statuses drifted");
}
for (const status of ["401", "403", "404", "405", "409", "503"]) {
  if (activeWorkspaceOperation.responses?.[status]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
    throw new Error(`GET /api/v1/workspaces/active ${status} must return Problem`);
  }
}
const activeWorkspacePathPosition = openAPISource.indexOf('    "/api/v1/workspaces/active": {');
const workspaceIDPathPosition = openAPISource.indexOf('    "/api/v1/workspaces/{workspace_id}": {');
if (activeWorkspacePathPosition < 0 || workspaceIDPathPosition < 0 || activeWorkspacePathPosition >= workspaceIDPathPosition) {
  throw new Error("Active Workspace path must remain before the dynamic Workspace path");
}
const activeWorkspaceSchema = document.components?.schemas?.ActiveWorkspace;
const activeWorkspaceFields = "id,name,root_path,status,availability,version";
const forbiddenActiveWorkspaceFields = ["git", "warnings", "created_at", "updated_at"];
if (activeWorkspaceSchema?.type !== "object" ||
    activeWorkspaceSchema.additionalProperties !== false ||
    activeWorkspaceSchema.required?.join(",") !== activeWorkspaceFields ||
    Object.keys(activeWorkspaceSchema.properties ?? {}).join(",") !== activeWorkspaceFields ||
    forbiddenActiveWorkspaceFields.some((field) => activeWorkspaceSchema.properties?.[field] !== undefined) ||
    activeWorkspaceSchema.properties?.id?.type !== "string" ||
    activeWorkspaceSchema.properties?.id?.format !== "uuid" ||
    activeWorkspaceSchema.properties?.name?.type !== "string" ||
    activeWorkspaceSchema.properties?.name?.minLength !== 1 ||
    activeWorkspaceSchema.properties?.root_path?.type !== "string" ||
    activeWorkspaceSchema.properties?.root_path?.minLength !== 1 ||
    activeWorkspaceSchema.properties?.status?.type !== "string" ||
    activeWorkspaceSchema.properties?.status?.const !== "active" ||
    activeWorkspaceSchema.properties?.availability?.type !== "string" ||
    activeWorkspaceSchema.properties?.availability?.enum?.join(",") !== "available,unavailable,migration_required" ||
    activeWorkspaceSchema.properties?.version?.type !== "integer" ||
    activeWorkspaceSchema.properties?.version?.minimum !== 1) {
  throw new Error("ActiveWorkspace must remain the exact six-field bootstrap projection");
}
const workspaceSchema = document.components?.schemas?.Workspace;
if (workspaceSchema?.required?.includes("availability") || workspaceSchema?.properties?.availability !== undefined) {
  throw new Error("Workspace detail/create schema must not absorb the ActiveWorkspace availability field");
}
for (const [path, method] of [
  ["/api/v1/workflows/{run_id}", "get"],
  ["/api/v1/workflows/{run_id}/pause", "post"],
  ["/api/v1/workflows/{run_id}/resume", "post"],
  ["/api/v1/workflows/{run_id}/cancel", "post"],
  ["/api/v1/workflows/{run_id}/human-tasks/{task_id}/decision", "post"],
]) {
  const parameters = document.paths[path]?.parameters ?? [];
  if (!parameters.some((parameter) => parameter.$ref === "#/components/parameters/WorkflowWorkspaceID")) {
    throw new Error(`${method.toUpperCase()} ${path} must require X-Workspace-ID`);
  }
}
if (!document.paths["/api/v1/settings/models/test"].post.parameters?.some(
  (parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey",
)) {
  throw new Error("POST /api/v1/settings/models/test must require Idempotency-Key");
}
const workflowWorkspaceHeader = document.components?.parameters?.WorkflowWorkspaceID;
if (workflowWorkspaceHeader?.name !== "X-Workspace-ID" || workflowWorkspaceHeader.in !== "header" ||
    workflowWorkspaceHeader.required !== true || workflowWorkspaceHeader.schema?.type !== "string" ||
    workflowWorkspaceHeader.schema?.format !== "uuid") {
  throw new Error("WorkflowWorkspaceID must be a required UUID X-Workspace-ID header");
}
if (!document.paths["/readyz"].get.responses["503"]) {
  throw new Error("missing 503 response for GET /readyz");
}
for (const path of ["/livez", "/readyz", "/api/v1/system/status"]) {
  if (document.paths[path].get.security?.length !== 0) throw new Error(`${path} must remain public`);
}
const exactSecurity = (actual, expected) => JSON.stringify(actual) === JSON.stringify(expected);
const businessSecurity = [{ sessionCookie: [] }, { apiBearer: [] }];
if (!exactSecurity(document.security, businessSecurity)) {
  throw new Error("root security must require exactly sessionCookie or apiBearer");
}
const publicOperations = new Set([
  "GET /livez",
  "GET /readyz",
  "GET /api/v1/system/status",
]);
for (const [path, pathItem] of Object.entries(document.paths)) {
  for (const method of ["get", "post", "put", "patch", "delete", "head", "options", "trace"]) {
    const operation = pathItem[method];
    if (!operation) continue;
    const key = `${method.toUpperCase()} ${path}`;
    if (publicOperations.has(key)) {
      if (!exactSecurity(operation.security, [])) throw new Error(`${key} must explicitly remain public`);
    } else if (exactSecurity(operation.security, [])) {
      throw new Error(`${key} must not disable business authentication`);
    }
  }
}
for (const scheme of ["sessionCookie", "apiBearer", "bootstrapBearer"]) {
  if (!document.components?.securitySchemes?.[scheme]) throw new Error(`missing auth security scheme ${scheme}`);
}
if (document.components.securitySchemes.sessionCookie.name !== "zhixu_session" ||
    document.components.securitySchemes.sessionCookie.in !== "cookie" ||
    document.components.securitySchemes.apiBearer.scheme !== "bearer") {
  throw new Error("auth security schemes drifted from runtime credentials");
}
for (const [path, method, expected] of [
  ["/api/v1/auth/sessions", "post", [{ bootstrapBearer: [] }]],
  ["/api/v1/auth/session", "get", [{ sessionCookie: [] }]],
  ["/api/v1/auth/session", "delete", [{ sessionCookie: [] }]],
  ["/api/v1/auth/session/rotate", "post", [{ sessionCookie: [] }]],
  ["/api/v1/auth/api-tokens", "get", [{ sessionCookie: [] }]],
  ["/api/v1/auth/api-tokens", "post", [{ sessionCookie: [] }]],
  ["/api/v1/auth/api-tokens/{token_id}", "delete", [{ sessionCookie: [] }]],
  ["/api/v1/settings/models", "get", [{ sessionCookie: [] }]],
  ["/api/v1/settings/models", "put", [{ sessionCookie: [] }]],
  ["/api/v1/settings/models/activations", "post", [{ sessionCookie: [] }]],
  ["/api/v1/settings/models/test", "post", [{ sessionCookie: [] }]],
]) {
  if (!exactSecurity(document.paths[path][method].security, expected)) {
    throw new Error(`${method.toUpperCase()} ${path} auth scheme is not exact`);
  }
}
for (const [path, method] of [
  ["/api/v1/auth/session", "delete"],
  ["/api/v1/auth/session/rotate", "post"],
  ["/api/v1/auth/api-tokens", "post"],
  ["/api/v1/auth/api-tokens/{token_id}", "delete"],
  ["/api/v1/settings/models", "put"],
  ["/api/v1/settings/models/activations", "post"],
  ["/api/v1/settings/models/test", "post"],
]) {
  const parameters = document.paths[path][method].parameters ?? [];
  for (const requiredParameter of ["#/components/parameters/Origin", "#/components/parameters/CSRFToken"]) {
    if (!parameters.some((parameter) => parameter.$ref === requiredParameter)) {
      throw new Error(`${method.toUpperCase()} ${path} must require ${requiredParameter}`);
    }
  }
}
const authOperations = [
  ["/api/v1/auth/sessions", "post", "SessionCredential", ["400", "401", "403", "405", "503"]],
  ["/api/v1/auth/session", "get", "SessionInfo", ["401", "403", "405", "503"]],
  ["/api/v1/auth/session/rotate", "post", "SessionCredential", ["400", "401", "403", "405", "503"]],
  ["/api/v1/auth/api-tokens", "get", "APITokenPage", ["401", "403", "405", "503"]],
  ["/api/v1/auth/api-tokens", "post", "APITokenCredential", ["400", "401", "403", "405", "503"]],
];
for (const [path, method, schema, errors] of authOperations) {
  const operation = document.paths[path][method];
  const success = method === "post" && path !== "/api/v1/auth/session/rotate" ? "201" : "200";
  if (operation.responses[success]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${schema}`) {
    throw new Error(`invalid auth success schema for ${method.toUpperCase()} ${path}`);
  }
  for (const status of errors) {
    if (resolveRef(operation.responses[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid auth ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
for (const [path, method] of [
  ["/api/v1/auth/sessions", "post"],
  ["/api/v1/auth/session", "delete"],
  ["/api/v1/auth/session/rotate", "post"],
  ["/api/v1/auth/api-tokens/{token_id}", "delete"],
]) {
  const operation = document.paths[path][method];
  if (operation.requestBody !== undefined) {
    throw new Error(`${method.toUpperCase()} ${path} must not accept a request body`);
  }
  if (resolveRef(operation.responses["400"])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
    throw new Error(`${method.toUpperCase()} ${path} must declare its invalid-body Problem response`);
  }
}
for (const [path, method] of [
  ["/api/v1/auth/session", "delete"],
  ["/api/v1/auth/api-tokens/{token_id}", "delete"],
]) {
  if (document.paths[path][method].responses["204"]?.content) throw new Error(`${method.toUpperCase()} ${path} 204 must not have a body`);
}
for (const status of ["400", "401", "403", "404", "405", "503"]) {
  if (resolveRef(document.paths["/api/v1/auth/api-tokens/{token_id}"].delete.responses[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
    throw new Error(`invalid DELETE /api/v1/auth/api-tokens/{token_id} ${status} Problem schema`);
  }
}
if (!document.paths["/api/v1/workspaces/{workspace_id}/proposals"].post.responses["200"]) {
  throw new Error("missing idempotent replay response for Proposal creation");
}
if (!document.paths["/api/v1/proposals/{proposal_id}/approvals"].post.responses["200"]) {
  throw new Error("missing exact replay response for Approval decision");
}

const conversationOperations = [
  ["/api/v1/conversations", "post", ["200", "201", "400", "409", "415", "500", "503", "405"]],
  ["/api/v1/conversations", "get", ["200", "400", "404", "500", "503", "405"]],
  ["/api/v1/conversations/{conversation_id}", "get", ["200", "400", "404", "500", "503", "405"]],
  ["/api/v1/conversations/{conversation_id}/questions", "post", ["200", "202", "400", "404", "409", "415", "500", "503", "405"]],
  ["/api/v1/conversations/{conversation_id}/turns", "get", ["200", "400", "404", "500", "503", "405"]],
  ["/api/v1/answers/{answer_id}", "get", ["200", "400", "404", "500", "503", "405"]],
  ["/api/v1/answers/{answer_id}/feedback", "post", ["200", "201", "400", "404", "409", "415", "500", "503", "405"]],
  ["/api/v1/events", "get", ["200", "400", "404", "409", "500", "503", "405"]],
];
for (const [path, method, statuses] of conversationOperations) {
  const responses = document.paths[path][method].responses;
  for (const status of statuses) {
    if (!responses[status]) throw new Error(`missing ${status} response for ${method.toUpperCase()} ${path}`);
    if (Number(status) >= 400 && resolveRef(responses[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}

const graphOperations = [
  ["/api/v1/graph/global", "post", "GraphGlobalResponse", ["400", "409", "415", "500", "503", "405"]],
  ["/api/v1/graph/neighborhood", "post", "GraphNeighborhoodResponse", ["400", "404", "409", "415", "500", "503", "405"]],
  ["/api/v1/graph/path", "post", "GraphPathResponse", ["400", "404", "409", "415", "422", "500", "503", "405"]],
  ["/api/v1/graph/nodes", "get", "GraphNodeSearchResponse", ["400", "409", "500", "503", "405"]],
  ["/api/v1/graph/nodes/{node_type}/{node_id}", "get", "GraphNode", ["400", "404", "409", "500", "503", "405"]],
  ["/api/v1/graph/relations/{relation_id}", "get", "GraphRelationDetailResponse", ["400", "404", "409", "500", "503", "405"]],
  ["/api/v1/graph/relations/{relation_id}/evidence", "get", "GraphRelationEvidenceResponse", ["400", "404", "409", "500", "503", "405"]],
];
for (const [path, method, successSchema, errorStatuses] of graphOperations) {
  const operation = document.paths[path][method];
  const actual = operation.responses["200"]?.content?.["application/json"]?.schema?.$ref;
  if (actual !== `#/components/schemas/${successSchema}`) {
    throw new Error(`invalid Graph success schema for ${method.toUpperCase()} ${path}: ${String(actual)}`);
  }
  for (const status of errorStatuses) {
    const response = resolveRef(operation.responses[status]);
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Graph ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}

const semanticLinkOperations = [
  ["/api/v1/graph/candidates", "get", "SemanticLinkCandidatePage", ["400", "409", "500", "503", "405"]],
  ["/api/v1/graph/candidates/{candidate_id}", "get", "SemanticLinkCandidate", ["400", "404", "409", "500", "503", "405"]],
  ["/api/v1/graph/candidates/{candidate_id}/decisions", "post", "SemanticLinkCandidateDecisionReceipt", ["200", "400", "404", "409", "415", "500", "503", "405"]],
];
for (const [path, method, successSchema, errorStatuses] of semanticLinkOperations) {
  const operation = document.paths[path][method];
  const successStatus = method === "post" ? "201" : "200";
  const actual = operation.responses[successStatus]?.content?.["application/json"]?.schema?.$ref;
  if (actual !== `#/components/schemas/${successSchema}`) {
    throw new Error(`invalid Semantic Link success schema for ${method.toUpperCase()} ${path}: ${String(actual)}`);
  }
  for (const status of errorStatuses) {
    if (status === successStatus || (method === "post" && status === "200")) continue;
    const response = resolveRef(operation.responses[status]);
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Semantic Link ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}

const semanticLinkScanOperations = [
  ["/api/v1/graph/candidate-scans", "post", "202", "SemanticLinkScanAcceptance", ["400", "404", "409", "415", "500", "503", "405"]],
  ["/api/v1/graph/candidate-scans/{scan_id}", "get", "200", "SemanticLinkScan", ["400", "404", "409", "500", "503", "405"]],
];
for (const [path, method, successStatus, successSchema, errorStatuses] of semanticLinkScanOperations) {
  const operation = document.paths[path][method];
  const actual = operation.responses[successStatus]?.content?.["application/json"]?.schema?.$ref;
  if (actual !== `#/components/schemas/${successSchema}`) {
    throw new Error(`invalid Semantic Link scan success schema for ${method.toUpperCase()} ${path}: ${String(actual)}`);
  }
  for (const status of errorStatuses) {
    const response = resolveRef(operation.responses[status]);
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Semantic Link scan ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}

const timelineOperations = [
  ["/api/v1/workspaces/{workspace_id}/timeline", "get", ["200"], "KnowledgeTimelinePage", ["400", "401", "403", "405", "500", "503"], "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/timeline/{event_id}", "get", ["200"], "KnowledgeEvent", ["400", "401", "403", "404", "405", "500", "503"], "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis", "post", ["200", "201"], "ImpactAnalysisResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"], "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/impact-reports/{report_id}", "get", ["200"], "ImpactReport", ["400", "401", "403", "404", "405", "500", "503"], "READ_LOCAL"],
];
for (const [path, method, successStatuses, successSchema, errorStatuses, requiredCapability] of timelineOperations) {
  const operation = document.paths[path][method];
  if (operation.security?.length === 0 || operation["x-required-capability"] !== requiredCapability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${requiredCapability}`);
  }
  for (const status of successStatuses) {
    const actual = operation.responses[status]?.content?.["application/json"]?.schema?.$ref;
    if (actual !== `#/components/schemas/${successSchema}`) {
      throw new Error(`invalid Timeline/Impact ${status} success schema for ${method.toUpperCase()} ${path}: ${String(actual)}`);
    }
  }
  for (const status of errorStatuses) {
    const response = resolveRef(operation.responses[status]);
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Timeline/Impact ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
const timelinePath = "/api/v1/workspaces/{workspace_id}/timeline";
if (document.paths[timelinePath].post || document.paths[timelinePath].put || document.paths[timelinePath].patch || document.paths[timelinePath].delete) {
  throw new Error("Knowledge Timeline must not expose a public event write operation");
}
const timelineParameters = document.paths[timelinePath].get.parameters;
const timelineLimit = timelineParameters.find((parameter) => parameter.name === "limit")?.schema;
const timelineCursor = timelineParameters.find((parameter) => parameter.name === "cursor")?.schema;
const timelineEventTypes = timelineParameters.find((parameter) => parameter.name === "event_type")?.schema;
if (timelineLimit?.minimum !== 1 || timelineLimit.maximum !== 100 || timelineLimit.default !== 25 ||
    timelineCursor?.minLength !== 1 || timelineCursor.maxLength !== 4096 ||
    timelineEventTypes?.type !== "array" || timelineEventTypes.maxItems !== 32 || timelineEventTypes.uniqueItems !== true ||
    timelineEventTypes.items?.$ref !== "#/components/schemas/KnowledgeEventType") {
  throw new Error("Knowledge Timeline pagination or event filter bounds drifted");
}
const impactOperation = document.paths["/api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis"].post;
if (!impactOperation.parameters?.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") ||
    impactOperation.requestBody?.required !== true || impactOperation.requestBody?.["x-max-body-bytes"] !== 4096 ||
    impactOperation.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ImpactAnalysisRequest") {
  throw new Error("Impact Analysis must require Idempotency-Key and a bounded empty JSON object");
}
const downstreamProposalPath = "/api/v1/workspaces/{workspace_id}/impact-reports/{report_id}/proposals";
const downstreamProposalOperation = document.paths[downstreamProposalPath]?.post;
if (!downstreamProposalOperation || downstreamProposalOperation.security !== undefined ||
    downstreamProposalOperation["x-required-capability"] !== "WRITE_PROPOSAL" ||
    !downstreamProposalOperation.parameters?.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") ||
    downstreamProposalOperation.requestBody?.required !== true ||
    downstreamProposalOperation.requestBody?.["x-max-body-bytes"] !== 16384 ||
    downstreamProposalOperation.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/CreateDownstreamUpdateProposalRequest") {
  throw new Error("Downstream Proposal creation must inherit authentication and require WRITE_PROPOSAL, Idempotency-Key and a bounded strict body");
}
for (const [status, replayed] of [["200", true], ["201", false]]) {
  const schema = downstreamProposalOperation.responses?.[status]?.content?.["application/json"]?.schema;
  if (schema?.allOf?.[0]?.$ref !== "#/components/schemas/DownstreamUpdateProposalCreateResponse" ||
      schema?.allOf?.[1]?.properties?.replayed?.const !== replayed ||
      (status === "201" && schema?.allOf?.[1]?.properties?.approval?.type !== "null") ||
      (status === "200" && schema?.allOf?.[1]?.properties?.approval !== undefined)) {
    throw new Error(`Downstream Proposal ${status} must expose endpoint-only replayed=${String(replayed)}`);
  }
}
for (const status of ["400", "401", "403", "404", "405", "409", "415", "500", "503"]) {
  const response = resolveRef(downstreamProposalOperation.responses?.[status]);
  if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
    throw new Error(`invalid Downstream Proposal ${status} Problem response`);
  }
}
for (const errorCode of [
  "DOWNSTREAM_UPDATE_PROPOSAL_INVALID", "DOWNSTREAM_UPDATE_TARGET_INVALID", "IDEMPOTENCY_KEY_REQUIRED", "IDEMPOTENCY_KEY_REUSED",
  "INVALID_JSON", "UNSUPPORTED_MEDIA_TYPE", "KNOWLEDGE_IMPACT_NOT_FOUND", "KNOWLEDGE_IMPACT_CONFLICT",
  "KNOWLEDGE_IMPACT_UNAVAILABLE", "CHANGE_CONTROL_SERVICE_UNAVAILABLE", "INTERNAL_ERROR",
]) {
  if (!downstreamProposalOperation["x-error-codes"]?.includes(errorCode)) {
    throw new Error(`Downstream Proposal error matrix is missing ${errorCode}`);
  }
}

for (const [path, method] of [
  ["/api/v1/search", "post"],
  ["/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}", "get"],
  ["/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}", "get"],
]) {
  const responses = document.paths[path][method].responses;
  for (const status of ["400", "404", "405", "409", "500", "503"]) {
    if (!responses[status]) throw new Error(`missing ${status} response for ${method.toUpperCase()} ${path}`);
    if (responses[status]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}

for (const schema of [
  "Liveness",
  "Readiness",
  "SystemStatus",
  "AuthCapability",
  "AuthCapabilityStatus",
  "SessionCredential",
  "SessionInfo",
  "CreateAPITokenRequest",
  "APITokenInfo",
  "APITokenCredential",
  "APITokenPage",
  "RAGCapabilityStatus",
  "CreateWorkspaceRequest",
  "ActiveWorkspace",
  "Workspace",
  "WorkspaceScan",
  "IngestionRequest",
  "IngestionResult",
  "StartWorkflowRequest",
  "WorkflowStart",
  "WorkflowRun",
  "PendingWorkflowHumanTask",
  "WorkflowHumanTaskReview",
  "WorkflowReviewEvidence",
  "WorkflowReviewSourceEvidence",
  "WorkflowReviewDocumentEvidence",
  "WorkflowTopicOutlineSection",
  "WorkflowTopicOutlineReview",
  "WorkflowMergeCategory",
  "WorkflowMergeEvidence",
  "WorkflowMergeSourceEvidence",
  "WorkflowMergeDocumentEvidence",
  "WorkflowMergeComparisonReview",
  "WorkflowRunPage",
  "WorkflowRunSummary",
  "HumanDecisionRequest",
  "HumanTask",
  "CreateProposalRequest",
  "ProposalRiskLevel",
  "ProposalRevision",
  "Approval",
  "NonFileApproval",
  "ApprovalDecisionResponse",
  "Proposal",
  "ProposalPage",
  "ProposalSummary",
  "ProposalCurrentContent",
  "ProposalRevisionCapability",
  "ProposalRevisionMergePreviewRequest",
  "AppendProposalRevisionRequest",
  "ProposalRevisionTextSnapshot",
  "ProposalRevisionMergeConflict",
  "ProposalRevisionMergePreview",
  "ProposalRevisionHistoryApproval",
  "ProposalRevisionWorkflow",
  "ProposalRevisionHistoryItem",
  "ProposalRevisionHistoryPage",
  "ProposalRevisionBaseSnapshot",
  "ProposalRevisionLineage",
  "HistoricalProposalRevision",
  "ProposalRevisionHistoryDetail",
  "ProposalRevisionAppendResponse",
  "FilePatchProposal",
  "KnowledgeChangeTargetRef",
  "KnowledgeChangeBaseVersion",
  "KnowledgeChangeEndpoint",
  "KnowledgeChangeSet",
  "KnowledgeChangeEvidenceRef",
  "KnowledgeChangeRevision",
  "KnowledgeChangeProposal",
  "PublishArtifactCoverage",
  "PublishArtifactBinding",
  "PublishArtifactRevision",
  "PublishArtifactProposal",
  "CreateDownstreamUpdateProposalRequest",
  "DownstreamUpdateSourceReport",
  "DownstreamUpdateSourceEvent",
  "DownstreamUpdate",
  "DownstreamUpdateRevision",
  "DownstreamUpdateProposal",
  "DownstreamUpdateProposalCreateResponse",
  "ProposalDecisionRequest",
  "ApplyPreflightRequest",
  "ApplyPreflightResult",
  "SearchFilter",
  "SearchCursor",
  "SearchRequest",
  "SearchDegradation",
  "EvidenceSpan",
  "EvidenceProvenance",
  "LexicalScore",
  "VectorDistance",
  "StageScore",
  "RerankScore",
  "EvidenceScores",
  "SearchEvidence",
  "SearchResponse",
  "EvidenceSourceVersion",
  "SourceVersionPage",
  "SourceVersionSummary",
  "EvidenceSourceSpan",
  "GraphCanonicalJSONValue",
  "GraphCanonicalJSONObject",
  "GraphNodeRef",
  "GraphApplicability",
  "GraphTopicNode",
  "GraphClaimNode",
  "GraphNode",
  "GraphConfirmation",
  "GraphEdge",
  "GraphPageMeta",
  "GraphFilter",
  "GraphGlobalRequest",
  "GraphGlobalCluster",
  "GraphGlobalResponse",
  "GraphNodeSearchMatch",
  "GraphNodeSearchResponse",
  "GraphNeighborhoodRequest",
  "GraphNeighborhoodResponse",
  "GraphPathRequest",
  "GraphPathResponse",
  "GraphRelationDetailResponse",
  "GraphProvenance",
  "GraphRelationEvidenceItem",
  "GraphRelationEvidenceResponse",
  "SemanticLinkNodeType",
  "SemanticLinkRelationType",
  "SemanticLinkCandidateStatus",
  "SemanticLinkDiscoveryMethod",
  "SemanticLinkCandidateEndpoint",
  "SemanticLinkCandidateEvidence",
  "SemanticLinkGeneration",
  "SemanticLinkCandidate",
  "SemanticLinkCandidatePage",
  "SemanticLinkCandidateDecisionRequest",
  "SemanticLinkCandidateDecisionReceipt",
  "SemanticLinkScanStatus",
  "SemanticLinkTopicScanScope",
  "SemanticLinkScanStartRequest",
  "SemanticLinkScanAcceptance",
  "SemanticLinkScan",
  "SemanticLinkCapabilityStatus",
  "PageCursor",
  "CreateConversationRequest",
  "Conversation",
  "ConversationPage",
  "QuestionScopeRequest",
  "QuestionScope",
  "SubmitQuestionRequest",
  "Question",
  "WorkflowProjection",
  "QuestionAcceptance",
  "AnswerCitation",
  "RAGResultCitation",
  "RAGAssertion",
  "RAGConflictPosition",
  "RelatedTopic",
  "RAGAnswerPayload",
  "RAGAnswerResult",
  "RefusalResult",
  "ClarificationResult",
  "RetrievalDegradation",
  "RetrievalScopeSummary",
  "RetrievalSummary",
  "Answer",
  "Turn",
  "TurnPage",
  "SubmitFeedbackRequest",
  "AnswerFeedback",
  "ServerEventPayloadSummary",
  "ServerEventEnvelope",
  "KnowledgeEventType",
  "TimelineAggregateType",
  "ArtifactImpactBinding",
  "ReviewCardImpactBinding",
  "KnowledgeEventOperator",
  "ArtifactEventOwnerBinding",
  "ReviewCardEventOwnerBinding",
  "KnowledgeEventOwnerBinding",
  "KnowledgeEventCorrelation",
  "KnowledgeEventV1",
  "KnowledgeEventV2",
  "KnowledgeEvent",
  "KnowledgeTimelinePage",
  "ImpactAnalysisRequest",
  "ImpactObjectType",
  "ImpactAction",
  "ImpactObjectV1",
  "ImpactObjectV2Base",
  "ImpactObjectV2Legacy",
  "ArtifactImpactObject",
  "ReviewCardImpactObject",
  "ImpactObject",
  "ImpactReportV1",
  "ImpactReportV2",
  "ImpactReport",
  "ImpactProposalDraft",
  "ImpactAnalysisResult",
  "Problem",
]) {
  if (!document.components?.schemas?.[schema]) throw new Error(`missing schema ${schema}`);
}

const expectedSuccessSchemas = [
  ["/api/v1/workspaces/{workspace_id}/source-versions", "get", "SourceVersionPage"],
  ["/api/v1/workspaces/{workspace_id}/proposals", "get", "ProposalPage"],
  ["/api/v1/workspaces/{workspace_id}/workflows", "get", "WorkflowRunPage"],
  ["/api/v1/proposals/{proposal_id}", "get", "Proposal"],
  ["/api/v1/proposals/{proposal_id}/current-content", "get", "ProposalCurrentContent"],
  ["/api/v1/search", "post", "SearchResponse"],
  ["/api/v1/graph/global", "post", "GraphGlobalResponse"],
  ["/api/v1/graph/neighborhood", "post", "GraphNeighborhoodResponse"],
  ["/api/v1/graph/path", "post", "GraphPathResponse"],
  ["/api/v1/graph/nodes", "get", "GraphNodeSearchResponse"],
  ["/api/v1/graph/nodes/{node_type}/{node_id}", "get", "GraphNode"],
  ["/api/v1/graph/relations/{relation_id}", "get", "GraphRelationDetailResponse"],
  ["/api/v1/graph/relations/{relation_id}/evidence", "get", "GraphRelationEvidenceResponse"],
  ["/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}", "get", "EvidenceSourceVersion"],
  ["/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}", "get", "EvidenceSourceSpan"],
];
for (const [path, method, schema] of expectedSuccessSchemas) {
  const actual = document.paths[path][method].responses["200"]?.content?.["application/json"]?.schema?.$ref;
  if (actual !== `#/components/schemas/${schema}`) {
    throw new Error(`invalid success schema for ${method.toUpperCase()} ${path}: ${String(actual)}`);
  }
}

for (const status of ["200", "201"]) {
  const actual = document.paths["/api/v1/workspaces/{workspace_id}/proposals"].post.responses[status]?.content?.["application/json"]?.schema?.$ref;
  if (actual !== "#/components/schemas/Proposal") {
    throw new Error(`Proposal creation ${status} must return Proposal`);
  }
}

for (const [path, method, statuses] of [
  ["/api/v1/workspaces/{workspace_id}/source-versions", "get", ["400", "503", "405"]],
  ["/api/v1/workspaces/{workspace_id}/proposals", "get", ["400", "500", "503", "405"]],
  ["/api/v1/workspaces/{workspace_id}/workflows", "get", ["400", "503", "405"]],
  ["/api/v1/proposals/{proposal_id}/current-content", "get", ["400", "404", "503", "405"]],
]) {
  const responses = document.paths[path][method].responses;
  for (const status of statuses) {
    const response = resolveRef(responses[status]);
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Business read ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
for (const status of ["200", "201"]) {
  const actual = document.paths["/api/v1/proposals/{proposal_id}/approvals"].post.responses[status]?.content?.["application/json"]?.schema?.$ref;
  if (actual !== "#/components/schemas/ApprovalDecisionResponse") {
    throw new Error(`Approval decision ${status} must return ApprovalDecisionResponse`);
  }
}

if (document.paths["/api/v1/search"].post.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/SearchRequest") {
  throw new Error("POST /api/v1/search request body must reference SearchRequest");
}
for (const [path, schema] of [
  ["/api/v1/graph/global", "GraphGlobalRequest"],
  ["/api/v1/graph/neighborhood", "GraphNeighborhoodRequest"],
  ["/api/v1/graph/path", "GraphPathRequest"],
]) {
  const requestBody = document.paths[path].post.requestBody;
  if (requestBody?.required !== true || requestBody?.["x-max-body-bytes"] !== 65536 ||
      requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${schema}`) {
    throw new Error(`${path} must use its strict Graph request schema and 64 KiB body limit`);
  }
}

const schemas = document.components.schemas;
const versionedResponseOperations = [
  ["/conversations/{conversation_id}/questions", "post", "QuestionAcceptance", "QuestionAcceptanceV2"],
  ["/conversations/{conversation_id}/turns", "get", "TurnPage", "TurnPageV2"],
  ["/answers/{answer_id}", "get", "Answer", "AnswerV2"],
  ["/answers/{answer_id}/analysis-timeline", "get", "WorkspaceAnalysisTimeline", "WorkspaceAnalysisTimelineResponse"],
  ["/review/interviews", "get", "InterviewSessionPage", "InterviewSessionPageV2"],
  ["/review/interviews", "post", "InterviewStartResult", "InterviewStartResultV2"],
  ["/review/interviews/{session_id}", "get", "InterviewSnapshot", "InterviewSnapshotV2"],
  ["/review/interviews/{session_id}/turns", "post", "InterviewTurnResult", "InterviewTurnResultV2"],
  ["/review/interviews/{session_id}/complete", "post", "InterviewCompletionResult", "InterviewCompletionResultV2"],
  ["/review/learning-paths/{path_id}/steps/{step_id}", "put", "LearningPathStepResult", "LearningPathStepResultV2"],
];
for (const [suffix, method, legacySchema, currentSchema] of versionedResponseOperations) {
  const legacy = document.paths[`/api/v1${suffix}`]?.[method];
  const current = document.paths[`/api/v2${suffix}`]?.[method];
  if (!legacy || !current || current.operationId !== `${legacy.operationId}V2` ||
      current.security !== undefined || JSON.stringify(current.requestBody) !== JSON.stringify(legacy.requestBody)) {
    throw new Error(`${method.toUpperCase()} ${suffix} must retain its authenticated versioned operation and command contract`);
  }
  for (const [operation, schema] of [[legacy, legacySchema], [current, currentSchema]]) {
    for (const [status, response] of Object.entries(operation.responses)) {
      if (status.startsWith("2") && response.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${schema}`) {
        throw new Error(`${operation.operationId} must return its own ${schema} response contract`);
      }
    }
  }
  if (method !== "get") {
    const expectedCapability = suffix.startsWith("/review/") ? "WriteProposal" : "ReadLocal";
    const methodName = method[0].toUpperCase() + method.slice(1);
    if (!authHandlerSource.includes(`oneCapability(http.Method${methodName}, "/api/v2${suffix}", capability.${expectedCapability})`)) {
      throw new Error(`${current.operationId} must retain its explicit authentication capability`);
    }
  }
}
for (const name of ["InterviewScope", "InterviewQuestion", "InterviewScore", "InterviewFinding", "LearningPathStep"]) {
  if (schemas[name]?.type !== "object" || schemas[name].additionalProperties !== false || schemas[name].oneOf !== undefined) {
    throw new Error(`${name} must retain its v1 object contract; new source unions belong to v2`);
  }
}
if (schemas.Answer.properties.result.oneOf[3]?.$ref !== "#/components/schemas/WorkspaceAnalysisAnswerResult" ||
    schemas.AnswerV2.properties.result.oneOf[3]?.$ref !== "#/components/schemas/WorkspaceAnalysisPublishedAnswerResult" ||
    schemas.InterviewReport.properties.note_sources !== undefined ||
    schemas.InterviewQuestion.properties.source_kind !== undefined ||
    schemas.LearningPathStep.properties.claim_id?.format !== "uuid" ||
    schemas.QuestionAcceptanceV2.properties.status_url?.pattern !== "^/api/v2/answers/") {
  throw new Error("v1 response guarantees must stay isolated from dynamic analysis and frozen-note v2 responses");
}
const workspaceAnalysisTimelinePath = "/api/v1/answers/{answer_id}/analysis-timeline";
const workspaceAnalysisTimelineOperation = document.paths?.[workspaceAnalysisTimelinePath]?.get;
const workspaceAnalysisTimelineParameters = document.paths?.[workspaceAnalysisTimelinePath]?.parameters ?? [];
if (workspaceAnalysisTimelineOperation?.operationId !== "getWorkspaceAnalysisTimeline" ||
    workspaceAnalysisTimelineOperation.security !== undefined || workspaceAnalysisTimelineOperation.requestBody !== undefined ||
    workspaceAnalysisTimelineParameters.map((parameter) => parameter.$ref).join(",") !==
      "#/components/parameters/AnswerID,#/components/parameters/WorkspaceIDQuery" ||
    workspaceAnalysisTimelineOperation.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !==
      "#/components/schemas/WorkspaceAnalysisTimeline") {
  throw new Error("Workspace Analysis timeline operation must remain authenticated, Answer/Workspace-scoped, and authoritative");
}
for (const status of ["400", "404", "405", "500", "503"]) {
  if (resolveRef(workspaceAnalysisTimelineOperation.responses?.[status])?.content?.["application/json"]?.schema?.$ref !==
      "#/components/schemas/Problem") {
    throw new Error(`Workspace Analysis timeline ${status} must return Problem`);
  }
}
const workspaceAnalysisTimelineBudgetFields = [
  "model_calls", "tool_calls", "source_reads", "input_tokens", "output_tokens", "estimated_cost_microunits",
];
const workspaceAnalysisTimelineBudget = schemas.WorkspaceAnalysisTimelineBudget;
if (workspaceAnalysisTimelineBudget?.type !== "object" || workspaceAnalysisTimelineBudget.additionalProperties !== false ||
    workspaceAnalysisTimelineBudget.required?.join(",") !== workspaceAnalysisTimelineBudgetFields.join(",") ||
    Object.keys(workspaceAnalysisTimelineBudget.properties ?? {}).join(",") !== workspaceAnalysisTimelineBudgetFields.join(",") ||
    workspaceAnalysisTimelineBudgetFields.slice(0, 5).some((field) =>
      workspaceAnalysisTimelineBudget.properties?.[field]?.$ref !== "#/components/schemas/WorkspaceAnalysisTimelineCounter") ||
    workspaceAnalysisTimelineBudget.properties?.estimated_cost_microunits?.oneOf?.[0]?.$ref !==
      "#/components/schemas/WorkspaceAnalysisTimelineCounter" ||
    workspaceAnalysisTimelineBudget.properties?.estimated_cost_microunits?.oneOf?.[1]?.type !== "null") {
  throw new Error("WorkspaceAnalysisTimelineBudget must retain all durable usage/maxima dimensions");
}
const workspaceAnalysisToolRefs = schemas.WorkspaceAnalysisTimelineToolRef?.oneOf ?? [];
const workspaceAnalysisExactToolRefs = ["ReadGitStatus@2", "SearchKnowledge@2", "ReadSource@3", "ValidateCitation@3"];
if (workspaceAnalysisToolRefs.length !== workspaceAnalysisExactToolRefs.length ||
    workspaceAnalysisToolRefs.map((schema) => `${schema.properties?.name?.const}@${schema.properties?.version?.const}`).join(",") !==
      workspaceAnalysisExactToolRefs.join(",") ||
    workspaceAnalysisToolRefs.some((schema) => schema.type !== "object" || schema.additionalProperties !== false ||
      schema.required?.join(",") !== "name,version" || Object.keys(schema.properties ?? {}).join(",") !== "name,version")) {
  throw new Error("WorkspaceAnalysisTimelineToolRef must retain the exact frozen name/version pairs");
}
const workspaceAnalysisTimelineErrorCodes = [
  "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT", "WORKSPACE_ANALYSIS_CITATION_INVALID",
  "WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED", "WORKSPACE_ANALYSIS_MODEL_REFUSED",
  "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", "WORKSPACE_ANALYSIS_RECEIPT_INVALID",
  "WORKSPACE_ANALYSIS_RESULT_UNKNOWN", "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED",
  "WORKSPACE_ANALYSIS_MODEL_FAILED", "WORKSPACE_ANALYSIS_TOOL_FAILED", "WORKSPACE_ANALYSIS_RUNTIME_FAILED",
  "WORKSPACE_ANALYSIS_CANCELLED", null,
];
if (JSON.stringify(schemas.WorkspaceAnalysisTimelineItem?.properties?.error_code?.enum) !==
    JSON.stringify(workspaceAnalysisTimelineErrorCodes)) {
  throw new Error("Workspace Analysis timeline item error_code must remain a closed public-code enum");
}
for (const [union, v1, v2] of [
  ["WorkspaceAnalysisTimelineResponse", "WorkspaceAnalysisTimeline", "WorkspaceAnalysisTimelineV2"],
  ["WorkspaceAnalysisPublishedAnswerResult", "WorkspaceAnalysisAnswerResult", "WorkspaceAnalysisAnswerResultV2"],
]) {
  if (schemas[union]?.oneOf?.map((branch) => branch.$ref).join(",") !== `#/components/schemas/${v1},#/components/schemas/${v2}` ||
      schemas[v1]?.properties?.schema_version?.const !== "v1" || schemas[v2]?.properties?.schema_version?.const !== "v2" ||
      schemas[v1]?.additionalProperties !== false || schemas[v2]?.additionalProperties !== false) {
    throw new Error(`${union} must retain independent, closed v1 and v2 contracts`);
  }
}
const workspaceAnalysisV2ToolRefs = schemas.WorkspaceAnalysisTimelineToolRefV2?.oneOf ?? [];
if (workspaceAnalysisV2ToolRefs.map((schema) => `${schema.properties?.name?.const}@${schema.properties?.version?.const}`).join(",") !==
    "ReadGitStatus@3,SearchKnowledge@3,ReadSource@4,ValidateCitation@4" ||
    workspaceAnalysisV2ToolRefs.some((schema) => schema.additionalProperties !== false || schema.required?.join(",") !== "name,version") ||
    schemas.WorkspaceAnalysisTimelineV2.properties.items?.maxItems !== 27 || schemas.WorkspaceAnalysisTimelineItemV2.properties.sequence?.maximum !== 27 ||
    schemas.WorkspaceAnalysisTimelineItemV2.properties.phase?.enum?.join(",") !== "decide_next,inspect_workspace,retrieve_evidence,read_evidence,synthesize_answer,validate_citations,review_publish" ||
    schemas.WorkspaceAnalysisTimelineItemV2.properties.kind?.enum?.join(",") !== "node,model,tool" ||
    JSON.stringify(schemas.WorkspaceAnalysisTimelineItemV2.properties.error_code?.enum) !== JSON.stringify(workspaceAnalysisTimelineErrorCodes)) {
  throw new Error("Workspace Analysis v2 must retain its exact tools, journal bound, phases and public errors");
}
const workspaceAnalysisV2Budget = schemas.WorkspaceAnalysisTimelineBudgetV2;
if (workspaceAnalysisV2Budget.additionalProperties !== false || workspaceAnalysisV2Budget.required?.join(",") !== workspaceAnalysisTimelineBudgetFields.join(",") ||
    [["model_calls", 14], ["tool_calls", 13], ["source_reads", 8], ["input_tokens", 917504]].some(([field, max]) => workspaceAnalysisV2Budget.properties[field]?.properties?.max?.const !== max) ||
    workspaceAnalysisV2Budget.properties.output_tokens?.properties?.max?.minimum !== 7169 || workspaceAnalysisV2Budget.properties.output_tokens?.properties?.max?.maximum !== 11264 ||
    schemas.WorkspaceAnalysisTimelineSourceSummaryV2.properties.source.properties.evidence_ref.pattern !== "^E(?:[1-9]|[12][0-9]|3[0-2])$" ||
    schemas.WorkspaceAnalysisTimelineSourceSummary.properties.source.properties.evidence_ref.pattern !== "^E[1-3]$") {
  throw new Error("Workspace Analysis v2 budgets/global evidence refs must not widen the frozen v1 contract");
}
const workspaceAnalysisV2Payload = schemas.WorkspaceAnalysisAnswerResultV2.properties.payload;
if (!workspaceAnalysisV2Payload.required.includes("git_status") || workspaceAnalysisV2Payload.properties.git_status?.oneOf?.[0]?.$ref !== "#/components/schemas/WorkspaceAnalysisGitStatus" ||
    workspaceAnalysisV2Payload.properties.git_status?.oneOf?.[1]?.type !== "null" || workspaceAnalysisV2Payload.properties.citations?.maxItems !== 32 ||
    workspaceAnalysisV2Payload.properties.budget?.$ref !== "#/components/schemas/WorkspaceAnalysisBudgetSummaryV2" ||
    schemas.WorkspaceAnalysisBudgetSummaryV2.properties.model_calls?.minimum !== 5 || schemas.WorkspaceAnalysisBudgetSummaryV2.properties.model_calls?.maximum !== 14 ||
    schemas.WorkspaceAnalysisBudgetSummary.properties.model_calls?.const !== 3 ||
    schemas.WorkspaceAnalysisAnswerResult.properties.payload.properties.git_status?.$ref !== "#/components/schemas/WorkspaceAnalysisGitStatus") {
  throw new Error("Workspace Analysis v2 result requires nullable actual Git facts and settled bounded dynamic usage");
}
const revisionPreviewPath = "/api/v1/proposals/{proposal_id}/revision-merge-previews";
const revisionCollectionPath = "/api/v1/proposals/{proposal_id}/revisions";
const revisionDetailPath = "/api/v1/proposals/{proposal_id}/revisions/{revision_id}";
const revisionPreviewOperation = document.paths[revisionPreviewPath]?.post;
const revisionHistoryOperation = document.paths[revisionCollectionPath]?.get;
const revisionAppendOperation = document.paths[revisionCollectionPath]?.post;
const revisionDetailOperation = document.paths[revisionDetailPath]?.get;
const revisionHashPattern = "^[0-9a-f]{64}$";

function requireExactRevisionObject(schemaName, required, optional = []) {
  const schema = schemas[schemaName];
  const properties = [...required, ...optional];
  if (schema?.type !== "object" || schema.additionalProperties !== false ||
      schema.required?.join(",") !== required.join(",") ||
      Object.keys(schema.properties ?? {}).join(",") !== properties.join(",")) {
    throw new Error(`${schemaName} must remain an exact strict Proposal Revision object`);
  }
}

for (const [operation, capability] of [
  [revisionPreviewOperation, "READ_LOCAL"],
  [revisionHistoryOperation, "READ_LOCAL"],
  [revisionAppendOperation, "WRITE_PROPOSAL"],
  [revisionDetailOperation, "READ_LOCAL"],
]) {
  if (!operation || operation.security !== undefined || operation["x-required-capability"] !== capability) {
    throw new Error(`Proposal Revision operation must inherit authentication and require ${capability}`);
  }
}
if (revisionPreviewOperation.requestBody?.required !== true ||
    revisionPreviewOperation.requestBody?.["x-max-body-bytes"] !== 4096 ||
    revisionPreviewOperation.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ProposalRevisionMergePreviewRequest" ||
    revisionPreviewOperation.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ProposalRevisionMergePreview") {
  throw new Error("Proposal Revision preview must use its bounded strict request and response schemas");
}
if (!revisionAppendOperation.parameters?.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") ||
    revisionAppendOperation.requestBody?.required !== true ||
    revisionAppendOperation.requestBody?.["x-max-body-bytes"] !== 8388608 ||
    revisionAppendOperation.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/AppendProposalRevisionRequest") {
  throw new Error("Proposal Revision append must require Idempotency-Key and its bounded strict request schema");
}
for (const [status, replayed] of [["200", true], ["201", false]]) {
  const response = revisionAppendOperation.responses?.[status];
  const schema = response?.content?.["application/json"]?.schema;
  if (schema?.allOf?.[0]?.$ref !== "#/components/schemas/ProposalRevisionAppendResponse" ||
      schema?.allOf?.[1]?.properties?.replayed?.const !== replayed ||
      response?.["x-max-response-bytes"] !== 16777216 ||
      response?.headers?.["Cache-Control"]?.schema?.const !== "private, no-store") {
    throw new Error(`Proposal Revision append ${status} must expose a bounded replayed=${String(replayed)} no-store response`);
  }
}
const historyParameters = revisionHistoryOperation.parameters ?? [];
const historyLimit = historyParameters.find((parameter) => parameter.name === "limit");
const historyCursor = historyParameters.find((parameter) => parameter.name === "before_revision_no");
if (historyParameters.length !== 2 || historyLimit?.in !== "query" ||
    historyLimit.schema?.minimum !== 1 || historyLimit.schema?.maximum !== 100 || historyLimit.schema?.default !== 30 ||
    historyCursor?.in !== "query" || historyCursor.schema?.minimum !== 2 ||
    revisionHistoryOperation.requestBody !== undefined ||
    revisionHistoryOperation.responses?.["200"]?.["x-max-response-bytes"] !== 1048576 ||
    revisionHistoryOperation.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ProposalRevisionHistoryPage") {
  throw new Error("Proposal Revision history must retain its strict bounded descending cursor contract");
}
if (revisionDetailOperation.requestBody !== undefined ||
    revisionDetailOperation.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ProposalRevisionHistoryDetail") {
  throw new Error("Proposal Revision detail must return the exact historical detail schema");
}
for (const [operation, successStatuses] of [
  [revisionPreviewOperation, ["200"]],
  [revisionHistoryOperation, ["200"]],
  [revisionDetailOperation, ["200"]],
]) {
  for (const status of successStatuses) {
    const response = operation.responses?.[status];
    if (response?.headers?.["Cache-Control"]?.schema?.const !== "private, no-store") {
      throw new Error(`Proposal Revision ${operation.operationId} ${status} must remain private, no-store`);
    }
  }
}
if (revisionPreviewOperation.responses?.["200"]?.["x-max-response-bytes"] !== 16777216 ||
    revisionDetailOperation.responses?.["200"]?.["x-max-response-bytes"] !== 16777216) {
  throw new Error("Proposal Revision body-bearing reads must retain the 16 MiB response limit");
}
for (const [operation, statuses] of [
  [revisionPreviewOperation, ["400", "401", "403", "404", "405", "409", "413", "415", "422", "500", "503"]],
  [revisionAppendOperation, ["400", "401", "403", "404", "405", "409", "413", "415", "422", "500", "503"]],
  [revisionHistoryOperation, ["400", "401", "403", "404", "405", "500", "503"]],
  [revisionDetailOperation, ["400", "401", "403", "404", "405", "413", "500", "503"]],
]) {
  for (const status of statuses) {
    const response = resolveRef(operation.responses?.[status]);
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`Proposal Revision ${operation.operationId} ${status} must return Problem`);
    }
  }
}
for (const [operation, errorCodes] of [
  [revisionPreviewOperation, [
    "PROPOSAL_MERGE_CONTENT_INVALID", "PROPOSAL_REVISION_INPUT_TOO_LARGE", "PROPOSAL_REVISION_NOT_EDITABLE",
    "PROPOSAL_BASE_SNAPSHOT_UNAVAILABLE", "PROPOSAL_REVISION_STALE", "PROPOSAL_REVISION_WORKFLOW_ACTIVE",
    "PROPOSAL_MERGE_RESULT_TOO_LARGE", "PROPOSAL_MERGE_BUSY", "PROPOSAL_MERGE_TIMEOUT",
    "PROPOSAL_MERGE_TEMPORARY_STORAGE_UNAVAILABLE", "PROPOSAL_MERGE_ENGINE_UNAVAILABLE",
    "PROPOSAL_MERGE_ENGINE_UNSUPPORTED", "PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID",
    "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE", "PROPOSAL_CURRENT_CONTENT_UNAVAILABLE",
    "PROPOSAL_REVISION_SOURCE_INVALID", "PROPOSAL_CURRENT_CONTENT_HASH_INVALID", "PROPOSAL_BASE_SNAPSHOT_INVALID",
  ]],
  [revisionAppendOperation, [
    "IDEMPOTENCY_KEY_REQUIRED", "IDEMPOTENCY_KEY_REUSED", "PROPOSAL_REVISION_INPUT_TOO_LARGE",
    "PROPOSAL_REVISION_STALE", "PROPOSAL_REVISION_SIDE_EFFECT_STARTED", "PROPOSAL_REVISION_WORKFLOW_ACTIVE",
    "PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE",
    "PROPOSAL_REVISION_AUTHORIZATION_CONFLICT", "PROPOSAL_REVISION_CONFLICTS_UNRESOLVED",
    "PROPOSAL_MERGE_RESULT_TOO_LARGE", "PROPOSAL_MERGE_ENGINE_UNAVAILABLE",
    "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE", "PROPOSAL_CURRENT_CONTENT_UNAVAILABLE",
    "PROPOSAL_REVISION_SOURCE_INVALID", "PROPOSAL_CURRENT_CONTENT_HASH_INVALID", "PROPOSAL_BASE_SNAPSHOT_INVALID",
    "PROPOSAL_REVISION_RESULT_INVALID",
  ]],
  [revisionHistoryOperation, [
    "PROPOSAL_REVISION_HISTORY_QUERY_INVALID", "PROPOSAL_REVISION_HISTORY_BINDING_INVALID",
    "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE",
  ]],
  [revisionDetailOperation, [
    "PROPOSAL_REVISION_HISTORY_QUERY_INVALID", "PROPOSAL_REVISION_NOT_FOUND",
    "PROPOSAL_REVISION_INPUT_TOO_LARGE",
    "PROPOSAL_REVISION_HISTORY_BINDING_INVALID", "PROPOSAL_REVISION_REPOSITORY_UNAVAILABLE",
  ]],
]) {
  for (const errorCode of errorCodes) {
    if (!operation["x-error-codes"]?.includes(errorCode)) {
      throw new Error(`Proposal Revision ${operation.operationId} error matrix is missing ${errorCode}`);
    }
  }
}

requireExactRevisionObject("ProposalRevisionCapability", ["editable", "reason"]);
if (schemas.ProposalRevisionCapability.properties?.editable?.type !== "boolean" ||
    schemas.ProposalRevisionCapability.properties?.reason?.enum?.join(",") !==
      "AVAILABLE,PROPOSAL_REVISION_UNSUPPORTED_TYPE,PROPOSAL_REVISION_UNSUPPORTED_MODE,PROPOSAL_REVISION_STATUS_NOT_EDITABLE,PROPOSAL_REVISION_STALE,PROPOSAL_REVISION_WORKFLOW_ACTIVE,PROPOSAL_REVISION_SIDE_EFFECT_STARTED,PROPOSAL_REVISION_INPUT_TOO_LARGE,PROPOSAL_MERGE_ENGINE_UNAVAILABLE") {
  throw new Error("ProposalRevisionCapability reason set drifted");
}
requireExactRevisionObject("ProposalRevisionMergePreviewRequest", [
  "source_revision_id", "source_change_hash", "expected_proposal_version",
]);
if (schemas.ProposalRevisionMergePreviewRequest.properties?.source_revision_id?.format !== "uuid" ||
    schemas.ProposalRevisionMergePreviewRequest.properties?.source_change_hash?.pattern !== revisionHashPattern ||
    schemas.ProposalRevisionMergePreviewRequest.properties?.expected_proposal_version?.minimum !== 1) {
  throw new Error("ProposalRevisionMergePreviewRequest source binding drifted");
}
const appendRevisionRequired = [
  "expected_proposal_version", "source_revision_id", "source_change_hash", "expected_current_hash", "merge_fingerprint",
  "merge_algorithm", "merge_algorithm_version", "content", "evidence_summary", "risk", "rollback_plan", "resolved_conflict_ids",
];
requireExactRevisionObject("AppendProposalRevisionRequest", appendRevisionRequired);
const appendRevisionRequest = schemas.AppendProposalRevisionRequest;
if (appendRevisionRequest.properties?.expected_proposal_version?.minimum !== 1 ||
    appendRevisionRequest.properties?.source_revision_id?.format !== "uuid" ||
    ["source_change_hash", "expected_current_hash", "merge_fingerprint"].some((field) => appendRevisionRequest.properties?.[field]?.pattern !== revisionHashPattern) ||
    appendRevisionRequest.properties?.merge_algorithm?.const !== "git-merge-file" ||
    appendRevisionRequest.properties?.merge_algorithm_version?.const !== "diff3/myers/marker32/v1" ||
    appendRevisionRequest.properties?.content?.["x-max-utf8-bytes"] !== 1048576 ||
    ["evidence_summary", "risk", "rollback_plan"].some((field) => appendRevisionRequest.properties?.[field]?.["x-max-utf8-bytes"] !== 65536) ||
    appendRevisionRequest.properties?.resolved_conflict_ids?.maxItems !== 1024 ||
    appendRevisionRequest.properties?.resolved_conflict_ids?.uniqueItems !== true ||
    appendRevisionRequest.properties?.resolved_conflict_ids?.items?.pattern !== revisionHashPattern) {
  throw new Error("AppendProposalRevisionRequest merge binding or public bounds drifted");
}
requireExactRevisionObject("ProposalRevisionTextSnapshot", ["content", "hash", "byte_size"]);
if (schemas.ProposalRevisionTextSnapshot.properties?.content?.["x-max-utf8-bytes"] !== 1048576 ||
    schemas.ProposalRevisionTextSnapshot.properties?.hash?.pattern !== revisionHashPattern ||
    schemas.ProposalRevisionTextSnapshot.properties?.byte_size?.maximum !== 1048576) {
  throw new Error("ProposalRevisionTextSnapshot byte and hash contract drifted");
}
requireExactRevisionObject("ProposalRevisionMergeConflict", ["id", "ordinal", "base", "current", "proposed"]);
if (schemas.ProposalRevisionMergeConflict.properties?.id?.pattern !== revisionHashPattern ||
    schemas.ProposalRevisionMergeConflict.properties?.ordinal?.minimum !== 1 ||
    schemas.ProposalRevisionMergeConflict.properties?.ordinal?.maximum !== 1024 ||
    ["base", "current", "proposed"].some((field) => schemas.ProposalRevisionMergeConflict.properties?.[field]?.["x-max-utf8-bytes"] !== 1048576)) {
  throw new Error("ProposalRevisionMergeConflict exact fragment contract drifted");
}
const previewRequired = [
  "schema_version", "merge_algorithm", "merge_algorithm_version", "merge_fingerprint", "proposal_id", "workspace_id",
  "proposal_version", "source_revision_id", "source_revision_no", "source_change_hash", "target_path", "target_mode",
  "base", "current", "proposed", "candidate", "conflict_count", "conflicts",
];
requireExactRevisionObject("ProposalRevisionMergePreview", previewRequired);
const revisionPreview = schemas.ProposalRevisionMergePreview;
if (revisionPreview.properties?.schema_version?.const !== "proposal-text-merge-preview/v1" ||
    revisionPreview.properties?.merge_algorithm?.const !== "git-merge-file" ||
    revisionPreview.properties?.merge_algorithm_version?.const !== "diff3/myers/marker32/v1" ||
    revisionPreview.properties?.target_mode?.const !== "REPLACE" ||
    ["base", "current", "proposed", "candidate"].some((field) => revisionPreview.properties?.[field]?.$ref !== "#/components/schemas/ProposalRevisionTextSnapshot") ||
    revisionPreview.properties?.conflict_count?.maximum !== 1024 ||
    revisionPreview.properties?.conflicts?.maxItems !== 1024 ||
    revisionPreview.properties?.conflicts?.items?.$ref !== "#/components/schemas/ProposalRevisionMergeConflict") {
  throw new Error("ProposalRevisionMergePreview immutable merge contract drifted");
}
requireExactRevisionObject("ProposalRevisionHistoryApproval", [
  "id", "proposal_id", "revision_id", "change_hash", "decision", "decided_at",
], ["approved_git_head"]);
const revisionHistoryApproval = schemas.ProposalRevisionHistoryApproval;
if (revisionHistoryApproval.properties?.id?.format !== "uuid" ||
    revisionHistoryApproval.properties?.proposal_id?.format !== "uuid" ||
    revisionHistoryApproval.properties?.revision_id?.format !== "uuid" ||
    revisionHistoryApproval.properties?.change_hash?.pattern !== revisionHashPattern ||
    revisionHistoryApproval.properties?.decision?.enum?.join(",") !== "approved,rejected" ||
    revisionHistoryApproval.properties?.approved_git_head?.pattern !== "^([0-9a-f]{40}|[0-9a-f]{64})$" ||
    revisionHistoryApproval.properties?.decided_at?.format !== "date-time") {
  throw new Error("ProposalRevisionHistoryApproval immutable decision contract drifted");
}
requireExactRevisionObject("ProposalRevisionWorkflow", ["id", "status_url"]);
requireExactRevisionObject("ProposalRevisionHistoryItem", [
  "proposal_id", "revision_id", "revision_no", "target_path", "target_mode", "base_hash", "change_hash",
  "base_available", "current", "approval", "workflow", "created_at",
]);
const revisionHistoryItem = schemas.ProposalRevisionHistoryItem;
if (revisionHistoryItem.properties?.target_mode?.const !== "REPLACE" ||
    revisionHistoryItem.properties?.base_hash?.pattern !== revisionHashPattern ||
    revisionHistoryItem.properties?.change_hash?.pattern !== revisionHashPattern ||
    revisionHistoryItem.properties?.approval?.oneOf?.[0]?.$ref !== "#/components/schemas/ProposalRevisionHistoryApproval" ||
    revisionHistoryItem.properties?.approval?.oneOf?.[1]?.type !== "null" ||
    revisionHistoryItem.properties?.workflow?.oneOf?.[0]?.$ref !== "#/components/schemas/ProposalRevisionWorkflow" ||
    revisionHistoryItem.properties?.workflow?.oneOf?.[1]?.type !== "null") {
  throw new Error("ProposalRevisionHistoryItem historical binding drifted");
}
requireExactRevisionObject("ProposalRevisionHistoryPage", ["items"], ["next_before_revision_no"]);
if (schemas.ProposalRevisionHistoryPage.properties?.items?.maxItems !== 100 ||
    schemas.ProposalRevisionHistoryPage.properties?.items?.items?.$ref !== "#/components/schemas/ProposalRevisionHistoryItem" ||
    schemas.ProposalRevisionHistoryPage.properties?.next_before_revision_no?.minimum !== 2) {
  throw new Error("ProposalRevisionHistoryPage pagination contract drifted");
}
requireExactRevisionObject("ProposalRevisionBaseSnapshot", ["hash", "content", "byte_size", "schema_version", "created_at"]);
if (schemas.ProposalRevisionBaseSnapshot.properties?.hash?.pattern !== revisionHashPattern ||
    schemas.ProposalRevisionBaseSnapshot.properties?.content?.["x-max-utf8-bytes"] !== 1048576 ||
    schemas.ProposalRevisionBaseSnapshot.properties?.byte_size?.maximum !== 1048576 ||
    schemas.ProposalRevisionBaseSnapshot.properties?.schema_version?.const !== "proposal-base-snapshot/v1") {
  throw new Error("ProposalRevisionBaseSnapshot exact-body contract drifted");
}
requireExactRevisionObject("ProposalRevisionLineage", [
  "source_revision_id", "source_change_hash", "kind", "merge_algorithm", "merge_algorithm_version", "merge_fingerprint", "created_at",
]);
if (schemas.ProposalRevisionLineage.properties?.source_revision_id?.format !== "uuid" ||
    schemas.ProposalRevisionLineage.properties?.source_change_hash?.pattern !== revisionHashPattern ||
    schemas.ProposalRevisionLineage.properties?.kind?.enum?.join(",") !== "DIRECT_EDIT,THREE_WAY_MERGE" ||
    schemas.ProposalRevisionLineage.properties?.merge_algorithm?.const !== "git-merge-file" ||
    schemas.ProposalRevisionLineage.properties?.merge_algorithm_version?.const !== "diff3/myers/marker32/v1" ||
    schemas.ProposalRevisionLineage.properties?.merge_fingerprint?.pattern !== revisionHashPattern) {
  throw new Error("ProposalRevisionLineage source binding drifted");
}
const historicalRevision = schemas.HistoricalProposalRevision;
if (historicalRevision.allOf?.[0]?.$ref !== "#/components/schemas/ProposalRevision" ||
    historicalRevision.allOf?.[1]?.properties?.target_mode?.const !== "REPLACE" ||
    historicalRevision.allOf?.[1]?.properties?.base_hash?.pattern !== revisionHashPattern ||
    historicalRevision.allOf?.[1]?.properties?.content?.["x-max-utf8-bytes"] !== 1048576 ||
    ["evidence_summary", "risk", "rollback_plan"].some((field) => historicalRevision.allOf?.[1]?.properties?.[field]?.["x-max-utf8-bytes"] !== 65536)) {
  throw new Error("HistoricalProposalRevision bounded file-patch contract drifted");
}
requireExactRevisionObject("ProposalRevisionHistoryDetail", [
  "workspace_id", "proposal_id", "current_revision_id", "current", "revision", "base_available",
  "base_snapshot", "lineage", "approval", "workflow",
]);
const revisionHistoryDetail = schemas.ProposalRevisionHistoryDetail;
if (revisionHistoryDetail.properties?.revision?.$ref !== "#/components/schemas/HistoricalProposalRevision" ||
    revisionHistoryDetail.properties?.base_snapshot?.oneOf?.[0]?.$ref !== "#/components/schemas/ProposalRevisionBaseSnapshot" ||
    revisionHistoryDetail.properties?.base_snapshot?.oneOf?.[1]?.type !== "null" ||
    revisionHistoryDetail.properties?.lineage?.oneOf?.[0]?.$ref !== "#/components/schemas/ProposalRevisionLineage" ||
    revisionHistoryDetail.properties?.lineage?.oneOf?.[1]?.type !== "null" ||
    revisionHistoryDetail.properties?.approval?.oneOf?.[0]?.$ref !== "#/components/schemas/ProposalRevisionHistoryApproval" ||
    revisionHistoryDetail.properties?.approval?.oneOf?.[1]?.type !== "null" ||
    revisionHistoryDetail.properties?.workflow?.oneOf?.[0]?.$ref !== "#/components/schemas/ProposalRevisionWorkflow" ||
    revisionHistoryDetail.properties?.workflow?.oneOf?.[1]?.type !== "null") {
  throw new Error("ProposalRevisionHistoryDetail nullable historical projections drifted");
}
requireExactRevisionObject("ProposalRevisionAppendResponse", [
  "proposal_type", "id", "workspace_id", "target_path", "status", "risk_level", "version", "revision_capability",
  "revision", "approval", "created_at", "updated_at", "replayed",
]);
const revisionAppendResponse = schemas.ProposalRevisionAppendResponse;
if (revisionAppendResponse.properties?.proposal_type?.const !== "file_patch" ||
    revisionAppendResponse.properties?.status?.const !== "ready_for_review" ||
    revisionAppendResponse.properties?.version?.minimum !== 1 ||
    revisionAppendResponse.properties?.revision_capability?.$ref !== "#/components/schemas/ProposalRevisionCapability" ||
    revisionAppendResponse.properties?.revision?.$ref !== "#/components/schemas/ProposalRevision" ||
    revisionAppendResponse.properties?.approval?.type !== "null" ||
    revisionAppendResponse.properties?.replayed?.type !== "boolean") {
  throw new Error("ProposalRevisionAppendResponse ready-for-review receipt drifted");
}
for (const schemaName of [
  "FilePatchProposal", "RestoreDocumentProposal", "KnowledgeChangeProposal", "PublishArtifactProposal", "DownstreamUpdateProposal",
]) {
  if (!schemas[schemaName].required?.includes("version") || schemas[schemaName].properties?.version?.minimum !== 1 ||
      !schemas[schemaName].required?.includes("revision_capability") ||
      schemas[schemaName].properties?.revision_capability?.$ref !== "#/components/schemas/ProposalRevisionCapability") {
    throw new Error(`${schemaName} must expose version and revision_capability`);
  }
}
if (!schemas.ProposalSummary.required?.includes("version") || schemas.ProposalSummary.properties?.version?.minimum !== 1 ||
    !schemas.ProposalSummary.required?.includes("revision_capability") ||
    schemas.ProposalSummary.properties?.revision_capability?.$ref !== "#/components/schemas/ProposalRevisionCapability" ||
    !document.paths["/api/v1/workspaces/{workspace_id}/proposals"]?.get?.["x-error-codes"]?.includes("PROPOSAL_REVISION_CAPABILITY_INVALID")) {
  throw new Error("ProposalSummary must expose version and revision_capability with fail-closed output validation");
}
for (const [operation, statuses] of [
  [document.paths["/api/v1/workspaces/{workspace_id}/proposals"]?.post, ["200", "201"]],
  [document.paths["/api/v1/proposals/{proposal_id}"]?.get, ["200"]],
  [document.paths["/api/v1/workspaces/{workspace_id}/impact-reports/{report_id}/proposals"]?.post, ["200", "201"]],
]) {
  for (const status of statuses) {
    if (operation?.responses?.[status]?.headers?.["Cache-Control"]?.schema?.const !== "private, no-store") {
      throw new Error(`Proposal response ${operation?.operationId ?? "unknown"} ${status} must remain private, no-store`);
    }
  }
}

const workflowRunHumanTask = schemas.WorkflowRun?.properties?.human_task?.oneOf ?? [];
if (!schemas.WorkflowRun?.required?.includes("human_task") ||
    workflowRunHumanTask[0]?.$ref !== "#/components/schemas/PendingWorkflowHumanTask" ||
    workflowRunHumanTask[1]?.type !== "null") {
  throw new Error("WorkflowRun must expose an explicit pending Human Task or null");
}
const pendingWorkflowHumanTask = schemas.PendingWorkflowHumanTask;
if (pendingWorkflowHumanTask?.type !== "object" || pendingWorkflowHumanTask.additionalProperties !== false ||
    pendingWorkflowHumanTask.required?.join(",") !== "id,run_id,node_run_id,status,expected_input_schema,target_version,expires_at,created_at,review" ||
    pendingWorkflowHumanTask.properties?.status?.const !== "pending" ||
    pendingWorkflowHumanTask.properties?.review?.oneOf?.[0]?.$ref !== "#/components/schemas/WorkflowHumanTaskReview" ||
    pendingWorkflowHumanTask.properties?.review?.oneOf?.[1]?.type !== "null") {
  throw new Error("PendingWorkflowHumanTask must retain its exact nullable review contract");
}
const humanTaskReviewRefs = schemas.WorkflowHumanTaskReview?.oneOf?.map((entry) => entry.$ref) ?? [];
if (humanTaskReviewRefs.join(",") !== "#/components/schemas/WorkflowTopicOutlineReview,#/components/schemas/WorkflowMergeComparisonReview" ||
    schemas.WorkflowHumanTaskReview?.discriminator?.propertyName !== "kind") {
  throw new Error("WorkflowHumanTaskReview must remain a kind-discriminated closed union");
}
for (const [schemaName, required, kind] of [
  ["WorkflowTopicOutlineReview", "kind,schema_version,workspace_id,run_id,task_id,node_run_id,snapshot_id,snapshot_hash,template_revision_id,template_hash,outline", "TOPIC_OUTLINE"],
  ["WorkflowMergeComparisonReview", "kind,schema_version,workspace_id,run_id,task_id,node_run_id,snapshot_id,snapshot_hash,artifact_id,revision_hash,default_target_path,diff_hash,diff_preview,diff_truncated,conflict_count,evidence_count,document_count,categories,comparison", "MERGE_COMPARISON"],
]) {
  const schema = schemas[schemaName];
  if (schema?.type !== "object" || schema.additionalProperties !== false || schema.required?.join(",") !== required ||
      schema.properties?.kind?.const !== kind || schema.properties?.schema_version?.const !== 1) {
    throw new Error(`${schemaName} must retain its exact versioned review identity`);
  }
}
const workflowEvidenceVariants = [
  ["WorkflowReviewSourceEvidence", "kind,source_version_id,source_span_id,content_hash,excerpt_hash", "SOURCE_VERSION", false],
  ["WorkflowReviewDocumentEvidence", "kind,document_id,article_revision_id,revision_no,content_hash", "DOCUMENT_REVISION", true],
  ["WorkflowMergeSourceEvidence", "category,kind,source_version_id,source_span_id,content_hash,excerpt_hash", "SOURCE_VERSION", false],
  ["WorkflowMergeDocumentEvidence", "category,kind,document_id,article_revision_id,revision_no,content_hash", "DOCUMENT_REVISION", true],
];
for (const [schemaName, required, kind, documentVariant] of workflowEvidenceVariants) {
  const schema = schemas[schemaName];
  if (schema?.type !== "object" || schema.additionalProperties !== false || schema.required?.join(",") !== required ||
      schema.properties?.kind?.const !== kind || schema.properties?.content_hash?.pattern !== "^[0-9a-f]{64}$" ||
      (documentVariant ? schema.properties?.revision_no?.minimum !== 1 : schema.properties?.excerpt_hash?.pattern !== "^[0-9a-f]{64}$")) {
    throw new Error(`${schemaName} must retain its exact immutable source shape`);
  }
}
for (const [unionName, sourceName, documentName] of [
  ["WorkflowReviewEvidence", "WorkflowReviewSourceEvidence", "WorkflowReviewDocumentEvidence"],
  ["WorkflowMergeEvidence", "WorkflowMergeSourceEvidence", "WorkflowMergeDocumentEvidence"],
]) {
  const schema = schemas[unionName];
  const refs = schema?.oneOf?.map((entry) => entry.$ref) ?? [];
  if (refs.join(",") !== `#/components/schemas/${sourceName},#/components/schemas/${documentName}` ||
      schema?.discriminator?.propertyName !== "kind") {
    throw new Error(`${unionName} must remain a kind-discriminated Source/Document union`);
  }
}
if (schemas.WorkflowTopicOutlineReview.properties.outline.minItems !== 1 ||
    schemas.WorkflowTopicOutlineReview.properties.outline.maxItems !== 24 ||
    schemas.WorkflowTopicOutlineSection.properties.supports.maxItems !== 32 ||
    schemas.WorkflowTopicOutlineSection.oneOf?.length !== 2) {
  throw new Error("Topic outline review bounds or Evidence/GAP exclusivity drifted");
}
const mergeReview = schemas.WorkflowMergeComparisonReview.properties;
const mergeCategoryOrder = mergeReview.categories.prefixItems?.map((entry) => entry.allOf?.[1]?.properties?.category?.const) ?? [];
const mergeCategoryItems = mergeReview.categories.items;
if (mergeReview.categories.minItems !== 4 || mergeReview.categories.maxItems !== 4 ||
    mergeCategoryItems === null || typeof mergeCategoryItems !== "object" || Array.isArray(mergeCategoryItems) ||
    Object.keys(mergeCategoryItems).length !== 0 ||
    mergeCategoryOrder.join(",") !== "DUPLICATE,COMPLEMENTARY,CONFLICT,UNIQUE" ||
    mergeReview.comparison.maxItems !== 64 || mergeReview.document_count?.minimum !== 0 || mergeReview.diff_preview.minLength !== 1 ||
    mergeReview.diff_preview.maxLength !== 32768 || mergeReview.diff_preview["x-max-utf8-bytes"] !== 32768 ||
    mergeReview.default_target_path?.minLength !== 1 || mergeReview.default_target_path?.maxLength !== 4096 ||
    mergeReview.default_target_path?.["x-max-utf8-bytes"] !== 4096 ||
    mergeReview.default_target_path?.pattern !== "^(?!/)(?!\\.git(?:/|$))(?!\\.knowledge(?:/|$))(?!.*\\\\)(?!.*(?:^|/)\\.\\.?(?:/|$)).+\\.(?:md|markdown)$") {
  throw new Error("Merge review category order, Evidence preview, or bounded diff contract drifted");
}
const captureOperations = [
  ["/api/v1/workspaces/{workspace_id}/captures", "post", ["200", "201"], "CaptureCommandResult", ["400", "401", "403", "404", "405", "409", "422", "500", "503"], "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/captures", "get", ["200"], "CapturePage", ["400", "401", "403", "405", "500", "503"], "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/capture-files", "post", ["200", "201"], "CaptureCommandResult", ["400", "401", "403", "404", "405", "409", "422", "500", "503"], "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/captures/{capture_id}", "get", ["200"], "Capture", ["400", "401", "403", "404", "405", "500", "503"], "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/captures/{capture_id}/retry", "post", ["200", "202"], "CaptureCommandResult", ["400", "401", "403", "404", "405", "409", "422", "500", "503"], "WRITE_PROPOSAL"],
];
for (const [path, method, successStatuses, successSchema, errorStatuses, capability] of captureOperations) {
  const operation = document.paths[path]?.[method];
  if (!operation || operation.security !== undefined || operation["x-required-capability"] !== capability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${capability}`);
  }
  for (const status of successStatuses) {
    if (operation.responses?.[status]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}`) {
      throw new Error(`invalid Capture ${status} success schema for ${method.toUpperCase()} ${path}`);
    }
  }
  for (const status of errorStatuses) {
    if (resolveRef(operation.responses?.[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Capture ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
const captureList = document.paths["/api/v1/workspaces/{workspace_id}/captures"].get;
const captureQuery = (name) => captureList.parameters.find((parameter) => parameter.name === name)?.schema;
if (captureQuery("kind")?.$ref !== "#/components/schemas/CaptureKind" ||
    captureQuery("status")?.$ref !== "#/components/schemas/CaptureStatus" ||
    captureQuery("limit")?.minimum !== 1 || captureQuery("limit")?.maximum !== 100 || captureQuery("limit")?.default !== 30 ||
    captureQuery("cursor")?.maxLength !== 4096 || schemas.CapturePage.properties.items.maxItems !== 100) {
  throw new Error("Capture list filter, cursor or page limit contract drifted");
}
if (document.paths["/api/v1/workspaces/{workspace_id}/captures"].post.requestBody?.["x-max-body-bytes"] !== 2097152 ||
    document.paths["/api/v1/workspaces/{workspace_id}/capture-files"].post.requestBody?.["x-max-body-bytes"] !== 11534336 ||
    document.paths["/api/v1/workspaces/{workspace_id}/captures/{capture_id}/retry"].post.requestBody?.["x-max-body-bytes"] !== 1024) {
  throw new Error("Capture request body limits drifted from the HTTP boundary");
}
for (const schemaName of ["CreateCaptureRequest", "UploadCaptureRequest", "RetryCaptureRequest", "Capture", "CaptureCommandResult", "CapturePage"]) {
  if (schemas[schemaName].additionalProperties !== false && schemaName !== "CreateCaptureRequest") {
    throw new Error(`${schemaName} must reject unknown fields`);
  }
}
const profilePath = "/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/knowledge-profile";
const profileRetryPath = `${profilePath}/retry`;
for (const [path, method, successStatuses, schema, capability] of [
  [profilePath, "get", ["200"], "DocumentKnowledgeProfileResponse", "READ_LOCAL"],
  [profileRetryPath, "post", ["200", "202"], "KnowledgeProfileCommandResult", "WRITE_PROPOSAL"],
]) {
  const operation = document.paths[path]?.[method];
  if (!operation || operation.security !== undefined || operation["x-required-capability"] !== capability) {
    throw new Error(`invalid Profile capability contract for ${method.toUpperCase()} ${path}`);
  }
  for (const status of successStatuses) {
    if (operation.responses?.[status]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${schema}`) {
      throw new Error(`invalid Profile ${status} success schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
if (document.paths[profileRetryPath].post.requestBody?.["x-max-body-bytes"] !== 1024 ||
    document.paths[profileRetryPath].post.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/RetryKnowledgeProfileRequest" ||
    schemas.CaptureStageStatus?.enum?.join(",") !== "PENDING,RUNNING,READY,FAILED,CAPABILITY_UNAVAILABLE,STALE,NOT_APPLICABLE" ||
    schemas.KnowledgeProfileStatus?.enum?.join(",") !== "PENDING,RUNNING,READY,FAILED,CAPABILITY_UNAVAILABLE,STALE" ||
    schemas.RetryKnowledgeProfileRequest?.additionalProperties !== false ||
    schemas.DocumentKnowledgeProfileResponse?.additionalProperties !== false ||
    schemas.KnowledgeProfileCommandResult?.additionalProperties !== false ||
    schemas.DocumentKnowledgeProfile?.additionalProperties !== false ||
    schemas.DocumentKnowledgeProfileContent?.properties?.topics?.minItems !== 1 ||
    schemas.DocumentKnowledgeProfileContent?.properties?.knowledge_points?.minItems !== 1 ||
    schemas.KnowledgeProfileRevision?.properties?.content?.$ref !== "#/components/schemas/DocumentKnowledgeProfileContent" ||
    schemas.KnowledgeProfileRevision?.properties?.schema_version?.const !== "document-knowledge-profile/v1") {
  throw new Error("Document Knowledge Profile contract drifted");
}
if (schemas.CreateCaptureRequest.oneOf?.length !== 2 || !schemas.CreateCaptureRequest.oneOf.every((variant) => variant.additionalProperties === false) ||
    document.paths["/api/v1/workspaces/{workspace_id}/capture-files"].post.requestBody?.content?.["multipart/form-data"]?.schema?.$ref !== "#/components/schemas/UploadCaptureRequest" ||
    schemas.Capture.properties.original_location !== undefined ||
    schemas.Capture.properties.original_input_hash !== undefined ||
    !schemas.SystemStatus.required?.includes("capture") ||
    schemas.SystemStatus.properties?.capture?.$ref !== "#/components/schemas/OptionalCapabilityStatus") {
  throw new Error("Capture strict request, redacted response, or capability status contract drifted");
}
const authoringOperations = [
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts", "post", ["200", "201"], "WorkingDraftCommandResult", "AuthoringEmptyCommand", 4096, "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts", "get", ["200"], "WorkingDraftPage", undefined, undefined, "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}", "get", ["200"], "WorkingDraft", undefined, undefined, "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}", "put", ["200"], "WorkingDraftCommandResult", "UpdateWorkingDraftRequest", 62947328, "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts/{draft_id}/freeze", "post", ["200", "201"], "FreezeWorkingDraftResult", "FreezeWorkingDraftRequest", 4096, "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/revisions/{revision_id}/publish-proposals", "post", ["200", "201"], "PublishArticleRevisionResult", "AuthoringEmptyCommand", 4096, "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}", "get", ["200"], "DocumentDraftDetail", undefined, undefined, "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/authoring/overview", "get", ["200"], "AuthoringOverview", undefined, undefined, "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/authoring/documents", "get", ["200"], "DocumentDraftPage", undefined, undefined, "READ_LOCAL"],
];
for (const [path, method, successStatuses, successSchema, requestSchema, bodyLimit, capability] of authoringOperations) {
  const pathItem = document.paths[path];
  const operation = pathItem?.[method];
  if (!operation || operation.security !== undefined || operation["x-required-capability"] !== capability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${capability}`);
  }
  for (const status of successStatuses) {
    if (operation.responses?.[status]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}`) {
      throw new Error(`invalid Authoring ${status} success schema for ${method.toUpperCase()} ${path}`);
    }
  }
  const errorStatuses = method === "get"
    ? ["400", "401", "403", "404", "405", "500", "503"]
    : ["400", "401", "403", "404", "405", "409", "415", "422", "500", "503"];
  for (const status of errorStatuses) {
    if (resolveRef(operation.responses?.[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Authoring ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
  if (requestSchema === undefined) continue;
  const parameters = [...(pathItem.parameters ?? []), ...(operation.parameters ?? [])];
  if (!parameters.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") ||
      operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== bodyLimit ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}` ||
      !operation.responses?.["200"]) {
    throw new Error(`${method.toUpperCase()} ${path} must retain its bounded idempotent Authoring command contract`);
  }
}
for (const schemaName of [
  "AuthoringEmptyCommand", "UpdateWorkingDraftRequest", "FreezeWorkingDraftRequest",
  "WorkingDraft", "WorkingDraftSummary", "WorkingDraftPage", "DocumentDraft", "DocumentDraftSummary", "DocumentDraftPage", "ArticleRevision", "PublicationBinding",
  "WorkingDraftCommandResult", "FreezeWorkingDraftResult", "PublishArticleRevisionResult",
  "DocumentDraftDetail", "OrganizingAvailability", "AuthoringOverview",
]) {
  if (schemas[schemaName]?.type !== "object" || schemas[schemaName]?.additionalProperties !== false) {
    throw new Error(`${schemaName} must retain an exact Authoring object shape`);
  }
}
if (schemas.AuthoringEmptyCommand.properties && Object.keys(schemas.AuthoringEmptyCommand.properties).length !== 0 ||
    schemas.UpdateWorkingDraftRequest.required?.join(",") !== "expected_version,title,target_path,body" ||
    schemas.UpdateWorkingDraftRequest.properties.expected_version.minimum !== 1 ||
    schemas.UpdateWorkingDraftRequest.properties.title["x-max-utf8-bytes"] !== 512 ||
    schemas.UpdateWorkingDraftRequest.properties.target_path["x-max-utf8-bytes"] !== 4096 ||
    schemas.UpdateWorkingDraftRequest.properties.body["x-max-utf8-bytes"] !== 10485760 ||
    schemas.FreezeWorkingDraftRequest.required?.join(",") !== "expected_version" ||
    schemas.WorkingDraft.required?.join(",") !== "id,workspace_id,document_id,title,target_path,body,status,version,created_at,updated_at" ||
    schemas.WorkingDraftSummary.properties.body !== undefined ||
    schemas.WorkingDraftPage.properties.items.items.$ref !== "#/components/schemas/WorkingDraftSummary" ||
    schemas.DocumentDraft.properties.lifecycle_status.enum?.join(",") !== "DRAFT,PUBLISHED,ARCHIVED,DELETED" ||
    schemas.DocumentDraftSummary.properties.lifecycle_status.const !== "DRAFT" ||
    schemas.DocumentDraftSummary.properties.current_published_revision_id.type !== "null" ||
    schemas.DocumentDraftPage.properties.items.items.$ref !== "#/components/schemas/DocumentDraftSummary" ||
    schemas.ArticleRevision.properties.content_hash.pattern !== "^[0-9a-f]{64}$" ||
    schemas.ArticleRevision.properties.status.enum?.join(",") !== "DRAFT,REVIEW,APPROVED,PUBLISHED,SUPERSEDED,ARCHIVED" ||
    schemas.PublicationBinding.properties.status.enum?.join(",") !== "PENDING,PUBLISHED,RECOVERY_REQUIRED,CLOSED" ||
    schemas.PublicationBinding.properties.proposal_href.pattern !== "^/proposals/[0-9a-f-]+$" ||
    schemas.AuthoringOverview.properties.recent_drafts.maxItems !== 100 ||
    schemas.AuthoringOverview.properties.pending_publications.maxItems !== 100 ||
    schemas.AuthoringOverview.properties.completed_documents.maxItems !== 100) {
  throw new Error("Authoring strict request, response, status, or bounded overview contract drifted");
}
for (const [path, pageSchema] of [
  ["/api/v1/workspaces/{workspace_id}/authoring/working-drafts", "WorkingDraftPage"],
  ["/api/v1/workspaces/{workspace_id}/authoring/documents", "DocumentDraftPage"],
]) {
  const parameters = document.paths[path].get.parameters;
  const cursor = parameters.find((parameter) => parameter.name === "cursor")?.schema;
  const limit = parameters.find((parameter) => parameter.name === "limit")?.schema;
  if (cursor?.maxLength !== 4096 || limit?.minimum !== 1 || limit?.maximum !== 100 || limit?.default !== 30 ||
      schemas[pageSchema].properties.items.maxItems !== 100 ||
      schemas[pageSchema].properties.next_cursor.minLength !== 1 || schemas[pageSchema].properties.next_cursor.maxLength !== 4096 ||
      schemas[pageSchema].required?.join(",") !== "workspace_id,items") {
    throw new Error(`${pageSchema} Authoring keyset pagination contract drifted`);
  }
}
const authoringPublishOperation = document.paths["/api/v1/workspaces/{workspace_id}/documents/{document_id}/revisions/{revision_id}/publish-proposals"].post;
for (const code of ["AUTHORING_TARGET_BASE_CONFLICT", "AUTHORING_PROPOSAL_BINDING_CONFLICT", "AUTHORING_REVISION_NOT_LATEST", "WRITEBACK_TARGET_PARENT_NOT_FOUND"]) {
  if (!authoringPublishOperation["x-error-codes"]?.includes(code)) {
    throw new Error(`missing Authoring publish error code: ${code}`);
  }
}
if (!authoringPublishOperation.description.includes("same key stably replays WRITEBACK_TARGET_PARENT_NOT_FOUND with 404")) {
  throw new Error("Authoring publish must document abandoned deterministic-failure replay semantics");
}
const workflowStatuses = ["pending", "running", "waiting_for_human", "retry_wait", "paused", "succeeded", "failed", "cancelled"];
const proposalStatuses = ["draft", "validating", "ready_for_review", "approved", "applying", "applied", "verifying", "completed", "rejected", "needs_revision", "deferred", "apply_failed", "verify_failed", "rolled_back", "cancelled"];
const proposalTypes = ["file_patch", "restore_document", "knowledge_change", "publish_artifact", "downstream_update"];
const proposalRiskLevels = ["CRITICAL", "HIGH", "MEDIUM", "LOW"];
const sourceSecurityStatuses = ["pending", "passed", "quarantined"];
const sourceIngestionStatuses = ["validating", "parsing", "parsed", "chunking", "chunked", "parse_failed", "cancelled"];
const sourceIndexStatuses = ["included", "excluded"];
const queryParameter = (path, name) => document.paths[path].get.parameters.find((parameter) => parameter.name === name)?.schema;
for (const [path, pageSchema, summarySchema] of [
  ["/api/v1/workspaces/{workspace_id}/source-versions", "SourceVersionPage", "SourceVersionSummary"],
  ["/api/v1/workspaces/{workspace_id}/proposals", "ProposalPage", "ProposalSummary"],
  ["/api/v1/workspaces/{workspace_id}/workflows", "WorkflowRunPage", "WorkflowRunSummary"],
]) {
  const cursor = queryParameter(path, "cursor");
  const limit = queryParameter(path, "limit");
  if (cursor?.maxLength !== 2048 || limit?.minimum !== 1 || limit?.maximum !== 100 || limit?.default !== 30) {
    throw new Error(`${path} cursor or limit contract drifted`);
  }
  if (schemas[pageSchema].properties.items.maxItems !== 100 ||
      schemas[pageSchema].properties.items.items.$ref !== `#/components/schemas/${summarySchema}` ||
      schemas[pageSchema].properties.next_cursor.minLength !== 1 ||
      schemas[pageSchema].properties.next_cursor.maxLength !== 2048 ||
      schemas[pageSchema].type !== "object" || schemas[pageSchema].additionalProperties !== false ||
      schemas[pageSchema].required?.join(",") !== "items") {
    throw new Error(`${pageSchema} pagination schema drifted`);
  }
}
if (queryParameter("/api/v1/workspaces/{workspace_id}/proposals", "status")?.enum?.join(",") !== proposalStatuses.join(",") ||
    schemas.ProposalSummary.properties.status.enum?.join(",") !== proposalStatuses.join(",")) {
  throw new Error("Proposal list status enum drifted from the domain contract");
}
if (queryParameter("/api/v1/workspaces/{workspace_id}/proposals", "proposal_type")?.enum?.join(",") !== proposalTypes.join(",") ||
    schemas.ProposalSummary.properties.proposal_type.enum?.join(",") !== proposalTypes.join(",")) {
  throw new Error("Proposal list type enum drifted from the typed Proposal contract");
}
const documentHistoryOperations = [
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/history", "get", ["200"], "DocumentHistoryPage", "READ_LOCAL", false],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/history/compare", "get", ["200"], "DocumentHistoryCompare", "READ_LOCAL", false],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-previews", "post", ["200"], "DocumentRestorePreview", "READ_LOCAL", true],
  ["/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-proposals", "post", ["200", "201"], "DocumentRestoreProposalResult", "WRITE_PROPOSAL", true],
];
for (const [path, method, successStatuses, successSchema, capability, acceptsJSON] of documentHistoryOperations) {
  const pathItem = document.paths[path];
  const operation = pathItem?.[method];
  if (!operation || operation.security !== undefined || operation["x-required-capability"] !== capability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${capability}`);
  }
  const pathParameters = pathItem.parameters ?? [];
  for (const name of ["workspace_id", "document_id"]) {
    const parameter = pathParameters.find((candidate) => candidate.name === name);
    if (parameter?.in !== "path" || parameter.required !== true || parameter.schema?.type !== "string" || parameter.schema?.format !== "uuid") {
      throw new Error(`${method.toUpperCase()} ${path} must require UUID ${name}`);
    }
  }
  for (const status of successStatuses) {
    const response = operation.responses?.[status];
    if (response?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}` ||
        response.headers?.["Cache-Control"]?.schema?.const !== "private, no-store") {
      throw new Error(`invalid Document History ${status} response for ${method.toUpperCase()} ${path}`);
    }
  }
  const errorStatuses = acceptsJSON
    ? ["400", "401", "403", "404", "405", "409", "413", "415", "500", "503"]
    : ["400", "401", "403", "404", "405", "409", "413", "500", "503"];
  for (const status of errorStatuses) {
    if (resolveRef(operation.responses?.[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Document History ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
const historyPath = "/api/v1/workspaces/{workspace_id}/documents/{document_id}/history";
const comparePath = `${historyPath}/compare`;
const previewPath = "/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-previews";
const restoreProposalPath = "/api/v1/workspaces/{workspace_id}/documents/{document_id}/restore-proposals";
const historyQuery = (name) => document.paths[historyPath].get.parameters.find((parameter) => parameter.name === name)?.schema;
const compareQuery = (name) => document.paths[comparePath].get.parameters.find((parameter) => parameter.name === name)?.schema;
if (historyQuery("limit")?.minimum !== 1 || historyQuery("limit")?.maximum !== 50 || historyQuery("limit")?.default !== 30 ||
    historyQuery("cursor")?.maxLength !== 4096 ||
    compareQuery("left")?.$ref !== "#/components/schemas/DocumentHistoryVersionRef" ||
    compareQuery("right")?.$ref !== "#/components/schemas/DocumentHistoryVersionRef") {
  throw new Error("Document History query bounds or version reference contract drifted");
}
for (const [path, requestSchema, idempotent] of [
  [previewPath, "DocumentRestorePreviewRequest", false],
  [restoreProposalPath, "DocumentRestoreProposalRequest", true],
]) {
  const operation = document.paths[path].post;
  const parameters = operation.parameters ?? [];
  if (operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== 16384 ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}` ||
      parameters.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") !== idempotent) {
    throw new Error(`POST ${path} must retain its strict bounded${idempotent ? " idempotent" : ""} command contract`);
  }
}
for (const schemaName of [
  "DocumentHistoryExternalEntry", "DocumentHistoryManagedEntry", "DocumentHistoryCurrentEntry", "DocumentHistoryPage",
  "DocumentHistoryCompare", "DocumentRestorePreviewRequest", "DocumentRestorePreview", "DocumentRestoreProposalRequest",
  "DocumentRestoreProposalResult", "RestoreDocumentBinding", "RestoreDocumentRevision", "RestoreDocumentProposal",
]) {
  if (schemas[schemaName]?.type !== "object" || schemas[schemaName]?.additionalProperties !== false) {
    throw new Error(`${schemaName} must retain an exact Document History object shape`);
  }
}
const historyEntryRefs = schemas.DocumentHistoryEntry?.oneOf?.map((entry) => entry.$ref) ?? [];
const proposalRefs = schemas.Proposal?.oneOf?.map((entry) => entry.$ref) ?? [];
if (historyEntryRefs.join(",") !== "#/components/schemas/DocumentHistoryManagedEntry,#/components/schemas/DocumentHistoryExternalEntry,#/components/schemas/DocumentHistoryCurrentEntry" ||
    schemas.DocumentHistoryEntry?.discriminator?.propertyName !== "kind" ||
    schemas.DocumentHistoryPage.properties.items.maxItems !== 51 ||
    schemas.DocumentHistoryPage.properties.items.items.$ref !== "#/components/schemas/DocumentHistoryEntry" ||
    schemas.DocumentHistoryPage.properties.next_cursor.maxLength !== 4096 ||
    schemas.DocumentHistoryCompare.properties.left_content["x-max-utf8-bytes"] !== 10485760 ||
    schemas.DocumentHistoryCompare.properties.patch["x-max-utf8-bytes"] !== 2097152 ||
    schemas.DocumentRestorePreview.properties.target_content["x-max-utf8-bytes"] !== 10485760 ||
    schemas.DocumentRestorePreview.properties.patch["x-max-utf8-bytes"] !== 2097152 ||
    schemas.DocumentHistoryObjectID.pattern !== "^(?:[0-9a-f]{40}|[0-9a-f]{64})$" ||
    schemas.DocumentHistoryVersionRef.pattern !== "^(?:WORKTREE|[0-9a-f]{40}|[0-9a-f]{64})$") {
  throw new Error("Document History discriminated timeline or bounded content contract drifted");
}
if (!proposalRefs.includes("#/components/schemas/RestoreDocumentProposal") ||
    schemas.Proposal.discriminator.mapping.restore_document !== "#/components/schemas/RestoreDocumentProposal" ||
    schemas.RestoreDocumentProposal.properties.proposal_type.const !== "restore_document" ||
    schemas.RestoreDocumentProposal.properties.risk_level.const !== "HIGH" ||
    schemas.RestoreDocumentProposal.properties.revision.$ref !== "#/components/schemas/RestoreDocumentRevision" ||
    schemas.RestoreDocumentRevision.properties.target_mode.const !== "REPLACE" ||
    schemas.RestoreDocumentRevision.properties.restore.$ref !== "#/components/schemas/RestoreDocumentBinding" ||
    schemas.RestoreDocumentBinding.properties.schema_version.const !== "document-restore/v1" ||
    schemas.DocumentRestoreProposalResult.properties.status.enum?.join(",") !== proposalStatuses.join(",")) {
  throw new Error("restore_document Proposal provenance or replay status contract drifted");
}

const expectedGitSyncPaths = [
  "/api/v1/workspaces/{workspace_id}/git-remote",
  "/api/v1/workspaces/{workspace_id}/git-remote/tests",
  "/api/v1/workspaces/{workspace_id}/git-sync",
  "/api/v1/workspaces/{workspace_id}/git-sync/runs",
  "/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}",
  "/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}/retries",
];
const actualGitSyncPaths = Object.keys(document.paths)
  .filter((path) => path.includes("/git-remote") || path.includes("/git-sync"))
  .sort();
if (actualGitSyncPaths.join(",") !== expectedGitSyncPaths.slice().sort().join(",")) {
  throw new Error("Git Sync path inventory drifted from the HTTP Router");
}
const expectedGitSyncRouteRegistrations = [
  "DELETE /workspaces/{workspace_id}/git-remote handler.removeConfig",
  "GET /workspaces/{workspace_id}/git-remote handler.getConfig",
  "GET /workspaces/{workspace_id}/git-sync handler.getStatus",
  "GET /workspaces/{workspace_id}/git-sync/runs handler.listRuns",
  "GET /workspaces/{workspace_id}/git-sync/runs/{run_id} handler.getRun",
  "POST /workspaces/{workspace_id}/git-remote/tests handler.testConfig",
  "POST /workspaces/{workspace_id}/git-sync/runs handler.createRun",
  "POST /workspaces/{workspace_id}/git-sync/runs/{run_id}/retries handler.retryRun",
  "PUT /workspaces/{workspace_id}/git-remote handler.saveConfig",
].sort();
const actualGitSyncRouteRegistrations = [...gitSyncHandlerSource.matchAll(/router\.(GET|PUT|POST|DELETE)\(\s*"([^"]+)"\s*,\s*httpapi\.GinHandler\(handler\.([A-Za-z0-9_]+)\)\s*\)/g)]
  .map((match) => `${match[1]} ${canonicalGinRoute(match[2])} handler.${match[3]}`)
  .sort();
if (actualGitSyncRouteRegistrations.join(",") !== expectedGitSyncRouteRegistrations.join(",")) {
  throw new Error("Git Sync HTTP Router must register exactly the documented operations");
}
const gitSyncCapabilityNames = new Map([
  ["ReadLocal", "READ_LOCAL"],
  ["GitWrite", "GIT_WRITE"],
]);
const actualGitSyncCapabilities = new Map(
  [...authHandlerSource.matchAll(/oneCapability\(http\.Method(Get|Put|Post|Delete),\s*"([^"]+)",\s*capability\.([A-Za-z0-9]+)\)/g)]
    .filter((match) => match[2].includes("/git-remote") || match[2].includes("/git-sync"))
    .map((match) => {
      return [`${match[1].toUpperCase()} ${match[2]}`, gitSyncCapabilityNames.get(match[3])];
    }),
);
if (actualGitSyncCapabilities.size !== 9 || [...actualGitSyncCapabilities.values()].some((capability) => capability === undefined)) {
  throw new Error("Git Sync Auth capability route inventory is incomplete or unsupported");
}

const gitSyncOperations = [
  {
    path: "/api/v1/workspaces/{workspace_id}/git-remote", method: "get", operationId: "getGitRemoteConfig",
    statuses: ["200", "400", "401", "403", "405", "409", "500", "503"], successStatuses: ["200"],
    schemaName: "GitRemoteConfig", capability: "READ_LOCAL", requestSchema: null, idempotent: false,
    errorCodes: ["GIT_SYNC_INVALID", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-remote", method: "put", operationId: "saveGitRemoteConfig",
    statuses: ["200", "201", "400", "401", "403", "404", "405", "409", "415", "500", "503"], successStatuses: ["200", "201"],
    schemaName: "GitRemoteConfig", capability: "GIT_WRITE", requestSchema: "SaveGitRemoteConfigRequest", idempotent: true,
    errorCodes: ["GIT_SYNC_INVALID", "INVALID_JSON", "UNSUPPORTED_MEDIA_TYPE", "GIT_REMOTE_SECRET_ACTION_INVALID", "GIT_REMOTE_SECRET_UNAVAILABLE", "GIT_REMOTE_URL_INVALID", "GIT_REMOTE_BRANCH_INVALID", "GIT_SYNC_OFFLINE", "GIT_SYNC_IDEMPOTENCY_CONFLICT", "GIT_REMOTE_REVISION_CONFLICT", "GIT_SYNC_RUN_ACTIVE", "GIT_SYNC_RUN_NOT_FOUND", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-remote", method: "delete", operationId: "removeGitRemoteConfig",
    statuses: ["200", "400", "401", "403", "405", "409", "415", "500", "503"], successStatuses: ["200"],
    schemaName: "GitRemoteConfig", capability: "GIT_WRITE", requestSchema: "RemoveGitRemoteConfigRequest", idempotent: true,
    errorCodes: ["GIT_SYNC_INVALID", "INVALID_JSON", "UNSUPPORTED_MEDIA_TYPE", "GIT_SYNC_IDEMPOTENCY_CONFLICT", "GIT_REMOTE_REVISION_CONFLICT", "GIT_SYNC_RUN_ACTIVE", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-remote/tests", method: "post", operationId: "testGitRemoteConfig",
    statuses: ["200", "400", "401", "403", "405", "409", "415", "500", "503"], successStatuses: ["200"],
    schemaName: "GitRemoteTestResult", capability: "GIT_WRITE", requestSchema: "TestGitRemoteConfigRequest", idempotent: false,
    errorCodes: ["GIT_SYNC_INVALID", "INVALID_JSON", "UNSUPPORTED_MEDIA_TYPE", "GIT_REMOTE_SECRET_ACTION_INVALID", "GIT_REMOTE_SECRET_UNAVAILABLE", "GIT_REMOTE_URL_INVALID", "GIT_REMOTE_BRANCH_INVALID", "GIT_REMOTE_REVISION_CONFLICT", "GIT_SYNC_CONFIG_STALE", "GIT_SYNC_REF_DRIFT", "GIT_SYNC_AUTHENTICATION_FAILED", "GIT_SYNC_OFFLINE", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-sync", method: "get", operationId: "getGitSyncStatus",
    statuses: ["200", "400", "401", "403", "405", "409", "500", "503"], successStatuses: ["200"],
    schemaName: "GitSyncStatus", capability: "READ_LOCAL", requestSchema: null, idempotent: false,
    errorCodes: ["GIT_SYNC_INVALID", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-sync/runs", method: "get", operationId: "listGitSyncRuns",
    statuses: ["200", "400", "401", "403", "405", "409", "500", "503"], successStatuses: ["200"],
    schemaName: "GitSyncRunPage", capability: "READ_LOCAL", requestSchema: null, idempotent: false,
    errorCodes: ["GIT_SYNC_INVALID", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-sync/runs", method: "post", operationId: "createGitSyncRun",
    statuses: ["200", "202", "400", "401", "403", "404", "405", "409", "500", "503"], successStatuses: ["200", "202"],
    schemaName: "GitSyncRun", capability: "GIT_WRITE", requestSchema: null, idempotent: true,
    errorCodes: ["GIT_SYNC_INVALID", "GIT_REMOTE_NOT_CONFIGURED", "GIT_REMOTE_SECRET_UNAVAILABLE", "GIT_SYNC_CONFIG_STALE", "GIT_SYNC_IDEMPOTENCY_CONFLICT", "GIT_SYNC_RUN_ACTIVE", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}", method: "get", operationId: "getGitSyncRun",
    statuses: ["200", "400", "401", "403", "404", "405", "409", "500", "503"], successStatuses: ["200"],
    schemaName: "GitSyncRun", capability: "READ_LOCAL", requestSchema: null, idempotent: false,
    errorCodes: ["GIT_SYNC_INVALID", "GIT_SYNC_RUN_NOT_FOUND", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
  {
    path: "/api/v1/workspaces/{workspace_id}/git-sync/runs/{run_id}/retries", method: "post", operationId: "retryGitSyncRun",
    statuses: ["200", "202", "400", "401", "403", "404", "405", "409", "415", "500", "503"], successStatuses: ["200", "202"],
    schemaName: "GitSyncRun", capability: "GIT_WRITE", requestSchema: "RetryGitSyncRunRequest", idempotent: true,
    errorCodes: ["GIT_SYNC_INVALID", "INVALID_JSON", "UNSUPPORTED_MEDIA_TYPE", "GIT_SYNC_RUN_NOT_FOUND", "GIT_SYNC_RUN_TRANSITION_CONFLICT", "GIT_SYNC_IDEMPOTENCY_CONFLICT", "GIT_REMOTE_NOT_CONFIGURED", "GIT_REMOTE_SECRET_UNAVAILABLE", "GIT_SYNC_CONFIG_STALE", "GIT_SYNC_RUN_ACTIVE", "GIT_SYNC_UNAVAILABLE", "GIT_SYNC_STATE_CORRUPT", "GIT_SYNC_HTTP_UNAVAILABLE", "INTERNAL_ERROR"],
  },
];
const resolveParameter = (parameter) => {
  if (!parameter?.$ref) return parameter;
  const name = parameter.$ref.split("/").at(-1);
  return document.components?.parameters?.[name];
};
for (const { path, method, operationId, statuses, successStatuses, schemaName, capability, requestSchema, idempotent, errorCodes } of gitSyncOperations) {
  const pathItem = document.paths[path];
  const operation = pathItem?.[method];
  if (!operation || operation.operationId !== operationId || operation.security !== undefined || operation["x-required-capability"] !== capability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${capability}`);
  }
  if (actualGitSyncCapabilities.get(`${method.toUpperCase()} ${path}`) !== capability) {
    throw new Error(`${method.toUpperCase()} ${path} OpenAPI capability drifted from Auth middleware`);
  }
  if (Object.keys(operation.responses ?? {}).sort().join(",") !== statuses.slice().sort().join(",") ||
      operation["x-error-codes"]?.join(",") !== errorCodes.join(",")) {
    throw new Error(`${method.toUpperCase()} ${path} response status or stable error-code contract drifted`);
  }
  for (const parameter of pathItem.parameters ?? []) {
    const resolved = resolveParameter(parameter);
    if (resolved?.name === "workspace_id" && (resolved.in !== "path" || resolved.required !== true || resolved.schema?.format !== "uuid")) {
      throw new Error(`${method.toUpperCase()} ${path} must bind a UUID workspace_id`);
    }
    if (resolved?.name === "run_id" && (resolved.in !== "path" || resolved.required !== true || resolved.schema?.format !== "uuid")) {
      throw new Error(`${method.toUpperCase()} ${path} must bind a UUID run_id`);
    }
  }
  if (!(pathItem.parameters ?? []).some((parameter) => resolveParameter(parameter)?.name === "workspace_id")) {
    throw new Error(`${method.toUpperCase()} ${path} must bind workspace_id`);
  }
  const hasRunID = (pathItem.parameters ?? []).some((parameter) => resolveParameter(parameter)?.name === "run_id");
  if (hasRunID !== path.includes("{run_id}")) {
    throw new Error(`${method.toUpperCase()} ${path} run_id binding drifted`);
  }
  for (const status of successStatuses) {
    const response = operation.responses?.[status];
    if (response?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${schemaName}` ||
        response.headers?.["Cache-Control"]?.schema?.const !== "private, no-store") {
      throw new Error(`invalid Git Sync ${status} response for ${method.toUpperCase()} ${path}`);
    }
  }
  for (const status of statuses.filter((status) => Number(status) >= 400)) {
    if (resolveRef(operation.responses?.[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Git Sync ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
  if (requestSchema === null) {
    if (operation.requestBody !== undefined) {
      throw new Error(`${method.toUpperCase()} ${path} must not define a request body`);
    }
  } else if (operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== 32768 ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}` ||
      Object.keys(operation.requestBody?.content ?? {}).join(",") !== "application/json") {
    throw new Error(`${method.toUpperCase()} ${path} must require its strict bounded JSON command body`);
  }
  const hasIdempotencyKey = (operation.parameters ?? []).some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey");
  if (hasIdempotencyKey !== idempotent) {
    throw new Error(`${method.toUpperCase()} ${path} Idempotency-Key contract drifted`);
  }
}
if (schemas.SaveGitRemoteConfigRequest.required?.join(",") !== "expected_revision,remote_url,branch,auto_sync,token" ||
    schemas.TestGitRemoteConfigRequest.required?.join(",") !== "expected_revision,remote_url,branch,token" ||
    schemas.RemoveGitRemoteConfigRequest.required?.join(",") !== "expected_revision" ||
    schemas.RemoveGitRemoteConfigRequest.properties?.expected_revision?.minimum !== 1 ||
    schemas.RetryGitSyncRunRequest.required?.join(",") !== "expected_version" ||
    schemas.RetryGitSyncRunRequest.properties?.expected_version?.minimum !== 1 ||
    schemas.TestGitRemoteConfigRequest.properties?.token?.$ref !== "#/components/schemas/GitSyncTestTokenAction") {
  throw new Error("Git Sync CAS request contracts drifted");
}
for (const schemaName of [
  "GitSyncKeepTokenAction", "GitSyncReplaceTokenAction", "GitSyncClearTokenAction", "SaveGitRemoteConfigRequest",
  "RemoveGitRemoteConfigRequest", "TestGitRemoteConfigRequest", "RetryGitSyncRunRequest", "GitRemoteConfig",
  "GitRemoteTestResult", "GitSyncFileChange", "GitSyncRun", "GitSyncStatus", "GitSyncRunPage",
]) {
  if (schemas[schemaName]?.type !== "object" || schemas[schemaName]?.additionalProperties !== false) {
    throw new Error(`${schemaName} must remain a strict Git Sync object shape`);
  }
}
for (const schemaName of ["GitRemoteConfig", "GitRemoteTestResult", "GitSyncFileChange", "GitSyncRun", "GitSyncStatus", "GitSyncRunPage"]) {
  if (schemas[schemaName].properties?.token !== undefined) {
    throw new Error(`${schemaName} must remain credential-free`);
  }
}
const gitSyncResponseShapes = [
  [
    "GitRemoteConfig",
    ["workspace_id", "configured", "remote_url", "branch", "auto_sync", "token_configured", "revision", "created_at", "updated_at", "replayed"],
    ["workspace_id", "configured", "remote_url", "branch", "auto_sync", "token_configured", "revision", "created_at", "updated_at"],
  ],
  ["GitRemoteTestResult", ["status", "remote_url", "branch"], ["status", "remote_url", "branch"]],
  ["GitSyncFileChange", ["path", "old_path", "kind"], ["path", "old_path", "kind"]],
  [
    "GitSyncRun",
    ["id", "workspace_id", "config_revision", "remote_url", "branch", "trigger", "retry_of_run_id", "status", "direction", "failure_class", "error_code", "retryable", "expected_head_oid", "expected_remote_oid", "verified_head_oid", "verified_remote_oid", "changed_files", "index_status", "index_error_code", "index_retryable", "index_version_id", "attempt_count", "version", "created_at", "updated_at", "completed_at", "replayed"],
    ["id", "workspace_id", "config_revision", "remote_url", "branch", "trigger", "retry_of_run_id", "status", "direction", "failure_class", "error_code", "retryable", "expected_head_oid", "expected_remote_oid", "verified_head_oid", "verified_remote_oid", "changed_files", "index_status", "index_error_code", "index_retryable", "index_version_id", "attempt_count", "version", "created_at", "updated_at", "completed_at"],
  ],
  ["GitSyncStatus", ["config", "current_run"], ["config", "current_run"]],
  ["GitSyncRunPage", ["items", "next_cursor"], ["items"]],
];
for (const [schemaName, properties, required] of gitSyncResponseShapes) {
  if (Object.keys(schemas[schemaName].properties ?? {}).join(",") !== properties.join(",") ||
      schemas[schemaName].required?.join(",") !== required.join(",")) {
    throw new Error(`${schemaName} response fields drifted from the credential-free HTTP DTO`);
  }
}
const nullableOIDRef = (property) => property?.oneOf?.[0]?.$ref === "#/components/schemas/GitSyncOID" &&
  property.oneOf?.[1]?.type === "null";
if (schemas.GitRemoteConfig.properties?.token_configured?.type !== "boolean" ||
    schemas.GitRemoteConfig.properties?.remote_url?.oneOf?.[0]?.$ref !== "#/components/schemas/GitSyncHTTPSRemoteURL" ||
    schemas.GitRemoteConfig.properties?.remote_url?.oneOf?.[1]?.type !== "null" ||
    schemas.GitSyncRunPage.properties?.items?.maxItems !== 100 ||
    schemas.GitSyncRunPage.properties?.next_cursor?.maxLength !== 2048 ||
    schemas.GitSyncRun.properties?.changed_files?.maxItems !== 500 ||
    !["expected_head_oid", "expected_remote_oid", "verified_head_oid", "verified_remote_oid"].every((name) => nullableOIDRef(schemas.GitSyncRun.properties?.[name])) ||
    schemas.GitSyncTokenAction?.oneOf?.length !== 3 || schemas.GitSyncTestTokenAction?.oneOf?.length !== 2 ||
    schemas.GitSyncTokenAction.oneOf.map((entry) => entry.$ref).join(",") !== "#/components/schemas/GitSyncKeepTokenAction,#/components/schemas/GitSyncReplaceTokenAction,#/components/schemas/GitSyncClearTokenAction" ||
    schemas.GitSyncTestTokenAction.oneOf.map((entry) => entry.$ref).join(",") !== "#/components/schemas/GitSyncKeepTokenAction,#/components/schemas/GitSyncReplaceTokenAction" ||
    schemas.GitSyncReplaceTokenAction.properties?.value?.writeOnly !== true ||
    schemas.GitSyncReplaceTokenAction.properties?.value?.["x-max-utf8-bytes"] !== 16384 ||
    schemas.GitSyncHTTPSRemoteInput?.["x-max-utf8-bytes"] !== 2048 ||
    document.components.parameters.GitSyncCursor.schema?.maxLength !== 2048 ||
    document.components.parameters.GitSyncLimit.schema?.minimum !== 1 ||
    document.components.parameters.GitSyncLimit.schema?.maximum !== 100 ||
    document.components.parameters.GitSyncLimit.schema?.default !== 30 ||
    schemas.GitSyncOID?.pattern !== "^(?:[0-9a-f]{40}|[0-9a-f]{64})$") {
  throw new Error("Git Sync credential masking or bounded response contract drifted");
}
for (const code of ["DOCUMENT_RESTORE_STALE", "DOCUMENT_RESTORE_DIRTY_WORKTREE", "IDEMPOTENCY_KEY_REUSED"]) {
  if (!document.paths[restoreProposalPath].post["x-error-codes"]?.includes(code)) {
    throw new Error(`missing Document restore Proposal error code: ${code}`);
  }
}
if (schemas.ProposalRiskLevel.type !== "string" || schemas.ProposalRiskLevel.enum?.join(",") !== proposalRiskLevels.join(",") ||
    queryParameter("/api/v1/workspaces/{workspace_id}/proposals", "risk")?.$ref !== "#/components/schemas/ProposalRiskLevel") {
  throw new Error("Proposal risk query must use the frozen uppercase ProposalRiskLevel contract");
}
const createProposalRequired = ["target_path", "base_hash", "content", "evidence_summary", "risk_level", "risk", "rollback_plan"];
if (schemas.CreateProposalRequest.required?.join(",") !== createProposalRequired.join(",") ||
    schemas.CreateProposalRequest.properties.risk_level?.$ref !== "#/components/schemas/ProposalRiskLevel") {
  throw new Error("CreateProposalRequest must require the explicit Proposal-level risk contract");
}
for (const schemaName of ["ProposalSummary", "FilePatchProposal"]) {
  if (!schemas[schemaName].required.includes("risk_level") ||
      schemas[schemaName].properties.risk_level?.$ref !== "#/components/schemas/ProposalRiskLevel") {
    throw new Error(`${schemaName} must require top-level risk_level`);
  }
}
if (!schemas.KnowledgeChangeProposal.required.includes("risk_level") ||
    schemas.KnowledgeChangeProposal.properties.risk_level?.const !== "HIGH" ||
    schemas.KnowledgeChangeRevision.properties.base_versions?.uniqueItems !== true) {
  throw new Error("KnowledgeChangeProposal must be HIGH and expose unique endpoint base versions");
}
if (!schemas.PublishArtifactProposal.required.includes("risk_level") ||
    schemas.PublishArtifactProposal.properties.risk_level?.const !== "HIGH") {
  throw new Error("PublishArtifactProposal must retain its HIGH risk boundary");
}
if (!schemas.DownstreamUpdateProposal.required.includes("risk_level") ||
    schemas.DownstreamUpdateProposal.properties.risk_level?.const !== "HIGH") {
  throw new Error("DownstreamUpdateProposal must retain its HIGH risk boundary");
}
if (!schemas.ProposalSummary.required.includes("risk") || schemas.ProposalSummary.properties.risk?.type !== "string") {
  throw new Error("ProposalSummary must retain the human-readable risk description");
}
for (const schemaName of ["ProposalRevision", "KnowledgeChangeRevision", "PublishArtifactRevision", "DownstreamUpdateRevision"]) {
  if (!schemas[schemaName].required.includes("risk") || schemas[schemaName].properties.risk?.type !== "string" ||
      schemas[schemaName].properties.risk_level !== undefined) {
    throw new Error(`${schemaName} must retain risk as description without owning risk_level`);
  }
}
if (queryParameter("/api/v1/workspaces/{workspace_id}/workflows", "status")?.enum?.join(",") !== workflowStatuses.join(",") ||
    schemas.WorkflowRunSummary.properties.status.enum?.join(",") !== workflowStatuses.join(",")) {
  throw new Error("Workflow list status enum drifted from the domain contract");
}
for (const [field, expected] of [
  ["security_status", sourceSecurityStatuses],
  ["ingestion_status", sourceIngestionStatuses],
  ["workflow_status", workflowStatuses],
  ["index_status", sourceIndexStatuses],
]) {
  if (queryParameter("/api/v1/workspaces/{workspace_id}/source-versions", field)?.enum?.join(",") !== expected.join(",") ||
      schemas.SourceVersionSummary.properties[field].enum?.join(",") !== expected.join(",")) {
    throw new Error(`Source Version list ${field} enum drifted from the domain contract`);
  }
}
if (schemas.EvidenceSourceVersion.properties.security_status.enum?.join(",") !== sourceSecurityStatuses.join(",") ||
    schemas.EvidenceSourceVersion.properties.ingestion_status.enum?.join(",") !== sourceIngestionStatuses.join(",") ||
    schemas.EvidenceSourceVersion.properties.workflow_status.enum?.join(",") !== workflowStatuses.join(",") ||
    schemas.EvidenceSourceVersion.properties.index_status.enum?.join(",") !== sourceIndexStatuses.join(",")) {
  throw new Error("Source Version detail status projections drifted from the handler contract");
}
const approvalRequired = ["id", "proposal_id", "revision_id", "change_hash", "decision", "decided_at"];
if (schemas.ProposalSummary.properties.approval?.$ref !== "#/components/schemas/Approval" ||
    approvalRequired.some((field) => !schemas.Approval.required.includes(field)) ||
    approvalRequired.some((field) => !schemas.ApprovalDecisionResponse.required.includes(field)) ||
    schemas.Approval.properties.workflow_run_id?.format !== "uuid" ||
    schemas.Approval.properties.workflow_status_url?.pattern !== "^/api/v1/workflows/[0-9a-f-]{36}$" ||
    schemas.Approval.properties.dispatch_status ||
    schemas.ApprovalDecisionResponse.properties.approved_git_head?.pattern !== "^([0-9a-f]{40}|[0-9a-f]{64})$" ||
    schemas.ApprovalDecisionResponse.properties.workflow_run_id?.format !== "uuid" ||
    schemas.ApprovalDecisionResponse.properties.workflow_status_url?.pattern !== "^/api/v1/workflows/[0-9a-f-]{36}$" ||
    schemas.ApprovalDecisionResponse.properties.dispatch_status?.enum?.join(",") !== "queued,running,replayed") {
  throw new Error("Proposal Approval snapshot and decision response contracts are not separated");
}
if (!schemas.FilePatchProposal.required.includes("approval") ||
    schemas.FilePatchProposal.properties.approval.oneOf?.[0]?.$ref !== "#/components/schemas/Approval" ||
    schemas.FilePatchProposal.properties.approval.oneOf?.[1]?.type !== "null") {
  throw new Error("FilePatchProposal must require a nullable persistent Approval snapshot");
}
const nonFileApprovalForbiddenFields = ["approved_git_head", "workflow_run_id", "workflow_status_url"];
if (schemas.NonFileApproval.allOf?.[0]?.$ref !== "#/components/schemas/Approval" ||
    schemas.NonFileApproval.allOf?.[1]?.not?.anyOf?.map((item) => item.required?.join(",")).join(",") !== nonFileApprovalForbiddenFields.join(",") ||
    schemas.ProposalSummary.allOf?.[0]?.if?.properties?.proposal_type?.enum?.join(",") !== "knowledge_change,publish_artifact,downstream_update" ||
    schemas.ProposalSummary.allOf?.[0]?.then?.properties?.approval?.$ref !== "#/components/schemas/NonFileApproval") {
  throw new Error("Non-file Proposal summaries must forbid Git and Workflow Approval fields");
}
for (const schemaName of ["KnowledgeChangeProposal", "PublishArtifactProposal", "DownstreamUpdateProposal"]) {
  if (!schemas[schemaName].required.includes("approval") ||
      schemas[schemaName].properties.approval.oneOf?.[0]?.$ref !== "#/components/schemas/NonFileApproval" ||
      schemas[schemaName].properties.approval.oneOf?.[1]?.type !== "null") {
    throw new Error(`${schemaName} must require a nullable non-file Approval snapshot`);
  }
}
const currentContent = schemas.ProposalCurrentContent;
const targetBasePattern = "^(?:[0-9a-f]{64}|workspace-target-absent/v1:[0-9a-f]{64})$";
const zeroAbsenceToken = `workspace-target-absent/v1:${"0".repeat(64)}`;
const targetModes = "REPLACE,CREATE_ONLY";
for (const field of ["proposal_id", "workspace_id", "target_path", "target_mode", "content", "current_hash", "base_hash", "base_hash_match"]) {
  if (!currentContent.required.includes(field)) throw new Error(`ProposalCurrentContent must require ${field}`);
}
if (currentContent.additionalProperties !== false || currentContent.properties.content.maxLength !== 1048576 ||
    currentContent.properties.proposal_id.format !== "uuid" || currentContent.properties.workspace_id.format !== "uuid" ||
    currentContent.properties.target_path.minLength !== 1 ||
    currentContent.properties.target_mode.enum?.join(",") !== targetModes ||
    currentContent.properties.current_hash.pattern !== targetBasePattern || currentContent.properties.base_hash.pattern !== targetBasePattern ||
    currentContent.properties.current_hash.not?.const !== zeroAbsenceToken || currentContent.properties.base_hash.not?.const !== zeroAbsenceToken ||
    currentContent.properties.base_hash_match.type !== "boolean" ||
    currentContent["x-invariant"] !== "base_hash_match == (current_hash == base_hash); CREATE_ONLY requires empty content and matching non-zero workspace-target-absent/v1 tokens." ||
    document.paths["/api/v1/proposals/{proposal_id}/current-content"].get.responses["200"].headers?.["Cache-Control"]?.schema?.const !== "private, no-store") {
  throw new Error("Proposal current-content body or no-store contract drifted");
}
for (const schemaName of ["ProposalRevision", "ApplyPreflightResult"]) {
  const schema = schemas[schemaName];
  if (!schema.required.includes("target_mode") || schema.properties.target_mode.enum?.join(",") !== targetModes ||
      schema.properties.base_hash.pattern !== targetBasePattern || schema.properties.base_hash.not?.const !== zeroAbsenceToken) {
    throw new Error(`${schemaName} must bind REPLACE and CREATE_ONLY target baselines`);
  }
}
if (!schemas.SystemStatus.required.includes("rag") || schemas.SystemStatus.properties.rag.$ref !== "#/components/schemas/RAGCapabilityStatus" ||
    schemas.RAGCapabilityStatus.additionalProperties !== false) {
  throw new Error("SystemStatus must expose the strict RAG capability state");
}
if (!schemas.SystemStatus.required.includes("graph") || schemas.SystemStatus.properties.graph.$ref !== "#/components/schemas/GraphCapabilityStatus" ||
    schemas.GraphCapabilityStatus.additionalProperties !== false ||
    !schemas.GraphCapabilityStatus.properties.status.enum.includes("ready") ||
    !schemas.GraphCapabilityStatus.properties.status.enum.includes("unavailable") ||
    !schemas.GraphCapabilityStatus.properties.reason.enum.includes("graph_dependencies_unavailable")) {
  throw new Error("SystemStatus must expose the strict Graph capability state");
}
if (!schemas.SystemStatus.required.includes("semantic_links") || schemas.SystemStatus.properties.semantic_links.$ref !== "#/components/schemas/SemanticLinkCapabilityStatus" ||
    schemas.SemanticLinkCapabilityStatus.additionalProperties !== false) {
  throw new Error("SystemStatus must expose the strict Semantic Link capability state");
}
if (!schemas.SystemStatus.required.includes("auth") || schemas.SystemStatus.properties.auth.$ref !== "#/components/schemas/AuthCapabilityStatus" ||
    schemas.AuthCapabilityStatus.properties.status.enum.join(",") !== "disabled,ready,unavailable") {
  throw new Error("SystemStatus must expose the strict authentication capability state");
}
if (schemas.AuthCapability.enum?.join(",") !== "READ_LOCAL,READ_EXTERNAL,WRITE_PROPOSAL,WRITE_KNOWLEDGE,GIT_WRITE,INDEX_MAINTENANCE,EVALUATION_RUN,MANAGE_SYSTEM_SETTINGS") {
  throw new Error("AuthCapability must remain aligned with the canonical capability catalog");
}
if (!schemas.SystemStatus.required.includes("knowledge_timeline") ||
    schemas.SystemStatus.properties.knowledge_timeline.$ref !== "#/components/schemas/OptionalCapabilityStatus") {
  throw new Error("SystemStatus must expose the Knowledge Timeline capability state");
}
for (const capabilityName of ["review", "memory", "interview", "authoring", "capture", "organizing"]) {
  if (!schemas.SystemStatus.required.includes(capabilityName) ||
      schemas.SystemStatus.properties[capabilityName]?.$ref !== "#/components/schemas/OptionalCapabilityStatus") {
    throw new Error(`SystemStatus must expose the strict ${capabilityName} capability state`);
  }
}
if (schemas.SessionCredential.properties.session_token || schemas.SessionInfo.properties.token_hash || schemas.SessionInfo.properties.csrf_hash ||
    schemas.APITokenInfo.properties.token || schemas.APITokenInfo.properties.token_hash ||
    !schemas.APITokenCredential.required.includes("token") ||
    schemas.SessionCredential.properties.csrf_token.readOnly !== true ||
    schemas.SessionCredential.properties.csrf_token.writeOnly !== undefined ||
    schemas.APITokenCredential.properties.token.readOnly !== true ||
    schemas.APITokenCredential.properties.token.writeOnly !== undefined) {
  throw new Error("authentication schemas exposed a persisted digest or lost the one-time token contract");
}
const apiTokenListParameters = document.paths["/api/v1/auth/api-tokens"].get.parameters;
const apiTokenCursor = apiTokenListParameters.find((parameter) => parameter.name === "cursor")?.schema;
const apiTokenLimit = apiTokenListParameters.find((parameter) => parameter.name === "limit")?.schema;
if (apiTokenCursor?.type !== "string" || apiTokenCursor.maxLength !== 2048 ||
    apiTokenLimit?.type !== "integer" || apiTokenLimit.minimum !== 1 || apiTokenLimit.maximum !== 100 || apiTokenLimit.default !== 30 ||
    schemas.APITokenPage.type !== "object" || schemas.APITokenPage.additionalProperties !== false ||
    schemas.APITokenPage.required?.join(",") !== "items" || schemas.APITokenPage.properties.items.maxItems !== 100 ||
    schemas.APITokenPage.properties.items.items?.$ref !== "#/components/schemas/APITokenInfo" ||
    schemas.APITokenPage.properties.next_cursor.minLength !== 1 || schemas.APITokenPage.properties.next_cursor.maxLength !== 2048) {
  throw new Error("API Token pagination contract drifted");
}
if (document.components.parameters.CSRFToken.name !== "X-CSRF-Token" ||
    document.components.parameters.CSRFToken.required !== true) {
  throw new Error("CSRF header contract drifted");
}
if (document.components.parameters.Origin.name !== "Origin" ||
    document.components.parameters.Origin.in !== "header" ||
    document.components.parameters.Origin.required !== true ||
    document.components.parameters.Origin.schema?.type !== "string" ||
    document.components.parameters.Origin.schema?.minLength !== 1) {
  throw new Error("Origin header contract drifted");
}
function resolveRef(value) {
  if (!value?.$ref) return value;
  const prefix = "#/components/responses/";
  if (!value.$ref.startsWith(prefix)) throw new Error(`unsupported response ref ${value.$ref}`);
  return document.components.responses[value.$ref.slice(prefix.length)];
}

for (const [path, pathItem] of Object.entries(document.paths)) {
  for (const method of ["get", "post", "put", "patch", "delete", "head", "options", "trace"]) {
    const operation = pathItem[method];
    if (!operation) continue;
    const key = `${method.toUpperCase()} ${path}`;
    const responseStatuses = Object.keys(operation.responses ?? {}).sort();
    if (key === "POST /api/v1/health/issues/{issue_id}/repair-proposals") {
      if (responseStatuses.join(",") !== "400,401,403,405,503") {
        throw new Error(`${key} must remain the exact reserved no-success operation`);
      }
    } else if (!responseStatuses.some((status) => /^2\d{2}$/.test(status))) {
      throw new Error(`${key} must declare a success response`);
    }
    const statuses = publicOperations.has(key) ? ["405"] : ["401", "403", "405"];
    const expectedSchema = key === "POST /api/v1/settings/models/test"
      ? "#/components/schemas/ModelSettingsTestProblem"
      : "#/components/schemas/Problem";
    for (const status of statuses) {
      if (resolveRef(operation.responses?.[status])?.content?.["application/json"]?.schema?.$ref !== expectedSchema) {
        throw new Error(`${key} ${status} must declare a Problem response`);
      }
    }
  }
}

const modelSettingsOperations = [
  ["/api/v1/settings/models", "get", "200", "ModelSettingsResponse", undefined, ["200", "401", "403", "405", "500", "503"]],
  ["/api/v1/settings/models", "put", "200", "ModelSettingsResponse", "UpdateModelSettingsRequest", ["200", "400", "401", "403", "405", "409", "415", "500", "503"]],
  ["/api/v1/settings/models/activations", "post", "202", "ModelSettingsResponse", "StartModelSettingsActivationRequest", ["202", "400", "401", "403", "405", "409", "415", "500", "503"]],
  ["/api/v1/settings/models/test", "post", "200", "ModelSettingsTestResponse", "TestModelSettingsRequest", ["200", "400", "401", "403", "405", "409", "415", "500", "502", "503", "504"]],
];
for (const [path, method, successStatus, successSchema, requestSchema, statuses] of modelSettingsOperations) {
  const operation = document.paths[path]?.[method];
  if (!operation) throw new Error(`missing Model Settings operation ${method.toUpperCase()} ${path}`);
  if (Object.keys(operation.responses ?? {}).sort().join(",") !== statuses.slice().sort().join(",")) {
    throw new Error(`${method.toUpperCase()} ${path} response status contract drifted`);
  }
  if (operation.responses[successStatus]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}` ||
      operation.responses[successStatus]?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
    throw new Error(`${method.toUpperCase()} ${path} must return ${successSchema} with Cache-Control: no-store`);
  }
  for (const status of statuses.filter((status) => Number(status) >= 400)) {
    const expectedSchema = status === "409"
      ? "#/components/schemas/ModelSettingsConflictProblem"
      : path === "/api/v1/settings/models/test"
        ? "#/components/schemas/ModelSettingsTestProblem"
        : "#/components/schemas/Problem";
    if (resolveRef(operation.responses[status])?.content?.["application/json"]?.schema?.$ref !== expectedSchema) {
      throw new Error(`${method.toUpperCase()} ${path} ${status} must use ${expectedSchema}`);
    }
  }
  if (requestSchema === undefined) {
    if (operation.requestBody !== undefined) throw new Error(`${method.toUpperCase()} ${path} must not accept a request body`);
  } else if (operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== 65536 ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}`) {
    throw new Error(`${method.toUpperCase()} ${path} must use the bounded strict ${requestSchema} body`);
  }
}
for (const responseName of ["BadGateway", "GatewayTimeout"]) {
  if (document.components.responses[responseName]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
    throw new Error(`${responseName} must remain a redacted Problem response`);
  }
}

const strictModelSettingsSchemas = [
  "ModelSettingsConflictProblem",
  "ModelSettingsConflictDetails",
  "ModelSettingsTestProblem",
  "ModelSettingsTestDetails",
  "ModelSettingsResponse",
  "ModelSettingsSummary",
  "ModelChatSettingsSummary",
  "ModelEmbeddingSettingsSummary",
  "ModelSettingsRuntime",
  "ModelSettingsRuntimeRole",
  "ModelSettingsRollout",
  "ModelSettingsParticipants",
  "ModelSettingsParticipant",
  "ModelSettingsCapabilities",
  "ModelLocalRuntime",
  "StartModelSettingsActivationRequest",
  "UpdateModelSettingsRequest",
  "ModelChatSettingsDraft",
  "ModelEmbeddingSettingsDraft",
  "ModelAPIKeyKeep",
  "ModelAPIKeyReplace",
  "ModelAPIKeyClear",
  "TestModelSettingsRequest",
  "ModelSettingsTestResponse",
];
for (const schemaName of strictModelSettingsSchemas) {
  if (schemas[schemaName]?.type !== "object" || schemas[schemaName]?.additionalProperties !== false) {
    throw new Error(`${schemaName} must remain a strict object schema`);
  }
}
if (schemas.ModelCapabilityState?.type !== "string" || schemas.ModelCapabilityState.enum?.join(",") !== "disabled,configured,unavailable") {
  throw new Error("Model capability state must remain exhaustive");
}

const exactModelSettingsShape = (schemaName, required, properties = required) => {
  const schema = schemas[schemaName];
  if (schema.required?.join(",") !== required.join(",") || Object.keys(schema.properties ?? {}).join(",") !== properties.join(",")) {
    throw new Error(`${schemaName} required/property shape drifted`);
  }
};
exactModelSettingsShape("ModelSettingsResponse", ["desired_revision", "active_revision", "desired_settings", "active_settings", "runtime", "rollout", "participants", "apply_required", "restart_required", "capabilities", "local_runtime"]);
exactModelSettingsShape("ModelSettingsConflictProblem", ["error_code", "message", "retryable", "details"]);
exactModelSettingsShape("ModelSettingsConflictDetails", ["current_revision"]);
exactModelSettingsShape("ModelSettingsTestProblem", ["error_code", "message", "retryable"], ["error_code", "message", "retryable", "workflow_run_id", "details"]);
exactModelSettingsShape("ModelSettingsTestDetails", ["target", "stage"], ["target", "stage", "provider_http_status", "provider_error_code", "provider_error_type", "provider_message", "provider_request_id", "transport_error", "validation_reason"]);
exactModelSettingsShape("ModelSettingsSummary", ["chat", "embedding"]);
exactModelSettingsShape("ModelChatSettingsSummary", ["provider", "api_style", "base_url", "model", "model_version", "adapter_version", "api_key_configured"]);
exactModelSettingsShape("ModelEmbeddingSettingsSummary", ["provider", "base_url", "model", "dimensions", "normalization", "distance_metric", "api_key_configured"]);
exactModelSettingsShape("ModelSettingsRuntime", ["api", "worker"]);
exactModelSettingsShape("ModelSettingsRuntimeRole", ["applied_revision", "phase", "fresh"]);
exactModelSettingsShape("ModelSettingsRollout", ["id", "version", "phase", "target_revision", "last_error_code", "retryable"]);
exactModelSettingsShape("ModelSettingsParticipants", ["api", "worker"]);
exactModelSettingsShape("ModelSettingsParticipant", ["present", "target_revision", "phase", "fresh", "last_error_code", "retryable"]);
exactModelSettingsShape("ModelSettingsCapabilities", ["chat", "embedding"]);
exactModelSettingsShape("ModelLocalRuntime", ["mode", "phase", "fresh", "requirement_hash", "ready_hash", "operation_id", "operation_phase", "completed_bytes", "total_bytes", "progress_known", "operation_error", "operation_retryable"]);
exactModelSettingsShape("StartModelSettingsActivationRequest", ["expected_revision"]);
exactModelSettingsShape("UpdateModelSettingsRequest", ["expected_revision", "chat", "embedding"]);
exactModelSettingsShape("ModelChatSettingsDraft", ["provider", "api_style", "base_url", "model", "model_version", "adapter_version", "api_key"]);
exactModelSettingsShape("ModelEmbeddingSettingsDraft", ["provider", "base_url", "model", "dimensions", "normalization", "distance_metric", "api_key"]);
exactModelSettingsShape("ModelAPIKeyKeep", ["action"]);
exactModelSettingsShape("ModelAPIKeyReplace", ["action", "value"]);
exactModelSettingsShape("ModelAPIKeyClear", ["action"]);
exactModelSettingsShape("TestModelSettingsRequest", ["target"], ["target", "chat", "embedding"]);
exactModelSettingsShape("ModelSettingsTestResponse", ["target", "status", "provider", "model", "endpoint_path", "latency_ms"], ["target", "status", "provider", "model", "api_style", "endpoint_path", "latency_ms"]);

const modelResponse = schemas.ModelSettingsResponse;
const modelConflict = schemas.ModelSettingsConflictProblem;
const modelTestProblem = schemas.ModelSettingsTestProblem;
const modelTestDetails = schemas.ModelSettingsTestDetails;
if (modelConflict.properties.error_code.pattern !== "^[A-Z][A-Z0-9_]*$" ||
    modelConflict.properties.error_code.maxLength !== 128 || modelConflict.properties.message.maxLength !== 4096 ||
    modelConflict.properties.retryable.type !== "boolean" ||
    modelConflict.properties.details.$ref !== "#/components/schemas/ModelSettingsConflictDetails" ||
    schemas.ModelSettingsConflictDetails.properties.current_revision.minimum !== 0) {
  throw new Error("Model Settings conflict must expose only a bounded stable code and current revision");
}
if (modelTestProblem.properties.error_code.pattern !== "^[A-Z][A-Z0-9_]*$" ||
    modelTestProblem.properties.error_code.maxLength !== 128 || modelTestProblem.properties.message.maxLength !== 4096 ||
    modelTestProblem.properties.details.$ref !== "#/components/schemas/ModelSettingsTestDetails" ||
    modelTestDetails.properties.target.enum?.join(",") !== "chat,embedding" ||
    modelTestDetails.properties.stage.enum?.join(",") !== "request,dns,connect,tls,provider_response,response_read,response_validation,cancelled,timeout" ||
    modelTestDetails.properties.provider_http_status.minimum !== 100 || modelTestDetails.properties.provider_http_status.maximum !== 599 ||
    modelTestDetails.properties.provider_error_code.maxLength !== 128 || modelTestDetails.properties.provider_error_type.maxLength !== 128 ||
    modelTestDetails.properties.provider_error_code["x-max-utf8-bytes"] !== 128 || modelTestDetails.properties.provider_error_type["x-max-utf8-bytes"] !== 128 ||
    modelTestDetails.properties.provider_message.maxLength !== 1024 || modelTestDetails.properties.provider_message["x-max-utf8-bytes"] !== 1024 ||
    modelTestDetails.properties.provider_request_id.maxLength !== 256 || modelTestDetails.properties.provider_request_id["x-max-utf8-bytes"] !== 256 ||
    modelTestDetails.properties.transport_error.maxLength !== 256 || modelTestDetails.properties.transport_error["x-max-utf8-bytes"] !== 256 ||
    modelTestDetails.properties.validation_reason.enum?.join(",") !== "invalid_response,model_mismatch,finish_reason_length,finish_reason_invalid,empty_content,refusal,tool_calls,missing_usage,invalid_usage,response_contract_invalid") {
  throw new Error("Model Settings test diagnostics must remain target-bound, bounded, and exhaustive");
}
if (modelResponse.properties.desired_revision.minimum !== 0 || modelResponse.properties.active_revision.minimum !== 0 ||
    modelResponse.properties.desired_settings.$ref !== "#/components/schemas/ModelSettingsSummary" ||
    modelResponse.properties.active_settings.$ref !== "#/components/schemas/ModelSettingsSummary" ||
    modelResponse.properties.runtime.$ref !== "#/components/schemas/ModelSettingsRuntime" ||
    modelResponse.properties.rollout.$ref !== "#/components/schemas/ModelSettingsRollout" ||
    modelResponse.properties.participants.$ref !== "#/components/schemas/ModelSettingsParticipants" ||
    modelResponse.properties.apply_required.type !== "boolean" ||
    modelResponse.properties.capabilities.$ref !== "#/components/schemas/ModelSettingsCapabilities" ||
    modelResponse.properties.restart_required.const !== false || modelResponse.properties.restart_required.deprecated !== true) {
  throw new Error("Model Settings desired/active/runtime/rollout response contract drifted");
}
const responseSchemas = [modelResponse, modelConflict, schemas.ModelSettingsConflictDetails, modelTestProblem, modelTestDetails, schemas.ModelSettingsSummary, schemas.ModelChatSettingsSummary, schemas.ModelEmbeddingSettingsSummary,
  schemas.ModelSettingsRuntime, schemas.ModelSettingsRuntimeRole, schemas.ModelSettingsRollout, schemas.ModelSettingsParticipants, schemas.ModelSettingsParticipant, schemas.ModelSettingsCapabilities];
if (responseSchemas.some((schema) => JSON.stringify(schema).includes('"writeOnly"') || JSON.stringify(schema).includes('"api_key":') ||
    JSON.stringify(schema).includes('"ciphertext"') || JSON.stringify(schema).includes('"nonce"') ||
    JSON.stringify(schema).includes('"key_id"') || JSON.stringify(schema).includes('"instance_id"'))) {
  throw new Error("Model Settings response schemas must not expose secrets, ciphertext, key metadata, or instance identity");
}

const chatSummary = schemas.ModelChatSettingsSummary;
const embeddingSummary = schemas.ModelEmbeddingSettingsSummary;
if (chatSummary.properties.provider.enum?.join(",") !== "disabled,openai-compatible,ollama" ||
    chatSummary.properties.api_style.enum?.join(",") !== "chat_completions,responses" ||
    chatSummary.oneOf?.map((branch) => branch.properties?.provider?.const).join(",") !== "disabled,openai-compatible,ollama" ||
    chatSummary.oneOf[0].properties.base_url.const !== "" || chatSummary.oneOf[0].properties.model.const !== "" ||
    chatSummary.oneOf[0].properties.model_version.const !== "" ||
    chatSummary.oneOf[0].properties.api_key_configured.const !== false ||
    chatSummary.oneOf[1].properties.base_url.pattern !== undefined ||
    chatSummary.oneOf[2].properties.api_style.const !== "chat_completions" ||
    chatSummary.oneOf[2].properties.base_url.const !== "http://127.0.0.1:11434" ||
    chatSummary.oneOf[2].properties.api_key_configured.const !== false) {
  throw new Error("Chat summary must remain a strict disabled/openai-compatible/ollama provider union");
}
const modelTestSuccess = schemas.ModelSettingsTestResponse;
if (modelTestSuccess.properties.api_style.enum?.join(",") !== "chat_completions,responses" ||
    modelTestSuccess.properties.endpoint_path.enum?.join(",") !== "/v1/chat/completions,/v1/responses,/v1/embeddings" ||
    modelTestSuccess.properties.latency_ms.minimum !== 0 || modelTestSuccess.oneOf?.[0]?.required?.join(",") !== "api_style" ||
    modelTestSuccess.oneOf?.[1]?.properties?.endpoint_path?.const !== "/v1/embeddings" ||
    modelTestSuccess.oneOf?.[0]?.oneOf?.length !== 2 ||
    modelTestSuccess.oneOf?.[0]?.oneOf?.[0]?.properties?.api_style?.const !== "chat_completions" ||
    modelTestSuccess.oneOf?.[0]?.oneOf?.[0]?.properties?.endpoint_path?.const !== "/v1/chat/completions" ||
    modelTestSuccess.oneOf?.[0]?.oneOf?.[1]?.properties?.api_style?.const !== "responses" ||
    modelTestSuccess.oneOf?.[0]?.oneOf?.[1]?.properties?.endpoint_path?.const !== "/v1/responses") {
  throw new Error("Model Settings test success must expose only fixed protocol metadata and non-negative latency");
}
if (embeddingSummary.properties.provider.enum?.join(",") !== "disabled,openai-compatible,ollama" ||
    embeddingSummary.oneOf?.map((branch) => branch.properties?.provider?.const).join(",") !== "disabled,openai-compatible,ollama" ||
    embeddingSummary.properties.dimensions.maximum !== 16000 || embeddingSummary.properties.model.maxLength !== 128 ||
    embeddingSummary.properties.normalization.enum?.join(",") !== "none,l2" ||
    embeddingSummary.properties.distance_metric.enum?.join(",") !== "cosine,inner_product,euclidean" ||
    embeddingSummary.oneOf[0].properties.base_url.const !== "" || embeddingSummary.oneOf[0].properties.model.const !== "" ||
    embeddingSummary.oneOf[0].properties.dimensions.const !== 0 || embeddingSummary.oneOf[0].properties.api_key_configured.const !== false ||
    embeddingSummary.oneOf[1].properties.api_key_configured.const !== true ||
    embeddingSummary.oneOf[2].properties.api_key_configured.const !== false) {
  throw new Error("Embedding summary must remain a strict disabled/openai-compatible/ollama provider union");
}

if (schemas.ModelSettingsRuntime.properties.api.$ref !== "#/components/schemas/ModelSettingsRuntimeRole" ||
    schemas.ModelSettingsRuntime.properties.worker.$ref !== "#/components/schemas/ModelSettingsRuntimeRole" ||
    schemas.ModelSettingsRuntimeRole.properties.applied_revision.minimum !== 0 ||
    schemas.ModelSettingsRuntimeRole.properties.phase.enum?.join(",") !== "active,unavailable" ||
    schemas.ModelSettingsRuntimeRole.properties.fresh.type !== "boolean") {
  throw new Error("Model Settings API/Worker applied runtime contract drifted");
}
if (schemas.ModelSettingsRollout.properties.id.type?.join(",") !== "string,null" ||
    schemas.ModelSettingsRollout.properties.id.format !== "uuid" ||
    schemas.ModelSettingsRollout.properties.version.minimum !== 0 ||
    schemas.ModelSettingsRollout.properties.phase.enum?.join(",") !== "idle,preparing,arming,activating,failed" ||
    schemas.ModelSettingsRollout.properties.target_revision.type?.join(",") !== "integer,null" ||
    schemas.ModelSettingsRollout.properties.target_revision.minimum !== 0 ||
    schemas.ModelSettingsRollout.properties.last_error_code.type?.join(",") !== "string,null" ||
    schemas.ModelSettingsRollout.properties.last_error_code.pattern !== "^[A-Z][A-Z0-9_]*$" ||
    schemas.ModelSettingsRollout.properties.retryable.type !== "boolean") {
  throw new Error("Model Settings activation projection contract drifted");
}
const rolloutBranches = schemas.ModelSettingsRollout.oneOf ?? [];
if (rolloutBranches.length !== 3 || rolloutBranches[0].properties?.phase?.const !== "idle" ||
    rolloutBranches[0].properties?.id?.type !== "null" || rolloutBranches[0].properties?.version?.minimum !== 0 ||
    rolloutBranches[0].properties?.target_revision?.type !== "null" ||
    rolloutBranches[0].properties?.last_error_code?.type !== "null" || rolloutBranches[0].properties?.retryable?.const !== false ||
    rolloutBranches[1].properties?.id?.type !== "string" || rolloutBranches[1].properties?.id?.format !== "uuid" ||
    rolloutBranches[1].properties?.version?.minimum !== 1 ||
    rolloutBranches[1].properties?.phase?.enum?.join(",") !== "preparing,arming,activating" ||
    rolloutBranches[1].properties?.target_revision?.type !== "integer" ||
    rolloutBranches[1].properties?.last_error_code?.type !== "null" || rolloutBranches[1].properties?.retryable?.const !== false ||
    rolloutBranches[2].properties?.id?.type !== "string" || rolloutBranches[2].properties?.id?.format !== "uuid" ||
    rolloutBranches[2].properties?.version?.minimum !== 1 ||
    rolloutBranches[2].properties?.phase?.const !== "failed" ||
    rolloutBranches[2].properties?.target_revision?.type !== "integer" ||
    rolloutBranches[2].properties?.last_error_code?.type !== "string" || rolloutBranches[2].properties?.retryable?.const !== true) {
  throw new Error("Model Settings rollout must remain an exact idle/in-progress/failed union");
}
const modelParticipants = schemas.ModelSettingsParticipants;
const modelParticipant = schemas.ModelSettingsParticipant;
const participantBranches = modelParticipant.oneOf ?? [];
if (modelParticipants.properties.api.$ref !== "#/components/schemas/ModelSettingsParticipant" ||
    modelParticipants.properties.worker.$ref !== "#/components/schemas/ModelSettingsParticipant" ||
    modelParticipant.properties.target_revision.type?.join(",") !== "integer,null" ||
    modelParticipant.properties.target_revision.minimum !== 0 ||
    modelParticipant.properties.phase.type?.join(",") !== "string,null" ||
    modelParticipant.properties.phase.enum?.join(",") !== "preparing,prepared,armed,activated,failed,aborted,retired," ||
    modelParticipant.properties.last_error_code.type?.join(",") !== "string,null" ||
    modelParticipant.properties.last_error_code.pattern !== "^[A-Z][A-Z0-9_]*$" ||
    participantBranches.length !== 3 || participantBranches[0].properties?.present?.const !== false ||
    participantBranches[0].properties?.target_revision?.type !== "null" || participantBranches[0].properties?.phase?.type !== "null" ||
    participantBranches[0].properties?.fresh?.const !== false || participantBranches[0].properties?.last_error_code?.type !== "null" ||
    participantBranches[0].properties?.retryable?.const !== false || participantBranches[1].properties?.present?.const !== true ||
    participantBranches[1].properties?.phase?.enum?.join(",") !== "preparing,prepared,armed,activated,aborted,retired" ||
    participantBranches[1].properties?.last_error_code?.type !== "null" || participantBranches[1].properties?.retryable?.const !== false ||
    participantBranches[2].properties?.present?.const !== true || participantBranches[2].properties?.phase?.const !== "failed" ||
    participantBranches[2].properties?.last_error_code?.type !== "string") {
  throw new Error("Model Settings participant projection must preserve exact absent and present shapes");
}

const modelSettingsProblem = document.components.responses.ModelSettingsProblemNoStore;
if (modelSettingsProblem?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
    modelSettingsProblem?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
  throw new Error("Model Settings Problem responses must remain no-store");
}
const modelSettingsConflict = document.components.responses.ModelSettingsConflictNoStore;
if (modelSettingsConflict?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
    modelSettingsConflict?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ModelSettingsConflictProblem") {
  throw new Error("Model Settings conflict responses must include current_revision and remain no-store");
}
const modelSettingsTestProblem = document.components.responses.ModelSettingsTestProblemNoStore;
if (modelSettingsTestProblem?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
    modelSettingsTestProblem?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ModelSettingsTestProblem") {
  throw new Error("Model Settings test Problem responses must use bounded diagnostics and remain no-store");
}
for (const [path, method, successStatus] of [
  ["/api/v1/settings/models", "get", "200"],
  ["/api/v1/settings/models", "put", "200"],
  ["/api/v1/settings/models/activations", "post", "202"],
  ["/api/v1/settings/models/test", "post", "200"],
]) {
  const responses = document.paths[path][method].responses;
  if (responses[successStatus]?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
      Object.entries(responses).some(([status, response]) => status !== successStatus && response.$ref !==
        (status === "409"
          ? "#/components/responses/ModelSettingsConflictNoStore"
          : path === "/api/v1/settings/models/test"
            ? "#/components/responses/ModelSettingsTestProblemNoStore"
            : "#/components/responses/ModelSettingsProblemNoStore"))) {
    throw new Error(`${method.toUpperCase()} ${path} responses must all remain no-store`);
  }
}

const updateModelSettings = schemas.UpdateModelSettingsRequest;
const startModelSettingsActivation = schemas.StartModelSettingsActivationRequest;
const chatDraft = schemas.ModelChatSettingsDraft;
const embeddingDraft = schemas.ModelEmbeddingSettingsDraft;
if (startModelSettingsActivation.properties.expected_revision.minimum !== 0 ||
    JSON.stringify(startModelSettingsActivation).includes("api_key") ||
    JSON.stringify(startModelSettingsActivation).includes("base_url") ||
    JSON.stringify(startModelSettingsActivation).includes("rollout_id") ||
    JSON.stringify(startModelSettingsActivation).includes("phase")) {
  throw new Error("Model Settings activation must contain only the exact desired revision");
}
if (updateModelSettings.properties.expected_revision.minimum !== 0 ||
    updateModelSettings.properties.chat.$ref !== "#/components/schemas/ModelChatSettingsDraft" ||
    updateModelSettings.properties.embedding.$ref !== "#/components/schemas/ModelEmbeddingSettingsDraft") {
  throw new Error("Model Settings update must remain an optimistic full replacement");
}
if (chatDraft.properties.provider.enum?.join(",") !== "disabled,openai-compatible,ollama" ||
    chatDraft.oneOf?.map((branch) => branch.properties?.provider?.const).join(",") !== "disabled,openai-compatible,ollama" ||
    chatDraft.properties.api_key.$ref !== "#/components/schemas/ModelAPIKeyAction" ||
    chatDraft.properties.api_key.writeOnly !== true || chatDraft.properties.model.maxLength !== 128 ||
    chatDraft.properties.model_version.maxLength !== 64 || chatDraft.properties.adapter_version.maxLength !== 64 ||
    chatDraft.oneOf[1].properties.base_url.pattern !== "^[Hh][Tt][Tt][Pp][Ss]://" ||
    chatDraft.oneOf[2].properties.api_style.const !== "chat_completions" ||
    chatDraft.oneOf[2].properties.base_url.const !== "http://127.0.0.1:11434" ||
    chatDraft.oneOf[2].properties.api_key.$ref !== "#/components/schemas/ModelAPIKeyClear") {
  throw new Error("Chat draft provider or write-only API-key contract drifted");
}
if (embeddingDraft.properties.provider.enum?.join(",") !== "disabled,openai-compatible,ollama" ||
    embeddingDraft.oneOf?.map((branch) => branch.properties?.provider?.const).join(",") !== "disabled,openai-compatible,ollama" ||
    embeddingDraft.properties.api_key.$ref !== "#/components/schemas/ModelAPIKeyAction" ||
    embeddingDraft.properties.api_key.writeOnly !== true || embeddingDraft.properties.dimensions.maximum !== 16000 ||
    embeddingDraft.properties.model.maxLength !== 128 ||
    embeddingDraft.properties.normalization.enum?.join(",") !== "none,l2" ||
    embeddingDraft.properties.distance_metric.enum?.join(",") !== "cosine,inner_product,euclidean") {
  throw new Error("Embedding draft provider or write-only API-key contract drifted");
}

const apiKeyAction = schemas.ModelAPIKeyAction;
if (!apiKeyAction.description?.includes("provider and normalized base_url") ||
    apiKeyAction.oneOf?.map((branch) => branch.$ref).join(",") !==
      "#/components/schemas/ModelAPIKeyKeep,#/components/schemas/ModelAPIKeyReplace,#/components/schemas/ModelAPIKeyClear" ||
    apiKeyAction.discriminator?.propertyName !== "action" ||
    apiKeyAction.discriminator.mapping?.keep !== "#/components/schemas/ModelAPIKeyKeep" ||
    apiKeyAction.discriminator.mapping?.replace !== "#/components/schemas/ModelAPIKeyReplace" ||
    apiKeyAction.discriminator.mapping?.clear !== "#/components/schemas/ModelAPIKeyClear" ||
    schemas.ModelAPIKeyKeep.properties.action.const !== "keep" || schemas.ModelAPIKeyClear.properties.action.const !== "clear" ||
    schemas.ModelAPIKeyReplace.properties.action.const !== "replace" || schemas.ModelAPIKeyReplace.properties.value.writeOnly !== true ||
    schemas.ModelAPIKeyReplace.properties.value.minLength !== 1 || schemas.ModelAPIKeyReplace.properties.value.maxLength !== 16384) {
  throw new Error("Model API key must remain an exact keep/replace/clear write-only tagged union");
}

const testModelSettings = schemas.TestModelSettingsRequest;
if (testModelSettings.properties.target.enum?.join(",") !== "chat,embedding" ||
    testModelSettings.properties.chat.$ref !== "#/components/schemas/ModelChatSettingsDraft" ||
    testModelSettings.properties.embedding.$ref !== "#/components/schemas/ModelEmbeddingSettingsDraft" ||
    testModelSettings.oneOf?.map((branch) => branch.properties?.target?.const).join(",") !== "chat,embedding" ||
    testModelSettings.oneOf[0].required?.join(",") !== "chat" || testModelSettings.oneOf[0].not?.required?.join(",") !== "embedding" ||
    testModelSettings.oneOf[0].properties.chat?.properties?.provider?.enum?.join(",") !== "openai-compatible,ollama" ||
    testModelSettings.oneOf[1].required?.join(",") !== "embedding" || testModelSettings.oneOf[1].not?.required?.join(",") !== "chat" ||
    testModelSettings.oneOf[1].properties.embedding?.properties?.provider?.enum?.join(",") !== "openai-compatible,ollama") {
  throw new Error("Model connection test must contain exactly one enabled target draft");
}
const modelTestResponse = schemas.ModelSettingsTestResponse;
if (modelTestResponse.properties.status.const !== "ok" ||
    modelTestResponse.properties.model.maxLength !== 128 ||
    modelTestResponse.oneOf?.map((branch) => branch.properties?.target?.const).join(",") !== "chat,embedding" ||
    modelTestResponse.oneOf[0].properties.provider.enum?.join(",") !== "openai-compatible,ollama" ||
    modelTestResponse.oneOf[0].oneOf?.[0]?.properties?.provider?.enum?.join(",") !== "openai-compatible,ollama" ||
    modelTestResponse.oneOf[0].oneOf?.[1]?.properties?.provider?.const !== "openai-compatible" ||
    modelTestResponse.oneOf[1].properties.provider.enum?.join(",") !== "openai-compatible,ollama") {
  throw new Error("Model connection test success response contract drifted");
}

const expectedExportPaths = [
  "/api/v1/exports",
  "/api/v1/exports/{export_id}",
  "/api/v1/exports/{export_id}/download",
  "/api/v1/workspaces/{workspace_id}/attachment-exports",
  "/api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}",
  "/api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}/download",
  "/api/v1/workspaces/{workspace_id}/exports",
];
const actualExportPaths = Object.keys(document.paths)
  .filter((path) => path.startsWith("/api/v1/exports") || path === "/api/v1/workspaces/{workspace_id}/exports" ||
    path.startsWith("/api/v1/workspaces/{workspace_id}/attachment-exports"))
  .sort();
if (actualExportPaths.join(",") !== expectedExportPaths.slice().sort().join(",")) {
  throw new Error("Export path inventory drifted from the Collection and attachment Router operations");
}
const expectedExportRouteRegistrations = [
  "GET /exports/{export_id} handler.get",
  "GET /exports/{export_id}/download handler.download",
  "GET /workspaces/{workspace_id}/attachment-exports handler.listAttachments",
  "GET /workspaces/{workspace_id}/attachment-exports/{export_id} handler.getAttachment",
  "GET /workspaces/{workspace_id}/attachment-exports/{export_id}/download handler.downloadAttachment",
  "GET /workspaces/{workspace_id}/exports handler.list",
  "POST /exports handler.create",
  "POST /workspaces/{workspace_id}/attachment-exports handler.createAttachment",
];
const actualExportRouteRegistrations = [...exportHandlerSource.matchAll(/router\.(GET|POST)\(\s*"([^"]+)"\s*,\s*httpapi\.GinHandler\(handler\.([A-Za-z0-9_]+)\)\s*\)/g)]
  .map((match) => `${match[1]} ${canonicalGinRoute(match[2])} handler.${match[3]}`)
  .sort();
if (actualExportRouteRegistrations.join(",") !== expectedExportRouteRegistrations.join(",")) {
  throw new Error("Export HTTP Router must register exactly the Collection and attachment operations");
}

const exportOperations = [
  [
    "/api/v1/exports",
    "post",
    ["200", "202", "400", "401", "403", "404", "405", "409", "415", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_PERMISSION_DENIED", "EXPORT_NOT_FOUND", "EXPORT_IDEMPOTENCY_CONFLICT", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
  [
    "/api/v1/exports/{export_id}",
    "get",
    ["200", "400", "401", "403", "404", "405", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_NOT_FOUND", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
  [
    "/api/v1/exports/{export_id}/download",
    "get",
    ["200", "400", "401", "403", "404", "405", "409", "410", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_NOT_FOUND", "EXPORT_RESULT_NOT_READY", "EXPORT_EXPIRED", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
  [
    "/api/v1/workspaces/{workspace_id}/exports",
    "get",
    ["200", "400", "401", "403", "405", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
  [
    "/api/v1/workspaces/{workspace_id}/attachment-exports",
    "post",
    ["200", "202", "400", "401", "403", "404", "405", "409", "415", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_PERMISSION_DENIED", "EXPORT_NOT_FOUND", "EXPORT_IDEMPOTENCY_CONFLICT", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
  [
    "/api/v1/workspaces/{workspace_id}/attachment-exports",
    "get",
    ["200", "400", "401", "403", "405", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
  [
    "/api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}",
    "get",
    ["200", "400", "401", "403", "404", "405", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_NOT_FOUND", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
  [
    "/api/v1/workspaces/{workspace_id}/attachment-exports/{export_id}/download",
    "get",
    ["200", "400", "401", "403", "404", "405", "409", "410", "500", "503"],
    ["EXPORT_REQUEST_INVALID", "EXPORT_NOT_FOUND", "EXPORT_RESULT_NOT_READY", "EXPORT_EXPIRED", "EXPORT_RESULT_INCONSISTENT", "EXPORT_DEPENDENCY_UNAVAILABLE"],
  ],
];
for (const [path, method, statuses, errorCodes] of exportOperations) {
  const operation = document.paths[path]?.[method];
  if (!operation) throw new Error(`missing Export operation ${method.toUpperCase()} ${path}`);
  if (operation.security !== undefined || operation["x-required-capability"] !== "READ_LOCAL") {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require READ_LOCAL`);
  }
  if (Object.keys(operation.responses).sort().join(",") !== statuses.slice().sort().join(",")) {
    throw new Error(`${method.toUpperCase()} ${path} response status contract drifted`);
  }
  if (operation["x-error-codes"]?.join(",") !== errorCodes.join(",")) {
    throw new Error(`${method.toUpperCase()} ${path} stable Export error codes drifted`);
  }
  for (const status of statuses.filter((status) => Number(status) >= 400)) {
    if (resolveRef(operation.responses[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Export ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}

const exportCreate = document.paths["/api/v1/exports"].post;
if (exportCreate.parameters?.length !== 1 || exportCreate.parameters[0]?.$ref !== "#/components/parameters/IdempotencyKey" ||
    exportCreate.requestBody?.required !== true || exportCreate.requestBody?.["x-max-body-bytes"] !== 65536 ||
    exportCreate.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ExportCreateRequest") {
  throw new Error("Export create must retain its strict 64 KiB idempotent JSON command contract");
}
for (const status of ["200", "202"]) {
  const response = exportCreate.responses[status];
  const schema = response.content?.["application/json"]?.schema;
  if (schema?.allOf?.length !== 2 || schema.allOf[0]?.$ref !== "#/components/schemas/ExportCreateResponse" ||
      schema.allOf[1]?.properties?.replayed?.const !== (status === "200") ||
      response.headers?.Location?.schema?.format !== "uri-reference" ||
      response.headers?.Location?.schema?.pattern !== "^/api/v1/exports/[0-9a-f-]{36}\\?workspace_id=[0-9a-f-]{36}$" ||
      response.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
    throw new Error(`Export create ${status} response lost Location, no-store or its strict response schema`);
  }
}
if (Object.keys(exportCreate.responses).filter((status) => status.startsWith("2")).join(",") !== "200,202") {
  throw new Error("Export create must use only 202 for first acceptance and 200 for exact replay");
}

for (const path of ["/api/v1/exports/{export_id}", "/api/v1/exports/{export_id}/download"]) {
  const parameter = document.paths[path].parameters?.[0];
  const query = document.paths[path].get.parameters;
  if (document.paths[path].parameters?.length !== 1 || parameter?.name !== "export_id" || parameter.in !== "path" ||
      parameter.required !== true || parameter.schema?.type !== "string" || parameter.schema?.format !== "uuid" ||
      query?.length !== 1 || query[0]?.$ref !== "#/components/parameters/WorkspaceIDQuery") {
    throw new Error(`${path} must retain strict Export and Workspace identity parameters`);
  }
}
const exportGet = document.paths["/api/v1/exports/{export_id}"].get;
if (exportGet.responses["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ExportJob" ||
    exportGet.responses["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
  throw new Error("Export detail must return the strict no-store ExportJob projection");
}

const exportListPath = document.paths["/api/v1/workspaces/{workspace_id}/exports"];
const exportList = exportListPath.get;
const exportListWorkspace = exportListPath.parameters?.[0];
const exportCollection = exportList.parameters?.find((parameter) => parameter.name === "collection_id");
const exportLimit = exportList.parameters?.find((parameter) => parameter.name === "limit")?.schema;
const exportCursor = exportList.parameters?.find((parameter) => parameter.name === "cursor")?.schema;
if (exportListPath.parameters?.length !== 1 || exportListWorkspace?.name !== "workspace_id" || exportListWorkspace.in !== "path" ||
    exportListWorkspace.required !== true || exportListWorkspace.schema?.type !== "string" || exportListWorkspace.schema?.format !== "uuid" ||
    exportList.parameters?.length !== 3 || exportCollection?.in !== "query" || exportCollection.required !== true ||
    exportCollection.schema?.type !== "string" || exportCollection.schema?.format !== "uuid" ||
    exportLimit?.type !== "integer" || exportLimit.minimum !== 1 || exportLimit.maximum !== 100 || exportLimit.default !== 50 ||
    exportCursor?.type !== "string" || exportCursor.minLength !== 1 || exportCursor.maxLength !== 4096 || exportCursor["x-max-utf8-bytes"] !== 4096 ||
    exportList.responses["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ExportPage" ||
    exportList.responses["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
  throw new Error("Collection Export list identity, cursor, limit or response contract drifted");
}

const exportDownload = document.paths["/api/v1/exports/{export_id}/download"].get;
const exportDownloadResponse = exportDownload.responses["200"];
const exportDownloadHeaders = exportDownloadResponse.headers;
const exportDownloadMediaTypes = Object.keys(exportDownloadResponse.content ?? {}).sort();
if (exportDownloadMediaTypes.join(",") !== "application/json,text/markdown" ||
    exportDownloadMediaTypes.some((mediaType) => exportDownloadResponse.content[mediaType]?.schema?.type !== "string" || exportDownloadResponse.content[mediaType]?.schema?.format !== "binary") ||
    exportDownloadHeaders?.["Content-Disposition"]?.schema?.pattern !== "^attachment; filename=\\\"collection-[0-9a-f-]{36}-[0-9a-f-]{36}\\.(?:md|json)\\\"$" ||
    exportDownloadHeaders?.["Content-Length"]?.schema?.type !== "integer" || exportDownloadHeaders?.["Content-Length"]?.schema?.minimum !== 0 ||
    exportDownloadHeaders?.["Cache-Control"]?.schema?.const !== "private, no-store" ||
    exportDownloadHeaders?.["X-Content-Type-Options"]?.schema?.const !== "nosniff" ||
    exportDownload.responses["410"]?.$ref !== "#/components/responses/Gone") {
  throw new Error("Export download media type, attachment, length, cache, nosniff or expiry contract drifted");
}

for (const schemaName of ["ExportCreateRequest", "ExportCreateResponse", "ExportJob", "ExportPage"]) {
  if (schemas[schemaName]?.type !== "object" || schemas[schemaName].additionalProperties !== false) {
    throw new Error(`${schemaName} must remain a strict object schema`);
  }
}
const exportCreateRequest = schemas.ExportCreateRequest;
const exportCreateRequestProperties = ["workspace_id", "kind", "collection_id", "collection_version", "query_hash", "fields", "redaction_policy", "include_sensitive", "expires_in_seconds"];
const exportFields = ["object_type", "id", "title", "summary", "status", "topic", "source", "relations", "health", "confidence", "created_at", "updated_at", "applicability"];
if (exportCreateRequest.required?.join(",") !== "workspace_id,kind,collection_id,collection_version,query_hash" ||
    Object.keys(exportCreateRequest.properties).join(",") !== exportCreateRequestProperties.join(",") ||
    exportCreateRequest.properties.workspace_id.format !== "uuid" || exportCreateRequest.properties.collection_id.format !== "uuid" ||
    exportCreateRequest.properties.collection_version.minimum !== 1 || exportCreateRequest.properties.query_hash.pattern !== "^[0-9a-f]{64}$" ||
    exportCreateRequest.properties.kind.enum?.join(",") !== "MARKDOWN,METADATA_JSON" ||
    exportCreateRequest.properties.fields.minItems !== 1 || exportCreateRequest.properties.fields.maxItems !== 32 ||
    exportCreateRequest.properties.fields.uniqueItems !== true || exportCreateRequest.properties.fields.items.enum?.join(",") !== exportFields.join(",") ||
    exportCreateRequest.properties.redaction_policy.enum?.join(",") !== "MASKED,FULL" || exportCreateRequest.properties.redaction_policy.default !== "MASKED" ||
    exportCreateRequest.properties.include_sensitive.default !== false || exportCreateRequest.properties.expires_in_seconds.minimum !== 1 ||
    exportCreateRequest.properties.expires_in_seconds.maximum !== 604800 || exportCreateRequest.properties.expires_in_seconds.default !== 86400 ||
    exportCreateRequest.properties.schema_version !== undefined) {
  throw new Error("ExportCreateRequest public fields, defaults, bounds or fixed schema ownership drifted");
}
const exportRequestFullRule = exportCreateRequest.allOf?.find((rule) => rule.if?.properties?.redaction_policy?.const === "FULL");
const exportRequestSensitiveRule = exportCreateRequest.allOf?.find((rule) => rule.if?.properties?.include_sensitive?.const === true);
if (exportRequestFullRule?.then?.properties?.include_sensitive?.const !== true || !exportRequestFullRule.then.required?.includes("include_sensitive") ||
    exportRequestSensitiveRule?.then?.properties?.redaction_policy?.const !== "FULL" || !exportRequestSensitiveRule.then.required?.includes("redaction_policy")) {
  throw new Error("Export FULL and include_sensitive request invariants drifted");
}

const exportCreateResponse = schemas.ExportCreateResponse;
if (exportCreateResponse.required?.join(",") !== "job,replayed,dispatch_pending" ||
    Object.keys(exportCreateResponse.properties).join(",") !== "job,replayed,dispatch_pending" ||
    exportCreateResponse.properties.job?.$ref !== "#/components/schemas/ExportJob" ||
    exportCreateResponse.properties.replayed?.type !== "boolean" || exportCreateResponse.properties.dispatch_pending?.type !== "boolean") {
  throw new Error("ExportCreateResponse replay or dispatch projection drifted");
}

const exportJob = schemas.ExportJob;
const exportJobProperties = [
  "id", "version", "workspace_id", "kind", "schema_version", "collection_id", "collection_version", "query_hash",
  "read_model_revision", "exact_count", "fields", "redaction_policy", "include_sensitive", "status", "file_hash", "file_size",
  "error_code", "error_message", "attempt_count", "expires_at", "created_at", "updated_at", "started_at", "completed_at",
  "download_count", "last_downloaded_at", "download_url",
];
const exportJobRequired = [
  "id", "version", "workspace_id", "kind", "schema_version", "collection_id", "collection_version", "query_hash", "fields",
  "redaction_policy", "include_sensitive", "status", "file_size", "attempt_count", "expires_at", "created_at", "updated_at", "download_count",
];
if (Object.keys(exportJob.properties).join(",") !== exportJobProperties.join(",") ||
    exportJob.required?.join(",") !== exportJobRequired.join(",") ||
    exportJob.properties.id.format !== "uuid" || exportJob.properties.workspace_id.format !== "uuid" || exportJob.properties.collection_id.format !== "uuid" ||
    exportJob.properties.version.minimum !== 1 || exportJob.properties.collection_version.minimum !== 1 ||
    exportJob.properties.kind.enum?.join(",") !== "MARKDOWN,METADATA_JSON" || exportJob.properties.schema_version.const !== "export/v1" ||
    exportJob.properties.query_hash.pattern !== "^[0-9a-f]{64}$" || exportJob.properties.read_model_revision.pattern !== "^[0-9a-f]{64}$" ||
    exportJob.properties.file_hash.pattern !== "^[0-9a-f]{64}$" || exportJob.properties.exact_count.maximum !== 10000 ||
    exportJob.properties.fields.minItems !== 1 || exportJob.properties.fields.maxItems !== 32 || exportJob.properties.fields.uniqueItems !== true ||
    exportJob.properties.fields.items.enum?.join(",") !== exportFields.join(",") ||
    exportJob.properties.redaction_policy.enum?.join(",") !== "MASKED,FULL" ||
    exportJob.properties.status.enum?.join(",") !== "PENDING,RUNNING,SUCCEEDED,FAILED,EXPIRED,CANCELLED") {
  throw new Error("ExportJob identity, snapshot, field or status projection drifted");
}
if (exportJob.dependentRequired?.read_model_revision?.join(",") !== "exact_count,file_hash" ||
    exportJob.dependentRequired?.exact_count?.join(",") !== "read_model_revision,file_hash" ||
    exportJob.dependentRequired?.file_hash?.join(",") !== "read_model_revision,exact_count") {
  throw new Error("ExportJob prepared result fields must remain an indivisible public binding");
}
const exportJobMaskedRule = exportJob.allOf?.find((rule) => rule.if?.properties?.redaction_policy?.const === "MASKED");
const exportJobFullRule = exportJob.allOf?.find((rule) => rule.if?.properties?.redaction_policy?.const === "FULL");
const exportJobDownloadRule = exportJob.allOf?.find((rule) => rule.if?.properties?.download_count?.const === 0);
if (exportJobMaskedRule?.then?.properties?.include_sensitive?.const !== false || exportJobFullRule?.then?.properties?.include_sensitive?.const !== true ||
    exportJobDownloadRule?.then?.not?.required?.[0] !== "last_downloaded_at" ||
    exportJobDownloadRule?.else?.required?.[0] !== "last_downloaded_at" ||
    exportJobDownloadRule?.else?.properties?.status?.enum?.join(",") !== "SUCCEEDED,EXPIRED") {
  throw new Error("ExportJob redaction or historical download-stat invariants drifted");
}
const exportStatusRules = new Map((exportJob.oneOf ?? []).map((rule) => [rule.properties?.status?.const, rule]));
if ([...exportStatusRules.keys()].join(",") !== "PENDING,RUNNING,SUCCEEDED,FAILED,EXPIRED,CANCELLED") {
  throw new Error("ExportJob must define exactly one strict schema branch for every public status");
}
const exportForbiddenFields = (rule) => new Set((rule?.not?.anyOf ?? []).flatMap((entry) => entry.required ?? []));
const exportPendingRule = exportStatusRules.get("PENDING");
const exportRunningRule = exportStatusRules.get("RUNNING");
const exportSucceededRule = exportStatusRules.get("SUCCEEDED");
const exportFailedRule = exportStatusRules.get("FAILED");
const exportExpiredRule = exportStatusRules.get("EXPIRED");
const exportCancelledRule = exportStatusRules.get("CANCELLED");
if (exportPendingRule?.properties?.file_size?.const !== 0 || exportPendingRule?.properties?.download_count?.const !== 0 ||
    !["read_model_revision", "exact_count", "file_hash", "started_at", "completed_at", "error_code", "error_message", "last_downloaded_at", "download_url"].every((field) => exportForbiddenFields(exportPendingRule).has(field)) ||
    !exportRunningRule?.required?.includes("started_at") || exportRunningRule?.properties?.download_count?.const !== 0 ||
    !["completed_at", "error_code", "error_message", "last_downloaded_at", "download_url"].every((field) => exportForbiddenFields(exportRunningRule).has(field)) ||
    !["read_model_revision", "exact_count", "file_hash", "started_at", "completed_at", "download_url"].every((field) => exportSucceededRule?.required?.includes(field)) ||
    !["error_code", "error_message"].every((field) => exportForbiddenFields(exportSucceededRule).has(field)) ||
    !["started_at", "completed_at", "error_code", "error_message"].every((field) => exportFailedRule?.required?.includes(field)) ||
    exportFailedRule?.properties?.download_count?.const !== 0 || !exportForbiddenFields(exportFailedRule).has("download_url") ||
    !exportExpiredRule?.required?.includes("completed_at") || !["error_code", "error_message", "download_url"].every((field) => exportForbiddenFields(exportExpiredRule).has(field)) ||
    ["read_model_revision", "exact_count", "file_hash", "last_downloaded_at"].some((field) => exportForbiddenFields(exportExpiredRule).has(field)) ||
    exportCancelledRule?.properties?.download_count?.const !== 0 || !["error_code", "error_message", "last_downloaded_at", "download_url"].every((field) => exportForbiddenFields(exportCancelledRule).has(field))) {
  throw new Error("ExportJob status/result/expiry field combinations drifted");
}

const exportPage = schemas.ExportPage;
if (exportPage.required?.join(",") !== "workspace_id,items" || Object.keys(exportPage.properties).join(",") !== "workspace_id,items,next_cursor" ||
    exportPage.properties.workspace_id.format !== "uuid" || exportPage.properties.items.maxItems !== 100 ||
    exportPage.properties.items.items?.$ref !== "#/components/schemas/ExportJob" ||
    exportPage.properties.next_cursor.minLength !== 1 || exportPage.properties.next_cursor.maxLength !== 4096 ||
    exportPage.properties.next_cursor["x-max-utf8-bytes"] !== 4096) {
  throw new Error("ExportPage Workspace, item or opaque cursor contract drifted");
}

const attachmentBasePath = "/api/v1/workspaces/{workspace_id}/attachment-exports";
const attachmentDetailPath = `${attachmentBasePath}/{export_id}`;
const attachmentDownloadPath = `${attachmentDetailPath}/download`;
const attachmentPathItem = document.paths[attachmentBasePath];
const attachmentWorkspaceParameter = attachmentPathItem.parameters?.[0];
if (attachmentPathItem.parameters?.length !== 1 || attachmentWorkspaceParameter?.name !== "workspace_id" ||
    attachmentWorkspaceParameter.in !== "path" || attachmentWorkspaceParameter.required !== true ||
    attachmentWorkspaceParameter.schema?.type !== "string" || attachmentWorkspaceParameter.schema?.format !== "uuid") {
  throw new Error("Attachment Export create/list must bind one UUID Workspace path parameter");
}
for (const path of [attachmentDetailPath, attachmentDownloadPath]) {
  const parameters = document.paths[path]?.parameters;
  if (parameters?.length !== 2 || parameters[0]?.name !== "workspace_id" || parameters[0]?.in !== "path" ||
      parameters[0]?.required !== true || parameters[0]?.schema?.format !== "uuid" ||
      parameters[1]?.name !== "export_id" || parameters[1]?.in !== "path" || parameters[1]?.required !== true ||
      parameters[1]?.schema?.format !== "uuid") {
    throw new Error(`${path} must bind strict Workspace and Export UUID path parameters`);
  }
}

const attachmentCreate = attachmentPathItem.post;
if (attachmentCreate.parameters?.length !== 1 || attachmentCreate.parameters[0]?.$ref !== "#/components/parameters/IdempotencyKey" ||
    attachmentCreate.requestBody?.required !== true || attachmentCreate.requestBody?.["x-max-body-bytes"] !== 65536 ||
    attachmentCreate.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/AttachmentExportCreateRequest") {
  throw new Error("Attachment Export create must use the strict 64 KiB idempotent JSON command contract");
}
for (const status of ["200", "202"]) {
  const response = attachmentCreate.responses[status];
  const schema = response.content?.["application/json"]?.schema;
  if (schema?.allOf?.length !== 2 || schema.allOf[0]?.$ref !== "#/components/schemas/AttachmentExportCreateResponse" ||
      schema.allOf[1]?.properties?.replayed?.const !== (status === "200") ||
      response.headers?.Location?.schema?.format !== "uri-reference" ||
      response.headers?.Location?.schema?.pattern !== "^/api/v1/workspaces/[0-9a-f-]{36}/attachment-exports/[0-9a-f-]{36}$" ||
      response.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
    throw new Error(`Attachment Export create ${status} response lost replay, Location or no-store binding`);
  }
}
if (Object.keys(attachmentCreate.responses).filter((status) => status.startsWith("2")).join(",") !== "200,202") {
  throw new Error("Attachment Export create must use only 202 for first acceptance and 200 for exact replay");
}

const attachmentList = attachmentPathItem.get;
const attachmentListLimit = attachmentList.parameters?.find((parameter) => parameter.name === "limit")?.schema;
const attachmentListCursor = attachmentList.parameters?.find((parameter) => parameter.name === "cursor")?.schema;
if (attachmentList.parameters?.length !== 2 || attachmentList.parameters.some((parameter) => parameter.name === "collection_id") ||
    attachmentListLimit?.type !== "integer" || attachmentListLimit.minimum !== 1 || attachmentListLimit.maximum !== 100 || attachmentListLimit.default !== 50 ||
    attachmentListCursor?.type !== "string" || attachmentListCursor.minLength !== 1 || attachmentListCursor.maxLength !== 4096 ||
    attachmentListCursor["x-max-utf8-bytes"] !== 4096 ||
    attachmentList.responses["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/AttachmentExportPage" ||
    attachmentList.responses["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
  throw new Error("Attachment Export list scope, cursor, limit or response contract drifted");
}
const attachmentGet = document.paths[attachmentDetailPath].get;
if (attachmentGet.responses["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/AttachmentExportJob" ||
    attachmentGet.responses["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
  throw new Error("Attachment Export detail must return the strict no-store tagged Job projection");
}

const attachmentDownload = document.paths[attachmentDownloadPath].get;
const attachmentDownloadResponse = attachmentDownload.responses["200"];
const attachmentDownloadHeaders = attachmentDownloadResponse.headers;
if (Object.keys(attachmentDownloadResponse.content ?? {}).join(",") !== "application/zip" ||
    attachmentDownloadResponse.content?.["application/zip"]?.schema?.type !== "string" ||
    attachmentDownloadResponse.content?.["application/zip"]?.schema?.format !== "binary" ||
    attachmentDownloadHeaders?.["Content-Disposition"]?.schema?.pattern !== "^attachment; filename=\\\"workspace-attachments-[0-9a-f-]{36}\\.zip\\\"$" ||
    attachmentDownloadHeaders?.["Content-Length"]?.schema?.type !== "integer" ||
    attachmentDownloadHeaders?.["Content-Length"]?.schema?.minimum !== 0 ||
    attachmentDownloadHeaders?.["Content-Length"]?.schema?.maximum !== 1073741824 ||
    attachmentDownloadHeaders?.["Cache-Control"]?.schema?.const !== "private, no-store" ||
    attachmentDownloadHeaders?.["X-Content-Type-Options"]?.schema?.const !== "nosniff" ||
    attachmentDownload.responses["410"]?.$ref !== "#/components/responses/Gone") {
  throw new Error("Attachment Export download ZIP, filename, length, cache, nosniff or expiry contract drifted");
}

for (const schemaName of ["AttachmentExportCreateRequest", "AttachmentExportCreateResponse", "AttachmentExportJob", "AttachmentExportPage"]) {
  if (schemas[schemaName]?.type !== "object" || schemas[schemaName].additionalProperties !== false) {
    throw new Error(`${schemaName} must remain a strict object schema`);
  }
}
const attachmentCreateRequest = schemas.AttachmentExportCreateRequest;
const attachmentRequestProperties = ["kind", "schema_version", "attachment_root_contract_version", "content_policy", "expires_in_seconds"];
if (attachmentCreateRequest.required?.join(",") !== "kind,schema_version,attachment_root_contract_version,content_policy" ||
    Object.keys(attachmentCreateRequest.properties).join(",") !== attachmentRequestProperties.join(",") ||
    attachmentCreateRequest.properties.kind.const !== "ATTACHMENTS_ZIP" ||
    attachmentCreateRequest.properties.schema_version.const !== "attachment-export/v1" ||
    attachmentCreateRequest.properties.attachment_root_contract_version.const !== "workspace-attachments/v1" ||
    attachmentCreateRequest.properties.content_policy.const !== "RAW_USER_OWNED" ||
    attachmentCreateRequest.properties.expires_in_seconds.minimum !== 1 ||
    attachmentCreateRequest.properties.expires_in_seconds.maximum !== 604800 ||
    attachmentCreateRequest.properties.expires_in_seconds.default !== 86400) {
  throw new Error("AttachmentExportCreateRequest tagged scope, policy or TTL contract drifted");
}
const attachmentCreateResponse = schemas.AttachmentExportCreateResponse;
if (attachmentCreateResponse.required?.join(",") !== "job,replayed,dispatch_pending" ||
    Object.keys(attachmentCreateResponse.properties).join(",") !== "job,replayed,dispatch_pending" ||
    attachmentCreateResponse.properties.job?.$ref !== "#/components/schemas/AttachmentExportJob" ||
    attachmentCreateResponse.properties.replayed?.type !== "boolean" ||
    attachmentCreateResponse.properties.dispatch_pending?.type !== "boolean") {
  throw new Error("AttachmentExportCreateResponse replay or dispatch projection drifted");
}

const attachmentJob = schemas.AttachmentExportJob;
const attachmentJobProperties = [
  "id", "workspace_id", "scope_kind", "kind", "schema_version", "attachment_root_contract_version", "content_policy",
  "status", "version", "manifest_sha256", "entry_count", "total_uncompressed_bytes", "archive_sha256", "archive_size",
  "error_code", "error_message", "attempt_count", "expires_at", "created_at", "updated_at", "started_at", "completed_at",
  "download_count", "last_downloaded_at", "download_url",
];
const attachmentJobRequired = [
  "id", "workspace_id", "scope_kind", "kind", "schema_version", "attachment_root_contract_version", "content_policy",
  "status", "version", "attempt_count", "expires_at", "created_at", "updated_at", "download_count",
];
if (Object.keys(attachmentJob.properties).join(",") !== attachmentJobProperties.join(",") ||
    attachmentJob.required?.join(",") !== attachmentJobRequired.join(",") ||
    attachmentJob.properties.id.format !== "uuid" || attachmentJob.properties.workspace_id.format !== "uuid" ||
    attachmentJob.properties.scope_kind.const !== "WORKSPACE_ATTACHMENTS" || attachmentJob.properties.kind.const !== "ATTACHMENTS_ZIP" ||
    attachmentJob.properties.schema_version.const !== "attachment-export/v1" ||
    attachmentJob.properties.attachment_root_contract_version.const !== "workspace-attachments/v1" ||
    attachmentJob.properties.content_policy.const !== "RAW_USER_OWNED" ||
    attachmentJob.properties.status.enum?.join(",") !== "PENDING,RUNNING,SUCCEEDED,FAILED,EXPIRED,CANCELLED" ||
    attachmentJob.properties.manifest_sha256.pattern !== "^[0-9a-f]{64}$" ||
    attachmentJob.properties.archive_sha256.pattern !== "^[0-9a-f]{64}$" ||
    attachmentJob.properties.entry_count.maximum !== 10000 ||
    attachmentJob.properties.total_uncompressed_bytes.maximum !== 1073741824 ||
    attachmentJob.properties.archive_size.maximum !== 1073741824 ||
    attachmentJob.properties.download_url.pattern !== "^/api/v1/workspaces/[0-9a-f-]{36}/attachment-exports/[0-9a-f-]{36}/download$") {
  throw new Error("AttachmentExportJob tagged identity, archive limits or public fields drifted");
}
const attachmentPreparedFields = ["manifest_sha256", "entry_count", "total_uncompressed_bytes", "archive_sha256", "archive_size"];
for (const field of attachmentPreparedFields) {
  const requiredPeers = attachmentPreparedFields.filter((peer) => peer !== field);
  if (attachmentJob.dependentRequired?.[field]?.join(",") !== requiredPeers.join(",")) {
    throw new Error("AttachmentExportJob prepared archive facts must remain an indivisible binding");
  }
}
const attachmentStatusRules = new Map((attachmentJob.oneOf ?? []).map((rule) => [rule.properties?.status?.const, rule]));
if ([...attachmentStatusRules.keys()].join(",") !== "PENDING,RUNNING,SUCCEEDED,FAILED,EXPIRED,CANCELLED") {
  throw new Error("AttachmentExportJob must define exactly one strict branch for every public status");
}
const attachmentPendingRule = attachmentStatusRules.get("PENDING");
const attachmentRunningRule = attachmentStatusRules.get("RUNNING");
const attachmentSucceededRule = attachmentStatusRules.get("SUCCEEDED");
const attachmentFailedRule = attachmentStatusRules.get("FAILED");
const attachmentExpiredRule = attachmentStatusRules.get("EXPIRED");
if (attachmentPendingRule?.properties?.download_count?.const !== 0 ||
    !attachmentRunningRule?.required?.includes("started_at") || attachmentRunningRule?.properties?.download_count?.const !== 0 ||
    ![...attachmentPreparedFields, "started_at", "completed_at", "download_url"].every((field) => attachmentSucceededRule?.required?.includes(field)) ||
    !["started_at", "completed_at", "error_code", "error_message"].every((field) => attachmentFailedRule?.required?.includes(field)) ||
    !attachmentExpiredRule?.required?.includes("completed_at")) {
  throw new Error("AttachmentExportJob status, archive and failure field combinations drifted");
}

const attachmentPage = schemas.AttachmentExportPage;
if (attachmentPage.required?.join(",") !== "workspace_id,scope_kind,items" ||
    Object.keys(attachmentPage.properties).join(",") !== "workspace_id,scope_kind,items,next_cursor" ||
    attachmentPage.properties.workspace_id.format !== "uuid" ||
    attachmentPage.properties.scope_kind.const !== "WORKSPACE_ATTACHMENTS" ||
    attachmentPage.properties.items.maxItems !== 100 ||
    attachmentPage.properties.items.items?.$ref !== "#/components/schemas/AttachmentExportJob" ||
    attachmentPage.properties.next_cursor.minLength !== 1 || attachmentPage.properties.next_cursor.maxLength !== 4096 ||
    attachmentPage.properties.next_cursor["x-max-utf8-bytes"] !== 4096) {
  throw new Error("AttachmentExportPage Workspace, scope, item or opaque cursor contract drifted");
}

for (const schemaName of [
  "CreateConversationRequest", "Conversation", "ConversationPage", "QuestionScopeRequest", "QuestionScope", "SubmitQuestionRequest", "Question",
  "WorkflowProjection", "QuestionAcceptance", "AnswerCitation", "RAGResultCitation", "RAGAssertion", "RAGConflictPosition", "RelatedTopic", "RAGAnswerPayload", "RAGAnswerResult", "RefusalResult",
  "ClarificationResult", "RetrievalDegradation", "RetrievalScopeSummary", "RetrievalSummary", "Answer", "Turn", "TurnPage",
  "SubmitFeedbackRequest", "AnswerFeedback", "ServerEventPayloadSummary", "ServerEventEnvelope",
  "AuthCapabilityStatus", "SessionCredential", "SessionInfo", "CreateAPITokenRequest", "APITokenInfo", "APITokenCredential", "APITokenPage",
  "ArtifactImpactBinding", "ReviewCardImpactBinding", "KnowledgeEventOperator", "ArtifactEventOwnerBinding", "ReviewCardEventOwnerBinding",
  "KnowledgeEventCorrelation", "KnowledgeEventV1", "KnowledgeEventV2", "KnowledgeTimelinePage", "ImpactAnalysisRequest",
  "ImpactObjectV1", "ImpactReportV1", "ImpactReportV2", "ImpactProposalDraft", "ImpactAnalysisResult",
  "GraphCanonicalJSONObject", "GraphNodeRef", "GraphApplicability", "GraphTopicNode", "GraphClaimNode",
  "GraphConfirmation", "GraphEdge", "GraphPageMeta", "GraphFilter", "GraphGlobalRequest", "GraphGlobalCluster",
  "GraphGlobalResponse", "GraphNodeSearchMatch", "GraphNodeSearchResponse", "GraphNeighborhoodRequest",
  "GraphNeighborhoodResponse", "GraphPathRequest", "GraphPathResponse", "GraphRelationDetailResponse",
  "GraphProvenance", "GraphRelationEvidenceItem", "GraphRelationEvidenceResponse",
  "SemanticLinkCandidateEndpoint", "SemanticLinkCandidateEvidence", "SemanticLinkGeneration", "SemanticLinkCandidate",
  "SemanticLinkCandidatePage", "SemanticLinkCandidateDecisionReceipt",
  "FilePatchProposal", "KnowledgeChangeTargetRef", "KnowledgeChangeBaseVersion", "KnowledgeChangeEndpoint",
  "KnowledgeChangeSet", "KnowledgeChangeEvidenceRef", "KnowledgeChangeRevision", "KnowledgeChangeProposal",
  "PublishArtifactCoverage", "PublishArtifactBinding", "PublishArtifactRevision", "PublishArtifactProposal",
  "CreateDownstreamUpdateProposalRequest", "DownstreamUpdateSourceReport", "DownstreamUpdateSourceEvent", "DownstreamUpdate", "DownstreamUpdateRevision", "DownstreamUpdateProposal",
]) {
  if (schemas[schemaName].additionalProperties !== false) throw new Error(`${schemaName} must reject unknown properties`);
}

const reviewOperations = [
  ["/api/v1/review/decks", "get", "ReviewDeckList", undefined, ["200", "400", "405", "500", "503"], false],
  ["/api/v1/review/decks", "post", "ReviewDeck", "ReviewCreateDeckRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/decks/{deck_id}", "get", "ReviewDeck", undefined, ["200", "400", "404", "405", "500", "503"], false],
  ["/api/v1/review/decks/{deck_id}/cards", "get", "ReviewCardList", undefined, ["200", "400", "404", "405", "500", "503"], false],
  ["/api/v1/review/decks/{deck_id}/cards", "post", "ReviewCard", "ReviewCreateCardRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/cards/{card_id}", "put", "ReviewCard", "ReviewEditCardRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/decks/{deck_id}/schedule/pause", "post", "ReviewDeck", "ReviewDeckScheduleRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/decks/{deck_id}/schedule/resume", "post", "ReviewDeck", "ReviewDeckScheduleRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/decks/{deck_id}/schedule/reset", "post", "ReviewDeck", "ReviewDeckScheduleRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/due", "get", "ReviewDueCardList", undefined, ["200", "400", "404", "405", "409", "500", "503"], false],
  ["/api/v1/review/cards/{card_id}/approve", "post", "ReviewCard", "ReviewCardDecisionRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/cards/{card_id}/reject", "post", "ReviewCard", "ReviewCardDecisionRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/cards/{card_id}/invalidate", "post", "ReviewCard", "ReviewCardDecisionRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/invalidation", "post", "ReviewInvalidationResult", "ReviewInvalidationRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/sessions", "post", "ReviewSession", "ReviewStartSessionRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/sessions/{session_id}/complete", "post", "ReviewSession", "ReviewCompleteSessionRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/review/sessions/{session_id}/answers", "post", "ReviewAnswerResult", "ReviewSubmitAnswerRequest", ["200", "201", "400", "404", "405", "409", "415", "500", "503"], true],
];
for (const [path, method, successSchema, requestSchema, statuses, requiresIdempotencyKey] of reviewOperations) {
  const pathItem = document.paths[path];
  const operation = pathItem?.[method];
  if (!operation) throw new Error(`missing Review operation ${method.toUpperCase()} ${path}`);
  const expectedCapability = method === "get" ? "READ_LOCAL" : "WRITE_PROPOSAL";
  if (operation.security !== undefined || operation["x-required-capability"] !== expectedCapability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${expectedCapability}`);
  }
  for (const status of ["401", "403", ...statuses]) {
    if (!operation.responses?.[status]) throw new Error(`missing Review ${status} response for ${method.toUpperCase()} ${path}`);
    if (Number(status) >= 400 && resolveRef(operation.responses[status])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Review ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
  if (successSchema) {
    for (const status of statuses.filter((status) => Number(status) < 300)) {
      if (operation.responses[status]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}`) {
        throw new Error(`invalid Review ${status} success schema for ${method.toUpperCase()} ${path}`);
      }
    }
  }
  if (requestSchema && operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}`) {
    throw new Error(`invalid Review request schema for ${method.toUpperCase()} ${path}`);
  }
  if (requiresIdempotencyKey && ![...(pathItem.parameters ?? []), ...(operation.parameters ?? [])].some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey")) {
    throw new Error(`${method.toUpperCase()} ${path} must require Idempotency-Key`);
  }
}
for (const schemaName of [
  "ReviewEvidenceBinding", "ReviewDeck", "ReviewDeckList", "ReviewCard", "ReviewCardList", "ReviewSchedule", "ReviewDueCardSummary", "ReviewDueCard", "ReviewDueCardList", "ReviewSession",
  "ReviewCreateDeckRequest", "ReviewCreateCardRequest", "ReviewEditCardRequest", "ReviewCardDecisionRequest", "ReviewInvalidationRequest", "ReviewInvalidationResult", "ReviewStartSessionRequest", "ReviewCompleteSessionRequest", "ReviewDeckScheduleRequest", "ReviewSubmitAnswerRequest",
  "ReviewScoreDimension", "ReviewScoreEvidence", "ReviewScore", "ReviewAnswer", "ReviewAnswerResult",
]) {
  if (!schemas[schemaName] || schemas[schemaName].additionalProperties !== false) throw new Error(`${schemaName} must remain a strict Review schema`);
}
for (const schemaName of ["ReviewDeck", "ReviewCreateDeckRequest"]) {
  const dailyLimit = schemas[schemaName].properties.daily_limit;
  if (dailyLimit?.type !== "integer" || dailyLimit.minimum !== 1 || dailyLimit.maximum !== 1000) {
    throw new Error(`${schemaName}.daily_limit must match the 1..1000 domain bound`);
  }
}
for (const schemaName of ["ReviewCard", "ReviewCreateCardRequest", "ReviewEditCardRequest"]) {
  const properties = schemas[schemaName].properties;
  if (properties.question?.["x-max-utf8-bytes"] !== 8192 || properties.answer_points?.maxItems !== 128 || properties.answer_points?.items?.["x-max-utf8-bytes"] !== 4096 || properties.evidence?.maxItems !== 128) {
    throw new Error(`${schemaName} card bounds drifted from the Review domain`);
  }
}
if (schemas.ReviewEvidenceBinding.properties.quote !== undefined || schemas.ReviewScoreEvidence.properties.quote !== undefined) {
  throw new Error("Review evidence bindings must not expose a client-authored quote field");
}
const reviewDecisionReason = schemas.ReviewCardDecisionRequest.properties.reason;
if (reviewDecisionReason?.maxLength !== 1024 || reviewDecisionReason?.["x-max-utf8-bytes"] !== 1024) {
  throw new Error("Review card invalidation reason must match the 1024-byte domain bound");
}
const reviewDueCardSummary = schemas.ReviewDueCardSummary;
const reviewDueCardFields = ["id", "workspace_id", "deck_id", "question", "card_type", "difficulty", "status", "version"];
const reviewDueForbiddenFields = ["claim_id", "answer_points", "evidence", "fingerprint", "model_version", "invalidation_reason", "invalidated_at", "created_at", "updated_at"];
if (reviewDueCardSummary.required?.join(",") !== reviewDueCardFields.join(",") ||
    Object.keys(reviewDueCardSummary.properties ?? {}).join(",") !== reviewDueCardFields.join(",") ||
    reviewDueForbiddenFields.some((field) => field in reviewDueCardSummary.properties) ||
    reviewDueCardSummary.properties.status?.const !== "APPROVED" ||
    schemas.ReviewDueCard.properties.card?.$ref !== "#/components/schemas/ReviewDueCardSummary" ||
    schemas.ReviewDueCard.required?.join(",") !== "card,schedule,question_ref" ||
    schemas.ReviewDueCard.properties.question_ref?.["x-max-utf8-bytes"] !== 512) {
  throw new Error("Review due cards must use the redacted pre-answer summary wire shape");
}
const reviewDueOperation = document.paths["/api/v1/review/due"].get;
const reviewCardListOperation = document.paths["/api/v1/review/decks/{deck_id}/cards"].get;
const reviewDueSessionParameter = reviewDueOperation.parameters?.find((parameter) => parameter.name === "session_id" && parameter.in === "query");
if (reviewDueSessionParameter?.required !== true || reviewDueSessionParameter.schema?.format !== "uuid" ||
    schemas.ReviewDueCardList.required?.join(",") !== "workspace_id,items" ||
    reviewDueOperation.responses["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
    reviewCardListOperation.responses["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
  throw new Error("Review due must bind an active Session and private reads must remain no-store");
}
const reviewSession = schemas.ReviewSession;
const reviewStartSession = schemas.ReviewStartSessionRequest;
if (reviewSession.required?.join(",") !== "id,workspace_id,deck_id,session_type,status,config,started_at" ||
    reviewSession.properties.session_type?.const !== "REVIEW" ||
    reviewStartSession.required?.join(",") !== "workspace_id,deck_id,session_type" ||
    reviewStartSession.properties.session_type?.const !== "REVIEW") {
  throw new Error("Review session API must require a Deck-bound REVIEW session");
}
const reviewAnswerRequest = schemas.ReviewSubmitAnswerRequest;
if (reviewAnswerRequest.properties.score || reviewAnswerRequest.properties.feedback ||
    reviewAnswerRequest.required.includes("score") || reviewAnswerRequest.required.includes("feedback") ||
    reviewAnswerRequest.required.join(",") !== "workspace_id,card_id,question_ref,user_answer,rating" ||
    !schemas.ReviewCard.required.includes("claim_id")) {
  throw new Error("Review answer ingress must not accept client score or feedback");
}
const reviewAnswerOperation = document.paths["/api/v1/review/sessions/{session_id}/answers"].post;
if (reviewAnswerOperation.requestBody?.required !== true || reviewAnswerOperation.requestBody?.["x-max-body-bytes"] !== 131072 ||
    reviewAnswerOperation.responses["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ReviewAnswerResult" ||
    reviewAnswerOperation.responses["201"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ReviewAnswerResult" ||
    reviewAnswerOperation.responses["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
    reviewAnswerOperation.responses["201"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
    schemas.ReviewAnswer.required?.includes("scorer_version") !== true ||
    schemas.ReviewAnswer.properties.scorer_version?.["x-max-utf8-bytes"] !== 128) {
  throw new Error("Review answer command must be bounded and declare durable scoring success");
}
const reviewScoringUnavailable = resolveRef(reviewAnswerOperation.responses["503"]);
const reviewScoringExample = reviewScoringUnavailable?.content?.["application/json"]?.examples?.review_scoring_unavailable?.value;
if (reviewScoringExample?.error_code !== "REVIEW_SCORING_UNAVAILABLE" || reviewScoringExample.retryable !== true) {
  throw new Error("Review answer 503 must declare retryable REVIEW_SCORING_UNAVAILABLE semantics");
}
const reviewScore = schemas.ReviewScore;
if (reviewScore.required?.join(",") !== "schema_version,correctness,coverage,boundaries,clarity,confidence,evidence" ||
    ["correctness", "coverage", "boundaries", "clarity", "confidence"].some((field) => reviewScore.properties[field]?.$ref !== "#/components/schemas/ReviewScoreDimension") ||
    reviewScore.properties.evidence?.minItems !== 1 || reviewScore.properties.evidence?.maxItems !== 128 ||
    reviewScore.properties.evidence?.items?.$ref !== "#/components/schemas/ReviewScoreEvidence") {
  throw new Error("Review answer success must declare the server-generated evidence-bound score");
}
const reviewInvalidation = schemas.ReviewInvalidationRequest;
if (reviewInvalidation.required?.join(",") !== "workspace_id,reason" ||
    reviewInvalidation.properties.reason?.["x-max-utf8-bytes"] !== 1024 ||
    reviewInvalidation.anyOf?.map((item) => item.required?.join(",")).join(",") !== "claim_id,source_version_id,source_span_id" ||
    schemas.ReviewInvalidationResult.required?.join(",") !== "workspace_id,invalidated_count,has_more,replayed" ||
    schemas.ReviewInvalidationResult.properties.invalidated_count?.maximum !== 200 ||
    schemas.ReviewInvalidationResult.properties.cards !== undefined) {
  throw new Error("Review invalidation selector and replay result contract drifted");
}

const reviewLearningPathOperations = [
  ["/api/v1/review/answers/{answer_id}/learning-path", "get", "200", "ReviewLearningPathResult", ["400", "401", "403", "404", "405", "500", "503"]],
  ["/api/v1/review/answers/{answer_id}/learning-path", "post", "201", "ReviewLearningPathResult", ["200", "400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/review/answers/{answer_id}/learning-path/status", "put", "200", "ReviewLearningPathStatusResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/review/answers/{answer_id}/learning-path/steps/{step_id}", "put", "200", "ReviewLearningPathStepResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
];
for (const [path, method, successStatus, successSchema, statuses] of reviewLearningPathOperations) {
  const pathItem = document.paths[path];
  const operation = pathItem?.[method];
  if (!operation) throw new Error(`missing Review Learning Path operation ${method.toUpperCase()} ${path}`);
  const expectedCapability = method === "get" ? "READ_LOCAL" : "WRITE_PROPOSAL";
  if (operation.security !== undefined || operation["x-required-capability"] !== expectedCapability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${expectedCapability}`);
  }
  if (operation.responses?.[successStatus]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}` ||
      operation.responses?.[successStatus]?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
    throw new Error(`invalid Review Learning Path ${successStatus} success contract for ${method.toUpperCase()} ${path}`);
  }
  for (const status of statuses) {
    const response = resolveRef(operation.responses?.[status]);
    if (status === "200" && successStatus === "201") {
      if (response?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}` ||
          response?.headers?.["Cache-Control"]?.schema?.const !== "no-store") {
        throw new Error(`Review Learning Path replay must return ${successSchema}`);
      }
      continue;
    }
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Review Learning Path ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
for (const [path, method, requestSchema] of [
  ["/api/v1/review/answers/{answer_id}/learning-path", "post", "CreateReviewLearningPathRequest"],
  ["/api/v1/review/answers/{answer_id}/learning-path/status", "put", "UpdateReviewLearningPathStatusRequest"],
  ["/api/v1/review/answers/{answer_id}/learning-path/steps/{step_id}", "put", "UpdateReviewLearningPathStepRequest"],
]) {
  const operation = document.paths[path]?.[method];
  if (!operation?.parameters?.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") ||
      operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== 131072 ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}`) {
    throw new Error(`Review Learning Path mutation contract drifted for ${method.toUpperCase()} ${path}`);
  }
}
const reviewLearningPathGet = document.paths["/api/v1/review/answers/{answer_id}/learning-path"]?.get;
if (!reviewLearningPathGet?.parameters?.some((parameter) => parameter.$ref === "#/components/parameters/WorkspaceIDQuery")) {
  throw new Error("Review Learning Path recovery must remain Workspace-scoped");
}
for (const [path, ids] of [
  ["/api/v1/review/answers/{answer_id}/learning-path", ["answer_id"]],
  ["/api/v1/review/answers/{answer_id}/learning-path/status", ["answer_id"]],
  ["/api/v1/review/answers/{answer_id}/learning-path/steps/{step_id}", ["answer_id", "step_id"]],
]) {
  const pathItem = document.paths[path];
  const parameters = [...(pathItem?.parameters ?? []), ...(pathItem?.get?.parameters ?? []), ...(pathItem?.post?.parameters ?? []), ...(pathItem?.put?.parameters ?? [])];
  for (const id of ids) {
    if (!parameters.some((parameter) => parameter.name === id && parameter.in === "path" && parameter.required === true && parameter.schema?.format === "uuid")) {
      throw new Error(`Review Learning Path ${id} path contract drifted for ${path}`);
    }
  }
}
for (const schemaName of [
  "CreateReviewLearningPathRequest", "UpdateReviewLearningPathStatusRequest", "UpdateReviewLearningPathStepRequest",
  "ReviewLearningPath", "ReviewLearningPathStep", "ReviewLearningPathResult", "ReviewLearningPathStatusResult", "ReviewLearningPathStepResult",
]) {
  if (!schemas[schemaName] || schemas[schemaName].additionalProperties !== false) {
    throw new Error(`${schemaName} must remain a strict Review Learning Path schema`);
  }
}
const reviewLearningPath = schemas.ReviewLearningPath;
const reviewLearningPathStep = schemas.ReviewLearningPathStep;
if (reviewLearningPath.required?.join(",") !== "id,workspace_id,origin_type,review_answer_id,artifact,source_policy_version,status,version,created_at,updated_at" ||
    Object.keys(reviewLearningPath.properties ?? {}).join(",") !== "id,workspace_id,origin_type,review_answer_id,artifact,source_policy_version,status,version,created_at,updated_at" ||
    reviewLearningPath.properties.origin_type?.const !== "REVIEW" ||
    reviewLearningPath.properties.artifact?.$ref !== "#/components/schemas/LearningPathArtifactBinding" ||
    reviewLearningPath.properties.source_policy_version?.["x-max-utf8-bytes"] !== 128 ||
    reviewLearningPath.properties.status?.enum?.join(",") !== "ACTIVE,PAUSED,COMPLETED" ||
    reviewLearningPath.properties.version?.minimum !== 1 ||
    reviewLearningPathStep.required?.join(",") !== "id,workspace_id,path_id,step_no,claim_id,source_version_id,source_span_id,evidence_hash,title,rationale,status,version,created_at,updated_at" ||
    Object.keys(reviewLearningPathStep.properties ?? {}).join(",") !== "id,workspace_id,path_id,step_no,claim_id,topic_id,source_version_id,source_span_id,evidence_hash,title,rationale,status,version,created_at,updated_at" ||
    reviewLearningPathStep.properties.step_no?.minimum !== 1 ||
    reviewLearningPathStep.properties.evidence_hash?.pattern !== "^[0-9a-f]{64}$" ||
    reviewLearningPathStep.properties.title?.["x-max-utf8-bytes"] !== 512 ||
    reviewLearningPathStep.properties.rationale?.["x-max-utf8-bytes"] !== 4096 ||
    reviewLearningPathStep.properties.status?.enum?.join(",") !== "PENDING,IN_PROGRESS,COMPLETED,SKIPPED" ||
    reviewLearningPathStep.properties.version?.minimum !== 1) {
  throw new Error("Review Learning Path public DTO drifted from the strict frontend contract");
}
if (schemas.CreateReviewLearningPathRequest.required?.join(",") !== "workspace_id" ||
    Object.keys(schemas.CreateReviewLearningPathRequest.properties ?? {}).join(",") !== "workspace_id" ||
    schemas.UpdateReviewLearningPathStatusRequest.required?.join(",") !== "workspace_id,expected_version,status" ||
    schemas.UpdateReviewLearningPathStepRequest.required?.join(",") !== "workspace_id,expected_version,status" ||
    schemas.UpdateReviewLearningPathStatusRequest.properties.status?.enum?.join(",") !== "ACTIVE,PAUSED,COMPLETED" ||
    schemas.UpdateReviewLearningPathStepRequest.properties.status?.enum?.join(",") !== "IN_PROGRESS,COMPLETED,SKIPPED" ||
    schemas.UpdateReviewLearningPathStatusRequest.properties.expected_version?.minimum !== 1 ||
    schemas.UpdateReviewLearningPathStepRequest.properties.expected_version?.minimum !== 1 ||
    schemas.ReviewLearningPathResult.required?.join(",") !== "path,steps,replayed" ||
    schemas.ReviewLearningPathResult.properties.path?.$ref !== "#/components/schemas/ReviewLearningPath" ||
    schemas.ReviewLearningPathResult.properties.steps?.items?.$ref !== "#/components/schemas/ReviewLearningPathStep" ||
    schemas.ReviewLearningPathResult.properties.steps?.maxItems !== 40 ||
    schemas.ReviewLearningPathStatusResult.required?.join(",") !== "path,replayed" ||
    schemas.ReviewLearningPathStepResult.required?.join(",") !== "path,step,replayed") {
  throw new Error("Review Learning Path command, replay, or result shape drifted");
}

const artifactOperations = [
  ["/api/v1/artifacts", "get", "ArtifactPage", undefined, ["400", "405", "500", "503"], false],
  ["/api/v1/artifacts", "post", "ArtifactCommandResult", "ArtifactPlanRequest", ["200", "201", "400", "405", "409", "415", "500", "503"], true],
  ["/api/v1/artifacts/{artifact_id}", "get", "Artifact", undefined, ["400", "404", "405", "500", "503"], false],
  ["/api/v1/artifacts/{artifact_id}/outline", "post", "ArtifactCommandResult", "ArtifactOutlineRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/artifacts/{artifact_id}/outline/approve", "post", "ArtifactCommandResult", "ArtifactRevisionRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/artifacts/{artifact_id}/revisions", "post", "ArtifactCommandResult", "ArtifactRevisionRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/artifacts/{artifact_id}/sections", "post", "ArtifactCommandResult", "ArtifactRecordSectionRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/artifacts/{artifact_id}/sections/generate", "post", "ArtifactSectionGenerationAcceptance", "ArtifactSectionGenerationRequest", ["202", "400", "404", "405", "409", "415", "500", "503"], true, "202"],
  ["/api/v1/artifacts/{artifact_id}/section-generations", "get", "ArtifactSectionGenerationReadResponse", undefined, ["400", "404", "405", "500", "503"], false],
  ["/api/v1/artifacts/{artifact_id}/draft/approve", "post", "ArtifactCommandResult", "ArtifactRevisionRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/artifacts/{artifact_id}/exports/markdown", "post", "ArtifactCommandResult", "ArtifactRevisionRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
  ["/api/v1/artifacts/{artifact_id}/exports/{export_id}", "get", "ArtifactExport", undefined, ["400", "404", "405", "500", "503"], false],
  ["/api/v1/artifacts/{artifact_id}/publish-proposals", "post", "ArtifactCommandResult", "ArtifactRevisionRequest", ["200", "400", "404", "405", "409", "415", "500", "503"], true],
];
const artifactPaths = Object.keys(document.paths).filter((path) => path.startsWith("/api/v1/artifacts"));
if (artifactOperations.length !== 13 || artifactPaths.length !== 12) {
  throw new Error("Artifact HTTP contract must expose twelve URI templates and thirteen operations");
}
for (const [path, method, successSchema, requestSchema, statuses, mutation, successStatus = "200"] of artifactOperations) {
  const pathItem = document.paths[path];
  const operation = pathItem?.[method];
  if (!operation) throw new Error(`missing Artifact operation ${method.toUpperCase()} ${path}`);
  if (operation.security?.length === 0) throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication`);
  if (operation.responses?.[successStatus]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}`) {
    throw new Error(`invalid Artifact success schema for ${method.toUpperCase()} ${path}`);
  }
  for (const status of ["401", "403", ...statuses]) {
    if (Number(status) < 400) continue;
    const response = resolveRef(operation.responses?.[status]);
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Artifact ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
  const parameters = [...(pathItem.parameters ?? []), ...(operation.parameters ?? [])];
  if (mutation) {
    if (!parameters.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") ||
        operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== 131072 ||
        operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}`) {
      throw new Error(`${method.toUpperCase()} ${path} must retain bounded idempotent Artifact command input`);
    }
  } else if (!parameters.some((parameter) => parameter.$ref === "#/components/parameters/WorkspaceIDQuery")) {
    throw new Error(`${method.toUpperCase()} ${path} must remain Workspace-bound`);
  }
}
const artifactList = document.paths["/api/v1/artifacts"].get;
const artifactLimit = artifactList.parameters.find((parameter) => parameter.name === "limit")?.schema;
const artifactCursor = artifactList.parameters.find((parameter) => parameter.name === "cursor")?.schema;
if (artifactLimit?.minimum !== 1 || artifactLimit.maximum !== 100 || artifactLimit.default !== 50 ||
    artifactCursor?.minLength !== 1 || artifactCursor.maxLength !== 1366 ||
    schemas.ArtifactPage.properties.items.maxItems !== 100 || schemas.ArtifactPage.properties.next_cursor.maxLength !== 1366) {
  throw new Error("Artifact cursor and page bounds drifted from the HTTP handler");
}
for (const schemaName of [
  "ArtifactOutlineSection", "ArtifactGap", "ArtifactCoverage", "ArtifactGenerationMetadata", "ArtifactCitationInput", "ArtifactCitation", "ArtifactDocumentSource",
  "ArtifactSectionInput", "ArtifactSection", "ArtifactPlanRequest", "ArtifactOutlineRequest", "ArtifactRevisionRequest", "ArtifactRecordSectionRequest",
  "ArtifactSectionGenerationRequest", "ArtifactSectionGenerationAcceptance", "ArtifactSectionGenerationReadItem", "ArtifactSectionGenerationReadResponse",
  "ArtifactRevision", "Artifact", "ArtifactPage", "ArtifactExport", "ArtifactPublication", "ArtifactCommandResult",
]) {
  if (!schemas[schemaName] || schemas[schemaName].additionalProperties !== false) throw new Error(`${schemaName} must remain a strict Artifact schema`);
}
for (const requestName of ["ArtifactOutlineRequest", "ArtifactRevisionRequest", "ArtifactRecordSectionRequest", "ArtifactSectionGenerationRequest"]) {
  if (!schemas[requestName].required.includes("workspace_id") || !schemas[requestName].required.includes("expected_version") ||
      schemas[requestName].properties.expected_version.minimum !== 1) {
    throw new Error(`${requestName} must bind Workspace and compare-and-swap version`);
  }
}
if (schemas.ArtifactPlanRequest.required.join(",") !== "workspace_id,type,title,scope_definition" ||
    schemas.ArtifactOutlineRequest.properties.outline.minItems !== 1 ||
    schemas.ArtifactRecordSectionRequest.required.join(",") !== "workspace_id,expected_version,section" ||
    Object.keys(schemas.ArtifactRecordSectionRequest.properties).sort().join(",") !== "expected_version,section,workspace_id" ||
    schemas.ArtifactSectionGenerationRequest.required.join(",") !== "workspace_id,expected_version,section_key" ||
    Object.keys(schemas.ArtifactSectionGenerationRequest.properties).sort().join(",") !== "expected_version,section_key,workspace_id" ||
    schemas.ArtifactSectionGenerationRequest.properties.section_key.pattern !== "^[a-z0-9-]+$" ||
    schemas.ArtifactSectionGenerationRequest.properties.section_key["x-max-utf8-bytes"] !== 128) {
  throw new Error("Artifact plan, outline or section ingress contract drifted");
}
const artifactGenerationAcceptance = schemas.ArtifactSectionGenerationAcceptance;
const artifactGenerationOperation = document.paths["/api/v1/artifacts/{artifact_id}/sections/generate"].post;
if (artifactGenerationAcceptance.required.join(",") !== "generation_id,workspace_id,artifact_id,source_revision_id,source_revision_no,source_artifact_version,section_key,workflow_run_id,node_run_id,status,version,created_at,updated_at,replayed,status_url" ||
    artifactGenerationAcceptance.properties.status.enum.join(",") !== "PENDING,COMPLETED,FAILED,CANCELLED,RECOVERY_REQUIRED" ||
    artifactGenerationAcceptance.properties.status_url.pattern !== "^/api/v1/workflows/[0-9a-f-]{36}$" ||
    artifactGenerationAcceptance.properties.status_url.format !== "uri-reference" ||
    !["generation_id", "workspace_id", "artifact_id", "source_revision_id", "workflow_run_id", "node_run_id"].every((name) => artifactGenerationAcceptance.properties[name].format === "uuid") ||
    artifactGenerationAcceptance.properties.source_revision_no.minimum !== 1 ||
    artifactGenerationAcceptance.properties.source_artifact_version.minimum !== 1 ||
    artifactGenerationAcceptance.properties.version.minimum !== 1) {
  throw new Error("Artifact section generation acceptance must retain its frozen Workflow binding");
}
if (Object.keys(artifactGenerationOperation.responses).filter((status) => status.startsWith("2")).join(",") !== "202") {
  throw new Error("Artifact section generation first acceptance and exact replay must both use only 202");
}
const artifactGenerationReadPath = document.paths["/api/v1/artifacts/{artifact_id}/section-generations"];
const artifactGenerationReadOperation = artifactGenerationReadPath.get;
const artifactGenerationReadItem = schemas.ArtifactSectionGenerationReadItem;
const artifactGenerationReadResponse = schemas.ArtifactSectionGenerationReadResponse;
const artifactGenerationReadStatuses = ["PENDING", "FAILED", "CANCELLED", "RECOVERY_REQUIRED"];
const artifactGenerationReadRequired = artifactGenerationAcceptance.required.filter((name) => name !== "replayed");
const artifactGenerationReadProperties = Object.keys(artifactGenerationAcceptance.properties).filter((name) => name !== "replayed");
const artifactGenerationPathParameter = artifactGenerationReadPath.parameters?.[0];
const artifactGenerationQueryParameter = document.components.parameters.WorkspaceIDQuery;
if (artifactGenerationReadOperation["x-required-capability"] !== "READ_LOCAL" ||
    artifactGenerationReadOperation.security !== undefined || artifactGenerationReadOperation.requestBody !== undefined ||
    artifactGenerationReadPath.parameters?.length !== 1 || artifactGenerationPathParameter?.name !== "artifact_id" ||
    artifactGenerationPathParameter.in !== "path" || artifactGenerationPathParameter.required !== true ||
    artifactGenerationPathParameter.schema?.type !== "string" || artifactGenerationPathParameter.schema?.format !== "uuid" ||
    artifactGenerationReadOperation.parameters?.length !== 1 || artifactGenerationReadOperation.parameters[0]?.$ref !== "#/components/parameters/WorkspaceIDQuery" ||
    artifactGenerationQueryParameter?.name !== "workspace_id" || artifactGenerationQueryParameter?.in !== "query" ||
    artifactGenerationQueryParameter?.required !== true || artifactGenerationQueryParameter?.schema?.type !== "string" ||
    artifactGenerationQueryParameter?.schema?.format !== "uuid") {
  throw new Error("Artifact section generation read must inherit business authentication and require exactly READ_LOCAL plus UUID path/query bindings");
}
const artifactGenerationReadResponseStatuses = Object.keys(artifactGenerationReadOperation.responses).sort();
if (artifactGenerationReadResponseStatuses.join(",") !== ["200", "400", "401", "403", "404", "405", "500", "503"].join(",") ||
    artifactGenerationReadOperation.responses["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ArtifactSectionGenerationReadResponse") {
  throw new Error("Artifact section generation read response matrix drifted");
}
for (const [status, responseRef] of Object.entries({
  "400": "BadRequest", "401": "Unauthorized", "403": "Forbidden", "404": "NotFound",
  "405": "MethodNotAllowed", "500": "InternalError", "503": "Unavailable",
})) {
  if (artifactGenerationReadOperation.responses[status]?.$ref !== `#/components/responses/${responseRef}`) {
    throw new Error(`Artifact section generation read ${status} response must use ${responseRef}`);
  }
}
if (artifactGenerationReadItem.required.join(",") !== artifactGenerationReadRequired.join(",") ||
    Object.keys(artifactGenerationReadItem.properties).join(",") !== artifactGenerationReadProperties.join(",") ||
    artifactGenerationReadItem.properties.replayed !== undefined ||
    artifactGenerationReadItem.properties.status.enum.join(",") !== artifactGenerationReadStatuses.join(",") ||
    artifactGenerationReadItem.properties.status.enum.includes("COMPLETED")) {
  throw new Error("Artifact section generation read item must reuse acceptance fields without replayed or COMPLETED");
}
for (const propertyName of artifactGenerationReadProperties) {
  if (propertyName !== "status" && JSON.stringify(artifactGenerationReadItem.properties[propertyName]) !== JSON.stringify(artifactGenerationAcceptance.properties[propertyName])) {
    throw new Error(`Artifact section generation read item ${propertyName} drifted from acceptance`);
  }
}
if (artifactGenerationReadResponse.required.join(",") !== "workspace_id,artifact_id,items" ||
    Object.keys(artifactGenerationReadResponse.properties).join(",") !== "workspace_id,artifact_id,items" ||
    artifactGenerationReadResponse.properties.workspace_id.format !== "uuid" ||
    artifactGenerationReadResponse.properties.artifact_id.format !== "uuid" ||
    artifactGenerationReadResponse.properties.items.type !== "array" ||
    artifactGenerationReadResponse.properties.items.maxItems !== undefined ||
    artifactGenerationReadResponse.properties.items.items?.$ref !== "#/components/schemas/ArtifactSectionGenerationReadItem") {
  throw new Error("Artifact section generation read response must remain an unpaginated Workspace-bound list");
}
const artifactGenerationUnavailable = resolveRef(artifactGenerationOperation.responses["503"]);
const artifactGenerationUnavailableExample = artifactGenerationUnavailable?.content?.["application/json"]?.examples?.artifact_generation_capability_unavailable?.value;
if (artifactGenerationUnavailableExample?.error_code !== "ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE" || artifactGenerationUnavailableExample.retryable !== false) {
  throw new Error("Artifact section generation 503 must remain stable and non-retryable when Chat dependencies are disabled");
}
const artifactCitationInput = schemas.ArtifactCitationInput;
if (Object.keys(artifactCitationInput.properties).sort().join(",") !== "chunk_id,index_version_id,source_span_id,source_version_id" ||
    artifactCitationInput.required.join(",") !== "index_version_id,chunk_id,source_version_id,source_span_id" ||
    schemas.ArtifactCitation.properties.verified.const !== true ||
    !schemas.ArtifactCitation.required.includes("verified_content_hash") || !schemas.ArtifactCitation.required.includes("excerpt")) {
  throw new Error("Artifact Citation ingress must remain server-verified and response-only");
}
const artifactDocumentSource = schemas.ArtifactDocumentSource;
if (artifactDocumentSource?.type !== "object" || artifactDocumentSource.additionalProperties !== false ||
    artifactDocumentSource.required?.join(",") !== "document_id,article_revision_id,revision_no,verified_content_hash,verified" ||
    artifactDocumentSource.properties?.document_id?.format !== "uuid" || artifactDocumentSource.properties?.article_revision_id?.format !== "uuid" ||
    artifactDocumentSource.properties?.revision_no?.minimum !== 1 || artifactDocumentSource.properties?.verified_content_hash?.pattern !== "^[0-9a-f]{64}$" ||
    artifactDocumentSource.properties?.verified?.const !== true ||
    !schemas.ArtifactSection.required?.includes("document_sources") ||
    schemas.ArtifactSection.properties?.document_sources?.type !== "array" ||
    schemas.ArtifactSection.properties?.document_sources?.items?.$ref !== "#/components/schemas/ArtifactDocumentSource") {
  throw new Error("Artifact Section must expose exact verified Document Sources alongside Citations");
}
const artifactCoverage = schemas.ArtifactCoverage;
if (artifactCoverage.properties.status.enum.join(",") !== "COVERED,PARTIAL,GAP" ||
    artifactCoverage.allOf?.[0]?.then?.properties?.gaps?.maxItems !== 0 ||
    artifactCoverage.allOf?.[1]?.then?.properties?.gaps?.minItems !== 1 ||
    schemas.ArtifactSectionInput.allOf?.[0]?.then?.properties?.content?.maxLength !== 0 ||
    schemas.ArtifactSectionInput.allOf?.[0]?.then?.properties?.citations?.maxItems !== 0 ||
    schemas.ArtifactSectionInput.allOf?.[1]?.then?.properties?.content?.minLength !== 1 ||
    schemas.ArtifactSectionInput.allOf?.[1]?.then?.properties?.citations?.minItems !== 1 ||
    schemas.ArtifactSection.allOf?.[0]?.then?.properties?.content?.maxLength !== 0 ||
    schemas.ArtifactSection.allOf?.[0]?.then?.properties?.citations?.maxItems !== 0 ||
    schemas.ArtifactSection.allOf?.[1]?.then?.properties?.content?.minLength !== 1 ||
    schemas.ArtifactSection.allOf?.[1]?.then?.properties?.citations?.minItems !== 1) {
  throw new Error("Artifact COVERED/PARTIAL/GAP evidence invariants drifted");
}
const artifactStatuses = schemas.Artifact.properties.status.enum;
if (artifactStatuses.join(",") !== "PLANNING,OUTLINE_REVIEW,GENERATING,DRAFT,APPROVED,EXPORTED,PUBLISH_PROPOSED,PUBLISHED,ARCHIVED" ||
    schemas.ArtifactRevision.properties.content_hash.pattern !== "^[0-9a-f]{64}$" ||
    schemas.ArtifactExport.properties.revision_hash.pattern !== "^[0-9a-f]{64}$" ||
    schemas.ArtifactExport.properties.output_hash.pattern !== "^[0-9a-f]{64}$") {
  throw new Error("Artifact lifecycle or immutable hash contract drifted");
}
const artifactPublication = schemas.ArtifactPublication;
if (artifactPublication.required.join(",") !== "workspace_id,artifact_id,revision_id,artifact_version,revision_no,content_hash,proposal_id,created_at" ||
    artifactPublication.properties.content_hash.pattern !== "^[0-9a-f]{64}$" ||
    !document.paths["/api/v1/artifacts/{artifact_id}/publish-proposals"].post.description.includes("PUBLISH_ARTIFACT") ||
    !document.paths["/api/v1/artifacts/{artifact_id}/publish-proposals"].post.description.includes("never creates a Document")) {
  throw new Error("Artifact publication must remain a frozen PUBLISH_ARTIFACT Proposal binding");
}
if (schemas.SemanticLinkCandidate.properties.discovery_methods.maxItems !== 6 ||
    schemas.SemanticLinkCandidate.properties.evidence.maxItems !== 100 ||
    schemas.SemanticLinkCandidatePage.properties.items.maxItems !== 100 ||
    schemas.SemanticLinkGeneration.required.join(",") !== "index_version_id,embedding_version_id,rerank_version_id") {
  throw new Error("Semantic Link Candidate bounds or generation contract drifted");
}
if (schemas.ImpactAnalysisRequest.maxProperties !== 0 || schemas.ImpactAnalysisRequest.additionalProperties !== false ||
    schemas.KnowledgeTimelinePage.properties.items.maxItems !== 100 ||
    schemas.KnowledgeTimelinePage.properties.items.items.$ref !== "#/components/schemas/KnowledgeEvent" ||
    schemas.KnowledgeTimelinePage.properties.next_cursor.maxLength !== 4096 ||
    schemas.ImpactReportV1.properties.objects.maxItems !== 500 ||
    schemas.ImpactReportV1.properties.objects.items.$ref !== "#/components/schemas/ImpactObjectV1" ||
    schemas.ImpactReportV2.properties.objects.maxItems !== 500 ||
    schemas.ImpactReportV2.properties.objects.items.$ref !== "#/components/schemas/ImpactObject" ||
    schemas.ImpactAnalysisResult.properties.proposal_drafts.maxItems !== 500 ||
    schemas.ImpactAnalysisResult.properties.proposal_drafts.items.$ref !== "#/components/schemas/ImpactProposalDraft") {
  throw new Error("Timeline/Impact page, empty request or bounded result schemas drifted");
}
const knowledgeEventTypes = [
  "PROPOSAL_CREATED", "APPROVAL_GRANTED", "APPROVAL_REJECTED", "GIT_COMMITTED", "RELATION_CONFIRMED", "RELATION_DEPRECATED",
  "CONFLICT_OPENED", "CONFLICT_TRANSITIONED", "CONFLICT_RESOLVED", "VERSION_PUBLISHED", "VERSION_SUPERSEDED",
  "HEALTH_ISSUE_DETECTED", "HEALTH_ISSUE_RESOLVED", "IMPACT_ANALYZED", "ARTIFACT_GENERATED", "REVIEW_CARD_INVALIDATED", "CORRECTIVE_EVENT",
];
const timelineAggregateTypes = [
  "PROPOSAL", "APPROVAL", "GIT_COMMIT", "TOPIC", "CLAIM", "RELATION", "CONFLICT", "DOCUMENT", "ARTICLE_REVISION",
  "HEALTH_ISSUE", "IMPACT_REPORT", "ARTIFACT", "REVIEW_CARD",
];
const eventV1Properties = [
  "id", "workspace_id", "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "source_ref", "event_version",
  "schema_version", "summary", "payload", "correlation", "occurred_at", "created_at",
];
const eventV1Required = eventV1Properties.filter((field) => field !== "aggregate_id");
if (schemas.KnowledgeEvent.oneOf?.map((item) => item.$ref).join(",") !==
      "#/components/schemas/KnowledgeEventV1,#/components/schemas/KnowledgeEventV2" ||
    schemas.KnowledgeEvent.discriminator?.propertyName !== "schema_version" ||
    schemas.KnowledgeEvent.discriminator?.mapping?.["knowledge-event/v1"] !== "#/components/schemas/KnowledgeEventV1" ||
    schemas.KnowledgeEvent.discriminator?.mapping?.["knowledge-event/v2"] !== "#/components/schemas/KnowledgeEventV2" ||
    schemas.KnowledgeEventType.enum?.join(",") !== knowledgeEventTypes.join(",") ||
    schemas.TimelineAggregateType.enum?.join(",") !== timelineAggregateTypes.join(",") ||
    schemas.KnowledgeEventV1.required?.join(",") !== eventV1Required.join(",") ||
    Object.keys(schemas.KnowledgeEventV1.properties ?? {}).join(",") !== eventV1Properties.join(",") ||
    schemas.KnowledgeEventV1.properties.schema_version?.const !== "knowledge-event/v1" ||
    schemas.KnowledgeEventV1.properties.event_type?.enum?.includes("ARTIFACT_GENERATED") ||
    schemas.KnowledgeEventV1.properties.event_type?.enum?.includes("REVIEW_CARD_INVALIDATED") ||
    schemas.KnowledgeEventV2.properties.schema_version?.const !== "knowledge-event/v2" ||
    !schemas.KnowledgeEventV2.required?.includes("operator") || !schemas.KnowledgeEventV2.required?.includes("owner_binding") ||
    schemas.KnowledgeEventV2.properties.operator?.$ref !== "#/components/schemas/KnowledgeEventOperator" ||
    schemas.KnowledgeEventV2.properties.owner_binding?.oneOf?.[0]?.$ref !== "#/components/schemas/KnowledgeEventOwnerBinding" ||
    schemas.KnowledgeEventV2.properties.owner_binding?.oneOf?.[1]?.type !== "null") {
  throw new Error("Knowledge Event v1/v2 discriminator, exact legacy fields or required nullable v2 owner binding drifted");
}
if (schemas.KnowledgeEventOperator.properties.type?.enum?.join(",") !== "USER,API_TOKEN,SYSTEM,UNKNOWN" ||
    schemas.KnowledgeEventOperator.allOf?.[0]?.then?.not?.required?.join(",") !== "id" ||
    schemas.KnowledgeEventOwnerBinding.oneOf?.map((item) => item.$ref).join(",") !==
      "#/components/schemas/ArtifactEventOwnerBinding,#/components/schemas/ReviewCardEventOwnerBinding" ||
    schemas.ReviewCardEventOwnerBinding.properties.review_card?.allOf?.[0]?.$ref !== "#/components/schemas/ReviewCardImpactBinding" ||
    schemas.ReviewCardEventOwnerBinding.properties.review_card?.allOf?.[1]?.properties?.status?.const !== "INVALIDATED" ||
    schemas.KnowledgeEventV2.allOf?.[0]?.then?.properties?.aggregate_type?.const !== "ARTIFACT" ||
    schemas.KnowledgeEventV2.allOf?.[0]?.then?.properties?.owner_binding?.$ref !== "#/components/schemas/ArtifactEventOwnerBinding" ||
    schemas.KnowledgeEventV2.allOf?.[1]?.then?.properties?.aggregate_type?.const !== "REVIEW_CARD" ||
    schemas.KnowledgeEventV2.allOf?.[1]?.then?.properties?.owner_binding?.$ref !== "#/components/schemas/ReviewCardEventOwnerBinding" ||
    schemas.KnowledgeEventV2.allOf?.[2]?.then?.properties?.owner_binding?.type !== "null") {
  throw new Error("Knowledge Event operator or owner-event conditional binding drifted");
}
const artifactBindingFields = ["artifact_id", "artifact_version", "revision_id", "revision_no", "content_hash"];
const reviewBindingFields = ["card_id", "card_version", "status", "fingerprint", "claim_id", "evidence_binding_fingerprint"];
if (schemas.ArtifactImpactBinding.required?.join(",") !== artifactBindingFields.join(",") ||
    Object.keys(schemas.ArtifactImpactBinding.properties ?? {}).join(",") !== artifactBindingFields.join(",") ||
    schemas.ArtifactImpactBinding.properties.content_hash?.pattern !== "^[0-9a-f]{64}$" ||
    schemas.ReviewCardImpactBinding.required?.join(",") !== reviewBindingFields.join(",") ||
    Object.keys(schemas.ReviewCardImpactBinding.properties ?? {}).join(",") !== reviewBindingFields.join(",") ||
    schemas.ReviewCardImpactBinding.properties.status?.enum?.join(",") !== "DRAFT,APPROVED,INVALIDATED,REJECTED" ||
    schemas.ReviewCardImpactBinding.properties.fingerprint?.pattern !== "^[0-9a-f]{64}$" ||
    schemas.ReviewCardImpactBinding.properties.evidence_binding_fingerprint?.pattern !== "^[0-9a-f]{64}$") {
  throw new Error("Artifact or Review Card immutable impact binding drifted");
}
const legacyImpactObjectTypes = ["TOPIC", "CLAIM", "RELATION", "CONFLICT", "HEALTH_ISSUE", "PROPOSAL", "ARTICLE_REVISION", "AUDIT_EVENT"];
const impactObjectTypes = [...legacyImpactObjectTypes, "ARTIFACT", "REVIEW_CARD"];
const impactActions = ["REVIEW", "REINDEX", "RESOLVE_CONFLICT", "REFRESH_HEALTH", "REGENERATE_ARTIFACT", "REVALIDATE_REVIEW_CARD", "NO_ACTION"];
if (schemas.ImpactObjectType.enum?.join(",") !== impactObjectTypes.join(",") ||
    schemas.ImpactAction.enum?.join(",") !== impactActions.join(",") ||
    schemas.ImpactObjectV1.properties.type?.enum?.join(",") !== legacyImpactObjectTypes.join(",") ||
    schemas.ImpactObjectV1.properties.action?.enum?.join(",") !== "REVIEW,REINDEX,RESOLVE_CONFLICT,REFRESH_HEALTH,NO_ACTION" ||
    schemas.ImpactObject.oneOf?.map((item) => item.$ref).join(",") !==
      "#/components/schemas/ImpactObjectV2Legacy,#/components/schemas/ArtifactImpactObject,#/components/schemas/ReviewCardImpactObject" ||
    schemas.ImpactObject.discriminator?.propertyName !== "type" ||
    schemas.ArtifactImpactObject.allOf?.[1]?.properties?.type?.const !== "ARTIFACT" ||
    schemas.ArtifactImpactObject.allOf?.[1]?.properties?.action?.const !== "REGENERATE_ARTIFACT" ||
    schemas.ArtifactImpactObject.allOf?.[1]?.properties?.requires_proposal?.const !== true ||
    schemas.ArtifactImpactObject.allOf?.[1]?.properties?.artifact_binding?.$ref !== "#/components/schemas/ArtifactImpactBinding" ||
    schemas.ReviewCardImpactObject.allOf?.[1]?.properties?.type?.const !== "REVIEW_CARD" ||
    schemas.ReviewCardImpactObject.allOf?.[1]?.properties?.action?.const !== "REVALIDATE_REVIEW_CARD" ||
    schemas.ReviewCardImpactObject.allOf?.[1]?.properties?.requires_proposal?.const !== true ||
    schemas.ReviewCardImpactObject.allOf?.[1]?.properties?.review_card_binding?.$ref !== "#/components/schemas/ReviewCardImpactBinding" ||
    schemas.ImpactObjectV2Legacy.unevaluatedProperties !== false || schemas.ArtifactImpactObject.unevaluatedProperties !== false ||
    schemas.ReviewCardImpactObject.unevaluatedProperties !== false) {
  throw new Error("Impact Object v1/v2 union or owner-specific action binding drifted");
}
const reportV1Properties = [
  "id", "workspace_id", "source_event_id", "source_event_ref", "source_event_version", "status", "objects", "summary", "fingerprint",
  "error_code", "stale_reason", "schema_version", "generated_at", "created_at", "version",
];
const reportV1Required = reportV1Properties.filter((field) => field !== "error_code" && field !== "stale_reason");
if (schemas.ImpactReport.oneOf?.map((item) => item.$ref).join(",") !==
      "#/components/schemas/ImpactReportV1,#/components/schemas/ImpactReportV2" ||
    schemas.ImpactReport.discriminator?.propertyName !== "schema_version" ||
    schemas.ImpactReport.discriminator?.mapping?.["impact-report/v1"] !== "#/components/schemas/ImpactReportV1" ||
    schemas.ImpactReport.discriminator?.mapping?.["impact-report/v2"] !== "#/components/schemas/ImpactReportV2" ||
    schemas.ImpactReportV1.required?.join(",") !== reportV1Required.join(",") ||
    Object.keys(schemas.ImpactReportV1.properties ?? {}).join(",") !== reportV1Properties.join(",") ||
    schemas.ImpactReportV1.properties.schema_version?.const !== "impact-report/v1" ||
    schemas.ImpactReportV1.properties.analysis_version !== undefined ||
    schemas.ImpactReportV2.properties.schema_version?.const !== "impact-report/v2" ||
    schemas.ImpactReportV2.properties.analysis_version?.const !== "impact-analysis/v2" ||
    !schemas.ImpactReportV2.required?.includes("analysis_version") || !schemas.ImpactReportV2.required?.includes("supersedes_report_id") ||
    !schemas.ImpactReportV2.required?.includes("superseded_by_report_id") ||
    schemas.ImpactReportV2.properties.supersedes_report_id?.oneOf?.[1]?.type !== "null" ||
    schemas.ImpactReportV2.properties.superseded_by_report_id?.oneOf?.[1]?.type !== "null" ||
    schemas.ImpactReportV1.properties.fingerprint?.pattern !== "^[0-9a-f]{64}$" ||
    schemas.ImpactReportV2.properties.fingerprint?.pattern !== "^[0-9a-f]{64}$") {
  throw new Error("Impact Report v1/v2 discriminator, exact legacy fields or supersession contract drifted");
}
if (schemas.ImpactProposalDraft.properties.target_type?.enum?.join(",") !== impactObjectTypes.join(",") ||
    schemas.ImpactProposalDraft.properties.operation?.enum?.join(",") !== impactActions.filter((action) => action !== "NO_ACTION").join(",") ||
    schemas.ImpactProposalDraft.properties.requires_approval.const !== true ||
    schemas.ImpactProposalDraft.properties.requires_write_authorization.const !== true ||
    schemas.ImpactProposalDraft.properties.operation.enum.includes("NO_ACTION")) {
  throw new Error("Impact Proposal draft authorization boundary drifted");
}
if (schemas.Proposal.oneOf?.map((item) => item.$ref).join(",") !==
      "#/components/schemas/FilePatchProposal,#/components/schemas/RestoreDocumentProposal,#/components/schemas/KnowledgeChangeProposal,#/components/schemas/PublishArtifactProposal,#/components/schemas/DownstreamUpdateProposal" ||
    schemas.Proposal.discriminator?.propertyName !== "proposal_type" ||
    schemas.Proposal.discriminator?.mapping?.file_patch !== "#/components/schemas/FilePatchProposal" ||
    schemas.Proposal.discriminator?.mapping?.restore_document !== "#/components/schemas/RestoreDocumentProposal" ||
    schemas.Proposal.discriminator?.mapping?.knowledge_change !== "#/components/schemas/KnowledgeChangeProposal" ||
    schemas.Proposal.discriminator?.mapping?.publish_artifact !== "#/components/schemas/PublishArtifactProposal" ||
    schemas.Proposal.discriminator?.mapping?.downstream_update !== "#/components/schemas/DownstreamUpdateProposal" ||
    !schemas.FilePatchProposal.required.includes("proposal_type") ||
    schemas.FilePatchProposal.properties.proposal_type?.const !== "file_patch" ||
    !schemas.RestoreDocumentProposal.required.includes("proposal_type") ||
    schemas.RestoreDocumentProposal.properties.proposal_type?.const !== "restore_document" ||
    !schemas.KnowledgeChangeProposal.required.includes("proposal_type") ||
    schemas.KnowledgeChangeProposal.properties.proposal_type.const !== "knowledge_change" ||
    !schemas.PublishArtifactProposal.required.includes("proposal_type") ||
    schemas.PublishArtifactProposal.properties.proposal_type.const !== "publish_artifact" ||
    !schemas.DownstreamUpdateProposal.required.includes("proposal_type") ||
    schemas.DownstreamUpdateProposal.properties.proposal_type.const !== "downstream_update" ||
    schemas.KnowledgeChangeRevision.properties.schema_version.const !== "knowledge-relation-change/v1" ||
    schemas.KnowledgeChangeRevision.properties.base_versions.minItems !== 2 ||
    schemas.KnowledgeChangeRevision.properties.base_versions.maxItems !== 2) {
  throw new Error("typed Proposal discriminated response contract drifted");
}
const downstreamRequest = schemas.CreateDownstreamUpdateProposalRequest;
const downstreamUpdate = schemas.DownstreamUpdate;
const downstreamRevision = schemas.DownstreamUpdateRevision;
const downstreamProposal = schemas.DownstreamUpdateProposal;
if (downstreamRequest.required?.join(",") !== "target_type,target_id,action" ||
    Object.keys(downstreamRequest.properties ?? {}).join(",") !== "target_type,target_id,action" ||
    downstreamRequest.additionalProperties !== false || downstreamRequest.properties.target_id?.format !== "uuid" ||
    downstreamRequest.oneOf?.[0]?.properties?.target_type?.const !== "ARTIFACT" ||
    downstreamRequest.oneOf?.[0]?.properties?.action?.const !== "REGENERATE_ARTIFACT" ||
    downstreamRequest.oneOf?.[1]?.properties?.target_type?.const !== "REVIEW_CARD" ||
    downstreamRequest.oneOf?.[1]?.properties?.action?.const !== "REVALIDATE_REVIEW_CARD") {
  throw new Error("Downstream Proposal request must accept only one compatible target_type, target_id and action tuple");
}
const downstreamUpdateRequired = [
  "workspace_id", "source_report", "source_event", "target_type", "target_id", "base_version", "action", "reason", "schema_version",
];
if (downstreamUpdate.required?.join(",") !== downstreamUpdateRequired.join(",") ||
    downstreamUpdate.properties.source_report?.$ref !== "#/components/schemas/DownstreamUpdateSourceReport" ||
    downstreamUpdate.properties.source_event?.$ref !== "#/components/schemas/DownstreamUpdateSourceEvent" ||
    downstreamUpdate.properties.schema_version?.const !== "impact-downstream-update/v1" ||
    downstreamUpdate.properties.artifact_binding?.$ref !== "#/components/schemas/ArtifactImpactBinding" ||
    downstreamUpdate.properties.review_card_binding?.$ref !== "#/components/schemas/ReviewCardImpactBinding" ||
    downstreamUpdate.oneOf?.[0]?.required?.join(",") !== "artifact_binding" ||
    downstreamUpdate.oneOf?.[0]?.not?.required?.join(",") !== "review_card_binding" ||
    downstreamUpdate.oneOf?.[1]?.required?.join(",") !== "review_card_binding" ||
    downstreamUpdate.oneOf?.[1]?.not?.required?.join(",") !== "artifact_binding" ||
    schemas.DownstreamUpdateSourceReport.properties.analysis_version?.const !== "impact-analysis/v2" ||
    schemas.DownstreamUpdateSourceReport.properties.fingerprint?.pattern !== "^[0-9a-f]{64}$" ||
    schemas.DownstreamUpdateSourceEvent.properties.event_version?.minimum !== 1) {
  throw new Error("Downstream Proposal must freeze the v2 report, event and exactly one owner binding");
}
if (downstreamRevision.required?.join(",") !== "id,revision_no,update,risk,rollback_plan,change_hash,created_at" ||
    downstreamRevision.properties.update?.$ref !== "#/components/schemas/DownstreamUpdate" ||
    downstreamRevision.properties.change_hash?.pattern !== "^[0-9a-f]{64}$" ||
    downstreamRevision.properties.content !== undefined || downstreamRevision.properties.publication !== undefined ||
    downstreamProposal.properties.proposal_type?.const !== "downstream_update" ||
    downstreamProposal.properties.status?.enum?.join(",") !== "ready_for_review,approved,rejected" ||
    downstreamProposal.properties.risk_level?.const !== "HIGH" ||
    downstreamProposal.properties.revision?.$ref !== "#/components/schemas/DownstreamUpdateRevision" ||
    downstreamProposal.properties.approval?.oneOf?.[0]?.$ref !== "#/components/schemas/NonFileApproval" ||
    downstreamProposal.properties.approval?.oneOf?.[1]?.type !== "null") {
  throw new Error("Downstream Proposal typed revision or approval-only lifecycle drifted");
}
const downstreamForbiddenFields = [
  "replayed", "target_path", "approved_git_head", "workflow_run_id", "workflow_status_url", "write_authorization", "writeback", "apply",
];
const downstreamCreateResponse = schemas.DownstreamUpdateProposalCreateResponse;
const downstreamCreateResponseRequired = [
  "proposal_type", "id", "workspace_id", "status", "risk_level", "version", "revision_capability", "revision", "approval", "replayed", "created_at", "updated_at",
];
const applyPreflightOperation = document.paths["/api/v1/proposals/{proposal_id}/apply-preflight"]?.post;
const decideProposalOperation = document.paths["/api/v1/proposals/{proposal_id}/approvals"]?.post;
if (downstreamForbiddenFields.some((field) => downstreamProposal.properties[field] !== undefined) ||
    schemas.ProposalSummary.properties.replayed !== undefined ||
    downstreamCreateResponse.type !== "object" ||
    downstreamCreateResponse.required?.join(",") !== downstreamCreateResponseRequired.join(",") ||
    Object.keys(downstreamCreateResponse.properties ?? {}).join(",") !== downstreamCreateResponseRequired.join(",") ||
    downstreamCreateResponse.properties?.proposal_type?.const !== "downstream_update" ||
    downstreamCreateResponse.properties?.status?.enum?.join(",") !== "ready_for_review,approved,rejected" ||
    downstreamCreateResponse.properties?.risk_level?.const !== "HIGH" ||
    downstreamCreateResponse.properties?.revision?.$ref !== "#/components/schemas/DownstreamUpdateRevision" ||
    downstreamCreateResponse.properties?.approval?.oneOf?.[0]?.$ref !== "#/components/schemas/NonFileApproval" ||
    downstreamCreateResponse.properties?.approval?.oneOf?.[1]?.type !== "null" ||
    downstreamCreateResponse.properties?.replayed?.type !== "boolean" ||
    downstreamCreateResponse.additionalProperties !== false || downstreamCreateResponse.allOf !== undefined ||
    !applyPreflightOperation?.["x-error-codes"]?.includes("DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE") ||
    !applyPreflightOperation?.responses?.["409"]?.description?.includes("DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE")) {
  throw new Error("replayed must remain endpoint-only and approved downstream Proposals must forbid apply/writeback fields");
}
if (!decideProposalOperation?.["x-error-codes"]?.includes("PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE") ||
    !decideProposalOperation?.responses?.["503"] ||
    !applyPreflightOperation?.["x-error-codes"]?.includes("PROPOSAL_REVISION_WORKFLOW_CANCEL_UNAVAILABLE") ||
    !applyPreflightOperation?.responses?.["503"]) {
  throw new Error("Proposal approval and preflight must expose retryable Revision Workflow cancellation dependency failures");
}
const publishArtifactCoverage = schemas.PublishArtifactCoverage;
const publishArtifactBinding = schemas.PublishArtifactBinding;
if (publishArtifactCoverage.required?.join(",") !== "section_key,status,gaps" ||
    publishArtifactCoverage.properties.section_key?.minLength !== 1 ||
    publishArtifactCoverage.properties.section_key?.["x-max-utf8-bytes"] !== 128 ||
    publishArtifactCoverage.properties.section_key?.pattern !== undefined ||
    publishArtifactCoverage.properties.status?.enum?.join(",") !== "COVERED,PARTIAL,GAP" ||
    publishArtifactCoverage.properties.gaps?.uniqueItems !== true ||
    publishArtifactCoverage.properties.gaps?.items?.$ref !== "#/components/schemas/ArtifactGap" ||
    publishArtifactCoverage.allOf?.[0]?.then?.properties?.gaps?.maxItems !== 0 ||
    publishArtifactCoverage.allOf?.[1]?.then?.properties?.gaps?.minItems !== 1 ||
    schemas.ArtifactGap.properties.code?.["x-max-utf8-bytes"] !== 128 ||
    schemas.ArtifactGap.properties.description?.["x-max-utf8-bytes"] !== 4096) {
  throw new Error("PublishArtifactCoverage must preserve the frozen section and explicit gap rules");
}
if (publishArtifactBinding.required?.join(",") !== "workspace_id,artifact_id,revision_id,revision_no,artifact_version,content_hash,source_coverage,schema_version" ||
    publishArtifactBinding.properties.workspace_id?.format !== "uuid" ||
    publishArtifactBinding.properties.artifact_id?.format !== "uuid" ||
    publishArtifactBinding.properties.revision_id?.format !== "uuid" ||
    publishArtifactBinding.properties.revision_no?.minimum !== 1 ||
    publishArtifactBinding.properties.artifact_version?.minimum !== 1 ||
    publishArtifactBinding.properties.content_hash?.pattern !== "^[0-9a-f]{64}$" ||
    publishArtifactBinding.properties.source_coverage?.minItems !== 1 ||
    publishArtifactBinding.properties.source_coverage?.uniqueItems !== true ||
    publishArtifactBinding.properties.source_coverage?.items?.$ref !== "#/components/schemas/PublishArtifactCoverage" ||
    publishArtifactBinding.properties.schema_version?.const !== "artifact-publication/v1" ||
    schemas.PublishArtifactRevision.properties.publication?.$ref !== "#/components/schemas/PublishArtifactBinding" ||
    schemas.PublishArtifactRevision.properties.change_hash?.pattern !== "^[0-9a-f]{64}$" ||
    schemas.PublishArtifactProposal.properties.revision?.$ref !== "#/components/schemas/PublishArtifactRevision") {
  throw new Error("PublishArtifact Proposal must retain its immutable Artifact publication binding");
}
for (const operation of [
  document.paths["/api/v1/conversations"].post,
  document.paths["/api/v1/conversations/{conversation_id}/questions"].post,
  document.paths["/api/v1/answers/{answer_id}/feedback"].post,
]) {
  const parameter = operation.parameters?.find((item) => item.$ref === "#/components/parameters/IdempotencyKey") ??
    document.paths[Object.keys(document.paths).find((path) => document.paths[path]?.post === operation)]?.parameters?.find((item) => item.$ref === "#/components/parameters/IdempotencyKey");
  if (!parameter) throw new Error(`missing Idempotency-Key for ${operation.operationId}`);
  if (!operation.responses["200"]) throw new Error(`missing exact replay response for ${operation.operationId}`);
}
if (document.components.parameters.WorkspaceIDQuery.required !== true || document.components.parameters.WorkspaceIDQuery.in !== "query") {
  throw new Error("workspace_id query parameter must be required");
}
for (const requestName of ["CreateConversationRequest", "SubmitQuestionRequest", "SubmitFeedbackRequest"]) {
  if (!schemas[requestName].required.includes("workspace_id")) throw new Error(`${requestName} must require workspace_id`);
}
if (schemas.PageCursor.maxLength !== 2048 || document.components.parameters.Limit.schema.maximum !== 100) {
  throw new Error("Conversation cursor/limit bounds drifted");
}
const graphNodeRefs = schemas.GraphNode.oneOf?.map((item) => item.$ref).join(",");
if (graphNodeRefs !== "#/components/schemas/GraphTopicNode,#/components/schemas/GraphClaimNode" ||
    schemas.GraphNode.discriminator?.propertyName !== "type" ||
    schemas.GraphNode.discriminator?.mapping?.TOPIC !== "#/components/schemas/GraphTopicNode" ||
    schemas.GraphNode.discriminator?.mapping?.CLAIM !== "#/components/schemas/GraphClaimNode" ||
    schemas.GraphTopicNode.properties?.type?.const !== "TOPIC" || schemas.GraphClaimNode.properties?.type?.const !== "CLAIM") {
  throw new Error("GraphNode must remain a strict TOPIC/CLAIM discriminated union");
}
if (schemas.GraphEdge.properties.status.enum.join(",") !== "CONFIRMED,STALE" ||
    schemas.GraphFilter.properties.relation_statuses.items.enum.join(",") !== "CONFIRMED,STALE" ||
    schemas.GraphFilter.properties.claim_statuses.items.enum.join(",") !== "CONFIRMED,DISPUTED" ||
    schemas.GraphFilter.properties.relation_statuses.maxItems !== 2 ||
    schemas.GraphFilter.properties.claim_statuses.maxItems !== 2 ||
    schemas.GraphFilter.properties.topic_ids.maxItems !== 500) {
  throw new Error("Graph formal status or filter bounds drifted from the domain validators");
}
if (schemas.GraphEdge.properties.evidence_href.pattern !== "^/api/v1/graph/relations/[0-9a-f-]{36}/evidence\\?workspace_id=[0-9a-f-]{36}$" ||
    schemas.GraphRelationEvidenceItem.properties.source_href.pattern !== "^/api/v1/workspaces/[0-9a-f-]{36}/source-versions/[0-9a-f-]{36}$" ||
    schemas.GraphRelationEvidenceItem.properties.span_href.pattern !== "^/api/v1/workspaces/[0-9a-f-]{36}/source-versions/[0-9a-f-]{36}/spans/[0-9a-f-]{36}$") {
  throw new Error("Graph Evidence hrefs must remain directly followable and Workspace scoped");
}
if (schemas.GraphGlobalRequest.properties.limit.default !== 25 || schemas.GraphGlobalRequest.properties.limit.maximum !== 100 ||
    schemas.GraphNeighborhoodRequest.properties.depth.default !== 1 || schemas.GraphNeighborhoodRequest.properties.depth.maximum !== 3 ||
    schemas.GraphNeighborhoodRequest.properties.limit.default !== 25 || schemas.GraphNeighborhoodRequest.properties.max_nodes.maximum !== 500 ||
    schemas.GraphNeighborhoodRequest.properties.max_edges.maximum !== 1000 || schemas.GraphNeighborhoodRequest.properties.max_frontier.maximum !== 500 ||
    schemas.GraphPathRequest.properties.max_depth.default !== 6 || schemas.GraphPathRequest.properties.max_depth.maximum !== 8 ||
    schemas.GraphPathRequest.properties.max_visited.default !== 500 || schemas.GraphPathRequest.properties.max_visited.maximum !== 500 ||
    schemas.GraphNeighborhoodResponse.properties.boundary_nodes.maxItems !== 2000) {
  throw new Error("Graph limit, depth or hard budget contract drifted");
}
for (const requestName of ["GraphGlobalRequest", "GraphNeighborhoodRequest", "GraphPathRequest"]) {
  if (!schemas[requestName].required.includes("workspace_id") || schemas[requestName].properties.cursor?.$ref && schemas[requestName].properties.cursor.$ref !== "#/components/schemas/PageCursor") {
    throw new Error(`${requestName} Workspace or opaque cursor contract drifted`);
  }
}
if (!schemas.GraphNeighborhoodRequest.required.includes("center") ||
    !schemas.GraphPathRequest.required.includes("from") || !schemas.GraphPathRequest.required.includes("to") ||
    schemas.GraphPageMeta.properties.next_cursor.$ref !== "#/components/schemas/PageCursor") {
  throw new Error("Graph required endpoint or cursor fields drifted");
}
const graphSearchQuery = document.paths["/api/v1/graph/nodes"].get.parameters.find((item) => item.name === "query")?.schema;
const graphSearchLimit = document.paths["/api/v1/graph/nodes"].get.parameters.find((item) => item.name === "limit")?.schema;
if (graphSearchQuery?.minLength !== 1 || graphSearchQuery?.["x-min-utf8-bytes"] !== 2 || graphSearchQuery?.["x-max-utf8-bytes"] !== 256 ||
    graphSearchLimit?.default !== 20 || graphSearchLimit?.maximum !== 50) {
  throw new Error("Graph node search UTF-8 or limit bounds drifted");
}
if (schemas.SubmitQuestionRequest.properties.answer_depth.default !== "standard" || schemas.SubmitQuestionRequest.properties.output_format.default !== "markdown" || schemas.QuestionScopeRequest.properties.retrieval_mode.default !== "hybrid" || schemas.SubmitQuestionRequest.properties.scope.$ref !== "#/components/schemas/QuestionScopeRequest") {
  throw new Error("Question option defaults drifted");
}
if (!schemas.WorkflowProjection.required.includes("status_url")) {
  throw new Error("WorkflowProjection must require status_url");
}
if (schemas.SubmitQuestionRequest.properties.question["x-max-utf8-bytes"] !== 8192 || schemas.SubmitFeedbackRequest.properties.comment["x-max-utf8-bytes"] !== 2048) {
  throw new Error("Question or Feedback UTF-8 byte bounds drifted");
}
if (document.paths["/api/v1/conversations/{conversation_id}"].get.responses["200"].headers?.ETag?.$ref !== "#/components/headers/ETag" ||
    document.paths["/api/v1/answers/{answer_id}"].get.responses["200"].headers?.ETag?.$ref !== "#/components/headers/ETag") {
  throw new Error("Conversation and Answer reads must expose ETag");
}
if (!document.components.headers.ETag.schema.pattern.includes("-stage-")) {
  throw new Error("Answer ETag must bind the persisted RAG stage projection");
}
if (document.paths["/api/v1/conversations/{conversation_id}/questions"].post.responses["202"].content?.["application/json"]?.schema?.$ref !== "#/components/schemas/QuestionAcceptance" ||
    schemas.QuestionAcceptance.required.includes("status_url") === false) {
  throw new Error("Question 202 must return QuestionAcceptance with status_url");
}
const latestTurn = document.paths["/api/v1/conversations/{conversation_id}/turns"].parameters.find((item) => item.name === "latest");
if (latestTurn?.schema?.const !== true) throw new Error("Turn latest recovery query contract drifted");
const answerDraftPath = document.paths["/api/v1/answers/{answer_id}/stream"];
const answerDraftSSE = answerDraftPath?.get;
const answerDraftParameters = answerDraftPath?.parameters ?? [];
const answerDraftCursor = answerDraftParameters.find((item) => item.name === "Last-Event-ID" && item.in === "header");
const answerDraftRefs = answerDraftSSE?.responses?.["200"]?.content?.["text/event-stream"]?.schema?.oneOf?.map((item) => item.$ref) ?? [];
if (answerDraftSSE?.operationId !== "subscribeAnswerDraft" || answerDraftSSE.security !== undefined || answerDraftSSE.requestBody !== undefined ||
    !answerDraftParameters.some((item) => item.$ref === "#/components/parameters/AnswerID") ||
    !answerDraftParameters.some((item) => item.$ref === "#/components/parameters/WorkspaceIDQuery") ||
    answerDraftCursor?.required !== false || answerDraftCursor?.schema?.pattern !== "^[1-9][0-9]*:[1-9][0-9]*$" ||
    answerDraftCursor?.schema?.maxLength !== 39 ||
    Object.keys(answerDraftSSE.responses?.["200"]?.content ?? {}).join(",") !== "text/event-stream" ||
    answerDraftSSE.responses?.["200"]?.headers?.["Cache-Control"]?.schema?.const !== "no-store" ||
    answerDraftRefs.join(",") !== "#/components/schemas/AnswerDraftChunk,#/components/schemas/AnswerDraftReset,#/components/schemas/AnswerDraftEnd" ||
    answerDraftRefs.includes("#/components/schemas/ServerEventEnvelope")) {
  throw new Error("Answer Draft SSE cursor, cache, recovery or event union contract drifted");
}
if (schemas.AnswerDraftChunk?.additionalProperties !== false || schemas.AnswerDraftChunk.required?.join(",") !== "generation,sequence,content" ||
    schemas.AnswerDraftChunk.properties?.generation?.minimum !== 1 || schemas.AnswerDraftChunk.properties?.sequence?.minimum !== 1 ||
    schemas.AnswerDraftChunk.properties?.content?.minLength !== 1 || schemas.AnswerDraftChunk.properties?.content?.maxLength !== 65536 ||
    schemas.AnswerDraftChunk.properties?.content?.["x-max-utf8-bytes"] !== 65536 ||
    schemas.AnswerDraftReset?.additionalProperties !== false || schemas.AnswerDraftReset.required?.join(",") !== "generation,reason,action" ||
    schemas.AnswerDraftReset.properties?.generation?.minimum !== 0 ||
    schemas.AnswerDraftReset.properties?.reason?.enum?.join(",") !== "generation_replaced,draft_unavailable,draft_stale,aborted,superseded" ||
    schemas.AnswerDraftReset.properties?.action?.const !== "refetch" ||
    schemas.AnswerDraftEnd?.additionalProperties !== false || schemas.AnswerDraftEnd.required?.join(",") !== "generation,status,action" ||
    schemas.AnswerDraftEnd.properties?.generation?.minimum !== 0 || schemas.AnswerDraftEnd.properties?.status?.enum?.join(",") !== "PUBLISHED,RESET" ||
    schemas.AnswerDraftEnd.properties?.action?.const !== "refetch") {
  throw new Error("Answer Draft chunk, reset or end schema contract drifted");
}
const sse = document.paths["/api/v1/events"].get;
if (sse.responses["200"].content?.["text/event-stream"]?.schema?.$ref !== "#/components/schemas/ServerEventEnvelope" ||
    !sse.parameters.some((item) => item.name === "Last-Event-ID" && item.in === "header")) {
  throw new Error("SSE content type, envelope or Last-Event-ID contract drifted");
}
const sseBootstrapCursor = sse.parameters.find((item) => item.name === "last_event_id" && item.in === "query");
const sseEventFormat = sse.parameters.find((item) => item.name === "event_format" && item.in === "query");
const sseHeaderCursor = sse.parameters.find((item) => item.name === "Last-Event-ID" && item.in === "header");
if (sseBootstrapCursor?.schema?.pattern !== "^[1-9][0-9]*$" ||
    sseBootstrapCursor?.schema?.maxLength !== 19 ||
    sseEventFormat?.schema?.enum?.join(",") !== "message" ||
    !sseHeaderCursor?.description?.includes("takes precedence") ||
    !sse.responses["200"].description.includes("legacy") ||
    !sse.responses["200"].description.includes("event_format=message")) {
  throw new Error("SSE native EventSource bootstrap, format or precedence contract drifted");
}
for (const field of ["id", "type", "occurred_at", "workspace_id", "resource_ref", "resource_version", "payload_summary", "schema_version"]) {
  if (!schemas.ServerEventEnvelope.required.includes(field)) throw new Error(`ServerEventEnvelope must require ${field}`);
}
if (schemas.ServerEventPayloadSummary.properties.scope_kind?.enum?.join(",") !== "collection,workspace_attachments") {
  throw new Error("ServerEventPayloadSummary.scope_kind must retain the two delivered Export scopes");
}
for (const [schemaName, fields] of [
  ["Conversation", ["archived_at"]],
  ["Question", ["workspace_id"]],
  ["AnswerCitation", ["workspace_id", "index_version_id", "href"]],
  ["RAGResultCitation", ["workspace_id", "index_version_id"]],
  ["AnswerFeedback", ["workspace_id", "feedback_type"]],
]) {
  for (const field of fields) {
    if (!schemas[schemaName].required.includes(field) || !schemas[schemaName].properties[field]) {
      throw new Error(`${schemaName} must expose ${field}`);
    }
  }
}
if (schemas.AnswerFeedback.properties.type || schemas.AnswerFeedback.required.includes("type")) {
  throw new Error("AnswerFeedback must use feedback_type on the wire");
}
if (schemas.RetrievalSummary.properties.rewrites.minItems !== 0 ||
    schemas.RAGAnswerPayload.properties.assertions.maxItems !== 500 ||
    schemas.RAGAnswerPayload.properties.citations.maxItems !== 500 ||
    schemas.RAGAnswerPayload.properties.conflict_positions.maxItems !== 500 ||
    schemas.RAGAnswerPayload.properties.related_topics.maxItems !== 50 ||
    schemas.RAGAnswerPayload.properties.follow_up_questions.items["x-max-utf8-bytes"] !== 2048 ||
    schemas.RAGAnswerPayload.properties.citations.items.$ref !== "#/components/schemas/RAGResultCitation" ||
    schemas.Answer.properties.citations.maxItems !== 500 ||
    schemas.Answer.properties.citations.items.$ref !== "#/components/schemas/AnswerCitation") {
  throw new Error("RAG v2 and retrieval summary public bounds drifted from domain contracts");
}
if (schemas.RetrievalDegradation.properties.capability.enum.join(",") !== "vector,rerank" ||
    schemas.RAGAnswerPayload.properties.assertions.items.$ref !== "#/components/schemas/RAGAssertion" ||
    schemas.RAGAnswerPayload.properties.conflict_positions.items.$ref !== "#/components/schemas/RAGConflictPosition") {
  throw new Error("RAG typed payload schemas drifted from runtime wire contracts");
}
if (!schemas.Answer.required.includes("current_stage") ||
    schemas.Answer.properties.current_stage.enum.join(",") !== "plan.started,plan.completed,retrieval.started,retrieval.completed,validation.started,validation.completed,") {
  throw new Error("Answer current_stage must remain nullable and limited to the six persisted RAG stages");
}
const lastEventID = sse.parameters.find((item) => item.name === "Last-Event-ID");
if (lastEventID?.schema?.pattern !== "^[1-9][0-9]*$" ||
    document.paths["/api/v1/events"].get.responses["200"].headers?.["Cache-Control"]?.schema?.const !== "no-store") {
  throw new Error("SSE cursor or cache-control contract drifted");
}
if (schemas.SearchCursor.type !== "string" || schemas.SearchCursor.maxLength !== 2048) {
  throw new Error("SearchCursor must remain an opaque string with maxLength 2048");
}
if (schemas.SearchRequest.additionalProperties !== false ||
    schemas.SearchRequest.properties?.cursor?.$ref !== "#/components/schemas/SearchCursor" ||
    !schemas.SearchRequest.required?.includes("workspace_id") || !schemas.SearchRequest.required?.includes("query")) {
  throw new Error("SearchRequest strict contract drifted");
}
const searchQuery = schemas.SearchRequest.properties?.query;
if (searchQuery?.type !== "string" || searchQuery.minLength !== 1 || searchQuery.maxLength !== 8192 ||
    searchQuery["x-max-utf8-bytes"] !== 8192 ||
    typeof searchQuery.pattern !== "string" || !searchQuery.pattern.includes("\\u0000") ||
    typeof searchQuery.description !== "string" || !searchQuery.description.includes("8192 UTF-8 bytes") ||
    !searchQuery.description.includes("non-empty") || !searchQuery.description.includes("no NUL")) {
  throw new Error("SearchRequest.query UTF-8 byte, whitespace or NUL contract drifted");
}
if (schemas.SearchResponse.additionalProperties !== false ||
    schemas.SearchResponse.properties?.next_cursor?.$ref !== "#/components/schemas/SearchCursor" ||
    schemas.SearchResponse.properties?.items?.items?.$ref !== "#/components/schemas/SearchEvidence") {
  throw new Error("SearchResponse strict contract drifted");
}
if (!schemas.VectorDistance.required?.includes("distance") || "score" in (schemas.VectorDistance.properties ?? {})) {
  throw new Error("VectorDistance must expose raw distance and must not expose similarity score");
}
if (schemas.Problem.additionalProperties !== false || schemas.Problem.properties?.workflow_run_id?.format !== "uuid") {
  throw new Error("Problem strict schema or workflow_run_id format drifted");
}
const memoryOperations = [
  ["/api/v1/memories", "get", "200", "MemoryPage", ["400", "401", "403", "405", "500", "503"]],
  ["/api/v1/memories", "post", "201", "MemoryCommandResult", ["200", "400", "401", "403", "405", "409", "415", "500", "503"]],
  ["/api/v1/memories/{memory_id}", "get", "200", "Memory", ["400", "401", "403", "404", "405", "500", "503"]],
  ["/api/v1/memories/{memory_id}", "put", "200", "MemoryCommandResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/memories/{memory_id}", "delete", "200", "MemoryCommandResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/memories/{memory_id}/confirm", "post", "200", "MemoryCommandResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/memories/{memory_id}/pause", "post", "200", "MemoryCommandResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/memories/{memory_id}/resume", "post", "200", "MemoryCommandResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
];
for (const [path, method, successStatus, successSchema, statuses] of memoryOperations) {
  const operation = document.paths[path]?.[method];
  if (!operation) throw new Error(`missing Memory operation ${method.toUpperCase()} ${path}`);
  const expectedCapability = method === "get" ? "READ_LOCAL" : "WRITE_PROPOSAL";
  if (operation.security !== undefined || operation["x-required-capability"] !== expectedCapability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${expectedCapability}`);
  }
  if (operation.responses?.[successStatus]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}`) {
    throw new Error(`invalid Memory ${successStatus} success schema for ${method.toUpperCase()} ${path}`);
  }
  for (const status of statuses) {
    const response = resolveRef(operation.responses?.[status]);
    if (status === "200" && method === "post" && path === "/api/v1/memories") {
      if (response?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/MemoryCommandResult") {
        throw new Error("Memory candidate replay must return MemoryCommandResult");
      }
      continue;
    }
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Memory ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
for (const [path, method, requestSchema, bodyLimit] of [
  ["/api/v1/memories", "post", "CreateMemoryCandidateRequest", 131072],
  ["/api/v1/memories/{memory_id}", "put", "EditMemoryRequest", 131072],
  ["/api/v1/memories/{memory_id}", "delete", "MemoryTransitionRequest", 4096],
  ["/api/v1/memories/{memory_id}/confirm", "post", "MemoryTransitionRequest", 4096],
  ["/api/v1/memories/{memory_id}/pause", "post", "MemoryTransitionRequest", 4096],
  ["/api/v1/memories/{memory_id}/resume", "post", "MemoryTransitionRequest", 4096],
]) {
  const operation = document.paths[path][method];
  if (!operation.parameters?.some((item) => item.$ref === "#/components/parameters/IdempotencyKey") ||
      operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== bodyLimit ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}`) {
    throw new Error(`Memory mutation contract drifted for ${method.toUpperCase()} ${path}`);
  }
}
const memoryList = document.paths["/api/v1/memories"].get;
const memoryQuery = (name) => memoryList.parameters.find((item) => item.name === name)?.schema;
if (!memoryList.parameters.some((item) => item.$ref === "#/components/parameters/WorkspaceIDQuery") ||
    memoryQuery("limit")?.minimum !== 1 || memoryQuery("limit")?.maximum !== 100 || memoryQuery("limit")?.default !== 50 ||
    memoryQuery("cursor")?.minLength !== 1 || memoryQuery("cursor")?.maxLength !== 4096 ||
    memoryQuery("type")?.items?.enum?.join(",") !== "PREFERENCE,EPISODIC,GOAL,FEEDBACK" ||
    memoryQuery("status")?.items?.enum?.join(",") !== "CANDIDATE,ACTIVE,PAUSED,EXPIRED,DELETED") {
  throw new Error("Memory list Workspace, cursor, limit or lifecycle filters drifted");
}
for (const path of ["/api/v1/memories/{memory_id}", "/api/v1/memories/{memory_id}/confirm", "/api/v1/memories/{memory_id}/pause", "/api/v1/memories/{memory_id}/resume"]) {
  const pathItem = document.paths[path];
  const parameters = [...(pathItem.parameters ?? []), ...(pathItem.get?.parameters ?? []), ...(pathItem.post?.parameters ?? []), ...(pathItem.put?.parameters ?? []), ...(pathItem.delete?.parameters ?? [])];
  if (!parameters.some((item) => item.name === "memory_id" && item.in === "path" && item.required === true && item.schema?.format === "uuid")) {
    throw new Error(`Memory path identifier contract drifted for ${path}`);
  }
}
for (const schemaName of ["MemorySource", "MemoryContent", "Memory", "CreateMemoryCandidateRequest", "EditMemoryRequest", "MemoryTransitionRequest", "MemoryCommandResult", "MemoryPage"]) {
  if (!schemas[schemaName]) throw new Error(`missing ${schemaName} schema`);
}
if (schemas.Memory.additionalProperties !== false || schemas.Memory.required?.join(",") !== "id,workspace_id,type,content,source,status,version,created_at,updated_at" ||
    schemas.Memory.properties.owner !== undefined || schemas.Memory.properties.confirmed_by !== undefined ||
    schemas.Memory.properties.type.enum?.join(",") !== "PREFERENCE,EPISODIC,GOAL,FEEDBACK" ||
    schemas.Memory.properties.status.enum?.join(",") !== "CANDIDATE,ACTIVE,PAUSED,EXPIRED,DELETED" ||
    schemas.MemoryContent.minProperties !== 1 || schemas.MemoryContent.maxProperties !== 64 || schemas.MemoryContent["x-max-json-bytes"] !== 16384 ||
    schemas.MemoryContent["x-max-string-bytes"] !== 4096 || schemas.MemoryContent["x-max-depth"] !== 8 || schemas.MemoryContent["x-max-array-items"] !== 128 ||
    schemas.MemorySource.required?.join(",") !== "type,ref" || schemas.MemorySource.properties.type.enum?.join(",") !== "USER,AGENT,INTERVIEW" ||
    schemas.MemorySource.properties.ref?.maxLength !== 512 || schemas.MemorySource.properties.ref?.["x-max-utf8-bytes"] !== 512 ||
    schemas.MemorySource.properties.ref?.pattern !== "^\\S(?:[\\s\\S]*\\S)?$") {
  throw new Error("Memory public model must preserve lifecycle and credential-identity privacy boundaries");
}
if (schemas.CreateMemoryCandidateRequest.additionalProperties !== false || schemas.CreateMemoryCandidateRequest.required?.join(",") !== "workspace_id,type,content" ||
    schemas.CreateMemoryCandidateRequest.properties.source !== undefined ||
    schemas.EditMemoryRequest.additionalProperties !== false || schemas.EditMemoryRequest.required?.join(",") !== "workspace_id,expected_version,content" ||
    schemas.EditMemoryRequest.properties.source !== undefined || schemas.EditMemoryRequest.properties.type !== undefined ||
    schemas.MemoryTransitionRequest.additionalProperties !== false || schemas.MemoryTransitionRequest.required?.join(",") !== "workspace_id,expected_version" ||
    schemas.MemoryTransitionRequest.properties.expected_version.minimum !== 1 ||
    schemas.MemoryCommandResult.required?.join(",") !== "memory,replayed" || schemas.MemoryPage.required?.join(",") !== "workspace_id,items" ||
    schemas.MemoryPage.properties.items.maxItems !== 100 || schemas.MemoryPage.properties.next_cursor.maxLength !== 4096) {
  throw new Error("Memory strict request, replay, version or pagination contract drifted");
}
for (const schemaName of ["CreateMemoryCandidateRequest", "EditMemoryRequest"]) {
  const properties = schemas[schemaName].properties;
  if (properties.task_scope_id?.type !== "string" || properties.task_scope_id?.format !== "uuid" || properties.task_scope_id?.nullable === true ||
      properties.expires_at?.type !== "string" || properties.expires_at?.format !== "date-time" || properties.expires_at?.nullable === true) {
    throw new Error(`${schemaName} optional scope and expiry fields must remain non-null strings`);
  }
}
const interviewOperations = [
  ["/api/v1/review/interviews", "get", "200", "InterviewSessionPage", ["400", "401", "403", "405", "500", "503"]],
  ["/api/v1/review/interviews", "post", "201", "InterviewStartResult", ["200", "400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/review/interviews/{session_id}", "get", "200", "InterviewSnapshot", ["400", "401", "403", "404", "405", "500", "503"]],
  ["/api/v1/review/interviews/{session_id}/turns", "post", "200", "InterviewTurnResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/review/interviews/{session_id}/complete", "post", "200", "InterviewCompletionResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/review/interviews/{session_id}/learning-paths/{path_id}/steps/{step_id}/memory-candidate", "post", "201", "InterviewMemoryCandidateResult", ["200", "400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/review/learning-paths/{path_id}/status", "put", "200", "LearningPathStatusResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/review/learning-paths/{path_id}/steps/{step_id}", "put", "200", "LearningPathStepResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
];
for (const [path, method, successStatus, successSchema, statuses] of interviewOperations) {
  const operation = document.paths[path]?.[method];
  if (!operation) throw new Error(`missing Interview operation ${method.toUpperCase()} ${path}`);
  const expectedCapability = method === "get" ? "READ_LOCAL" : "WRITE_PROPOSAL";
  if (operation.security !== undefined || operation["x-required-capability"] !== expectedCapability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${expectedCapability}`);
  }
  if (operation.responses?.[successStatus]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}`) {
    throw new Error(`invalid Interview ${successStatus} success schema for ${method.toUpperCase()} ${path}`);
  }
  for (const status of statuses) {
    const response = resolveRef(operation.responses?.[status]);
    if (status === "200" && successStatus === "201") {
      if (response?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${successSchema}`) throw new Error(`Interview replay must return ${successSchema}`);
      continue;
    }
    if (!response || response.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
      throw new Error(`invalid Interview ${status} Problem schema for ${method.toUpperCase()} ${path}`);
    }
  }
}
const interviewCandidateReplayDescription = document.paths["/api/v1/review/interviews/{session_id}/learning-paths/{path_id}/steps/{step_id}/memory-candidate"]?.post?.responses?.["200"]?.description ?? "";
if (!interviewCandidateReplayDescription.includes("Idempotency-Key") || !interviewCandidateReplayDescription.includes("Interview provenance")) {
  throw new Error("Interview Memory Candidate 200 must document exact-key replay and semantic provenance reuse");
}
for (const [path, method, requestSchema] of [
  ["/api/v1/review/interviews", "post", "StartInterviewRequest"],
  ["/api/v1/review/interviews/{session_id}/turns", "post", "SubmitInterviewTurnRequest"],
  ["/api/v1/review/interviews/{session_id}/complete", "post", "CompleteInterviewRequest"],
  ["/api/v1/review/interviews/{session_id}/learning-paths/{path_id}/steps/{step_id}/memory-candidate", "post", "InterviewMemoryCandidateRequest"],
  ["/api/v1/review/learning-paths/{path_id}/status", "put", "UpdateLearningPathStatusRequest"],
  ["/api/v1/review/learning-paths/{path_id}/steps/{step_id}", "put", "UpdateLearningPathStepRequest"],
]) {
  const operation = document.paths[path][method];
  if (!operation.parameters?.some((item) => item.$ref === "#/components/parameters/IdempotencyKey") ||
      operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== 131072 ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}`) {
    throw new Error(`Interview idempotent mutation contract drifted for ${method.toUpperCase()} ${path}`);
  }
}
const interviewGet = document.paths["/api/v1/review/interviews/{session_id}"].get;
if (!interviewGet.parameters?.some((item) => item.$ref === "#/components/parameters/WorkspaceIDQuery")) {
  throw new Error("Interview recovery must remain Workspace-scoped");
}
const interviewList = document.paths["/api/v1/review/interviews"].get;
const interviewListLimit = interviewList.parameters?.find((item) => item.name === "limit");
const interviewListCursor = interviewList.parameters?.find((item) => item.name === "cursor");
if (interviewList.parameters?.length !== 3 || interviewList.parameters[0]?.$ref !== "#/components/parameters/WorkspaceIDQuery" ||
    interviewListLimit?.in !== "query" || interviewListLimit.required === true || interviewListLimit.schema?.type !== "integer" ||
    interviewListLimit.schema.minimum !== 1 || interviewListLimit.schema.maximum !== 100 || interviewListLimit.schema.default !== 20 ||
    interviewListCursor?.in !== "query" || interviewListCursor.required === true || interviewListCursor.schema?.type !== "string" ||
    interviewListCursor.schema.minLength !== 1 || interviewListCursor.schema.maxLength !== 4096 || interviewListCursor.schema["x-max-utf8-bytes"] !== 4096 ||
    !interviewListCursor.description?.includes("Opaque, Workspace-bound")) {
  throw new Error("Interview list Workspace, cursor or limit contract drifted");
}
for (const [path, requiredIDs] of [
  ["/api/v1/review/interviews/{session_id}", ["session_id"]],
  ["/api/v1/review/interviews/{session_id}/turns", ["session_id"]],
  ["/api/v1/review/interviews/{session_id}/complete", ["session_id"]],
  ["/api/v1/review/interviews/{session_id}/learning-paths/{path_id}/steps/{step_id}/memory-candidate", ["session_id", "path_id", "step_id"]],
  ["/api/v1/review/learning-paths/{path_id}/status", ["path_id"]],
  ["/api/v1/review/learning-paths/{path_id}/steps/{step_id}", ["path_id", "step_id"]],
]) {
  const pathItem = document.paths[path];
  const parameters = [...(pathItem.parameters ?? []), ...(pathItem.get?.parameters ?? []), ...(pathItem.post?.parameters ?? []), ...(pathItem.put?.parameters ?? []), ...(pathItem.delete?.parameters ?? [])];
  for (const id of requiredIDs) {
    if (!parameters.some((item) => item.name === id && item.in === "path" && item.required === true && item.schema?.format === "uuid")) throw new Error(`Interview ${id} path contract drifted for ${path}`);
  }
}
for (const schemaName of ["InterviewScope", "InterviewConfig", "InterviewEvidence", "InterviewSession", "InterviewSessionPage", "InterviewQuestion", "InterviewScore", "InterviewTurn", "InterviewReport", "LearningPath", "LearningPathStep", "StartInterviewRequest", "SubmitInterviewTurnRequest", "CompleteInterviewRequest", "InterviewMemoryCandidateRequest", "InterviewMemoryCandidateResult", "UpdateLearningPathStatusRequest", "UpdateLearningPathStepRequest", "InterviewStartResult", "InterviewSnapshot", "InterviewTurnResult", "InterviewCompletionResult", "LearningPathStatusResult", "LearningPathStepResult"]) {
  if (!schemas[schemaName]) throw new Error(`missing ${schemaName} schema`);
}
const interviewListForbiddenFields = ["questions", "turns", "answer_points", "evidence", "user_answer", "request_hash", "idempotency_key", "receipt"];
if (schemas.InterviewSessionPage.additionalProperties !== false || schemas.InterviewSessionPage.required?.join(",") !== "workspace_id,items" ||
    Object.keys(schemas.InterviewSessionPage.properties ?? {}).join(",") !== "workspace_id,items,next_cursor" ||
    schemas.InterviewSessionPage.properties.workspace_id?.format !== "uuid" || schemas.InterviewSessionPage.properties.items?.maxItems !== 100 ||
    schemas.InterviewSessionPage.properties.items?.items?.$ref !== "#/components/schemas/InterviewSession" ||
    schemas.InterviewSessionPage.properties.next_cursor?.minLength !== 1 || schemas.InterviewSessionPage.properties.next_cursor?.maxLength !== 4096 ||
    schemas.InterviewSessionPage.properties.next_cursor?.["x-max-utf8-bytes"] !== 4096 ||
    interviewListForbiddenFields.some((field) => schemas.InterviewSessionPage.properties?.[field] !== undefined || schemas.InterviewSession.properties?.[field] !== undefined)) {
  throw new Error("Interview Session list response contract drifted");
}
if (schemas.InterviewConfig.additionalProperties !== false || schemas.InterviewConfig.required?.join(",") !== "schema_version,role,scope,difficulty,duration_minutes,question_count,max_follow_ups" ||
    schemas.InterviewConfig.properties.schema_version.const !== "interview/v1" || schemas.InterviewConfig.properties.difficulty.enum?.join(",") !== "FOUNDATION,INTERMEDIATE,ADVANCED" ||
    schemas.InterviewConfig.properties.duration_minutes.maximum !== 240 || schemas.InterviewConfig.properties.question_count.maximum !== 20 || schemas.InterviewConfig.properties.max_follow_ups.maximum !== 20 ||
    schemas.InterviewEvidence.properties.schema_version.const !== "interview-evidence/v1" || schemas.InterviewEvidence.properties.support_type.const !== "SUPPORTS" ||
    schemas.InterviewEvidence.properties.evidence_hash.pattern !== "^[0-9a-f]{64}$") {
  throw new Error("Interview config or formal SUPPORTS Evidence contract drifted");
}
const interviewQuestionFields = ["id", "workspace_id", "session_id", "question_no", "follow_up_no", "parent_question_id", "claim_id", "topic_id", "prompt", "status", "created_at", "answered_at", "source_kind"];
const interviewQuestionRequired = ["id", "workspace_id", "session_id", "question_no", "follow_up_no", "claim_id", "prompt", "status", "created_at"];
for (const [union, claim, note] of [
  ["InterviewScopeV2", "InterviewClaimScope", "InterviewNoteScope"],
  ["InterviewQuestionV2", "InterviewClaimQuestion", "InterviewNoteQuestion"],
  ["InterviewScoreV2", "InterviewClaimScore", "InterviewNoteScore"],
  ["InterviewFindingV2", "InterviewClaimFinding", "InterviewNoteFinding"],
  ["LearningPathStepV2", "LearningPathClaimStep", "LearningPathNoteStep"],
]) {
  if (schemas[union]?.oneOf?.map((branch) => branch.$ref).join(",") !== `#/components/schemas/${claim},#/components/schemas/${note}` ||
      schemas[claim]?.additionalProperties !== false || schemas[note]?.additionalProperties !== false) {
    throw new Error(`${union} must remain a closed formal-Claim / frozen-Note union`);
  }
}
const claimQuestion = schemas.InterviewClaimQuestion;
const noteQuestion = schemas.InterviewNoteQuestion;
const questionHiddenFields = ["answer_points", "evidence", "note_source", "sources", "follow_up_plan", "user_answer", "model_run_id"];
if (Object.keys(claimQuestion.properties).join(",") !== interviewQuestionFields.join(",") || claimQuestion.required?.join(",") !== interviewQuestionRequired.join(",") ||
    claimQuestion.properties.claim_id?.format !== "uuid" || claimQuestion.properties.source_kind?.const !== "CLAIM" || claimQuestion.required.includes("source_kind") ||
    Object.keys(noteQuestion.properties).join(",") !== [...interviewQuestionFields.filter((field) => field !== "topic_id"), "note_item"].join(",") ||
    noteQuestion.required?.join(",") !== [...interviewQuestionRequired, "source_kind", "note_item"].join(",") ||
    noteQuestion.properties.source_kind?.const !== "NOTE_REVISION" || noteQuestion.properties.claim_id?.type !== "null" ||
    noteQuestion.properties.note_item?.$ref !== "#/components/schemas/InterviewNoteItem" ||
    [claimQuestion, noteQuestion].some((schema) => questionHiddenFields.some((field) => schema.properties[field] !== undefined)) ||
    schemas.InterviewTurn.additionalProperties !== false || schemas.InterviewTurn.properties.user_answer !== undefined || schemas.InterviewTurn.properties.idempotency_key !== undefined || schemas.InterviewTurn.properties.request_hash !== undefined ||
    schemas.InterviewClaimScore.properties.schema_version.const !== "interview-score/v1" || schemas.InterviewNoteScore.properties.schema_version.const !== "interview-score/v1" || schemas.InterviewReport.properties.schema_version.const !== "interview-report/v1" ||
    schemas.InterviewReport.properties.artifact.$ref !== "#/components/schemas/InterviewReportArtifactBinding" || schemas.LearningPath.properties.artifact.$ref !== "#/components/schemas/LearningPathArtifactBinding" ||
    schemas.InterviewReportArtifactBinding.allOf?.[1]?.properties?.kind?.const !== "INTERVIEW_DOC" || schemas.LearningPathArtifactBinding.allOf?.[1]?.properties?.kind?.const !== "LEARNING_PATH") {
  throw new Error("Interview must hide scoring answers/raw submissions and retain immutable Artifact kind bindings");
}
if (schemas.StartInterviewRequest.properties.config?.$ref !== "#/components/schemas/InterviewClaimConfig" ||
    schemas.InterviewClaimConfig.properties.scope?.$ref !== "#/components/schemas/InterviewClaimScope" ||
    Object.keys(schemas.InterviewNoteScope.properties).join(",") !== "note_revision" || schemas.InterviewNoteScope.required?.join(",") !== "note_revision" ||
    Object.keys(schemas.InterviewNoteItem.properties).join(",") !== "revision,item_id,item_kind" ||
    schemas.InterviewNoteQuestionSource.properties.sources?.maxItems !== 256 || schemas.InterviewReportV2.properties.note_sources?.maxItems !== 20) {
  throw new Error("Note interviews must freeze a published revision through preparation and hide sources before answering");
}
for (const name of ["InterviewNoteScore", "InterviewNoteFinding", "LearningPathNoteStep"]) {
  const schema = schemas[name];
  if (!schema.required?.includes("note_source") || schema.properties.note_source?.$ref !== "#/components/schemas/InterviewNoteQuestionSource" ||
      name !== "LearningPathNoteStep" && schema.properties.evidence?.maxItems !== 0 ||
      name !== "InterviewNoteScore" && (schema.properties.claim_id?.type !== "null" || schema.properties.source_kind?.const !== "NOTE_REVISION" || schema.properties.topic_id !== undefined)) {
    throw new Error(`${name} must expose exact note sources without fabricating formal Claim evidence`);
  }
}
if (["source_version_id", "source_span_id", "evidence_hash"].some((field) => schemas.LearningPathNoteStep.properties[field]?.type !== "null") ||
    schemas.InterviewClaimScore.properties.evidence?.minItems !== 1 || schemas.InterviewClaimFinding.properties.evidence?.minItems !== 1 ||
    schemas.LearningPathClaimStep.properties.claim_id?.format !== "uuid") {
  throw new Error("Claim interview contracts must retain formal evidence while Note steps keep those fields null");
}
if (schemas.StartInterviewRequest.required?.join(",") !== "workspace_id,config" || schemas.SubmitInterviewTurnRequest.required?.join(",") !== "workspace_id,question_id" ||
    schemas.SubmitInterviewTurnRequest.properties.user_answer["x-max-utf8-bytes"] !== 65536 || schemas.SubmitInterviewTurnRequest.properties.user_answer.default !== "" || schemas.CompleteInterviewRequest.required?.join(",") !== "workspace_id" || schemas.CompleteInterviewRequest.properties.manual_end.default !== false ||
    schemas.InterviewMemoryCandidateRequest.additionalProperties !== false || schemas.InterviewMemoryCandidateRequest.required?.join(",") !== "workspace_id" || Object.keys(schemas.InterviewMemoryCandidateRequest.properties ?? {}).join(",") !== "workspace_id" || schemas.InterviewMemoryCandidateRequest.properties.workspace_id.type !== "string" || schemas.InterviewMemoryCandidateRequest.properties.workspace_id.format !== "uuid" ||
    schemas.InterviewMemoryCandidateResult.additionalProperties !== false || schemas.InterviewMemoryCandidateResult.required?.join(",") !== "memory_id,replayed" || Object.keys(schemas.InterviewMemoryCandidateResult.properties ?? {}).join(",") !== "memory_id,replayed" || schemas.InterviewMemoryCandidateResult.properties.memory_id.type !== "string" || schemas.InterviewMemoryCandidateResult.properties.memory_id.format !== "uuid" || schemas.InterviewMemoryCandidateResult.properties.replayed.type !== "boolean" ||
    schemas.UpdateLearningPathStatusRequest.properties.status.enum?.join(",") !== "ACTIVE,PAUSED,COMPLETED" || schemas.UpdateLearningPathStepRequest.properties.status.enum?.join(",") !== "IN_PROGRESS,COMPLETED,SKIPPED" ||
    schemas.UpdateLearningPathStatusRequest.properties.expected_version.minimum !== 1 || schemas.UpdateLearningPathStepRequest.properties.expected_version.minimum !== 1) {
  throw new Error("Interview or Learning Path command shape drifted");
}
const synthesisPrefix = "/api/v1/workspaces/{workspace_id}/synthesis";
for (const [suffix, method, schema] of [
  ["/notes", "get", "SynthesisNotePage"],
  ["/notes/{note_id}", "get", "SynthesisNoteDetail"],
  ["/notes/{note_id}/revisions", "get", "SynthesisRevisionPage"],
  ["/notes/{note_id}/revisions/{revision_id}", "get", "SynthesisRevisionResult"],
  ["/notes/{note_id}/revisions/{revision_id}/sources/{source_span_id}", "get", "SynthesisSourceView"],
  ["/processing", "get", "SynthesisProcessingPage"],
  ["/processing/{processing_id}", "get", "SynthesisProcessingResult"],
  ["/processing/{processing_id}/retry", "post", "SynthesisRetryResult"],
  ["/notes/{note_id}/interviews", "post", "InterviewNotePreparationResult"],
  ["/notes/{note_id}/interviews", "get", "InterviewNotePreparationPage"],
  ["/notes/{note_id}/interviews/{preparation_id}", "get", "InterviewNotePreparationResult"],
  ["/notes/{note_id}/interviews/{preparation_id}/retry", "post", "InterviewNotePreparationResult"],
]) {
  const operation = document.paths[synthesisPrefix + suffix]?.[method];
  const status = method === "post" ? "202" : "200";
  if (!operation || operation.security !== undefined || operation["x-required-capability"] !== (method === "post" ? "WRITE_PROPOSAL" : "READ_LOCAL") ||
      operation.responses?.[status]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${schema}` ||
      method === "post" && (!operation.parameters?.some((item) => item.$ref === "#/components/parameters/IdempotencyKey") || operation.requestBody?.required !== true)) {
    throw new Error(`Synthesis operation must retain authentication, scope, exact response and idempotency: ${method} ${suffix}`);
  }
}
for (const name of ["SynthesisNote", "SynthesisRevision", "SynthesisProcessing", "SynthesisSourceRef", "SynthesisSourceView", "InterviewNotePreparation"]) {
  if (schemas[name]?.additionalProperties !== false || ["request_hash", "model_run_id", "model_output", "idempotency_key", "delta", "answer_points", "follow_up_plan"].some((field) => schemas[name]?.properties?.[field] !== undefined)) {
    throw new Error(`${name} must remain a closed redacted public projection`);
  }
}
if (schemas.SynthesisItem?.oneOf?.map((item) => item.$ref).join(",") !== "#/components/schemas/SynthesisFactItem,#/components/schemas/SynthesisConflictItem,#/components/schemas/SynthesisGapItem" ||
    schemas.SynthesisProcessingStatus?.enum?.join(",") !== "PENDING,RUNNING,SUCCEEDED,NO_CHANGE,SKIPPED,FAILED,RECOVERY_REQUIRED" ||
    schemas.SynthesisSourceView.properties.availability?.enum?.join(",") !== "AVAILABLE,STALE,UNAVAILABLE" ||
    schemas.SynthesisRetryRequest.required?.join(",") !== "expected_version" || Object.keys(schemas.SynthesisRetryRequest.properties).join(",") !== "expected_version" ||
    schemas.InterviewNotePreparationPage.properties.items?.maxItems !== 20 || schemas.InterviewNotePreparation.properties.options?.$ref !== "#/components/schemas/InterviewNotePreparationOptions") {
  throw new Error("Synthesis content, recovery and interview preparation contracts drifted");
}
for (const [schema, fields] of [
  ["SynthesisNote", ["current_revision_id", "workflow_run_id", "failure"]],
  ["SynthesisProcessing", ["workflow_run_id", "failure", "completed_at"]],
  ["InterviewNotePreparation", ["session_id", "failure"]],
  ["SynthesisSourceView", ["text"]],
]) {
  if (fields.some((field) => !schemas[schema].required?.includes(field) || !schemas[schema].properties[field]?.oneOf?.some((branch) => branch.type === "null"))) {
    throw new Error(`${schema} must retain explicit nullable fields`);
  }
}
const organizingOperations = [
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts", "post", "201", "OrganizingDraftCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}", "get", "200", "OrganizingDraftEnvelope", "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}", "put", "200", "OrganizingDraftCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/suggestions", "post", "200", "OrganizingDraftCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials", "post", "201", "OrganizingDraftCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}", "patch", "200", "OrganizingDraftCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}", "delete", "200", "OrganizingDraftCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/confirm", "post", "202", "OrganizingConfirmResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/materials/search", "get", "200", "OrganizingMaterialSearchPage", "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/snapshots/{snapshot_id}", "get", "200", "OrganizingSnapshotEnvelope", "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates", "get", "200", "OrganizingTemplatePage", "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates", "post", "201", "OrganizingTemplateCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}", "get", "200", "OrganizingTemplateEnvelope", "READ_LOCAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/clone", "post", "201", "OrganizingTemplateCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/revisions", "post", "201", "OrganizingTemplateCommandResult", "WRITE_PROPOSAL"],
  ["/api/v1/workspaces/{workspace_id}/organizing/runs/{snapshot_id}", "get", "200", "OrganizingRunEnvelope", "READ_LOCAL"],
];
for (const [path, method, status, schema, capability] of organizingOperations) {
  const operation = document.paths[path]?.[method];
  if (!operation || operation.security !== undefined || operation["x-required-capability"] !== capability) {
    throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication and require ${capability}`);
  }
  if (operation.responses?.[status]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${schema}`) {
    throw new Error(`invalid Organizing ${status} success schema for ${method.toUpperCase()} ${path}`);
  }
  if (resolveRef(operation.responses?.["405"])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
    throw new Error(`invalid Organizing 405 response for ${method.toUpperCase()} ${path}`);
  }
}
for (const [path, method, requestSchema, bodyLimit] of [
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts", "post", "OrganizingCreateDraftRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}", "put", "OrganizingUpdateDraftRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/suggestions", "post", "OrganizingSuggestRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials", "post", "OrganizingAddMaterialRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}", "patch", "OrganizingMaterialSelectionRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}", "delete", "OrganizingExpectedVersionRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/confirm", "post", "OrganizingConfirmRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates", "post", "OrganizingTemplateCreateRequest", 65536],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/clone", "post", "OrganizingTemplateCloneRequest", 16384],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/revisions", "post", "OrganizingTemplateReviseRequest", 65536],
]) {
  const operation = document.paths[path][method];
  if (!operation.parameters?.some((item) => item.$ref === "#/components/parameters/IdempotencyKey") ||
      operation.requestBody?.required !== true || operation.requestBody?.["x-max-body-bytes"] !== bodyLimit ||
      operation.requestBody?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${requestSchema}`) {
    throw new Error(`Organizing strict idempotent mutation contract drifted for ${method.toUpperCase()} ${path}`);
  }
}
for (const [path, method] of [
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts", "post"],
  ["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials", "post"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates", "post"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/clone", "post"],
  ["/api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/revisions", "post"],
]) {
  const operation = document.paths[path][method];
  const createdSchema = operation.responses?.["201"]?.content?.["application/json"]?.schema?.$ref;
  if (!createdSchema || operation.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !== createdSchema) {
    throw new Error(`Organizing create/replay response schemas drifted for ${method.toUpperCase()} ${path}`);
  }
}
const hasExactDiscriminatorMapping = (schema, expected) => {
  const mapping = schema?.discriminator?.mapping;
  return schema?.discriminator?.propertyName === "kind" && mapping &&
    Object.keys(mapping).length === Object.keys(expected).length &&
    Object.entries(expected).every(([kind, ref]) => mapping[kind] === ref);
};
const organizingAddMapping = {
  SOURCE_VERSION: "#/components/schemas/OrganizingAddSourceVersionRequest",
  DOCUMENT_REVISION: "#/components/schemas/OrganizingAddDocumentRevisionRequest",
  CLAIM: "#/components/schemas/OrganizingAddClaimRequest",
  SMART_COLLECTION: "#/components/schemas/OrganizingAddSmartCollectionRequest",
};
const organizingAddBranches = schemas.OrganizingAddMaterialRequest?.oneOf?.map((item) => schemas[item.$ref?.replace("#/components/schemas/", "")]);
if (organizingAddBranches?.length !== 4 || organizingAddBranches.some((branch) => branch?.additionalProperties !== false) ||
    !hasExactDiscriminatorMapping(schemas.OrganizingAddMaterialRequest, organizingAddMapping) ||
    organizingAddBranches.some((branch) => ["availability", "evidence", "content_hash", "query_hash", "read_model_revision", "title", "score"].some((field) => branch.properties?.[field] !== undefined))) {
  throw new Error("Organizing Add Material must remain an identity-only four-kind discriminated union");
}
const organizingSearchOperation = document.paths["/api/v1/workspaces/{workspace_id}/organizing/materials/search"]?.get;
const organizingSearchParameters = Object.fromEntries((organizingSearchOperation?.parameters ?? []).map((item) => [item.name, item]));
const organizingSearchMapping = {
  SOURCE_VERSION: "#/components/schemas/OrganizingSourceVersionSearchReference",
  DOCUMENT_REVISION: "#/components/schemas/OrganizingDocumentRevisionSearchReference",
  CLAIM: "#/components/schemas/OrganizingClaimSearchReference",
  SMART_COLLECTION: "#/components/schemas/OrganizingSmartCollectionSearchReference",
};
const organizingSearchBranches = schemas.OrganizingMaterialSearchReference?.oneOf?.map((item) => schemas[item.$ref?.replace("#/components/schemas/", "")]);
if (organizingSearchParameters.q?.required !== true || organizingSearchParameters.q?.schema?.["x-min-utf8-bytes"] !== 2 || organizingSearchParameters.q?.schema?.["x-max-utf8-bytes"] !== 256 ||
    organizingSearchParameters.kind?.required !== true || organizingSearchParameters.kind?.schema?.$ref !== "#/components/schemas/OrganizingMaterialKind" ||
    organizingSearchParameters.limit?.schema?.minimum !== 1 || organizingSearchParameters.limit?.schema?.maximum !== 25 || organizingSearchParameters.limit?.schema?.default !== 12 ||
    schemas.OrganizingMaterialSearchPage?.additionalProperties !== false || schemas.OrganizingMaterialSearchPage?.properties?.items?.maxItems !== 25 ||
    schemas.OrganizingMaterialSearchItem?.additionalProperties !== false || schemas.OrganizingMaterialSearchItem?.required?.join(",") !== "workspace_id,kind,title,availability,reference" ||
    schemas.OrganizingMaterialSearchItem?.properties?.evidence !== undefined || organizingSearchBranches?.length !== 4 ||
    !hasExactDiscriminatorMapping(schemas.OrganizingMaterialSearchReference, organizingSearchMapping) ||
    organizingSearchBranches.some((branch) => branch?.additionalProperties !== false || ["version", "content_hash", "query_hash", "read_model_revision", "evidence"].some((field) => branch.properties?.[field] !== undefined))) {
  throw new Error("Organizing material search must remain bounded, Workspace-scoped, and identity-only");
}
if (schemas.OrganizingUpdateDraftRequest?.additionalProperties !== false ||
    schemas.OrganizingUpdateDraftRequest.required?.join(",") !== "expected_version,intent,template_revision_id" ||
    schemas.OrganizingUpdateDraftRequest.properties?.template_revision_id?.format !== "uuid" ||
    schemas.OrganizingMaterialSelectionRequest?.additionalProperties !== false ||
    schemas.OrganizingMaterialSelectionRequest.required?.join(",") !== "expected_version,selected" ||
    schemas.OrganizingMaterialSelectionRequest.properties?.selected?.type !== "boolean" ||
    schemas.OrganizingDraft.required?.join(",") !== "id,workspace_id,intent,status,template_revision_id,confirmed_snapshot_id,version,materials,created_at,updated_at") {
  throw new Error("Organizing Draft must preserve CAS and exact Template Revision selection");
}
if (schemas.OrganizingCreateDraftRequest?.properties?.intent?.minLength !== 1 ||
    schemas.OrganizingTemplateSection?.properties?.key?.maxLength !== 64 ||
    schemas.OrganizingPresentationPolicy?.properties?.tone?.maxLength !== 128 ||
    schemas.OrganizingOutputDefaults?.properties?.filename_pattern?.maxLength !== 256 ||
    schemas.OrganizingTemplateDeclaration?.properties?.description?.minLength !== 1) {
  throw new Error("Organizing OpenAPI input bounds must match the domain declaration validator");
}
const organizingGovernanceSections = schemas.OrganizingTemplateDeclaration?.properties?.sections?.allOf ?? [];
const organizingGovernanceKeys = organizingGovernanceSections.map((entry) => entry.contains?.properties?.key?.const);
if (organizingGovernanceSections.length !== 3 || organizingGovernanceKeys.join(",") !== "conflicts,gaps,sources" ||
    organizingGovernanceSections.some((entry) => entry.minContains !== 1 || entry.maxContains !== 1 ||
      entry.contains?.properties?.required?.const !== true || entry.contains?.required?.join(",") !== "key,required")) {
  throw new Error("Organizing templates must preserve the required conflicts, gaps, and sources governance sections");
}
if (schemas.OrganizingEvidence?.required?.join(",") !== "index_version_id,chunk_id,source_version_id,source_span_id,content_hash,excerpt_hash" ||
    schemas.OrganizingEvidence.additionalProperties !== false || schemas.OrganizingMaterialReference?.properties?.evidence !== undefined) {
  throw new Error("Organizing Evidence must remain a complete separate Citation tuple");
}
if (schemas.OrganizingConfirmResult?.properties?.dispatch_status?.const !== "PENDING" ||
    schemas.OrganizingConfirmResult.properties?.workflow_run_id !== undefined || schemas.OrganizingConfirmResult.properties?.outbox_id !== undefined ||
    document.paths["/api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/confirm"].post.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/OrganizingConfirmResult") {
  throw new Error("Organizing confirmation must expose immutable Snapshot plus true pending dispatch without inventing a Run");
}
const organizingRunBranches = schemas.OrganizingRun?.oneOf ?? [];
const organizingStartedBranch = organizingRunBranches.find((branch) => branch.properties?.dispatch_status?.const === "STARTED");
const organizingPendingBranch = organizingRunBranches.find((branch) => branch.properties?.dispatch_status?.const === "PENDING");
const organizingPoisonedBranch = organizingRunBranches.find((branch) => branch.properties?.dispatch_status?.const === "POISONED");
const organizingStartedStates = organizingStartedBranch?.oneOf ?? [];
const organizingSucceededState = organizingStartedStates.find((branch) => branch.properties?.workflow_status?.const === "succeeded");
const organizingActiveState = organizingStartedStates.find((branch) => branch.properties?.result?.type === "null");
if (schemas.OrganizingRun?.properties?.dispatch_status?.enum?.join(",") !== "PENDING,STARTED,POISONED" ||
    !schemas.OrganizingRun.required?.includes("workflow_status") || schemas.OrganizingRun.properties?.attempt_count?.maximum !== 1000 ||
    organizingRunBranches.length !== 3 || organizingStartedBranch?.properties?.workflow_status?.type !== "string" ||
    organizingStartedBranch?.properties?.binding?.$ref !== "#/components/schemas/OrganizingRunBinding" ||
    organizingStartedStates.length !== 2 || organizingSucceededState?.properties?.result?.$ref !== "#/components/schemas/OrganizingRunResult" ||
    organizingActiveState?.properties?.workflow_status?.enum?.includes("succeeded") !== false ||
    organizingPendingBranch?.properties?.workflow_status?.type !== "null" || organizingPendingBranch?.properties?.retryable?.const !== true ||
    organizingPoisonedBranch?.properties?.workflow_status?.type !== "null" || organizingPoisonedBranch?.properties?.retryable?.const !== false ||
    resolveRef(document.paths["/api/v1/workspaces/{workspace_id}/organizing/runs/{snapshot_id}"].get.responses?.["404"])?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/Problem") {
  throw new Error("Organizing Run must expose true Outbox and Workflow states without inferring either projection");
}
for (const field of ["source_ids", "source_version_ids", "path_prefixes"]) {
  if (schemas.SearchFilter.properties?.[field]?.uniqueItems === true) {
    throw new Error(`SearchFilter.${field} must allow canonicalizable duplicate input`);
  }
}

for (const [path, operationId, responseSchema] of [
  ["/api/v1/health/issues/{issue_id}/observations", "listHealthIssueObservations", "HealthIssueObservationPage"],
  ["/api/v1/health/issues/{issue_id}/decisions", "listHealthIssueDecisions", "HealthIssueDecisionPage"],
]) {
  const operation = document.paths[path]?.get;
  const limit = operation?.parameters?.find((parameter) => parameter.name === "limit");
  const cursor = operation?.parameters?.find((parameter) => parameter.name === "cursor");
  const issueID = operation?.parameters?.find((parameter) => parameter.name === "issue_id");
  if (operation?.operationId !== operationId || issueID?.in !== "path" || issueID?.required !== true || issueID?.schema?.format !== "uuid" ||
      !operation.parameters?.some((parameter) => parameter.$ref === "#/components/parameters/WorkspaceIDQuery") ||
      limit?.schema?.minimum !== 1 || limit?.schema?.maximum !== 100 || limit?.schema?.default !== 25 ||
      cursor?.schema?.minLength !== 1 || cursor?.schema?.maxLength !== 2048 ||
      operation.responses?.["200"]?.content?.["application/json"]?.schema?.$ref !== `#/components/schemas/${responseSchema}` ||
      operation.responses?.["400"]?.$ref !== "#/components/responses/BadRequest" ||
      operation.responses?.["404"]?.$ref !== "#/components/responses/NotFound" ||
      operation.responses?.["503"]?.$ref !== "#/components/responses/Unavailable") {
    throw new Error(`Health bounded-history operation drifted for GET ${path}`);
  }
}
for (const [schemaName, itemSchema] of [
  ["HealthIssueObservationPage", "HealthIssueObservation"],
  ["HealthIssueDecisionPage", "HealthIssueDecision"],
]) {
  const page = schemas[schemaName];
  if (page?.additionalProperties !== false || page.required?.join(",") !== "workspace_id,issue_id,items,has_more" ||
      Object.keys(page.properties ?? {}).join(",") !== "workspace_id,issue_id,items,next_cursor,has_more" ||
      page.properties.workspace_id?.format !== "uuid" || page.properties.issue_id?.format !== "uuid" ||
      page.properties.items?.maxItems !== 100 || page.properties.items?.items?.$ref !== `#/components/schemas/${itemSchema}` ||
      page.properties.next_cursor?.minLength !== 1 || page.properties.next_cursor?.maxLength !== 2048 ||
      page.properties.has_more?.type !== "boolean") {
    throw new Error(`${schemaName} must remain strict, Workspace/Issue-scoped, and bounded to 100 items`);
  }
}
const healthDetail = schemas.HealthIssueDetail;
if (healthDetail?.additionalProperties !== false ||
    healthDetail.required?.join(",") !== "issue,latest_observation,observations,observations_has_more,decisions,decisions_has_more" ||
    Object.keys(healthDetail.properties ?? {}).join(",") !== "issue,latest_observation,observations,observations_next_cursor,observations_has_more,decisions,decisions_next_cursor,decisions_has_more" ||
    healthDetail.properties.latest_observation?.$ref !== "#/components/schemas/HealthIssueObservation" ||
    healthDetail.properties.observations?.minItems !== 1 || healthDetail.properties.observations?.maxItems !== 25 || healthDetail.properties.decisions?.maxItems !== 25 ||
    healthDetail.properties.observations_next_cursor?.maxLength !== 2048 || healthDetail.properties.decisions_next_cursor?.maxLength !== 2048 ||
    healthDetail.properties.observations_has_more?.type !== "boolean" || healthDetail.properties.decisions_has_more?.type !== "boolean") {
  throw new Error("Health Issue detail must expose two bounded first pages with explicit continuation metadata");
}

console.log("OpenAPI contract check passed");
