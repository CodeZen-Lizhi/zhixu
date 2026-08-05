//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryReadsDocumentAndBatchMapsManagedAndExternalCommits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newDocumentHistoryIntegrationDatabase(t, ctx)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := documentHistoryIntegrationID(1)
	documentID := documentHistoryIntegrationID(2)
	articleRevisionID := documentHistoryIntegrationID(3)
	proposalID := documentHistoryIntegrationID(4)
	proposalRevisionID := documentHistoryIntegrationID(5)
	approvalID := documentHistoryIntegrationID(6)
	writebackID := documentHistoryIntegrationID(7)
	articleCommit := strings.Repeat("a", 40)
	proposalCommit := strings.Repeat("b", 40)
	externalCommit := strings.Repeat("c", 40)
	decidedAt := time.Date(2026, 8, 4, 12, 0, 0, 123000000, time.UTC)
	seedDocumentHistoryProjectionFacts(t, ctx, pool, documentHistoryProjectionFixture{
		workspaceID: workspaceID, documentID: documentID, articleRevisionID: articleRevisionID,
		proposalID: proposalID, proposalRevisionID: proposalRevisionID, approvalID: approvalID,
		writebackID: writebackID, articleCommit: articleCommit, proposalCommit: proposalCommit,
		decidedAt: decidedAt,
	})

	document, err := repository.GetDocument(ctx, workspaceID, documentID)
	if err != nil {
		t.Fatal(err)
	}
	if document.ID != documentID || document.WorkspaceID != workspaceID || document.CanonicalPath != "notes/java-ai.md" ||
		document.Lifecycle != "PUBLISHED" || document.Version != 7 || document.CurrentPublishedRevisionID != articleRevisionID {
		t.Fatalf("document=%+v", document)
	}

	mappings, err := repository.MapCommits(ctx, workspaceID, documentID, "notes/java-ai.md", []string{externalCommit, proposalCommit, articleCommit})
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 2 || mappings[0].GitCommit != proposalCommit || mappings[1].GitCommit != articleCommit {
		t.Fatalf("mappings=%+v", mappings)
	}
	proposal := mappings[0]
	if proposal.ProposalID != proposalID || proposal.ProposalRevisionID != proposalRevisionID || proposal.ApprovalID != approvalID ||
		proposal.WritebackID != writebackID || proposal.WorkflowRunID != "" || proposal.ProposalType != "restore_document" ||
		proposal.ApprovalDecidedAt == nil || !proposal.ApprovalDecidedAt.Equal(decidedAt) {
		t.Fatalf("proposal mapping=%+v", proposal)
	}
	article := mappings[1]
	if article.ArticleRevisionID != articleRevisionID || article.ArticleRevisionNo != 3 || article.ProposalID != "" {
		t.Fatalf("article mapping=%+v", article)
	}

	otherWorkspace := documentHistoryIntegrationID(20)
	other, err := repository.MapCommits(ctx, otherWorkspace, documentID, "notes/java-ai.md", []string{articleCommit, proposalCommit})
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-workspace mappings=%+v err=%v", other, err)
	}
	if _, err := repository.MapCommits(ctx, workspaceID, documentID, "notes/java-ai.md", []string{articleCommit, articleCommit}); err == nil {
		t.Fatal("duplicate commit request was accepted")
	}
}

type documentHistoryProjectionFixture struct {
	workspaceID        foundation.ID
	documentID         foundation.ID
	articleRevisionID  foundation.ID
	proposalID         foundation.ID
	proposalRevisionID foundation.ID
	approvalID         foundation.ID
	writebackID        foundation.ID
	articleCommit      string
	proposalCommit     string
	decidedAt          time.Time
}

func seedDocumentHistoryProjectionFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture documentHistoryProjectionFixture) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	now := fixture.decidedAt.Add(-time.Hour)
	content := "# Java AI\n"
	contentHash := documentHistoryIntegrationHash(content)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,'document-history-projection','/tmp/document-history-projection','/tmp/document-history-projection',$2,'active',$2,$2)`, string(fixture.workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
	) VALUES($1,$2,'notes/java-ai.md','Java AI','PUBLISHED',$3,7,$4,$4)`,
		string(fixture.documentID), string(fixture.workspaceID), string(fixture.articleRevisionID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,revision_no,content,content_hash,status,git_commit,optimization_mode,created_by_type,created_at
	) VALUES($1,$2,$3,3,$4,$5,'PUBLISHED',$6,'NONE','SYSTEM',$7)`,
		string(fixture.articleRevisionID), string(fixture.workspaceID), string(fixture.documentID), content, contentHash, fixture.articleCommit, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,risk_level,status,idempotency_key,request_hash,version,created_at,updated_at
	) VALUES($1,$2,'restore_document','HIGH','completed','document-history-proposal',repeat('d',64),1,$3,$3)`,
		string(fixture.proposalID), string(fixture.workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.approval(
		id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at
	) VALUES($1,$2,$3,repeat('e',64),'approved',repeat('f',40),$4)`,
		string(fixture.approvalID), string(fixture.proposalID), string(fixture.proposalRevisionID), fixture.decidedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_commit(
		id,workspace_id,writeback_execution_id,proposal_id,revision_id,approval_id,
		git_commit,parent_git_commit,target_path,diff_hash,result_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,repeat('f',40),'notes/java-ai.md',repeat('1',64),repeat('2',64),$8)`,
		string(documentHistoryIntegrationID(8)), string(fixture.workspaceID), string(fixture.writebackID),
		string(fixture.proposalID), string(fixture.proposalRevisionID), string(fixture.approvalID),
		fixture.proposalCommit, fixture.decidedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func newDocumentHistoryIntegrationDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_document_history_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	runner, err := platformmigration.NewRunner(pool, projectmigrations.FS)
	if err == nil {
		err = runner.Up(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func documentHistoryIntegrationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("76000000-0000-4000-8000-%012d", value))
}

func documentHistoryIntegrationHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
