package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// SchemaVersionV1 是导出 JSON/Markdown envelope 的稳定版本。
	SchemaVersionV1                = "export/v1"
	DefaultTTL                     = 24 * time.Hour
	MaxTTL                         = 7 * 24 * time.Hour
	DefaultListLimit               = 50
	MaxListLimit                   = 100
	DefaultLease                   = 2 * time.Minute
	MaxSnapshotItems               = 10_000
	DefaultOrphanGrace             = 15 * time.Minute
	attachmentLimitContractVersion = "attachment-limits/v1"
)

// Dependencies 是导出应用服务的显式依赖。
type Dependencies struct {
	Repository  Repository
	Dispatcher  Dispatcher
	Snapshots   SnapshotReader
	Workspaces  WorkspaceReader
	Files       FileStore
	Attachments AttachmentArchiver
	Authorizer  Authorizer
	IDs         foundation.IDGenerator
	Clock       foundation.Clock
	Lease       time.Duration
}

// Service 编排导出任务创建、执行、下载和恢复。
type Service struct{ dependencies Dependencies }

// NewService 创建 fail-closed 的导出服务。
func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Repository == nil || dependencies.Snapshots == nil || dependencies.Workspaces == nil || dependencies.Files == nil || dependencies.IDs == nil || dependencies.Clock == nil {
		return nil, unavailable(errors.New("export dependencies are incomplete"))
	}
	if dependencies.Lease <= 0 {
		dependencies.Lease = DefaultLease
	}
	return &Service{dependencies: dependencies}, nil
}

// Create 创建或精确重放一个异步导出任务。持久化成功但投递失败时保留 PENDING，供恢复重试。
func (s *Service) Create(ctx context.Context, request domain.CreateRequest) (CreateResult, error) {
	if err := validContext(ctx); err != nil {
		return CreateResult{}, err
	}
	job, err := s.buildJob(ctx, request)
	if err != nil {
		return CreateResult{}, err
	}
	persisted, replayed, err := s.dependencies.Repository.Create(ctx, job)
	if err != nil {
		return CreateResult{}, err
	}
	if err := persisted.Validate(); err != nil || !domain.SameRequest(persisted, job) {
		if err == nil {
			err = errors.New("export repository returned a mismatched create result")
		}
		return CreateResult{}, resultInvalid(err)
	}
	result := CreateResult{Job: persisted, Replayed: replayed}
	if s.dependencies.Dispatcher != nil && persisted.Status == domain.StatusPending {
		if dispatchErr := s.dependencies.Dispatcher.Dispatch(ctx, persisted.WorkspaceID, persisted.ID); dispatchErr != nil {
			result.DispatchPending = true
		}
	}
	return result, nil
}

// CreateResult 是创建接口返回的任务和投递状态。
type CreateResult struct {
	Job             domain.Job
	Replayed        bool
	DispatchPending bool
}

