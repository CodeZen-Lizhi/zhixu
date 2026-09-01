//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authoringchangecontrol "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gormlogger "gorm.io/gorm/logger"
)

func TestRepositoryPostgreSQLWorkingDraftCASReplayFreezeAndWorkspaceScope(t *testing.T) {
	runAuthoringIntegrationVariants(t, testRepositoryPostgreSQLWorkingDraftCASReplayFreezeAndWorkspaceScope)
}

func testRepositoryPostgreSQLWorkingDraftCASReplayFreezeAndWorkspaceScope(t *testing.T, variant authoringIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := variant.open(t)
	statementCounter := installAuthoringStatementCounter(t, repository)
	workspaceA := authoringIntegrationID(1)
	workspaceB := authoringIntegrationID(2)
	seedAuthoringWorkspace(t, ctx, pool, workspaceA, "authoring-a")
	seedAuthoringWorkspace(t, ctx, pool, workspaceB, "authoring-b")
	now := time.Date(2026, 8, 3, 8, 0, 0, 123456789, time.UTC)
	service, err := authoringapp.NewService(authoringapp.Dependencies{
		Repository: repository, IDs: &authoringIntegrationIDs{next: 10}, Clock: foundation.FixedClock{Value: now},
		Proposals: authoringIntegrationProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: workspaceA, IdempotencyKey: "create-a"})
	if err != nil || created.Replayed || created.Draft.Version != 1 || created.Draft.Body != "" {
		t.Fatalf("create=%#v err=%v", created, err)
	}
	replayedCreate, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: workspaceA, IdempotencyKey: "create-a"})
	if err != nil || !replayedCreate.Replayed || replayedCreate.Draft != created.Draft {
		t.Fatalf("create replay=%#v err=%v", replayedCreate, err)
	}
	if _, err := service.GetWorkingDraft(ctx, workspaceB, created.Draft.ID); !authoringIntegrationError(err, foundation.ErrorNotFound, authoringapp.ErrorCodeNotFound) {
		t.Fatalf("cross-workspace get error=%v", err)
	}

	updated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: 1,
		Title: "Java AI", TargetPath: "notes//java-ai.md", Body: "# Java AI\n\nRAG", IdempotencyKey: "update-a-1",
	})
	if err != nil || updated.Replayed || updated.Draft.Version != 2 {
		t.Fatalf("update=%#v err=%v", updated, err)
	}
	replayedUpdate, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: 1,
		Title: "Java AI", TargetPath: "notes//java-ai.md", Body: "# Java AI\n\nRAG", IdempotencyKey: "update-a-1",
	})
	if err != nil || !replayedUpdate.Replayed || replayedUpdate.Draft != updated.Draft {
		t.Fatalf("update replay=%#v err=%v", replayedUpdate, err)
	}
	if _, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: 1,
		Title: "Java AI", TargetPath: "notes//java-ai.md", Body: "different", IdempotencyKey: "update-a-1",
	}); !authoringIntegrationError(err, foundation.ErrorVersionConflict, authoringapp.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same-key different request error=%v", err)
	}
	if _, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: 1,
		Title: "stale", TargetPath: "stale.md", Body: "stale", IdempotencyKey: "update-a-stale",
	}); !authoringIntegrationError(err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.article_revision WHERE workspace_id=$1`, string(workspaceA)).Scan(&revisions); err != nil || revisions != 0 {
		t.Fatalf("article revisions after autosave=%d err=%v", revisions, err)
	}

	frozen, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: 2, IdempotencyKey: "freeze-a-1",
	})
	if err != nil || frozen.Replayed || frozen.Draft.Version != 3 || frozen.Revision.RevisionNo != 1 ||
		frozen.Revision.ParentRevisionID != "" || frozen.Document.CanonicalPath != "notes/java-ai.md" {
		t.Fatalf("first freeze=%#v err=%v", frozen, err)
	}
	replayedFreeze, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: 2, IdempotencyKey: "freeze-a-1",
	})
	if err != nil || !replayedFreeze.Replayed || replayedFreeze != (authoringapp.FreezeResult{
		Draft: frozen.Draft, Document: frozen.Document, Revision: frozen.Revision, Replayed: true,
	}) {
		t.Fatalf("freeze replay=%#v err=%v", replayedFreeze, err)
	}
	statementCounter.reset()
	searchHits, err := service.SearchArticleRevisions(ctx, authoringapp.ArticleRevisionSearchQuery{WorkspaceID: workspaceA, Query: "Java", Limit: 5})
	if err != nil || len(searchHits) != 1 || searchHits[0].Document.ID != frozen.Document.ID || searchHits[0].RevisionID != frozen.Revision.ID {
		t.Fatalf("article revision search=%#v err=%v", searchHits, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "article revision search")
	statementCounter.reset()
	otherWorkspaceHits, err := service.SearchArticleRevisions(ctx, authoringapp.ArticleRevisionSearchQuery{WorkspaceID: workspaceB, Query: "Java", Limit: 5})
	if err != nil || len(otherWorkspaceHits) != 0 {
		t.Fatalf("cross-workspace article revision search=%#v err=%v", otherWorkspaceHits, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "cross-workspace article revision search")

	secondUpdate, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: 3,
		Title: "Java AI v2", TargetPath: "notes/java-ai-v2.md", Body: frozen.Revision.Content + "\n\nAgent", IdempotencyKey: "update-a-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondFreeze, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceA, DraftID: created.Draft.ID, ExpectedVersion: secondUpdate.Draft.Version, IdempotencyKey: "freeze-a-2",
	})
	if err != nil || secondFreeze.Revision.RevisionNo != 2 || secondFreeze.Revision.ParentRevisionID != frozen.Revision.ID ||
		secondFreeze.Document.ID != frozen.Document.ID || secondFreeze.Document.Version != 2 {
		t.Fatalf("second freeze=%#v err=%v", secondFreeze, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.article_revision WHERE workspace_id=$1 AND document_id=$2`,
		string(workspaceA), string(frozen.Document.ID)).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("article revisions after explicit freezes=%d err=%v", revisions, err)
	}

	batchQuery := authoringapp.ArticleRevisionBatchQuery{
		WorkspaceID: workspaceA,
		Items: []authoringapp.ArticleRevisionIdentity{
			{DocumentID: secondFreeze.Document.ID, RevisionID: secondFreeze.Revision.ID},
			{DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID},
		},
	}
	statementCounter.reset()
	snapshots, err := service.GetArticleRevisions(ctx, batchQuery)
	if err != nil || len(snapshots) != 2 || snapshots[0].Document.ID != secondFreeze.Document.ID ||
		snapshots[0].Revision.ID != secondFreeze.Revision.ID || snapshots[0].Revision.Content != secondFreeze.Revision.Content ||
		snapshots[1].Document.ID != frozen.Document.ID || snapshots[1].Revision.ID != frozen.Revision.ID ||
		snapshots[1].Revision.ContentHash != frozen.Revision.ContentHash {
		t.Fatalf("article revision batch=%#v err=%v", snapshots, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "article revision batch")

	statementCounter.reset()
	missingSnapshots, err := service.GetArticleRevisions(ctx, authoringapp.ArticleRevisionBatchQuery{
		WorkspaceID: workspaceA,
		Items: []authoringapp.ArticleRevisionIdentity{
			{DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID},
			{DocumentID: frozen.Document.ID, RevisionID: authoringIntegrationID(9999)},
		},
	})
	if !authoringIntegrationError(err, foundation.ErrorNotFound, authoringapp.ErrorCodeNotFound) || len(missingSnapshots) != 0 {
		t.Fatalf("missing article revision batch=%#v err=%v", missingSnapshots, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "missing article revision batch")

	statementCounter.reset()
	duplicateSnapshots, err := service.GetArticleRevisions(ctx, authoringapp.ArticleRevisionBatchQuery{
		WorkspaceID: workspaceA,
		Items: []authoringapp.ArticleRevisionIdentity{
			{DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID},
			{DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID},
		},
	})
	if !authoringIntegrationError(err, foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid) || len(duplicateSnapshots) != 0 {
		t.Fatalf("duplicate article revision batch=%#v err=%v", duplicateSnapshots, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 0, "duplicate article revision validation")

	statementCounter.reset()
	detail, err := repository.GetDocumentDetail(ctx, workspaceA, frozen.Document.ID)
	if err != nil || detail.Document.ID != frozen.Document.ID || detail.CurrentRevision == nil ||
		detail.CurrentRevision.ID != secondFreeze.Revision.ID || detail.Publication != nil {
		t.Fatalf("document detail=%#v err=%v", detail, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 3, "document detail snapshot")

	statementCounter.reset()
	overview, err := repository.GetOverview(ctx, workspaceA, 10)
	if err != nil || overview.WorkspaceID != workspaceA || len(overview.RecentDrafts) != 1 ||
		overview.RecentDrafts[0].ID != created.Draft.ID || len(overview.PendingPublications) != 0 ||
		len(overview.CompletedDocuments) != 0 {
		t.Fatalf("authoring overview=%#v err=%v", overview, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 3, "authoring overview snapshot")

	if _, err := pool.Exec(ctx, `UPDATE core.article_revision SET content='mutated' WHERE id=$1`, string(frozen.Revision.ID)); err == nil {
		t.Fatal("immutable article revision content was updated")
	} else {
		authoringIntegrationPostgresCode(t, err, "55000")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO authoring.working_draft(
		id,workspace_id,document_id,title,target_path,body,status,version,created_at,updated_at
	) VALUES($1,$2,$3,'cross','cross.md','body','EDITING',1,$4,$4)`,
		string(authoringIntegrationID(90)), string(workspaceB), string(frozen.Document.ID), now); err == nil {
		t.Fatal("cross-workspace Document binding was inserted")
	} else {
		authoringIntegrationPostgresCode(t, err, "23503")
	}
	otherDocumentID := authoringIntegrationID(91)
	if _, err := pool.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,version,created_at,updated_at
	) VALUES($1,$2,'other.md','other','DRAFT',1,$3,$3)`, string(otherDocumentID), string(workspaceA), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,parent_revision_id,revision_no,content,content_hash,status,
		optimization_mode,created_by_type,created_at
	) VALUES($1,$2,$3,$4,2,'invalid parent',repeat('a',64),'DRAFT','NONE','USER',$5)`,
		string(authoringIntegrationID(92)), string(workspaceA), string(otherDocumentID), string(frozen.Revision.ID), now); err == nil {
		t.Fatal("cross-document parent revision was inserted")
	} else {
		authoringIntegrationPostgresCode(t, err, "23503")
	}

	statementCounter.reset()
	page, err := service.ListWorkingDrafts(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.Draft.ID {
		t.Fatalf("draft page=%#v err=%v", page, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "working draft list")
}

func TestRepositoryPostgreSQLListsWorkingAndDocumentDraftsByWorkspaceKeyset(t *testing.T) {
	runAuthoringIntegrationVariants(t, testRepositoryPostgreSQLListsWorkingAndDocumentDraftsByWorkspaceKeyset)
}

func testRepositoryPostgreSQLListsWorkingAndDocumentDraftsByWorkspaceKeyset(t *testing.T, variant authoringIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := variant.open(t)
	statementCounter := installAuthoringStatementCounter(t, repository)
	workspaceA := authoringIntegrationID(930)
	workspaceB := authoringIntegrationID(931)
	seedAuthoringWorkspace(t, ctx, pool, workspaceA, "authoring-list-a")
	seedAuthoringWorkspace(t, ctx, pool, workspaceB, "authoring-list-b")
	base := time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC)

	workingNewest := authoringIntegrationID(934)
	workingTieHigh := authoringIntegrationID(933)
	workingTieLow := authoringIntegrationID(932)
	seedAuthoringListWorkingDraft(t, ctx, pool, workspaceA, workingTieLow, "working-low", base)
	seedAuthoringListWorkingDraft(t, ctx, pool, workspaceA, workingTieHigh, "working-high", base)
	seedAuthoringListWorkingDraft(t, ctx, pool, workspaceA, workingNewest, "working-newest", base.Add(time.Second))
	seedAuthoringListWorkingDraft(t, ctx, pool, workspaceB, authoringIntegrationID(935), "working-other", base.Add(2*time.Second))

	statementCounter.reset()
	workingFirst, err := repository.List(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 2})
	if err != nil || len(workingFirst.Items) != 2 || workingFirst.Next == nil ||
		workingFirst.Items[0].ID != workingNewest || workingFirst.Items[1].ID != workingTieHigh {
		t.Fatalf("working first page=%#v err=%v", workingFirst, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "working draft first page")
	statementCounter.reset()
	workingSecond, err := repository.List(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 2, After: workingFirst.Next})
	if err != nil || len(workingSecond.Items) != 1 || workingSecond.Next != nil || workingSecond.Items[0].ID != workingTieLow {
		t.Fatalf("working second page=%#v err=%v", workingSecond, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "working draft second page")
	statementCounter.reset()
	workingAfterLast, err := repository.List(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 2,
		After: &authoringapp.Cursor{UpdatedAt: base, ID: workingTieLow}})
	if err != nil || len(workingAfterLast.Items) != 0 || workingAfterLast.Next != nil {
		t.Fatalf("working page after last=%#v err=%v", workingAfterLast, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "working draft terminal page")

	documentNewest := authoringIntegrationID(938)
	documentTieHigh := authoringIntegrationID(937)
	documentTieLow := authoringIntegrationID(936)
	seedAuthoringListDocument(t, ctx, pool, workspaceA, documentTieLow, "document-low", base)
	seedAuthoringListDocument(t, ctx, pool, workspaceA, documentTieHigh, "document-high", base)
	seedAuthoringListDocument(t, ctx, pool, workspaceA, documentNewest, "document-newest", base.Add(time.Second))
	seedAuthoringListDocument(t, ctx, pool, workspaceB, authoringIntegrationID(939), "document-other", base.Add(2*time.Second))

	statementCounter.reset()
	documentFirst, err := repository.ListDocuments(ctx, authoringapp.DocumentListQuery{WorkspaceID: workspaceA, Limit: 2})
	if err != nil || len(documentFirst.Items) != 2 || documentFirst.Next == nil ||
		documentFirst.Items[0].ID != documentNewest || documentFirst.Items[1].ID != documentTieHigh {
		t.Fatalf("document first page=%#v err=%v", documentFirst, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "document first page")
	statementCounter.reset()
	documentSecond, err := repository.ListDocuments(ctx, authoringapp.DocumentListQuery{WorkspaceID: workspaceA, Limit: 2, After: documentFirst.Next})
	if err != nil || len(documentSecond.Items) != 1 || documentSecond.Next != nil || documentSecond.Items[0].ID != documentTieLow {
		t.Fatalf("document second page=%#v err=%v", documentSecond, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "document second page")
	statementCounter.reset()
	documentAfterLast, err := repository.ListDocuments(ctx, authoringapp.DocumentListQuery{WorkspaceID: workspaceA, Limit: 2,
		After: &authoringapp.DocumentCursor{UpdatedAt: base, ID: documentTieLow}})
	if err != nil || len(documentAfterLast.Items) != 0 || documentAfterLast.Next != nil {
		t.Fatalf("document page after last=%#v err=%v", documentAfterLast, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "document terminal page")

	assertAuthoringListCancellationAndPoolReuse(t, ctx, variant, repository, pool, workspaceA)

	corruptDocumentID := authoringIntegrationID(940)
	if _, err := pool.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
	) VALUES($1,$2,'notes//corrupt.md','corrupt','DRAFT',NULL,1,$3,$3)`,
		string(corruptDocumentID), string(workspaceA), base.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	statementCounter.reset()
	corruptPage, err := repository.ListDocuments(ctx, authoringapp.DocumentListQuery{WorkspaceID: workspaceA, Limit: 10})
	if !authoringIntegrationError(err, foundation.ErrorConsistencyViolation, authoringapp.ErrorCodeResultInvalid) ||
		len(corruptPage.Items) != 0 || corruptPage.Next != nil {
		t.Fatalf("corrupt document page=%#v err=%v", corruptPage, err)
	}
	assertAuthoringStatementCount(t, statementCounter, 1, "corrupt document page")
}

func TestRepositoryPostgreSQLPathConflictAndConcurrentCommandSerialization(t *testing.T) {
	runAuthoringIntegrationVariants(t, testRepositoryPostgreSQLPathConflictAndConcurrentCommandSerialization)
}

func testRepositoryPostgreSQLPathConflictAndConcurrentCommandSerialization(t *testing.T, variant authoringIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := variant.open(t)
	workspaceID := authoringIntegrationID(100)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-concurrency")
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	ids := &authoringIntegrationIDs{next: 110}
	service, err := authoringapp.NewService(authoringapp.Dependencies{
		Repository: repository, IDs: ids, Clock: foundation.FixedClock{Value: now},
		Proposals: authoringIntegrationProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}

	const attempts = 8
	type createOutcome struct {
		result authoringapp.CreateResult
		err    error
	}
	created := make(chan createOutcome, attempts)
	var group sync.WaitGroup
	group.Add(attempts)
	for range attempts {
		go func() {
			defer group.Done()
			result, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: "concurrent-create"})
			created <- createOutcome{result: result, err: err}
		}()
	}
	group.Wait()
	close(created)
	originals := 0
	var winner domain.WorkingDraft
	for outcome := range created {
		if outcome.err != nil {
			t.Fatalf("concurrent create error=%v", outcome.err)
		}
		if !outcome.result.Replayed {
			originals++
			winner = outcome.result.Draft
		}
	}
	if originals != 1 {
		t.Fatalf("non-replayed creates=%d", originals)
	}
	var drafts, commands int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authoring.working_draft WHERE workspace_id=$1`, string(workspaceID)).Scan(&drafts); err != nil || drafts != 1 {
		t.Fatalf("draft count=%d err=%v", drafts, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authoring.working_draft_command WHERE workspace_id=$1`, string(workspaceID)).Scan(&commands); err != nil || commands != 1 {
		t.Fatalf("command count=%d err=%v", commands, err)
	}

	updated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: winner.ID, ExpectedVersion: 1,
		Title: "winner", TargetPath: "notes/winner.md", Body: "winner", IdempotencyKey: "winner-update",
	})
	if err != nil {
		t.Fatal(err)
	}
	type freezeOutcome struct {
		result authoringapp.FreezeResult
		err    error
	}
	freezes := make(chan freezeOutcome, 2)
	group.Add(2)
	for _, key := range []string{"freeze-race-a", "freeze-race-b"} {
		key := key
		go func() {
			defer group.Done()
			result, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
				WorkspaceID: workspaceID, DraftID: winner.ID, ExpectedVersion: updated.Draft.Version, IdempotencyKey: key,
			})
			freezes <- freezeOutcome{result: result, err: err}
		}()
	}
	group.Wait()
	close(freezes)
	successes, stale := 0, 0
	var winningFreeze authoringapp.FreezeResult
	for outcome := range freezes {
		if outcome.err == nil {
			successes++
			winningFreeze = outcome.result
		} else if authoringIntegrationError(outcome.err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict) {
			stale++
		} else {
			t.Fatalf("unexpected freeze race error=%v", outcome.err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("freeze race successes=%d stale=%d", successes, stale)
	}

	other, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: "other-create"})
	if err != nil {
		t.Fatal(err)
	}
	otherUpdated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: other.Draft.ID, ExpectedVersion: 1,
		Title: "other", TargetPath: winningFreeze.Document.CanonicalPath, Body: "other", IdempotencyKey: "other-update",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: other.Draft.ID, ExpectedVersion: otherUpdated.Draft.Version, IdempotencyKey: "other-freeze",
	}); !authoringIntegrationError(err, foundation.ErrorVersionConflict, authoringapp.ErrorCodePathConflict) {
		t.Fatalf("occupied path freeze error=%v", err)
	}
}

