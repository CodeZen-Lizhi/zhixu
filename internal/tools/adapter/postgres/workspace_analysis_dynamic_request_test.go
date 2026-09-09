package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestDynamicToolAuthorizationBindsCanonicalArgumentsToCallHash(t *testing.T) {
	registry, err := catalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.ToolRef{Name: "SearchKnowledge", Version: 3}
	contract, err := registry.ResolveContract(ref)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := contract.DecodeInput([]byte(`{"query":"approved evidence","mode":"keyword","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(struct {
		SchemaVersion int              `json:"schema_version"`
		ToolName      string           `json:"tool_name"`
		ToolVersion   int64            `json:"tool_version"`
		InputSchema   domain.SchemaRef `json:"input_schema"`
		Arguments     json.RawMessage  `json:"arguments"`
		Reason        string           `json:"reason"`
	}{1, ref.Name, ref.Version, contract.Definition.InputSchema, arguments, "dynamic decision"})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	command := application.AuthorizeWorkspaceAnalysisToolCallCommand{
		Identity: domain.TrustedExecutionIdentity{DefinitionVersion: 2}, CanonicalRequest: document,
		Call: domain.ToolCall{Tool: &ref, InputSchema: &contract.Definition.InputSchema, RequestHash: hex.EncodeToString(digest[:]), RequestBytes: int64(len(document))},
	}
	actual, err := workspaceAnalysisAuthorizedArguments(command)
	if err != nil || string(actual) != string(arguments) {
		t.Fatalf("canonical request failed: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*application.AuthorizeWorkspaceAnalysisToolCallCommand)
	}{
		{"changed query", func(c *application.AuthorizeWorkspaceAnalysisToolCallCommand) {
			c.CanonicalRequest = []byte(strings.Replace(string(document), "approved evidence", "other evidence", 1))
		}},
		{"hash", func(c *application.AuthorizeWorkspaceAnalysisToolCallCommand) {
			c.Call.RequestHash = strings.Repeat("a", 64)
		}},
		{"bytes", func(c *application.AuthorizeWorkspaceAnalysisToolCallCommand) { c.Call.RequestBytes++ }},
		{"tool", func(c *application.AuthorizeWorkspaceAnalysisToolCallCommand) {
			tool := domain.ToolRef{Name: "ReadSource", Version: 4}
			c.Call.Tool = &tool
		}},
		{"missing document", func(c *application.AuthorizeWorkspaceAnalysisToolCallCommand) { c.CanonicalRequest = nil }},
		{"duplicate key", func(c *application.AuthorizeWorkspaceAnalysisToolCallCommand) {
			c.CanonicalRequest = []byte(strings.Replace(string(document), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := command
			test.mutate(&changed)
			if _, err := workspaceAnalysisAuthorizedArguments(changed); err == nil {
				t.Fatal("changed canonical request was accepted")
			}
		})
	}
}
