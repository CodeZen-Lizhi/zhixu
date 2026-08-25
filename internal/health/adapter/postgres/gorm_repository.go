package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	collectionapplication "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapplication "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
)

// GORMScanRepository is the staged Scan adapter. It retains the frozen SQL
// implementation as a behavior oracle while all transactions run through the
// shared GORM/database/sql UnitOfWork.
type GORMScanRepository struct {
	database *gormDB
	legacy   *ScanRepository
}

// NewGORMScanRepository constructs a scoped Scan adapter. Optional
// dependencies may be the new Workflow/Event scoped ports, their legacy
// equivalents, an ID generator, a clock, or a Health binding verifier.
func NewGORMScanRepository(pool *platformpostgres.Pool, dependencies ...any) (*GORMScanRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	var runtime ScanRuntimeStarter
	var events eventsapplication.Appender
	var verifier SmartCollectionBindingVerifier
	var ids foundation.IDGenerator
	var clock foundation.Clock
	for _, dependency := range dependencies {
		switch typed := dependency.(type) {
		case workflowapplication.ScopedRuntimeStarter:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Workflow dependency is nil")
			}
			runtime = gormRuntimeStarter{scoped: typed}
		case ScanRuntimeStarter:
			runtime = typed
		case collectionapplication.ScopedDurableScanBindingVerifier:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Collection dependency is nil")
			}
			verifier = gormCollectionBindingVerifier{scoped: typed}
		case eventsapplication.ScopedAppender:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Event dependency is nil")
			}
			events = gormEventAppender{scoped: typed}
		case eventsapplication.Appender:
			events = typed
		case SmartCollectionBindingVerifier:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM Collection verifier is nil")
			}
			verifier = typed
		case foundation.IDGenerator:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM ID generator is nil")
			}
			ids = typed
		case foundation.Clock:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM clock is nil")
			}
			clock = typed
		case nil:
			return nil, healthGORMInvalid("health GORM Scan dependency is nil")
		default:
			return nil, healthGORMInvalid(fmt.Sprintf("unsupported health GORM Scan dependency %T", dependency))
		}
	}
	if isNilHealthDependency(runtime) || isNilHealthDependency(events) {
		return nil, healthGORMUnavailable(errors.New("health GORM Scan requires scoped Workflow and Event dependencies"))
	}
	if isNilHealthDependency(ids) {
		ids = foundation.NewUUIDGenerator(nil)
	}
	if isNilHealthDependency(clock) {
		clock = foundation.SystemClock{}
	}
	var legacy *ScanRepository
	if verifier == nil {
		legacy, err = NewScanRepository(database, runtime, events, ids, clock)
	} else {
		legacy, err = NewScanRepository(database, runtime, events, ids, clock, verifier)
	}
	if err != nil {
		return nil, err
	}
	return &GORMScanRepository{database: database, legacy: legacy}, nil
}

// gormCollectionBindingVerifier bridges the Health legacy callback to the
// Collection scoped port without opening a second transaction.
type gormCollectionBindingVerifier struct {
	scoped collectionapplication.ScopedDurableScanBindingVerifier
}

func (verifier gormCollectionBindingVerifier) Verify(ctx context.Context, transaction pgx.Tx, binding healthapplication.SmartCollectionBinding) error {
	bridge, ok := transaction.(*healthGORMTx)
	if !ok || bridge.scope == nil || isNilHealthDependency(verifier.scoped) {
		return errors.New("health scoped Collection transaction is unavailable")
	}
	return verifier.scoped.VerifyDurableScanBindingScoped(ctx, bridge.scope, collectionapplication.DurableScanBinding{
		WorkspaceID: binding.WorkspaceID, CollectionID: binding.CollectionID,
		CollectionVersion: binding.CollectionVersion, QueryHash: binding.QueryHash,
		ReadModelRevision: binding.ReadModelRevision, ExactCount: binding.ExactCount,
	})
}

// NewGORMScanStateRepository constructs the worker-only Scan state adapter.
func NewGORMScanStateRepository(pool *platformpostgres.Pool, dependencies ...any) (*GORMScanRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	var events eventsapplication.Appender
	for _, dependency := range dependencies {
		switch typed := dependency.(type) {
		case eventsapplication.ScopedAppender:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Event dependency is nil")
			}
			events = gormEventAppender{scoped: typed}
		case eventsapplication.Appender:
			events = typed
		case nil:
			return nil, healthGORMInvalid("health GORM Scan state dependency is nil")
		default:
			return nil, healthGORMInvalid(fmt.Sprintf("unsupported health GORM Scan state dependency %T", dependency))
		}
	}
	if isNilHealthDependency(events) {
		return nil, healthGORMUnavailable(errors.New("health GORM Scan state requires an Event dependency"))
	}
	legacy, err := NewScanStateRepository(database, events)
	if err != nil {
		return nil, err
	}
	return &GORMScanRepository{database: database, legacy: legacy}, nil
}

