package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

// DeliveryRepository 使用 PostgreSQL 数据库时间实现 Reindex Delivery 的租约与 Fence。
type DeliveryRepository struct {
	db  DB
	ids foundation.IDGenerator
}

var _ application.DeliveryRuntimePort = (*DeliveryRepository)(nil)

// NewDeliveryRepository 创建使用追加式 Attempt 与数据库时间租约的 Delivery Repository。
func NewDeliveryRepository(db DB, ids foundation.IDGenerator) (*DeliveryRepository, error) {
	if db == nil || nilInterface(ids) {
		return nil, dependency("REINDEX_DELIVERY_DEPENDENCY_UNAVAILABLE", errors.New("delivery database or id generator is nil"))
	}
	return &DeliveryRepository{db: db, ids: ids}, nil
}

// Claim 取得当前 generation、精确重放有效 Claim，或在数据库确认过期后追加新 Attempt。
func (r *DeliveryRepository) Claim(ctx context.Context, command application.DeliveryClaimCommand) (application.DeliveryClaimResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.DeliveryClaimResult{}, classify(err, "REINDEX_DELIVERY_CLAIM_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	delivery, err := lockDelivery(ctx, tx, command.DeliveryID)
	if err != nil {
		return application.DeliveryClaimResult{}, err
	}
	if command.DispatchNo < delivery.DispatchNo {
		return commitClaimResult(ctx, tx, application.DeliveryClaimResult{Disposition: application.DeliveryClaimStale})
	}
	if command.DispatchNo > delivery.DispatchNo {
		return application.DeliveryClaimResult{}, consistency("REINDEX_DISPATCH_BINDING_CONFLICT", errors.New("claim generation is ahead of persisted delivery"))
	}
	if isCommittedDelivery(delivery.Status) {
		attempt, queryErr := currentAttempt(ctx, tx, delivery)
		if queryErr != nil {
			return application.DeliveryClaimResult{}, queryErr
		}
		if !sameClaimTransport(attempt, command) {
			return commitClaimResult(ctx, tx, application.DeliveryClaimResult{Disposition: application.DeliveryClaimStale})
		}
		return commitClaimResult(ctx, tx, application.DeliveryClaimResult{
			Disposition: application.DeliveryClaimCommitted, Delivery: delivery, Attempt: attempt, Replayed: true,
		})
	}

	reclaimed := false
	var databaseNow time.Time
	if delivery.Status == domain.DeliveryStatusProcessing {
		attempt, queryErr := currentAttempt(ctx, tx, delivery)
		if queryErr != nil {
			return application.DeliveryClaimResult{}, queryErr
		}
		databaseNow, queryErr = deliveryDatabaseNow(ctx, tx)
		if queryErr != nil {
			return application.DeliveryClaimResult{}, queryErr
		}
		leaseActive := databaseNow.Before(attempt.LeaseUntil)
		if leaseActive {
			if sameClaimTransport(attempt, command) {
				return commitClaimResult(ctx, tx, claimedResult(delivery, attempt, true, false))
			}
			return application.DeliveryClaimResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_HELD", true, errors.New("another transport attempt holds the database lease"))
		}
		if _, queryErr = tx.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET
			status='lease_lost',failure_class='retryable',error_kind=$2,error_code='REINDEX_DELIVERY_LEASE_EXPIRED',
			error_summary='delivery lease expired before transport reclaim',ended_at=$3
			WHERE id=$1 AND status='processing' AND $3 >= lease_until`, string(attempt.ID), string(foundation.ErrorRetryableFailure), databaseNow); queryErr != nil {
			return application.DeliveryClaimResult{}, classify(queryErr, "REINDEX_DELIVERY_LEASE_LOST_FAILED")
		}
		if _, queryErr = tx.Exec(ctx, `UPDATE retrieval.reindex_delivery SET status='dispatched',version=version+1,updated_at=$3
			WHERE id=$1 AND status='processing' AND current_attempt_id=$2`, string(delivery.ID), string(attempt.ID), databaseNow); queryErr != nil {
			return application.DeliveryClaimResult{}, classify(queryErr, "REINDEX_DELIVERY_RECLAIM_PREPARE_FAILED")
		}
		delivery.Status = domain.DeliveryStatusDispatched
		delivery.Version++
		reclaimed = true
	}
	if delivery.Status != domain.DeliveryStatusDispatched {
		return commitClaimResult(ctx, tx, application.DeliveryClaimResult{Disposition: application.DeliveryClaimStale})
	}
	if databaseNow.IsZero() {
		databaseNow, err = deliveryDatabaseNow(ctx, tx)
		if err != nil {
			return application.DeliveryClaimResult{}, err
		}
	}
	var inheritedIngestionAttempt any
	if delivery.ParseProjectionID != nil {
		priorAttempt, queryErr := currentAttempt(ctx, tx, delivery)
		if queryErr != nil {
			return application.DeliveryClaimResult{}, queryErr
		}
		if priorAttempt.IngestionAttemptID == nil {
			return application.DeliveryClaimResult{}, consistency("REINDEX_DELIVERY_INGESTION_BINDING_INVALID", errors.New("persisted ingestion checkpoint has no attempt evidence"))
		}
		inheritedIngestionAttempt = string(*priorAttempt.IngestionAttemptID)
	}

	attemptID, err := r.ids.New()
	if err != nil {
		return application.DeliveryClaimResult{}, err
	}
	attemptNo := delivery.AttemptNo + 1
	attempt, err := scanDeliveryAttempt(tx.QueryRow(ctx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
		INSERT INTO retrieval.reindex_delivery_attempt
		(id,delivery_id,attempt_no,dispatch_no,river_job_id,river_attempt,delivery_key,lease_owner,lease_until,status,ingestion_attempt_id,started_at,heartbeat_at)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,database_clock.now + ($9 * interval '1 microsecond'),'processing',$10,database_clock.now,database_clock.now
		FROM database_clock
		RETURNING `+deliveryAttemptColumns, string(attemptID), string(delivery.ID), attemptNo, command.DispatchNo,
		command.RiverJobID, command.RiverAttempt, command.DeliveryKey, command.LeaseOwner, command.LeaseDuration.Microseconds(), inheritedIngestionAttempt))
	if err != nil {
		return application.DeliveryClaimResult{}, classify(err, "REINDEX_DELIVERY_ATTEMPT_CREATE_FAILED")
	}
	delivery, err = scanDelivery(tx.QueryRow(ctx, `UPDATE retrieval.reindex_delivery SET
		status='processing',attempt_no=$2,current_attempt_id=$3,version=version+1,updated_at=$5
		WHERE id=$1 AND status='dispatched' AND dispatch_no=$4 RETURNING `+deliveryColumns,
		string(delivery.ID), attemptNo, string(attemptID), command.DispatchNo, databaseNow))
	if err != nil {
		return application.DeliveryClaimResult{}, classify(err, "REINDEX_DELIVERY_CLAIM_UPDATE_FAILED")
	}
	return commitClaimResult(ctx, tx, claimedResult(delivery, attempt, false, reclaimed))
}

