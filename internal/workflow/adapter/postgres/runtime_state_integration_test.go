//go:build integration

package workflowpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRuntimeStateClaimHeartbeatCompleteAndReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a1000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-state',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-state"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStateStartFixture(workspaceID, "state-complete", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second})
	started, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID := fmt.Sprintf("job-%d-attempt-0", started.Job.JobID)
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: deliveryID, RiverJobID: started.Job.JobID, LeaseOwner: "worker-a", LeaseDuration: time.Minute})
	if err != nil || claimed.Disposition != application.ClaimDispositionClaimed {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	heartbeat, err := repository.Heartbeat(ctx, application.HeartbeatCommand{NodeRunID: started.FirstNode.ID, Fence: domain.LeaseFence{Owner: "worker-a", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("heartbeat: %v cause=%v", err, errors.Unwrap(err))
	}
	transition := application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: deliveryID, Fence: domain.LeaseFence{Owner: "worker-a", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: heartbeat.Node.Version}}, Result: domain.AttemptResult{Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1}}
	completed, err := repository.TransitionDelivery(ctx, transition)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Run.Status != domain.RunStatusSucceeded || completed.Node.Status != domain.NodeStatusSucceeded || completed.Attempt.Status != domain.AttemptStatusSucceeded {
		t.Fatalf("completed=%+v", completed)
	}
	replayed, err := repository.TransitionDelivery(ctx, transition)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Attempt.ID != completed.Attempt.ID || replayed.Node.Status != domain.NodeStatusSucceeded || replayed.Run.Status != domain.RunStatusSucceeded {
		t.Fatalf("replayed=%+v completed=%+v", replayed, completed)
	}
}

