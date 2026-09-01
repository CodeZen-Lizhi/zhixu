package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"gorm.io/gorm"
)

// GORMDeliveryRepository is the staged GORM implementation of the Reindex
// delivery lease and checkpoint state machine. Production remains on the pgx
// repository until the PostgreSQL parity gate passes.
type GORMDeliveryRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	ids        foundation.IDGenerator
}

var _ application.DeliveryRuntimePort = (*GORMDeliveryRepository)(nil)

// NewGORMDeliveryRepository derives GORM and UoW dependencies from one shared
// PostgreSQL Pool. It opens no connection and performs no schema mutation.
func NewGORMDeliveryRepository(pool *platformpostgres.Pool, ids foundation.IDGenerator) (*GORMDeliveryRepository, error) {
	if nilInterface(ids) {
		return nil, dependency("REINDEX_DELIVERY_DEPENDENCY_UNAVAILABLE", errors.New("delivery id generator is nil"))
	}
	database, unitOfWork, err := gormRetrievalDependencies(pool)
	if err != nil {
		return nil, dependency("REINDEX_DELIVERY_DEPENDENCY_UNAVAILABLE", fmt.Errorf("delivery GORM dependencies are unavailable: %w", err))
	}
	return &GORMDeliveryRepository{database: database, unitOfWork: unitOfWork, ids: ids}, nil
}

