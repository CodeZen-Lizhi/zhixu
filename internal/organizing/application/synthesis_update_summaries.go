package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const MaxSynthesisUpdateSummaryNotes = 50

// 复核计数表示持久观察，不表示已生成更新或裁决结果。
type SynthesisUpdateSummary struct {
	RevisionID        foundation.ID `json:"revision_id"`
	SourceReviewCount int64         `json:"source_review_count"`
	BodyReviewCount   int64         `json:"body_review_count"`
}
type SynthesisNoteUpdateSummary struct {
	NoteID              foundation.ID            `json:"note_id"`
	CurrentRevisionID   foundation.ID            `json:"current_revision_id"`
	PublishedRevisionID foundation.ID            `json:"published_revision_id"`
	Items               []SynthesisUpdateSummary `json:"items"`
}
type SynthesisUpdateSummaryQuery struct {
	WorkspaceID foundation.ID
	NoteIDs     []foundation.ID
}
type SynthesisUpdateSummaries struct {
	WorkspaceID foundation.ID                `json:"workspace_id"`
	Items       []SynthesisNoteUpdateSummary `json:"items"`
}
type SynthesisUpdateSummaryReader interface {
	ReadSynthesisUpdateSummaries(context.Context, SynthesisUpdateSummaryQuery) (SynthesisUpdateSummaries, error)
}

func (service *SynthesisService) ReadSynthesisUpdateSummaries(ctx context.Context, query SynthesisUpdateSummaryQuery) (SynthesisUpdateSummaries, error) {
	if err := service.ready(ctx, query.WorkspaceID); err != nil {
		return SynthesisUpdateSummaries{}, err
	}
	reader, ok := service.dependencies.Store.(SynthesisUpdateSummaryReader)
	if !ok || nilSynthesisDependency(reader) {
		return SynthesisUpdateSummaries{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("update summary reader unavailable"))
	}
	return reader.ReadSynthesisUpdateSummaries(ctx, query)
}
