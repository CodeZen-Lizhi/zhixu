package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMRepository 使用共享物理连接池持久化 Organizing 事实。
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMRepository 复用平台 GORM 与事务边界，不创建连接池或改变 Schema。
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, unavailable(errors.New("organizing PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(err)
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(err)
	}
	return &GORMRepository{database: database, unitOfWork: uow}, nil
}

func (repository *GORMRepository) validateReady(ctx context.Context) error {
	if repository == nil || repository.database == nil || isNilInterface(repository.unitOfWork) {
		return unavailable(errors.New("organizing repository is unavailable"))
	}
	if ctx == nil {
		return invalid(errors.New("organizing context is nil"))
	}
	return nil
}

func (repository *GORMRepository) within(ctx context.Context, options foundation.TransactionOptions,
	work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	if err := repository.validateReady(ctx); err != nil {
		return err
	}
	err := repository.unitOfWork.Within(ctx, options, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return unavailable(err)
		}
		return work(ctx, scope, tx.WithContext(ctx))
	})
	return classifyGORM(err)
}

func (repository *GORMRepository) command(ctx context.Context, binding organizingapp.CommandBinding,
	options foundation.TransactionOptions, work func(context.Context, foundation.TransactionScope, *gorm.DB) error,
) error {
	return repository.within(ctx, options, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(? || chr(31) || ?,0))`,
			string(binding.WorkspaceID), binding.IdempotencyKey).Error; err != nil {
			return classifyGORM(err)
		}
		return work(ctx, scope, tx)
	})
}

func gormNoRows(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, sql.ErrNoRows)
}

func classifyGORM(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeRepositoryUnavailable, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return unavailable(err)
	}
	switch platformpostgres.SQLState(err) {
	case "23505":
		if platformpostgres.ConstraintName(err) == "command_receipt_pkey" {
			return idempotencyConflict(err)
		}
		return inconsistent(err)
	case "23502", "23503", "23514", "22P02", "55000":
		return inconsistent(err)
	case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01", "57014":
		return foundation.NewError(foundation.ErrorRetryableFailure, organizingapp.ErrorCodeRepositoryUnavailable, true, err)
	}
	return unavailable(err)
}

var (
	_ organizingapp.DraftRepository            = (*GORMRepository)(nil)
	_ organizingapp.TemplateRepository         = (*GORMRepository)(nil)
	_ organizingapp.StartRepository            = (*GORMRepository)(nil)
	_ organizingapp.ScopedTerminalResultWriter = (*GORMRepository)(nil)
)
