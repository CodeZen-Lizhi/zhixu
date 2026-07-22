import { readFileSync } from "node:fs";

const document = JSON.parse(readFileSync(new URL("./openapi.json", import.meta.url), "utf8"));
if (document.openapi !== "3.1.0") {
  throw new Error(`expected OpenAPI 3.1.0, got ${document.openapi}`);
}

const requiredOperations = [
  ["/livez", "get", "200"],
  ["/readyz", "get", "200"],
  ["/api/v1/system/status", "get", "200"],
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
if (!schemas.ProposalSummary.required.includes("risk") || schemas.ProposalSummary.properties.risk?.type !== "string") {
  throw new Error("ProposalSummary must retain the human-readable risk description");
}
for (const schemaName of ["ProposalRevision", "KnowledgeChangeRevision"]) {
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
  "GraphCanonicalJSONObject", "GraphNodeRef", "GraphApplicability", "GraphTopicNode", "GraphClaimNode",
  "GraphConfirmation", "GraphEdge", "GraphPageMeta", "GraphFilter", "GraphGlobalRequest", "GraphGlobalCluster",
  "GraphGlobalResponse", "GraphNodeSearchMatch", "GraphNodeSearchResponse", "GraphNeighborhoodRequest",
  "GraphNeighborhoodResponse", "GraphPathRequest", "GraphPathResponse", "GraphRelationDetailResponse",
  "GraphProvenance", "GraphRelationEvidenceItem", "GraphRelationEvidenceResponse",
  "SemanticLinkCandidateEndpoint", "SemanticLinkCandidateEvidence", "SemanticLinkGeneration", "SemanticLinkCandidate",
  "SemanticLinkCandidatePage", "SemanticLinkCandidateDecisionReceipt",
  "FilePatchProposal", "KnowledgeChangeTargetRef", "KnowledgeChangeBaseVersion", "KnowledgeChangeEndpoint",
  "KnowledgeChangeSet", "KnowledgeChangeEvidenceRef", "KnowledgeChangeRevision", "KnowledgeChangeProposal",
]) {
  if (schemas[schemaName].additionalProperties !== false) throw new Error(`${schemaName} must reject unknown properties`);
}
if (schemas.SemanticLinkCandidate.properties.discovery_methods.maxItems !== 6 ||
    schemas.SemanticLinkCandidate.properties.evidence.maxItems !== 100 ||
    schemas.SemanticLinkCandidatePage.properties.items.maxItems !== 100 ||
    schemas.SemanticLinkGeneration.required.join(",") !== "index_version_id,embedding_version_id,rerank_version_id") {
  throw new Error("Semantic Link Candidate bounds or generation contract drifted");
}
if (schemas.Proposal.oneOf?.map((item) => item.$ref).join(",") !==
      "#/components/schemas/FilePatchProposal,#/components/schemas/KnowledgeChangeProposal" ||
    schemas.Proposal.discriminator?.propertyName !== "proposal_type" ||
    schemas.Proposal.discriminator?.mapping?.file_patch !== "#/components/schemas/FilePatchProposal" ||
    schemas.Proposal.discriminator?.mapping?.knowledge_change !== "#/components/schemas/KnowledgeChangeProposal" ||
    !schemas.FilePatchProposal.required.includes("proposal_type") ||
    schemas.FilePatchProposal.properties.proposal_type?.const !== "file_patch" ||
    !schemas.KnowledgeChangeProposal.required.includes("proposal_type") ||
    schemas.KnowledgeChangeProposal.properties.proposal_type.const !== "knowledge_change" ||
    schemas.KnowledgeChangeRevision.properties.schema_version.const !== "knowledge-relation-change/v1" ||
    schemas.KnowledgeChangeRevision.properties.base_versions.minItems !== 2 ||
    schemas.KnowledgeChangeRevision.properties.base_versions.maxItems !== 2) {
  throw new Error("typed Proposal discriminated response contract drifted");
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