func (repository *GORMScanRepository) StartOrReplay(ctx context.Context, request healthapplication.ScanStartRequest) (healthapplication.ScanStartResult, error) {
	if err := repository.ready(ctx); err != nil {
		return healthapplication.ScanStartResult{}, err
	}
	return repository.legacy.StartOrReplay(ctx, request)
}

func (repository *GORMScanRepository) Get(ctx context.Context, workspaceID, scanID foundation.ID) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.legacy.Get(ctx, workspaceID, scanID)
}

func (repository *GORMScanRepository) GetByWorkflowRun(ctx context.Context, workspaceID, workflowRunID foundation.ID) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.legacy.GetByWorkflowRun(ctx, workspaceID, workflowRunID)
}

func (repository *GORMScanRepository) Advance(ctx context.Context, progress healthapplication.ScanProgress) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.legacy.Advance(ctx, progress)
}

func (repository *GORMScanRepository) Finish(ctx context.Context, terminal healthapplication.ScanTerminal) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.legacy.Finish(ctx, terminal)
}

func (repository *GORMScanRepository) ready(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.legacy == nil {
		return healthGORMUnavailable(errors.New("health GORM Scan repository is unavailable"))
	}
	if ctx == nil {
		return healthGORMInvalid("health GORM Scan context is nil")
	}
	return nil
}

// GORMScheduleRepository preserves schedule command receipts and due leases.
type GORMScheduleRepository struct {
	database *gormDB
	legacy   *ScheduleRepository
}

// NewGORMScheduleRepository constructs the staged Schedule adapter.
func NewGORMScheduleRepository(pool *platformpostgres.Pool, dependencies ...any) (*GORMScheduleRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	var legacy *ScheduleRepository
	ids := foundation.IDGenerator(foundation.NewUUIDGenerator(nil))
	generatorConfigured := false
	var lease time.Duration
	leaseConfigured := false
	for _, dependency := range dependencies {
		switch typed := dependency.(type) {
		case foundation.IDGenerator:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM Schedule ID generator is nil")
			}
			if generatorConfigured {
				return nil, healthGORMInvalid("health GORM Schedule ID generator is duplicated")
			}
			ids = typed
			generatorConfigured = true
		case time.Duration:
			if leaseConfigured {
				return nil, healthGORMInvalid("health GORM Schedule lease is duplicated")
			}
			lease = typed
			leaseConfigured = true
		case nil:
			return nil, healthGORMInvalid("health GORM Schedule dependency is nil")
		default:
			return nil, healthGORMInvalid(fmt.Sprintf("unsupported health GORM Schedule dependency %T", dependency))
		}
	}
	if leaseConfigured {
		legacy, err = NewScheduleRepository(database, ids, lease)
	} else {
		legacy, err = NewScheduleRepository(database, ids)
	}
	if err != nil {
		return nil, err
	}
	return &GORMScheduleRepository{database: database, legacy: legacy}, nil
}

func (repository *GORMScheduleRepository) Create(ctx context.Context, command healthapplication.ScheduleCreateCommand) (domain.Schedule, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return domain.Schedule{}, err
	}
	return repository.legacy.Create(ctx, command)
}

func (repository *GORMScheduleRepository) Update(ctx context.Context, command healthapplication.ScheduleUpdateCommand) (domain.Schedule, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return domain.Schedule{}, err
	}
	return repository.legacy.Update(ctx, command)
}

func (repository *GORMScheduleRepository) Get(ctx context.Context, workspaceID, scheduleID foundation.ID) (domain.Schedule, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return domain.Schedule{}, err
	}
	return repository.legacy.Get(ctx, workspaceID, scheduleID)
}

func (repository *GORMScheduleRepository) ClaimDue(ctx context.Context, limit int) ([]healthapplication.DueScheduleClaim, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return nil, err
	}
	return repository.legacy.ClaimDue(ctx, limit)
}

func (repository *GORMScheduleRepository) AcknowledgeDue(ctx context.Context, claim healthapplication.DueScheduleClaim) error {
	if err := repository.scheduleReady(ctx); err != nil {
		return err
	}
	return repository.legacy.AcknowledgeDue(ctx, claim)
}

func (repository *GORMScheduleRepository) ReleaseDue(ctx context.Context, claim healthapplication.DueScheduleClaim) error {
	if err := repository.scheduleReady(ctx); err != nil {
		return err
	}
	return repository.legacy.ReleaseDue(ctx, claim)
}

func (repository *GORMScheduleRepository) scheduleReady(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.legacy == nil {
		return healthGORMUnavailable(errors.New("health GORM Schedule repository is unavailable"))
	}
	if ctx == nil {
		return healthGORMInvalid("health GORM Schedule context is nil")
	}
	return nil
}