// Heartbeat 在完整 Fence 仍有效且数据库租约未过期时续租。
func (r *DeliveryRepository) Heartbeat(ctx context.Context, command application.DeliveryHeartbeatCommand) (application.DeliveryMutationResult, error) {
	return r.mutate(ctx, command.Fence, true, func(delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
		// Heartbeat 同一事务以同一个数据库 wall-clock 值更新 Attempt 与 Delivery；
		// 仅该绑定可证明旧 version 是响应丢失，而不是别的 checkpoint 已推进。
		return attempt.HeartbeatAt.Equal(delivery.UpdatedAt)
	}, func(ctx context.Context, tx pgx.Tx, delivery domain.Delivery, attempt domain.DeliveryAttempt, _ time.Time) (application.DeliveryMutationResult, error) {
		attempt, err := scanDeliveryAttempt(tx.QueryRow(ctx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
			UPDATE retrieval.reindex_delivery_attempt SET
			heartbeat_at=database_clock.now,lease_until=database_clock.now + ($2 * interval '1 microsecond')
			FROM database_clock
			WHERE id=$1 AND status='processing' AND database_clock.now < lease_until RETURNING `+deliveryAttemptColumns,
			string(attempt.ID), command.LeaseDuration.Microseconds()))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.DeliveryMutationResult{}, deliveryLeaseExpired()
		}
		if err != nil {
			return application.DeliveryMutationResult{}, classify(err, "REINDEX_DELIVERY_HEARTBEAT_ATTEMPT_FAILED")
		}
		delivery, err = bumpDelivery(ctx, tx, delivery.ID, attempt.HeartbeatAt)
		if err != nil {
			return application.DeliveryMutationResult{}, err
		}
		return appliedMutation(delivery, attempt, false), nil
	})
}

// Checkpoint 按 Source、Ingestion、Index、Regression 顺序原子持久化不可逆绑定。
func (r *DeliveryRepository) Checkpoint(ctx context.Context, command application.DeliveryCheckpointCommand) (application.DeliveryMutationResult, error) {
	return r.mutate(ctx, command.Fence, true, func(delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
		return domain.DeliveryCheckpointMatches(delivery, attempt, command.Checkpoint)
	}, func(ctx context.Context, tx pgx.Tx, delivery domain.Delivery, attempt domain.DeliveryAttempt, _ time.Time) (application.DeliveryMutationResult, error) {
		if domain.DeliveryCheckpointMatches(delivery, attempt, command.Checkpoint) {
			return appliedMutation(delivery, attempt, true), nil
		}
		rebindIngestion := checkpointRebindsCurrentAttempt(delivery, attempt, command.Checkpoint)
		if !checkpointCanAdvance(delivery, command.Checkpoint) && !rebindIngestion {
			return application.DeliveryMutationResult{}, consistency("REINDEX_DELIVERY_CHECKPOINT_ORDER_INVALID", errors.New("checkpoint does not extend the persisted prefix"))
		}
		var err error
		if command.Checkpoint.Stage != domain.DeliveryCheckpointSourceCaptured {
			var bindingOK bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM ingestion.attempt ingestion_attempt
				WHERE ingestion_attempt.id=$1
				  AND ingestion_attempt.workspace_id=$2
				  AND ingestion_attempt.source_version_id=$3
				  AND ingestion_attempt.parse_projection_id=$4
				  AND ingestion_attempt.status='chunked'
				  AND ingestion_attempt.security_status='passed'
			)`, string(command.Checkpoint.IngestionAttemptID), string(delivery.WorkspaceID),
				string(command.Checkpoint.SourceVersionID), string(command.Checkpoint.ParseProjectionID)).Scan(&bindingOK); err != nil {
				return application.DeliveryMutationResult{}, classify(err, "REINDEX_DELIVERY_INGESTION_BINDING_QUERY_FAILED")
			}
			if !bindingOK {
				return application.DeliveryMutationResult{}, consistency("REINDEX_DELIVERY_INGESTION_BINDING_INVALID", errors.New("ingestion attempt does not match the delivery checkpoint"))
			}
			attempt, err = scanDeliveryAttempt(tx.QueryRow(ctx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
				UPDATE retrieval.reindex_delivery_attempt SET ingestion_attempt_id=$2
				FROM database_clock
				WHERE id=$1 AND status='processing' AND database_clock.now < lease_until RETURNING `+deliveryAttemptColumns,
				string(attempt.ID), string(command.Checkpoint.IngestionAttemptID)))
			if errors.Is(err, pgx.ErrNoRows) {
				return application.DeliveryMutationResult{}, deliveryLeaseExpired()
			}
			if err != nil {
				return application.DeliveryMutationResult{}, classify(err, "REINDEX_DELIVERY_CHECKPOINT_ATTEMPT_FAILED")
			}
		}
		if rebindIngestion {
			delivery, err = bumpDeliveryLive(ctx, tx, delivery.ID)
			if err != nil {
				return application.DeliveryMutationResult{}, err
			}
			return appliedMutation(delivery, attempt, false), nil
		}
		delivery, err = persistCheckpoint(ctx, tx, delivery.ID, command.Checkpoint)
		if err != nil {
			return application.DeliveryMutationResult{}, err
		}
		return appliedMutation(delivery, attempt, false), nil
	})
}

func checkpointRebindsCurrentAttempt(delivery domain.Delivery, attempt domain.DeliveryAttempt, checkpoint domain.DeliveryCheckpoint) bool {
	return checkpoint.Stage == domain.DeliveryCheckpointIngested && attempt.IngestionAttemptID == nil &&
		delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID &&
		delivery.ParseProjectionID != nil && *delivery.ParseProjectionID == checkpoint.ParseProjectionID
}

// Fail 在完整 Fence 仍有效时原子归约业务失败，并使 Attempt 先进入匹配终态。
func (r *DeliveryRepository) Fail(ctx context.Context, command application.DeliveryFailureCommand) (application.DeliveryMutationResult, error) {
	return r.mutate(ctx, command.Fence, false, func(delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
		return delivery.Failure != nil && attempt.Failure != nil && *delivery.Failure == command.Failure && *attempt.Failure == command.Failure
	}, func(ctx context.Context, tx pgx.Tx, delivery domain.Delivery, attempt domain.DeliveryAttempt, _ time.Time) (application.DeliveryMutationResult, error) {
		attemptStatus, deliveryStatus := failureStatuses(command.Failure.Class)
		attempt, err := scanDeliveryAttempt(tx.QueryRow(ctx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
			UPDATE retrieval.reindex_delivery_attempt SET
			status=$2,failure_class=$3,error_kind=$4,error_code=$5,error_summary=$6,ended_at=database_clock.now
			FROM database_clock
			WHERE id=$1 AND status='processing' AND database_clock.now < lease_until RETURNING `+deliveryAttemptColumns,
			string(attempt.ID), string(attemptStatus), string(command.Failure.Class), string(command.Failure.ErrorKind), command.Failure.Code, command.Failure.Summary))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.DeliveryMutationResult{}, deliveryLeaseExpired()
		}
		if err != nil {
			return application.DeliveryMutationResult{}, classify(err, "REINDEX_DELIVERY_FAILURE_ATTEMPT_FAILED")
		}
		retryMicros := command.RetryDelay.Microseconds()
		delivery, err = scanDelivery(tx.QueryRow(ctx, `UPDATE retrieval.reindex_delivery SET status=$2,
			next_attempt_at=CASE WHEN $2='retry_wait' THEN $8::timestamptz + ($7 * interval '1 microsecond') ELSE NULL END,
			failure_class=$3,error_kind=$4,error_code=$5,error_summary=$6,
			manual_recovery_required=($2='manual_recovery'),completed_at=CASE WHEN $2 IN ('failed','succeeded') THEN $8 ELSE NULL END,
			version=version+1,updated_at=$8 WHERE id=$1 AND status='processing' RETURNING `+deliveryColumns,
			string(delivery.ID), string(deliveryStatus), string(command.Failure.Class), string(command.Failure.ErrorKind), command.Failure.Code, command.Failure.Summary, retryMicros, *attempt.EndedAt))
		if err != nil {
			return application.DeliveryMutationResult{}, classify(err, "REINDEX_DELIVERY_FAILURE_UPDATE_FAILED")
		}
		return application.DeliveryMutationResult{Disposition: application.DeliveryMutationCommitted, Delivery: delivery, Attempt: attempt}, nil
	})
}

