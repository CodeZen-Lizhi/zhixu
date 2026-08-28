//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const documentHistoryPlanFixtureRows = 5_000

func TestRepositoryReadsDocumentAndBatchMapsManagedAndExternalCommits(t *testing.T) {
	fixture := testdb.Require(t, testdb.Config{
		Availability: testdb.FailWhenUnavailable,
		MaxConns:     8,
	})
	pool := fixture.Pool()
	if pool == nil || pool.DB() == nil {
		t.Fatal("test database fixture did not provide a platform pool")
	}
	gormRoot, err := pool.GORM()
	if err != nil {
		t.Fatalf("resolve shared GORM root: %v", err)
	}

	legacyCounter := &documentHistoryStatementCounter{database: pool.DB()}
	legacy, err := NewRepository(legacyCounter)
	if err != nil {
		t.Fatal(err)
	}
	gormCounter := &documentHistoryGORMStatementCounter{}
	gormRepository, err := NewGORMRepository(gormRoot.Session(&gorm.Session{Logger: gormCounter}))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	workspaceID := documentHistoryIntegrationID(1)
	documentID := documentHistoryIntegrationID(2)
	nullableRevisionDocumentID := documentHistoryIntegrationID(9)
	articleRevisionID := documentHistoryIntegrationID(3)
	proposalID := documentHistoryIntegrationID(4)
	proposalRevisionID := documentHistoryIntegrationID(5)
	approvalID := documentHistoryIntegrationID(6)
	writebackID := documentHistoryIntegrationID(7)
	articleCommit := documentHistoryOID("article")
	proposalCommit := documentHistoryOID("proposal")
	externalCommit := documentHistoryOID("external")
	decidedAt := time.Date(2026, 8, 4, 12, 0, 0, 123456000, time.UTC)
	seedDocumentHistoryProjectionFacts(t, ctx, pool.DB(), documentHistoryProjectionFixture{
		workspaceID: workspaceID, documentID: documentID, articleRevisionID: articleRevisionID,
		proposalID: proposalID, proposalRevisionID: proposalRevisionID, approvalID: approvalID,
		writebackID: writebackID, articleCommit: articleCommit, proposalCommit: proposalCommit,
		decidedAt: decidedAt,
	})
	seedDocumentHistoryNullableRevision(t, ctx, pool.DB(), workspaceID, nullableRevisionDocumentID, decidedAt)
	otherWorkspaceID, planCommits := seedDocumentHistoryPlanFacts(t, ctx, pool.DB(), workspaceID, documentID, decidedAt)

	readers := []documentHistoryIntegrationReader{
		{name: "legacy", reader: legacy},
		{name: "gorm", reader: gormRepository},
	}
	documentsByReader := make(map[string][]application.DocumentSnapshot, len(readers))
	mappingsByReader := make(map[string][]domain.CommitMapping, len(readers))
	for _, candidate := range readers {
		candidate := candidate
		t.Run(candidate.name, func(t *testing.T) {
			document, err := candidate.reader.GetDocument(ctx, workspaceID, documentID)
			if err != nil {
				t.Fatal(err)
			}
			if document.ID != documentID || document.WorkspaceID != workspaceID || document.CanonicalPath != "notes/java-ai.md" ||
				document.Lifecycle != "PUBLISHED" || document.Version != 7 || document.CurrentPublishedRevisionID != articleRevisionID {
				t.Fatalf("document=%+v", document)
			}
			nullableDocument, err := candidate.reader.GetDocument(ctx, workspaceID, nullableRevisionDocumentID)
			if err != nil {
				t.Fatal(err)
			}
			if nullableDocument.CurrentPublishedRevisionID != "" {
				t.Fatalf("nullable current revision=%q", nullableDocument.CurrentPublishedRevisionID)
			}
			documentsByReader[candidate.name] = []application.DocumentSnapshot{document, nullableDocument}

			mappings, err := candidate.reader.MapCommits(ctx, workspaceID, documentID, document.CanonicalPath, []string{externalCommit, proposalCommit, articleCommit})
			if err != nil {
				t.Fatal(err)
			}
			assertDocumentHistoryMixedMappings(t, mappings, articleRevisionID, proposalID, proposalRevisionID, approvalID, writebackID, articleCommit, proposalCommit, decidedAt)
			mappingsByReader[candidate.name] = mappings

			crossWorkspace, err := candidate.reader.MapCommits(ctx, otherWorkspaceID, documentID, document.CanonicalPath, []string{articleCommit, proposalCommit})
			if err != nil || len(crossWorkspace) != 0 {
				t.Fatalf("cross-workspace mappings=%+v err=%v", crossWorkspace, err)
			}
			wrongPath, err := candidate.reader.MapCommits(ctx, workspaceID, documentID, "notes/not-java-ai.md", []string{proposalCommit})
			if err != nil || len(wrongPath) != 0 {
				t.Fatalf("wrong-path mappings=%+v err=%v", wrongPath, err)
			}
			if _, err := candidate.reader.MapCommits(ctx, workspaceID, documentID, document.CanonicalPath, []string{articleCommit, articleCommit}); err == nil {
				t.Fatal("duplicate commit request was accepted")
			}
			if _, err := candidate.reader.MapCommits(ctx, workspaceID, documentID, document.CanonicalPath, append(append([]string{}, planCommits...), documentHistoryOID("over-limit"))); err == nil {
				t.Fatal("more than 50 commit request was accepted")
			}
			if _, err := candidate.reader.MapCommits(ctx, workspaceID, documentID, document.CanonicalPath, []string{documentHistoryIntegrationHash("sha256")}); err != nil {
				t.Fatalf("SHA-256 object ID was rejected: %v", err)
			}
			if _, err := candidate.reader.MapCommits(ctx, workspaceID, documentID, document.CanonicalPath, []string{"ABCDEF"}); err == nil {
				t.Fatal("invalid object ID was accepted")
			}
			_, err = candidate.reader.GetDocument(ctx, workspaceID, documentHistoryIntegrationID(99))
			assertDocumentHistoryError(t, err, foundation.ErrorNotFound, application.ErrorCodeNotFound, false, nil)
			_, err = candidate.reader.GetDocument(ctx, otherWorkspaceID, documentID)
			assertDocumentHistoryError(t, err, foundation.ErrorNotFound, application.ErrorCodeNotFound, false, nil)

			cancelled, stop := context.WithCancel(context.Background())
			stop()
			_, err = candidate.reader.GetDocument(cancelled, workspaceID, documentID)
			assertDocumentHistoryError(t, err, foundation.ErrorNonRetryableFailure, "DOCUMENT_HISTORY_DOCUMENT_QUERY_FAILED", false, context.Canceled)
			deadline, stopDeadline := context.WithTimeout(context.Background(), 0)
			<-deadline.Done()
			stopDeadline()
			_, err = candidate.reader.GetDocument(deadline, workspaceID, documentID)
			assertDocumentHistoryError(t, err, foundation.ErrorRetryableFailure, "DOCUMENT_HISTORY_DOCUMENT_QUERY_FAILED", true, context.DeadlineExceeded)
		})
	}
	if !reflect.DeepEqual(documentsByReader["legacy"], documentsByReader["gorm"]) || !reflect.DeepEqual(mappingsByReader["legacy"], mappingsByReader["gorm"]) {
		t.Fatalf("legacy and GORM repository results diverged: legacy=%#v/%#v gorm=%#v/%#v", documentsByReader["legacy"], mappingsByReader["legacy"], documentsByReader["gorm"], mappingsByReader["gorm"])
	}

	legacyCounter.reset()
	legacyMappings, err := legacy.MapCommits(ctx, workspaceID, documentID, "notes/java-ai.md", planCommits)
	if err != nil || len(legacyMappings) != len(planCommits) {
		t.Fatalf("legacy plan mappings=%d err=%v", len(legacyMappings), err)
	}
	if rows, queries := legacyCounter.snapshot(); rows != 0 || queries != 1 {
		t.Fatalf("legacy MapCommits statements row=%d query=%d, want 0/1", rows, queries)
	}
	gormCounter.reset()
	gormMappings, err := gormRepository.MapCommits(ctx, workspaceID, documentID, "notes/java-ai.md", planCommits)
	if err != nil || len(gormMappings) != len(planCommits) {
		t.Fatalf("GORM plan mappings=%d err=%v", len(gormMappings), err)
	}
	if statements := gormCounter.snapshot(); statements != 1 {
		t.Fatalf("GORM MapCommits statements=%d, want 1", statements)
	}
	if !reflect.DeepEqual(legacyMappings, gormMappings) {
		t.Fatalf("legacy and GORM 50-commit mappings diverged: legacy=%#v gorm=%#v", legacyMappings, gormMappings)
	}
	assertDocumentHistoryPlanUsesIndexes(t, ctx, pool.DB(), workspaceID, documentID, "notes/java-ai.md", planCommits[:1])
}

