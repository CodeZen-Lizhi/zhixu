//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	ccpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	ccdomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingspostgres "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/adapter/postgres"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	reindexriver "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDispatcherFirstDispatchReplaysExactPendingAndResponseLoss(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	fixture := seedDispatcherWriteback(t, ctx, database, "", "response-loss")
	pendingID := dispatcherID(t)
	if _, err := database.DB().Exec(ctx, `INSERT INTO retrieval.reindex_delivery(
		id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'pending',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(pendingID), ReindexConsumerName,
		string(fixture.EventID), string(fixture.WorkspaceID), string(fixture.ExecutionID)); err != nil {
		t.Fatal(err)
	}
	inserter := &dispatcherInserterFake{}
	unitOfWork, err := database.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	loss := &completionResponseLossDB{UnitOfWork: unitOfWork, lose: true}
	dispatcher := newGORMTestDispatcher(t, database, inserter, loss)
	if err := dispatcher.DispatchBatch(ctx, 1); dispatcherErrorCode(err) != "REINDEX_DISPATCH_COMMIT_FAILED" {
		t.Fatalf("response-loss error=%v", err)
	}
	assertDispatchedOutbox(t, ctx, database.DB(), fixture.EventID, pendingID, 1)
	if commands := inserter.Commands(); len(commands) != 1 || commands[0].SchemaVersion != reindexriver.JobSchemaVersion || commands[0].DeliveryID != pendingID || commands[0].DispatchNo != 1 {
		t.Fatalf("commands=%#v", commands)
	}
	var eventKey *string
	var schemaVersion *int
	var eventVersion *int64
	if err := database.DB().QueryRow(ctx, `SELECT event_key,schema_version,event_version FROM workflow.outbox_event WHERE id=$1`, string(fixture.EventID)).Scan(&eventKey, &schemaVersion, &eventVersion); err != nil {
		t.Fatal(err)
	}
	if eventKey != nil || schemaVersion != nil || eventVersion != nil {
		t.Fatalf("legacy runtime tuple was changed: %v %v %v", eventKey, schemaVersion, eventVersion)
	}

	replay := newGORMTestDispatcher(t, database, inserter)
	if err := replay.DispatchBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(inserter.Commands()) != 1 {
		t.Fatalf("response-loss replay inserted another job: %#v", inserter.Commands())
	}
}

func TestDispatcherInsertFailureRollsBackDeliveryAndPublishedAt(t *testing.T) {
	for _, implementation := range []string{"gorm"} {
		t.Run(implementation, func(t *testing.T) {
			_, database, ctx := newRetrievalTestRepository(t)
			fixture := seedDispatcherWriteback(t, ctx, database, "", "insert-rollback")
			cause := errors.New("river unavailable")
			inserter := &dispatcherInserterFake{err: foundation.NewError(foundation.ErrorRetryableFailure, "WORKFLOW_RIVER_JOB_INSERT_FAILED", true, cause)}
			dispatcher := newGORMTestDispatcher(t, database, inserter)
			if err := dispatcher.DispatchBatch(ctx, 1); !errors.Is(err, cause) {
				t.Fatalf("insert failure=%v", err)
			}
			assertOutboxPendingWithoutDelivery(t, ctx, database.DB(), fixture.EventID)
			{
				var jobs int
				if err := database.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1`, reindexriver.JobKind).Scan(&jobs); err != nil || jobs != 0 {
					t.Fatalf("rolled-back River jobs=%d err=%v", jobs, err)
				}
			}

			inserter.err = nil
			dispatcher = newGORMTestDispatcher(t, database, nil)
			if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := database.DB().QueryRow(ctx, `SELECT count(*) FROM retrieval.reindex_delivery WHERE outbox_event_id=$1`, string(fixture.EventID)).Scan(&count); err != nil || count != 1 {
				t.Fatalf("delivery count=%d err=%v", count, err)
			}
			{
				var jobs int
				if err := database.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job job
		        JOIN retrieval.reindex_delivery delivery ON delivery.id::text=job.args->>'delivery_id'
		        JOIN workflow.outbox_event event ON event.id=delivery.outbox_event_id
		        WHERE job.kind=$1 AND job.queue='retrieval-migration' AND event.id=$2
		        AND event.published_at IS NOT NULL AND delivery.status='dispatched'
		        AND (job.args->>'dispatch_no')::integer=delivery.dispatch_no`, reindexriver.JobKind, string(fixture.EventID)).Scan(&jobs); err != nil || jobs != 1 {
					t.Fatalf("committed River/Delivery/Outbox closure=%d err=%v", jobs, err)
				}
			}
		})
	}
}

func TestConcurrentDispatchersCreateOneDeliveryAndOneJob(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	fixture := seedDispatcherWriteback(t, ctx, database, "", "concurrent")
	inserter := &dispatcherInserterFake{}
	first := newGORMTestDispatcher(t, database, inserter)
	second := newGORMTestDispatcher(t, database, inserter)
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	for _, dispatcher := range []application.BatchDispatcher{first, second} {
		go func(value application.BatchDispatcher) {
			<-start
			errorsOut <- value.DispatchBatch(ctx, 1)
		}(dispatcher)
	}
	close(start)
	for range 2 {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if commands := inserter.Commands(); len(commands) != 1 {
		t.Fatalf("concurrent commands=%#v", commands)
	}
	var count int
	if err := database.DB().QueryRow(ctx, `SELECT count(*) FROM retrieval.reindex_delivery WHERE outbox_event_id=$1`, string(fixture.EventID)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("delivery count=%d err=%v", count, err)
	}
}

func TestDispatcherBlocksLaterOutboxForSameWorkspace(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	first := seedDispatcherWriteback(t, ctx, database, "", "workspace-first")
	second := seedDispatcherWriteback(t, ctx, database, first.WorkspaceID, "workspace-second")
	inserter := &dispatcherInserterFake{}
	dispatcher := newGORMTestDispatcher(t, database, inserter)
	if err := dispatcher.DispatchBatch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if len(inserter.Commands()) != 1 {
		t.Fatalf("same-workspace jobs=%#v", inserter.Commands())
	}
	var firstPublished, secondPublished *time.Time
	if err := database.DB().QueryRow(ctx, `SELECT
		(SELECT published_at FROM workflow.outbox_event WHERE id=$1),
		(SELECT published_at FROM workflow.outbox_event WHERE id=$2)`, string(first.EventID), string(second.EventID)).Scan(&firstPublished, &secondPublished); err != nil {
		t.Fatal(err)
	}
	if firstPublished == nil || secondPublished != nil {
		t.Fatalf("published first=%v second=%v", firstPublished, secondPublished)
	}
}

func TestDispatcherRetryCreatesNextGenerationWithoutChangingPublishedAt(t *testing.T) {
	for _, implementation := range []string{"gorm"} {
		t.Run(implementation, func(t *testing.T) {
			_, database, ctx := newRetrievalTestRepository(t)
			fixture := seedDispatcherWriteback(t, ctx, database, "", "retry")
			inserter := &dispatcherInserterFake{}
			dispatcher := newGORMTestDispatcher(t, database, inserter)
			if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
				t.Fatal(err)
			}
			var deliveryID foundation.ID
			var publishedAt time.Time
			if err := database.DB().QueryRow(ctx, `SELECT delivery.id::text,event.published_at
				FROM retrieval.reindex_delivery delivery JOIN workflow.outbox_event event ON event.id=delivery.outbox_event_id
				WHERE event.id=$1`, string(fixture.EventID)).Scan(&deliveryID, &publishedAt); err != nil {
				t.Fatal(err)
			}
			repository, err := NewGORMDeliveryRepository(database, foundation.NewUUIDGenerator(nil))
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := application.NewDeliveryRuntime(repository)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := runtime.Claim(ctx, application.DeliveryClaimCommand{
				DeliveryID: deliveryID, DispatchNo: 1, RiverJobID: 1,
				RiverAttempt: 1, DeliveryKey: "retry-generation-one", LeaseOwner: "retry-owner", LeaseDuration: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Fail(ctx, application.DeliveryFailureCommand{
				Fence: claim.Fence, RetryDelay: time.Microsecond,
				Failure: retrievaldomain.DeliveryFailure{Class: retrievaldomain.DeliveryFailureRetryable,
					ErrorKind: foundation.ErrorRetryableFailure, Code: "REINDEX_INGESTION_TEMPORARY", Summary: "temporary ingestion failure"},
			}); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) {
					t.Fatalf("fail delivery: %v (SQLSTATE=%s message=%s)", err, pgErr.Code, pgErr.Message)
				}
				t.Fatal(err)
			}
			if _, err := database.DB().Exec(ctx, `UPDATE retrieval.reindex_delivery SET next_attempt_at=CURRENT_TIMESTAMP,
				updated_at=CURRENT_TIMESTAMP,version=version+1 WHERE id=$1 AND status='retry_wait'`, string(deliveryID)); err != nil {
				t.Fatal(err)
			}
			lockTx, err := database.DB().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(fixture.WorkspaceID)); err != nil {
				_ = lockTx.Rollback(ctx)
				t.Fatal(err)
			}
			lockedContext, cancelLocked := context.WithTimeout(ctx, 500*time.Millisecond)
			if err := dispatcher.DispatchBatch(lockedContext, 1); err != nil {
				cancelLocked()
				_ = lockTx.Rollback(ctx)
				t.Fatalf("retry dispatcher must not wait while holding the delivery row: %v", err)
			}
			cancelLocked()
			if len(inserter.Commands()) != 1 {
				_ = lockTx.Rollback(ctx)
				t.Fatalf("retry dispatched while workspace lock was held: %#v", inserter.Commands())
			}
			if err := lockTx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
				t.Fatal(err)
			}
			commands := inserter.Commands()
			if len(commands) != 2 || commands[1].DeliveryID != deliveryID || commands[1].DispatchNo != 2 {
				t.Fatalf("retry commands=%#v", commands)
			}
			var afterPublished time.Time
			var status string
			var dispatchNo int
			if err := database.DB().QueryRow(ctx, `SELECT event.published_at,delivery.status,delivery.dispatch_no
				FROM workflow.outbox_event event JOIN retrieval.reindex_delivery delivery ON delivery.outbox_event_id=event.id
				WHERE event.id=$1`, string(fixture.EventID)).Scan(&afterPublished, &status, &dispatchNo); err != nil {
				t.Fatal(err)
			}
			if !afterPublished.Equal(publishedAt) || status != "dispatched" || dispatchNo != 2 {
				t.Fatalf("retry published=%v want=%v status=%s dispatch=%d", afterPublished, publishedAt, status, dispatchNo)
			}
		})
	}
}

func TestDispatcherUsesFIFOAndSkipsLockedOutbox(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	oldest := seedDispatcherWriteback(t, ctx, database, "", "fifo-oldest")
	time.Sleep(time.Millisecond)
	newer := seedDispatcherWriteback(t, ctx, database, "", "fifo-newer")
	lockTx, err := database.DB().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(ctx, `SELECT id FROM workflow.outbox_event WHERE id=$1 FOR UPDATE`, string(oldest.EventID)); err != nil {
		t.Fatal(err)
	}
	inserter := &dispatcherInserterFake{}
	dispatcher := newGORMTestDispatcher(t, database, inserter)
	if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	assertEventPublished(t, ctx, database.DB(), newer.EventID, true)
	assertEventPublished(t, ctx, database.DB(), oldest.EventID, false)
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	assertEventPublished(t, ctx, database.DB(), oldest.EventID, true)
}

func TestDispatcherPoisonedOutboxIsFatalAndUnchanged(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	fixture := seedDispatcherWriteback(t, ctx, database, "", "poison")
	if _, err := database.DB().Exec(ctx, `ALTER TABLE workflow.outbox_event DISABLE TRIGGER writeback_reindex_guard_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(ctx, `UPDATE workflow.outbox_event SET payload='{}'::jsonb WHERE id=$1`, string(fixture.EventID)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(ctx, `ALTER TABLE workflow.outbox_event ENABLE TRIGGER writeback_reindex_guard_update`); err != nil {
		t.Fatal(err)
	}
	inserter := &dispatcherInserterFake{}
	dispatcher := newGORMTestDispatcher(t, database, inserter)
	err := dispatcher.DispatchBatch(ctx, 1)
	if dispatcherErrorCode(err) != outboxContractInvalid {
		t.Fatalf("poison error=%v", err)
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Retryable || classified.Kind != foundation.ErrorNonRetryableFailure {
		t.Fatalf("poison classification=%#v", classified)
	}
	assertOutboxPendingWithoutDelivery(t, ctx, database.DB(), fixture.EventID)
}

func TestDispatcherUnboundWritebackLifecycleIsFatalAndUnchanged(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	fixture := seedDispatcherWriteback(t, ctx, database, "", "unbound")
	if _, err := database.DB().Exec(ctx, `UPDATE change_control.writeback_execution
		SET status='verify_failed',failure_code='REGRESSION_FAILED',version=version+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND status='verifying'`, string(fixture.ExecutionID)); err != nil {
		t.Fatal(err)
	}
	inserter := &dispatcherInserterFake{}
	dispatcher := newGORMTestDispatcher(t, database, inserter)
	if err := dispatcher.DispatchBatch(ctx, 1); dispatcherErrorCode(err) != outboxContractInvalid {
		t.Fatalf("unbound error=%v", err)
	}
	assertOutboxPendingWithoutDelivery(t, ctx, database.DB(), fixture.EventID)
}

func TestDispatcherRejectsDuplicateJobReceiptAndRollsBack(t *testing.T) {
	_, database, ctx := newRetrievalTestRepository(t)
	fixture := seedDispatcherWriteback(t, ctx, database, "", "duplicate")
	inserter := &dispatcherInserterFake{duplicate: true}
	dispatcher := newGORMTestDispatcher(t, database, inserter)
	if err := dispatcher.DispatchBatch(ctx, 1); dispatcherErrorCode(err) != "REINDEX_DISPATCH_JOB_RECEIPT_CONFLICT" {
		t.Fatalf("duplicate error=%v", err)
	}
	assertOutboxPendingWithoutDelivery(t, ctx, database.DB(), fixture.EventID)
}

type dispatcherWritebackFixture struct {
	WorkspaceID foundation.ID
	ExecutionID foundation.ID
	EventID     foundation.ID
}

func seedDispatcherWriteback(t *testing.T, ctx context.Context, database *platformpostgres.Pool, workspaceID foundation.ID, label string) dispatcherWritebackFixture {
	t.Helper()
	pool := database.DB()
	repository, err := ccpostgres.NewGORMRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	next := func() foundation.ID { return dispatcherID(t) }
	if workspaceID == "" {
		workspaceID = next()
		root := "/tmp/reindex-dispatcher-" + string(workspaceID)
		if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
			VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), "Dispatcher "+label, root, now); err != nil {
			t.Fatal(err)
		}
	}
	definitionID, runID, nodeID := next(), next(), next()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,'{}',$4)`,
		string(definitionID), string(workspaceID), "dispatcher-"+label+"-"+string(definitionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(workspaceID), string(definitionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,lease_owner,lease_until)
		VALUES($1,$2,$3,'tool','running',1,'{}',1,$4,$4,'dispatcher-owner',$5)`, string(nodeID), string(runID), "writeback-"+label, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	proposalID, revisionID, approvalID := next(), next(), next()
	targetPath := "docs/" + label + ".md"
	baseHash := dispatcherHash(label + "-base")
	resultHash := dispatcherHash(label + "-result")
	content := "dispatcher content " + label
	changeHash := ccdomain.ComputeChangeHash(targetPath, baseHash, content)
	requestHash, err := ccdomain.ComputeRequestHashWithRiskLevel(
		workspaceID,
		targetPath,
		baseHash,
		content,
		"verified",
		ccdomain.ProposalRiskLevelLow,
		"low",
		"revert",
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := ccdomain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, TargetPath: targetPath,
		IdempotencyKey: "proposal-" + string(proposalID),
		RiskLevel:      ccdomain.ProposalRiskLevelLow,
		RequestHash:    requestHash,
		Status:         ccdomain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: ccdomain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: targetPath,
			BaseHash: baseHash, Content: content, EvidenceSummary: "verified", Risk: "low", RollbackPlan: "revert", ChangeHash: changeHash, CreatedAt: now},
	}
	if _, err := repository.CreateProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	gitHead := dispatcherGitOID(label + "-head")
	if _, err := repository.Approve(ctx, ccdomain.Approval{ID: approvalID, ProposalID: proposalID, RevisionID: revisionID,
		ChangeHash: changeHash, Decision: ccdomain.DecisionApproved, ApprovedGitHead: &gitHead, DecidedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE change_control.proposal
		SET workflow_run_id=$2,updated_at=$3,version=version+1
		WHERE id=$1 AND status='approved' AND workflow_run_id IS NULL`,
		string(proposalID), string(runID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision_dispatch(
		workspace_id,proposal_id,revision_id,approval_id,workflow_run_id,created_at
	) VALUES($1,$2,$3,$4,$5,$6)`,
		string(workspaceID), string(proposalID), string(revisionID), string(approvalID), string(runID), now); err != nil {
		t.Fatal(err)
	}
	writeAuthorizationID, gitAuthorizationID := next(), next()
	seedDispatcherAuthorization(t, ctx, repository, writeAuthorizationID, workspaceID, runID, nodeID, proposalID, revisionID, approvalID,
		"ApplyApprovedPatch", ccdomain.CapabilityWriteKnowledge, targetPath, changeHash, baseHash, now)
	seedDispatcherAuthorization(t, ctx, repository, gitAuthorizationID, workspaceID, runID, nodeID, proposalID, revisionID, approvalID,
		"CreateGitCommit", ccdomain.CapabilityGitWrite, targetPath, changeHash, baseHash, now)
	executionID := next()
	created, err := repository.CreateWritebackExecution(ctx, ccdomain.CreateWriteback{
		ID: executionID, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID,
		ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID,
		WriteAuthorizationID: writeAuthorizationID, GitAuthorizationID: gitAuthorizationID,
		TargetPath: targetPath, BaseHash: baseHash, ResultHash: resultHash, ApprovedChangeHash: changeHash, ApprovedGitHead: gitHead,
		IdempotencyKey: "writeback-" + string(executionID), TemporaryRef: "tmp/" + label, BackupRef: "backup/" + label,
	})
	if err != nil {
		t.Fatal(err)
	}
	fileApplied, err := repository.CheckpointWritebackExecution(ctx, ccdomain.CheckpointWriteback{
		ExecutionID: executionID, ExpectedVersion: created.Version, Status: ccdomain.WritebackStatusFileApplied, ResultHash: resultHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	commitHash := dispatcherGitOID(label + "-commit")
	parentHash := dispatcherGitOID(label + "-parent")
	diffHash := dispatcherHash(label + "-diff")
	gitCommitted, err := repository.CheckpointWritebackExecution(ctx, ccdomain.CheckpointWriteback{
		ExecutionID: executionID, ExpectedVersion: fileApplied.Version, Status: ccdomain.WritebackStatusGitCommitted,
		ResultHash: resultHash, GitCommit: commitHash, ParentGitCommit: parentHash, DiffHash: diffHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := reindexcontract.EncodeCanonical(reindexcontract.RequestV1{
		SchemaVersion: reindexcontract.SchemaVersionV1, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID,
		ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, WritebackExecutionID: executionID,
		TargetPath: targetPath, ResultHash: resultHash, GitCommit: commitHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventID := next()
	_, err = repository.PublishWriteback(ctx, ccdomain.PublishWriteback{
		ExecutionID: executionID, ExpectedVersion: gitCommitted.Version,
		Commit: ccdomain.ProposalCommit{ID: next(), WorkspaceID: workspaceID, WritebackExecutionID: executionID,
			ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, GitCommit: commitHash,
			ParentGitCommit: parentHash, TargetPath: targetPath, DiffHash: diffHash, ResultHash: resultHash},
		Event: ccdomain.WritebackOutboxEvent{ID: eventID, WorkspaceID: workspaceID, RunID: runID,
			Type: reindexcontract.EventTypeReindexRequested, IdempotencyKey: ccdomain.ExpectedWritebackReindexKey(gitCommitted), Payload: payload},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcherWritebackFixture{WorkspaceID: workspaceID, ExecutionID: executionID, EventID: eventID}
}

func seedDispatcherAuthorization(t *testing.T, ctx context.Context, repository *ccpostgres.GORMRepository, id, workspaceID, runID, nodeID, proposalID, revisionID, approvalID foundation.ID, tool string, capability ccdomain.Capability, targetPath, changeHash, baseHash string, now time.Time) {
	t.Helper()
	_, err := repository.CreateAuthorization(ctx, ccdomain.ToolAuthorization{
		ID: id, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID,
		ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID,
		ToolName: tool, Capability: capability, Scope: ccdomain.ExpectedAuthorizationScope(targetPath),
		ApprovedChangeHash: changeHash, TargetVersion: baseHash, TokenHash: dispatcherHash(string(id) + "-token"),
		IdempotencyKey: "authorization-" + string(id), Status: ccdomain.AuthorizationIssued,
		IssuedAt: now, ExpiresAt: now.Add(2 * time.Minute), Version: 1,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatalf("authorization: %v (%s)", err, pgErr.Message)
		}
		t.Fatal(err)
	}
}

type dispatcherInserterFake struct {
	mu        sync.Mutex
	commands  []reindexriver.Args
	err       error
	duplicate bool
	scoped    application.ScopedJobInserter
}

// 先插入真实 River Job，再注入错误以验证整个外层事务回滚。
func (f *dispatcherInserterFake) InsertScoped(ctx context.Context, scope foundation.TransactionScope, job application.ReindexJob) (application.JobReceipt, error) {
	receipt, err := f.scoped.InsertScoped(ctx, scope, job)
	if err != nil {
		return application.JobReceipt{}, err
	}
	args, err := reindexriver.NewArgs(job.DeliveryID, job.DispatchNo)
	if err != nil {
		return application.JobReceipt{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, args)
	if f.err != nil {
		return application.JobReceipt{}, f.err
	}
	receipt.Duplicate = receipt.Duplicate || f.duplicate
	return receipt, nil
}

func newGORMTestDispatcher(t *testing.T, pool *platformpostgres.Pool, fault *dispatcherInserterFake, transactions ...foundation.UnitOfWork) application.BatchDispatcher {
	t.Helper()
	outbox, err := workflowpostgres.NewGORMReindexOutbox(pool)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := ccpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := modelcrypto.NewSealer(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := auditpostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := modelsettingspostgres.NewGORMRepository(pool,
		modelsettingspostgres.WithGORMSecretSealer(sealer), modelsettingspostgres.WithGORMAuditAppender(audit))
	if err != nil {
		t.Fatal(err)
	}
	options := workflowriver.DefaultOptions()
	options.Queue = "retrieval-migration"
	if fault == nil {
		dispatcher, err := NewGORMDispatcher(pool, foundation.NewUUIDGenerator(nil), options, fence, outbox, binding)
		if err != nil {
			t.Fatal(err)
		}
		return dispatcher
	}
	store, err := newGORMDispatcherStore(pool, foundation.NewUUIDGenerator(nil), outbox, binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) > 0 {
		store.unitOfWork = transactions[0]
	}
	client, err := workflowriver.NewClientWithOptions(pool.DB(), nil, options)
	if err != nil {
		t.Fatal(err)
	}
	fault.scoped, err = reindexriver.NewScopedApplicationInserter(pool, client, fence)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := application.NewScopedDispatcher(store, fault)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func (f *dispatcherInserterFake) Commands() []reindexriver.Args {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]reindexriver.Args, len(f.commands))
	copy(result, f.commands)
	return result
}

func assertDispatchedOutbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID, deliveryID foundation.ID, dispatchNo int) {
	t.Helper()
	var persistedDelivery foundation.ID
	var status string
	var persistedDispatch int
	var publishedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT delivery.id::text,delivery.status,delivery.dispatch_no,event.published_at
		FROM retrieval.reindex_delivery delivery JOIN workflow.outbox_event event ON event.id=delivery.outbox_event_id
		WHERE event.id=$1`, string(eventID)).Scan(&persistedDelivery, &status, &persistedDispatch, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if persistedDelivery != deliveryID || status != "dispatched" || persistedDispatch != dispatchNo || publishedAt == nil {
		t.Fatalf("delivery=%s status=%s dispatch=%d published=%v", persistedDelivery, status, persistedDispatch, publishedAt)
	}
}

func assertOutboxPendingWithoutDelivery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID foundation.ID) {
	t.Helper()
	var publishedAt *time.Time
	var deliveries int
	if err := pool.QueryRow(ctx, `SELECT event.published_at,
		(SELECT count(*) FROM retrieval.reindex_delivery delivery WHERE delivery.outbox_event_id=event.id)
		FROM workflow.outbox_event event WHERE event.id=$1`, string(eventID)).Scan(&publishedAt, &deliveries); err != nil {
		t.Fatal(err)
	}
	if publishedAt != nil || deliveries != 0 {
		t.Fatalf("published=%v deliveries=%d", publishedAt, deliveries)
	}
}

func assertEventPublished(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID foundation.ID, expected bool) {
	t.Helper()
	var publishedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT published_at FROM workflow.outbox_event WHERE id=$1`, string(eventID)).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if (publishedAt != nil) != expected {
		t.Fatalf("event %s published=%v, want %v", eventID, publishedAt, expected)
	}
}

func dispatcherID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func dispatcherHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func dispatcherGitOID(value string) string { return dispatcherHash(value)[:40] }

func dispatcherErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return fmt.Sprintf("%v", err)
}
