package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// These models deliberately contain only storage fields. They are never
// passed to schema mutation APIs or association saving; the Atlas migration
// directory remains the sole schema authority.

type gitRemoteConfigModel struct {
	WorkspaceID     string    `gorm:"column:workspace_id;type:uuid;primaryKey"`
	Configured      bool      `gorm:"column:configured;not null"`
	NormalizedURL   *string   `gorm:"column:normalized_url;type:text"`
	Branch          *string   `gorm:"column:branch;type:text"`
	AutoSync        bool      `gorm:"column:auto_sync;not null"`
	TokenConfigured bool      `gorm:"column:token_configured;not null"`
	Revision        int64     `gorm:"column:revision;not null"`
	CreatedAt       time.Time `gorm:"column:created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

// gitSyncJSONB keeps database/sql from inferring JSON payloads as bytea.
type gitSyncJSONB []byte

func (value gitSyncJSONB) Value() (driver.Value, error) {
	if !json.Valid(value) {
		return nil, errors.New("Git sync JSONB value is invalid")
	}
	return string(value), nil
}

func (value *gitSyncJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("Git sync JSONB destination is nil")
	}
	var encoded []byte
	switch typed := source.(type) {
	case []byte:
		encoded = append(encoded, typed...)
	case string:
		encoded = append(encoded, typed...)
	default:
		return errors.New("Git sync JSONB source has an invalid type")
	}
	if !json.Valid(encoded) {
		return errors.New("Git sync JSONB source is invalid")
	}
	*value = encoded
	return nil
}

func (gitRemoteConfigModel) TableName() string { return "ops.git_remote_config" }

type gitRemoteConfigRevisionModel struct {
	WorkspaceID     string    `gorm:"column:workspace_id;type:uuid;primaryKey"`
	Revision        int64     `gorm:"column:revision;primaryKey"`
	Configured      bool      `gorm:"column:configured;not null"`
	NormalizedURL   *string   `gorm:"column:normalized_url;type:text"`
	Branch          *string   `gorm:"column:branch;type:text"`
	AutoSync        bool      `gorm:"column:auto_sync;not null"`
	TokenConfigured bool      `gorm:"column:token_configured;not null"`
	Actor           string    `gorm:"column:actor;type:text;not null"`
	ConfigCreatedAt time.Time `gorm:"column:config_created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	CreatedAt       time.Time `gorm:"column:created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

func (gitRemoteConfigRevisionModel) TableName() string { return "ops.git_remote_config_revision" }

type gitRemoteCredentialModel struct {
	WorkspaceID    string    `gorm:"column:workspace_id;type:uuid;primaryKey"`
	ConfigRevision int64     `gorm:"column:config_revision;not null"`
	KeyID          string    `gorm:"column:key_id;type:text;not null"`
	Nonce          []byte    `gorm:"column:nonce;type:bytea;not null"`
	Ciphertext     []byte    `gorm:"column:ciphertext;type:bytea;not null"`
	AADDigest      string    `gorm:"column:aad_digest;type:text;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

func (gitRemoteCredentialModel) TableName() string { return "ops.git_remote_credential" }

type gitRemoteCommandReceiptModel struct {
	WorkspaceID      string    `gorm:"column:workspace_id;type:uuid;primaryKey"`
	IdempotencyKey   string    `gorm:"column:idempotency_key;type:text;primaryKey"`
	RequestHash      string    `gorm:"column:request_hash;type:text;not null"`
	CommandType      string    `gorm:"column:command_type;type:text;not null"`
	ExpectedRevision int64     `gorm:"column:expected_revision;not null"`
	ResultRevision   int64     `gorm:"column:result_revision;not null"`
	CreatedAt        time.Time `gorm:"column:created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

func (gitRemoteCommandReceiptModel) TableName() string { return "ops.git_remote_command_receipt" }

type gitSyncRunModel struct {
	ID                string       `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       string       `gorm:"column:workspace_id;type:uuid;not null"`
	ConfigRevision    int64        `gorm:"column:config_revision;not null"`
	RemoteURL         string       `gorm:"column:remote_url;type:text;not null"`
	Branch            string       `gorm:"column:branch;type:text;not null"`
	Trigger           string       `gorm:"column:trigger;type:text;not null"`
	RetryOfRunID      *string      `gorm:"column:retry_of_run_id;type:uuid"`
	IdempotencyKey    string       `gorm:"column:idempotency_key;type:text;not null"`
	RequestHash       string       `gorm:"column:request_hash;type:text;not null"`
	Status            string       `gorm:"column:status;type:text;not null"`
	Direction         string       `gorm:"column:direction;type:text;not null"`
	FailureClass      string       `gorm:"column:failure_class;type:text;not null"`
	ErrorCode         string       `gorm:"column:error_code;type:text;not null"`
	Retryable         bool         `gorm:"column:retryable;not null"`
	ExpectedHeadOID   *string      `gorm:"column:expected_head_oid;type:text"`
	ExpectedRemoteOID *string      `gorm:"column:expected_remote_oid;type:text"`
	VerifiedHeadOID   *string      `gorm:"column:verified_head_oid;type:text"`
	VerifiedRemoteOID *string      `gorm:"column:verified_remote_oid;type:text"`
	ChangedFiles      gitSyncJSONB `gorm:"column:changed_files;type:jsonb;not null"`
	IndexStatus       string       `gorm:"column:index_status;type:text;not null"`
	IndexErrorCode    string       `gorm:"column:index_error_code;type:text;not null"`
	IndexRetryable    bool         `gorm:"column:index_retryable;not null"`
	IndexVersionID    *string      `gorm:"column:index_version_id;type:uuid"`
	AttemptCount      int          `gorm:"column:attempt_count;not null"`
	Version           int64        `gorm:"column:version;not null"`
	CreatedAt         time.Time    `gorm:"column:created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt         time.Time    `gorm:"column:updated_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	CompletedAt       *time.Time   `gorm:"column:completed_at;autoCreateTime:false;autoUpdateTime:false"`
}

func (gitSyncRunModel) TableName() string { return "ops.git_sync_run" }

type gitSyncAttemptModel struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       string     `gorm:"column:workspace_id;type:uuid;not null"`
	RunID             string     `gorm:"column:run_id;type:uuid;not null"`
	AttemptNo         int        `gorm:"column:attempt_no;not null"`
	Status            string     `gorm:"column:status;type:text;not null"`
	Phase             string     `gorm:"column:phase;type:text;not null"`
	ExpectedHeadOID   *string    `gorm:"column:expected_head_oid;type:text"`
	ExpectedRemoteOID *string    `gorm:"column:expected_remote_oid;type:text"`
	ResultKnown       *bool      `gorm:"column:result_known"`
	ErrorCode         string     `gorm:"column:error_code;type:text;not null"`
	Retryable         bool       `gorm:"column:retryable;not null"`
	LeaseOwner        *string    `gorm:"column:lease_owner;type:text"`
	LeaseExpiresAt    *time.Time `gorm:"column:lease_expires_at;autoCreateTime:false;autoUpdateTime:false"`
	StartedAt         time.Time  `gorm:"column:started_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	CompletedAt       *time.Time `gorm:"column:completed_at;autoCreateTime:false;autoUpdateTime:false"`
	Version           int64      `gorm:"column:version;not null"`
}

func (gitSyncAttemptModel) TableName() string { return "ops.git_sync_attempt" }

type gitSyncOutboxModel struct {
	ID             string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID    string     `gorm:"column:workspace_id;type:uuid;not null"`
	RunID          string     `gorm:"column:run_id;type:uuid;not null"`
	Kind           string     `gorm:"column:kind;type:text;not null"`
	EventKey       string     `gorm:"column:event_key;type:text;not null"`
	AvailableAt    time.Time  `gorm:"column:available_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	AttemptCount   int        `gorm:"column:attempt_count;not null"`
	LastErrorCode  *string    `gorm:"column:last_error_code;type:text"`
	LeaseOwner     *string    `gorm:"column:lease_owner;type:text"`
	LeaseExpiresAt *time.Time `gorm:"column:lease_expires_at;autoCreateTime:false;autoUpdateTime:false"`
	PublishedAt    *time.Time `gorm:"column:published_at;autoCreateTime:false;autoUpdateTime:false"`
	PoisonedAt     *time.Time `gorm:"column:poisoned_at;autoCreateTime:false;autoUpdateTime:false"`
	Version        int64      `gorm:"column:version;not null"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

func (gitSyncOutboxModel) TableName() string { return "ops.git_sync_outbox" }

type gitSyncIndexRetryReceiptModel struct {
	WorkspaceID    string    `gorm:"column:workspace_id;type:uuid;primaryKey"`
	IdempotencyKey string    `gorm:"column:idempotency_key;type:text;primaryKey"`
	RequestHash    string    `gorm:"column:request_hash;type:text;not null"`
	RunID          string    `gorm:"column:run_id;type:uuid;not null"`
	ResultVersion  int64     `gorm:"column:result_version;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;not null;autoCreateTime:false;autoUpdateTime:false"`
}

func (gitSyncIndexRetryReceiptModel) TableName() string { return "ops.git_sync_index_retry_receipt" }

type gitSyncTableNamer interface {
	TableName() string
}

var (
	_ gitSyncTableNamer = gitRemoteConfigModel{}
	_ gitSyncTableNamer = gitRemoteConfigRevisionModel{}
	_ gitSyncTableNamer = gitRemoteCredentialModel{}
	_ gitSyncTableNamer = gitRemoteCommandReceiptModel{}
	_ gitSyncTableNamer = gitSyncRunModel{}
	_ gitSyncTableNamer = gitSyncAttemptModel{}
	_ gitSyncTableNamer = gitSyncOutboxModel{}
	_ gitSyncTableNamer = gitSyncIndexRetryReceiptModel{}
)
