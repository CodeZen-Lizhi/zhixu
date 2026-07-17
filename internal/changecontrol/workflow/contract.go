package changecontrolworkflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	changecontrolapplication "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	// SafeWritebackDefinitionKey 是 Approval Dispatch 唯一允许引用的内置 Definition。
	SafeWritebackDefinitionKey = "change-control.safe-writeback"
	// SafeWritebackDefinitionVersion 固定当前 Safe Writeback Definition 版本。
	SafeWritebackDefinitionVersion int64 = 1
	// SafeWritebackNodeKey 是单节点 Definition 内的稳定逻辑节点键。
	SafeWritebackNodeKey = "safe-writeback"
	// SafeWritebackNodeKind 是 Executor Registry 的稳定解析键。
	SafeWritebackNodeKind = "change_control.safe_writeback"
	// SafeWritebackBootstrapInputSchemaVersion 是持久 Node input 契约版本。
	SafeWritebackBootstrapInputSchemaVersion = 1
	// SafeWritebackOutputSchemaVersion 是 verifying/index_pending 输出契约版本。
	SafeWritebackOutputSchemaVersion = changecontrolapplication.SafeWritebackSchemaVersion
	// SafeWritebackAuthorizationTTL 限制 Bootstrap 瞬时双授权的生命周期。
	SafeWritebackAuthorizationTTL = time.Minute
	// MaxSafeWritebackBootstrapInputBytes 限制持久 Node input，防止把正文或无界数据塞入 Workflow。
	MaxSafeWritebackBootstrapInputBytes = 1024
	safeWritebackGraphHash              = "4ef937969e5856c0efcf8ad9035f3fffe386d2f419e58443822a968d578172a6"
)

// BootstrapInput 是批准后持久化到 Workflow Node 的最小安全输入。
// Execution、路径、正文、Credential 与 Git 参数均由 Worker 重新加载或瞬时派生。
type BootstrapInput struct {
	SchemaVersion      int           `json:"schema_version"`
	ProposalID         foundation.ID `json:"proposal_id"`
	RevisionID         foundation.ID `json:"revision_id"`
	ApprovedChangeHash string        `json:"approved_change_hash"`
}

// RegisteredDefinition 返回固定、可复制且已带 canonical GraphHash 的 Safe Writeback Definition。
func RegisteredDefinition() workflowdomain.RegisteredDefinition {
	graph := workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key:                 SafeWritebackNodeKey,
		Kind:                SafeWritebackNodeKind,
		InputSchemaVersion:  SafeWritebackBootstrapInputSchemaVersion,
		OutputSchemaVersion: SafeWritebackOutputSchemaVersion,
		// M4-C 固定三次有界业务重试；进程崩溃仍由同一 River Job 做 transport redelivery。
		RetryPolicy:         workflowdomain.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 30 * time.Second},
		RequiredPermissions: []workflowdomain.Permission{workflowdomain.PermissionGitWrite, workflowdomain.PermissionWriteKnowledge},
	}}}
	return workflowdomain.RegisteredDefinition{
		Key: SafeWritebackDefinitionKey, Version: SafeWritebackDefinitionVersion,
		InputSchemaVersion: SafeWritebackBootstrapInputSchemaVersion,
		Graph:              graph, GraphHash: safeWritebackGraphHash,
	}
}

// EncodeBootstrapInput 校验并生成 Approval UoW 与 Bootstrap Executor 共用的 canonical JSON。
func EncodeBootstrapInput(proposalID, revisionID foundation.ID, approvedChangeHash string) (json.RawMessage, error) {
	parsedProposalID, err := foundation.ParseID(string(proposalID))
	if err != nil {
		return nil, workflowContractError(err)
	}
	parsedRevisionID, err := foundation.ParseID(string(revisionID))
	if err != nil {
		return nil, workflowContractError(err)
	}
	approvedChangeHash = strings.ToLower(strings.TrimSpace(approvedChangeHash))
	if !domain.ValidHash(approvedChangeHash) {
		return nil, workflowContractError(domain.ErrWritebackInvalidInput)
	}
	encoded, err := json.Marshal(BootstrapInput{
		SchemaVersion: SafeWritebackBootstrapInputSchemaVersion, ProposalID: parsedProposalID,
		RevisionID: parsedRevisionID, ApprovedChangeHash: approvedChangeHash,
	})
	if err != nil {
		return nil, foundation.NewError(foundation.ErrorNonRetryableFailure, "WRITEBACK_WORKFLOW_INPUT_ENCODING_FAILED", false, err)
	}
	return encoded, nil
}

// DecodeBootstrapInput 严格解析持久 Node input；未知字段、尾随 JSON 与非 canonical 身份均被拒绝。
func DecodeBootstrapInput(payload json.RawMessage) (BootstrapInput, error) {
	if len(payload) == 0 || len(payload) > MaxSafeWritebackBootstrapInputBytes {
		return BootstrapInput{}, workflowContractError(domain.ErrWritebackInvalidInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var input BootstrapInput
	if err := decoder.Decode(&input); err != nil {
		return BootstrapInput{}, workflowContractError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("bootstrap input contains multiple JSON values")
		}
		return BootstrapInput{}, workflowContractError(err)
	}
	proposalID, proposalErr := foundation.ParseID(string(input.ProposalID))
	revisionID, revisionErr := foundation.ParseID(string(input.RevisionID))
	if input.SchemaVersion != SafeWritebackBootstrapInputSchemaVersion || proposalErr != nil || revisionErr != nil || proposalID != input.ProposalID || revisionID != input.RevisionID || !domain.ValidHash(input.ApprovedChangeHash) || strings.ToLower(input.ApprovedChangeHash) != input.ApprovedChangeHash {
		return BootstrapInput{}, workflowContractError(domain.ErrWritebackInvalidInput)
	}
	return input, nil
}

// SafeWritebackExecutionKey 返回 Node Run 唯一且跨 delivery 稳定的 Atomic Begin 幂等键。
func SafeWritebackExecutionKey(nodeRunID foundation.ID) (string, error) {
	parsed, err := foundation.ParseID(string(nodeRunID))
	if err != nil || parsed != nodeRunID {
		return "", workflowContractError(errors.Join(err, domain.ErrWritebackInvalidInput))
	}
	return "safe-writeback:" + string(parsed), nil
}

func workflowContractError(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_WORKFLOW_CONTRACT_INVALID", false, cause)
}
