//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

func TestAnswerFinalizerAtomicallyPublishesRefusalAndExactlyReplaysConcurrently(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	command := fixture.command(finalizerRefusalProposal(fixture.run.ID))

	type outcome struct {
		replayed bool
		err      error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command)
			outcomes <- outcome{replayed, err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)
	created, replayed := 0, 0
	for result := range outcomes {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatalf("created=%d replayed=%d", created, replayed)
	}

	receipt, found, err := fixture.finalizer.Lookup(fixture.ctx, fixture.lookup)
	if err != nil || !found || receipt.AnswerID != fixture.dispatched.Answer.ID || receipt.ResultHash == "" {
		t.Fatalf("Lookup=%#v found=%t err=%v", receipt, found, err)
	}
	var answerStatus, runStatus, runResult, eventType string
	var answerVersion, runVersion, conversationVersion, eventCount int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,m.final_result_type,a.version,m.version,c.version,
		(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.refused'),
		(SELECT event_type FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type='answer.refused')
		FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id JOIN agent.conversation c ON c.id=a.conversation_id WHERE a.id=$1`,
		string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &runResult, &answerVersion, &runVersion, &conversationVersion, &eventCount, &eventType); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "refused" || runStatus != "REFUSED" || runResult != "refusal" || answerVersion != 2 || runVersion != 2 || conversationVersion != 3 || eventCount != 1 || eventType != "answer.refused" {
		t.Fatalf("answer=%s run=%s/%s versions=%d/%d/%d event=%d/%s", answerStatus, runStatus, runResult, answerVersion, runVersion, conversationVersion, eventCount, eventType)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM ops.server_event WHERE workspace_id=$1 AND event_type='answer.refused'`, string(fixture.lookup.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := fixture.finalizer.Finalize(fixture.ctx, command); err != nil || !replay {
		t.Fatalf("retention replay=%t err=%v", replay, err)
	}
	conflict := command
	conflict.Proposal.Refusal.Payload.Summary = "Different refusal."
	if _, _, err := fixture.finalizer.Finalize(fixture.ctx, conflict); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
		t.Fatalf("conflict=%v", err)
	}
}

func TestAnswerFinalizerRollsBackDraftModelRunAndAnswerWhenTerminalEventFails(t *testing.T) {
	fixture := newFinalizerFixture(t, true)
	proposal := seedFinalizerCompletedProposal(t, fixture)
	draft := insertFinalizerDraft(t, fixture, agentapplication.DraftStreamCompleted)
	command := fixture.command(proposal)
	command.Draft = &draft
	_, _, err := fixture.finalizer.Finalize(fixture.ctx, command)
	if finalizerErrorCode(err) != "TEST_FINALIZER_EVENT_FAILURE" {
		t.Fatalf("Finalize error=%v", err)
	}
	var answerStatus, runStatus, draftStatus string
	var answerVersion, runVersion, conversationVersion int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,a.version,m.version,c.version,d.status
		FROM agent.answer a JOIN agent.model_run m ON m.workflow_run_id=a.workflow_run_id
		JOIN agent.conversation c ON c.id=a.conversation_id JOIN agent.answer_draft_session d ON d.answer_id=a.id
		WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &answerVersion, &runVersion, &conversationVersion, &draftStatus); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "pending" || runStatus != "RUNNING" || draftStatus != string(agentapplication.DraftStreamCompleted) || answerVersion != 1 || runVersion != 1 || conversationVersion != 2 {
		t.Fatalf("rollback=%s/%s/%s v%d/%d/%d", answerStatus, runStatus, draftStatus, answerVersion, runVersion, conversationVersion)
	}
}

func TestAnswerFinalizerRollsBackAbortedDraftWhenTerminalEventFails(t *testing.T) {
	fixture := newFinalizerFixture(t, true)
	draft := insertFinalizerDraft(t, fixture, agentapplication.DraftStreamActive)
	command := fixture.command(finalizerRefusalProposal(fixture.run.ID))
	command.Draft = &draft
	_, _, err := fixture.finalizer.Finalize(fixture.ctx, command)
	if finalizerErrorCode(err) != "TEST_FINALIZER_EVENT_FAILURE" {
		t.Fatalf("Finalize error=%v", err)
	}
	assertPendingFinalizerDraft(t, fixture, draft.SessionID, agentapplication.DraftStreamActive)
}

