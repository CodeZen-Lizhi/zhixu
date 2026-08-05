package changecontrolworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// BootstrapChangeControl 是 Bootstrap 读取批准事实并签发瞬时双授权的应用端口。
type BootstrapChangeControl interface {
	GetProposal(context.Context, foundation.ID) (domain.Proposal, error)
	IssueWriteAuthorization(context.Context, domain.AuthorizationIssue) (domain.AuthorizationIssueResult, error)
}

// WritebackBeginner 是 Atomic Begin 的最小应用端口。
type WritebackBeginner interface {
	Begin(context.Context, changecontrolapplication.BeginWritebackCommand) (changecontrolapplication.BeginWritebackResult, error)
}

// SafeWritebackNode 是 Begin/lookup 后继续现有 Saga 的瞬时 Node 端口。
type SafeWritebackNode interface {
	Execute(context.Context, Input, changecontrolapplication.WritebackResumeIdentity) (Output, error)
}

// BootstrapExecutorDependencies 是 Safe Writeback Bootstrap Executor 的受信依赖。
type BootstrapExecutorDependencies struct {
	Lookup        domain.WritebackExecutionLookup
	ChangeControl BootstrapChangeControl
	Beginner      WritebackBeginner
	Node          SafeWritebackNode
	IDs           foundation.IDGenerator
}

// BootstrapExecutor 把持久 Proposal identity 转换为 exact Execution，再调用现有 Safe Writeback Node。
type BootstrapExecutor struct {
	lookup        domain.WritebackExecutionLookup
	changeControl BootstrapChangeControl
	beginner      WritebackBeginner
	node          SafeWritebackNode
	ids           foundation.IDGenerator
}

// NewBootstrapExecutor 创建不持有 Credential 或副作用状态的 Safe Writeback Executor。
func NewBootstrapExecutor(dependencies BootstrapExecutorDependencies) (*BootstrapExecutor, error) {
	if isNilBootstrapDependency(dependencies.Lookup) || isNilBootstrapDependency(dependencies.ChangeControl) || isNilBootstrapDependency(dependencies.Beginner) || isNilBootstrapDependency(dependencies.Node) || isNilBootstrapDependency(dependencies.IDs) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BOOTSTRAP_DEPENDENCY_MISSING", false, errors.New("safe writeback bootstrap dependency missing"))
	}
	return &BootstrapExecutor{
		lookup: dependencies.Lookup, changeControl: dependencies.ChangeControl,
		beginner: dependencies.Beginner, node: dependencies.Node, ids: dependencies.IDs,
	}, nil
}

// Execute 先 exact lookup，缺失时才签发瞬时 Credential 并 Atomic Begin，随后只 Resume Durable Execution。
func (e *BootstrapExecutor) Execute(ctx context.Context, execution workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	input, err := validateBootstrapExecutionContext(ctx, execution)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	execution.LeaseOwner = strings.TrimSpace(execution.LeaseOwner)
	executionKey, err := SafeWritebackExecutionKey(execution.NodeRunID)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	durable, found, err := e.lookup.FindWritebackExecutionByKey(ctx, execution.WorkspaceID, executionKey)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	proposal, err := e.changeControl.GetProposal(ctx, input.ProposalID)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateBootstrapProposal(execution, input, proposal); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if found {
		if err := validateBootstrapExecutionBinding(execution, input, proposal, durable, executionKey); err != nil {
			return workflowapplication.ExecutionResult{}, err
		}
		return e.executeNode(ctx, execution, durable)
	}
	if proposal.Status != domain.StatusApproved {
		return workflowapplication.ExecutionResult{}, bootstrapBindingConflict(errors.New("proposal is not approved before atomic begin"))
	}

	generation, err := e.ids.New()
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if parsed, parseErr := foundation.ParseID(string(generation)); parseErr != nil || parsed != generation {
		return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_AUTHORIZATION_GENERATION_INVALID", true, errors.Join(parseErr, domain.ErrWritebackInvalidInput))
	}
	writeKey := authorizationGenerationKey(execution.NodeRunID, generation, "write")
	gitKey := authorizationGenerationKey(execution.NodeRunID, generation, "git")
	writeAuthorization, err := e.issueAuthorization(ctx, execution, proposal, writeKey, "ApplyApprovedPatch", domain.CapabilityWriteKnowledge)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	gitAuthorization, err := e.issueAuthorization(ctx, execution, proposal, gitKey, "CreateGitCommit", domain.CapabilityGitWrite)
	if err != nil {
		writeAuthorization.Credential = ""
		return workflowapplication.ExecutionResult{}, err
	}
	beginCommand := changecontrolapplication.BeginWritebackCommand{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID,
		ProposalID: input.ProposalID, LeaseOwner: execution.LeaseOwner, IdempotencyKey: executionKey,
		WriteCredential: writeAuthorization.Credential, GitCredential: gitAuthorization.Credential,
		WriteAuthorizationKey: writeAuthorization.Authorization.IdempotencyKey,
		GitAuthorizationKey:   gitAuthorization.Authorization.IdempotencyKey,
	}
	beginResult, beginErr := e.beginner.Begin(ctx, beginCommand)
	beginCommand.WriteCredential, beginCommand.GitCredential = "", ""
	writeAuthorization.Credential, gitAuthorization.Credential = "", ""

	durable, found, lookupErr := e.lookup.FindWritebackExecutionByKey(ctx, execution.WorkspaceID, executionKey)
	if lookupErr != nil {
		if beginErr != nil {
			return workflowapplication.ExecutionResult{}, errors.Join(beginErr, lookupErr)
		}
		return workflowapplication.ExecutionResult{}, lookupErr
	}
	if !found {
		if beginErr != nil {
			if hasFoundationCode(beginErr, "WRITEBACK_IDENTITY_CONFLICT") {
				return workflowapplication.ExecutionResult{}, bootstrapBindingConflict(beginErr)
			}
			return workflowapplication.ExecutionResult{}, beginErr
		}
		return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorManualRecoveryRequired, "WRITEBACK_EXECUTION_MISSING", false, errors.New("atomic begin returned without a durable execution"))
	}
	if beginErr == nil && beginResult.ExecutionID != durable.ID {
		return workflowapplication.ExecutionResult{}, bootstrapBindingConflict(errors.New("atomic begin result differs from exact execution"))
	}
	if err := validateBootstrapExecutionBinding(execution, input, proposal, durable, executionKey); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return e.executeNode(ctx, execution, durable)
}

