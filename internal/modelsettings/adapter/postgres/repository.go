// Package postgres persists model settings revisions and rollout ownership in PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the minimal pgx pool contract required by the repository.
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Option configures transaction-bound side facts for model settings writes.
type Option func(*Repository) error

// WithLocalModelLifecycle binds the transaction-aware managed Ollama facts.
// The repository never owns or starts a runtime process; it only uses this
// boundary while it already holds the model-settings transaction.
func WithLocalModelLifecycle(lifecycle localmodelruntime.TxLifecycle) Option {
	return func(repository *Repository) error {
		if nilInterface(lifecycle) {
			return invalid(errors.New("local model lifecycle is nil"))
		}
		if !nilInterface(repository.localLifecycle) {
			return invalid(errors.New("local model lifecycle is duplicated"))
		}
		repository.localLifecycle = lifecycle
		return nil
	}
}

// WithSecretSealer configures the process-owned AEAD boundary used by revision writes and reads.
func WithSecretSealer(sealer application.SecretSealer) Option {
	return func(repository *Repository) error {
		if nilInterface(sealer) {
			return invalid(errors.New("model settings secret sealer is nil"))
		}
		if !nilInterface(repository.sealer) {
			return invalid(errors.New("model settings secret sealer is duplicated"))
		}
		repository.sealer = sealer
		return nil
	}
}

// WithSettingsAuditAppender configures the transaction-scoped redacted audit boundary.
func WithSettingsAuditAppender(appender application.SettingsAuditAppender) Option {
	return func(repository *Repository) error {
		if nilInterface(appender) {
			return invalid(errors.New("model settings audit appender is nil"))
		}
		if !nilInterface(repository.audit) {
			return invalid(errors.New("model settings audit appender is duplicated"))
		}
		repository.audit = appender
		return nil
	}
}

// WithAuditAppender adapts the shared append-only Audit store for model settings.
func WithAuditAppender(appender auditapplication.Appender) Option {
	settingsAppender, err := NewSettingsAuditAppender(appender)
	return func(repository *Repository) error {
		if err != nil {
			return err
		}
		return WithSettingsAuditAppender(settingsAppender)(repository)
	}
}

// Repository is the PostgreSQL atomic boundary for model settings.
type Repository struct {
	db             DB
	sealer         application.SecretSealer
	audit          application.SettingsAuditAppender
	localLifecycle localmodelruntime.TxLifecycle
}

// String returns a dependency-only summary without expanding database or secret configuration.
func (repository Repository) String() string {
	return fmt.Sprintf("Repository{database_configured:%t sealer_configured:%t audit_configured:%t}",
		!nilInterface(repository.db), !nilInterface(repository.sealer), !nilInterface(repository.audit))
}

// GoString uses the same safe dependency-only summary.
func (repository Repository) GoString() string { return repository.String() }

// NewRepository creates a repository without opening connections or migrating schemas.
func NewRepository(db DB, options ...Option) (*Repository, error) {
	if nilInterface(db) {
		return nil, unavailable(errors.New("model settings database is nil"))
	}
	repository := &Repository{db: db}
	for _, option := range options {
		if option == nil {
			return nil, invalid(errors.New("model settings repository option is nil"))
		}
		if err := option(repository); err != nil {
			return nil, err
		}
	}
	if nilInterface(repository.sealer) || nilInterface(repository.audit) {
		return nil, unavailable(errors.New("model settings repository dependencies are unavailable"))
	}
	return repository, nil
}

var (
	_ application.RevisionStore            = (*Repository)(nil)
	_ application.RolloutStore             = (*Repository)(nil)
	_ application.RuntimeStore             = (*Repository)(nil)
	_ application.RuntimeAvailabilityStore = (*Repository)(nil)
	_ application.ActivationStore          = (*Repository)(nil)
	_ application.ParticipantStore         = (*Repository)(nil)
)

const stateColumns = `desired_revision,active_revision,rollout_id::text,target_revision,
previous_active_revision,phase,lease_expires_at,last_error_code,version`

type stateRecord struct {
	desiredRevision int64
	activeRevision  int64
	rolloutID       sql.NullString
	targetRevision  sql.NullInt64
	previousActive  sql.NullInt64
	phase           string
	leaseExpiresAt  sql.NullTime
	lastErrorCode   sql.NullString
	version         int64
}