// GORMIssueRepository preserves detector-page idempotency and Issue CAS.
type GORMIssueRepository struct {
	database *gormDB
	legacy   *IssueRepository
}

// NewGORMIssueRepository constructs the staged Issue adapter. Optional
// dependencies may be a Health membership port or an ID generator.
func NewGORMIssueRepository(pool *platformpostgres.Pool, dependencies ...any) (*GORMIssueRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	var membership healthapplication.SmartCollectionMembershipPort
	var generator foundation.IDGenerator
	for _, dependency := range dependencies {
		switch typed := dependency.(type) {
		case healthapplication.SmartCollectionMembershipPort:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM Issue membership is nil")
			}
			membership = typed
		case foundation.IDGenerator:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM Issue ID generator is nil")
			}
			generator = typed
		case nil:
			return nil, healthGORMInvalid("health GORM Issue dependency is nil")
		default:
			return nil, healthGORMInvalid(fmt.Sprintf("unsupported health GORM Issue dependency %T", dependency))
		}
	}
	if isNilHealthDependency(generator) {
		generator = foundation.NewUUIDGenerator(nil)
	}
	var legacy *IssueRepository
	if membership != nil {
		legacy, err = NewSmartCollectionIssueRepository(database, membership, generator)
	} else {
		legacy, err = NewIssueRepository(database, generator)
	}
	if err != nil {
		return nil, err
	}
	return &GORMIssueRepository{database: database, legacy: legacy}, nil
}

func (repository *GORMIssueRepository) ReconcileDetectorPage(ctx context.Context, request healthapplication.DetectorPageReconcileRequest) (healthapplication.DetectorPageReconcileResult, error) {
	if err := repository.issueReady(ctx); err != nil {
		return healthapplication.DetectorPageReconcileResult{}, err
	}
	return repository.legacy.ReconcileDetectorPage(ctx, request)
}

func (repository *GORMIssueRepository) ResolveMissingForCompleteScan(ctx context.Context, workspaceID, scanID foundation.ID, detectorID string) (int64, error) {
	if err := repository.issueReady(ctx); err != nil {
		return 0, err
	}
	return repository.legacy.ResolveMissingForCompleteScan(ctx, workspaceID, scanID, detectorID)
}

func (repository *GORMIssueRepository) UpsertObservation(ctx context.Context, workspaceID, scanID, issueID foundation.ID, observation domain.IssueObservation, repairOptions []domain.RepairOption, observedAt time.Time) (domain.Issue, domain.ObservationOutcome, error) {
	if err := repository.issueReady(ctx); err != nil {
		return domain.Issue{}, "", err
	}
	return repository.legacy.UpsertObservation(ctx, workspaceID, scanID, issueID, observation, repairOptions, observedAt)
}

func (repository *GORMIssueRepository) GetIssue(ctx context.Context, workspaceID, issueID foundation.ID, repairOptions []domain.RepairOption) (domain.Issue, error) {
	if err := repository.issueReady(ctx); err != nil {
		return domain.Issue{}, err
	}
	return repository.legacy.GetIssue(ctx, workspaceID, issueID, repairOptions)
}

func (repository *GORMIssueRepository) ApplyDecision(ctx context.Context, workspaceID, issueID foundation.ID, decision domain.IssueDecision, at time.Time, repairOptions []domain.RepairOption) (domain.Issue, error) {
	if err := repository.issueReady(ctx); err != nil {
		return domain.Issue{}, err
	}
	return repository.legacy.ApplyDecision(ctx, workspaceID, issueID, decision, at, repairOptions)
}

func (repository *GORMIssueRepository) issueReady(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.legacy == nil {
		return healthGORMUnavailable(errors.New("health GORM Issue repository is unavailable"))
	}
	if ctx == nil {
		return healthGORMInvalid("health GORM Issue context is nil")
	}
	return nil
}

// GORMReadRepository is the staged read projection adapter.
type GORMReadRepository struct {
	database *gormDB
	legacy   *ReadRepository
}

func NewGORMReadRepository(pool *platformpostgres.Pool) (*GORMReadRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	return &GORMReadRepository{database: database, legacy: &ReadRepository{db: database}}, nil
}

func (repository *GORMReadRepository) ListIssues(ctx context.Context, request healthapplication.IssueListQuery) (healthapplication.IssueListResult, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueListResult{}, err
	}
	return repository.legacy.ListIssues(ctx, request)
}

func (repository *GORMReadRepository) GetIssue(ctx context.Context, workspaceID, issueID foundation.ID) (healthapplication.IssueSnapshot, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueSnapshot{}, err
	}
	return repository.legacy.GetIssue(ctx, workspaceID, issueID)
}

