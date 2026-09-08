package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	reviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/domain"
	pathapp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GORMRepository 使用共享平台连接池和 Unit of Work 持久化 Learning Path。
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

var _ pathapp.Store = (*GORMRepository)(nil)

// NewGORMRepository 从同一个平台 Pool 获取共享 GORM root 和 Unit of Work。
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, gormLearningPathUnavailable(errors.New("learning path PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormLearningPathUnavailable(fmt.Errorf("learning path GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, gormLearningPathUnavailable(fmt.Errorf("learning path GORM unit of work is unavailable: %w", err))
	}
	if !validLearningPathGORMDatabase(database) || nilLearningPathUnitOfWork(unitOfWork) {
		return nil, gormLearningPathUnavailable(errors.New("learning path GORM dependencies are unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork}, nil
}

// FindCreateReplay 按请求哈希精确读取创建 receipt。
func (repository *GORMRepository) FindCreateReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string) (pathapp.Result, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return pathapp.Result{}, false, err
	}
	return gormFindResultReceipt(ctx, repository.database.WithContext(ctx), workspaceID, key, requestHash, pathapp.CommandTypeCreateReviewPath, 0)
}

// BeginReviewCreate 锁定 Workspace 与 Answer，并冻结可恢复的 Review 来源快照。
func (repository *GORMRepository) BeginReviewCreate(ctx context.Context, workspaceID, answerID foundation.ID, key, requestHash string) (pathapp.Reservation, pathapp.ReviewSnapshot, *pathapp.Result, error) {
	var outputReservation pathapp.Reservation
	var outputSnapshot pathapp.ReviewSnapshot
	var replay *pathapp.Result
	err := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockWorkspace(callbackCtx, tx, workspaceID); err != nil {
			return err
		}
		if receipt, found, err := gormFindResultReceipt(callbackCtx, tx, workspaceID, key, requestHash, pathapp.CommandTypeCreateReviewPath, 0); err != nil || found {
			if err != nil {
				return err
			}
			replay = &receipt
			return nil
		}
		if err := gormLockReviewAnswer(callbackCtx, tx, workspaceID, answerID); err != nil {
			return err
		}

		reservation, found, err := gormLoadReservation(callbackCtx, tx, workspaceID, answerID, true)
		if err != nil {
			return err
		}
		if found {
			if reservation.Status == pathapp.ReservationCompleted {
				result, err := gormLoadPathResultByAnswer(callbackCtx, tx, workspaceID, answerID, false)
				if err != nil {
					return err
				}
				if reservation.IdempotencyKey == key && reservation.RequestHash == requestHash {
					return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "completed learning path reservation has no create receipt")
				}
				if err := gormInsertResultReceipt(callbackCtx, tx, workspaceID, key, requestHash, pathapp.CommandTypeCreateReviewPath, 0, result); err != nil {
					return err
				}
				result.Replayed = true
				outputReservation = reservation
				replay = &result
				return nil
			}
			if reservation.IdempotencyKey != key || reservation.RequestHash != requestHash {
				return pathdomain.ConflictError(pathdomain.ErrorCodeReservationPending, "review answer already has a learning path reservation")
			}
			if reservation.Status == pathapp.ReservationPending {
				outputReservation = reservation
				outputSnapshot = reservation.SourceSnapshot
				return nil
			}
			if reservation.Status != pathapp.ReservationAbandoned {
				return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path reservation status is invalid")
			}
		}

		snapshot, err := gormLoadReviewSnapshot(callbackCtx, tx, workspaceID, answerID)
		if err != nil {
			return err
		}
		if !snapshot.Gap.Actionable() {
			outputSnapshot = snapshot
			return nil
		}
		canonical, snapshotJSON, snapshotDigest, err := pathapp.CanonicalReviewSnapshot(snapshot)
		if err != nil {
			return err
		}
		currentAttempt := int64(0)
		if found {
			currentAttempt = reservation.AttemptNo
		}
		reservation = pathapp.Reservation{
			WorkspaceID: workspaceID, ReviewAnswerID: answerID, IdempotencyKey: key, RequestHash: requestHash,
			SourceSnapshot: canonical, SourceSnapshotDigest: snapshotDigest, AttemptNo: 1, Status: pathapp.ReservationPending,
		}
		if found {
			row, err := gormLearningPathRawRow(tx.WithContext(callbackCtx), gormReopenReservationSQL,
				learningPathJSONB(snapshotJSON), snapshotDigest, string(workspaceID), string(answerID), key, requestHash, currentAttempt)
			if err != nil {
				return gormLearningPathClassify(callbackCtx, err)
			}
			err = row.Scan(&reservation.AttemptNo, &reservation.CreatedAt, &reservation.UpdatedAt)
			if gormLearningPathNoRows(err) {
				return pathdomain.ConflictError(pathdomain.ErrorCodeReservationPending, "learning path reservation retry lost its compare-and-swap")
			}
			if err != nil {
				return gormLearningPathClassify(callbackCtx, err)
			}
		} else {
			row, err := gormLearningPathRawRow(tx.WithContext(callbackCtx), gormInsertReservationSQL,
				string(workspaceID), string(answerID), key, requestHash, learningPathJSONB(snapshotJSON), snapshotDigest)
			if err != nil {
				return gormLearningPathClassify(callbackCtx, err)
			}
			if err := row.Scan(&reservation.CreatedAt, &reservation.UpdatedAt); err != nil {
				return gormLearningPathClassify(callbackCtx, err)
			}
		}
		outputReservation = reservation
		outputSnapshot = canonical
		return nil
	})
	if err != nil {
		return pathapp.Reservation{}, pathapp.ReviewSnapshot{}, nil, err
	}
	return outputReservation, outputSnapshot, replay, nil
}

