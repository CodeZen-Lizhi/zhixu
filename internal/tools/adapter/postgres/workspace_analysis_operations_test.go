package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestWorkspaceAnalysisToolAdmissionSeparatesDeadlineFromBudget(t *testing.T) {
	tests := []struct {
		name                  string
		deadlineExceeded      bool
		toolBudgetAvailable   bool
		sourceBudgetAvailable bool
		concurrencyAvailable  bool
		wantCode              string
		wantKind              foundation.ErrorKind
		wantDeadlineCause     bool
	}{
		{
			name: "available", toolBudgetAvailable: true,
			sourceBudgetAvailable: true, concurrencyAvailable: true,
		},
		{
			name: "database deadline", deadlineExceeded: true,
			toolBudgetAvailable: true, sourceBudgetAvailable: true, concurrencyAvailable: true,
			wantCode: agentdomain.ErrorCodeWorkspaceAnalysisPreAuthorizationDeadline,
			wantKind: foundation.ErrorNonRetryableFailure, wantDeadlineCause: true,
		},
		{
			name: "tool budget", sourceBudgetAvailable: true, concurrencyAvailable: true,
			wantCode: ErrorCodeContextStale, wantKind: foundation.ErrorVersionConflict,
		},
		{
			name: "source budget", toolBudgetAvailable: true, concurrencyAvailable: true,
			wantCode: ErrorCodeContextStale, wantKind: foundation.ErrorVersionConflict,
		},
		{
			name: "concurrency", toolBudgetAvailable: true, sourceBudgetAvailable: true,
			wantCode: ErrorCodeContextStale, wantKind: foundation.ErrorVersionConflict,
		},
		{
			name: "deadline does not mask budget", deadlineExceeded: true,
			sourceBudgetAvailable: true, concurrencyAvailable: true,
			wantCode: ErrorCodeContextStale, wantKind: foundation.ErrorVersionConflict,
		},
		{
			name: "deadline does not mask concurrency", deadlineExceeded: true,
			toolBudgetAvailable: true, sourceBudgetAvailable: true,
			wantCode: ErrorCodeContextStale, wantKind: foundation.ErrorVersionConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := workspaceAnalysisToolAdmissionError(
				test.deadlineExceeded, test.toolBudgetAvailable, test.sourceBudgetAvailable,
				test.concurrencyAvailable,
			)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("available admission err=%v", err)
				}
				return
			}
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != test.wantCode || classified.Kind != test.wantKind ||
				classified.Retryable || errors.Is(err, context.DeadlineExceeded) != test.wantDeadlineCause {
				t.Fatalf("admission err=%#v", err)
			}
		})
	}
}

func TestWorkspaceAnalysisToolForOperationUsesOnlyFourExactCanonicalTools(t *testing.T) {
	tests := []struct {
		name string
		key  agentdomain.WorkspaceAnalysisOperationKey
		want domain.ToolRef
	}{
		{"git", operationKey("inspect_workspace", "GIT_STATUS", 1), workspaceAnalysisGitStatusRef},
		{"search", operationKey("retrieve_evidence", "KNOWLEDGE_SEARCH", 1), workspaceAnalysisSearchRef},
		{"source", operationKey("read_evidence", "SOURCE_READ", 3), workspaceAnalysisSourceRef},
		{"citation", operationKey("validate_citations", "CITATION_VALIDATION", 1), workspaceAnalysisCitationRef},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(test.key)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := workspaceAnalysisToolForOperation(contract)
			if !ok || got != test.want {
				t.Fatalf("tool=%+v ok=%v want=%+v", got, ok, test.want)
			}
		})
	}
	for _, key := range []agentdomain.WorkspaceAnalysisOperationKey{
		operationKey("retrieve_evidence", "RETRIEVAL_PLAN", 1),
		operationKey("synthesize_answer", "ANSWER_SYNTHESIS", 1),
		operationKey("review_publish", "FAITHFULNESS_REVIEW", 1),
	} {
		contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := workspaceAnalysisToolForOperation(contract); ok {
			t.Fatalf("model operation %s was accepted as a tool", key.Kind)
		}
	}
}

