package workflow

import (
	"reflect"
	"strings"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestWorkspaceAnalysisInputCanonicalRoundTripAndNoBody(t *testing.T) {
	input := WorkspaceAnalysisInput{
		SchemaVersion:   WorkspaceAnalysisInputSchemaVersion,
		ConversationID:  foundation.ID("8b000000-0000-4000-8000-000000000001"),
		QuestionID:      foundation.ID("8b000000-0000-4000-8000-000000000002"),
		AnswerID:        foundation.ID("8b000000-0000-4000-8000-000000000003"),
		QuestionOrdinal: 2,
		ContextHash:     strings.Repeat("a", 64),
	}
	encoded, err := EncodeWorkspaceAnalysisInput(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWorkspaceAnalysisInput(encoded)
	if err != nil || decoded != input || strings.Contains(string(encoded), "question_text") || strings.Contains(string(encoded), "scope") {
		t.Fatalf("encoded=%s decoded=%#v err=%v", encoded, decoded, err)
	}
	if string(encoded) != `{"schema_version":1,"conversation_id":"8b000000-0000-4000-8000-000000000001","question_id":"8b000000-0000-4000-8000-000000000002","answer_id":"8b000000-0000-4000-8000-000000000003","question_ordinal":2,"context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` {
		t.Fatalf("workspace analysis input drifted: %s", encoded)
	}

	jsonbOrdered := ` {"answer_id":"8b000000-0000-4000-8000-000000000003","context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","question_id":"8b000000-0000-4000-8000-000000000002","schema_version":1,"conversation_id":"8b000000-0000-4000-8000-000000000001","question_ordinal":2} `
	decoded, err = DecodeWorkspaceAnalysisInput([]byte(jsonbOrdered))
	if err != nil || decoded != input {
		t.Fatalf("jsonb-ordered input decoded=%#v err=%v", decoded, err)
	}
}

func TestWorkspaceAnalysisInputRejectsInvalidOrIncompleteDocuments(t *testing.T) {
	valid := `{"schema_version":1,"conversation_id":"8b000000-0000-4000-8000-000000000001","question_id":"8b000000-0000-4000-8000-000000000002","answer_id":"8b000000-0000-4000-8000-000000000003","question_ordinal":2,"context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	invalid := []string{
		strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(valid, `,"context_hash":"`+strings.Repeat("a", 64)+`"`, ``, 1),
		strings.Replace(valid, `"question_ordinal":2`, `"question_ordinal":null`, 1),
		strings.TrimSuffix(valid, "}") + `,"question_text":"secret"}`,
		valid + `{}`,
	}
	for _, raw := range invalid {
		if _, err := DecodeWorkspaceAnalysisInput([]byte(raw)); err == nil {
			t.Fatalf("DecodeWorkspaceAnalysisInput(%q) succeeded", raw)
		}
	}
}

func TestWorkspaceAnalysisDefinitionFreezesSixNodeChainAndExactToolAllowlist(t *testing.T) {
	definition := RegisteredWorkspaceAnalysisDefinition()
	if definition.Key != WorkspaceAnalysisDefinitionKey || definition.Version != WorkspaceAnalysisDefinitionVersion ||
		definition.InputSchemaVersion != WorkspaceAnalysisInputSchemaVersion || len(definition.Graph.Nodes) != 6 {
		t.Fatalf("definition=%#v", definition)
	}

	byKey := make(map[string]workflowdomain.NodeDefinition, len(definition.Graph.Nodes))
	for _, node := range definition.Graph.Nodes {
		if _, exists := byKey[node.Key]; exists {
			t.Fatalf("duplicate node %q", node.Key)
		}
		byKey[node.Key] = node
		if node.Kind != workspaceAnalysisNodeKind(node.Key) || node.InputSchemaVersion != 1 || node.OutputSchemaVersion != 1 ||
			!reflect.DeepEqual(node.RequiredPermissions, []workflowdomain.Permission{workflowdomain.PermissionReadLocal}) {
			t.Fatalf("node contract=%#v", node)
		}
		if node.RetryPolicy.MaxRetries != 0 || node.RetryPolicy.BaseDelay != 0 || node.RetryPolicy.MaxDelay != 0 {
			t.Fatalf("%s retry policy=%#v, want no workflow retry", node.Key, node.RetryPolicy)
		}
	}

	expectations := map[string]struct {
		dependency string
		tool       toolsdomain.ToolRef
	}{
		WorkspaceAnalysisNodeInspectWorkspace:  {tool: toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}},
		WorkspaceAnalysisNodeRetrieveEvidence:  {dependency: WorkspaceAnalysisNodeInspectWorkspace, tool: toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 2}},
		WorkspaceAnalysisNodeReadEvidence:      {dependency: WorkspaceAnalysisNodeRetrieveEvidence, tool: toolsdomain.ToolRef{Name: "ReadSource", Version: 3}},
		WorkspaceAnalysisNodeSynthesizeAnswer:  {dependency: WorkspaceAnalysisNodeReadEvidence},
		WorkspaceAnalysisNodeValidateCitations: {dependency: WorkspaceAnalysisNodeSynthesizeAnswer, tool: toolsdomain.ToolRef{Name: "ValidateCitation", Version: 3}},
		WorkspaceAnalysisNodeReviewPublish:     {dependency: WorkspaceAnalysisNodeValidateCitations},
	}
	for key, want := range expectations {
		node := byKey[key]
		if want.dependency != "" && !reflect.DeepEqual(node.Dependencies, []string{want.dependency}) {
			t.Fatalf("%s dependencies=%v, want %q", key, node.Dependencies, want.dependency)
		}
		if want.tool == (toolsdomain.ToolRef{}) {
			if len(node.AllowedTools) != 0 {
				t.Fatalf("%s allowed tools=%v, want none", key, node.AllowedTools)
			}
			continue
		}
		if !reflect.DeepEqual(node.AllowedTools, []toolsdomain.ToolRef{want.tool}) {
			t.Fatalf("%s allowed tools=%v, want %v", key, node.AllowedTools, want.tool)
		}
	}

	hash, err := workflowapplication.ComputeCanonicalGraphHash(definition.Graph)
	if err != nil || hash != definition.GraphHash {
		t.Fatalf("graph hash=%q, definition hash=%q, err=%v", hash, definition.GraphHash, err)
	}
	if definition.GraphHash != "6faa6f0eee72c7e99b3e6118d29322c7d0c377b655403c81fd2337e820a18f73" {
		t.Fatalf("workspace analysis graph hash=%q", definition.GraphHash)
	}
}

func TestWorkspaceAnalysisDefinitionDoesNotAliasNodePermissions(t *testing.T) {
	definition := RegisteredWorkspaceAnalysisDefinition()
	definition.Graph.Nodes[0].RequiredPermissions[0] = workflowdomain.PermissionReadExternal
	if definition.Graph.Nodes[1].RequiredPermissions[0] != workflowdomain.PermissionReadLocal {
		t.Fatal("node permissions share mutable backing storage")
	}
}

func TestWorkspaceAnalysisNodeKeysAreThePublicTimelinePhases(t *testing.T) {
	want := []string{
		string(conversationdomain.WorkspaceAnalysisPhaseInspectWorkspace),
		string(conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence),
		string(conversationdomain.WorkspaceAnalysisPhaseReadEvidence),
		string(conversationdomain.WorkspaceAnalysisPhaseSynthesizeAnswer),
		string(conversationdomain.WorkspaceAnalysisPhaseValidateCitations),
		string(conversationdomain.WorkspaceAnalysisPhaseReviewPublish),
	}
	got := []string{
		WorkspaceAnalysisNodeInspectWorkspace, WorkspaceAnalysisNodeRetrieveEvidence, WorkspaceAnalysisNodeReadEvidence,
		WorkspaceAnalysisNodeSynthesizeAnswer, WorkspaceAnalysisNodeValidateCitations, WorkspaceAnalysisNodeReviewPublish,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workflow node keys=%v, timeline phases=%v", got, want)
	}
}

func TestWorkspaceAnalysisOperationNodeKeysMatchWorkflowNodes(t *testing.T) {
	want := map[agentdomain.WorkspaceAnalysisOperationNodeKey]string{
		agentdomain.WorkspaceAnalysisOperationNodeInspectWorkspace:  WorkspaceAnalysisNodeInspectWorkspace,
		agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence:  WorkspaceAnalysisNodeRetrieveEvidence,
		agentdomain.WorkspaceAnalysisOperationNodeReadEvidence:      WorkspaceAnalysisNodeReadEvidence,
		agentdomain.WorkspaceAnalysisOperationNodeSynthesizeAnswer:  WorkspaceAnalysisNodeSynthesizeAnswer,
		agentdomain.WorkspaceAnalysisOperationNodeValidateCitations: WorkspaceAnalysisNodeValidateCitations,
		agentdomain.WorkspaceAnalysisOperationNodeReviewPublish:     WorkspaceAnalysisNodeReviewPublish,
	}
	seen := make(map[agentdomain.WorkspaceAnalysisOperationNodeKey]struct{}, len(want))
	for _, contract := range agentdomain.WorkspaceAnalysisV1OperationContracts() {
		workflowNode, exists := want[contract.NodeKey]
		if !exists || string(contract.NodeKey) != workflowNode {
			t.Fatalf("operation node %q has no matching workflow node", contract.NodeKey)
		}
		seen[contract.NodeKey] = struct{}{}
	}
	if len(seen) != len(want) {
		t.Fatalf("operation nodes=%v, want all workflow nodes=%v", seen, want)
	}
}
