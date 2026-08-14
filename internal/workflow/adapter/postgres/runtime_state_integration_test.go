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

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	modelsettingsapplication "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/application"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeStateClaimHeartbeatCompleteAndReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a1000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-state',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-state"); err != nil {
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
	hook := &terminalHookRecorder{inspect: func(ctx context.Context, tx pgx.Tx, event application.WorkflowNodeTerminalEvent) error {
		var runStatus domain.RunStatus
		var nodeStatus domain.NodeStatus
		if err := tx.QueryRow(ctx, `SELECT status FROM workflow.run WHERE id=$1`, string(event.WorkflowRunID)).Scan(&runStatus); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(event.NodeRunID)).Scan(&nodeStatus); err != nil {
			return err
		}
		if runStatus != domain.RunStatusSucceeded || nodeStatus != domain.NodeStatusSucceeded {
			return fmt.Errorf("hook observed run=%s node=%s", runStatus, nodeStatus)
		}
		return nil
	}}
	repository, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: hook})
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
	var persistedGraph domain.CanonicalGraph
	if err := json.Unmarshal(request.Definition.Graph, &persistedGraph); err != nil {
		t.Fatal(err)
	}
	wantDefinitionHash, err := application.ComputeCanonicalGraphHash(persistedGraph)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Definition.ID != request.Definition.ID || claimed.Definition.WorkspaceID != workspaceID || claimed.Definition.Version != request.Definition.Version ||
		claimed.Definition.GraphHash != wantDefinitionHash || claimed.Node.NodeKey != request.FirstNode.NodeKey {
		t.Fatalf("claim identity=%+v node=%+v", claimed.Definition, claimed.Node)
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
	wantEvent := application.WorkflowNodeTerminalEvent{
		WorkspaceID:    workspaceID,
		WorkflowRunID:  started.Run.ID,
		NodeRunID:      started.FirstNode.ID,
		NodeKind:       started.FirstNode.NodeType,
		NodeAttemptID:  claimed.Attempt.ID,
		Outcome:        application.WorkflowTerminalOutcomeSucceeded,
		TerminalOutput: string(completed.Node.Output),
		TerminalAt:     *completed.Attempt.EndedAt,
	}
	if len(hook.events) != 1 || hook.events[0] != wantEvent {
		t.Fatalf("terminal events=%+v want=%+v", hook.events, wantEvent)
	}
	replayed, err := repository.TransitionDelivery(ctx, transition)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Attempt.ID != completed.Attempt.ID || replayed.Node.Status != domain.NodeStatusSucceeded || replayed.Run.Status != domain.RunStatusSucceeded {
		t.Fatalf("replayed=%+v completed=%+v", replayed, completed)
	}
	if len(hook.events) != 1 {
		t.Fatalf("terminal replay calls=%d events=%+v", len(hook.events), hook.events)
	}
}

func TestRuntimeStateClaimPersistsStaticModelRuntimeAsNull(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a1100000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-static-model',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-static-model"); err != nil {
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
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "state-static-model", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "static-model-delivery", RiverJobID: started.Job.JobID, LeaseOwner: "static-worker", LeaseDuration: time.Minute})
	if err != nil || claimed.Disposition != application.ClaimDispositionClaimed {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if claimed.Attempt.ModelSettingsRevision != nil || claimed.Attempt.ModelRuntimeInstanceID != nil {
		t.Fatalf("static attempt model runtime binding=%+v", claimed.Attempt)
	}
	var revisionIsNull, instanceIsNull bool
	if err := pool.QueryRow(ctx, `SELECT model_settings_revision IS NULL,model_runtime_instance_id IS NULL FROM workflow.node_attempt WHERE id=$1`, string(claimed.Attempt.ID)).Scan(&revisionIsNull, &instanceIsNull); err != nil {
		t.Fatal(err)
	}
	if !revisionIsNull || !instanceIsNull {
		t.Fatalf("static persisted binding revision_null=%t instance_null=%t", revisionIsNull, instanceIsNull)
	}
}

func TestRuntimeStateClaimSelectsManagedBindingAndReplaysOldAttemptAcrossCutover(t *testing.T) {
	fixture := newManagedClaimFixture(t, "managed-cutover", true)
	targetRevision := int64(1)
	insertDisabledModelSettingsRevision(t, fixture.ctx, fixture.pool, targetRevision)

	started := fixture.start(t, "managed-old", "a")
	command := application.ClaimCommand{
		NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-old-delivery",
		RiverJobID: started.Job.JobID, ModelRuntimeInstanceID: &fixture.workerInstanceID,
		LeaseOwner: "managed-worker", LeaseDuration: time.Minute,
	}
	claimed, err := fixture.repository.Claim(fixture.ctx, command)
	if err != nil || claimed.Disposition != application.ClaimDispositionClaimed {
		t.Fatalf("old claim=%+v err=%v", claimed, err)
	}
	assertAttemptModelRuntimeBinding(t, claimed.Attempt, 0, fixture.workerInstanceID)

	var persistedRevision int64
	var persistedInstanceID string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT model_settings_revision,model_runtime_instance_id::text FROM workflow.node_attempt WHERE id=$1`, string(claimed.Attempt.ID)).Scan(&persistedRevision, &persistedInstanceID); err != nil {
		t.Fatal(err)
	}
	if persistedRevision != 0 || foundation.ID(persistedInstanceID) != fixture.workerInstanceID {
		t.Fatalf("persisted binding revision=%d instance=%q", persistedRevision, persistedInstanceID)
	}
	otherInstanceID := foundation.ID("a1200000-0000-4000-8000-000000000003")
	_, err = fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_attempt SET model_settings_revision=999 WHERE id=$1`, string(claimed.Attempt.ID))
	assertRuntimePostgresCode(t, err, "55000")
	_, err = fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_attempt SET model_runtime_instance_id=$2::uuid WHERE id=$1`, string(claimed.Attempt.ID), string(otherInstanceID))
	assertRuntimePostgresCode(t, err, "55000")
	_, err = fixture.pool.Exec(fixture.ctx, `INSERT INTO workflow.node_attempt(
id,node_run_id,attempt_no,dispatch_no,retry_no,river_job_id,delivery_id,status,
model_settings_revision,model_runtime_instance_id,started_at,heartbeat_at,ended_at)
VALUES($1,$2,2,1,0,$3,'wrong-active-revision','succeeded',$4,$5::uuid,
clock_timestamp(),clock_timestamp(),clock_timestamp())`, "a1200000-0000-4000-8000-000000000005", string(started.FirstNode.ID), started.Job.JobID, targetRevision, string(fixture.workerInstanceID))
	assertRuntimePostgresCode(t, err, "23514")

	beginHotActivation(t, fixture.ctx, fixture.pool, fixture.rolloutID, targetRevision)
	preparingStart := fixture.start(t, "managed-preparing", "b")
	preparingClaim, err := fixture.repository.Claim(fixture.ctx, application.ClaimCommand{
		NodeRunID: preparingStart.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-preparing-delivery",
		RiverJobID: preparingStart.Job.JobID, ModelRuntimeInstanceID: &fixture.workerInstanceID,
		LeaseOwner: "managed-worker", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAttemptModelRuntimeBinding(t, preparingClaim.Attempt, 0, fixture.workerInstanceID)

	setHotActivationPhase(t, fixture.ctx, fixture.pool, "arming")
	replayed, err := fixture.repository.Claim(fixture.ctx, command)
	if err != nil || replayed.Attempt.ID != claimed.Attempt.ID {
		t.Fatalf("arming exact replay=%+v err=%v", replayed, err)
	}
	assertAttemptModelRuntimeBinding(t, replayed.Attempt, 0, fixture.workerInstanceID)

	blockedStart := fixture.start(t, "managed-blocked", "c")
	blockedCommand := application.ClaimCommand{
		NodeRunID: blockedStart.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-blocked-delivery",
		RiverJobID: blockedStart.Job.JobID, ModelRuntimeInstanceID: &fixture.workerInstanceID,
		LeaseOwner: "managed-worker", LeaseDuration: time.Minute,
	}
	assertRuntimeRetryableCode(t, claimError(fixture.repository.Claim(fixture.ctx, blockedCommand)), "WORKFLOW_MODEL_RUNTIME_CLAIM_BLOCKED")

	setHotActivationPhase(t, fixture.ctx, fixture.pool, "activating")
	replayed, err = fixture.repository.Claim(fixture.ctx, command)
	if err != nil || replayed.Attempt.ID != claimed.Attempt.ID {
		t.Fatalf("activating exact replay=%+v err=%v", replayed, err)
	}
	assertAttemptModelRuntimeBinding(t, replayed.Attempt, 0, fixture.workerInstanceID)
	replayFromOtherOwner := command
	replayFromOtherOwner.ModelRuntimeInstanceID = &otherInstanceID
	if _, err := fixture.repository.Claim(fixture.ctx, replayFromOtherOwner); !hasCode(err, "WORKFLOW_MODEL_RUNTIME_BINDING_MISMATCH") {
		t.Fatalf("other owner exact replay error=%v", err)
	}
	assertRuntimeRetryableCode(t, claimError(fixture.repository.Claim(fixture.ctx, blockedCommand)), "WORKFLOW_MODEL_RUNTIME_CLAIM_BLOCKED")
	var attempts int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM workflow.node_attempt WHERE node_run_id=$1`, string(blockedStart.FirstNode.ID)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("blocked workflow attempts=%d", attempts)
	}
}