func TestRepositoryPostgreSQLTerminalProposalClosesPublicationAndReleasesDocument(t *testing.T) {
	runAuthoringIntegrationVariants(t, testRepositoryPostgreSQLTerminalProposalClosesPublicationAndReleasesDocument)
}

func testRepositoryPostgreSQLTerminalProposalClosesPublicationAndReleasesDocument(t *testing.T, variant authoringIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := variant.open(t)
	workspaceID := authoringIntegrationID(300)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-publication-terminal")
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	targets := &authoringIntegrationTargetReader{}
	service := newAuthoringPublicationIntegrationService(t, repository, pool, targets, now, 310, 410)

	draft, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{
		WorkspaceID: workspaceID, IdempotencyKey: "terminal-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: draft.Draft.Version,
		Title: "Terminal Proposal", TargetPath: "notes/terminal.md", Body: "# First\n", IdempotencyKey: "terminal-update-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: updated.Draft.Version,
		IdempotencyKey: "terminal-freeze-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	for index, proposalStatus := range []string{"rejected", "needs_revision"} {
		publication, publishErr := service.PublishArticleRevision(ctx, authoringapp.PublishCommand{
			WorkspaceID: workspaceID, DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID,
			IdempotencyKey: fmt.Sprintf("terminal-publish-%d", index+1),
		})
		if publishErr != nil || publication.Publication.Status != domain.PublicationPending {
			t.Fatalf("publish status=%q result=%#v err=%v", proposalStatus, publication, publishErr)
		}
		if _, updateErr := pool.Exec(ctx, `UPDATE authoring.document_publication_binding
			SET status='RECOVERY_REQUIRED',git_commit=$1,error_code='AUTHORING_FAKE_RECOVERY',
				version=version+1,updated_at=updated_at+interval '1 microsecond'
			WHERE id=$2 AND workspace_id=$3`, strings.Repeat("a", 40),
			string(publication.Publication.ID), string(workspaceID)); updateErr == nil {
			t.Fatalf("recovery publication %d accepted a Git commit", index+1)
		} else {
			authoringIntegrationPostgresCode(t, updateErr, "23514")
		}
		if _, updateErr := pool.Exec(ctx, `UPDATE core.document
			SET canonical_path=$1,version=version+1,updated_at=updated_at+interval '1 microsecond'
			WHERE id=$2 AND workspace_id=$3`,
			fmt.Sprintf("notes/drift-%d.md", index), string(frozen.Document.ID), string(workspaceID)); updateErr == nil {
			t.Fatalf("canonical path changed while publication %d was pending", index+1)
		} else {
			authoringIntegrationPostgresCode(t, updateErr, "23514")
		}
		if _, updateErr := pool.Exec(ctx, `UPDATE change_control.proposal
			SET status=$1,version=version+1,updated_at=updated_at+interval '1 microsecond'
			WHERE id=$2 AND workspace_id=$3`, proposalStatus, string(publication.Publication.ProposalID), string(workspaceID)); updateErr != nil {
			t.Fatal(updateErr)
		}
		detail, detailErr := service.GetDocumentDetail(ctx, workspaceID, frozen.Document.ID)
		if detailErr != nil || detail.Publication == nil || detail.Publication.Status != domain.PublicationClosed ||
			detail.Publication.ErrorCode == "" {
			t.Fatalf("terminal detail status=%q detail=%#v err=%v", proposalStatus, detail, detailErr)
		}
		overview, overviewErr := service.GetOverview(ctx, workspaceID)
		if overviewErr != nil || len(overview.PendingPublications) != 0 {
			t.Fatalf("terminal overview status=%q overview=%#v err=%v", proposalStatus, overview, overviewErr)
		}

		next, updateErr := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
			WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: frozen.Draft.Version,
			Title: "Terminal Proposal", TargetPath: fmt.Sprintf("notes/terminal-%d.md", index+2),
			Body: fmt.Sprintf("# Revision %d\n", index+2), IdempotencyKey: fmt.Sprintf("terminal-update-%d", index+2),
		})
		if updateErr != nil {
			t.Fatal(updateErr)
		}
		frozen, err = service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
			WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: next.Draft.Version,
			IdempotencyKey: fmt.Sprintf("terminal-freeze-%d", index+2),
		})
		if err != nil {
			t.Fatalf("freeze after terminal status=%q: %v", proposalStatus, err)
		}
	}

	third, err := service.PublishArticleRevision(ctx, authoringapp.PublishCommand{
		WorkspaceID: workspaceID, DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID,
		IdempotencyKey: "terminal-publish-3",
	})
	if err != nil || third.Publication.Status != domain.PublicationPending {
		t.Fatalf("third publish after terminal releases=%#v err=%v", third, err)
	}
	if calls := targets.AbsenceChecks(); calls != 3 {
		t.Fatalf("CREATE_ONLY absence checks=%d, want 3", calls)
	}
}

