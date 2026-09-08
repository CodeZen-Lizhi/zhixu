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
)

// GORMScanRepository 使用共享 UnitOfWork 与同一套 Health SQL、锁和恢复规则。
type GORMScanRepository struct {
	database *gormDB
	core     *ScanRepository
}

// NewGORMScanRepository 接受 scoped Workflow/Event/Collection 端口、ID generator 与 clock。
func NewGORMScanRepository(pool *platformpostgres.Pool, dependencies ...any) (*GORMScanRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	var runtime workflowapplication.ScopedRuntimeStarter
	var events eventsapplication.ScopedAppender
	var verifier scopedHealthBindingVerifier
	var ids foundation.IDGenerator
	var clock foundation.Clock
	for _, dependency := range dependencies {
		switch typed := dependency.(type) {
		case workflowapplication.ScopedRuntimeStarter:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Workflow dependency is nil")
			}
			runtime = typed
		case collectionapplication.ScopedDurableScanBindingVerifier:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Collection dependency is nil")
			}
			verifier = gormCollectionBindingVerifier{scoped: typed}
		case scopedHealthBindingVerifier:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Collection verifier is nil")
			}
			verifier = typed
		case eventsapplication.ScopedAppender:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Event dependency is nil")
			}
			events = typed
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
	var core *ScanRepository
	if verifier == nil {
		core, err = newScanRepository(database, runtime, events, ids, clock)
	} else {
		core, err = newScanRepository(database, runtime, events, ids, clock, verifier)
	}
	if err != nil {
		return nil, err
	}
	return &GORMScanRepository{database: database, core: core}, nil
}

// gormCollectionBindingVerifier 将 Health binding 映射到 Collection 的同事务 scoped 端口。
type gormCollectionBindingVerifier struct {
	scoped collectionapplication.ScopedDurableScanBindingVerifier
}

func (verifier gormCollectionBindingVerifier) VerifyBindingScoped(ctx context.Context, scope foundation.TransactionScope, binding healthapplication.SmartCollectionBinding) error {
	if scope == nil || isNilHealthDependency(verifier.scoped) {
		return errors.New("health scoped Collection transaction is unavailable")
	}
	return verifier.scoped.VerifyDurableScanBindingScoped(ctx, scope, collectionapplication.DurableScanBinding{
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
	var events eventsapplication.ScopedAppender
	for _, dependency := range dependencies {
		switch typed := dependency.(type) {
		case eventsapplication.ScopedAppender:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Event dependency is nil")
			}
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
	core, err := newScanStateRepository(database, events)
	if err != nil {
		return nil, err
	}
	return &GORMScanRepository{database: database, core: core}, nil
}

func (repository *GORMScanRepository) StartOrReplay(ctx context.Context, request healthapplication.ScanStartRequest) (healthapplication.ScanStartResult, error) {
	if err := repository.ready(ctx); err != nil {
		return healthapplication.ScanStartResult{}, err
	}
	return repository.core.StartOrReplay(ctx, request)
}

func (repository *GORMScanRepository) Get(ctx context.Context, workspaceID, scanID foundation.ID) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.core.Get(ctx, workspaceID, scanID)
}

func (repository *GORMScanRepository) GetByWorkflowRun(ctx context.Context, workspaceID, workflowRunID foundation.ID) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.core.GetByWorkflowRun(ctx, workspaceID, workflowRunID)
}

func (repository *GORMScanRepository) Advance(ctx context.Context, progress healthapplication.ScanProgress) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.core.Advance(ctx, progress)
}

func (repository *GORMScanRepository) Finish(ctx context.Context, terminal healthapplication.ScanTerminal) (domain.Scan, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Scan{}, err
	}
	return repository.core.Finish(ctx, terminal)
}

func (repository *GORMScanRepository) ready(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.core == nil {
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
	core     *ScheduleRepository
}

// NewGORMScheduleRepository 构造使用共享事务边界的 Schedule adapter.
func NewGORMScheduleRepository(pool *platformpostgres.Pool, dependencies ...any) (*GORMScheduleRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	var core *ScheduleRepository
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
		core, err = newScheduleRepository(database, ids, lease)
	} else {
		core, err = newScheduleRepository(database, ids)
	}
	if err != nil {
		return nil, err
	}
	return &GORMScheduleRepository{database: database, core: core}, nil
}

func (repository *GORMScheduleRepository) Create(ctx context.Context, command healthapplication.ScheduleCreateCommand) (domain.Schedule, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return domain.Schedule{}, err
	}
	return repository.core.Create(ctx, command)
}

func (repository *GORMScheduleRepository) Update(ctx context.Context, command healthapplication.ScheduleUpdateCommand) (domain.Schedule, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return domain.Schedule{}, err
	}
	return repository.core.Update(ctx, command)
}

