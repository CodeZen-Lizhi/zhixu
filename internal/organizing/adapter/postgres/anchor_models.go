package postgres

import (
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type anchorModel struct {
	ID              string    `gorm:"column:id"`
	WorkspaceID     string    `gorm:"column:workspace_id"`
	NoteID          string    `gorm:"column:note_id"`
	BasisRevisionID string    `gorm:"column:basis_revision_id"`
	Title           string    `gorm:"column:title"`
	ScopeVersion    int64     `gorm:"column:scope_version"`
	Version         int64     `gorm:"column:version"`
	CreatedAt       time.Time `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt       time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (anchorModel) TableName() string { return "organizing.knowledge_anchor" }

type anchorScopeModel struct {
	AnchorID    string          `gorm:"column:anchor_id"`
	WorkspaceID string          `gorm:"column:workspace_id"`
	Version     int64           `gorm:"column:version"`
	Scope       organizingJSONB `gorm:"column:scope"`
	ProposalID  *string         `gorm:"column:proposal_id"`
	CreatedAt   time.Time       `gorm:"column:created_at;autoCreateTime:false"`
}

func (anchorScopeModel) TableName() string { return "organizing.anchor_scope_revision" }

type anchorProposalModel struct {
	ID           string           `gorm:"column:id"`
	WorkspaceID  string           `gorm:"column:workspace_id"`
	AnchorID     string           `gorm:"column:anchor_id"`
	Kind         string           `gorm:"column:kind"`
	ScopeVersion int64            `gorm:"column:scope_version"`
	Suggested    *organizingJSONB `gorm:"column:suggested"`
	Reason       string           `gorm:"column:reason"`
	ModelRunID   string           `gorm:"column:model_run_id"`
	CreatedAt    time.Time        `gorm:"column:created_at;autoCreateTime:false"`
}

func (anchorProposalModel) TableName() string { return "organizing.anchor_proposal" }

type anchorEvidenceModel struct {
	WorkspaceID       string `gorm:"column:workspace_id"`
	AnchorID          string `gorm:"column:anchor_id"`
	ProposalID        string `gorm:"column:proposal_id"`
	SourceID          string `gorm:"column:source_id"`
	SourceVersionID   string `gorm:"column:source_version_id"`
	ContentArtifactID string `gorm:"column:content_artifact_id"`
	ParseProjectionID string `gorm:"column:parse_projection_id"`
	SourceSpanID      string `gorm:"column:source_span_id"`
	ContentHash       string `gorm:"column:content_hash"`
	ExcerptHash       string `gorm:"column:excerpt_hash"`
	Title             string `gorm:"column:title"`
}

func (anchorEvidenceModel) TableName() string { return "organizing.anchor_proposal_evidence" }
func (e anchorEvidenceModel) ref() domain.SynthesisSourceRef {
	return domain.SynthesisSourceRef{Source: domain.SynthesisSourceVersion{WorkspaceID: foundation.ID(e.WorkspaceID), SourceID: foundation.ID(e.SourceID), SourceVersionID: foundation.ID(e.SourceVersionID), ContentArtifactID: foundation.ID(e.ContentArtifactID), ParseProjectionID: foundation.ID(e.ParseProjectionID), ContentHash: e.ContentHash}, SourceSpanID: foundation.ID(e.SourceSpanID), ExcerptHash: e.ExcerptHash, Title: e.Title}
}

type anchorDecisionModel struct {
	ProposalID    string    `gorm:"column:proposal_id"`
	WorkspaceID   string    `gorm:"column:workspace_id"`
	AnchorID      string    `gorm:"column:anchor_id"`
	Decision      string    `gorm:"column:decision"`
	AnchorVersion int64     `gorm:"column:anchor_version"`
	ReceiptKey    string    `gorm:"column:receipt_key"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (anchorDecisionModel) TableName() string { return "organizing.anchor_decision" }

type anchorFusionRequestModel struct {
	ID                 string          `gorm:"column:id"`
	WorkspaceID        string          `gorm:"column:workspace_id"`
	AnchorID           string          `gorm:"column:anchor_id"`
	NoteID             string          `gorm:"column:note_id"`
	ProposalID         string          `gorm:"column:proposal_id"`
	ScopeVersion       int64           `gorm:"column:scope_version"`
	SourceEventID      string          `gorm:"column:source_event_id"`
	SourceID           string          `gorm:"column:source_id"`
	SourceVersionID    string          `gorm:"column:source_version_id"`
	ContentArtifactID  string          `gorm:"column:content_artifact_id"`
	ParseProjectionID  string          `gorm:"column:parse_projection_id"`
	SourceContentHash  string          `gorm:"column:source_content_hash"`
	IngestionAttemptID string          `gorm:"column:ingestion_attempt_id"`
	SourceOccurredAt   time.Time       `gorm:"column:source_occurred_at"`
	AllowedSources     organizingJSONB `gorm:"column:allowed_sources"`
	ProcessingID       *string         `gorm:"column:processing_id"`
	Status             string          `gorm:"column:status"`
	CreatedAt          time.Time       `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt          time.Time       `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (anchorFusionRequestModel) TableName() string { return "organizing.anchor_fusion_request" }

type anchorReceiptModel struct {
	WorkspaceID    string          `gorm:"column:workspace_id"`
	IdempotencyKey string          `gorm:"column:idempotency_key"`
	RequestHash    string          `gorm:"column:request_hash"`
	Operation      string          `gorm:"column:operation"`
	AnchorID       string          `gorm:"column:anchor_id"`
	Result         organizingJSONB `gorm:"column:result"`
	CreatedAt      time.Time       `gorm:"column:created_at;autoCreateTime:false"`
}

func (anchorReceiptModel) TableName() string { return "organizing.anchor_receipt" }

type anchorRecommendationRequestModel struct {
	ID                   string           `gorm:"column:id"`
	WorkspaceID          string           `gorm:"column:workspace_id"`
	NoteID               string           `gorm:"column:note_id"`
	AnchorID             *string          `gorm:"column:anchor_id"`
	BasisRevisionID      string           `gorm:"column:basis_revision_id"`
	ExpectedScopeVersion *int64           `gorm:"column:expected_scope_version"`
	Kind                 string           `gorm:"column:kind"`
	Source               *organizingJSONB `gorm:"column:source"`
	Evidence             organizingJSONB  `gorm:"column:evidence"`
	RequestHash          string           `gorm:"column:request_hash"`
	WorkflowRunID        *string          `gorm:"column:workflow_run_id"`
	NodeRunID            *string          `gorm:"column:node_run_id"`
	NodeAttemptID        *string          `gorm:"column:node_attempt_id"`
	IdempotencyKey       string           `gorm:"column:idempotency_key"`
	Status               string           `gorm:"column:status"`
	ModelRunID           *string          `gorm:"column:model_run_id"`
	ModelInputHash       *string          `gorm:"column:model_input_hash"`
	ModelOutput          *[]byte          `gorm:"column:model_output"`
	ProposalID           *string          `gorm:"column:proposal_id"`
	ErrorCode            *string          `gorm:"column:error_code"`
	Retryable            bool             `gorm:"column:retryable"`
	RetryIdempotencyKey  *string          `gorm:"column:retry_idempotency_key"`
	RetryRequestHash     *string          `gorm:"column:retry_request_hash"`
	Version              int64            `gorm:"column:version"`
	CreatedAt            time.Time        `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt            time.Time        `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (anchorRecommendationRequestModel) TableName() string {
	return "organizing.anchor_recommendation_request"
}
func encodeAnchor(value any) (organizingJSONB, error) {
	b, e := json.Marshal(value)
	return organizingJSONB(b), e
}