func TestRuntimeStateClaimRejectsMissingStaleAndUnavailableWorkerOwner(t *testing.T) {
	fixture := newManagedClaimFixture(t, "managed-owner-health", false)
	commandFor := func(started application.RuntimeStartResult, delivery string) application.ClaimCommand {
		return application.ClaimCommand{
			NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: delivery,
			RiverJobID: started.Job.JobID, ModelRuntimeInstanceID: &fixture.workerInstanceID,
			LeaseOwner: "managed-worker", LeaseDuration: time.Minute,
		}
	}
	missing := fixture.start(t, "managed-missing", "a")
	assertRuntimeRetryableCode(t, claimError(fixture.repository.Claim(fixture.ctx, commandFor(missing, "missing-owner"))), "WORKFLOW_MODEL_RUNTIME_OWNERSHIP_LOST")

	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES('worker',$1::uuid,0,NULL,'active',clock_timestamp()-interval '1 minute',clock_timestamp()-interval '1 minute')`, string(fixture.workerInstanceID)); err != nil {
		t.Fatal(err)
	}
	stale := fixture.start(t, "managed-stale", "b")
	assertRuntimeRetryableCode(t, claimError(fixture.repository.Claim(fixture.ctx, commandFor(stale, "stale-owner"))), "WORKFLOW_MODEL_RUNTIME_OWNERSHIP_LOST")

	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE ops.model_settings_runtime
SET phase='unavailable',heartbeat_at=clock_timestamp() WHERE role='worker'`); err != nil {
		t.Fatal(err)
	}
	unavailable := fixture.start(t, "managed-unavailable", "c")
	assertRuntimeRetryableCode(t, claimError(fixture.repository.Claim(fixture.ctx, commandFor(unavailable, "unavailable-owner"))), "WORKFLOW_MODEL_RUNTIME_OWNERSHIP_LOST")
}

