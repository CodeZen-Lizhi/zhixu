package catalog

import (
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestWorkspaceAnalysisDynamicCatalogSnapshot(t *testing.T) {
	registry, err := NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	v1, err := WorkspaceAnalysisToolCatalogSnapshotFromRegistry(registry)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := WorkspaceAnalysisToolCatalogSnapshotV2FromRegistry(registry)
	if err != nil {
		t.Fatal(err)
	}
	if v2.SchemaVersion != 2 || len(v2.Tools) != 4 || v1.Hash == v2.Hash {
		t.Fatal("dynamic catalog did not retain a separate version")
	}
	for i, entry := range v2.Tools {
		if entry.Name != v1.Tools[i].Name || entry.Version != v1.Tools[i].Version+1 || entry.DefinitionHash == v1.Tools[i].DefinitionHash {
			t.Fatal("dynamic tuple is not additive")
		}
		contract, err := registry.ResolveContract(domain.ToolRef{Name: entry.Name, Version: entry.Version})
		if err != nil {
			t.Fatal(err)
		}
		if len(contract.Definition.AllowedWorkflows) != 1 || contract.Definition.AllowedWorkflows[0] != (domain.WorkflowBinding{Key: "workspace-analysis", Version: 2}) {
			t.Fatal("dynamic tool workflow is not exact")
		}
		t.Logf("%s@%d %s", entry.Name, entry.Version, entry.DefinitionHash)
	}
	t.Logf("snapshot %s", v2.Hash)
}

func TestWorkspaceAnalysisDynamicReadAndCitationStrictRequests(t *testing.T) {
	registry, err := NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	read, err := registry.ResolveContract(domain.ToolRef{Name: "ReadSource", Version: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"evidence_ref":"E1"}`, `{"evidence_ref":"E10"}`, `{"evidence_ref":"E32"}`} {
		if _, err := read.DecodeInput([]byte(raw)); err != nil {
			t.Fatalf("valid global reference: %v", err)
		}
	}
	for _, raw := range []string{`{"evidence_ref":"E33"}`, `{"evidence_ref":"E01"}`, `{"evidence_ref":"E1","workspace_id":"` + testID1 + `"}`, `{"evidence_ref":"E1","evidence_ref":"E2"}`, `{"evidence_ref":"E1","path":"/tmp/private"}`} {
		if _, err := read.DecodeInput([]byte(raw)); err == nil {
			t.Fatal("unsafe read accepted")
		}
	}
	citation, err := registry.ResolveContract(domain.ToolRef{Name: "ValidateCitation", Version: 4})
	if err != nil {
		t.Fatal(err)
	}
	good := `{"candidate_id":null,"candidate_hash":null,"evidence_refs":["E1","E12","E32"]}`
	if _, err := citation.DecodeInput([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"evidence_refs":["E1"]}`, `{"candidate_id":"` + testID1 + `","candidate_hash":null,"evidence_refs":["E1"]}`, strings.Replace(good, `"E12"`, `"E1"`, 1), strings.Replace(good, `"E32"`, `"E33"`, 1)} {
		if _, err := citation.DecodeInput([]byte(raw)); err == nil {
			t.Fatal("incomplete or drifted citation accepted")
		}
	}
	legacy, err := registry.ResolveContract(domain.ToolRef{Name: "ReadSource", Version: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.DecodeInput([]byte(`{"evidence_ref":"E12"}`)); err == nil {
		t.Fatal("legacy reference bound was expanded")
	}
}
