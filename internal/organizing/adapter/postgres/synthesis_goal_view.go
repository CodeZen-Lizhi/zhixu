package postgres

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// goalProcessingScopedReader 留在适配器边界，因为其不透明事务作用域只属于基础设施。合成运行时仍是处理行投影和校验的唯一负责模块。
type goalProcessingScopedReader interface {
	ReadSynthesisGoalProcessingBatchScoped(context.Context, foundation.TransactionScope, foundation.ID, []foundation.ID) (map[foundation.ID]app.SynthesisProcessing, error)
}

// GORMGoalViewReader 仅返回持久化目标进度与生成链接；不读取原始来源字节、模型运行记录或笔记当前版本指针。
type GORMGoalViewReader struct {
	db         *gorm.DB
	uow        foundation.UnitOfWork
	processing goalProcessingScopedReader
}

func NewGORMGoalViewReader(pool *platformpostgres.Pool, processingScopedReader goalProcessingScopedReader) (*GORMGoalViewReader, error) {
	if pool == nil || isNilInterface(processingScopedReader) {
		return nil, goalInvalid()
	}
	db, err := pool.GORM()
	if err != nil {
		return nil, err
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, err
	}
	return &GORMGoalViewReader{db: db, uow: uow, processing: processingScopedReader}, nil
}

func (reader *GORMGoalViewReader) GetSynthesisGoalView(ctx context.Context, workspaceID, requestID foundation.ID) (app.SynthesisGoalView, error) {
	var result app.SynthesisGoalView
	if reader == nil || reader.db == nil || reader.uow == nil || isNilInterface(reader.processing) || ctx == nil || !validID(workspaceID) || !validID(requestID) {
		return result, goalInvalid()
	}
	err := reader.readOnly(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadSynthesisGoal(tx, workspaceID, requestID, false)
		if err != nil {
			return err
		}
		views, err := reader.readViews(ctx, scope, tx, workspaceID, []synthesisGoalModel{row})
		if err != nil {
			return err
		}
		if len(views) != 1 {
			return goalConflict()
		}
		result = views[0]
		return nil
	})
	return result, err
}

func (reader *GORMGoalViewReader) ListSynthesisGoalViews(ctx context.Context, query app.SynthesisListQuery) (app.SynthesisGoalPage, error) {
	page := app.SynthesisGoalPage{Items: []app.SynthesisGoalView{}}
	if reader == nil || reader.db == nil || reader.uow == nil || isNilInterface(reader.processing) || ctx == nil || !validGoalViewListQuery(query) {
		return page, goalInvalid()
	}
	err := reader.readOnly(ctx, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		statement := tx.Select(synthesisGoalViewColumns).Where("workspace_id=?", string(query.WorkspaceID)).Order("created_at DESC,id DESC").Limit(query.Limit + 1)
		if query.BeforeTime != nil {
			statement = statement.Where("(created_at,id) < (?,?)", canonicalTime(*query.BeforeTime), string(query.BeforeID))
		}
		var rows []synthesisGoalModel
		if err := statement.Find(&rows).Error; err != nil {
			return err
		}
		more := len(rows) > query.Limit
		if more {
			rows = rows[:query.Limit]
		}
		views, err := reader.readViews(ctx, scope, tx, query.WorkspaceID, rows)
		if err != nil {
			return err
		}
		page.Items = views
		if more {
			page.NextTime = timePointer(rows[len(rows)-1].CreatedAt)
			page.NextID = foundation.ID(rows[len(rows)-1].ID)
		}
		return nil
	})
	return page, err
}

const synthesisGoalViewColumns = "id,workspace_id,idempotency_key,request_hash,goal_text,status,after_source_id,catalog_batches,next_check_at,error_code,version,created_at,updated_at"

func validGoalViewListQuery(query app.SynthesisListQuery) bool {
	return validID(query.WorkspaceID) && query.Limit >= 1 && query.Limit <= app.MaxSynthesisListLimit &&
		(query.BeforeTime == nil) == (query.BeforeID == "") && (query.BeforeTime == nil || (!query.BeforeTime.IsZero() && validID(query.BeforeID)))
}

func (reader *GORMGoalViewReader) readOnly(ctx context.Context, operation func(context.Context, foundation.TransactionScope, *gorm.DB) error) error {
	err := reader.uow.Within(ctx, foundation.TransactionOptions{ReadOnly: true, Isolation: foundation.TransactionIsolationRepeatableRead}, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return operation(ctx, scope, tx.WithContext(ctx))
	})
	return synthesisDBError(ctx, err)
}

