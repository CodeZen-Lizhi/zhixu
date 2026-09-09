package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// WorkspaceAnalysisDecisionProviderResult is the bounded, provider-neutral
// decision projection. Provider call IDs and reasoning never cross this port.
// A provider error may still carry known usage, which must be settled.
type WorkspaceAnalysisDecisionProviderResult struct {
	Decision domain.WorkspaceAnalysisDecision
	Usage    domain.TokenUsage
}

// WorkspaceAnalysisLoopModelCall describes one canonical request. Invoke is
// called at most once, and only after the durable repository grants CREATED.
// Completed decisions are loaded from their receipt without invoking it.
type WorkspaceAnalysisLoopModelCall struct {
	Ordinal         int
	RequestDocument []byte
	Invoke          func(context.Context) (WorkspaceAnalysisDecisionProviderResult, error)
}

// WorkspaceAnalysisLoopModelCaller owns durable authorization, settlement and
// exact replay for every AGENT call in a loop attempt.
type WorkspaceAnalysisLoopModelCaller interface {
	CallWorkspaceAnalysisDecision(context.Context, WorkspaceAnalysisLoopModelCall) (WorkspaceAnalysisDecisionMutationResult, error)
}

// WorkspaceAnalysisLoopToolInvoker resolves the accepted decision against its
// persisted receipt and invokes the single project Tool ExecutionService. Its
// result contains only the bounded, model-visible projection of that receipt.
type WorkspaceAnalysisLoopToolInvoker interface {
	InvokeWorkspaceAnalysisDecisionTool(context.Context, WorkspaceAnalysisDecisionMutationResult) (AgentToolResult, error)
}

// WorkspaceAnalysisLoopRequest binds the transient Eino loop to project-owned
// durable authorities. The initial messages contain no server identities.
type WorkspaceAnalysisLoopRequest struct {
	Messages    []AgentMessage
	Tools       []AgentToolSpec
	ModelCalls  WorkspaceAnalysisLoopModelCaller
	ToolInvoker WorkspaceAnalysisLoopToolInvoker
}

// WorkspaceAnalysisLoopResult proves only that evidence collection finished.
// Synthesis, citation validation and independent review must still run.
type WorkspaceAnalysisLoopResult struct {
	Finish    WorkspaceAnalysisDecisionMutationResult
	Decisions int
	ToolCalls int
}

// WorkspaceAnalysisLoopRuntime uses the selected Eino ChatModelAgent for the
// generic model/tool protocol. It does not own PostgreSQL recovery or budgets.
type WorkspaceAnalysisLoopRuntime interface {
	RunWorkspaceAnalysisLoop(context.Context, WorkspaceAnalysisLoopRequest) (WorkspaceAnalysisLoopResult, error)
}

// ValidateWorkspaceAnalysisLoopRequest rejects an ambiguous catalog before any
// model authorization. A loop citation check cannot replace the downstream gate.
func ValidateWorkspaceAnalysisLoopRequest(request WorkspaceAnalysisLoopRequest) error {
	if isNilPort(request.ModelCalls) || isNilPort(request.ToolInvoker) || len(request.Messages) != 2 ||
		request.Messages[0].Role != AgentMessageSystem || request.Messages[1].Role != AgentMessageUser ||
		validateAgentMessages(request.Messages) != nil || len(request.Tools) != 4 {
		return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false,
			errors.New("workspace analysis loop binding is invalid"))
	}
	expected := map[string]int64{"ReadGitStatus": 3, "SearchKnowledge": 3, "ReadSource": 4, "ValidateCitation": 4}
	for _, tool := range request.Tools {
		version, found := expected[tool.Name]
		if !found || tool.Ref != (toolsdomain.ToolRef{Name: tool.Name, Version: version}) ||
			tool.Description == "" || len(tool.Description) > toolsdomain.MaxToolDescriptionBytes ||
			len(tool.InputSchema) == 0 || len(tool.InputSchema) > toolsdomain.MaxToolSchemaBytes {
			return applicationError(foundation.ErrorInvalidInput, ErrorCodeAgentRuntimeInvalid, false,
				errors.New("workspace analysis loop catalog is not the frozen read-only catalog"))
		}
		delete(expected, tool.Name)
	}
	return nil
}
