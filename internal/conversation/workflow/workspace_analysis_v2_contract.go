package workflow

import (
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	WorkspaceAnalysisDefinitionVersionV2   int64 = 2
	WorkspaceAnalysisOutputSchemaVersionV2       = 2
	WorkspaceAnalysisNodeDecideNext              = "decide_next"
	WorkspaceAnalysisGraphHashV2                 = "5a2c57b0fae4c1687220140aec2ed763f9cfc2e09fea561a17d44bc8e24c9ad2"
)

// WorkspaceAnalysisNodeKindV2 prevents a v1 executor from claiming a v2 node.
func WorkspaceAnalysisNodeKindV2(nodeKey string) string {
	return "agent.workspace-analysis.v2." + nodeKey
}

// RegisteredWorkspaceAnalysisDefinitionV2 keeps the durable DAG acyclic. The
// decide_next executor follows the separately persisted decision/tool journal.
// Choosing finish only unlocks synthesis; citation and review remain mandatory.
func RegisteredWorkspaceAnalysisDefinitionV2() workflowdomain.RegisteredDefinition {
	noRetry := workflowdomain.RetryPolicy{MaxRetries: 0}
	return workflowdomain.RegisteredDefinition{
		Key: WorkspaceAnalysisDefinitionKey, Version: WorkspaceAnalysisDefinitionVersionV2,
		InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, GraphHash: WorkspaceAnalysisGraphHashV2,
		Graph: workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{
			{Key: WorkspaceAnalysisNodeDecideNext, Kind: WorkspaceAnalysisNodeKindV2(WorkspaceAnalysisNodeDecideNext),
				InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion, OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersionV2,
				RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
				AllowedTools: []toolsdomain.ToolRef{{Name: "ReadGitStatus", Version: 3}, {Name: "ReadSource", Version: 4}, {Name: "SearchKnowledge", Version: 3}, {Name: "ValidateCitation", Version: 4}}},
			{Key: WorkspaceAnalysisNodeReviewPublish, Kind: WorkspaceAnalysisNodeKindV2(WorkspaceAnalysisNodeReviewPublish),
				Dependencies: []string{WorkspaceAnalysisNodeValidateCitations}, InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion,
				OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersionV2, RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions()},
			{Key: WorkspaceAnalysisNodeSynthesizeAnswer, Kind: WorkspaceAnalysisNodeKindV2(WorkspaceAnalysisNodeSynthesizeAnswer),
				Dependencies: []string{WorkspaceAnalysisNodeDecideNext}, InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion,
				OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersionV2, RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions()},
			{Key: WorkspaceAnalysisNodeValidateCitations, Kind: WorkspaceAnalysisNodeKindV2(WorkspaceAnalysisNodeValidateCitations),
				Dependencies: []string{WorkspaceAnalysisNodeSynthesizeAnswer}, InputSchemaVersion: WorkspaceAnalysisInputSchemaVersion,
				OutputSchemaVersion: WorkspaceAnalysisOutputSchemaVersionV2, RetryPolicy: noRetry, RequiredPermissions: workspaceAnalysisReadPermissions(),
				AllowedTools: []toolsdomain.ToolRef{{Name: "ValidateCitation", Version: 4}}},
		}},
	}
}