// Get 返回 Workspace 绑定的任务；Repository 使用数据库时间先归约过期状态。
func (s *Service) Get(ctx context.Context, workspaceID, jobID foundation.ID) (domain.Job, error) {
	if err := validContext(ctx); err != nil {
		return domain.Job{}, err
	}
	job, err := s.dependencies.Repository.Get(ctx, workspaceID, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if job.WorkspaceID != workspaceID || job.ID != jobID {
		return domain.Job{}, resultInvalid(errors.New("export repository crossed the requested identity binding"))
	}
	if err := job.Validate(); err != nil {
		return domain.Job{}, resultInvalid(err)
	}
	return job, nil
}

// GetForScope 返回绑定到指定公开 scope 的任务；错 scope 与不存在统一为 NotFound，避免跨入口枚举。
func (s *Service) GetForScope(ctx context.Context, workspaceID, jobID foundation.ID, scopeKind domain.ScopeKind) (domain.Job, error) {
	if scopeKind != domain.ScopeCollection && scopeKind != domain.ScopeWorkspaceAttachments {
		return domain.Job{}, invalid(errors.New("export scope kind is invalid"))
	}
	job, err := s.Get(ctx, workspaceID, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if job.Scope.Kind != scopeKind {
		return domain.Job{}, notFound(errors.New("export job does not belong to the requested scope"))
	}
	return job, nil
}

// List 返回 Workspace 内稳定、有界的任务页面。
func (s *Service) List(ctx context.Context, query ListQuery) (ListPage, error) {
	if err := validContext(ctx); err != nil {
		return ListPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = DefaultListLimit
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > MaxListLimit || len(query.Cursor) > 4096 ||
		(query.ScopeKind != domain.ScopeCollection && query.ScopeKind != domain.ScopeWorkspaceAttachments) ||
		(query.CollectionID != nil && !validID(*query.CollectionID)) ||
		(query.ScopeKind == domain.ScopeCollection) != (query.CollectionID != nil) {
		return ListPage{}, invalid(errors.New("export list query is invalid"))
	}
	page, err := s.dependencies.Repository.List(ctx, query)
	if err != nil {
		return ListPage{}, err
	}
	if len(page.Items) > query.Limit {
		return ListPage{}, resultInvalid(errors.New("export list exceeded requested limit"))
	}
	for _, job := range page.Items {
		if job.WorkspaceID != query.WorkspaceID || job.Scope.Kind != query.ScopeKind || job.Validate() != nil ||
			(query.CollectionID != nil && (job.Scope.CollectionID == nil || *job.Scope.CollectionID != *query.CollectionID)) {
			return ListPage{}, resultInvalid(errors.New("export list crossed workspace binding"))
		}
	}
	return page, nil
}

// Execute 由 Worker 调用，执行一次带租约的生成并归约结果。
func (s *Service) Execute(ctx context.Context, workspaceID, jobID foundation.ID, owner string) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	owner = strings.TrimSpace(owner)
	if !validID(workspaceID) || !validID(jobID) || owner == "" || len(owner) > 128 || strings.ContainsAny(owner, "\r\n\t/") {
		return invalid(errors.New("export execution identity is invalid"))
	}
	claimed, ok, err := s.dependencies.Repository.Claim(ctx, workspaceID, jobID, owner, s.dependencies.Lease)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if claimed.WorkspaceID != workspaceID || claimed.ID != jobID {
		return resultInvalid(errors.New("export claim crossed the requested identity binding"))
	}
	if err := claimed.Validate(); err != nil {
		return resultInvalid(err)
	}
	if claimed.Status == domain.StatusExpired {
		return nil
	}
	executionBudget := s.dependencies.Lease * 3 / 4
	if executionBudget <= 0 || executionBudget >= s.dependencies.Lease {
		executionBudget = s.dependencies.Lease / 2
	}
	executionCtx, cancel := context.WithTimeout(ctx, executionBudget)
	defer cancel()
	if claimed.IsPrepared() {
		return s.completePrepared(executionCtx, claimed, owner)
	}
	if claimed.Scope.Kind == domain.ScopeWorkspaceAttachments {
		return s.executeAttachments(executionCtx, claimed, owner)
	}
	snapshot, err := s.dependencies.Snapshots.ReadCollection(executionCtx, claimed.WorkspaceID, claimed.Scope, MaxSnapshotItems)
	if err != nil {
		return s.failExecution(executionCtx, claimed, owner, err)
	}
	if err := validateSnapshotBinding(snapshot, claimed); err != nil {
		return s.failExecution(executionCtx, claimed, owner, err)
	}
	payload, extension, err := Render(snapshot, claimed)
	if err != nil {
		return s.failExecution(executionCtx, claimed, owner, err)
	}
	finalPath := exportRelativePath(claimed.ID, extension)
	preparedFile, err := s.dependencies.Files.Stage(executionCtx, workspaceID, jobID, "."+extension, payload)
	if err != nil {
		return s.failExecution(executionCtx, claimed, owner, err)
	}
	if err := validatePreparedFile(preparedFile, finalPath, int64(len(payload))); err != nil {
		return s.failExecution(executionCtx, claimed, owner, err)
	}
	prepared, err := s.dependencies.Repository.Prepare(executionCtx, PrepareRequest{
		WorkspaceID: workspaceID, JobID: jobID, LeaseOwner: owner, ExpectedVersion: claimed.Version,
		ReadModelRevision: snapshot.ReadModelRevision, ExactCount: snapshot.ExactCount, PreparedFile: preparedFile,
	})
	if err != nil {
		// Stage 已成功但尚无权威 binding；旧 owner 不得通过 Fail 改写新租约，文件由 orphan sweep 回收。
		return err
	}
	if prepared.Status == domain.StatusExpired {
		return nil
	}
	if err := validatePreparedJob(prepared, PrepareRequest{ReadModelRevision: snapshot.ReadModelRevision, ExactCount: snapshot.ExactCount, PreparedFile: preparedFile}, owner); err != nil {
		return resultInvalid(err)
	}
	return s.completePrepared(executionCtx, prepared, owner)
}

func (s *Service) executeAttachments(ctx context.Context, claimed domain.Job, owner string) error {
	if s.dependencies.Attachments == nil {
		return s.failExecution(ctx, claimed, owner, unavailable(errors.New("attachment archiver is unavailable")))
	}
	archive, err := s.dependencies.Attachments.StageAttachments(ctx, claimed.WorkspaceID, claimed.ID)
	if err != nil {
		return s.failExecution(ctx, claimed, owner, err)
	}
	if err := validateAttachmentArchive(archive, claimed.ID); err != nil {
		return s.failExecution(ctx, claimed, owner, err)
	}
	request := PrepareRequest{
		WorkspaceID: claimed.WorkspaceID, JobID: claimed.ID, LeaseOwner: owner, ExpectedVersion: claimed.Version,
		ManifestHash: archive.ManifestHash, EntryCount: archive.EntryCount,
		TotalUncompressedBytes: archive.TotalUncompressedBytes, PreparedFile: archive.PreparedFile,
	}
	prepared, err := s.dependencies.Repository.Prepare(ctx, request)
	if err != nil {
		return err
	}
	if prepared.Status == domain.StatusExpired {
		return nil
	}
	if err := validatePreparedJob(prepared, request, owner); err != nil {
		return resultInvalid(err)
	}
	return s.completePrepared(ctx, prepared, owner)
}

func (s *Service) completePrepared(ctx context.Context, job domain.Job, owner string) error {
	preparedFile := PreparedFile{StagingPath: job.PreparedStagingPath, FinalPath: job.FilePath, FileHash: job.FileHash, FileSize: job.FileSize}
	if err := s.dependencies.Files.Promote(ctx, job.WorkspaceID, preparedFile); err != nil {
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Retryable {
			// prepared binding 已成为唯一结果事实；可重试文件错误保留
			// RUNNING，等待租约到期后由下一 owner 验证并继续提升。
			return err
		}
		return s.failExecution(ctx, job, owner, err)
	}
	completed, err := s.dependencies.Repository.Complete(ctx, CompleteRequest{
		WorkspaceID: job.WorkspaceID, JobID: job.ID, LeaseOwner: owner,
		ExpectedVersion: job.Version, PreparedFile: preparedFile,
	})
	if err != nil {
		return err
	}
	if completed.Status == domain.StatusExpired {
		return nil
	}
	if completed.Status != domain.StatusSucceeded || !samePreparedFile(completed, preparedFile) {
		return resultInvalid(errors.New("export completion returned an inconsistent result binding"))
	}
	return nil
}

// Download 校验生命周期和 hash 后打开结果流；仅供兼容内部调用，生产 HTTP 必须使用 DownloadAs 传入当前主体。
func (s *Service) Download(ctx context.Context, workspaceID, jobID foundation.ID) (domain.Job, io.ReadCloser, error) {
	return s.DownloadAs(ctx, workspaceID, jobID, DownloadActor{Type: auditdomain.ActorSystem, Ref: "export-service-compat"})
}

// DownloadAs 校验固定结果，并在同一 PostgreSQL 事务中记录下载统计和当前主体 Audit。
func (s *Service) DownloadAs(ctx context.Context, workspaceID, jobID foundation.ID, actor DownloadActor) (domain.Job, io.ReadCloser, error) {
	job, err := s.Get(ctx, workspaceID, jobID)
	if err != nil {
		return domain.Job{}, nil, err
	}
	return s.downloadJobAs(ctx, job, actor)
}

// DownloadAsForScope 仅下载指定公开 scope 的任务，并在文件读取与 Audit 副作用前拒绝错 scope ID。
func (s *Service) DownloadAsForScope(ctx context.Context, workspaceID, jobID foundation.ID, scopeKind domain.ScopeKind, actor DownloadActor) (domain.Job, io.ReadCloser, error) {
	job, err := s.GetForScope(ctx, workspaceID, jobID, scopeKind)
	if err != nil {
		return domain.Job{}, nil, err
	}
	return s.downloadJobAs(ctx, job, actor)
}

func (s *Service) downloadJobAs(ctx context.Context, job domain.Job, actor DownloadActor) (domain.Job, io.ReadCloser, error) {
	if job.Status == domain.StatusExpired {
		return domain.Job{}, nil, expired(errors.New("export result has expired"))
	}
	if job.Status != domain.StatusSucceeded {
		return domain.Job{}, nil, foundation.NewError(foundation.ErrorVersionConflict, "EXPORT_RESULT_NOT_READY", true, errors.New("export result is not ready"))
	}
	content, err := s.dependencies.Files.Open(ctx, job.WorkspaceID, job.FilePath, job.FileHash, job.FileSize)
	if err != nil {
		return domain.Job{}, nil, resultInvalid(err)
	}
	if content == nil {
		return domain.Job{}, nil, resultInvalid(errors.New("export file store returned a nil result stream"))
	}
	closeWith := func(cause error) (domain.Job, io.ReadCloser, error) {
		return domain.Job{}, nil, errors.Join(cause, content.Close())
	}
	auditEventID, err := s.dependencies.IDs.New()
	if err != nil {
		return closeWith(unavailable(err))
	}
	updated, err := s.dependencies.Repository.RecordDownload(ctx, DownloadRecord{
		WorkspaceID: job.WorkspaceID, JobID: job.ID, AuditEventID: auditEventID,
		Actor: actor, ScopeKind: job.Scope.Kind, EntryCount: cloneInt64(job.EntryCount), FileHash: job.FileHash, FileSize: job.FileSize,
	})
	if err != nil {
		return closeWith(err)
	}
	if err := updated.Validate(); err != nil {
		return closeWith(resultInvalid(err))
	}
	if updated.WorkspaceID != job.WorkspaceID || updated.ID != job.ID || updated.Status != domain.StatusSucceeded ||
		updated.Version <= job.Version || updated.DownloadCount <= job.DownloadCount ||
		updated.FileHash != job.FileHash || updated.FileSize != job.FileSize {
		return closeWith(resultInvalid(errors.New("export download transaction returned an inconsistent projection")))
	}
	return updated, content, nil
}

// Recover 重新投递 pending 或租约过期的任务，供 Worker 启动和运维调用。
func (s *Service) Recover(ctx context.Context, workspaceID foundation.ID, limit int) (int, error) {
	if err := validContext(ctx); err != nil {
		return 0, err
	}
	if s.dependencies.Dispatcher == nil {
		return 0, unavailable(errors.New("export dispatcher is unavailable"))
	}
	if limit <= 0 || limit > MaxListLimit {
		return 0, invalid(errors.New("export recovery limit is invalid"))
	}
	jobs, err := s.dependencies.Repository.RecoveryCandidates(ctx, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	var lastDispatchErr error
	for _, job := range jobs {
		if workspaceID != "" && job.WorkspaceID != workspaceID {
			continue
		}
		if err := s.dependencies.Dispatcher.Dispatch(ctx, job.WorkspaceID, job.ID); err != nil {
			lastDispatchErr = err
			continue
		}
		count++
	}
	return count, lastDispatchErr
}

// OrphanSweepResult 汇总一次有界 Workspace staging 扫描，并返回后继 Workspace 游标。
type OrphanSweepResult struct {
	ScannedWorkspaces int
	Deleted           int
	NextWorkspaceID   foundation.ID
}

// SweepOrphansAll 从持久 Export Workspace 集合执行一页 orphan sweep，调用方可用 NextWorkspaceID 继续。
func (s *Service) SweepOrphansAll(ctx context.Context, afterWorkspaceID foundation.ID, workspaceLimit, fileLimit int, grace time.Duration) (OrphanSweepResult, error) {
	if err := validContext(ctx); err != nil {
		return OrphanSweepResult{}, err
	}
	if afterWorkspaceID != "" && !validID(afterWorkspaceID) || workspaceLimit < 1 || workspaceLimit > MaxListLimit || fileLimit < 1 || fileLimit > MaxListLimit {
		return OrphanSweepResult{}, invalid(errors.New("export global orphan sweep request is invalid"))
	}
	workspaces, err := s.dependencies.Repository.OrphanSweepWorkspaces(ctx, afterWorkspaceID, workspaceLimit+1)
	if err != nil {
		return OrphanSweepResult{}, err
	}
	result := OrphanSweepResult{}
	if len(workspaces) > workspaceLimit {
		result.NextWorkspaceID = workspaces[workspaceLimit-1]
		workspaces = workspaces[:workspaceLimit]
	}
	for _, workspaceID := range workspaces {
		deleted, sweepErr := s.SweepOrphans(ctx, workspaceID, grace, fileLimit)
		if sweepErr != nil {
			return result, sweepErr
		}
		result.ScannedWorkspaces++
		result.Deleted += deleted
	}
	return result, nil
}

// SweepResult 汇总一次有界过期归约和物理清理结果。
type SweepResult struct {
	Expired int
	Cleaned int
	Failed  int
}

// Sweep 使用 Repository 的数据库时间归约到期任务，并幂等清理持久 staging/final 路径。
func (s *Service) Sweep(ctx context.Context, limit int) (SweepResult, error) {
	if err := validContext(ctx); err != nil {
		return SweepResult{}, err
	}
	if limit < 1 || limit > MaxListLimit {
		return SweepResult{}, invalid(errors.New("export sweep limit is invalid"))
	}
	expiredJobs, err := s.dependencies.Repository.ExpireCandidates(ctx, limit)
	if err != nil {
		return SweepResult{}, err
	}
	result := SweepResult{Expired: len(expiredJobs)}
	candidates, err := s.dependencies.Repository.CleanupCandidates(ctx, limit)
	if err != nil {
		return result, err
	}
	var failures []error
	for _, job := range candidates {
		if job.Status != domain.StatusExpired || job.CleanupStatus == domain.CleanupSucceeded {
			return result, resultInvalid(errors.New("export repository returned an invalid cleanup candidate"))
		}
		cleanupErr := deletePreparedFiles(ctx, s.dependencies.Files, job)
		request := CleanupRequest{WorkspaceID: job.WorkspaceID, JobID: job.ID, ExpectedVersion: job.Version, Succeeded: cleanupErr == nil}
		if cleanupErr != nil {
			request.ErrorCode = cleanupErrorCode(cleanupErr)
		}
		updated, recordErr := s.dependencies.Repository.RecordCleanup(ctx, request)
		if recordErr != nil {
			failures = append(failures, recordErr)
			result.Failed++
			continue
		}
		if cleanupErr != nil {
			failures = append(failures, cleanupErr)
			result.Failed++
			continue
		}
		if updated.CleanupStatus != domain.CleanupSucceeded || updated.FileDeletedAt == nil {
			return result, resultInvalid(errors.New("export cleanup completion was not persisted"))
		}
		result.Cleaned++
	}
	return result, errors.Join(failures...)
}

// SweepOrphans 删除超过宽限期且未被任何 Job prepared binding 引用的受控 staging 文件。
func (s *Service) SweepOrphans(ctx context.Context, workspaceID foundation.ID, grace time.Duration, limit int) (int, error) {
	if err := validContext(ctx); err != nil {
		return 0, err
	}
	if !validID(workspaceID) || limit < 1 || limit > MaxListLimit {
		return 0, invalid(errors.New("export orphan sweep request is invalid"))
	}
	if grace == 0 {
		grace = DefaultOrphanGrace
	}
	if grace < time.Minute {
		return 0, invalid(errors.New("export orphan grace is too short"))
	}
	files, err := s.dependencies.Files.ListStaging(ctx, workspaceID, s.dependencies.Clock.Now().UTC().Add(-grace), limit)
	if err != nil {
		return 0, err
	}
	paths := make([]string, len(files))
	candidates := make(map[string]struct{}, len(files))
	for index, file := range files {
		paths[index] = file.Path
		candidates[file.Path] = struct{}{}
	}
	orphans, err := s.dependencies.Repository.UnreferencedStaging(ctx, workspaceID, paths)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, orphan := range orphans {
		if _, allowed := candidates[orphan]; !allowed {
			return deleted, resultInvalid(errors.New("export repository returned an unrequested orphan path"))
		}
		if err := s.dependencies.Files.DeleteOrphan(ctx, workspaceID, orphan); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (s *Service) buildJob(ctx context.Context, request domain.CreateRequest) (domain.Job, error) {
	request, ttlSeconds, err := normalizeCreateRequest(request)
	if err != nil {
		return domain.Job{}, err
	}
	if request.IncludeSensitive {
		if request.Redaction != domain.RedactionFull || s.dependencies.Authorizer == nil {
			return domain.Job{}, forbidden(errors.New("sensitive export requires explicit authorization"))
		}
		if err := s.dependencies.Authorizer.AuthorizeExport(ctx, AuthorizationRequest{WorkspaceID: request.WorkspaceID, Kind: request.Kind, PermissionScope: request.PermissionScope, IncludeSensitive: true}); err != nil {
			return domain.Job{}, err
		}
	}
	id, err := s.dependencies.IDs.New()
	if err != nil {
		return domain.Job{}, err
	}
	now := s.dependencies.Clock.Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		return domain.Job{}, invalid(errors.New("export clock returned zero time"))
	}
	requestHash := canonicalRequestHash(request, ttlSeconds)
	job := domain.Job{
		ID: id, WorkspaceID: request.WorkspaceID, Kind: request.Kind, SchemaVersion: request.SchemaVersion,
		Scope: request.Scope, Fields: append([]domain.Field{}, request.Fields...), Redaction: request.Redaction,
		IncludeSensitive: request.IncludeSensitive, PermissionScope: request.PermissionScope, RequestedBy: request.RequestedBy,
		IdempotencyKey: request.IdempotencyKey, RequestHash: requestHash, RequestTTLSeconds: ttlSeconds,
		Status: domain.StatusPending, Version: 1, ExpiresAt: now.Add(request.ExpiresIn), CreatedAt: now, UpdatedAt: now,
		CleanupStatus: domain.CleanupNotRequired,
	}
	if err := job.Validate(); err != nil {
		return domain.Job{}, invalid(err)
	}
	return job, nil
}

func (s *Service) failExecution(ctx context.Context, job domain.Job, owner string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || ctx == nil || ctx.Err() != nil {
		return cause
	}
	classified := &foundation.Error{}
	code := "EXPORT_GENERATION_FAILED"
	retryable := true
	message := "export generation failed"
	if errors.As(cause, &classified) {
		code = classified.Code
		retryable = classified.Retryable
	}
	failed, err := s.dependencies.Repository.Fail(ctx, FailRequest{
		WorkspaceID: job.WorkspaceID, JobID: job.ID, LeaseOwner: owner, ExpectedVersion: job.Version,
		ErrorCode: code, ErrorMessage: message, Retryable: retryable,
	})
	if err != nil {
		return err
	}
	if failed.Status == domain.StatusExpired {
		return nil
	}
	if retryable {
		return cause
	}
	return nil
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return invalid(errors.New("context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
func isLowerHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
func canonicalFields(values []domain.Field, kind domain.Kind) ([]domain.Field, error) {
	if len(values) == 0 {
		return append([]domain.Field(nil), domain.DefaultFields(kind)...), nil
	}
	seen := make(map[domain.Field]struct{}, len(values))
	result := make([]domain.Field, 0, len(values))
	for _, value := range values {
		if !domain.ValidField(value) {
			return nil, invalid(fmt.Errorf("unsupported export field %q", value))
		}
		if _, ok := seen[value]; ok {
			return nil, invalid(errors.New("export fields are duplicated"))
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) > 32 {
		return nil, invalid(errors.New("too many export fields"))
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result, nil
}

func exportRelativePath(id foundation.ID, extension string) string {
	return fmt.Sprintf(".knowledge/exports/%s.%s", string(id), extension)
}

// ComputeRequestHash 返回使用与 Create 相同默认值和字段规范化规则的请求摘要。
func ComputeRequestHash(request domain.CreateRequest) (string, error) {
	canonical, ttlSeconds, err := normalizeCreateRequest(request)
	if err != nil {
		return "", err
	}
	return canonicalRequestHash(canonical, ttlSeconds), nil
}

func normalizeCreateRequest(request domain.CreateRequest) (domain.CreateRequest, int64, error) {
	if !validID(request.WorkspaceID) || !domain.ValidKind(request.Kind) ||
		strings.TrimSpace(request.IdempotencyKey) == "" || request.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) ||
		len(request.IdempotencyKey) > 128 || strings.ContainsAny(request.IdempotencyKey, "\r\n\x00") {
		return domain.CreateRequest{}, 0, invalid(errors.New("export request identity or kind is invalid"))
	}
	if request.Scope.Kind == domain.ScopeWorkspaceAttachments {
		if request.SchemaVersion == "" {
			request.SchemaVersion = "attachment-export/v1"
		}
		if request.Redaction == "" {
			request.Redaction = domain.RedactionRawUserOwned
		}
	} else {
		if request.SchemaVersion == "" {
			request.SchemaVersion = SchemaVersionV1
		}
		if request.Redaction == "" {
			request.Redaction = domain.RedactionMasked
		}
	}
	if request.ExpiresIn == 0 {
		request.ExpiresIn = DefaultTTL
	}
	if request.ExpiresIn <= 0 || request.ExpiresIn > MaxTTL || request.ExpiresIn%time.Second != 0 {
		return domain.CreateRequest{}, 0, invalid(errors.New("export ttl is invalid"))
	}
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if request.RequestedBy == "" {
		request.RequestedBy = "system"
	}
	request.PermissionScope = strings.TrimSpace(request.PermissionScope)
	if request.PermissionScope == "" {
		request.PermissionScope = "READ_LOCAL"
	}
	if len(request.RequestedBy) > 256 || len(request.PermissionScope) > 128 ||
		strings.ContainsAny(request.RequestedBy, "\r\n\x00") || strings.ContainsAny(request.PermissionScope, "\r\n\x00") {
		return domain.CreateRequest{}, 0, invalid(errors.New("export actor or capability binding is invalid"))
	}
	if request.Scope.Kind == domain.ScopeCollection {
		fields, err := canonicalFields(request.Fields, request.Kind)
		if err != nil {
			return domain.CreateRequest{}, 0, err
		}
		request.Fields = fields
		if (request.Kind != domain.KindMarkdown && request.Kind != domain.KindMetadataJSON) || request.SchemaVersion != SchemaVersionV1 ||
			request.Scope.CollectionID == nil || request.Scope.CollectionVersion == nil || !validID(*request.Scope.CollectionID) ||
			*request.Scope.CollectionVersion < 1 || !isLowerHex(request.Scope.QueryHash) || request.Scope.AttachmentRootContractVersion != "" ||
			(request.Redaction != domain.RedactionMasked && request.Redaction != domain.RedactionFull) || (request.Redaction == domain.RedactionFull) != request.IncludeSensitive {
			return domain.CreateRequest{}, 0, invalid(errors.New("export Collection binding is invalid"))
		}
	} else if request.Scope.Kind == domain.ScopeWorkspaceAttachments {
		if request.Kind != domain.KindAttachmentsZIP || request.SchemaVersion != "attachment-export/v1" || request.Scope.CollectionID != nil ||
			request.Scope.CollectionVersion != nil || request.Scope.QueryHash != "" || request.Scope.AttachmentRootContractVersion != "workspace-attachments/v1" ||
			len(request.Fields) != 0 || request.Redaction != domain.RedactionRawUserOwned || request.IncludeSensitive {
			return domain.CreateRequest{}, 0, invalid(errors.New("export attachment binding is invalid"))
		}
		request.Fields = []domain.Field{}
	} else {
		return domain.CreateRequest{}, 0, invalid(errors.New("export scope kind is invalid"))
	}
	return request, int64(request.ExpiresIn / time.Second), nil
}

func canonicalRequestHash(request domain.CreateRequest, ttlSeconds int64) string {
	if request.Scope.Kind == domain.ScopeCollection {
		canonical, _ := json.Marshal(struct {
			WorkspaceID       foundation.ID          `json:"workspace_id"`
			CollectionID      string                 `json:"collection_id"`
			CollectionVersion int64                  `json:"collection_version"`
			QueryHash         string                 `json:"query_hash"`
			Kind              domain.Kind            `json:"kind"`
			SchemaVersion     string                 `json:"schema_version"`
			Fields            []domain.Field         `json:"fields"`
			Redaction         domain.RedactionPolicy `json:"redaction"`
			IncludeSensitive  bool                   `json:"include_sensitive"`
			RequestedBy       string                 `json:"requested_by"`
			PermissionScope   string                 `json:"permission_scope"`
			TTLSeconds        int64                  `json:"ttl_seconds"`
		}{
			request.WorkspaceID, string(*request.Scope.CollectionID), *request.Scope.CollectionVersion, request.Scope.QueryHash,
			request.Kind, request.SchemaVersion, request.Fields, request.Redaction, request.IncludeSensitive,
			request.RequestedBy, request.PermissionScope, ttlSeconds,
		})
		digest := sha256.Sum256(canonical)
		return hex.EncodeToString(digest[:])
	}
	canonical, _ := json.Marshal(struct {
		WorkspaceID                    foundation.ID          `json:"workspace_id"`
		ScopeKind                      domain.ScopeKind       `json:"scope_kind"`
		AttachmentRootContractVersion  string                 `json:"attachment_root_contract_version"`
		AttachmentLimitContractVersion string                 `json:"attachment_limit_contract_version"`
		Kind                           domain.Kind            `json:"kind"`
		SchemaVersion                  string                 `json:"schema_version"`
		Fields                         []domain.Field         `json:"fields"`
		Redaction                      domain.RedactionPolicy `json:"redaction"`
		IncludeSensitive               bool                   `json:"include_sensitive"`
		RequestedBy                    string                 `json:"requested_by"`
		PermissionScope                string                 `json:"permission_scope"`
		TTLSeconds                     int64                  `json:"ttl_seconds"`
	}{
		request.WorkspaceID, request.Scope.Kind, request.Scope.AttachmentRootContractVersion, attachmentLimitContractVersion, request.Kind,
		request.SchemaVersion, request.Fields, request.Redaction, request.IncludeSensitive,
		request.RequestedBy, request.PermissionScope, ttlSeconds,
	})
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func validateSnapshotBinding(snapshot CollectionSnapshot, job domain.Job) error {
	if snapshot.WorkspaceID != job.WorkspaceID || !sameOptionalID(snapshot.CollectionID, job.Scope.CollectionID) ||
		!sameOptionalInt64(snapshot.CollectionVersion, job.Scope.CollectionVersion) || snapshot.QueryHash != job.Scope.QueryHash ||
		!isLowerHex(snapshot.ReadModelRevision) || snapshot.ExactCount < 0 || snapshot.ExactCount > MaxSnapshotItems ||
		snapshot.ExactCount != int64(len(snapshot.Items)) {
		return resultInvalid(errors.New("export snapshot binding changed"))
	}
	return nil
}

func validatePreparedFile(file PreparedFile, finalPath string, payloadSize int64) error {
	if strings.TrimSpace(file.StagingPath) == "" || file.FinalPath != finalPath || file.StagingPath == file.FinalPath ||
		!isLowerHex(file.FileHash) || file.FileSize != payloadSize {
		return resultInvalid(errors.New("export file store returned an inconsistent prepared binding"))
	}
	return nil
}

func validatePreparedJob(job domain.Job, request PrepareRequest, owner string) error {
	if err := job.Validate(); err != nil {
		return err
	}
	if job.Status != domain.StatusRunning || job.LeaseOwner != owner || !job.IsPrepared() ||
		!samePreparedSummary(job, request) || !samePreparedFile(job, request.PreparedFile) {
		return errors.New("export repository returned an inconsistent prepared result")
	}
	return nil
}

func samePreparedSummary(job domain.Job, request PrepareRequest) bool {
	if job.Scope.Kind == domain.ScopeCollection {
		return job.ReadModelRevision == request.ReadModelRevision && job.ExactCount != nil && *job.ExactCount == request.ExactCount
	}
	return job.ManifestHash == request.ManifestHash && job.EntryCount != nil && *job.EntryCount == request.EntryCount &&
		job.TotalUncompressedBytes != nil && *job.TotalUncompressedBytes == request.TotalUncompressedBytes
}

func validateAttachmentArchive(archive AttachmentArchive, exportID foundation.ID) error {
	if !isLowerHex(archive.ManifestHash) || archive.EntryCount < 0 || archive.EntryCount > 10_000 ||
		archive.TotalUncompressedBytes < 0 || archive.TotalUncompressedBytes > 1<<30 ||
		archive.PreparedFile.FinalPath != exportRelativePath(exportID, "zip") || !isLowerHex(archive.PreparedFile.FileHash) ||
		archive.PreparedFile.FileSize < 0 || archive.PreparedFile.FileSize > 1<<30 {
		return resultInvalid(errors.New("attachment archiver returned an inconsistent binding"))
	}
	return nil
}

func samePreparedFile(job domain.Job, file PreparedFile) bool {
	return job.PreparedStagingPath == file.StagingPath && job.FilePath == file.FinalPath &&
		job.FileHash == file.FileHash && job.FileSize == file.FileSize
}

func sameOptionalID(left, right *foundation.ID) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func deletePreparedFiles(ctx context.Context, files FileStore, job domain.Job) error {
	var failures []error
	if job.PreparedStagingPath != "" {
		if err := files.DeletePrepared(ctx, job.WorkspaceID, job.PreparedStagingPath, job.FileHash, job.FileSize); err != nil {
			failures = append(failures, err)
		}
	}
	if job.FilePath != "" {
		if err := files.DeletePrepared(ctx, job.WorkspaceID, job.FilePath, job.FileHash, job.FileSize); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func cleanupErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) && strings.TrimSpace(classified.Code) != "" {
		return classified.Code
	}
	return "EXPORT_FILE_CLEANUP_FAILED"
}