func TestRuntimeStateClaimUsesInjectedManagedRuntimeFreshness(t *testing.T) {
	withinFixture := newManagedClaimFixtureWithFreshness(t, "managed-owner-custom-freshness", false, 45*time.Second)
	if _, err := withinFixture.pool.Exec(withinFixture.ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES('worker',$1::uuid,0,NULL,'active',clock_timestamp()-interval '30 seconds',clock_timestamp()-interval '30 seconds')`, string(withinFixture.workerInstanceID)); err != nil {
		t.Fatal(err)
	}
	withinCustomWindow := withinFixture.start(t, "managed-custom-fresh", "a")
	claimed, err := withinFixture.repository.Claim(withinFixture.ctx, application.ClaimCommand{
		NodeRunID: withinCustomWindow.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-custom-fresh-delivery",
		RiverJobID: withinCustomWindow.Job.JobID, ModelRuntimeInstanceID: &withinFixture.workerInstanceID,
		LeaseOwner: "managed-worker", LeaseDuration: time.Minute,
	})
	if err != nil || claimed.Disposition != application.ClaimDispositionClaimed {
		t.Fatalf("claim within injected freshness window=%+v err=%v", claimed, err)
	}

	beyondFixture := newManagedClaimFixtureWithFreshness(t, "managed-owner-custom-stale", false, 45*time.Second)
	if _, err := beyondFixture.pool.Exec(beyondFixture.ctx, `INSERT INTO ops.model_settings_runtime(
role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES('worker',$1::uuid,0,NULL,'active',clock_timestamp()-interval '46 seconds',clock_timestamp()-interval '46 seconds')`, string(beyondFixture.workerInstanceID)); err != nil {
		t.Fatal(err)
	}
	beyondCustomWindow := beyondFixture.start(t, "managed-custom-stale", "a")
	err = claimError(beyondFixture.repository.Claim(beyondFixture.ctx, application.ClaimCommand{
		NodeRunID: beyondCustomWindow.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-custom-stale-delivery",
		RiverJobID: beyondCustomWindow.Job.JobID, ModelRuntimeInstanceID: &beyondFixture.workerInstanceID,
		LeaseOwner: "managed-worker", LeaseDuration: time.Minute,
	}))
	assertRuntimeRetryableCode(t, err, "WORKFLOW_MODEL_RUNTIME_OWNERSHIP_LOST")
}

func TestRuntimeStateExpiredAttemptHigherDeliveryUsesCurrentBinding(t *testing.T) {
	fixture := newManagedClaimFixture(t, "managed-reclaim", true)
	targetRevision := int64(1)
	insertDisabledModelSettingsRevision(t, fixture.ctx, fixture.pool, targetRevision)
	started := fixture.start(t, "managed-reclaim", "a")
	first, err := fixture.repository.Claim(fixture.ctx, application.ClaimCommand{
		NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-attempt-1",
		RiverJobID: started.Job.JobID, RiverJobAttempt: 1, ModelRuntimeInstanceID: &fixture.workerInstanceID,
		LeaseOwner: "managed-worker-1", LeaseDuration: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAttemptModelRuntimeBinding(t, first.Attempt, 0, fixture.workerInstanceID)

	beginHotActivation(t, fixture.ctx, fixture.pool, fixture.rolloutID, targetRevision)
	setHotActivationPhase(t, fixture.ctx, fixture.pool, "arming")
	setHotActivationPhase(t, fixture.ctx, fixture.pool, "activating")
	replacementInstanceID := foundation.ID("a1200000-0000-4000-8000-000000000003")
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE ops.model_settings_runtime
SET instance_id=$1::uuid,applied_revision=$2,rollout_id=NULL,phase='active',
    applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role='worker'`, string(replacementInstanceID), targetRevision); err != nil {
		t.Fatal(err)
	}
	finalizeHotActivation(t, fixture.ctx, fixture.pool)
	time.Sleep(180 * time.Millisecond)

	reclaimed, err := fixture.repository.Claim(fixture.ctx, application.ClaimCommand{
		NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-attempt-2",
		RiverJobID: started.Job.JobID, RiverJobAttempt: 2, ModelRuntimeInstanceID: &replacementInstanceID,
		LeaseOwner: "managed-worker-2", LeaseDuration: time.Minute,
	})
	if err != nil || !reclaimed.LeaseReclaimed || reclaimed.Attempt.AttemptNo != first.Attempt.AttemptNo+1 {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
	assertAttemptModelRuntimeBinding(t, reclaimed.Attempt, targetRevision, replacementInstanceID)
	var oldStatus domain.AttemptStatus
	var oldRevision, newRevision int64
	var oldInstance, newInstance string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT status,model_settings_revision,model_runtime_instance_id::text
FROM workflow.node_attempt WHERE id=$1`, string(first.Attempt.ID)).Scan(&oldStatus, &oldRevision, &oldInstance); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT model_settings_revision,model_runtime_instance_id::text
FROM workflow.node_attempt WHERE id=$1`, string(reclaimed.Attempt.ID)).Scan(&newRevision, &newInstance); err != nil {
		t.Fatal(err)
	}
	if oldStatus != domain.AttemptStatusLeaseLost || oldRevision != 0 || foundation.ID(oldInstance) != fixture.workerInstanceID || newRevision != targetRevision || foundation.ID(newInstance) != replacementInstanceID {
		t.Fatalf("old=(%s,%d,%s) new=(%d,%s)", oldStatus, oldRevision, oldInstance, newRevision, newInstance)
	}
}

func TestRuntimeStateClaimAndActivationCommitSerializeCompleteBinding(t *testing.T) {
	fixture := newManagedClaimFixture(t, "managed-claim-commit-race", true)
	targetRevision := int64(1)
	insertDisabledModelSettingsRevision(t, fixture.ctx, fixture.pool, targetRevision)
	beginHotActivation(t, fixture.ctx, fixture.pool, fixture.rolloutID, targetRevision)
	prepareWorkerParticipant(t, fixture.ctx, fixture.pool, fixture.rolloutID, fixture.workerInstanceID, targetRevision)
	started := fixture.start(t, "managed-race", "a")
	command := application.ClaimCommand{
		NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "managed-race-delivery",
		RiverJobID: started.Job.JobID, ModelRuntimeInstanceID: &fixture.workerInstanceID,
		LeaseOwner: "managed-worker", LeaseDuration: time.Minute,
	}
	start := make(chan struct{})
	type claimOutcome struct {
		result application.ClaimResult
		err    error
	}
	claimResult := make(chan claimOutcome, 1)
	commitResult := make(chan error, 1)
	go func() {
		<-start
		result, err := fixture.repository.Claim(fixture.ctx, command)
		claimResult <- claimOutcome{result: result, err: err}
	}()
	go func() {
		<-start
		commitResult <- commitHotActivationSameOwner(fixture.ctx, fixture.pool, fixture.rolloutID, fixture.workerInstanceID, targetRevision)
	}()
	close(start)
	claimed := <-claimResult
	if err := <-commitResult; err != nil {
		t.Fatal(err)
	}
	if claimed.err != nil {
		t.Fatal(claimed.err)
	}
	if claimed.result.Attempt.ModelSettingsRevision == nil || claimed.result.Attempt.ModelRuntimeInstanceID == nil || *claimed.result.Attempt.ModelRuntimeInstanceID != fixture.workerInstanceID {
		t.Fatalf("incomplete claim binding=%+v", claimed.result.Attempt)
	}
	selected := *claimed.result.Attempt.ModelSettingsRevision
	if selected != 0 && selected != targetRevision {
		t.Fatalf("claim observed split revision=%d", selected)
	}
}

func assertRuntimePostgresCode(t *testing.T, err error, expected string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != expected {
		t.Fatalf("postgres error=%v code=%q want=%q", err, postgresErrorCode(postgresError), expected)
	}
}

type managedClaimFixture struct {
	ctx              context.Context
	pool             *pgxpool.Pool
	repository       *RuntimeRepository
	workspaceID      foundation.ID
	workerInstanceID foundation.ID
	rolloutID        foundation.ID
}

func newManagedClaimFixture(t *testing.T, name string, withWorker bool) *managedClaimFixture {
	return newManagedClaimFixtureWithFreshness(t, name, withWorker, modelsettingsapplication.DefaultRuntimeFreshWithin)
}

func newManagedClaimFixtureWithFreshness(t *testing.T, name string, withWorker bool, freshness time.Duration) *managedClaimFixture {
	t.Helper()
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	t.Cleanup(cleanup)
	fixture := &managedClaimFixture{
		ctx: ctx, pool: pool,
		workspaceID:      foundation.ID("a1200000-0000-4000-8000-000000000001"),
		workerInstanceID: foundation.ID("a1200000-0000-4000-8000-000000000002"),
		rolloutID:        foundation.ID("a1200000-0000-4000-8000-000000000004"),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
VALUES($1,$2,$3,$3,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(fixture.workspaceID), name, "/tmp/"+name); err != nil {
		t.Fatal(err)
	}
	if withWorker {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_runtime(role,instance_id,applied_revision,rollout_id,phase,applied_at,heartbeat_at)
VALUES('worker',$1::uuid,0,NULL,'active',clock_timestamp(),clock_timestamp())`, string(fixture.workerInstanceID)); err != nil {
			t.Fatal(err)
		}
	}
	client, err := riveradapter.NewClient(pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := riveradapter.NewJobInserter(client)
	if err != nil {
		t.Fatal(err)
	}
	fixture.repository, err = NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{ModelRuntimeFreshWithin: freshness})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *managedClaimFixture) start(t *testing.T, key, prefix string) application.RuntimeStartResult {
	t.Helper()
	request := runtimeStateStartFixture(fixture.workspaceID, key, domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second})
	if prefix != "a" {
		remapRuntimeStartIDs(&request, prefix)
	}
	started, err := fixture.repository.Start(fixture.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func insertDisabledModelSettingsRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, revision int64) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_revisions(
revision,chat_provider,chat_base_url,chat_model,chat_model_version,chat_adapter_version,
chat_timeout_microseconds,chat_max_request_bytes,chat_max_response_bytes,
embedding_provider,embedding_base_url,embedding_model,embedding_dimensions,
embedding_normalization,embedding_distance_metric,embedding_max_batch_size,
embedding_max_input_bytes,embedding_max_batch_input_bytes,embedding_timeout_microseconds,
embedding_max_response_bytes,created_by)
VALUES($1,'disabled','','','', 'v1',30000000,4194304,4194304,
'disabled','','',0,'none','cosine',1,1,1,30000000,67108864,'workflow-test')`, revision)
	if err != nil {
		t.Fatal(err)
	}
}

func beginHotActivation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, rolloutID foundation.ID, targetRevision int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET desired_revision=$1,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND phase='idle'`, targetRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET rollout_id=$1::uuid,target_revision=$2,previous_active_revision=active_revision,
    phase='preparing',lease_expires_at=clock_timestamp()+interval '5 minutes',
    version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND desired_revision=$2 AND phase='idle'`, string(rolloutID), targetRevision); err != nil {
		t.Fatal(err)
	}
}

func setHotActivationPhase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, phase string) {
	t.Helper()
	var statement string
	switch phase {
	case "arming":
		statement = `UPDATE ops.model_settings_state SET phase='arming',lease_expires_at=clock_timestamp()+interval '5 minutes',version=version+1,updated_at=clock_timestamp() WHERE singleton=true AND phase='preparing'`
	case "activating":
		statement = `UPDATE ops.model_settings_state SET active_revision=target_revision,phase='activating',lease_expires_at=clock_timestamp()+interval '5 minutes',version=version+1,updated_at=clock_timestamp() WHERE singleton=true AND phase='arming'`
	default:
		t.Fatalf("unsupported hot activation phase %q", phase)
	}
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatal(err)
	}
}

