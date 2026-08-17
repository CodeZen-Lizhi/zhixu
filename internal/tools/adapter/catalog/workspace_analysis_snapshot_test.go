package catalog

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestWorkspaceAnalysisToolCatalogSnapshotIsStable(t *testing.T) {
	snapshot, err := WorkspaceAnalysisToolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", snapshot.SchemaVersion)
	}
	const wantHash = "e4383a8e6775ad85ec971a75e8eab098cd567aa3dd073d8d4f6f8f9e2a1d1d56"
	if snapshot.Hash != wantHash {
		t.Fatalf("snapshot hash = %q, want %q", snapshot.Hash, wantHash)
	}
	wantTools := []WorkspaceAnalysisToolCatalogEntry{
		{Name: "ReadGitStatus", Version: 2, DefinitionHash: "b5dd1fcca72d5bb41fd9ad3f74006d3706e4184ad39e2a3409b565d1ff896bbd"},
		{Name: "SearchKnowledge", Version: 2, DefinitionHash: "db7180086adb06a18a4be8d1eb80a208fa6385c70f1a71d6d67c4f263fbd1807"},
		{Name: "ReadSource", Version: 3, DefinitionHash: "d41dabac535261b885e453b64e11fe5bb779838e31a255aea4798e27ead54d32"},
		{Name: "ValidateCitation", Version: 3, DefinitionHash: "bf5e643c47d57478b46d258eca250dc30e810ee7a0693569f44edaab19037d54"},
	}
	if len(snapshot.Tools) != len(wantTools) {
		t.Fatalf("tool count = %d, want %d", len(snapshot.Tools), len(wantTools))
	}
	for index := range wantTools {
		if snapshot.Tools[index] != wantTools[index] {
			t.Fatalf("tool[%d] = %+v, want %+v", index, snapshot.Tools[index], wantTools[index])
		}
	}
	if snapshot.ReadGitStatusTimeout != 10*time.Second || snapshot.SearchKnowledgeTimeout != 15*time.Second ||
		snapshot.ReadSourceTimeout != 15*time.Second || snapshot.ValidateCitationTimeout != 15*time.Second {
		t.Fatalf("timeouts = git:%s search:%s read:%s validate:%s", snapshot.ReadGitStatusTimeout, snapshot.SearchKnowledgeTimeout, snapshot.ReadSourceTimeout, snapshot.ValidateCitationTimeout)
	}
}

func TestWorkspaceAnalysisToolCatalogSnapshotRejectsContractDrift(t *testing.T) {
	base := workspaceAnalysisSnapshotContracts(t)
	for _, test := range []struct {
		name   string
		mutate func(application.Contract) application.Contract
	}{
		{
			name: "reference",
			mutate: func(contract application.Contract) application.Contract {
				contract.Definition.Ref.Version = 1
				return contract
			},
		},
		{
			name: "definition hash",
			mutate: func(contract application.Contract) application.Contract {
				contract.Definition.DefinitionHash = testHashA
				return contract
			},
		},
		{
			name: "allowed workflow",
			mutate: func(contract application.Contract) application.Contract {
				contract.Definition.AllowedWorkflows = []domain.WorkflowBinding{{Key: "agent-rag", Version: 1}}
				return canonicalWorkspaceAnalysisSnapshotContract(t, contract)
			},
		},
		{
			name: "persistence policy",
			mutate: func(contract application.Contract) application.Contract {
				contract.Definition.ResultPersistencePolicy = domain.ResultPersistenceDisabled
				return canonicalWorkspaceAnalysisSnapshotContract(t, contract)
			},
		},
		{
			name: "capability",
			mutate: func(contract application.Contract) application.Contract {
				contract.Definition.RequiredCapability = capability.ReadExternal
				return canonicalWorkspaceAnalysisSnapshotContract(t, contract)
			},
		},
		{
			name: "timeout",
			mutate: func(contract application.Contract) application.Contract {
				contract.Definition.Timeout = 2 * time.Second
				return canonicalWorkspaceAnalysisSnapshotContract(t, contract)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			contracts := cloneWorkspaceAnalysisSnapshotContracts(base)
			ref := workspaceAnalysisToolCatalogRefs[0]
			contracts[ref] = test.mutate(contracts[ref])
			if _, err := workspaceAnalysisToolCatalogSnapshot(workspaceAnalysisSnapshotRegistry{contracts: contracts}); err == nil {
				t.Fatal("drifted contract was accepted")
			}
		})
	}
}

type workspaceAnalysisSnapshotRegistry struct {
	contracts map[domain.ToolRef]application.Contract
}

func (registry workspaceAnalysisSnapshotRegistry) ResolveContract(ref domain.ToolRef) (application.Contract, error) {
	contract, found := registry.contracts[ref]
	if !found {
		return application.Contract{}, errWorkspaceAnalysisSnapshotContractMissing
	}
	return contract, nil
}

var errWorkspaceAnalysisSnapshotContractMissing = &workspaceAnalysisSnapshotError{}

type workspaceAnalysisSnapshotError struct{}

func (*workspaceAnalysisSnapshotError) Error() string {
	return "workspace analysis snapshot contract is missing"
}

func workspaceAnalysisSnapshotContracts(t *testing.T) map[domain.ToolRef]application.Contract {
	t.Helper()
	contracts := contractsByRef(t)
	result := make(map[domain.ToolRef]application.Contract, len(workspaceAnalysisToolCatalogRefs))
	for _, ref := range workspaceAnalysisToolCatalogRefs {
		result[ref] = contracts[ref]
	}
	return result
}

func cloneWorkspaceAnalysisSnapshotContracts(source map[domain.ToolRef]application.Contract) map[domain.ToolRef]application.Contract {
	result := make(map[domain.ToolRef]application.Contract, len(source))
	for ref, contract := range source {
		contract.Definition.InputSchemaDocument = append([]byte(nil), contract.Definition.InputSchemaDocument...)
		contract.Definition.OutputSchemaDocument = append([]byte(nil), contract.Definition.OutputSchemaDocument...)
		contract.Definition.AllowedWorkflows = append([]domain.WorkflowBinding(nil), contract.Definition.AllowedWorkflows...)
		result[ref] = contract
	}
	return result
}

func canonicalWorkspaceAnalysisSnapshotContract(t *testing.T, contract application.Contract) application.Contract {
	t.Helper()
	definition, err := domain.CanonicalizeDefinition(contract.Definition)
	if err != nil {
		t.Fatal(err)
	}
	contract.Definition = definition
	return contract
}