type deliveryMutation func(context.Context, pgx.Tx, domain.Delivery, domain.DeliveryAttempt, time.Time) (application.DeliveryMutationResult, error)
type committedReplayMatcher func(domain.Delivery, domain.DeliveryAttempt) bool

func (r *DeliveryRepository) mutate(ctx context.Context, fence domain.DeliveryFence, processingReplay bool, replayMatches committedReplayMatcher, apply deliveryMutation) (application.DeliveryMutationResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.DeliveryMutationResult{}, classify(err, "REINDEX_DELIVERY_MUTATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	delivery, err := lockDelivery(ctx, tx, fence.DeliveryID)
	if err != nil {
		return application.DeliveryMutationResult{}, err
	}
	if delivery.CurrentAttemptID == nil {
		return commitMutationResult(ctx, tx, application.DeliveryMutationResult{Disposition: application.DeliveryMutationStale})
	}
	attempt, err := currentAttempt(ctx, tx, delivery)
	if err != nil {
		return application.DeliveryMutationResult{}, err
	}
	databaseNow, err := deliveryDatabaseNow(ctx, tx)
	if err != nil {
		return application.DeliveryMutationResult{}, err
	}
	if !fenceMatches(fence, delivery, attempt) {
		if isCommittedDelivery(delivery.Status) && committedFenceIdentityMatches(fence, delivery, attempt) && replayMatches(delivery, attempt) {
			return commitMutationResult(ctx, tx, application.DeliveryMutationResult{Disposition: application.DeliveryMutationCommitted, Delivery: delivery, Attempt: attempt, Replayed: true})
		}
		if processingReplay && delivery.Status == domain.DeliveryStatusProcessing && attempt.Status == domain.DeliveryAttemptProcessing &&
			delivery.Version > fence.DeliveryVersion && committedFenceIdentityMatches(fence, delivery, attempt) && replayMatches(delivery, attempt) {
			if !databaseNow.Before(attempt.LeaseUntil) {
				return application.DeliveryMutationResult{}, deliveryLeaseExpired()
			}
			return commitMutationResult(ctx, tx, appliedMutation(delivery, attempt, true))
		}
		if fence.DeliveryID == delivery.ID && fence.DispatchNo == delivery.DispatchNo && fence.AttemptID != attempt.ID {
			var priorStatus domain.DeliveryAttemptStatus
			queryErr := tx.QueryRow(ctx, `SELECT status FROM retrieval.reindex_delivery_attempt WHERE id=$1 AND delivery_id=$2`, string(fence.AttemptID), string(delivery.ID)).Scan(&priorStatus)
			if queryErr == nil && priorStatus == domain.DeliveryAttemptLeaseLost {
				return application.DeliveryMutationResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_LOST", true, errors.New("delivery fence belongs to a reclaimed attempt"))
			}
			if queryErr != nil && !errors.Is(queryErr, pgx.ErrNoRows) {
				return application.DeliveryMutationResult{}, classify(queryErr, "REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED")
			}
		}
		return commitMutationResult(ctx, tx, application.DeliveryMutationResult{Disposition: application.DeliveryMutationStale})
	}
	if delivery.Status != domain.DeliveryStatusProcessing || attempt.Status != domain.DeliveryAttemptProcessing {
		return commitMutationResult(ctx, tx, application.DeliveryMutationResult{Disposition: application.DeliveryMutationStale})
	}
	if !databaseNow.Before(attempt.LeaseUntil) {
		return application.DeliveryMutationResult{}, deliveryLeaseExpired()
	}
	result, err := apply(ctx, tx, delivery, attempt, databaseNow)
	if err != nil {
		return application.DeliveryMutationResult{}, err
	}
	return commitMutationResult(ctx, tx, result)
}

func lockDelivery(ctx context.Context, tx pgx.Tx, id foundation.ID) (domain.Delivery, error) {
	value, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM retrieval.reindex_delivery WHERE id=$1 FOR UPDATE`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Delivery{}, notFound("REINDEX_DELIVERY_NOT_FOUND", err)
	}
	if err != nil {
		return domain.Delivery{}, classify(err, "REINDEX_DELIVERY_QUERY_FAILED")
	}
	return value, nil
}

func currentAttempt(ctx context.Context, tx pgx.Tx, delivery domain.Delivery) (domain.DeliveryAttempt, error) {
	value, err := scanDeliveryAttempt(tx.QueryRow(ctx, `SELECT `+deliveryAttemptColumns+` FROM retrieval.reindex_delivery_attempt
		WHERE id=$1 AND delivery_id=$2 FOR UPDATE`, string(*delivery.CurrentAttemptID), string(delivery.ID)))
	if err != nil {
		return domain.DeliveryAttempt{}, classify(err, "REINDEX_DELIVERY_ATTEMPT_QUERY_FAILED")
	}
	return value, nil
}

func bumpDelivery(ctx context.Context, tx pgx.Tx, id foundation.ID, at time.Time) (domain.Delivery, error) {
	value, err := scanDelivery(tx.QueryRow(ctx, `UPDATE retrieval.reindex_delivery SET version=version+1,updated_at=$2
		WHERE id=$1 AND status='processing' RETURNING `+deliveryColumns, string(id), at))
	if err != nil {
		return domain.Delivery{}, classify(err, "REINDEX_DELIVERY_FENCE_UPDATE_FAILED")
	}
	return value, nil
}

func bumpDeliveryLive(ctx context.Context, tx pgx.Tx, id foundation.ID) (domain.Delivery, error) {
	value, err := scanDelivery(tx.QueryRow(ctx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
		UPDATE retrieval.reindex_delivery delivery SET version=delivery.version+1,updated_at=database_clock.now
		FROM database_clock
		WHERE delivery.id=$1 AND delivery.status='processing' AND EXISTS (
			SELECT 1 FROM retrieval.reindex_delivery_attempt attempt
			WHERE attempt.id=delivery.current_attempt_id AND attempt.status='processing'
			  AND database_clock.now < attempt.lease_until
		) RETURNING `+deliveryColumns, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Delivery{}, deliveryLeaseExpired()
	}
	if err != nil {
		return domain.Delivery{}, classify(err, "REINDEX_DELIVERY_FENCE_UPDATE_FAILED")
	}
	return value, nil
}

