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
	events   eventsapplication.ScopedAppender
}

func NewGORMScanCancellationGuard(pool *platformpostgres.Pool, appender eventsapplication.ScopedAppender) (*GORMScanCancellationGuard, error) {
	if pool == nil || isNilHealthDependency(appender) {
		return nil, healthGORMUnavailable(errors.New("health GORM cancellation dependencies are unavailable"))
	}
	database, err := pool.GORM()
	if err != nil || !validHealthGORMDatabase(database) {
		return nil, healthGORMUnavailable(errors.New("health GORM cancellation database is unavailable"))
	}
	return &GORMScanCancellationGuard{database: database, events: appender}, nil
}

func (guard *GORMScanCancellationGuard) SafeToCancelWorkflowNodeScoped(ctx context.Context, scope foundation.TransactionScope, nodeRunID foundation.ID) (bool, error) {
	if guard == nil || isNilHealthDependency(guard.events) || !validHealthGORMDatabase(guard.database) || ctx == nil || scope == nil {
		return false, healthGORMInvalid("health GORM cancellation scope is invalid")
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return false, healthGORMUnavailable(err)
	}
	return cancelHealthWorkflowNode(ctx, healthTransaction{healthSQL: &gormDB{database: transaction}, scope: scope}, guard.events, nodeRunID)
}

var _ workflowapplication.ScopedCancellationSafetyGuard = (*GORMScanCancellationGuard)(nil)
