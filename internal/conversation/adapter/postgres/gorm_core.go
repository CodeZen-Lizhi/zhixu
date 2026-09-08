package postgres

import (
	"context"
	"errors"

	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMRepository 使用共享 Pool 持久化 Conversation 与有界 Turn/Answer 投影。
type GORMRepository struct {
	db     *gorm.DB
	uow    foundation.UnitOfWork
	events eventsapplication.ScopedAppender
}

// NewGORMRepository 从同一物理 Pool 构造查询根与跨 owner 事务边界。
func NewGORMRepository(pool *platformpostgres.Pool, events eventsapplication.ScopedAppender) (*GORMRepository, error) {
	if isNilInterface(events) {
		return nil, dependency(ErrorCodeDatabaseUnavailable, errors.New("conversation event appender is nil"))
	}
	db, uow, err := conversationGORMDependencies(pool, ErrorCodeDatabaseUnavailable)
	if err != nil {
		return nil, err
	}
	return &GORMRepository{db: db, uow: uow, events: events}, nil
}

func conversationGORMDependencies(pool *platformpostgres.Pool, code string) (*gorm.DB, foundation.UnitOfWork, error) {
	if pool == nil {
		return nil, nil, dependency(code, errors.New("conversation database pool is nil"))
	}
	db, err := pool.GORM()
	if err != nil {
		return nil, nil, dependency(code, err)
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, nil, dependency(code, err)
	}
	return db, uow, nil
}

func withinConversationTransaction(ctx context.Context, uow foundation.UnitOfWork, options foundation.TransactionOptions, work func(context.Context, *gorm.DB, foundation.TransactionScope) error) error {
	return uow.Within(ctx, options, func(ctx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		return work(ctx, tx.WithContext(ctx), scope)
	})
}

// gormScanRow 将 GORM 查询构造错误与行读取错误交给现有严格领域解码器。
func gormScanRow(statement *gorm.DB) scanner {
	if statement == nil {
		return conversationScanError{errors.New("conversation GORM statement is nil")}
	}
	if statement.Error != nil {
		return conversationScanError{statement.Error}
	}
	row := statement.Row()
	if row == nil {
		if statement.Error != nil {
			return conversationScanError{statement.Error}
		}
		return conversationScanError{errors.New("conversation GORM row is nil")}
	}
	return row
}

type conversationScanError struct{ err error }

func (row conversationScanError) Scan(...any) error { return row.err }
