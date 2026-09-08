//go:build integration && testcontainers

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdetector "github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFactReaderEvidenceDetectorsDeduplicateAndPageByTypedKey uses multiple
// provenance rows per target, then verifies each target is emitted once and
// both object types survive a page boundary when they share one UUID.
func TestFactReaderEvidenceDetectorsDeduplicateAndPageByTypedKey(t *testing.T) {
	ctx := context.Background()
	platform := requireHealthIntegrationPlatform(t)
	pool := platform.DB()
	fixture, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := graphfixture.Cleanup(cleanupCtx, pool, foreign.WorkspaceID); err != nil {
			t.Errorf("cleanup foreign fixture: %v", err)
		}
		if err := graphfixture.Cleanup(cleanupCtx, pool, fixture.WorkspaceID); err != nil {
			t.Errorf("cleanup fixture: %v", err)
		}
	})

	foreignSourceVersion, foreignSourceSpan := loadFixtureProvenance(t, ctx, pool, foreign.WorkspaceID, foreign.FirstClaimID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	// Two cross-workspace provenance rows per target are deliberately inserted
	// under the fixture-only replica role so BROKEN_REFERENCE has two bad rows
	// to collapse without weakening production constraints.
	for index := 0; index < 2; index++ {
		insertBadClaimSource(t, ctx, tx, fixture.WorkspaceID, fixture.FirstClaimID, foreignSourceVersion, foreignSourceSpan, index)
		insertBadRelationEvidence(t, ctx, tx, fixture.WorkspaceID, fixture.SupportRelationID, foreignSourceVersion, foreignSourceSpan, index)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewGORMFactReader(platform)
	if err != nil {
		t.Fatal(err)
	}
	broken, err := reader.Find(ctx, healthdetector.DetectorBrokenReference, healthapp.PageRequest{
		Scope:     healthapp.Scope{WorkspaceID: fixture.WorkspaceID, Type: domain.ScanScopeTypeWorkspace, Ref: fixture.WorkspaceID},
		BatchSize: 100, Descriptor: availableDetectorDescriptor(healthdetector.DetectorBrokenReference),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !broken.Complete || broken.Processed != 2 {
		t.Fatalf("broken reference page processed=%d complete=%t findings=%d", broken.Processed, broken.Complete, len(broken.Findings))
	}

	prepareSupersededFixture(t, ctx, pool, fixture)
	seen := make(map[string]struct{})
	var cursor string
	pages := 0
	for {
		page, findErr := reader.Find(ctx, healthdetector.DetectorSupersededUsage, healthapp.PageRequest{
			Scope:  healthapp.Scope{WorkspaceID: fixture.WorkspaceID, Type: domain.ScanScopeTypeWorkspace, Ref: fixture.WorkspaceID},
			Cursor: cursor, BatchSize: 1, Descriptor: availableDetectorDescriptor(healthdetector.DetectorSupersededUsage),
		})
		if findErr != nil {
			t.Fatal(findErr)
		}
		pages++
		if page.Processed != int64(len(page.Findings)) || page.Processed > 1 {
			t.Fatalf("invalid superseded page: %#v", page)
		}
		for _, finding := range page.Findings {
			key := string(finding.Target.Type) + ":" + string(finding.Target.ID)
			if _, exists := seen[key]; exists {
				t.Fatalf("duplicate finding across pages: %s", key)
			}
			seen[key] = struct{}{}
		}
		if page.Complete {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatalf("superseded cursor did not advance: previous=%q next=%q", cursor, page.NextCursor)
		}
		cursor = page.NextCursor
	}
	if pages < 2 {
		t.Fatalf("superseded fixture did not cross a page boundary: pages=%d", pages)
	}
	if _, ok := seen[string(domain.ObjectTypeClaim)+":"+string(fixture.FirstClaimID)]; !ok {
		t.Fatalf("claim with shared UUID missing from findings: %v", seen)
	}
	if _, ok := seen[string(domain.ObjectTypeRelation)+":"+string(fixture.FirstClaimID)]; !ok {
		t.Fatalf("relation with shared UUID missing from findings: %v", seen)
	}
	if len(seen) != 7 {
		t.Fatalf("deduplicated superseded target count=%d want=7: %v", len(seen), seen)
	}
}

func loadFixtureProvenance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, claimID foundation.ID) (string, string) {
	t.Helper()
	var sourceVersion, sourceSpan string
	if err := pool.QueryRow(ctx, `SELECT cs.source_version_id::text,cs.source_span_id::text
FROM core.claim_source cs
JOIN core.source_version sv ON sv.id=cs.source_version_id
JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=$1
WHERE cs.workspace_id=$1 AND cs.claim_id=$2
ORDER BY cs.id LIMIT 1`, string(workspaceID), string(claimID)).Scan(&sourceVersion, &sourceSpan); err != nil {
		t.Fatal(err)
	}
	return sourceVersion, sourceSpan
}

func insertBadClaimSource(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, claimID foundation.ID, sourceVersion, sourceSpan string, index int) {
	t.Helper()
	_, err := tx.Exec(ctx, `INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
VALUES($1,$2,$3,$4,$5,'SUPPORTS',$6,$7,$8)`, testDetectorID(t), string(workspaceID), string(claimID), sourceVersion, sourceSpan, "cross workspace broken provenance", strings.Repeat(fmt.Sprintf("%x", index+1), 64)[:64], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
}

func insertBadRelationEvidence(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, relationID foundation.ID, sourceVersion, sourceSpan string, index int) {
	t.Helper()
	_, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,'{}','knowledge-applicability/v1',$8,$9)`, testDetectorID(t), string(workspaceID), string(relationID), sourceVersion, sourceSpan, "cross workspace broken provenance", strings.Repeat(fmt.Sprintf("%x", index+11), 64)[:64], strings.Repeat(fmt.Sprintf("%x", index+21), 64)[:64], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
}

func prepareSupersededFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture graphfixture.Fixture) {
	t.Helper()
	oldSourceVersion, oldSourceSpan := loadFixtureProvenance(t, ctx, pool, fixture.WorkspaceID, fixture.FirstClaimID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	var sourceID, artifactID string
	var byteSize int64
	var mimeType, location, security string
	if err := tx.QueryRow(ctx, `SELECT sv.source_id::text,sv.content_artifact_id::text,sv.byte_size,sv.mime_type,sv.original_content_location,sv.security_status
FROM core.source_version sv WHERE sv.id=$1`, oldSourceVersion).Scan(&sourceID, &artifactID, &byteSize, &mimeType, &location, &security); err != nil {
		t.Fatal(err)
	}
	newSourceVersion := testDetectorID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, newSourceVersion, sourceID, string(fixture.WorkspaceID), artifactID, strings.Repeat("9", 64), byteSize, mimeType, location, security, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		insertOldClaimSource(t, ctx, tx, fixture.WorkspaceID, fixture.FirstClaimID, oldSourceVersion, oldSourceSpan, index)
		insertOldRelationEvidence(t, ctx, tx, fixture.WorkspaceID, fixture.SupportRelationID, oldSourceVersion, oldSourceSpan, index)
	}
	// This relation deliberately reuses the claim UUID. The typed cursor must
	// still return both rows rather than treating the UUID as globally unique.
	sharedRelationID := string(fixture.FirstClaimID)
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,version,created_at,updated_at)
VALUES($1,$2,'CLAIM',$3,'TOPIC',$4,'CITES','SUGGESTED',0.5,$5,1,$6,$6)`, sharedRelationID, string(fixture.WorkspaceID), string(fixture.FirstClaimID), string(fixture.SecondaryTopicID), strings.Repeat("d", 64), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		insertOldRelationEvidence(t, ctx, tx, fixture.WorkspaceID, foundation.ID(sharedRelationID), oldSourceVersion, oldSourceSpan, index+10)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertOldClaimSource(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, claimID foundation.ID, sourceVersion, sourceSpan string, index int) {
	insertID := testDetectorID(t)
	_, err := tx.Exec(ctx, `INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
VALUES($1,$2,$3,$4,$5,'SUPPORTS','duplicate old evidence',$6,$7)`, insertID, string(workspaceID), string(claimID), sourceVersion, sourceSpan, strings.Repeat(fmt.Sprintf("%x", index+31), 64)[:64], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
}

func insertOldRelationEvidence(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, relationID foundation.ID, sourceVersion, sourceSpan string, index int) {
	insertID := testDetectorID(t)
	_, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,created_at)
VALUES($1,$2,$3,$4,$5,'duplicate old evidence',$6,'{}','knowledge-applicability/v1',$7,$8)`, insertID, string(workspaceID), string(relationID), sourceVersion, sourceSpan, strings.Repeat(fmt.Sprintf("%x", index+41), 64)[:64], strings.Repeat(fmt.Sprintf("%x", index+51), 64)[:64], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
}

func testDetectorID(t *testing.T) string {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return string(id)
}
