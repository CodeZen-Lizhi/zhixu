package postgres

import (
	"context"
	"errors"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// GORMRepository reads Document History projections through the shared GORM
// root.
type GORMRepository struct {
	database *gorm.DB
}

// NewGORMRepository creates a Document History projection repository.
func NewGORMRepository(database *gorm.DB) (*GORMRepository, error) {
	if !validGORMDatabase(database) {
		return nil, dependencyUnavailable(errors.New("document history GORM database is unavailable"))
	}
	return &GORMRepository{database: database}, nil
}

// GetDocument reads one exact Workspace/Document identity.
func (repository *GORMRepository) GetDocument(ctx context.Context, workspaceID, documentID foundation.ID) (application.DocumentSnapshot, error) {
	if err := repository.ready(ctx); err != nil {
		return application.DocumentSnapshot{}, err
	}
	if !domain.ValidID(workspaceID) || !domain.ValidID(documentID) {
		return application.DocumentSnapshot{}, invalid("document identity is invalid")
	}
	row := repository.database.WithContext(ctx).Raw(
		gormGetDocumentSQL, string(workspaceID), string(documentID),
	).Row()
	if row == nil {
		return application.DocumentSnapshot{}, classify(errors.New("document history document query is unavailable"), "DOCUMENT_HISTORY_DOCUMENT_QUERY_FAILED")
	}
	snapshot, err := scanDocument(row)
	if err != nil {
		return application.DocumentSnapshot{}, documentQueryError(err)
	}
	return snapshot, nil
}

// MapCommits batch-enriches only the requested current-page commits.
func (repository *GORMRepository) MapCommits(ctx context.Context, workspaceID, documentID foundation.ID, targetPath string, commits []string) ([]domain.CommitMapping, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	values, err := validateCommitMappingRequest(workspaceID, documentID, targetPath, commits)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return []domain.CommitMapping{}, nil
	}
	rows, err := repository.database.WithContext(ctx).Raw(
		gormCommitMappingSQL,
		pq.Array(values),
		string(workspaceID), string(documentID),
		string(workspaceID), string(documentID), targetPath,
		string(workspaceID), targetPath,
	).Rows()
	if err != nil {
		return nil, classify(err, "DOCUMENT_HISTORY_COMMIT_MAPPING_QUERY_FAILED")
	}
	if rows == nil {
		return nil, classify(errors.New("document history commit mapping query is unavailable"), "DOCUMENT_HISTORY_COMMIT_MAPPING_QUERY_FAILED")
	}
	defer rows.Close()

	mappings := make([]domain.CommitMapping, 0, len(values))
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

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || ctx == nil || !validGORMDatabase(repository.database) {
		return dependencyUnavailable(errors.New("document history GORM repository is unavailable"))
	}
	return nil
}

func validGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

var _ application.DocumentReader = (*GORMRepository)(nil)
