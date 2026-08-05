// Package postgres projects Authoring and Change Control facts onto Git history.
package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// DB is the minimal PostgreSQL read boundary needed by Document History.
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// Repository reads existing Authoring and Change Control facts without owning them.
type Repository struct{ db DB }

// NewRepository creates a Document History PostgreSQL projection repository.
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeGitUnavailable, true, errors.New("document history database is required"))
	}
	return &Repository{db: db}, nil
}

// GetDocument reads one exact Workspace/Document identity.
func (repository *Repository) GetDocument(ctx context.Context, workspaceID, documentID foundation.ID) (application.DocumentSnapshot, error) {
	if !domain.ValidID(workspaceID) || !domain.ValidID(documentID) {
		return application.DocumentSnapshot{}, invalid("document identity is invalid")
	}
	var snapshot application.DocumentSnapshot
	err := repository.db.QueryRow(ctx, `SELECT
		id::text,workspace_id::text,canonical_path,title,lifecycle_status,
		COALESCE(current_published_revision_id::text,''),version
		FROM core.document
		WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(documentID)).Scan(
		&snapshot.ID, &snapshot.WorkspaceID, &snapshot.CanonicalPath, &snapshot.Title,
		&snapshot.Lifecycle, &snapshot.CurrentPublishedRevisionID, &snapshot.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DocumentSnapshot{}, foundation.NewError(foundation.ErrorNotFound, application.ErrorCodeNotFound, false, errors.New("document was not found"))
	}
	if err != nil {
		return application.DocumentSnapshot{}, classify(err, "DOCUMENT_HISTORY_DOCUMENT_QUERY_FAILED")
	}
	return snapshot, nil
}

// MapCommits batch-enriches only the requested current-page commits.
func (repository *Repository) MapCommits(ctx context.Context, workspaceID, documentID foundation.ID, targetPath string, commits []string) ([]domain.CommitMapping, error) {
	if !domain.ValidID(workspaceID) || !domain.ValidID(documentID) || !domain.ValidPath(targetPath) || len(commits) > domain.MaxHistoryLimit {
		return nil, invalid("commit mapping request is invalid")
	}
	if len(commits) == 0 {
		return []domain.CommitMapping{}, nil
	}
	values := make([]string, len(commits))
	seen := make(map[string]struct{}, len(commits))
	for index, commit := range commits {
		if !domain.ValidObjectID(commit) {
			return nil, invalid("commit mapping contains an invalid object id")
		}
		if _, duplicate := seen[commit]; duplicate {
			return nil, invalid("commit mapping contains duplicate object ids")
		}
		seen[commit] = struct{}{}
		values[index] = commit
	}
	rows, err := repository.db.Query(ctx, `WITH requested(git_commit,ordinality) AS (
		SELECT git_commit,ordinality
		FROM unnest($4::text[]) WITH ORDINALITY AS input(git_commit,ordinality)
	), revision_mapping AS (
		SELECT requested.ordinality,requested.git_commit,revision.id,revision.revision_no
		FROM requested
		JOIN core.article_revision AS revision
		  ON revision.workspace_id=$1 AND revision.document_id=$2
		 AND revision.git_commit=requested.git_commit
		UNION
		SELECT requested.ordinality,requested.git_commit,revision.id,revision.revision_no
		FROM requested
		JOIN authoring.document_publication_binding AS binding
		  ON binding.workspace_id=$1 AND binding.document_id=$2
		 AND binding.target_path=$3 AND binding.git_commit=requested.git_commit
		JOIN core.article_revision AS revision
		  ON revision.workspace_id=binding.workspace_id
		 AND revision.document_id=binding.document_id
		 AND revision.id=binding.article_revision_id
	), proposal_mapping AS (
		SELECT requested.ordinality,requested.git_commit,
		       proposal_commit.proposal_id,proposal_commit.revision_id,
		       proposal_commit.approval_id,proposal.workflow_run_id,
		       proposal_commit.writeback_execution_id,proposal.proposal_type,
		       approval.decided_at
		FROM requested
		JOIN change_control.proposal_commit AS proposal_commit
		  ON proposal_commit.workspace_id=$1
		 AND proposal_commit.target_path=$3
		 AND proposal_commit.git_commit=requested.git_commit
		JOIN change_control.proposal AS proposal
		  ON proposal.id=proposal_commit.proposal_id
		 AND proposal.workspace_id=proposal_commit.workspace_id
		JOIN change_control.approval AS approval
		  ON approval.id=proposal_commit.approval_id
		 AND approval.proposal_id=proposal_commit.proposal_id
		 AND approval.revision_id=proposal_commit.revision_id
	)
	SELECT requested.git_commit,
	       COALESCE(revision_mapping.id::text,''),COALESCE(revision_mapping.revision_no,0),
	       COALESCE(proposal_mapping.proposal_id::text,''),COALESCE(proposal_mapping.revision_id::text,''),
	       COALESCE(proposal_mapping.approval_id::text,''),COALESCE(proposal_mapping.workflow_run_id::text,''),
	       COALESCE(proposal_mapping.writeback_execution_id::text,''),COALESCE(proposal_mapping.proposal_type,''),
	       proposal_mapping.decided_at
	FROM requested
	LEFT JOIN revision_mapping USING(ordinality,git_commit)
	LEFT JOIN proposal_mapping USING(ordinality,git_commit)
	ORDER BY requested.ordinality`, string(workspaceID), string(documentID), targetPath, values)
	if err != nil {
		return nil, classify(err, "DOCUMENT_HISTORY_COMMIT_MAPPING_QUERY_FAILED")
	}
	defer rows.Close()
	mappings := make([]domain.CommitMapping, 0, len(commits))
	for rows.Next() {
		var mapping domain.CommitMapping
		var articleRevisionID, proposalID, proposalRevisionID, approvalID string
		var workflowRunID, writebackID, proposalType string
		var articleRevisionNo int
		var decidedAt sql.NullTime
		if err := rows.Scan(
			&mapping.GitCommit, &articleRevisionID, &articleRevisionNo,
			&proposalID, &proposalRevisionID, &approvalID, &workflowRunID,
			&writebackID, &proposalType, &decidedAt,
		); err != nil {
			return nil, classify(err, "DOCUMENT_HISTORY_COMMIT_MAPPING_SCAN_FAILED")
		}
		if articleRevisionID == "" && proposalID == "" {
			continue
		}
		mapping.ArticleRevisionID = foundation.ID(articleRevisionID)
		mapping.ArticleRevisionNo = articleRevisionNo
		mapping.ProposalID = foundation.ID(proposalID)
		mapping.ProposalRevisionID = foundation.ID(proposalRevisionID)
		mapping.ApprovalID = foundation.ID(approvalID)
		mapping.WorkflowRunID = foundation.ID(workflowRunID)
		mapping.WritebackID = foundation.ID(writebackID)
		mapping.ProposalType = proposalType
		if decidedAt.Valid {
			value := decidedAt.Time.UTC()
			mapping.ApprovalDecidedAt = &value
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "DOCUMENT_HISTORY_COMMIT_MAPPING_QUERY_FAILED")
	}
	return mappings, nil
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeInvalid, false, errors.New(message))
}

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}

var _ application.DocumentReader = (*Repository)(nil)
