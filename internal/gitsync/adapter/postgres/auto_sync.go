package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
)

// ListAutoSyncCandidates returns completed approved writebacks whose effective
// config at completion and current config both opt in to automatic sync.
func (repository *Repository) ListAutoSyncCandidates(ctx context.Context, limit int) ([]application.AutoSyncCandidate, error) {
	if repository == nil || nilInterface(repository.db) || limit < 1 || limit > application.MaxAutoSyncBatch {
		return nil, invalid("Git automatic sync candidate query is invalid")
	}
	rows, err := repository.db.Query(ctx, `
			SELECT commit_mapping.workspace_id::text,commit_mapping.id::text,current_config.revision
		FROM change_control.proposal_commit AS commit_mapping
		JOIN change_control.writeback_execution AS execution
		  ON execution.id=commit_mapping.writeback_execution_id
		 AND execution.workspace_id=commit_mapping.workspace_id
		 AND execution.status='completed'
		 AND execution.completed_at IS NOT NULL
		JOIN LATERAL (
			SELECT revision.configured,revision.auto_sync,revision.token_configured
			FROM ops.git_remote_config_revision AS revision
			WHERE revision.workspace_id=commit_mapping.workspace_id
			  AND revision.created_at<=execution.completed_at
			ORDER BY revision.revision DESC
			LIMIT 1
		) AS effective ON effective.configured AND effective.auto_sync AND effective.token_configured
		JOIN ops.git_remote_config AS current_config
		  ON current_config.workspace_id=commit_mapping.workspace_id
		 AND current_config.configured AND current_config.auto_sync AND current_config.token_configured
		WHERE NOT EXISTS (
			SELECT 1 FROM ops.git_sync_run AS run
			WHERE run.workspace_id=commit_mapping.workspace_id
				  AND run.idempotency_key=$1 || commit_mapping.id::text || ':config:' || current_config.revision::text
		)
		ORDER BY execution.completed_at,commit_mapping.id
		LIMIT $2`, autoSyncKeyPrefix, limit)
	if err != nil {
		return nil, classify(err, domain.ErrorCodeUnavailable)
	}
	defer rows.Close()
	candidates := make([]application.AutoSyncCandidate, 0, limit)
	for rows.Next() {
		var workspaceID, commitID string
		var configRevision int64
		if err := rows.Scan(&workspaceID, &commitID, &configRevision); err != nil {
			return nil, classify(err, domain.ErrorCodeUnavailable)
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
		return nil, classify(err, domain.ErrorCodeUnavailable)
	}
	return candidates, nil
}

const autoSyncKeyPrefix = "auto-writeback:"
