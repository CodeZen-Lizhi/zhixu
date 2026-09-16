package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SynthesisBodyImpact 将观察绑定到不可变的下游基线及两个精确的上游发布。
// 它不授予正文替换权限。
type SynthesisBodyImpact struct {
	ID                    foundation.ID `json:"id"`
	WorkspaceID           foundation.ID `json:"workspace_id"`
	NoteID                foundation.ID `json:"note_id"`
	BaseRevisionID        foundation.ID `json:"base_revision_id"`
	ItemID                foundation.ID `json:"item_id"`
	UpstreamNoteID        foundation.ID `json:"upstream_note_id"`
	UpstreamRevisionID    foundation.ID `json:"upstream_revision_id"`
	UpstreamItemID        foundation.ID `json:"upstream_item_id"`
	UpstreamPublicationID foundation.ID `json:"upstream_publication_id"`
	PublicationID         foundation.ID `json:"publication_id"`
	PublishedRevisionID   foundation.ID `json:"published_revision_id"`
	EventID               foundation.ID `json:"event_id"`
	Reason                string        `json:"reason"`
	DetectedAt            time.Time     `json:"detected_at"`
}

type SynthesisBodyImpactQuery struct {
	WorkspaceID, NoteID, BaseRevisionID foundation.ID
	AfterID                             foundation.ID
	Limit                               int
}

type SynthesisBodyImpacts struct {
	Items       []SynthesisBodyImpact `json:"items"`
	NextAfterID foundation.ID         `json:"next_after_id,omitempty"`
}

type SynthesisBodyImpactReader interface {
	ReadSynthesisBodyImpacts(context.Context, SynthesisBodyImpactQuery) (SynthesisBodyImpacts, error)
}
type SynthesisBodyImpactReconciler interface {
	ReconcileSynthesisBodyImpacts(context.Context, int) (int, error)
}

func (service *SynthesisService) ReadSynthesisBodyImpacts(ctx context.Context, query SynthesisBodyImpactQuery) (SynthesisBodyImpacts, error) {
	if err := service.ready(ctx, query.WorkspaceID, query.NoteID, query.BaseRevisionID); err != nil {
		return SynthesisBodyImpacts{}, err
	}
	reader, ok := service.dependencies.Store.(SynthesisBodyImpactReader)
	if !ok || nilSynthesisDependency(reader) {
		return SynthesisBodyImpacts{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("body impact reader is unavailable"))
	}
	return reader.ReadSynthesisBodyImpacts(ctx, query)
}