// Claim obtains the current generation, exactly replays an active claim, or
// appends a replacement Attempt after PostgreSQL proves the lease expired.
func (repository *GORMDeliveryRepository) Claim(ctx context.Context, command application.DeliveryClaimCommand) (application.DeliveryClaimResult, error) {
	var result application.DeliveryClaimResult
	err := repository.within(ctx, "REINDEX_DELIVERY_CLAIM_TRANSACTION_FAILED", "REINDEX_DELIVERY_CLAIM_COMMIT_FAILED", func(callbackCtx context.Context, tx *gorm.DB) error {
		delivery, err := gormDeliveryLock(callbackCtx, tx, command.DeliveryID)
		if err != nil {
			return err
		}
		if command.DispatchNo < delivery.DispatchNo {
			result = application.DeliveryClaimResult{Disposition: application.DeliveryClaimStale}
			return nil
		}
		if command.DispatchNo > delivery.DispatchNo {
			return consistency("REINDEX_DISPATCH_BINDING_CONFLICT", errors.New("claim generation is ahead of persisted delivery"))
		}

		// Lock the current Attempt before consulting DB time. This order is shared
		// by claim, reclaim and all fenced mutations.
		var lockedAttempt *domain.DeliveryAttempt
		if delivery.CurrentAttemptID != nil {
			attempt, queryErr := gormDeliveryCurrentAttempt(callbackCtx, tx, delivery)
			if queryErr != nil {
				return queryErr
			}
			lockedAttempt = &attempt
		}

		if isCommittedDelivery(delivery.Status) {
			if lockedAttempt == nil {
				return consistency("REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED", errors.New("committed delivery has no current attempt"))
			}
			if !sameClaimTransport(*lockedAttempt, command) {
				result = application.DeliveryClaimResult{Disposition: application.DeliveryClaimStale}
				return nil
			}
			result = application.DeliveryClaimResult{
				Disposition: application.DeliveryClaimCommitted,
				Delivery:    delivery,
				Attempt:     *lockedAttempt,
				Replayed:    true,
			}
			return nil
		}

		reclaimed := false
		var databaseNow time.Time
		if delivery.Status == domain.DeliveryStatusProcessing {
			if lockedAttempt == nil {
				return consistency("REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED", errors.New("processing delivery has no current attempt"))
			}
			databaseNow, err = gormDeliveryDatabaseNow(callbackCtx, tx)
			if err != nil {
				return err
			}
			if databaseNow.Before(lockedAttempt.LeaseUntil) {
				if sameClaimTransport(*lockedAttempt, command) {
					result = claimedResult(delivery, *lockedAttempt, true, false)
					return nil
				}
				return foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_HELD", true, errors.New("another transport attempt holds the database lease"))
			}
			if _, queryErr := gormRetrievalExec(callbackCtx, tx, `UPDATE retrieval.reindex_delivery_attempt SET
				status='lease_lost',failure_class='retryable',error_kind=?,error_code='REINDEX_DELIVERY_LEASE_EXPIRED',
				error_summary='delivery lease expired before transport reclaim',ended_at=?
				WHERE id=? AND status='processing' AND ? >= lease_until`, string(foundation.ErrorRetryableFailure), databaseNow,
				string(lockedAttempt.ID), databaseNow); queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "REINDEX_DELIVERY_LEASE_LOST_FAILED")
			}
			if _, queryErr := gormRetrievalExec(callbackCtx, tx, `UPDATE retrieval.reindex_delivery SET status='dispatched',version=version+1,updated_at=?
				WHERE id=? AND status='processing' AND current_attempt_id=?`, databaseNow, string(delivery.ID), string(lockedAttempt.ID)); queryErr != nil {
				return classifyGORMRetrieval(callbackCtx, queryErr, "REINDEX_DELIVERY_RECLAIM_PREPARE_FAILED")
			}
			delivery.Status = domain.DeliveryStatusDispatched
			delivery.Version++
			reclaimed = true
		}
		if delivery.Status != domain.DeliveryStatusDispatched {
			result = application.DeliveryClaimResult{Disposition: application.DeliveryClaimStale}
			return nil
		}
		if databaseNow.IsZero() {
			databaseNow, err = gormDeliveryDatabaseNow(callbackCtx, tx)
			if err != nil {
				return err
			}
		}

		var inheritedIngestionAttempt any
		if delivery.ParseProjectionID != nil {
			if lockedAttempt == nil || lockedAttempt.IngestionAttemptID == nil {
				return consistency("REINDEX_DELIVERY_INGESTION_BINDING_INVALID", errors.New("persisted ingestion checkpoint has no attempt evidence"))
			}
			inheritedIngestionAttempt = string(*lockedAttempt.IngestionAttemptID)
		}

		attemptID, err := repository.ids.New()
		if err != nil {
			return err
		}
		attemptNo := delivery.AttemptNo + 1
		row, err := gormRetrievalRawRow(callbackCtx, tx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
			INSERT INTO retrieval.reindex_delivery_attempt
			(id,delivery_id,attempt_no,dispatch_no,river_job_id,river_attempt,delivery_key,lease_owner,lease_until,status,ingestion_attempt_id,started_at,heartbeat_at)
			SELECT ?,?,?,?,?,?,?,?,database_clock.now + (? * interval '1 microsecond'),'processing',?,database_clock.now,database_clock.now
			FROM database_clock RETURNING `+deliveryAttemptColumns,
			string(attemptID), string(delivery.ID), attemptNo, command.DispatchNo, command.RiverJobID, command.RiverAttempt,
			command.DeliveryKey, command.LeaseOwner, command.LeaseDuration.Microseconds(), inheritedIngestionAttempt)
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_ATTEMPT_CREATE_FAILED")
		}
		attempt, err := scanDeliveryAttempt(row)
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_ATTEMPT_CREATE_FAILED")
		}

		row, err = gormRetrievalRawRow(callbackCtx, tx, `UPDATE retrieval.reindex_delivery SET
			status='processing',attempt_no=?,current_attempt_id=?,version=version+1,updated_at=?
			WHERE id=? AND status='dispatched' AND dispatch_no=? RETURNING `+deliveryColumns,
			attemptNo, string(attemptID), databaseNow, string(delivery.ID), command.DispatchNo)
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_CLAIM_UPDATE_FAILED")
		}
		delivery, err = scanDelivery(row)
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_CLAIM_UPDATE_FAILED")
		}
		result = claimedResult(delivery, attempt, false, reclaimed)
		return nil
	})
	if err != nil {
		return application.DeliveryClaimResult{}, err
	}
	return result, nil
}

// Heartbeat renews an unexpired Attempt and advances the Delivery fence using
// the same PostgreSQL wall-clock value.
func (repository *GORMDeliveryRepository) Heartbeat(ctx context.Context, command application.DeliveryHeartbeatCommand) (application.DeliveryMutationResult, error) {
	return repository.mutate(ctx, command.Fence, true, func(delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
		return attempt.HeartbeatAt.Equal(delivery.UpdatedAt)
	}, func(callbackCtx context.Context, tx *gorm.DB, delivery domain.Delivery, attempt domain.DeliveryAttempt, _ time.Time) (application.DeliveryMutationResult, error) {
		row, err := gormRetrievalRawRow(callbackCtx, tx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
			UPDATE retrieval.reindex_delivery_attempt SET
			heartbeat_at=database_clock.now,lease_until=database_clock.now + (? * interval '1 microsecond')
			FROM database_clock
			WHERE id=? AND status='processing' AND database_clock.now < lease_until RETURNING `+deliveryAttemptColumns,
			command.LeaseDuration.Microseconds(), string(attempt.ID))
		if err != nil {
			return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_HEARTBEAT_ATTEMPT_FAILED")
		}
		attempt, err = scanDeliveryAttempt(row)
		if gormRetrievalNoRows(err) {
			return application.DeliveryMutationResult{}, deliveryLeaseExpired()
		}
		if err != nil {
			return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_HEARTBEAT_ATTEMPT_FAILED")
		}
		delivery, err = gormDeliveryBump(callbackCtx, tx, delivery.ID, attempt.HeartbeatAt)
		if err != nil {
			return application.DeliveryMutationResult{}, err
		}
		return appliedMutation(delivery, attempt, false), nil
	})
}

