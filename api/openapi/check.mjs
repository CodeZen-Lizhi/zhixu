import { readFileSync } from "node:fs";

const document = JSON.parse(readFileSync(new URL("./openapi.json", import.meta.url), "utf8"));
if (document.openapi !== "3.1.0") {
  throw new Error(`expected OpenAPI 3.1.0, got ${document.openapi}`);
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
  ["/api/v1/workspaces", "post", "201"],
  ["/api/v1/workspaces/{workspace_id}", "get", "200"],
  ["/api/v1/workspaces/{workspace_id}/scan", "post", "200"],
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
  ["/api/v1/auth/session", "get", "SessionInfo", ["401", "405", "503"]],
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
  ["/api/v1/workspaces/{workspace_id}/timeline", "get", ["200"], "KnowledgeTimelinePage", ["400", "401", "403", "405", "500", "503"]],
  ["/api/v1/workspaces/{workspace_id}/timeline/{event_id}", "get", ["200"], "KnowledgeEvent", ["400", "401", "403", "404", "405", "500", "503"]],
  ["/api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis", "post", ["200", "201"], "ImpactAnalysisResult", ["400", "401", "403", "404", "405", "409", "415", "500", "503"]],
  ["/api/v1/workspaces/{workspace_id}/impact-reports/{report_id}", "get", ["200"], "ImpactReport", ["400", "401", "403", "404", "405", "500", "503"]],
];
for (const [path, method, successStatuses, successSchema, errorStatuses] of timelineOperations) {
  const operation = document.paths[path][method];
  if (operation.security?.length === 0) throw new Error(`${method.toUpperCase()} ${path} must inherit business authentication`);
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
    timelineEventTypes?.type !== "array" || timelineEventTypes.maxItems !== 32 || timelineEventTypes.uniqueItems !== true) {
  throw new Error("Knowledge Timeline pagination or event filter bounds drifted");
}
const impactOperation = document.paths["/api/v1/workspaces/{workspace_id}/timeline/{event_id}/impact-analysis"].post;
if (!impactOperation.parameters?.some((parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey") ||
    impactOperation.requestBody?.required !== true || impactOperation.requestBody?.["x-max-body-bytes"] !== 4096 ||
    impactOperation.requestBody?.content?.["application/json"]?.schema?.$ref !== "#/components/schemas/ImpactAnalysisRequest") {
  throw new Error("Impact Analysis must require Idempotency-Key and a bounded empty JSON object");
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
  "Workspace",
  "WorkspaceScan",
  "IngestionRequest",
  "IngestionResult",
  "StartWorkflowRequest",
  "WorkflowStart",
  "WorkflowRun",
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
  "KnowledgeEventCorrelation",
  "KnowledgeEvent",
  "KnowledgeTimelinePage",
  "ImpactAnalysisRequest",
  "ImpactObject",
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
  ["/api/v1/workspaces/{workspace_id}/proposals", "get", ["400", "503", "405"]],
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
const workflowStatuses = ["pending", "running", "waiting_for_human", "retry_wait", "paused", "succeeded", "failed", "cancelled"];
const proposalStatuses = ["draft", "validating", "ready_for_review", "approved", "applying", "applied", "verifying", "completed", "rejected", "needs_revision", "deferred", "apply_failed", "verify_failed", "rolled_back", "cancelled"];
const proposalTypes = ["file_patch", "knowledge_change", "publish_artifact"];
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
if (!schemas.ProposalSummary.required.includes("risk") || schemas.ProposalSummary.properties.risk?.type !== "string") {
  throw new Error("ProposalSummary must retain the human-readable risk description");
}
for (const schemaName of ["ProposalRevision", "KnowledgeChangeRevision", "PublishArtifactRevision"]) {
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
for (const schemaName of ["FilePatchProposal", "KnowledgeChangeProposal"]) {
  if (!schemas[schemaName].required.includes("approval") ||
      schemas[schemaName].properties.approval.oneOf?.[0]?.$ref !== "#/components/schemas/Approval" ||
      schemas[schemaName].properties.approval.oneOf?.[1]?.type !== "null") {
    throw new Error(`${schemaName} must require a nullable persistent Approval snapshot`);
  }
}
const nonFileApprovalForbiddenFields = ["approved_git_head", "workflow_run_id", "workflow_status_url"];
if (schemas.NonFileApproval.allOf?.[0]?.$ref !== "#/components/schemas/Approval" ||
    schemas.NonFileApproval.allOf?.[1]?.not?.anyOf?.map((item) => item.required?.join(",")).join(",") !== nonFileApprovalForbiddenFields.join(",") ||
    !schemas.PublishArtifactProposal.required.includes("approval") ||
    schemas.PublishArtifactProposal.properties.approval.oneOf?.[0]?.$ref !== "#/components/schemas/NonFileApproval" ||
    schemas.PublishArtifactProposal.properties.approval.oneOf?.[1]?.type !== "null" ||
    schemas.ProposalSummary.allOf?.[0]?.if?.properties?.proposal_type?.const !== "publish_artifact" ||
    schemas.ProposalSummary.allOf?.[0]?.then?.properties?.approval?.$ref !== "#/components/schemas/NonFileApproval") {
  throw new Error("publish_artifact Approval snapshots must forbid Git and Workflow fields");
}
const currentContent = schemas.ProposalCurrentContent;
for (const field of ["proposal_id", "workspace_id", "target_path", "content", "current_hash", "base_hash", "base_hash_match"]) {
  if (!currentContent.required.includes(field)) throw new Error(`ProposalCurrentContent must require ${field}`);
}
if (currentContent.additionalProperties !== false || currentContent.properties.content.maxLength !== 1048576 ||
    currentContent.properties.proposal_id.format !== "uuid" || currentContent.properties.workspace_id.format !== "uuid" ||
    currentContent.properties.target_path.minLength !== 1 ||
    currentContent.properties.current_hash.pattern !== "^[0-9a-f]{64}$" || currentContent.properties.base_hash.pattern !== "^[0-9a-f]{64}$" ||
    currentContent.properties.base_hash_match.type !== "boolean" ||
    currentContent["x-invariant"] !== "base_hash_match == (current_hash == base_hash)" ||
    document.paths["/api/v1/proposals/{proposal_id}/current-content"].get.responses["200"].headers?.["Cache-Control"]?.schema?.const !== "no-store") {
  throw new Error("Proposal current-content body or no-store contract drifted");
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
if (!schemas.SystemStatus.required.includes("knowledge_timeline") ||
    schemas.SystemStatus.properties.knowledge_timeline.$ref !== "#/components/schemas/OptionalCapabilityStatus") {
  throw new Error("SystemStatus must expose the Knowledge Timeline capability state");
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

for (const schemaName of [
  "CreateConversationRequest", "Conversation", "ConversationPage", "QuestionScopeRequest", "QuestionScope", "SubmitQuestionRequest", "Question",
  "WorkflowProjection", "QuestionAcceptance", "AnswerCitation", "RAGResultCitation", "RAGAssertion", "RAGConflictPosition", "RelatedTopic", "RAGAnswerPayload", "RAGAnswerResult", "RefusalResult",
  "ClarificationResult", "RetrievalDegradation", "RetrievalScopeSummary", "RetrievalSummary", "Answer", "Turn", "TurnPage",
  "SubmitFeedbackRequest", "AnswerFeedback", "ServerEventPayloadSummary", "ServerEventEnvelope",
  "AuthCapabilityStatus", "SessionCredential", "SessionInfo", "CreateAPITokenRequest", "APITokenInfo", "APITokenCredential", "APITokenPage",
  "KnowledgeEventCorrelation", "KnowledgeEvent", "KnowledgeTimelinePage", "ImpactAnalysisRequest", "ImpactObject", "ImpactReport", "ImpactProposalDraft", "ImpactAnalysisResult",
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
]) {
  if (schemas[schemaName].additionalProperties !== false) throw new Error(`${schemaName} must reject unknown properties`);
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
  "ArtifactOutlineSection", "ArtifactGap", "ArtifactCoverage", "ArtifactGenerationMetadata", "ArtifactCitationInput", "ArtifactCitation",
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

if (schemas.ImpactAnalysisRequest.maxProperties !== 0 || schemas.ImpactAnalysisRequest.additionalProperties !== false ||
    schemas.KnowledgeTimelinePage.properties.items.maxItems !== 100 ||
    schemas.KnowledgeTimelinePage.properties.items.items.$ref !== "#/components/schemas/KnowledgeEvent" ||
    schemas.KnowledgeTimelinePage.properties.next_cursor.maxLength !== 4096 ||
    schemas.ImpactReport.properties.objects.maxItems !== 500 ||
    schemas.ImpactReport.properties.objects.items.$ref !== "#/components/schemas/ImpactObject" ||
    schemas.ImpactAnalysisResult.properties.proposal_drafts.maxItems !== 500 ||
    schemas.ImpactAnalysisResult.properties.proposal_drafts.items.$ref !== "#/components/schemas/ImpactProposalDraft") {
  throw new Error("Timeline/Impact page, empty request or bounded result schemas drifted");
}
if (schemas.KnowledgeEvent.properties.schema_version.const !== "knowledge-event/v1" ||
    schemas.ImpactReport.properties.schema_version.const !== "impact-report/v1" ||
    schemas.ImpactReport.properties.fingerprint.pattern !== "^[0-9a-f]{64}$" ||
    schemas.ImpactObject.properties.type.enum.join(",") !== "RELATION,CONFLICT,HEALTH_ISSUE" ||
    schemas.ImpactProposalDraft.properties.requires_approval.const !== true ||
    schemas.ImpactProposalDraft.properties.requires_write_authorization.const !== true ||
    schemas.ImpactProposalDraft.properties.operation.enum.includes("NO_ACTION")) {
  throw new Error("Timeline/Impact immutable version or Proposal authorization boundary drifted");
}

if (schemas.SemanticLinkCandidate.properties.discovery_methods.maxItems !== 6 ||
    schemas.SemanticLinkCandidate.properties.evidence.maxItems !== 100 ||
    schemas.SemanticLinkCandidatePage.properties.items.maxItems !== 100 ||
    schemas.SemanticLinkGeneration.required.join(",") !== "index_version_id,embedding_version_id,rerank_version_id") {
  throw new Error("Semantic Link Candidate bounds or generation contract drifted");
}
if (schemas.Proposal.oneOf?.map((item) => item.$ref).join(",") !==
      "#/components/schemas/FilePatchProposal,#/components/schemas/KnowledgeChangeProposal,#/components/schemas/PublishArtifactProposal" ||
    schemas.Proposal.discriminator?.propertyName !== "proposal_type" ||
    schemas.Proposal.discriminator?.mapping?.file_patch !== "#/components/schemas/FilePatchProposal" ||
    schemas.Proposal.discriminator?.mapping?.knowledge_change !== "#/components/schemas/KnowledgeChangeProposal" ||
    schemas.Proposal.discriminator?.mapping?.publish_artifact !== "#/components/schemas/PublishArtifactProposal" ||
    !schemas.FilePatchProposal.required.includes("proposal_type") ||
    schemas.FilePatchProposal.properties.proposal_type?.const !== "file_patch" ||
    !schemas.KnowledgeChangeProposal.required.includes("proposal_type") ||
    schemas.KnowledgeChangeProposal.properties.proposal_type.const !== "knowledge_change" ||
    !schemas.PublishArtifactProposal.required.includes("proposal_type") ||
    schemas.PublishArtifactProposal.properties.proposal_type.const !== "publish_artifact" ||
    schemas.KnowledgeChangeRevision.properties.schema_version.const !== "knowledge-relation-change/v1" ||
    schemas.KnowledgeChangeRevision.properties.base_versions.minItems !== 2 ||
    schemas.KnowledgeChangeRevision.properties.base_versions.maxItems !== 2) {
  throw new Error("typed Proposal discriminated response contract drifted");
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
const sse = document.paths["/api/v1/events"].get;
if (sse.responses["200"].content?.["text/event-stream"]?.schema?.$ref !== "#/components/schemas/ServerEventEnvelope" ||
    !sse.parameters.some((item) => item.name === "Last-Event-ID" && item.in === "header")) {
  throw new Error("SSE content type, envelope or Last-Event-ID contract drifted");
}
for (const field of ["id", "type", "occurred_at", "workspace_id", "resource_ref", "resource_version", "payload_summary", "schema_version"]) {
  if (!schemas.ServerEventEnvelope.required.includes(field)) throw new Error(`ServerEventEnvelope must require ${field}`);
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
const lastEventID = document.paths["/api/v1/events"].get.parameters.find((item) => item.name === "Last-Event-ID");
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
for (const field of ["source_ids", "source_version_ids", "path_prefixes"]) {
  if (schemas.SearchFilter.properties?.[field]?.uniqueItems === true) {
    throw new Error(`SearchFilter.${field} must allow canonicalizable duplicate input`);
  }
}

console.log("OpenAPI contract check passed");
