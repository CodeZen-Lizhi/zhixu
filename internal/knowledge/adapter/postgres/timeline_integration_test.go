//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphtestfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	knowledgeaudit "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/audit"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

type timelineIntegrationRepository interface {
	domain.Repository
	knowledgeapp.EvidenceTopicRepository
	knowledgeapp.TimelineReader
	knowledgeapp.EventProjector
	knowledgeapp.ImpactRepository
	knowledgeapp.TimelineProjectionPort
}

type timelineImpactAuditCollaborator interface {
	knowledgeapp.ImpactAuditPort
	knowledgeapp.ScopedImpactAuditPort
}

type timelineIntegrationVariant struct {
	open          func(*testing.T, *platformpostgres.Pool) timelineIntegrationRepository
	openAudit     func(*testing.T, *platformpostgres.Pool) timelineImpactAuditCollaborator
	saveWithAudit func(context.Context, timelineIntegrationRepository, domain.ImpactReport, string, timelineImpactAuditCollaborator) (domain.ImpactReport, bool, error)
}

type timelineIntegrationCase struct {
	repository timelineIntegrationRepository
	pool       *pgxpool.Pool
	platform   *platformpostgres.Pool
	ctx        context.Context
	variant    timelineIntegrationVariant
	audit      timelineImpactAuditCollaborator
}

func TestTimelineImpactProjectionLegacyIntegration(t *testing.T) {
	runTimelineIntegrationVariant(t, timelineIntegrationVariant{
		open:          openLegacyTimelineIntegrationRepository,
		openAudit:     openLegacyTimelineImpactAudit,
		saveWithAudit: saveLegacyTimelineImpactWithAudit,
	})
}

func TestTimelineImpactProjectionGORMIntegration(t *testing.T) {
	runTimelineIntegrationVariant(t, timelineIntegrationVariant{
		open:          openGORMTimelineIntegrationRepository,
		openAudit:     openGORMTimelineImpactAudit,
		saveWithAudit: saveGORMTimelineImpactWithAudit,
	})
}

func runTimelineIntegrationVariant(t *testing.T, variant timelineIntegrationVariant) {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         16,
	})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("Knowledge Timeline fixture did not provide a shared platform pool")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	testCase := timelineIntegrationCase{
		repository: variant.open(t, platform),
		pool:       platform.DB(),
		platform:   platform,
		ctx:        ctx,
		variant:    variant,
		audit:      variant.openAudit(t, platform),
	}
	scenarios := []struct {
		name string
		run  func(*testing.T, timelineIntegrationCase)
	}{
		{name: "append_page_and_impact_replay", run: testTimelineRepositoryAppendPageAndImpactReplay},
		{name: "impact_v2_supersedes_v1", run: testImpactReportV2SupersedesV1AndDerivesSuccessor},
		{name: "impact_v2_predecessor", run: testImpactReportV2RequiresMatchingV1Predecessor},
		{name: "impact_object_limit", run: testTimelineRepositoryListImpactObjectsRejectsMoreThan500Candidates},
		{name: "impact_concurrent_replay", run: testImpactReportV2ConcurrentCreateReplaysOneWinner},
		{name: "impact_selector_not_ready", run: testImpactReportV2RejectsIncompleteSelectorMarkerWithoutWrites},
		{name: "impact_owner_binding", run: testTimelineRepositoryListsOwnerBackedImpactFromExactProvenanceAndRejectsStaleSnapshot},
		{name: "impact_audit_rollback", run: testImpactReportAndTimelineOutboxRollBackWhenTransactionalAuditFails},
		{name: "impact_actions_read_only", run: testTimelineRepositoryListImpactObjectsKeepsActionsReadOnly},
		{name: "projection_domain_sources", run: testTimelineProjectionOutboxConnectsProposalConflictAndImpact},
		{name: "projection_poison", run: testTimelineProjectionPersistsPoisonWithoutWritingKnowledgeEvent},
		{name: "projection_v2_owner_replay", run: testTimelineProjectionV2PersistsOwnerBindingAndReplays},
		{name: "projection_skip_locked", run: testTimelineProjectionTwoDispatchersSkipLockedAndPersistExactlyOneEvent},
		{name: "connection_resources", run: testTimelineRepositoryReturnsConnections},
		{name: "projection_v2_malformed_poison", run: testTimelineProjectionV2PoisonsMalformedOwnerAndOperator},
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			scenario.run(t, testCase)
		})
	}
}

