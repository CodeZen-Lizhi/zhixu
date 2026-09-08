package postgres

import (
	"database/sql"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type rowScanner interface {
	Scan(...any) error
}

type documentRecord struct {
	ID                         string
	WorkspaceID                string
	CanonicalPath              string
	Title                      string
	Lifecycle                  string
	CurrentPublishedRevisionID string
	Version                    int64
}

func scanDocument(row rowScanner) (application.DocumentSnapshot, error) {
	var record documentRecord
	if err := row.Scan(
		&record.ID, &record.WorkspaceID, &record.CanonicalPath, &record.Title,
		&record.Lifecycle, &record.CurrentPublishedRevisionID, &record.Version,
	); err != nil {
		return application.DocumentSnapshot{}, err
	}
	return record.toApplication(), nil
}

func (record documentRecord) toApplication() application.DocumentSnapshot {
	return application.DocumentSnapshot{
		ID:                         foundation.ID(record.ID),
		WorkspaceID:                foundation.ID(record.WorkspaceID),
		CanonicalPath:              record.CanonicalPath,
		Title:                      record.Title,
		Lifecycle:                  record.Lifecycle,
		CurrentPublishedRevisionID: foundation.ID(record.CurrentPublishedRevisionID),
		Version:                    record.Version,
	}
}

type commitMappingRecord struct {
	GitCommit          string
	ArticleRevisionID  string
	ArticleRevisionNo  int
	ProposalID         string
	ProposalRevisionID string
	ApprovalID         string
	WorkflowRunID      string
	WritebackID        string
	ProposalType       string
	ApprovalDecidedAt  sql.NullTime
}

func scanCommitMapping(row rowScanner) (domain.CommitMapping, bool, error) {
	var record commitMappingRecord
	if err := row.Scan(
		&record.GitCommit, &record.ArticleRevisionID, &record.ArticleRevisionNo,
		&record.ProposalID, &record.ProposalRevisionID, &record.ApprovalID,
		&record.WorkflowRunID, &record.WritebackID, &record.ProposalType,
		&record.ApprovalDecidedAt,
	); err != nil {
		return domain.CommitMapping{}, false, err
	}
	if record.ArticleRevisionID == "" && record.ProposalID == "" {
		return domain.CommitMapping{}, false, nil
	}
	return record.toDomain(), true, nil
}

func (record commitMappingRecord) toDomain() domain.CommitMapping {
	mapping := domain.CommitMapping{
		GitCommit:          record.GitCommit,
		ArticleRevisionID:  foundation.ID(record.ArticleRevisionID),
		ArticleRevisionNo:  record.ArticleRevisionNo,
		ProposalID:         foundation.ID(record.ProposalID),
		ProposalRevisionID: foundation.ID(record.ProposalRevisionID),
		ApprovalID:         foundation.ID(record.ApprovalID),
		WorkflowRunID:      foundation.ID(record.WorkflowRunID),
		WritebackID:        foundation.ID(record.WritebackID),
		ProposalType:       record.ProposalType,
	}
	if record.ApprovalDecidedAt.Valid {
		value := record.ApprovalDecidedAt.Time.UTC()
		mapping.ApprovalDecidedAt = &value
	}
	return mapping
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