func TestAnswerFinalizerPublishesOnlyCompletedOrDegradedBoundDraft(t *testing.T) {
	for _, status := range []agentapplication.DraftStreamStatus{agentapplication.DraftStreamCompleted, agentapplication.DraftStreamDegraded} {
		t.Run(string(status), func(t *testing.T) {
			fixture := newFinalizerFixture(t, false)
			proposal := seedFinalizerCompletedProposal(t, fixture)
			draft := insertFinalizerDraft(t, fixture, status)
			command := fixture.command(proposal)
			command.Draft = &draft

			receipt, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command)
			if err != nil || replayed || receipt.PublicationStatus != "completed" {
				t.Fatalf("Finalize=%#v replay=%t err=%v", receipt, replayed, err)
			}
			var answerStatus, draftStatus string
			var completedAt *time.Time
			if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,d.status,d.completed_at
				FROM agent.answer a JOIN agent.answer_draft_session d ON d.answer_id=a.id
				WHERE a.workspace_id=$1 AND a.id=$2 AND d.id=$3`, string(fixture.lookup.WorkspaceID), string(fixture.lookup.AnswerID), string(draft.SessionID)).Scan(
				&answerStatus, &draftStatus, &completedAt,
			); err != nil {
				t.Fatal(err)
			}
			if answerStatus != "completed" || draftStatus != string(agentapplication.DraftStreamPublished) || completedAt == nil {
				t.Fatalf("published answer=%s draft=%s completed_at=%v", answerStatus, draftStatus, completedAt)
			}
			if _, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command); err != nil || !replayed {
				t.Fatalf("draft replay=%t err=%v", replayed, err)
			}
			conflicting := command
			wrongDraft := draft
			wrongDraft.Generation++
			conflicting.Draft = &wrongDraft
			if _, _, err := fixture.finalizer.Finalize(fixture.ctx, conflicting); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
				t.Fatalf("conflicting draft replay error=%v", err)
			}
			if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM agent.answer_draft_session WHERE id=$1`, string(draft.SessionID)); err != nil {
				t.Fatal(err)
			}
			if _, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command); err != nil || !replayed {
				t.Fatalf("draft TTL cleanup replay=%t err=%v", replayed, err)
			}
			insertFinalizerDraftWithID(t, fixture, agentapplication.DraftStreamAborted, conversationPostgresID(809))
			if _, _, err := fixture.finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
				t.Fatalf("conflicting retained projection replay error=%v", err)
			}
		})
	}
}

func TestAnswerFinalizerAtomicallyAbortsDraftForRefusalAndClarification(t *testing.T) {
	tests := []struct {
		name          string
		source        agentapplication.DraftStreamStatus
		proposal      func(foundation.ID) agentapplication.RAGTerminalProposal
		answerStatus  string
		runStatus     string
		terminalEvent string
	}{
		{name: "refusal from active", source: agentapplication.DraftStreamActive, proposal: finalizerRefusalProposal,
			answerStatus: "refused", runStatus: "REFUSED", terminalEvent: "answer.refused"},
		{name: "clarification from degraded", source: agentapplication.DraftStreamDegraded, proposal: finalizerClarificationProposal,
			answerStatus: "clarification_required", runStatus: "SUCCEEDED", terminalEvent: "answer.clarification_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFinalizerFixture(t, false)
			draft := insertFinalizerDraft(t, fixture, test.source)
			command := fixture.command(test.proposal(fixture.run.ID))
			command.Draft = &draft

			receipt, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command)
			if err != nil || replayed || string(receipt.PublicationStatus) != test.answerStatus {
				t.Fatalf("Finalize=%#v replay=%t err=%v", receipt, replayed, err)
			}
			var answerStatus, runStatus, draftStatus string
			var completedAt *time.Time
			var eventCount int64
			if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,d.status,d.completed_at,
				(SELECT count(*) FROM ops.server_event e WHERE e.workspace_id=a.workspace_id AND e.resource_ref='answer:'||a.id::text AND e.event_type=$4)
				FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id
				JOIN agent.answer_draft_session d ON d.answer_id=a.id
				WHERE a.workspace_id=$1 AND a.id=$2 AND d.id=$3`, string(fixture.lookup.WorkspaceID), string(fixture.lookup.AnswerID),
				string(draft.SessionID), test.terminalEvent).Scan(&answerStatus, &runStatus, &draftStatus, &completedAt, &eventCount); err != nil {
				t.Fatal(err)
			}
			if answerStatus != test.answerStatus || runStatus != test.runStatus || draftStatus != string(agentapplication.DraftStreamAborted) || completedAt == nil || eventCount != 1 {
				t.Fatalf("answer=%s run=%s draft=%s completed_at=%v events=%d", answerStatus, runStatus, draftStatus, completedAt, eventCount)
			}
			if _, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command); err != nil || !replayed {
				t.Fatalf("exact replay=%t err=%v", replayed, err)
			}
			if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM agent.answer_draft_session WHERE id=$1`, string(draft.SessionID)); err != nil {
				t.Fatal(err)
			}
			if _, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command); err != nil || !replayed {
				t.Fatalf("draft TTL cleanup replay=%t err=%v", replayed, err)
			}
		})
	}
}

