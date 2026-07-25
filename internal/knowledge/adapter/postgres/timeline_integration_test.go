//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphtestfixture "github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestTimelineRepositoryAppendPageAndImpactReplay(t *testing.T) {
	repository, tx, ctx := integrationRepository(t)
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
	saved, replayed, err := repository.SaveImpactReport(ctx, report)
	if err != nil || replayed || saved.ID != report.ID {
		t.Fatalf("save report=%#v replayed=%t err=%v", saved, replayed, err)
	}
	if _, replayed, err = repository.SaveImpactReport(ctx, report); err != nil || !replayed {
		t.Fatalf("report replay=%t err=%v", replayed, err)
	}
	loaded, err := repository.GetImpactReportByID(ctx, fixture.workspaceID, report.ID)
	if err != nil || loaded.Fingerprint != fingerprint || loaded.SourceEventRef != newer.SourceEventRef {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestImpactReportAndTimelineOutboxRollBackWhenTransactionalAuditFails(t *testing.T) {
	repository, tx, ctx := integrationRepository(t)
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
	if _, _, err := repository.SaveImpactReportWithAudit(ctx, report, "audit-rollback", impactAuditFailureStub{err: auditFailure}); !errors.Is(err, auditFailure) {
		t.Fatalf("atomic impact save error=%v", err)
	}
	if _, found, err := repository.GetImpactReport(ctx, fixture.workspaceID, event.ID); err != nil || found {
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

func TestTimelineRepositoryListImpactObjectsKeepsActionsReadOnly(t *testing.T) {
	repository, pool, ctx := integrationPoolRepository(t)
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

func TestTimelineProjectionOutboxConnectsProposalConflictAndImpact(t *testing.T) {
	repository, pool, ctx := integrationPoolRepository(t)
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

func TestTimelineProjectionPersistsPoisonWithoutWritingKnowledgeEvent(t *testing.T) {
	repository, pool, ctx := integrationPoolRepository(t)
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

func TestTimelineProjectionTwoDispatchersSkipLockedAndPersistExactlyOneEvent(t *testing.T) {
	repository, pool, ctx := integrationPoolRepository(t)
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

	secondRepository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type dispatchResult struct {
		result knowledgeapp.TimelineProjectionResult
		found  bool
		err    error
	}
	results := make(chan dispatchResult, 2)
	var workers sync.WaitGroup
	for _, dispatcher := range []*Repository{repository, secondRepository} {
		workers.Add(1)
		go func(projector *Repository) {
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

func mustTimelineDispatcher(t *testing.T, repository *Repository) *knowledgeapp.TimelineProjectionDispatcher {
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

func mustTimelineEventBySource(t *testing.T, ctx context.Context, repository *Repository, workspaceID foundation.ID, sourceEventRef string) domain.KnowledgeEvent {
	t.Helper()
	event, found, err := getTimelineEventBySource(ctx, repository.db, workspaceID, sourceEventRef)
	if err != nil || !found {
		t.Fatalf("timeline source %q found=%t err=%v", sourceEventRef, found, err)
	}
	return event
}

func timelineIntegrationEvent(id, workspaceID, aggregateID foundation.ID, sourceEventRef string, occurredAt time.Time) domain.KnowledgeEvent {
	return domain.KnowledgeEvent{
		ID: id, WorkspaceID: workspaceID, EventType: domain.EventConflictResolved, AggregateType: domain.TimelineAggregateConflict,
		AggregateID: &aggregateID, SourceEventRef: sourceEventRef, SourceRef: "conflict:" + string(aggregateID),
		EventVersion: 1, SchemaVersion: domain.KnowledgeEventSchemaVersion, Summary: "conflict resolved", Payload: json.RawMessage(`{}`),
		OccurredAt: occurredAt, CreatedAt: occurredAt,
	}
}