// PrepareReviewCreate 将 PENDING reservation 固定到一个 Artifact digest。
func (repository *GORMRepository) PrepareReviewCreate(ctx context.Context, reservation pathapp.Reservation, digest string) (pathapp.Reservation, *pathapp.Result, error) {
	if !validHash(digest) {
		return pathapp.Reservation{}, nil, pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path artifact digest is invalid")
	}
	var output pathapp.Reservation
	var replay *pathapp.Result
	err := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
		current, found, err := gormLoadReservation(callbackCtx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID, true)
		if err != nil {
			return err
		}
		if !found {
			return pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "learning path reservation was not found")
		}
		if current.IdempotencyKey != reservation.IdempotencyKey || current.RequestHash != reservation.RequestHash ||
			current.SourceSnapshotDigest != reservation.SourceSnapshotDigest || current.AttemptNo != reservation.AttemptNo {
			return pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path reservation binding drifted")
		}
		if current.Status == pathapp.ReservationCompleted {
			result, err := gormLoadPathResultByAnswer(callbackCtx, tx, current.WorkspaceID, current.ReviewAnswerID, false)
			if err != nil {
				return err
			}
			result.Replayed = true
			output, replay = current, &result
			return nil
		}
		if current.Status != pathapp.ReservationPending {
			return pathdomain.ConflictError(pathdomain.ErrorCodeReservationPending, "learning path reservation is not pending")
		}
		if current.ArtifactDigest != "" && current.ArtifactDigest != digest {
			return pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path artifact digest conflicts")
		}
		if current.ArtifactDigest == "" {
			row, err := gormLearningPathRawRow(tx.WithContext(callbackCtx), gormPrepareReservationSQL,
				digest, string(current.WorkspaceID), string(current.ReviewAnswerID), current.AttemptNo)
			if err != nil {
				return gormLearningPathClassify(callbackCtx, err)
			}
			err = row.Scan(&current.UpdatedAt)
			if gormLearningPathNoRows(err) {
				return pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path reservation preparation lost its compare-and-swap")
			}
			if err != nil {
				return gormLearningPathClassify(callbackCtx, err)
			}
			current.ArtifactDigest = digest
		}
		output = current
		return nil
	})
	if err != nil {
		return pathapp.Reservation{}, nil, err
	}
	return output, replay, nil
}

// CompleteReviewCreate 原子写入 Path、Steps、receipt、最终 reservation 并释放精确 hold。
func (repository *GORMRepository) CompleteReviewCreate(ctx context.Context, reservation pathapp.Reservation, path pathdomain.Path, steps []pathdomain.Step) (pathapp.Result, error) {
	result := pathapp.Result{Path: path, Steps: append([]pathdomain.Step(nil), steps...)}
	if err := validateResult(result, reservation.WorkspaceID, path.ID, path.Version); err != nil {
		return pathapp.Result{}, err
	}
	var output pathapp.Result
	err := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockReviewAnswer(callbackCtx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID); err != nil {
			return err
		}
		current, found, err := gormLoadReservation(callbackCtx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID, true)
		if err != nil {
			return err
		}
		if !found {
			return pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "learning path reservation was not found")
		}
		if current.IdempotencyKey != reservation.IdempotencyKey || current.RequestHash != reservation.RequestHash ||
			current.SourceSnapshotDigest != reservation.SourceSnapshotDigest || current.AttemptNo != reservation.AttemptNo {
			return pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path completion reservation drifted")
		}
		if current.Status == pathapp.ReservationCompleted {
			replayed, err := gormLoadPathResultByAnswer(callbackCtx, tx, reservation.WorkspaceID, reservation.ReviewAnswerID, false)
			if err != nil {
				return err
			}
			replayed.Replayed = true
			output = replayed
			return nil
		}
		if current.Status != pathapp.ReservationPending || current.ArtifactDigest == "" || current.ArtifactDigest != reservation.ArtifactDigest {
			return pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path completion reservation drifted")
		}
		if path.WorkspaceID != reservation.WorkspaceID || path.ReviewAnswerID == nil ||
			*path.ReviewAnswerID != reservation.ReviewAnswerID || !path.CreatedAt.Equal(current.CreatedAt) {
			return pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path source binding is invalid")
		}
		if err := gormInsertPath(callbackCtx, tx, path); err != nil {
			return err
		}
		if err := gormInsertSteps(callbackCtx, tx, steps); err != nil {
			return err
		}
		if err := gormInsertResultReceipt(callbackCtx, tx, path.WorkspaceID, reservation.IdempotencyKey, reservation.RequestHash, pathapp.CommandTypeCreateReviewPath, 0, result); err != nil {
			return err
		}
		completed, err := gormLearningPathExec(callbackCtx, tx, gormCompleteReservationSQL,
			string(path.ID), string(path.Artifact.ArtifactID), string(path.Artifact.ArtifactRevisionID), path.Artifact.ArtifactVersion,
			string(path.WorkspaceID), string(reservation.ReviewAnswerID), reservation.IdempotencyKey, reservation.RequestHash,
			reservation.SourceSnapshotDigest, reservation.ArtifactDigest, reservation.AttemptNo)
		if err != nil {
			return gormLearningPathClassify(callbackCtx, err)
		}
		if completed != 1 {
			return pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path reservation completion lost its compare-and-swap")
		}
		released, err := gormLearningPathExec(callbackCtx, tx, gormReleasePathHoldSQL,
			string(path.WorkspaceID), string(path.Artifact.ArtifactID), string(reservation.ReviewAnswerID),
			string(reservation.ReviewAnswerID), reservation.ArtifactDigest)
		if err != nil {
			return gormLearningPathClassify(callbackCtx, err)
		}
		if released != 1 {
			return pathdomain.ConflictError(pathdomain.ErrorCodeArtifactConflict, "learning path hold was not released exactly once")
		}
		output = result
		return nil
	})
	if err != nil {
		return pathapp.Result{}, err
	}
	return output, nil
}

