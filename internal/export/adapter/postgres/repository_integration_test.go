//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/export/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExportRepositoryLifecycleIdempotencyAndDownloadAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newExportIntegrationRepository(t, ctx)

	workspaceID := exportIntegrationID(t)
	collectionID := exportIntegrationID(t)
	queryHash := exportIntegrationHash("collection:" + string(collectionID))
	seedExportIntegrationScope(t, ctx, pool, workspaceID, collectionID, queryHash)

	mainCandidate := exportIntegrationJob(t, workspaceID, collectionID, queryHash, time.Hour, "main")
	created, replayed, err := repository.Create(ctx, mainCandidate)
	if err != nil || replayed || created.Status != domain.StatusPending || created.Version != 1 {
		t.Fatalf("create job=%#v replayed=%t err=%v detail=%s", created, replayed, err, exportIntegrationErrorDetail(err))
	}
	expiryCandidate := exportIntegrationJob(t, workspaceID, collectionID, queryHash, time.Second, "expiry")
	expiryCreated, replayed, err := repository.Create(ctx, expiryCandidate)
	if err != nil || replayed || expiryCreated.Status != domain.StatusPending {
		t.Fatalf("create expiring job=%#v replayed=%t err=%v", expiryCreated, replayed, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE learning.smart_collection SET
		status='ARCHIVED',version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(collectionID)); err != nil {
		t.Fatalf("archive Collection after create: %v", err)
	}
	replayCandidate := mainCandidate
	replayCandidate.ID = exportIntegrationID(t)
	replayedJob, replayed, err := repository.Create(ctx, replayCandidate)
	if err != nil || !replayed || replayedJob.ID != created.ID {
		t.Fatalf("idempotency-first replay job=%#v replayed=%t err=%v", replayedJob, replayed, err)
	}
	conflictCandidate := replayCandidate
	conflictCandidate.ID = exportIntegrationID(t)
	conflictCandidate.RequestHash = exportIntegrationHash("conflict")
	if _, _, err := repository.Create(ctx, conflictCandidate); exportIntegrationErrorCode(err) != domain.ErrorCodeConflict {
		t.Fatalf("idempotency conflict code=%q err=%v", exportIntegrationErrorCode(err), err)
	}

	const owner = "export-integration-worker"
	claimed, ok, err := repository.Claim(ctx, workspaceID, created.ID, owner, 2*time.Minute)
	if err != nil || !ok || claimed.Status != domain.StatusRunning || claimed.Version != 2 || claimed.LeaseOwner != owner {
		t.Fatalf("claim job=%#v ok=%t err=%v", claimed, ok, err)
	}
	payload := []byte("export integration payload")
	preparedFile := exportapp.PreparedFile{
		StagingPath: ".knowledge/exports/.staging/" + string(created.ID) + "-" + strings.Repeat("d", 32) + ".md.stage",
		FinalPath:   ".knowledge/exports/" + string(created.ID) + ".md",
		FileHash:    exportIntegrationBytesHash(payload),
		FileSize:    int64(len(payload)),
	}
	prepared, err := repository.Prepare(ctx, exportapp.PrepareRequest{
		WorkspaceID: workspaceID, JobID: created.ID, LeaseOwner: owner, ExpectedVersion: claimed.Version,
		ReadModelRevision: exportIntegrationHash("read-model"), ExactCount: 2, PreparedFile: preparedFile,
	})
	if err != nil || prepared.Status != domain.StatusRunning || prepared.Version != 3 || !prepared.IsPrepared() ||
		prepared.ExactCount == nil || *prepared.ExactCount != 2 {
		t.Fatalf("prepare job=%#v err=%v", prepared, err)
	}
	completed, err := repository.Complete(ctx, exportapp.CompleteRequest{
		WorkspaceID: workspaceID, JobID: created.ID, LeaseOwner: owner,
		ExpectedVersion: prepared.Version, PreparedFile: preparedFile,
	})
	if err != nil || completed.Status != domain.StatusSucceeded || completed.Version != 4 || completed.LeaseOwner != "" {
		t.Fatalf("complete job=%#v err=%v", completed, err)
	}

	invalidAuditID := exportIntegrationID(t)
	_, err = repository.RecordDownload(ctx, exportapp.DownloadRecord{
		WorkspaceID: workspaceID, JobID: created.ID, AuditEventID: invalidAuditID,
		Actor: exportapp.DownloadActor{Type: auditdomain.ActorType("INVALID"), Ref: "invalid"}, ScopeKind: domain.ScopeCollection,
		FileHash: preparedFile.FileHash, FileSize: preparedFile.FileSize,
	})
	if err == nil {
		t.Fatal("download with an invalid Audit actor succeeded")
	}
	assertExportDownloadProjection(t, ctx, pool, created.ID, 4, 0)

	auditEventID := exportIntegrationID(t)
	actorRef := exportIntegrationID(t)
	downloaded, err := repository.RecordDownload(ctx, exportapp.DownloadRecord{
		WorkspaceID: workspaceID, JobID: created.ID, AuditEventID: auditEventID,
		Actor: exportapp.DownloadActor{Type: auditdomain.ActorUser, Ref: string(actorRef)}, ScopeKind: domain.ScopeCollection,
		FileHash: preparedFile.FileHash, FileSize: preparedFile.FileSize,
	})
	if err != nil || downloaded.Version != 5 || downloaded.DownloadCount != 1 || downloaded.LastDownloadedAt == nil {
		t.Fatalf("record download job=%#v err=%v", downloaded, err)
	}
	assertExportDownloadProjection(t, ctx, pool, created.ID, 5, 1)
	assertExportDownloadAudit(t, ctx, pool, auditEventID, created.ID, preparedFile.FileHash)

	if _, err := pool.Exec(ctx, `SELECT pg_sleep(1.1)`); err != nil {
		t.Fatalf("wait for export expiry: %v", err)
	}
	expired, err := repository.Expire(ctx, workspaceID, expiryCreated.ID)
	if err != nil || expired.Status != domain.StatusExpired || expired.CleanupStatus != domain.CleanupPending || expired.Version != 2 {
		t.Fatalf("expire job=%#v err=%v", expired, err)
	}

	var lifecycleEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND resource_ref=$2`,
		string(workspaceID), "export_job:"+string(created.ID)).Scan(&lifecycleEvents); err != nil {
		t.Fatalf("count export lifecycle events: %v", err)
	}
	if lifecycleEvents != 4 {
		t.Fatalf("main export lifecycle events=%d, want 4", lifecycleEvents)
	}
}

func TestExportRepositoryAttachmentCapabilityLifecycleAndScopeIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newExportIntegrationRepository(t, ctx)

	workspaceID := exportIntegrationID(t)
	collectionID := exportIntegrationID(t)
	queryHash := exportIntegrationHash("collection:" + string(collectionID))
	seedExportIntegrationScope(t, ctx, pool, workspaceID, collectionID, queryHash)
	otherWorkspaceID := exportIntegrationID(t)
	otherCollectionID := exportIntegrationID(t)
	seedExportIntegrationScope(t, ctx, pool, otherWorkspaceID, otherCollectionID, exportIntegrationHash("collection:"+string(otherCollectionID)))

	disabledCandidate := exportIntegrationAttachmentJob(t, workspaceID, time.Hour, "disabled")
	if _, _, err := repository.Create(ctx, disabledCandidate); exportIntegrationErrorCode(err) != domain.ErrorCodeUnavailable {
		t.Fatalf("disabled attachment create code=%q err=%v detail=%s", exportIntegrationErrorCode(err), err, exportIntegrationErrorDetail(err))
	}
	var disabledRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.export_job WHERE id=$1`, string(disabledCandidate.ID)).Scan(&disabledRows); err != nil {
		t.Fatal(err)
	}
	if disabledRows != 0 {
		t.Fatalf("disabled attachment create persisted %d rows", disabledRows)
	}

	setExportAttachmentCapability(t, ctx, pool, true)
	firstCandidate := exportIntegrationAttachmentJob(t, workspaceID, time.Hour, "first")
	first, replayed, err := repository.Create(ctx, firstCandidate)
	if err != nil || replayed || first.Scope.Kind != domain.ScopeWorkspaceAttachments {
		t.Fatalf("create attachment job=%#v replayed=%t err=%v detail=%s", first, replayed, err, exportIntegrationErrorDetail(err))
	}
	replayCandidate := firstCandidate
	replayCandidate.ID = exportIntegrationID(t)
	replayedJob, replayed, err := repository.Create(ctx, replayCandidate)
	if err != nil || !replayed || replayedJob.ID != first.ID {
		t.Fatalf("replay attachment job=%#v replayed=%t err=%v", replayedJob, replayed, err)
	}
	secondCandidate := exportIntegrationAttachmentJob(t, workspaceID, time.Hour, "second")
	second, replayed, err := repository.Create(ctx, secondCandidate)
	if err != nil || replayed {
		t.Fatalf("create second attachment job=%#v replayed=%t err=%v", second, replayed, err)
	}
	collectionCandidate := exportIntegrationJob(t, workspaceID, collectionID, queryHash, time.Hour, "scope-list")
	collectionJob, replayed, err := repository.Create(ctx, collectionCandidate)
	if err != nil || replayed {
		t.Fatalf("create collection scope fixture job=%#v replayed=%t err=%v", collectionJob, replayed, err)
	}

	firstPage, err := repository.List(ctx, exportapp.ListQuery{
		WorkspaceID: workspaceID, ScopeKind: domain.ScopeWorkspaceAttachments, Limit: 1,
	})
	if err != nil || len(firstPage.Items) != 1 || firstPage.NextCursor == "" || firstPage.Items[0].Scope.Kind != domain.ScopeWorkspaceAttachments {
		t.Fatalf("first attachment page=%#v err=%v", firstPage, err)
	}
	secondPage, err := repository.List(ctx, exportapp.ListQuery{
		WorkspaceID: workspaceID, ScopeKind: domain.ScopeWorkspaceAttachments, Limit: 1, Cursor: firstPage.NextCursor,
	})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].Scope.Kind != domain.ScopeWorkspaceAttachments ||
		secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("second attachment page=%#v err=%v", secondPage, err)
	}
	collectionPage, err := repository.List(ctx, exportapp.ListQuery{
		WorkspaceID: workspaceID, ScopeKind: domain.ScopeCollection, CollectionID: &collectionID, Limit: 10,
	})
	if err != nil || len(collectionPage.Items) != 1 || collectionPage.Items[0].ID != collectionJob.ID {
		t.Fatalf("collection-only page=%#v err=%v", collectionPage, err)
	}

	const owner = "attachment-integration-worker"
	claimed, ok, err := repository.Claim(ctx, workspaceID, first.ID, owner, 2*time.Minute)
	if err != nil || !ok || claimed.Status != domain.StatusRunning || claimed.Scope.Kind != domain.ScopeWorkspaceAttachments {
		t.Fatalf("claim attachment job=%#v ok=%t err=%v", claimed, ok, err)
	}
	preparedFile := exportIntegrationAttachmentPreparedFile(first.ID)
	manifestHash := exportIntegrationHash("manifest:" + string(first.ID))
	prepared, err := repository.Prepare(ctx, exportapp.PrepareRequest{
		WorkspaceID: workspaceID, JobID: first.ID, LeaseOwner: owner, ExpectedVersion: claimed.Version,
		ManifestHash: manifestHash, EntryCount: 2, TotalUncompressedBytes: 42, PreparedFile: preparedFile,
	})
	if err != nil || prepared.ManifestHash != manifestHash || prepared.EntryCount == nil || *prepared.EntryCount != 2 ||
		prepared.TotalUncompressedBytes == nil || *prepared.TotalUncompressedBytes != 42 || prepared.ReadModelRevision != "" || prepared.ExactCount != nil {
		t.Fatalf("prepare attachment job=%#v err=%v detail=%s", prepared, err, exportIntegrationErrorDetail(err))
	}
	completed, err := repository.Complete(ctx, exportapp.CompleteRequest{
		WorkspaceID: workspaceID, JobID: first.ID, LeaseOwner: owner, ExpectedVersion: prepared.Version, PreparedFile: preparedFile,
	})
	if err != nil || completed.Status != domain.StatusSucceeded {
		t.Fatalf("complete attachment job=%#v err=%v", completed, err)
	}
	lifecycleRows, err := pool.Query(ctx, `SELECT event_type,payload_summary->>'scope_kind'
		FROM ops.server_event WHERE workspace_id=$1 AND resource_ref=$2 ORDER BY seq`,
		string(workspaceID), "export_job:"+string(first.ID))
	if err != nil {
		t.Fatalf("read attachment lifecycle event scopes: %v", err)
	}
	defer lifecycleRows.Close()
	var lifecycleStages []string
	for lifecycleRows.Next() {
		var stage, scopeKind string
		if err := lifecycleRows.Scan(&stage, &scopeKind); err != nil {
			t.Fatalf("scan attachment lifecycle event scope: %v", err)
		}
		if scopeKind != "workspace_attachments" {
			t.Fatalf("attachment lifecycle event %q scope_kind=%q", stage, scopeKind)
		}
		lifecycleStages = append(lifecycleStages, stage)
	}
	if err := lifecycleRows.Err(); err != nil {
		t.Fatalf("iterate attachment lifecycle event scopes: %v", err)
	}
	const wantLifecycleStages = "export.created,export.claimed,export.prepared,export.completed"
	if strings.Join(lifecycleStages, ",") != wantLifecycleStages {
		t.Fatalf("attachment lifecycle stages=%v, want %s", lifecycleStages, wantLifecycleStages)
	}
	auditEventID := exportIntegrationID(t)
	actorRef := exportIntegrationID(t)
	downloaded, err := repository.RecordDownload(ctx, exportapp.DownloadRecord{
		WorkspaceID: workspaceID, JobID: first.ID, AuditEventID: auditEventID,
		Actor:     exportapp.DownloadActor{Type: auditdomain.ActorUser, Ref: string(actorRef)},
		ScopeKind: domain.ScopeWorkspaceAttachments, EntryCount: completed.EntryCount,
		FileHash: preparedFile.FileHash, FileSize: preparedFile.FileSize,
	})
	if err != nil || downloaded.DownloadCount != 1 {
		t.Fatalf("record attachment download job=%#v err=%v", downloaded, err)
	}
	assertExportAttachmentDownloadAudit(t, ctx, pool, auditEventID, first.ID, preparedFile.FileHash, 2)
	if _, err := repository.Get(ctx, otherWorkspaceID, first.ID); exportIntegrationErrorCode(err) != domain.ErrorCodeNotFound {
		t.Fatalf("cross-workspace attachment get code=%q err=%v", exportIntegrationErrorCode(err), err)
	}

	setExportAttachmentCapability(t, ctx, pool, false)
	blockedCandidate := exportIntegrationAttachmentJob(t, workspaceID, time.Hour, "blocked-again")
	if _, _, err := repository.Create(ctx, blockedCandidate); exportIntegrationErrorCode(err) != domain.ErrorCodeUnavailable {
		t.Fatalf("disabled-again create code=%q err=%v", exportIntegrationErrorCode(err), err)
	}
	notClaimed, ok, err := repository.Claim(ctx, workspaceID, second.ID, owner, 2*time.Minute)
	if exportIntegrationErrorCode(err) != domain.ErrorCodeUnavailable || ok || notClaimed.ID != "" {
		t.Fatalf("disabled claim job=%#v ok=%t code=%q err=%v", notClaimed, ok, exportIntegrationErrorCode(err), err)
	}
	pendingAfterDisabledClaim, err := repository.Get(ctx, workspaceID, second.ID)
	if err != nil || pendingAfterDisabledClaim.Status != domain.StatusPending || pendingAfterDisabledClaim.Version != second.Version {
		t.Fatalf("disabled claim changed job=%#v err=%v", pendingAfterDisabledClaim, err)
	}
	candidates, err := repository.RecoveryCandidates(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Scope.Kind == domain.ScopeWorkspaceAttachments {
			t.Fatalf("disabled recovery returned attachment job %#v", candidate)
		}
	}
	preserved, err := repository.Get(ctx, workspaceID, completed.ID)
	if err != nil || preserved.ManifestHash != manifestHash || preserved.Status != domain.StatusSucceeded {
		t.Fatalf("disabled gate did not preserve completed attachment job=%#v err=%v", preserved, err)
	}
}

func TestExportRepositoryDatabaseTimeCAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newExportIntegrationRepository(t, ctx)

	workspaceID := exportIntegrationID(t)
	collectionID := exportIntegrationID(t)
	queryHash := exportIntegrationHash("collection:" + string(collectionID))
	seedExportIntegrationScope(t, ctx, pool, workspaceID, collectionID, queryHash)

	tests := []struct {
		name      string
		operation string
		boundary  string
	}{
		{name: "claim rejects crossed ttl", operation: "claim", boundary: "ttl"},
		{name: "prepare rejects crossed lease", operation: "prepare", boundary: "lease"},
		{name: "prepare expires crossed ttl", operation: "prepare", boundary: "ttl"},
		{name: "complete rejects crossed lease", operation: "complete", boundary: "lease"},
		{name: "complete expires crossed ttl", operation: "complete", boundary: "ttl"},
		{name: "fail rejects crossed lease", operation: "fail", boundary: "lease"},
		{name: "fail expires crossed ttl", operation: "fail", boundary: "ttl"},
		{name: "download expires crossed ttl without audit", operation: "download", boundary: "ttl"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ttl := 10 * time.Second
			lease := 5 * time.Second
			delay := 1100 * time.Millisecond
			if test.boundary == "ttl" {
				ttl = time.Second
			} else {
				lease = 500 * time.Millisecond
				delay = 700 * time.Millisecond
			}

			created, replayed, err := repository.Create(ctx, exportIntegrationJob(
				t, workspaceID, collectionID, queryHash, ttl, test.operation+"-"+test.boundary,
			))
			if err != nil || replayed {
				t.Fatalf("create %s job=%#v replayed=%t err=%v detail=%s", test.operation, created, replayed, err, exportIntegrationErrorDetail(err))
			}

			owner := "export-db-time-" + test.operation + "-" + test.boundary
			before := created
			preparedFile := exportIntegrationPreparedFile(created.ID)
			wantPrepared := false
			if test.operation != "claim" {
				claimed, ok, claimErr := repository.Claim(ctx, workspaceID, created.ID, owner, lease)
				if claimErr != nil || !ok {
					t.Fatalf("claim %s job=%#v ok=%t err=%v", test.operation, claimed, ok, claimErr)
				}
				before = claimed
			}
			if test.operation == "complete" || test.operation == "download" {
				prepared, prepareErr := repository.Prepare(ctx, exportapp.PrepareRequest{
					WorkspaceID: workspaceID, JobID: created.ID, LeaseOwner: owner, ExpectedVersion: before.Version,
					ReadModelRevision: exportIntegrationHash("read-model:" + string(created.ID)), ExactCount: 2,
					PreparedFile: preparedFile,
				})
				if prepareErr != nil {
					t.Fatalf("prepare %s job=%#v err=%v", test.operation, prepared, prepareErr)
				}
				before = prepared
				wantPrepared = true
			}
			if test.operation == "download" {
				completed, completeErr := repository.Complete(ctx, exportapp.CompleteRequest{
					WorkspaceID: workspaceID, JobID: created.ID, LeaseOwner: owner,
					ExpectedVersion: before.Version, PreparedFile: preparedFile,
				})
				if completeErr != nil {
					t.Fatalf("complete download fixture job=%#v err=%v", completed, completeErr)
				}
				before = completed
			}

			assertExportIntegrationBoundaryActive(t, ctx, pool, created.ID, test.boundary)
			installExportIntegrationUpdateDelay(t, ctx, pool, delay)

			var result domain.Job
			var operationErr error
			switch test.operation {
			case "claim":
				var ok bool
				result, ok, operationErr = repository.Claim(ctx, workspaceID, created.ID, owner, lease)
				if ok {
					t.Fatal("claim succeeded after crossing export ttl")
				}
			case "prepare":
				result, operationErr = repository.Prepare(ctx, exportapp.PrepareRequest{
					WorkspaceID: workspaceID, JobID: created.ID, LeaseOwner: owner, ExpectedVersion: before.Version,
					ReadModelRevision: exportIntegrationHash("read-model:" + string(created.ID)), ExactCount: 2,
					PreparedFile: preparedFile,
				})
			case "complete":
				result, operationErr = repository.Complete(ctx, exportapp.CompleteRequest{
					WorkspaceID: workspaceID, JobID: created.ID, LeaseOwner: owner,
					ExpectedVersion: before.Version, PreparedFile: preparedFile,
				})
			case "fail":
				result, operationErr = repository.Fail(ctx, exportapp.FailRequest{
					WorkspaceID: workspaceID, JobID: created.ID, LeaseOwner: owner, ExpectedVersion: before.Version,
					ErrorCode: "EXPORT_TEST_FAILURE", ErrorMessage: "forced integration failure",
				})
			case "download":
				result, operationErr = repository.RecordDownload(ctx, exportapp.DownloadRecord{
					WorkspaceID: workspaceID, JobID: created.ID, AuditEventID: exportIntegrationID(t),
					Actor: exportapp.DownloadActor{Type: auditdomain.ActorUser, Ref: string(exportIntegrationID(t))}, ScopeKind: domain.ScopeCollection,
					FileHash: preparedFile.FileHash, FileSize: preparedFile.FileSize,
				})
			default:
				t.Fatalf("unsupported operation %q", test.operation)
			}

			persisted := readExportIntegrationJob(t, ctx, pool, workspaceID, created.ID)
			wantDelayedUpdates := int64(2)
			if test.boundary == "lease" {
				wantDelayedUpdates = 1
			}
			assertExportIntegrationDelayedUpdates(t, ctx, pool, wantDelayedUpdates)
			if test.boundary == "lease" {
				if exportIntegrationErrorCode(operationErr) != "EXPORT_LEASE_LOST" {
					t.Fatalf("%s lease error code=%q err=%v detail=%s", test.operation, exportIntegrationErrorCode(operationErr), operationErr, exportIntegrationErrorDetail(operationErr))
				}
				if persisted.Status != domain.StatusRunning || persisted.Version != before.Version || persisted.CompletedAt != nil ||
					persisted.ErrorCode != "" || persisted.IsPrepared() != wantPrepared {
					t.Fatalf("%s committed after lease boundary: %#v", test.operation, persisted)
				}
				assertExportIntegrationDatabaseBoundary(t, ctx, pool, created.ID, false, true)
				return
			}

			if test.operation == "download" {
				if exportIntegrationErrorCode(operationErr) != domain.ErrorCodeExpired {
					t.Fatalf("download ttl error code=%q err=%v detail=%s", exportIntegrationErrorCode(operationErr), operationErr, exportIntegrationErrorDetail(operationErr))
				}
			} else if operationErr != nil {
				t.Fatalf("%s ttl transition err=%v detail=%s", test.operation, operationErr, exportIntegrationErrorDetail(operationErr))
			}
			if test.operation != "download" && (result.ID != created.ID || result.Status != domain.StatusExpired) {
				t.Fatalf("%s ttl result=%#v", test.operation, result)
			}
			if persisted.Status != domain.StatusExpired || persisted.Version != before.Version+1 ||
				persisted.CleanupStatus != domain.CleanupPending || persisted.DownloadCount != 0 ||
				persisted.IsPrepared() != wantPrepared {
				t.Fatalf("%s ttl persisted job=%#v", test.operation, persisted)
			}
			assertExportIntegrationDatabaseBoundary(t, ctx, pool, created.ID, true, false)
			assertExportIntegrationNoDownloadAudit(t, ctx, pool, created.ID)
		})
	}
}

func TestExportRepositoryConcurrentCreateAndDownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newExportIntegrationRepository(t, ctx)

	workspaceID := exportIntegrationID(t)
	collectionID := exportIntegrationID(t)
	queryHash := exportIntegrationHash("collection:" + string(collectionID))
	seedExportIntegrationScope(t, ctx, pool, workspaceID, collectionID, queryHash)

	firstCandidate := exportIntegrationJob(t, workspaceID, collectionID, queryHash, time.Hour, "concurrent-create")
	secondCandidate := firstCandidate
	secondCandidate.ID = exportIntegrationID(t)
	secondCandidate.Fields = append([]domain.Field(nil), firstCandidate.Fields...)
	collectionIDCopy := *firstCandidate.Scope.CollectionID
	collectionVersionCopy := *firstCandidate.Scope.CollectionVersion
	secondCandidate.Scope.CollectionID = &collectionIDCopy
	secondCandidate.Scope.CollectionVersion = &collectionVersionCopy

	type createResult struct {
		job      domain.Job
		replayed bool
		err      error
	}
	createStart := make(chan struct{})
	createResults := make(chan createResult, 2)
	for _, candidate := range []domain.Job{firstCandidate, secondCandidate} {
		candidate := candidate
		go func() {
			<-createStart
			job, replayed, err := repository.Create(ctx, candidate)
			createResults <- createResult{job: job, replayed: replayed, err: err}
		}()
	}
	close(createStart)
	results := []createResult{<-createResults, <-createResults}
	if results[0].err != nil || results[1].err != nil {
		t.Fatalf("concurrent create errors first=%v (%s) second=%v (%s)",
			results[0].err, exportIntegrationErrorDetail(results[0].err),
			results[1].err, exportIntegrationErrorDetail(results[1].err))
	}
	if results[0].job.ID != results[1].job.ID || results[0].replayed == results[1].replayed {
		t.Fatalf("concurrent create results first=%#v second=%#v", results[0], results[1])
	}
	authoritative := results[0].job
	if results[0].replayed {
		authoritative = results[1].job
	}
	if authoritative.ID != firstCandidate.ID && authoritative.ID != secondCandidate.ID {
		t.Fatalf("concurrent create persisted unexpected ID %s", authoritative.ID)
	}
	var persistedJobs, createdEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.export_job WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(workspaceID), firstCandidate.IdempotencyKey).Scan(&persistedJobs); err != nil {
		t.Fatalf("count concurrent export jobs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1 AND event_type='export.created'
		AND resource_ref=$2`, string(workspaceID), "export_job:"+string(authoritative.ID)).Scan(&createdEvents); err != nil {
		t.Fatalf("count concurrent export create events: %v", err)
	}
	if persistedJobs != 1 || createdEvents != 1 {
		t.Fatalf("concurrent create persisted jobs=%d events=%d", persistedJobs, createdEvents)
	}

	const owner = "export-concurrent-download-worker"
	claimed, ok, err := repository.Claim(ctx, workspaceID, authoritative.ID, owner, 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim concurrent download job=%#v ok=%t err=%v", claimed, ok, err)
	}
	preparedFile := exportIntegrationPreparedFile(authoritative.ID)
	prepared, err := repository.Prepare(ctx, exportapp.PrepareRequest{
		WorkspaceID: workspaceID, JobID: authoritative.ID, LeaseOwner: owner, ExpectedVersion: claimed.Version,
		ReadModelRevision: exportIntegrationHash("read-model:" + string(authoritative.ID)), ExactCount: 2,
		PreparedFile: preparedFile,
	})
	if err != nil {
		t.Fatalf("prepare concurrent download job=%#v err=%v", prepared, err)
	}
	completed, err := repository.Complete(ctx, exportapp.CompleteRequest{
		WorkspaceID: workspaceID, JobID: authoritative.ID, LeaseOwner: owner,
		ExpectedVersion: prepared.Version, PreparedFile: preparedFile,
	})
	if err != nil {
		t.Fatalf("complete concurrent download job=%#v err=%v", completed, err)
	}

	const downloadTotal = 8
	records := make([]exportapp.DownloadRecord, downloadTotal)
	for index := range records {
		records[index] = exportapp.DownloadRecord{
			WorkspaceID: workspaceID, JobID: authoritative.ID, AuditEventID: exportIntegrationID(t),
			Actor: exportapp.DownloadActor{Type: auditdomain.ActorUser, Ref: string(exportIntegrationID(t))}, ScopeKind: domain.ScopeCollection,
			FileHash: preparedFile.FileHash, FileSize: preparedFile.FileSize,
		}
	}
	type downloadResult struct {
		job domain.Job
		err error
	}
	downloadStart := make(chan struct{})
	downloadResults := make(chan downloadResult, downloadTotal)
	for _, record := range records {
		record := record
		go func() {
			<-downloadStart
			job, recordErr := repository.RecordDownload(ctx, record)
			downloadResults <- downloadResult{job: job, err: recordErr}
		}()
	}
	close(downloadStart)
	seenCounts := make(map[int]struct{}, downloadTotal)
	for range downloadTotal {
		result := <-downloadResults
		if result.err != nil {
			t.Fatalf("concurrent download error=%v detail=%s", result.err, exportIntegrationErrorDetail(result.err))
		}
		seenCounts[result.job.DownloadCount] = struct{}{}
	}
	for count := 1; count <= downloadTotal; count++ {
		if _, exists := seenCounts[count]; !exists {
			t.Fatalf("concurrent download results missed count %d: %v", count, seenCounts)
		}
	}
	persisted := readExportIntegrationJob(t, ctx, pool, workspaceID, authoritative.ID)
	if persisted.DownloadCount != downloadTotal || persisted.Version != completed.Version+downloadTotal || persisted.LastDownloadedAt == nil {
		t.Fatalf("concurrent download projection=%#v", persisted)
	}
	var auditCount, distinctAuditIDs, distinctActors int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT id),count(DISTINCT actor_ref)
		FROM ops.audit_event WHERE action='export.download' AND resource_ref=$1`,
		"export_job:"+string(authoritative.ID)).Scan(&auditCount, &distinctAuditIDs, &distinctActors); err != nil {
		t.Fatalf("count concurrent download Audit events: %v", err)
	}
	if auditCount != downloadTotal || distinctAuditIDs != downloadTotal || distinctActors != downloadTotal {
		t.Fatalf("concurrent download Audit count=%d IDs=%d actors=%d", auditCount, distinctAuditIDs, distinctActors)
	}
}

func TestExportRepositoryCleanupFailureCanRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newExportIntegrationRepository(t, ctx)

	workspaceID := exportIntegrationID(t)
	collectionID := exportIntegrationID(t)
	queryHash := exportIntegrationHash("collection:" + string(collectionID))
	seedExportIntegrationScope(t, ctx, pool, workspaceID, collectionID, queryHash)
	created, replayed, err := repository.Create(ctx, exportIntegrationJob(t, workspaceID, collectionID, queryHash, time.Second, "cleanup-retry"))
	if err != nil || replayed {
		t.Fatalf("create cleanup retry job=%#v replayed=%t err=%v detail=%s", created, replayed, err, exportIntegrationErrorDetail(err))
	}
	if _, err := pool.Exec(ctx, `SELECT pg_sleep(1.1)`); err != nil {
		t.Fatalf("wait for cleanup retry job expiry: %v", err)
	}
	expired, err := repository.Expire(ctx, workspaceID, created.ID)
	if err != nil || expired.Status != domain.StatusExpired || expired.CleanupStatus != domain.CleanupPending {
		t.Fatalf("expire cleanup retry job=%#v err=%v", expired, err)
	}

	const cleanupErrorCode = "EXPORT_TEST_CLEANUP_FAILED"
	failed, err := repository.RecordCleanup(ctx, exportapp.CleanupRequest{
		WorkspaceID: workspaceID, JobID: created.ID, ExpectedVersion: expired.Version,
		Succeeded: false, ErrorCode: cleanupErrorCode,
	})
	if err != nil || failed.Status != domain.StatusExpired || failed.CleanupStatus != domain.CleanupFailed ||
		failed.CleanupAttemptCount != 1 || failed.CleanupError != cleanupErrorCode || failed.FileDeletedAt != nil {
		t.Fatalf("record failed cleanup job=%#v err=%v detail=%s", failed, err, exportIntegrationErrorDetail(err))
	}
	candidates, err := repository.CleanupCandidates(ctx, 10)
	if err != nil || len(candidates) != 1 || candidates[0].ID != created.ID || candidates[0].CleanupStatus != domain.CleanupFailed {
		t.Fatalf("failed cleanup candidates=%#v err=%v", candidates, err)
	}

	cleaned, err := repository.RecordCleanup(ctx, exportapp.CleanupRequest{
		WorkspaceID: workspaceID, JobID: created.ID, ExpectedVersion: failed.Version, Succeeded: true,
	})
	if err != nil || cleaned.Status != domain.StatusExpired || cleaned.CleanupStatus != domain.CleanupSucceeded ||
		cleaned.CleanupAttemptCount != 2 || cleaned.CleanupError != "" || cleaned.FileDeletedAt == nil {
		t.Fatalf("record retried cleanup job=%#v err=%v detail=%s", cleaned, err, exportIntegrationErrorDetail(err))
	}
}