// Checkpoint persists one irreversible Source, Ingestion, Index or Regression
// binding while the complete Delivery fence and lease remain current.
func (repository *GORMDeliveryRepository) Checkpoint(ctx context.Context, command application.DeliveryCheckpointCommand) (application.DeliveryMutationResult, error) {
	return repository.mutate(ctx, command.Fence, true, func(delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
		return domain.DeliveryCheckpointMatches(delivery, attempt, command.Checkpoint)
	}, func(callbackCtx context.Context, tx *gorm.DB, delivery domain.Delivery, attempt domain.DeliveryAttempt, _ time.Time) (application.DeliveryMutationResult, error) {
		if domain.DeliveryCheckpointMatches(delivery, attempt, command.Checkpoint) {
			return appliedMutation(delivery, attempt, true), nil
		}
		rebindIngestion := checkpointRebindsCurrentAttempt(delivery, attempt, command.Checkpoint)
		if !checkpointCanAdvance(delivery, command.Checkpoint) && !rebindIngestion {
			return application.DeliveryMutationResult{}, consistency("REINDEX_DELIVERY_CHECKPOINT_ORDER_INVALID", errors.New("checkpoint does not extend the persisted prefix"))
		}
		var err error
		if command.Checkpoint.Stage != domain.DeliveryCheckpointSourceCaptured {
			row, queryErr := gormRetrievalRawRow(callbackCtx, tx, `SELECT EXISTS (
				SELECT 1 FROM ingestion.attempt ingestion_attempt
				WHERE ingestion_attempt.id=?
				  AND ingestion_attempt.workspace_id=?
				  AND ingestion_attempt.source_version_id=?
				  AND ingestion_attempt.parse_projection_id=?
				  AND ingestion_attempt.status='chunked'
				  AND ingestion_attempt.security_status='passed'
			)`, string(command.Checkpoint.IngestionAttemptID), string(delivery.WorkspaceID),
				string(command.Checkpoint.SourceVersionID), string(command.Checkpoint.ParseProjectionID))
			if queryErr != nil {
				return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, queryErr, "REINDEX_DELIVERY_INGESTION_BINDING_QUERY_FAILED")
			}
			var bindingOK bool
			if queryErr = row.Scan(&bindingOK); queryErr != nil {
				return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, queryErr, "REINDEX_DELIVERY_INGESTION_BINDING_QUERY_FAILED")
			}
			if !bindingOK {
				return application.DeliveryMutationResult{}, consistency("REINDEX_DELIVERY_INGESTION_BINDING_INVALID", errors.New("ingestion attempt does not match the delivery checkpoint"))
			}

			row, queryErr = gormRetrievalRawRow(callbackCtx, tx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
				UPDATE retrieval.reindex_delivery_attempt SET ingestion_attempt_id=?
				FROM database_clock
				WHERE id=? AND status='processing' AND database_clock.now < lease_until RETURNING `+deliveryAttemptColumns,
				string(command.Checkpoint.IngestionAttemptID), string(attempt.ID))
			if queryErr != nil {
				return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, queryErr, "REINDEX_DELIVERY_CHECKPOINT_ATTEMPT_FAILED")
			}
			attempt, err = scanDeliveryAttempt(row)
			if gormRetrievalNoRows(err) {
				return application.DeliveryMutationResult{}, deliveryLeaseExpired()
			}
			if err != nil {
				return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_CHECKPOINT_ATTEMPT_FAILED")
			}
		}
		if rebindIngestion {
			delivery, err = gormDeliveryBumpLive(callbackCtx, tx, delivery.ID)
			if err != nil {
				return application.DeliveryMutationResult{}, err
			}
			return appliedMutation(delivery, attempt, false), nil
		}
		delivery, err = gormDeliveryPersistCheckpoint(callbackCtx, tx, delivery.ID, command.Checkpoint)
		if err != nil {
			return application.DeliveryMutationResult{}, err
		}
		return appliedMutation(delivery, attempt, false), nil
	})
}

// Fail atomically moves the Attempt first and then its Delivery to the matching
// retry, terminal or manual-recovery state.
func (repository *GORMDeliveryRepository) Fail(ctx context.Context, command application.DeliveryFailureCommand) (application.DeliveryMutationResult, error) {
	return repository.mutate(ctx, command.Fence, false, func(delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
		return delivery.Failure != nil && attempt.Failure != nil && *delivery.Failure == command.Failure && *attempt.Failure == command.Failure
	}, func(callbackCtx context.Context, tx *gorm.DB, delivery domain.Delivery, attempt domain.DeliveryAttempt, _ time.Time) (application.DeliveryMutationResult, error) {
		attemptStatus, deliveryStatus := failureStatuses(command.Failure.Class)
		row, err := gormRetrievalRawRow(callbackCtx, tx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
			UPDATE retrieval.reindex_delivery_attempt SET
			status=?,failure_class=?,error_kind=?,error_code=?,error_summary=?,ended_at=database_clock.now
			FROM database_clock
			WHERE id=? AND status='processing' AND database_clock.now < lease_until RETURNING `+deliveryAttemptColumns,
			string(attemptStatus), string(command.Failure.Class), string(command.Failure.ErrorKind), command.Failure.Code,
			command.Failure.Summary, string(attempt.ID))
		if err != nil {
			return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_FAILURE_ATTEMPT_FAILED")
		}
		attempt, err = scanDeliveryAttempt(row)
		if gormRetrievalNoRows(err) {
			return application.DeliveryMutationResult{}, deliveryLeaseExpired()
		}
		if err != nil {
			return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_FAILURE_ATTEMPT_FAILED")
		}

		endedAt := *attempt.EndedAt
		status := string(deliveryStatus)
		row, err = gormRetrievalRawRow(callbackCtx, tx, `UPDATE retrieval.reindex_delivery SET status=?,
			next_attempt_at=CASE WHEN ?='retry_wait' THEN ?::timestamptz + (? * interval '1 microsecond') ELSE NULL END,
			failure_class=?,error_kind=?,error_code=?,error_summary=?,
			manual_recovery_required=(?='manual_recovery'),completed_at=CASE WHEN ? IN ('failed','succeeded') THEN ? ELSE NULL END,
			version=version+1,updated_at=? WHERE id=? AND status='processing' RETURNING `+deliveryColumns,
			status, status, endedAt, command.RetryDelay.Microseconds(), string(command.Failure.Class), string(command.Failure.ErrorKind),
			command.Failure.Code, command.Failure.Summary, status, status, endedAt, endedAt, string(delivery.ID))
		if err != nil {
			return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_FAILURE_UPDATE_FAILED")
		}
		delivery, err = scanDelivery(row)
		if err != nil {
			return application.DeliveryMutationResult{}, classifyGORMRetrieval(callbackCtx, err, "REINDEX_DELIVERY_FAILURE_UPDATE_FAILED")
		}
		return application.DeliveryMutationResult{
			Disposition: application.DeliveryMutationCommitted,
			Delivery:    delivery,
			Attempt:     attempt,
		}, nil
	})
}