func TestRepositoryPostgreSQLCreateOnlyProvesAbsenceBeforeProposalPersistence(t *testing.T) {
	runAuthoringIntegrationVariants(t, testRepositoryPostgreSQLCreateOnlyProvesAbsenceBeforeProposalPersistence)
}

func testRepositoryPostgreSQLCreateOnlyProvesAbsenceBeforeProposalPersistence(t *testing.T, variant authoringIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := variant.open(t)
	workspaceID := authoringIntegrationID(500)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-create-only-proof")
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	absenceErr := errors.New("target exists")
	targets := &authoringIntegrationTargetReader{absenceErr: absenceErr}
	service := newAuthoringPublicationIntegrationService(t, repository, pool, targets, now, 510, 610)

	draft, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{
		WorkspaceID: workspaceID, IdempotencyKey: "proof-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: draft.Draft.Version,
		Title: "Absence Proof", TargetPath: "notes/new.md", Body: "# New\n", IdempotencyKey: "proof-update",
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: updated.Draft.Version,
		IdempotencyKey: "proof-freeze",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PublishArticleRevision(ctx, authoringapp.PublishCommand{
		WorkspaceID: workspaceID, DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID,
		IdempotencyKey: "proof-publish",
	})
	if !errors.Is(err, absenceErr) {
		t.Fatalf("publish error=%v, want target absence failure", err)
	}
	if calls := targets.AbsenceChecks(); calls != 1 {
		t.Fatalf("CREATE_ONLY absence checks=%d, want 1", calls)
	}
	var proposals int
	if queryErr := pool.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1`, string(workspaceID)).Scan(&proposals); queryErr != nil {
		t.Fatal(queryErr)
	}
	if proposals != 0 {
		t.Fatalf("proposals persisted after failed absence proof=%d", proposals)
	}
}

func TestRepositoryPostgreSQLAbandonsDeterministicPreProposalFailure(t *testing.T) {
	runAuthoringIntegrationVariants(t, testRepositoryPostgreSQLAbandonsDeterministicPreProposalFailure)
}

func testRepositoryPostgreSQLAbandonsDeterministicPreProposalFailure(t *testing.T, variant authoringIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := variant.open(t)
	workspaceID := authoringIntegrationID(540)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-abandoned-reservation")
	now := time.Date(2026, 8, 3, 12, 30, 0, 0, time.UTC)
	targets := &authoringIntegrationTargetReader{
		absenceErr: foundation.NewError(foundation.ErrorNotFound, authoringapp.ErrorCodePublicationTargetParentNotFound, false, errors.New("missing parent")),
	}
	service := newAuthoringPublicationIntegrationService(t, repository, pool, targets, now, 550, 650)

	draft, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: "abandon-create"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: draft.Draft.Version,
		Title: "Missing parent", TargetPath: "missing/article.md", Body: "# Missing parent\n", IdempotencyKey: "abandon-update-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: updated.Draft.Version, IdempotencyKey: "abandon-freeze-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	publish := authoringapp.PublishCommand{
		WorkspaceID: workspaceID, DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID, IdempotencyKey: "abandon-publish-1",
	}
	if _, err := service.PublishArticleRevision(ctx, publish); !authoringIntegrationError(err, foundation.ErrorNotFound, authoringapp.ErrorCodePublicationTargetParentNotFound) {
		t.Fatalf("deterministic publish error=%v", err)
	}
	var reservationID, reservationStatus, errorCode string
	var bindingCount, proposalCount int
	if err := pool.QueryRow(ctx, `SELECT id::text,status,error_code
		FROM authoring.document_publication_reservation WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(workspaceID), publish.IdempotencyKey).Scan(&reservationID, &reservationStatus, &errorCode); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authoring.document_publication_binding WHERE reservation_id=$1`, reservationID).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1`, string(workspaceID)).Scan(&proposalCount); err != nil {
		t.Fatal(err)
	}
	if reservationStatus != string(authoringapp.PublicationReservationAbandoned) || errorCode != authoringapp.ErrorCodePublicationTargetParentNotFound || bindingCount != 0 || proposalCount != 0 {
		t.Fatalf("abandoned reservation=%s/%s bindings=%d proposals=%d", reservationStatus, errorCode, bindingCount, proposalCount)
	}
	if calls := targets.AbsenceChecks(); calls != 1 {
		t.Fatalf("first publish absence checks=%d", calls)
	}
	if _, err := service.PublishArticleRevision(ctx, publish); !authoringIntegrationError(err, foundation.ErrorNotFound, authoringapp.ErrorCodePublicationTargetParentNotFound) {
		t.Fatalf("abandoned replay error=%v", err)
	}
	if calls := targets.AbsenceChecks(); calls != 1 {
		t.Fatalf("abandoned replay retried proposal creation: checks=%d", calls)
	}

	if _, err := pool.Exec(ctx, `UPDATE authoring.document_publication_reservation
		SET status='PENDING',error_code='',abandoned_at=NULL
		WHERE id=$1`, reservationID); err == nil {
		t.Fatal("database revived an abandoned publication reservation")
	} else {
		authoringIntegrationPostgresCode(t, err, "23514")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM authoring.document_publication_reservation WHERE id=$1`, reservationID); err == nil {
		t.Fatal("database deleted an abandoned publication reservation")
	} else {
		authoringIntegrationPostgresCode(t, err, "23514")
	}

	targets.absenceErr = nil
	updated, err = service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: frozen.Draft.Version,
		Title: "Corrected parent", TargetPath: "notes/article.md", Body: "# Corrected parent\n", IdempotencyKey: "abandon-update-2",
	})
	if err != nil {
		t.Fatalf("update after abandonment: %v", err)
	}
	frozen, err = service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: updated.Draft.Version, IdempotencyKey: "abandon-freeze-2",
	})
	if err != nil {
		t.Fatalf("freeze after abandonment: %v", err)
	}
	published, err := service.PublishArticleRevision(ctx, authoringapp.PublishCommand{
		WorkspaceID: workspaceID, DocumentID: frozen.Document.ID, RevisionID: frozen.Revision.ID, IdempotencyKey: "abandon-publish-2",
	})
	if err != nil || published.Publication.Status != domain.PublicationPending {
		t.Fatalf("publish after corrected path=%#v err=%v", published, err)
	}

	retryDraft, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: workspaceID, IdempotencyKey: "retry-create"})
	if err != nil {
		t.Fatal(err)
	}
	retryUpdated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: retryDraft.Draft.ID, ExpectedVersion: retryDraft.Draft.Version,
		Title: "Retry", TargetPath: "retry/article.md", Body: "# Retry\n", IdempotencyKey: "retry-update",
	})
	if err != nil {
		t.Fatal(err)
	}
	retryFrozen, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: retryDraft.Draft.ID, ExpectedVersion: retryUpdated.Draft.Version, IdempotencyKey: "retry-freeze",
	})
	if err != nil {
		t.Fatal(err)
	}
	targets.absenceErr = foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_PARENT_READ_FAILED", true, errors.New("temporary parent read failure"))
	retryPublish := authoringapp.PublishCommand{
		WorkspaceID: workspaceID, DocumentID: retryFrozen.Document.ID, RevisionID: retryFrozen.Revision.ID, IdempotencyKey: "retry-publish",
	}
	if _, err := service.PublishArticleRevision(ctx, retryPublish); !authoringIntegrationError(err, foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_PARENT_READ_FAILED") {
		t.Fatalf("retryable publish error=%v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM authoring.document_publication_reservation
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), retryPublish.IdempotencyKey).Scan(&reservationStatus); err != nil {
		t.Fatal(err)
	}
	if reservationStatus != string(authoringapp.PublicationReservationPending) {
		t.Fatalf("retryable failure changed reservation to %s", reservationStatus)
	}
}

func TestRepositoryPostgreSQLPublishedDocumentPathCannotDrift(t *testing.T) {
	runAuthoringIntegrationVariants(t, testRepositoryPostgreSQLPublishedDocumentPathCannotDrift)
}

func testRepositoryPostgreSQLPublishedDocumentPathCannotDrift(t *testing.T, variant authoringIntegrationVariant) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := variant.open(t)
	workspaceID := authoringIntegrationID(700)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-published-path")
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP - INTERVAL '1 hour'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Microsecond)
	service, err := authoringapp.NewService(authoringapp.Dependencies{
		Repository: repository, IDs: &authoringIntegrationIDs{next: 710}, Clock: foundation.FixedClock{Value: now},
		Proposals: authoringIntegrationProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{
		WorkspaceID: workspaceID, IdempotencyKey: "published-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: draft.Draft.Version,
		Title: "Published", TargetPath: "notes/published.md", Body: "# Published\n", IdempotencyKey: "published-update",
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{
		WorkspaceID: workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: updated.Draft.Version,
		IdempotencyKey: "published-freeze",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.document
		SET canonical_path='notes/simultaneous-drift.md',lifecycle_status='PUBLISHED',
			current_published_revision_id=$1,version=version+1,updated_at=updated_at+interval '1 microsecond'
		WHERE id=$2 AND workspace_id=$3`, string(frozen.Revision.ID), string(frozen.Document.ID), string(workspaceID)); err == nil {
		t.Fatal("document changed canonical path while entering PUBLISHED")
	} else {
		authoringIntegrationPostgresCode(t, err, "23514")
	}
	binding := authoringPublishBinding(t, workspaceID, frozen.Document.ID, frozen.Revision.ID, "published-path")
	preparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(720), ReservedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := seedAuthoringPublicationProposal(t, ctx, pool, preparation.Reservation, frozen.Revision.Content,
		authoringIntegrationID(721), authoringIntegrationID(722), now.Add(3*time.Second))
	if _, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: binding, ReservationID: preparation.Reservation.ID, PublicationID: authoringIntegrationID(723),
		ProposalID: proposal.ID, ProposalRevisionID: proposal.Revision.ID, CompletedAt: now.Add(4 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	gitCommit := seedAuthoringProposalCommit(t, ctx, pool, proposal, preparation.Reservation,
		frozen.Revision.Content, 730, now.Add(5*time.Second))
	advanced, err := repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: workspaceID, ProposalID: proposal.ID, ProposalRevisionID: proposal.Revision.ID,
		Limit: 1, Now: now.Add(6 * time.Second),
	})
	if err != nil || advanced != 1 {
		t.Fatalf("publication reconcile advanced=%d err=%v", advanced, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.document
		SET canonical_path='notes/drifted.md',version=version+1,updated_at=updated_at+interval '1 microsecond'
		WHERE id=$1 AND workspace_id=$2`, string(frozen.Document.ID), string(workspaceID)); err == nil {
		t.Fatal("published document canonical path changed")
	} else {
		authoringIntegrationPostgresCode(t, err, "23514")
	}
	var storedCommit string
	if err := pool.QueryRow(ctx, `SELECT git_commit FROM core.article_revision WHERE id=$1`, string(frozen.Revision.ID)).Scan(&storedCommit); err != nil || storedCommit != gitCommit {
		t.Fatalf("published git commit=%s want=%s err=%v", storedCommit, gitCommit, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.article_revision SET git_commit=$1
		WHERE id=$2 AND workspace_id=$3`, strings.Repeat("b", 40), string(frozen.Revision.ID), string(workspaceID)); err == nil {
		t.Fatal("published article revision Git commit changed")
	} else {
		authoringIntegrationPostgresCode(t, err, "23514")
	}
}

