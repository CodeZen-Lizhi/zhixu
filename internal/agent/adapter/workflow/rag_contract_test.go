package workflow

import (
	"reflect"
	"strings"
	"testing"

	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRAGWorkflowContractReusesConversationStableTypes(t *testing.T) {
	input := RAGWorkflowInput{
		SchemaVersion: RAGWorkflowInputSchemaVersion, ConversationID: ragContractID(1), QuestionID: ragContractID(2),
		AnswerID: ragContractID(3), QuestionOrdinal: 4, ContextHash: strings.Repeat("a", 64),
	}
	encoded, err := EncodeRAGWorkflowInput(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"conversation_id":"75000000-0000-4000-8000-000000000001","question_id":"75000000-0000-4000-8000-000000000002","answer_id":"75000000-0000-4000-8000-000000000003","question_ordinal":4,"context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	if string(encoded) != want {
		t.Fatalf("encoded input = %s", encoded)
	}
	decoded, err := DecodeRAGWorkflowInput(encoded)
	if err != nil || decoded != input {
		t.Fatalf("decoded input = %#v, err=%v", decoded, err)
	}

	output := RAGWorkflowOutput{
		SchemaVersion: RAGWorkflowOutputSchemaVersion, AnswerID: input.AnswerID,
		PublicationStatus: conversationworkflow.PublicationStatusCompleted, ResultType: conversationworkflow.ResultTypeRAGAnswer,
		ModelRunID: ragContractID(4), ResultHash: strings.Repeat("b", 64),
	}
	encoded, err = EncodeRAGWorkflowOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	decodedOutput, err := DecodeRAGWorkflowOutput(encoded)
	if err != nil || decodedOutput != output {
		t.Fatalf("decoded output = %#v, err=%v", decodedOutput, err)
	}
}

func TestRAGWorkflowContractRejectsUnknownDuplicateTrailingAndInvalidBindings(t *testing.T) {
	validInput := `{"schema_version":1,"conversation_id":"75000000-0000-4000-8000-000000000001","question_id":"75000000-0000-4000-8000-000000000002","answer_id":"75000000-0000-4000-8000-000000000003","question_ordinal":4,"context_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	invalidInputs := []string{
		strings.TrimSuffix(validInput, "}") + `,"question":"must not persist"}`,
		strings.Replace(validInput, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		validInput + `{}`,
		strings.Replace(validInput, `"answer_id":"75000000-0000-4000-8000-000000000003"`, `"answer_id":"75000000-0000-4000-8000-000000000002"`, 1),
	}
	for _, raw := range invalidInputs {
		if _, err := DecodeRAGWorkflowInput([]byte(raw)); err == nil {
			t.Fatalf("DecodeRAGWorkflowInput(%q) succeeded", raw)
		}
	}

	validOutput := `{"schema_version":1,"answer_id":"75000000-0000-4000-8000-000000000003","publication_status":"completed","result_type":"rag_answer","model_run_id":"75000000-0000-4000-8000-000000000004","result_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
	invalidOutputs := []string{
		strings.TrimSuffix(validOutput, "}") + `,"business_json":{}}`,
		strings.Replace(validOutput, `"result_type":"rag_answer"`, `"result_type":"rag_answer","result_type":"rag_answer"`, 1),
		validOutput + `{}`,
		strings.Replace(validOutput, `"publication_status":"completed"`, `"publication_status":"refused"`, 1),
		strings.Replace(validOutput, `"model_run_id":"75000000-0000-4000-8000-000000000004"`, `"model_run_id":"75000000-0000-4000-8000-000000000003"`, 1),
	}
	for _, raw := range invalidOutputs {
		if _, err := DecodeRAGWorkflowOutput([]byte(raw)); err == nil {
			t.Fatalf("DecodeRAGWorkflowOutput(%q) succeeded", raw)
		}
	}
}

func TestRegisteredRAGDefinitionIsStableReadLocalSingleNodeWithV2Tools(t *testing.T) {
	definition := RegisteredRAGDefinition()
	if definition.Key != RAGWorkflowDefinitionKey || definition.Version != RAGWorkflowDefinitionVersion ||
		definition.InputSchemaVersion != RAGWorkflowInputSchemaVersion || len(definition.Graph.Nodes) != 1 {
		t.Fatalf("definition = %#v", definition)
	}
	node := definition.Graph.Nodes[0]
	wantTools := []toolsdomain.ToolRef{{Name: "ReadSource", Version: 2}, {Name: "ValidateCitation", Version: 2}}
	if node.Key != RAGWorkflowNodeKey || node.Kind != RAGWorkflowNodeKind ||
		node.InputSchemaVersion != RAGWorkflowInputSchemaVersion || node.OutputSchemaVersion != RAGWorkflowOutputSchemaVersion ||
		!reflect.DeepEqual(node.RequiredPermissions, []workflowdomain.Permission{workflowdomain.PermissionReadLocal}) || !reflect.DeepEqual(node.AllowedTools, wantTools) {
		t.Fatalf("node = %#v", node)
	}
	wantHash, err := workflowapplication.ComputeCanonicalGraphHash(definition.Graph)
	if err != nil || definition.GraphHash != wantHash {
		t.Fatalf("graph hash = %q, want %q, err=%v", definition.GraphHash, wantHash, err)
	}
	if !reflect.DeepEqual(definition, conversationworkflow.RegisteredDefinition()) {
		t.Fatalf("agent definition drifted from conversation contract: %#v", definition)
	}
}

func ragContractID(value int) foundation.ID {
	return foundation.ID("75000000-0000-4000-8000-" + leftPadRAGContractID(value))
}

func leftPadRAGContractID(value int) string {
	const zeros = "000000000000"
	raw := ""
	for value > 0 {
		raw = string(rune('0'+value%10)) + raw
		value /= 10
	}
	return zeros[:len(zeros)-len(raw)] + raw
}
