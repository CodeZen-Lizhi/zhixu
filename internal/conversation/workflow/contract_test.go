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

func TestRAGWorkflowOutputReceiptRoundTripsCanonicalIdentityOnly(t *testing.T) {
	tests := []struct {
		name   string
		status conversationworkflow.PublicationStatus
		result conversationworkflow.ResultType
	}{
		{name: "completed", status: conversationworkflow.PublicationStatusCompleted, result: conversationworkflow.ResultTypeRAGAnswer},
		{name: "refused", status: conversationworkflow.PublicationStatusRefused, result: conversationworkflow.ResultTypeRefusal},
		{name: "clarification", status: conversationworkflow.PublicationStatusClarificationRequired, result: conversationworkflow.ResultTypeClarification},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := conversationworkflow.OutputReceipt{
				SchemaVersion:     conversationworkflow.OutputSchemaVersion,
				AnswerID:          workflowTestID(1),
				PublicationStatus: test.status,
				ResultType:        test.result,
				ModelRunID:        workflowTestID(2),
				ResultHash:        strings.Repeat("b", 64),
			}
			encoded, err := conversationworkflow.EncodeOutputReceipt(output)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := conversationworkflow.DecodeOutputReceipt(encoded)
			if err != nil || decoded != output {
				t.Fatalf("decoded output = %#v, %v", decoded, err)
			}
		})
	}

	output := conversationworkflow.OutputReceipt{
		SchemaVersion:     conversationworkflow.OutputSchemaVersion,
		AnswerID:          workflowTestID(1),
		PublicationStatus: conversationworkflow.PublicationStatusCompleted,
		ResultType:        conversationworkflow.ResultTypeRAGAnswer,
		ModelRunID:        workflowTestID(2),
		ResultHash:        strings.Repeat("b", 64),
	}
	encoded, err := conversationworkflow.EncodeOutputReceipt(output)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"answer_id":"74000000-0000-4000-8000-000000000001","publication_status":"completed","result_type":"rag_answer","model_run_id":"74000000-0000-4000-8000-000000000002","result_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
	if string(encoded) != want {
		t.Fatalf("encoded output = %s", encoded)
	}
	decoded, err := conversationworkflow.DecodeOutputReceipt(encoded)
	if err != nil || decoded != output {
		t.Fatalf("decoded output = %#v, %v", decoded, err)
	}
	reordered := `{"result_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","model_run_id":"74000000-0000-4000-8000-000000000002","result_type":"rag_answer","publication_status":"completed","answer_id":"74000000-0000-4000-8000-000000000001","schema_version":1}`
	decoded, err = conversationworkflow.DecodeOutputReceipt([]byte(reordered))
	if err != nil || decoded != output {
		t.Fatalf("decoded reordered output = %#v, %v", decoded, err)
	}
}

func TestRAGWorkflowOutputReceiptRejectsUntrustedOrIncompatibleDocuments(t *testing.T) {
	valid := `{"schema_version":1,"answer_id":"74000000-0000-4000-8000-000000000001","publication_status":"completed","result_type":"rag_answer","model_run_id":"74000000-0000-4000-8000-000000000002","result_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
	tests := []string{
		`null`,
		`{"schema_version":1}`,
		strings.Replace(valid, `"publication_status":"completed"`, `"publication_status":"completed","publication_status":"completed"`, 1),
		strings.Replace(valid, `"result_type":"rag_answer"`, `"result_type":"rag_answer","result":"must not persist"`, 1),
		strings.Replace(valid, `"result_type":"rag_answer"`, `"result_type":"rag_answer","retrieval_summary":{"rewrites":[]}`, 1),
		strings.Replace(valid, `"result_type":"rag_answer"`, `"result_type":"rag_answer","provider":{"name":"x"}`, 1),
		strings.Replace(valid, `"publication_status":"completed"`, `"publication_status":"refused"`, 1),
		strings.Replace(valid, `"result_type":"rag_answer"`, `"result_type":"clarification"`, 1),
		strings.Replace(valid, `"model_run_id":"74000000-0000-4000-8000-000000000002"`, `"model_run_id":"74000000-0000-4000-8000-000000000001"`, 1),
		strings.Replace(valid, strings.Repeat("b", 64), strings.Repeat("B", 64), 1),
		strings.Replace(valid, strings.Repeat("b", 64), strings.Repeat("b", 63), 1),
		strings.Replace(valid, `"answer_id":"74000000-0000-4000-8000-000000000001"`, `"answer_id":"74000000-0000-4000-8000-00000000ZZZZ"`, 1),
		strings.Replace(valid, `"model_run_id":"74000000-0000-4000-8000-000000000002"`, `"model_run_id":null`, 1),
		valid + `{}`,
	}
	for _, raw := range tests {
		if _, err := conversationworkflow.DecodeOutputReceipt([]byte(raw)); err == nil {
			t.Fatalf("DecodeOutputReceipt(%q) succeeded", raw)
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