func TestRuntimeStateRetrySchedulesOneBusinessGeneration(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a2000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-retry',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-retry"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStateStartFixture(workspaceID, "state-retry", domain.RetryPolicy{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Second})
	started, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID := fmt.Sprintf("job-%d-attempt-0", started.Job.JobID)
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: deliveryID, RiverJobID: started.Job.JobID, LeaseOwner: "worker-r", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	transition := application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: deliveryID, Fence: domain.LeaseFence{Owner: "worker-r", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}}, Result: domain.AttemptResult{Failure: &domain.FailureEnvelope{Class: domain.FailureClassRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "DEPENDENCY_BUSY", Summary: "retry"}}}
	result, err := repository.TransitionDelivery(ctx, transition)
	if err != nil {
		t.Fatalf("retry transition: %v cause=%v", err, errors.Unwrap(err))
	}
	if result.Run.Status != domain.RunStatusRetryWait || result.Node.Status != domain.NodeStatusRetryWait || result.Node.RetryNo != 1 || result.Node.DispatchNo != 2 || result.Attempt.Status != domain.AttemptStatusRetryScheduled {
		t.Fatalf("retry result=%+v", result)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2 AND (args->>'dispatch_no')::integer=2`, riveradapter.NodeJobKind, string(started.FirstNode.ID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("scheduled jobs=%d", jobs)
	}
}

func TestRuntimeStatePauseResumeAndCancelAreIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a3000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-control',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-control"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStateStartFixture(workspaceID, "state-control", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second})
	started, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := application.NewRuntimeCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := coordinator.Pause(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: 1, IdempotencyKey: "pause-1"})
	if err != nil {
		t.Fatalf("pause: %v cause=%v", err, errors.Unwrap(err))
	}
	if paused.Status != domain.RunStatusPaused {
		t.Fatalf("paused=%+v", paused)
	}
	replayed, err := coordinator.Pause(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: 1, IdempotencyKey: "pause-1"})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Version != paused.Version || replayed.Status != paused.Status {
		t.Fatalf("replayed=%+v paused=%+v", replayed, paused)
	}
	resumed, err := coordinator.Resume(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: paused.Version, IdempotencyKey: "resume-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != domain.RunStatusRunning {
		t.Fatalf("resumed=%+v", resumed)
	}
	cancelRequest := runtimeStateStartFixture(workspaceID, "state-cancel", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second})
	remapRuntimeStartIDs(&cancelRequest, "b")
	cancelRequest.Definition.Key = "m4a-runtime-cancel"
	cancelRequest.Run.IdempotencyKey = "m4a-runtime-cancel"
	cancelRequest.FirstNode.IdempotencyKey = "node-start:m4a-runtime-cancel"
	cancelRequest.Event.IdempotencyKey = "workflow-start:m4a-runtime-cancel"
	cancelRequest.Event.EventKey = "workflow.run.started:m4a-runtime-cancel"
	cancelled, err := repository.Start(ctx, cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Cancel(ctx, application.RunControlCommand{WorkflowRunID: cancelled.Run.ID, ExpectedVersion: 1, IdempotencyKey: "cancel-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.RunStatusCancelled {
		t.Fatalf("cancelled=%+v", result)
	}
}

func TestRuntimeStateRunningPauseAndCancelConvergeAtDeliveryCheckpoint(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a4000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-checkpoint',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-checkpoint"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := application.NewRuntimeCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}

	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "state-running-pause", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := coordinator.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "pause-delivery", RiverJobID: started.Job.JobID, LeaseOwner: "worker-p", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	pauseRequest, err := coordinator.Pause(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: claimed.Run.Version, IdempotencyKey: "pause-running"})
	if err != nil || pauseRequest.Status != domain.RunStatusRunning {
		t.Fatalf("pause request=%+v err=%v", pauseRequest, err)
	}
	paused, err := coordinator.Complete(ctx, application.CompleteDeliveryCommand{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "pause-delivery", Fence: domain.LeaseFence{Owner: "worker-p", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}}, Output: json.RawMessage(`{"ignored":true}`), OutputSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Run.Status != domain.RunStatusPaused || paused.Node.Status != domain.NodeStatusPaused || paused.Attempt.Status != domain.AttemptStatusCancelled {
		t.Fatalf("paused checkpoint=%+v", paused)
	}
	resumed, err := coordinator.Resume(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: paused.Run.Version, IdempotencyKey: "resume-running"})
	if err != nil || resumed.Status != domain.RunStatusRunning {
		t.Fatalf("resume=%+v err=%v", resumed, err)
	}
	var dispatchNo int
	if err := pool.QueryRow(ctx, `SELECT dispatch_no FROM workflow.node_run WHERE id=$1`, string(started.FirstNode.ID)).Scan(&dispatchNo); err != nil || dispatchNo != 2 {
		t.Fatalf("dispatch_no=%d err=%v", dispatchNo, err)
	}

	cancelRequest := runtimeStateStartFixture(workspaceID, "state-running-cancel", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second})
	remapRuntimeStartIDs(&cancelRequest, "c")
	cancelledStart, err := repository.Start(ctx, cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	cancelClaim, err := coordinator.Claim(ctx, application.ClaimCommand{NodeRunID: cancelledStart.FirstNode.ID, DispatchNo: 1, DeliveryID: "cancel-delivery", RiverJobID: cancelledStart.Job.JobID, LeaseOwner: "worker-c", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(ctx, application.RunControlCommand{WorkflowRunID: cancelledStart.Run.ID, ExpectedVersion: cancelClaim.Run.Version, IdempotencyKey: "cancel-running"}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := coordinator.Complete(ctx, application.CompleteDeliveryCommand{Binding: application.DeliveryBinding{NodeRunID: cancelledStart.FirstNode.ID, DispatchNo: 1, DeliveryID: "cancel-delivery", Fence: domain.LeaseFence{Owner: "worker-c", AttemptNo: cancelClaim.Attempt.AttemptNo, NodeVersion: cancelClaim.Node.Version}}, Output: json.RawMessage(`{"ignored":true}`), OutputSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Run.Status != domain.RunStatusCancelled || cancelled.Node.Status != domain.NodeStatusCancelled || cancelled.Attempt.Status != domain.AttemptStatusCancelled {
		t.Fatalf("cancelled checkpoint=%+v", cancelled)
	}
}

func TestRuntimeStateHumanWaitSubmitCompletesNodeAndReplaysDecision(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a6000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-human',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-human"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "state-human", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "human-delivery", RiverJobID: started.Job.JobID, LeaseOwner: "worker-h", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	human, err := application.NewRuntimeHumanCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	waited, err := human.WaitForHuman(ctx, application.HumanWaitCommand{TaskID: foundation.ID("a6000000-0000-4000-8000-000000000015"), RunID: started.Run.ID, NodeRunID: started.FirstNode.ID, Fence: domain.LeaseFence{Owner: "worker-h", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}, ExpectedInputSchema: json.RawMessage(`{}`), TargetVersion: 1, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if waited.Task.Status != domain.HumanTaskPending || waited.Run.Status != domain.RunStatusWaitingForHuman || waited.Node.Status != domain.NodeStatusWaitingForHuman || waited.Attempt.Status != domain.AttemptStatusWaitingForHuman || waited.Attempt.LeaseOwner != "" {
		t.Fatalf("waited=%+v", waited)
	}
	decision := json.RawMessage(`{"approved":true}`)
	submitted, err := human.SubmitHuman(ctx, application.HumanDecisionCommand{RunID: started.Run.ID, TaskID: waited.Task.ID, TargetVersion: 1, Decision: decision})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Task.Status != domain.HumanTaskSubmitted || submitted.Node.Status != domain.NodeStatusSucceeded || submitted.Run.Status != domain.RunStatusSucceeded {
		t.Fatalf("submitted=%+v", submitted)
	}
	replayed, err := human.SubmitHuman(ctx, application.HumanDecisionCommand{RunID: started.Run.ID, TaskID: waited.Task.ID, TargetVersion: 1, Decision: decision})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Task.ID != submitted.Task.ID || !jsonEqual(replayed.Task.Decision, decision) {
		t.Fatalf("replayed=%+v submitted=%+v", replayed, submitted)
	}
}

func TestRuntimeStateConcurrentClaimAndLeaseReclaimFenceOldOwner(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a7000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-claim',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-claim"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "state-claim", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		claim application.ClaimResult
		err   error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for _, owner := range []string{"worker-a", "worker-b"} {
		owner := owner
		group.Add(1)
		go func() {
			defer group.Done()
			claim, claimErr := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "delivery-" + owner, RiverJobID: started.Job.JobID, LeaseOwner: owner, LeaseDuration: 100 * time.Millisecond})
			results <- result{claim: claim, err: claimErr}
		}()
	}
	group.Wait()
	close(results)
	claimedCount := 0
	var claimed application.ClaimResult
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if item.claim.Disposition == application.ClaimDispositionClaimed {
			claimedCount++
			claimed = item.claim
		}
	}
	if claimedCount != 1 {
		t.Fatalf("claimed count=%d", claimedCount)
	}
	if _, err := repository.Heartbeat(ctx, application.HeartbeatCommand{NodeRunID: started.FirstNode.ID, Fence: domain.LeaseFence{Owner: "wrong-owner", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}, LeaseDuration: time.Minute}); err == nil {
		t.Fatal("old owner heartbeat accepted")
	}
	time.Sleep(180 * time.Millisecond)
	reclaimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "delivery-" + claimed.Attempt.LeaseOwner, RiverJobID: started.Job.JobID, LeaseOwner: "worker-reclaim", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Disposition != application.ClaimDispositionClaimed || reclaimed.Attempt.AttemptNo != claimed.Attempt.AttemptNo+1 || !reclaimed.LeaseReclaimed {
		t.Fatalf("reclaimed=%+v claimed=%+v", reclaimed, claimed)
	}
	var leaseLost int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_attempt WHERE node_run_id=$1 AND status='lease_lost'`, string(started.FirstNode.ID)).Scan(&leaseLost); err != nil {
		t.Fatal(err)
	}
	if leaseLost != 1 {
		t.Fatalf("lease_lost attempts=%d", leaseLost)
	}
}

