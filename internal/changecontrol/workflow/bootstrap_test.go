package changecontrolworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

const (
	bootstrapWorkspaceID  foundation.ID = "81000000-0000-4000-8000-000000000001"
	bootstrapRunID        foundation.ID = "82000000-0000-4000-8000-000000000001"
	bootstrapNodeID       foundation.ID = "83000000-0000-4000-8000-000000000001"
	bootstrapProposalID   foundation.ID = "84000000-0000-4000-8000-000000000001"
	bootstrapRevisionID   foundation.ID = "85000000-0000-4000-8000-000000000001"
	bootstrapApprovalID   foundation.ID = "86000000-0000-4000-8000-000000000001"
	bootstrapExecutionID  foundation.ID = "87000000-0000-4000-8000-000000000001"
	bootstrapWriteAuthID  foundation.ID = "88000000-0000-4000-8000-000000000001"
	bootstrapGitAuthID    foundation.ID = "89000000-0000-4000-8000-000000000001"
	bootstrapGeneration   foundation.ID = "8a000000-0000-4000-8000-000000000001"
	bootstrapDefinitionID foundation.ID = "8b000000-0000-4000-8000-000000000001"
	bootstrapAttemptID    foundation.ID = "8c000000-0000-4000-8000-000000000001"
	bootstrapBaseHash                   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	bootstrapGitHead                    = "dddddddddddddddddddddddddddddddddddddddd"
	bootstrapGitCommit                  = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	bootstrapResultHash                 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	bootstrapDiffHash                   = "1111111111111111111111111111111111111111111111111111111111111111"
)

func TestBootstrapExecutorResumesExactExecutionWithoutIssuingCredentials(t *testing.T) {
	proposal := bootstrapProposal()
	execution := bootstrapExecution(proposal)
	lookup := &bootstrapLookupFake{execution: execution, found: true}
	changeControl := &bootstrapChangeControlFake{proposal: proposal}
	beginner := &bootstrapBeginFake{}
	resumer := &bootstrapResumer{result: bootstrapResult(execution)}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	executor := newBootstrapExecutorFixture(t, lookup, changeControl, beginner, node)

	result, err := executor.Execute(context.Background(), bootstrapExecutionContext(t))
	if err != nil {
		t.Fatal(err)
	}
	var output Output
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.ExecutionID != execution.ID || output.Status != domain.WritebackStatusVerifying || output.IndexStatus != changecontrolapplication.WritebackIndexStatusPending {
		t.Fatalf("output=%#v", output)
	}
	if len(changeControl.issues) != 0 || beginner.calls != 0 || resumer.executionID != execution.ID || resumer.identity.LeaseOwner != "worker-delivery" || resumer.identity.NodeAttemptID != bootstrapAttemptID {
		t.Fatalf("issues=%#v begin_calls=%d resume=%s identity=%+v", changeControl.issues, beginner.calls, resumer.executionID, resumer.identity)
	}
}

func TestBootstrapExecutorCreatesExecutionWithEphemeralDoubleAuthorization(t *testing.T) {
	proposal := bootstrapProposal()
	execution := bootstrapExecution(proposal)
	lookup := &bootstrapLookupFake{}
	changeControl := &bootstrapChangeControlFake{proposal: proposal}
	beginner := &bootstrapBeginFake{lookup: lookup, execution: execution}
	resumer := &bootstrapResumer{result: bootstrapResult(execution)}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	executor := newBootstrapExecutorFixture(t, lookup, changeControl, beginner, node)

	result, err := executor.Execute(context.Background(), bootstrapExecutionContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) == 0 || len(changeControl.issues) != 2 || beginner.calls != 1 {
		t.Fatalf("result=%s issues=%#v beginner=%#v", result.Output, changeControl.issues, beginner)
	}
	writeIssue, gitIssue := changeControl.issues[0], changeControl.issues[1]
	if writeIssue.Capability != domain.CapabilityWriteKnowledge || writeIssue.ToolName != "ApplyApprovedPatch" || gitIssue.Capability != domain.CapabilityGitWrite || gitIssue.ToolName != "CreateGitCommit" {
		t.Fatalf("issues=%#v", changeControl.issues)
	}
	if writeIssue.TTL != SafeWritebackAuthorizationTTL || gitIssue.TTL != SafeWritebackAuthorizationTTL || writeIssue.IdempotencyKey == gitIssue.IdempotencyKey || !strings.Contains(writeIssue.IdempotencyKey, string(bootstrapGeneration)) || writeIssue.Scope != domain.ExpectedAuthorizationScope(proposal.TargetPath) {
		t.Fatalf("issues=%#v", changeControl.issues)
	}
	if beginner.command.IdempotencyKey != "safe-writeback:"+string(bootstrapNodeID) || beginner.command.WriteCredential != "write-credential" || beginner.command.GitCredential != "git-credential" || beginner.command.LeaseOwner != "worker-delivery" {
		t.Fatalf("begin=%#v", beginner.command)
	}
}

