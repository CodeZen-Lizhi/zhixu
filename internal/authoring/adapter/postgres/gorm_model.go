package postgres

import "time"

// Authoring persistence records keep the database contract explicit. They are
// intentionally not used as GORM associations or auto-timestamp models.
type gormWorkingDraftRecord struct {
	ID          string    `gorm:"column:id"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	DocumentID  *string   `gorm:"column:document_id"`
	Title       string    `gorm:"column:title"`
	TargetPath  string    `gorm:"column:target_path"`
	Body        string    `gorm:"column:body"`
	Status      string    `gorm:"column:status"`
	Version     int64     `gorm:"column:version"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (gormWorkingDraftRecord) TableName() string { return "authoring.working_draft" }

type gormWorkingDraftCommandRecord struct {
	WorkspaceID                 string     `gorm:"column:workspace_id"`
	IdempotencyKey              string     `gorm:"column:idempotency_key"`
	RequestHash                 string     `gorm:"column:request_hash"`
	CommandType                 string     `gorm:"column:command_type"`
	WorkingDraftID              *string    `gorm:"column:working_draft_id"`
	ExpectedVersion             int64      `gorm:"column:expected_version"`
	ResultDraftVersion          *int64     `gorm:"column:result_draft_version"`
	DocumentID                  *string    `gorm:"column:document_id"`
	DocumentVersion             *int64     `gorm:"column:document_version"`
	ArticleRevisionID           *string    `gorm:"column:article_revision_id"`
	RevisionNo                  *int       `gorm:"column:revision_no"`
	ParentRevisionID            *string    `gorm:"column:parent_revision_id"`
	ResponseDraftCreatedAt      *time.Time `gorm:"column:response_draft_created_at"`
	ResponseDocumentCreatedAt   *time.Time `gorm:"column:response_document_created_at"`
	ResponseDocumentLifecycle   *string    `gorm:"column:response_document_lifecycle"`
	ResponsePublishedRevisionID *string    `gorm:"column:response_published_revision_id"`
	ResponseTitle               *string    `gorm:"column:response_title"`
	ResponseTargetPath          *string    `gorm:"column:response_target_path"`
	ResponseContentHash         *string    `gorm:"column:response_content_hash"`
	ProposalID                  *string    `gorm:"column:proposal_id"`
	ProposalRevisionID          *string    `gorm:"column:proposal_revision_id"`
	CreatedAt                   time.Time  `gorm:"column:created_at;autoCreateTime:false"`
}

func (gormWorkingDraftCommandRecord) TableName() string {
	return "authoring.working_draft_command"
}

type gormDocumentRecord struct {
	ID                         string    `gorm:"column:id"`
	WorkspaceID                string    `gorm:"column:workspace_id"`
	CanonicalPath              string    `gorm:"column:canonical_path"`
	Title                      string    `gorm:"column:title"`
	LifecycleStatus            string    `gorm:"column:lifecycle_status"`
	CurrentPublishedRevisionID *string   `gorm:"column:current_published_revision_id"`
	Version                    int64     `gorm:"column:version"`
	CreatedAt                  time.Time `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt                  time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (gormDocumentRecord) TableName() string { return "core.document" }

type gormArticleRevisionRecord struct {
	ID               string    `gorm:"column:id"`
	WorkspaceID      string    `gorm:"column:workspace_id"`
	DocumentID       string    `gorm:"column:document_id"`
	SourceVersionID  *string   `gorm:"column:source_version_id"`
	ParentRevisionID *string   `gorm:"column:parent_revision_id"`
	RevisionNo       int       `gorm:"column:revision_no"`
	Content          string    `gorm:"column:content"`
	ContentHash      string    `gorm:"column:content_hash"`
	Status           string    `gorm:"column:status"`
	OptimizationMode string    `gorm:"column:optimization_mode"`
	GitCommit        *string   `gorm:"column:git_commit"`
	CreatedByType    string    `gorm:"column:created_by_type"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (gormArticleRevisionRecord) TableName() string { return "core.article_revision" }

type gormPublicationReservationRecord struct {
	ID                     string     `gorm:"column:id"`
	WorkspaceID            string     `gorm:"column:workspace_id"`
	DocumentID             string     `gorm:"column:document_id"`
	ArticleRevisionID      string     `gorm:"column:article_revision_id"`
	IdempotencyKey         string     `gorm:"column:idempotency_key"`
	RequestHash            string     `gorm:"column:request_hash"`
	ProposalIdempotencyKey string     `gorm:"column:proposal_idempotency_key"`
	TargetPath             string     `gorm:"column:target_path"`
	ContentHash            string     `gorm:"column:content_hash"`
	TargetMode             string     `gorm:"column:target_mode"`
	BaseVersion            string     `gorm:"column:base_version"`
	AbsenceToken           string     `gorm:"column:absence_token"`
	Status                 string     `gorm:"column:status"`
	ErrorCode              string     `gorm:"column:error_code"`
	CreatedAt              time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt              time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	ClosedAt               *time.Time `gorm:"column:closed_at"`
	AbandonedAt            *time.Time `gorm:"column:abandoned_at"`
}

func (gormPublicationReservationRecord) TableName() string {
	return "authoring.document_publication_reservation"
}

type gormPublicationBindingRecord struct {
	ID                 string     `gorm:"column:id"`
	ReservationID      string     `gorm:"column:reservation_id"`
	WorkspaceID        string     `gorm:"column:workspace_id"`
	DocumentID         string     `gorm:"column:document_id"`
	ArticleRevisionID  string     `gorm:"column:article_revision_id"`
	ProposalID         string     `gorm:"column:proposal_id"`
	ProposalRevisionID string     `gorm:"column:proposal_revision_id"`
	TargetPath         string     `gorm:"column:target_path"`
	ContentHash        string     `gorm:"column:content_hash"`
	TargetMode         string     `gorm:"column:target_mode"`
	AbsenceToken       string     `gorm:"column:absence_token"`
	Status             string     `gorm:"column:status"`
	GitCommit          *string    `gorm:"column:git_commit"`
	ErrorCode          string     `gorm:"column:error_code"`
	Version            int64      `gorm:"column:version"`
	CreatedAt          time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	PublishedAt        *time.Time `gorm:"column:published_at"`
}

func (gormPublicationBindingRecord) TableName() string {
	return "authoring.document_publication_binding"
}

type gormRestorePublicationRecord struct {
	WritebackExecutionID    string    `gorm:"column:writeback_execution_id"`
	WorkspaceID             string    `gorm:"column:workspace_id"`
	DocumentID              string    `gorm:"column:document_id"`
	ArticleRevisionID       string    `gorm:"column:article_revision_id"`
	ProposalID              string    `gorm:"column:proposal_id"`
	ProposalRevisionID      string    `gorm:"column:proposal_revision_id"`
	ProposalCommitID        string    `gorm:"column:proposal_commit_id"`
	ExpectedDocumentVersion int64     `gorm:"column:expected_document_version"`
	TargetPath              string    `gorm:"column:target_path"`
	CurrentContentHash      string    `gorm:"column:current_content_hash"`
	TargetContentHash       string    `gorm:"column:target_content_hash"`
	GitCommit               string    `gorm:"column:git_commit"`
	CreatedAt               time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (gormRestorePublicationRecord) TableName() string {
	return "authoring.document_restore_publication"
}

type gormProposalRecord struct {
	ID             string `gorm:"column:id"`
	WorkspaceID    string `gorm:"column:workspace_id"`
	ProposalType   string `gorm:"column:proposal_type"`
	IdempotencyKey string `gorm:"column:idempotency_key"`
	Status         string `gorm:"column:status"`
}

func (gormProposalRecord) TableName() string { return "change_control.proposal" }

type gormProposalRevisionRecord struct {
	ID                             string  `gorm:"column:id"`
	ProposalID                     string  `gorm:"column:proposal_id"`
	TargetPath                     string  `gorm:"column:target_path"`
	TargetMode                     string  `gorm:"column:target_mode"`
	BaseHash                       string  `gorm:"column:base_hash"`
	Content                        string  `gorm:"column:content"`
	SchemaVersion                  string  `gorm:"column:schema_version"`
	RestoreWorkspaceID             *string `gorm:"column:restore_workspace_id"`
	RestoreDocumentID              *string `gorm:"column:restore_document_id"`
	RestoreExpectedDocumentVersion *int64  `gorm:"column:restore_expected_document_version"`
	RestoreCurrentContentHash      *string `gorm:"column:restore_current_content_hash"`
	RestoreTargetContentHash       *string `gorm:"column:restore_target_content_hash"`
}

func (gormProposalRevisionRecord) TableName() string { return "change_control.proposal_revision" }

type gormProposalCommitRecord struct {
	ID                   string    `gorm:"column:id"`
	WorkspaceID          string    `gorm:"column:workspace_id"`
	ProposalID           string    `gorm:"column:proposal_id"`
	RevisionID           string    `gorm:"column:revision_id"`
	WritebackExecutionID string    `gorm:"column:writeback_execution_id"`
	GitCommit            string    `gorm:"column:git_commit"`
	TargetPath           string    `gorm:"column:target_path"`
	TargetMode           string    `gorm:"column:target_mode"`
	ResultHash           string    `gorm:"column:result_hash"`
	CreatedAt            time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (gormProposalCommitRecord) TableName() string { return "change_control.proposal_commit" }