func openLegacyTimelineIntegrationRepository(t *testing.T, platform *platformpostgres.Pool) timelineIntegrationRepository {
	t.Helper()
	repository, err := NewRepository(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func openGORMTimelineIntegrationRepository(t *testing.T, platform *platformpostgres.Pool) timelineIntegrationRepository {
	t.Helper()
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func openLegacyTimelineImpactAudit(t *testing.T, platform *platformpostgres.Pool) timelineImpactAuditCollaborator {
	t.Helper()
	store, err := auditpostgres.NewStore(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	return newTimelineImpactAudit(t, store)
}

func openGORMTimelineImpactAudit(t *testing.T, platform *platformpostgres.Pool) timelineImpactAuditCollaborator {
	t.Helper()
	store, err := auditpostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	return newTimelineImpactAudit(t, store)
}

func newTimelineImpactAudit(t *testing.T, repository auditapplication.Repository) timelineImpactAuditCollaborator {
	t.Helper()
	recorder, err := auditapplication.NewRecorder(repository)
	if err != nil {
		t.Fatal(err)
	}
	impactRecorder, err := knowledgeaudit.NewImpactRecorder(recorder, func(context.Context) (auditdomain.ActorType, string) {
		return auditdomain.ActorUser, "timeline-impact-integration"
	})
	if err != nil {
		t.Fatal(err)
	}
	return impactRecorder
}

func saveLegacyTimelineImpactWithAudit(ctx context.Context, repository timelineIntegrationRepository, report domain.ImpactReport, key string, audit timelineImpactAuditCollaborator) (domain.ImpactReport, bool, error) {
	return repository.SaveImpactReportWithAudit(ctx, report, key, audit)
}

func saveGORMTimelineImpactWithAudit(ctx context.Context, repository timelineIntegrationRepository, report domain.ImpactReport, key string, audit timelineImpactAuditCollaborator) (domain.ImpactReport, bool, error) {
	scoped, ok := repository.(knowledgeapp.ScopedImpactRepository)
	if !ok {
		return domain.ImpactReport{}, false, errors.New("Knowledge GORM repository does not expose scoped Impact persistence")
	}
	return scoped.SaveImpactReportWithScopedAudit(ctx, report, key, audit)
}

func (testCase timelineIntegrationCase) openRepository(t *testing.T) timelineIntegrationRepository {
	t.Helper()
	return testCase.variant.open(t, testCase.platform)
}

func (testCase timelineIntegrationCase) saveImpactWithAudit(ctx context.Context, report domain.ImpactReport, key string, audit timelineImpactAuditCollaborator) (domain.ImpactReport, bool, error) {
	return testCase.variant.saveWithAudit(ctx, testCase.repository, report, key, audit)
}

func testTimelineRepositoryAppendPageAndImpactReplay(t *testing.T, testCase timelineIntegrationCase) {
	repository, tx, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, tx, "timeline-impact")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	aggregateID := newID(t)
	newer := timelineIntegrationEvent(newID(t), fixture.workspaceID, aggregateID, "conflict:resolved:2", now)
	older := timelineIntegrationEvent(newID(t), fixture.workspaceID, aggregateID, "conflict:resolved:1", now.Add(-time.Second))

	persisted, replayed, err := repository.AppendEvent(ctx, older)
	if err != nil || replayed || persisted.ID != older.ID {
		t.Fatalf("append older=%#v replayed=%t err=%v", persisted, replayed, err)
	}
	if _, replayed, err = repository.AppendEvent(ctx, older); err != nil || !replayed {
		t.Fatalf("append replay=%t err=%v", replayed, err)
	}
	if _, _, err = repository.AppendEvent(ctx, newer); err != nil {
		t.Fatal(err)
	}

	page, err := repository.ListEvents(ctx, domain.TimelineQuery{WorkspaceID: fixture.workspaceID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != newer.ID || !page.HasMore || page.Next == nil || page.Next.ID != newer.ID {
		t.Fatalf("page=%#v", page)
	}
	next, err := repository.ListEvents(ctx, domain.TimelineQuery{WorkspaceID: fixture.workspaceID, Limit: 1, After: page.Next})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != older.ID || next.HasMore {
		t.Fatalf("next=%#v err=%v", next, err)
	}

	fingerprint, err := domain.ComputeImpactFingerprint(newer.ID, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: newer.ID, SourceEventRef: newer.SourceEventRef,
		SourceVersion: 1, Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil),
		Fingerprint: fingerprint, GeneratedAt: now.Add(time.Second), CreatedAt: now.Add(time.Second), Version: 1,
	}
	const firstAuditKey = "timeline-impact-replay"
	saved, replayed, err := testCase.saveImpactWithAudit(ctx, report, firstAuditKey, testCase.audit)
	if err != nil || replayed || saved.ID != report.ID {
		t.Fatalf("save report=%#v replayed=%t err=%v cause=%v", saved, replayed, err, errors.Unwrap(err))
	}
	if _, replayed, err = testCase.saveImpactWithAudit(ctx, report, firstAuditKey, testCase.audit); err != nil || !replayed {
		t.Fatalf("report replay=%t err=%v", replayed, err)
	}
	if _, replayed, err = testCase.saveImpactWithAudit(ctx, report, "timeline-impact-replay-second", testCase.audit); err != nil || !replayed {
		t.Fatalf("report second audit replay=%t err=%v", replayed, err)
	}
	loaded, err := repository.GetImpactReportByID(ctx, fixture.workspaceID, report.ID)
	if err != nil || loaded.Fingerprint != fingerprint || loaded.SourceEventRef != newer.SourceEventRef {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	var auditRows int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.audit_event
WHERE workspace_id=$1 AND action='IMPACT_ANALYZED' AND resource_ref=$2`, string(fixture.workspaceID), string(report.ID)).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if auditRows != 2 {
		t.Fatalf("impact audit rows=%d want 2", auditRows)
	}
}

func testImpactReportV2SupersedesV1AndDerivesSuccessor(t *testing.T, testCase timelineIntegrationCase) {
	repository, tx, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, tx, "timeline-impact-v2-supersession")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	aggregateID := newID(t)
	event := timelineIntegrationEvent(newID(t), fixture.workspaceID, aggregateID, "impact:v2-supersession", now)
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append source event replayed=%t err=%v", replayed, err)
	}
	ready, err := repository.ImpactAnalysisReady(ctx, fixture.workspaceID)
	if err != nil || !ready {
		t.Fatalf("impact readiness=%t err=%v", ready, err)
	}

	v1Fingerprint, err := domain.ComputeImpactFingerprint(event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	v1 := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{},
		Summary: domain.SummarizeImpactObjects(nil), Fingerprint: v1Fingerprint,
		GeneratedAt: now.Add(time.Second), CreatedAt: now.Add(time.Second), Version: 1,
	}
	if persisted, replayed, err := repository.SaveImpactReport(ctx, v1); err != nil || replayed || persisted.ID != v1.ID || persisted.EffectiveAnalysisVersion() != domain.ImpactAnalysisVersionV1 {
		t.Fatalf("v1 persisted=%#v replayed=%t err=%v", persisted, replayed, err)
	}
	v2Fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	v2 := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2, SupersedesReportID: &v1.ID,
		Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil), Fingerprint: v2Fingerprint,
		GeneratedAt: now.Add(2 * time.Second), CreatedAt: now.Add(2 * time.Second), Version: 1,
	}
	persistedV2, replayed, err := repository.SaveImpactReport(ctx, v2)
	if err != nil || replayed || persistedV2.ID != v2.ID || persistedV2.AnalysisVersion != domain.ImpactAnalysisVersionV2 || persistedV2.SupersedesReportID == nil || *persistedV2.SupersedesReportID != v1.ID {
		t.Fatalf("v2 persisted=%#v replayed=%t err=%v", persistedV2, replayed, err)
	}

	competitor := v2
	competitor.ID = newID(t)
	competitor.GeneratedAt = now.Add(3 * time.Second)
	competitor.CreatedAt = competitor.GeneratedAt
	replayedV2, replayed, err := repository.SaveImpactReport(ctx, competitor)
	if err != nil || !replayed || replayedV2.ID != v2.ID || replayedV2.GeneratedAt != v2.GeneratedAt {
		t.Fatalf("v2 replay=%#v replayed=%t err=%v", replayedV2, replayed, err)
	}

	loadedV1, err := repository.GetImpactReportByID(ctx, fixture.workspaceID, v1.ID)
	if err != nil || loadedV1.ID != v1.ID || loadedV1.Fingerprint != v1Fingerprint || loadedV1.SupersededByReportID == nil || *loadedV1.SupersededByReportID != v2.ID {
		t.Fatalf("loaded v1=%#v err=%v", loadedV1, err)
	}
	loadedCurrent, found, err := repository.GetImpactReport(ctx, fixture.workspaceID, event.ID, domain.ImpactAnalysisVersionV2)
	if err != nil || !found || loadedCurrent.ID != v2.ID || loadedCurrent.SupersededByReportID != nil {
		t.Fatalf("loaded current=%#v found=%t err=%v", loadedCurrent, found, err)
	}
	var storedV1Schema, storedV1Analysis string
	var storedV1Supersedes *string
	if err := tx.QueryRow(ctx, `SELECT schema_version,analysis_version,supersedes_report_id::text
FROM ops.impact_report WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(v1.ID)).Scan(&storedV1Schema, &storedV1Analysis, &storedV1Supersedes); err != nil {
		t.Fatal(err)
	}
	if storedV1Schema != domain.ImpactReportSchemaVersion || storedV1Analysis != string(domain.ImpactAnalysisVersionV1) || storedV1Supersedes != nil {
		t.Fatalf("stored v1 schema=%q analysis=%q supersedes=%v", storedV1Schema, storedV1Analysis, storedV1Supersedes)
	}
}

func testImpactReportV2RequiresMatchingV1Predecessor(t *testing.T, testCase timelineIntegrationCase) {
	repository, tx, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, tx, "timeline-impact-v2-predecessor")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	event := timelineIntegrationEvent(newID(t), fixture.workspaceID, newID(t), "impact:v2-predecessor", now)
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append source event replayed=%t err=%v", replayed, err)
	}
	v1Fingerprint, err := domain.ComputeImpactFingerprint(event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	v1 := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{},
		Summary: domain.SummarizeImpactObjects(nil), Fingerprint: v1Fingerprint,
		GeneratedAt: now.Add(time.Second), CreatedAt: now.Add(time.Second), Version: 1,
	}
	if _, replayed, err := repository.SaveImpactReport(ctx, v1); err != nil || replayed {
		t.Fatalf("save v1 replayed=%t err=%v", replayed, err)
	}
	v2Fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	missingPredecessor := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2,
		Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil), Fingerprint: v2Fingerprint,
		GeneratedAt: now.Add(2 * time.Second), CreatedAt: now.Add(2 * time.Second), Version: 1,
	}
	if _, _, err := repository.SaveImpactReport(ctx, missingPredecessor); !hasCode(err, domain.ErrorCodeImpactConflict) {
		t.Fatalf("missing predecessor error=%v", err)
	}
	var v2Count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.impact_report
WHERE workspace_id=$1 AND source_event_id=$2 AND analysis_version='impact-analysis/v2'`, string(fixture.workspaceID), string(event.ID)).Scan(&v2Count); err != nil {
		t.Fatal(err)
	}
	if v2Count != 0 {
		t.Fatalf("v2 reports without predecessor=%d", v2Count)
	}

	freshEvent := timelineIntegrationEvent(newID(t), fixture.workspaceID, newID(t), "impact:v2-without-predecessor", now.Add(3*time.Second))
	if _, replayed, err := repository.AppendEvent(ctx, freshEvent); err != nil || replayed {
		t.Fatalf("append fresh source event replayed=%t err=%v", replayed, err)
	}
	freshFingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, freshEvent.ID, int64(freshEvent.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	freshV2 := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: freshEvent.ID, SourceEventRef: freshEvent.SourceEventRef,
		SourceVersion: int64(freshEvent.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2,
		Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil), Fingerprint: freshFingerprint,
		GeneratedAt: now.Add(4 * time.Second), CreatedAt: now.Add(4 * time.Second), Version: 1,
	}
	if persisted, replayed, err := repository.SaveImpactReport(ctx, freshV2); err != nil || replayed || persisted.ID != freshV2.ID {
		t.Fatalf("fresh v2 persisted=%#v replayed=%t err=%v", persisted, replayed, err)
	}
}

func testTimelineRepositoryListImpactObjectsRejectsMoreThan500Candidates(t *testing.T, testCase timelineIntegrationCase) {
	repository, tx, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, tx, "timeline-impact-object-limit")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	claimID := newID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim(
	id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,
	status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
) VALUES($1,$2,$3,$3,'{}','knowledge-applicability/v1',$4,'SUGGESTED',0.9,'{}',$5,1,$6,$6)`,
		string(claimID), string(fixture.workspaceID), "Timeline impact object limit claim", testHash("timeline-impact-object-limit-applicability"), testHash("timeline-impact-object-limit-claim"), now); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= domain.MaxImpactObjects; index++ {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.health_issue(
	id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,fingerprint,
	detector_version,severity,evidence_summary,status,version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at
) VALUES($1,$2,'ORPHAN','CLAIM',$3,'timeline-impact-limit',$4,'health-issue-fingerprint/v1',$5,
	'detector/v1','HIGH','timeline impact object limit','OPEN',1,$6,$6,$6,$6,$6)`,
			string(newID(t)), string(fixture.workspaceID), string(claimID), testHash(fmt.Sprintf("timeline-impact-limit-identity-%d", index)), testHash(fmt.Sprintf("timeline-impact-limit-fingerprint-%d", index)), now); err != nil {
			t.Fatal(err)
		}
	}
	event := domain.KnowledgeEvent{
		ID: newID(t), WorkspaceID: fixture.workspaceID, EventType: domain.EventVersionPublished,
		AggregateType: domain.TimelineAggregateClaim, AggregateID: &claimID,
		SourceEventRef: "timeline-impact-object-limit:" + string(claimID), SourceRef: "claim:" + string(claimID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "timeline impact object limit", Payload: json.RawMessage(`{}`),
		OccurredAt: now, CreatedAt: now,
	}
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append limit source event replayed=%t err=%v", replayed, err)
	}
	if _, err := repository.ListImpactObjects(ctx, event); !hasCode(err, domain.ErrorCodeTimelineInconsistent) {
		t.Fatalf("impact object limit error=%v", err)
	}
}

func testImpactReportV2ConcurrentCreateReplaysOneWinner(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, pool, "timeline-impact-v2-concurrent")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	event := timelineIntegrationEvent(newID(t), fixture.workspaceID, newID(t), "impact:v2-concurrent", now)
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append source event replayed=%t err=%v", replayed, err)
	}
	v1Fingerprint, err := domain.ComputeImpactFingerprint(event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	v1 := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{},
		Summary: domain.SummarizeImpactObjects(nil), Fingerprint: v1Fingerprint,
		GeneratedAt: now.Add(time.Second), CreatedAt: now.Add(time.Second), Version: 1,
	}
	if _, replayed, err := repository.SaveImpactReport(ctx, v1); err != nil || replayed {
		t.Fatalf("save v1 replayed=%t err=%v", replayed, err)
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	makeReport := func(id foundation.ID, at time.Time) domain.ImpactReport {
		return domain.ImpactReport{
			ID: id, WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
			SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2, SupersedesReportID: &v1.ID,
			Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil), Fingerprint: fingerprint,
			GeneratedAt: at, CreatedAt: at, Version: 1,
		}
	}
	secondRepository := testCase.openRepository(t)
	type result struct {
		report   domain.ImpactReport
		replayed bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for _, candidate := range []struct {
		repository timelineIntegrationRepository
		report     domain.ImpactReport
	}{
		{repository: repository, report: makeReport(newID(t), now.Add(time.Second))},
		{repository: secondRepository, report: makeReport(newID(t), now.Add(2*time.Second))},
	} {
		workers.Add(1)
		go func(candidateRepository timelineIntegrationRepository, candidateReport domain.ImpactReport) {
			defer workers.Done()
			<-start
			persisted, replayed, saveErr := candidateRepository.SaveImpactReport(ctx, candidateReport)
			results <- result{report: persisted, replayed: replayed, err: saveErr}
		}(candidate.repository, candidate.report)
	}
	close(start)
	workers.Wait()
	close(results)

	var winner foundation.ID
	created, replayed := 0, 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("concurrent v2 save error=%v", outcome.err)
		}
		if winner == "" {
			winner = outcome.report.ID
		}
		if outcome.report.ID != winner {
			t.Fatalf("concurrent v2 reports diverged: winner=%s report=%#v", winner, outcome.report)
		}
		if outcome.replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatalf("concurrent v2 created=%d replayed=%d", created, replayed)
	}
	var reportCount, outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.impact_report
WHERE workspace_id=$1 AND source_event_id=$2 AND analysis_version='impact-analysis/v2'`, string(fixture.workspaceID), string(event.ID)).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
WHERE workspace_id=$1 AND source_event_ref LIKE $2`, string(fixture.workspaceID), "impact-report:"+string(winner)+":v%").Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 1 || outboxCount != 1 {
		t.Fatalf("concurrent v2 reports=%d outbox=%d", reportCount, outboxCount)
	}
}

func testImpactReportV2RejectsIncompleteSelectorMarkerWithoutWrites(t *testing.T, testCase timelineIntegrationCase) {
	repository, tx, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, tx, "timeline-impact-v2-not-ready")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	aggregateID := newID(t)
	event := timelineIntegrationEvent(newID(t), fixture.workspaceID, aggregateID, "impact:v2-not-ready", now)
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append source event replayed=%t err=%v", replayed, err)
	}
	setupTx, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = setupTx.Rollback(context.Background()) }()
	if _, err := setupTx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `UPDATE learning.artifact_citation_selector_backfill