func persistCheckpoint(ctx context.Context, tx pgx.Tx, id foundation.ID, checkpoint domain.DeliveryCheckpoint) (domain.Delivery, error) {
	query := `WITH database_clock AS (SELECT clock_timestamp() AS now)
		UPDATE retrieval.reindex_delivery delivery SET source_version_id=$2,version=delivery.version+1,updated_at=database_clock.now`
	args := []any{string(id), string(checkpoint.SourceVersionID)}
	switch checkpoint.Stage {
	case domain.DeliveryCheckpointIngested:
		query += `,parse_projection_id=$3`
		args = append(args, string(checkpoint.ParseProjectionID))
	case domain.DeliveryCheckpointIndexBuilding:
		query += `,parse_projection_id=$3,index_version_id=$4,excluded_source_count=$5`
		args = append(args, string(checkpoint.ParseProjectionID), string(checkpoint.IndexVersionID), *checkpoint.ExcludedSourceCount)
	case domain.DeliveryCheckpointRegressionPassed:
		query += `,parse_projection_id=$3,index_version_id=$4,excluded_source_count=$5,regression_code=$6,regression_hash=$7,regression_passed_at=database_clock.now`
		args = append(args, string(checkpoint.ParseProjectionID), string(checkpoint.IndexVersionID), *checkpoint.ExcludedSourceCount, checkpoint.RegressionCode, checkpoint.RegressionHash)
	}
	query += ` FROM database_clock WHERE delivery.id=$1 AND delivery.status='processing' AND EXISTS (
		SELECT 1 FROM retrieval.reindex_delivery_attempt attempt
		WHERE attempt.id=delivery.current_attempt_id AND attempt.status='processing'
		  AND database_clock.now < attempt.lease_until
	) RETURNING ` + deliveryColumns
	value, err := scanDelivery(tx.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Delivery{}, deliveryLeaseExpired()
	}
	if err != nil {
		return domain.Delivery{}, classify(err, "REINDEX_DELIVERY_CHECKPOINT_UPDATE_FAILED")
	}
	return value, nil
}

