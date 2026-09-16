package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type synthesisGoalModel struct {
	ID             string
	WorkspaceID    string
	IdempotencyKey string
	RequestHash    string
	GoalText       string
	Status         string
	AfterSourceID  *string
	CatalogBatches int64
	NextCheckAt    time.Time
	ErrorCode      *string
	Version        int64
	CreatedAt      time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime:false"`
}

func (synthesisGoalModel) TableName() string { return "organizing.synthesis_goal_request" }

type synthesisGoalBatchModel struct {
	ID                string
	WorkspaceID       string
	RequestID         string
	BatchNo           int64
	AfterSourceID     *string
	NextAfterSourceID *string
	ItemCount         int
	CreatedAt         time.Time `gorm:"autoCreateTime:false"`
}

func (synthesisGoalBatchModel) TableName() string { return "organizing.synthesis_goal_catalog_batch" }

type synthesisGoalItemModel struct {
	BatchID           string
	WorkspaceID       string
	Ordinal           int
	SourceID          string
	SourceVersionID   string
	ContentArtifactID string
	ParseProjectionID string
	ContentHash       string
	ProfileRevisionID string
	Title             string
}

func (synthesisGoalItemModel) TableName() string { return "organizing.synthesis_goal_catalog_item" }

func (row synthesisGoalModel) request() (app.SynthesisGoalRequest, error) {
	value := app.SynthesisGoalRequest{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), Goal: row.GoalText, Status: row.Status, AfterSourceID: foundation.ID(stringValue(row.AfterSourceID)), CatalogBatches: row.CatalogBatches, NextCheckAt: row.NextCheckAt, ErrorCode: stringValue(row.ErrorCode), Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if !validID(value.ID) || (app.CreateSynthesisGoalCommand{WorkspaceID: value.WorkspaceID, Goal: value.Goal, IdempotencyKey: row.IdempotencyKey}).Validate() != nil || value.Version < 1 || value.CatalogBatches < 0 || value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) || value.AfterSourceID != "" && !validID(value.AfterSourceID) || value.Status != app.SynthesisGoalDiscovering && value.Status != app.SynthesisGoalCatalogReady {
		return app.SynthesisGoalRequest{}, goalConflict()
	}
	return value, nil
}

func (store *GORMSynthesisStore) CreateSynthesisGoal(ctx context.Context, command app.CreateSynthesisGoalCommand) (app.SynthesisGoalCreateResult, error) {
	var result app.SynthesisGoalCreateResult
	if err := command.Validate(); err != nil {
		return result, err
	}
	if err := store.ready(ctx, command.WorkspaceID); err != nil {
		return result, err
	}
	err := store.within(ctx, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var err error
		result, err = createSynthesisGoalInTransaction(tx, command)
		return err
	})
	return result, err
}

func createSynthesisGoalInTransaction(tx *gorm.DB, command app.CreateSynthesisGoalCommand) (result app.SynthesisGoalCreateResult, err error) {
	if err := command.Validate(); err != nil {
		return result, err
	}
	raw, _ := json.Marshal(struct {
		WorkspaceID foundation.ID
		Goal        string
	}{command.WorkspaceID, command.Goal})
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	err = func() error {
		var inserted synthesisGoalModel
		statement := tx.Raw(`INSERT INTO organizing.synthesis_goal_request(workspace_id,idempotency_key,request_hash,goal_text) VALUES(?,?,?,?) ON CONFLICT(workspace_id,idempotency_key) DO NOTHING RETURNING *`, string(command.WorkspaceID), command.IdempotencyKey, hash, command.Goal).Scan(&inserted)
		if statement.Error != nil {
			return statement.Error
		}
		if statement.RowsAffected == 0 {
			if err := tx.Where("workspace_id=? AND idempotency_key=?", string(command.WorkspaceID), command.IdempotencyKey).Take(&inserted).Error; err != nil {
				return err
			}
			if inserted.RequestHash != hash || inserted.GoalText != command.Goal {
				return goalConflict()
			}
			result.Replayed = true
		}
		var err error
		result.Request, err = inserted.request()
		return err
	}()
	return result, err
}

func (store *GORMSynthesisStore) GetSynthesisGoal(ctx context.Context, workspaceID, requestID foundation.ID) (app.SynthesisGoalRequest, error) {
	if err := store.ready(ctx, workspaceID, requestID); err != nil {
		return app.SynthesisGoalRequest{}, err
	}
	row, err := loadSynthesisGoal(store.database.WithContext(ctx), workspaceID, requestID, false)
	if err != nil {
		return app.SynthesisGoalRequest{}, synthesisDBError(ctx, err)
	}
	return row.request()
}

