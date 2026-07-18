package postgres

import (
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

const deliveryColumns = `id::text,consumer_name,outbox_event_id::text,workspace_id::text,writeback_execution_id::text,
	status,dispatch_no,attempt_no,current_attempt_id::text,version,next_attempt_at,source_version_id::text,
	parse_projection_id::text,index_version_id::text,activation_id::text,excluded_source_count,
	regression_code,regression_hash,regression_passed_at,failure_class,error_kind,error_code,error_summary,
	manual_recovery_required,created_at,updated_at,completed_at`

const deliveryAttemptColumns = `id::text,delivery_id::text,attempt_no,dispatch_no,river_job_id,river_attempt,
	delivery_key,lease_owner,lease_until,status,ingestion_attempt_id::text,failure_class,error_kind,error_code,
	error_summary,started_at,heartbeat_at,ended_at`

func scanDelivery(row pgx.Row) (domain.Delivery, error) {
	var value domain.Delivery
	var currentAttemptID, sourceVersionID, projectionID, indexID, activationID *string
	var regressionCode, regressionHash *string
	var regressionPassedAt *time.Time
	var failureClass, errorKind, errorCode, errorSummary *string
	err := row.Scan(
		&value.ID, &value.ConsumerName, &value.OutboxEventID, &value.WorkspaceID, &value.WritebackExecutionID,
		&value.Status, &value.DispatchNo, &value.AttemptNo, &currentAttemptID, &value.Version, &value.NextAttemptAt,
		&sourceVersionID, &projectionID, &indexID, &activationID, &value.ExcludedSourceCount,
		&regressionCode, &regressionHash, &regressionPassedAt, &failureClass, &errorKind, &errorCode, &errorSummary,
		&value.ManualRecoveryRequired, &value.CreatedAt, &value.UpdatedAt, &value.CompletedAt,
	)
	if err != nil {
		return value, err
	}
	value.CurrentAttemptID = scannedOptionalID(currentAttemptID)
	value.SourceVersionID = scannedOptionalID(sourceVersionID)
	value.ParseProjectionID = scannedOptionalID(projectionID)
	value.IndexVersionID = scannedOptionalID(indexID)
	value.ActivationID = scannedOptionalID(activationID)
	if regressionCode != nil {
		value.Regression = &domain.DeliveryRegression{Code: *regressionCode, Hash: *regressionHash, PassedAt: *regressionPassedAt}
	}
	value.Failure = scannedDeliveryFailure(failureClass, errorKind, errorCode, errorSummary)
	return value, nil
}

func scanDeliveryAttempt(row pgx.Row) (domain.DeliveryAttempt, error) {
	var value domain.DeliveryAttempt
	var ingestionAttemptID *string
	var failureClass, errorKind, errorCode, errorSummary *string
	err := row.Scan(
		&value.ID, &value.DeliveryID, &value.AttemptNo, &value.DispatchNo, &value.RiverJobID, &value.RiverAttempt,
		&value.DeliveryKey, &value.LeaseOwner, &value.LeaseUntil, &value.Status, &ingestionAttemptID,
		&failureClass, &errorKind, &errorCode, &errorSummary, &value.StartedAt, &value.HeartbeatAt, &value.EndedAt,
	)
	if err != nil {
		return value, err
	}
	value.IngestionAttemptID = scannedOptionalID(ingestionAttemptID)
	value.Failure = scannedDeliveryFailure(failureClass, errorKind, errorCode, errorSummary)
	return value, nil
}

func scannedOptionalID(value *string) *foundation.ID {
	if value == nil {
		return nil
	}
	id := foundation.ID(*value)
	return &id
}

func scannedDeliveryFailure(class, kind, code, summary *string) *domain.DeliveryFailure {
	if class == nil {
		return nil
	}
	return &domain.DeliveryFailure{
		Class: domain.DeliveryFailureClass(*class), ErrorKind: foundation.ErrorKind(*kind), Code: *code, Summary: *summary,
	}
}
