package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// RelationApplySchemaVersion 是 Approval 到 Knowledge 的固定命令契约版本。
	RelationApplySchemaVersion = "knowledge-relation-apply/v1"
	// RelationApplyIdempotencyPrefix 是 Knowledge receipt 使用的稳定前缀。
	RelationApplyIdempotencyPrefix = "knowledge-relation-approval/v1:"
)

// ApprovedRelationApplyCommand 将一次已批准的 typed Proposal 绑定到 Knowledge apply。
// Proposal、Revision 和 Approval 必须属于同一 Workspace；调用方不得传入关系正文，
// adapter 会在数据库事务内从不可变 Revision 重新读取并校验正文。
type ApprovedRelationApplyCommand struct {
	WorkspaceID foundation.ID
	ProposalID  foundation.ID
	RevisionID  foundation.ID
	ApprovalID  foundation.ID
}

// ApprovedRelationApplyResult 返回正式 Relation 事实及 Proposal 的应用状态。
// Candidate 本身不会在 apply 阶段变成 Relation，也不会生成文件写回执行记录。
type ApprovedRelationApplyResult struct {
	Relation        domain.Relation
	Evidence        []domain.RelationEvidence
	ProposalStatus  changecontroldomain.ProposalStatus
	ProposalVersion int64
	Replayed        bool
}

// ApprovedRelationApplyPort 是唯一的 Approval 到 Knowledge 正式关系写入 seam。
type ApprovedRelationApplyPort interface {
	ApplyApprovedRelation(context.Context, ApprovedRelationApplyCommand) (ApprovedRelationApplyResult, error)
}

// ValidateApprovedRelationApplyCommand 校验跨模块身份边界。
func ValidateApprovedRelationApplyCommand(command ApprovedRelationApplyCommand) error {
	if !validApplyID(command.WorkspaceID) || !validApplyID(command.ProposalID) ||
		!validApplyID(command.RevisionID) || !validApplyID(command.ApprovalID) {
		return foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_APPLY_INVALID", false, errors.New("relation apply identity is invalid"))
	}
	return nil
}

// RelationApplyIdempotencyKey 返回绑定 Approval ID 的不可变 receipt key。
func RelationApplyIdempotencyKey(approvalID foundation.ID) string {
	return RelationApplyIdempotencyPrefix + string(approvalID)
}

// RelationApplyRequestHash 计算完整命令绑定的稳定请求哈希。
func RelationApplyRequestHash(command ApprovedRelationApplyCommand) (string, error) {
	if err := ValidateApprovedRelationApplyCommand(command); err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion string        `json:"schema_version"`
		WorkspaceID   foundation.ID `json:"workspace_id"`
		ProposalID    foundation.ID `json:"proposal_id"`
		RevisionID    foundation.ID `json:"revision_id"`
		ApprovalID    foundation.ID `json:"approval_id"`
	}{
		SchemaVersion: RelationApplySchemaVersion,
		WorkspaceID:   command.WorkspaceID,
		ProposalID:    command.ProposalID,
		RevisionID:    command.RevisionID,
		ApprovalID:    command.ApprovalID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validApplyID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(strings.TrimSpace(string(value)))
	return err == nil && parsed == value
}