func finalizeHotActivation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_state
SET rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,phase='idle',
    lease_expires_at=NULL,last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND phase='activating'`); err != nil {
		t.Fatal(err)
	}
}

func prepareWorkerParticipant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, rolloutID, instanceID foundation.ID, targetRevision int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO ops.model_settings_rollout_participant(
rollout_id,role,instance_id,target_revision,phase,heartbeat_at,last_error_retryable,version,prepared_at,activated_at,retired_at)
VALUES($1::uuid,'worker',$2::uuid,$3,'preparing',clock_timestamp(),false,1,NULL,NULL,NULL)`, string(rolloutID), string(instanceID), targetRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='prepared',prepared_at=clock_timestamp(),heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role='worker' AND phase='preparing'`, string(rolloutID)); err != nil {
		t.Fatal(err)
	}
}

func commitHotActivationSameOwner(ctx context.Context, pool *pgxpool.Pool, rolloutID, instanceID foundation.ID, targetRevision int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_state
SET phase='arming',lease_expires_at=clock_timestamp()+interval '5 minutes',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND phase='preparing' AND rollout_id=$1::uuid`, string(rolloutID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='armed',heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role='worker' AND instance_id=$2::uuid AND target_revision=$3 AND phase='prepared'`, string(rolloutID), string(instanceID), targetRevision); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_state
SET active_revision=target_revision,phase='activating',lease_expires_at=clock_timestamp()+interval '5 minutes',version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND phase='arming' AND rollout_id=$1::uuid`, string(rolloutID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_runtime
SET applied_revision=$1,applied_at=clock_timestamp(),heartbeat_at=clock_timestamp()
WHERE role='worker' AND instance_id=$2::uuid`, targetRevision, string(instanceID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_rollout_participant
SET phase='activated',activated_at=clock_timestamp(),heartbeat_at=clock_timestamp(),version=version+1
WHERE rollout_id=$1::uuid AND role='worker' AND instance_id=$2::uuid AND target_revision=$3 AND phase='armed'`, string(rolloutID), string(instanceID), targetRevision); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.model_settings_state
SET rollout_id=NULL,target_revision=NULL,previous_active_revision=NULL,phase='idle',lease_expires_at=NULL,last_error_code=NULL,version=version+1,updated_at=clock_timestamp()
WHERE singleton=true AND phase='activating' AND rollout_id=$1::uuid`, string(rolloutID)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func assertAttemptModelRuntimeBinding(t *testing.T, attempt domain.NodeAttempt, revision int64, instanceID foundation.ID) {
	t.Helper()
	if attempt.ModelSettingsRevision == nil || *attempt.ModelSettingsRevision != revision || attempt.ModelRuntimeInstanceID == nil || *attempt.ModelRuntimeInstanceID != instanceID {
		t.Fatalf("attempt model runtime binding=%+v want revision=%d instance=%s", attempt, revision, instanceID)
	}
}

func claimError(_ application.ClaimResult, err error) error { return err }

func assertRuntimeRetryableCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code || !classified.Retryable {
		t.Fatalf("error=%v code=%q retryable=%v want code=%q retryable=true", err, postgresErrorCode(nil), classifiedRetryable(classified), code)
	}
}

func classifiedRetryable(err *foundation.Error) bool {
	return err != nil && err.Retryable
}

func postgresErrorCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}

func TestRuntimeStateRetrySchedulesOneBusinessGeneration(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a2000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-retry',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-retry"); err != nil {
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
	hook := &terminalHookRecorder{}
	repository, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: hook})
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
	if len(hook.events) != 0 {
		t.Fatalf("retry_scheduled invoked terminal hook: %+v", hook.events)
	}
	coordinator, err := application.NewRuntimeCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	cancelCommand := application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: result.Run.Version, IdempotencyKey: "cancel-retry-wait"}
	cancelled, err := coordinator.Cancel(ctx, cancelCommand)
	if err != nil || cancelled.Status != domain.RunStatusCancelled {
		t.Fatalf("cancel retry_wait=%+v err=%v", cancelled, err)
	}
	var terminalAt time.Time
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT completed_at FROM workflow.node_run WHERE id=$1`, string(started.FirstNode.ID)).Scan(&terminalAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_attempt WHERE node_run_id=$1`, string(started.FirstNode.ID)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("retry_wait cancel attempts=%d", attempts)
	}
	wantEvent := application.WorkflowNodeTerminalEvent{
		WorkspaceID:    workspaceID,
		WorkflowRunID:  started.Run.ID,
		NodeRunID:      started.FirstNode.ID,
		NodeKind:       started.FirstNode.NodeType,
		NodeAttemptID:  result.Attempt.ID,
		Outcome:        application.WorkflowTerminalOutcomeCancelled,
		FailureClass:   domain.FailureClassCancelled,
		FailureCode:    "WORKFLOW_CANCELLED",
		FailureSummary: "WORKFLOW_CANCELLED",
		TerminalAt:     terminalAt,
	}
	if len(hook.events) != 1 || hook.events[0] != wantEvent {
		t.Fatalf("retry_wait cancel events=%+v want=%+v", hook.events, wantEvent)
	}
	if replayed, err := coordinator.Cancel(ctx, cancelCommand); err != nil || replayed.Version != cancelled.Version || len(hook.events) != 1 {
		t.Fatalf("retry_wait cancel replay=%+v events=%d err=%v", replayed, len(hook.events), err)
	}
}

func TestRuntimeStatePauseResumeAndCancelAreIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a3000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-control',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-control"); err != nil {
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
	hook := &terminalHookRecorder{}
	repository, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: hook})
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
	if paused.Status != domain.RunStatusPaused || !paused.PauseRequested || paused.CancelRequested {
		t.Fatalf("paused=%+v", paused)
	}
	replayed, err := coordinator.Pause(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: 1, IdempotencyKey: "pause-1"})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Version != paused.Version || replayed.Status != paused.Status || replayed.PauseRequested != paused.PauseRequested || replayed.CancelRequested != paused.CancelRequested {
		t.Fatalf("replayed=%+v paused=%+v", replayed, paused)
	}
	resumed, err := coordinator.Resume(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: paused.Version, IdempotencyKey: "resume-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != domain.RunStatusRunning || resumed.PauseRequested || resumed.CancelRequested {
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
	hook.inspect = func(ctx context.Context, tx pgx.Tx, event application.WorkflowNodeTerminalEvent) error {
		var runStatus domain.RunStatus
		var nodeStatus domain.NodeStatus
		if err := tx.QueryRow(ctx, `SELECT status FROM workflow.run WHERE id=$1`, string(event.WorkflowRunID)).Scan(&runStatus); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(event.NodeRunID)).Scan(&nodeStatus); err != nil {
			return err
		}
		if runStatus != domain.RunStatusCancelled || nodeStatus != domain.NodeStatusCancelled {
			return fmt.Errorf("direct cancel hook observed run=%s node=%s", runStatus, nodeStatus)
		}
		return nil
	}
	cancelCommand := application.RunControlCommand{WorkflowRunID: cancelled.Run.ID, ExpectedVersion: 1, IdempotencyKey: "cancel-1"}
	result, err := coordinator.Cancel(ctx, cancelCommand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.RunStatusCancelled || result.PauseRequested || !result.CancelRequested {
		t.Fatalf("cancelled=%+v", result)
	}
	var terminalAt time.Time
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT completed_at FROM workflow.node_run WHERE id=$1`, string(cancelled.FirstNode.ID)).Scan(&terminalAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_attempt WHERE node_run_id=$1`, string(cancelled.FirstNode.ID)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("pending cancel created attempts=%d", attempts)
	}
	wantEvent := application.WorkflowNodeTerminalEvent{
		WorkspaceID:    workspaceID,
		WorkflowRunID:  cancelled.Run.ID,
		NodeRunID:      cancelled.FirstNode.ID,
		NodeKind:       cancelled.FirstNode.NodeType,
		Outcome:        application.WorkflowTerminalOutcomeCancelled,
		FailureClass:   domain.FailureClassCancelled,
		FailureCode:    "WORKFLOW_CANCELLED",
		FailureSummary: "WORKFLOW_CANCELLED",
		TerminalAt:     terminalAt,
	}
	if len(hook.events) != 1 || hook.events[0] != wantEvent {
		t.Fatalf("direct cancel events=%+v want=%+v", hook.events, wantEvent)
	}
	replayedCancel, err := coordinator.Cancel(ctx, cancelCommand)
	if err != nil || replayedCancel.Version != result.Version {
		t.Fatalf("direct cancel replay=%+v err=%v", replayedCancel, err)
	}
	if len(hook.events) != 1 {
		t.Fatalf("direct cancel replay terminal calls=%d", len(hook.events))
	}
}

func TestRuntimeStateControlAuthorizesBeforeMutationAndReplay(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a3500000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-control-auth',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-control-auth"); err != nil {
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
	request := runtimeStateStartFixtureWithPermissions(t, workspaceID, "state-control-auth", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second}, capability.GitWrite, capability.WriteKnowledge)
	started, err := repository.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := application.NewRuntimeCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	command := application.RunControlCommand{
		WorkflowRunID: started.Run.ID, ExpectedVersion: 1, IdempotencyKey: "pause-auth",
		CallerCapabilities: []capability.Capability{capability.WriteProposal},
	}
	if _, err := coordinator.Pause(ctx, command); !hasCode(err, "WORKFLOW_CALLER_CAPABILITY_DENIED") {
		t.Fatalf("low-scope pause error=%v", err)
	}
	var status domain.RunStatus
	var version int64
	var controls int
	if err := pool.QueryRow(ctx, `SELECT status,version FROM workflow.run WHERE id=$1`, string(started.Run.ID)).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.control_command WHERE run_id=$1`, string(started.Run.ID)).Scan(&controls); err != nil {
		t.Fatal(err)
	}
	if status != domain.RunStatusPending || version != 1 || controls != 0 {
		t.Fatalf("unauthorized mutation status=%s version=%d controls=%d", status, version, controls)
	}
	command.CallerCapabilities = []capability.Capability{capability.GitWrite, capability.WriteKnowledge, capability.WriteProposal}
	paused, err := coordinator.Pause(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	command.CallerCapabilities = []capability.Capability{capability.WriteProposal}
	if _, err := coordinator.Pause(ctx, command); !hasCode(err, "WORKFLOW_CALLER_CAPABILITY_DENIED") {
		t.Fatalf("low-scope replay error=%v", err)
	}
	command.CallerCapabilities = []capability.Capability{capability.GitWrite, capability.WriteKnowledge, capability.WriteProposal}
	replayed, err := coordinator.Pause(ctx, command)
	if err != nil || replayed.Version != paused.Version || replayed.Status != paused.Status {
		t.Fatalf("authorized replay=%+v err=%v", replayed, err)
	}
}

