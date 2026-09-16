package postgres

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func (s *GORMGoalSelectionStore) ListGoalSelections(ctx context.Context, q app.SynthesisGoalSelectionListQuery) (app.SynthesisGoalSelectionPage, error) {
	page := app.SynthesisGoalSelectionPage{Items: []app.SynthesisGoalSelection{}}
	if s == nil || s.GORMGoalSelectionResultReader == nil || s.uow == nil || ctx == nil || !validID(q.WorkspaceID) || !validID(q.RequestID) || q.AfterID != "" && !validID(q.AfterID) || q.Limit < 1 || q.Limit > 100 {
		return page, goalInvalid()
	}
	err := s.uow.Within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		tx = tx.WithContext(ctx)
		if _, err := loadSynthesisGoal(tx, q.WorkspaceID, q.RequestID, false); err != nil {
			return err
		}
		query := tx.Select("id,workspace_id,request_id,status,error_code,retryable,version,created_at,updated_at").Where("workspace_id=? AND request_id=?", string(q.WorkspaceID), string(q.RequestID)).Order("id ASC").Limit(q.Limit + 1)
		if q.AfterID != "" {
			query = query.Where("id>?", string(q.AfterID))
		}
		var rows []goalSelectionModel
		if err := query.Find(&rows).Error; err != nil {
			return err
		}
		more := len(rows) > q.Limit
		if more {
			rows = rows[:q.Limit]
		}
		for _, row := range rows {
			page.Items = append(page.Items, row.selection())
		}
		if more {
			page.NextAfterID = page.Items[len(page.Items)-1].ID
		}
		return nil
	})
	return page, synthesisDBError(ctx, err)
}