func (e *BootstrapExecutor) issueAuthorization(ctx context.Context, execution workflowapplication.ExecutionContext, proposal domain.Proposal, key, tool string, capability domain.Capability) (domain.AuthorizationIssueResult, error) {
	issue := domain.AuthorizationIssue{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID,
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: proposal.Approval.ID,
		ToolName: tool, Capability: capability, Scope: domain.ExpectedAuthorizationScopeForTarget(proposal.TargetPath, proposal.Revision.TargetMode),
		IdempotencyKey: key, TTL: SafeWritebackAuthorizationTTL,
	}
	result, err := e.changeControl.IssueWriteAuthorization(ctx, issue)
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	authorization := result.Authorization
	if result.Replayed || authorization.ID == "" || authorization.WorkspaceID != issue.WorkspaceID || authorization.WorkflowRunID != issue.WorkflowRunID || authorization.NodeRunID != issue.NodeRunID || authorization.ProposalID != issue.ProposalID || authorization.RevisionID != issue.RevisionID || authorization.ApprovalID != issue.ApprovalID || authorization.ToolName != issue.ToolName || authorization.Capability != issue.Capability || authorization.Scope != issue.Scope || domain.NormalizeTargetMode(authorization.TargetMode) != domain.NormalizeTargetMode(proposal.Revision.TargetMode) || !strings.EqualFold(authorization.ApprovedChangeHash, proposal.Revision.ChangeHash) || !strings.EqualFold(authorization.TargetVersion, proposal.Revision.BaseHash) || authorization.IdempotencyKey != key || authorization.Status != domain.AuthorizationIssued || strings.TrimSpace(result.Credential) == "" || len(result.Credential) > domain.MaxAuthorizationCredentialBytes {
		result.Credential = ""
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_AUTHORIZATION_ISSUE_RESULT_INVALID", false, errors.New("authorization issue did not return a new ephemeral credential"))
	}
	return result, nil
}

