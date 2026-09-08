//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryCreatesReplaysAndReadsTypedRestoreDocumentProposal(t *testing.T) {
	platform, ctx := newChangeControlMigrationTestPool(t)
	pool := platform.DB()
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := integrationID(0xd1)
	documentID := integrationID(0xd2)
	articleRevisionID := integrationID(0xd3)
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	seedRestoreDocumentOwner(t, ctx, pool, workspaceID, documentID, articleRevisionID, now)

	proposal := restoreDocumentProposalFixture(t, workspaceID, documentID, integrationID(0xd4), integrationID(0xd5), 7, now)
	created, err := repository.CreateRestoreDocumentProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != proposal.ID || created.Revision.RestoreDocument == nil {
		t.Fatalf("created=%+v", created)
	}
	loaded, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Type != domain.ProposalTypeRestoreDocument || loaded.RiskLevel != domain.ProposalRiskLevelHigh ||
		loaded.Revision.RestoreDocument == nil || *loaded.Revision.RestoreDocument != *proposal.Revision.RestoreDocument ||
		loaded.Revision.Content != proposal.Revision.Content || loaded.RequestHash != proposal.RequestHash {
		t.Fatalf("loaded=%+v", loaded)
	}

	replay := proposal
	replay.ID = integrationID(0xd6)
	replay.Revision.ID = integrationID(0xd7)
	replay.Revision.ProposalID = replay.ID
	replayed, err := repository.CreateRestoreDocumentProposal(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != proposal.ID || replayed.Revision.ID != proposal.Revision.ID {
		t.Fatalf("replayed=%+v", replayed)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), proposal.IdempotencyKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("proposal count=%d err=%v", count, err)
	}

	stale := restoreDocumentProposalFixture(t, workspaceID, documentID, integrationID(0xd8), integrationID(0xd9), 6, now.Add(time.Minute))
	_, err = repository.CreateRestoreDocumentProposal(ctx, stale)
	assertRestoreRepositoryError(t, err, foundation.ErrorVersionConflict, "DOCUMENT_RESTORE_STALE")
}

func seedRestoreDocumentOwner(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, documentID, articleRevisionID foundation.ID, now time.Time) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,'restore-document-owner',$2,$2,$3,'active',$3,$3)`,
		string(workspaceID), "/tmp/restore-document-owner-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
	) VALUES($1,$2,'notes/java-ai.md','Java AI','PUBLISHED',$3,7,$4,$4)`,
		string(documentID), string(workspaceID), string(articleRevisionID), now); err != nil {
		t.Fatal(err)
	}
	currentHash := domain.ComputeContentHash([]byte("current\n"))
	if _, err := tx.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,revision_no,content,content_hash,status,git_commit,optimization_mode,created_by_type,created_at
	) VALUES($1,$2,$3,1,$4,$5,'PUBLISHED',repeat('b',40),'NONE','SYSTEM',$6)`,
		string(articleRevisionID), string(workspaceID), string(documentID), "current\n", currentHash, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func restoreDocumentProposalFixture(t *testing.T, workspaceID, documentID, proposalID, revisionID foundation.ID, documentVersion int64, now time.Time) domain.Proposal {
	t.Helper()
	currentHash := domain.ComputeContentHash([]byte("current\n"))
	targetContent := "target\n"
	restore := domain.RestoreDocument{
		WorkspaceID: workspaceID, DocumentID: documentID,
		TargetCommit:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedHead:            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExpectedDocumentVersion: documentVersion, PreviewHash: domain.ComputeContentHash([]byte("preview")),
		CurrentContentHash: currentHash, TargetContentHash: domain.ComputeContentHash([]byte(targetContent)),
		SchemaVersion: domain.RestoreDocumentSchemaVersion,
	}
	changeHash, err := domain.ComputeChangeHashForTarget(workspaceID, "notes/java-ai.md", domain.TargetModeReplace, currentHash, targetContent)
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.Revision{
		ID: revisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: "notes/java-ai.md",
		TargetMode: domain.TargetModeReplace, BaseHash: currentHash, Content: targetContent,
		EvidenceSummary: "server verified history target", Risk: "newer information may be removed",
		RollbackPlan: "restore the previous HEAD through a new proposal", ChangeHash: changeHash,
		RestoreDocument: &restore, CreatedAt: now,
	}
	requestHash, err := domain.ComputeRestoreDocumentRequestHash(
		workspaceID, restore, revision.TargetPath, revision.Content, revision.EvidenceSummary,
		domain.ProposalRiskLevelHigh, revision.Risk, revision.RollbackPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, Type: domain.ProposalTypeRestoreDocument,
		RiskLevel: domain.ProposalRiskLevelHigh, TargetPath: revision.TargetPath,
		IdempotencyKey: "restore-document-integration-" + string(proposalID), RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now, Revision: revision,
	}
}

func assertRestoreRepositoryError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error=%v, want kind=%s code=%s", err, kind, code)
	}
}
