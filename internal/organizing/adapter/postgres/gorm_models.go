package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
)

// organizingJSONB 以文本绑定 JSONB，避免 database/sql 将其解释为 bytea。
type organizingJSONB []byte

func (value organizingJSONB) Value() (driver.Value, error) {
	if !json.Valid(value) {
		return nil, errors.New("organizing JSON document is invalid")
	}
	return string(value), nil
}

func (value *organizingJSONB) Scan(source any) error {
	var raw []byte
	switch source := source.(type) {
	case []byte:
		raw = append([]byte(nil), source...)
	case string:
		raw = []byte(source)
	default:
		return errors.New("stored organizing JSON document has an invalid type")
	}
	if !json.Valid(raw) {
		return errors.New("stored organizing JSON document is invalid")
	}
	*value = raw
	return nil
}

type draftModel struct {
	ID                  string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID         string    `gorm:"column:workspace_id;type:uuid"`
	Intent              string    `gorm:"column:intent"`
	Status              string    `gorm:"column:status"`
	TemplateRevisionID  *string   `gorm:"column:template_revision_id;type:uuid"`
	ConfirmedSnapshotID *string   `gorm:"column:confirmed_snapshot_id;type:uuid"`
	Version             int64     `gorm:"column:version"`
	CreatedAt           time.Time `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt           time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (draftModel) TableName() string { return "organizing.draft" }

const gormDraftColumns = "id,workspace_id,intent,status,template_revision_id,confirmed_snapshot_id,version,created_at,updated_at"

type draftVersionModel struct {
	WorkspaceID         string    `gorm:"column:workspace_id;type:uuid;primaryKey"`
	DraftID             string    `gorm:"column:draft_id;type:uuid;primaryKey"`
	Version             int64     `gorm:"column:version;primaryKey;autoIncrement:false"`
	Intent              string    `gorm:"column:intent"`
	Status              string    `gorm:"column:status"`
	TemplateRevisionID  *string   `gorm:"column:template_revision_id;type:uuid"`
	ConfirmedSnapshotID *string   `gorm:"column:confirmed_snapshot_id;type:uuid"`
	DraftCreatedAt      time.Time `gorm:"column:draft_created_at;autoCreateTime:false"`
	UpdatedAt           time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (draftVersionModel) TableName() string { return "organizing.draft_version" }

const gormDraftVersionColumns = "workspace_id,draft_id,version,intent,status,template_revision_id,confirmed_snapshot_id,draft_created_at,updated_at"

type materialRefModel struct {
	Kind               string          `gorm:"column:kind"`
	SourceVersionID    *string         `gorm:"column:source_version_id;type:uuid"`
	DocumentID         *string         `gorm:"column:document_id;type:uuid"`
	ArticleRevisionID  *string         `gorm:"column:article_revision_id;type:uuid"`
	ClaimID            *string         `gorm:"column:claim_id;type:uuid"`
	CollectionID       *string         `gorm:"column:collection_id;type:uuid"`
	OriginCollectionID *string         `gorm:"column:origin_collection_id;type:uuid"`
	ProfileRevisionID  *string         `gorm:"column:profile_revision_id;type:uuid"`
	MaterialVersion    int64           `gorm:"column:material_version"`
	ContentHash        *string         `gorm:"column:content_hash"`
	QueryHash          *string         `gorm:"column:query_hash"`
	ReadModelRevision  *string         `gorm:"column:read_model_revision"`
	Evidence           organizingJSONB `gorm:"column:evidence;type:jsonb"`
}

const gormMaterialRefColumns = "kind,source_version_id,document_id,article_revision_id,claim_id,collection_id,origin_collection_id,profile_revision_id,material_version,content_hash,query_hash,read_model_revision,evidence"

type draftMaterialModel struct {
	ID           string           `gorm:"column:id;type:uuid"`
	WorkspaceID  string           `gorm:"column:workspace_id;type:uuid;primaryKey"`
	DraftID      string           `gorm:"column:draft_id;type:uuid;primaryKey"`
	DraftVersion int64            `gorm:"column:draft_version;primaryKey;autoIncrement:false"`
	Position     int              `gorm:"column:position;primaryKey;autoIncrement:false"`
	Reference    materialRefModel `gorm:"embedded"`
	Title        string           `gorm:"column:title"`
	Reasons      pq.StringArray   `gorm:"column:reasons;type:text[]"`
	Origin       string           `gorm:"column:origin"`
	Availability string           `gorm:"column:availability"`
	Score        float64          `gorm:"column:score"`
	Selected     bool             `gorm:"column:selected"`
	CreatedAt    time.Time        `gorm:"column:created_at;autoCreateTime:false"`
}

func (draftMaterialModel) TableName() string { return "organizing.draft_material" }

const gormDraftMaterialColumns = "id,workspace_id,draft_id,draft_version,position," + gormMaterialRefColumns + ",title,reasons,origin,availability,score,selected,created_at"

type snapshotModel struct {
	ID                 string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID        string    `gorm:"column:workspace_id;type:uuid"`
	DraftID            string    `gorm:"column:draft_id;type:uuid"`
	DraftVersion       int64     `gorm:"column:draft_version"`
	TemplateID         string    `gorm:"column:template_id;type:uuid"`
	TemplateRevisionID string    `gorm:"column:template_revision_id;type:uuid"`
	TemplateHash       string    `gorm:"column:template_hash"`
	Intent             string    `gorm:"column:intent"`
	SnapshotHash       string    `gorm:"column:snapshot_hash"`
	CreatedAt          time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (snapshotModel) TableName() string { return "organizing.workflow_input_snapshot" }

const gormSnapshotColumns = "id,workspace_id,draft_id,draft_version,template_id,template_revision_id,template_hash,intent,snapshot_hash,created_at"

type snapshotMaterialModel struct {
	ID          string           `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID string           `gorm:"column:workspace_id;type:uuid"`
	SnapshotID  string           `gorm:"column:snapshot_id;type:uuid"`
	Position    int              `gorm:"column:position"`
	Reference   materialRefModel `gorm:"embedded"`
	CreatedAt   time.Time        `gorm:"column:created_at;autoCreateTime:false"`
}