func TestDocumentDraftAuthoringMigrationConstraints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newAuthoringIntegrationDatabase(t, ctx)
	var bodyConstraint string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid='authoring.working_draft'::regclass AND contype='c'
		  AND pg_get_constraintdef(oid) LIKE '%octet_length(body)%'`).Scan(&bodyConstraint); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodyConstraint, "10485760") {
		t.Fatalf("working draft body constraint = %q", bodyConstraint)
	}
	var reservationModeConstraint, publicationStateConstraint string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid='authoring.document_publication_reservation'::regclass
		  AND conname='authoring_reservation_mode_shape'`).Scan(&reservationModeConstraint); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid='authoring.document_publication_binding'::regclass
		  AND conname='authoring_publication_state_shape'`).Scan(&publicationStateConstraint); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reservationModeConstraint, "workspace-target-absent/v1:") ||
		!strings.Contains(publicationStateConstraint, "git_commit IS NULL") {
		t.Fatalf("publication constraints mode=%q state=%q", reservationModeConstraint, publicationStateConstraint)
	}
}

func TestGORMRepositoryPostgreSQLCommitFailureAndResponseLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := testdb.Require(t, testdb.Config{MaxConns: 16, Availability: testdb.FailWhenUnavailable})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("shared PostgreSQL fixture did not provide a platform pool")
	}
	normalRepository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := authoringIntegrationID(1500)
	seedAuthoringWorkspace(t, ctx, platform.DB(), workspaceID, "authoring-gorm-commit-loss")
	now := time.Date(2026, 8, 3, 18, 0, 0, 0, time.UTC)
	responseLoss := errors.New("injected Authoring commit response loss")
	lossyRepository := *normalRepository
	lossyRepository.unitOfWork = authoringCommitResponseLossUnitOfWork{
		delegate: normalRepository.unitOfWork,
		cause:    responseLoss,
	}
	lossyService, err := authoringapp.NewService(authoringapp.Dependencies{
		Repository: &lossyRepository,
		IDs:        &authoringIntegrationIDs{next: 1500},
		Clock:      foundation.FixedClock{Value: now},
		Proposals:  authoringIntegrationProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := lossyService.CreateWorkingDraft(ctx, authoringapp.CreateCommand{
		WorkspaceID: workspaceID, IdempotencyKey: "gorm-create-response-loss",
	})
	if created != (authoringapp.CreateResult{}) ||
		!authoringIntegrationError(err, foundation.ErrorDependencyUnavailable, "AUTHORING_COMMIT_FAILED") ||
		!errors.Is(err, responseLoss) {
		t.Fatalf("commit response loss create=%#v err=%v", created, err)
	}

	replayService, err := authoringapp.NewService(authoringapp.Dependencies{
		Repository: normalRepository,
		IDs:        &authoringIntegrationIDs{next: 1600},
		Clock:      foundation.FixedClock{Value: now},
		Proposals:  authoringIntegrationProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := replayService.CreateWorkingDraft(ctx, authoringapp.CreateCommand{
		WorkspaceID: workspaceID, IdempotencyKey: "gorm-create-response-loss",
	})
	if err != nil || !replayed.Replayed || replayed.Draft.ID != authoringIntegrationID(1501) ||
		replayed.Draft.WorkspaceID != workspaceID || !replayed.Draft.CreatedAt.Equal(now) {
		t.Fatalf("commit response loss replay=%#v err=%v", replayed, err)
	}
	var drafts, receipts int
	if err := platform.DB().QueryRow(ctx, `SELECT count(*) FROM authoring.working_draft WHERE workspace_id=$1`,
		string(workspaceID)).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if err := platform.DB().QueryRow(ctx, `SELECT count(*) FROM authoring.working_draft_command
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), "gorm-create-response-loss").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if drafts != 1 || receipts != 1 {
		t.Fatalf("commit response loss facts drafts=%d receipts=%d", drafts, receipts)
	}

	documentID, revisionID, _, gitCommit := seedAuthoringPendingPublicationWithCommit(
		t, ctx, platform.DB(), normalRepository, workspaceID, 1700, "gorm-deferred-commit", now.Add(time.Minute),
	)
	var deferredMutationCompleted atomic.Bool
	failingRepository := *normalRepository
	failingRepository.unitOfWork = authoringDeferredCommitFailureUnitOfWork{
		delegate:          normalRepository.unitOfWork,
		workspaceID:       workspaceID,
		documentID:        documentID,
		revisionID:        revisionID,
		gitCommit:         gitCommit,
		mutationCompleted: &deferredMutationCompleted,
	}
	failingService, err := authoringapp.NewService(authoringapp.Dependencies{
		Repository: &failingRepository,
		IDs:        &authoringIntegrationIDs{next: 1800},
		Clock:      foundation.FixedClock{Value: now.Add(2 * time.Minute)},
		Proposals:  authoringIntegrationProposalCreator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := failingService.CreateWorkingDraft(ctx, authoringapp.CreateCommand{
		WorkspaceID: workspaceID, IdempotencyKey: "gorm-deferred-fail",
	})
	if failed != (authoringapp.CreateResult{}) {
		t.Fatalf("deferred commit failure returned a result: %#v", failed)
	}
	if !deferredMutationCompleted.Load() {
		t.Fatalf("deferred commit failure occurred before the transaction callback completed: %v", err)
	}
	if !authoringIntegrationError(err, foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid) {
		var classified *foundation.Error
		_ = errors.As(err, &classified)
		t.Fatalf("deferred commit failure classification=%#v err=%v", classified, err)
	}
	authoringIntegrationPostgresCode(t, err, "23514")

	var failedDrafts, failedReceipts int
	if err := platform.DB().QueryRow(ctx, `SELECT count(*) FROM authoring.working_draft
		WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(authoringIntegrationID(1801))).Scan(&failedDrafts); err != nil {
		t.Fatal(err)
	}
	if err := platform.DB().QueryRow(ctx, `SELECT count(*) FROM authoring.working_draft_command
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), "gorm-deferred-fail").Scan(&failedReceipts); err != nil {
		t.Fatal(err)
	}
	var revisionStatus string
	if err := platform.DB().QueryRow(ctx, `SELECT status FROM core.article_revision
		WHERE workspace_id=$1 AND document_id=$2 AND id=$3`, string(workspaceID), string(documentID), string(revisionID)).Scan(&revisionStatus); err != nil {
		t.Fatal(err)
	}
	if failedDrafts != 0 || failedReceipts != 0 || revisionStatus != string(domain.RevisionDraft) {
		t.Fatalf("deferred commit failure leaked facts drafts=%d receipts=%d revision_status=%s",
			failedDrafts, failedReceipts, revisionStatus)
	}
}