// GetByReviewAnswer 读取唯一的 Review-origin Path 聚合。
func (repository *GORMRepository) GetByReviewAnswer(ctx context.Context, workspaceID, answerID foundation.ID) (pathapp.Result, error) {
	var result pathapp.Result
	err := repository.within(ctx, true, func(callbackCtx context.Context, tx *gorm.DB) error {
		var err error
		result, err = gormLoadPathResultByAnswer(callbackCtx, tx, workspaceID, answerID, false)
		return err
	})
	if err != nil {
		return pathapp.Result{}, err
	}
	return result, nil
}

// Get 按身份读取一个 Review-origin Path 聚合。
func (repository *GORMRepository) Get(ctx context.Context, workspaceID, pathID foundation.ID) (pathapp.Result, error) {
	var result pathapp.Result
	err := repository.within(ctx, true, func(callbackCtx context.Context, tx *gorm.DB) error {
		var err error
		result, err = gormLoadResultByPath(callbackCtx, tx, workspaceID, pathID, false)
		return err
	})
	if err != nil {
		return pathapp.Result{}, err
	}
	return result, nil
}

// FindPathStatusReplay 读取一个精确的 Path 状态命令 receipt。
func (repository *GORMRepository) FindPathStatusReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string, expectedVersion int64) (pathapp.Result, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return pathapp.Result{}, false, err
	}
	return gormFindResultReceipt(ctx, repository.database.WithContext(ctx), workspaceID, key, requestHash, pathapp.CommandTypePathStatus, expectedVersion)
}

// UpdatePathStatus 用聚合 CAS 持久化用户授权的生命周期转换。
func (repository *GORMRepository) UpdatePathStatus(ctx context.Context, record pathapp.UpdatePathStatusRecord) (pathapp.Result, error) {
	var output pathapp.Result
	err := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		if replay, found, err := gormFindResultReceipt(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, pathapp.CommandTypePathStatus, record.ExpectedVersion); err != nil || found {
			if err != nil {
				return err
			}
			output = replay
			return nil
		}
		result, err := gormLoadResultByPath(callbackCtx, tx, record.WorkspaceID, record.PathID, true)
		if err != nil {
			return err
		}
		if result.Path.Version != record.ExpectedVersion || !pathdomain.CanTransitionPath(result.Path.Status, record.Status) ||
			(record.Status == pathdomain.StatusCompleted && !pathdomain.AllStepsTerminal(result.Steps)) {
			return pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path status transition is invalid")
		}
		updated, err := gormLearningPathExec(callbackCtx, tx, gormUpdatePathStatusSQL,
			string(record.Status), record.At.UTC(), string(record.WorkspaceID), string(record.PathID), record.ExpectedVersion)
		if err != nil {
			return gormLearningPathClassify(callbackCtx, err)
		}
		if updated != 1 {
			return pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path version changed")
		}
		result.Path.Status, result.Path.Version, result.Path.UpdatedAt = record.Status, record.ExpectedVersion+1, record.At.UTC()
		if err := gormInsertResultReceipt(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, pathapp.CommandTypePathStatus, record.ExpectedVersion, result); err != nil {
			return err
		}
		output = result
		return nil
	})
	if err != nil {
		return pathapp.Result{}, err
	}
	return output, nil
}

// FindStepReplay 读取一个精确的 Step 命令 receipt。
func (repository *GORMRepository) FindStepReplay(ctx context.Context, workspaceID foundation.ID, key, requestHash string, expectedVersion int64) (pathapp.StepResult, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return pathapp.StepResult{}, false, err
	}
	return gormFindStepReceipt(ctx, repository.database.WithContext(ctx), workspaceID, key, requestHash, expectedVersion)
}