func (store *GORMSynthesisStore) ListDiscoveringSynthesisGoals(ctx context.Context, workspaceID foundation.ID, limit int) ([]app.SynthesisGoalRequest, error) {
	if err := store.ready(ctx, workspaceID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, goalInvalid()
	}
	var rows []synthesisGoalModel
	if err := store.database.WithContext(ctx).Where("workspace_id=? AND status='DISCOVERING' AND next_check_at<=clock_timestamp()", string(workspaceID)).Order("created_at,id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, synthesisDBError(ctx, err)
	}
	result := make([]app.SynthesisGoalRequest, 0, len(rows))
	for _, row := range rows {
		value, err := row.request()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (store *GORMSynthesisStore) FreezeSynthesisGoalCatalog(ctx context.Context, expected app.SynthesisGoalRequest, page app.SynthesisGoalCatalogPage) (app.SynthesisGoalFreezeResult, error) {
	var result app.SynthesisGoalFreezeResult
	if err := store.ready(ctx, expected.WorkspaceID, expected.ID); err != nil {
		return result, err
	}
	err := store.within(ctx, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var err error
		result, err = freezeSynthesisGoalCatalogInTransaction(tx, expected, page)
		return err
	})
	return result, err
}

func freezeSynthesisGoalCatalogInTransaction(tx *gorm.DB, expected app.SynthesisGoalRequest, page app.SynthesisGoalCatalogPage) (result app.SynthesisGoalFreezeResult, err error) {
	if expected.Status != app.SynthesisGoalDiscovering || expected.Version < 1 || expected.CatalogBatches < 0 || page.DeferredCode != "" || page.Items == nil || len(page.Items) > 32 || expected.AfterSourceID != "" && !validID(expected.AfterSourceID) {
		return result, goalInvalid()
	}
	next := ""
	if page.NextAfterSourceID != nil {
		if !validID(*page.NextAfterSourceID) || *page.NextAfterSourceID <= expected.AfterSourceID {
			return result, goalInvalid()
		}
		next = string(*page.NextAfterSourceID)
	}
	last := expected.AfterSourceID
	for _, item := range page.Items {
		if item.Source.Validate() != nil || item.Source.WorkspaceID != expected.WorkspaceID || item.Source.SourceID <= last || next != "" && string(item.Source.SourceID) > next || item.Directory.Status != app.KnowledgeDirectoryAnalyzed || item.Directory.WorkspaceID != expected.WorkspaceID || item.Directory.SourceVersionID != item.Source.SourceVersionID || item.Directory.ParseProjectionID != item.Source.ParseProjectionID || !validID(item.Directory.ProfileRevisionID) || item.Title == "" || item.Title != strings.TrimSpace(item.Title) || len(item.Title) > 512 {
			return result, goalInvalid()
		}
		last = item.Source.SourceID
	}
	err = func() error {
		row, err := loadSynthesisGoal(tx, expected.WorkspaceID, expected.ID, true)
		if err != nil {
			return err
		}
		if row.GoalText != expected.Goal {
			return goalConflict()
		}
		// 并发枚举者返回已提交的胜出结果，不能用更新的可变目录观察替换已冻结的 Profile 身份。
		var existing synthesisGoalBatchModel
		err = tx.Where("request_id=? AND workspace_id=? AND batch_no=?", row.ID, row.WorkspaceID, expected.CatalogBatches+1).Take(&existing).Error
		if err == nil {
			if foundation.ID(stringValue(existing.AfterSourceID)) != expected.AfterSourceID {
				return goalConflict()
			}
			result.Request, err = row.request()
			if err != nil {
				return err
			}
			result.Batch, err = readSynthesisGoalBatch(tx, existing)
			result.Replayed = true
			return err
		}
		if !gormNoRows(err) {
			return err
		}
		if row.Version != expected.Version || row.Status != app.SynthesisGoalDiscovering || row.CatalogBatches != expected.CatalogBatches || foundation.ID(stringValue(row.AfterSourceID)) != expected.AfterSourceID {
			return goalConflict()
		}
		var batch synthesisGoalBatchModel
		if err := tx.Raw(`INSERT INTO organizing.synthesis_goal_catalog_batch(workspace_id,request_id,batch_no,after_source_id,next_after_source_id,item_count) VALUES(?,?,?,NULLIF(?,'')::uuid,NULLIF(?,'')::uuid,?) RETURNING *`, row.WorkspaceID, row.ID, row.CatalogBatches+1, string(expected.AfterSourceID), next, len(page.Items)).Scan(&batch).Error; err != nil {
			return err
		}
		for i, item := range page.Items {
			persisted := synthesisGoalItemModel{BatchID: batch.ID, WorkspaceID: row.WorkspaceID, Ordinal: i + 1, SourceID: string(item.Source.SourceID), SourceVersionID: string(item.Source.SourceVersionID), ContentArtifactID: string(item.Source.ContentArtifactID), ParseProjectionID: string(item.Source.ParseProjectionID), ContentHash: item.Source.ContentHash, ProfileRevisionID: string(item.Directory.ProfileRevisionID), Title: item.Title}
			if err := tx.Create(&persisted).Error; err != nil {
				return err
			}
		}
		status := app.SynthesisGoalDiscovering
		if next == "" {
			status = app.SynthesisGoalCatalogReady
		}
		updated := tx.Raw(`UPDATE organizing.synthesis_goal_request SET status=?,error_code=NULL,next_check_at=clock_timestamp(),after_source_id=NULLIF(?,'')::uuid,catalog_batches=catalog_batches+1,version=version+1,updated_at=GREATEST(clock_timestamp(),updated_at) WHERE id=? AND workspace_id=? AND version=? RETURNING *`, status, next, row.ID, row.WorkspaceID, row.Version).Scan(&row)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return goalConflict()
		}
		result.Request, err = row.request()
		if err != nil {
			return err
		}
		result.Batch, err = readSynthesisGoalBatch(tx, batch)
		return err
	}()
	return result, err
}

