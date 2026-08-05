//go:build integration

package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDocumentFileHistoryMigrationFiltersRestoreTimelineAndRestoresProjectors(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 74); err != nil {
		t.Fatal(err)
	}
	before := documentHistoryProjectorDefinitions(t, ctx, pool)
	if _, err := provider.UpTo(ctx, 75); err != nil {
		t.Fatal(err)
	}
	var restorePublicationTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('authoring.document_restore_publication')::text`).Scan(&restorePublicationTable); err != nil || restorePublicationTable == nil {
		t.Fatalf("restore publication table=%v err=%v", restorePublicationTable, err)
	}
	var historyIndex *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('authoring.idx_authoring_publication_history_git')::text`).Scan(&historyIndex); err != nil || historyIndex == nil {
		t.Fatalf("history mapping index=%v err=%v", historyIndex, err)
	}

	const (
		workspaceID       = "75000000-0000-4000-8000-000000000001"
		documentID        = "75000000-0000-4000-8000-000000000002"
		articleRevisionID = "75000000-0000-4000-8000-000000000003"
		restoreProposalID = "75000000-0000-4000-8000-000000000010"
		restoreRevisionID = "75000000-0000-4000-8000-000000000011"
		wrongRevisionID   = "75000000-0000-4000-8000-000000000012"
		fileProposalID    = "75000000-0000-4000-8000-000000000020"
		fileRevisionID    = "75000000-0000-4000-8000-000000000021"
	)
	currentHash := documentHistorySHA256("current body")
	targetHash := documentHistorySHA256("restored body")
	seedDocumentHistoryOwners(t, ctx, pool, documentHistoryFixture{
		workspaceID: workspaceID, documentID: documentID, articleRevisionID: articleRevisionID,
		restoreProposalID: restoreProposalID, fileProposalID: fileProposalID, fileRevisionID: fileRevisionID,
		currentHash: currentHash,
	})

	insertRestoreRevision := func(revisionID, frozenTargetHash string) error {
		_, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,
			change_hash,schema_version,restore_workspace_id,restore_document_id,restore_target_commit,
			restore_expected_head,restore_expected_document_version,restore_preview_hash,
			restore_current_content_hash,restore_target_content_hash,created_at
		) VALUES($1,$2,1,'docs/history.md','REPLACE',$3,'restored body','restore evidence','HIGH','restore rollback',
			repeat('d',64),'document-restore/v1',$4,$5,repeat('1',40),repeat('2',40),1,repeat('c',64),$3,$6,now())`,
			revisionID, restoreProposalID, currentHash, workspaceID, documentID, frozenTargetHash)
		return err
	}
	if err := insertRestoreRevision(wrongRevisionID, strings.Repeat("f", 64)); !isPostgresCode(err, "23514") {
		t.Fatalf("restore revision with mismatched content hash error=%v", err)
	}
	if err := insertRestoreRevision(restoreRevisionID, targetHash); err != nil {
		t.Fatalf("valid restore revision insert failed: %v", err)
	}

	installDocumentHistoryProjectorFixtures(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO ops.document_history_proposal_event VALUES($1,$2,'restore_document',now())`, restoreProposalID, workspaceID); err != nil {
		t.Fatalf("project restore proposal: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.document_history_approval_event VALUES('75000000-0000-4000-8000-000000000013',$1,'approved',now())`, restoreProposalID); err != nil {
		t.Fatalf("project restore approval: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.document_history_commit_event VALUES(
		'75000000-0000-4000-8000-000000000014',$1,$2,$3,
		'75000000-0000-4000-8000-000000000013',repeat('3',40),now())`,
		workspaceID, restoreProposalID, restoreRevisionID); err != nil {
		t.Fatalf("project restore commit: %v", err)
	}
	documentHistoryAssertTimelineCount(t, ctx, pool, workspaceID, 0)

	const fileApprovalID = "75000000-0000-4000-8000-000000000023"
	const fileCommitID = "75000000-0000-4000-8000-000000000024"
	if _, err := pool.Exec(ctx, `INSERT INTO ops.document_history_proposal_event VALUES($1,$2,'file_patch',now())`, fileProposalID, workspaceID); err != nil {
		t.Fatalf("project file patch proposal: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.document_history_approval_event VALUES($1,$2,'approved',now())`, fileApprovalID, fileProposalID); err != nil {
		t.Fatalf("project file patch approval: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.document_history_commit_event VALUES($1,$2,$3,$4,$5,repeat('4',40),now())`,
		fileCommitID, workspaceID, fileProposalID, fileRevisionID, fileApprovalID); err != nil {
		t.Fatalf("project file patch commit: %v", err)
	}
	documentHistoryAssertTimelineCount(t, ctx, pool, workspaceID, 4)

	var refs []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(source_event_ref ORDER BY source_event_ref)
		FROM ops.timeline_projection_outbox WHERE workspace_id=$1`, workspaceID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	wantRefs := []string{
		"approval:" + fileApprovalID + ":v1",
		"proposal-commit:" + fileCommitID + ":v1",
		"proposal-revision-published:" + fileCommitID + ":v1",
		"proposal.created:" + fileProposalID + ":v1",
	}
	sort.Strings(refs)
	sort.Strings(wantRefs)
	if strings.Join(refs, "\n") != strings.Join(wantRefs, "\n") {
		t.Fatalf("file patch Timeline refs=%v want=%v", refs, wantRefs)
	}

	if _, err := provider.DownTo(ctx, 74); err == nil || !strings.Contains(err.Error(), "cannot downgrade document file history while restore proposals exist") {
		t.Fatalf("00075 guarded Down error=%v", err)
	}
	deleteDocumentHistoryRestoreFixture(t, ctx, pool, restoreProposalID)
	if _, err := provider.DownTo(ctx, 74); err != nil {
		t.Fatalf("00075 Down failed: %v", err)
	}
	after := documentHistoryProjectorDefinitions(t, ctx, pool)
	for name, definition := range before {
		if after[name] != definition {
			t.Fatalf("Document History dependency %s was not exactly restored by Down", name)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('authoring.document_restore_publication')::text`).Scan(&restorePublicationTable); err != nil || restorePublicationTable != nil {
		t.Fatalf("restore publication table after Down=%v err=%v", restorePublicationTable, err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('authoring.idx_authoring_publication_history_git')::text`).Scan(&historyIndex); err != nil || historyIndex != nil {
		t.Fatalf("history mapping index after Down=%v err=%v", historyIndex, err)
	}
}

type documentHistoryFixture struct {
	workspaceID       string
	documentID        string
	articleRevisionID string
	restoreProposalID string
	fileProposalID    string
	fileRevisionID    string
	currentHash       string
}

func seedDocumentHistoryOwners(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture documentHistoryFixture) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
		VALUES($1,'document-history','/tmp/document-history','/tmp/document-history',now(),'active',now(),now())`, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.document(id,workspace_id,canonical_path,title,lifecycle_status,version,created_at,updated_at)
		VALUES($1,$2,'docs/history.md','History','PUBLISHED',1,now(),now())`, fixture.documentID, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,revision_no,content,content_hash,status,git_commit,created_by_type,created_at
	) VALUES($1,$2,$3,1,'current body',$4,'PUBLISHED',repeat('2',40),'SYSTEM',now())`,
		fixture.articleRevisionID, fixture.workspaceID, fixture.documentID, fixture.currentHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.document SET current_published_revision_id=$1 WHERE id=$2`, fixture.articleRevisionID, fixture.documentID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,risk_level,status,idempotency_key,request_hash,version,created_at,updated_at
	) VALUES
		($1,$2,'restore_document','HIGH','ready_for_review','restore-history',repeat('a',64),1,now(),now()),
		($3,$2,'file_patch','LOW','ready_for_review','file-history',repeat('b',64),1,now(),now())`,
		fixture.restoreProposalID, fixture.workspaceID, fixture.fileProposalID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
	) VALUES($1,$2,1,'docs/history.md','REPLACE',repeat('e',64),'file body','file evidence','LOW','file rollback',repeat('f',64),now())`,
		fixture.fileRevisionID, fixture.fileProposalID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func installDocumentHistoryProjectorFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE ops.document_history_proposal_event(
			id uuid,workspace_id uuid,proposal_type text,created_at timestamptz
		);
		CREATE TRIGGER project_document_history_proposal
		AFTER INSERT ON ops.document_history_proposal_event
		FOR EACH ROW EXECUTE FUNCTION ops.project_proposal_timeline_source();
		CREATE TABLE ops.document_history_approval_event(
			id uuid,proposal_id uuid,decision text,decided_at timestamptz
		);
		CREATE TRIGGER project_document_history_approval
		AFTER INSERT ON ops.document_history_approval_event
		FOR EACH ROW EXECUTE FUNCTION ops.project_approval_timeline_source();
		CREATE TABLE ops.document_history_commit_event(
			id uuid,workspace_id uuid,proposal_id uuid,revision_id uuid,approval_id uuid,git_commit text,created_at timestamptz
		);
		CREATE TRIGGER project_document_history_commit
		AFTER INSERT ON ops.document_history_commit_event
		FOR EACH ROW EXECUTE FUNCTION ops.project_proposal_commit_timeline_source();`); err != nil {
		t.Fatal(err)
	}
}

func deleteDocumentHistoryRestoreFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proposalID string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM change_control.proposal_revision WHERE proposal_id=$1`, proposalID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM change_control.proposal WHERE id=$1`, proposalID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func documentHistoryProjectorDefinitions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	result := make(map[string]string, 6)
	for _, name := range []string{
		"ops.project_proposal_timeline_source()",
		"ops.project_approval_timeline_source()",
		"ops.project_proposal_commit_timeline_source()",
		"authoring.validate_article_revision_mutation()",
		"authoring.guard_document_publication_path()",
		"authoring.verify_publication_terminal_state()",
	} {
		var definition string
		if err := pool.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure)`, name).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		result[name] = definition
	}
	return result
}

func documentHistoryAssertTimelineCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox WHERE workspace_id=$1`, workspaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("Document History Timeline projection count=%d want=%d", count, want)
	}
}

func documentHistorySHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
