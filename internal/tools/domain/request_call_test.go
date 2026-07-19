package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestToolRequestV1Validate(t *testing.T) {
	valid := ToolRequestV1{SchemaVersion: 1, ToolName: "SearchKnowledge", Arguments: json.RawMessage(`{"query":"safe"}`), Reason: "需要检索证据"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tests := []ToolRequestV1{
		{},
		{SchemaVersion: 2, ToolName: valid.ToolName, Arguments: valid.Arguments, Reason: valid.Reason},
		{SchemaVersion: 1, ToolName: "search", Arguments: valid.Arguments, Reason: valid.Reason},
		{SchemaVersion: 1, ToolName: valid.ToolName, Arguments: json.RawMessage(`[]`), Reason: valid.Reason},
		{SchemaVersion: 1, ToolName: valid.ToolName, Arguments: json.RawMessage(`{} {}`), Reason: valid.Reason},
		{SchemaVersion: 1, ToolName: valid.ToolName, Arguments: valid.Arguments, Reason: " "},
	}
	for index, candidate := range tests {
		if err := candidate.Validate(); err == nil {
			t.Fatalf("candidate %d accepted", index)
		}
	}
}

func TestTrustedExecutionIdentityAndIdempotencyBinding(t *testing.T) {
	identity := validExecutionIdentity()
	if err := identity.Validate(); err != nil {
		t.Fatalf("identity Validate: %v", err)
	}
	binding := IdempotencyBinding{
		WorkspaceID: identity.WorkspaceID, WorkflowRunID: identity.WorkflowRunID, NodeRunID: identity.NodeRunID,
		NodeAttemptID: identity.NodeAttemptID, Tool: ToolRef{Name: "ApplyApprovedPatch", Version: 1}, SchemaVersion: 1,
		Capability: capability.WriteKnowledge, TargetRef: "proposal:revision:target", RequestHash: strings.Repeat("a", 64), Key: "tool-call:writeback:1",
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("binding Validate: %v", err)
	}
	binding.Capability = capability.EvaluationRun
	if err := binding.Validate(); err != nil {
		t.Fatalf("exact non-hierarchical capability should still be a valid binding: %v", err)
	}
	binding.Capability = "ADMIN_MAINTENANCE"
	if err := binding.Validate(); err == nil {
		t.Fatal("legacy capability accepted")
	}
}

func TestValidateToolCallLifecycleAndTransition(t *testing.T) {
	started := validStartedCall()
	if err := ValidateToolCall(started); err != nil {
		t.Fatalf("started call: %v", err)
	}
	completed := started.StartedAt.Add(time.Second)
	succeeded := started
	succeeded.Status = CallSucceeded
	succeeded.ResponseHash = strings.Repeat("b", 64)
	succeeded.ResponseBytes = 16
	succeeded.ResponseSummary = json.RawMessage(`{"item_count":1}`)
	succeeded.CompletedAt = &completed
	succeeded.DurationMillis = 1000
	succeeded.Version = 2
	if err := ValidateToolCall(succeeded); err != nil {
		t.Fatalf("succeeded call: %v", err)
	}
	if err := ValidateToolCallTransition(CallStarted, CallSucceeded); err != nil {
		t.Fatalf("valid transition: %v", err)
	}
	if err := ValidateToolCallTransition(CallFailed, CallSucceeded); err == nil {
		t.Fatal("terminal transition accepted")
	}

	unknown := started
	unknown.Status = CallUnknown
	unknown.ErrorCode = "TOOL_OUTCOME_UNKNOWN"
	unknown.CompletedAt = &completed
	unknown.DurationMillis = 1000
	unknown.Version = 2
	if err := ValidateToolCall(unknown); err != nil {
		t.Fatalf("unknown call: %v", err)
	}
	unknown.ResponseHash = strings.Repeat("c", 64)
	if err := ValidateToolCall(unknown); err == nil {
		t.Fatal("unknown call claimed response")
	}
}

func TestToolCallStableReferencesRejectPathsSecretsAndUnknownTypes(t *testing.T) {
	valid := []string{
		"index-version:00000000-0000-4000-8000-000000000001",
		"source-span:00000000-0000-4000-8000-000000000002",
		"citation-batch:" + strings.Repeat("a", 64),
		"diff:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64),
		"git-head:" + strings.Repeat("c", 40),
	}
	for _, value := range valid {
		if !ValidResultReference(value) {
			t.Fatalf("valid result reference rejected: %q", value)
		}
	}
	for _, value := range []string{"/Users/private/source.md", "Authorization:Bearer-secret", "unknown:value", "index-version:not-a-uuid"} {
		if ValidResultReference(value) {
			t.Fatalf("unsafe result reference accepted: %q", value)
		}
	}
	if !ValidSideEffectReference("writeback_execution", "00000000-0000-4000-8000-000000000003") ||
		!ValidSideEffectReference("web_fetch", strings.Repeat("d", 64)) ||
		ValidSideEffectReference("writeback_execution", "/Users/private") || ValidSideEffectReference("shell", "00000000-0000-4000-8000-000000000003") {
		t.Fatal("side effect reference validation drifted")
	}
}

func TestValidateUnresolvedRefusedToolCall(t *testing.T) {
	started := validStartedCall()
	completed := started.StartedAt
	refused := ToolCall{
		ID: started.ID, WorkspaceID: started.WorkspaceID, WorkflowRunID: started.WorkflowRunID, NodeRunID: started.NodeRunID,
		NodeAttemptID: started.NodeAttemptID, CallNo: 1, RequestedToolName: "UnknownTool", RequestHash: strings.Repeat("d", 64),
		RequestBytes: 32, RequestSummary: json.RawMessage(`{"argument_bytes":2}`), Status: CallRefused,
		ErrorCode: "TOOL_NOT_REGISTERED", Version: 1, StartedAt: started.StartedAt, CompletedAt: &completed,
	}
	if err := ValidateToolCall(refused); err != nil {
		t.Fatalf("refused call: %v", err)
	}
	refused.Capability = capability.ReadLocal
	if err := ValidateToolCall(refused); err == nil {
		t.Fatal("unresolved refusal claimed capability")
	}
}

func validStartedCall() ToolCall {
	tool := ToolRef{Name: "SearchKnowledge", Version: 1}
	input := SchemaRef{ID: "tool.search_knowledge.input", Version: 1}
	output := SchemaRef{ID: "tool.search_knowledge.output", Version: 1}
	return ToolCall{
		ID: domainTestID(1), WorkspaceID: domainTestID(2), WorkflowRunID: domainTestID(3), NodeRunID: domainTestID(4), NodeAttemptID: domainTestID(5),
		CallNo: 1, RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: strings.Repeat("f", 64), InputSchema: &input, OutputSchema: &output,
		Capability: capability.ReadLocal, SideEffectLevel: SideEffectNone, InvocationPolicy: InvocationModelRequestable,
		RequestHash: strings.Repeat("a", 64), RequestBytes: 32, RequestSummary: json.RawMessage(`{"query_hash":"safe"}`),
		Status: CallStarted, Version: 1, StartedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	}
}

func validExecutionIdentity() TrustedExecutionIdentity {
	return TrustedExecutionIdentity{
		WorkspaceID: domainTestID(10), DefinitionID: domainTestID(11), DefinitionVersion: 1, DefinitionHash: strings.Repeat("e", 64),
		WorkflowRunID: domainTestID(12), NodeKey: "tool-node", NodeRunID: domainTestID(13), NodeAttemptID: domainTestID(14),
		LeaseOwner: "worker-1", LeaseFence: 1,
	}
}

func domainTestID(ordinal int) foundation.ID {
	return foundation.ID(fmt.Sprintf("%08d-0000-4000-8000-%012d", ordinal, ordinal))
}
