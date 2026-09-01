package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// interviewJSONB keeps one validated JSON document in one database/sql bind.
// Returning string prevents pgx stdlib from treating []byte as bytea.
type interviewJSONB []byte

func (value interviewJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("interview JSONB value is invalid")
	}
	return string(value), nil
}

func (value *interviewJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("interview JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("interview JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted interview JSONB value is invalid")
	}
	*value = interviewJSONB(raw)
	return nil
}

type interviewSessionGORMRecord struct {
	SessionID           string    `gorm:"column:session_id;type:uuid;primaryKey"`
	WorkspaceID         string    `gorm:"column:workspace_id;type:uuid;not null"`
	DomainSchemaVersion string    `gorm:"column:domain_schema_version;not null"`
	Version             int64     `gorm:"column:version;not null"`
	FollowUpCount       int       `gorm:"column:follow_up_count;not null"`
	CreatedAt           time.Time `gorm:"column:created_at;not null;autoCreateTime:false"`
	UpdatedAt           time.Time `gorm:"column:updated_at;not null;autoUpdateTime:false"`
}

func (interviewSessionGORMRecord) TableName() string { return "learning.interview_session" }

type interviewQuestionGORMRecord struct {
	ID               string         `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID      string         `gorm:"column:workspace_id;type:uuid;not null"`
	SessionID        string         `gorm:"column:session_id;type:uuid;not null"`
	QuestionNo       int            `gorm:"column:question_no;not null"`
	FollowUpNo       int            `gorm:"column:follow_up_no;not null"`
	ParentQuestionID *string        `gorm:"column:parent_question_id;type:uuid"`
	ClaimID          string         `gorm:"column:claim_id;type:uuid;not null"`
	TopicID          *string        `gorm:"column:topic_id;type:uuid"`
	Prompt           string         `gorm:"column:prompt;not null"`
	AnswerPoints     interviewJSONB `gorm:"column:answer_points;type:jsonb;not null"`
	Evidence         interviewJSONB `gorm:"column:evidence;type:jsonb;not null"`
	Status           string         `gorm:"column:status;not null"`
	Fingerprint      string         `gorm:"column:fingerprint;not null"`
	CreatedAt        time.Time      `gorm:"column:created_at;not null;autoCreateTime:false"`
	AnsweredAt       *time.Time     `gorm:"column:answered_at"`
}

func (interviewQuestionGORMRecord) TableName() string { return "learning.interview_question" }

type interviewTurnGORMRecord struct {
	ID             string         `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string         `gorm:"column:workspace_id;type:uuid;not null"`
	SessionID      string         `gorm:"column:session_id;type:uuid;not null"`
	QuestionID     string         `gorm:"column:question_id;type:uuid;not null"`
	IdempotencyKey string         `gorm:"column:idempotency_key;not null"`
	RequestHash    string         `gorm:"column:request_hash;not null"`
	UserAnswer     string         `gorm:"column:user_answer;not null"`
	Score          interviewJSONB `gorm:"column:score;type:jsonb;not null"`
	Decision       interviewJSONB `gorm:"column:decision;type:jsonb;not null"`
	ScorerVersion  string         `gorm:"column:scorer_version;not null"`
	CreatedAt      time.Time      `gorm:"column:created_at;not null;autoCreateTime:false"`
}

func (interviewTurnGORMRecord) TableName() string { return "learning.interview_turn" }

