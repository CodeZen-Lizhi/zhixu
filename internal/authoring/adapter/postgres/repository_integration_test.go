//go:build integration

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
	"sync"
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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryPostgreSQLWorkingDraftCASReplayFreezeAndWorkspaceScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
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
	searchHits, err := service.SearchArticleRevisions(ctx, authoringapp.ArticleRevisionSearchQuery{WorkspaceID: workspaceA, Query: "Java", Limit: 5})
	if err != nil || len(searchHits) != 1 || searchHits[0].Document.ID != frozen.Document.ID || searchHits[0].RevisionID != frozen.Revision.ID {
		t.Fatalf("article revision search=%#v err=%v", searchHits, err)
	}
	otherWorkspaceHits, err := service.SearchArticleRevisions(ctx, authoringapp.ArticleRevisionSearchQuery{WorkspaceID: workspaceB, Query: "Java", Limit: 5})
	if err != nil || len(otherWorkspaceHits) != 0 {
		t.Fatalf("cross-workspace article revision search=%#v err=%v", otherWorkspaceHits, err)
	}

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

	page, err := service.ListWorkingDrafts(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.Draft.ID {
		t.Fatalf("draft page=%#v err=%v", page, err)
	}
}

func TestRepositoryPostgreSQLListsWorkingAndDocumentDraftsByWorkspaceKeyset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
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

	workingFirst, err := repository.List(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 2})
	if err != nil || len(workingFirst.Items) != 2 || workingFirst.Next == nil ||
		workingFirst.Items[0].ID != workingNewest || workingFirst.Items[1].ID != workingTieHigh {
		t.Fatalf("working first page=%#v err=%v", workingFirst, err)
	}
	workingSecond, err := repository.List(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 2, After: workingFirst.Next})
	if err != nil || len(workingSecond.Items) != 1 || workingSecond.Next != nil || workingSecond.Items[0].ID != workingTieLow {
		t.Fatalf("working second page=%#v err=%v", workingSecond, err)
	}
	workingAfterLast, err := repository.List(ctx, authoringapp.ListQuery{WorkspaceID: workspaceA, Limit: 2,
		After: &authoringapp.Cursor{UpdatedAt: base, ID: workingTieLow}})
	if err != nil || len(workingAfterLast.Items) != 0 || workingAfterLast.Next != nil {
		t.Fatalf("working page after last=%#v err=%v", workingAfterLast, err)
	}

	documentNewest := authoringIntegrationID(938)
	documentTieHigh := authoringIntegrationID(937)
	documentTieLow := authoringIntegrationID(936)
	seedAuthoringListDocument(t, ctx, pool, workspaceA, documentTieLow, "document-low", base)
	seedAuthoringListDocument(t, ctx, pool, workspaceA, documentTieHigh, "document-high", base)
	seedAuthoringListDocument(t, ctx, pool, workspaceA, documentNewest, "document-newest", base.Add(time.Second))
	seedAuthoringListDocument(t, ctx, pool, workspaceB, authoringIntegrationID(939), "document-other", base.Add(2*time.Second))

	documentFirst, err := repository.ListDocuments(ctx, authoringapp.DocumentListQuery{WorkspaceID: workspaceA, Limit: 2})
	if err != nil || len(documentFirst.Items) != 2 || documentFirst.Next == nil ||
		documentFirst.Items[0].ID != documentNewest || documentFirst.Items[1].ID != documentTieHigh {
		t.Fatalf("document first page=%#v err=%v", documentFirst, err)
	}
	documentSecond, err := repository.ListDocuments(ctx, authoringapp.DocumentListQuery{WorkspaceID: workspaceA, Limit: 2, After: documentFirst.Next})
	if err != nil || len(documentSecond.Items) != 1 || documentSecond.Next != nil || documentSecond.Items[0].ID != documentTieLow {
		t.Fatalf("document second page=%#v err=%v", documentSecond, err)
	}
	documentAfterLast, err := repository.ListDocuments(ctx, authoringapp.DocumentListQuery{WorkspaceID: workspaceA, Limit: 2,
		After: &authoringapp.DocumentCursor{UpdatedAt: base, ID: documentTieLow}})
	if err != nil || len(documentAfterLast.Items) != 0 || documentAfterLast.Next != nil {
		t.Fatalf("document page after last=%#v err=%v", documentAfterLast, err)
	}
}

func TestRepositoryPostgreSQLPathConflictAndConcurrentCommandSerialization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
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

func newAuthoringIntegrationRepository(t *testing.T, ctx context.Context) (*Repository, *pgxpool.Pool) {
	t.Helper()
	pool := newAuthoringIntegrationDatabase(t, ctx)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	return repository, pool
}

func newAuthoringIntegrationDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return newAuthoringIntegrationDatabaseToVersion(t, ctx, 0)
}

// newAuthoringIntegrationDatabaseToVersion creates a disposable database migrated
// only up to the given Atlas version; version<=0 applies every pending migration.
func newAuthoringIntegrationDatabaseToVersion(t *testing.T, ctx context.Context, version int64) *pgxpool.Pool {
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
	name := fmt.Sprintf("zhixu_authoring_%d", time.Now().UnixNano())
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
	if err := platformmigration.MigrateAtlasToVersion(ctx, pool, version); err != nil {
		t.Fatal(err)
	}
	return pool
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
	repository *Repository,
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
