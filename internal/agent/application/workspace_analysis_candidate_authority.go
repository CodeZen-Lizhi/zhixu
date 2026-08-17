package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisCandidateAuthorityQuery 精确定位一个已完成 Synthesis 的不可变候选。
type WorkspaceAnalysisCandidateAuthorityQuery struct {
	WorkspaceID   foundation.ID
	WorkflowRunID foundation.ID
	AnalysisRunID foundation.ID
	CandidateID   foundation.ID
	CandidateHash string
}

// Validate 拒绝缺失、复用或非 canonical 的候选 authority 查询。
func (query WorkspaceAnalysisCandidateAuthorityQuery) Validate() error {
	if err := validateWorkspaceAnalysisModelQueryIDs(
		query.WorkspaceID, query.WorkflowRunID, query.AnalysisRunID, query.CandidateID,
	); err != nil || !canonicalWorkspaceAnalysisSHA256(query.CandidateHash) {
		return workspaceAnalysisModelError(errors.New("workspace analysis candidate authority query is invalid"))
	}
	return nil
}

// WorkspaceAnalysisCandidateAuthorityReader 只返回已与 Synthesis Operation、预算、Model Run 和 Call 闭合的候选。
type WorkspaceAnalysisCandidateAuthorityReader interface {
	LoadWorkspaceAnalysisCandidateAuthority(
		context.Context,
		WorkspaceAnalysisCandidateAuthorityQuery,
	) (domain.WorkspaceAnalysisCandidate, error)
}
