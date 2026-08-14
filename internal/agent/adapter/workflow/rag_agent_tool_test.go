package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const testRAGToolDefinitionID foundation.ID = "83000000-0000-4000-8000-000000000001"

func TestRAGAgentToolBridgeRejectsUseBeforeShortReferenceScopeIsBound(t *testing.T) {
	service := &ragAgentToolExecutionFake{}
	bridge := newRAGAgentToolBridgeForTest(t, service)
	if specs := bridge.Specs(); specs != nil {
		t.Fatalf("unbound bridge exposed full-identity schemas: %+v", specs)
	}
	_, err := bridge.Invoke(context.Background(), agentapplication.AgentToolInvocation{
		Ref:       toolsdomain.ToolRef{Name: ragReadSourceTool, Version: 2},
		Arguments: []byte(`{"source_version_id":"83000000-0000-4000-8000-000000000010","source_span_id":"83000000-0000-4000-8000-000000000011"}`),
		Reason:    "must not execute", CallNo: 1,
	})
	if codeOf(err) != ragAgentToolScopeInvalid || len(service.commands) != 0 {
		t.Fatalf("err=%v code=%q commands=%d", err, codeOf(err), len(service.commands))
	}
}

func TestRAGAgentToolMetadataScopeExposesOnlyShortReferenceSchemas(t *testing.T) {
	bridge, _ := newScopedRAGAgentToolBridgeForTest(t, &ragAgentToolExecutionFake{}, ragAgentToolBindings(t, true))
	specs := bridge.Specs()
	if len(specs) != 2 {
		t.Fatalf("specs=%+v", specs)
	}
	for _, spec := range specs {
		for _, forbidden := range []string{
			string(testWorkspaceID), string(testIndexID), string(testChunkID), string(testSourceID), string(testSpanID), "citation-1",
			"source_version_id", "source_span_id", "chunk_id", "index_version_id", "citation_id",
		} {
			if bytes.Contains(spec.InputSchema, []byte(forbidden)) {
				t.Fatalf("tool %s schema leaked %q: %s", spec.Name, forbidden, spec.InputSchema)
			}
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Fatalf("tool %s schema: %v", spec.Name, err)
		}
		properties := schema["properties"].(map[string]any)
		switch spec.Name {
		case ragReadSourceTool:
			if !slices.Equal(jsonStrings(properties["evidence_ref"].(map[string]any)["enum"]), []string{"E1", "E2"}) {
				t.Fatalf("read schema=%s", spec.InputSchema)
			}
		case ragValidateCitationTool:
			if !slices.Equal(jsonStrings(properties["evidence_refs"].(map[string]any)["items"].(map[string]any)["enum"]), []string{"E1", "E2"}) ||
				!slices.Equal(jsonStrings(properties["conflict_refs"].(map[string]any)["items"].(map[string]any)["enum"]), []string{"C1", "C2"}) {
				t.Fatalf("citation schema=%s", spec.InputSchema)
			}
			for _, field := range []string{"evidence_refs", "conflict_refs"} {
				if _, exists := properties[field].(map[string]any)["uniqueItems"]; exists {
					t.Fatalf("citation schema uses unsupported uniqueItems for %s: %s", field, spec.InputSchema)
				}
			}
		default:
			t.Fatalf("unexpected tool %q", spec.Name)
		}
	}

	specs[0].InputSchema[0] = '!'
	if bridge.Specs()[0].InputSchema[0] == '!' {
		t.Fatal("Specs returned mutable schema storage")
	}
}