func (repository *GORMReadRepository) ListIssueObservations(ctx context.Context, request healthapplication.IssueObservationQuery) (healthapplication.IssueObservationResult, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueObservationResult{}, err
	}
	return repository.legacy.ListIssueObservations(ctx, request)
}

func (repository *GORMReadRepository) ListIssueDecisions(ctx context.Context, request healthapplication.IssueDecisionQuery) (healthapplication.IssueDecisionResult, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueDecisionResult{}, err
	}
	return repository.legacy.ListIssueDecisions(ctx, request)
}

func (repository *GORMReadRepository) GetHealthSummary(ctx context.Context, workspaceID foundation.ID) (healthapplication.HealthSummary, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.HealthSummary{}, err
	}
	return repository.legacy.GetHealthSummary(ctx, workspaceID)
}

func (repository *GORMReadRepository) readReady(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.legacy == nil {
		return healthGORMUnavailable(errors.New("health GORM read repository is unavailable"))
	}
	if ctx == nil {
		return healthGORMInvalid("health GORM read context is nil")
	}
	return nil
}

// GORMFactReader adapts canonical detector reads to the shared GORM root.
type GORMFactReader struct {
	database *gormDB
	legacy   *FactReader
}

func NewGORMFactReader(pool *platformpostgres.Pool, memberships ...healthapplication.SmartCollectionMembershipPort) (*GORMFactReader, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	for _, membership := range memberships {
		if isNilHealthDependency(membership) {
			return nil, healthGORMInvalid("health GORM detector membership is nil")
		}
	}
	legacy, err := NewFactReader(database, memberships...)
	if err != nil {
		return nil, err
	}
	return &GORMFactReader{database: database, legacy: legacy}, nil
}

func (reader *GORMFactReader) Find(ctx context.Context, detectorID string, request healthapplication.PageRequest) (healthapplication.Page, error) {
	if reader == nil || reader.database == nil || reader.legacy == nil {
		return healthapplication.Page{}, healthapplication.ErrDetectorUnavailable
	}
	if ctx == nil {
		return healthapplication.Page{}, healthGORMInvalid("health GORM detector context is nil")
	}
	return reader.legacy.Find(ctx, detectorID, request)
}

// GORMAffectedChangeDispatchRepository preserves one-row SKIP LOCKED claim
// and commit recovery while using the GORM-backed Scan transaction seam.
type GORMAffectedChangeDispatchRepository struct {
	database *gormDB
	legacy   *AffectedChangeDispatchRepository
}

func NewGORMAffectedChangeDispatchRepository(scans *GORMScanRepository, planner *healthapplication.AffectedChangePlanner) (*GORMAffectedChangeDispatchRepository, error) {
	if scans == nil || scans.database == nil || scans.legacy == nil {
		return nil, healthGORMUnavailable(errors.New("health GORM affected-change Scan repository is unavailable"))
	}
	legacy, err := NewAffectedChangeDispatchRepository(scans.legacy, planner)
	if err != nil {
		return nil, err
	}
	return &GORMAffectedChangeDispatchRepository{database: scans.database, legacy: legacy}, nil
}

func (repository *GORMAffectedChangeDispatchRepository) DispatchNext(ctx context.Context) (healthapplication.AffectedChangeDispatchResult, bool, error) {
	if repository == nil || repository.database == nil || repository.legacy == nil {
		return healthapplication.AffectedChangeDispatchResult{}, false, healthGORMUnavailable(errors.New("health GORM affected-change repository is unavailable"))
	}
	if ctx == nil {
		return healthapplication.AffectedChangeDispatchResult{}, false, healthGORMInvalid("health GORM affected-change context is nil")
	}
	return repository.legacy.DispatchNext(ctx)
}

func newHealthGORMDependencies(pool *platformpostgres.Pool) (*gormDB, error) {
	if pool == nil {
		return nil, healthGORMUnavailable(errors.New("health PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, healthGORMUnavailable(fmt.Errorf("health GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, healthGORMUnavailable(fmt.Errorf("health GORM unit of work is unavailable: %w", err))
	}
	return newHealthGORMDB(database, unitOfWork)
}

func healthGORMInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "HEALTH_GORM_INVALID", false, errors.New(message))
}

func healthGORMUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "HEALTH_GORM_UNAVAILABLE", true, cause)
}

var _ healthapplication.ScanStartPort = (*GORMScanRepository)(nil)
var _ healthapplication.ScanStatePort = (*GORMScanRepository)(nil)
var _ healthapplication.ScheduleDispatchPort = (*GORMScheduleRepository)(nil)
var _ healthapplication.DetectorPageStore = (*GORMIssueRepository)(nil)
var _ healthapplication.IssueReadPort = (*GORMReadRepository)(nil)
var _ healthapplication.FactReader = (*GORMFactReader)(nil)
var _ healthapplication.AffectedChangeDispatchPort = (*GORMAffectedChangeDispatchRepository)(nil)
