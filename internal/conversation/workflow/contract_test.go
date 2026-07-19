package workflow_test

import (
	"strings"
	"testing"

	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRAGWorkflowInputRoundTripsCanonicalIdentityOnly(t *testing.T) {
	input := conversationworkflow.Input{
		SchemaVersion:   conversationworkflow.InputSchemaVersion,
		ConversationID:  workflowTestID(1),
		QuestionID:      workflowTestID(2),
		AnswerID:        workflowTestID(3),
		QuestionOrdinal: 4,
		ContextHash:     strings.Repeat("a", 64),
	}
	encoded, err := conversationworkflow.EncodeInput(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"conversation_id":"74000000-0000-4000-8000-000000000001","question_id":"74000000-0000-4000-8000-000000000002","answer_id":"74000000-0000-4000-8000-000000000003","question_ordinal":4,"context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	if string(encoded) != want {
		t.Fatalf("encoded input = %s", encoded)
	}
	decoded, err := conversationworkflow.DecodeInput(encoded)
	if err != nil || decoded != input {
		t.Fatalf("decoded input = %#v, %v", decoded, err)
	}
	reordered := `{"context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","question_ordinal":4,"answer_id":"74000000-0000-4000-8000-000000000003","question_id":"74000000-0000-4000-8000-000000000002","conversation_id":"74000000-0000-4000-8000-000000000001","schema_version":1}`
	decoded, err = conversationworkflow.DecodeInput([]byte(reordered))
	if err != nil || decoded != input {
		t.Fatalf("decoded reordered JSONB input = %#v, %v", decoded, err)
	}
}

func TestRAGWorkflowInputRejectsUntrustedOrNonCanonicalDocuments(t *testing.T) {
	valid := `{"schema_version":1,"conversation_id":"74000000-0000-4000-8000-000000000001","question_id":"74000000-0000-4000-8000-000000000002","answer_id":"74000000-0000-4000-8000-000000000003","question_ordinal":4,"context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	tests := []string{
		`null`,
		`{"schema_version":1}`,
		strings.Replace(valid, `"question_ordinal":4`, `"question_ordinal":0`, 1),
		strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		strings.Replace(valid, `"answer_id":"74000000-0000-4000-8000-000000000003"`, `"answer_id":"74000000-0000-4000-8000-000000000002"`, 1),
		strings.TrimSuffix(valid, "}") + `,"question_text":"must not persist"}`,
		strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		valid + `{}`,
	}
	for _, raw := range tests {
		if _, err := conversationworkflow.DecodeInput([]byte(raw)); err == nil {
			t.Fatalf("DecodeInput(%q) succeeded", raw)
		}
	}
}

func TestRAGWorkflowDefinitionOwnsSingleReadOnlyNode(t *testing.T) {
	definition := conversationworkflow.RegisteredDefinition()
	if definition.Key != conversationworkflow.DefinitionKey || definition.Version != conversationworkflow.DefinitionVersion ||
		definition.InputSchemaVersion != conversationworkflow.InputSchemaVersion || len(definition.Graph.Nodes) != 1 {
		t.Fatalf("definition = %#v", definition)
	}
	node := definition.Graph.Nodes[0]
	if node.Key != conversationworkflow.NodeKey || node.Kind != conversationworkflow.NodeKind ||
		node.InputSchemaVersion != conversationworkflow.InputSchemaVersion || node.OutputSchemaVersion != conversationworkflow.OutputSchemaVersion ||
		node.RetryPolicy.MaxRetries != 2 || len(node.RequiredPermissions) != 1 || node.RequiredPermissions[0] != workflowdomain.PermissionReadLocal ||
		len(node.AllowedTools) != 0 {
		t.Fatalf("node = %#v", node)
	}
	wantHash, err := workflowapplication.ComputeCanonicalGraphHash(definition.Graph)
	if err != nil || definition.GraphHash != wantHash {
		t.Fatalf("graph hash = %q, want %q, error=%v", definition.GraphHash, wantHash, err)
	}
}

func workflowTestID(value int) foundation.ID {
	return foundation.ID("74000000-0000-4000-8000-" + workflowLeftPad12(value))
}

func workflowLeftPad12(value int) string {
	const zeros = "000000000000"
	raw := ""
	for value > 0 {
		raw = string(rune('0'+value%10)) + raw
		value /= 10
	}
	return zeros[:len(zeros)-len(raw)] + raw
}
