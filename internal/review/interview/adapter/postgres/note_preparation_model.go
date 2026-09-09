package postgres

import (
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

type notePreparationModel struct {
	ID                    string         `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID           string         `gorm:"column:workspace_id;type:uuid;not null"`
	NoteID                string         `gorm:"column:note_id;type:uuid;not null"`
	RevisionID            string         `gorm:"column:revision_id;type:uuid;not null"`
	DocumentID            string         `gorm:"column:document_id;type:uuid;not null"`
	ArticleRevisionID     string         `gorm:"column:article_revision_id;type:uuid;not null"`
	ProjectionHash        string         `gorm:"column:projection_hash;not null"`
	Snapshot              interviewJSONB `gorm:"column:snapshot;type:jsonb;not null"`
	Options               interviewJSONB `gorm:"column:options;type:jsonb;not null"`
	Status                string         `gorm:"column:status;not null"`
	WorkflowRunID         string         `gorm:"column:workflow_run_id;type:uuid;not null"`
	NodeRunID             string         `gorm:"column:node_run_id;type:uuid;not null"`
	NodeAttemptID         *string        `gorm:"column:node_attempt_id;type:uuid"`
	ModelRunID            *string        `gorm:"column:model_run_id;type:uuid"`
	ModelSettingsRevision *int64         `gorm:"column:model_settings_revision"`
	ModelOutput           []byte         `gorm:"column:model_output;type:bytea"`
	SessionID             *string        `gorm:"column:session_id;type:uuid"`
	FailureCode           *string        `gorm:"column:failure_code"`
	FailureRetryable      bool           `gorm:"column:failure_retryable;not null"`
	Version               int64          `gorm:"column:version;not null"`
	IdempotencyKey        string         `gorm:"column:idempotency_key;not null"`
	RequestHash           string         `gorm:"column:request_hash;not null"`
	RetryOf               *string        `gorm:"column:retry_of;type:uuid"`
	CreatedAt             time.Time      `gorm:"column:created_at;not null;autoCreateTime:false"`
	UpdatedAt             time.Time      `gorm:"column:updated_at;not null;autoUpdateTime:false"`
}

func (notePreparationModel) TableName() string { return "learning.note_interview_preparation" }

func notePreparationRow(p interviewapp.NotePreparation) (notePreparationModel, error) {
	snapshot, err := json.Marshal(p.Snapshot)
	if err != nil {
		return notePreparationModel{}, err
	}
	options, err := json.Marshal(p.Options)
	if err != nil {
		return notePreparationModel{}, err
	}
	row := notePreparationModel{ID: string(p.ID), WorkspaceID: string(p.WorkspaceID), NoteID: string(p.NoteRevision.NoteID), RevisionID: string(p.NoteRevision.RevisionID),
		DocumentID: string(p.NoteRevision.DocumentID), ArticleRevisionID: string(p.NoteRevision.ArticleRevisionID), ProjectionHash: p.NoteRevision.ProjectionHash,
		Snapshot: interviewJSONB(snapshot), Options: interviewJSONB(options), Status: string(p.Status), WorkflowRunID: string(p.WorkflowRunID), NodeRunID: string(p.NodeRunID), NodeAttemptID: noteStringID(p.NodeAttemptID), ModelRunID: noteStringID(p.ModelRunID),
		ModelSettingsRevision: p.ModelSettingsRevision, ModelOutput: append([]byte(nil), p.ModelOutput...), SessionID: noteOptionalID(p.SessionID), Version: p.Version, IdempotencyKey: p.IdempotencyKey, RequestHash: p.RequestHash,
		RetryOf: noteOptionalID(p.RetryOf), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if p.Failure != nil {
		row.FailureCode = &p.Failure.Code
		row.FailureRetryable = p.Failure.Retryable
	}
	return row, nil
}

func (row notePreparationModel) value() (interviewapp.NotePreparation, error) {
	p := interviewapp.NotePreparation{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), Status: interviewapp.NotePreparationStatus(row.Status), WorkflowRunID: foundation.ID(row.WorkflowRunID), NodeRunID: foundation.ID(row.NodeRunID),
		NodeAttemptID: noteIDValue(row.NodeAttemptID), ModelRunID: noteIDValue(row.ModelRunID), ModelSettingsRevision: row.ModelSettingsRevision, ModelOutput: append(json.RawMessage(nil), row.ModelOutput...),
		SessionID: noteIDPointer(row.SessionID), Version: row.Version, IdempotencyKey: row.IdempotencyKey, RequestHash: row.RequestHash, RetryOf: noteIDPointer(row.RetryOf), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if json.Unmarshal(row.Snapshot, &p.Snapshot) != nil || json.Unmarshal(row.Options, &p.Options) != nil {
		return p, notePreparationConflict("persisted note preparation input is invalid")
	}
	var err error
	p.NoteRevision, err = domain.NoteRevisionFromSnapshot(p.Snapshot)
	if err != nil {
		return p, err
	}
	if row.FailureCode != nil {
		p.Failure = &interviewapp.NotePreparationFailure{Code: *row.FailureCode, Retryable: row.FailureRetryable}
	}
	if string(p.NoteRevision.NoteID) != row.NoteID || string(p.NoteRevision.RevisionID) != row.RevisionID || string(p.NoteRevision.DocumentID) != row.DocumentID || string(p.NoteRevision.ArticleRevisionID) != row.ArticleRevisionID || p.NoteRevision.ProjectionHash != row.ProjectionHash {
		return p, notePreparationConflict("persisted note preparation source changed")
	}
	return p, p.Validate()
}

func noteStringID(id foundation.ID) *string {
	if id == "" {
		return nil
	}
	v := string(id)
	return &v
}
func noteOptionalID(id *foundation.ID) *string {
	if id == nil {
		return nil
	}
	return noteStringID(*id)
}
func noteIDValue(id *string) foundation.ID {
	if id == nil {
		return ""
	}
	return foundation.ID(*id)
}
func noteIDPointer(id *string) *foundation.ID {
	if id == nil {
		return nil
	}
	v := foundation.ID(*id)
	return &v
}