func TestBootstrapExecutorBindingConflictIsManualAndHasNoSideEffect(t *testing.T) {
	proposal := bootstrapProposal()
	execution := bootstrapExecution(proposal)
	execution.RevisionID = "85000000-0000-4000-8000-000000000099"
	lookup := &bootstrapLookupFake{execution: execution, found: true}
	changeControl := &bootstrapChangeControlFake{proposal: proposal}
	beginner := &bootstrapBeginFake{}
	resumer := &bootstrapResumer{result: bootstrapResult(execution)}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	executor := newBootstrapExecutorFixture(t, lookup, changeControl, beginner, node)

	_, err = executor.Execute(context.Background(), bootstrapExecutionContext(t))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired || classified.Code != "WRITEBACK_EXECUTION_BINDING_CONFLICT" {
		t.Fatalf("err=%v", err)
	}
	if len(changeControl.issues) != 0 || beginner.calls != 0 || resumer.executionID != "" {
		t.Fatalf("issues=%#v begin=%d resume=%s", changeControl.issues, beginner.calls, resumer.executionID)
	}
}

func TestBootstrapExecutorRejectsProposalBoundToDifferentRun(t *testing.T) {
	proposal := bootstrapProposal()
	otherRunID := foundation.ID("73000000-0000-4000-8000-000000000099")
	proposal.WorkflowRunID = &otherRunID
	changeControl := &bootstrapChangeControlFake{proposal: proposal}
	beginner := &bootstrapBeginFake{}
	resumer := &bootstrapResumer{}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	executor := newBootstrapExecutorFixture(t, &bootstrapLookupFake{}, changeControl, beginner, node)

	_, err = executor.Execute(context.Background(), bootstrapExecutionContext(t))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired || classified.Code != "WRITEBACK_EXECUTION_BINDING_CONFLICT" {
		t.Fatalf("err=%v", err)
	}
	if len(changeControl.issues) != 0 || beginner.calls != 0 || resumer.executionID != "" {
		t.Fatalf("issues=%#v begin=%d resume=%s", changeControl.issues, beginner.calls, resumer.executionID)
	}
}

func TestBootstrapExecutorRejectsDownstreamUpdateBeforeIssuingAuthorization(t *testing.T) {
	proposal := bootstrapProposal()
	proposal.Type = domain.ProposalTypeDownstreamUpdate
	execution := bootstrapExecution(proposal)
	lookup := &bootstrapLookupFake{execution: execution, found: true}
	changeControl := &bootstrapChangeControlFake{proposal: proposal}
	beginner := &bootstrapBeginFake{}
	resumer := &bootstrapResumer{result: bootstrapResult(execution)}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	executor := newBootstrapExecutorFixture(t, lookup, changeControl, beginner, node)

	_, err = executor.Execute(context.Background(), bootstrapExecutionContext(t))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.DownstreamUpdateApplyUnavailableCode || classified.Kind != foundation.ErrorVersionConflict || classified.Retryable {
		t.Fatalf("error=%v", err)
	}
	if len(changeControl.issues) != 0 || beginner.calls != 0 || resumer.executionID != "" {
		t.Fatalf("downstream update reached bootstrap side effect: issues=%#v begin=%d resume=%s", changeControl.issues, beginner.calls, resumer.executionID)
	}
}

func TestBootstrapExecutorRecoversAtomicBeginResponseLossByExactLookup(t *testing.T) {
	proposal := bootstrapProposal()
	execution := bootstrapExecution(proposal)
	lookup := &bootstrapLookupFake{}
	changeControl := &bootstrapChangeControlFake{proposal: proposal}
	beginner := &bootstrapBeginFake{
		lookup: lookup, execution: execution,
		err: foundation.NewError(foundation.ErrorRetryableFailure, "WRITEBACK_BEGIN_RESPONSE_LOST", true, errors.New("response lost")),
	}
	resumer := &bootstrapResumer{result: bootstrapResult(execution)}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	executor := newBootstrapExecutorFixture(t, lookup, changeControl, beginner, node)

	result, err := executor.Execute(context.Background(), bootstrapExecutionContext(t))
	if err != nil || len(result.Output) == 0 || resumer.executionID != execution.ID {
		t.Fatalf("result=%s resume=%s err=%v", result.Output, resumer.executionID, err)
	}
}