func TestRuntimeStateRunningPauseAndCancelConvergeAtDeliveryCheckpoint(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a4000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-checkpoint',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-checkpoint"); err != nil {
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
	hook := &terminalHookRecorder{}
	repository, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: hook})
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
	if err != nil || pauseRequest.Status != domain.RunStatusRunning || !pauseRequest.PauseRequested || pauseRequest.CancelRequested {
		t.Fatalf("pause request=%+v err=%v", pauseRequest, err)
	}
	paused, err := coordinator.Complete(ctx, application.CompleteDeliveryCommand{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "pause-delivery", Fence: domain.LeaseFence{Owner: "worker-p", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}}, Output: json.RawMessage(`{"ignored":true}`), OutputSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Run.Status != domain.RunStatusPaused || paused.Run.PauseRequestedAt == nil || paused.Run.CancelRequestedAt != nil || paused.Node.Status != domain.NodeStatusPaused || paused.Attempt.Status != domain.AttemptStatusCancelled {
		t.Fatalf("paused checkpoint=%+v", paused)
	}
	if len(hook.events) != 0 {
		t.Fatalf("pause checkpoint invoked terminal hook: %+v", hook.events)
	}
	resumed, err := coordinator.Resume(ctx, application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: paused.Run.Version, IdempotencyKey: "resume-running"})
	if err != nil || resumed.Status != domain.RunStatusRunning || resumed.PauseRequested || resumed.CancelRequested {
		t.Fatalf("resume=%+v err=%v", resumed, err)
	}
	var dispatchNo int
	if err := pool.QueryRow(ctx, `SELECT dispatch_no FROM workflow.node_run WHERE id=$1`, string(started.FirstNode.ID)).Scan(&dispatchNo); err != nil || dispatchNo != 2 {
		t.Fatalf("dispatch_no=%d err=%v", dispatchNo, err)
	}

	cancelStartRequest := runtimeStateStartFixture(workspaceID, "state-running-cancel", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second})
	remapRuntimeStartIDs(&cancelStartRequest, "c")
	cancelledStart, err := repository.Start(ctx, cancelStartRequest)
	if err != nil {
		t.Fatal(err)
	}
	cancelClaim, err := coordinator.Claim(ctx, application.ClaimCommand{NodeRunID: cancelledStart.FirstNode.ID, DispatchNo: 1, DeliveryID: "cancel-delivery", RiverJobID: cancelledStart.Job.JobID, LeaseOwner: "worker-c", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cancelControl, err := coordinator.Cancel(ctx, application.RunControlCommand{WorkflowRunID: cancelledStart.Run.ID, ExpectedVersion: cancelClaim.Run.Version, IdempotencyKey: "cancel-running"})
	if err != nil {
		t.Fatal(err)
	}
	if !cancelControl.CancelRequested || cancelControl.PauseRequested {
		t.Fatalf("cancel request=%+v", cancelControl)
	}
	cancelCommand := application.CompleteDeliveryCommand{Binding: application.DeliveryBinding{NodeRunID: cancelledStart.FirstNode.ID, DispatchNo: 1, DeliveryID: "cancel-delivery", Fence: domain.LeaseFence{Owner: "worker-c", AttemptNo: cancelClaim.Attempt.AttemptNo, NodeVersion: cancelClaim.Node.Version}}, Output: json.RawMessage(`{"ignored":true}`), OutputSchemaVersion: 1}
	cancelled, err := coordinator.Complete(ctx, cancelCommand)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Run.Status != domain.RunStatusCancelled || cancelled.Node.Status != domain.NodeStatusCancelled || cancelled.Attempt.Status != domain.AttemptStatusCancelled {
		t.Fatalf("cancelled checkpoint=%+v", cancelled)
	}
	wantEvent := application.WorkflowNodeTerminalEvent{
		WorkspaceID:    workspaceID,
		WorkflowRunID:  cancelledStart.Run.ID,
		NodeRunID:      cancelledStart.FirstNode.ID,
		NodeKind:       cancelledStart.FirstNode.NodeType,
		NodeAttemptID:  cancelClaim.Attempt.ID,
		Outcome:        application.WorkflowTerminalOutcomeCancelled,
		FailureClass:   domain.FailureClassCancelled,
		FailureCode:    "WORKFLOW_CANCELLED",
		FailureSummary: "WORKFLOW_CANCELLED",
		TerminalAt:     *cancelled.Attempt.EndedAt,
	}
	if len(hook.events) != 1 || hook.events[0] != wantEvent {
		t.Fatalf("cancel checkpoint events=%+v want=%+v", hook.events, wantEvent)
	}
	replayed, err := coordinator.Complete(ctx, cancelCommand)
	if err != nil || !replayed.Replayed {
		t.Fatalf("cancel checkpoint replay=%+v err=%v", replayed, err)
	}
	if len(hook.events) != 1 {
		t.Fatalf("cancel checkpoint replay terminal calls=%d", len(hook.events))
	}
}

func TestRuntimeStateHumanWaitSubmitCompletesNodeAndReplaysDecision(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a6000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-human',$2,$2,CURRENT_TIMESTAMP,'inactive',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-human"); err != nil {
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
	request := runtimeStateStartFixtureWithPermissions(t, workspaceID, "state-human", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second}, capability.GitWrite, capability.WriteKnowledge)
	started, err := repository.Start(ctx, request)
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
	queryRepository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	pendingTask, found, err := queryRepository.GetPendingHumanTask(ctx, started.Run.ID)
	if err != nil || !found || pendingTask.ID != waited.Task.ID || pendingTask.RunID != started.Run.ID {
		t.Fatalf("pending task=%+v found=%v err=%v", pendingTask, found, err)
	}
	decision := json.RawMessage(`{"approved":true}`)
	lowScope := application.HumanDecisionCommand{
		RunID: started.Run.ID, TaskID: waited.Task.ID, TargetVersion: 1, Decision: decision,
		CallerCapabilities: []capability.Capability{capability.WriteProposal},
	}
	if _, err := human.SubmitHuman(ctx, lowScope); !hasCode(err, "WORKFLOW_CALLER_CAPABILITY_DENIED") {
		t.Fatalf("low-scope human decision error=%v", err)
	}
	var taskStatus domain.HumanTaskStatus
	var nodeStatus domain.NodeStatus
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.human_task WHERE id=$1`, string(waited.Task.ID)).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(waited.Node.ID)).Scan(&nodeStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != domain.HumanTaskPending || nodeStatus != domain.NodeStatusWaitingForHuman {
		t.Fatalf("unauthorized human mutation task=%s node=%s", taskStatus, nodeStatus)
	}
	authorized := lowScope
	authorized.CallerCapabilities = []capability.Capability{capability.GitWrite, capability.WriteKnowledge, capability.WriteProposal}
	submitted, err := human.SubmitHuman(ctx, authorized)
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Task.Status != domain.HumanTaskSubmitted || submitted.Node.Status != domain.NodeStatusSucceeded || submitted.Run.Status != domain.RunStatusSucceeded {
		t.Fatalf("submitted=%+v", submitted)
	}
	if pendingTask, found, err = queryRepository.GetPendingHumanTask(ctx, started.Run.ID); err != nil || found {
		t.Fatalf("resolved pending task=%+v found=%v err=%v", pendingTask, found, err)
	}
	if _, err := human.SubmitHuman(ctx, lowScope); !hasCode(err, "WORKFLOW_CALLER_CAPABILITY_DENIED") {
		t.Fatalf("low-scope human replay error=%v", err)
	}
	replayed, err := human.SubmitHuman(ctx, authorized)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Task.ID != submitted.Task.ID || !jsonEqual(replayed.Task.Decision, decision) {
		t.Fatalf("replayed=%+v submitted=%+v", replayed, submitted)
	}
}

func TestRuntimeStateControlDirectCancelWaitingHumanUsesExistingAttempt(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a6500000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-human-cancel',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-human-cancel"); err != nil {
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
	hook := &terminalHookRecorder{}
	repository, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: hook})
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "state-human-cancel", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "human-cancel-delivery", RiverJobID: started.Job.JobID, LeaseOwner: "worker-human-cancel", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	human, err := application.NewRuntimeHumanCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	waited, err := human.WaitForHuman(ctx, application.HumanWaitCommand{TaskID: foundation.ID("a6500000-0000-4000-8000-000000000015"), RunID: started.Run.ID, NodeRunID: started.FirstNode.ID, Fence: domain.LeaseFence{Owner: "worker-human-cancel", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}, ExpectedInputSchema: json.RawMessage(`{}`), TargetVersion: 1, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := application.NewRuntimeCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	cancelCommand := application.RunControlCommand{WorkflowRunID: started.Run.ID, ExpectedVersion: waited.Run.Version, IdempotencyKey: "cancel-waiting-human"}
	cancelled, err := coordinator.Cancel(ctx, cancelCommand)
	if err != nil || cancelled.Status != domain.RunStatusCancelled {
		t.Fatalf("cancel waiting human=%+v err=%v", cancelled, err)
	}
	var taskStatus domain.HumanTaskStatus
	var terminalAt time.Time
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.human_task WHERE id=$1`, string(waited.Task.ID)).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT completed_at FROM workflow.node_run WHERE id=$1`, string(started.FirstNode.ID)).Scan(&terminalAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.node_attempt WHERE node_run_id=$1`, string(started.FirstNode.ID)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	wantEvent := application.WorkflowNodeTerminalEvent{
		WorkspaceID:    workspaceID,
		WorkflowRunID:  started.Run.ID,
		NodeRunID:      started.FirstNode.ID,
		NodeKind:       started.FirstNode.NodeType,
		NodeAttemptID:  waited.Attempt.ID,
		Outcome:        application.WorkflowTerminalOutcomeCancelled,
		FailureClass:   domain.FailureClassCancelled,
		FailureCode:    "WORKFLOW_CANCELLED",
		FailureSummary: "WORKFLOW_CANCELLED",
		TerminalAt:     terminalAt,
	}
	if taskStatus != domain.HumanTaskCancelled || attempts != 1 || len(hook.events) != 1 || hook.events[0] != wantEvent {
		t.Fatalf("task=%s attempts=%d events=%+v want=%+v", taskStatus, attempts, hook.events, wantEvent)
	}
	if replayed, err := coordinator.Cancel(ctx, cancelCommand); err != nil || replayed.Version != cancelled.Version || len(hook.events) != 1 {
		t.Fatalf("waiting human cancel replay=%+v events=%d err=%v", replayed, len(hook.events), err)
	}
}

