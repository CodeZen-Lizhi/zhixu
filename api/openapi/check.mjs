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
  ["/api/v1/workspaces/{workspace_id}/workflows", "post", "202"],
  ["/api/v1/workflows/{run_id}", "get", "200"],
  ["/api/v1/workflows/{run_id}/human-tasks/{task_id}/decision", "post", "200"],
  ["/api/v1/workspaces/{workspace_id}/proposals", "post", "201"],
  ["/api/v1/proposals/{proposal_id}", "get", "200"],
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
  "HumanDecisionRequest",
  "HumanTask",
  "CreateProposalRequest",
  "ProposalRevision",
  "Approval",
  "ApprovalDecisionResponse",
  "Proposal",
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
]) {
  if (schemas[schemaName].additionalProperties !== false) throw new Error(`${schemaName} must reject unknown properties`);
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
