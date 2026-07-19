package changecontrolworkflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	contractProposalID foundation.ID = "71000000-0000-4000-8000-000000000001"
	contractRevisionID foundation.ID = "72000000-0000-4000-8000-000000000001"
	contractNodeRunID  foundation.ID = "73000000-0000-4000-8000-000000000001"
	contractChangeHash               = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestSafeWritebackWorkflowContractIsStableAndPermissionBound(t *testing.T) {
	definition := RegisteredDefinition()
	if definition.Key != SafeWritebackDefinitionKey || definition.Version != SafeWritebackDefinitionVersion || definition.InputSchemaVersion != SafeWritebackBootstrapInputSchemaVersion {
		t.Fatalf("definition identity=%#v", definition)
	}
	if len(definition.Graph.Nodes) != 1 {
		t.Fatalf("nodes=%#v", definition.Graph.Nodes)
	}
	node := definition.Graph.Nodes[0]
	wantRetry := workflowdomain.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 30 * time.Second}
	if node.Key != SafeWritebackNodeKey || node.Kind != SafeWritebackNodeKind || node.InputSchemaVersion != SafeWritebackBootstrapInputSchemaVersion || node.OutputSchemaVersion != SafeWritebackOutputSchemaVersion || node.RetryPolicy != wantRetry {
		t.Fatalf("node=%#v", node)
	}
	if len(node.RequiredPermissions) != 2 || node.RequiredPermissions[0] != workflowdomain.PermissionGitWrite || node.RequiredPermissions[1] != workflowdomain.PermissionWriteKnowledge {
		t.Fatalf("permissions=%#v", node.RequiredPermissions)
	}
	if len(node.AllowedTools) != 0 {
		t.Fatalf("safe writeback v1 unexpectedly gained tools=%#v", node.AllowedTools)
	}
	encodedGraph, err := json.Marshal(definition.Graph)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encodedGraph)
	if definition.GraphHash != hex.EncodeToString(digest[:]) {
		t.Fatalf("graph hash=%q graph=%s", definition.GraphHash, encodedGraph)
	}
	const stableV1GraphHash = "4ef937969e5856c0efcf8ad9035f3fffe386d2f419e58443822a968d578172a6"
	if definition.GraphHash != stableV1GraphHash || bytes.Contains(encodedGraph, []byte("allowed_tools")) {
		t.Fatalf("safe writeback v1 graph drifted hash=%q graph=%s", definition.GraphHash, encodedGraph)
	}
}

func TestBootstrapInputRoundTripIsCanonicalAndStrict(t *testing.T) {
	encoded, err := EncodeBootstrapInput(contractProposalID, contractRevisionID, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"schema_version":1,"proposal_id":"71000000-0000-4000-8000-000000000001","revision_id":"72000000-0000-4000-8000-000000000001","approved_change_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded=%s", encoded)
	}
	decoded, err := DecodeBootstrapInput(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != SafeWritebackBootstrapInputSchemaVersion || decoded.ProposalID != contractProposalID || decoded.RevisionID != contractRevisionID || decoded.ApprovedChangeHash != contractChangeHash {
		t.Fatalf("decoded=%#v", decoded)
	}
	if key, err := SafeWritebackExecutionKey(contractNodeRunID); err != nil || key != "safe-writeback:73000000-0000-4000-8000-000000000001" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestBootstrapInputRejectsUnknownFieldsAndNonCanonicalIdentity(t *testing.T) {
	invalid := [][]byte{
		[]byte(`{"schema_version":1,"proposal_id":"71000000-0000-4000-8000-000000000001","revision_id":"72000000-0000-4000-8000-000000000001","approved_change_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","credential":"secret"}`),
		[]byte(`{"schema_version":2,"proposal_id":"71000000-0000-4000-8000-000000000001","revision_id":"72000000-0000-4000-8000-000000000001","approved_change_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		[]byte(`{"schema_version":1,"proposal_id":"not-a-uuid","revision_id":"72000000-0000-4000-8000-000000000001","approved_change_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		[]byte(`{"schema_version":1,"proposal_id":"71000000-0000-4000-8000-000000000001","revision_id":"72000000-0000-4000-8000-000000000001","approved_change_hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`),
		[]byte(`{"schema_version":1,"proposal_id":"71000000-0000-4000-8000-000000000001","revision_id":"72000000-0000-4000-8000-000000000001","approved_change_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {}`),
		bytes.Repeat([]byte(" "), MaxSafeWritebackBootstrapInputBytes+1),
	}
	for _, payload := range invalid {
		if _, err := DecodeBootstrapInput(payload); err == nil {
			t.Fatalf("invalid payload accepted: %s", payload)
		}
	}
	if _, err := EncodeBootstrapInput("not-a-uuid", contractRevisionID, contractChangeHash); err == nil {
		t.Fatal("invalid proposal id accepted")
	}
	if _, err := SafeWritebackExecutionKey("not-a-uuid"); err == nil {
		t.Fatal("invalid node run id accepted")
	}
}