type documentHistoryIntegrationReader struct {
	name   string
	reader application.DocumentReader
}

type documentHistoryStatementCounter struct {
	database  *pgxpool.Pool
	queryRows atomic.Int64
	queries   atomic.Int64
}

func (counter *documentHistoryStatementCounter) QueryRow(ctx context.Context, query string, arguments ...any) pgx.Row {
	counter.queryRows.Add(1)
	return counter.database.QueryRow(ctx, query, arguments...)
}

func (counter *documentHistoryStatementCounter) Query(ctx context.Context, query string, arguments ...any) (pgx.Rows, error) {
	counter.queries.Add(1)
	return counter.database.Query(ctx, query, arguments...)
}

func (counter *documentHistoryStatementCounter) reset() {
	counter.queryRows.Store(0)
	counter.queries.Store(0)
}

func (counter *documentHistoryStatementCounter) snapshot() (int64, int64) {
	return counter.queryRows.Load(), counter.queries.Load()
}

type documentHistoryGORMStatementCounter struct{ statements atomic.Int64 }

func (counter *documentHistoryGORMStatementCounter) LogMode(logger.LogLevel) logger.Interface {
	return counter
}

func (*documentHistoryGORMStatementCounter) Info(context.Context, string, ...interface{}) {}

func (*documentHistoryGORMStatementCounter) Warn(context.Context, string, ...interface{}) {}