// UpdateStep 在同一事务推进 Step 和父 Path 版本。
func (repository *GORMRepository) UpdateStep(ctx context.Context, record pathapp.UpdateStepRecord) (pathapp.StepResult, error) {
	var output pathapp.StepResult
	err := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		}
		if replay, found, err := gormFindStepReceipt(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, record.ExpectedVersion); err != nil || found {
			if err != nil {
				return err
			}
			output = replay
			return nil
		}
		result, err := gormLoadResultByPath(callbackCtx, tx, record.WorkspaceID, record.PathID, true)
		if err != nil {
			return err
		}
		if result.Path.Version != record.ExpectedVersion || result.Path.Status != pathdomain.StatusActive {
			return pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path is not active at the expected version")
		}
		selectedIndex := -1
		for index := range result.Steps {
			if result.Steps[index].ID == record.StepID {
				selectedIndex = index
				break
			}
		}
		if selectedIndex < 0 || !pathdomain.CanTransitionStep(result.Steps[selectedIndex].Status, record.Status) {
			return pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path step transition is invalid")
		}
		currentStepVersion := result.Steps[selectedIndex].Version
		updated, err := gormLearningPathExec(callbackCtx, tx, gormUpdateStepSQL,
			string(record.Status), record.At.UTC(), string(record.WorkspaceID), string(record.PathID), string(record.StepID), currentStepVersion)
		if err != nil {
			return gormLearningPathClassify(callbackCtx, err)
		}
		if updated != 1 {
			return pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path step version changed")
		}
		result.Steps[selectedIndex].Status = record.Status
		result.Steps[selectedIndex].Version++
		result.Steps[selectedIndex].UpdatedAt = record.At.UTC()
		nextPathStatus := result.Path.Status
		if pathdomain.AllStepsTerminal(result.Steps) {
			nextPathStatus = pathdomain.StatusCompleted
		}
		updated, err = gormLearningPathExec(callbackCtx, tx, gormUpdateParentPathSQL,
			string(nextPathStatus), record.At.UTC(), string(record.WorkspaceID), string(record.PathID), record.ExpectedVersion)
		if err != nil {
			return gormLearningPathClassify(callbackCtx, err)
		}
		if updated != 1 {
			return pathdomain.ConflictError(pathdomain.ErrorCodeStateConflict, "learning path version changed")
		}
		result.Path.Status = nextPathStatus
		result.Path.Version = record.ExpectedVersion + 1
		result.Path.UpdatedAt = record.At.UTC()
		response := pathapp.StepResult{Path: result.Path, Step: result.Steps[selectedIndex]}
		if err := gormInsertStepReceipt(callbackCtx, tx, record.WorkspaceID, record.IdempotencyKey, record.RequestHash, record.ExpectedVersion, response); err != nil {
			return err
		}
		output = response
		return nil
	})
	if err != nil {
		return pathapp.StepResult{}, err
	}
	return output, nil
}

// MaintainReservations 放弃一个有界锁定批次，并孤立每个 reservation 的精确 hold。
func (repository *GORMRepository) MaintainReservations(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit < 1 || limit > pathapp.MaxMaintenanceBatch {
		return 0, pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path maintenance limit is invalid")
	}
	abandoned := 0
	err := repository.within(ctx, false, func(callbackCtx context.Context, tx *gorm.DB) error {
		row, err := gormLearningPathRawRow(tx.WithContext(callbackCtx), gormMaintainReservationsSQL, before.UTC(), limit)
		if err != nil {
			return gormLearningPathClassify(callbackCtx, err)
		}
		orphaned := 0
		if err := row.Scan(&abandoned, &orphaned); err != nil {
			return gormLearningPathClassify(callbackCtx, err)
		}
		if orphaned > abandoned {
			return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path maintenance found duplicate active holds")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return abandoned, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validLearningPathGORMDatabase(repository.database) || nilLearningPathUnitOfWork(repository.unitOfWork) {
		return gormLearningPathUnavailable(errors.New("learning path GORM repository is unavailable"))
	}
	if ctx == nil {
		return pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path context is nil")
	}
	return nil
}

func (repository *GORMRepository) within(ctx context.Context, readOnly bool, work func(context.Context, *gorm.DB) error) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path transaction callback is nil")
	}
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{ReadOnly: readOnly}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(callbackCtx, tx.WithContext(callbackCtx))
	})
	return gormLearningPathClassify(ctx, err)
}

func gormLockWorkspace(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID) error {
	row, err := gormLearningPathRawRow(tx.WithContext(ctx), gormLockWorkspaceSQL, string(workspaceID))
	if err != nil {
		return gormLearningPathClassify(ctx, err)
	}
	var id string
	if err := row.Scan(&id); gormLearningPathNoRows(err) {
		return pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "workspace was not found")
	} else if err != nil {
		return gormLearningPathClassify(ctx, err)
	}
	return nil
}

func gormLockReviewAnswer(ctx context.Context, tx *gorm.DB, workspaceID, answerID foundation.ID) error {
	row, err := gormLearningPathRawRow(tx.WithContext(ctx), gormLockReviewAnswerSQL, string(workspaceID), string(answerID))
	if err != nil {
		return gormLearningPathClassify(ctx, err)
	}
	var id string
	if err := row.Scan(&id); gormLearningPathNoRows(err) {
		return pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "review answer was not found")
	} else if err != nil {
		return gormLearningPathClassify(ctx, err)
	}
	return nil
}

