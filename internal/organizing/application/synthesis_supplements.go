package application

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisSourceSupplement 是修订冻结之后附加的只追加证据，
// 不属于该修订的条目或投影哈希。
type SynthesisSourceSupplement struct {
	ID               foundation.ID             `json:"id"`
	WorkspaceID      foundation.ID             `json:"workspace_id"`
	NoteID           foundation.ID             `json:"note_id"`
	BaseRevisionID   foundation.ID             `json:"base_revision_id"`
	ItemID           foundation.ID             `json:"item_id"`
	Slot             string                    `json:"slot"`
	AlternativeIndex int                       `json:"alternative_index"`
	ProcessingID     foundation.ID             `json:"processing_id"`
	Reference        domain.SynthesisSourceRef `json:"reference"`
	CreatedAt        time.Time                 `json:"created_at"`
}

func (value SynthesisSourceSupplement) Validate() error {
	validSlot := value.Slot == "CONFLICT" && value.AlternativeIndex >= 0 && value.AlternativeIndex < 4 ||
		(value.Slot == "FACT" || value.Slot == "GAP_CONTEXT" || value.Slot == "GAP_RESOLUTION") && value.AlternativeIndex == -1
	if !validID(value.ID) || !validID(value.WorkspaceID) || !validID(value.NoteID) || !validID(value.BaseRevisionID) || !validID(value.ItemID) || !validID(value.ProcessingID) || value.CreatedAt.IsZero() || !validSlot || value.Reference.Validate() != nil || value.Reference.Source.WorkspaceID != value.WorkspaceID {
		return invalid(domain.ErrorCodeSynthesisSourceInvalid, "synthesis supplement is invalid")
	}
	return nil
}

// MatchesItem 根据当前不可变条目结构检查附加关系。
// 条目身份跨修订保留，因此基线修订可以是旧版本。
func (value SynthesisSourceSupplement) MatchesItem(items []domain.SynthesisItem) bool {
	for _, item := range items {
		if item.ID != value.ItemID {
			continue
		}
		switch value.Slot {
		case "FACT":
			return item.Kind == domain.SynthesisFactItem
		case "CONFLICT":
			return item.Kind == domain.SynthesisConflictItem && value.AlternativeIndex >= 0 && value.AlternativeIndex < len(item.Conflict.Alternatives)
		case "GAP_CONTEXT":
			return item.Kind == domain.SynthesisGapItem
		case "GAP_RESOLUTION":
			return item.Kind == domain.SynthesisGapItem && item.Gap.Resolution != nil
		}
	}
	return false
}

type SynthesisSupplementListQuery struct {
	WorkspaceID foundation.ID
	NoteID      foundation.ID
	Limit       int
	BeforeTime  *time.Time
	BeforeID    foundation.ID
}

type SynthesisSupplementPage struct {
	Items    []SynthesisSourceSupplement
	NextTime *time.Time
	NextID   foundation.ID
}

// SynthesisSupplementStore 与不可变修订读取器分离。
type SynthesisSupplementStore interface {
	ListSynthesisSupplements(context.Context, SynthesisSupplementListQuery) (SynthesisSupplementPage, error)
	GetSynthesisSupplement(context.Context, foundation.ID, foundation.ID, foundation.ID) (SynthesisSourceSupplement, error)
}

func (service *SynthesisService) ListSupplements(ctx context.Context, query SynthesisSupplementListQuery) (SynthesisSupplementPage, error) {
	if err := service.ready(ctx, query.WorkspaceID, query.NoteID); err != nil {
		return SynthesisSupplementPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = DefaultSynthesisListLimit
	}
	if query.Limit < 1 || query.Limit > MaxSynthesisListLimit || (query.BeforeTime == nil) != (query.BeforeID == "") || query.BeforeTime != nil && (query.BeforeTime.IsZero() || !validID(query.BeforeID)) {
		return SynthesisSupplementPage{}, invalid(domain.ErrorCodeSynthesisInvalid, "synthesis supplement page is invalid")
	}
	store, ok := service.dependencies.Store.(SynthesisSupplementStore)
	if !ok {
		return SynthesisSupplementPage{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, false, errors.New("synthesis supplements are unavailable"))
	}
	return store.ListSynthesisSupplements(ctx, query)
}

func (service *SynthesisService) OpenSupplement(ctx context.Context, workspaceID, noteID, supplementID foundation.ID) (SynthesisSourceSupplement, SynthesisSourceView, error) {
	if err := service.ready(ctx, workspaceID, noteID, supplementID); err != nil {
		return SynthesisSourceSupplement{}, SynthesisSourceView{}, err
	}
	store, ok := service.dependencies.Store.(SynthesisSupplementStore)
	if !ok {
		return SynthesisSourceSupplement{}, SynthesisSourceView{}, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, false, errors.New("synthesis supplements are unavailable"))
	}
	supplement, err := store.GetSynthesisSupplement(ctx, workspaceID, noteID, supplementID)
	if err != nil {
		return supplement, SynthesisSourceView{}, err
	}
	if supplement.WorkspaceID != workspaceID || supplement.NoteID != noteID || supplement.ID != supplementID || supplement.Reference.Source.WorkspaceID != workspaceID || supplement.Reference.Validate() != nil {
		return SynthesisSourceSupplement{}, SynthesisSourceView{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisConsistency, false, errors.New("synthesis supplement binding changed"))
	}
	view, err := service.dependencies.Sources.OpenSynthesisSource(ctx, supplement.Reference)
	if err == nil && (view.Reference != supplement.Reference || view.Validate(workspaceID) != nil) {
		err = foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeSynthesisConsistency, false, errors.New("source reader changed supplementary reference"))
	}
	return supplement, view, err
}