func TestAnswerFinalizerReplayRequiresExpectedDraftTerminalStatus(t *testing.T) {
	tests := []struct {
		name        string
		proposal    func(*testing.T, finalizerFixture) agentapplication.RAGTerminalProposal
		wrongStatus agentapplication.DraftStreamStatus
	}{
		{name: "answer requires published", proposal: func(t *testing.T, fixture finalizerFixture) agentapplication.RAGTerminalProposal {
			return seedFinalizerCompletedProposal(t, fixture)
		}, wrongStatus: agentapplication.DraftStreamAborted},
		{name: "refusal requires aborted", proposal: func(_ *testing.T, fixture finalizerFixture) agentapplication.RAGTerminalProposal {
			return finalizerRefusalProposal(fixture.run.ID)
		}, wrongStatus: agentapplication.DraftStreamPublished},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFinalizerFixture(t, false)
			proposal := test.proposal(t, fixture)
			command := fixture.command(proposal)
			if _, replayed, err := fixture.finalizer.Finalize(fixture.ctx, command); err != nil || replayed {
				t.Fatalf("initial Finalize replay=%t err=%v", replayed, err)
			}
			draft := insertFinalizerDraft(t, fixture, test.wrongStatus)
			command.Draft = &draft
			if _, _, err := fixture.finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
				t.Fatalf("terminal status replay error=%v", err)
			}
		})
	}
}

func TestAnswerFinalizerFailsClosedForDraftBindingOrStateConflict(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	proposal := seedFinalizerCompletedProposal(t, fixture)
	draft := insertFinalizerDraft(t, fixture, agentapplication.DraftStreamCompleted)
	base := fixture.command(proposal)
	base.Draft = &draft

	tests := []struct {
		name   string
		mutate func(*conversationapplication.AnswerDraftTerminalBinding)
	}{
		{name: "session", mutate: func(binding *conversationapplication.AnswerDraftTerminalBinding) {
			binding.SessionID = conversationPostgresID(899)
		}},
		{name: "generation", mutate: func(binding *conversationapplication.AnswerDraftTerminalBinding) { binding.Generation++ }},
		{name: "attempt", mutate: func(binding *conversationapplication.AnswerDraftTerminalBinding) { binding.AttemptNo++ }},
		{name: "owner", mutate: func(binding *conversationapplication.AnswerDraftTerminalBinding) { binding.LeaseOwner = "other-worker" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := base
			binding := draft
			test.mutate(&binding)
			command.Draft = &binding
			if _, _, err := fixture.finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
				t.Fatalf("Finalize error=%v", err)
			}
			assertPendingFinalizerDraft(t, fixture, draft.SessionID, agentapplication.DraftStreamCompleted)
		})
	}
}

