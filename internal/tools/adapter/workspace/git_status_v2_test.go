package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

func TestReadGitStatusV2ExecutorReturnsDirtyAggregateWithoutPaths(t *testing.T) {
	inspector := &gitStatusAggregateInspectorFake{aggregate: gitcli.StatusAggregate{
		WorkspaceID: testGitWorkspaceID, Branch: "feature/analysis", Head: strings.Repeat("a", 40),
		ObjectFormat: gitcli.StatusObjectFormatSHA1, StagedCount: 1, UnstagedCount: 2, UntrackedCount: 3, ConflictCount: 4,
	}}
	executor, err := NewReadGitStatusV2Executor(inspector)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), gitStatusV2Request([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if inspector.workspaceID != testGitWorkspaceID || result.ResultRef != "" || result.PrivateBinding != nil {
		t.Fatalf("workspace=%s result=%+v", inspector.workspaceID, result)
	}
	decoder := readGitStatusV2CatalogDecoder(t)
	canonical, err := decoder(result.Output)
	if err != nil {
		t.Fatalf("catalog rejected output %s: %v", result.Output, err)
	}
	if string(canonical) != string(result.Output) {
		t.Fatalf("output is not canonical: got=%s canonical=%s", result.Output, canonical)
	}
	var output readGitStatusOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Clean == nil || *output.Clean || *output.StagedCount != 1 || *output.UnstagedCount != 2 ||
		*output.UntrackedCount != 3 || *output.ConflictCount != 4 || strings.Contains(string(result.Output), "secret.txt") {
		t.Fatalf("output=%s", result.Output)
	}
}

func TestReadGitStatusV2ExecutorRejectsInjectionAndAggregateDrift(t *testing.T) {
	valid := gitcli.StatusAggregate{
		WorkspaceID: testGitWorkspaceID, Branch: "main", Head: strings.Repeat("a", 40),
		ObjectFormat: gitcli.StatusObjectFormatSHA1, Clean: true,
	}
	for _, arguments := range [][]byte{
		[]byte(`{"path":"/tmp/repo"}`),
		[]byte(`{"git_args":["status"]}`),
		append([]byte{' ', ' ', ' ', ' '}, make([]byte, maxGitStatusV2InputBytes)...),
	} {
		executor, err := NewReadGitStatusV2Executor(&gitStatusAggregateInspectorFake{aggregate: valid})
		if err != nil {
			t.Fatal(err)
		}
		_, err = executor.Execute(context.Background(), gitStatusV2Request(arguments))
		requireGitCode(t, err, errorCodeGitInputInvalid)
	}

	invalid := valid
	invalid.WorkspaceID = "75000000-0000-4000-8000-000000000099"
	executor, err := NewReadGitStatusV2Executor(&gitStatusAggregateInspectorFake{aggregate: invalid})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), gitStatusV2Request([]byte(`{}`)))
	requireGitCode(t, err, errorCodeGitResultInvalid)

	invalid = valid
	invalid.Clean = true
	invalid.UntrackedCount = 1
	executor, err = NewReadGitStatusV2Executor(&gitStatusAggregateInspectorFake{aggregate: invalid})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), gitStatusV2Request([]byte(`{}`)))
	requireGitCode(t, err, errorCodeGitResultInvalid)

	valid.Clean = false
	valid.StagedCount = 60_000
	valid.UnstagedCount = 60_000
	if err := validateGitStatusAggregate(testGitWorkspaceID, valid); err != nil {
		t.Fatalf("individually bounded counts were rejected: %v", err)
	}
}

func TestReadGitStatusV2ExecutorPreservesInspectorFailureWithoutLeakingOutput(t *testing.T) {
	dependencyErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "GIT_DOWN", false, errors.New("secret/path/file.txt"))
	executor, err := NewReadGitStatusV2Executor(&gitStatusAggregateInspectorFake{err: dependencyErr})
	if err != nil {
		t.Fatal(err)
	}
	result, actual := executor.Execute(context.Background(), gitStatusV2Request([]byte(`{}`)))
	if !errors.Is(actual, dependencyErr) || len(result.Output) != 0 || result.PrivateBinding != nil {
		t.Fatalf("result=%+v error=%v", result, actual)
	}
}

type gitStatusAggregateInspectorFake struct {
	workspaceID foundation.ID
	aggregate   gitcli.StatusAggregate
	err         error
}

func (fake *gitStatusAggregateInspectorFake) Inspect(_ context.Context, workspaceID foundation.ID) (gitcli.StatusAggregate, error) {
	fake.workspaceID = workspaceID
	return fake.aggregate, fake.err
}

func gitStatusV2Request(arguments []byte) toolsapplication.ExecutorRequest {
	request := gitStatusRequest(arguments)
	request.Tool = readGitStatusV2Ref
	return request
}

func readGitStatusV2CatalogDecoder(t *testing.T) func(json.RawMessage) (json.RawMessage, error) {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range contracts {
		if contract.Definition.Ref == readGitStatusV2Ref {
			return func(document json.RawMessage) (json.RawMessage, error) {
				return contract.DecodeOutput(document)
			}
		}
	}
	t.Fatal("ReadGitStatus@2 contract not found")
	return nil
}

var _ toolsapplication.Executor = (*ReadGitStatusV2Executor)(nil)
var _ = toolsdomain.ResultPersistenceCanonical