func gormLockCommand(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string) error {
	if _, err := gormLearningPathExec(ctx, tx, gormLockCommandSQL, string(workspaceID)+":"+key); err != nil {
		return gormLearningPathClassify(ctx, err)
	}
	return nil
}

func gormLoadReservation(ctx context.Context, tx *gorm.DB, workspaceID, answerID foundation.ID, lock bool) (pathapp.Reservation, bool, error) {
	query := gormReservationSQL
	if lock {
		query = gormReservationForUpdateSQL
	}
	row, err := gormLearningPathRawRow(tx.WithContext(ctx), query, string(workspaceID), string(answerID))
	if err != nil {
		return pathapp.Reservation{}, false, gormLearningPathClassify(ctx, err)
	}
	var value pathapp.Reservation
	var snapshotJSON []byte
	var status string
	err = row.Scan(
		&value.WorkspaceID, &value.ReviewAnswerID, &value.IdempotencyKey, &value.RequestHash,
		&snapshotJSON, &value.SourceSnapshotDigest, &value.AttemptNo, &value.ArtifactDigest, &status,
		&value.CreatedAt, &value.UpdatedAt,
	)
	if gormLearningPathNoRows(err) {
		return pathapp.Reservation{}, false, nil
	}
	if err != nil {
		return pathapp.Reservation{}, false, gormLearningPathClassify(ctx, err)
	}
	value.Status = pathapp.ReservationStatus(status)
	snapshot, err := decodeReviewSnapshot(snapshotJSON, value.SourceSnapshotDigest)
	if err != nil {
		return pathapp.Reservation{}, false, err
	}
	value.SourceSnapshot = snapshot
	if err := validateReservation(value, workspaceID, answerID); err != nil {
		return pathapp.Reservation{}, false, err
	}
	return value, true, nil
}

func gormLoadReviewSnapshot(ctx context.Context, tx *gorm.DB, workspaceID, answerID foundation.ID) (pathapp.ReviewSnapshot, error) {
	row, err := gormLearningPathRawRow(tx.WithContext(ctx), gormReviewAnswerSnapshotSQL, string(workspaceID), string(answerID))
	if err != nil {
		return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
	}
	var scoreJSON []byte
	var cardID sql.NullString
	var scorerVersion string
	if err := row.Scan(&scoreJSON, &cardID, &scorerVersion); gormLearningPathNoRows(err) {
		return pathapp.ReviewSnapshot{}, pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "review answer was not found")
	} else if err != nil {
		return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
	}
	if !cardID.Valid {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "review answer has no review card")
	}
	parsedCardID, err := foundation.ParseID(cardID.String)
	if err != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review answer card is invalid")
	}
	row, err = gormLearningPathRawRow(tx.WithContext(ctx), gormReviewCardStatusSQL, string(workspaceID), cardID.String)
	if err != nil {
		return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
	}
	var cardStatus string
	err = row.Scan(&cardStatus)
	if gormLearningPathNoRows(err) || (err == nil && cardStatus != string(reviewdomain.CardStatusApproved)) {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "review card is no longer approved")
	}
	if err != nil {
		return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
	}
	var score reviewdomain.Score
	if err := decodeStrict(scoreJSON, &score); err != nil || reviewdomain.ValidateScore(score) != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted review score is invalid")
	}
	if len(score.Evidence) == 0 {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "review score has no verifiable evidence")
	}

	claimIDs := make([]string, 0, len(score.Evidence))
	sourceVersionIDs := make([]string, 0, len(score.Evidence))
	spanIDs := make([]string, 0, len(score.Evidence))
	hashes := make([]string, 0, len(score.Evidence))
	for _, evidence := range score.Evidence {
		claimIDs = append(claimIDs, string(evidence.ClaimID))
		sourceVersionIDs = append(sourceVersionIDs, string(evidence.SourceVersionID))
		spanIDs = append(spanIDs, string(evidence.SourceSpanID))
		hashes = append(hashes, evidence.EvidenceHash)
	}
	workspace := string(workspaceID)
	rows, err := gormLearningPathRawRows(tx.WithContext(ctx), gormReviewEvidenceSQL,
		pq.Array(claimIDs), pq.Array(sourceVersionIDs), pq.Array(spanIDs), pq.Array(hashes),
		workspace, cardID.String, workspace, workspace, workspace, workspace, workspace, workspace, workspace)
	if err != nil {
		return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
	}
	defer rows.Close()
	citations := make([]pathdomain.Citation, 0, len(score.Evidence))
	seen := make(map[int]struct{}, len(score.Evidence))
	for rows.Next() {
		var requestNo int
		var citation pathdomain.Citation
		var topicID sql.NullString
		if err := rows.Scan(&requestNo, &citation.ClaimID, &citation.IndexVersionID, &citation.ChunkID,
			&citation.SourceVersionID, &citation.SourceSpanID, &citation.EvidenceHash, &topicID); err != nil {
			return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
		}
		if requestNo < 1 || requestNo > len(score.Evidence) {
			return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence projection returned an invalid position")
		}
		if _, duplicate := seen[requestNo]; duplicate {
			return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence projection is ambiguous")
		}
		seen[requestNo] = struct{}{}
		if topicID.Valid {
			parsed, err := foundation.ParseID(topicID.String)
			if err != nil {
				return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence topic is invalid")
			}
			citation.TopicID = &parsed
		}
		expected := score.Evidence[requestNo-1]
		if citation.ClaimID != expected.ClaimID || citation.SourceVersionID != expected.SourceVersionID ||
			citation.SourceSpanID != expected.SourceSpanID || citation.EvidenceHash != expected.EvidenceHash ||
			pathdomain.ValidateCitation(citation) != nil {
			return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review evidence projection drifted")
		}
		citations = append(citations, citation)
	}
	if err := rows.Err(); err != nil {
		return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return pathapp.ReviewSnapshot{}, gormLearningPathClassify(ctx, err)
	}
	if len(citations) != len(score.Evidence) {
		return pathapp.ReviewSnapshot{}, pathdomain.ConflictError(pathdomain.ErrorCodeEvidenceStale, "one or more review score evidence bindings are no longer verifiable")
	}

	snapshot := pathapp.ReviewSnapshot{
		SchemaVersion: pathapp.ReviewSnapshotSchemaVersion, ReviewAnswerID: answerID,
		ReviewCardID: parsedCardID, ScorerVersion: scorerVersion, PolicyVersion: pathdomain.ReviewGapSchemaVersion,
		Gap: pathdomain.Gap{
			Errors: append([]string(nil), score.Errors...), Omissions: append([]string(nil), score.Omissions...),
			Correctness: score.Correctness.Value, Coverage: score.Coverage.Value, Boundaries: score.Boundaries.Value,
			Citations: citations,
		},
	}
	canonical, _, _, err := pathapp.CanonicalReviewSnapshot(snapshot)
	if err != nil {
		return pathapp.ReviewSnapshot{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "review path source snapshot is invalid")
	}
	return canonical, nil
}