func (e *BootstrapExecutor) executeNode(ctx context.Context, execution workflowapplication.ExecutionContext, durable domain.WritebackExecution) (workflowapplication.ExecutionResult, error) {
	output, err := e.node.Execute(ctx, Input{
		SchemaVersion: changecontrolapplication.SafeWritebackSchemaVersion,
		ExecutionID:   durable.ID, WorkspaceID: durable.WorkspaceID,
		WorkflowRunID: durable.WorkflowRunID, NodeRunID: durable.NodeRunID,
	}, changecontrolapplication.WritebackResumeIdentity{
		WorkspaceID: execution.WorkspaceID, DefinitionID: execution.DefinitionID,
		DefinitionVersion: execution.DefinitionVersion, DefinitionHash: execution.DefinitionHash,
		WorkflowRunID: execution.RunID, NodeKey: execution.NodeKey, NodeRunID: execution.NodeRunID,
		NodeAttemptID: execution.NodeAttemptID, LeaseOwner: execution.LeaseOwner, LeaseFence: int64(execution.AttemptNo),
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return workflowapplication.ExecutionResult{}, foundation.NewError(foundation.ErrorNonRetryableFailure, "WRITEBACK_WORKFLOW_OUTPUT_ENCODING_FAILED", false, err)
	}
	return workflowapplication.ExecutionResult{Output: encoded}, nil
}

func validateBootstrapExecutionContext(ctx context.Context, execution workflowapplication.ExecutionContext) (BootstrapInput, error) {
	if ctx == nil || execution.NodeKind != SafeWritebackNodeKind || execution.InputSchemaVersion != SafeWritebackBootstrapInputSchemaVersion || execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 || domain.ValidateWritebackLeaseOwner(execution.LeaseOwner) != nil {
		return BootstrapInput{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_BOOTSTRAP_CONTEXT_INVALID", false, domain.ErrWritebackInvalidInput)
	}
	for _, id := range []foundation.ID{execution.WorkspaceID, execution.RunID, execution.NodeRunID} {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return BootstrapInput{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_BOOTSTRAP_CONTEXT_INVALID", false, errors.Join(err, domain.ErrWritebackInvalidInput))
		}
	}
	return DecodeBootstrapInput(execution.Input)
}

func validateBootstrapProposal(execution workflowapplication.ExecutionContext, input BootstrapInput, proposal domain.Proposal) error {
	if domain.NormalizeProposalType(proposal.Type) == domain.ProposalTypeDownstreamUpdate {
		return domain.NewDownstreamUpdateApplyUnavailableError()
	}
	expectedChangeHash, err := domain.ComputeChangeHashForTarget(proposal.WorkspaceID, proposal.Revision.TargetPath, proposal.Revision.TargetMode, proposal.Revision.BaseHash, proposal.Revision.Content)
	if err != nil || proposal.ID != input.ProposalID || proposal.WorkspaceID != execution.WorkspaceID || proposal.WorkflowRunID == nil || *proposal.WorkflowRunID != execution.RunID || proposal.TargetPath != proposal.Revision.TargetPath || proposal.Revision.ID != input.RevisionID || proposal.Revision.ProposalID != proposal.ID || !strings.EqualFold(proposal.Revision.ChangeHash, input.ApprovedChangeHash) || !strings.EqualFold(proposal.Revision.ChangeHash, expectedChangeHash) || proposal.Approval == nil || proposal.Approval.ID == "" || proposal.Approval.ProposalID != proposal.ID || proposal.Approval.RevisionID != proposal.Revision.ID || proposal.Approval.Decision != domain.DecisionApproved || !strings.EqualFold(proposal.Approval.ChangeHash, input.ApprovedChangeHash) || proposal.Approval.ApprovedGitHead == nil || !domain.ValidGitHead(*proposal.Approval.ApprovedGitHead) {
		return bootstrapBindingConflict(domain.ErrWritebackIdentityConflict)
	}
	return nil
}

func validateBootstrapExecutionBinding(execution workflowapplication.ExecutionContext, input BootstrapInput, proposal domain.Proposal, durable domain.WritebackExecution, expectedKey string) error {
	if durable.ID == "" || durable.WorkspaceID != execution.WorkspaceID || durable.WorkflowRunID != execution.RunID || durable.NodeRunID != execution.NodeRunID || durable.ProposalID != input.ProposalID || durable.RevisionID != input.RevisionID || durable.ApprovalID != proposal.Approval.ID || durable.WriteAuthorizationID == "" || durable.GitAuthorizationID == "" || durable.WriteAuthorizationID == durable.GitAuthorizationID || durable.TargetPath != proposal.TargetPath || domain.NormalizeTargetMode(durable.TargetMode) != domain.NormalizeTargetMode(proposal.Revision.TargetMode) || !strings.EqualFold(durable.BaseHash, proposal.Revision.BaseHash) || !strings.EqualFold(durable.ResultHash, domain.ComputeWritebackResultHash([]byte(proposal.Revision.Content))) || !strings.EqualFold(durable.ApprovedChangeHash, input.ApprovedChangeHash) || !strings.EqualFold(durable.ApprovedGitHead, *proposal.Approval.ApprovedGitHead) || durable.IdempotencyKey != expectedKey {
		return bootstrapBindingConflict(domain.ErrWritebackIdentityConflict)
	}
	return nil
}

func authorizationGenerationKey(nodeRunID, generation foundation.ID, capability string) string {
	return fmt.Sprintf("safe-writeback-auth:%s:%s:%s", nodeRunID, generation, capability)
}

func bootstrapBindingConflict(cause error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, "WRITEBACK_EXECUTION_BINDING_CONFLICT", false, cause)
}

func hasFoundationCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}

func isNilBootstrapDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ workflowapplication.Executor = (*BootstrapExecutor)(nil)
