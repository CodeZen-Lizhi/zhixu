package postgres

import (
	"context"
	"errors"

	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMScanCancellationGuard exposes the Health cancellation safety check on
// Workflow's scoped transaction port. It never commits or rolls back scope.
type GORMScanCancellationGuard struct {
	database *gorm.DB
	legacy   *ScanCancellationGuard
}

func NewGORMScanCancellationGuard(pool *platformpostgres.Pool, appender eventsapplication.ScopedAppender) (*GORMScanCancellationGuard, error) {
	if pool == nil || isNilHealthDependency(appender) {
		return nil, healthGORMUnavailable(errors.New("health GORM cancellation dependencies are unavailable"))
	}
	database, err := pool.GORM()
	if err != nil || !validHealthGORMDatabase(database) {
		return nil, healthGORMUnavailable(errors.New("health GORM cancellation database is unavailable"))
	}
	legacy, err := NewScanCancellationGuard(gormEventAppender{scoped: appender})
	if err != nil {
		return nil, err
	}
	return &GORMScanCancellationGuard{database: database, legacy: legacy}, nil
}

func (guard *GORMScanCancellationGuard) SafeToCancelWorkflowNodeScoped(ctx context.Context, scope foundation.TransactionScope, nodeRunID foundation.ID) (bool, error) {
	if guard == nil || guard.legacy == nil || !validHealthGORMDatabase(guard.database) || ctx == nil || scope == nil {
		return false, healthGORMInvalid("health GORM cancellation scope is invalid")
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return false, healthGORMUnavailable(err)
	}
	bridge := &healthGORMTx{database: transaction.WithContext(ctx), scope: scope}
	return guard.legacy.SafeToCancelWorkflowNode(ctx, bridge, nodeRunID)
}

var _ workflowapplication.ScopedCancellationSafetyGuard = (*GORMScanCancellationGuard)(nil)
