package changecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

var (
	applyApprovedPatchRef = toolsdomain.ToolRef{Name: "ApplyApprovedPatch", Version: 1}
	createGitCommitRef    = toolsdomain.ToolRef{Name: "CreateGitCommit", Version: 1}
)

// WritebackAuditRecorder 将既有 Safe Writeback 检查点映射为两个固定逻辑 Tool Call，不执行文件或 Git 操作。
type WritebackAuditRecorder struct {
	service *toolsapplication.TrustedWriteAuditService
}

// NewWritebackAuditRecorder 创建只读 receipt/审计桥；真实副作用仍只由 Change Control Saga 执行。
func NewWritebackAuditRecorder(service *toolsapplication.TrustedWriteAuditService) (*WritebackAuditRecorder, error) {
	if service == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TOOL_AUDIT_UNAVAILABLE", false, errors.New("trusted write audit service is nil"))
	}
	return &WritebackAuditRecorder{service: service}, nil
}

// EnsureStarted 在副作用前创建或重放固定 Tool Call。
func (recorder *WritebackAuditRecorder) EnsureStarted(ctx context.Context, identity changecontrolapplication.WritebackResumeIdentity, execution changecontroldomain.WritebackExecution, step changecontrolapplication.WritebackAuditStep) error {
	if recorder == nil || recorder.service == nil {
		return auditUnavailable()
	}
	command, err := trustedWriteCommand(identity, execution, step)
	if err != nil {
		return err
	}
	_, err = recorder.service.EnsureStarted(ctx, command)
	return err
}

// RequireStarted 要求副作用发生前已经存在完整绑定的 STARTED/SUCCEEDED。
func (recorder *WritebackAuditRecorder) RequireStarted(ctx context.Context, identity changecontrolapplication.WritebackResumeIdentity, execution changecontroldomain.WritebackExecution, step changecontrolapplication.WritebackAuditStep) error {
	if recorder == nil || recorder.service == nil {
		return auditUnavailable()
	}
	command, err := trustedWriteCommand(identity, execution, step)
	if err != nil {
		return err
	}
	_, err = recorder.service.RequireStarted(ctx, command)
	return err
}

// RecordSucceeded 根据 file_applied 或 git_committed 权威检查点归约逻辑 Tool Call。
func (recorder *WritebackAuditRecorder) RecordSucceeded(ctx context.Context, identity changecontrolapplication.WritebackResumeIdentity, execution changecontroldomain.WritebackExecution, step changecontrolapplication.WritebackAuditStep) error {
	if recorder == nil || recorder.service == nil {
		return auditUnavailable()
	}
	command, err := trustedWriteCommand(identity, execution, step)
	if err != nil {
		return err
	}
	output, err := trustedWriteOutput(execution, step)
	if err != nil {
		return err
	}
	_, err = recorder.service.RecordSucceeded(ctx, toolsapplication.TrustedWriteAuditReceipt{Command: command, Output: output})
	return err
}

