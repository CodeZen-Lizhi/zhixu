package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryWritebackCreateReplayAndIdentityConflict(t *testing.T) {
	fixture := newWritebackFixture(t)

	created, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != domain.WritebackStatusPrepared || created.Version != 1 || created.ID != fixture.executionID || created.ApprovedGitHead != fixture.gitHead || created.ApprovedChangeHash != fixture.changeHash {
		t.Fatalf("created execution=%#v", created)
	}
	assertProposalState(t, fixture.ctx, fixture.tx, fixture.proposalID, domain.StatusApplying, 3)

	queried, err := fixture.repository.GetWritebackExecution(fixture.ctx, fixture.executionID)
	if err != nil || queried.ID != created.ID || queried.Version != created.Version || queried.Status != created.Status {
		t.Fatalf("queried=%#v err=%v", queried, err)
	}
	replayed, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil || replayed.ID != created.ID || replayed.Version != created.Version || replayed.Status != created.Status {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	assertProposalState(t, fixture.ctx, fixture.tx, fixture.proposalID, domain.StatusApplying, 3)

	conflicting := fixture.create
	conflicting.ResultHash = strings.Repeat("e", 64)
	if _, err := fixture.repository.CreateWritebackExecution(fixture.ctx, conflicting); !hasCode(err, "WRITEBACK_IDENTITY_CONFLICT") {
		t.Fatalf("identity conflict err=%v", err)
	}
}

func TestRepositoryFindWritebackExecutionByKeyIsExactAndWorkspaceScoped(t *testing.T) {
	fixture := newWritebackFixture(t)
	created, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := fixture.repository.FindWritebackExecutionByKey(fixture.ctx, fixture.workspaceID, fixture.create.IdempotencyKey)
	if err != nil || !ok || found.ID != created.ID || found.IdempotencyKey != fixture.create.IdempotencyKey || found.WriteAuthorizationID != fixture.writeAuthorizationID || found.GitAuthorizationID != fixture.gitAuthorizationID {
		t.Fatalf("found=%#v ok=%v err=%v", found, ok, err)
	}
	if _, ok, err := fixture.repository.FindWritebackExecutionByKey(fixture.ctx, fixture.workspaceID, "missing-writeback"); err != nil || ok {
		t.Fatalf("missing ok=%v err=%v", ok, err)
	}
	otherWorkspace := fixture.nextID(t)
	if _, ok, err := fixture.repository.FindWritebackExecutionByKey(fixture.ctx, otherWorkspace, fixture.create.IdempotencyKey); err != nil || ok {
		t.Fatalf("cross-workspace ok=%v err=%v", ok, err)
	}
	if _, _, err := fixture.repository.FindWritebackExecutionByKey(fixture.ctx, "not-a-uuid", fixture.create.IdempotencyKey); err == nil {
		t.Fatal("invalid workspace id accepted")
	}
	if _, _, err := fixture.repository.FindWritebackExecutionByKey(fixture.ctx, fixture.workspaceID, " "); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, _, err := fixture.repository.FindWritebackExecutionByKey(fixture.ctx, fixture.workspaceID, " "+fixture.create.IdempotencyKey); err == nil {
		t.Fatal("non-canonical key accepted")
	}
}

func TestRepositorySafeToCancelWorkflowNodeRequiresSafeCheckpoint(t *testing.T) {
	fixture := newWritebackFixture(t)
	if safe, err := fixture.repository.SafeToCancelWorkflowNode(fixture.ctx, fixture.tx, fixture.nodeID); err != nil || !safe {
		t.Fatalf("node before atomic begin safe=%t err=%v", safe, err)
	}
	created, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil {
		t.Fatal(err)
	}
	if safe, err := fixture.repository.SafeToCancelWorkflowNode(fixture.ctx, fixture.tx, fixture.nodeID); err != nil || safe {
		t.Fatalf("prepared execution safe=%t err=%v", safe, err)
	}
	failed, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: created.Version, Status: domain.WritebackStatusApplyFailed,
		FailureCode: "WRITEBACK_CANCELLED_BEFORE_SIDE_EFFECT", At: fixture.now,
	})
	if err != nil || failed.Status != domain.WritebackStatusApplyFailed {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	if safe, err := fixture.repository.SafeToCancelWorkflowNode(fixture.ctx, fixture.tx, fixture.nodeID); err != nil || !safe {
		t.Fatalf("terminal execution safe=%t err=%v", safe, err)
	}
	if _, err := fixture.repository.SafeToCancelWorkflowNode(fixture.ctx, nil, fixture.nodeID); !hasCode(err, "WRITEBACK_CANCELLATION_TRANSACTION_INVALID") {
		t.Fatalf("invalid transaction err=%v", err)
	}
}

