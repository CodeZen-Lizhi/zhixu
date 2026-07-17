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

for (const schema of [
  "Liveness",
  "Readiness",
  "SystemStatus",
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
  "Problem",
]) {
  if (!document.components?.schemas?.[schema]) throw new Error(`missing schema ${schema}`);
}

console.log("OpenAPI contract check passed");