SET status='PENDING',completed_at=NULL,version=version+1,updated_at=updated_at+interval '1 microsecond'
WHERE workspace_id=$1`, string(fixture.workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `SET LOCAL session_replication_role = origin`); err != nil {
		t.Fatal(err)
	}
	if err := setupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ready, err := repository.ImpactAnalysisReady(ctx, fixture.workspaceID)
	if err != nil || ready {
		t.Fatalf("impact readiness=%t err=%v", ready, err)
	}
	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2,
		Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil), Fingerprint: fingerprint,
		GeneratedAt: now.Add(time.Second), CreatedAt: now.Add(time.Second), Version: 1,
	}
	if _, _, err := repository.SaveImpactReport(ctx, report); !hasCode(err, domain.ErrorCodeImpactUnavailable) {
		t.Fatalf("v2 save error=%v", err)
	}
	var reportCount, outboxCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.impact_report WHERE workspace_id=$1 AND source_event_id=$2`, string(fixture.workspaceID), string(event.ID)).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox WHERE workspace_id=$1 AND source_event_ref=$2`, string(fixture.workspaceID), "impact-report:"+string(report.ID)+":v1").Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 0 || outboxCount != 0 {
		t.Fatalf("report rows=%d outbox rows=%d", reportCount, outboxCount)
	}
}

func testTimelineRepositoryListsOwnerBackedImpactFromExactProvenanceAndRejectsStaleSnapshot(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	first := seedProvenance(t, ctx, pool, "timeline-owner-impact-first")
	second := seedProvenanceForWorkspace(t, ctx, pool, first.workspaceID, "timeline-owner-impact-second", true)
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()

	insertClaim := func(id foundation.ID, fixture provenanceFixture, label string) string {
		t.Helper()
		evidenceHash := testHash("timeline-owner-impact-claim-source-" + label)
		if _, err := seedTx.Exec(ctx, `INSERT INTO core.claim(
id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,
status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
) VALUES($1,$2,$3,$3,'{}','knowledge-applicability/v1',$4,'SUGGESTED',0.9,'{}',$5,1,$6,$6)`,
			string(id), string(first.workspaceID), "Timeline owner impact claim "+label, testHash("timeline-owner-impact-applicability-"+label), testHash("timeline-owner-impact-claim-"+label), now); err != nil {
			t.Fatal(err)
		}
		if _, err := seedTx.Exec(ctx, `INSERT INTO core.claim_source(
id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at
) VALUES($1,$2,$3,$4,$5,'SUPPORTS',$6,$7,$8)`,
			string(newID(t)), string(first.workspaceID), string(id), string(fixture.sourceVersionID), string(fixture.sourceSpanID), "timeline owner impact provenance", evidenceHash, now); err != nil {
			t.Fatal(err)
		}
		return evidenceHash
	}

	claimA, claimB, claimC := newID(t), newID(t), newID(t)
	claimAEvidenceHash := insertClaim(claimA, first, "a")
	insertClaim(claimB, first, "b")
	claimCEvidenceHash := insertClaim(claimC, second, "c")
	relationID := newID(t)
	if _, err := seedTx.Exec(ctx, `INSERT INTO core.relation(
id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,
fingerprint,version,created_at,updated_at
) VALUES($1,$2,'CLAIM',$3,'CLAIM',$4,'CITES','SUGGESTED',$5,1,$6,$6)`,
		string(relationID), string(first.workspaceID), string(claimA), string(claimB), testHash("timeline-owner-impact-relation"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(ctx, `INSERT INTO core.relation_evidence(
id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,
applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at
) VALUES($1,$2,$3,$4,$5,$6,$7,'{}','knowledge-applicability/v1',$8,'SOURCE_DERIVED',$9,$10)`,
		string(newID(t)), string(first.workspaceID), string(relationID), string(second.sourceVersionID), string(second.sourceSpanID),
		"timeline owner impact relation evidence", testHash("timeline-owner-impact-relation-evidence"), testHash("timeline-owner-impact-relation-applicability"), "timeline-owner-impact", now); err != nil {
		t.Fatal(err)
	}

	insertArtifact := func(fixture provenanceFixture, label string) (foundation.ID, foundation.ID, string) {
		t.Helper()
		artifactID, revisionID := newID(t), newID(t)
		contentHash := testHash("timeline-owner-impact-artifact-" + label)
		if _, err := seedTx.Exec(ctx, `INSERT INTO learning.artifact(
id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,
domain_schema_version,scope_definition,source_coverage,current_revision_id
) VALUES($1,$2,'CUSTOM',$3,'{}','DRAFT',1,$4,$4,'artifact/v1','workspace','[]',$5)`,
			string(artifactID), string(first.workspaceID), "Timeline owner impact artifact "+label, now, string(revisionID)); err != nil {
			t.Fatal(err)
		}
		if _, err := seedTx.Exec(ctx, `INSERT INTO learning.artifact_revision(
id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at,
domain_schema_version,content_hash,created_by_type,generation_metadata
) VALUES($1,$2,$3,1,'SNAPSHOT','[]','[]','[]','[]','[]','', '{}',$4,
'artifact-revision/v1',$5,'HUMAN',NULL)`,
			string(revisionID), string(artifactID), string(first.workspaceID), now, contentHash); err != nil {
			t.Fatal(err)
		}
		if _, err := seedTx.Exec(ctx, `INSERT INTO learning.artifact_revision_citation_selector(
workspace_id,artifact_id,revision_id,source_version_id,source_span_id
) VALUES($1,$2,$3,$4,$5)`,
			string(first.workspaceID), string(artifactID), string(revisionID), string(fixture.sourceVersionID), string(fixture.sourceSpanID)); err != nil {
			t.Fatal(err)
		}
		return artifactID, revisionID, contentHash
	}
	artifactA, revisionA, artifactAHash := insertArtifact(first, "a")
	artifactB, _, artifactBHash := insertArtifact(second, "b")

	deckID := newID(t)
	if _, err := seedTx.Exec(ctx, `INSERT INTO learning.review_deck(
id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
) VALUES($1,$2,$3,'{}','ACTIVE',20,'fsrs/v1',1,$4,$4)`, string(deckID), string(first.workspaceID), "Timeline owner impact deck", now); err != nil {
		t.Fatal(err)
	}
	insertCard := func(claimID foundation.ID, fixture provenanceFixture, evidenceHash, status, label string) foundation.ID {
		t.Helper()
		cardID := newID(t)
		evidence, err := json.Marshal([]map[string]string{{
			"schema_version": "review-evidence/v1", "claim_id": string(claimID), "source_version_id": string(fixture.sourceVersionID),
			"source_span_id": string(fixture.sourceSpanID), "evidence_hash": evidenceHash,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seedTx.Exec(ctx, `INSERT INTO learning.review_card(
id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,
fingerprint,model_version,version,created_at,updated_at
) VALUES($1,$2,$3,$4,$5,'["answer"]',$6::jsonb,'SHORT_ANSWER',0.5,$7,$8,'manual',1,$9,$9)`,
			string(cardID), string(first.workspaceID), string(deckID), string(claimID), "Timeline owner impact card "+label, string(evidence), status, testHash("timeline-owner-impact-card-"+label), now); err != nil {
			t.Fatal(err)
		}
		if status == "APPROVED" {
			if _, err := seedTx.Exec(ctx, `INSERT INTO learning.review_schedule(
card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version
) VALUES($1,$2,$3,0,0,0.5,NULL,'fsrs/v1',false,1)`, string(cardID), string(first.workspaceID), now); err != nil {
				t.Fatal(err)
			}
		}
		return cardID
	}
	directCard := insertCard(claimA, first, claimAEvidenceHash, "DRAFT", "direct")
	evidenceCard := insertCard(claimC, second, claimCEvidenceHash, "APPROVED", "evidence")
	crossPairCard := newID(t)
	crossPairEvidence, err := json.Marshal([]map[string]string{
		{
			"schema_version": "review-evidence/v1", "claim_id": string(claimC), "source_version_id": string(first.sourceVersionID),
			"source_span_id": string(second.sourceSpanID), "evidence_hash": testHash("timeline-owner-impact-cross-pair-a"),
		},
		{
			"schema_version": "review-evidence/v1", "claim_id": string(claimC), "source_version_id": string(second.sourceVersionID),
			"source_span_id": string(first.sourceSpanID), "evidence_hash": testHash("timeline-owner-impact-cross-pair-b"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(ctx, `INSERT INTO learning.review_card(
	id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,
	fingerprint,model_version,version,created_at,updated_at
) VALUES($1,$2,$3,$4,$5,'["answer"]',$6::jsonb,'SHORT_ANSWER',0.5,'APPROVED',$7,'manual',1,$8,$8)`,
		string(crossPairCard), string(first.workspaceID), string(deckID), string(claimC), "Timeline owner impact cross-pair card",
		string(crossPairEvidence), testHash("timeline-owner-impact-card-cross-pair"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(ctx, `INSERT INTO learning.review_schedule(
card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version
) VALUES($1,$2,$3,0,0,0.5,NULL,'fsrs/v1',false,1)`, string(crossPairCard), string(first.workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	event := domain.KnowledgeEvent{
		ID: newID(t), WorkspaceID: first.workspaceID, EventType: domain.EventVersionPublished,
		AggregateType: domain.TimelineAggregateRelation, AggregateID: &relationID,
		SourceEventRef: "timeline-owner-impact-relation:" + string(relationID), SourceRef: "relation:" + string(relationID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "Timeline owner impact relation changed",
		Payload: json.RawMessage(`{}`), OccurredAt: now, CreatedAt: now,
	}
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append relation event replayed=%t err=%v", replayed, err)
	}
	objects, err := repository.ListImpactObjects(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[string]domain.ImpactObject, len(objects))
	for _, object := range objects {
		byKey[string(object.Type)+":"+string(object.ID)] = object
	}
	artifactAObject, artifactAFound := byKey[string(domain.ImpactObjectArtifact)+":"+string(artifactA)]
	artifactBObject, artifactBFound := byKey[string(domain.ImpactObjectArtifact)+":"+string(artifactB)]
	directCardObject, directCardFound := byKey[string(domain.ImpactObjectReviewCard)+":"+string(directCard)]
	evidenceCardObject, evidenceCardFound := byKey[string(domain.ImpactObjectReviewCard)+":"+string(evidenceCard)]
	if !artifactAFound || !artifactBFound || !directCardFound || !evidenceCardFound {
		t.Fatalf("owner impact objects=%#v", objects)
	}
	if crossPairObject, found := byKey[string(domain.ImpactObjectReviewCard)+":"+string(crossPairCard)]; found {
		t.Fatalf("cross-paired Review selector produced an impact object=%#v", crossPairObject)
	}
	if artifactAObject.ArtifactBinding == nil || artifactAObject.ArtifactBinding.RevisionID != revisionA || artifactAObject.ArtifactBinding.ContentHash != artifactAHash || artifactBObject.ArtifactBinding == nil || artifactBObject.ArtifactBinding.ContentHash != artifactBHash || artifactAObject.Action != domain.ImpactActionRegenerateArtifact || !artifactAObject.RequiresProposal {
		t.Fatalf("artifact owner bindings=%#v / %#v", artifactAObject, artifactBObject)
	}
	if directCardObject.ReviewCardBinding == nil || directCardObject.ReviewCardBinding.ClaimID != claimA || evidenceCardObject.ReviewCardBinding == nil || evidenceCardObject.ReviewCardBinding.ClaimID != claimC || directCardObject.Action != domain.ImpactActionRevalidateReviewCard || !directCardObject.RequiresProposal {
		t.Fatalf("review owner bindings=%#v / %#v", directCardObject, evidenceCardObject)
	}
	if relationObject, found := byKey[string(domain.ImpactObjectRelation)+":"+string(relationID)]; !found || relationObject.RequiresProposal || relationObject.Action != domain.ImpactActionReview {
		t.Fatalf("relation impact=%#v", relationObject)
	}

	fingerprint, err := domain.ComputeImpactFingerprintForVersion(domain.ImpactAnalysisVersionV2, event.ID, int64(event.EventVersion), objects)
	if err != nil {
		t.Fatal(err)
	}
	newRevisionID := newID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.artifact_revision(
id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,conflicts,content_markdown,provenance,created_at,
domain_schema_version,content_hash,created_by_type,generation_metadata
) VALUES($1,$2,$3,2,'SNAPSHOT','[]','[]','[]','[]','[]','', '{}',$4,
'artifact-revision/v1',$5,'HUMAN',NULL)`,
		string(newRevisionID), string(artifactA), string(first.workspaceID), now.Add(time.Second), testHash("timeline-owner-impact-artifact-a-next")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact
SET current_revision_id=$1,version=version+1,updated_at=$2
WHERE workspace_id=$3 AND id=$4`, string(newRevisionID), now.Add(time.Second), string(first.workspaceID), string(artifactA)); err != nil {
		t.Fatal(err)
	}
	report := domain.ImpactReport{
		ID: newID(t), WorkspaceID: first.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), AnalysisVersion: domain.ImpactAnalysisVersionV2,
		Status: domain.ImpactReportReady, Objects: objects, Summary: domain.SummarizeImpactObjects(objects), Fingerprint: fingerprint,
		GeneratedAt: now.Add(2 * time.Second), CreatedAt: now.Add(2 * time.Second), Version: 1,
	}
	if _, _, err := repository.SaveImpactReport(ctx, report); !hasCode(err, domain.ErrorCodeImpactConflict) {
		t.Fatalf("stale owner snapshot save err=%v", err)
	}
	var reportCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.impact_report WHERE workspace_id=$1 AND source_event_id=$2 AND analysis_version='impact-analysis/v2'`, string(first.workspaceID), string(event.ID)).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 0 {
		t.Fatalf("stale owner snapshot created v2 reports=%d", reportCount)
	}
}

func testImpactReportAndTimelineOutboxRollBackWhenTransactionalAuditFails(t *testing.T, testCase timelineIntegrationCase) {
	repository, tx, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, tx, "timeline-impact-audit-rollback")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	aggregateID := newID(t)
	event := timelineIntegrationEvent(newID(t), fixture.workspaceID, aggregateID, "impact:audit-rollback", now)
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append source event replayed=%t err=%v", replayed, err)
	}
	fingerprint, err := domain.ComputeImpactFingerprint(event.ID, int64(event.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: event.ID, SourceEventRef: event.SourceEventRef,
		SourceVersion: int64(event.EventVersion), Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{},
		Summary: domain.SummarizeImpactObjects(nil), Fingerprint: fingerprint, GeneratedAt: now, CreatedAt: now, Version: 1,
	}
	auditFailure := errors.New("injected transactional audit failure")
	if _, _, err := testCase.saveImpactWithAudit(ctx, report, "audit-rollback", impactAuditFailureStub{err: auditFailure}); !errors.Is(err, auditFailure) {
		t.Fatalf("atomic impact save error=%v", err)
	}
	if _, found, err := repository.GetImpactReport(ctx, fixture.workspaceID, event.ID, domain.ImpactAnalysisVersionV1); err != nil || found {
		t.Fatalf("rolled back impact report found=%t err=%v", found, err)
	}
	var outboxRows int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
WHERE workspace_id=$1 AND source_event_ref=$2`, string(fixture.workspaceID), "impact-report:"+string(report.ID)+":v1").Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 0 {
		t.Fatalf("rolled back Impact Timeline outbox rows=%d", outboxRows)
	}
}

type impactAuditFailureStub struct{ err error }

func (stub impactAuditFailureStub) RecordImpactAnalysis(context.Context, knowledgeapp.ImpactAuditRecord) error {
	return stub.err
}

func (stub impactAuditFailureStub) RecordImpactAnalysisTx(context.Context, any, knowledgeapp.ImpactAuditRecord) error {
	return stub.err
}

func (stub impactAuditFailureStub) RecordImpactAnalysisScoped(context.Context, foundation.TransactionScope, knowledgeapp.ImpactAuditRecord) error {
	return stub.err
}

func testTimelineRepositoryListImpactObjectsKeepsActionsReadOnly(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture, err := graphtestfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cleanupTx, err := pool.Begin(cleanupCtx)
		if err != nil {
			t.Errorf("begin conflict cleanup transaction: %v", err)
			return
		}
		defer func() { _ = cleanupTx.Rollback(cleanupCtx) }()
		if _, err := cleanupTx.Exec(cleanupCtx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Errorf("set conflict cleanup role: %v", err)
			return
		}
		if _, err := cleanupTx.Exec(cleanupCtx, `DELETE FROM core.conflict_member WHERE workspace_id=$1`, string(fixture.WorkspaceID)); err != nil {
			t.Errorf("delete impact conflict member fixture: %v", err)
			return
		}
		if _, err := cleanupTx.Exec(cleanupCtx, `DELETE FROM core.conflict WHERE workspace_id=$1`, string(fixture.WorkspaceID)); err != nil {
			t.Errorf("delete impact conflict fixture: %v", err)
			return
		}
		if err := cleanupTx.Commit(cleanupCtx); err != nil {
			t.Errorf("commit conflict cleanup: %v", err)
			return
		}
		if err := graphtestfixture.Cleanup(cleanupCtx, pool, fixture.WorkspaceID); err != nil {
			t.Errorf("cleanup graph fixture: %v", err)
		}
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	conflictID := newID(t)
	conflictTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conflictTx.Rollback(context.Background()) }()
	for _, claimID := range []foundation.ID{fixture.FirstClaimID, fixture.SecondClaimID} {
		if _, err := conflictTx.Exec(ctx, `UPDATE core.claim
SET status='DISPUTED',version=version+1,updated_at=$3
WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(claimID), now); err != nil {
			t.Fatal(err)
		}
	}
	var applicabilityHash string
	var distinctApplicability int
	if err := conflictTx.QueryRow(ctx, `SELECT min(applicability_hash),count(DISTINCT applicability_hash)
FROM core.claim WHERE workspace_id=$1 AND id=ANY($2::uuid[])`, string(fixture.WorkspaceID), []string{string(fixture.FirstClaimID), string(fixture.SecondClaimID)}).Scan(&applicabilityHash, &distinctApplicability); err != nil {
		t.Fatal(err)
	}
	if distinctApplicability != 1 {
		t.Fatalf("graph fixture applicability count=%d want 1", distinctApplicability)
	}
	if _, err := conflictTx.Exec(ctx, `INSERT INTO core.conflict(
		id,workspace_id,topic_id,status,severity,summary,applicability_assessment,applicability_hash,fingerprint,
		version,created_at,updated_at
	) VALUES($1,$2,$3,'OPEN','LOW','Timeline impact conflict','EXACT',$4,$5,1,$6,$6)`,
		string(conflictID), string(fixture.WorkspaceID), string(fixture.PrimaryTopicID), applicabilityHash, testHash("timeline-impact-read-only-conflict"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := conflictTx.Exec(ctx, `INSERT INTO core.conflict_member(
		conflict_id,claim_id,workspace_id,applicability,applicability_schema_version,
		applicability_hash,position_summary,created_at)
	SELECT $1,id,workspace_id,applicability,applicability_schema_version,applicability_hash,
	       'Timeline impact conflict member', $4
	FROM core.claim WHERE workspace_id=$2 AND id=ANY($3::uuid[])`, string(conflictID), string(fixture.WorkspaceID), []string{string(fixture.FirstClaimID), string(fixture.SecondClaimID)}, now); err != nil {
		t.Fatal(err)
	}
	if err := conflictTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	eventID := newID(t)
	event := domain.KnowledgeEvent{
		ID: eventID, WorkspaceID: fixture.WorkspaceID, EventType: domain.EventVersionPublished,
		AggregateType: domain.TimelineAggregateTopic, AggregateID: &fixture.PrimaryTopicID,
		SourceEventRef: "timeline-impact-read-only:" + string(eventID), SourceRef: "topic:" + string(fixture.PrimaryTopicID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "Timeline impact read-only projection",
		Payload: json.RawMessage(`{}`), OccurredAt: now, CreatedAt: now,
	}
	objects, err := repository.ListImpactObjects(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 {
		t.Fatalf("impact objects=%#v", objects)
	}

	seenRelations, seenConflict := 0, false
	for _, object := range objects {
		if object.RequiresProposal || object.Reason == "" {
			t.Fatalf("impact object unexpectedly requires a proposal or lost its reason: %#v", object)
		}
		switch object.Type {
		case domain.ImpactObjectRelation:
			if object.Action != domain.ImpactActionReview {
				t.Fatalf("relation action=%q object=%#v", object.Action, object)
			}
			seenRelations++
		case domain.ImpactObjectConflict:
			if object.ID != conflictID || object.Action != domain.ImpactActionResolveConflict {
				t.Fatalf("conflict action=%q object=%#v", object.Action, object)
			}
			seenConflict = true
		default:
			t.Fatalf("unexpected impact object=%#v", object)
		}
	}
	if seenRelations != 2 || !seenConflict {
		t.Fatalf("relation_count=%d seen_conflict=%t objects=%#v", seenRelations, seenConflict, objects)
	}
}

func testTimelineProjectionOutboxConnectsProposalConflictAndImpact(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	dispatcher, err := knowledgeapp.NewTimelineProjectionDispatcher(repository)
	if err != nil {
		t.Fatal(err)
	}
	drainTimelineProjectionQueue(t, ctx, dispatcher)
	fixture := seedProvenance(t, ctx, pool, "timeline-projector")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	proposalID := newID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
id,workspace_id,status,risk_level,idempotency_key,request_hash,created_at,updated_at)
VALUES($1,$2,'ready_for_review','LOW',$3,$4,$5,$5)`,
		string(proposalID), string(fixture.workspaceID), "timeline-projector-proposal", testHash("timeline-projector-proposal"), now); err != nil {
		t.Fatal(err)
	}
	applicability := mustApplicability(t, `{}`)
	first := suggestClaim(t, ctx, repository, fixture.workspaceID, "Timeline 投影冲突主张一", applicability, "timeline-projector-claim-a", now)
	second := suggestClaim(t, ctx, repository, fixture.workspaceID, "Timeline 投影冲突主张二", applicability, "timeline-projector-claim-b", now.Add(time.Second))
	first = confirmClaim(t, ctx, repository, first, fixture, "timeline-projector-confirm-a", now.Add(2*time.Second))
	second = confirmClaim(t, ctx, repository, second, fixture, "timeline-projector-confirm-b", now.Add(3*time.Second))
	conflict, members := newConflict(t, fixture.workspaceID, first.Claim, second.Claim, now.Add(4*time.Second))
	if _, err := repository.OpenConflict(ctx, domain.OpenConflictRecord{
		Conflict: conflict, Members: members, IdempotencyKey: "timeline-projector-conflict", RequestHash: testHash("timeline-projector-conflict-command"),
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := dispatcher.DispatchBatch(ctx, 10)
	if err != nil || batch.Processed != 4 || batch.Projected != 4 {
		t.Fatalf("projection batch=%#v err=%v", batch, err)
	}
	proposalEvent := mustTimelineEventBySource(t, ctx, repository, fixture.workspaceID, "proposal.created:"+string(proposalID)+":v1")
	if proposalEvent.EventType != domain.EventProposalCreated || proposalEvent.AggregateID == nil || *proposalEvent.AggregateID != proposalID || proposalEvent.Correlation.ProposalID == nil || *proposalEvent.Correlation.ProposalID != proposalID {
		t.Fatalf("proposal event=%#v", proposalEvent)
	}
	conflictEvent := mustTimelineEventBySource(t, ctx, repository, fixture.workspaceID, "knowledge-command:conflict.open:"+string(conflict.ID)+":v1")
	if conflictEvent.EventType != domain.EventConflictOpened || conflictEvent.AggregateID == nil || *conflictEvent.AggregateID != conflict.ID {
		t.Fatalf("conflict event=%#v", conflictEvent)
	}
	investigating, err := repository.TransitionConflict(ctx, domain.TransitionConflictRecord{
		WorkspaceID: fixture.workspaceID, ConflictID: conflict.ID, ExpectedVersion: 1, Status: domain.ConflictStatusInvestigating,
		IdempotencyKey: "timeline-projector-conflict-investigating", RequestHash: testHash("timeline-projector-conflict-investigating"), At: now.Add(5 * time.Second),
	})
	if err != nil || investigating.Conflict.Version != 2 || investigating.Conflict.Status != domain.ConflictStatusInvestigating {
		t.Fatalf("investigating conflict=%#v err=%v", investigating, err)
	}
	proposed, err := repository.TransitionConflict(ctx, domain.TransitionConflictRecord{
		WorkspaceID: fixture.workspaceID, ConflictID: conflict.ID, ExpectedVersion: 2, Status: domain.ConflictStatusResolutionProposed,
		IdempotencyKey: "timeline-projector-conflict-proposed", RequestHash: testHash("timeline-projector-conflict-proposed"), At: now.Add(6 * time.Second),
	})
	if err != nil || proposed.Conflict.Version != 3 || proposed.Conflict.Status != domain.ConflictStatusResolutionProposed {
		t.Fatalf("proposed conflict=%#v err=%v", proposed, err)
	}
	resolution, reference := "Timeline 投影冲突已解决", "proposal:"+string(proposalID)
	resolved, err := repository.TransitionConflict(ctx, domain.TransitionConflictRecord{
		WorkspaceID: fixture.workspaceID, ConflictID: conflict.ID, ExpectedVersion: 3, Status: domain.ConflictStatusResolved,
		Resolution: &resolution, ResolutionReference: &reference,
		IdempotencyKey: "timeline-projector-conflict-resolved", RequestHash: testHash("timeline-projector-conflict-resolved"), At: now.Add(7 * time.Second),
	})
	if err != nil || resolved.Conflict.Version != 4 || resolved.Conflict.Status != domain.ConflictStatusResolved {
		t.Fatalf("resolved conflict=%#v err=%v", resolved, err)
	}
	batch, err = dispatcher.DispatchBatch(ctx, 10)
	if err != nil || batch.Processed != 3 || batch.Projected != 3 {
		t.Fatalf("conflict transition projection batch=%#v err=%v", batch, err)
	}
	transitionEvent := mustTimelineEventBySource(t, ctx, repository, fixture.workspaceID, "knowledge-command:conflict.transition:"+string(conflict.ID)+":v3")
	if transitionEvent.EventType != domain.EventConflictTransitioned || transitionEvent.SourceRef != "conflict:"+string(conflict.ID) || transitionEvent.EventVersion != 3 {
		t.Fatalf("conflict transition event=%#v", transitionEvent)
	}
	resolvedEvent := mustTimelineEventBySource(t, ctx, repository, fixture.workspaceID, "knowledge-command:conflict.transition:"+string(conflict.ID)+":v4")
	if resolvedEvent.EventType != domain.EventConflictResolved || resolvedEvent.SourceRef != "conflict:"+string(conflict.ID) || resolvedEvent.EventVersion != 4 {
		t.Fatalf("conflict resolved event=%#v", resolvedEvent)
	}

	fingerprint, err := domain.ComputeImpactFingerprint(conflictEvent.ID, int64(conflictEvent.EventVersion), nil)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.ImpactReport{
		ID: newID(t), WorkspaceID: fixture.workspaceID, SourceEventID: conflictEvent.ID, SourceEventRef: conflictEvent.SourceEventRef,
		SourceVersion: int64(conflictEvent.EventVersion), Status: domain.ImpactReportReady, Objects: []domain.ImpactObject{}, Summary: domain.SummarizeImpactObjects(nil),
		Fingerprint: fingerprint, GeneratedAt: now.Add(5 * time.Second), CreatedAt: now.Add(5 * time.Second), Version: 1,
	}
	persisted, replayed, err := repository.SaveImpactReport(ctx, report)
	if err != nil || replayed || persisted.ID != report.ID {
		t.Fatalf("impact report=%#v replayed=%t err=%v", persisted, replayed, err)
	}
	batch, err = dispatcher.DispatchBatch(ctx, 10)
	if err != nil || batch.Processed != 1 || batch.Projected != 1 {
		t.Fatalf("impact projection batch=%#v err=%v", batch, err)
	}
	impactEvent := mustTimelineEventBySource(t, ctx, repository, fixture.workspaceID, "impact-report:"+string(report.ID)+":v1")
	if impactEvent.EventType != domain.EventImpactAnalyzed || impactEvent.AggregateType != domain.TimelineAggregateImpactReport || impactEvent.AggregateID == nil || *impactEvent.AggregateID != report.ID {
		t.Fatalf("impact event=%#v", impactEvent)
	}
}

func testTimelineProjectionPersistsPoisonWithoutWritingKnowledgeEvent(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	dispatcher, err := knowledgeapp.NewTimelineProjectionDispatcher(repository)
	if err != nil {
		t.Fatal(err)
	}
	drainTimelineProjectionQueue(t, ctx, dispatcher)
	fixture := seedProvenance(t, ctx, pool, "timeline-projector-poison")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	sourceID, eventID, aggregateID := newID(t), newID(t), newID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
summary,correlation,occurred_at,status,created_at,updated_at)
VALUES($1,$2,$3,'CONFLICT_OPENED','CONFLICT',$4,'poison:unknown-correlation','conflict:poison',1,
'poisoned projection','{"unexpected":"field"}',$5,'PENDING',$5,$5)`,
		string(sourceID), string(eventID), string(fixture.workspaceID), string(aggregateID), now); err != nil {
		t.Fatal(err)
	}
	batch, err := dispatcher.DispatchBatch(ctx, 1)
	var classified *foundation.Error
	if batch.Processed != 1 || batch.Poisoned != 1 || !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired || classified.Code != domain.ErrorCodeTimelineProjectionPoisoned {
		t.Fatalf("poison batch=%#v classified=%#v err=%v", batch, classified, err)
	}
	var status, errorCode string
	if err := pool.QueryRow(ctx, `SELECT status,error_code FROM ops.timeline_projection_outbox WHERE id=$1`, string(sourceID)).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "POISONED" || errorCode != domain.ErrorCodeTimelineProjectionPoisoned {
		t.Fatalf("projection status=%s error=%s", status, errorCode)
	}
	if _, err := repository.GetEvent(ctx, fixture.workspaceID, eventID); !hasCode(err, domain.ErrorCodeTimelineNotFound) {
		t.Fatalf("poisoned event lookup err=%v", err)
	}
}

func testTimelineProjectionV2PersistsOwnerBindingAndReplays(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	drainTimelineProjectionQueue(t, ctx, mustTimelineDispatcher(t, repository))
	fixture := seedProvenance(t, ctx, pool, "timeline-projector-v2")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)

	enqueue := func(event domain.KnowledgeEvent) (foundation.ID, foundation.ID) {
		t.Helper()
		var aggregateID, operatorID, ownerBinding any
		if event.AggregateID != nil {
			aggregateID = string(*event.AggregateID)
		}
		if event.Operator != nil && event.Operator.ID != nil {
			operatorID = string(*event.Operator.ID)
		}
		if event.OwnerBinding != nil {
			encoded, err := json.Marshal(event.OwnerBinding)
			if err != nil {
				t.Fatal(err)
			}
			ownerBinding = string(encoded)
		}
		correlation, err := json.Marshal(event.Correlation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `SELECT ops.enqueue_timeline_projection_v2(
$1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12::jsonb,$13)`,
			string(event.WorkspaceID), string(event.EventType), string(event.AggregateType), aggregateID,
			event.SourceEventRef, event.SourceRef, event.EventVersion, event.Summary, string(correlation),
			string(event.Operator.Type), operatorID, ownerBinding, event.OccurredAt); err != nil {
			t.Fatal(err)
		}
		var sourceID, eventID string
		if err := pool.QueryRow(ctx, `SELECT id::text,event_id::text FROM ops.timeline_projection_outbox
WHERE workspace_id=$1 AND source_event_ref=$2`, string(event.WorkspaceID), event.SourceEventRef).Scan(&sourceID, &eventID); err != nil {
			t.Fatal(err)
		}
		return foundation.ID(sourceID), foundation.ID(eventID)
	}

	artifactID, revisionID, operatorID := newID(t), newID(t), newID(t)
	projected := domain.KnowledgeEvent{
		WorkspaceID: fixture.workspaceID, EventType: domain.EventArtifactGenerated,
		AggregateType: domain.TimelineAggregateArtifact, AggregateID: &artifactID,
		SourceEventRef: "artifact-generated:" + string(artifactID) + ":" + string(revisionID) + ":v2",
		SourceRef:      "artifact:" + string(artifactID), EventVersion: 2,
		SchemaVersion: domain.KnowledgeEventSchemaVersionV2, Summary: "artifact revision generated",
		Payload: json.RawMessage(`{}`), Operator: &domain.EventOperator{Type: domain.EventOperatorUser, ID: &operatorID},
		OwnerBinding: &domain.EventOwnerBinding{Artifact: &domain.ArtifactImpactBinding{
			ArtifactID: artifactID, ArtifactVersion: 2, RevisionID: revisionID, RevisionNo: 2,
			ContentHash: testHash("timeline-v2-artifact"),
		}},
		OccurredAt: now, CreatedAt: now,
	}
	projectedSourceID, projectedEventID := enqueue(projected)
	projected.ID = projectedEventID
	result, found, err := repository.ProjectNext(ctx)
	if err != nil || !found || result.Outcome != knowledgeapp.TimelineProjectionProjected || result.SourceID != projectedSourceID || result.EventID != projectedEventID {
		t.Fatalf("v2 projection result=%#v found=%t err=%v", result, found, err)
	}
	loaded := mustTimelineEventBySource(t, ctx, repository, fixture.workspaceID, projected.SourceEventRef)
	if !sameTimelineEvent(loaded, projected) || !reflect.DeepEqual(loaded.Operator, projected.Operator) || !reflect.DeepEqual(loaded.OwnerBinding, projected.OwnerBinding) {
		t.Fatalf("v2 projected event=%#v want=%#v", loaded, projected)
	}

	cardID, claimID := newID(t), newID(t)
	replayed := domain.KnowledgeEvent{
		ID: newID(t), WorkspaceID: fixture.workspaceID, EventType: domain.EventReviewCardInvalidated,
		AggregateType: domain.TimelineAggregateReviewCard, AggregateID: &cardID,
		SourceEventRef: "review-card-invalidated:" + string(cardID) + ":v3",
		SourceRef:      "review-card:" + string(cardID), EventVersion: 3,
		SchemaVersion: domain.KnowledgeEventSchemaVersionV2, Summary: "review card invalidated",
		Payload: json.RawMessage(`{}`), Operator: &domain.EventOperator{Type: domain.EventOperatorUnknown},
		OwnerBinding: &domain.EventOwnerBinding{ReviewCard: &domain.ReviewCardImpactBinding{
			CardID: cardID, CardVersion: 3, Status: "INVALIDATED", Fingerprint: testHash("timeline-v2-card"),
			ClaimID: claimID, EvidenceBindingFingerprint: testHash("timeline-v2-card-evidence"),
		}},
		OccurredAt: now.Add(time.Second), CreatedAt: now.Add(time.Second),
	}
	if persisted, wasReplay, err := repository.AppendEvent(ctx, replayed); err != nil || wasReplay || !sameTimelineEvent(persisted, replayed) {
		t.Fatalf("seed replay event=%#v replayed=%t err=%v", persisted, wasReplay, err)
	}
	replayedSourceID, replayedEventID := enqueue(replayed)
	if replayedEventID != replayed.ID {
		t.Fatalf("replayed outbox event id=%s want=%s", replayedEventID, replayed.ID)
	}
	result, found, err = repository.ProjectNext(ctx)
	if err != nil || !found || result.Outcome != knowledgeapp.TimelineProjectionReplayed || result.SourceID != replayedSourceID || result.EventID != replayed.ID {
		t.Fatalf("v2 replay result=%#v found=%t err=%v", result, found, err)
	}
	operatorDrift := replayed
	operatorDrift.Operator = &domain.EventOperator{Type: domain.EventOperatorSystem}
	if _, _, err := repository.AppendEvent(ctx, operatorDrift); !classifiedAs(err, foundation.ErrorVersionConflict) {
		t.Fatalf("v2 operator drift error=%v", err)
	}
	ownerDrift := replayed
	reviewBindingDrift := *replayed.OwnerBinding.ReviewCard
	reviewBindingDrift.Fingerprint = testHash("timeline-v2-card-drift")
	ownerDrift.OwnerBinding = &domain.EventOwnerBinding{ReviewCard: &reviewBindingDrift}
	if _, _, err := repository.AppendEvent(ctx, ownerDrift); !classifiedAs(err, foundation.ErrorVersionConflict) {
		t.Fatalf("v2 owner binding drift error=%v", err)
	}
}

