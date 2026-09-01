package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// reviewJSONB keeps one validated JSON document in one database/sql bind.
// Returning string prevents pgx stdlib from treating []byte as bytea.
type reviewJSONB []byte

func (value reviewJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("review JSONB value is invalid")
	}
	return string(value), nil
}

func (value *reviewJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("review JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("review JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted review JSONB value is invalid")
	}
	*value = reviewJSONB(raw)
	return nil
}

type reviewDeckGORMRecord struct {
	ID               string      `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID      string      `gorm:"column:workspace_id;type:uuid;not null"`
	Name             string      `gorm:"column:name;not null"`
	Scope            reviewJSONB `gorm:"column:scope;type:jsonb;not null"`
	Status           string      `gorm:"column:status;not null"`
	DailyLimit       int         `gorm:"column:daily_limit;not null"`
	SchedulerVersion string      `gorm:"column:scheduler_version;not null"`
	Version          int64       `gorm:"column:version;not null"`
	CreatedAt        time.Time   `gorm:"column:created_at;not null;autoCreateTime:false"`
	UpdatedAt        time.Time   `gorm:"column:updated_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

func (reviewDeckGORMRecord) TableName() string { return "learning.review_deck" }

type reviewCardGORMRecord struct {
	ID                 string      `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID        string      `gorm:"column:workspace_id;type:uuid;not null"`
	DeckID             string      `gorm:"column:deck_id;type:uuid;not null"`
	ClaimID            *string     `gorm:"column:claim_id;type:uuid"`
	Question           string      `gorm:"column:question;not null"`
	AnswerPoints       reviewJSONB `gorm:"column:answer_points;type:jsonb;not null"`
	Evidence           reviewJSONB `gorm:"column:evidence;type:jsonb;not null"`
	CardType           string      `gorm:"column:card_type;not null"`
	Difficulty         float64     `gorm:"column:difficulty;not null"`
	Status             string      `gorm:"column:status;not null"`
	Fingerprint        string      `gorm:"column:fingerprint;not null"`
	ModelVersion       string      `gorm:"column:model_version;not null"`
	InvalidationReason *string     `gorm:"column:invalidation_reason"`
	InvalidatedAt      *time.Time  `gorm:"column:invalidated_at"`
	Version            int64       `gorm:"column:version;not null"`
	CreatedAt          time.Time   `gorm:"column:created_at;not null;autoCreateTime:false"`
	UpdatedAt          time.Time   `gorm:"column:updated_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

func (reviewCardGORMRecord) TableName() string { return "learning.review_card" }

type reviewScheduleGORMRecord struct {
	CardID           string     `gorm:"column:card_id;type:uuid;primaryKey"`
	WorkspaceID      string     `gorm:"column:workspace_id;type:uuid;not null"`
	DueAt            time.Time  `gorm:"column:due_at;not null"`
	IntervalDays     float64    `gorm:"column:interval_days;not null"`
	Stability        float64    `gorm:"column:stability;not null"`
	Difficulty       float64    `gorm:"column:difficulty;not null"`
	LastReviewedAt   *time.Time `gorm:"column:last_reviewed_at"`
	SchedulerVersion string     `gorm:"column:scheduler_version;not null"`
	Paused           bool       `gorm:"column:paused;not null"`
	Version          int64      `gorm:"column:version;not null"`
}

func (reviewScheduleGORMRecord) TableName() string { return "learning.review_schedule" }

type reviewSessionGORMRecord struct {
	ID             string      `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string      `gorm:"column:workspace_id;type:uuid;not null"`
	DeckID         *string     `gorm:"column:deck_id;type:uuid"`
	SessionType    string      `gorm:"column:session_type;not null"`
	Status         string      `gorm:"column:status;not null"`
	Config         reviewJSONB `gorm:"column:config;type:jsonb;not null"`
	IdempotencyKey string      `gorm:"column:idempotency_key;not null"`
	RequestHash    string      `gorm:"column:request_hash;not null"`
	StartedAt      time.Time   `gorm:"column:started_at;not null"`
	EndedAt        *time.Time  `gorm:"column:ended_at"`
}

func (reviewSessionGORMRecord) TableName() string { return "learning.review_session" }

type reviewAnswerGORMRecord struct {
	ID               string      `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID      string      `gorm:"column:workspace_id;type:uuid;not null"`
	SessionID        string      `gorm:"column:session_id;type:uuid;not null"`
	CardID           *string     `gorm:"column:card_id;type:uuid"`
	QuestionRef      string      `gorm:"column:question_ref;not null"`
	IdempotencyKey   string      `gorm:"column:idempotency_key;not null"`
	RequestHash      string      `gorm:"column:request_hash;not null"`
	UserAnswer       string      `gorm:"column:user_answer;not null"`
	Rating           int16       `gorm:"column:rating;not null"`
	ScorerVersion    string      `gorm:"column:scorer_version;not null"`
	Score            reviewJSONB `gorm:"column:score;type:jsonb;not null"`
	Feedback         reviewJSONB `gorm:"column:feedback;type:jsonb;not null"`
	ScheduleSnapshot reviewJSONB `gorm:"column:schedule_snapshot;type:jsonb;not null"`
	CreatedAt        time.Time   `gorm:"column:created_at;not null;autoCreateTime:false"`
}

func (reviewAnswerGORMRecord) TableName() string { return "learning.review_answer" }

type reviewCommandGORMRecord struct {
	WorkspaceID      string      `gorm:"column:workspace_id;type:uuid;primaryKey"`
	IdempotencyKey   string      `gorm:"column:idempotency_key;primaryKey"`
	RequestHash      string      `gorm:"column:request_hash;not null"`
	CommandType      string      `gorm:"column:command_type;not null"`
	AggregateID      string      `gorm:"column:aggregate_id;type:uuid;not null"`
	AggregateVersion int64       `gorm:"column:aggregate_version;not null"`
	Response         reviewJSONB `gorm:"column:response;type:jsonb;not null"`
	CreatedAt        time.Time   `gorm:"column:created_at;not null;autoCreateTime:false"`
}

func (reviewCommandGORMRecord) TableName() string { return "learning.review_command" }
