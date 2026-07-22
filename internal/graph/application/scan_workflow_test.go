package application

import (
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
)

func TestSemanticLinkTopicScanWorkflowV1KeepsCanonicalBytes(t *testing.T) {
	request := semanticLinkWorkflowTestRequest(graphdomain.SemanticLinkScanScopeTopic)
	request.WorkflowDefinitionVersion = SemanticLinkScanWorkflowDefinitionVersion
	request.WorkflowInputSchemaVersion = SemanticLinkScanInputSchemaVersion
	request.Scope = graphdomain.SemanticLinkScanScope{
		Type: graphdomain.SemanticLinkScanScopeTopic, Ref: "92000000-0000-4000-8000-000000000002",
		Version: 3, SchemaVersion: SemanticLinkTopicScanScopeSchemaVersion,
	}

	encoded, err := EncodeSemanticLinkScanWorkflowInput(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"workspace_id":"92000000-0000-4000-8000-000000000001","scope_type":"TOPIC","scope_ref":"92000000-0000-4000-8000-000000000002","scope_version":3,"fingerprint":"` + strings.Repeat("a", 64) + `","request_hash":"` + strings.Repeat("b", 64) + `","idempotency_key":"topic-v1","workflow_version":"1"}`
	if string(encoded) != want {
		t.Fatalf("v1 workflow bytes changed\n got: %s\nwant: %s", encoded, want)
	}
	decoded, err := DecodeSemanticLinkScanWorkflowInput(encoded)
	if err != nil || decoded.Scope() != request.Scope || decoded.Generation().WorkflowVersion != SemanticLinkScanWorkflowGenerationVersion {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if _, err := DecodeSemanticLinkSmartCollectionScanWorkflowInput(encoded); err == nil {
		t.Fatal("v2 decoder accepted Topic v1 payload")
	}
}

func TestSemanticLinkSmartCollectionWorkflowV2BindsDurableScope(t *testing.T) {
	request := semanticLinkWorkflowTestRequest(graphdomain.SemanticLinkScanScopeSmartCollection)
	encoded, err := EncodeSemanticLinkScanWorkflowInput(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSemanticLinkSmartCollectionScanWorkflowInput(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Scope() != request.Scope || decoded.Generation().WorkflowVersion != SemanticLinkSmartCollectionScanWorkflowGenerationVersion {
		t.Fatalf("decoded=%#v request=%#v", decoded, request)
	}
	if _, err := DecodeSemanticLinkScanWorkflowInput(encoded); err == nil {
		t.Fatal("v1 decoder accepted SMART_COLLECTION v2 payload")
	}
	if _, err := DecodeSemanticLinkSmartCollectionScanWorkflowInput(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("v2 decoder accepted trailing JSON")
	}

	v1, err := RegisteredSemanticLinkScanDefinition()
	if err != nil {
		t.Fatal(err)
	}
	v2, err := RegisteredSemanticLinkSmartCollectionScanDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || v1.InputSchemaVersion != 1 || v2.Version != 2 || v2.InputSchemaVersion != 2 || v1.GraphHash == v2.GraphHash {
		t.Fatalf("v1=%#v v2=%#v", v1, v2)
	}
}

func TestSemanticLinkWorkflowEncodersRejectCrossSchemaScope(t *testing.T) {
	v1 := semanticLinkWorkflowTestRequest(graphdomain.SemanticLinkScanScopeSmartCollection)
	v1.WorkflowDefinitionVersion = SemanticLinkScanWorkflowDefinitionVersion
	v1.WorkflowInputSchemaVersion = SemanticLinkScanInputSchemaVersion
	if _, err := EncodeSemanticLinkScanWorkflowInput(v1); err == nil {
		t.Fatal("v1 encoder accepted SMART_COLLECTION scope")
	}

	v2 := semanticLinkWorkflowTestRequest(graphdomain.SemanticLinkScanScopeSmartCollection)
	v2.Scope.Type = graphdomain.SemanticLinkScanScopeTopic
	v2.Scope.QueryHash = ""
	v2.Scope.ReadModelRevision = ""
	v2.Scope.SchemaVersion = SemanticLinkTopicScanScopeSchemaVersion
	if _, err := EncodeSemanticLinkSmartCollectionScanWorkflowInput(v2); err == nil {
		t.Fatal("v2 encoder accepted Topic scope")
	}
}

func semanticLinkWorkflowTestRequest(scopeType graphdomain.SemanticLinkScanScopeType) SemanticLinkScanStartRequest {
	return SemanticLinkScanStartRequest{
		WorkspaceID: foundation.ID("92000000-0000-4000-8000-000000000001"),
		Scope: graphdomain.SemanticLinkScanScope{
			Type: scopeType, Ref: "92000000-0000-4000-8000-000000000003", Version: 7,
			SchemaVersion: SemanticLinkSmartCollectionScanScopeSchemaVersion,
			QueryHash:     strings.Repeat("c", 64), ReadModelRevision: strings.Repeat("d", 64),
		},
		Fingerprint: strings.Repeat("a", 64), RequestHash: strings.Repeat("b", 64), IdempotencyKey: "topic-v1",
		WorkflowDefinitionKey:      SemanticLinkScanWorkflowDefinitionKey,
		WorkflowDefinitionVersion:  SemanticLinkSmartCollectionScanWorkflowDefinitionVersion,
		WorkflowInputSchemaVersion: SemanticLinkSmartCollectionScanInputSchemaVersion,
	}
}