func trustedWriteCommand(identity changecontrolapplication.WritebackResumeIdentity, execution changecontroldomain.WritebackExecution, step changecontrolapplication.WritebackAuditStep) (toolsapplication.TrustedWriteAuditCommand, error) {
	if identity.Validate() != nil || execution.ID == "" || execution.WorkspaceID != identity.WorkspaceID || execution.WorkflowRunID != identity.WorkflowRunID || execution.NodeRunID != identity.NodeRunID {
		return toolsapplication.TrustedWriteAuditCommand{}, auditInvalid(changecontroldomain.ErrWritebackIdentityConflict)
	}
	arguments, err := json.Marshal(struct {
		WritebackExecutionID foundation.ID `json:"writeback_execution_id"`
	}{WritebackExecutionID: execution.ID})
	if err != nil {
		return toolsapplication.TrustedWriteAuditCommand{}, auditInvalid(err)
	}
	toolIdentity := toolsdomain.TrustedExecutionIdentity{
		WorkspaceID: identity.WorkspaceID, DefinitionID: identity.DefinitionID,
		DefinitionVersion: identity.DefinitionVersion, DefinitionHash: identity.DefinitionHash,
		WorkflowRunID: identity.WorkflowRunID, NodeKey: identity.NodeKey, NodeRunID: identity.NodeRunID,
		NodeAttemptID: identity.NodeAttemptID, LeaseOwner: identity.LeaseOwner, LeaseFence: identity.LeaseFence,
	}
	command := toolsapplication.TrustedWriteAuditCommand{
		Identity: toolIdentity, Arguments: arguments, WritebackReceipt: execution.ID,
	}
	switch step {
	case changecontrolapplication.WritebackAuditApply:
		command.Tool = applyApprovedPatchRef
		command.CallNo = 1
		command.IdempotencyKey = fmt.Sprintf("safe-writeback-tool:%s:apply:v1", execution.ID)
	case changecontrolapplication.WritebackAuditGit:
		command.Tool = createGitCommitRef
		command.CallNo = 2
		command.IdempotencyKey = fmt.Sprintf("safe-writeback-tool:%s:git:v1", execution.ID)
	default:
		return toolsapplication.TrustedWriteAuditCommand{}, auditInvalid(errors.New("writeback audit step is invalid"))
	}
	return command, nil
}

func trustedWriteOutput(execution changecontroldomain.WritebackExecution, step changecontrolapplication.WritebackAuditStep) (json.RawMessage, error) {
	resultRef := "writeback-execution:" + string(execution.ID)
	var value any
	switch step {
	case changecontrolapplication.WritebackAuditApply:
		if !applyReceiptAvailable(execution.Status) || !changecontroldomain.ValidHash(execution.ResultHash) {
			return nil, auditInvalid(errors.New("file_applied receipt is unavailable"))
		}
		value = struct {
			WritebackExecutionID foundation.ID `json:"writeback_execution_id"`
			ResultRef            string        `json:"result_ref"`
			ResultHash           string        `json:"result_hash"`
			Status               string        `json:"status"`
		}{execution.ID, resultRef, strings.ToLower(execution.ResultHash), "APPLIED"}
	case changecontrolapplication.WritebackAuditGit:
		if !gitReceiptAvailable(execution.Status) || !changecontroldomain.ValidGitHead(execution.GitCommit) {
			return nil, auditInvalid(errors.New("git_committed receipt is unavailable"))
		}
		value = struct {
			WritebackExecutionID foundation.ID `json:"writeback_execution_id"`
			ResultRef            string        `json:"result_ref"`
			CommitOID            string        `json:"commit_oid"`
			Status               string        `json:"status"`
		}{execution.ID, resultRef, strings.ToLower(execution.GitCommit), "COMMITTED"}
	default:
		return nil, auditInvalid(errors.New("writeback audit step is invalid"))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, auditInvalid(err)
	}
	return encoded, nil
}

func applyReceiptAvailable(status changecontroldomain.WritebackStatus) bool {
	switch status {
	case changecontroldomain.WritebackStatusFileApplied, changecontroldomain.WritebackStatusGitPrepared,
		changecontroldomain.WritebackStatusGitCommitted, changecontroldomain.WritebackStatusPublishRecovery,
		changecontroldomain.WritebackStatusVerifying, changecontroldomain.WritebackStatusCompensatingFile,
		changecontroldomain.WritebackStatusCompensated, changecontroldomain.WritebackStatusManualRecovery:
		return true
	default:
		return false
	}
}

func gitReceiptAvailable(status changecontroldomain.WritebackStatus) bool {
	switch status {
	case changecontroldomain.WritebackStatusGitCommitted, changecontroldomain.WritebackStatusPublishRecovery, changecontroldomain.WritebackStatusVerifying:
		return true
	default:
		return false
	}
}

func auditUnavailable() error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TOOL_AUDIT_UNAVAILABLE", false, errors.New("writeback tool audit recorder is unavailable"))
}

func auditInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_TOOL_AUDIT_RECEIPT_INVALID", false, cause)
}

var _ changecontrolapplication.WritebackAuditRecorder = (*WritebackAuditRecorder)(nil)
