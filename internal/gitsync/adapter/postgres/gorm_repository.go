package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

// GORMRepository owns Git Sync persistence through the shared platform unit of work.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	sealer     application.CredentialSealer
}

// NewGORMRepository constructs a repository from the shared GORM root and its
// matching Unit of Work.  It does not open connections or inspect the schema.
func NewGORMRepository(database *gorm.DB, unitOfWork foundation.UnitOfWork, sealer application.CredentialSealer) (*GORMRepository, error) {
	if !validGitsyncGORMDatabase(database) {
		return nil, unavailable("Git sync GORM database is unavailable")
	}
	if nilInterface(unitOfWork) {
		return nil, unavailable("Git sync GORM unit of work is unavailable")
	}
	if nilInterface(sealer) {
		return nil, unavailable("Git sync credential sealer is unavailable")
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork, sealer: sealer}, nil
}

func (repository GORMRepository) String() string {
	return fmt.Sprintf("GORMRepository{database_configured:%t unit_of_work_configured:%t sealer_configured:%t}",
		validGitsyncGORMDatabase(repository.database), !nilInterface(repository.unitOfWork), !nilInterface(repository.sealer))
}

func (repository GORMRepository) GoString() string { return repository.String() }

var (
	_ application.ConfigStore             = (*GORMRepository)(nil)
	_ application.RunStore                = (*GORMRepository)(nil)
	_ application.AutoSyncCandidateSource = (*GORMRepository)(nil)
	_ application.FollowupStore           = (*GORMRepository)(nil)
	_ application.OutboxStore             = (*GORMRepository)(nil)
)

type gormScanner interface {
	Scan(...any) error
}

var errGORMDatabaseFailure = errors.New("Git sync PostgreSQL operation failed")

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || ctx == nil || !validGitsyncGORMDatabase(repository.database) ||
		nilInterface(repository.unitOfWork) || nilInterface(repository.sealer) {
		return unavailable("Git sync GORM repository is unavailable")
	}
	return nil
}

func (repository *GORMRepository) within(ctx context.Context, work func(context.Context, *gorm.DB) error) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(callbackCtx, database.WithContext(callbackCtx))
	})
	return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
}

func gormRawRow(ctx context.Context, database *gorm.DB, query string, args ...any) (gormScanner, error) {
	if database == nil {
		return nil, errors.New("Git sync GORM database is nil")
	}
	statement := database.WithContext(ctx).Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("Git sync GORM query returned a nil row")
	}
	return row, nil
}

func gormRawRows(ctx context.Context, database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if database == nil {
		return nil, errors.New("Git sync GORM database is nil")
	}
	statement := database.WithContext(ctx).Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("Git sync GORM query returned nil rows")
	}
	return rows, nil
}

func gormExec(ctx context.Context, database *gorm.DB, query string, args ...any) (int64, error) {
	if database == nil {
		return 0, errors.New("Git sync GORM database is nil")
	}
	statement := database.WithContext(ctx).Exec(query, args...)
	if statement.Error != nil {
		return 0, statement.Error
	}
	return statement.RowsAffected, nil
}

func gormNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func classifyGORM(ctx context.Context, err error, code string) error {
	if err == nil {
		return nil
	}
	var existing *foundation.Error
	if errors.As(err, &existing) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		cause := ctx.Err()
		if contextCause := context.Cause(ctx); contextCause != nil && !errors.Is(contextCause, cause) {
			cause = errors.Join(cause, contextCause)
		}
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, cause)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, errGORMDatabaseFailure)
	}
	if sqlState := platformpostgres.SQLState(err); sqlState != "" {
		switch sqlState {
		case "40001", "40P01", "55P03", "57014", "53300":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, errGORMDatabaseFailure)
		case "23503":
			return foundation.NewError(foundation.ErrorNotFound, domain.ErrorCodeRunNotFound, false, errGORMDatabaseFailure)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeRunActive, false, errGORMDatabaseFailure)
		case "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, errGORMDatabaseFailure)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, errGORMDatabaseFailure)
}

func validGitsyncGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func gormLockWorkspace(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, purpose string) error {
	key := string(workspaceID) + ":" + purpose
	if _, err := gormExec(ctx, database, `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, key); err != nil {
		return classifyGORM(ctx, err, domain.ErrorCodeUnavailable)
	}
	return nil
}