func testTimelineProjectionV2PoisonsMalformedOwnerAndOperator(t *testing.T, testCase timelineIntegrationCase) {
	repository, tx, ctx := testCase.repository, testCase.pool, testCase.ctx
	drainTimelineProjectionQueue(t, ctx, mustTimelineDispatcher(t, repository))
	fixture := seedProvenance(t, ctx, tx, "timeline-projector-v2-poison")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	operatorSourceID, operatorEventID, conflictID := newID(t), newID(t), newID(t)
	ownerSourceID, ownerEventID, artifactID, revisionID := newID(t), newID(t), newID(t), newID(t)

	if _, err := tx.Exec(ctx, `ALTER TABLE ops.timeline_projection_outbox
DROP CONSTRAINT ops_timeline_projection_v2_wire`); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
schema_version,summary,correlation,operator_type,operator_id,owner_binding,occurred_at,status,created_at,updated_at)
VALUES($1,$2,$3,'CONFLICT_OPENED','CONFLICT',$4,$5,$6,1,'knowledge-event/v2',
'malformed operator projection','{}','SYSTEM',$7,NULL,$8,'PENDING',$8,$8)`,
		string(operatorSourceID), string(operatorEventID), string(fixture.workspaceID), string(conflictID),
		"malformed-operator:"+string(operatorSourceID), "conflict:"+string(conflictID), string(newID(t)), now); err != nil {
		t.Fatal(err)
	}
	malformedOwner := `{"artifact":{"artifact_id":"` + string(artifactID) + `","artifact_version":1,"revision_id":"` + string(revisionID) + `","revision_no":1,"content_hash":"` + testHash("malformed-owner") + `","unexpected":true}}`
	if _, err := tx.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
schema_version,summary,correlation,operator_type,operator_id,owner_binding,occurred_at,status,created_at,updated_at)
VALUES($1,$2,$3,'ARTIFACT_GENERATED','ARTIFACT',$4,$5,$6,1,'knowledge-event/v2',
'malformed owner projection','{}','SYSTEM',NULL,$7::jsonb,$8,'PENDING',$8,$8)`,
		string(ownerSourceID), string(ownerEventID), string(fixture.workspaceID), string(artifactID),
		"malformed-owner:"+string(ownerSourceID), "artifact:"+string(artifactID), malformedOwner, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []struct {
		sourceID foundation.ID
		eventID  foundation.ID
	}{
		{sourceID: operatorSourceID, eventID: operatorEventID},
		{sourceID: ownerSourceID, eventID: ownerEventID},
	} {
		result, found, err := repository.ProjectNext(ctx)
		var classified *foundation.Error
		if !found || result.SourceID != expected.sourceID || result.EventID != expected.eventID || result.Outcome != knowledgeapp.TimelineProjectionPoisoned || !errors.As(err, &classified) || classified.Kind != foundation.ErrorManualRecoveryRequired || classified.Code != domain.ErrorCodeTimelineProjectionPoisoned {
			t.Fatalf("malformed v2 projection result=%#v found=%t classified=%#v err=%v", result, found, classified, err)
		}
		var status, errorCode string
		if err := tx.QueryRow(ctx, `SELECT status,error_code FROM ops.timeline_projection_outbox WHERE id=$1`, string(expected.sourceID)).Scan(&status, &errorCode); err != nil {
			t.Fatal(err)
		}
		if status != "POISONED" || errorCode != domain.ErrorCodeTimelineProjectionPoisoned {
			t.Fatalf("malformed v2 status=%s error=%s", status, errorCode)
		}
		if _, err := repository.GetEvent(ctx, fixture.workspaceID, expected.eventID); !hasCode(err, domain.ErrorCodeTimelineNotFound) {
			t.Fatalf("malformed v2 event lookup err=%v", err)
		}
	}
}