type goalViewProgressRow struct {
	RequestID                                                                        string
	CatalogBatches, PreparedBatches, Selections, Pending, Running, Succeeded, Failed int64
	RecoveryRequired, SelectedPoints                                                 int64
	PreparationFailures                                                              int64
	PreparationErrorCode                                                             string
}

func (reader *GORMGoalViewReader) readViews(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, workspaceID foundation.ID, requests []synthesisGoalModel) ([]app.SynthesisGoalView, error) {
	if len(requests) == 0 {
		return []app.SynthesisGoalView{}, nil
	}
	ids := make([]string, 0, len(requests))
	requestByID := make(map[foundation.ID]app.SynthesisGoalRequest, len(requests))
	for _, row := range requests {
		request, err := row.request()
		if err != nil {
			return nil, err
		}
		if request.WorkspaceID != workspaceID {
			return nil, goalNotFound()
		}
		if _, duplicate := requestByID[request.ID]; duplicate {
			return nil, goalConflict()
		}
		requestByID[request.ID] = request
		ids = append(ids, string(request.ID))
	}

	var progressRows []goalViewProgressRow
	if err := tx.Raw(`WITH batches AS (
 SELECT b.request_id,count(*) AS catalog_batches,count(m.batch_id) AS prepared_batches,
 count(p.batch_id) FILTER(WHERE m.batch_id IS NULL) AS preparation_failures,
 min(p.error_code) FILTER(WHERE m.batch_id IS NULL) AS preparation_error_code
 FROM organizing.synthesis_goal_catalog_batch b
 LEFT JOIN organizing.synthesis_goal_selection_manifest m ON m.batch_id=b.id AND m.workspace_id=b.workspace_id AND m.sealed
 LEFT JOIN organizing.synthesis_goal_selection_preparation p ON p.batch_id=b.id AND p.workspace_id=b.workspace_id
 WHERE b.workspace_id=? AND b.request_id IN ? GROUP BY b.request_id
), selections AS (
 SELECT s.request_id,count(*) AS selections,count(*) FILTER(WHERE s.status='PENDING') AS pending,
  count(*) FILTER(WHERE s.status='RUNNING') AS running,count(*) FILTER(WHERE s.status='SUCCEEDED') AS succeeded,
  count(*) FILTER(WHERE s.status='FAILED') AS failed,count(*) FILTER(WHERE s.status='RECOVERY_REQUIRED') AS recovery_required,
  COALESCE(sum(CASE WHEN s.status='SUCCEEDED' THEN jsonb_array_length(convert_from(s.model_output,'UTF8')::jsonb->'selections') ELSE 0 END),0) AS selected_points
 FROM organizing.synthesis_goal_selection s WHERE s.workspace_id=? AND s.request_id IN ? GROUP BY s.request_id
)
SELECT g.id AS request_id,COALESCE(b.catalog_batches,0) AS catalog_batches,COALESCE(b.prepared_batches,0) AS prepared_batches,
 COALESCE(s.selections,0) AS selections,COALESCE(s.pending,0) AS pending,COALESCE(s.running,0) AS running,
 COALESCE(s.succeeded,0) AS succeeded,COALESCE(s.failed,0) AS failed,COALESCE(s.recovery_required,0) AS recovery_required,
 COALESCE(s.selected_points,0) AS selected_points,
 COALESCE(b.preparation_failures,0) AS preparation_failures,COALESCE(b.preparation_error_code,'') AS preparation_error_code
FROM organizing.synthesis_goal_request g LEFT JOIN batches b ON b.request_id=g.id LEFT JOIN selections s ON s.request_id=g.id
WHERE g.workspace_id=? AND g.id IN ?`, string(workspaceID), ids, string(workspaceID), ids, string(workspaceID), ids).Scan(&progressRows).Error; err != nil {
		return nil, err
	}
	if len(progressRows) != len(requests) {
		return nil, goalNotFound()
	}
	progressByID := make(map[foundation.ID]goalViewProgressRow, len(progressRows))
	for _, row := range progressRows {
		id := foundation.ID(row.RequestID)
		if !validID(id) || requestByID[id].ID == "" {
			return nil, goalConflict()
		}
		if _, duplicate := progressByID[id]; duplicate || row.CatalogBatches < 0 || row.PreparedBatches < 0 || row.Selections < 0 || row.Pending < 0 || row.Running < 0 || row.Succeeded < 0 || row.Failed < 0 || row.RecoveryRequired < 0 || row.SelectedPoints < 0 {
			return nil, goalConflict()
		}
		progressByID[id] = row
	}
	processings, err := reader.processing.ReadSynthesisGoalProcessingBatchScoped(ctx, scope, workspaceID, asGoalIDs(requests))
	if err != nil {
		return nil, err
	}
	for goalID, processing := range processings {
		if requestByID[goalID].ID == "" || processing.GoalRequestID != goalID {
			return nil, goalConflict()
		}
	}
	candidates, err := readGoalCandidates(tx, workspaceID, ids, processings)
	if err != nil {
		return nil, err
	}
	views := make([]app.SynthesisGoalView, 0, len(requests))
	for _, requestRow := range requests {
		request := requestByID[foundation.ID(requestRow.ID)]
		row := progressByID[request.ID]
		progress := app.SynthesisGoalSelectionProgress{Request: request, CatalogBatches: row.CatalogBatches, PreparedBatches: row.PreparedBatches, Selections: row.Selections, Pending: row.Pending, Running: row.Running, Succeeded: row.Succeeded, Failed: row.Failed, RecoveryRequired: row.RecoveryRequired}
		if progress.CatalogBatches != request.CatalogBatches || progress.PreparedBatches > progress.CatalogBatches || progress.Selections != progress.Pending+progress.Running+progress.Succeeded+progress.Failed+progress.RecoveryRequired {
			return nil, goalConflict()
		}
		if row.PreparationFailures < 0 || row.PreparationFailures > progress.CatalogBatches-progress.PreparedBatches || (row.PreparationFailures == 0) != (row.PreparationErrorCode == "") || row.PreparationErrorCode != "" && !synthesisGoalErrorPattern.MatchString(row.PreparationErrorCode) {
			return nil, goalConflict()
		}
		view := app.SynthesisGoalView{Request: request, Progress: progress, SelectedPoints: row.SelectedPoints, PreparationFailures: row.PreparationFailures, PreparationErrorCode: row.PreparationErrorCode}
		if processing, exists := processings[request.ID]; exists {
			if processing.GoalRequestID != request.ID {
				return nil, goalConflict()
			}
			copy := processing
			view.Processing = &copy
		}
		view.Candidate = candidates[request.ID]
		views = append(views, view)
	}
	return views, nil
}

