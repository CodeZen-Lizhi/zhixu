package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

type learningPathJSONB []byte

func (value learningPathJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("learning path JSONB value is invalid")
	}
	return string(value), nil
}

func (value *learningPathJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("learning path JSONB destination is nil")
	}
	var encoded []byte
	switch source := source.(type) {
	case []byte:
		encoded = append([]byte(nil), source...)
	case string:
		encoded = []byte(source)
	default:
		return errors.New("learning path JSONB source is invalid")
	}
	if len(encoded) == 0 || !json.Valid(encoded) {
		return errors.New("learning path JSONB source is invalid")
	}
	*value = encoded
	return nil
}

type learningPathGORMRecord struct {
	ID                  string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID         string    `gorm:"column:workspace_id;type:uuid;not null"`
	InterviewSessionID  *string   `gorm:"column:interview_session_id;type:uuid"`
	InterviewReportID   *string   `gorm:"column:interview_report_id;type:uuid"`
	OriginType          string    `gorm:"column:origin_type;not null"`
	ReviewAnswerID      *string   `gorm:"column:review_answer_id;type:uuid"`
	SourcePolicyVersion string    `gorm:"column:source_policy_version;not null"`
	ArtifactID          string    `gorm:"column:artifact_id;type:uuid;not null"`
	ArtifactRevisionID  string    `gorm:"column:artifact_revision_id;type:uuid;not null"`
	ArtifactVersion     int64     `gorm:"column:artifact_version;not null"`
	Status              string    `gorm:"column:status;not null"`
	Version             int64     `gorm:"column:version;not null"`
	CreatedAt           time.Time `gorm:"column:created_at;not null;autoCreateTime:false"`
	UpdatedAt           time.Time `gorm:"column:updated_at;not null;autoUpdateTime:false"`
}

func (learningPathGORMRecord) TableName() string { return "learning.learning_path" }

type learningPathStepGORMRecord struct {
	ID              string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID     string    `gorm:"column:workspace_id;type:uuid;not null"`
	PathID          string    `gorm:"column:path_id;type:uuid;not null"`
	StepNo          int       `gorm:"column:step_no;not null"`
	ClaimID         string    `gorm:"column:claim_id;type:uuid;not null"`
	TopicID         *string   `gorm:"column:topic_id;type:uuid"`
	SourceVersionID string    `gorm:"column:source_version_id;type:uuid;not null"`
	SourceSpanID    string    `gorm:"column:source_span_id;type:uuid;not null"`
	EvidenceHash    string    `gorm:"column:evidence_hash;not null"`
	Title           string    `gorm:"column:title;not null"`
	Rationale       string    `gorm:"column:rationale;not null"`
	Status          string    `gorm:"column:status;not null"`
	Version         int64     `gorm:"column:version;not null"`
	CreatedAt       time.Time `gorm:"column:created_at;not null;autoCreateTime:false"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null;autoUpdateTime:false"`
}

func (learningPathStepGORMRecord) TableName() string { return "learning.learning_path_step" }

type learningPathCommandGORMRecord struct {
	WorkspaceID     string            `gorm:"column:workspace_id;type:uuid;primaryKey"`
	IdempotencyKey  string            `gorm:"column:idempotency_key;primaryKey"`
	RequestHash     string            `gorm:"column:request_hash;not null"`
	CommandType     string            `gorm:"column:command_type;not null"`
	PathID          string            `gorm:"column:path_id;type:uuid;not null"`
	ExpectedVersion int64             `gorm:"column:expected_version;not null"`
	PathVersion     int64             `gorm:"column:path_version;not null"`
	Response        learningPathJSONB `gorm:"column:response;type:jsonb;not null"`
	CreatedAt       time.Time         `gorm:"column:created_at;not null;autoCreateTime:false"`
}

func (learningPathCommandGORMRecord) TableName() string {
	return "learning.learning_path_command"
}

type learningPathReservationGORMRecord struct {
	WorkspaceID          string            `gorm:"column:workspace_id;type:uuid;primaryKey"`
	ReviewAnswerID       string            `gorm:"column:review_answer_id;type:uuid;primaryKey"`
	IdempotencyKey       string            `gorm:"column:idempotency_key;not null"`
	RequestHash          string            `gorm:"column:request_hash;not null"`
	SourceSnapshot       learningPathJSONB `gorm:"column:source_snapshot;type:jsonb;not null"`
	SourceSnapshotDigest string            `gorm:"column:source_snapshot_digest;not null"`
	AttemptNo            int64             `gorm:"column:attempt_no;not null"`
	OriginType           string            `gorm:"column:origin_type;not null"`
	ArtifactDigest       *string           `gorm:"column:artifact_digest"`
	PathID               *string           `gorm:"column:path_id;type:uuid"`
	ArtifactID           *string           `gorm:"column:artifact_id;type:uuid"`
	ArtifactRevisionID   *string           `gorm:"column:artifact_revision_id;type:uuid"`
	ArtifactVersion      *int64            `gorm:"column:artifact_version"`
	Status               string            `gorm:"column:status;not null"`
	CreatedAt            time.Time         `gorm:"column:created_at;not null;autoCreateTime:false"`
	PreparedAt           *time.Time        `gorm:"column:prepared_at"`
	CompletedAt          *time.Time        `gorm:"column:completed_at"`
	AbandonedAt          *time.Time        `gorm:"column:abandoned_at"`
	UpdatedAt            time.Time         `gorm:"column:updated_at;not null;autoUpdateTime:false"`
}

func (learningPathReservationGORMRecord) TableName() string {
	return "learning.learning_path_creation_reservation"
}
