package workspace

import (
	"context"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestReadGitStatusV3RequiresDynamicWorkflowAndExactTool(t *testing.T) {
	inspector := &gitStatusAggregateInspectorFake{aggregate: gitcli.StatusAggregate{WorkspaceID: testGitWorkspaceID, Branch: "main", Head: strings.Repeat("a", 40), ObjectFormat: gitcli.StatusObjectFormatSHA1, Clean: true}}
	executor, err := NewReadGitStatusV3Executor(inspector)
	if err != nil {
		t.Fatal(err)
	}
	request := gitStatusV2Request([]byte(`{}`))
	request.Tool = toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 3}
	_, err = executor.Execute(context.Background(), request)
	requireGitCode(t, err, errorCodeGitInputInvalid)
	if inspector.workspaceID != "" {
		t.Fatal("legacy workflow reached the Git inspector")
	}
	request.Identity.DefinitionVersion = 2
	request.Identity.NodeKey = "decide_next"
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := catalog.NewFrozenContractRegistry()
	if err != nil {
		t.Fatal(err)
	}
	contract, err := registry.ResolveContract(request.Tool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contract.DecodeOutput(result.Output); err != nil {
		t.Fatal(err)
	}
	if inspector.workspaceID != testGitWorkspaceID {
		t.Fatal("Git status did not bind the authorized workspace")
	}
}