func TestRuntimeStateHigherRiverAttemptWaitsForActiveLeaseThenReclaims(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a9100000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-transport-retry',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-transport-retry"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "transport-retry", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "job-attempt-1", RiverJobID: started.Job.JobID, RiverJobAttempt: 1, LeaseOwner: "worker-attempt-1", LeaseDuration: 100 * time.Millisecond})
	if err != nil || claimed.Disposition != application.ClaimDispositionClaimed {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	_, err = repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "job-attempt-2", RiverJobID: started.Job.JobID, RiverJobAttempt: 2, LeaseOwner: "worker-attempt-2", LeaseDuration: time.Minute})
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_LEASE_HELD" || !classified.Retryable {
		t.Fatalf("active lease retry err=%v", err)
	}
	time.Sleep(180 * time.Millisecond)
	reclaimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "job-attempt-2", RiverJobID: started.Job.JobID, RiverJobAttempt: 2, LeaseOwner: "worker-attempt-2", LeaseDuration: time.Minute})
	if err != nil || reclaimed.Disposition != application.ClaimDispositionClaimed || reclaimed.Attempt.AttemptNo != claimed.Attempt.AttemptNo+1 || !reclaimed.LeaseReclaimed {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
}

func TestRuntimeStateRetryExhaustionFailsWithoutNewJob(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a8000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-exhaust',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-exhaust"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "state-exhaust", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "exhaust-delivery", RiverJobID: started.Job.JobID, LeaseOwner: "worker-e", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.TransitionDelivery(ctx, application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "exhaust-delivery", Fence: domain.LeaseFence{Owner: "worker-e", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}}, Result: domain.AttemptResult{Failure: &domain.FailureEnvelope{Class: domain.FailureClassRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "BUSY", Summary: "busy"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Node.Status != domain.NodeStatusFailed || result.Attempt.Status != domain.AttemptStatusFailed || result.Node.DispatchNo != 1 {
		t.Fatalf("exhausted=%+v", result)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`, riveradapter.NodeJobKind, string(started.FirstNode.ID)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("jobs=%d", jobs)
	}
}

func TestRuntimeStateActivatesJoinSuccessorOnceAfterAllPredecessors(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a5000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-dag',$2,$2,CURRENT_TIMESTAMP,'test',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-dag"); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRuntimeRepository(pool, inserter)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeStateStartFixture(workspaceID, "state-dag", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second})
	request.Definition.Graph, _ = json.Marshal(domain.CanonicalGraph{Nodes: []domain.NodeDefinition{
		{Key: "a", Kind: application.CanonicalJSONHashNodeKind, InputSchemaVersion: 1, OutputSchemaVersion: 1},
		{Key: "b", Kind: application.CanonicalJSONHashNodeKind, InputSchemaVersion: 1, OutputSchemaVersion: 1, Dependencies: []string{"a"}},
		{Key: "join", Kind: application.CanonicalJSONHashNodeKind, InputSchemaVersion: 1, OutputSchemaVersion: 1, Dependencies: []string{"a", "b"}},
	}})
	request.FirstNode.NodeKey = "a"
	request.FirstNode.IdempotencyKey = "node-start-state-dag-a"
	started, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	bID := foundation.ID("a5000000-0000-4000-8000-000000000014")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var databaseTimestamp time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseTimestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,idempotency_key,input_schema_version,output_schema_version,dispatch_no,retry_no) VALUES($1,$2,'b',$3,'pending',0,'{}',1,$4,$4,'node-start-state-dag-b',1,1,1,0)`, string(bID), string(started.Run.ID), application.CanonicalJSONHashNodeKind, databaseTimestamp); err != nil {
		t.Fatal(err)
	}
	bJob, err := inserter.InsertTx(ctx, tx, riveradapter.NodeJobArgs{SchemaVersion: riveradapter.NodeJobSchemaVersion, NodeRunID: bID, DispatchNo: 1}, riveradapter.InsertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claimA, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "dag-a", RiverJobID: started.Job.JobID, LeaseOwner: "worker-a", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionDelivery(ctx, application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "dag-a", Fence: domain.LeaseFence{Owner: "worker-a", AttemptNo: claimA.Attempt.AttemptNo, NodeVersion: claimA.Node.Version}}, Result: domain.AttemptResult{Output: json.RawMessage(`{"a":true}`), OutputSchemaVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	claimB, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: bID, DispatchNo: 1, DeliveryID: "dag-b", RiverJobID: bJob.JobID, LeaseOwner: "worker-b", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionDelivery(ctx, application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: bID, DispatchNo: 1, DeliveryID: "dag-b", Fence: domain.LeaseFence{Owner: "worker-b", AttemptNo: claimB.Attempt.AttemptNo, NodeVersion: claimB.Node.Version}}, Result: domain.AttemptResult{Output: json.RawMessage(`{"b":true}`), OutputSchemaVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	var joinID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM workflow.node_run WHERE run_id=$1 AND node_key='join'`, string(started.Run.ID)).Scan(&joinID); err != nil {
		t.Fatal(err)
	}
	var joinJobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_job WHERE kind=$1 AND args->>'node_run_id'=$2`, riveradapter.NodeJobKind, joinID).Scan(&joinJobs); err != nil || joinJobs != 1 {
		t.Fatalf("join jobs=%d err=%v", joinJobs, err)
	}
}

func runtimeStateStartFixture(workspaceID foundation.ID, key string, retry domain.RetryPolicy) application.RuntimeStartRequest {
	definitionID := foundation.ID("a4000000-0000-4000-8000-000000000011")
	runID := foundation.ID("a4000000-0000-4000-8000-000000000012")
	nodeID := foundation.ID("a4000000-0000-4000-8000-000000000013")
	eventID := foundation.ID("a4000000-0000-4000-8000-000000000014")
	graph, _ := json.Marshal(domain.CanonicalGraph{Nodes: []domain.NodeDefinition{{Key: "hash", Kind: application.CanonicalJSONHashNodeKind, InputSchemaVersion: 1, OutputSchemaVersion: 1, RetryPolicy: retry}}})
	runCopy := runID
	return application.RuntimeStartRequest{Definition: domain.Definition{ID: definitionID, WorkspaceID: workspaceID, Key: key, Version: 1, Graph: graph, CreatedAt: time.Now().UTC()}, DefinitionGraphHash: "1111111111111111111111111111111111111111111111111111111111111111", DefinitionInputSchemaVersion: 1, Run: domain.Run{ID: runID, WorkspaceID: workspaceID, DefinitionID: definitionID, Status: domain.RunStatusPending, Input: json.RawMessage(`{"value":1}`), IdempotencyKey: key, RequestHash: "2222222222222222222222222222222222222222222222222222222222222222", Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, FirstNode: domain.NodeRun{ID: nodeID, RunID: runID, NodeKey: "hash", NodeType: application.CanonicalJSONHashNodeKind, Status: domain.NodeStatusPending, Input: json.RawMessage(`{"value":1}`), IdempotencyKey: "node-start-" + key, InputSchemaVersion: 1, OutputSchemaVersion: 1, DispatchNo: 1, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, Event: domain.OutboxEvent{ID: eventID, WorkspaceID: workspaceID, RunID: &runCopy, Type: "workflow.run.started", IdempotencyKey: "workflow-start:" + key, EventKey: "workflow.run.started:" + key, SchemaVersion: 1, EventVersion: 1, Payload: json.RawMessage(`{}`), OccurredAt: time.Now().UTC()}, RequestHash: "2222222222222222222222222222222222222222222222222222222222222222"}
}