func gormFindResultReceipt(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, requestHash, command string, expectedVersion int64) (pathapp.Result, bool, error) {
	row, err := gormLearningPathRawRow(database.WithContext(ctx), gormResultReceiptSQL, string(workspaceID), key)
	if err != nil {
		return pathapp.Result{}, false, gormLearningPathClassify(ctx, err)
	}
	var storedHash, storedCommand, pathID string
	var storedExpected, pathVersion int64
	var response []byte
	err = row.Scan(&storedHash, &storedCommand, &pathID, &storedExpected, &pathVersion, &response)
	if gormLearningPathNoRows(err) {
		return pathapp.Result{}, false, nil
	}
	if err != nil {
		return pathapp.Result{}, false, gormLearningPathClassify(ctx, err)
	}
	if storedHash != requestHash || storedCommand != command || storedExpected != expectedVersion {
		return pathapp.Result{}, false, pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path idempotency key is bound to another request")
	}
	var result pathapp.Result
	if err := decodeStrict(response, &result); err != nil || result.Replayed || validateResult(result, workspaceID, foundation.ID(pathID), pathVersion) != nil {
		return pathapp.Result{}, false, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path receipt is invalid")
	}
	result.Replayed = true
	return result, true, nil
}

func gormLoadPathResultByAnswer(ctx context.Context, database *gorm.DB, workspaceID, answerID foundation.ID, lock bool) (pathapp.Result, error) {
	query := gormPathByAnswerSQL
	if lock {
		query = gormPathByAnswerForUpdateSQL
	}
	return gormLoadResult(ctx, database, query, workspaceID, answerID, lock)
}

func gormLoadResultByPath(ctx context.Context, database *gorm.DB, workspaceID, pathID foundation.ID, lock bool) (pathapp.Result, error) {
	query := gormPathByIDSQL
	if lock {
		query = gormPathByIDForUpdateSQL
	}
	return gormLoadResult(ctx, database, query, workspaceID, pathID, lock)
}

