package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolcatalog "github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestNewRegisteredDefinitionDerivesCanonicalPermissionsAndTools(t *testing.T) {
	catalog, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	diff := toolsdomain.ToolRef{Name: "CalculateDiff", Version: 1}
	search := toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 1}
	definition, err := NewRegisteredDefinition(catalog, []toolsdomain.ToolRef{search, diff})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Key != DefinitionKey || definition.Version != DefinitionVersion || definition.InputSchemaVersion != InputSchemaVersion || len(definition.Graph.Nodes) != 1 {
		t.Fatalf("definition=%+v", definition)
	}
	node := definition.Graph.Nodes[0]
	if node.Key != NodeKey || node.Kind != NodeKind || node.InputSchemaVersion != InputSchemaVersion || node.OutputSchemaVersion != OutputSchemaVersion ||
		len(node.AllowedTools) != 2 || node.AllowedTools[0] != diff || node.AllowedTools[1] != search ||
		len(node.RequiredPermissions) != 1 || node.RequiredPermissions[0] != capability.ReadLocal {
		t.Fatalf("node=%+v", node)
	}
}

func TestProductionDefinitionExposesOnlyEmptyOrStableIDInputs(t *testing.T) {
	catalog, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewReplayRegisteredDefinition(catalog)
	if err != nil {
		t.Fatal(err)
	}
	want := []toolsdomain.ToolRef{
		{Name: "ReadGitStatus", Version: 1},
		{Name: "ReadSource", Version: 1},
		{Name: "ValidateCitation", Version: 1},
	}
	if len(definition.Graph.Nodes) != 1 || len(definition.Graph.Nodes[0].AllowedTools) != len(want) {
		t.Fatalf("definition=%+v", definition)
	}
	for index := range want {
		if definition.Graph.Nodes[0].AllowedTools[index] != want[index] {
			t.Fatalf("allowed[%d]=%+v want=%+v", index, definition.Graph.Nodes[0].AllowedTools[index], want[index])
		}
	}
	for _, forbidden := range []string{"SearchKnowledge", "CalculateDiff"} {
		for _, ref := range definition.Graph.Nodes[0].AllowedTools {
			if ref.Name == forbidden {
				t.Fatalf("raw-content Tool entered production persisted model directory: %s", forbidden)
			}
		}
	}
}

func TestEncodePersistedToolInvocationDropsModelReasonAndRejectsContentTools(t *testing.T) {
	catalog, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	request := toolsdomain.ToolRequestV1{
		SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
		ToolName:      "ReadGitStatus",
		Arguments:     json.RawMessage(`{}`),
		Reason:        "model supplied private reasoning must not persist",
	}
	encoded, err := EncodePersistedToolInvocationV1(catalog, request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "reason") || strings.Contains(string(encoded), "private reasoning") {
		t.Fatalf("model reason entered persisted invocation: %s", encoded)
	}
	decoded, err := DecodePersistedToolInvocationV1(encoded)
	if err != nil || decoded.ToolName != request.ToolName || string(decoded.Arguments) != `{}` {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for _, name := range []string{"SearchKnowledge", "CalculateDiff"} {
		forbidden := request
		forbidden.ToolName = name
		forbidden.Arguments = json.RawMessage(`{"query":"raw content","before":"raw","after":"raw"}`)
		if _, err := EncodePersistedToolInvocationV1(catalog, forbidden); err == nil {
			t.Fatalf("content-bearing tool persisted: %s", name)
		}
	}
	for _, raw := range [][]byte{
		[]byte(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{},"reason":"raw"}`),
		[]byte(`{"schema_version":1,"tool_name":"SearchKnowledge","arguments":{"query":"raw"}}`),
		[]byte(`{"schema_version":1,"tool_name":"ReadGitStatus","arguments":{},"arguments":{}}`),
	} {
		if _, err := DecodePersistedToolInvocationV1(raw); err == nil {
			t.Fatalf("unsafe persisted invocation accepted: %s", raw)
		}
	}
}

func TestNewRegisteredDefinitionRejectsUnavailableOrTrustedOnlyTools(t *testing.T) {
	catalog, err := toolcatalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegisteredDefinition(catalog, nil); err == nil {
		t.Fatal("empty tool definition was accepted")
	}
	if _, err := NewRegisteredDefinition(catalog, []toolsdomain.ToolRef{{Name: "ApplyApprovedPatch", Version: 1}}); err == nil {
		t.Fatal("trusted-only write tool was exposed through model-requestable workflow")
	}
	if _, err := NewRegisteredDefinition(catalog, []toolsdomain.ToolRef{{Name: "CalculateDiff", Version: 1}, {Name: "CalculateDiff", Version: 1}}); err == nil {
		t.Fatal("duplicate tool reference was accepted")
	}
	if _, err := NewRegisteredDefinition(catalog, []toolsdomain.ToolRef{{Name: "CalculateDiff", Version: 1}, {Name: "CalculateDiff", Version: 2}}); err == nil {
		t.Fatal("ambiguous tool versions were accepted")
	}
}

func TestDecodeToolResultV1RejectsUnsafeOrDriftingDocuments(t *testing.T) {
	valid := ToolResultV1{
		SchemaVersion: OutputSchemaVersion,
		ToolCallID:    testID(1),
		Tool:          toolsdomain.ToolRef{Name: "CalculateDiff", Version: 1},
		Status:        toolsdomain.CallSucceeded,
		ResultRef:     "diff:" + hash("a") + ":" + hash("b"),
		ResponseHash:  hash("c"), ResponseBytes: 10,
		ResponseSummary: ToolResponseSummaryV1{OutputBytes: 10, OutputHash: hash("c"), SchemaID: "tool.calculate_diff.output", SchemaVersion: 1},
		UntrustedData:   true,
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeToolResultV1(encoded)
	if err != nil || decoded.ToolCallID != valid.ToolCallID || !decoded.UntrustedData {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	invalid := [][]byte{
		[]byte(`{"schema_version":1,"tool_call_id":"` + string(testID(1)) + `","tool":{"name":"CalculateDiff","version":1},"status":"SUCCEEDED","response_hash":"` + hash("c") + `","response_bytes":10,"response_summary":{},"untrusted_data":true,"workspace_id":"` + string(testID(2)) + `"}`),
		append(append([]byte(nil), encoded...), []byte(` {}`)...),
		[]byte(`{"schema_version":1,"schema_version":1}`),
	}
	for _, candidate := range invalid {
		if _, err := DecodeToolResultV1(candidate); err == nil {
			t.Fatalf("invalid result accepted: %s", candidate)
		}
	}
}

func TestNewExecutorRejectsTypedNilService(t *testing.T) {
	var service *workflowExecutionFake
	if _, err := NewExecutor(service); err == nil {
		t.Fatal("typed nil execution service was accepted")
	}
	var classified *foundation.Error
	_, err := NewRegisteredDefinition((*nilContractCatalog)(nil), []toolsdomain.ToolRef{{Name: "CalculateDiff", Version: 1}})
	if !errors.As(err, &classified) || classified.Code != ErrorCodeContractUnavailable {
		t.Fatalf("error=%v", err)
	}
}

type nilContractCatalog struct{}

func (*nilContractCatalog) ResolveToolDefinition(toolsdomain.ToolRef) (toolsdomain.Definition, error) {
	return toolsdomain.Definition{}, nil
}

func testID(last int) foundation.ID {
	return foundation.ID("a0000000-0000-4000-8000-00000000000" + string(rune('0'+last)))
}

func hash(character string) string {
	result := ""
	for len(result) < 64 {
		result += character
	}
	return result[:64]
}
