package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
)

// The source guards run before entering command/replay transactions. The existing
// review shell, question and path-step triggers make these source bindings immutable.
// ClaimOnly is never stored in request hashes, receipts or completion reservations.
func (repository *GORMRepository) requireClaimSession(ctx context.Context, workspaceID, sessionID foundation.ID) error {
	session, err := gormInterviewLoadSession(ctx, repository.database, workspaceID, sessionID, false)
	if err != nil {
		return err
	}
	return interviewapp.RequireClaimSessionSource(session)
}

func (repository *GORMRepository) requireClaimPath(ctx context.Context, workspaceID, pathID foundation.ID) error {
	path, err := repository.GetPath(ctx, workspaceID, pathID)
	if err != nil {
		return err
	}
	return interviewapp.RequireClaimPathSources(path)
}