type gormDeliveryMutation func(context.Context, *gorm.DB, domain.Delivery, domain.DeliveryAttempt, time.Time) (application.DeliveryMutationResult, error)

func (repository *GORMDeliveryRepository) mutate(
	ctx context.Context,
	fence domain.DeliveryFence,
	processingReplay bool,
	replayMatches committedReplayMatcher,
	apply gormDeliveryMutation,
) (application.DeliveryMutationResult, error) {
	var result application.DeliveryMutationResult
	err := repository.within(ctx, "REINDEX_DELIVERY_MUTATION_TRANSACTION_FAILED", "REINDEX_DELIVERY_MUTATION_COMMIT_FAILED", func(callbackCtx context.Context, tx *gorm.DB) error {
		delivery, err := gormDeliveryLock(callbackCtx, tx, fence.DeliveryID)
		if err != nil {
			return err
		}
		if delivery.CurrentAttemptID == nil {
			result = application.DeliveryMutationResult{Disposition: application.DeliveryMutationStale}
			return nil
		}
		attempt, err := gormDeliveryCurrentAttempt(callbackCtx, tx, delivery)
		if err != nil {
			return err
		}
		databaseNow, err := gormDeliveryDatabaseNow(callbackCtx, tx)
		if err != nil {
			return err
		}
		if !fenceMatches(fence, delivery, attempt) {
			if isCommittedDelivery(delivery.Status) && committedFenceIdentityMatches(fence, delivery, attempt) && replayMatches(delivery, attempt) {
				result = application.DeliveryMutationResult{
					Disposition: application.DeliveryMutationCommitted,
					Delivery:    delivery,
					Attempt:     attempt,
					Replayed:    true,
				}
				return nil
			}
			if processingReplay && delivery.Status == domain.DeliveryStatusProcessing && attempt.Status == domain.DeliveryAttemptProcessing &&
				delivery.Version > fence.DeliveryVersion && committedFenceIdentityMatches(fence, delivery, attempt) && replayMatches(delivery, attempt) {
				if !databaseNow.Before(attempt.LeaseUntil) {
					return deliveryLeaseExpired()
				}
				result = appliedMutation(delivery, attempt, true)
				return nil
			}
			if fence.DeliveryID == delivery.ID && fence.DispatchNo == delivery.DispatchNo && fence.AttemptID != attempt.ID {
				row, queryErr := gormRetrievalRawRow(callbackCtx, tx, `SELECT status FROM retrieval.reindex_delivery_attempt WHERE id=? AND delivery_id=?`, string(fence.AttemptID), string(delivery.ID))
				if queryErr != nil {
					return classifyGORMRetrieval(callbackCtx, queryErr, "REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED")
				}
				var priorStatus domain.DeliveryAttemptStatus
				queryErr = row.Scan(&priorStatus)
				if queryErr == nil && priorStatus == domain.DeliveryAttemptLeaseLost {
					return foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_LOST", true, errors.New("delivery fence belongs to a reclaimed attempt"))
				}
				if queryErr != nil && !gormRetrievalNoRows(queryErr) {
					return classifyGORMRetrieval(callbackCtx, queryErr, "REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED")
				}
			}
			result = application.DeliveryMutationResult{Disposition: application.DeliveryMutationStale}
			return nil
		}
		if delivery.Status != domain.DeliveryStatusProcessing || attempt.Status != domain.DeliveryAttemptProcessing {
			result = application.DeliveryMutationResult{Disposition: application.DeliveryMutationStale}
			return nil
		}
		if !databaseNow.Before(attempt.LeaseUntil) {
			return deliveryLeaseExpired()
		}
		result, err = apply(callbackCtx, tx, delivery, attempt, databaseNow)
		return err
	})
	if err != nil {
		return application.DeliveryMutationResult{}, err
	}
	return result, nil
}