func asGoalIDs(requests []synthesisGoalModel) []foundation.ID {
	ids := make([]foundation.ID, len(requests))
	for i, request := range requests {
		ids[i] = foundation.ID(request.ID)
	}
	return ids
}

type goalCandidateRow struct {
	RequestID, RevisionID, NoteID string
}

func readGoalCandidates(tx *gorm.DB, workspaceID foundation.ID, ids []string, processings map[foundation.ID]app.SynthesisProcessing) (map[foundation.ID]*app.SynthesisGoalCandidate, error) {
	result := make(map[foundation.ID]*app.SynthesisGoalCandidate)
	expected := make(map[foundation.ID]foundation.ID)
	for goalID, processing := range processings {
		if processing.Status != app.SynthesisProcessingSucceeded {
			continue
		}
		if len(processing.RevisionIDs) != 1 {
			return nil, goalConflict()
		}
		expected[goalID] = processing.RevisionIDs[0]
	}
	if len(expected) == 0 {
		return result, nil
	}
	var rows []goalCandidateRow
	if err := tx.Raw(`SELECT p.goal_request_id AS request_id,r.id AS revision_id,r.note_id
FROM organizing.synthesis_processing p
CROSS JOIN LATERAL jsonb_array_elements_text(p.revision_ids) bound(revision_id)
JOIN organizing.synthesis_revision r ON r.id=bound.revision_id::uuid AND r.workspace_id=p.workspace_id
WHERE p.workspace_id=? AND p.goal_request_id IN ? AND p.status='SUCCEEDED'`, string(workspaceID), ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		goalID, revisionID, noteID := foundation.ID(row.RequestID), foundation.ID(row.RevisionID), foundation.ID(row.NoteID)
		if !validID(goalID) || !validID(revisionID) || !validID(noteID) || expected[goalID] != revisionID {
			return nil, goalConflict()
		}
		if _, duplicate := result[goalID]; duplicate {
			return nil, goalConflict()
		}
		result[goalID] = &app.SynthesisGoalCandidate{NoteID: noteID, RevisionID: revisionID}
	}
	for goalID := range expected {
		if result[goalID] == nil {
			return nil, goalConflict()
		}
	}
	return result, nil
}

var _ app.SynthesisGoalViewReader = (*GORMGoalViewReader)(nil)
