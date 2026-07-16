package postgres

import (
	"context"
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
		ID: proposalID, WorkspaceID: workspaceID, TargetPath: "a.md", Status: domain.StatusReady, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: "a.md", BaseHash: baseHash, Content: "new content", EvidenceSummary: "source evidence", Risk: "low", RollbackPlan: "revert commit", ChangeHash: changeHash, CreatedAt: now},
	}
	if _, err := repository.CreateProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	queried, err := repository.GetProposal(ctx, proposalID)
	if err != nil || queried.TargetPath != "a.md" || queried.Approval != nil {
		t.Fatalf("queried=%#v err=%v", queried, err)
	}

	approval := domain.Approval{ID: integrationID(4), ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: domain.DecisionApproved, DecidedAt: now.Add(time.Minute)}
	if _, err := repository.Approve(ctx, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Approve(ctx, domain.Approval{ID: integrationID(5), ProposalID: proposalID, RevisionID: revisionID, ChangeHash: changeHash, Decision: domain.DecisionApproved, DecidedAt: now.Add(2 * time.Minute)}); !hasCode(err, "PROPOSAL_NOT_READY_FOR_REVIEW") {
		t.Fatalf("duplicate approval err=%v", err)
	}
	queried, err = repository.GetProposal(ctx, proposalID)
	if err != nil || queried.Status != domain.StatusApproved || queried.Approval == nil || queried.Approval.ChangeHash != changeHash {
		t.Fatalf("approved proposal=%#v err=%v", queried, err)
	}

	if err := repository.MarkNeedsRevision(ctx, proposalID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	queried, err = repository.GetProposal(ctx, proposalID)
	if err != nil || queried.Status != domain.StatusNeedsRevision {
		t.Fatalf("needs revision proposal=%#v err=%v", queried, err)
	}

	assertImmutable(t, ctx, tx, `UPDATE change_control.proposal_revision SET content='tampered' WHERE id=$1`, string(revisionID))
	assertImmutable(t, ctx, tx, `UPDATE change_control.approval SET decision='rejected' WHERE id=$1`, string(approval.ID))
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

func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
