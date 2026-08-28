package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

const gormAutoSyncKeyPrefix = "auto-writeback:"

// ListAutoSyncCandidates returns bounded completed writebacks whose effective
// and current configuration both permit automatic synchronization.
func (repository *GORMRepository) ListAutoSyncCandidates(ctx context.Context, limit int) (candidates []application.AutoSyncCandidate, returnErr error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > application.MaxAutoSyncBatch {
		return nil, invalid("Git automatic sync candidate query is invalid")
	}
	rows, err := gormRawRows(ctx, repository.database, gormAutoSyncSQL, gormAutoSyncKeyPrefix, limit)
	if err != nil {
		return nil, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && returnErr == nil {
			candidates = nil
			returnErr = classifyGORM(ctx, closeErr, domain.ErrorCodeUnavailable)
		}
	}()
	candidates = make([]application.AutoSyncCandidate, 0, limit)
	for rows.Next() {
		var workspaceID, commitID string
		var configRevision int64
		if err := rows.Scan(&workspaceID, &commitID, &configRevision); err != nil {
			return nil, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
		}
		workspace, workspaceErr := foundation.ParseID(workspaceID)
		commit, commitErr := foundation.ParseID(commitID)
		if workspaceErr != nil || commitErr != nil || configRevision < 1 {
			return nil, corrupt("persisted Git automatic sync candidate is invalid")
		}
		candidates = append(candidates, application.AutoSyncCandidate{
			WorkspaceID: workspace, ProposalCommitID: commit, ConfigRevision: configRevision,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return candidates, nil
}
