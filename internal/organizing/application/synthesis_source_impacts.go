package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// 来源影响不会修改修订，也不决定其中知识声明的真伪。
type SynthesisSourceImpact struct {
	ID                   foundation.ID             `json:"id"`
	Reason               string                    `json:"reason"`
	DetectedAt           time.Time                 `json:"detected_at"`
	CurrentlyUnavailable bool                      `json:"currently_unavailable"`
	Reference            domain.SynthesisSourceRef `json:"reference"`
	ItemIDs              []foundation.ID           `json:"item_ids"`
}
type SynthesisSourceImpactQuery struct{ WorkspaceID, NoteID, RevisionID foundation.ID }
type SynthesisSourceImpacts struct {
	WorkspaceID foundation.ID           `json:"workspace_id"`
	NoteID      foundation.ID           `json:"note_id"`
	RevisionID  foundation.ID           `json:"revision_id"`
	Items       []SynthesisSourceImpact `json:"items"`
}
type SynthesisSourceImpactReader interface {
	ReadSynthesisSourceImpacts(context.Context, SynthesisSourceImpactQuery) (SynthesisSourceImpacts, error)
}
type SynthesisSourceImpactReconciler interface {
	ReconcileSynthesisSourceImpacts(context.Context, int) (int, error)
}

func (service *SynthesisService) ReadSynthesisSourceImpacts(ctx context.Context, query SynthesisSourceImpactQuery) (SynthesisSourceImpacts, error) {
	if err := service.ready(ctx, query.WorkspaceID, query.NoteID, query.RevisionID); err != nil {
		return SynthesisSourceImpacts{}, err
	}
	reader, ok := service.dependencies.Store.(SynthesisSourceImpactReader)
	if !ok || nilSynthesisDependency(reader) {
		return SynthesisSourceImpacts{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("source impact reader is unavailable"))
	}
	return reader.ReadSynthesisSourceImpacts(ctx, query)
}