func TestRAGAgentToolMetadataScopeExpandsReadSourceAndRedactsOutput(t *testing.T) {
	service := &ragAgentToolExecutionFake{}
	bridge, bindings := newScopedRAGAgentToolBridgeForTest(t, service, ragAgentToolBindings(t, true))
	service.execute = func(command toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error) {
		var arguments ragReadSourceExecutionArguments
		if err := json.Unmarshal(command.Request.Arguments, &arguments); err != nil {
			t.Fatal(err)
		}
		citation := bindings.Evidence[0].Citation
		if arguments.SourceVersionID != citation.SourceVersionID || arguments.SourceSpanID != citation.SourceSpanID ||
			command.Identity != bridge.identity || command.Invocation != toolsdomain.InvocationSourceModelRequest || command.CallNo != 1 {
			t.Fatalf("command=%+v arguments=%+v", command, arguments)
		}
		output := json.RawMessage(`{"source_version_id":"` + string(citation.SourceVersionID) + `","source_span_id":"` + string(citation.SourceSpanID) + `","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","excerpt":"Approved excerpt."}`)
		return successfulRAGAgentToolResult(toolsdomain.ToolRef{Name: ragReadSourceTool, Version: 2}, output), nil
	}

	result, err := bridge.Invoke(context.Background(), agentapplication.AgentToolInvocation{
		Ref: toolsdomain.ToolRef{Name: ragReadSourceTool, Version: 2}, Arguments: []byte(`{"evidence_ref":"E1"}`), Reason: "inspect evidence", CallNo: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Output) != `{"evidence_ref":"E1","excerpt":"Approved excerpt."}` {
		t.Fatalf("output=%s", result.Output)
	}
	assertNoRAGToolServerIdentity(t, result.Output, bindings)
	if len(service.commands) != 1 || bytes.Contains(service.commands[0].Request.Arguments, []byte("E1")) {
		t.Fatalf("persisted command=%+v", service.commands)
	}
}

func TestRAGAgentToolMetadataScopeExpandsCitationRefsAndRedactsOutput(t *testing.T) {
	service := &ragAgentToolExecutionFake{}
	bridge, bindings := newScopedRAGAgentToolBridgeForTest(t, service, ragAgentToolBindings(t, true))
	service.execute = func(command toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error) {
		var arguments ragValidateCitationExecutionArguments
		if err := json.Unmarshal(command.Request.Arguments, &arguments); err != nil {
			t.Fatal(err)
		}
		if len(arguments.Citations) != 2 || arguments.Citations[0] != citationTupleFromBinding(bindings.Evidence[0]) ||
			arguments.Citations[1] != citationTupleFromBinding(bindings.Evidence[1]) {
			t.Fatalf("expanded arguments=%+v", arguments)
		}
		output := ragValidateCitationExecutionOutput{Results: []ragValidateCitationExecutionResult{
			{ragCitationTuple: arguments.Citations[0], Valid: true, ReasonCode: "OK"},
			{ragCitationTuple: arguments.Citations[1], Valid: false, ReasonCode: "EVIDENCE_INELIGIBLE"},
		}}
		raw, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		return successfulRAGAgentToolResult(toolsdomain.ToolRef{Name: ragValidateCitationTool, Version: 2}, raw), nil
	}

	result, err := bridge.Invoke(context.Background(), agentapplication.AgentToolInvocation{
		Ref:       toolsdomain.ToolRef{Name: ragValidateCitationTool, Version: 2},
		Arguments: []byte(`{"evidence_refs":[],"conflict_refs":["C1","C2"]}`), Reason: "validate conflict evidence", CallNo: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"results":[{"evidence_ref":"E1","valid":true,"reason_code":"OK"},{"evidence_ref":"E2","valid":false,"reason_code":"EVIDENCE_INELIGIBLE"}]}`
	if string(result.Output) != want {
		t.Fatalf("output=%s want=%s", result.Output, want)
	}
	assertNoRAGToolServerIdentity(t, result.Output, bindings)
	if len(service.commands) != 1 || service.commands[0].CallNo != 2 || bytes.Contains(service.commands[0].Request.Arguments, []byte("C1")) {
		t.Fatalf("persisted command=%+v", service.commands)
	}
}

func TestRAGAgentToolMetadataScopeRejectsExpandedCitationOverlap(t *testing.T) {
	service := &ragAgentToolExecutionFake{}
	bridge, _ := newScopedRAGAgentToolBridgeForTest(t, service, ragAgentToolBindings(t, true))

	_, err := bridge.Invoke(context.Background(), agentapplication.AgentToolInvocation{
		Ref:       toolsdomain.ToolRef{Name: ragValidateCitationTool, Version: 2},
		Arguments: []byte(`{"evidence_refs":["E1"],"conflict_refs":["C1"]}`), Reason: "validate shared evidence", CallNo: 1,
	})
	if codeOf(err) != ragAgentToolArgumentsInvalid || len(service.commands) != 0 {
		t.Fatalf("err=%v code=%q commands=%d", err, codeOf(err), len(service.commands))
	}
}

func TestRAGAgentToolMetadataScopeRejectsUnknownDuplicateAndUnauthorizedRefs(t *testing.T) {
	tests := []struct {
		name      string
		ref       toolsdomain.ToolRef
		arguments string
		wantCode  string
	}{
		{name: "unknown evidence", ref: toolsdomain.ToolRef{Name: ragReadSourceTool, Version: 2}, arguments: `{"evidence_ref":"E9"}`, wantCode: ragAgentToolReferenceDenied},
		{name: "duplicate field", ref: toolsdomain.ToolRef{Name: ragReadSourceTool, Version: 2}, arguments: `{"evidence_ref":"E1","evidence_ref":"E2"}`, wantCode: ragAgentToolArgumentsInvalid},
		{name: "duplicate evidence", ref: toolsdomain.ToolRef{Name: ragValidateCitationTool, Version: 2}, arguments: `{"evidence_refs":["E1","E1"],"conflict_refs":[]}`, wantCode: ragAgentToolArgumentsInvalid},
		{name: "duplicate conflict", ref: toolsdomain.ToolRef{Name: ragValidateCitationTool, Version: 2}, arguments: `{"evidence_refs":[],"conflict_refs":["C1","C1"]}`, wantCode: ragAgentToolArgumentsInvalid},
		{name: "unknown conflict", ref: toolsdomain.ToolRef{Name: ragValidateCitationTool, Version: 2}, arguments: `{"evidence_refs":[],"conflict_refs":["C9"]}`, wantCode: ragAgentToolReferenceDenied},
		{name: "wrong exact version", ref: toolsdomain.ToolRef{Name: ragReadSourceTool, Version: 1}, arguments: `{"evidence_ref":"E1"}`, wantCode: ragAgentToolNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &ragAgentToolExecutionFake{}
			bridge, _ := newScopedRAGAgentToolBridgeForTest(t, service, ragAgentToolBindings(t, true))
			_, err := bridge.Invoke(context.Background(), agentapplication.AgentToolInvocation{
				Ref: test.ref, Arguments: []byte(test.arguments), Reason: "test scope", CallNo: 1,
			})
			if codeOf(err) != test.wantCode || len(service.commands) != 0 {
				t.Fatalf("err=%v code=%q commands=%d", err, codeOf(err), len(service.commands))
			}
		})
	}
}

func TestRAGAgentToolMetadataScopeRejectsCrossWorkspaceBinding(t *testing.T) {
	service := &ragAgentToolExecutionFake{}
	base := newRAGAgentToolBridgeForTest(t, service)
	bindings := ragAgentToolBindings(t, false)
	bindings.Evidence[0].Citation.WorkspaceID = foundation.ID("83000000-0000-4000-8000-000000000099")
	bindings.Evidence[1].Citation.WorkspaceID = bindings.Evidence[0].Citation.WorkspaceID
	if _, err := base.BindMetadataScope(bindings); codeOf(err) != ragAgentToolReferenceDenied {
		t.Fatalf("err=%v code=%q", err, codeOf(err))
	}
	if len(service.commands) != 0 {
		t.Fatalf("commands=%+v", service.commands)
	}
}

func TestRAGAgentToolMetadataScopePreservesExecutionErrorAndRejectsDrift(t *testing.T) {
	dependencyErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "TOOL_STORAGE_UNAVAILABLE", true, errors.New("storage unavailable"))
	service := &ragAgentToolExecutionFake{err: dependencyErr}
	bridge, bindings := newScopedRAGAgentToolBridgeForTest(t, service, ragAgentToolBindings(t, false))
	invocation := agentapplication.AgentToolInvocation{
		Ref: toolsdomain.ToolRef{Name: ragReadSourceTool, Version: 2}, Arguments: []byte(`{"evidence_ref":"E1"}`), Reason: "inspect evidence", CallNo: 1,
	}
	if _, err := bridge.Invoke(context.Background(), invocation); !errors.Is(err, dependencyErr) || codeOf(err) != "TOOL_STORAGE_UNAVAILABLE" {
		t.Fatalf("err=%v", err)
	}

	service.err = nil
	service.execute = func(toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error) {
		citation := bindings.Evidence[0].Citation
		output := json.RawMessage(`{"source_version_id":"` + string(citation.SourceVersionID) + `","source_span_id":"` + string(testSpanID2) + `","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","excerpt":"Wrong span."}`)
		return successfulRAGAgentToolResult(invocation.Ref, output), nil
	}
	if _, err := bridge.Invoke(context.Background(), invocation); codeOf(err) != ragAgentToolResultInvalid {
		t.Fatalf("err=%v code=%q", err, codeOf(err))
	}
}

func newScopedRAGAgentToolBridgeForTest(
	t *testing.T,
	service *ragAgentToolExecutionFake,
	bindings agentdomain.RAGAnswerMetadataBindings,
) (*RAGAgentToolBridge, agentdomain.RAGAnswerMetadataBindings) {
	t.Helper()
	base := newRAGAgentToolBridgeForTest(t, service)
	bridge, err := base.BindMetadataScope(bindings)
	if err != nil {
		t.Fatal(err)
	}
	return bridge, bindings
}

func newRAGAgentToolBridgeForTest(t *testing.T, service *ragAgentToolExecutionFake) *RAGAgentToolBridge {
	t.Helper()
	registry, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := conversationworkflow.RegisteredDefinitionV2()
	bridge, err := NewRAGAgentToolBridge(workflowapplication.ExecutionContext{
		WorkspaceID: testWorkspaceID, DefinitionID: testRAGToolDefinitionID,
		DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
		RunID: testWorkflowRunID, NodeKey: conversationworkflow.NodeKey, NodeRunID: testNodeRunID,
		NodeAttemptID: testAttemptID, NodeKind: RAGWorkflowNodeKind, NodeVersion: 1,
		InputSchemaVersion: RAGWorkflowInputSchemaVersion, AttemptNo: 1, DispatchNo: 1, RetryNo: 0, LeaseOwner: "tool-test-worker",
	}, registry, service)
	if err != nil {
		t.Fatal(err)
	}
	return bridge
}

func ragAgentToolBindings(t *testing.T, conflicts bool) agentdomain.RAGAnswerMetadataBindings {
	t.Helper()
	first := agentdomain.RAGAnswerMetadataEvidenceBinding{Ref: "E1", Citation: agentdomain.Citation{
		ID: "citation-1", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
		ChunkID: testChunkID, SourceVersionID: testSourceID, SourceSpanID: testSpanID,
	}}
	second := agentdomain.RAGAnswerMetadataEvidenceBinding{Ref: "E2", Citation: agentdomain.Citation{
		ID: "citation-2", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
		ChunkID: testChunkID2, SourceVersionID: testSourceID2, SourceSpanID: testSpanID2,
	}}
	bindings := agentdomain.RAGAnswerMetadataBindings{
		Evidence:  []agentdomain.RAGAnswerMetadataEvidenceBinding{first, second},
		Conflicts: []agentdomain.RAGAnswerMetadataConflictBinding{},
		RelatedTopics: []agentdomain.RAGAnswerMetadataTopicBinding{{Ref: "T1", Topic: agentdomain.RelatedTopic{
			TopicID: testCandidateID, Name: "Deployment", CitationIDs: []string{first.Citation.ID, second.Citation.ID},
		}}},
	}
	if conflicts {
		now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
		bindings.Conflicts = []agentdomain.RAGAnswerMetadataConflictBinding{
			{Ref: "C1", ClaimID: testCandidateID, Applicability: json.RawMessage(`{"environment":"production"}`), CitationIDs: []string{first.Citation.ID}, UpdatedAt: now},
			{Ref: "C2", ClaimID: testExistingID, Applicability: json.RawMessage(`{"environment":"development"}`), CitationIDs: []string{second.Citation.ID}, UpdatedAt: now},
		}
	}
	if err := bindings.Validate(); err != nil {
		t.Fatal(err)
	}
	return bindings
}

func citationTupleFromBinding(binding agentdomain.RAGAnswerMetadataEvidenceBinding) ragCitationTuple {
	citation := binding.Citation
	return ragCitationTuple{
		CitationID: citation.ID, IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
		SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
	}
}

func successfulRAGAgentToolResult(ref toolsdomain.ToolRef, output json.RawMessage) toolsapplication.ToolExecutionResult {
	tool := ref
	return toolsapplication.ToolExecutionResult{
		Call:   toolsdomain.ToolCall{Status: toolsdomain.CallSucceeded, Tool: &tool, ResponseBytes: int64(len(output))},
		Output: append(json.RawMessage(nil), output...), UntrustedData: true,
	}
}

func assertNoRAGToolServerIdentity(t *testing.T, output []byte, bindings agentdomain.RAGAnswerMetadataBindings) {
	t.Helper()
	for _, forbidden := range []string{string(testWorkspaceID), string(testIndexID)} {
		if bytes.Contains(output, []byte(forbidden)) {
			t.Fatalf("model output leaked identity %q: %s", forbidden, output)
		}
	}
	for _, binding := range bindings.Evidence {
		for _, forbidden := range []string{
			binding.Citation.ID, string(binding.Citation.ChunkID), string(binding.Citation.SourceVersionID), string(binding.Citation.SourceSpanID),
		} {
			if bytes.Contains(output, []byte(forbidden)) {
				t.Fatalf("model output leaked identity %q: %s", forbidden, output)
			}
		}
	}
}

func jsonStrings(value any) []string {
	items := value.([]any)
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.(string)
	}
	return result
}

type ragAgentToolExecutionFake struct {
	commands []toolsapplication.ExecuteToolCommand
	execute  func(toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error)
	err      error
}

func (fake *ragAgentToolExecutionFake) Execute(_ context.Context, command toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error) {
	command.Request.Arguments = append(json.RawMessage(nil), command.Request.Arguments...)
	fake.commands = append(fake.commands, command)
	if fake.err != nil {
		return toolsapplication.ToolExecutionResult{}, fake.err
	}
	if fake.execute == nil {
		return toolsapplication.ToolExecutionResult{}, errors.New("unexpected tool execution")
	}
	return fake.execute(command)
}