func TestRepositoryBeginWritebackAtomicDoubleConsumeAndReplay(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.bindProposalToRun(t)
	begin := domain.BeginWriteback{
		WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID,
		LeaseOwner: "test-owner", IdempotencyKey: "begin-" + string(fixture.executionID),
		WriteAuthorization: domain.AuthorizationConsume{
			Credential: string(fixture.writeAuthorizationID) + "write-auth", IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID),
			WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID,
			ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash,
		},
		GitAuthorization: domain.AuthorizationConsume{
			Credential: string(fixture.gitAuthorizationID) + "git-auth", IdempotencyKey: "git-auth-" + string(fixture.gitAuthorizationID),
			WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID,
			ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash,
		},
	}
	created, err := fixture.repository.BeginWriteback(fixture.ctx, begin)
	if err != nil {
		t.Fatal(err)
	}
	wantResultHash := domain.ComputeWritebackResultHash([]byte("writeback result"))
	if created.Status != domain.WritebackStatusPrepared || created.ID == "" || created.ResultHash != wantResultHash {
		t.Fatalf("created=%#v", created)
	}
	assertProposalState(t, fixture.ctx, fixture.tx, fixture.proposalID, domain.StatusApplying, 4)
	var writeStatus, gitStatus string
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT (SELECT status FROM change_control.tool_authorization WHERE id=$1),(SELECT status FROM change_control.tool_authorization WHERE id=$2)`, string(fixture.writeAuthorizationID), string(fixture.gitAuthorizationID)).Scan(&writeStatus, &gitStatus); err != nil {
		t.Fatal(err)
	}
	if writeStatus != string(domain.AuthorizationConsumed) || gitStatus != string(domain.AuthorizationConsumed) {
		t.Fatalf("statuses write=%s git=%s", writeStatus, gitStatus)
	}
	begin.ExecutionID = fixture.nextID(t)
	replayed, err := fixture.repository.BeginWriteback(fixture.ctx, begin)
	if err != nil || replayed.ID != created.ID || replayed.Version != created.Version {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	conflict := begin
	conflict.IdempotencyKey = "begin-conflict"
	conflict.WriteAuthorization.Credential = "wrong-token"
	if _, err := fixture.repository.BeginWriteback(fixture.ctx, conflict); !hasCode(err, "WRITEBACK_AUTHORIZATION_BINDING_CONFLICT") {
		t.Fatalf("credential conflict err=%v", err)
	}
}

func TestWorkflowCancellationWaitsForWritebackRecoveryCheckpoint(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.bindProposalToRun(t)
	deliveryID := "cancel-safety-delivery"
	if _, err := fixture.tx.Exec(fixture.ctx, `
		INSERT INTO workflow.node_attempt(
			id,node_run_id,attempt_no,dispatch_no,retry_no,river_job_id,river_job_attempt,
			delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at
		) VALUES($1,$2,1,1,0,1,1,$3,'test-owner',$4,'running',$5,$5)`,
		string(fixture.nextID(t)), string(fixture.nodeID), deliveryID, fixture.now.Add(time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	begin := domain.BeginWriteback{
		ExecutionID: fixture.executionID, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID,
		NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, LeaseOwner: "test-owner",
		IdempotencyKey: "cancel-safety-" + string(fixture.executionID),
		WriteAuthorization: domain.AuthorizationConsume{
			Credential: string(fixture.writeAuthorizationID) + "write-auth", IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID),
			WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID,
			ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID,
			ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge,
			Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash,
		},
		GitAuthorization: domain.AuthorizationConsume{
			Credential: string(fixture.gitAuthorizationID) + "git-auth", IdempotencyKey: "git-auth-" + string(fixture.gitAuthorizationID),
			WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID,
			ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID,
			ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite,
			Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash,
		},
	}
	created, err := fixture.repository.BeginWriteback(fixture.ctx, begin)
	if err != nil || created.Status != domain.WritebackStatusPrepared {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	if err := fixture.tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	changeRepository, err := NewRepository(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepository, err := workflowpostgres.NewRuntimeRepository(fixture.pool, cancellationSafetyJobInserter{}, changeRepository)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := workflowapplication.NewRuntimeCoordinator(runtimeRepository)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := coordinator.Cancel(fixture.ctx, workflowapplication.RunControlCommand{
		WorkflowRunID: fixture.runID, ExpectedVersion: 1, IdempotencyKey: "cancel-safe-writeback",
	})
	if err != nil || cancelled.Status != workflowdomain.RunStatusRunning {
		t.Fatalf("cancel request=%#v err=%v", cancelled, err)
	}
	heartbeat, err := coordinator.Heartbeat(fixture.ctx, workflowapplication.HeartbeatCommand{
		NodeRunID:     fixture.nodeID,
		Fence:         workflowdomain.LeaseFence{Owner: "test-owner", AttemptNo: 1, NodeVersion: 1},
		LeaseDuration: time.Minute,
	})
	if err != nil || heartbeat.Node.Status != workflowdomain.NodeStatusRunning {
		t.Fatalf("unsafe checkpoint heartbeat=%#v err=%v", heartbeat, err)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_run SET lease_until=CURRENT_TIMESTAMP - interval '1 second' WHERE id=$1`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_attempt SET lease_until=CURRENT_TIMESTAMP - interval '1 second' WHERE node_run_id=$1 AND status='running'`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := coordinator.Claim(fixture.ctx, workflowapplication.ClaimCommand{
		NodeRunID: fixture.nodeID, DispatchNo: 1, DeliveryID: "cancel-safety-recovery",
		RiverJobID: 1, RiverJobAttempt: 2, LeaseOwner: "recovery-owner", LeaseDuration: time.Minute,
	})
	if err != nil || reclaimed.Disposition != workflowapplication.ClaimDispositionClaimed || reclaimed.Attempt.AttemptNo != 2 {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	binding := workflowapplication.DeliveryBinding{
		NodeRunID: fixture.nodeID, DispatchNo: 1, DeliveryID: "cancel-safety-recovery",
		Fence: workflowdomain.LeaseFence{Owner: "recovery-owner", AttemptNo: reclaimed.Attempt.AttemptNo, NodeVersion: reclaimed.Node.Version},
	}
	checkpointFailure := foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CHECKPOINT", false, errors.New("cancel requested"))
	if _, err := coordinator.Fail(fixture.ctx, workflowapplication.FailDeliveryCommand{Binding: binding, Failure: workflowdomain.FailureInput{Err: checkpointFailure}}); !hasCode(err, "WORKFLOW_CANCELLATION_DEFERRED") {
		t.Fatalf("unsafe cancellation transition err=%v", err)
	}
	var runStatus, attemptStatus string
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT r.status,a.status FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id JOIN workflow.node_attempt a ON a.node_run_id=n.id AND a.attempt_no=2 WHERE r.id=$1`, string(fixture.runID)).Scan(&runStatus, &attemptStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != string(workflowdomain.RunStatusRunning) || attemptStatus != string(workflowdomain.AttemptStatusRunning) {
		t.Fatalf("unsafe cancellation persisted run=%s attempt=%s", runStatus, attemptStatus)
	}

	safeExecution, err := changeRepository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: created.Version, Status: domain.WritebackStatusApplyFailed,
		FailureCode: "WRITEBACK_CANCELLED_BEFORE_SIDE_EFFECT", At: fixture.now,
	})
	if err != nil || !domain.IsWritebackCancellationSafe(safeExecution.Status, safeExecution.CleanupCompletedAt != nil) {
		t.Fatalf("safe execution=%#v err=%v", safeExecution, err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_run SET lease_until=CURRENT_TIMESTAMP - interval '1 second' WHERE id=$1`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE workflow.node_attempt SET lease_until=CURRENT_TIMESTAMP - interval '1 second' WHERE node_run_id=$1 AND status='running'`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	finalClaim, err := coordinator.Claim(fixture.ctx, workflowapplication.ClaimCommand{
		NodeRunID: fixture.nodeID, DispatchNo: 1, DeliveryID: "cancel-safety-finalize",
		RiverJobID: 1, RiverJobAttempt: 3, LeaseOwner: "finalize-owner", LeaseDuration: time.Minute,
	})
	if err != nil || finalClaim.Disposition != workflowapplication.ClaimDispositionClaimed || finalClaim.Attempt.AttemptNo != 3 {
		t.Fatalf("safe checkpoint cancellation claim=%#v err=%v", finalClaim, err)
	}
	finalBinding := workflowapplication.DeliveryBinding{
		NodeRunID: fixture.nodeID, DispatchNo: 1, DeliveryID: "cancel-safety-finalize",
		Fence: workflowdomain.LeaseFence{Owner: "finalize-owner", AttemptNo: finalClaim.Attempt.AttemptNo, NodeVersion: finalClaim.Node.Version},
	}
	terminal, err := coordinator.Fail(fixture.ctx, workflowapplication.FailDeliveryCommand{Binding: finalBinding, Failure: workflowdomain.FailureInput{Err: checkpointFailure}})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Run.Status != workflowdomain.RunStatusCancelled || terminal.Node.Status != workflowdomain.NodeStatusCancelled || terminal.Attempt.Status != workflowdomain.AttemptStatusCancelled {
		t.Fatalf("terminal cancellation=%#v", terminal)
	}
	storedExecution, err := changeRepository.GetWritebackExecution(fixture.ctx, created.ID)
	if err != nil || !domain.IsWritebackCancellationSafe(storedExecution.Status, storedExecution.CleanupCompletedAt != nil) {
		t.Fatalf("terminal workflow has unsafe execution=%#v err=%v", storedExecution, err)
	}
}

func TestRepositoryBeginWritebackRollsBackBothAuthorizations(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.bindProposalToRun(t)
	begin := domain.BeginWriteback{
		ExecutionID: "not-a-uuid", WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID,
		LeaseOwner: "test-owner", IdempotencyKey: "rollback-" + string(fixture.executionID),
		WriteAuthorization: domain.AuthorizationConsume{Credential: string(fixture.writeAuthorizationID) + "write-auth", IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
		GitAuthorization:   domain.AuthorizationConsume{Credential: string(fixture.gitAuthorizationID) + "git-auth", IdempotencyKey: "git-auth-" + string(fixture.gitAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
	}
	if _, err := fixture.repository.BeginWriteback(fixture.ctx, begin); err == nil {
		t.Fatal("invalid execution id unexpectedly succeeded")
	}
	var writeStatus, gitStatus, proposalStatus string
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT (SELECT status FROM change_control.tool_authorization WHERE id=$1),(SELECT status FROM change_control.tool_authorization WHERE id=$2),(SELECT status FROM change_control.proposal WHERE id=$3)`, string(fixture.writeAuthorizationID), string(fixture.gitAuthorizationID), string(fixture.proposalID)).Scan(&writeStatus, &gitStatus, &proposalStatus); err != nil {
		t.Fatal(err)
	}
	if writeStatus != string(domain.AuthorizationIssued) || gitStatus != string(domain.AuthorizationIssued) || proposalStatus != string(domain.StatusApproved) {
		t.Fatalf("rollback statuses write=%s git=%s proposal=%s", writeStatus, gitStatus, proposalStatus)
	}
	assertRowCount(t, fixture.ctx, fixture.tx, `SELECT count(*) FROM change_control.writeback_execution WHERE workspace_id=$1`, 0, string(fixture.workspaceID))
}

func TestRepositoryBeginWritebackRejectsProposalBoundToDifferentRun(t *testing.T) {
	fixture := newWritebackFixture(t)
	otherRunID := fixture.nextID(t)
	var definitionID string
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT definition_id::text FROM workflow.run WHERE id=$1`, string(fixture.runID)).Scan(&definitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.tx.Exec(fixture.ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(otherRunID), string(fixture.workspaceID), definitionID, fixture.now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.tx.Exec(fixture.ctx, `UPDATE change_control.proposal SET workflow_run_id=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status='approved'`, string(fixture.proposalID), string(otherRunID), fixture.now); err != nil {
		t.Fatal(err)
	}
	begin := domain.BeginWriteback{
		ExecutionID: fixture.executionID, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID,
		LeaseOwner: "test-owner", IdempotencyKey: "wrong-run-" + string(fixture.executionID),
		WriteAuthorization: domain.AuthorizationConsume{Credential: string(fixture.writeAuthorizationID) + "write-auth", IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
		GitAuthorization:   domain.AuthorizationConsume{Credential: string(fixture.gitAuthorizationID) + "git-auth", IdempotencyKey: "git-auth-" + string(fixture.gitAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
	}
	if _, err := fixture.repository.BeginWriteback(fixture.ctx, begin); !hasCode(err, "WRITEBACK_IDENTITY_CONFLICT") {
		t.Fatalf("proposal bound to another run err=%v", err)
	}
	assertRowCount(t, fixture.ctx, fixture.tx, `SELECT count(*) FROM change_control.writeback_execution WHERE workspace_id=$1`, 0, string(fixture.workspaceID))
}

func TestRepositoryBeginWritebackRejectsPreviouslyConsumedAuthorizationsWithoutExecution(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.bindProposalToRun(t)
	begin := domain.BeginWriteback{
		ExecutionID: fixture.executionID, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID,
		LeaseOwner: "test-owner", IdempotencyKey: "consumed-before-begin-" + string(fixture.executionID),
		WriteAuthorization: domain.AuthorizationConsume{Credential: string(fixture.writeAuthorizationID) + "write-auth", IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
		GitAuthorization:   domain.AuthorizationConsume{Credential: string(fixture.gitAuthorizationID) + "git-auth", IdempotencyKey: "git-auth-" + string(fixture.gitAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
	}
	writeConsume := begin.WriteAuthorization
	writeConsume.Credential = hashText(writeConsume.Credential)
	if _, err := fixture.repository.ConsumeAuthorization(fixture.ctx, writeConsume); err != nil {
		t.Fatal(err)
	}
	gitConsume := begin.GitAuthorization
	gitConsume.Credential = hashText(gitConsume.Credential)
	if _, err := fixture.repository.ConsumeAuthorization(fixture.ctx, gitConsume); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.BeginWriteback(fixture.ctx, begin); !hasCode(err, "WRITEBACK_AUTHORIZATION_ALREADY_CONSUMED") {
		t.Fatalf("previously consumed authorizations started writeback: %v", err)
	}
	assertRowCount(t, fixture.ctx, fixture.tx, `SELECT count(*) FROM change_control.writeback_execution WHERE workspace_id=$1`, 0, string(fixture.workspaceID))
	assertProposalState(t, fixture.ctx, fixture.tx, fixture.proposalID, domain.StatusApproved, 3)
}

func TestRepositoryBeginWritebackConcurrentReplayCreatesOneExecution(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.bindProposalToRun(t)
	if err := fixture.tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	base := domain.BeginWriteback{
		WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID,
		LeaseOwner: "test-owner", IdempotencyKey: "concurrent-begin-" + string(fixture.executionID),
		WriteAuthorization: domain.AuthorizationConsume{Credential: string(fixture.writeAuthorizationID) + "write-auth", IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
		GitAuthorization:   domain.AuthorizationConsume{Credential: string(fixture.gitAuthorizationID) + "git-auth", IdempotencyKey: "git-auth-" + string(fixture.gitAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
	}
	commands := []domain.BeginWriteback{base, base}
	commands[0].ExecutionID = fixture.nextID(t)
	commands[1].ExecutionID = fixture.nextID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	type result struct {
		execution domain.WritebackExecution
		err       error
	}
	results := make(chan result, 2)
	for _, command := range commands {
		go func(command domain.BeginWriteback) {
			<-start
			execution, beginErr := repository.BeginWriteback(ctx, command)
			results <- result{execution: execution, err: beginErr}
		}(command)
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil || first.execution.ID == "" || first.execution.ID != second.execution.ID {
		t.Fatalf("first=%#v err=%v second=%#v err=%v", first.execution, first.err, second.execution, second.err)
	}
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM change_control.writeback_execution WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.workspaceID), base.IdempotencyKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("execution count=%d err=%v", count, err)
	}
}

func TestRepositoryWritebackPreparedCheckpointsAndLease(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.bindProposalToRun(t)
	begin := domain.BeginWriteback{
		WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID,
		LeaseOwner: "test-owner", IdempotencyKey: "checkpoint-" + string(fixture.executionID), ExecutionID: fixture.executionID,
		WriteAuthorization: domain.AuthorizationConsume{Credential: string(fixture.writeAuthorizationID) + "write-auth", IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
		GitAuthorization:   domain.AuthorizationConsume{Credential: string(fixture.gitAuthorizationID) + "git-auth", IdempotencyKey: "git-auth-" + string(fixture.gitAuthorizationID), WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID, ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite, Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash},
	}
	execution, err := fixture.repository.BeginWriteback(fixture.ctx, begin)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.ValidateWritebackLease(fixture.ctx, execution.ID, "test-owner"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.ValidateWritebackLease(fixture.ctx, fixture.nodeID, "test-owner"); !hasCode(err, "WRITEBACK_LEASE_LOST") {
		t.Fatalf("node id was accepted as execution id: %v", err)
	}
	if err := fixture.repository.ValidateWritebackLease(fixture.ctx, execution.ID, "wrong-owner"); !hasCode(err, "WRITEBACK_LEASE_LOST") {
		t.Fatalf("invalid lease accepted: %v", err)
	}
	if _, err := fixture.tx.Exec(fixture.ctx, `UPDATE workflow.node_run SET lease_until=CURRENT_TIMESTAMP-interval '1 second' WHERE id=$1`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.ValidateWritebackLease(fixture.ctx, execution.ID, "test-owner"); !hasCode(err, "WRITEBACK_LEASE_LOST") {
		t.Fatalf("expired lease accepted: %v", err)
	}
	if _, err := fixture.tx.Exec(fixture.ctx, `UPDATE workflow.node_run SET lease_until=CURRENT_TIMESTAMP+interval '1 hour' WHERE id=$1`, string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusFileApplied, ResultHash: execution.ResultHash}); !hasCode(err, "WRITEBACK_STATUS_CONFLICT") {
		t.Fatalf("atomic Begin execution skipped file_prepared: %v", err)
	}
	filePrepared, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: domain.WritebackStatusFilePrepared, ResultHash: execution.ResultHash, TemporaryRef: "tmp/checkpoint", BackupRef: "backup/checkpoint", FileByteSize: 15, FileMode: 0o644, FileLockToken: strings.Repeat("1", 64), FileResultLockToken: strings.Repeat("5", 64), FileBackupLockToken: strings.Repeat("6", 64)})
	if err != nil || filePrepared.Status != domain.WritebackStatusFilePrepared || filePrepared.FileByteSize != 15 || filePrepared.FileMode != 0o644 || filePrepared.FileResultLockToken != strings.Repeat("5", 64) || filePrepared.FileBackupLockToken != strings.Repeat("6", 64) {
		t.Fatalf("file prepared=%#v err=%v", filePrepared, err)
	}
	fileApplied, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: execution.ID, ExpectedVersion: filePrepared.Version, Status: domain.WritebackStatusFileApplied, ResultHash: execution.ResultHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: execution.ID, ExpectedVersion: fileApplied.Version, Status: domain.WritebackStatusGitCommitted, ResultHash: execution.ResultHash, GitCommit: fixture.commitHash, ParentGitCommit: fixture.parentCommitHash, DiffHash: fixture.diffHash}); !hasCode(err, "WRITEBACK_STATUS_CONFLICT") {
		t.Fatalf("durable file execution skipped git_prepared: %v", err)
	}
	gitPrepared, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: execution.ID, ExpectedVersion: fileApplied.Version, Status: domain.WritebackStatusGitPrepared, ResultHash: execution.ResultHash, DiffHash: fixture.diffHash, BaseBlobID: strings.Repeat("3", 40), ResultBlobID: strings.Repeat("4", 40), BaseMode: domain.GitFileModeRegular})
	if err != nil || gitPrepared.Status != domain.WritebackStatusGitPrepared || gitPrepared.BaseBlobID != strings.Repeat("3", 40) {
		t.Fatalf("git prepared=%#v err=%v", gitPrepared, err)
	}
}

func TestRepositoryFinalizeWritebackCleanupIsReplayable(t *testing.T) {
	fixture := newWritebackFixture(t)
	created, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil {
		t.Fatal(err)
	}
	fileApplied, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: created.ID, ExpectedVersion: created.Version, Status: domain.WritebackStatusFileApplied, ResultHash: fixture.resultHash})
	if err != nil {
		t.Fatal(err)
	}
	gitCommitted, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: created.ID, ExpectedVersion: fileApplied.Version, Status: domain.WritebackStatusGitCommitted, ResultHash: fixture.resultHash, GitCommit: fixture.commitHash, ParentGitCommit: fixture.parentCommitHash, DiffHash: fixture.diffHash})
	if err != nil {
		t.Fatal(err)
	}
	published, err := fixture.repository.PublishWriteback(fixture.ctx, fixture.publishCommand(t, gitCommitted))
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := fixture.repository.FinalizeWritebackCleanup(fixture.ctx, published.Execution.ID, published.Execution.Version, fixture.now)
	if err != nil || finalized.CleanupCompletedAt == nil || finalized.Version != published.Execution.Version+1 {
		t.Fatalf("finalized=%#v err=%v", finalized, err)
	}
	replayed, err := fixture.repository.FinalizeWritebackCleanup(fixture.ctx, published.Execution.ID, published.Execution.Version, fixture.now)
	if err != nil || replayed.ID != finalized.ID || replayed.Version != finalized.Version || replayed.CleanupCompletedAt == nil {
		t.Fatalf("cleanup replay=%#v err=%v", replayed, err)
	}
}

func TestRepositoryWritebackCheckpointAndPublish(t *testing.T) {
	fixture := newWritebackFixture(t)
	created, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil {
		t.Fatal(err)
	}

	fileApplied, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: created.Version, Status: domain.WritebackStatusFileApplied,
		ResultHash: strings.ToUpper(fixture.resultHash), TemporaryRef: "tmp/writeback", BackupRef: "backup/writeback",
	})
	if err != nil || fileApplied.Status != domain.WritebackStatusFileApplied || fileApplied.Version != 2 || fileApplied.TemporaryRef != "tmp/writeback" || fileApplied.BackupRef != "backup/writeback" {
		t.Fatalf("file applied=%#v err=%v", fileApplied, err)
	}
	assertProposalState(t, fixture.ctx, fixture.tx, fixture.proposalID, domain.StatusApplying, 3)

	if _, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: 1, Status: domain.WritebackStatusGitCommitted,
		ResultHash: fixture.resultHash, GitCommit: fixture.commitHash, ParentGitCommit: fixture.parentCommitHash, DiffHash: fixture.diffHash,
	}); !hasCode(err, "WRITEBACK_VERSION_CONFLICT") {
		t.Fatalf("stale checkpoint err=%v", err)
	}
	if _, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: fileApplied.Version, Status: domain.WritebackStatusApplyFailed, FailureCode: "WRITE_FAILED",
	}); !hasCode(err, "WRITEBACK_STATUS_CONFLICT") {
		t.Fatalf("invalid checkpoint err=%v", err)
	}

	gitCommitted, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: fileApplied.Version, Status: domain.WritebackStatusGitCommitted,
		ResultHash: strings.ToUpper(fixture.resultHash), GitCommit: strings.ToUpper(fixture.commitHash),
		ParentGitCommit: strings.ToUpper(fixture.parentCommitHash), DiffHash: strings.ToUpper(fixture.diffHash),
	})
	if err != nil || gitCommitted.Status != domain.WritebackStatusGitCommitted || gitCommitted.Version != 3 || gitCommitted.GitCommit != fixture.commitHash || gitCommitted.ParentGitCommit != fixture.parentCommitHash || gitCommitted.DiffHash != fixture.diffHash {
		t.Fatalf("git committed=%#v err=%v", gitCommitted, err)
	}
	assertProposalState(t, fixture.ctx, fixture.tx, fixture.proposalID, domain.StatusApplied, 4)

	publish := fixture.publishCommand(t, gitCommitted)
	conflictTx, err := fixture.tx.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	conflictRepository, err := NewRepository(conflictTx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conflictTx.Exec(fixture.ctx, `
		INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,payload,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,CURRENT_TIMESTAMP)`,
		string(fixture.nextID(t)), string(fixture.workspaceID), string(fixture.runID), publish.Event.Type, publish.Event.IdempotencyKey, publish.Event.Payload); err != nil {
		t.Fatal(err)
	}
	if _, err := conflictRepository.PublishWriteback(fixture.ctx, publish); !hasCode(err, "WRITEBACK_PUBLISH_BINDING_CONFLICT") {
		t.Fatalf("conflicting outbox publish err=%v", err)
	}
	assertRowCount(t, fixture.ctx, conflictTx, `SELECT count(*) FROM change_control.proposal_commit WHERE writeback_execution_id=$1`, 0, string(created.ID))
	afterRollback, err := conflictRepository.GetWritebackExecution(fixture.ctx, created.ID)
	if err != nil || afterRollback.Status != domain.WritebackStatusGitCommitted || afterRollback.Version != 3 {
		t.Fatalf("execution after rolled back publish=%#v err=%v", afterRollback, err)
	}
	assertProposalState(t, fixture.ctx, conflictTx, fixture.proposalID, domain.StatusApplied, 4)
	if err := conflictTx.Rollback(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	published, err := fixture.repository.PublishWriteback(fixture.ctx, publish)
	if err != nil || published.Replayed || published.Execution.Status != domain.WritebackStatusVerifying || published.Execution.Version != 4 || published.Commit.ID != publish.Commit.ID || published.Commit.GitCommit != fixture.commitHash {
		t.Fatalf("published=%#v err=%v", published, err)
	}
	assertProposalState(t, fixture.ctx, fixture.tx, fixture.proposalID, domain.StatusVerifying, 5)
	assertRowCount(t, fixture.ctx, fixture.tx, `SELECT count(*) FROM change_control.proposal_commit WHERE writeback_execution_id=$1`, 1, string(created.ID))
	assertRowCount(t, fixture.ctx, fixture.tx, `SELECT count(*) FROM workflow.outbox_event WHERE id=$1 AND idempotency_key=$2`, 1, string(publish.Event.ID), publish.Event.IdempotencyKey)

	replayed, err := fixture.repository.PublishWriteback(fixture.ctx, publish)
	if err != nil || !replayed.Replayed || replayed.Execution.Version != published.Execution.Version || replayed.Commit.ID != published.Commit.ID {
		t.Fatalf("publish replay=%#v err=%v", replayed, err)
	}
	mismatchedCommit := publish
	mismatchedCommit.Commit.ID = fixture.nextID(t)
	if _, err := fixture.repository.PublishWriteback(fixture.ctx, mismatchedCommit); !hasCode(err, "WRITEBACK_PUBLISH_BINDING_CONFLICT") {
		t.Fatalf("commit replay mismatch err=%v", err)
	}
	mismatchedEvent := publish
	mismatchedEvent.Event.Payload = append(json.RawMessage(nil), publish.Event.Payload...)
	mismatchedEvent.Event.Payload = json.RawMessage(strings.Replace(string(mismatchedEvent.Event.Payload), fixture.targetPath, "other.md", 1))
	if _, err := fixture.repository.PublishWriteback(fixture.ctx, mismatchedEvent); !hasCode(err, "WRITEBACK_PUBLISH_BINDING_CONFLICT") {
		t.Fatalf("event replay mismatch err=%v", err)
	}
}

func TestWritebackSQLConstraints(t *testing.T) {
	fixture := newWritebackFixture(t)
	badBinding := fixture.create
	assertSQLRejected(t, fixture.ctx, fixture.tx, writebackInsert,
		string(fixture.nextID(t)), string(badBinding.WorkspaceID), string(badBinding.WorkflowRunID), string(badBinding.NodeRunID),
		string(badBinding.ProposalID), string(badBinding.RevisionID), string(badBinding.ApprovalID),
		string(badBinding.GitAuthorizationID), string(badBinding.WriteAuthorizationID), badBinding.TargetPath,
		badBinding.BaseHash, badBinding.ResultHash, fixture.changeHash, fixture.gitHead,
		string(domain.WritebackStatusPrepared), "sql-cross-binding", nil, nil, int64(1), fixture.now, fixture.now,
	)

	created, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLRejected(t, fixture.ctx, fixture.tx, `UPDATE change_control.writeback_execution SET target_path='other.md',updated_at=CURRENT_TIMESTAMP,version=version+1 WHERE id=$1`, string(created.ID))
	assertSQLRejected(t, fixture.ctx, fixture.tx, `DELETE FROM change_control.writeback_execution WHERE id=$1`, string(created.ID))
	assertSQLRejected(t, fixture.ctx, fixture.tx, `UPDATE change_control.writeback_execution SET status='needs_revision',failure_code='INVALID_GIT_INTENT',base_blob_id=$2,result_blob_id=$3,base_mode='100644',updated_at=CURRENT_TIMESTAMP,version=version+1 WHERE id=$1`, string(created.ID), fixture.gitHead, strings.Repeat("e", len(fixture.gitHead)))

	fileApplied, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{ExecutionID: created.ID, ExpectedVersion: 1, Status: domain.WritebackStatusFileApplied, ResultHash: fixture.resultHash})
	if err != nil {
		t.Fatal(err)
	}
	gitCommitted, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: fileApplied.Version, Status: domain.WritebackStatusGitCommitted,
		ResultHash: fixture.resultHash, GitCommit: fixture.commitHash, ParentGitCommit: fixture.parentCommitHash, DiffHash: fixture.diffHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidPayload := fixture.publishCommand(t, gitCommitted)
	var invalidPayloadBody map[string]any
	if err := json.Unmarshal(invalidPayload.Event.Payload, &invalidPayloadBody); err != nil {
		t.Fatal(err)
	}
	invalidPayloadBody["schema_version"] = 2
	invalidPayload.Event.Payload, err = json.Marshal(invalidPayloadBody)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLRejected(t, fixture.ctx, fixture.tx, `
		INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,payload,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,CURRENT_TIMESTAMP)`,
		string(invalidPayload.Event.ID), string(fixture.workspaceID), string(fixture.runID), invalidPayload.Event.Type, invalidPayload.Event.IdempotencyKey, invalidPayload.Event.Payload,
	)
	assertSQLRejected(t, fixture.ctx, fixture.tx, `
		INSERT INTO change_control.proposal_commit(
			id,workspace_id,writeback_execution_id,proposal_id,revision_id,approval_id,git_commit,parent_git_commit,target_path,diff_hash,result_hash,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,CURRENT_TIMESTAMP)`,
		string(fixture.nextID(t)), string(fixture.workspaceID), string(created.ID), string(fixture.proposalID), string(fixture.revisionID), string(fixture.approvalID),
		fixture.commitHash, fixture.parentCommitHash, fixture.targetPath, fixture.diffHash, strings.Repeat("f", 64),
	)

	published, err := fixture.repository.PublishWriteback(fixture.ctx, fixture.publishCommand(t, gitCommitted))
	if err != nil {
		t.Fatal(err)
	}
	assertSQLRejected(t, fixture.ctx, fixture.tx, `UPDATE change_control.proposal_commit SET target_path='other.md' WHERE id=$1`, string(published.Commit.ID))
	assertSQLRejected(t, fixture.ctx, fixture.tx, `DELETE FROM change_control.proposal_commit WHERE id=$1`, string(published.Commit.ID))
	var outboxID string
	if err := fixture.tx.QueryRow(fixture.ctx, `SELECT id::text FROM workflow.outbox_event WHERE idempotency_key=$1`, domain.ExpectedWritebackReindexKey(published.Execution)).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	assertSQLRejected(t, fixture.ctx, fixture.tx, `UPDATE workflow.outbox_event SET payload='{}' WHERE id=$1`, outboxID)
	assertSQLRejected(t, fixture.ctx, fixture.tx, `DELETE FROM workflow.outbox_event WHERE id=$1`, outboxID)
}