func TestExportRepositoryOrphanSweepWorkspacesUsesStableCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newExportIntegrationRepository(t, ctx)

	workspaceIDs := []foundation.ID{exportIntegrationID(t), exportIntegrationID(t)}
	sort.Slice(workspaceIDs, func(left, right int) bool { return workspaceIDs[left] < workspaceIDs[right] })
	for index, workspaceID := range workspaceIDs {
		collectionID := exportIntegrationID(t)
		queryHash := exportIntegrationHash("orphan-sweep:" + string(collectionID))
		seedExportIntegrationScope(t, ctx, pool, workspaceID, collectionID, queryHash)
		if _, replayed, err := repository.Create(ctx, exportIntegrationJob(t, workspaceID, collectionID, queryHash, time.Hour, fmt.Sprintf("orphan-sweep-%d", index))); err != nil || replayed {
			t.Fatalf("create orphan sweep job index=%d replayed=%t err=%v detail=%s", index, replayed, err, exportIntegrationErrorDetail(err))
		}
	}

	first, err := repository.OrphanSweepWorkspaces(ctx, "", 1)
	if err != nil || len(first) != 1 || first[0] != workspaceIDs[0] {
		t.Fatalf("first orphan sweep page=%#v err=%v detail=%s", first, err, exportIntegrationErrorDetail(err))
	}
	second, err := repository.OrphanSweepWorkspaces(ctx, first[0], 1)
	if err != nil || len(second) != 1 || second[0] != workspaceIDs[1] {
		t.Fatalf("second orphan sweep page=%#v err=%v detail=%s", second, err, exportIntegrationErrorDetail(err))
	}
	last, err := repository.OrphanSweepWorkspaces(ctx, second[0], 1)
	if err != nil || len(last) != 0 {
		t.Fatalf("terminal orphan sweep page=%#v err=%v detail=%s", last, err, exportIntegrationErrorDetail(err))
	}
}

