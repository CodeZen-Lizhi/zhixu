package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// 必须且只能提供一个父级选择条件，AfterID 属于同一个集合。
type SynthesisSourceReviewQuery struct {
	WorkspaceID, ProcessingID, NoteID, RevisionID, AfterID foundation.ID
	Limit                                                  int
}
type SynthesisSourceReviewPage struct {
	WorkspaceID  foundation.ID               `json:"workspace_id"`
	ProcessingID foundation.ID               `json:"processing_id,omitempty"`
	NoteID       foundation.ID               `json:"note_id,omitempty"`
	RevisionID   foundation.ID               `json:"revision_id,omitempty"`
	Items        []SynthesisSourceReviewView `json:"items"`
	NextAfterID  foundation.ID               `json:"next_after_id,omitempty"`
}
type SynthesisSourceReviewReader interface {
	ListSourceReviewViews(context.Context, SynthesisSourceReviewQuery) (SynthesisSourceReviewPage, error)
	GetSourceReviewView(context.Context, foundation.ID, foundation.ID) (SynthesisSourceReviewView, error)
	OpenSourceReviewEvidence(context.Context, foundation.ID, foundation.ID, foundation.ID) (SynthesisSourceReviewEvidenceView, SynthesisSourceView, error)
}