type interviewReportGORMRecord struct {
	ID                  string         `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID         string         `gorm:"column:workspace_id;type:uuid;not null"`
	SessionID           string         `gorm:"column:session_id;type:uuid;not null"`
	DomainSchemaVersion string         `gorm:"column:domain_schema_version;not null"`
	Report              interviewJSONB `gorm:"column:report;type:jsonb;not null"`
	ReportHash          string         `gorm:"column:report_hash;not null"`
	ArtifactID          string         `gorm:"column:artifact_id;type:uuid;not null"`
	ArtifactRevisionID  string         `gorm:"column:artifact_revision_id;type:uuid;not null"`
	ArtifactVersion     int64          `gorm:"column:artifact_version;not null"`
	CreatedAt           time.Time      `gorm:"column:created_at;not null;autoCreateTime:false"`
}

func (interviewReportGORMRecord) TableName() string { return "learning.interview_report" }

// interviewLearningPathGORMRecord targets the INTERVIEW compatibility view.
// It must not be changed to the shared learning.learning_path base relation.
type interviewLearningPathGORMRecord struct {
	ID                 string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID        string    `gorm:"column:workspace_id;type:uuid;not null"`
	SessionID          string    `gorm:"column:session_id;type:uuid;not null"`
	ReportID           string    `gorm:"column:report_id;type:uuid;not null"`
	ArtifactID         string    `gorm:"column:artifact_id;type:uuid;not null"`
	ArtifactRevisionID string    `gorm:"column:artifact_revision_id;type:uuid;not null"`
	ArtifactVersion    int64     `gorm:"column:artifact_version;not null"`
	Status             string    `gorm:"column:status;not null"`
	Version            int64     `gorm:"column:version;not null"`
	CreatedAt          time.Time `gorm:"column:created_at;not null;autoCreateTime:false"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null;autoUpdateTime:false"`
}

func (interviewLearningPathGORMRecord) TableName() string {
	return "learning.interview_learning_path"
}

type interviewLearningPathStepGORMRecord struct {
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

func (interviewLearningPathStepGORMRecord) TableName() string {
	return "learning.interview_learning_path_step"
}

type interviewCommandGORMRecord struct {
	WorkspaceID    string         `gorm:"column:workspace_id;type:uuid;primaryKey"`
	IdempotencyKey string         `gorm:"column:idempotency_key;primaryKey"`
	RequestHash    string         `gorm:"column:request_hash;not null"`
	CommandType    string         `gorm:"column:command_type;not null"`
	SessionID      string         `gorm:"column:session_id;type:uuid;not null"`
	Response       interviewJSONB `gorm:"column:response;type:jsonb;not null"`
	CreatedAt      time.Time      `gorm:"column:created_at;not null;autoCreateTime:false"`
}

func (interviewCommandGORMRecord) TableName() string { return "learning.interview_command" }

type interviewCompletionReservationGORMRecord struct {
	WorkspaceID              string     `gorm:"column:workspace_id;type:uuid;primaryKey"`
	SessionID                string     `gorm:"column:session_id;type:uuid;primaryKey"`
	IdempotencyKey           string     `gorm:"column:idempotency_key;not null"`
	RequestHash              string     `gorm:"column:request_hash;not null"`
	ManualEnd                bool       `gorm:"column:manual_end;not null"`
	SnapshotVersion          int64      `gorm:"column:snapshot_version;not null"`
	ArtifactDigest           *string    `gorm:"column:artifact_digest"`
	ReportID                 *string    `gorm:"column:report_id;type:uuid"`
	ReportArtifactID         *string    `gorm:"column:report_artifact_id;type:uuid"`
	ReportArtifactRevisionID *string    `gorm:"column:report_artifact_revision_id;type:uuid"`
	ReportArtifactVersion    *int64     `gorm:"column:report_artifact_version"`
	PathID                   *string    `gorm:"column:path_id;type:uuid"`
	PathArtifactID           *string    `gorm:"column:path_artifact_id;type:uuid"`
	PathArtifactRevisionID   *string    `gorm:"column:path_artifact_revision_id;type:uuid"`
	PathArtifactVersion      *int64     `gorm:"column:path_artifact_version"`
	Status                   string     `gorm:"column:status;not null"`
	CreatedAt                time.Time  `gorm:"column:created_at;not null;autoCreateTime:false"`
	PreparedAt               *time.Time `gorm:"column:prepared_at"`
	CompletedAt              *time.Time `gorm:"column:completed_at"`
	AbandonedAt              *time.Time `gorm:"column:abandoned_at"`
	UpdatedAt                time.Time  `gorm:"column:updated_at;not null;autoUpdateTime:false"`
}

func (interviewCompletionReservationGORMRecord) TableName() string {
	return "learning.interview_completion_reservation"
}