func newExportIntegrationRepository(t *testing.T, ctx context.Context) (*Repository, *pgxpool.Pool) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	databaseName := fmt.Sprintf("zhixu_export_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create export test database: %v", err)
	}
	parsed.Path = "/" + databaseName
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatalf("connect export test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	runner, err := platformmigration.NewAtlasEmbeddedRunner(pool)
	if err == nil {
		err = runner.Up(ctx)
	}
	if err != nil {
		t.Fatalf("migrate export test database: %v", err)
	}
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatalf("create event store: %v", err)
	}
	audit, err := auditpostgres.NewStore(pool)
	if err != nil {
		t.Fatalf("create audit store: %v", err)
	}
	repository, err := NewRepository(pool, WithEventAppender(events), WithAuditAppender(audit))
	if err != nil {
		t.Fatalf("create export repository: %v", err)
	}
	return repository, pool
}

func exportIntegrationJob(t *testing.T, workspaceID, collectionID foundation.ID, queryHash string, ttl time.Duration, suffix string) domain.Job {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	collectionVersion := int64(1)
	id := exportIntegrationID(t)
	return domain.Job{
		ID: id, WorkspaceID: workspaceID, Kind: domain.KindMarkdown, SchemaVersion: exportapp.SchemaVersionV1,
		Scope:  domain.Scope{Kind: domain.ScopeCollection, CollectionID: &collectionID, CollectionVersion: &collectionVersion, QueryHash: queryHash},
		Fields: []domain.Field{domain.FieldID, domain.FieldTitle}, Redaction: domain.RedactionMasked,
		PermissionScope: "READ_LOCAL", RequestedBy: "USER:integration", IdempotencyKey: "export-integration-" + suffix + "-" + string(id),
		RequestHash: exportIntegrationHash("request:" + suffix + ":" + string(id)), RequestTTLSeconds: int64(ttl / time.Second),
		Status: domain.StatusPending, Version: 1, ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now,
		CleanupStatus: domain.CleanupNotRequired,
	}
}