func (*documentHistoryGORMStatementCounter) Error(context.Context, string, ...interface{}) {}

func (counter *documentHistoryGORMStatementCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.statements.Add(1)
}

func (counter *documentHistoryGORMStatementCounter) reset() { counter.statements.Store(0) }

func (counter *documentHistoryGORMStatementCounter) snapshot() int64 {
	return counter.statements.Load()
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

func seedDocumentHistoryNullableRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, documentID foundation.ID, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
	) VALUES($1,$2,'notes/without-revision.md','Without Revision','DRAFT',NULL,1,$3,$3)`, string(documentID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
}

func seedDocumentHistoryPlanFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, documentID foundation.ID, now time.Time) (foundation.ID, []string) {
	t.Helper()
	otherWorkspaceID := documentHistoryIntegrationID(20)
	documents := make([]struct {
		workspaceID foundation.ID
		documentID  foundation.ID
		path        string
	}, 0, 100)
	for index := 0; index < 100; index++ {
		candidate := struct {
			workspaceID foundation.ID
			documentID  foundation.ID
			path        string
		}{
			workspaceID: workspaceID,
			documentID:  documentHistoryIntegrationID(100 + index),
			path:        fmt.Sprintf("notes/plan-%03d.md", index),
		}
		if index%2 != 0 {
			candidate.workspaceID = otherWorkspaceID
		}
		if index == 0 {
			candidate.documentID = documentID
			candidate.path = "notes/java-ai.md"
		}
		documents = append(documents, candidate)
	}
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
	) VALUES($1,'document-history-other','/tmp/document-history-other','/tmp/document-history-other',$2,'inactive',$2,$2)`, string(otherWorkspaceID), now); err != nil {
		t.Fatal(err)
	}
	for _, document := range documents[1:] {
		if _, err := tx.Exec(ctx, `INSERT INTO core.document(
			id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
		) VALUES($1,$2,$3,'Plan Fixture','DRAFT',NULL,1,$4,$4)`, string(document.documentID), string(document.workspaceID), document.path, now); err != nil {
			t.Fatal(err)
		}
	}

	articleRows := make([][]any, 0, documentHistoryPlanFixtureRows)
	bindingRows := make([][]any, 0, documentHistoryPlanFixtureRows)
	proposalRows := make([][]any, 0, documentHistoryPlanFixtureRows)
	approvalRows := make([][]any, 0, documentHistoryPlanFixtureRows)
	proposalCommitRows := make([][]any, 0, documentHistoryPlanFixtureRows)
	planCommits := make([]string, 0, 50)
	contentHash := documentHistoryIntegrationHash("plan fixture")
	for index := 0; index < documentHistoryPlanFixtureRows; index++ {
		document := documents[index%len(documents)]
		commit := documentHistoryOID(fmt.Sprintf("plan-%d", index))
		if index%len(documents) == 0 && len(planCommits) < 50 {
			planCommits = append(planCommits, commit)
		}
		articleID := documentHistoryIntegrationID(10_000 + index)
		proposalID := documentHistoryIntegrationID(20_000 + index)
		proposalRevisionID := documentHistoryIntegrationID(30_000 + index)
		approvalID := documentHistoryIntegrationID(80_000 + index)
		articleRows = append(articleRows, []any{
			string(articleID), string(document.workspaceID), string(document.documentID), index/len(documents) + 10,
			"plan fixture", contentHash, "DRAFT", commit, "NONE", "SYSTEM", now,
		})
		bindingRows = append(bindingRows, []any{
			string(documentHistoryIntegrationID(40_000 + index)), string(documentHistoryIntegrationID(50_000 + index)),
			string(document.workspaceID), string(document.documentID), string(articleID), string(proposalID), string(proposalRevisionID),
			document.path, contentHash, "REPLACE", "", "PUBLISHED", commit, "", int64(1), now, now, now,
		})
		proposalRows = append(proposalRows, []any{
			string(proposalID), string(document.workspaceID), "restore_document", "HIGH", "completed",
			fmt.Sprintf("document-history-plan-%d", index), contentHash, int64(1), now, now,
		})
		approvalRows = append(approvalRows, []any{
			string(approvalID), string(proposalID), string(proposalRevisionID), contentHash, "approved", documentHistoryOID(fmt.Sprintf("approved-%d", index)), now,
		})
		proposalCommitRows = append(proposalCommitRows, []any{
			string(documentHistoryIntegrationID(60_000 + index)), string(document.workspaceID), string(documentHistoryIntegrationID(70_000 + index)),
			string(proposalID), string(proposalRevisionID), string(approvalID),
			commit, documentHistoryOID(fmt.Sprintf("parent-%d", index)), document.path, contentHash, contentHash, now,
		})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"core", "article_revision"}, []string{
		"id", "workspace_id", "document_id", "revision_no", "content", "content_hash", "status", "git_commit", "optimization_mode", "created_by_type", "created_at",
	}, pgx.CopyFromRows(articleRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"authoring", "document_publication_binding"}, []string{
		"id", "reservation_id", "workspace_id", "document_id", "article_revision_id", "proposal_id", "proposal_revision_id", "target_path", "content_hash", "target_mode", "absence_token", "status", "git_commit", "error_code", "version", "created_at", "updated_at", "published_at",
	}, pgx.CopyFromRows(bindingRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"change_control", "proposal"}, []string{
		"id", "workspace_id", "proposal_type", "risk_level", "status", "idempotency_key", "request_hash", "version", "created_at", "updated_at",
	}, pgx.CopyFromRows(proposalRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"change_control", "approval"}, []string{
		"id", "proposal_id", "revision_id", "change_hash", "decision", "approved_git_head", "decided_at",
	}, pgx.CopyFromRows(approvalRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"change_control", "proposal_commit"}, []string{
		"id", "workspace_id", "writeback_execution_id", "proposal_id", "revision_id", "approval_id", "git_commit", "parent_git_commit", "target_path", "diff_hash", "result_hash", "created_at",
	}, pgx.CopyFromRows(proposalCommitRows)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if len(planCommits) != 50 {
		t.Fatalf("plan commit count=%d, want 50", len(planCommits))
	}
	if _, err := pool.Exec(ctx, `ANALYZE core.article_revision; ANALYZE authoring.document_publication_binding; ANALYZE change_control.proposal; ANALYZE change_control.approval; ANALYZE change_control.proposal_commit`); err != nil {
		t.Fatal(err)
	}
	return otherWorkspaceID, planCommits
}