func deliveryLeaseExpired() error {
	return foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_EXPIRED", true, errors.New("delivery database lease has expired"))
}

func deliveryDatabaseNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classify(err, "REINDEX_DELIVERY_CLOCK_QUERY_FAILED")
	}
	return now.UTC(), nil
}

func checkpointCanAdvance(delivery domain.Delivery, checkpoint domain.DeliveryCheckpoint) bool {
	switch checkpoint.Stage {
	case domain.DeliveryCheckpointSourceCaptured:
		return delivery.SourceVersionID == nil
	case domain.DeliveryCheckpointIngested:
		return delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID && delivery.ParseProjectionID == nil
	case domain.DeliveryCheckpointIndexBuilding:
		return delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID &&
			delivery.ParseProjectionID != nil && *delivery.ParseProjectionID == checkpoint.ParseProjectionID && delivery.IndexVersionID == nil
	case domain.DeliveryCheckpointRegressionPassed:
		return delivery.SourceVersionID != nil && *delivery.SourceVersionID == checkpoint.SourceVersionID &&
			delivery.ParseProjectionID != nil && *delivery.ParseProjectionID == checkpoint.ParseProjectionID &&
			delivery.IndexVersionID != nil && *delivery.IndexVersionID == checkpoint.IndexVersionID &&
			delivery.ExcludedSourceCount != nil && checkpoint.ExcludedSourceCount != nil && *delivery.ExcludedSourceCount == *checkpoint.ExcludedSourceCount && delivery.Regression == nil
	default:
		return false
	}
}

