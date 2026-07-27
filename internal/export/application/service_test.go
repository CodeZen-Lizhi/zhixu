package application

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestServiceCreateExactReplayDoesNotReadMutableDependencies(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	request := serviceCreateRequest()
	requestHash, err := ComputeRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	existing := servicePendingJob(now)
	existing.ID = serviceID(9)
	existing.RequestHash = requestHash
	workspaceReads := 0
	snapshotReads := 0
	repository := &serviceRepositoryStub{
		create: func(_ context.Context, candidate domain.Job) (domain.Job, bool, error) {
			if !domain.SameRequest(candidate, existing) {
				t.Fatalf("candidate does not match persisted replay: candidate=%#v existing=%#v", candidate, existing)
			}
			return existing, true, nil
		},
	}
	service := newServiceForTest(t, repository,
		&serviceSnapshotStub{reads: &snapshotReads},
		&serviceWorkspaceStub{reads: &workspaceReads},
		&serviceFileStub{}, now,
	)
	result, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.Job.ID != existing.ID {
		t.Fatalf("replay result = %#v", result)
	}
	if workspaceReads != 0 || snapshotReads != 0 {
		t.Fatalf("exact replay read mutable dependencies: workspace=%d snapshot=%d", workspaceReads, snapshotReads)
	}
}

