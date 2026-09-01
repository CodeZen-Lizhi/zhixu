package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	localmodelruntime "github.com/CodeZen-Lizhi/zhixu/internal/localmodelruntime"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// GORMOption configures the staged GORM repository without changing the
// legacy pgx construction path.
type GORMOption func(*GORMRepository) error

// WithGORMSecretSealer configures the revision-bound credential envelope.
func WithGORMSecretSealer(sealer application.SecretSealer) GORMOption {
	return func(repository *GORMRepository) error {
		if nilInterface(sealer) {
			return invalid(errors.New("model settings GORM secret sealer is nil"))
		}
		if !nilInterface(repository.sealer) {
			return invalid(errors.New("model settings GORM secret sealer is duplicated"))
		}
		repository.sealer = sealer
		return nil
	}
}

// WithGORMScopedSettingsAuditAppender configures the caller-owned scoped
// append boundary used by SaveDesired.
func WithGORMScopedSettingsAuditAppender(appender application.ScopedSettingsAuditAppender) GORMOption {
	return func(repository *GORMRepository) error {
		if nilInterface(appender) {
			return invalid(errors.New("model settings GORM audit appender is nil"))
		}
		if !nilInterface(repository.audit) {
			return invalid(errors.New("model settings GORM audit appender is duplicated"))
		}
		repository.audit = appender
		return nil
	}
}

// WithGORMScopedLocalModelLifecycle configures the optional managed Ollama
// preparation boundary. It must originate from the same platform Pool.
func WithGORMScopedLocalModelLifecycle(lifecycle localmodelruntime.ScopedTxLifecycle) GORMOption {
	return func(repository *GORMRepository) error {
		if nilInterface(lifecycle) {
			return invalid(errors.New("model settings GORM local lifecycle is nil"))
		}
		if !nilInterface(repository.localLifecycle) {
			return invalid(errors.New("model settings GORM local lifecycle is duplicated"))
		}
		repository.localLifecycle = lifecycle
		return nil
	}
}

// GORMRepository is the staged database/sql-backed implementation. Production
// composition remains on Repository until the real PostgreSQL gate passes.
type GORMRepository struct {
	database       *gorm.DB
	unitOfWork     foundation.UnitOfWork
	sealer         application.SecretSealer
	audit          application.ScopedSettingsAuditAppender
	localLifecycle localmodelruntime.ScopedTxLifecycle
}

// String returns a dependency-only summary without exposing database or
// credential configuration.
func (repository GORMRepository) String() string {
	return fmt.Sprintf("GORMRepository{database_configured:%t sealer_configured:%t audit_configured:%t local_lifecycle_configured:%t}",
		validGORMDatabase(repository.database), !nilInterface(repository.sealer),
		!nilInterface(repository.audit), !nilInterface(repository.localLifecycle))
}

// GoString uses the same safe dependency-only summary.
func (repository GORMRepository) GoString() string { return repository.String() }

// NewGORMRepository builds the staged repository from one shared platform
// Pool. It opens no connection and performs no schema mutation.
func NewGORMRepository(pool *platformpostgres.Pool, options ...GORMOption) (*GORMRepository, error) {
	if pool == nil {
		return nil, unavailable(errors.New("model settings GORM pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(fmt.Errorf("model settings GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(fmt.Errorf("model settings GORM unit of work is unavailable: %w", err))
	}
	if !validGORMDatabase(database) || nilInterface(unitOfWork) {
		return nil, unavailable(errors.New("model settings GORM dependencies are unavailable"))
	}
	repository := &GORMRepository{database: database, unitOfWork: unitOfWork}
	for _, option := range options {
		if option == nil {
			return nil, invalid(errors.New("model settings GORM repository option is nil"))
		}
		if err := option(repository); err != nil {
			return nil, err
		}
	}
	if nilInterface(repository.sealer) || nilInterface(repository.audit) {
		return nil, unavailable(errors.New("model settings GORM repository dependencies are unavailable"))
	}
	return repository, nil
}

var (
	_ application.RevisionStore            = (*GORMRepository)(nil)
	_ application.RolloutStore             = (*GORMRepository)(nil)
	_ application.RuntimeStore             = (*GORMRepository)(nil)
	_ application.RuntimeAvailabilityStore = (*GORMRepository)(nil)
	_ application.ActivationStore          = (*GORMRepository)(nil)
	_ application.ParticipantStore         = (*GORMRepository)(nil)
)

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMDatabase(repository.database) || nilInterface(repository.unitOfWork) ||
		nilInterface(repository.sealer) || nilInterface(repository.audit) {
		return unavailable(errors.New("model settings GORM repository is unavailable"))
	}
	if ctx == nil {
		return invalid(errors.New("model settings context is nil"))
	}
	return nil
}

func (repository *GORMRepository) within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil {
		return invalid(errors.New("model settings GORM transaction callback is nil"))
	}
	err := repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return unavailable(fmt.Errorf("model settings GORM transaction is unavailable: %w", unwrapErr))
		}
		return work(callbackCtx, scope, database)
	})
	return classifyGORM(ctx, err)
}

func validGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func gormRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validGORMDatabase(database) {
		return nil, errors.New("model settings GORM row query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("model settings GORM row query returned no row handle")
	}
	return row, nil
}

func gormRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validGORMDatabase(database) {
		return nil, errors.New("model settings GORM rows query is unavailable")
	}
	statement := database.WithContext(ctx).Raw(query, arguments...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("model settings GORM rows query returned no rows handle")
	}
	return rows, nil
}

func gormNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormLoadState(ctx context.Context, database *gorm.DB, suffix string) (stateRecord, error) {
	switch suffix {
	case "", "FOR SHARE", "FOR UPDATE":
	default:
		return stateRecord{}, invalid(errors.New("model settings state lock is invalid"))
	}
	row, err := gormRawRow(ctx, database, `SELECT `+stateColumns+`
FROM ops.model_settings_state WHERE singleton=true `+suffix)
	if err != nil {
		return stateRecord{}, classifyGORM(ctx, err)
	}
	state, err := gormScanState(row)
	if gormNoRows(err) {
		return stateRecord{}, corrupt(errors.New("model settings singleton state is missing"))
	}
	if err != nil {
		return stateRecord{}, classifyGORM(ctx, err)
	}
	return state, nil
}

func gormScanState(row interface{ Scan(...any) error }) (stateRecord, error) {
	var state stateRecord
	if err := row.Scan(
		&state.desiredRevision, &state.activeRevision, &state.rolloutID, &state.targetRevision,
		&state.previousActive, &state.phase, &state.leaseExpiresAt, &state.lastErrorCode, &state.version,
	); err != nil {
		return stateRecord{}, err
	}
	if state.desiredRevision < 0 || state.activeRevision < 0 || state.version <= 0 {
		return stateRecord{}, corrupt(errors.New("model settings singleton state is invalid"))
	}
	return state, nil
}

func gormDatabaseNow(ctx context.Context, database *gorm.DB) (time.Time, error) {
	row, err := gormRawRow(ctx, database, `SELECT clock_timestamp()`)
	if err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, classifyGORM(ctx, err)
	}
	return now.UTC(), nil
}

func classifyGORM(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if cause := gormContextCause(ctx, err); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, domain.ErrorCodeUnavailable, false, cause)
		}
		return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeUnavailable, true, cause)
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
	if errors.Is(err, sql.ErrTxDone) {
		return unavailable(err)
	}
	return unavailable(err)
}

func gormContextCause(ctx context.Context, err error) error {
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
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}