func TestRepositoryWritebackCreateAndAuthorizationConsumeDoNotDeadlock(t *testing.T) {
	fixture := newWritebackFixture(t)
	if err := fixture.tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	type operationResult struct {
		name string
		err  error
	}
	results := make(chan operationResult, 2)
	go func() {
		<-start
		_, createErr := repository.CreateWritebackExecution(ctx, fixture.create)
		results <- operationResult{name: "create writeback", err: createErr}
	}()
	go func() {
		<-start
		_, consumeErr := repository.ConsumeAuthorization(ctx, domain.AuthorizationConsume{
			Credential:     hashText(string(fixture.writeAuthorizationID) + "write-auth"),
			IdempotencyKey: "write-auth-" + string(fixture.writeAuthorizationID),
			WorkspaceID:    fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID,
			ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID,
			ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge,
			Scope: domain.ExpectedAuthorizationScope(fixture.targetPath), ApprovedChangeHash: fixture.changeHash, TargetVersion: fixture.baseHash,
		})
		results <- operationResult{name: "consume authorization", err: consumeErr}
	}()
	close(start)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("%s failed: %v", result.name, result.err)
			}
		case <-ctx.Done():
			t.Fatalf("concurrent writeback operations timed out: %v", ctx.Err())
		}
	}
	execution, err := repository.GetWritebackExecution(context.Background(), fixture.executionID)
	if err != nil || execution.Status != domain.WritebackStatusPrepared {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
	proposal, err := repository.GetProposal(context.Background(), fixture.proposalID)
	if err != nil || proposal.Status != domain.StatusApplying {
		t.Fatalf("proposal=%#v err=%v", proposal, err)
	}
}