func TestRuntimeTerminalHookFailureRollsBackDeliveryAndControl(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a6800000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-terminal-rollback',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-terminal-rollback"); err != nil {
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
	hook := &terminalHookRecorder{}
	repository, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: hook})
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "terminal-hook-delivery", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.Claim(ctx, application.ClaimCommand{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "terminal-hook-delivery", RiverJobID: started.Job.JobID, LeaseOwner: "worker-terminal", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	delivery := application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "terminal-hook-delivery", Fence: domain.LeaseFence{Owner: "worker-terminal", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}}, Result: domain.AttemptResult{Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1}}
	hook.err = foundation.NewError(foundation.ErrorManualRecoveryRequired, "ARTIFACT_GENERATION_RECOVERY_REQUIRED", false, errors.New("injected terminal recovery"))
	if _, err := repository.TransitionDelivery(ctx, delivery); !hasCode(err, "ARTIFACT_GENERATION_RECOVERY_REQUIRED") {
		t.Fatalf("classified terminal hook error=%v", err)
	}
	var runStatus domain.RunStatus
	var nodeStatus domain.NodeStatus
	var attemptStatus domain.AttemptStatus
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.run WHERE id=$1`, string(started.Run.ID)).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(started.FirstNode.ID)).Scan(&nodeStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_attempt WHERE id=$1`, string(claimed.Attempt.ID)).Scan(&attemptStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != domain.RunStatusRunning || nodeStatus != domain.NodeStatusRunning || attemptStatus != domain.AttemptStatusRunning {
		t.Fatalf("delivery rollback run=%s node=%s attempt=%s", runStatus, nodeStatus, attemptStatus)
	}
	hook.err = nil
	if completed, err := repository.TransitionDelivery(ctx, delivery); err != nil || completed.Run.Status != domain.RunStatusSucceeded {
		t.Fatalf("delivery retry=%+v err=%v", completed, err)
	}

	controlRequest := runtimeStateStartFixture(workspaceID, "terminal-hook-control", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Nanosecond, MaxDelay: time.Second})
	remapRuntimeStartIDs(&controlRequest, "d")
	controlRequest.Definition.Key = "terminal-hook-control-definition"
	controlRequest.Run.IdempotencyKey = "terminal-hook-control-run"
	controlRequest.FirstNode.IdempotencyKey = "terminal-hook-control-node"
	controlRequest.Event.IdempotencyKey = "terminal-hook-control-event"
	controlRequest.Event.EventKey = "terminal-hook-control-event"
	controlStarted, err := repository.Start(ctx, controlRequest)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := application.NewRuntimeCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	control := application.RunControlCommand{WorkflowRunID: controlStarted.Run.ID, ExpectedVersion: 1, IdempotencyKey: "terminal-hook-cancel"}
	hook.err = errors.New("injected terminal hook dependency failure")
	_, err = coordinator.Cancel(ctx, control)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_TERMINAL_HOOK_FAILED" || !classified.Retryable {
		t.Fatalf("raw terminal hook error=%v", err)
	}
	var cancelRequestedAt *time.Time
	var controls int
	if err := pool.QueryRow(ctx, `SELECT status,cancel_requested_at FROM workflow.run WHERE id=$1`, string(controlStarted.Run.ID)).Scan(&runStatus, &cancelRequestedAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow.node_run WHERE id=$1`, string(controlStarted.FirstNode.ID)).Scan(&nodeStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.control_command WHERE run_id=$1`, string(controlStarted.Run.ID)).Scan(&controls); err != nil {
		t.Fatal(err)
	}
	if runStatus != domain.RunStatusPending || cancelRequestedAt != nil || nodeStatus != domain.NodeStatusPending || controls != 0 {
		t.Fatalf("control rollback run=%s cancel_at=%v node=%s controls=%d", runStatus, cancelRequestedAt, nodeStatus, controls)
	}
	hook.err = nil
	if cancelled, err := coordinator.Cancel(ctx, control); err != nil || cancelled.Status != domain.RunStatusCancelled {
		t.Fatalf("control retry=%+v err=%v", cancelled, err)
	}
}

