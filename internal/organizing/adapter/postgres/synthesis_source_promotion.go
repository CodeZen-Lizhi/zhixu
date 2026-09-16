package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

type synthesisSourcePromotionModel struct {
	WorkspaceID, IdempotencyKey, RequestID, SourceID, SourceVersionID, ProfileRevisionID string
}

func (synthesisSourcePromotionModel) TableName() string {
	return "organizing.synthesis_source_promotion"
}

func readSynthesisSourcePromotion(tx *gorm.DB, c app.PromoteSynthesisSourceCommand) (app.SynthesisSourcePromotion, bool, error) {
	var result app.SynthesisSourcePromotion
	var row synthesisSourcePromotionModel
	err := tx.Where("workspace_id=? AND idempotency_key=?", string(c.WorkspaceID), c.IdempotencyKey).Take(&row).Error
	if gormNoRows(err) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if row.SourceVersionID != string(c.SourceVersionID) {
		return result, false, goalConflict()
	}
	goal, err := loadSynthesisGoal(tx, c.WorkspaceID, foundation.ID(row.RequestID), false)
	if err != nil {
		return result, false, err
	}
	result.Request, err = goal.request()
	result.SourceVersionID = c.SourceVersionID
	result.Replayed = true
	return result, err == nil, err
}

func (s *GORMSynthesisStore) FindSynthesisSourcePromotion(ctx context.Context, c app.PromoteSynthesisSourceCommand) (app.SynthesisSourcePromotion, bool, error) {
	if err := c.Validate(); err != nil {
		return app.SynthesisSourcePromotion{}, false, err
	}
	if err := s.ready(ctx, c.WorkspaceID); err != nil {
		return app.SynthesisSourcePromotion{}, false, err
	}
	result, found, err := readSynthesisSourcePromotion(s.database.WithContext(ctx), c)
	return result, found, synthesisDBError(ctx, err)
}

func (s *GORMSynthesisStore) CreateSynthesisSourcePromotion(ctx context.Context, c app.PromoteSynthesisSourceCommand, item app.SynthesisGoalCatalogItem, reference domain.SynthesisSourceRef) (app.SynthesisSourcePromotion, error) {
	var result app.SynthesisSourcePromotion
	if err := c.Validate(); err != nil {
		return result, err
	}
	if err := s.ready(ctx, c.WorkspaceID); err != nil {
		return result, err
	}
	if item.Source.WorkspaceID != c.WorkspaceID || item.Source.SourceVersionID != c.SourceVersionID || reference.Validate() != nil || reference.Source != item.Source || reference.Title != item.Title || len(item.Directory.Points) == 0 || len(item.Directory.Points[0].SourceSpanIDs) == 0 || reference.SourceSpanID != item.Directory.Points[0].SourceSpanIDs[0] {
		return result, goalInvalid()
	}
	err := s.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "source-promotion:"+string(c.WorkspaceID)+":"+c.IdempotencyKey).Error; err != nil {
			return err
		}
		existing, found, err := readSynthesisSourcePromotion(tx, c)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		if err := s.dependencies.Sources.VerifySynthesisSourcesScoped(ctx, scope, c.WorkspaceID, []domain.SynthesisSourceRef{reference}); err != nil {
			return err
		}
		// 提升回执负责命令重放；检查回执后才分配内部目标键，避免普通目标预占此命令。
		goalKey, err := foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return err
		}
		created, err := createSynthesisGoalInTransaction(tx, app.CreateSynthesisGoalCommand{WorkspaceID: c.WorkspaceID, Goal: app.SynthesisSourcePromotionGoal(item.Title), IdempotencyKey: "source-promotion:" + string(goalKey)})
		if err != nil {
			return err
		}
		if created.Replayed {
			return goalConflict()
		}
		frozen, err := freezeSynthesisGoalCatalogInTransaction(tx, created.Request, app.SynthesisGoalCatalogPage{Items: []app.SynthesisGoalCatalogItem{item}})
		if err != nil {
			return err
		}
		row := synthesisSourcePromotionModel{WorkspaceID: string(c.WorkspaceID), IdempotencyKey: c.IdempotencyKey, RequestID: string(created.Request.ID), SourceID: string(item.Source.SourceID), SourceVersionID: string(c.SourceVersionID), ProfileRevisionID: string(item.Directory.ProfileRevisionID)}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		result = app.SynthesisSourcePromotion{SourceVersionID: c.SourceVersionID, Request: frozen.Request}
		return nil
	})
	return result, err
}

var _ app.SynthesisSourcePromotionStore = (*GORMSynthesisStore)(nil)
