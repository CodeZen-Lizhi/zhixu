package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryProposalApprovalAndImmutability(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	workspaceID := integrationID(1)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Change Test',$2,$2,$3,'active',1,$3,$3)`, string(workspaceID), "/tmp/change-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}

	proposalID, revisionID := integrationID(2), integrationID(3)
	baseHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	changeHash := domain.ComputeChangeHash("a.md", baseHash, "new content")
	proposal := domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, TargetPath: "a.md", IdempotencyKey: "create-one", RequestHash: domain.ComputeRequestHash(workspaceID, "a.md", baseHash, "new content", "source evidence", "low", "revert commit"), Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: "a.md", BaseHash: baseHash, Content: "new content", EvidenceSummary: "source evidence", Risk: "low", RollbackPlan: "revert commit", ChangeHash: changeHash, CreatedAt: now},
	}
	if _, err := repository.CreateProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	replayedRequest := proposal
	replayedRequest.ID = integrationID(9)
	replayedRequest.Revision.ID = integrationID(10)
	replayed, err := repository.CreateProposal(ctx, replayedRequest)
	if err != nil || replayed.ID != proposalID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	conflictingRequest := replayedRequest
	conflictingRequest.ID = integrationID(11)
	conflictingRequest.Revision.ID = integrationID(12)
	conflictingRequest.RequestHash = domain.ComputeRequestHash(workspaceID, "a.md", baseHash, "different", "source evidence", "low", "revert commit")
	if _, err := repository.CreateProposal(ctx, conflictingRequest); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("idempotency conflict err=%v", err)
	}
	queried, err := repository.GetProposal(ctx, proposalID)
	if err != nil || queried.TargetPath != "a.md" || queried.Approval != nil || queried.Version != 1 {
		t.Fatalf("queried=%#v err=%v", queried, err)
	}

	approvedGitHead := "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	approval := domain.Approval{ID: integrationID(4), ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead, DecidedAt: now.Add(time.Minute)}
	approved, err := repository.Approve(ctx, approval)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ApprovedGitHead == nil || *approved.ApprovedGitHead != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("approved git head was not normalized: %#v", approved.ApprovedGitHead)
	}
	replayedApproval, err := repository.Approve(ctx, domain.Approval{ID: integrationID(5), ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: domain.DecisionApproved, ApprovedGitHead: &approvedGitHead, DecidedAt: now.Add(2 * time.Minute)})
	if err != nil || replayedApproval.ID != approval.ID {
		t.Fatalf("replayed approval=%#v err=%v", replayedApproval, err)
	}
	if _, err := repository.Approve(ctx, domain.Approval{ID: integrationID(6), ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: domain.DecisionRejected, DecidedAt: now.Add(2 * time.Minute)}); !hasCode(err, "PROPOSAL_NOT_READY_FOR_REVIEW") {
		t.Fatalf("conflicting approval err=%v", err)
	}
	queried, err = repository.GetProposal(ctx, proposalID)
	if err != nil || queried.Status != domain.StatusApproved || queried.Version != 2 || queried.Approval == nil || queried.Approval.ChangeHash != changeHash || queried.Approval.ApprovedGitHead == nil || *queried.Approval.ApprovedGitHead != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("approved proposal=%#v err=%v", queried, err)
	}

	if err := repository.MarkNeedsRevision(ctx, proposalID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	queried, err = repository.GetProposal(ctx, proposalID)
	if err != nil || queried.Status != domain.StatusNeedsRevision || queried.Version != 3 {
		t.Fatalf("needs revision proposal=%#v err=%v", queried, err)
	}

	assertImmutable(t, ctx, tx, `UPDATE change_control.proposal_revision SET content='tampered' WHERE id=$1`, string(revisionID))
	assertImmutable(t, ctx, tx, `UPDATE change_control.approval SET decision='rejected' WHERE id=$1`, string(approval.ID))
}