func assertDocumentHistoryMixedMappings(t *testing.T, mappings []domain.CommitMapping, articleRevisionID, proposalID, proposalRevisionID, approvalID, writebackID foundation.ID, articleCommit, proposalCommit string, decidedAt time.Time) {
	t.Helper()
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
}

func assertDocumentHistoryError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool, cause error) {
	t.Helper()
	var classified *foundation.Error
	if err == nil || !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("error=%v, want kind=%s code=%s retryable=%t", err, kind, code, retryable)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("error=%v does not preserve cause %v", err, cause)
	}
}

type documentHistoryExplainPlan struct {
	NodeType     string                       `json:"Node Type"`
	RelationName string                       `json:"Relation Name"`
	IndexName    string                       `json:"Index Name"`
	Plans        []documentHistoryExplainPlan `json:"Plans"`
}

func assertDocumentHistoryPlanUsesIndexes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, documentID foundation.ID, path string, commits []string) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, COSTS OFF) `+commitMappingSQL,
		string(workspaceID), string(documentID), path, commits).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan documentHistoryExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid document history explain: %v %s", err, raw)
	}
	indexes := make(map[string]bool, 2)
	sequentialScans := make(map[string]bool, 2)
	visitDocumentHistoryPlan(documents[0].Plan, func(plan documentHistoryExplainPlan) {
		if plan.IndexName != "" {
			indexes[plan.IndexName] = true
		}
		if plan.NodeType == "Seq Scan" && (plan.RelationName == "document_publication_binding" || plan.RelationName == "proposal_commit") {
			sequentialScans[plan.RelationName] = true
		}
	})
	if !indexes["idx_authoring_publication_history_git"] || sequentialScans["document_publication_binding"] {
		t.Fatalf("document history plan index=idx_authoring_publication_history_git used=%t seq_scan_document_publication_binding=%t plan=%s", indexes["idx_authoring_publication_history_git"], sequentialScans["document_publication_binding"], raw)
	}
	if !indexes["uq_proposal_commit_git"] || sequentialScans["proposal_commit"] {
		t.Fatalf("document history plan index=uq_proposal_commit_git used=%t seq_scan_proposal_commit=%t plan=%s", indexes["uq_proposal_commit_git"], sequentialScans["proposal_commit"], raw)
	}
}

func visitDocumentHistoryPlan(plan documentHistoryExplainPlan, visit func(documentHistoryExplainPlan)) {
	visit(plan)
	for _, child := range plan.Plans {
		visitDocumentHistoryPlan(child, visit)
	}
}

func documentHistoryIntegrationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("76000000-0000-4000-8000-%012d", value))
}

func documentHistoryIntegrationHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func documentHistoryOID(value string) string {
	return documentHistoryIntegrationHash(value)[:40]
}