func TestAuthoringPostgreSQLKeyQueryPlans(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newAuthoringIntegrationDatabase(t, ctx)
	workspaceID := authoringIntegrationID(49000)
	revisionDocumentID := authoringIntegrationID(49001)
	historyDocumentID := authoringIntegrationID(49002)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-query-plans")
	base := time.Date(2026, 8, 3, 19, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO authoring.working_draft(
		id,workspace_id,document_id,title,target_path,body,status,version,created_at,updated_at
	) SELECT md5('authoring-plan-draft-'||series::text)::uuid,$1,NULL,
		'plan draft '||series::text,'notes/plan-draft-'||series::text||'.md','body','EDITING',1,
		$2::timestamptz + series*interval '1 second',$2::timestamptz + series*interval '1 second'
		FROM generate_series(1,5000) AS series`, string(workspaceID), base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
	) SELECT md5('authoring-plan-document-'||series::text)::uuid,$1,
		'notes/plan-document-'||series::text||'.md','plan document '||series::text,'DRAFT',NULL,1,
		$2::timestamptz + series*interval '1 second',$2::timestamptz + series*interval '1 second'
		FROM generate_series(1,5000) AS series`, string(workspaceID), base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
	) VALUES($1,$2,'notes/plan-revisions.md','plan revisions','DRAFT',NULL,1,$3,$3)`,
		string(revisionDocumentID), string(workspaceID), base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,source_version_id,parent_revision_id,revision_no,content,content_hash,status,
		optimization_mode,git_commit,created_by_type,created_at
	) SELECT md5('authoring-plan-revision-'||series::text)::uuid,$1,$2,NULL,NULL,series,
		'# plan revision '||series::text,lpad(to_hex(series),64,'0'),'DRAFT','NONE',NULL,'USER',
		$3::timestamptz + series*interval '1 second' FROM generate_series(1,5000) AS series`,
		string(workspaceID), string(revisionDocumentID), base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO authoring.document_publication_binding(
		id,reservation_id,workspace_id,document_id,article_revision_id,proposal_id,proposal_revision_id,
		target_path,content_hash,target_mode,absence_token,status,git_commit,error_code,version,
		created_at,updated_at,published_at
	) SELECT md5('authoring-plan-published-'||series::text)::uuid,
		md5('authoring-plan-published-reservation-'||series::text)::uuid,$1,$2,
		md5('authoring-plan-published-article-'||series::text)::uuid,
		md5('authoring-plan-published-proposal-'||series::text)::uuid,
		md5('authoring-plan-published-proposal-revision-'||series::text)::uuid,
		'notes/history.md',lpad(to_hex(10000+series),64,'0'),'REPLACE','','PUBLISHED',
		CASE WHEN series=1 THEN repeat('a',40)
			ELSE left(md5('authoring-plan-commit-'||series::text)||md5('authoring-plan-commit-extra-'||series::text),40) END,
		'',1,$3::timestamptz + series*interval '1 second',$3::timestamptz + series*interval '1 second',$3::timestamptz + series*interval '1 second'
		FROM generate_series(1,5000) AS series`, string(workspaceID), string(historyDocumentID), base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO authoring.document_publication_binding(
		id,reservation_id,workspace_id,document_id,article_revision_id,proposal_id,proposal_revision_id,
		target_path,content_hash,target_mode,absence_token,status,git_commit,error_code,version,
		created_at,updated_at,published_at
	) SELECT md5('authoring-plan-pending-'||series::text)::uuid,
		md5('authoring-plan-pending-reservation-'||series::text)::uuid,$1,
		md5('authoring-plan-pending-document-'||series::text)::uuid,
		md5('authoring-plan-pending-article-'||series::text)::uuid,
		md5('authoring-plan-pending-proposal-'||series::text)::uuid,
		md5('authoring-plan-pending-proposal-revision-'||series::text)::uuid,
		'notes/pending-'||series::text||'.md',lpad(to_hex(20000+series),64,'0'),'REPLACE','','PENDING',
		NULL,'',1,$2::timestamptz + series*interval '1 second',$2::timestamptz + series*interval '1 second',NULL
		FROM generate_series(1,100) AS series`, string(workspaceID), base); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=origin`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE authoring.working_draft;
		ANALYZE core.document;
		ANALYZE core.article_revision;
		ANALYZE authoring.document_publication_binding`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}

	cursorTime := base.Add(2 * time.Hour)
	cursorID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	tests := []struct {
		name       string
		indexNames []string
		maxRows    int
		query      string
		args       []any
	}{
		{
			name: "working-draft-keyset", indexNames: []string{"idx_authoring_working_draft_workspace_updated"}, maxRows: 11,
			query: `SELECT id FROM authoring.working_draft WHERE workspace_id=$1
				AND (updated_at,id)<($2,$3) ORDER BY updated_at DESC,id DESC LIMIT $4`,
			args: []any{string(workspaceID), cursorTime, cursorID, 11},
		},
		{
			name: "document-keyset", indexNames: []string{"idx_core_document_workspace_updated"}, maxRows: 11,
			query: `SELECT id FROM core.document WHERE workspace_id=$1 AND lifecycle_status='DRAFT'
				AND (updated_at,id)<($2,$3) ORDER BY updated_at DESC,id DESC LIMIT $4`,
			args: []any{string(workspaceID), cursorTime, cursorID, 11},
		},
		{
			name: "latest-article-revision", indexNames: []string{"uq_core_article_revision_no"}, maxRows: 1,
			query: `SELECT id FROM core.article_revision WHERE workspace_id=$1 AND document_id=$2
				ORDER BY revision_no DESC LIMIT 1`,
			args: []any{string(workspaceID), string(revisionDocumentID)},
		},
		{
			name: "publication-reconcile-candidates", indexNames: []string{
				"uq_authoring_publication_nonterminal_document",
				"idx_authoring_publication_workspace_status",
			}, maxRows: 10,
			query: `SELECT id FROM authoring.document_publication_binding WHERE workspace_id=$1
				AND status IN ('PENDING','RECOVERY_REQUIRED') ORDER BY created_at,id LIMIT $2`,
			args: []any{string(workspaceID), 10},
		},
		{
			name: "publication-history-git", indexNames: []string{"idx_authoring_publication_history_git"}, maxRows: 10,
			query: `SELECT id FROM authoring.document_publication_binding WHERE workspace_id=$1
				AND document_id=$2 AND target_path=$3 AND git_commit=$4 LIMIT $5`,
			args: []any{string(workspaceID), string(historyDocumentID), "notes/history.md", strings.Repeat("a", 40), 10},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := explainAuthoringQuery(t, ctx, tx, test.query, test.args...)
			if plan.ActualRows < 1 || plan.ActualRows > float64(test.maxRows) {
				t.Fatalf("query plan actual rows=%v, want 1..%d: %+v", plan.ActualRows, test.maxRows, plan)
			}
			if !authoringPlanUsesAnyIndex(plan, test.indexNames...) {
				t.Fatalf("query plan does not use one of indexes %v: %+v", test.indexNames, plan)
			}
		})
	}
}

