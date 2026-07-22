package workflow

import (
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
)

func TestScanInputRoundTripAndStrictDecode(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	request := healthapp.ScanStartRequest{
		WorkspaceID: workspaceID,
		Scope:       domain.ScanScope{Type: domain.ScanScopeTypeWorkspace, Ref: workspaceID, Version: 1, SchemaVersion: "health-scope/workspace/v1"},
		Coverage:    []domain.DetectorCoverage{{DetectorID: "health.detector.orphan", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusPending}},
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RequestHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MaxItems:    100, IdempotencyKey: "scan-1", WorkflowDefinitionKey: healthapp.HealthScanWorkflowDefinitionKey,
		WorkflowDefinitionVersion: healthapp.HealthScanWorkflowDefinitionVersion, WorkflowInputSchemaVersion: healthapp.HealthScanInputSchemaVersion,
	}
	encoded, err := EncodeScanInput(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeScanInput(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.WorkspaceID != workspaceID || decoded.Coverage[0].DetectorID != request.Coverage[0].DetectorID || decoded.Fingerprint != request.Fingerprint {
		t.Fatalf("decoded=%#v", decoded)
	}
	if _, err := DecodeScanInput(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
	if _, err := DecodeScanInput([]byte(`{"schema_version":1,"unknown":true}`)); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestSmartCollectionScanInputPreservesZeroExactCount(t *testing.T) {
	workspaceID := foundation.ID("10000000-0000-4000-8000-000000000001")
	request := healthapp.ScanStartRequest{
		WorkspaceID: workspaceID,
		Scope: domain.ScanScope{
			Type: domain.ScanScopeTypeSmartCollection, Ref: foundation.ID("10000000-0000-4000-8000-000000000002"), Version: 2,
			SchemaVersion: "health-scope/smart-collection/v1", Hash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64), ExactCount: 0,
		},
		Coverage:    []domain.DetectorCoverage{{DetectorID: "health.detector.orphan", DetectorVersion: "detector/v1", Status: domain.DetectorCoverageStatusPending}},
		Fingerprint: strings.Repeat("c", 64), RequestHash: strings.Repeat("d", 64), MaxItems: 100, IdempotencyKey: "smart-scan",
		WorkflowDefinitionKey: healthapp.HealthScanWorkflowDefinitionKey, WorkflowDefinitionVersion: healthapp.HealthScanWorkflowDefinitionVersion,
		WorkflowInputSchemaVersion: healthapp.HealthScanInputSchemaVersion,
	}
	encoded, err := EncodeScanInput(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeScanInput(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Scope != request.Scope || !strings.Contains(string(encoded), `"ExactCount":0`) {
		t.Fatalf("encoded=%s decoded=%#v", encoded, decoded.Scope)
	}
}

func TestRegisteredDefinitionMatchesHealthExecutorContract(t *testing.T) {
	definition, err := RegisteredDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if definition.Key != healthapp.HealthScanWorkflowDefinitionKey || definition.Version != healthapp.HealthScanWorkflowDefinitionVersion || len(definition.Graph.Nodes) != 1 || definition.Graph.Nodes[0].Kind != healthapp.HealthScanNodeKind || definition.GraphHash == "" {
		t.Fatalf("definition=%#v", definition)
	}
}