func (store *GORMSynthesisStore) GetSynthesisGoalCatalogBatch(ctx context.Context, workspaceID, requestID foundation.ID, batchNo int64) (app.SynthesisGoalCatalogBatch, error) {
	if err := store.ready(ctx, workspaceID, requestID); err != nil {
		return app.SynthesisGoalCatalogBatch{}, err
	}
	if batchNo < 1 {
		return app.SynthesisGoalCatalogBatch{}, goalInvalid()
	}
	tx := store.database.WithContext(ctx)
	var row synthesisGoalBatchModel
	if err := tx.Where("workspace_id=? AND request_id=? AND batch_no=?", string(workspaceID), string(requestID), batchNo).Take(&row).Error; err != nil {
		if gormNoRows(err) {
			return app.SynthesisGoalCatalogBatch{}, goalNotFound()
		}
		return app.SynthesisGoalCatalogBatch{}, synthesisDBError(ctx, err)
	}
	return readSynthesisGoalBatch(tx, row)
}

func loadSynthesisGoal(tx *gorm.DB, workspaceID, requestID foundation.ID, lock bool) (synthesisGoalModel, error) {
	var row synthesisGoalModel
	query := tx.Where("workspace_id=? AND id=?", string(workspaceID), string(requestID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if gormNoRows(err) {
		return row, goalNotFound()
	}
	return row, err
}

func readSynthesisGoalBatch(tx *gorm.DB, row synthesisGoalBatchModel) (app.SynthesisGoalCatalogBatch, error) {
	result := app.SynthesisGoalCatalogBatch{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), RequestID: foundation.ID(row.RequestID), BatchNo: row.BatchNo, AfterSourceID: foundation.ID(stringValue(row.AfterSourceID)), NextAfterSourceID: foundation.ID(stringValue(row.NextAfterSourceID)), Items: []app.SynthesisGoalCatalogBinding{}, CreatedAt: row.CreatedAt}
	var items []synthesisGoalItemModel
	if err := tx.Where("batch_id=? AND workspace_id=?", row.ID, row.WorkspaceID).Order("ordinal").Find(&items).Error; err != nil {
		return result, err
	}
	if len(items) != row.ItemCount {
		return result, goalConflict()
	}
	for i, item := range items {
		source := domain.SynthesisSourceVersion{WorkspaceID: result.WorkspaceID, SourceID: foundation.ID(item.SourceID), SourceVersionID: foundation.ID(item.SourceVersionID), ContentArtifactID: foundation.ID(item.ContentArtifactID), ParseProjectionID: foundation.ID(item.ParseProjectionID), ContentHash: item.ContentHash}
		if item.Ordinal != i+1 || source.Validate() != nil || !validID(foundation.ID(item.ProfileRevisionID)) {
			return result, goalConflict()
		}
		result.Items = append(result.Items, app.SynthesisGoalCatalogBinding{Source: source, ProfileRevisionID: foundation.ID(item.ProfileRevisionID), Title: item.Title})
	}
	return result, nil
}

func goalInvalid() error {
	return foundation.NewError(foundation.ErrorInvalidInput, "SYNTHESIS_GOAL_INVALID", false, errors.New("invalid synthesis goal request"))
}
func goalConflict() error {
	return foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_GOAL_CONFLICT", false, errors.New("synthesis goal request or catalog changed"))
}
func goalNotFound() error {
	return foundation.NewError(foundation.ErrorNotFound, "SYNTHESIS_GOAL_NOT_FOUND", false, errors.New("synthesis goal not found"))
}

var _ app.SynthesisGoalRequestStore = (*GORMSynthesisStore)(nil)

var synthesisGoalErrorPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// Defer 保留冻结游标，并防止单个异常请求独占发现流程；并发胜出者已负责后续推进。
func (store *GORMSynthesisStore) DeferSynthesisGoal(ctx context.Context, expected app.SynthesisGoalRequest, code string) error {
	if err := store.ready(ctx, expected.WorkspaceID, expected.ID); err != nil {
		return err
	}
	if expected.Version < 1 || !synthesisGoalErrorPattern.MatchString(code) {
		return goalInvalid()
	}
	return store.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		row, err := loadSynthesisGoal(tx, expected.WorkspaceID, expected.ID, true)
		if err != nil {
			return err
		}
		if row.Version != expected.Version || row.Status != app.SynthesisGoalDiscovering {
			return nil
		}
		return tx.Exec(`UPDATE organizing.synthesis_goal_request SET error_code=?,next_check_at=GREATEST(clock_timestamp(),next_check_at)+interval '30 seconds',version=version+1,updated_at=GREATEST(clock_timestamp(),updated_at) WHERE id=? AND workspace_id=? AND version=?`, code, row.ID, row.WorkspaceID, row.Version).Error
	})
}