type authoringIntegrationRepository interface {
	authoringapp.Repository
	authoringapp.ArticleRevisionSearchRepository
	ValidateRestoreWriteback(context.Context, authoringapp.RestoreWritebackCheck) error
	FinalizeRestorePublication(context.Context, authoringapp.RestorePublicationRecord) (bool, error)
}

type authoringIntegrationVariant struct {
	name string
}

func runAuthoringIntegrationVariants(
	t *testing.T,
	scenario func(*testing.T, authoringIntegrationVariant),
) {
	t.Helper()
	for _, name := range []string{"legacy-pgx", "gorm"} {
		variant := authoringIntegrationVariant{name: name}
		t.Run(name, func(t *testing.T) {
			scenario(t, variant)
		})
	}
}

func (variant authoringIntegrationVariant) open(t *testing.T) (authoringIntegrationRepository, *pgxpool.Pool) {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{MaxConns: 16, Availability: testdb.FailWhenUnavailable})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("shared PostgreSQL fixture did not provide a platform pool")
	}
	var repository authoringIntegrationRepository
	var err error
	switch variant.name {
	case "legacy-pgx":
		repository, err = NewRepository(platform.DB())
	case "gorm":
		repository, err = NewGORMRepository(platform)
	default:
		t.Fatalf("unknown Authoring integration variant %q", variant.name)
	}
	if err != nil {
		t.Fatal(err)
	}
	return repository, platform.DB()
}

