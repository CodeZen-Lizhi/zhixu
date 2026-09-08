//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"gorm.io/gorm"
)

func TestGORMCandidateRepositoryCreatesAndReplaysWithBatchEvidence(t *testing.T) {
	databaseFixture := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         8,
	})
	platform := databaseFixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("Graph Candidate fixture did not provide a shared platform pool")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	seedTx, err := platform.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seedCommitted := false
	defer func() {
		if !seedCommitted {
			_ = seedTx.Rollback(context.Background())
		}
	}()

	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, seedTx, now)
	provenance := seedGraphProvenance(t, ctx, seedTx, workspaceID, now)
	firstClaimID := seedGraphClaim(t, ctx, seedTx, workspaceID, "GORM candidate source", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	secondClaimID := seedGraphClaim(t, ctx, seedTx, workspaceID, "GORM candidate target", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	candidate := candidateFixture(t, workspaceID, firstClaimID, secondClaimID, provenance, now, "gorm-main", 1, 1)
	secondEvidence := candidate.Evidence[0]
	secondEvidence.ID = graphTestID(t)
	secondEvidence.SemanticHash = graphHash("candidate-evidence-gorm-main-second")
	secondEvidence.Reason = "second bounded source support"
	secondEvidence.Excerpt = "second bounded evidence excerpt"
	candidate.Evidence = append(candidate.Evidence, secondEvidence)
	if candidate.Evidence[1].SemanticHash < candidate.Evidence[0].SemanticHash {
		candidate.Evidence[0], candidate.Evidence[1] = candidate.Evidence[1], candidate.Evidence[0]
	}
	candidate.Fingerprint = ""
	candidate.Fingerprint, err = graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	seedCommitted = true

	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.UpsertSemanticLinkCandidate(ctx, candidate)
	if err != nil || !created.Created || created.Candidate.ID != candidate.ID || len(created.Candidate.Evidence) != 2 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	replayed, err := repository.UpsertSemanticLinkCandidate(ctx, candidate)
	if err != nil || replayed.Created || replayed.Candidate.ID != candidate.ID || replayed.Candidate.Fingerprint != candidate.Fingerprint || len(replayed.Candidate.Evidence) != 2 {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}

	page, err := repository.ListSemanticLinkCandidates(ctx, graphdomain.SemanticLinkCandidateQuery{WorkspaceID: workspaceID, Limit: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != candidate.ID || len(page.Items[0].Evidence) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	assertGORMCandidateConfirmAtomicity(t, ctx, platform, candidate, now)
}

func TestSemanticLinkCandidateRepositoryLifecycleAndBatchHydration(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	firstClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "first candidate claim", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	secondClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "second candidate claim", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)

	candidate := candidateFixture(t, workspaceID, firstClaimID, secondClaimID, provenance, now, "first", 1, 1)
	created, err := repository.UpsertSemanticLinkCandidate(ctx, candidate)
	if err != nil || !created.Created || created.Candidate.ID != candidate.ID || created.Suppressed || created.Reopened {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			t.Fatalf("created=%+v err=%v cause=%v", created, err, classified.Cause)
		}
		t.Fatalf("created=%+v err=%v", created, err)
	}
	replayed, err := repository.UpsertSemanticLinkCandidate(ctx, candidate)
	if err != nil || replayed.Created || replayed.Candidate.ID != candidate.ID || replayed.Candidate.Fingerprint != candidate.Fingerprint {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}

	queryCount := 0
	stopCounting := graphRowHook(t, tx.platform, false, func(*gorm.DB) { queryCount++ })
	page, err := repository.ListSemanticLinkCandidates(ctx, graphdomain.SemanticLinkCandidateQuery{WorkspaceID: workspaceID, Limit: 20})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Evidence) != 1 || page.Items[0].Source.Excerpt != candidate.Source.Excerpt || page.Items[0].Target.Excerpt != candidate.Target.Excerpt {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if queryCount != 2 {
		t.Fatalf("candidate list queries=%d, want 2", queryCount)
	}
	if page.Items[0].Evidence[0].Excerpt != candidate.Evidence[0].Excerpt {
		t.Fatalf("evidence was not hydrated: %+v", page.Items[0].Evidence)
	}
	stopCounting()

	otherWorkspaceID := seedGraphWorkspace(t, ctx, tx, now.Add(time.Microsecond))
	otherClaimID := seedGraphClaim(t, ctx, tx, otherWorkspaceID, "cross workspace claim", knowledge.ClaimStatusSuggested, floatPointer(0.7), now)
	crossWorkspace := candidateFixture(t, workspaceID, firstClaimID, otherClaimID, provenance, now.Add(time.Microsecond), "cross-workspace", 1, 1)
	if _, err := repository.UpsertSemanticLinkCandidate(ctx, crossWorkspace); !hasGraphCode(err, "GRAPH_CANDIDATE_ENDPOINT_NOT_FOUND") {
		t.Fatalf("cross-workspace candidate err=%v", err)
	}
	futureVersion := candidateFixture(t, workspaceID, firstClaimID, secondClaimID, provenance, now.Add(2*time.Microsecond), "future-version", 2, 1)
	if _, err := repository.UpsertSemanticLinkCandidate(ctx, futureVersion); !hasGraphCode(err, graphdomain.ErrorCodeSemanticLinkCandidateTransitionInvalid) {
		t.Fatalf("future-version candidate err=%v", err)
	}

	ignore := graphapp.SemanticLinkCandidateDecisionCommand{
		WorkspaceID: workspaceID, CandidateID: candidate.ID, ExpectedVersion: 1, IdempotencyKey: "candidate-ignore-1",
		Decision: graphdomain.SemanticLinkCandidateDecision{Action: graphdomain.SemanticLinkCandidateDecisionIgnore, Reason: "not enough independent support"},
	}
	ignored, err := repository.DecideSemanticLinkCandidate(ctx, ignore, nil)
	if err != nil || ignored.Candidate.Status != graphdomain.SemanticLinkCandidateStatusIgnored || ignored.Candidate.Version != 2 {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			t.Fatalf("ignored=%+v err=%v cause=%v", ignored, err, classified.Cause)
		}
		t.Fatalf("ignored=%+v err=%v", ignored, err)
	}
	ignoreReplay, err := repository.DecideSemanticLinkCandidate(ctx, ignore, nil)
	if err != nil || ignoreReplay.Candidate.Status != ignored.Candidate.Status || ignoreReplay.Candidate.Version != ignored.Candidate.Version {
		t.Fatalf("ignore replay=%+v err=%v", ignoreReplay, err)
	}
	conflictingReplay := ignore
	conflictingReplay.Decision.Reason = "different payload"
	if _, err := repository.DecideSemanticLinkCandidate(ctx, conflictingReplay, nil); !hasGraphCode(err, graphdomain.ErrorCodeSemanticLinkCandidateTransitionInvalid) {
		t.Fatalf("idempotency conflict err=%v", err)
	}
	if replay, err := repository.UpsertSemanticLinkCandidate(ctx, candidate); err != nil || !replay.Suppressed || replay.Candidate.Status != graphdomain.SemanticLinkCandidateStatusIgnored {
		t.Fatalf("suppressed replay=%+v err=%v", replay, err)
	}

	changed := candidateFixture(t, workspaceID, firstClaimID, secondClaimID, provenance, now.Add(time.Second), "changed", 1, 1)
	reopened, err := repository.UpsertSemanticLinkCandidate(ctx, changed)
	if err != nil || !reopened.Created || !reopened.Reopened || reopened.Candidate.ReopenedFromCandidateID == nil || *reopened.Candidate.ReopenedFromCandidateID != candidate.ID || reopened.Candidate.ReopenedReason != graphdomain.SemanticLinkCandidateReopenedReasonContentChanged {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	old, err := repository.GetSemanticLinkCandidate(ctx, workspaceID, candidate.ID)
	if err != nil || old.Status != graphdomain.SemanticLinkCandidateStatusSuperseded || old.Version != 3 {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	filtered, err := repository.ListSemanticLinkCandidates(ctx, graphdomain.SemanticLinkCandidateQuery{
		WorkspaceID: workspaceID, Statuses: []graphdomain.SemanticLinkCandidateStatus{graphdomain.SemanticLinkCandidateStatusActive},
		ReopenedReasons: []graphdomain.SemanticLinkCandidateReopenedReason{graphdomain.SemanticLinkCandidateReopenedReasonContentChanged}, Limit: 20,
	})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != changed.ID {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}

	deferred := candidateFixture(t, workspaceID, firstClaimID, secondClaimID, provenance, now.Add(2*time.Second), "defer", 1, 1)
	deferredResult, err := repository.UpsertSemanticLinkCandidate(ctx, deferred)
	if err != nil || !deferredResult.Created {
		t.Fatalf("deferred candidate=%+v err=%v", deferredResult, err)
	}
	resumeAt := now.Add(10 * time.Minute)
	deferCommand := graphapp.SemanticLinkCandidateDecisionCommand{
		WorkspaceID: workspaceID, CandidateID: deferred.ID, ExpectedVersion: 1, IdempotencyKey: "candidate-defer-1",
		Decision: graphdomain.SemanticLinkCandidateDecision{Action: graphdomain.SemanticLinkCandidateDecisionDefer, Reason: "review after source refresh", ResumeAfter: &resumeAt},
	}
	deferredDecision, err := repository.DecideSemanticLinkCandidate(ctx, deferCommand, nil)
	if err != nil || deferredDecision.Candidate.Status != graphdomain.SemanticLinkCandidateStatusDeferred || deferredDecision.Candidate.ResumeAfter == nil || !deferredDecision.Candidate.ResumeAfter.Equal(resumeAt) {
		t.Fatalf("deferred result=%+v err=%v", deferredDecision, err)
	}
	resumed, err := repository.DecideSemanticLinkCandidate(ctx, graphapp.SemanticLinkCandidateDecisionCommand{
		WorkspaceID: workspaceID, CandidateID: deferred.ID, ExpectedVersion: 2, IdempotencyKey: "candidate-resume-1",
		Decision: graphdomain.SemanticLinkCandidateDecision{Action: graphdomain.SemanticLinkCandidateDecisionResume},
	}, nil)
	if err != nil || resumed.Candidate.Status != graphdomain.SemanticLinkCandidateStatusActive || resumed.Candidate.ResumeAfter != nil {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	staleDefer := deferCommand
	staleDefer.IdempotencyKey = "candidate-defer-stale"
	if _, err := repository.DecideSemanticLinkCandidate(ctx, staleDefer, nil); !hasGraphCode(err, graphdomain.ErrorCodeSemanticLinkCandidateTransitionInvalid) {
		t.Fatalf("stale decision err=%v", err)
	}

	var decisionCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate_decision WHERE workspace_id=$1`, string(workspaceID)).Scan(&decisionCount); err != nil {
		t.Fatal(err)
	}
	if decisionCount != 3 {
		t.Fatalf("decision history count=%d", decisionCount)
	}
	_, err = tx.Exec(ctx, `UPDATE graph.semantic_link_candidate_decision SET reason='mutated' WHERE workspace_id=$1`, string(workspaceID))
	if err == nil {
		t.Fatal("append-only decision update unexpectedly succeeded")
	}
}

func TestSemanticLinkCandidateRepositoryListsClaimPairsByTopicMembership(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	firstTopicID := seedGraphTopic(t, ctx, tx, workspaceID, "Topic A", "topic a", now)
	secondTopicID := seedGraphTopic(t, ctx, tx, workspaceID, "Topic B", "topic b", now)
	firstClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "first topic candidate", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	secondClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "second topic candidate", knowledge.ClaimStatusConfirmed, floatPointer(0.8), now)
	thirdClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "unrelated topic candidate", knowledge.ClaimStatusConfirmed, floatPointer(0.7), now)
	for _, membership := range []struct {
		claimID foundation.ID
		topicID foundation.ID
	}{
		{claimID: firstClaimID, topicID: firstTopicID},
		{claimID: secondClaimID, topicID: firstTopicID},
		{claimID: secondClaimID, topicID: secondTopicID},
		{claimID: thirdClaimID, topicID: secondTopicID},
	} {
		seedGraphRelation(t, ctx, tx, workspaceID, membership.claimID, knowledge.NodeTypeClaim,
			membership.topicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo,
			knowledge.RelationStatusConfirmed, floatPointer(0.9), now)
	}

	firstCandidate := candidateFixture(t, workspaceID, firstClaimID, secondClaimID, provenance, now, "topic-a", 2, 2)
	secondCandidate := candidateFixture(t, workspaceID, secondClaimID, thirdClaimID, provenance, now.Add(time.Microsecond), "topic-b", 2, 2)
	directTopicCandidate := candidateFixture(t, workspaceID, thirdClaimID, firstTopicID, provenance, now.Add(2*time.Microsecond), "topic-direct", 2, 1)
	directTopicCandidate.Target.Ref.Type = knowledge.NodeTypeTopic
	directTopicCandidate.SuggestedRelationType = knowledge.RelationBelongsTo
	directTopicCandidate.Fingerprint = ""
	directTopicFingerprint, err := graphdomain.ComputeSemanticLinkCandidateFingerprint(directTopicCandidate)
	if err != nil {
		t.Fatal(err)
	}
	directTopicCandidate.Fingerprint = directTopicFingerprint
	if err := graphdomain.ValidateSemanticLinkCandidate(directTopicCandidate); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []graphdomain.SemanticLinkCandidate{firstCandidate, secondCandidate, directTopicCandidate} {
		if _, err := repository.UpsertSemanticLinkCandidate(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}

	topicRef := knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: firstTopicID}
	page, err := repository.ListSemanticLinkCandidates(ctx, graphdomain.SemanticLinkCandidateQuery{
		WorkspaceID: workspaceID,
		NodeRef:     &topicRef,
		Statuses:    []graphdomain.SemanticLinkCandidateStatus{graphdomain.SemanticLinkCandidateStatusActive},
		Limit:       20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("topic-scoped candidates=%+v, want direct Topic endpoint and member Claim pair", page.Items)
	}
	returned := map[foundation.ID]bool{}
	for _, candidate := range page.Items {
		returned[candidate.ID] = true
	}
	if !returned[firstCandidate.ID] || !returned[directTopicCandidate.ID] || returned[secondCandidate.ID] {
		t.Fatalf("topic-scoped candidate ids=%v", returned)
	}
}

func TestSemanticLinkCandidateRepositoryFingerprintConcurrency(t *testing.T) {
	pool := newGraphTestPool(t, 12)
	ctx := t.Context()
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seedTx.Rollback(context.Background()) }()
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := seedGraphWorkspace(t, ctx, seedTx, now)
	provenance := seedGraphProvenance(t, ctx, seedTx, workspaceID, now)
	firstClaimID := seedGraphClaim(t, ctx, seedTx, workspaceID, "concurrent first", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	secondClaimID := seedGraphClaim(t, ctx, seedTx, workspaceID, "concurrent second", knowledge.ClaimStatusSuggested, floatPointer(0.8), now)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	candidate := candidateFixture(t, workspaceID, firstClaimID, secondClaimID, provenance, now, "concurrent", 1, 1)

	const workers = 8
	results := make(chan SemanticLinkCandidateUpsertResult, workers)
	errorsCh := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			repository, repositoryErr := NewGORMRepository(pool.platform)
			if repositoryErr != nil {
				errorsCh <- repositoryErr
				return
			}
			result, upsertErr := repository.UpsertSemanticLinkCandidate(ctx, candidate)
			if upsertErr != nil {
				errorsCh <- upsertErr
				return
			}
			results <- result
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent upsert error: %v", err)
		}
	}
	createdCount := 0
	var winner foundation.ID
	for result := range results {
		if result.Created {
			createdCount++
		}
		if winner == "" {
			winner = result.Candidate.ID
		}
		if result.Candidate.ID != winner || result.Candidate.Fingerprint != candidate.Fingerprint {
			t.Fatalf("concurrent result=%+v winner=%s", result, winner)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created_count=%d", createdCount)
	}
	var persisted int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate WHERE workspace_id=$1 AND fingerprint=$2`, string(workspaceID), candidate.Fingerprint).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 {
		t.Fatalf("persisted=%d", persisted)
	}

	decision := graphapp.SemanticLinkCandidateDecisionCommand{
		WorkspaceID: workspaceID, CandidateID: candidate.ID, ExpectedVersion: 1, IdempotencyKey: "concurrent-ignore",
		Decision: graphdomain.SemanticLinkCandidateDecision{Action: graphdomain.SemanticLinkCandidateDecisionIgnore, Reason: "concurrent exact replay"},
	}
	decisionResults := make(chan graphapp.SemanticLinkCandidateDecisionResult, workers)
	decisionErrors := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			repository, repositoryErr := NewGORMRepository(pool.platform)
			if repositoryErr != nil {
				decisionErrors <- repositoryErr
				return
			}
			result, decisionErr := repository.DecideSemanticLinkCandidate(ctx, decision, nil)
			if decisionErr != nil {
				decisionErrors <- decisionErr
				return
			}
			decisionResults <- result
		}()
	}
	waitGroup.Wait()
	close(decisionResults)
	close(decisionErrors)
	for err := range decisionErrors {
		if err != nil {
			t.Fatalf("concurrent decision error: %v", err)
		}
	}
	for result := range decisionResults {
		if result.Candidate.ID != candidate.ID || result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusIgnored || result.Candidate.Version != 2 {
			t.Fatalf("concurrent decision result=%+v", result)
		}
	}
	var receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.semantic_link_candidate_decision WHERE workspace_id=$1 AND candidate_id=$2`, string(workspaceID), string(candidate.ID)).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 {
		t.Fatalf("decision receipts=%d", receiptCount)
	}
}

func candidateFixture(t *testing.T, workspaceID, sourceID, targetID foundation.ID, provenance graphProvenance, now time.Time, label string, sourceVersion, targetVersion int64) graphdomain.SemanticLinkCandidate {
	t.Helper()
	evidenceHash := graphHash("candidate-evidence-" + label)
	evidence := graphdomain.SemanticLinkCandidateEvidence{
		ID:           graphTestID(t),
		Provenance:   knowledge.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: provenance.sourceVersionID, SourceSpanID: provenance.sourceSpanID},
		SemanticHash: evidenceHash, Reason: "bounded source support", Excerpt: "bounded evidence excerpt",
	}
	candidate := graphdomain.SemanticLinkCandidate{
		ID: graphTestID(t), WorkspaceID: workspaceID,
		Source:                graphdomain.SemanticLinkCandidateEndpoint{Ref: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: sourceID}, Version: sourceVersion, Summary: "source summary " + label, Excerpt: "source excerpt " + label},
		Target:                graphdomain.SemanticLinkCandidateEndpoint{Ref: knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: targetID}, Version: targetVersion, Summary: "target summary " + label, Excerpt: "target excerpt " + label},
		SuggestedRelationType: knowledge.RelationComplements, Status: graphdomain.SemanticLinkCandidateStatusActive,
		Reason: "shared evidence suggests a complementary relation", Confidence: 0.82,
		DiscoveryMethods: []graphdomain.SemanticLinkDiscoveryMethod{graphdomain.SemanticLinkDiscoveryMethodCommonTopic, graphdomain.SemanticLinkDiscoveryMethodTitleAlias},
		Evidence:         []graphdomain.SemanticLinkCandidateEvidence{evidence},
		Generation:       graphdomain.SemanticLinkCandidateGeneration{RuleID: ptrID(graphTestID(t)), RuleVersion: "semantic-rule-v1"},
		Version:          1, CreatedAt: now, UpdatedAt: now,
	}
	fingerprint, err := graphdomain.ComputeSemanticLinkCandidateFingerprint(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Fingerprint = fingerprint
	if err := graphdomain.ValidateSemanticLinkCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func ptrID(value foundation.ID) *foundation.ID { return &value }