func claimedResult(delivery domain.Delivery, attempt domain.DeliveryAttempt, replayed, reclaimed bool) application.DeliveryClaimResult {
	return application.DeliveryClaimResult{Disposition: application.DeliveryClaimed, Delivery: delivery, Attempt: attempt,
		Fence: deliveryFence(delivery, attempt), Replayed: replayed, LeaseReclaimed: reclaimed}
}

func appliedMutation(delivery domain.Delivery, attempt domain.DeliveryAttempt, replayed bool) application.DeliveryMutationResult {
	return application.DeliveryMutationResult{Disposition: application.DeliveryMutationApplied, Delivery: delivery, Attempt: attempt,
		Fence: deliveryFence(delivery, attempt), Replayed: replayed}
}

func deliveryFence(delivery domain.Delivery, attempt domain.DeliveryAttempt) domain.DeliveryFence {
	return domain.DeliveryFence{DeliveryID: delivery.ID, DispatchNo: delivery.DispatchNo, AttemptID: attempt.ID,
		AttemptNo: attempt.AttemptNo, Owner: attempt.LeaseOwner, DeliveryVersion: delivery.Version}
}

func fenceMatches(fence domain.DeliveryFence, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return committedFenceIdentityMatches(fence, delivery, attempt) && delivery.Version == fence.DeliveryVersion &&
		delivery.Status == domain.DeliveryStatusProcessing && attempt.Status == domain.DeliveryAttemptProcessing
}