type authoringCommitResponseLossUnitOfWork struct {
	delegate foundation.UnitOfWork
	cause    error
}

func (unitOfWork authoringCommitResponseLossUnitOfWork) Within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work foundation.TransactionFunc,
) error {
	if err := unitOfWork.delegate.Within(ctx, options, work); err != nil {
		return err
	}
	return unitOfWork.cause
}

type authoringDeferredCommitFailureUnitOfWork struct {
	delegate          foundation.UnitOfWork
	workspaceID       foundation.ID
	documentID        foundation.ID
	revisionID        foundation.ID
	gitCommit         string
	mutationCompleted *atomic.Bool
}

func (unitOfWork authoringDeferredCommitFailureUnitOfWork) Within(
	ctx context.Context,
	options foundation.TransactionOptions,
	work foundation.TransactionFunc,
) error {
	return unitOfWork.delegate.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		if err := work(callbackCtx, scope); err != nil {
			return err
		}
		transaction, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		result := transaction.WithContext(callbackCtx).Exec(`UPDATE core.article_revision
			SET status='PUBLISHED',git_commit=?
			WHERE workspace_id=? AND document_id=? AND id=? AND status='DRAFT'`,
			unitOfWork.gitCommit, string(unitOfWork.workspaceID),
			string(unitOfWork.documentID), string(unitOfWork.revisionID))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("deferred commit failure mutation did not update one revision")
		}
		if unitOfWork.mutationCompleted != nil {
			unitOfWork.mutationCompleted.Store(true)
		}
		return nil
	})
}

type authoringStatementCounter struct {
	count atomic.Int64
}

func (counter *authoringStatementCounter) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return counter
}

func (*authoringStatementCounter) Info(context.Context, string, ...any)  {}
func (*authoringStatementCounter) Warn(context.Context, string, ...any)  {}
func (*authoringStatementCounter) Error(context.Context, string, ...any) {}

func (counter *authoringStatementCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.count.Add(1)
}

func (counter *authoringStatementCounter) reset() {
	if counter != nil {
		counter.count.Store(0)
	}
}

func (counter *authoringStatementCounter) statements() int64 {
	if counter == nil {
		return 0
	}
	return counter.count.Load()
}

func installAuthoringStatementCounter(t *testing.T, repository authoringIntegrationRepository) *authoringStatementCounter {
	t.Helper()
	gormRepository, ok := repository.(*GORMRepository)
	if !ok {
		return nil
	}
	if gormRepository.database == nil || gormRepository.database.Config == nil {
		t.Fatal("Authoring GORM repository has no logger configuration")
	}
	original := gormRepository.database.Config.Logger
	counter := &authoringStatementCounter{}
	gormRepository.database.Config.Logger = counter
	t.Cleanup(func() {
		gormRepository.database.Config.Logger = original
	})
	return counter
}

func assertAuthoringStatementCount(t *testing.T, counter *authoringStatementCounter, want int64, operation string) {
	t.Helper()
	if counter != nil && counter.statements() != want {
		t.Fatalf("%s GORM statements=%d, want %d", operation, counter.statements(), want)
	}
}