func (snapshotMaterialModel) TableName() string { return "organizing.workflow_input_material" }

const gormSnapshotMaterialColumns = "id,workspace_id,snapshot_id,position," + gormMaterialRefColumns + ",created_at"

type templateModel struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       *string   `gorm:"column:workspace_id;type:uuid"`
	Owner             string    `gorm:"column:owner"`
	Kind              string    `gorm:"column:kind"`
	CurrentRevisionID string    `gorm:"column:current_revision_id;type:uuid"`
	Version           int64     `gorm:"column:version"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt         time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (templateModel) TableName() string { return "organizing.template" }

type templateRevisionModel struct {
	ID              string          `gorm:"column:id;type:uuid;primaryKey"`
	TemplateID      string          `gorm:"column:template_id;type:uuid"`
	WorkspaceID     *string         `gorm:"column:workspace_id;type:uuid"`
	Owner           string          `gorm:"column:owner"`
	RevisionNo      int64           `gorm:"column:revision_no"`
	Kind            string          `gorm:"column:kind"`
	SchemaVersion   string          `gorm:"column:schema_version"`
	Declaration     organizingJSONB `gorm:"column:declaration;type:jsonb"`
	DeclarationHash string          `gorm:"column:declaration_hash"`
	CreatedAt       time.Time       `gorm:"column:created_at;autoCreateTime:false"`
}

func (templateRevisionModel) TableName() string { return "organizing.template_revision" }

type commandReceiptModel struct {
	WorkspaceID           string    `gorm:"column:workspace_id;type:uuid;primaryKey"`
	IdempotencyKey        string    `gorm:"column:idempotency_key;primaryKey"`
	RequestHash           string    `gorm:"column:request_hash"`
	CommandType           string    `gorm:"column:command_type"`
	AggregateID           string    `gorm:"column:aggregate_id;type:uuid"`
	ExpectedVersion       int64     `gorm:"column:expected_version"`
	DraftID               *string   `gorm:"column:draft_id;type:uuid"`
	ResultDraftVersion    *int64    `gorm:"column:result_draft_version"`
	SnapshotID            *string   `gorm:"column:snapshot_id;type:uuid"`
	OutboxID              *string   `gorm:"column:outbox_id;type:uuid"`
	TemplateID            *string   `gorm:"column:template_id;type:uuid"`
	ResultTemplateVersion *int64    `gorm:"column:result_template_version"`
	TemplateRevisionID    *string   `gorm:"column:template_revision_id;type:uuid"`
	CreatedAt             time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (commandReceiptModel) TableName() string { return "organizing.command_receipt" }

type startOutboxModel struct {
	ID                 string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID        string     `gorm:"column:workspace_id;type:uuid"`
	SnapshotID         string     `gorm:"column:snapshot_id;type:uuid"`
	TemplateRevisionID string     `gorm:"column:template_revision_id;type:uuid"`
	TemplateKind       string     `gorm:"column:template_kind"`
	Status             string     `gorm:"column:status"`
	AvailableAt        time.Time  `gorm:"column:available_at"`
	AttemptCount       int        `gorm:"column:attempt_count"`
	LastErrorCode      *string    `gorm:"column:last_error_code"`
	LeaseOwner         *string    `gorm:"column:lease_owner"`
	LeaseUntil         *time.Time `gorm:"column:lease_until"`
	RunBindingID       *string    `gorm:"column:run_binding_id;type:uuid"`
	Version            int64      `gorm:"column:version"`
	CreatedAt          time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	StartedAt          *time.Time `gorm:"column:started_at"`
	PoisonedAt         *time.Time `gorm:"column:poisoned_at"`
}

func (startOutboxModel) TableName() string { return "organizing.workflow_start_outbox" }

type runBindingModel struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID       string    `gorm:"column:workspace_id;type:uuid"`
	SnapshotID        string    `gorm:"column:snapshot_id;type:uuid"`
	WorkflowRunID     string    `gorm:"column:workflow_run_id;type:uuid"`
	DefinitionKey     string    `gorm:"column:definition_key"`
	DefinitionVersion int64     `gorm:"column:definition_version"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (runBindingModel) TableName() string { return "organizing.run_binding" }