func gormLoadResult(ctx context.Context, database *gorm.DB, query string, workspaceID, bindingID foundation.ID, lockSteps bool) (pathapp.Result, error) {
	row, err := gormLearningPathRawRow(database.WithContext(ctx), query, string(workspaceID), string(bindingID))
	if err != nil {
		return pathapp.Result{}, gormLearningPathClassify(ctx, err)
	}
	var path pathdomain.Path
	var pathID, persistedWorkspaceID, artifactID, artifactRevisionID string
	var origin, status string
	var sessionID, reportID, answerID sql.NullString
	err = row.Scan(
		&pathID, &persistedWorkspaceID, &origin, &sessionID, &reportID, &answerID,
		&path.SourcePolicyVersion, &artifactID, &artifactRevisionID,
		&path.Artifact.ArtifactVersion, &status, &path.Version, &path.CreatedAt, &path.UpdatedAt,
	)
	if gormLearningPathNoRows(err) {
		return pathapp.Result{}, pathdomain.NotFoundError(pathdomain.ErrorCodePathNotFound, "learning path was not found")
	}
	if err != nil {
		return pathapp.Result{}, gormLearningPathClassify(ctx, err)
	}
	path.ID = foundation.ID(pathID)
	path.WorkspaceID = foundation.ID(persistedWorkspaceID)
	path.Artifact.ArtifactID = foundation.ID(artifactID)
	path.Artifact.ArtifactRevisionID = foundation.ID(artifactRevisionID)
	path.OriginType, path.Status = pathdomain.Origin(origin), pathdomain.Status(status)
	if sessionID.Valid {
		value := foundation.ID(sessionID.String)
		path.InterviewSessionID = &value
	}
	if reportID.Valid {
		value := foundation.ID(reportID.String)
		path.InterviewReportID = &value
	}
	if answerID.Valid {
		value := foundation.ID(answerID.String)
		path.ReviewAnswerID = &value
	}

	stepsQuery := gormStepsSQL
	if lockSteps {
		stepsQuery = gormStepsForUpdateSQL
	}
	rows, err := gormLearningPathRawRows(database.WithContext(ctx), stepsQuery, string(workspaceID), pathID)
	if err != nil {
		return pathapp.Result{}, gormLearningPathClassify(ctx, err)
	}
	defer rows.Close()
	steps := make([]pathdomain.Step, 0)
	for rows.Next() {
		var step pathdomain.Step
		var stepID, stepWorkspaceID, stepPathID, claimID, sourceVersionID, sourceSpanID string
		var topicID sql.NullString
		var stepStatus string
		if err := rows.Scan(&stepID, &stepWorkspaceID, &stepPathID, &step.StepNo, &claimID,
			&topicID, &sourceVersionID, &sourceSpanID, &step.EvidenceHash,
			&step.Title, &step.Rationale, &stepStatus, &step.Version, &step.CreatedAt, &step.UpdatedAt); err != nil {
			return pathapp.Result{}, gormLearningPathClassify(ctx, err)
		}
		step.ID = foundation.ID(stepID)
		step.WorkspaceID = foundation.ID(stepWorkspaceID)
		step.PathID = foundation.ID(stepPathID)
		step.ClaimID = foundation.ID(claimID)
		step.SourceVersionID = foundation.ID(sourceVersionID)
		step.SourceSpanID = foundation.ID(sourceSpanID)
		if topicID.Valid {
			value := foundation.ID(topicID.String)
			step.TopicID = &value
		}
		step.Status = pathdomain.StepStatus(stepStatus)
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return pathapp.Result{}, gormLearningPathClassify(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return pathapp.Result{}, gormLearningPathClassify(ctx, err)
	}
	result := pathapp.Result{Path: path, Steps: steps}
	if err := validateResult(result, workspaceID, path.ID, path.Version); err != nil {
		return pathapp.Result{}, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "persisted learning path is invalid")
	}
	return result, nil
}

func gormInsertPath(ctx context.Context, tx *gorm.DB, path pathdomain.Path) error {
	var sessionID, reportID, answerID *string
	if path.InterviewSessionID != nil {
		value := string(*path.InterviewSessionID)
		sessionID = &value
	}
	if path.InterviewReportID != nil {
		value := string(*path.InterviewReportID)
		reportID = &value
	}
	if path.ReviewAnswerID != nil {
		value := string(*path.ReviewAnswerID)
		answerID = &value
	}
	record := learningPathGORMRecord{
		ID: string(path.ID), WorkspaceID: string(path.WorkspaceID), InterviewSessionID: sessionID,
		InterviewReportID: reportID, OriginType: string(path.OriginType), ReviewAnswerID: answerID,
		SourcePolicyVersion: path.SourcePolicyVersion, ArtifactID: string(path.Artifact.ArtifactID),
		ArtifactRevisionID: string(path.Artifact.ArtifactRevisionID), ArtifactVersion: path.Artifact.ArtifactVersion,
		Status: string(path.Status), Version: path.Version, CreatedAt: path.CreatedAt.UTC(), UpdatedAt: path.UpdatedAt.UTC(),
	}
	result := tx.WithContext(ctx).Select(
		"id", "workspace_id", "interview_session_id", "interview_report_id", "origin_type", "review_answer_id",
		"source_policy_version", "artifact_id", "artifact_revision_id", "artifact_version", "status", "version", "created_at", "updated_at",
	).Create(&record)
	if result.Error != nil {
		return gormLearningPathClassify(ctx, result.Error)
	}
	if result.RowsAffected != 1 {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path insert did not create exactly one row")
	}
	return nil
}

func gormInsertSteps(ctx context.Context, tx *gorm.DB, steps []pathdomain.Step) error {
	records := make([]learningPathStepGORMRecord, 0, len(steps))
	for _, step := range steps {
		var topicID *string
		if step.TopicID != nil {
			value := string(*step.TopicID)
			topicID = &value
		}
		records = append(records, learningPathStepGORMRecord{
			ID: string(step.ID), WorkspaceID: string(step.WorkspaceID), PathID: string(step.PathID), StepNo: step.StepNo,
			ClaimID: string(step.ClaimID), TopicID: topicID, SourceVersionID: string(step.SourceVersionID),
			SourceSpanID: string(step.SourceSpanID), EvidenceHash: step.EvidenceHash, Title: step.Title,
			Rationale: step.Rationale, Status: string(step.Status), Version: step.Version,
			CreatedAt: step.CreatedAt.UTC(), UpdatedAt: step.UpdatedAt.UTC(),
		})
	}
	result := tx.WithContext(ctx).Select(
		"id", "workspace_id", "path_id", "step_no", "claim_id", "topic_id", "source_version_id", "source_span_id",
		"evidence_hash", "title", "rationale", "status", "version", "created_at", "updated_at",
	).Create(&records)
	if result.Error != nil {
		return gormLearningPathClassify(ctx, result.Error)
	}
	if result.RowsAffected != int64(len(records)) {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step insert count is invalid")
	}
	return nil
}