func assertAuthoringListCancellationAndPoolReuse(
	t *testing.T,
	ctx context.Context,
	variant authoringIntegrationVariant,
	repository authoringIntegrationRepository,
	pool *pgxpool.Pool,
	workspaceID foundation.ID,
) {
	t.Helper()
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			blocker.Release()
		}
	}()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	if _, err := blockerTx.Exec(ctx, `LOCK TABLE authoring.working_draft IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	baselineAcquired := pool.Stat().AcquiredConns()

	cancelCause := errors.New("Authoring list canceled by caller")
	queryCtx, cancelQuery := context.WithCancelCause(ctx)
	cancelResult := make(chan error, 1)
	go func() {
		_, listErr := repository.List(queryCtx, authoringapp.ListQuery{WorkspaceID: workspaceID, Limit: 2})
		cancelResult <- listErr
	}()
	waitForBlockedAuthoringList(t, ctx, pool)
	cancelQuery(cancelCause)
	select {
	case err := <-cancelResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked Authoring list cancellation error=%v", err)
		}
		if variant.name == "gorm" && !errors.Is(err, cancelCause) {
			t.Fatalf("blocked GORM Authoring list did not preserve cancellation cause: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked Authoring list did not return after cancellation")
	}
	waitForAuthoringAcquiredConnections(t, pool, baselineAcquired)

	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 2*time.Second)
	defer deadlineCancel()
	deadlineResult := make(chan error, 1)
	go func() {
		_, listErr := repository.List(deadlineCtx, authoringapp.ListQuery{WorkspaceID: workspaceID, Limit: 2})
		deadlineResult <- listErr
	}()
	waitForBlockedAuthoringList(t, ctx, pool)
	select {
	case err := <-deadlineResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked Authoring list deadline error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked Authoring list did not return after deadline")
	}
	waitForAuthoringAcquiredConnections(t, pool, baselineAcquired)

	if err := blockerTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	blocker.Release()
	released = true
	waitForAuthoringAcquiredConnections(t, pool, 0)
	var one int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("Authoring shared pool was not reusable: one=%d err=%v", one, err)
	}
}

func waitForBlockedAuthoringList(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid()
				AND wait_event_type='Lock' AND state='active'
				AND query LIKE '%FROM authoring.working_draft%'
		)`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("blocked Authoring list was not observed in pg_stat_activity")
}

func waitForAuthoringAcquiredConnections(t *testing.T, pool *pgxpool.Pool, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for pool.Stat().AcquiredConns() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != want {
		t.Fatalf("Authoring shared pool acquired connections=%d, want %d", acquired, want)
	}
}

type authoringExplainPlan struct {
	NodeType     string                 `json:"Node Type"`
	RelationName string                 `json:"Relation Name"`
	IndexName    string                 `json:"Index Name"`
	ActualRows   float64                `json:"Actual Rows"`
	Plans        []authoringExplainPlan `json:"Plans"`
}

func explainAuthoringQuery(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args ...any) authoringExplainPlan {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON, COSTS OFF, SUMMARY OFF, TIMING OFF) `+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var documents []struct {
		Plan authoringExplainPlan `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid Authoring EXPLAIN document: err=%v plan=%s", err, raw)
	}
	return documents[0].Plan
}

func authoringPlanUsesAnyIndex(plan authoringExplainPlan, indexNames ...string) bool {
	for _, indexName := range indexNames {
		if plan.IndexName == indexName {
			return true
		}
	}
	for _, child := range plan.Plans {
		if authoringPlanUsesAnyIndex(child, indexNames...) {
			return true
		}
	}
	return false
}

func newAuthoringIntegrationDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return newAuthoringIntegrationDatabaseToVersion(t, ctx, 0)
}

// newAuthoringIntegrationDatabaseToVersion creates a disposable database migrated
// only up to the given Atlas version; version<=0 applies every pending migration.
func newAuthoringIntegrationDatabaseToVersion(t *testing.T, ctx context.Context, version int64) *pgxpool.Pool {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{
		MaxConns:     16,
		Availability: testdb.FailWhenUnavailable,
		Migrate: func(migrationContext context.Context, pool *pgxpool.Pool) error {
			return platformmigration.MigrateAtlasToVersion(migrationContext, pool, version)
		},
	})
	platform := fixture.Pool()
	if platform == nil || platform.DB() == nil {
		t.Fatal("shared PostgreSQL fixture did not provide a platform pool")
	}
	return platform.DB()
}

func seedAuthoringWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id foundation.ID, name string) {
	t.Helper()
	now := time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC)
	fingerprint := sha256.Sum256([]byte(name))
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
		status,availability,availability_reason,availability_checked_at,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,1,$3,$5,'inactive','available',NULL,$5,1,$5,$5)`,
		string(id), name, "/tmp/"+name, hex.EncodeToString(fingerprint[:]), now); err != nil {
		t.Fatal(err)
	}
}

func seedAuthoringListWorkingDraft(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, draftID foundation.ID, title string, updatedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO authoring.working_draft(
		id,workspace_id,document_id,title,target_path,body,status,version,created_at,updated_at
	) VALUES($1,$2,NULL,$3,$4,'body','EDITING',1,$5,$5)`,
		string(draftID), string(workspaceID), title, title+".md", updatedAt.UTC()); err != nil {
		t.Fatal(err)
	}
}

func seedAuthoringListDocument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, documentID foundation.ID, title string, updatedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,'DRAFT',NULL,1,$5,$5)`,
		string(documentID), string(workspaceID), title+".md", title, updatedAt.UTC()); err != nil {
		t.Fatal(err)
	}
}

type authoringIntegrationIDs struct {
	mu   sync.Mutex
	next int
}

func (ids *authoringIntegrationIDs) New() (foundation.ID, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return authoringIntegrationID(ids.next), nil
}

func authoringIntegrationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("69200000-0000-4000-8000-%012d", value))
}

type authoringIntegrationProposalCreator struct{}

func (authoringIntegrationProposalCreator) CreatePublicationProposal(
	context.Context,
	authoringapp.PublicationProposal,
) (authoringapp.PublicationProposalResult, error) {
	return authoringapp.PublicationProposalResult{}, errors.New("unexpected publication proposal creation")
}

func newAuthoringPublicationIntegrationService(
	t *testing.T,
	repository authoringIntegrationRepository,
	pool *pgxpool.Pool,
	targets *authoringIntegrationTargetReader,
	now time.Time,
	authoringIDStart int,
	changeControlIDStart int,
) *authoringapp.Service {
	t.Helper()
	proposalRepository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	proposalService, err := changecontrolapp.NewService(
		proposalRepository,
		&authoringIntegrationIDs{next: changeControlIDStart},
		foundation.FixedClock{Value: now},
		targets,
		authoringIntegrationGitInspector{},
	)
	if err != nil {
		t.Fatal(err)
	}
	proposalCreator, err := authoringchangecontrol.NewProposalCreator(proposalService, targets)
	if err != nil {
		t.Fatal(err)
	}
	service, err := authoringapp.NewService(authoringapp.Dependencies{
		Repository: repository,
		IDs:        &authoringIntegrationIDs{next: authoringIDStart},
		Clock:      foundation.FixedClock{Value: now},
		Proposals:  proposalCreator,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type authoringIntegrationTargetReader struct {
	mu          sync.Mutex
	absenceErr  error
	absentCalls int
}

func (reader *authoringIntegrationTargetReader) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	return strings.Repeat("a", 64), nil
}

func (reader *authoringIntegrationTargetReader) EnsureTargetAbsent(
	_ context.Context,
	workspaceID foundation.ID,
	targetPath string,
	absenceToken string,
) error {
	expected, err := changecontroldomain.ComputeAbsenceToken(workspaceID, targetPath)
	if err != nil {
		return err
	}
	if absenceToken != expected {
		return errors.New("unexpected CREATE_ONLY absence token")
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.absentCalls++
	return reader.absenceErr
}

func (reader *authoringIntegrationTargetReader) AbsenceChecks() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.absentCalls
}

type authoringIntegrationGitInspector struct{}

func (authoringIntegrationGitInspector) CaptureApprovalSnapshot(
	context.Context,
	foundation.ID,
) (changecontroldomain.GitSnapshot, error) {
	return changecontroldomain.GitSnapshot{}, errors.New("unexpected approval Git inspection")
}

func authoringIntegrationError(err error, kind foundation.ErrorKind, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind && classified.Code == code
}

func authoringIntegrationPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("PostgreSQL error=%v, want SQLSTATE %s", err, code)
	}
}

func authoringIntegrationFatal(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		t.Fatalf("error=%v PostgreSQL code=%s message=%s detail=%s constraint=%s", err, postgresError.Code, postgresError.Message, postgresError.Detail, postgresError.ConstraintName)
	}
	t.Fatal(err)
}