func loadState(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, suffix string) (stateRecord, error) {
	return scanState(queryer.QueryRow(ctx, `SELECT `+stateColumns+`
FROM ops.model_settings_state WHERE singleton=true `+suffix))
}

func scanState(row interface{ Scan(...any) error }) (stateRecord, error) {
	var state stateRecord
	if err := row.Scan(
		&state.desiredRevision, &state.activeRevision, &state.rolloutID, &state.targetRevision,
		&state.previousActive, &state.phase, &state.leaseExpiresAt, &state.lastErrorCode, &state.version,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return stateRecord{}, corrupt(errors.New("model settings singleton state is missing"))
		}
		return stateRecord{}, classify(err)
	}
	if state.desiredRevision < 0 || state.activeRevision < 0 || state.version <= 0 {
		return stateRecord{}, corrupt(errors.New("model settings singleton state is invalid"))
	}
	return state, nil
}

func (state stateRecord) rollout() (domain.RolloutState, error) {
	phase := domain.RolloutPhase(state.phase)
	switch phase {
	case domain.RolloutPhaseIdle:
		if state.rolloutID.Valid || state.targetRevision.Valid || state.previousActive.Valid || state.leaseExpiresAt.Valid || state.lastErrorCode.Valid {
			return domain.RolloutState{}, corrupt(errors.New("idle model settings rollout is invalid"))
		}
		return domain.RolloutState{Phase: phase, Version: state.version}, nil
	case domain.RolloutPhasePreparing, domain.RolloutPhaseArming, domain.RolloutPhaseActivating,
		domain.RolloutPhaseValidating, domain.RolloutPhaseDraining, domain.RolloutPhaseApplying,
		domain.RolloutPhaseVerifying, domain.RolloutPhaseFailed:
	default:
		return domain.RolloutState{}, corrupt(errors.New("model settings rollout phase is invalid"))
	}
	if !state.rolloutID.Valid || !state.targetRevision.Valid || !state.previousActive.Valid {
		return domain.RolloutState{}, corrupt(errors.New("model settings rollout binding is missing"))
	}
	id, err := foundation.ParseID(state.rolloutID.String)
	if err != nil {
		return domain.RolloutState{}, corrupt(errors.New("model settings rollout identifier is invalid"))
	}
	result := domain.RolloutState{
		ID: id, TargetRevision: state.targetRevision.Int64, PreviousActiveRevision: state.previousActive.Int64,
		Phase: phase, LastErrorCode: state.lastErrorCode.String, Version: state.version,
	}
	if state.leaseExpiresAt.Valid {
		result.LeaseExpiresAt = state.leaseExpiresAt.Time.UTC()
	}
	if phase == domain.RolloutPhaseFailed {
		if state.leaseExpiresAt.Valid || !state.lastErrorCode.Valid || state.activeRevision != state.previousActive.Int64 {
			return domain.RolloutState{}, corrupt(errors.New("failed model settings rollout is invalid"))
		}
	} else if !state.leaseExpiresAt.Valid || state.lastErrorCode.Valid {
		return domain.RolloutState{}, corrupt(errors.New("active model settings rollout lease is invalid"))
	}
	if (phase == domain.RolloutPhasePreparing || phase == domain.RolloutPhaseArming) && state.activeRevision != state.previousActive.Int64 {
		return domain.RolloutState{}, corrupt(errors.New("pre-commit model settings activation is invalid"))
	}
	if phase == domain.RolloutPhaseActivating && state.activeRevision != state.targetRevision.Int64 {
		return domain.RolloutState{}, corrupt(errors.New("post-commit model settings activation is invalid"))
	}
	return result, nil
}

const runtimeColumns = `role,instance_id::text,applied_revision,rollout_id::text,phase,applied_at,heartbeat_at`