func TestRepositoryWriteAuthorizationLifecycle(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	// Keep fixture timestamps behind the database clock; authorization consumption
	// uses PostgreSQL CURRENT_TIMESTAMP and the schema requires consumed_at >= issued_at.
	now := time.Now().UTC().Add(-time.Second)
	ids := foundation.NewUUIDGenerator(nil)
	nextID := func() foundation.ID {
		id, idErr := ids.New()
		if idErr != nil {
			t.Fatal(idErr)
		}
		return id
	}
	workspaceID := nextID()
	definitionID, runID, nodeID := nextID(), nextID(), nextID()
	proposalID, revisionID, approvalID := nextID(), nextID(), nextID()
	authorizationID := nextID()
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Auth Test',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/auth-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,'apply-test',1,'{}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(workspaceID), string(definitionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.node_run(id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,lease_owner,lease_until) VALUES($1,$2,'apply','tool','running',1,'{}',1,$3,$3,'test-owner',$4)`, string(nodeID), string(runID), now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	baseHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tokenHash := func(label string) string {
		digest := sha256.Sum256([]byte(string(workspaceID) + label))
		return hex.EncodeToString(digest[:])
	}
	changeHash := domain.ComputeChangeHash("a.md", baseHash, "new content")
	proposal := domain.Proposal{ID: proposalID, WorkspaceID: workspaceID, TargetPath: "a.md", IdempotencyKey: "auth-proposal", RequestHash: domain.ComputeRequestHash(workspaceID, "a.md", baseHash, "new content", "evidence", "low", "rollback"), Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: domain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: "a.md", BaseHash: baseHash, Content: "new content", EvidenceSummary: "evidence", Risk: "low", RollbackPlan: "rollback", ChangeHash: changeHash, CreatedAt: now}}
	if _, err := repository.CreateProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Approve(ctx, domain.Approval{ID: approvalID, ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: domain.DecisionApproved, DecidedAt: now}); err != nil {
		t.Fatal(err)
	}
	historical, err := repository.GetProposal(ctx, proposalID)
	if err != nil || historical.Version != 2 || historical.Approval == nil || historical.Approval.ApprovedGitHead != nil {
		t.Fatalf("historical approval=%#v err=%v", historical, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	repository, err = NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateWorkflowContext(ctx, workspaceID, runID, nodeID); err != nil {
		t.Fatal(err)
	}
	authorization := domain.ToolAuthorization{ID: authorizationID, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, TokenHash: tokenHash("issued"), IdempotencyKey: "auth-1", Status: domain.AuthorizationIssued, IssuedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(4 * time.Minute), Version: 1}
	issued, err := repository.CreateAuthorization(ctx, authorization)
	if err != nil || issued.Authorization.ID != authorizationID || issued.Replayed {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			t.Fatalf("issued=%#v err=%v cause=%#v", issued, err, classified.Cause)
		}
		t.Fatalf("issued=%#v err=%v", issued, err)
	}
	replayed, err := repository.CreateAuthorization(ctx, domain.ToolAuthorization{ID: nextID(), WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, TokenHash: tokenHash("replay"), IdempotencyKey: "auth-1", Status: domain.AuthorizationIssued, IssuedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(4 * time.Minute), Version: 1})
	if err != nil || !replayed.Replayed || replayed.Authorization.ID != authorizationID {
		var classified *foundation.Error
		if errors.As(err, &classified) {
			t.Fatalf("replayed=%#v err=%v cause=%#v", replayed, err, classified.Cause)
		}
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if _, err := repository.CreateAuthorization(ctx, domain.ToolAuthorization{ID: nextID(), WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, TokenHash: tokenHash("ttl-conflict"), IdempotencyKey: "auth-1", Status: domain.AuthorizationIssued, IssuedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(5 * time.Minute), Version: 1}); !hasCode(err, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("ttl idempotency conflict err=%v", err)
	}
	terminalInsert := domain.ToolAuthorization{ID: nextID(), WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, TokenHash: tokenHash("terminal-insert"), IdempotencyKey: "auth-terminal-insert", Status: domain.AuthorizationConsumed, IssuedAt: now, ExpiresAt: now.Add(time.Minute), ConsumedAt: ptrTime(now), Version: 1}
	if _, err := pool.Exec(ctx, authorizationInsert,
		string(terminalInsert.ID), string(terminalInsert.WorkspaceID), string(terminalInsert.WorkflowRunID), string(terminalInsert.NodeRunID), string(terminalInsert.ProposalID), string(terminalInsert.RevisionID), string(terminalInsert.ApprovalID), terminalInsert.ToolName, string(terminalInsert.Capability), terminalInsert.Scope, terminalInsert.ApprovedChangeHash, terminalInsert.TargetVersion, terminalInsert.TokenHash, terminalInsert.IdempotencyKey, string(terminalInsert.Status), terminalInsert.IssuedAt, terminalInsert.ExpiresAt, nil, terminalInsert.ConsumedAt, terminalInsert.Version); err == nil {
		t.Fatal("terminal insert constraint accepted")
	}
	orderCheck := domain.ToolAuthorization{ID: nextID(), WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, TokenHash: tokenHash("time-order"), IdempotencyKey: "auth-time-order", Status: domain.AuthorizationIssued, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Version: 1}
	if _, err := pool.Exec(ctx, authorizationInsert,
		string(orderCheck.ID), string(orderCheck.WorkspaceID), string(orderCheck.WorkflowRunID), string(orderCheck.NodeRunID), string(orderCheck.ProposalID), string(orderCheck.RevisionID), string(orderCheck.ApprovalID), orderCheck.ToolName, string(orderCheck.Capability), orderCheck.Scope, orderCheck.ApprovedChangeHash, orderCheck.TargetVersion, orderCheck.TokenHash, orderCheck.IdempotencyKey, string(orderCheck.Status), orderCheck.IssuedAt, orderCheck.ExpiresAt, nil, nil, orderCheck.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE change_control.tool_authorization SET status='consumed',consumed_at=issued_at-interval '1 second',version=2 WHERE id=$1`, string(orderCheck.ID)); err == nil {
		t.Fatal("timestamp order constraint accepted")
	}
	if _, err := repository.CreateAuthorization(ctx, domain.ToolAuthorization{ID: nextID(), WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "CreateGitCommit", Capability: domain.CapabilityGitWrite, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, TokenHash: tokenHash("conflict"), IdempotencyKey: "auth-1", Status: domain.AuthorizationIssued, IssuedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(4 * time.Minute), Version: 1}); !hasCode(err, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT") {
		t.Fatalf("idempotency conflict err=%v", err)
	}
	if _, err := repository.ConsumeAuthorization(ctx, domain.AuthorizationConsume{Credential: authorization.TokenHash, IdempotencyKey: "different-consume-key", WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash}); !hasCode(err, "WRITE_AUTHORIZATION_BINDING_CONFLICT") {
		t.Fatalf("consume idempotency binding err=%v", err)
	}
	consumed, err := repository.ConsumeAuthorization(ctx, domain.AuthorizationConsume{Credential: authorization.TokenHash, IdempotencyKey: "auth-1", WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, At: now.Add(3 * time.Minute)})
	if err != nil || consumed.Authorization.Status != domain.AuthorizationConsumed || consumed.Replayed {
		t.Fatalf("consumed=%#v err=%v", consumed, err)
	}
	replayedConsume, err := repository.ConsumeAuthorization(ctx, domain.AuthorizationConsume{Credential: authorization.TokenHash, IdempotencyKey: "auth-1", WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: "ApplyApprovedPatch", Capability: domain.CapabilityWriteKnowledge, Scope: "target:a.md", ApprovedChangeHash: changeHash, TargetVersion: baseHash, At: now.Add(3 * time.Minute)})
	if err != nil || !replayedConsume.Replayed {
		t.Fatalf("replayed consume=%#v err=%v", replayedConsume, err)
	}
	if err := repository.RevokeAuthorization(ctx, authorizationID, now.Add(3*time.Minute)); !hasCode(err, "WRITE_AUTHORIZATION_ALREADY_CONSUMED") {
		t.Fatalf("revoke consumed err=%v", err)
	}
	concurrent := authorization
	concurrent.ID = nextID()
	concurrent.TokenHash = tokenHash("concurrent")
	concurrent.IdempotencyKey = "auth-concurrent"
	if _, err := repository.CreateAuthorization(ctx, concurrent); err != nil {
		t.Fatal(err)
	}
	request := domain.AuthorizationConsume{Credential: concurrent.TokenHash, IdempotencyKey: concurrent.IdempotencyKey, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: concurrent.ToolName, Capability: concurrent.Capability, Scope: concurrent.Scope, ApprovedChangeHash: changeHash, TargetVersion: baseHash, At: now.Add(3 * time.Minute)}
	results := make(chan domain.AuthorizationConsumeResult, 2)
	errorsCh := make(chan error, 2)
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, consumeErr := repository.ConsumeAuthorization(ctx, request)
			results <- result
			errorsCh <- consumeErr
			done <- struct{}{}
		}()
	}
	for i := 0; i < 2; i++ {
		<-done
	}
	close(results)
	close(errorsCh)
	var successful, replayedCount int
	for consumeErr := range errorsCh {
		if consumeErr != nil {
			t.Fatalf("concurrent consume err=%v", consumeErr)
		}
		successful++
	}
	for result := range results {
		if result.Replayed {
			replayedCount++
		}
	}
	if successful != 2 || replayedCount != 1 {
		t.Fatalf("concurrent consume successful=%d replayed=%d", successful, replayedCount)
	}
	expired := concurrent
	expired.ID = nextID()
	expired.TokenHash = tokenHash("expired")
	expired.IdempotencyKey = "auth-expired"
	expired.IssuedAt = time.Now().UTC().Add(-2 * time.Minute)
	expired.ExpiresAt = time.Now().UTC().Add(-1 * time.Minute)
	if _, err := pool.Exec(ctx, authorizationInsert,
		string(expired.ID), string(expired.WorkspaceID), string(expired.WorkflowRunID), string(expired.NodeRunID),
		string(expired.ProposalID), string(expired.RevisionID), string(expired.ApprovalID), expired.ToolName,
		string(expired.Capability), expired.Scope, expired.ApprovedChangeHash, expired.TargetVersion,
		expired.TokenHash, expired.IdempotencyKey, string(expired.Status), expired.IssuedAt, expired.ExpiresAt,
		nil, nil, expired.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ConsumeAuthorization(ctx, domain.AuthorizationConsume{Credential: expired.TokenHash, IdempotencyKey: expired.IdempotencyKey, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: expired.ToolName, Capability: expired.Capability, Scope: expired.Scope, ApprovedChangeHash: changeHash, TargetVersion: baseHash, At: expired.IssuedAt}); !hasCode(err, "WRITE_AUTHORIZATION_EXPIRED") {
		t.Fatalf("expired consume err=%v", err)
	}
	revoked := concurrent
	revoked.ID = nextID()
	revoked.TokenHash = tokenHash("revoked")
	revoked.IdempotencyKey = "auth-revoked"
	if _, err := repository.CreateAuthorization(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	if err := repository.RevokeAuthorization(ctx, revoked.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ConsumeAuthorization(ctx, domain.AuthorizationConsume{Credential: revoked.TokenHash, IdempotencyKey: revoked.IdempotencyKey, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: revoked.ToolName, Capability: revoked.Capability, Scope: revoked.Scope, ApprovedChangeHash: changeHash, TargetVersion: baseHash, At: now.Add(3 * time.Minute)}); !hasCode(err, "WRITE_AUTHORIZATION_REVOKED") {
		t.Fatalf("revoked consume err=%v", err)
	}
	stale := concurrent
	stale.ID = nextID()
	stale.TokenHash = tokenHash("stale")
	stale.IdempotencyKey = "auth-stale"
	if _, err := repository.CreateAuthorization(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkNeedsRevision(ctx, proposalID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ConsumeAuthorization(ctx, domain.AuthorizationConsume{Credential: stale.TokenHash, IdempotencyKey: stale.IdempotencyKey, WorkspaceID: workspaceID, WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposalID, RevisionID: revisionID, ApprovalID: approvalID, ToolName: stale.ToolName, Capability: stale.Capability, Scope: stale.Scope, ApprovedChangeHash: changeHash, TargetVersion: baseHash}); !hasCode(err, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED") {
		t.Fatalf("stale proposal consume err=%v", err)
	}
	blocked := stale
	blocked.ID = nextID()
	blocked.TokenHash = tokenHash("blocked-after-stale")
	blocked.IdempotencyKey = "auth-blocked-after-stale"
	if _, err := repository.CreateAuthorization(ctx, blocked); !hasCode(err, "WRITE_AUTHORIZATION_CREATE_FAILED") {
		t.Fatalf("needs-revision proposal issued authorization err=%v", err)
	}
	deleteTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deleteTx.Exec(ctx, `DELETE FROM change_control.tool_authorization WHERE id=$1`, string(authorizationID)); err == nil {
		_ = deleteTx.Rollback(ctx)
		t.Fatal("tool authorization delete succeeded")
	}
	if err := deleteTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertImmutable(t *testing.T, ctx context.Context, parent pgx.Tx, query string, argument any) {
	t.Helper()
	tx, err := parent.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query, argument); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("immutable mutation succeeded: %s", query)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func integrationID(n byte) foundation.ID {
	return foundation.ID([]byte{hexDigit(n >> 4), hexDigit(n & 15), '0', '0', '0', '0', '0', '0', '-', '0', '0', '0', '0', '-', '4', '0', '0', '0', '-', '8', '0', '0', '0', '-', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0'})
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + n - 10
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