func (repository *GORMDeliveryRepository) within(ctx context.Context, transactionCode, commitCode string, work func(context.Context, *gorm.DB) error) error {
	return repository.withinOptions(ctx, foundation.TransactionOptions{}, transactionCode, commitCode, work)
}

func (repository *GORMDeliveryRepository) withinOptions(
	ctx context.Context,
	options foundation.TransactionOptions,
	transactionCode string,
	commitCode string,
	work func(context.Context, *gorm.DB) error,
) error {
	if repository == nil || !validGORMRetrievalDatabase(repository.database) || nilInterface(repository.unitOfWork) || nilInterface(repository.ids) {
		return dependency("REINDEX_DELIVERY_DEPENDENCY_UNAVAILABLE", errors.New("delivery GORM repository is unavailable"))
	}
	return withinGORMRetrieval(ctx, repository.unitOfWork, options, transactionCode, commitCode,
		func(callbackCtx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
			return work(callbackCtx, tx)
		})
}

func gormDeliveryLock(ctx context.Context, tx *gorm.DB, id foundation.ID) (domain.Delivery, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT `+deliveryColumns+` FROM retrieval.reindex_delivery WHERE id=? FOR UPDATE`, string(id))
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_QUERY_FAILED")
	}
	value, err := scanDelivery(row)
	if gormRetrievalNoRows(err) {
		return domain.Delivery{}, notFound("REINDEX_DELIVERY_NOT_FOUND", err)
	}
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_QUERY_FAILED")
	}
	return value, nil
}

func gormDeliveryCurrentAttempt(ctx context.Context, tx *gorm.DB, delivery domain.Delivery) (domain.DeliveryAttempt, error) {
	if delivery.CurrentAttemptID == nil {
		return domain.DeliveryAttempt{}, consistency("REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED", errors.New("delivery has no current attempt"))
	}
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT `+deliveryAttemptColumns+` FROM retrieval.reindex_delivery_attempt
		WHERE id=? AND delivery_id=? FOR UPDATE`, string(*delivery.CurrentAttemptID), string(delivery.ID))
	if err != nil {
		return domain.DeliveryAttempt{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED")
	}
	value, err := scanDeliveryAttempt(row)
	if err != nil {
		return domain.DeliveryAttempt{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED")
	}
	return value, nil
}

func gormDeliveryBump(ctx context.Context, tx *gorm.DB, id foundation.ID, at time.Time) (domain.Delivery, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `UPDATE retrieval.reindex_delivery SET version=version+1,updated_at=?
		WHERE id=? AND status='processing' RETURNING `+deliveryColumns, at, string(id))
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_FENCE_UPDATE_FAILED")
	}
	value, err := scanDelivery(row)
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_FENCE_UPDATE_FAILED")
	}
	return value, nil
}

