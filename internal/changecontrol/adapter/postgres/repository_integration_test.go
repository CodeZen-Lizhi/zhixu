package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	eventcontract "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/eventcontract"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
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
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Change Test',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/change-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}

	proposalID, revisionID := integrationID(2), integrationID(3)
	baseHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	changeHash := domain.ComputeChangeHash("a.md", baseHash, "new content")
	requestHash, err := domain.ComputeRequestHashWithRiskLevel(workspaceID, "a.md", baseHash, "new content", "source evidence", domain.ProposalRiskLevelLow, "low", "revert commit")
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, RiskLevel: domain.ProposalRiskLevelLow, TargetPath: "a.md", IdempotencyKey: "create-one", RequestHash: requestHash, Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: "a.md", BaseHash: baseHash, Content: "new content", EvidenceSummary: "source evidence", Risk: "low", RollbackPlan: "revert commit", ChangeHash: changeHash, CreatedAt: now},
	}
	if _, err := repository.CreateProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	replayedRequest := proposal
	replayedRequest.ID = integrationID(9)
	replayedRequest.Revision.ID = integrationID(10)
	replayedRequest.RiskLevel = domain.ProposalRiskLevelLow
	replayedRequest.RequestHash, err = domain.ComputeRequestHashWithRiskLevel(workspaceID, "a.md", baseHash, "new content", "source evidence", domain.ProposalRiskLevelLow, "low", "revert commit")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.CreateProposal(ctx, replayedRequest)
	if err != nil || replayed.ID != proposalID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	conflictingRequest := replayedRequest
	conflictingRequest.ID = integrationID(11)
	conflictingRequest.Revision.ID = integrationID(12)
	conflictingRequest.RequestHash = mustFileRequestHash(t, workspaceID, "a.md", baseHash, "different", "source evidence", domain.ProposalRiskLevelLow, "low", "revert commit")
	if _, err := repository.CreateProposal(ctx, conflictingRequest); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("idempotency conflict err=%v", err)
	}
	queried, err := repository.GetProposal(ctx, proposalID)
	if err != nil || queried.RiskLevel != domain.ProposalRiskLevelLow || queried.TargetPath != "a.md" || queried.Approval != nil || queried.WorkflowRunID != nil || queried.Version != 1 {
		t.Fatalf("queried=%#v err=%v", queried, err)
	}
	definitionID, workflowRunID := integrationID(7), integrationID(8)
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,'safe-writeback',1,'{}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(workflowRunID), string(workspaceID), string(definitionID), now); err != nil {
		t.Fatal(err)
	}
	duplicateProposal := proposal
	duplicateProposal.ID, duplicateProposal.Revision.ID = integrationID(13), integrationID(14)
	duplicateProposal.IdempotencyKey = "create-duplicate-binding"
	duplicateProposal.RequestHash = mustFileRequestHash(t, workspaceID, "b.md", baseHash, "other content", "source evidence", domain.ProposalRiskLevelLow, "low", "revert commit")
	duplicateProposal.TargetPath = "b.md"
	duplicateProposal.Revision.ProposalID = duplicateProposal.ID
	duplicateProposal.Revision.TargetPath = "b.md"
	duplicateProposal.Revision.Content = "other content"
	duplicateProposal.Revision.ChangeHash = domain.ComputeChangeHash("b.md", baseHash, "other content")
	if _, err := repository.CreateProposal(ctx, duplicateProposal); err != nil {
		t.Fatal(err)
	}
	otherWorkspaceID, otherDefinitionID, otherRunID := integrationID(15), integrationID(16), integrationID(17)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Other Change Test',$2,$2,$3,'test',1,$3,$3)`, string(otherWorkspaceID), "/tmp/change-"+string(otherWorkspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,'safe-writeback',1,'{}',$3)`, string(otherDefinitionID), string(otherWorkspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(otherRunID), string(otherWorkspaceID), string(otherDefinitionID), now); err != nil {
		t.Fatal(err)
	}
	crossWorkspaceProposal := proposal
	crossWorkspaceProposal.ID, crossWorkspaceProposal.Revision.ID = integrationID(18), integrationID(19)
	crossWorkspaceProposal.IdempotencyKey = "create-cross-workspace-binding"
	crossWorkspaceProposal.RequestHash = mustFileRequestHash(t, workspaceID, "c.md", baseHash, "cross content", "source evidence", domain.ProposalRiskLevelLow, "low", "revert commit")
	crossWorkspaceProposal.TargetPath = "c.md"
	crossWorkspaceProposal.Revision.ProposalID = crossWorkspaceProposal.ID
	crossWorkspaceProposal.Revision.TargetPath = "c.md"
	crossWorkspaceProposal.Revision.Content = "cross content"
	crossWorkspaceProposal.Revision.ChangeHash = domain.ComputeChangeHash("c.md", baseHash, "cross content")
	if _, err := repository.CreateProposal(ctx, crossWorkspaceProposal); err != nil {
		t.Fatal(err)
	}
	invalidStatusProposal := proposal
	invalidStatusProposal.ID, invalidStatusProposal.Revision.ID = integrationID(23), integrationID(24)
	invalidStatusProposal.IdempotencyKey = "create-invalid-status-binding"
	invalidStatusProposal.RequestHash = mustFileRequestHash(t, workspaceID, "d.md", baseHash, "invalid status content", "source evidence", domain.ProposalRiskLevelLow, "low", "revert commit")
	invalidStatusProposal.TargetPath = "d.md"
	invalidStatusProposal.Revision.ProposalID = invalidStatusProposal.ID
	invalidStatusProposal.Revision.TargetPath = "d.md"
	invalidStatusProposal.Revision.Content = "invalid status content"
	invalidStatusProposal.Revision.ChangeHash = domain.ComputeChangeHash("d.md", baseHash, "invalid status content")
	if _, err := repository.CreateProposal(ctx, invalidStatusProposal); err != nil {
		t.Fatal(err)
	}
	assertSQLState(t, ctx, tx, "23514", `UPDATE change_control.proposal SET status='rejected',workflow_run_id=$2,updated_at=$3,version=version+1 WHERE id=$1`, string(invalidStatusProposal.ID), string(workflowRunID), now.Add(time.Minute))
	assertSQLState(t, ctx, tx, "55000", `INSERT INTO change_control.proposal(id,workspace_id,status,risk_level,created_at,updated_at,idempotency_key,request_hash,workflow_run_id) VALUES($1,$2,'ready_for_review','LOW',$3,$3,$4,$5,$6)`, string(integrationID(20)), string(workspaceID), now, "insert-bound-proposal", strings.Repeat("0", 64), string(workflowRunID))

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
	if _, err := tx.Exec(ctx, `UPDATE change_control.proposal SET workflow_run_id=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status='approved'`, string(proposalID), string(workflowRunID), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	queried, err = repository.GetProposal(ctx, proposalID)
	if err != nil || queried.Status != domain.StatusApproved || queried.Version != 3 || queried.WorkflowRunID == nil || *queried.WorkflowRunID != workflowRunID || queried.Approval == nil || queried.Approval.ChangeHash != changeHash || queried.Approval.ApprovedGitHead == nil || *queried.Approval.ApprovedGitHead != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("approved proposal=%#v err=%v", queried, err)
	}
	assertSQLState(t, ctx, tx, "55000", `UPDATE change_control.proposal SET workflow_run_id=NULL,updated_at=$2,version=version+1 WHERE id=$1`, string(proposalID), now.Add(3*time.Minute))

	if _, err := repository.Approve(ctx, domain.Approval{ID: integrationID(21), ProposalID: duplicateProposal.ID, RevisionID: duplicateProposal.Revision.ID, ChangeHash: duplicateProposal.Revision.ChangeHash, Decision: domain.DecisionApproved, DecidedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	assertSQLState(t, ctx, tx, "23505", `UPDATE change_control.proposal SET workflow_run_id=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status='approved'`, string(duplicateProposal.ID), string(workflowRunID), now.Add(2*time.Minute))
	if _, err := repository.Approve(ctx, domain.Approval{ID: integrationID(22), ProposalID: crossWorkspaceProposal.ID, RevisionID: crossWorkspaceProposal.Revision.ID, ChangeHash: crossWorkspaceProposal.Revision.ChangeHash, Decision: domain.DecisionApproved, DecidedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	assertSQLState(t, ctx, tx, "23514", `UPDATE change_control.proposal SET workflow_run_id=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status='approved'`, string(crossWorkspaceProposal.ID), string(otherRunID), now.Add(2*time.Minute))

	if err := repository.MarkNeedsRevision(ctx, proposalID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	queried, err = repository.GetProposal(ctx, proposalID)
	if err != nil || queried.Status != domain.StatusNeedsRevision || queried.Version != 4 || queried.WorkflowRunID == nil || *queried.WorkflowRunID != workflowRunID {
		t.Fatalf("needs revision proposal=%#v err=%v", queried, err)
	}

	assertImmutable(t, ctx, tx, `UPDATE change_control.proposal_revision SET content='tampered' WHERE id=$1`, string(revisionID))
	assertImmutable(t, ctx, tx, `UPDATE change_control.approval SET decision='rejected' WHERE id=$1`, string(approval.ID))
}

func TestRepositoryRejectedApprovalPublishesProposalEventAndExactReplays(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	events, err := eventspostgres.NewStore(tx)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(tx, events)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
	workspaceID := integrationID(60)
	proposalID, revisionID, approvalID := integrationID(61), integrationID(62), integrationID(63)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'Proposal Event Test',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/proposal-event-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	baseHash := strings.Repeat("0", 64)
	content := "rejected proposal content"
	changeHash := domain.ComputeChangeHash("events/rejected.md", baseHash, content)
	requestHash, err := domain.ComputeRequestHashWithRiskLevel(
		workspaceID, "events/rejected.md", baseHash, content, "event evidence",
		domain.ProposalRiskLevelLow, "low", "restore the source",
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, RiskLevel: domain.ProposalRiskLevelLow,
		TargetPath: "events/rejected.md", IdempotencyKey: "proposal-event-rejected",
		RequestHash: requestHash, Status: domain.StatusReady, Version: 1,
		CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: revisionID, ProposalID: proposalID, RevisionNo: 1,
			TargetPath: "events/rejected.md", BaseHash: baseHash, Content: content,
			EvidenceSummary: "event evidence", Risk: "low", RollbackPlan: "restore the source",
			ChangeHash: changeHash, CreatedAt: now,
		},
	}
	if _, err := repository.CreateProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	approval := domain.Approval{
		ID: approvalID, ProposalID: proposalID, RevisionID: revisionID,
		ChangeHash: changeHash, Decision: domain.DecisionRejected, DecidedAt: now.Add(time.Minute),
	}
	if _, err := repository.Approve(ctx, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Approve(ctx, approval); err != nil {
		t.Fatal(err)
	}

	var eventCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.server_event
		WHERE workspace_id=$1 AND event_type=$2 AND resource_ref=$3`,
		string(workspaceID), eventcontract.ProposalRejectedEventType, "proposal:"+string(proposalID)).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("proposal rejected event count=%d, want 1", eventCount)
	}
	var eventType, resourceRef, sourceRef, status string
	var resourceVersion int64
	if err := tx.QueryRow(ctx, `SELECT event_type,resource_ref,resource_version,source_event_ref,payload_summary->>'status'
		FROM ops.server_event WHERE workspace_id=$1 ORDER BY seq`, string(workspaceID)).Scan(
		&eventType, &resourceRef, &resourceVersion, &sourceRef, &status,
	); err != nil {
		t.Fatal(err)
	}
	if eventType != eventcontract.ProposalRejectedEventType || resourceRef != "proposal:"+string(proposalID) ||
		resourceVersion != 2 || sourceRef != eventcontract.ProposalRejectedEventType+":"+string(approvalID)+":v1" || status != string(domain.StatusRejected) {
		t.Fatalf("proposal event=%s ref=%s version=%d source=%s status=%s", eventType, resourceRef, resourceVersion, sourceRef, status)
	}
}

func TestApprovalWritebackBindingMigrationDownGuard(t *testing.T) {
	pool, ctx := newChangeControlTestDatabase(t)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := platformmigration.NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName("goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 13); err != nil {
		t.Fatalf("migrate to 00013: %v", err)
	}
	defer func() {
		if _, upErr := provider.Up(context.Background()); upErr != nil {
			t.Errorf("restore latest migrations: %v", upErr)
		}
	}()
	ids := foundation.NewUUIDGenerator(nil)
	nextID := func() foundation.ID {
		id, idErr := ids.New()
		if idErr != nil {
			t.Fatal(idErr)
		}
		return id
	}
	workspaceID, definitionID, runID, proposalID := nextID(), nextID(), nextID(), nextID()
	now := time.Now().UTC()
	rootPath := "/tmp/approval-down-guard-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Approval Down Guard',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), rootPath, now); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM change_control.proposal WHERE id=$1`, string(proposalID))
		_, _ = pool.Exec(context.Background(), `DELETE FROM workflow.run WHERE id=$1`, string(runID))
		_, _ = pool.Exec(context.Background(), `DELETE FROM workflow.definition WHERE id=$1`, string(definitionID))
		_, _ = pool.Exec(context.Background(), `DELETE FROM core.workspace WHERE id=$1`, string(workspaceID))
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES($1,$2,'approval-down-guard',1,'{}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(runID), string(workspaceID), string(definitionID), now); err != nil {
		t.Fatal(err)
	}
	// 该断言精确验证 00013，因此 fixture 保持 00013 时的旧 Proposal schema。
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(id,workspace_id,status,created_at,updated_at,idempotency_key,request_hash) VALUES($1,$2,'approved',$3,$3,$4,$5)`, string(proposalID), string(workspaceID), now, "down-guard-"+string(proposalID), strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE change_control.proposal SET workflow_run_id=$2,updated_at=$3,version=version+1 WHERE id=$1`, string(proposalID), string(runID), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("00013 Down accepted a persisted Proposal to Workflow Run binding")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("guarded Down error=%v", err)
		}
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != 13 {
		t.Fatalf("guarded Down version=%d err=%v, want 13", version, err)
	}
}

func TestCreateOnlyMigrationEmptyDownUpAndGuard(t *testing.T) {
	pool, ctx := newChangeControlTestDatabase(t)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := platformmigration.NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName("goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 70); err != nil {
		t.Fatalf("migrate through 00070: %v", err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("empty 00070 down: %v", err)
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != 69 {
		t.Fatalf("version after empty down=%d err=%v, want 69", version, err)
	}
	if _, err := provider.UpTo(ctx, 70); err != nil {
		t.Fatalf("repeat 00070 up: %v", err)
	}

	workspaceID := integrationID(0x70)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'Create Only Migration Test',$2,$2,$3,'inactive',1,$3,$3)`,
		string(workspaceID), "/tmp/create-only-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	proposalID, revisionID := integrationID(0x71), integrationID(0x72)
	targetPath := "notes/create-only.md"
	absenceToken, err := domain.ComputeAbsenceToken(workspaceID, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	content := "# Create only\n"
	changeHash, err := domain.ComputeChangeHashForTarget(workspaceID, targetPath, domain.TargetModeCreateOnly, absenceToken, content)
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputeRequestHashWithTargetMode(
		workspaceID, targetPath, domain.TargetModeCreateOnly, absenceToken, content, "manual document revision",
		domain.ProposalRiskLevelLow, "low", "remove the created document",
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, Type: domain.ProposalTypeFilePatch,
		RiskLevel: domain.ProposalRiskLevelLow, IdempotencyKey: "create-only-migration", RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: targetPath,
			TargetMode: domain.TargetModeCreateOnly, BaseHash: absenceToken, Content: content,
			EvidenceSummary: "manual document revision", Risk: "low", RollbackPlan: "remove the created document",
			ChangeHash: changeHash, CreatedAt: now,
		},
	}
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision.TargetMode != domain.TargetModeCreateOnly || created.Revision.BaseHash != absenceToken {
		t.Fatalf("create-only revision=%#v", created.Revision)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("00070 Down accepted persisted CREATE_ONLY revision")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("guarded Down error=%v", err)
		}
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != 70 {
		t.Fatalf("guarded Down version=%d err=%v, want 70", version, err)
	}
}

func TestCreateOnlyMigrationDownWaitsForWriterAndRejectsCommittedFact(t *testing.T) {
	pool, ctx := newChangeControlTestDatabase(t)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := platformmigration.NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName("goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 70); err != nil {
		t.Fatalf("migrate through 00070: %v", err)
	}

	workspaceID := integrationID(0x73)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,'Create Only Concurrent Down',$2,$2,$3,'inactive',1,$3,$3)`,
		string(workspaceID), "/tmp/create-only-concurrent-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}

	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	if _, err := writer.Exec(ctx, `LOCK TABLE change_control.proposal_revision IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	downResult := make(chan error, 1)
	go func() {
		_, downErr := provider.Down(ctx)
		downResult <- downErr
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM pg_locks lock
				JOIN pg_class relation ON relation.oid=lock.relation
				JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
				WHERE namespace.nspname='change_control'
				  AND relation.relname='proposal_revision'
				  AND lock.mode='AccessExclusiveLock'
				  AND NOT lock.granted
			)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("00070 Down did not wait for the active CREATE_ONLY writer")
		}
		time.Sleep(10 * time.Millisecond)
	}

	targetPath := "notes/concurrent-create-only.md"
	absenceToken, err := domain.ComputeAbsenceToken(workspaceID, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	content := "# Concurrent create only\n"
	changeHash, err := domain.ComputeChangeHashForTarget(workspaceID, targetPath, domain.TargetModeCreateOnly, absenceToken, content)
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputeRequestHashWithTargetMode(
		workspaceID, targetPath, domain.TargetModeCreateOnly, absenceToken, content, "concurrent down guard",
		domain.ProposalRiskLevelLow, "low", "remove the created document",
	)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(writer)
	if err != nil {
		t.Fatal(err)
	}
	proposalID, revisionID := integrationID(0x74), integrationID(0x75)
	if _, err := repository.CreateProposal(ctx, domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, Type: domain.ProposalTypeFilePatch,
		RiskLevel: domain.ProposalRiskLevelLow, IdempotencyKey: "create-only-concurrent-down", RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: targetPath,
			TargetMode: domain.TargetModeCreateOnly, BaseHash: absenceToken, Content: content,
			EvidenceSummary: "concurrent down guard", Risk: "low", RollbackPlan: "remove the created document",
			ChangeHash: changeHash, CreatedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-downResult:
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("guarded concurrent Down error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("00070 Down remained blocked after the writer committed")
	}
	if version, err := provider.GetDBVersion(ctx); err != nil || version != 70 {
		t.Fatalf("guarded concurrent Down version=%d err=%v, want 70", version, err)
	}
}

func TestRepositoryKnowledgeChangeProposalCompatibility(t *testing.T) {
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
	now := time.Now().UTC().Add(-time.Second)
	workspaceID := integrationID(31)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'Knowledge Typed Test',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), "/tmp/knowledge-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	change := knowledgeChangeFixtureForIntegration()
	requestHash, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel(workspaceID, change, domain.ProposalRiskLevelHigh, "medium", "restore relation")
	if err != nil {
		t.Fatal(err)
	}
	changeHash, err := domain.ComputeKnowledgeChangeHash(change, "medium", "restore relation")
	if err != nil {
		t.Fatal(err)
	}
	proposalID, revisionID, approvalID := integrationID(32), integrationID(33), integrationID(34)
	proposal := domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, Type: domain.ProposalTypeKnowledgeChange,
		RiskLevel: domain.ProposalRiskLevelHigh, IdempotencyKey: "knowledge-create", RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: revisionID, ProposalID: proposalID, RevisionNo: 1,
			Risk: "medium", RollbackPlan: "restore relation", ChangeHash: changeHash,
			KnowledgeChange: &change, CreatedAt: now,
		},
	}
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.Type != domain.ProposalTypeKnowledgeChange || created.Revision.KnowledgeChange == nil {
		t.Fatalf("created=%#v", created)
	}
	queried, err := repository.GetProposal(ctx, proposalID)
	if err != nil {
		t.Fatal(err)
	}
	if queried.Type != domain.ProposalTypeKnowledgeChange || queried.TargetPath != "" || queried.Revision.TargetPath != "" || queried.Revision.BaseHash != "" || queried.Revision.Content != "" || queried.Revision.KnowledgeChange == nil {
		t.Fatalf("queried=%#v", queried)
	}
	if queried.Revision.KnowledgeChange.ChangeSet.RelationType != knowledge.RelationBelongsTo || queried.Revision.KnowledgeChange.SchemaVersion != domain.KnowledgeChangeSchemaVersion {
		t.Fatalf("queried typed revision=%#v", queried.Revision.KnowledgeChange)
	}
	approved, err := repository.Approve(ctx, domain.Approval{ID: approvalID, ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: domain.DecisionApproved, DecidedAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Decision != domain.DecisionApproved {
		t.Fatalf("approved=%#v", approved)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func newChangeControlMigrationTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool, ctx := newChangeControlTestDatabase(t)
	runner, err := platformmigration.NewRunner(pool, projectmigrations.FS)
	if err == nil {
		err = runner.Up(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func newChangeControlTestDatabase(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Change Control migration integration tests")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := fmt.Sprintf("zhixu_change_down_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + databaseName
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	return pool, ctx
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
	proposal := domain.Proposal{ID: proposalID, WorkspaceID: workspaceID, RiskLevel: domain.ProposalRiskLevelLow, TargetPath: "a.md", IdempotencyKey: "auth-proposal", RequestHash: mustFileRequestHash(t, workspaceID, "a.md", baseHash, "new content", "evidence", domain.ProposalRiskLevelLow, "low", "rollback"), Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: domain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: "a.md", BaseHash: baseHash, Content: "new content", EvidenceSummary: "evidence", Risk: "low", RollbackPlan: "rollback", ChangeHash: changeHash, CreatedAt: now}}
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

func assertImmutable(t *testing.T, ctx context.Context, parent pgx.Tx, query string, arguments ...any) {
	t.Helper()
	tx, err := parent.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query, arguments...); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("immutable mutation succeeded: %s", query)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertSQLState(t *testing.T, ctx context.Context, parent pgx.Tx, sqlState, query string, arguments ...any) {
	t.Helper()
	tx, err := parent.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, execErr := tx.Exec(ctx, query, arguments...)
	if execErr == nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("SQLSTATE %s mutation succeeded: %s", sqlState, query)
	}
	var pgErr *pgconn.PgError
	if !errors.As(execErr, &pgErr) || pgErr.Code != sqlState {
		_ = tx.Rollback(ctx)
		t.Fatalf("SQLSTATE=%v want=%s query=%s", execErr, sqlState, query)
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

func mustFileRequestHash(t *testing.T, workspaceID foundation.ID, targetPath, baseHash, content, evidence string, riskLevel domain.ProposalRiskLevel, risk, rollback string) string {
	t.Helper()
	hash, err := domain.ComputeRequestHashWithRiskLevel(workspaceID, targetPath, baseHash, content, evidence, riskLevel, risk, rollback)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func knowledgeChangeFixtureForIntegration() domain.KnowledgeChange {
	change := domain.KnowledgeChange{
		TargetRefs: []domain.KnowledgeTargetRef{{
			Type:        domain.KnowledgeTargetRefRelationCandidate,
			ID:          integrationID(40),
			Fingerprint: strings.Repeat("a", 64),
		}},
		BaseVersions: []domain.KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: integrationID(41), Version: 4},
			{NodeType: knowledge.NodeTypeTopic, NodeID: integrationID(42), Version: 2},
		},
		ChangeSet: domain.KnowledgeChangeSet{
			Operation:    domain.KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: integrationID(41)},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: integrationID(42)},
			RelationType: knowledge.RelationBelongsTo,
		},
		EvidenceRefs: []domain.KnowledgeEvidenceRef{{
			CandidateEvidenceID: integrationID(43),
			SemanticHash:        strings.Repeat("b", 64),
		}},
		SchemaVersion: domain.KnowledgeChangeSchemaVersion,
	}
	normalized, err := domain.ValidateKnowledgeChange(change)
	if err != nil {
		panic(err)
	}
	return normalized
}

func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