func (repository *GORMScheduleRepository) Get(ctx context.Context, workspaceID, scheduleID foundation.ID) (domain.Schedule, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return domain.Schedule{}, err
	}
	return repository.core.Get(ctx, workspaceID, scheduleID)
}

func (repository *GORMScheduleRepository) ClaimDue(ctx context.Context, limit int) ([]healthapplication.DueScheduleClaim, error) {
	if err := repository.scheduleReady(ctx); err != nil {
		return nil, err
	}
	return repository.core.ClaimDue(ctx, limit)
}

func (repository *GORMScheduleRepository) AcknowledgeDue(ctx context.Context, claim healthapplication.DueScheduleClaim) error {
	if err := repository.scheduleReady(ctx); err != nil {
		return err
	}
	return repository.core.AcknowledgeDue(ctx, claim)
}

func (repository *GORMScheduleRepository) ReleaseDue(ctx context.Context, claim healthapplication.DueScheduleClaim) error {
	if err := repository.scheduleReady(ctx); err != nil {
		return err
	}
	return repository.core.ReleaseDue(ctx, claim)
}

func (repository *GORMScheduleRepository) scheduleReady(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.core == nil {
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
	core     *IssueRepository
}

// NewGORMIssueRepository 构造 Issue adapter；传入 membership 时必须同时提供 scoped binding verifier。
func NewGORMIssueRepository(pool *platformpostgres.Pool, dependencies ...any) (*GORMIssueRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	var membership healthapplication.SmartCollectionMembershipPort
	var generator foundation.IDGenerator
	var bindingVerifier scopedHealthBindingVerifier
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
		case collectionapplication.ScopedDurableScanBindingVerifier:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Collection dependency is nil")
			}
			bindingVerifier = gormCollectionBindingVerifier{scoped: typed}
		case scopedHealthBindingVerifier:
			if isNilHealthDependency(typed) {
				return nil, healthGORMInvalid("health GORM scoped Collection verifier is nil")
			}
			bindingVerifier = typed
		case nil:
			return nil, healthGORMInvalid("health GORM Issue dependency is nil")
		default:
			return nil, healthGORMInvalid(fmt.Sprintf("unsupported health GORM Issue dependency %T", dependency))
		}
	}
	if isNilHealthDependency(generator) {
		generator = foundation.NewUUIDGenerator(nil)
	}
	var core *IssueRepository
	if membership != nil {
		if bindingVerifier == nil {
			return nil, healthGORMInvalid("health GORM Issue membership requires a scoped Collection verifier")
		}
		core, err = newSmartCollectionIssueRepository(database, membership, bindingVerifier, generator)
	} else {
		if bindingVerifier != nil {
			return nil, healthGORMInvalid("health GORM scoped Collection verifier requires membership")
		}
		core, err = newIssueRepository(database, generator)
	}
	if err != nil {
		return nil, err
	}
	return &GORMIssueRepository{database: database, core: core}, nil
}

func (repository *GORMIssueRepository) ReconcileDetectorPage(ctx context.Context, request healthapplication.DetectorPageReconcileRequest) (healthapplication.DetectorPageReconcileResult, error) {
	if err := repository.issueReady(ctx); err != nil {
		return healthapplication.DetectorPageReconcileResult{}, err
	}
	return repository.core.ReconcileDetectorPage(ctx, request)
}

func (repository *GORMIssueRepository) ResolveMissingForCompleteScan(ctx context.Context, workspaceID, scanID foundation.ID, detectorID string) (int64, error) {
	if err := repository.issueReady(ctx); err != nil {
		return 0, err
	}
	return repository.core.ResolveMissingForCompleteScan(ctx, workspaceID, scanID, detectorID)
}

func (repository *GORMIssueRepository) UpsertObservation(ctx context.Context, workspaceID, scanID, issueID foundation.ID, observation domain.IssueObservation, repairOptions []domain.RepairOption, observedAt time.Time) (domain.Issue, domain.ObservationOutcome, error) {
	if err := repository.issueReady(ctx); err != nil {
		return domain.Issue{}, "", err
	}
	return repository.core.UpsertObservation(ctx, workspaceID, scanID, issueID, observation, repairOptions, observedAt)
}

func (repository *GORMIssueRepository) GetIssue(ctx context.Context, workspaceID, issueID foundation.ID, repairOptions []domain.RepairOption) (domain.Issue, error) {
	if err := repository.issueReady(ctx); err != nil {
		return domain.Issue{}, err
	}
	return repository.core.GetIssue(ctx, workspaceID, issueID, repairOptions)
}

