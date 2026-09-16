package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type synthesisSupplementModel struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	WorkspaceID       string    `gorm:"column:workspace_id;type:uuid"`
	NoteID            string    `gorm:"column:note_id;type:uuid"`
	BaseRevisionID    string    `gorm:"column:base_revision_id;type:uuid"`
	ItemID            string    `gorm:"column:item_id;type:uuid"`
	Slot              string    `gorm:"column:slot"`
	AlternativeIndex  int       `gorm:"column:alternative_index"`
	ProcessingID      string    `gorm:"column:processing_id;type:uuid"`
	SourceID          string    `gorm:"column:source_id;type:uuid"`
	SourceVersionID   string    `gorm:"column:source_version_id;type:uuid"`
	ContentArtifactID string    `gorm:"column:content_artifact_id;type:uuid"`
	ParseProjectionID string    `gorm:"column:parse_projection_id;type:uuid"`
	SourceSpanID      string    `gorm:"column:source_span_id;type:uuid"`
	ContentHash       string    `gorm:"column:content_hash"`
	ExcerptHash       string    `gorm:"column:excerpt_hash"`
	Title             string    `gorm:"column:title"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (synthesisSupplementModel) TableName() string { return "organizing.synthesis_source_supplement" }

func (row synthesisSupplementModel) value() (app.SynthesisSourceSupplement, error) {
	value := app.SynthesisSourceSupplement{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), NoteID: foundation.ID(row.NoteID), BaseRevisionID: foundation.ID(row.BaseRevisionID), ItemID: foundation.ID(row.ItemID), Slot: row.Slot, AlternativeIndex: row.AlternativeIndex, ProcessingID: foundation.ID(row.ProcessingID), CreatedAt: row.CreatedAt.UTC(), Reference: domain.SynthesisSourceRef{Source: domain.SynthesisSourceVersion{WorkspaceID: foundation.ID(row.WorkspaceID), SourceID: foundation.ID(row.SourceID), SourceVersionID: foundation.ID(row.SourceVersionID), ContentArtifactID: foundation.ID(row.ContentArtifactID), ParseProjectionID: foundation.ID(row.ParseProjectionID), ContentHash: row.ContentHash}, SourceSpanID: foundation.ID(row.SourceSpanID), ExcerptHash: row.ExcerptHash, Title: row.Title}}
	if value.Validate() != nil {
		return value, synthesisConsistency("stored supplement is invalid")
	}
	return value, nil
}

type synthesisSupportSlot struct {
	name    string
	index   int
	sources []domain.SynthesisSourceRef
}

func synthesisSlots(item domain.SynthesisItem) []synthesisSupportSlot {
	switch item.Kind {
	case domain.SynthesisFactItem:
		return []synthesisSupportSlot{{"FACT", -1, item.Fact.Sources}}
	case domain.SynthesisConflictItem:
		result := make([]synthesisSupportSlot, len(item.Conflict.Alternatives))
		for i, alt := range item.Conflict.Alternatives {
			result[i] = synthesisSupportSlot{"CONFLICT", i, alt.Sources}
		}
		return result
	case domain.SynthesisGapItem:
		result := []synthesisSupportSlot{{"GAP_CONTEXT", -1, item.Gap.Sources}}
		if item.Gap.Resolution != nil {
			result = append(result, synthesisSupportSlot{"GAP_RESOLUTION", -1, item.Gap.Resolution.Sources})
		}
		return result
	}
	return nil
}

// 只有附加到既有断言的来源才属于补源；新断言和首次缺口解决结果随其新版本存储。
func insertSynthesisSupplements(tx *gorm.DB, base domain.SynthesisRevision, items []domain.SynthesisItem, processingID foundation.ID, at time.Time) (bool, error) {
	previous := map[foundation.ID]domain.SynthesisItem{}
	for _, item := range base.Items {
		previous[item.ID] = item
	}
	rows := []synthesisSupplementModel{}
	for _, item := range items {
		old, found := previous[item.ID]
		if !found {
			continue
		}
		for _, slot := range synthesisSlots(item) {
			var oldSlot *synthesisSupportSlot
			for _, prior := range synthesisSlots(old) {
				if prior.name == slot.name && prior.index == slot.index {
					copy := prior
					oldSlot = &copy
					break
				}
			}
			if oldSlot == nil {
				continue
			}
			seen := map[string]bool{}
			for _, ref := range oldSlot.sources {
				key, _ := ref.IdentityKey()
				seen[key] = true
			}
			for _, ref := range slot.sources {
				key, err := ref.IdentityKey()
				if err != nil {
					return false, err
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				rows = append(rows, synthesisSupplementModel{WorkspaceID: string(base.WorkspaceID), NoteID: string(base.NoteID), BaseRevisionID: string(base.ID), ItemID: string(item.ID), Slot: slot.name, AlternativeIndex: slot.index, ProcessingID: string(processingID), SourceID: string(ref.Source.SourceID), SourceVersionID: string(ref.Source.SourceVersionID), ContentArtifactID: string(ref.Source.ContentArtifactID), ParseProjectionID: string(ref.Source.ParseProjectionID), SourceSpanID: string(ref.SourceSpanID), ContentHash: ref.Source.ContentHash, ExcerptHash: ref.ExcerptHash, Title: ref.Title, CreatedAt: canonicalTime(at)})
			}
		}
	}
	if len(rows) == 0 {
		return false, nil
	}
	result := tx.Clauses(clause.OnConflict{OnConstraint: "uq_synthesis_supplement_support", DoNothing: true}).CreateInBatches(&rows, domain.MaxSynthesisSources)
	return result.RowsAffected > 0, result.Error
}

func (store *GORMSynthesisStore) ListSynthesisSupplements(ctx context.Context, query app.SynthesisSupplementListQuery) (app.SynthesisSupplementPage, error) {
	page := app.SynthesisSupplementPage{Items: []app.SynthesisSourceSupplement{}}
	if err := store.ready(ctx, query.WorkspaceID, query.NoteID); err != nil {
		return page, err
	}
	if query.Limit < 1 || query.Limit > app.MaxSynthesisListLimit || (query.BeforeTime == nil) != (query.BeforeID == "") || query.BeforeTime != nil && (query.BeforeTime.IsZero() || !validID(query.BeforeID)) {
		return page, invalid(errors.New("synthesis supplement page is invalid"))
	}
	err := store.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if _, err := loadSynthesisNote(tx, query.WorkspaceID, query.NoteID, false); err != nil {
			return err
		}
		q := tx.Where("workspace_id=? AND note_id=?", string(query.WorkspaceID), string(query.NoteID))
		if query.BeforeTime != nil {
			q = q.Where("(created_at,id)<(?,?)", query.BeforeTime.UTC(), string(query.BeforeID))
		}
		var rows []synthesisSupplementModel
		if err := q.Order("created_at DESC,id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > query.Limit {
			rows = rows[:query.Limit]
			at := rows[len(rows)-1].CreatedAt.UTC()
			page.NextTime = &at
			page.NextID = foundation.ID(rows[len(rows)-1].ID)
		}
		for _, row := range rows {
			value, err := row.value()
			if err != nil {
				return err
			}
			page.Items = append(page.Items, value)
		}
		return nil
	})
	return page, err
}

func (store *GORMSynthesisStore) GetSynthesisSupplement(ctx context.Context, workspaceID, noteID, id foundation.ID) (app.SynthesisSourceSupplement, error) {
	if err := store.ready(ctx, workspaceID, noteID, id); err != nil {
		return app.SynthesisSourceSupplement{}, err
	}
	var row synthesisSupplementModel
	err := store.database.WithContext(ctx).Where("workspace_id=? AND note_id=? AND id=?", string(workspaceID), string(noteID), string(id)).Take(&row).Error
	if gormNoRows(err) {
		return app.SynthesisSourceSupplement{}, foundation.NewError(foundation.ErrorNotFound, "SYNTHESIS_SOURCE_NOT_FOUND", false, errors.New("supplement source not found"))
	}
	if err != nil {
		return app.SynthesisSourceSupplement{}, synthesisDBError(ctx, err)
	}
	return row.value()
}
