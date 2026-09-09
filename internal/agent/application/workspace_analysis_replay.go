package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ScopedWorkspaceAnalysisRunReplayService reads the immutable dispatch binding
// without requiring a current model, budget configuration or Worker capability.
type ScopedWorkspaceAnalysisRunReplayService struct {
	repository ScopedWorkspaceAnalysisRunPersistence
}

// NewScopedWorkspaceAnalysisRunReplayService constructs a read-only replay owner.
func NewScopedWorkspaceAnalysisRunReplayService(repository ScopedWorkspaceAnalysisRunPersistence) (*ScopedWorkspaceAnalysisRunReplayService, error) {
	if isNilPort(repository) {
		return nil, workspaceAnalysisRunStartError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisRunStartInvalid, false,
			errors.New("workspace analysis replay persistence is unavailable"))
	}
	return &ScopedWorkspaceAnalysisRunReplayService{repository: repository}, nil
}

// StartWorkspaceAnalysisRunScoped rejects new starts and verifies the stored binding.
func (service *ScopedWorkspaceAnalysisRunReplayService) StartWorkspaceAnalysisRunScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	command WorkspaceAnalysisRunStartCommand,
) (domain.WorkspaceAnalysisRun, error) {
	if service == nil || isNilPort(service.repository) || isNilPort(scope) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorDependencyUnavailable, ErrorCodeWorkspaceAnalysisRunStartUnavailable, true,
			errors.New("workspace analysis replay service or transaction scope is unavailable"))
	}
	if ctx == nil || !command.Replayed || !validWorkspaceAnalysisRunStartCommand(command) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorInvalidInput, ErrorCodeWorkspaceAnalysisRunStartInvalid, false,
			errors.New("workspace analysis replay binding is invalid"))
	}
	if err := ctx.Err(); err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	existing, found, err := service.repository.FindWorkspaceAnalysisRunScoped(ctx, scope, command.WorkspaceID, command.QuestionID)
	if err != nil {
		return domain.WorkspaceAnalysisRun{}, err
	}
	if !found || domain.ValidateWorkspaceAnalysisRun(existing) != nil || !sameWorkspaceAnalysisRunDispatchBinding(existing, command) {
		return domain.WorkspaceAnalysisRun{}, workspaceAnalysisRunStartError(foundation.ErrorConsistencyViolation, ErrorCodeWorkspaceAnalysisRunStartConflict, false,
			errors.New("replayed workspace analysis run is missing or drifted"))
	}
	return existing, nil
}

var _ ScopedWorkspaceAnalysisRunStarter = (*ScopedWorkspaceAnalysisRunReplayService)(nil)
