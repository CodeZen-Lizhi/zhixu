//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const detectorPageFixedStatementCount = 7

type detectorPageQueryTracer struct {
	mu         sync.Mutex
	statements []string
}

func (tracer *detectorPageQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer.mu.Lock()
	tracer.statements = append(tracer.statements, strings.TrimSpace(data.SQL))
	tracer.mu.Unlock()
	return ctx
}

func (*detectorPageQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tracer *detectorPageQueryTracer) reset() {
	tracer.mu.Lock()
	tracer.statements = nil
	tracer.mu.Unlock()
}

func (tracer *detectorPageQueryTracer) snapshot() []string {
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	return append([]string(nil), tracer.statements...)
}

func TestIssueRepositoryReconcileDetectorPageUsesFixedStatementCount(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	for caseIndex, observationCount := range []int{1, 25, 100} {
		t.Run(fmt.Sprintf("observations_%d", observationCount), func(t *testing.T) {
			ctx := context.Background()
			config, err := pgxpool.ParseConfig(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			tracer := &detectorPageQueryTracer{}
			config.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			workspaceID := detectorPageID(0x81000000+caseIndex, 1)
			scanID := detectorPageID(0x82000000+caseIndex, 1)
			cleanupHealthIntegrationWorkspace(t, pool, workspaceID)
			t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
			now := time.Now().UTC().Truncate(time.Microsecond)
			seedDetectorPageScan(t, ctx, pool, workspaceID, scanID, detectorPageID(0x82000000+caseIndex, 2), detectorPageID(0x82000000+caseIndex, 3), now)

			observations := make([]domain.IssueObservation, 0, observationCount)
			seedRepository, err := NewIssueRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			expected := healthapp.DetectorPageReconcileResult{}
			unchangedIssueIDs := make([]foundation.ID, 0, observationCount/3)
			for index := range observationCount {
				topicID := detectorPageID(0x83000000+caseIndex, index+1)
				seedDetectorPageTopic(t, ctx, pool, workspaceID, topicID, index, now)
				observation := detectorPageObservationFixture(topicID, index)
				observations = append(observations, observation)
				switch index % 3 {
				case 0:
					expected.Created++
				case 1:
					seedID := detectorPageID(0x84000000+caseIndex, index+1)
					if _, _, err := seedRepository.UpsertObservation(ctx, workspaceID, scanID, seedID, observation, nil, now.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
					unchangedIssueIDs = append(unchangedIssueIDs, seedID)
					expected.Unchanged++
				case 2:
					seedID := detectorPageID(0x84000000+caseIndex, index+1)
					seeded, _, err := seedRepository.UpsertObservation(ctx, workspaceID, scanID, seedID, observation, nil, now.Add(time.Second))
					if err != nil {
						t.Fatal(err)
					}
					resolvedAt := now.Add(2 * time.Second)
					if tag, err := pool.Exec(ctx, `UPDATE ops.health_issue SET status='RESOLVED',resolved_at=$3,last_verified_at=$3,updated_at=$3,version=version+1 WHERE workspace_id=$1 AND id=$2 AND version=$4`, string(workspaceID), string(seeded.ID), resolvedAt, seeded.Version); err != nil || tag.RowsAffected() != 1 {
						t.Fatalf("resolve seed issue rows=%d err=%v", tag.RowsAffected(), err)
					}
					changed := observation
					changed.DetectorVersion = "detector/v2"
					observations[len(observations)-1] = changed
					expected.Reopened++
				}
			}
			repository, err := NewIssueRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			tracer.reset()
			result, err := repository.ReconcileDetectorPage(ctx, healthapp.DetectorPageReconcileRequest{
				WorkspaceID: workspaceID, ScanID: scanID, DetectorID: "health.detector.missing_source",
				Observations: observations, ObservedAt: now.Add(3 * time.Second),
			})
			statements := tracer.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if result != expected {
				t.Fatalf("result=%+v expected=%+v", result, expected)
			}
			for _, issueID := range unchangedIssueIDs {
				var version, observationRows, evidenceRows int64
				var lastDetectedAt, lastVerifiedAt, updatedAt time.Time
				if err := pool.QueryRow(ctx, `SELECT issue.version,issue.last_detected_at,issue.last_verified_at,issue.updated_at,
	(SELECT count(*) FROM ops.health_issue_observation WHERE workspace_id=issue.workspace_id AND issue_id=issue.id),
	(SELECT count(*) FROM ops.health_issue_evidence evidence JOIN ops.health_issue_observation observation ON observation.id=evidence.observation_id WHERE observation.workspace_id=issue.workspace_id AND observation.issue_id=issue.id)
FROM ops.health_issue issue WHERE issue.workspace_id=$1 AND issue.id=$2`, string(workspaceID), string(issueID)).Scan(&version, &lastDetectedAt, &lastVerifiedAt, &updatedAt, &observationRows, &evidenceRows); err != nil {
					t.Fatal(err)
				}
				if version != 1 || !lastDetectedAt.Equal(now.Add(time.Second)) || !lastVerifiedAt.Equal(now.Add(3*time.Second)) || !updatedAt.Equal(now.Add(time.Second)) || observationRows != 1 || evidenceRows != 2 {
					t.Fatalf("unchanged issue=%s version=%d last_detected=%s last_verified=%s updated=%s observations=%d evidence=%d", issueID, version, lastDetectedAt, lastVerifiedAt, updatedAt, observationRows, evidenceRows)
				}
			}
			if len(statements) != detectorPageFixedStatementCount {
				t.Fatalf("observations=%d statements=%d expected=%d\n%s", observationCount, len(statements), detectorPageFixedStatementCount, strings.Join(statements, "\n---\n"))
			}
			assertDetectorPageStatementShape(t, statements)
			t.Logf("observations=%d evidence=%d statements=%d created=%d reopened=%d unchanged=%d", observationCount, observationCount*2, len(statements), result.Created, result.Reopened, result.Unchanged)
		})
	}
}

func TestIssueRepositoryReconcileDetectorPageDuplicateReopenUsesOriginalVersionCAS(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	workspaceID := detectorPageID(0x8a000000, 1)
	scanID := detectorPageID(0x8a000000, 2)
	topicID := detectorPageID(0x8a000000, 3)
	cleanupHealthIntegrationWorkspace(t, pool, workspaceID)
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedDetectorPageScan(t, ctx, pool, workspaceID, scanID, detectorPageID(0x8a000000, 4), detectorPageID(0x8a000000, 5), now)
	seedDetectorPageTopic(t, ctx, pool, workspaceID, topicID, 0, now)
	seedRepository, err := NewIssueRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	initial := detectorPageObservationFixture(topicID, 0)
	if _, _, err := seedRepository.UpsertObservation(ctx, workspaceID, scanID, detectorPageID(0x8a000000, 6), initial, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	changed := initial
	changed.DetectorVersion = "detector/v2"
	repository, err := NewIssueRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.ReconcileDetectorPage(ctx, healthapp.DetectorPageReconcileRequest{
		WorkspaceID: workspaceID, ScanID: scanID, DetectorID: initial.DetectorID,
		Observations: []domain.IssueObservation{changed, changed}, ObservedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Reopened != 1 || result.Unchanged != 1 {
		t.Fatalf("result=%+v", result)
	}
	var version, observations int64
	var fingerprint string
	var lastDetected time.Time
	if err := pool.QueryRow(ctx, `SELECT issue.version,issue.fingerprint,issue.last_detected_at,
 (SELECT count(*) FROM ops.health_issue_observation WHERE workspace_id=issue.workspace_id AND issue_id=issue.id)
FROM ops.health_issue issue WHERE issue.workspace_id=$1 AND issue.target_id=$2`, string(workspaceID), string(topicID)).Scan(&version, &fingerprint, &lastDetected, &observations); err != nil {
		t.Fatal(err)
	}
	wantFingerprint, err := domain.ComputeIssueFingerprint(workspaceID, changed)
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 || fingerprint != wantFingerprint || observations != 2 || !lastDetected.Equal(now.Add(2*time.Second)) {
		t.Fatalf("version=%d fingerprint=%s observations=%d last_detected=%s", version, fingerprint, observations, lastDetected)
	}
}

func assertDetectorPageStatementShape(t *testing.T, statements []string) {
	t.Helper()
	expectedFragments := []string{
		"begin", "for update of scan,coverage", "unnest($4::text[])", "with locked as materialized",
		"jsonb_to_recordset($1::jsonb)", "inserted_observations as", "commit",
	}
	if len(statements) != len(expectedFragments) {
		t.Fatalf("statement shape count=%d", len(statements))
	}
	for index, fragment := range expectedFragments {
		if !strings.Contains(strings.ToLower(statements[index]), fragment) {
			t.Fatalf("statement %d does not contain %q: %s", index+1, fragment, statements[index])
		}
	}
}

type detectorPageUpsertBarrier struct {
	enabled atomic.Bool
	arrived atomic.Int32
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (barrier *detectorPageUpsertBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if barrier.enabled.Load() && strings.Contains(data.SQL, "INSERT INTO ops.health_issue(") && strings.Contains(data.SQL, "expected_version=0") {
		if barrier.arrived.Add(1) == 2 {
			barrier.once.Do(func() { close(barrier.ready) })
		}
		select {
		case <-barrier.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (*detectorPageUpsertBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestIssueRepositoryConcurrentNewIdentityRollsBackLosingPage(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	barrier := &detectorPageUpsertBarrier{ready: make(chan struct{}), release: make(chan struct{})}
	config.ConnConfig.Tracer = barrier
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	workspaceID := detectorPageID(0x85000000, 1)
	firstScanID := detectorPageID(0x85000000, 2)
	secondScanID := detectorPageID(0x85000000, 3)
	cleanupHealthIntegrationWorkspace(t, pool, workspaceID)
	t.Cleanup(func() { cleanupHealthIntegrationWorkspace(t, pool, workspaceID) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedDetectorPageScan(t, ctx, pool, workspaceID, firstScanID, detectorPageID(0x85000000, 4), detectorPageID(0x85000000, 5), now)
	seedDetectorPageScanOnly(t, ctx, pool, workspaceID, secondScanID, detectorPageID(0x85000000, 6), detectorPageID(0x85000000, 7), now)
	topicID := detectorPageID(0x85000000, 8)
	seedDetectorPageTopic(t, ctx, pool, workspaceID, topicID, 0, now)
	observation := detectorPageObservationFixture(topicID, 0)
	barrier.enabled.Store(true)

	type pageResult struct {
		result healthapp.DetectorPageReconcileResult
		err    error
	}
	results := make(chan pageResult, 2)
	for _, scanID := range []foundation.ID{firstScanID, secondScanID} {
		go func(scanID foundation.ID) {
			repository, repositoryErr := NewIssueRepository(pool)
			if repositoryErr != nil {
				results <- pageResult{err: repositoryErr}
				return
			}
			result, reconcileErr := repository.ReconcileDetectorPage(ctx, healthapp.DetectorPageReconcileRequest{
				WorkspaceID: workspaceID, ScanID: scanID, DetectorID: "health.detector.missing_source",
				Observations: []domain.IssueObservation{observation}, ObservedAt: now.Add(time.Second),
			})
			results <- pageResult{result: result, err: reconcileErr}
		}(scanID)
	}
	select {
	case <-barrier.ready:
	case <-ctx.Done():
		t.Fatal("concurrent page upserts did not both reach the barrier")
	}
	close(barrier.release)
	first, second := <-results, <-results
	successes, conflicts := 0, 0
	for _, value := range []pageResult{first, second} {
		if value.err == nil {
			if value.result.Created != 1 {
				t.Fatalf("winning result=%+v", value.result)
			}
			successes++
			continue
		}
		var classified *foundation.Error
		if !errors.As(value.err, &classified) || !classified.Retryable {
			t.Fatalf("losing page error=%v", value.err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var issues, observations, evidence, seen int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ops.health_issue WHERE workspace_id=$1),
		(SELECT count(*) FROM ops.health_issue_observation WHERE workspace_id=$1),
		(SELECT count(*) FROM ops.health_issue_evidence WHERE workspace_id=$1),
		(SELECT count(*) FROM ops.health_scan_seen_identity WHERE workspace_id=$1)`, string(workspaceID)).Scan(&issues, &observations, &evidence, &seen); err != nil {
		t.Fatal(err)
	}
	if issues != 1 || observations != 1 || evidence != 2 || seen != 1 {
		t.Fatalf("issues=%d observations=%d evidence=%d seen=%d", issues, observations, evidence, seen)
	}
	t.Logf("concurrent new identity: successes=%d retryable_conflicts=%d issues=%d observations=%d evidence=%d seen=%d", successes, conflicts, issues, observations, evidence, seen)
}

func seedDetectorPageScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, scanID, definitionID, runID foundation.ID, now time.Time) {
	t.Helper()
	root := "/tmp/health-page-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(workspaceID), "health page "+string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	seedDetectorPageScanOnly(t, ctx, pool, workspaceID, scanID, definitionID, runID, now)
}

func seedDetectorPageScanOnly(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, scanID, definitionID, runID foundation.ID, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,'{"nodes":[]}',$4)`, string(definitionID), string(workspaceID), "health-page-"+string(scanID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(workspaceID), string(definitionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan(id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at) VALUES($1,$2,'WORKSPACE',$2,1,'health-scope/workspace/v1',$3,$4,$5,$6,500,'RUNNING',1,$7,$7)`, string(scanID), string(workspaceID), detectorPageDigest("fingerprint", scanID), "health-page-"+string(scanID), detectorPageDigest("request", scanID), string(runID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan_detector(scan_id,workspace_id,detector_id,detector_version,status,checkpoint,processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count) VALUES($1,$2,'health.detector.missing_source','detector/v1','PENDING','{}',0,0,0,0,0,0)`, string(scanID), string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	if tag, err := pool.Exec(ctx, `UPDATE ops.health_scan_detector SET status='RUNNING',started_at=$3 WHERE scan_id=$1 AND workspace_id=$2 AND detector_id='health.detector.missing_source' AND status='PENDING'`, string(scanID), string(workspaceID), now); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("start detector coverage rows=%d err=%v", tag.RowsAffected(), err)
	}
}

func seedDetectorPageTopic(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, topicID foundation.ID, index int, now time.Time) {
	t.Helper()
	name := fmt.Sprintf("health page topic %d %s", index, topicID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,'','ACTIVE',1,$4,$4)`, string(topicID), string(workspaceID), name, now); err != nil {
		t.Fatal(err)
	}
}

func detectorPageObservationFixture(topicID foundation.ID, index int) domain.IssueObservation {
	ref := domain.ObjectRef{Type: domain.ObjectTypeTopic, ID: topicID}
	firstHash := fmt.Sprintf("%064x", index*2+1)
	secondHash := fmt.Sprintf("%064x", index*2+2)
	return domain.IssueObservation{
		Type: domain.IssueTypeMissingSource, Target: ref, DetectorID: "health.detector.missing_source",
		DetectorVersion: "detector/v1", Severity: domain.SeverityHigh,
		EvidenceSummary: "topic is missing a supporting source",
		Evidence: []domain.IssueEvidence{
			{Ref: ref, Hash: firstHash, Summary: "topic has no supporting source"},
			{Ref: ref, Hash: secondHash, Summary: "topic provenance remains unbound"},
		},
		ObjectVersions: []domain.ObjectVersion{{Ref: ref, Version: 1}},
	}
}

func detectorPageID(prefix, value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("%08x-0000-4000-8000-%012x", prefix, value))
}

func detectorPageDigest(namespace string, value foundation.ID) string {
	digest := sha256.Sum256([]byte(namespace + "\n" + string(value)))
	return hex.EncodeToString(digest[:])
}
