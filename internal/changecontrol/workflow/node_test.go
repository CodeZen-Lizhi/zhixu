package changecontrolworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	nodeExecutionID foundation.ID = "10000000-0000-4000-8000-000000000001"
	nodeWorkspaceID foundation.ID = "20000000-0000-4000-8000-000000000001"
	nodeRunID       foundation.ID = "30000000-0000-4000-8000-000000000001"
	nodeNodeID      foundation.ID = "40000000-0000-4000-8000-000000000001"
	nodeProposalID  foundation.ID = "50000000-0000-4000-8000-000000000001"
	nodeRevisionID  foundation.ID = "60000000-0000-4000-8000-000000000001"
	nodeGitCommit                 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nodeResultHash                = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	nodeDiffHash                  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type fakeResumer struct {
	result    application.WritebackResult
	err       error
	execution foundation.ID
	owner     string
}

func (f *fakeResumer) Resume(_ context.Context, execution foundation.ID, owner string) (application.WritebackResult, error) {
	f.execution, f.owner = execution, owner
	return f.result, f.err
}

func TestNodeExecutesOnlyStableBoundWriteback(t *testing.T) {
	resumer := &fakeResumer{result: successfulNodeResult()}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	input := validNodeInput()
	output, err := node.Execute(context.Background(), input, " worker-a ")
	if err != nil || output.Status != domain.WritebackStatusVerifying || output.IndexStatus != application.WritebackIndexStatusPending || output.GitCommit != nodeGitCommit || resumer.execution != nodeExecutionID || resumer.owner != "worker-a" {
		t.Fatalf("output=%#v execution=%s owner=%q err=%v", output, resumer.execution, resumer.owner, err)
	}
	encodedInput, _ := json.Marshal(input)
	encodedOutput, _ := json.Marshal(output)
	for _, forbidden := range []string{"credential", "content", "target_path", "temporary_ref", "backup_ref", "lock_token", "/tmp/", "git_args"} {
		if strings.Contains(string(encodedInput), forbidden) || strings.Contains(string(encodedOutput), forbidden) {
			t.Fatalf("node payload leaked %q: input=%s output=%s", forbidden, encodedInput, encodedOutput)
		}
	}
}

func TestNodeRejectsBindingMismatchAndIncompleteResult(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*application.WritebackResult)
	}{
		{"workspace", func(result *application.WritebackResult) { result.WorkspaceID = "different" }},
		{"node", func(result *application.WritebackResult) { result.NodeRunID = "different" }},
		{"cleanup pending", func(result *application.WritebackResult) { result.CleanupPending = true }},
		{"not verifying", func(result *application.WritebackResult) { result.Status = domain.WritebackStatusGitCommitted }},
		{"missing commit", func(result *application.WritebackResult) { result.GitCommit = "" }},
		{"missing proposal", func(result *application.WritebackResult) { result.ProposalID = "" }},
		{"missing revision", func(result *application.WritebackResult) { result.RevisionID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := successfulNodeResult()
			test.mutate(&result)
			node, err := NewNode(&fakeResumer{result: result})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := node.Execute(context.Background(), validNodeInput(), "worker-a"); err == nil {
				t.Fatal("invalid result was accepted")
			}
		})
	}
}

func TestNodePropagatesSagaFailureWithoutOutput(t *testing.T) {
	want := foundation.NewError(foundation.ErrorManualRecoveryRequired, "GIT_COMMIT_RESULT_UNKNOWN", false, errors.New("unknown"))
	node, err := NewNode(&fakeResumer{err: want})
	if err != nil {
		t.Fatal(err)
	}
	output, err := node.Execute(context.Background(), validNodeInput(), "worker-a")
	if !errors.Is(err, want) || output != (Output{}) {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func validNodeInput() Input {
	return Input{SchemaVersion: application.SafeWritebackSchemaVersion, ExecutionID: nodeExecutionID, WorkspaceID: nodeWorkspaceID, WorkflowRunID: nodeRunID, NodeRunID: nodeNodeID}
}

func successfulNodeResult() application.WritebackResult {
	return application.WritebackResult{
		ExecutionID: nodeExecutionID, WorkspaceID: nodeWorkspaceID, WorkflowRunID: nodeRunID, NodeRunID: nodeNodeID,
		ProposalID: nodeProposalID, RevisionID: nodeRevisionID, Status: domain.WritebackStatusVerifying,
		GitCommit: nodeGitCommit, ResultHash: nodeResultHash, DiffHash: nodeDiffHash,
		IndexStatus: application.WritebackIndexStatusPending,
	}
}