func exportIntegrationAttachmentJob(t *testing.T, workspaceID foundation.ID, ttl time.Duration, suffix string) domain.Job {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := exportIntegrationID(t)
	return domain.Job{
		ID: id, WorkspaceID: workspaceID, Kind: domain.KindAttachmentsZIP, SchemaVersion: "attachment-export/v1",
		Scope: domain.Scope{
			Kind:                          domain.ScopeWorkspaceAttachments,
			AttachmentRootContractVersion: "workspace-attachments/v1",
		},
		Redaction: domain.RedactionRawUserOwned, PermissionScope: "READ_LOCAL", RequestedBy: "USER:integration",
		IdempotencyKey: "attachment-export-integration-" + suffix + "-" + string(id),
		RequestHash:    exportIntegrationHash("attachment-request:" + suffix + ":" + string(id)), RequestTTLSeconds: int64(ttl / time.Second),
		Status: domain.StatusPending, Version: 1, ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now,
		CleanupStatus: domain.CleanupNotRequired,
	}
}

func seedExportIntegrationScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, collectionID foundation.ID, queryHash string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	root := "/tmp/export-integration-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(workspaceID), "export-integration-"+string(workspaceID), root, now); err != nil {
		t.Fatalf("seed export Workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.smart_collection(
		id,workspace_id,name,normalized_name,description,query_schema_version,query_version,
		query_definition,query_hash,view_type,view_config,status,version,created_at,updated_at
	) VALUES($1,$2,$1::uuid::text,$1::uuid::text,'','collection-query/v1',1,
		'{"root":{"kind":"group","operator":"AND","clauses":[]},"sort":[]}'::jsonb,
		$3,'LIST','{}'::jsonb,'ACTIVE',1,$4,$4)`, string(collectionID), string(workspaceID), queryHash, now); err != nil {
		t.Fatalf("seed export Collection: %v", err)
	}
}

func exportIntegrationPreparedFile(exportID foundation.ID) exportapp.PreparedFile {
	payload := []byte("export database time payload")
	return exportapp.PreparedFile{
		StagingPath: ".knowledge/exports/.staging/" + string(exportID) + "-" + strings.Repeat("e", 32) + ".md.stage",
		FinalPath:   ".knowledge/exports/" + string(exportID) + ".md",
		FileHash:    exportIntegrationBytesHash(payload),
		FileSize:    int64(len(payload)),
	}
}

func exportIntegrationAttachmentPreparedFile(exportID foundation.ID) exportapp.PreparedFile {
	payload := []byte("attachment export integration payload")
	return exportapp.PreparedFile{
		StagingPath: ".knowledge/exports/.staging/" + string(exportID) + "-" + strings.Repeat("f", 32) + ".zip.stage",
		FinalPath:   ".knowledge/exports/" + string(exportID) + ".zip",
		FileHash:    exportIntegrationBytesHash(payload),
		FileSize:    int64(len(payload)),
	}
}

func setExportAttachmentCapability(t *testing.T, ctx context.Context, pool *pgxpool.Pool, enabled bool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE ops.export_capability SET enabled=$1,updated_at=clock_timestamp()
		WHERE capability_key='workspace-attachments' AND contract_version='workspace-attachments/v1'`, enabled); err != nil {
		t.Fatalf("set attachment export capability=%t: %v", enabled, err)
	}
}

func readExportIntegrationJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, exportID foundation.ID) domain.Job {
	t.Helper()
	job, err := scanJob(pool.QueryRow(ctx, `SELECT `+selectColumns+` FROM ops.export_job WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(exportID)))
	if err != nil {
		t.Fatalf("read export integration job: %v", err)
	}
	return job
}

func assertExportIntegrationBoundaryActive(t *testing.T, ctx context.Context, pool *pgxpool.Pool, exportID foundation.ID, boundary string) {
	t.Helper()
	var ttlRemaining float64
	var leaseRemaining float64
	if err := pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM (expires_at-clock_timestamp())),
		COALESCE(EXTRACT(EPOCH FROM (lease_expires_at-clock_timestamp())),-1)
		FROM ops.export_job WHERE id=$1`, string(exportID)).Scan(&ttlRemaining, &leaseRemaining); err != nil {
		t.Fatalf("read export boundary: %v", err)
	}
	if ttlRemaining <= 0.25 {
		t.Fatalf("export ttl fixture has only %.3fs remaining before delayed statement", ttlRemaining)
	}
	if boundary == "lease" && leaseRemaining <= 0.25 {
		t.Fatalf("export lease fixture has insufficient time before delayed statement: %v", leaseRemaining)
	}
}

func installExportIntegrationUpdateDelay(t *testing.T, ctx context.Context, pool *pgxpool.Pool, delay time.Duration) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS trg_export_update_delay_test ON ops.export_job`)
		_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS ops.delay_export_update_for_test()`)
		_, _ = pool.Exec(context.Background(), `DROP SEQUENCE IF EXISTS ops.export_update_delay_counter_test`)
	})
	if _, err := pool.Exec(ctx, `CREATE SEQUENCE ops.export_update_delay_counter_test`); err != nil {
		t.Fatalf("create export update delay counter: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION ops.delay_export_update_for_test()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF nextval('ops.export_update_delay_counter_test') = 1 THEN
				PERFORM pg_sleep(TG_ARGV[0]::double precision);
			END IF;
			RETURN NULL;
		END
		$$`); err != nil {
		t.Fatalf("create export update delay function: %v", err)
	}
	triggerSQL := `CREATE TRIGGER trg_export_update_delay_test BEFORE UPDATE ON ops.export_job
		FOR EACH STATEMENT EXECUTE FUNCTION ops.delay_export_update_for_test('1.1')`
	if delay < time.Second {
		triggerSQL = `CREATE TRIGGER trg_export_update_delay_test BEFORE UPDATE ON ops.export_job
			FOR EACH STATEMENT EXECUTE FUNCTION ops.delay_export_update_for_test('0.7')`
	}
	if _, err := pool.Exec(ctx, triggerSQL); err != nil {
		t.Fatalf("create export update delay trigger: %v", err)
	}
}

func assertExportIntegrationDelayedUpdates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int64) {
	t.Helper()
	var updates int64
	if err := pool.QueryRow(ctx, `SELECT last_value FROM ops.export_update_delay_counter_test`).Scan(&updates); err != nil {
		t.Fatalf("read export update delay count: %v", err)
	}
	if updates != want {
		t.Fatalf("export delayed UPDATE count=%d, want %d", updates, want)
	}
}

func assertExportIntegrationDatabaseBoundary(t *testing.T, ctx context.Context, pool *pgxpool.Pool, exportID foundation.ID, wantTTLExpired, wantLeaseExpired bool) {
	t.Helper()
	var ttlExpired bool
	var leaseExpired bool
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()>=expires_at,
		COALESCE(clock_timestamp()>=lease_expires_at,false)
		FROM ops.export_job WHERE id=$1`, string(exportID)).Scan(&ttlExpired, &leaseExpired); err != nil {
		t.Fatalf("read crossed export boundary: %v", err)
	}
	if ttlExpired != wantTTLExpired || leaseExpired != wantLeaseExpired {
		t.Fatalf("crossed boundary ttl=%t lease=%t, want ttl=%t lease=%t", ttlExpired, leaseExpired, wantTTLExpired, wantLeaseExpired)
	}
}

func assertExportIntegrationNoDownloadAudit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, exportID foundation.ID) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.audit_event WHERE resource_ref=$1`, "export_job:"+string(exportID)).Scan(&count); err != nil {
		t.Fatalf("count export download Audit: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired export has %d download Audit events", count)
	}
}

func assertExportDownloadProjection(t *testing.T, ctx context.Context, pool *pgxpool.Pool, exportID foundation.ID, wantVersion int64, wantCount int) {
	t.Helper()
	var version int64
	var count int
	if err := pool.QueryRow(ctx, `SELECT version,download_count FROM ops.export_job WHERE id=$1`, string(exportID)).Scan(&version, &count); err != nil {
		t.Fatalf("read download projection: %v", err)
	}
	if version != wantVersion || count != wantCount {
		t.Fatalf("download projection version=%d count=%d, want version=%d count=%d", version, count, wantVersion, wantCount)
	}
}

func assertExportDownloadAudit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, auditID, exportID foundation.ID, fileHash string) {
	t.Helper()
	var action, actorType, resourceRef, correlatedExport, persistedHash, serverOutcome string
	if err := pool.QueryRow(ctx, `SELECT action,actor_type,resource_ref,
		correlation->>'export_id',payload->>'file_hash',payload->>'server_outcome'
		FROM ops.audit_event WHERE id=$1`, string(auditID)).Scan(
		&action, &actorType, &resourceRef, &correlatedExport, &persistedHash, &serverOutcome,
	); err != nil {
		t.Fatalf("read export download Audit: %v", err)
	}
	if action != "export.download" || actorType != string(auditdomain.ActorUser) ||
		resourceRef != "export_job:"+string(exportID) || correlatedExport != string(exportID) ||
		persistedHash != fileHash || serverOutcome != "prepared_for_return" {
		t.Fatalf("download Audit action=%q actor=%q resource=%q export=%q hash=%q outcome=%q",
			action, actorType, resourceRef, correlatedExport, persistedHash, serverOutcome)
	}
	_, err := pool.Exec(ctx, `UPDATE ops.audit_event SET action='tampered' WHERE id=$1`, string(auditID))
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "55000" {
		t.Fatalf("download Audit update error=%v", err)
	}
}

func assertExportAttachmentDownloadAudit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, auditID, exportID foundation.ID, archiveHash string, entryCount int64) {
	t.Helper()
	var scopeKind, persistedHash string
	var persistedCount int64
	if err := pool.QueryRow(ctx, `SELECT payload->>'scope_kind',payload->>'file_hash',(payload->>'entry_count')::bigint
		FROM ops.audit_event WHERE id=$1`, string(auditID)).Scan(&scopeKind, &persistedHash, &persistedCount); err != nil {
		t.Fatalf("read attachment download Audit: %v", err)
	}
	if scopeKind != string(domain.ScopeWorkspaceAttachments) || persistedHash != archiveHash || persistedCount != entryCount {
		t.Fatalf("attachment download Audit scope=%q archive_hash=%q entry_count=%d", scopeKind, persistedHash, persistedCount)
	}
}

func exportIntegrationID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatalf("generate export integration ID: %v", err)
	}
	return id
}

func exportIntegrationHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func exportIntegrationBytesHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func exportIntegrationErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func exportIntegrationErrorDetail(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return fmt.Sprintf("sqlstate=%s constraint=%s table=%s detail=%s", postgresError.Code, postgresError.ConstraintName, postgresError.TableName, postgresError.Detail)
	}
	for err != nil {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err.Error()
		}
		err = unwrapped
	}
	return ""
}