func TestRepositoryCheckpointAndLegacyInsertUseConsistentLockOrder(t *testing.T) {
	fixture := newWritebackFixture(t)
	created, err := fixture.repository.CreateWritebackExecution(fixture.ctx, fixture.create)
	if err != nil {
		t.Fatal(err)
	}
	fileApplied, err := fixture.repository.CheckpointWritebackExecution(fixture.ctx, domain.CheckpointWriteback{
		ExecutionID: created.ID, ExpectedVersion: created.Version, Status: domain.WritebackStatusFileApplied,
		ResultHash: fixture.resultHash, TemporaryRef: fixture.create.TemporaryRef, BackupRef: fixture.create.BackupRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	type operationResult struct {
		name string
		err  error
	}
	results := make(chan operationResult, 2)
	candidate := fixture.create
	candidate.ID = fixture.nextID(t)
	candidate.IdempotencyKey = "legacy-conflict-" + string(candidate.ID)
	go func() {
		<-start
		_, checkpointErr := repository.CheckpointWritebackExecution(ctx, domain.CheckpointWriteback{
			ExecutionID: fileApplied.ID, ExpectedVersion: fileApplied.Version, Status: domain.WritebackStatusGitCommitted,
			ResultHash: fixture.resultHash, GitCommit: fixture.commitHash, ParentGitCommit: fixture.parentCommitHash, DiffHash: fixture.diffHash,
		})
		results <- operationResult{name: "checkpoint", err: checkpointErr}
	}()
	go func() {
		<-start
		_, insertErr := fixture.pool.Exec(ctx, writebackInsert,
			string(candidate.ID), string(candidate.WorkspaceID), string(candidate.WorkflowRunID), string(candidate.NodeRunID),
			string(candidate.ProposalID), string(candidate.RevisionID), string(candidate.ApprovalID),
			string(candidate.WriteAuthorizationID), string(candidate.GitAuthorizationID), candidate.TargetPath,
			candidate.BaseHash, candidate.ResultHash, candidate.ApprovedChangeHash, candidate.ApprovedGitHead,
			string(domain.WritebackStatusPrepared), candidate.IdempotencyKey, candidate.TemporaryRef, candidate.BackupRef,
			int64(1), fixture.now, fixture.now)
		results <- operationResult{name: "legacy insert", err: insertErr}
	}()
	close(start)
	for range 2 {
		select {
		case result := <-results:
			var pgErr *pgconn.PgError
			if errors.As(result.err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("%s deadlocked: %v", result.name, result.err)
			}
			if result.name == "checkpoint" && result.err != nil {
				t.Fatalf("checkpoint failed: %v", result.err)
			}
			if result.name == "legacy insert" && result.err == nil {
				t.Fatal("conflicting legacy insert unexpectedly succeeded")
			}
		case <-ctx.Done():
			t.Fatalf("checkpoint/legacy insert lock-order test timed out: %v", ctx.Err())
		}
	}
}

type writebackFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	tx         pgx.Tx
	repository *Repository
	next       func() foundation.ID
	now        time.Time

	workspaceID, runID, nodeID                   foundation.ID
	proposalID, revisionID, approvalID           foundation.ID
	writeAuthorizationID, gitAuthorizationID     foundation.ID
	executionID                                  foundation.ID
	targetPath, baseHash, resultHash, changeHash string
	gitHead, commitHash, parentCommitHash        string
	diffHash                                     string
	create                                       domain.CreateWriteback
}

type cancellationSafetyJobInserter struct{}

func (cancellationSafetyJobInserter) InsertTx(context.Context, any, riveradapter.NodeJobArgs, riveradapter.InsertOptions) (workflowapplication.JobReceipt, error) {
	return workflowapplication.JobReceipt{JobID: 1}, nil
}

func newWritebackFixture(t *testing.T) *writebackFixture {
	t.Helper()
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	generator := foundation.NewUUIDGenerator(nil)
	next := func() foundation.ID {
		id, idErr := generator.New()
		if idErr != nil {
			t.Fatal(idErr)
		}
		return id
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC()
	fixture := &writebackFixture{
		ctx: ctx, pool: pool, tx: tx, repository: repository, next: next, now: now,
		workspaceID: next(), runID: next(), nodeID: next(), proposalID: next(), revisionID: next(), approvalID: next(),
		writeAuthorizationID: next(), gitAuthorizationID: next(), executionID: next(),
		targetPath: "docs/writeback.md",
		baseHash:   strings.Repeat("1", 64), resultHash: strings.Repeat("2", 64), gitHead: strings.Repeat("a", 40),
		commitHash: strings.Repeat("b", 40), parentCommitHash: strings.Repeat("c", 40), diffHash: strings.Repeat("d", 64),
	}
	definitionID := next()
	rootPath := "/tmp/writeback-" + string(fixture.workspaceID)
	definitionGraph, err := json.Marshal(workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key: "writeback", Kind: "tool", InputSchemaVersion: 1, OutputSchemaVersion: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflowapplication.DecodeCanonicalGraph(definitionGraph); err != nil {
		t.Fatalf("writeback fixture graph is not canonical: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Writeback Test',$2,$2,$3,'test',1,$3,$3)`, string(fixture.workspaceID), rootPath, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,$3,1,$4,$5)`, string(definitionID), string(fixture.workspaceID), "writeback-"+string(definitionID), definitionGraph, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(fixture.runID), string(fixture.workspaceID), string(definitionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,lease_owner,lease_until,idempotency_key,input_schema_version,output_schema_version,dispatch_no) VALUES($1,$2,'writeback','tool','running',1,'{}',1,$3,$3,'test-owner',$4,$5,1,1,1)`, string(fixture.nodeID), string(fixture.runID), now, now.Add(time.Hour), "writeback-node-"+string(fixture.nodeID)); err != nil {
		t.Fatal(err)
	}
	content := "writeback result"
	fixture.changeHash = domain.ComputeChangeHash(fixture.targetPath, fixture.baseHash, content)
	requestHash, err := domain.ComputeRequestHashWithRiskLevel(fixture.workspaceID, fixture.targetPath, fixture.baseHash, content, "verified evidence", domain.ProposalRiskLevelLow, "low", "revert commit")
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID: fixture.proposalID, WorkspaceID: fixture.workspaceID, RiskLevel: domain.ProposalRiskLevelLow, TargetPath: fixture.targetPath,
		IdempotencyKey: "proposal-" + string(fixture.proposalID),
		RequestHash:    requestHash,
		Status:         domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: fixture.revisionID, ProposalID: fixture.proposalID, RevisionNo: 1, TargetPath: fixture.targetPath, BaseHash: fixture.baseHash, Content: content, EvidenceSummary: "verified evidence", Risk: "low", RollbackPlan: "revert commit", ChangeHash: fixture.changeHash, CreatedAt: now},
	}
	if _, err := repository.CreateProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	upperGitHead := strings.ToUpper(fixture.gitHead)
	approved, err := repository.Approve(ctx, domain.Approval{
		ID: fixture.approvalID, ProposalID: fixture.proposalID, RevisionID: fixture.revisionID,
		ChangeHash: fixture.changeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &upperGitHead, DecidedAt: now,
	})
	if err != nil || approved.ApprovedGitHead == nil || *approved.ApprovedGitHead != fixture.gitHead {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	fixture.create = domain.CreateWriteback{
		ID: fixture.executionID, WorkspaceID: fixture.workspaceID, WorkflowRunID: fixture.runID, NodeRunID: fixture.nodeID,
		ProposalID: fixture.proposalID, RevisionID: fixture.revisionID, ApprovalID: fixture.approvalID,
		WriteAuthorizationID: fixture.writeAuthorizationID, GitAuthorizationID: fixture.gitAuthorizationID,
		TargetPath: fixture.targetPath, BaseHash: fixture.baseHash, ResultHash: fixture.resultHash,
		ApprovedChangeHash: strings.ToUpper(fixture.changeHash), ApprovedGitHead: strings.ToUpper(fixture.gitHead),
		IdempotencyKey: "writeback-" + string(fixture.executionID), TemporaryRef: "tmp/prepared", BackupRef: "backup/prepared",
	}
	fixture.createAuthorization(t, fixture.writeAuthorizationID, "ApplyApprovedPatch", domain.CapabilityWriteKnowledge, "write-auth")
	fixture.createAuthorization(t, fixture.gitAuthorizationID, "CreateGitCommit", domain.CapabilityGitWrite, "git-auth")
	return fixture
}

func (f *writebackFixture) createAuthorization(t *testing.T, id foundation.ID, toolName string, capability domain.Capability, label string) {
	t.Helper()
	issued, err := f.repository.CreateAuthorization(f.ctx, domain.ToolAuthorization{
		ID: id, WorkspaceID: f.workspaceID, WorkflowRunID: f.runID, NodeRunID: f.nodeID,
		ProposalID: f.proposalID, RevisionID: f.revisionID, ApprovalID: f.approvalID,
		ToolName: toolName, Capability: capability, Scope: domain.ExpectedAuthorizationScope(f.targetPath),
		ApprovedChangeHash: f.changeHash, TargetVersion: f.baseHash, TokenHash: hashText(string(id) + label),
		IdempotencyKey: label + "-" + string(id), Status: domain.AuthorizationIssued,
		IssuedAt: f.now, ExpiresAt: f.now.Add(2 * time.Minute), Version: 1,
	})
	if err != nil || issued.Replayed || issued.Authorization.ID != id {
		t.Fatalf("authorization=%#v err=%v", issued, err)
	}
}

func (f *writebackFixture) bindProposalToRun(t *testing.T) {
	t.Helper()
	if _, err := f.tx.Exec(f.ctx, `UPDATE change_control.proposal SET workflow_run_id=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status='approved' AND workflow_run_id IS NULL`, string(f.proposalID), string(f.runID), f.now); err != nil {
		t.Fatal(err)
	}
}

func (f *writebackFixture) publishCommand(t *testing.T, execution domain.WritebackExecution) domain.PublishWriteback {
	t.Helper()
	payload, err := reindexcontract.EncodeCanonical(reindexcontract.RequestV1{
		SchemaVersion: reindexcontract.SchemaVersionV1, WorkspaceID: f.workspaceID, WorkflowRunID: f.runID,
		NodeRunID: f.nodeID, ProposalID: f.proposalID, RevisionID: f.revisionID,
		ApprovalID: f.approvalID, WritebackExecutionID: execution.ID, TargetPath: f.targetPath,
		ResultHash: f.resultHash, GitCommit: f.commitHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventID := f.nextID(t)
	return domain.PublishWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version,
		Commit: domain.ProposalCommit{
			ID: f.nextID(t), WorkspaceID: f.workspaceID, WritebackExecutionID: execution.ID,
			ProposalID: f.proposalID, RevisionID: f.revisionID, ApprovalID: f.approvalID,
			GitCommit: strings.ToUpper(f.commitHash), ParentGitCommit: strings.ToUpper(f.parentCommitHash),
			TargetPath: f.targetPath, DiffHash: strings.ToUpper(f.diffHash), ResultHash: strings.ToUpper(f.resultHash),
		},
		Event: domain.WritebackOutboxEvent{
			ID: eventID, WorkspaceID: f.workspaceID, RunID: f.runID,
			Type: reindexcontract.EventTypeReindexRequested, IdempotencyKey: domain.ExpectedWritebackReindexKey(execution), Payload: payload,
		},
	}
}

func (f *writebackFixture) nextID(t *testing.T) foundation.ID {
	t.Helper()
	return f.next()
}

func assertProposalState(t *testing.T, ctx context.Context, tx pgx.Tx, proposalID foundation.ID, expectedStatus domain.ProposalStatus, expectedVersion int64) {
	t.Helper()
	var status string
	var version int64
	if err := tx.QueryRow(ctx, `SELECT status,version FROM change_control.proposal WHERE id=$1`, string(proposalID)).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != string(expectedStatus) || version != expectedVersion {
		t.Fatalf("proposal status=%s version=%d, want status=%s version=%d", status, version, expectedStatus, expectedVersion)
	}
}

func assertRowCount(t *testing.T, ctx context.Context, tx pgx.Tx, query string, expected int, arguments ...any) {
	t.Helper()
	var count int
	if err := tx.QueryRow(ctx, query, arguments...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("row count=%d, want %d for %s", count, expected, query)
	}
}

func assertSQLRejected(t *testing.T, ctx context.Context, parent pgx.Tx, query string, arguments ...any) {
	t.Helper()
	tx, err := parent.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query, arguments...); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("SQL constraint accepted mutation: %s", query)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
