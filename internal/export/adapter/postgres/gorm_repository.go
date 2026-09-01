package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMRepository is the staged Export implementation. It runs the existing
// explicit Export SQL through the shared GORM/database/sql transaction
// boundary; production composition remains on Repository until the Final child.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	core       *Repository
	events     eventsapplication.ScopedAppender
	audit      auditapplication.ScopedAppender
}

// GORMOption configures staged Export collaborators without exposing a
// concrete transaction type through the public constructor.
type GORMOption func(*GORMRepository) error

// WithGORMEventAppender configures the scoped lifecycle Event appender.
func WithGORMEventAppender(appender eventsapplication.ScopedAppender) GORMOption {
	return func(repository *GORMRepository) error {
		if isNilDependency(appender) {
			return invalid(errors.New("export event appender is nil"))
		}
		if !isNilDependency(repository.events) {
			return invalid(errors.New("export event appender is duplicated"))
		}
		repository.events = appender
		return nil
	}
}

// WithGORMAuditAppender configures the scoped download Audit appender.
func WithGORMAuditAppender(appender auditapplication.ScopedAppender) GORMOption {
	return func(repository *GORMRepository) error {
		if isNilDependency(appender) {
			return invalid(errors.New("export audit appender is nil"))
		}
		if !isNilDependency(repository.audit) {
			return invalid(errors.New("export audit appender is duplicated"))
		}
		repository.audit = appender
		return nil
	}
}

// NewGORMRepository constructs a staged repository from the shared platform
// pool. Optional appenders are retained for the scoped migration seam; no
// appender owns or commits the transaction.
func NewGORMRepository(pool *platformpostgres.Pool, options ...GORMOption) (*GORMRepository, error) {
	if pool == nil {
		return nil, unavailable(errors.New("export PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(fmt.Errorf("export GORM database is unavailable: %w", err))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(fmt.Errorf("export GORM unit of work is unavailable: %w", err))
	}
	return newGORMRepository(database, unitOfWork, options...)
}

func newGORMRepository(database *gorm.DB, unitOfWork foundation.UnitOfWork, options ...GORMOption) (*GORMRepository, error) {
	if !validExportGORMDatabase(database) || isNilDependency(unitOfWork) {
		return nil, unavailable(errors.New("export GORM repository dependencies are unavailable"))
	}
	gormDatabase, err := newGORMDB(database, unitOfWork)
	if err != nil {
		return nil, unavailable(err)
	}
	core, err := newRepository(gormDatabase)
	if err != nil {
		return nil, err
	}
	repository := &GORMRepository{database: database, unitOfWork: unitOfWork, core: core}
	for _, option := range options {
		if option == nil {
			return nil, invalid(errors.New("export GORM repository option is nil"))
		}
		if err := option(repository); err != nil {
			return nil, err
		}
	}
	if !isNilDependency(repository.events) {
		core.events = &gormEventAppender{scoped: repository.events}
	}
	if !isNilDependency(repository.audit) {
		core.audit = &gormAuditAppender{scoped: repository.audit}
	}
	// The shared SQL core keeps the legacy and GORM paths behaviorally aligned;
	// only their database and transaction adapters differ.
	return repository, nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validExportGORMDatabase(repository.database) || repository.core == nil || isNilDependency(repository.unitOfWork) {
		return unavailable(errors.New("export GORM repository is unavailable"))
	}
	if ctx == nil {
		return invalid(errors.New("export GORM context is nil"))
	}
	return nil
}

func validExportGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func (repository *GORMRepository) Create(ctx context.Context, job domain.Job) (domain.Job, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, false, err
	}
	created, replayed, err := repository.core.Create(ctx, job)
	return created, replayed, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) Get(ctx context.Context, workspaceID, jobID foundation.ID) (domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := repository.core.Get(ctx, workspaceID, jobID)
	return job, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) List(ctx context.Context, query exportapp.ListQuery) (exportapp.ListPage, error) {
	if err := repository.ready(ctx); err != nil {
		return exportapp.ListPage{}, err
	}
	page, err := repository.core.List(ctx, query)
	return page, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) Claim(ctx context.Context, workspaceID, jobID foundation.ID, owner string, lease time.Duration) (domain.Job, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, false, err
	}
	job, claimed, err := repository.core.Claim(ctx, workspaceID, jobID, owner, lease)
	return job, claimed, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) Prepare(ctx context.Context, request exportapp.PrepareRequest) (domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := repository.core.Prepare(ctx, request)
	return job, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) Complete(ctx context.Context, request exportapp.CompleteRequest) (domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := repository.core.Complete(ctx, request)
	return job, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) Fail(ctx context.Context, request exportapp.FailRequest) (domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := repository.core.Fail(ctx, request)
	return job, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) Expire(ctx context.Context, workspaceID, jobID foundation.ID) (domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := repository.core.Expire(ctx, workspaceID, jobID)
	return job, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) RecordDownload(ctx context.Context, request exportapp.DownloadRecord) (domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := repository.core.RecordDownload(ctx, request)
	return job, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) RecoveryCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	jobs, err := repository.core.RecoveryCandidates(ctx, limit)
	return jobs, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) ExpireCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	jobs, err := repository.core.ExpireCandidates(ctx, limit)
	return jobs, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) CleanupCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	jobs, err := repository.core.CleanupCandidates(ctx, limit)
	return jobs, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) RecordCleanup(ctx context.Context, request exportapp.CleanupRequest) (domain.Job, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := repository.core.RecordCleanup(ctx, request)
	return job, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) UnreferencedStaging(ctx context.Context, workspaceID foundation.ID, paths []string) ([]string, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	paths, err := repository.core.UnreferencedStaging(ctx, workspaceID, paths)
	return paths, preserveGORMExportContextCause(ctx, err)
}
func (repository *GORMRepository) OrphanSweepWorkspaces(ctx context.Context, afterWorkspaceID foundation.ID, limit int) ([]foundation.ID, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	workspaceIDs, err := repository.core.OrphanSweepWorkspaces(ctx, afterWorkspaceID, limit)
	return workspaceIDs, preserveGORMExportContextCause(ctx, err)
}

