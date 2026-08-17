package postgres

import (
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestValidateWorkspaceAnalysisToolRefusalCommandAllowsOnlyPreExecutorCodes(t *testing.T) {
	command := validWorkspaceAnalysisToolRefusalCommand()
	if err := validateWorkspaceAnalysisToolRefusalCommand(command); err != nil {
		t.Fatalf("valid command: %v", err)
	}
	for _, code := range []string{"TOOL_CONTEXT_STALE", "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", "TOOL_EXECUTION_FAILED", "TOOL_OUTCOME_UNKNOWN"} {
		command.ErrorCode = code
		if err := validateWorkspaceAnalysisToolRefusalCommand(command); err == nil {
			t.Fatalf("post-authorization code %q was accepted", code)
		}
	}
}

func validWorkspaceAnalysisToolRefusalCommand() toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand {
	identity := toolsdomain.TrustedExecutionIdentity{
		WorkspaceID: "93000000-0000-4000-8000-000000000001", DefinitionID: "93000000-0000-4000-8000-000000000002",
		DefinitionVersion: 1, DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkflowRunID: "93000000-0000-4000-8000-000000000003", NodeKey: "inspect_workspace",
		NodeRunID: "93000000-0000-4000-8000-000000000004", NodeAttemptID: "93000000-0000-4000-8000-000000000005",
		LeaseOwner: "worker", LeaseFence: 1,
	}
	return toolsapplication.RecordWorkspaceAnalysisToolRefusalCommand{
		RefusalID: foundation.ID("93000000-0000-4000-8000-000000000006"), Identity: identity,
		OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: foundation.ID("93000000-0000-4000-8000-000000000007"),
			NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeInspectWorkspace,
			Kind:          agentdomain.WorkspaceAnalysisOperationGitStatus,
			Ordinal:       1,
		},
		ErrorCode: "TOOL_NOT_ALLOWED",
	}
}
