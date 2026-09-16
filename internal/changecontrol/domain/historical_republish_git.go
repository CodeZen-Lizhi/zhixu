package domain

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// HistoricalRepublishGitAuthority 由发布所有者根据其
// 不可变回执和精确获批的执行记录重建，绝不来自请求。
// 它仅授权对该执行及目标进行字节不变的提交。
type HistoricalRepublishGitAuthority struct {
	ReceiptID, WorkspaceID, ExecutionID      foundation.ID
	ProposalID, RevisionID, ApprovalID       foundation.ID
	WorkflowRunID, NodeRunID                 foundation.ID
	TargetPath, ApprovedGitHead, ContentHash string
}
type historicalRepublishGitKey struct{}

func WithHistoricalRepublishGitAuthority(ctx context.Context, a HistoricalRepublishGitAuthority) (context.Context, error) {
	if !validGitID(a.ReceiptID) || !validGitCommitIdentity(a.WorkspaceID, a.WorkflowRunID, a.NodeRunID, a.ExecutionID, a.ProposalID, a.RevisionID, a.ApprovalID) || validateGitTargetPath(a.TargetPath) != nil || !ValidGitHead(a.ApprovedGitHead) || !ValidHash(a.ContentHash) {
		return nil, ErrGitInvalidInput
	}
	return context.WithValue(ctx, historicalRepublishGitKey{}, a), nil
}
func HistoricalRepublishGitAuthorityFromContext(ctx context.Context) (HistoricalRepublishGitAuthority, bool) {
	a, ok := ctx.Value(historicalRepublishGitKey{}).(HistoricalRepublishGitAuthority)
	return a, ok
}
func (a HistoricalRepublishGitAuthority) MatchesLookup(l GitCommitLookup) bool {
	return l.Operation == GitOperationApply && NormalizeTargetMode(l.TargetMode) == TargetModeReplace && a.WorkspaceID == l.WorkspaceID && a.ExecutionID == l.WritebackExecutionID && a.ProposalID == l.ProposalID && a.RevisionID == l.RevisionID && a.ApprovalID == l.ApprovalID && a.WorkflowRunID == l.WorkflowRunID && a.NodeRunID == l.NodeRunID && a.TargetPath == l.TargetPath && a.ApprovedGitHead == l.ApprovedGitHead && a.ContentHash == l.ResultHash && l.BaseBlobID == l.ResultBlobID
}