func gormDeliveryBumpLive(ctx context.Context, tx *gorm.DB, id foundation.ID) (domain.Delivery, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
		UPDATE retrieval.reindex_delivery delivery SET version=delivery.version+1,updated_at=database_clock.now
		FROM database_clock
		WHERE delivery.id=? AND delivery.status='processing' AND EXISTS (
			SELECT 1 FROM retrieval.reindex_delivery_attempt attempt
			WHERE attempt.id=delivery.current_attempt_id AND attempt.status='processing'
			  AND database_clock.now < attempt.lease_until
		) RETURNING `+deliveryColumns, string(id))
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_FENCE_UPDATE_FAILED")
	}
	value, err := scanDelivery(row)
	if gormRetrievalNoRows(err) {
		return domain.Delivery{}, deliveryLeaseExpired()
	}
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_FENCE_UPDATE_FAILED")
	}
	return value, nil
}

func gormDeliveryPersistCheckpoint(ctx context.Context, tx *gorm.DB, id foundation.ID, checkpoint domain.DeliveryCheckpoint) (domain.Delivery, error) {
	query := `WITH database_clock AS (SELECT clock_timestamp() AS now)
		UPDATE retrieval.reindex_delivery delivery SET source_version_id=?,version=delivery.version+1,updated_at=database_clock.now`
	arguments := []any{string(checkpoint.SourceVersionID)}
	switch checkpoint.Stage {
	case domain.DeliveryCheckpointIngested:
		query += `,parse_projection_id=?`
		arguments = append(arguments, string(checkpoint.ParseProjectionID))
	case domain.DeliveryCheckpointIndexBuilding:
		query += `,parse_projection_id=?,index_version_id=?,excluded_source_count=?`
		arguments = append(arguments, string(checkpoint.ParseProjectionID), string(checkpoint.IndexVersionID), *checkpoint.ExcludedSourceCount)
	case domain.DeliveryCheckpointRegressionPassed:
		query += `,parse_projection_id=?,index_version_id=?,excluded_source_count=?,regression_code=?,regression_hash=?,regression_passed_at=database_clock.now`
		arguments = append(arguments, string(checkpoint.ParseProjectionID), string(checkpoint.IndexVersionID), *checkpoint.ExcludedSourceCount,
			checkpoint.RegressionCode, checkpoint.RegressionHash)
	}
	query += ` FROM database_clock WHERE delivery.id=? AND delivery.status='processing' AND EXISTS (
		SELECT 1 FROM retrieval.reindex_delivery_attempt attempt
		WHERE attempt.id=delivery.current_attempt_id AND attempt.status='processing'
		  AND database_clock.now < attempt.lease_until
	) RETURNING ` + deliveryColumns
	arguments = append(arguments, string(id))

	row, err := gormRetrievalRawRow(ctx, tx, query, arguments...)
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_CHECKPOINT_UPDATE_FAILED")
	}
	value, err := scanDelivery(row)
	if gormRetrievalNoRows(err) {
		return domain.Delivery{}, deliveryLeaseExpired()
	}
	if err != nil {
		return domain.Delivery{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_CHECKPOINT_UPDATE_FAILED")
	}
	return value, nil
}

func gormDeliveryDatabaseNow(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_CLOCK_QUERY_FAILED")
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, classifyGORMRetrieval(ctx, err, "REINDEX_DELIVERY_CLOCK_QUERY_FAILED")
	}
	return now.UTC(), nil
}