func TestAnswerFinalizerRejectsReclaimedAttemptBeforeDraftPublication(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	proposal := seedFinalizerCompletedProposal(t, fixture)
	draft := insertFinalizerDraft(t, fixture, agentapplication.DraftStreamCompleted)
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_attempt
		SET status='lease_lost',failure_class='lease_lost',error_kind=$2,error_code='WORKFLOW_LEASE_LOST',
			error_summary='lease expired',lease_owner=NULL,lease_until=NULL,ended_at=CURRENT_TIMESTAMP,heartbeat_at=CURRENT_TIMESTAMP
		WHERE id=$1`, string(fixture.lookup.NodeAttemptID), string(foundation.ErrorVersionConflict)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_run
		SET attempt=2,lease_owner='replacement-worker',lease_until=CURRENT_TIMESTAMP+INTERVAL '5 minutes',updated_at=CURRENT_TIMESTAMP,version=version+1
		WHERE id=$1`, string(fixture.lookup.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
		VALUES($1,$2,2,1,0,'replacement-delivery','replacement-worker',CURRENT_TIMESTAMP+INTERVAL '5 minutes','running',CURRENT_TIMESTAMP)`,
		string(conversationPostgresID(809)), string(fixture.lookup.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	command := fixture.command(proposal)
	command.Draft = &draft
	if _, _, err := fixture.finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
		t.Fatalf("Finalize error=%v", err)
	}
	assertPendingFinalizerDraft(t, fixture, draft.SessionID, agentapplication.DraftStreamCompleted)
}

func TestAnswerFinalizerDraftLeaseUsesWallClock(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	draft := insertFinalizerDraft(t, fixture, agentapplication.DraftStreamCompleted)
	var leaseUntil time.Time
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT clock_timestamp()+interval '75 milliseconds'`).Scan(&leaseUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_run
		SET lease_until=$2,updated_at=clock_timestamp() WHERE id=$1`, string(fixture.lookup.NodeRunID), leaseUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_attempt
		SET lease_until=$2,heartbeat_at=clock_timestamp() WHERE id=$1`, string(fixture.lookup.NodeAttemptID), leaseUntil); err != nil {
		t.Fatal(err)
	}
	command := fixture.command(agentapplication.RAGTerminalProposal{})
	err := withinConversationTransaction(fixture.ctx, fixture.finalizer.uow, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB, _ foundation.TransactionScope) error {
		lockedUntil, err := gormLockActiveDraftClaim(ctx, tx, command, draft)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`SELECT pg_sleep(0.15)`).Error; err != nil {
			return err
		}
		if err := gormValidateActiveDraftClaimLease(ctx, tx, lockedUntil); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
			t.Fatalf("expired long-transaction finalizer claim error = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnswerFinalizerRejectsIncompatibleDraftSourceStateWithoutPublishingAnswer(t *testing.T) {
	tests := []struct {
		name     string
		status   agentapplication.DraftStreamStatus
		proposal func(*testing.T, finalizerFixture) agentapplication.RAGTerminalProposal
	}{
		{name: "answer from active", status: agentapplication.DraftStreamActive, proposal: func(t *testing.T, fixture finalizerFixture) agentapplication.RAGTerminalProposal {
			return seedFinalizerCompletedProposal(t, fixture)
		}},
		{name: "refusal from published", status: agentapplication.DraftStreamPublished, proposal: func(_ *testing.T, fixture finalizerFixture) agentapplication.RAGTerminalProposal {
			return finalizerRefusalProposal(fixture.run.ID)
		}},
		{name: "clarification from aborted", status: agentapplication.DraftStreamAborted, proposal: func(_ *testing.T, fixture finalizerFixture) agentapplication.RAGTerminalProposal {
			return finalizerClarificationProposal(fixture.run.ID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFinalizerFixture(t, false)
			draft := insertFinalizerDraft(t, fixture, test.status)
			command := fixture.command(test.proposal(t, fixture))
			command.Draft = &draft
			if _, _, err := fixture.finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != ErrorCodeAnswerFinalizeConflict {
				t.Fatalf("Finalize error=%v", err)
			}
			assertPendingFinalizerDraft(t, fixture, draft.SessionID, test.status)
		})
	}
}

func TestAnswerFinalizerPublishesCompletedAnswerWithDeferredRetrievalBinding(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	proposal := seedFinalizerCompletedProposal(t, fixture)
	indexID := proposal.Retrieval.IndexVersionID
	receipt, replayed, err := fixture.finalizer.Finalize(fixture.ctx, fixture.command(proposal))
	if err != nil || replayed || receipt.PublicationStatus != "completed" {
		t.Fatalf("Finalize=%#v replay=%t err=%v", receipt, replayed, err)
	}
	var answerStatus, runStatus, persistedIndex string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,m.retrieval_index_version_id::text FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &persistedIndex); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "completed" || runStatus != "SUCCEEDED" || persistedIndex != string(indexID) {
		t.Fatalf("completed=%s/%s index=%s", answerStatus, runStatus, persistedIndex)
	}
}

func TestAnswerFinalizerPublishesClarification(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	receipt, replayed, err := fixture.finalizer.Finalize(fixture.ctx, fixture.command(finalizerClarificationProposal(fixture.run.ID)))
	if err != nil || replayed || receipt.PublicationStatus != "clarification_required" || receipt.ResultType != "clarification" {
		t.Fatalf("Finalize=%#v replay=%t err=%v", receipt, replayed, err)
	}
	var answerStatus, runStatus, runResult string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,m.final_result_type FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerStatus, &runStatus, &runResult); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "clarification_required" || runStatus != "SUCCEEDED" || runResult != "clarification" {
		t.Fatalf("clarification=%s/%s/%s", answerStatus, runStatus, runResult)
	}
}