func TestWorkspaceAnalysisBuiltinDefinitionIncludesGitAndCitationContracts(t *testing.T) {
	for _, ref := range []domain.ToolRef{workspaceAnalysisGitStatusRef, workspaceAnalysisSearchRef, workspaceAnalysisSourceRef, workspaceAnalysisCitationRef} {
		definition, err := workspaceAnalysisBuiltinDefinition(ref)
		if err != nil {
			t.Fatalf("definition %s@%d: %v", ref.Name, ref.Version, err)
		}
		if definition.Ref != ref || definition.ResultPersistencePolicy != domain.ResultPersistenceCanonical || definition.InvocationPolicy != domain.InvocationTrustedWorkflowOnly {
			t.Fatalf("definition=%+v", definition)
		}
	}
	definition, err := workspaceAnalysisBuiltinDefinition(workspaceAnalysisGitStatusRef)
	if err != nil {
		t.Fatal(err)
	}
	drifted := definition
	drifted.Timeout += time.Millisecond
	contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(operationKey("inspect_workspace", "GIT_STATUS", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exactWorkspaceAnalysisToolDefinition(drifted, contract, workspaceAnalysisGitStatusRef); err == nil {
		t.Fatal("definition payload drift with a stale definition hash was accepted")
	}
}

func TestWorkspaceAnalysisToolRequestBindingSurvivesAttemptReplacement(t *testing.T) {
	tool := workspaceAnalysisGitStatusRef
	input := domain.SchemaRef{ID: "tool.read_git_status.input", Version: 1}
	output := domain.SchemaRef{ID: "tool.read_git_status.output", Version: 1}
	existing := domain.ToolCall{
		WorkspaceID: authorityTestID(1), WorkflowRunID: authorityTestID(2), NodeRunID: authorityTestID(3),
		NodeAttemptID: authorityTestID(4), CallNo: 1, RequestedToolName: tool.Name, Tool: &tool,
		DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		InputSchema:    &input, OutputSchema: &output, Capability: capability.ReadLocal,
		SideEffectLevel: domain.SideEffectNone, InvocationPolicy: domain.InvocationTrustedWorkflowOnly,
		RequestHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RequestBytes: 2, RequestSummary: json.RawMessage(`{}`),
	}
	replacement := existing
	replacement.NodeAttemptID = authorityTestID(5)
	if !sameWorkspaceAnalysisToolRequestBinding(existing, replacement) {
		t.Fatal("replacement attempt changed only the execution fence but was rejected")
	}
	replacement.CallNo++
	if sameWorkspaceAnalysisToolRequestBinding(existing, replacement) {
		t.Fatal("different call number was accepted as the same logical request")
	}
}

func TestWorkspaceAnalysisSourceAuthorizationBindsOrdinalToCallNumber(t *testing.T) {
	definition, err := workspaceAnalysisBuiltinDefinition(workspaceAnalysisSourceRef)
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.TrustedExecutionIdentity{
		WorkspaceID: authorityTestID(1), DefinitionID: authorityTestID(2), DefinitionVersion: 1,
		DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkflowRunID:  authorityTestID(3), NodeKey: "read_evidence", NodeRunID: authorityTestID(4),
		NodeAttemptID: authorityTestID(5), LeaseOwner: "worker", LeaseFence: 1,
	}
	tool, input, output := definition.Ref, definition.InputSchema, definition.OutputSchema
	call := domain.ToolCall{
		ID: authorityTestID(6), WorkspaceID: identity.WorkspaceID, WorkflowRunID: identity.WorkflowRunID,
		NodeRunID: identity.NodeRunID, NodeAttemptID: identity.NodeAttemptID, CallNo: 1,
		RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: definition.DefinitionHash,
		InputSchema: &input, OutputSchema: &output, Capability: definition.RequiredCapability,
		SideEffectLevel: definition.SideEffectLevel, InvocationPolicy: definition.InvocationPolicy,
		RequestHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RequestBytes: 2, RequestSummary: json.RawMessage(`{}`), Status: domain.CallStarted,
		Version: 1, StartedAt: time.Now().UTC(),
	}
	command := application.AuthorizeWorkspaceAnalysisToolCallCommand{
		Identity: identity,
		OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: authorityTestID(9), NodeKey: "read_evidence",
			Kind: agentdomain.WorkspaceAnalysisOperationSourceRead, Ordinal: 2,
		},
		OperationID: authorityTestID(7), ReservationID: authorityTestID(8), Call: call, Definition: definition,
	}
	if _, _, _, err := validateWorkspaceAnalysisToolAuthorization(command); err == nil {
		t.Fatal("source ordinal two accepted call number one")
	}
	command.Call.CallNo = 2
	if _, _, _, err := validateWorkspaceAnalysisToolAuthorization(command); err != nil {
		t.Fatalf("source ordinal two rejected call number two: %v", err)
	}
}

func operationKey(node string, kind string, ordinal int) agentdomain.WorkspaceAnalysisOperationKey {
	return agentdomain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: authorityTestID(9),
		NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeKey(node),
		Kind:          agentdomain.WorkspaceAnalysisOperationKind(kind),
		Ordinal:       ordinal,
	}
}
