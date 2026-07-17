package riveradapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// Migrator wraps River's official migrator with the workflow schema frozen.
type Migrator struct {
	inner  *rivermigrate.Migrator[pgx.Tx]
	schema string
}

// NewMigrator constructs the official River migrator for the workflow schema.
func NewMigrator(pool *pgxpool.Pool) (*Migrator, error) {
	if pool == nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_POOL_MISSING", errors.New("PostgreSQL pool is nil"))
	}
	inner, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: WorkflowSchema})
	if err != nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_MIGRATOR_INVALID", err)
	}
	return &Migrator{inner: inner, schema: WorkflowSchema}, nil
}

// Schema returns the explicitly configured River migration schema.
func (m *Migrator) Schema() string {
	if m == nil {
		return ""
	}
	return m.schema
}

// Up applies all pending official River migrations.
func (m *Migrator) Up(ctx context.Context) error {
	if m == nil || m.inner == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_MIGRATOR_MISSING", errors.New("River migrator is nil"))
	}
	if _, err := m.inner.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_MIGRATION_FAILED", true, err)
	}
	return nil
}

// Validate verifies that the database matches the bundled River migrations.
func (m *Migrator) Validate(ctx context.Context) error {
	if m == nil || m.inner == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_MIGRATOR_MISSING", errors.New("River migrator is nil"))
	}
	result, err := m.inner.Validate(ctx, nil)
	if err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_MIGRATION_VALIDATE_FAILED", true, err)
	}
	if result == nil || !result.OK {
		return jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_RIVER_MIGRATION_INVALID", fmt.Errorf("River migration validation failed: %#v", result))
	}
	return nil
}
