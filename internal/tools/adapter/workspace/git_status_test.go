package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const testGitWorkspaceID foundation.ID = "75000000-0000-4000-8000-000000000001"

func TestReadGitStatusExecutorUsesOnlyTrustedWorkspaceAndMatchesCatalog(t *testing.T) {
	inspector := &gitInspectorFake{snapshot: validGitSnapshot()}
	executor, err := NewReadGitStatusExecutor(inspector)
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{}`)
	original := append([]byte(nil), arguments...)
	result, err := executor.Execute(context.Background(), gitStatusRequest(arguments))
	if err != nil {
		t.Fatal(err)
	}
	if inspector.workspaceID != testGitWorkspaceID || string(arguments) != string(original) {
		t.Fatalf("workspace=%s mutated=%t", inspector.workspaceID, string(arguments) != string(original))
	}
	decodeGitCatalogOutput(t, result.Output)
	var output readGitStatusOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Branch != "main" || output.Head != strings.Repeat("a", 40) || output.ObjectFormat != "sha1" || output.Clean == nil || !*output.Clean ||
		output.StagedCount == nil || *output.StagedCount != 0 || output.UnstagedCount == nil || *output.UnstagedCount != 0 ||
		output.UntrackedCount == nil || *output.UntrackedCount != 0 || output.ConflictCount == nil || *output.ConflictCount != 0 ||
		result.ResultRef != "git-head:"+strings.Repeat("a", 40) {
		t.Fatalf("output=%s ref=%q", result.Output, result.ResultRef)
	}
	request := gitStatusRequest(arguments)
	tool := request.Tool
	receipt, err := executor.LoadResultReceipt(context.Background(), request, toolsdomain.ToolCall{Status: toolsdomain.CallSucceeded, Tool: &tool, RequestedToolName: readGitStatusName, ResultRef: result.ResultRef})
	if err != nil || string(receipt.Output) != string(result.Output) || receipt.ResultRef != result.ResultRef {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestReadGitStatusExecutorRejectsInjectedFieldsAndInvalidInspectorResult(t *testing.T) {
	executor, err := NewReadGitStatusExecutor(&gitInspectorFake{snapshot: validGitSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]byte{
		[]byte(`{"workspace_id":"forged"}`),
		[]byte(`{"path":"/tmp/repo"}`),
		[]byte(`{"git_args":["status"]}`),
	} {
		_, err := executor.Execute(context.Background(), gitStatusRequest(arguments))
		requireGitCode(t, err, errorCodeGitInputInvalid)
	}
	invalid := validGitSnapshot()
	invalid.WorkspaceID = "75000000-0000-4000-8000-000000000099"
	executor, err = NewReadGitStatusExecutor(&gitInspectorFake{snapshot: invalid})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), gitStatusRequest([]byte(`{}`)))
	requireGitCode(t, err, errorCodeGitResultInvalid)
}

func TestReadGitStatusExecutorPreservesDependencyAndContextErrors(t *testing.T) {
	dependencyErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "GIT_DOWN", false, errors.New("down"))
	executor, err := NewReadGitStatusExecutor(&gitInspectorFake{err: dependencyErr})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := executor.Execute(context.Background(), gitStatusRequest([]byte(`{}`)))
	if !errors.Is(actual, dependencyErr) {
		t.Fatalf("error=%v", actual)
	}
	executor, err = NewReadGitStatusExecutor(&gitInspectorFake{waitForContext: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, actual = executor.Execute(ctx, gitStatusRequest([]byte(`{}`)))
	if !errors.Is(actual, context.Canceled) {
		t.Fatalf("cancel error=%v", actual)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_, actual = executor.Execute(ctx, gitStatusRequest([]byte(`{}`)))
	if !errors.Is(actual, context.DeadlineExceeded) {
		t.Fatalf("deadline error=%v", actual)
	}
}

func TestReadGitStatusExecutorRealGitSmoke(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runGit(t, gitPath, root, "init")
	runGit(t, gitPath, root, "config", "user.name", "Zhixu Test")
	runGit(t, gitPath, root, "config", "user.email", "zhixu@example.test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# smoke\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, root, "add", "--", "README.md")
	runGit(t, gitPath, root, "commit", "-m", "initial")

	client, err := gitcli.NewWritebackClient(gitcli.New(gitPath), workspaceRepositoryFake{workspace: workspacedomain.Workspace{
		ID: testGitWorkspaceID, Name: "smoke", RootPath: root, Status: workspacedomain.WorkspaceStatusActive, Version: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewReadGitStatusExecutor(client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), gitStatusRequest([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	decodeGitCatalogOutput(t, result.Output)
	var output readGitStatusOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Clean == nil || !*output.Clean || output.Head == "" || output.Branch == "" {
		t.Fatalf("output=%s", result.Output)
	}
}

type gitInspectorFake struct {
	workspaceID    foundation.ID
	snapshot       changecontroldomain.GitSnapshot
	err            error
	waitForContext bool
}

func (fake *gitInspectorFake) CaptureApprovalSnapshot(ctx context.Context, workspaceID foundation.ID) (changecontroldomain.GitSnapshot, error) {
	fake.workspaceID = workspaceID
	if fake.waitForContext {
		<-ctx.Done()
		return changecontroldomain.GitSnapshot{}, ctx.Err()
	}
	return fake.snapshot, fake.err
}

type workspaceRepositoryFake struct{ workspace workspacedomain.Workspace }

func (fake workspaceRepositoryFake) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return fake.workspace, nil
}

func validGitSnapshot() changecontroldomain.GitSnapshot {
	return changecontroldomain.GitSnapshot{
		WorkspaceID: testGitWorkspaceID, Branch: "main", Head: strings.Repeat("a", 40),
		ObjectFormat: changecontroldomain.GitObjectFormatSHA1, Clean: true,
	}
}

func gitStatusRequest(arguments []byte) toolsapplication.ExecutorRequest {
	return toolsapplication.ExecutorRequest{
		Identity: toolsdomain.TrustedExecutionIdentity{
			WorkspaceID: testGitWorkspaceID, DefinitionID: "75000000-0000-4000-8000-000000000002", DefinitionVersion: 1,
			DefinitionHash: strings.Repeat("b", 64), WorkflowRunID: "75000000-0000-4000-8000-000000000003",
			NodeKey: "git-status", NodeRunID: "75000000-0000-4000-8000-000000000004", NodeAttemptID: "75000000-0000-4000-8000-000000000005",
			LeaseOwner: "worker-1", LeaseFence: 1,
		},
		Tool: toolsdomain.ToolRef{Name: readGitStatusName, Version: 1}, Arguments: arguments, Reason: "inspect workspace git status",
	}
}

func decodeGitCatalogOutput(t *testing.T, raw []byte) {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range contracts {
		if contract.Definition.Ref.Name == readGitStatusName {
			if _, err := contract.DecodeOutput(raw); err != nil {
				t.Fatalf("catalog rejected output %s: %v", raw, err)
			}
			return
		}
	}
	t.Fatal("ReadGitStatus contract not found")
}

func requireGitCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error=%v code=%q", err, code)
	}
}

func runGit(t *testing.T, executable, root string, arguments ...string) {
	t.Helper()
	command := exec.Command(executable, append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