func TestAnswerFinalizerFailsClosedForWorkspaceAndAttemptBinding(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	wrongWorkspace := fixture.lookup
	wrongWorkspace.WorkspaceID = conversationPostgresID(899)
	_, foundWorkspace, workspaceErr := fixture.finalizer.Lookup(fixture.ctx, wrongWorkspace)
	wrongAttempt := fixture.lookup
	wrongAttempt.NodeAttemptID = conversationPostgresID(898)
	_, foundAttempt, attemptErr := fixture.finalizer.Lookup(fixture.ctx, wrongAttempt)
	if foundWorkspace || foundAttempt || finalizerErrorCode(workspaceErr) != ErrorCodeAnswerNotFound || finalizerErrorCode(attemptErr) != ErrorCodeAnswerNotFound {
		t.Fatalf("workspace found=%t err=%v attempt found=%t err=%v", foundWorkspace, workspaceErr, foundAttempt, attemptErr)
	}
}

func TestAnswerFinalizerLookupDetectsTerminalModelRunWithPendingAnswer(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	repository, err := agentpostgres.NewGORMRepository(fixture.shared)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := fixture.run.UpdatedAt.Add(20 * time.Second)
	terminal := fixture.run
	terminal.Status = agentdomain.ModelRunSucceeded
	terminal.FinalResultType = agentdomain.ResultTypeClarification
	terminal.Version = 2
	terminal.UpdatedAt = completedAt
	terminal.CompletedAt = &completedAt
	if _, replayed, err := repository.FinalizeModelRun(fixture.ctx, agentapplication.FinalizeModelRunCommand{ExpectedVersion: 1, Run: terminal}); err != nil || replayed {
		t.Fatalf("split setup replay=%t err=%v", replayed, err)
	}
	_, found, err := fixture.finalizer.Lookup(fixture.ctx, fixture.lookup)
	if found || finalizerErrorCode(err) != ErrorCodeAnswerFinalizeCorrupt {
		t.Fatalf("Lookup found=%t err=%v", found, err)
	}
}