func TestBootstrapExecutorNeverReusesAnAuthorizationReplayWithoutCredential(t *testing.T) {
	proposal := bootstrapProposal()
	lookup := &bootstrapLookupFake{}
	changeControl := &bootstrapChangeControlFake{proposal: proposal, replayCapability: domain.CapabilityWriteKnowledge}
	beginner := &bootstrapBeginFake{}
	resumer := &bootstrapResumer{}
	node, err := NewNode(resumer)
	if err != nil {
		t.Fatal(err)
	}
	executor := newBootstrapExecutorFixture(t, lookup, changeControl, beginner, node)

	_, err = executor.Execute(context.Background(), bootstrapExecutionContext(t))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WRITEBACK_AUTHORIZATION_ISSUE_RESULT_INVALID" {
		t.Fatalf("err=%v", err)
	}
	if beginner.calls != 0 || resumer.executionID != "" || len(changeControl.issues) != 1 {
		t.Fatalf("issues=%#v begin=%d resume=%s", changeControl.issues, beginner.calls, resumer.executionID)
	}
}

func newBootstrapExecutorFixture(t *testing.T, lookup *bootstrapLookupFake, changeControl *bootstrapChangeControlFake, beginner *bootstrapBeginFake, node *Node) *BootstrapExecutor {
	t.Helper()
	executor, err := NewBootstrapExecutor(BootstrapExecutorDependencies{
		Lookup: lookup, ChangeControl: changeControl, Beginner: beginner, Node: node,
		IDs: &bootstrapIDGenerator{ids: []foundation.ID{bootstrapGeneration}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func bootstrapExecutionContext(t *testing.T) workflowapplication.ExecutionContext {
	t.Helper()
	input, err := EncodeBootstrapInput(bootstrapProposalID, bootstrapRevisionID, bootstrapProposal().Revision.ChangeHash)
	if err != nil {
		t.Fatal(err)
	}
	return workflowapplication.ExecutionContext{
		WorkspaceID: bootstrapWorkspaceID, DefinitionID: bootstrapDefinitionID, DefinitionVersion: SafeWritebackDefinitionVersion,
		DefinitionHash: safeWritebackGraphHash, RunID: bootstrapRunID, NodeKey: SafeWritebackNodeKey, NodeRunID: bootstrapNodeID,
		NodeAttemptID: bootstrapAttemptID,
		NodeKind:      SafeWritebackNodeKind, InputSchemaVersion: SafeWritebackBootstrapInputSchemaVersion,
		AttemptNo: 1, DispatchNo: 1, LeaseOwner: "worker-delivery", Input: input,
	}
}

func bootstrapProposal() domain.Proposal {
	head := bootstrapGitHead
	content := "# Approved\n"
	workflowRunID := bootstrapRunID
	proposal := domain.Proposal{
		ID: bootstrapProposalID, WorkspaceID: bootstrapWorkspaceID, WorkflowRunID: &workflowRunID, TargetPath: "notes/approved.md", Status: domain.StatusApproved,
		Revision: domain.Revision{ID: bootstrapRevisionID, ProposalID: bootstrapProposalID, RevisionNo: 1, TargetPath: "notes/approved.md", BaseHash: bootstrapBaseHash, Content: content},
		Approval: &domain.Approval{ID: bootstrapApprovalID, ProposalID: bootstrapProposalID, RevisionID: bootstrapRevisionID, Decision: domain.DecisionApproved, ApprovedGitHead: &head},
	}
	proposal.Revision.ChangeHash = domain.ComputeChangeHash(proposal.TargetPath, proposal.Revision.BaseHash, content)
	proposal.Approval.ChangeHash = proposal.Revision.ChangeHash
	return proposal
}

func bootstrapExecution(proposal domain.Proposal) domain.WritebackExecution {
	key, _ := SafeWritebackExecutionKey(bootstrapNodeID)
	return domain.WritebackExecution{
		ID: bootstrapExecutionID, WorkspaceID: bootstrapWorkspaceID, WorkflowRunID: bootstrapRunID, NodeRunID: bootstrapNodeID,
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID,
		WriteAuthorizationID: bootstrapWriteAuthID, GitAuthorizationID: bootstrapGitAuthID,
		TargetPath: proposal.TargetPath, BaseHash: proposal.Revision.BaseHash,
		ResultHash: domain.ComputeWritebackResultHash([]byte(proposal.Revision.Content)), ApprovedChangeHash: proposal.Revision.ChangeHash,
		ApprovedGitHead: *proposal.Approval.ApprovedGitHead, Status: domain.WritebackStatusPrepared, IdempotencyKey: key, Version: 1,
	}
}

func bootstrapResult(execution domain.WritebackExecution) changecontrolapplication.WritebackResult {
	return changecontrolapplication.WritebackResult{
		ExecutionID: execution.ID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.WorkflowRunID, NodeRunID: execution.NodeRunID,
		ProposalID: execution.ProposalID, RevisionID: execution.RevisionID, Status: domain.WritebackStatusVerifying,
		GitCommit: bootstrapGitCommit, ResultHash: bootstrapResultHash, DiffHash: bootstrapDiffHash,
		IndexStatus: changecontrolapplication.WritebackIndexStatusPending,
	}
}

type bootstrapLookupFake struct {
	execution domain.WritebackExecution
	found     bool
	err       error
	workspace foundation.ID
	key       string
	calls     int
}

func (f *bootstrapLookupFake) FindWritebackExecutionByKey(_ context.Context, workspace foundation.ID, key string) (domain.WritebackExecution, bool, error) {
	f.calls++
	f.workspace = workspace
	f.key = key
	return f.execution, f.found, f.err
}

type bootstrapChangeControlFake struct {
	proposal         domain.Proposal
	issues           []domain.AuthorizationIssue
	err              error
	replayCapability domain.Capability
}

func (f *bootstrapChangeControlFake) GetProposal(context.Context, foundation.ID) (domain.Proposal, error) {
	return f.proposal, f.err
}

func (f *bootstrapChangeControlFake) IssueWriteAuthorization(_ context.Context, issue domain.AuthorizationIssue) (domain.AuthorizationIssueResult, error) {
	f.issues = append(f.issues, issue)
	result := domain.AuthorizationIssueResult{Authorization: domain.ToolAuthorization{IdempotencyKey: issue.IdempotencyKey}}
	result.Authorization.WorkspaceID = issue.WorkspaceID
	result.Authorization.WorkflowRunID = issue.WorkflowRunID
	result.Authorization.NodeRunID = issue.NodeRunID
	result.Authorization.ProposalID = issue.ProposalID
	result.Authorization.RevisionID = issue.RevisionID
	result.Authorization.ApprovalID = issue.ApprovalID
	result.Authorization.ToolName = issue.ToolName
	result.Authorization.Capability = issue.Capability
	result.Authorization.Scope = issue.Scope
	result.Authorization.ApprovedChangeHash = f.proposal.Revision.ChangeHash
	result.Authorization.TargetVersion = f.proposal.Revision.BaseHash
	result.Authorization.Status = domain.AuthorizationIssued
	if issue.Capability == f.replayCapability {
		result.Replayed = true
		return result, f.err
	}
	switch issue.Capability {
	case domain.CapabilityWriteKnowledge:
		result.Authorization.ID = bootstrapWriteAuthID
		result.Credential = "write-credential"
	case domain.CapabilityGitWrite:
		result.Authorization.ID = bootstrapGitAuthID
		result.Credential = "git-credential"
	}
	return result, f.err
}

type bootstrapBeginFake struct {
	lookup    *bootstrapLookupFake
	execution domain.WritebackExecution
	command   changecontrolapplication.BeginWritebackCommand
	calls     int
	err       error
}

func (f *bootstrapBeginFake) Begin(_ context.Context, command changecontrolapplication.BeginWritebackCommand) (changecontrolapplication.BeginWritebackResult, error) {
	f.calls++
	f.command = command
	if f.lookup != nil {
		f.lookup.execution = f.execution
		f.lookup.found = true
	}
	return changecontrolapplication.BeginWritebackResult{ExecutionID: f.execution.ID, Status: f.execution.Status}, f.err
}

type bootstrapResumer struct {
	result      changecontrolapplication.WritebackResult
	err         error
	executionID foundation.ID
	identity    changecontrolapplication.WritebackResumeIdentity
}

func (f *bootstrapResumer) Resume(_ context.Context, executionID foundation.ID, identity changecontrolapplication.WritebackResumeIdentity) (changecontrolapplication.WritebackResult, error) {
	f.executionID = executionID
	f.identity = identity
	return f.result, f.err
}

type bootstrapIDGenerator struct {
	ids []foundation.ID
}

func (g *bootstrapIDGenerator) New() (foundation.ID, error) {
	if len(g.ids) == 0 {
		return "", errors.New("no id")
	}
	id := g.ids[0]
	g.ids = g.ids[1:]
	return id, nil
}

var _ workflowapplication.Executor = (*BootstrapExecutor)(nil)