const gormRunBindingColumns = "id,workspace_id,snapshot_id,workflow_run_id,definition_key,definition_version,created_at"

type runResultModel struct {
	ID            string    `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID   string    `gorm:"column:workspace_id;type:uuid"`
	RunBindingID  string    `gorm:"column:run_binding_id;type:uuid"`
	SnapshotID    string    `gorm:"column:snapshot_id;type:uuid"`
	WorkflowRunID string    `gorm:"column:workflow_run_id;type:uuid"`
	NodeRunID     string    `gorm:"column:node_run_id;type:uuid"`
	ResultKind    string    `gorm:"column:result_kind"`
	ResultRef     string    `gorm:"column:result_ref;type:uuid"`
	ResultHash    string    `gorm:"column:result_hash"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (runResultModel) TableName() string { return "organizing.run_result" }

const gormRunResultColumns = "id,workspace_id,run_binding_id,snapshot_id,workflow_run_id,node_run_id,result_kind,result_ref,result_hash,created_at"

type generationModel struct {
	ID                    string     `gorm:"column:id;type:uuid;primaryKey"`
	WorkspaceID           string     `gorm:"column:workspace_id;type:uuid"`
	SnapshotID            string     `gorm:"column:snapshot_id;type:uuid"`
	WorkflowRunID         string     `gorm:"column:workflow_run_id;type:uuid"`
	NodeRunID             string     `gorm:"column:node_run_id;type:uuid"`
	NodeAttemptID         string     `gorm:"column:node_attempt_id;type:uuid"`
	GenerationKind        string     `gorm:"column:generation_kind"`
	RequestHash           string     `gorm:"column:request_hash"`
	ModelSettingsRevision *int64     `gorm:"column:model_settings_revision"`
	ModelRunID            *string    `gorm:"column:model_run_id;type:uuid"`
	Status                string     `gorm:"column:status"`
	OutputDocument        []byte     `gorm:"column:output_document;type:bytea"`
	OutputHash            *string    `gorm:"column:output_hash"`
	ErrorCode             *string    `gorm:"column:error_code"`
	Retryable             bool       `gorm:"column:retryable"`
	Version               int64      `gorm:"column:version"`
	CreatedAt             time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt             time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	CompletedAt           *time.Time `gorm:"column:completed_at"`
}

func (generationModel) TableName() string { return "organizing.generation" }

const gormGenerationColumns = "id,workspace_id,snapshot_id,workflow_run_id,node_run_id,node_attempt_id,generation_kind,request_hash,model_settings_revision,model_run_id,status,output_document,output_hash,error_code,retryable,version,created_at,updated_at,completed_at"