func TestAnswerFinalizerRecoversExactReplayAfterCommitResponseLoss(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	proposal := seedFinalizerCompletedProposal(t, fixture)
	draft := insertFinalizerDraft(t, fixture, agentapplication.DraftStreamCompleted)
	agentRepository, err := agentpostgres.NewGORMRepository(fixture.shared)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(fixture.shared)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := NewGORMAnswerFinalizer(fixture.shared, agentRepository, events, foundation.FixedClock{Value: fixture.run.CreatedAt.Add(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	finalizer.uow = &conversationCommitResponseLossDB{UnitOfWork: finalizer.uow, loseNext: true}
	command := fixture.command(proposal)
	command.Draft = &draft
	if _, _, err := finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != conversationapplication.ErrorCodeAnswerFinalizationUnknown {
		t.Fatalf("Finalize commit response loss code=%q err=%v", finalizerErrorCode(err), err)
	}
	receipt, replayed, err := finalizer.Finalize(fixture.ctx, command)
	if err != nil || !replayed || receipt.ResultHash == "" {
		t.Fatalf("replay=%#v replayed=%t err=%v", receipt, replayed, err)
	}
	var draftStatus string
	var answerVersion, runVersion, eventCount int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.version,m.version,d.status,
		(SELECT count(*) FROM ops.server_event e WHERE e.resource_ref='answer:'||a.id::text AND e.event_type='answer.completed')
		FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id
		JOIN agent.answer_draft_session d ON d.answer_id=a.id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerVersion, &runVersion, &draftStatus, &eventCount); err != nil {
		t.Fatal(err)
	}
	if answerVersion != 2 || runVersion != 2 || draftStatus != string(agentapplication.DraftStreamPublished) || eventCount != 1 {
		t.Fatalf("versions=%d/%d draft=%s events=%d", answerVersion, runVersion, draftStatus, eventCount)
	}
}

func TestAnswerFinalizerRecoversAbortedDraftReplayAfterCommitResponseLoss(t *testing.T) {
	fixture := newFinalizerFixture(t, false)
	draft := insertFinalizerDraft(t, fixture, agentapplication.DraftStreamActive)
	agentRepository, err := agentpostgres.NewGORMRepository(fixture.shared)
	if err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(fixture.shared)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := NewGORMAnswerFinalizer(fixture.shared, agentRepository, events, foundation.FixedClock{Value: fixture.run.CreatedAt.Add(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	finalizer.uow = &conversationCommitResponseLossDB{UnitOfWork: finalizer.uow, loseNext: true}
	command := fixture.command(finalizerRefusalProposal(fixture.run.ID))
	command.Draft = &draft
	if _, _, err := finalizer.Finalize(fixture.ctx, command); finalizerErrorCode(err) != conversationapplication.ErrorCodeAnswerFinalizationUnknown {
		t.Fatalf("Finalize commit response loss code=%q err=%v", finalizerErrorCode(err), err)
	}
	receipt, replayed, err := finalizer.Finalize(fixture.ctx, command)
	if err != nil || !replayed || receipt.ResultHash == "" || receipt.PublicationStatus != "refused" {
		t.Fatalf("replay=%#v replayed=%t err=%v", receipt, replayed, err)
	}
	var draftStatus string
	var answerVersion, runVersion, eventCount int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.version,m.version,d.status,
		(SELECT count(*) FROM ops.server_event e WHERE e.resource_ref='answer:'||a.id::text AND e.event_type='answer.refused')
		FROM agent.answer a JOIN agent.model_run m ON m.id=a.model_run_id
		JOIN agent.answer_draft_session d ON d.answer_id=a.id WHERE a.id=$1`, string(fixture.dispatched.Answer.ID)).Scan(&answerVersion, &runVersion, &draftStatus, &eventCount); err != nil {
		t.Fatal(err)
	}
	if answerVersion != 2 || runVersion != 2 || draftStatus != string(agentapplication.DraftStreamAborted) || eventCount != 1 {
		t.Fatalf("versions=%d/%d draft=%s events=%d", answerVersion, runVersion, draftStatus, eventCount)
	}
}

type finalizerFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	shared     *platformpostgres.Pool
	finalizer  *GORMAnswerFinalizer
	lookup     conversationapplication.AnswerPublicationLookup
	dispatched conversationapplication.SubmitQuestionResult
	run        agentdomain.ModelRun
}

func newFinalizerFixture(t *testing.T, failingEvent bool) finalizerFixture {
	t.Helper()
	repository, shared, pool, ctx := newConversationTestRepository(t)
	workspaceID, conversationID := conversationPostgresID(800), conversationPostgresID(801)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := repository.CreateConversation(ctx, conversationCreateRecord(t, workspaceID, conversationID, "Finalizer", "finalizer", createdAt)); err != nil {
		t.Fatal(err)
	}
	dispatched, err := newQuestionDispatcherIntegration(t, shared).SubmitQuestion(ctx, questionDispatchRecord(t, workspaceID, conversationID, "Need evidence?", "finalizer-question"))
	if err != nil {
		t.Fatal(err)
	}
	attemptID := conversationPostgresID(802)
	if _, err := pool.Exec(ctx, `UPDATE workflow.node_run
		SET status='running',attempt=1,lease_owner='finalizer-worker',lease_until=CURRENT_TIMESTAMP+INTERVAL '5 minutes',
			updated_at=CURRENT_TIMESTAMP,version=version+1
		WHERE id=$1`, string(dispatched.NodeRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at)
		VALUES($1,$2,1,1,0,'finalizer-delivery','finalizer-worker',CURRENT_TIMESTAMP+INTERVAL '5 minutes','running',$3)`, string(attemptID), string(dispatched.NodeRunID), createdAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lookup := conversationapplication.AnswerPublicationLookup{WorkspaceID: workspaceID, WorkflowRunID: dispatched.Workflow.RunID, NodeRunID: dispatched.NodeRunID,
		NodeAttemptID: attemptID, ConversationID: conversationID, QuestionID: dispatched.Question.ID, AnswerID: dispatched.Answer.ID}
	run := finalizerTestRun(lookup, createdAt.Add(2*time.Second))
	run.ID = conversationPostgresID(803)
	agentRepository, err := agentpostgres.NewGORMRepository(shared)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := agentdomain.RAGMemorySnapshot{
		ID: conversationPostgresID(805), WorkspaceID: workspaceID, WorkflowRunID: dispatched.Workflow.RunID,
		NodeRunID: dispatched.NodeRunID, NodeAttemptID: attemptID, ClaimantID: conversationPostgresID(806),
		OwnerKind: "USER", OwnerID: conversationPostgresID(807), TaskScopeID: conversationID,
		Status: agentdomain.RAGMemorySnapshotPreparing, CreatedAt: createdAt.Add(time.Second), UpdatedAt: createdAt.Add(time.Second),
	}
	if _, claimed, err := agentRepository.BeginRAGMemorySnapshot(ctx, snapshot); err != nil || !claimed {
		t.Fatalf("BeginRAGMemorySnapshot claimed=%t err=%v", claimed, err)
	}
	run.MemoryContext = agentdomain.RAGMemoryContextRef{
		SnapshotID: snapshot.ID, SchemaVersion: agentdomain.RAGMemoryContextSchemaVersion,
		Digest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", ByteCount: 2,
	}
	if _, err := agentRepository.FinalizeRAGMemorySnapshotAndCreateModelRun(ctx, agentapplication.FinalizeRAGMemorySnapshotCommand{
		SnapshotID: snapshot.ID, ClaimantID: snapshot.ClaimantID, Context: run.MemoryContext, Run: run,
	}); err != nil {
		t.Fatalf("FinalizeRAGMemorySnapshotAndCreateModelRun err=%v", err)
	}
	call := agentdomain.ModelCall{ID: conversationPostgresID(804), ModelRunID: run.ID, CallNo: 1, Phase: agentdomain.ModelCallPlan,
		Model: run.Model, Profile: run.Profile, Prompt: run.Prompt, Schema: agentdomain.SchemaRef{ID: agentdomain.RAGQueryPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		MaxOutputTokens: 64, Status: agentdomain.ModelCallStarted, RequestHash: string(make([]byte, 64)), RequestBytes: 10, Version: 1, StartedAt: createdAt.Add(3 * time.Second)}
	call.RequestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := agentRepository.StartModelCall(ctx, workspaceID, call); err != nil {
		t.Fatal(err)
	}
	completedAt := createdAt.Add(4 * time.Second)
	call.Status = agentdomain.ModelCallSucceeded
	call.ResponseHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	call.ResponseBytes = 10
	call.Usage = agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	call.Version = 2
	call.CompletedAt = &completedAt
	if _, _, err := agentRepository.CompleteModelCall(ctx, agentapplication.CompleteModelCallCommand{WorkspaceID: workspaceID, ExpectedVersion: 1, Call: call}); err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewGORMStore(shared)
	if err != nil {
		t.Fatal(err)
	}
	var appender interface {
		AppendScoped(context.Context, foundation.TransactionScope, eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error)
	} = events
	if failingEvent {
		appender = finalizerFailingAppender{}
	}
	finalizer, err := NewGORMAnswerFinalizer(shared, agentRepository, appender, foundation.FixedClock{Value: createdAt.Add(10 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	return finalizerFixture{ctx: ctx, pool: pool, shared: shared, finalizer: finalizer, lookup: lookup, dispatched: dispatched, run: run}
}

func seedFinalizerCompletedProposal(t *testing.T, fixture finalizerFixture) agentapplication.RAGTerminalProposal {
	t.Helper()
	proposal := finalizerCompletedProposal(fixture.run.ID, fixture.lookup.WorkspaceID)
	indexID := proposal.Retrieval.IndexVersionID
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,
		expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,source_manifest_hash,
		expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version)
		VALUES($1,$2,'default','v1',$3,'{}','finalizer',$4,1,'finalizer-index','building','["vector"]',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,$5,1,'goldmark','v1',$3,'chunk-v1','schema-v1')`,
		string(indexID), string(fixture.lookup.WorkspaceID), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	return proposal
}

func insertFinalizerDraft(t *testing.T, fixture finalizerFixture, status agentapplication.DraftStreamStatus) conversationapplication.AnswerDraftTerminalBinding {
	t.Helper()
	return insertFinalizerDraftWithID(t, fixture, status, conversationPostgresID(808))
}

func insertFinalizerDraftWithID(t *testing.T, fixture finalizerFixture, status agentapplication.DraftStreamStatus, sessionID foundation.ID) conversationapplication.AnswerDraftTerminalBinding {
	t.Helper()
	draft := conversationapplication.AnswerDraftTerminalBinding{
		SessionID:  sessionID,
		Generation: 1,
		AttemptNo:  1,
		LeaseOwner: "finalizer-worker",
	}
	createdAt := fixture.run.CreatedAt.Add(time.Second)
	var completedAt any
	switch status {
	case agentapplication.DraftStreamCompleted, agentapplication.DraftStreamPublished,
		agentapplication.DraftStreamAborted, agentapplication.DraftStreamSuperseded:
		completedAt = createdAt.Add(time.Second)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO agent.answer_draft_session(
		id,workspace_id,answer_id,workflow_run_id,node_run_id,node_attempt_id,attempt_no,lease_owner,generation,status,
		next_sequence,total_bytes,expires_at,created_at,updated_at,completed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,0,$11,$12,$12,$13)`,
		string(draft.SessionID), string(fixture.lookup.WorkspaceID), string(fixture.lookup.AnswerID), string(fixture.lookup.WorkflowRunID),
		string(fixture.lookup.NodeRunID), string(fixture.lookup.NodeAttemptID), draft.AttemptNo, draft.LeaseOwner, draft.Generation,
		string(status), createdAt.Add(30*time.Minute), createdAt, completedAt); err != nil {
		t.Fatal(err)
	}
	return draft
}

func assertPendingFinalizerDraft(t *testing.T, fixture finalizerFixture, sessionID foundation.ID, wantStatus agentapplication.DraftStreamStatus) {
	t.Helper()
	var answerStatus, runStatus, draftStatus string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT a.publication_status,m.status,d.status
		FROM agent.answer a JOIN agent.model_run m ON m.workflow_run_id=a.workflow_run_id
		JOIN agent.answer_draft_session d ON d.answer_id=a.id
		WHERE a.workspace_id=$1 AND a.id=$2 AND d.id=$3`, string(fixture.lookup.WorkspaceID), string(fixture.lookup.AnswerID), string(sessionID)).Scan(
		&answerStatus, &runStatus, &draftStatus,
	); err != nil {
		t.Fatal(err)
	}
	if answerStatus != "pending" || runStatus != "RUNNING" || draftStatus != string(wantStatus) {
		t.Fatalf("answer=%s run=%s draft=%s", answerStatus, runStatus, draftStatus)
	}
}

func (fixture finalizerFixture) command(proposal agentapplication.RAGTerminalProposal) conversationapplication.FinalizeAnswerCommand {
	return conversationapplication.FinalizeAnswerCommand{AnswerPublicationLookup: fixture.lookup, ModelRunID: fixture.run.ID, ExpectedAnswerVersion: 1, ExpectedModelRunVersion: 1, Proposal: proposal}
}

type finalizerFailingAppender struct{}

func (finalizerFailingAppender) AppendScoped(context.Context, foundation.TransactionScope, eventsdomain.AppendRequest) (eventsdomain.ServerEvent, bool, error) {
	return eventsdomain.ServerEvent{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_FINALIZER_EVENT_FAILURE", true, errors.New("injected"))
}

func finalizerErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
