// Package postgres projects Authoring and Change Control facts onto Git history.
package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"

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
	snapshot, err := scanDocument(repository.db.QueryRow(ctx, getDocumentSQL, string(workspaceID), string(documentID)))
	if err != nil {
		return application.DocumentSnapshot{}, documentQueryError(err)
	}
	return snapshot, nil
}

// MapCommits batch-enriches only the requested current-page commits.
func (repository *Repository) MapCommits(ctx context.Context, workspaceID, documentID foundation.ID, targetPath string, commits []string) ([]domain.CommitMapping, error) {
	values, err := validateCommitMappingRequest(workspaceID, documentID, targetPath, commits)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return []domain.CommitMapping{}, nil
	}
	rows, err := repository.db.Query(ctx, commitMappingSQL, string(workspaceID), string(documentID), targetPath, values)
	if err != nil {
		return nil, classify(err, "DOCUMENT_HISTORY_COMMIT_MAPPING_QUERY_FAILED")
	}
	defer rows.Close()
	mappings := make([]domain.CommitMapping, 0, len(commits))
	for rows.Next() {
		mapping, managed, scanErr := scanCommitMapping(rows)
		if scanErr != nil {
			return nil, classify(scanErr, "DOCUMENT_HISTORY_COMMIT_MAPPING_SCAN_FAILED")
		}
		if managed {
			mappings = append(mappings, mapping)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "DOCUMENT_HISTORY_COMMIT_MAPPING_QUERY_FAILED")
	}
	return mappings, nil
}

func validateCommitMappingRequest(workspaceID, documentID foundation.ID, targetPath string, commits []string) ([]string, error) {
	if !domain.ValidID(workspaceID) || !domain.ValidID(documentID) || !domain.ValidPath(targetPath) || len(commits) > domain.MaxHistoryLimit {
		return nil, invalid("commit mapping request is invalid")
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
	return values, nil
}

func documentQueryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return foundation.NewError(foundation.ErrorNotFound, application.ErrorCodeNotFound, false, errors.New("document was not found"))
	}
	return classify(err, "DOCUMENT_HISTORY_DOCUMENT_QUERY_FAILED")
}

func dependencyUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeGitUnavailable, true, err)
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