func committedFenceIdentityMatches(fence domain.DeliveryFence, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return delivery.ID == fence.DeliveryID && delivery.DispatchNo == fence.DispatchNo && delivery.AttemptNo == fence.AttemptNo &&
		delivery.CurrentAttemptID != nil && *delivery.CurrentAttemptID == fence.AttemptID && attempt.ID == fence.AttemptID &&
		attempt.DeliveryID == fence.DeliveryID && attempt.DispatchNo == fence.DispatchNo && attempt.AttemptNo == fence.AttemptNo && attempt.LeaseOwner == fence.Owner
}

func sameClaimTransport(attempt domain.DeliveryAttempt, command application.DeliveryClaimCommand) bool {
	return attempt.DispatchNo == command.DispatchNo && attempt.RiverJobID == command.RiverJobID && attempt.RiverAttempt == command.RiverAttempt &&
		attempt.DeliveryKey == command.DeliveryKey && attempt.LeaseOwner == command.LeaseOwner
}

func isCommittedDelivery(status domain.DeliveryStatus) bool {
	return status == domain.DeliveryStatusRetryWait || status == domain.DeliveryStatusSucceeded ||
		status == domain.DeliveryStatusFailed || status == domain.DeliveryStatusManualRecovery
}

func failureStatuses(class domain.DeliveryFailureClass) (domain.DeliveryAttemptStatus, domain.DeliveryStatus) {
	switch class {
	case domain.DeliveryFailureRetryable:
		return domain.DeliveryAttemptRetryWait, domain.DeliveryStatusRetryWait
	case domain.DeliveryFailureNonRetryable:
		return domain.DeliveryAttemptFailed, domain.DeliveryStatusFailed
	default:
		return domain.DeliveryAttemptManualRecovery, domain.DeliveryStatusManualRecovery
	}
}

func commitClaimResult(ctx context.Context, tx pgx.Tx, result application.DeliveryClaimResult) (application.DeliveryClaimResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return application.DeliveryClaimResult{}, classify(err, "REINDEX_DELIVERY_CLAIM_COMMIT_FAILED")
	}
	return result, nil
}

func commitMutationResult(ctx context.Context, tx pgx.Tx, result application.DeliveryMutationResult) (application.DeliveryMutationResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return application.DeliveryMutationResult{}, classify(err, "REINDEX_DELIVERY_MUTATION_COMMIT_FAILED")
	}
	return result, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
