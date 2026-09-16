package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// 提升操作从一份冻结原始资料创建经审核流程处理的主笔记候选；
// 后续扩展仍须经过锚点范围与关联审核。
type PromoteSynthesisSourceCommand struct {
	WorkspaceID, SourceVersionID foundation.ID
	IdempotencyKey               string
}

func (c PromoteSynthesisSourceCommand) Validate() error {
	if ValidateAnchorCommand(c.WorkspaceID, c.IdempotencyKey) != nil || !validID(c.SourceVersionID) {
		return invalid("SYNTHESIS_SOURCE_PROMOTION_INVALID", "source promotion is invalid")
	}
	return nil
}

type SynthesisSourcePromotion struct {
	SourceVersionID foundation.ID
	Request         SynthesisGoalRequest
	Replayed        bool
}
type SynthesisSourcePromotionStore interface {
	FindSynthesisSourcePromotion(context.Context, PromoteSynthesisSourceCommand) (SynthesisSourcePromotion, bool, error)
	CreateSynthesisSourcePromotion(context.Context, PromoteSynthesisSourceCommand, SynthesisGoalCatalogItem, domain.SynthesisSourceRef) (SynthesisSourcePromotion, error)
}
type SynthesisSourcePromotionService struct {
	store   SynthesisSourcePromotionStore
	catalog SynthesisGoalCatalogReader
	sources AnchorDiscoverySourceResolver
}

func NewSynthesisSourcePromotionService(store SynthesisSourcePromotionStore, catalog SynthesisGoalCatalogReader, sources AnchorDiscoverySourceResolver) (*SynthesisSourcePromotionService, error) {
	if nilSynthesisDependency(store) || nilSynthesisDependency(catalog) || nilSynthesisDependency(sources) {
		return nil, invalid("SYNTHESIS_SOURCE_PROMOTION_UNAVAILABLE", "source promotion dependencies are unavailable")
	}
	return &SynthesisSourcePromotionService{store: store, catalog: catalog, sources: sources}, nil
}
func (s *SynthesisSourcePromotionService) PromoteSynthesisSource(ctx context.Context, c PromoteSynthesisSourceCommand) (SynthesisSourcePromotion, error) {
	var result SynthesisSourcePromotion
	if err := c.Validate(); err != nil {
		return result, err
	}
	if s == nil || ctx == nil {
		return result, invalid("SYNTHESIS_SOURCE_PROMOTION_INVALID", "source promotion is unavailable")
	}
	existing, found, err := s.store.FindSynthesisSourcePromotion(ctx, c)
	if err != nil || found {
		return existing, err
	}
	page, err := s.catalog.ReadSynthesisGoalCatalog(ctx, SynthesisGoalCatalogQuery{WorkspaceID: c.WorkspaceID, SourceVersionID: c.SourceVersionID, Limit: 1})
	if err != nil {
		return result, err
	}
	if page.DeferredCode != "" {
		return result, foundation.NewError(foundation.ErrorDependencyUnavailable, page.DeferredCode, true, errors.New("source analysis has not completed"))
	}
	if len(page.Items) != 1 || page.NextAfterSourceID != nil || len(page.Items[0].Directory.Points) == 0 {
		return result, foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_PROMOTION_NOT_READY", true, errors.New("source is unavailable or has no analyzed knowledge"))
	}
	item := page.Items[0]
	if item.Source.WorkspaceID != c.WorkspaceID || item.Source.SourceVersionID != c.SourceVersionID {
		return result, goalSelectionInvalid()
	}
	if len(item.Directory.Points[0].SourceSpanIDs) == 0 {
		return result, goalSelectionInvalid()
	}
	refs, err := s.sources.ResolveAnchorDiscoverySources(ctx, item.Source, []foundation.ID{item.Directory.Points[0].SourceSpanIDs[0]})
	if err != nil {
		return result, err
	}
	if len(refs) != 1 {
		return result, goalSelectionInvalid()
	}
	return s.store.CreateSynthesisSourcePromotion(ctx, c, item, refs[0])
}

func SynthesisSourcePromotionGoal(title string) string {
	return "以《" + title + "》的全部知识为基础建立主笔记，保留原笔记的主题、用途、适用条件和各模块语境。"
}