func (repository *GORMIssueRepository) ApplyDecision(ctx context.Context, workspaceID, issueID foundation.ID, decision domain.IssueDecision, at time.Time, repairOptions []domain.RepairOption) (domain.Issue, error) {
	if err := repository.issueReady(ctx); err != nil {
		return domain.Issue{}, err
	}
	return repository.core.ApplyDecision(ctx, workspaceID, issueID, decision, at, repairOptions)
}

func (repository *GORMIssueRepository) issueReady(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.core == nil {
		return healthGORMUnavailable(errors.New("health GORM Issue repository is unavailable"))
	}
	if ctx == nil {
		return healthGORMInvalid("health GORM Issue context is nil")
	}
	return nil
}

// GORMReadRepository 通过共享 GORM root 读取 Health 投影。
type GORMReadRepository struct {
	database *gormDB
	core     *ReadRepository
}

func NewGORMReadRepository(pool *platformpostgres.Pool) (*GORMReadRepository, error) {
	database, err := newHealthGORMDependencies(pool)
	if err != nil {
		return nil, err
	}
	return &GORMReadRepository{database: database, core: &ReadRepository{db: database}}, nil
}

func (repository *GORMReadRepository) ListIssues(ctx context.Context, request healthapplication.IssueListQuery) (healthapplication.IssueListResult, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueListResult{}, err
	}
	return repository.core.ListIssues(ctx, request)
}

func (repository *GORMReadRepository) GetIssue(ctx context.Context, workspaceID, issueID foundation.ID) (healthapplication.IssueSnapshot, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueSnapshot{}, err
	}
	return repository.core.GetIssue(ctx, workspaceID, issueID)
}

func (repository *GORMReadRepository) ListIssueObservations(ctx context.Context, request healthapplication.IssueObservationQuery) (healthapplication.IssueObservationResult, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueObservationResult{}, err
	}
	return repository.core.ListIssueObservations(ctx, request)
}

func (repository *GORMReadRepository) ListIssueDecisions(ctx context.Context, request healthapplication.IssueDecisionQuery) (healthapplication.IssueDecisionResult, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.IssueDecisionResult{}, err
	}
	return repository.core.ListIssueDecisions(ctx, request)
}

func (repository *GORMReadRepository) GetHealthSummary(ctx context.Context, workspaceID foundation.ID) (healthapplication.HealthSummary, error) {
	if err := repository.readReady(ctx); err != nil {
		return healthapplication.HealthSummary{}, err
	}
	return repository.core.GetHealthSummary(ctx, workspaceID)
}

func (repository *GORMReadRepository) readReady(ctx context.Context) error {
	if repository == nil || repository.database == nil || repository.core == nil {
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
	core     *FactReader
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
	core, err := newFactReader(database, memberships...)
	if err != nil {
		return nil, err
	}
	return &GORMFactReader{database: database, core: core}, nil
}

func (reader *GORMFactReader) Find(ctx context.Context, detectorID string, request healthapplication.PageRequest) (healthapplication.Page, error) {
	if reader == nil || reader.database == nil || reader.core == nil {
		return healthapplication.Page{}, healthapplication.ErrDetectorUnavailable
	}
	if ctx == nil {
		return healthapplication.Page{}, healthGORMInvalid("health GORM detector context is nil")
	}
	return reader.core.Find(ctx, detectorID, request)
}

// GORMAffectedChangeDispatchRepository preserves one-row SKIP LOCKED claim
// and commit recovery while using the GORM-backed Scan transaction seam.
type GORMAffectedChangeDispatchRepository struct {
	database *gormDB
	core     *AffectedChangeDispatchRepository
}

func NewGORMAffectedChangeDispatchRepository(scans *GORMScanRepository, planner *healthapplication.AffectedChangePlanner) (*GORMAffectedChangeDispatchRepository, error) {
	if scans == nil || scans.database == nil || scans.core == nil {
		return nil, healthGORMUnavailable(errors.New("health GORM affected-change Scan repository is unavailable"))
	}
	core, err := NewAffectedChangeDispatchRepository(scans.core, planner)
	if err != nil {
		return nil, err
	}
	return &GORMAffectedChangeDispatchRepository{database: scans.database, core: core}, nil
}

func (repository *GORMAffectedChangeDispatchRepository) DispatchNext(ctx context.Context) (healthapplication.AffectedChangeDispatchResult, bool, error) {
	if repository == nil || repository.database == nil || repository.core == nil {
		return healthapplication.AffectedChangeDispatchResult{}, false, healthGORMUnavailable(errors.New("health GORM affected-change repository is unavailable"))
	}
	if ctx == nil {
		return healthapplication.AffectedChangeDispatchResult{}, false, healthGORMInvalid("health GORM affected-change context is nil")
	}
	return repository.core.DispatchNext(ctx)
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
