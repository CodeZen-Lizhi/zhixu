package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisCandidateDraft 是一次 Synthesis Provider stream 的临时安全投影。
// Candidate 持久化不由该接口拥有，Completed/Degraded 也不代表 Answer 已发布。
type WorkspaceAnalysisCandidateDraft interface {
	WorkspaceAnalysisCandidateStreamSink
	Session() DraftStreamSession
	Complete(context.Context) (DraftStreamSession, error)
	Abort(context.Context) error
}

// WorkspaceAnalysisCandidateDraftQuery 只按 Candidate 冻结的首次 Attempt 加载旧 Draft。
type WorkspaceAnalysisCandidateDraftQuery struct {
	WorkspaceID   foundation.ID
	AnswerID      foundation.ID
	NodeAttemptID foundation.ID
}

// Validate 拒绝缺失、复用或非 canonical 的 Draft 查询身份。
func (query WorkspaceAnalysisCandidateDraftQuery) Validate() error {
	ids := []foundation.ID{query.WorkspaceID, query.AnswerID, query.NodeAttemptID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		if !canonicalApplicationID(id) {
			return workspaceAnalysisModelError(errors.New("workspace analysis candidate draft query id is invalid"))
		}
		if _, duplicate := seen[id]; duplicate {
			return workspaceAnalysisModelError(errors.New("workspace analysis candidate draft query id is reused"))
		}
		seen[id] = struct{}{}
	}
	return nil
}

// WorkspaceAnalysisCandidateDraftCoordinator 只在首次授权后创建 Draft，并在候选重放时读取旧终态。
type WorkspaceAnalysisCandidateDraftCoordinator interface {
	Begin(context.Context, DraftStreamBinding) (WorkspaceAnalysisCandidateDraft, error)
	Load(context.Context, WorkspaceAnalysisCandidateDraftQuery) (DraftStreamSession, error)
}

// WorkspaceAnalysisCandidateDraftSessionLoader 是 PostgreSQL 对旧 Candidate Draft 的窄读取边界。
type WorkspaceAnalysisCandidateDraftSessionLoader interface {
	LoadWorkspaceAnalysisCandidateDraftSession(context.Context, WorkspaceAnalysisCandidateDraftQuery) (DraftStreamSession, error)
}