func gormInsertResultReceipt(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key, hash, command string, expectedVersion int64, result pathapp.Result) error {
	if err := validateResult(result, workspaceID, result.Path.ID, result.Path.Version); err != nil {
		return err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path receipt cannot be encoded")
	}
	if _, err := gormLearningPathExec(ctx, tx, gormInsertResultReceiptSQL,
		string(workspaceID), key, hash, command, string(result.Path.ID), expectedVersion, result.Path.Version, learningPathJSONB(payload)); err != nil {
		return gormLearningPathClassify(ctx, err)
	}
	return nil
}

func gormFindStepReceipt(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key, requestHash string, expectedVersion int64) (pathapp.StepResult, bool, error) {
	row, err := gormLearningPathRawRow(database.WithContext(ctx), gormResultReceiptSQL, string(workspaceID), key)
	if err != nil {
		return pathapp.StepResult{}, false, gormLearningPathClassify(ctx, err)
	}
	var storedHash, command, pathID string
	var storedExpected, pathVersion int64
	var response []byte
	err = row.Scan(&storedHash, &command, &pathID, &storedExpected, &pathVersion, &response)
	if gormLearningPathNoRows(err) {
		return pathapp.StepResult{}, false, nil
	}
	if err != nil {
		return pathapp.StepResult{}, false, gormLearningPathClassify(ctx, err)
	}
	if storedHash != requestHash || command != pathapp.CommandTypePathStep || storedExpected != expectedVersion {
		return pathapp.StepResult{}, false, pathdomain.ConflictError(pathdomain.ErrorCodeIdempotencyConflict, "learning path idempotency key is bound to another request")
	}
	var value pathapp.StepResult
	if err := decodeStrict(response, &value); err != nil || value.Replayed || pathdomain.ValidatePath(value.Path) != nil ||
		pathdomain.ValidateStep(value.Step) != nil || value.Path.WorkspaceID != workspaceID || value.Path.ID != foundation.ID(pathID) ||
		value.Path.Version != pathVersion || value.Path.OriginType != pathdomain.OriginReview || value.Step.WorkspaceID != workspaceID ||
		value.Step.PathID != value.Path.ID {
		return pathapp.StepResult{}, false, pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step receipt is invalid")
	}
	value.Replayed = true
	return value, true, nil
}

func gormInsertStepReceipt(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key, hash string, expectedVersion int64, value pathapp.StepResult) error {
	if value.Replayed || pathdomain.ValidatePath(value.Path) != nil || pathdomain.ValidateStep(value.Step) != nil ||
		value.Path.WorkspaceID != workspaceID || value.Step.WorkspaceID != workspaceID || value.Step.PathID != value.Path.ID ||
		value.Path.OriginType != pathdomain.OriginReview {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step receipt cannot be encoded")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path step receipt cannot be encoded")
	}
	if _, err := gormLearningPathExec(ctx, tx, gormInsertResultReceiptSQL,
		string(workspaceID), key, hash, pathapp.CommandTypePathStep, string(value.Path.ID), expectedVersion, value.Path.Version, learningPathJSONB(payload)); err != nil {
		return gormLearningPathClassify(ctx, err)
	}
	return nil
}

func gormLearningPathNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormLearningPathClassify(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var known *foundation.Error
	if errors.As(err, &known) {
		return err
	}
	if cause := gormLearningPathContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, pathdomain.ErrorCodeDependencyUnavailable, false, cause)
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, pathdomain.ErrorCodeDependencyUnavailable, true, cause)
	}
	switch platformpostgres.SQLState(err) {
	case "23505":
		return foundation.NewError(foundation.ErrorVersionConflict, pathdomain.ErrorCodeIdempotencyConflict, false, err)
	case "23503", "23514", "23502", "22001", "22P02":
		return foundation.NewError(foundation.ErrorConsistencyViolation, pathdomain.ErrorCodePersistenceInvalid, false, err)
	case "40001", "40P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, pathdomain.ErrorCodeDependencyUnavailable, true, err)
	}
	return gormLearningPathUnavailable(err)
}

func gormLearningPathContextCause(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			return ctx.Err()
		}
		if !errors.Is(cause, ctx.Err()) {
			return errors.Join(ctx.Err(), cause)
		}
		return cause
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

func gormLearningPathUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, pathdomain.ErrorCodeDependencyUnavailable, true, cause)
}

func validLearningPathGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilLearningPathUnitOfWork(unitOfWork foundation.UnitOfWork) bool {
	if unitOfWork == nil {
		return true
	}
	value := reflect.ValueOf(unitOfWork)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
