//go:build integration && testcontainers

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
	healthdetector "github.com/CodeZen-Lizhi/zhixu/internal/health/detector"
	"github.com/CodeZen-Lizhi/zhixu/internal/health/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
)

func TestTopicScopeReaderAndMissingSetUseConfirmedMembership(t *testing.T) {
	ctx := context.Background()
	pool := newHealthIntegrationPool(t)
	fixture, err := graphfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if cleanupErr := graphfixture.Cleanup(cleanupCtx, pool, fixture.WorkspaceID); cleanupErr != nil {
			t.Errorf("cleanup graph fixture: %v", cleanupErr)
		}
	})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := tx.Exec(ctx, `UPDATE core.claim
SET confidence_score=0.2,version=version+1,updated_at=$3
WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(fixture.FirstClaimID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.relation
SET confidence_score=0.2,version=version+1,updated_at=$3
WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(fixture.MembershipRelationID), now); err != nil {
		t.Fatal(err)
	}
	orphanClaimID := seedConfirmedClaimWithSuggestedMembership(t, ctx, tx, fixture, now)
	isolatedTopicID := seedIsolatedTopic(t, ctx, tx, fixture.WorkspaceID, now)

	reader, err := NewFactReader(tx)
	if err != nil {
		t.Fatal(err)
	}
	primaryLowConfidence := readTopicDetectorPage(t, ctx, reader, healthdetector.DetectorLowConfidence, fixture.WorkspaceID, fixture.PrimaryTopicID, 0.5)
	assertFinding(t, primaryLowConfidence, domain.ObjectTypeClaim, fixture.FirstClaimID, true)
	assertFinding(t, primaryLowConfidence, domain.ObjectTypeRelation, fixture.MembershipRelationID, true)
	assertFinding(t, primaryLowConfidence, domain.ObjectTypeClaim, orphanClaimID, false)

	secondaryLowConfidence := readTopicDetectorPage(t, ctx, reader, healthdetector.DetectorLowConfidence, fixture.WorkspaceID, fixture.SecondaryTopicID, 0.5)
	assertFinding(t, secondaryLowConfidence, domain.ObjectTypeClaim, fixture.FirstClaimID, false)
	assertFinding(t, secondaryLowConfidence, domain.ObjectTypeRelation, fixture.MembershipRelationID, false)

	primaryOrphans := readTopicDetectorPage(t, ctx, reader, healthdetector.DetectorOrphan, fixture.WorkspaceID, fixture.PrimaryTopicID, 0)
	assertFinding(t, primaryOrphans, domain.ObjectTypeClaim, orphanClaimID, false)
	isolatedOrphans := readTopicDetectorPage(t, ctx, reader, healthdetector.DetectorOrphan, fixture.WorkspaceID, isolatedTopicID, 0)
	assertFinding(t, isolatedOrphans, domain.ObjectTypeTopic, isolatedTopicID, true)

	scanID := seedTopicScopeScan(t, ctx, tx, fixture.WorkspaceID, fixture.PrimaryTopicID, now)
	repository, err := NewIssueRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	issues := []struct {
		id      foundation.ID
		target  domain.ObjectRef
		version int64
		want    domain.IssueStatus
	}{
		{mustTopicScopeID(t), domain.ObjectRef{Type: domain.ObjectTypeTopic, ID: fixture.PrimaryTopicID}, 1, domain.IssueStatusResolved},
		{mustTopicScopeID(t), domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: fixture.FirstClaimID}, 3, domain.IssueStatusResolved},
		{mustTopicScopeID(t), domain.ObjectRef{Type: domain.ObjectTypeClaim, ID: orphanClaimID}, 2, domain.IssueStatusOpen},
		{mustTopicScopeID(t), domain.ObjectRef{Type: domain.ObjectTypeTopic, ID: fixture.SecondaryTopicID}, 1, domain.IssueStatusOpen},
	}
	for index, item := range issues {
		observation := topicScopeOrphanObservation(item.target, item.version, index)
		if _, _, err := repository.UpsertObservation(ctx, fixture.WorkspaceID, scanID, item.id, observation, nil, now.Add(time.Duration(index+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := repository.ResolveMissingForCompleteScan(ctx, fixture.WorkspaceID, scanID, healthdetector.DetectorOrphan)
	if err != nil || resolved != 2 {
		t.Fatalf("resolved=%d err=%v", resolved, err)
	}
	for _, item := range issues {
		issue, loadErr := repository.GetIssue(ctx, fixture.WorkspaceID, item.id, nil)
		if loadErr != nil || issue.Status != item.want {
			t.Fatalf("issue %s status=%s want=%s err=%v", item.id, issue.Status, item.want, loadErr)
		}
	}
	var scanResolved, orphanResolved, indexResolved int64
	if err := tx.QueryRow(ctx, `SELECT scan.resolved_count,
  (SELECT resolved_count FROM ops.health_scan_detector WHERE scan_id=scan.id AND detector_id=$2),
  (SELECT resolved_count FROM ops.health_scan_detector WHERE scan_id=scan.id AND detector_id=$3)
FROM ops.health_scan scan WHERE scan.id=$1`, string(scanID), healthdetector.DetectorOrphan, healthdetector.DetectorIndexError).Scan(&scanResolved, &orphanResolved, &indexResolved); err != nil {
		t.Fatal(err)
	}
	if scanResolved != 2 || orphanResolved != 2 || indexResolved != 0 {
		t.Fatalf("resolved counters scan=%d orphan=%d index=%d", scanResolved, orphanResolved, indexResolved)
	}
	if _, err := repository.ResolveMissingForCompleteScan(ctx, fixture.WorkspaceID, scanID, healthdetector.DetectorIndexError); !errors.Is(err, healthapp.ErrDetectorUnavailable) {
		t.Fatalf("index detector topic missing-set error = %v", err)
	}
}

func readTopicDetectorPage(t *testing.T, ctx context.Context, reader *FactReader, detectorID string, workspaceID, topicID foundation.ID, threshold float64) healthapp.Page {
	t.Helper()
	page, err := reader.Find(ctx, detectorID, healthapp.PageRequest{
		Scope:                  healthapp.Scope{WorkspaceID: workspaceID, Type: domain.ScanScopeTypeTopic, Ref: topicID, Version: 1},
		BatchSize:              domain.DefaultDetectorBatchSize,
		Descriptor:             availableDetectorDescriptor(detectorID),
		LowConfidenceThreshold: threshold,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Complete || page.NextCursor != "" || page.Processed != int64(len(page.Findings)) {
		t.Fatalf("invalid detector page: %#v", page)
	}
	return page
}

func assertFinding(t *testing.T, page healthapp.Page, targetType domain.ObjectType, targetID foundation.ID, want bool) {
	t.Helper()
	found := false
	for _, finding := range page.Findings {
		if finding.Target.Type == targetType && finding.Target.ID == targetID {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("finding %s/%s found=%t want=%t page=%#v", targetType, targetID, found, want, page)
	}
}

func seedIsolatedTopic(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, now time.Time) foundation.ID {
	t.Helper()
	topicID := mustTopicScopeID(t)
	display, normalized, err := knowledge.NormalizeTopicText("Health Isolated Topic " + string(topicID))
	if err != nil {
		t.Fatal(err)
	}
	topic := knowledge.Topic{ID: topicID, WorkspaceID: workspaceID, Name: display, NormalizedName: normalized, Description: "", Status: knowledge.TopicStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := knowledge.ValidateTopic(topic); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.topic(
id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(topic.ID), string(topic.WorkspaceID), topic.Name, topic.NormalizedName, topic.Description, string(topic.Status), topic.Version, topic.CreatedAt, topic.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	return topicID
}

func seedConfirmedClaimWithSuggestedMembership(t *testing.T, ctx context.Context, tx pgx.Tx, fixture graphfixture.Fixture, now time.Time) foundation.ID {
	t.Helper()
	claimID, sourceID, relationID := mustTopicScopeID(t), mustTopicScopeID(t), mustTopicScopeID(t)
	statement, normalized, err := knowledge.NormalizeStatement("Health topic scope excludes suggested membership " + string(claimID))
	if err != nil {
		t.Fatal(err)
	}
	applicability, err := knowledge.ParseApplicability(json.RawMessage(`{"scope":"health-topic-scope"}`))
	if err != nil {
		t.Fatal(err)
	}
	factors, err := knowledge.NormalizeConfidenceFactors(json.RawMessage(`{"source":"topic-scope-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	confidence := 0.2
	claim := knowledge.Claim{
		ID: claimID, WorkspaceID: fixture.WorkspaceID, Statement: statement, NormalizedStatement: normalized,
		Applicability: applicability, Status: knowledge.ClaimStatusSuggested, ConfidenceScore: &confidence,
		ConfidenceFactors: factors, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	claim.Fingerprint = knowledge.ComputeClaimFingerprint(claim.WorkspaceID, claim.NormalizedStatement, claim.Applicability)
	if err := knowledge.ValidateClaim(claim); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim(
id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,
status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		string(claim.ID), string(claim.WorkspaceID), claim.Statement, claim.NormalizedStatement,
		string(claim.Applicability.CanonicalJSON), claim.Applicability.SchemaVersion, claim.Applicability.Hash,
		string(claim.Status), claim.ConfidenceScore, string(claim.ConfidenceFactors), claim.Fingerprint,
		claim.Version, claim.CreatedAt, claim.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	var sourceVersionID, sourceSpanID string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text
FROM core.claim_source WHERE workspace_id=$1 AND claim_id=$2 AND support_type='SUPPORTS'
ORDER BY created_at,id LIMIT 1`, string(fixture.WorkspaceID), string(fixture.FirstClaimID)).Scan(&sourceVersionID, &sourceSpanID); err != nil {
		t.Fatal(err)
	}
	reason, err := knowledge.NormalizeReason("topic scope membership fixture provenance", true)
	if err != nil {
		t.Fatal(err)
	}
	source := knowledge.ClaimSource{
		ID: sourceID, WorkspaceID: fixture.WorkspaceID, ClaimID: claim.ID,
		Provenance:  knowledge.ProvenanceRef{WorkspaceID: fixture.WorkspaceID, SourceVersionID: foundation.ID(sourceVersionID), SourceSpanID: foundation.ID(sourceSpanID)},
		SupportType: knowledge.ClaimSupportSupports, Reason: reason, CreatedAt: now.Add(time.Microsecond),
	}
	source.EvidenceHash = knowledge.ComputeClaimSourceEvidenceHash(source, claim.Applicability)
	if err := knowledge.ValidateClaimSource(source, claim.Applicability); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(
id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(source.ID), string(source.WorkspaceID), string(source.ClaimID), string(source.Provenance.SourceVersionID), string(source.Provenance.SourceSpanID), string(source.SupportType), source.Reason, source.EvidenceHash, source.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(claim.ID), now.Add(2*time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	relationConfidence := 0.9
	relation := knowledge.Relation{
		ID: relationID, WorkspaceID: fixture.WorkspaceID,
		Source: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: claim.ID},
		Target: knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.PrimaryTopicID},
		Type:   knowledge.RelationBelongsTo, Status: knowledge.RelationStatusSuggested,
		ConfidenceScore: &relationConfidence, Version: 1, CreatedAt: now.Add(3 * time.Microsecond), UpdatedAt: now.Add(3 * time.Microsecond),
	}
	relation.Fingerprint = knowledge.ComputeRelationFingerprint(relation.WorkspaceID, relation.Type, relation.Source, relation.Target)
	if err := knowledge.ValidateRelation(relation); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(
id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,
confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,NULL,NULL,$11,$12,$13)`,
		string(relation.ID), string(relation.WorkspaceID), string(relation.Source.Type), string(relation.Source.ID),
		string(relation.Target.Type), string(relation.Target.ID), string(relation.Type), string(relation.Status),
		relation.ConfidenceScore, relation.Fingerprint, relation.Version, relation.CreatedAt, relation.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	return claim.ID
}

func seedTopicScopeScan(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, topicID foundation.ID, now time.Time) foundation.ID {
	t.Helper()
	definitionID, runID, scanID := mustTopicScopeID(t), mustTopicScopeID(t), mustTopicScopeID(t)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,'health-topic-scope-test',1,'{"nodes":[]}',$3)`, []any{string(definitionID), string(workspaceID), now}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, []any{string(runID), string(workspaceID), string(definitionID), now}},
		{`INSERT INTO ops.health_scan(
id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,fingerprint,idempotency_key,request_hash,
workflow_run_id,max_items,status,version,created_at,updated_at)
VALUES($1,$2,'TOPIC',$3,1,'health-scope/topic/v1',$4,'health-topic-scope-test',$5,$6,100,'RUNNING',1,$7,$7)`, []any{string(scanID), string(workspaceID), string(topicID), topicScopeHash("scan-fingerprint"), topicScopeHash("scan-request"), string(runID), now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, detectorID := range []string{healthdetector.DetectorOrphan, healthdetector.DetectorIndexError} {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.health_scan_detector(
scan_id,workspace_id,detector_id,detector_version,status,checkpoint,
processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count)
VALUES($1,$2,$3,$4,'PENDING','{}',0,0,0,0,0,0)`, string(scanID), string(workspaceID), detectorID, healthdetector.DefaultDetectorVersion); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector SET status='RUNNING',started_at=$4
WHERE scan_id=$1 AND workspace_id=$2 AND detector_id=$3`, string(scanID), string(workspaceID), detectorID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE ops.health_scan_detector SET status='SUCCEEDED',completed_at=$4
WHERE scan_id=$1 AND workspace_id=$2 AND detector_id=$3`, string(scanID), string(workspaceID), detectorID, now); err != nil {
			t.Fatal(err)
		}
	}
	return scanID
}

func topicScopeOrphanObservation(target domain.ObjectRef, version int64, index int) domain.IssueObservation {
	summary := "topic scope orphan observation"
	hash := topicScopeHash(string(target.Type) + ":" + string(target.ID) + ":" + string(rune('a'+index)))
	return domain.IssueObservation{
		Type: domain.IssueTypeOrphan, Target: target, DetectorID: healthdetector.DetectorOrphan,
		DetectorVersion: healthdetector.DefaultDetectorVersion, Severity: domain.SeverityMedium,
		EvidenceSummary: summary, Evidence: []domain.IssueEvidence{{Ref: target, Hash: hash, Summary: summary}},
		ObjectVersions: []domain.ObjectVersion{{Ref: target, Version: version}},
	}
}

func mustTopicScopeID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func topicScopeHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