func scanRuntime(row interface{ Scan(...any) error }) (domain.RuntimeRecord, error) {
	var role, instance, phase string
	var appliedRevision int64
	var rollout sql.NullString
	var appliedAt, heartbeatAt time.Time
	if err := row.Scan(&role, &instance, &appliedRevision, &rollout, &phase, &appliedAt, &heartbeatAt); err != nil {
		return domain.RuntimeRecord{}, err
	}
	instanceID, err := foundation.ParseID(instance)
	if err != nil {
		return domain.RuntimeRecord{}, corrupt(errors.New("model settings runtime instance is invalid"))
	}
	result := domain.RuntimeRecord{
		Role: domain.RuntimeRole(role), InstanceID: instanceID, AppliedRevision: appliedRevision,
		Phase: domain.RuntimePhase(phase), AppliedAt: appliedAt.UTC(), HeartbeatAt: heartbeatAt.UTC(),
	}
	if rollout.Valid {
		rolloutID, parseErr := foundation.ParseID(rollout.String)
		if parseErr != nil {
			return domain.RuntimeRecord{}, corrupt(errors.New("model settings runtime rollout is invalid"))
		}
		result.RolloutID = &rolloutID
	}
	if !domain.ValidRuntimeRole(result.Role) || !domain.ValidRuntimePhase(result.Phase) || result.AppliedRevision < 0 || result.HeartbeatAt.Before(result.AppliedAt) {
		return domain.RuntimeRecord{}, corrupt(errors.New("model settings runtime record is invalid"))
	}
	return result, nil
}

const participantColumns = `rollout_id::text,role,instance_id::text,target_revision,phase,
heartbeat_at,last_error_code,last_error_retryable,version,prepared_at,activated_at,retired_at`

func scanParticipant(row interface{ Scan(...any) error }) (domain.ParticipantRecord, error) {
	var rolloutID, role, instanceID, phase string
	var targetRevision, version int64
	var heartbeatAt time.Time
	var lastErrorCode sql.NullString
	var errorRetryable bool
	var preparedAt, activatedAt, retiredAt sql.NullTime
	if err := row.Scan(
		&rolloutID, &role, &instanceID, &targetRevision, &phase, &heartbeatAt,
		&lastErrorCode, &errorRetryable, &version, &preparedAt, &activatedAt, &retiredAt,
	); err != nil {
		return domain.ParticipantRecord{}, err
	}
	parsedRolloutID, err := foundation.ParseID(rolloutID)
	if err != nil {
		return domain.ParticipantRecord{}, corrupt(errors.New("model settings participant rollout is invalid"))
	}
	parsedInstanceID, err := foundation.ParseID(instanceID)
	if err != nil {
		return domain.ParticipantRecord{}, corrupt(errors.New("model settings participant instance is invalid"))
	}
	record := domain.ParticipantRecord{
		RolloutID: parsedRolloutID, Role: domain.RuntimeRole(role), InstanceID: parsedInstanceID,
		TargetRevision: targetRevision, Phase: domain.ParticipantPhase(phase), HeartbeatAt: heartbeatAt.UTC(),
		Version: version, LastErrorCode: lastErrorCode.String, ErrorRetryable: errorRetryable,
	}
	if preparedAt.Valid {
		record.PreparedAt = preparedAt.Time.UTC()
	}
	if activatedAt.Valid {
		record.ActivatedAt = activatedAt.Time.UTC()
	}
	if retiredAt.Valid {
		record.RetiredAt = retiredAt.Time.UTC()
	}
	if !domain.ValidRuntimeRole(record.Role) || !domain.ValidParticipantPhase(record.Phase) || record.TargetRevision < 0 || record.Version <= 0 {
		return domain.ParticipantRecord{}, corrupt(errors.New("model settings participant record is invalid"))
	}
	return record, nil
}

func databaseNow(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (time.Time, error) {
	var now time.Time
	if err := queryer.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, classify(err)
	}
	return now.UTC(), nil
}

func commit(tx pgx.Tx, ctx context.Context) error {
	if err := tx.Commit(ctx); err != nil {
		return classify(err)
	}
	return nil
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

func classify(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeUnavailable, true, err)
		case "23502", "23503", "23505", "23514", "55000":
			return corrupt(err)
		}
	}
	return unavailable(err)
}

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeInvalid, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, cause)
}

func corrupt(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, cause)
}

func revisionConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRevisionConflict, false, cause)
}

func rolloutInProgress(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRolloutInProgress, false, cause)
}

func rolloutConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRolloutConflict, false, cause)
}

func leaseExpired(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRolloutLeaseExpired, false, cause)
}

func runtimeConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRuntimeConflict, false, cause)
}

func runtimeNotPrepared(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRuntimeNotPrepared, false, cause)
}

func activationConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeActivationConflict, false, cause)
}

func activationLeaseExpired(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeActivationLeaseExpired, false, cause)
}

func participantConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeParticipantConflict, false, cause)
}

func runtimeOwnershipLost(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRuntimeOwnershipLost, false, cause)
}

func runtimeNotReady(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeRuntimeNotReady, true, cause)
}

func enqueuePaused(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeEnqueuePaused, true, cause)
}
