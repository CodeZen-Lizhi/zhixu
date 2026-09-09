package postgres

import "time"

type gormGeneratedDocumentRecord struct {
	DocumentID  string    `gorm:"column:document_id"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	OriginKind  string    `gorm:"column:origin_kind"`
	OriginID    string    `gorm:"column:origin_id"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (gormGeneratedDocumentRecord) TableName() string { return "authoring.generated_document" }

type gormGeneratedRevisionRecord struct {
	ArticleRevisionID       string    `gorm:"column:article_revision_id"`
	WorkspaceID             string    `gorm:"column:workspace_id"`
	DocumentID              string    `gorm:"column:document_id"`
	OriginKind              string    `gorm:"column:origin_kind"`
	OriginID                string    `gorm:"column:origin_id"`
	OriginRevisionID        string    `gorm:"column:origin_revision_id"`
	ProjectionHash          string    `gorm:"column:projection_hash"`
	IdempotencyKey          string    `gorm:"column:idempotency_key"`
	RequestHash             string    `gorm:"column:request_hash"`
	ExpectedDocumentVersion int64     `gorm:"column:expected_document_version"`
	DocumentVersion         int64     `gorm:"column:document_version"`
	RevisionNo              int       `gorm:"column:revision_no"`
	ParentRevisionID        *string   `gorm:"column:parent_revision_id"`
	ContentHash             string    `gorm:"column:content_hash"`
	Title                   string    `gorm:"column:title"`
	TargetPath              string    `gorm:"column:target_path"`
	DocumentCreatedAt       time.Time `gorm:"column:document_created_at;autoCreateTime:false"`
	DocumentLifecycle       string    `gorm:"column:document_lifecycle"`
	PublishedRevisionID     *string   `gorm:"column:published_revision_id"`
	CreatedAt               time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (gormGeneratedRevisionRecord) TableName() string { return "authoring.generated_article_revision" }

const generatedRevisionColumns = `article_revision_id,workspace_id,document_id,origin_kind,origin_id,
origin_revision_id,projection_hash,idempotency_key,request_hash,expected_document_version,document_version,
revision_no,parent_revision_id,content_hash,title,target_path,document_created_at,document_lifecycle,
published_revision_id,created_at`

type gormGeneratedRetirementRecord struct {
	WorkspaceID             string    `gorm:"column:workspace_id"`
	IdempotencyKey          string    `gorm:"column:idempotency_key"`
	RequestHash             string    `gorm:"column:request_hash"`
	PublicationID           string    `gorm:"column:publication_id"`
	DocumentID              string    `gorm:"column:document_id"`
	ArticleRevisionID       string    `gorm:"column:article_revision_id"`
	OriginKind              string    `gorm:"column:origin_kind"`
	OriginID                string    `gorm:"column:origin_id"`
	OriginRevisionID        string    `gorm:"column:origin_revision_id"`
	ProjectionHash          string    `gorm:"column:projection_hash"`
	ExpectedDocumentVersion int64     `gorm:"column:expected_document_version"`
	ExpectedProposalVersion int64     `gorm:"column:expected_proposal_version"`
	CreatedAt               time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (gormGeneratedRetirementRecord) TableName() string {
	return "authoring.generated_publication_retirement"
}

const generatedRetirementColumns = `workspace_id,idempotency_key,request_hash,publication_id,document_id,
article_revision_id,origin_kind,origin_id,origin_revision_id,projection_hash,expected_document_version,
expected_proposal_version,created_at`