func testTimelineProjectionTwoDispatchersSkipLockedAndPersistExactlyOneEvent(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	drainTimelineProjectionQueue(t, ctx, mustTimelineDispatcher(t, repository))
	fixture := seedProvenance(t, ctx, pool, "timeline-projector-concurrent")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	sourceID, eventID, aggregateID := newID(t), newID(t), newID(t)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.timeline_projection_outbox(
	id,event_id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
	summary,correlation,occurred_at,status,created_at,updated_at)
VALUES($1,$2,$3,'CONFLICT_OPENED','CONFLICT',$4,$5,$6,1,
'concurrent projection','{}',$7,'PENDING',$7,$7)`,
		string(sourceID), string(eventID), string(fixture.workspaceID), string(aggregateID),
		"concurrent-projection:"+string(sourceID), "conflict:"+string(aggregateID), now); err != nil {
		t.Fatal(err)
	}

	locked, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Rollback(context.Background()) }()
	var lockedID string
	if err := locked.QueryRow(ctx, `SELECT id::text FROM ops.timeline_projection_outbox
WHERE id=$1 FOR UPDATE SKIP LOCKED`, string(sourceID)).Scan(&lockedID); err != nil || lockedID != string(sourceID) {
		t.Fatalf("lock projection source id=%q err=%v", lockedID, err)
	}
	if _, found, err := repository.ProjectNext(ctx); err != nil || found {
		t.Fatalf("SKIP LOCKED claim found=%t err=%v", found, err)
	}
	if err := locked.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	secondRepository := testCase.openRepository(t)
	start := make(chan struct{})
	type dispatchResult struct {
		result knowledgeapp.TimelineProjectionResult
		found  bool
		err    error
	}
	results := make(chan dispatchResult, 2)
	var workers sync.WaitGroup
	for _, dispatcher := range []knowledgeapp.TimelineProjectionPort{repository, secondRepository} {
		workers.Add(1)
		go func(projector knowledgeapp.TimelineProjectionPort) {
			defer workers.Done()
			<-start
			result, found, projectErr := projector.ProjectNext(ctx)
			results <- dispatchResult{result: result, found: found, err: projectErr}
		}(dispatcher)
	}
	close(start)
	workers.Wait()
	close(results)

	projected := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent projection result=%#v", result)
		}
		if result.found {
			if result.result.Outcome != knowledgeapp.TimelineProjectionProjected || result.result.SourceID != sourceID || result.result.EventID != eventID {
				t.Fatalf("unexpected concurrent projection result=%#v", result)
			}
			projected++
		}
	}
	if projected != 1 {
		t.Fatalf("concurrent projected=%d want 1", projected)
	}

	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.knowledge_event
WHERE workspace_id=$1 AND source_event_ref=$2`, fixture.workspaceID, "concurrent-projection:"+string(sourceID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("concurrent Knowledge Event count=%d want 1", events)
	}
	var status string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT status,version FROM ops.timeline_projection_outbox WHERE id=$1`, string(sourceID)).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != "PROJECTED" || version != 2 {
		t.Fatalf("concurrent projection CAS status=%s version=%d", status, version)
	}
}

func testTimelineRepositoryReturnsConnections(t *testing.T, testCase timelineIntegrationCase) {
	repository, pool, ctx := testCase.repository, testCase.pool, testCase.ctx
	fixture := seedProvenance(t, ctx, pool, "timeline-connection-resources")
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	event := timelineIntegrationEvent(newID(t), fixture.workspaceID, newID(t), "timeline:connection-resources", now)
	if _, replayed, err := repository.AppendEvent(ctx, event); err != nil || replayed {
		t.Fatalf("append connection event replayed=%t err=%v", replayed, err)
	}
	for iteration := 0; iteration < 20; iteration++ {
		page, err := repository.ListEvents(ctx, domain.TimelineQuery{
			WorkspaceID: fixture.workspaceID,
			Filter:      domain.TimelineFilter{SourceEventRef: event.SourceEventRef},
			Limit:       1,
		})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != event.ID {
			t.Fatalf("connection page iteration=%d page=%#v err=%v", iteration, page, err)
		}
		if _, err := repository.GetEvent(ctx, fixture.workspaceID, event.ID); err != nil {
			t.Fatalf("connection event iteration=%d err=%v", iteration, err)
		}
		if _, _, err := repository.GetImpactReport(ctx, fixture.workspaceID, event.ID, domain.ImpactAnalysisVersionV2); err != nil {
			t.Fatalf("connection report iteration=%d err=%v", iteration, err)
		}
	}
	database, err := testCase.platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	if stats := sqlDB.Stats(); stats.InUse != 0 {
		t.Fatalf("GORM connections still in use=%d", stats.InUse)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("pgx connections still acquired=%d", acquired)
	}
}

func mustTimelineDispatcher(t *testing.T, repository knowledgeapp.TimelineProjectionPort) *knowledgeapp.TimelineProjectionDispatcher {
	t.Helper()
	dispatcher, err := knowledgeapp.NewTimelineProjectionDispatcher(repository)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func drainTimelineProjectionQueue(t *testing.T, ctx context.Context, dispatcher *knowledgeapp.TimelineProjectionDispatcher) {
	t.Helper()
	for iteration := 0; iteration < 100; iteration++ {
		batch, err := dispatcher.DispatchBatch(ctx, knowledgeapp.MaxTimelineProjectionBatch)
		if err != nil {
			t.Fatalf("drain timeline projection queue batch=%#v err=%v", batch, err)
		}
		if batch.Processed < knowledgeapp.MaxTimelineProjectionBatch {
			return
		}
	}
	t.Fatal("timeline projection queue did not drain within 100 batches")
}

func mustTimelineEventBySource(t *testing.T, ctx context.Context, repository knowledgeapp.TimelineReader, workspaceID foundation.ID, sourceEventRef string) domain.KnowledgeEvent {
	t.Helper()
	page, err := repository.ListEvents(ctx, domain.TimelineQuery{
		WorkspaceID: workspaceID,
		Filter:      domain.TimelineFilter{SourceEventRef: sourceEventRef},
		Limit:       1,
	})
	if err != nil || len(page.Items) != 1 || page.HasMore {
		t.Fatalf("timeline source %q page=%#v err=%v", sourceEventRef, page, err)
	}
	return page.Items[0]
}

func timelineIntegrationEvent(id, workspaceID, aggregateID foundation.ID, sourceEventRef string, occurredAt time.Time) domain.KnowledgeEvent {
	return domain.KnowledgeEvent{
		ID: id, WorkspaceID: workspaceID, EventType: domain.EventConflictResolved, AggregateType: domain.TimelineAggregateConflict,
		AggregateID: &aggregateID, SourceEventRef: sourceEventRef, SourceRef: "conflict:" + string(aggregateID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "conflict resolved", Payload: json.RawMessage(`{}`),
		OccurredAt: occurredAt, CreatedAt: occurredAt,
	}
}