func TestServiceExecutePreparedReplaySkipsSnapshotAndCompletes(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	prepared := servicePreparedRunningJob(now)
	snapshotReads := 0
	promotes := 0
	completes := 0
	repository := &serviceRepositoryStub{
		claim: func(context.Context, foundation.ID, foundation.ID, string, time.Duration) (domain.Job, bool, error) {
			return prepared, true, nil
		},
		complete: func(_ context.Context, request CompleteRequest) (domain.Job, error) {
			completes++
			if request.ExpectedVersion != prepared.Version || !samePreparedFile(prepared, request.PreparedFile) {
				t.Fatalf("completion request = %#v", request)
			}
			completed := prepared
			completed.Status = domain.StatusSucceeded
			completed.Version++
			completed.UpdatedAt = prepared.UpdatedAt.Add(time.Second)
			completed.CompletedAt = cloneServiceTime(completed.UpdatedAt)
			completed.LeaseOwner = ""
			completed.LeaseExpiresAt = nil
			return completed, nil
		},
	}
	files := &serviceFileStub{promote: func(_ context.Context, _ foundation.ID, file PreparedFile) error {
		promotes++
		if !samePreparedFile(prepared, file) {
			t.Fatalf("promoted file = %#v", file)
		}
		return nil
	}}
	service := newServiceForTest(t, repository, &serviceSnapshotStub{reads: &snapshotReads}, &serviceWorkspaceStub{}, files, now)
	if err := service.Execute(context.Background(), prepared.WorkspaceID, prepared.ID, prepared.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if snapshotReads != 0 || promotes != 1 || completes != 1 {
		t.Fatalf("snapshot=%d promote=%d complete=%d", snapshotReads, promotes, completes)
	}
}

func TestServiceExecuteFirstRunStagesPreparesPromotesAndCompletes(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	claimed := servicePendingJob(now)
	startedAt := now.Add(time.Second)
	leaseExpiresAt := now.Add(10 * time.Minute)
	claimed.Status = domain.StatusRunning
	claimed.Version = 2
	claimed.AttemptCount = 1
	claimed.UpdatedAt = startedAt
	claimed.StartedAt = &startedAt
	claimed.LeaseOwner = "worker:first-run"
	claimed.LeaseExpiresAt = &leaseExpiresAt
	snapshot := CollectionSnapshot{
		WorkspaceID: claimed.WorkspaceID, CollectionID: claimed.Scope.CollectionID,
		CollectionVersion: claimed.Scope.CollectionVersion, QueryHash: claimed.Scope.QueryHash,
		ReadModelRevision: strings.Repeat("b", 64), ExactCount: 0, Name: "First run export",
	}
	steps := make([]string, 0, 6)
	var preparedFile PreparedFile

	repository := &serviceRepositoryStub{
		claim: func(_ context.Context, workspaceID, jobID foundation.ID, owner string, lease time.Duration) (domain.Job, bool, error) {
			steps = append(steps, "claim")
			if workspaceID != claimed.WorkspaceID || jobID != claimed.ID || owner != claimed.LeaseOwner || lease != 4*time.Minute {
				t.Fatalf("claim binding workspace=%s job=%s owner=%q lease=%s", workspaceID, jobID, owner, lease)
			}
			return claimed, true, nil
		},
		prepare: func(_ context.Context, request PrepareRequest) (domain.Job, error) {
			steps = append(steps, "prepare")
			if request.WorkspaceID != claimed.WorkspaceID || request.JobID != claimed.ID ||
				request.LeaseOwner != claimed.LeaseOwner || request.ExpectedVersion != claimed.Version ||
				request.ReadModelRevision != snapshot.ReadModelRevision || request.ExactCount != snapshot.ExactCount ||
				request.PreparedFile != preparedFile {
				t.Fatalf("prepare request = %#v", request)
			}
			prepared := claimed
			preparedAt := claimed.UpdatedAt.Add(time.Second)
			exactCount := snapshot.ExactCount
			prepared.Version++
			prepared.UpdatedAt = preparedAt
			prepared.PreparedAt = &preparedAt
			prepared.ReadModelRevision = snapshot.ReadModelRevision
			prepared.ExactCount = &exactCount
			prepared.PreparedStagingPath = preparedFile.StagingPath
			prepared.FilePath = preparedFile.FinalPath
			prepared.FileHash = preparedFile.FileHash
			prepared.FileSize = preparedFile.FileSize
			return prepared, nil
		},
		complete: func(_ context.Context, request CompleteRequest) (domain.Job, error) {
			steps = append(steps, "complete")
			if request.WorkspaceID != claimed.WorkspaceID || request.JobID != claimed.ID ||
				request.LeaseOwner != claimed.LeaseOwner || request.ExpectedVersion != 3 || request.PreparedFile != preparedFile {
				t.Fatalf("complete request = %#v", request)
			}
			completed := claimed
			preparedAt := claimed.UpdatedAt.Add(time.Second)
			completedAt := preparedAt.Add(time.Second)
			exactCount := snapshot.ExactCount
			completed.Status = domain.StatusSucceeded
			completed.Version = 4
			completed.UpdatedAt = completedAt
			completed.CompletedAt = &completedAt
			completed.LeaseOwner = ""
			completed.LeaseExpiresAt = nil
			completed.PreparedAt = &preparedAt
			completed.ReadModelRevision = snapshot.ReadModelRevision
			completed.ExactCount = &exactCount
			completed.PreparedStagingPath = preparedFile.StagingPath
			completed.FilePath = preparedFile.FinalPath
			completed.FileHash = preparedFile.FileHash
			completed.FileSize = preparedFile.FileSize
			return completed, nil
		},
	}
	snapshots := &serviceSnapshotStub{readCollection: func(_ context.Context, workspaceID foundation.ID, scope domain.Scope, limit int) (CollectionSnapshot, error) {
		steps = append(steps, "snapshot")
		if workspaceID != claimed.WorkspaceID || !sameOptionalID(scope.CollectionID, claimed.Scope.CollectionID) ||
			!sameOptionalInt64(scope.CollectionVersion, claimed.Scope.CollectionVersion) || scope.QueryHash != claimed.Scope.QueryHash ||
			limit != MaxSnapshotItems {
			t.Fatalf("snapshot binding workspace=%s scope=%#v limit=%d", workspaceID, scope, limit)
		}
		return snapshot, nil
	}}
	files := &serviceFileStub{
		stage: func(_ context.Context, workspaceID, jobID foundation.ID, extension string, payload []byte) (PreparedFile, error) {
			steps = append(steps, "stage")
			if workspaceID != claimed.WorkspaceID || jobID != claimed.ID || extension != ".md" || len(payload) == 0 {
				t.Fatalf("stage binding workspace=%s job=%s extension=%q bytes=%d", workspaceID, jobID, extension, len(payload))
			}
			preparedFile = PreparedFile{
				StagingPath: ".knowledge/exports/.staging/" + string(claimed.ID) + "-" + strings.Repeat("d", 32) + ".md.stage",
				FinalPath:   ".knowledge/exports/" + string(claimed.ID) + ".md",
				FileHash:    strings.Repeat("c", 64), FileSize: int64(len(payload)),
			}
			return preparedFile, nil
		},
		promote: func(_ context.Context, workspaceID foundation.ID, file PreparedFile) error {
			steps = append(steps, "promote")
			if workspaceID != claimed.WorkspaceID || file != preparedFile {
				t.Fatalf("promote binding workspace=%s file=%#v", workspaceID, file)
			}
			return nil
		},
	}
	service := newServiceForTest(t, repository, snapshots, &serviceWorkspaceStub{}, files, now)
	if err := service.Execute(context.Background(), claimed.WorkspaceID, claimed.ID, claimed.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(steps, ","), "claim,snapshot,stage,prepare,promote,complete"; got != want {
		t.Fatalf("first execution steps=%q, want %q", got, want)
	}
}

func TestServiceExecuteRetryablePreparedPromoteRetainsRunningBinding(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	prepared := servicePreparedRunningJob(now)
	snapshotReads := 0
	fails := 0
	completes := 0
	retryable := foundation.NewError(foundation.ErrorRetryableFailure, "EXPORT_FILE_IO_FAILED", true, errors.New("temporary fs failure"))
	repository := &serviceRepositoryStub{
		claim: func(context.Context, foundation.ID, foundation.ID, string, time.Duration) (domain.Job, bool, error) {
			return prepared, true, nil
		},
		complete: func(context.Context, CompleteRequest) (domain.Job, error) {
			completes++
			return domain.Job{}, errors.New("unexpected completion")
		},
		fail: func(context.Context, FailRequest) (domain.Job, error) {
			fails++
			return domain.Job{}, errors.New("unexpected fail")
		},
	}
	files := &serviceFileStub{promote: func(context.Context, foundation.ID, PreparedFile) error { return retryable }}
	service := newServiceForTest(t, repository, &serviceSnapshotStub{reads: &snapshotReads}, &serviceWorkspaceStub{}, files, now)
	err := service.Execute(context.Background(), prepared.WorkspaceID, prepared.ID, prepared.LeaseOwner)
	if !errors.Is(err, retryable) {
		t.Fatalf("execute error = %v", err)
	}
	if snapshotReads != 0 || fails != 0 || completes != 0 {
		t.Fatalf("prepared retry read or rewrote facts: snapshot=%d fail=%d complete=%d", snapshotReads, fails, completes)
	}
}

func TestServiceExecuteNonRetryablePreparedPromoteRecordsFailedBinding(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	prepared := servicePreparedRunningJob(now)
	fails := 0
	nonRetryable := foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInvalid, false, errors.New("hash mismatch"))
	repository := &serviceRepositoryStub{
		claim: func(context.Context, foundation.ID, foundation.ID, string, time.Duration) (domain.Job, bool, error) {
			return prepared, true, nil
		},
		fail: func(_ context.Context, request FailRequest) (domain.Job, error) {
			fails++
			if request.Retryable || request.ExpectedVersion != prepared.Version {
				t.Fatalf("failure request = %#v", request)
			}
			failed := prepared
			failed.Status = domain.StatusFailed
			failed.Version++
			failed.UpdatedAt = prepared.UpdatedAt.Add(time.Second)
			failed.CompletedAt = cloneServiceTime(failed.UpdatedAt)
			failed.LeaseOwner = ""
			failed.LeaseExpiresAt = nil
			failed.ErrorCode = request.ErrorCode
			failed.ErrorMessage = request.ErrorMessage
			return failed, nil
		},
	}
	files := &serviceFileStub{promote: func(context.Context, foundation.ID, PreparedFile) error { return nonRetryable }}
	service := newServiceForTest(t, repository, &serviceSnapshotStub{}, &serviceWorkspaceStub{}, files, now)
	if err := service.Execute(context.Background(), prepared.WorkspaceID, prepared.ID, prepared.LeaseOwner); err != nil {
		t.Fatalf("non-retryable failure should be durably terminal: %v", err)
	}
	if fails != 1 {
		t.Fatalf("fail calls = %d", fails)
	}
}

func TestServiceSweepDeletesPreparedPathsAndRecordsCleanup(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	expiredJob := servicePreparedRunningJob(now)
	expiredJob.Status = domain.StatusExpired
	expiredJob.Version++
	expiredJob.UpdatedAt = expiredJob.UpdatedAt.Add(time.Second)
	expiredJob.CompletedAt = cloneServiceTime(expiredJob.UpdatedAt)
	expiredJob.LeaseOwner = ""
	expiredJob.LeaseExpiresAt = nil
	expiredJob.CleanupStatus = domain.CleanupPending
	deleted := make([]string, 0, 2)
	repository := &serviceRepositoryStub{
		expireCandidates:  func(context.Context, int) ([]domain.Job, error) { return []domain.Job{expiredJob}, nil },
		cleanupCandidates: func(context.Context, int) ([]domain.Job, error) { return []domain.Job{expiredJob}, nil },
		recordCleanup: func(_ context.Context, request CleanupRequest) (domain.Job, error) {
			if !request.Succeeded || request.ExpectedVersion != expiredJob.Version {
				t.Fatalf("cleanup request = %#v", request)
			}
			cleaned := expiredJob
			cleaned.Version++
			cleaned.UpdatedAt = expiredJob.UpdatedAt.Add(time.Second)
			cleaned.CleanupStatus = domain.CleanupSucceeded
			cleaned.CleanupAttemptCount = 1
			cleaned.CleanupUpdatedAt = cloneServiceTime(cleaned.UpdatedAt)
			cleaned.FileDeletedAt = cloneServiceTime(cleaned.UpdatedAt)
			return cleaned, nil
		},
	}
	files := &serviceFileStub{delete: func(_ context.Context, _ foundation.ID, path string) error {
		deleted = append(deleted, path)
		return nil
	}}
	service := newServiceForTest(t, repository, &serviceSnapshotStub{}, &serviceWorkspaceStub{}, files, now)
	result, err := service.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != 1 || result.Cleaned != 1 || result.Failed != 0 || len(deleted) != 2 ||
		deleted[0] != expiredJob.PreparedStagingPath || deleted[1] != expiredJob.FilePath {
		t.Fatalf("sweep=%#v deleted=%v", result, deleted)
	}
}

func TestServiceSweepRetriesFailedCleanup(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	current := servicePreparedRunningJob(now)
	current.Status = domain.StatusExpired
	current.Version++
	current.UpdatedAt = current.UpdatedAt.Add(time.Second)
	current.CompletedAt = cloneServiceTime(current.UpdatedAt)
	current.LeaseOwner = ""
	current.LeaseExpiresAt = nil
	current.CleanupStatus = domain.CleanupPending

	const cleanupErrorCode = "EXPORT_STAGING_DELETE_FAILED"
	cleanupRequests := make([]CleanupRequest, 0, 2)
	repository := &serviceRepositoryStub{
		expireCandidates: func(context.Context, int) ([]domain.Job, error) { return nil, nil },
		cleanupCandidates: func(context.Context, int) ([]domain.Job, error) {
			if current.CleanupStatus == domain.CleanupSucceeded {
				return nil, nil
			}
			return []domain.Job{current}, nil
		},
		recordCleanup: func(_ context.Context, request CleanupRequest) (domain.Job, error) {
			cleanupRequests = append(cleanupRequests, request)
			if request.ExpectedVersion != current.Version {
				t.Fatalf("cleanup expected version=%d, want %d", request.ExpectedVersion, current.Version)
			}
			updated := current
			updated.Version++
			updated.UpdatedAt = current.UpdatedAt.Add(time.Second)
			updated.CleanupAttemptCount++
			updated.CleanupUpdatedAt = cloneServiceTime(updated.UpdatedAt)
			if request.Succeeded {
				if request.ErrorCode != "" {
					t.Fatalf("successful cleanup error code=%q", request.ErrorCode)
				}
				updated.CleanupStatus = domain.CleanupSucceeded
				updated.CleanupError = ""
				updated.FileDeletedAt = cloneServiceTime(updated.UpdatedAt)
			} else {
				if request.ErrorCode != cleanupErrorCode {
					t.Fatalf("failed cleanup error code=%q", request.ErrorCode)
				}
				updated.CleanupStatus = domain.CleanupFailed
				updated.CleanupError = request.ErrorCode
				updated.FileDeletedAt = nil
			}
			if err := updated.Validate(); err != nil {
				t.Fatalf("cleanup update is invalid: %v", err)
			}
			current = updated
			return updated, nil
		},
	}
	deleteCalls := make([]string, 0, 4)
	failStagingDelete := true
	files := &serviceFileStub{delete: func(_ context.Context, _ foundation.ID, path string) error {
		deleteCalls = append(deleteCalls, path)
		if path == current.PreparedStagingPath && failStagingDelete {
			failStagingDelete = false
			return foundation.NewError(foundation.ErrorDependencyUnavailable, cleanupErrorCode, true, errors.New("injected staging delete failure"))
		}
		return nil
	}}
	service := newServiceForTest(t, repository, &serviceSnapshotStub{}, &serviceWorkspaceStub{}, files, now)

	first, err := service.Sweep(context.Background(), 10)
	if err == nil {
		t.Fatal("cleanup delete failure was not returned")
	}
	if first.Expired != 0 || first.Cleaned != 0 || first.Failed != 1 || current.CleanupStatus != domain.CleanupFailed ||
		current.CleanupAttemptCount != 1 || current.CleanupError != cleanupErrorCode || current.FileDeletedAt != nil {
		t.Fatalf("first sweep=%#v job=%#v", first, current)
	}

	second, err := service.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if second.Expired != 0 || second.Cleaned != 1 || second.Failed != 0 || current.CleanupStatus != domain.CleanupSucceeded ||
		current.CleanupAttemptCount != 2 || current.CleanupError != "" || current.FileDeletedAt == nil {
		t.Fatalf("second sweep=%#v job=%#v", second, current)
	}
	if len(cleanupRequests) != 2 || cleanupRequests[0].Succeeded || !cleanupRequests[1].Succeeded || len(deleteCalls) != 4 {
		t.Fatalf("cleanup requests=%#v delete calls=%v", cleanupRequests, deleteCalls)
	}
}

func TestServiceDownloadAsPassesCurrentActorToAtomicRecord(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	job := servicePreparedRunningJob(now)
	job.Status = domain.StatusSucceeded
	job.Version++
	job.UpdatedAt = job.UpdatedAt.Add(time.Second)
	job.CompletedAt = cloneServiceTime(job.UpdatedAt)
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	payload := []byte("export payload")
	job.FileSize = int64(len(payload))
	actor := DownloadActor{Type: auditdomain.ActorUser, Ref: string(serviceID(88))}
	repository := &serviceRepositoryStub{
		get: func(context.Context, foundation.ID, foundation.ID) (domain.Job, error) { return job, nil },
		recordDownload: func(_ context.Context, request DownloadRecord) (domain.Job, error) {
			if request.Actor != actor || request.FileHash != job.FileHash || request.FileSize != job.FileSize {
				t.Fatalf("download record = %#v", request)
			}
			updated := job
			updated.Version++
			updated.DownloadCount++
			updated.UpdatedAt = job.UpdatedAt.Add(time.Second)
			updated.LastDownloadedAt = cloneServiceTime(updated.UpdatedAt)
			return updated, nil
		},
	}
	files := &serviceFileStub{read: func(context.Context, foundation.ID, string, string, int64) ([]byte, error) {
		return payload, nil
	}}
	service := newServiceForTest(t, repository, &serviceSnapshotStub{}, &serviceWorkspaceStub{}, files, now)
	updated, content, err := service.DownloadAs(context.Background(), job.WorkspaceID, job.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DownloadCount != 1 || string(content) != string(payload) {
		t.Fatalf("updated=%#v content=%q", updated, content)
	}
}

func TestServiceDownloadAsAllowsConcurrentMonotonicRecords(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	job := servicePreparedRunningJob(now)
	completedAt := job.UpdatedAt.Add(time.Second)
	job.Status = domain.StatusSucceeded
	job.Version++
	job.UpdatedAt = completedAt
	job.CompletedAt = &completedAt
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	payload := []byte("export payload")
	job.FileSize = int64(len(payload))

	var getMu sync.Mutex
	getCalls := 0
	bothGets := make(chan struct{})
	var recordMu sync.Mutex
	durable := job
	auditIDs := make(map[foundation.ID]struct{}, 2)
	repository := &serviceRepositoryStub{
		get: func(context.Context, foundation.ID, foundation.ID) (domain.Job, error) {
			getMu.Lock()
			getCalls++
			if getCalls == 2 {
				close(bothGets)
			}
			getMu.Unlock()
			<-bothGets
			return job, nil
		},
		recordDownload: func(_ context.Context, request DownloadRecord) (domain.Job, error) {
			recordMu.Lock()
			defer recordMu.Unlock()
			if request.FileHash != job.FileHash || request.FileSize != job.FileSize {
				return domain.Job{}, errors.New("concurrent download result binding changed")
			}
			if _, exists := auditIDs[request.AuditEventID]; exists {
				return domain.Job{}, errors.New("concurrent download reused an Audit ID")
			}
			auditIDs[request.AuditEventID] = struct{}{}
			durable.Version++
			durable.DownloadCount++
			durable.UpdatedAt = durable.UpdatedAt.Add(time.Microsecond)
			durable.LastDownloadedAt = cloneServiceTime(durable.UpdatedAt)
			return durable, nil
		},
	}
	files := &serviceFileStub{read: func(context.Context, foundation.ID, string, string, int64) ([]byte, error) {
		return append([]byte(nil), payload...), nil
	}}
	service, err := NewService(Dependencies{
		Repository: repository, Snapshots: &serviceSnapshotStub{}, Workspaces: &serviceWorkspaceStub{}, Files: files,
		IDs: &serviceConcurrentIDGenerator{next: 100}, Clock: foundation.FixedClock{Value: now}, Lease: 4 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	type downloadResult struct {
		job     domain.Job
		content []byte
		err     error
	}
	start := make(chan struct{})
	results := make(chan downloadResult, 2)
	for index := 0; index < 2; index++ {
		actor := DownloadActor{Type: auditdomain.ActorUser, Ref: string(serviceID(80 + index))}
		go func() {
			<-start
			updated, content, downloadErr := service.DownloadAs(context.Background(), job.WorkspaceID, job.ID, actor)
			results <- downloadResult{job: updated, content: content, err: downloadErr}
		}()
	}
	close(start)
	seenCounts := make(map[int]struct{}, 2)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent DownloadAs error: %v", result.err)
		}
		if string(result.content) != string(payload) {
			t.Fatalf("concurrent DownloadAs content=%q", result.content)
		}
		seenCounts[result.job.DownloadCount] = struct{}{}
	}
	if _, first := seenCounts[1]; !first {
		t.Fatalf("concurrent DownloadAs missed first count: %v", seenCounts)
	}
	if _, second := seenCounts[2]; !second {
		t.Fatalf("concurrent DownloadAs missed second count: %v", seenCounts)
	}
	recordMu.Lock()
	finalCount := durable.DownloadCount
	auditCount := len(auditIDs)
	recordMu.Unlock()
	if finalCount != 2 || auditCount != 2 {
		t.Fatalf("concurrent DownloadAs durable count=%d Audit=%d", finalCount, auditCount)
	}
}

func newServiceForTest(t *testing.T, repository Repository, snapshots SnapshotReader, workspaces WorkspaceReader, files FileStore, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Dependencies{
		Repository: repository, Snapshots: snapshots, Workspaces: workspaces, Files: files,
		IDs: serviceIDGenerator{}, Clock: foundation.FixedClock{Value: now}, Lease: 4 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func serviceCreateRequest() domain.CreateRequest {
	collectionID := serviceID(2)
	version := int64(3)
	return domain.CreateRequest{
		WorkspaceID: serviceID(1), Kind: domain.KindMarkdown,
		Scope:  domain.Scope{CollectionID: &collectionID, CollectionVersion: &version, QueryHash: strings.Repeat("a", 64)},
		Fields: []domain.Field{domain.FieldTitle, domain.FieldID}, Redaction: domain.RedactionMasked,
		IdempotencyKey: "export-service-test", RequestedBy: "USER:test", PermissionScope: "READ_LOCAL",
	}
}

func servicePendingJob(now time.Time) domain.Job {
	request := serviceCreateRequest()
	request.Fields = []domain.Field{domain.FieldID, domain.FieldTitle}
	return domain.Job{
		ID: serviceID(3), WorkspaceID: request.WorkspaceID, Kind: request.Kind, SchemaVersion: SchemaVersionV1,
		Scope: request.Scope, Fields: request.Fields, Redaction: request.Redaction,
		PermissionScope: request.PermissionScope, RequestedBy: request.RequestedBy,
		IdempotencyKey: request.IdempotencyKey, RequestHash: strings.Repeat("9", 64), RequestTTLSeconds: int64(DefaultTTL / time.Second),
		Status: domain.StatusPending, Version: 1, ExpiresAt: now.Add(DefaultTTL), CreatedAt: now, UpdatedAt: now,
		CleanupStatus: domain.CleanupNotRequired,
	}
}

func servicePreparedRunningJob(now time.Time) domain.Job {
	job := servicePendingJob(now)
	startedAt := now.Add(time.Second)
	preparedAt := startedAt.Add(time.Second)
	leaseExpiresAt := now.Add(10 * time.Minute)
	exactCount := int64(2)
	job.Status = domain.StatusRunning
	job.Version = 3
	job.AttemptCount = 1
	job.StartedAt = &startedAt
	job.UpdatedAt = preparedAt
	job.LeaseOwner = "worker:test"
	job.LeaseExpiresAt = &leaseExpiresAt
	job.ReadModelRevision = strings.Repeat("b", 64)
	job.ExactCount = &exactCount
	job.PreparedAt = &preparedAt
	job.PreparedStagingPath = ".knowledge/exports/.staging/10000000-0000-4000-8000-000000000003-00000000000000000000000000000000.md.stage"
	job.FilePath = ".knowledge/exports/10000000-0000-4000-8000-000000000003.md"
	job.FileHash = strings.Repeat("c", 64)
	job.FileSize = 14
	return job
}

func cloneServiceTime(value time.Time) *time.Time {
	clone := value
	return &clone
}

func serviceID(suffix int) foundation.ID {
	return foundation.ID("10000000-0000-4000-8000-" + strings.Repeat("0", 12-len(strconv.Itoa(suffix))) + strconv.Itoa(suffix))
}

type serviceIDGenerator struct{}

func (serviceIDGenerator) New() (foundation.ID, error) { return serviceID(99), nil }

type serviceConcurrentIDGenerator struct {
	mu   sync.Mutex
	next int
}

func (generator *serviceConcurrentIDGenerator) New() (foundation.ID, error) {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	generator.next++
	return serviceID(generator.next), nil
}

type serviceWorkspaceStub struct{ reads *int }

func (stub *serviceWorkspaceStub) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	if stub.reads != nil {
		*stub.reads++
	}
	return workspacedomain.Workspace{}, errors.New("unexpected workspace read")
}

type serviceSnapshotStub struct {
	reads          *int
	readCollection func(context.Context, foundation.ID, domain.Scope, int) (CollectionSnapshot, error)
}

func (stub *serviceSnapshotStub) ReadCollection(ctx context.Context, workspaceID foundation.ID, scope domain.Scope, limit int) (CollectionSnapshot, error) {
	if stub.reads != nil {
		*stub.reads++
	}
	if stub.readCollection != nil {
		return stub.readCollection(ctx, workspaceID, scope, limit)
	}
	return CollectionSnapshot{}, errors.New("unexpected snapshot read")
}

type serviceFileStub struct {
	stage   func(context.Context, foundation.ID, foundation.ID, string, []byte) (PreparedFile, error)
	promote func(context.Context, foundation.ID, PreparedFile) error
	read    func(context.Context, foundation.ID, string, string, int64) ([]byte, error)
	delete  func(context.Context, foundation.ID, string) error
}

func (stub *serviceFileStub) Stage(ctx context.Context, workspaceID, jobID foundation.ID, extension string, payload []byte) (PreparedFile, error) {
	if stub.stage == nil {
		return PreparedFile{}, errors.New("unexpected stage")
	}
	return stub.stage(ctx, workspaceID, jobID, extension, payload)
}
func (stub *serviceFileStub) Promote(ctx context.Context, workspaceID foundation.ID, file PreparedFile) error {
	if stub.promote == nil {
		return errors.New("unexpected promote")
	}
	return stub.promote(ctx, workspaceID, file)
}
func (stub *serviceFileStub) Read(ctx context.Context, workspaceID foundation.ID, path, hash string, size int64) ([]byte, error) {
	if stub.read == nil {
		return nil, errors.New("unexpected read")
	}
	return stub.read(ctx, workspaceID, path, hash, size)
}
func (stub *serviceFileStub) DeletePrepared(ctx context.Context, workspaceID foundation.ID, path, _ string, _ int64) error {
	if stub.delete == nil {
		return errors.New("unexpected delete")
	}
	return stub.delete(ctx, workspaceID, path)
}
func (stub *serviceFileStub) DeleteOrphan(ctx context.Context, workspaceID foundation.ID, path string) error {
	if stub.delete == nil {
		return errors.New("unexpected delete")
	}
	return stub.delete(ctx, workspaceID, path)
}
func (*serviceFileStub) ListStaging(context.Context, foundation.ID, time.Time, int) ([]StagingFile, error) {
	return nil, errors.New("unexpected staging list")
}

type serviceRepositoryStub struct {
	create            func(context.Context, domain.Job) (domain.Job, bool, error)
	get               func(context.Context, foundation.ID, foundation.ID) (domain.Job, error)
	claim             func(context.Context, foundation.ID, foundation.ID, string, time.Duration) (domain.Job, bool, error)
	prepare           func(context.Context, PrepareRequest) (domain.Job, error)
	complete          func(context.Context, CompleteRequest) (domain.Job, error)
	fail              func(context.Context, FailRequest) (domain.Job, error)
	recordDownload    func(context.Context, DownloadRecord) (domain.Job, error)
	expireCandidates  func(context.Context, int) ([]domain.Job, error)
	cleanupCandidates func(context.Context, int) ([]domain.Job, error)
	recordCleanup     func(context.Context, CleanupRequest) (domain.Job, error)
}

func (stub *serviceRepositoryStub) Create(ctx context.Context, job domain.Job) (domain.Job, bool, error) {
	return stub.create(ctx, job)
}
func (stub *serviceRepositoryStub) Get(ctx context.Context, workspaceID, jobID foundation.ID) (domain.Job, error) {
	return stub.get(ctx, workspaceID, jobID)
}
func (*serviceRepositoryStub) List(context.Context, ListQuery) (ListPage, error) {
	return ListPage{}, errors.New("unexpected list")
}
func (stub *serviceRepositoryStub) Claim(ctx context.Context, workspaceID, jobID foundation.ID, owner string, lease time.Duration) (domain.Job, bool, error) {
	return stub.claim(ctx, workspaceID, jobID, owner, lease)
}
func (stub *serviceRepositoryStub) Prepare(ctx context.Context, request PrepareRequest) (domain.Job, error) {
	if stub.prepare == nil {
		return domain.Job{}, errors.New("unexpected prepare")
	}
	return stub.prepare(ctx, request)
}
func (stub *serviceRepositoryStub) Complete(ctx context.Context, request CompleteRequest) (domain.Job, error) {
	return stub.complete(ctx, request)
}
func (stub *serviceRepositoryStub) Fail(ctx context.Context, request FailRequest) (domain.Job, error) {
	return stub.fail(ctx, request)
}
func (*serviceRepositoryStub) Expire(context.Context, foundation.ID, foundation.ID) (domain.Job, error) {
	return domain.Job{}, errors.New("unexpected expire")
}
func (stub *serviceRepositoryStub) RecordDownload(ctx context.Context, request DownloadRecord) (domain.Job, error) {
	return stub.recordDownload(ctx, request)
}
func (*serviceRepositoryStub) RecoveryCandidates(context.Context, int) ([]domain.Job, error) {
	return nil, errors.New("unexpected recovery candidates")
}
func (stub *serviceRepositoryStub) ExpireCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	return stub.expireCandidates(ctx, limit)
}
func (stub *serviceRepositoryStub) CleanupCandidates(ctx context.Context, limit int) ([]domain.Job, error) {
	return stub.cleanupCandidates(ctx, limit)
}
func (stub *serviceRepositoryStub) RecordCleanup(ctx context.Context, request CleanupRequest) (domain.Job, error) {
	return stub.recordCleanup(ctx, request)
}
func (*serviceRepositoryStub) UnreferencedStaging(context.Context, foundation.ID, []string) ([]string, error) {
	return nil, errors.New("unexpected orphan lookup")
}
func (*serviceRepositoryStub) OrphanSweepWorkspaces(context.Context, foundation.ID, int) ([]foundation.ID, error) {
	return nil, errors.New("unexpected orphan workspaces")
}