func TestRuntimeStateLeaseChecksUseDatabaseTimeAfterLockWait(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a6f00000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-lease-clock',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-lease-clock"); err != nil {
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
	human, err := application.NewRuntimeHumanCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}

	startAndClaim := func(prefix, key, owner, deliveryID string) (application.RuntimeStartResult, application.ClaimResult) {
		t.Helper()
		request := runtimeStateStartFixture(workspaceID, key, domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second})
		remapRuntimeStartIDs(&request, prefix)
		started, startErr := repository.Start(ctx, request)
		if startErr != nil {
			t.Fatal(startErr)
		}
		claimed, claimErr := repository.Claim(ctx, application.ClaimCommand{
			NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: deliveryID,
			RiverJobID: started.Job.JobID, RiverJobAttempt: 1, LeaseOwner: owner, LeaseDuration: time.Minute,
		})
		if claimErr != nil || claimed.Disposition != application.ClaimDispositionClaimed {
			t.Fatalf("claim=%+v err=%v", claimed, claimErr)
		}
		return started, claimed
	}

	runAcrossExpiredLease := func(runID, nodeID, attemptID foundation.ID, operation func() error) error {
		t.Helper()
		blocker, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer func() { _ = blocker.Rollback(ctx) }()
		var locked int
		if lockErr := blocker.QueryRow(ctx, `SELECT 1 FROM workflow.run WHERE id=$1 FOR UPDATE`, string(runID)).Scan(&locked); lockErr != nil {
			t.Fatal(lockErr)
		}
		result := make(chan error, 1)
		go func() { result <- operation() }()

		deadline := time.Now().Add(5 * time.Second)
		for {
			var waiting bool
			if waitErr := pool.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database() AND pid<>pg_backend_pid()
				  AND state='active' AND wait_event_type='Lock'
			)`).Scan(&waiting); waitErr != nil {
				t.Fatal(waitErr)
			}
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("runtime operation did not block on the held workflow lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, updateErr := blocker.Exec(ctx, `UPDATE workflow.node_run SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, string(nodeID)); updateErr != nil {
			t.Fatal(updateErr)
		}
		if _, updateErr := blocker.Exec(ctx, `UPDATE workflow.node_attempt SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, string(attemptID)); updateErr != nil {
			t.Fatal(updateErr)
		}
		time.Sleep(250 * time.Millisecond)
		if commitErr := blocker.Commit(ctx); commitErr != nil {
			t.Fatal(commitErr)
		}
		select {
		case operationErr := <-result:
			return operationErr
		case <-time.After(5 * time.Second):
			t.Fatal("runtime operation did not finish after the workflow lock was released")
			return nil
		}
	}

	t.Run("heartbeat rejects expired owner", func(t *testing.T) {
		started, claimed := startAndClaim("b", "lease-clock-heartbeat", "worker-heartbeat", "lease-clock-heartbeat")
		err := runAcrossExpiredLease(started.Run.ID, claimed.Node.ID, claimed.Attempt.ID, func() error {
			_, heartbeatErr := repository.Heartbeat(ctx, application.HeartbeatCommand{
				NodeRunID: claimed.Node.ID,
				Fence: domain.LeaseFence{
					Owner: claimed.Attempt.LeaseOwner, AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version,
				},
				LeaseDuration: time.Minute,
			})
			return heartbeatErr
		})
		if !hasCode(err, "WORKFLOW_LEASE_EXPIRED") {
			t.Fatalf("heartbeat after lock wait err=%v", err)
		}
	})

	t.Run("delivery transition rejects expired owner", func(t *testing.T) {
		started, claimed := startAndClaim("c", "lease-clock-transition", "worker-transition", "lease-clock-transition")
		err := runAcrossExpiredLease(started.Run.ID, claimed.Node.ID, claimed.Attempt.ID, func() error {
			_, transitionErr := repository.TransitionDelivery(ctx, application.DeliveryTransition{
				Binding: application.DeliveryBinding{
					NodeRunID: claimed.Node.ID, DispatchNo: claimed.Node.DispatchNo, DeliveryID: claimed.Attempt.DeliveryID,
					Fence: domain.LeaseFence{
						Owner: claimed.Attempt.LeaseOwner, AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version,
					},
				},
				Result: domain.AttemptResult{Output: json.RawMessage(`{"ok":true}`), OutputSchemaVersion: 1},
			})
			return transitionErr
		})
		if !hasCode(err, "WORKFLOW_LEASE_LOST") {
			t.Fatalf("transition after lock wait err=%v", err)
		}
	})

	t.Run("human wait rejects expired owner", func(t *testing.T) {
		started, claimed := startAndClaim("e", "lease-clock-human", "worker-human", "lease-clock-human")
		err := runAcrossExpiredLease(started.Run.ID, claimed.Node.ID, claimed.Attempt.ID, func() error {
			_, waitErr := human.WaitForHuman(ctx, application.HumanWaitCommand{
				TaskID:    foundation.ID("e6f00000-0000-4000-8000-000000000015"),
				RunID:     started.Run.ID,
				NodeRunID: claimed.Node.ID,
				Fence: domain.LeaseFence{
					Owner: claimed.Attempt.LeaseOwner, AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version,
				},
				ExpectedInputSchema: json.RawMessage(`{}`),
				TargetVersion:       1,
				ExpiresIn:           time.Hour,
			})
			return waitErr
		})
		if !hasCode(err, "WORKFLOW_LEASE_LOST") {
			t.Fatalf("human wait after lock wait err=%v", err)
		}
		var nodeStatus domain.NodeStatus
		var nodeLeaseOwner string
		var nodeVersion int64
		var attemptStatus domain.AttemptStatus
		var attemptLeaseOwner string
		var attemptEndedAt *time.Time
		var taskCount int
		if err := pool.QueryRow(ctx, `SELECT status,lease_owner,version FROM workflow.node_run WHERE id=$1`, string(claimed.Node.ID)).Scan(&nodeStatus, &nodeLeaseOwner, &nodeVersion); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT status,lease_owner,ended_at FROM workflow.node_attempt WHERE id=$1`, string(claimed.Attempt.ID)).Scan(&attemptStatus, &attemptLeaseOwner, &attemptEndedAt); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.human_task WHERE run_id=$1`, string(started.Run.ID)).Scan(&taskCount); err != nil {
			t.Fatal(err)
		}
		if nodeStatus != domain.NodeStatusRunning || nodeLeaseOwner != claimed.Attempt.LeaseOwner || nodeVersion != claimed.Node.Version ||
			attemptStatus != domain.AttemptStatusRunning || attemptLeaseOwner != claimed.Attempt.LeaseOwner || attemptEndedAt != nil || taskCount != 0 {
			t.Fatalf("human wait mutated expired lease node_status=%s node_owner=%q node_version=%d attempt_status=%s attempt_owner=%q attempt_ended_at=%v tasks=%d",
				nodeStatus, nodeLeaseOwner, nodeVersion, attemptStatus, attemptLeaseOwner, attemptEndedAt, taskCount)
		}
	})

	t.Run("claim reclaims lease expired while blocked", func(t *testing.T) {
		started, claimed := startAndClaim("d", "lease-clock-claim", "worker-old", "lease-clock-old")
		var reclaimed application.ClaimResult
		err := runAcrossExpiredLease(started.Run.ID, claimed.Node.ID, claimed.Attempt.ID, func() error {
			var claimErr error
			reclaimed, claimErr = repository.Claim(ctx, application.ClaimCommand{
				NodeRunID: claimed.Node.ID, DispatchNo: claimed.Node.DispatchNo, DeliveryID: "lease-clock-new",
				RiverJobID: started.Job.JobID, RiverJobAttempt: 2, LeaseOwner: "worker-new", LeaseDuration: time.Minute,
			})
			return claimErr
		})
		if err != nil || reclaimed.Disposition != application.ClaimDispositionClaimed || !reclaimed.LeaseReclaimed ||
			reclaimed.Attempt.AttemptNo != claimed.Attempt.AttemptNo+1 {
			t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
		}
	})
}

func TestRuntimeStateConcurrentClaimAndLeaseReclaimFenceOldOwner(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newRuntimeTestDatabase(t, ctx)
	defer cleanup()
	workspaceID := foundation.ID("a7000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-claim',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-claim"); err != nil {
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
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-transport-retry',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-transport-retry"); err != nil {
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
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-exhaust',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-exhaust"); err != nil {
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
	hook := &terminalHookRecorder{}
	repository, err := NewRuntimeRepositoryWithHooks(pool, inserter, RuntimeRepositoryHooks{Terminal: hook})
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
	if result.Node.Status != domain.NodeStatusFailed || result.Node.FailureClass != domain.FailureClassNonRetryable || result.Node.ErrorCode != "WORKFLOW_RETRY_EXHAUSTED" || result.Attempt.Status != domain.AttemptStatusFailed || result.Attempt.FailureClass != domain.FailureClassRetryable || result.Attempt.ErrorCode != "BUSY" || result.Node.DispatchNo != 1 {
		t.Fatalf("exhausted=%+v", result)
	}
	wantEvent := application.WorkflowNodeTerminalEvent{
		WorkspaceID:    workspaceID,
		WorkflowRunID:  started.Run.ID,
		NodeRunID:      started.FirstNode.ID,
		NodeKind:       started.FirstNode.NodeType,
		NodeAttemptID:  claimed.Attempt.ID,
		Outcome:        application.WorkflowTerminalOutcomeFailed,
		FailureClass:   domain.FailureClassNonRetryable,
		FailureCode:    "WORKFLOW_RETRY_EXHAUSTED",
		FailureSummary: "retry limit exhausted",
		TerminalAt:     *result.Attempt.EndedAt,
	}
	if len(hook.events) != 1 || hook.events[0] != wantEvent {
		t.Fatalf("terminal events=%+v want=%+v", hook.events, wantEvent)
	}
	replayed, err := repository.TransitionDelivery(ctx, application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "exhaust-delivery", Fence: domain.LeaseFence{Owner: "worker-e", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}}, Result: domain.AttemptResult{Failure: &domain.FailureEnvelope{Class: domain.FailureClassRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "BUSY", Summary: "busy"}}})
	if err != nil || !replayed.Replayed {
		t.Fatalf("exhausted replay=%+v err=%v", replayed, err)
	}
	if len(hook.events) != 1 {
		t.Fatalf("exhausted replay terminal calls=%d", len(hook.events))
	}
	conflicting := application.DeliveryTransition{Binding: application.DeliveryBinding{NodeRunID: started.FirstNode.ID, DispatchNo: 1, DeliveryID: "exhaust-delivery", Fence: domain.LeaseFence{Owner: "worker-e", AttemptNo: claimed.Attempt.AttemptNo, NodeVersion: claimed.Node.Version}}, Result: domain.AttemptResult{Failure: &domain.FailureEnvelope{Class: domain.FailureClassRetryable, ErrorKind: foundation.ErrorRetryableFailure, Code: "DIFFERENT_FAILURE", Summary: "different"}}}
	if _, err := repository.TransitionDelivery(ctx, conflicting); !hasCode(err, "WORKFLOW_COMPLETION_CONFLICT") {
		t.Fatalf("different exhausted result replay error=%v", err)
	}
	if len(hook.events) != 1 {
		t.Fatalf("different exhausted result invoked terminal hook: %d", len(hook.events))
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
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-dag',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-dag"); err != nil {
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

func runtimeStateStartFixtureWithPermissions(t *testing.T, workspaceID foundation.ID, key string, retry domain.RetryPolicy, permissions ...capability.Capability) application.RuntimeStartRequest {
	t.Helper()
	request := runtimeStateStartFixture(workspaceID, key, retry)
	var graph domain.CanonicalGraph
	if err := json.Unmarshal(request.Definition.Graph, &graph); err != nil {
		t.Fatal(err)
	}
	graph.Nodes[0].RequiredPermissions = append([]domain.Permission(nil), permissions...)
	encoded, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	request.Definition.Graph = encoded
	return request
}

type terminalHookRecorder struct {
	events  []application.WorkflowNodeTerminalEvent
	inspect func(context.Context, pgx.Tx, application.WorkflowNodeTerminalEvent) error
	err     error
}

func (hook *terminalHookRecorder) OnWorkflowNodeTerminal(ctx context.Context, transaction any, event application.WorkflowNodeTerminalEvent) error {
	tx, ok := transaction.(pgx.Tx)
	if !ok {
		return errors.New("terminal hook transaction is not pgx.Tx")
	}
	hook.events = append(hook.events, event)
	if hook.inspect != nil {
		if err := hook.inspect(ctx, tx, event); err != nil {
			return err
		}
	}
	return hook.err
}