func preserveGORMExportContextCause(ctx context.Context, err error) error {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return err
	}
	contextErr := ctx.Err()
	callerCause := context.Cause(ctx)
	if callerCause == nil {
		callerCause = contextErr
	}
	if errors.Is(err, contextErr) && errors.Is(err, callerCause) {
		return err
	}
	cause := errors.Join(err, contextErr)
	if !errors.Is(cause, callerCause) {
		cause = errors.Join(cause, callerCause)
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return foundation.NewError(classified.Kind, classified.Code, classified.Retryable, cause)
	}
	return cause
}

var _ exportapp.Repository = (*GORMRepository)(nil)

type gormEventAppender struct {
	scoped eventsapplication.ScopedAppender
}

func (appender *gormEventAppender) AppendTx(ctx context.Context, transaction any, request eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	if appender == nil || isNilDependency(appender.scoped) {
		return eventsdomain.ServerEvent{}, false, unavailable(errors.New("export lifecycle event appender is unavailable"))
	}
	tx, ok := transaction.(*gormTx)
	if !ok || tx.scope == nil {
		return eventsdomain.ServerEvent{}, false, unavailable(errors.New("export lifecycle transaction scope is unavailable"))
	}
	return appender.scoped.AppendScoped(ctx, tx.scope, request)
}

type gormAuditAppender struct {
	scoped auditapplication.ScopedAppender
}

func (appender *gormAuditAppender) AppendTx(ctx context.Context, transaction any, event auditdomain.Event) (auditdomain.Event, bool, error) {
	if appender == nil || isNilDependency(appender.scoped) {
		return auditdomain.Event{}, false, unavailable(errors.New("export download audit appender is unavailable"))
	}
	tx, ok := transaction.(*gormTx)
	if !ok || tx.scope == nil {
		return auditdomain.Event{}, false, unavailable(errors.New("export audit transaction scope is unavailable"))
	}
	return appender.scoped.AppendScoped(ctx, tx.scope, event)
}
