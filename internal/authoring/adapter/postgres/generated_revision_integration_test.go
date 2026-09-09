//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

func TestGeneratedAuthoringPostgreSQLAtomicReplayAndScope(t *testing.T) {
	fixture := newGeneratedAuthoringFixture(t)
	ctx := t.Context()
	record := generatedIntegrationRecord(t, fixture.workspaceID, 20000, fixture.now)
	reservation := generatedReservationRecord(t, record, 20009, fixture.now.Add(time.Second))
	rollback := errors.New("semantic projection failed")
	err := fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		if _, err := fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, record); err != nil {
			return err
		}
		if _, err := fixture.repository.ReservePublicationScoped(ctx, scope, reservation); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("caller rollback cause was lost: %v", err)
	}
	assertGeneratedCount(t, fixture.database, "core.document", "id", string(record.Request.DocumentID), 0)
	assertGeneratedCount(t, fixture.database, "core.article_revision", "id", string(record.Request.ArticleRevisionID), 0)
	assertGeneratedCount(t, fixture.database, "authoring.generated_article_revision", "article_revision_id", string(record.Request.ArticleRevisionID), 0)
	assertGeneratedCount(t, fixture.database, "authoring.document_publication_reservation", "id", string(reservation.ReservationID), 0)

	lostResponse := errors.New("committed response lost")
	lossUoW := authoringCommitResponseLossUnitOfWork{delegate: fixture.uow, cause: lostResponse}
	var original authoringapp.GeneratedRevisionResult
	err = lossUoW.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		original, err = fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, record)
		return err
	})
	if !errors.Is(err, lostResponse) || original.Replayed || original.Revision.CreatedByType != "AGENT" || original.Revision.SourceVersionID != "" ||
		original.Document.Lifecycle != domain.DocumentDraft || original.Document.CurrentPublishedRevisionID != "" {
		t.Fatalf("generation or response-loss setup failed: result=%+v error=%v", original, err)
	}
	replayRecord := record
	replayRecord.CreatedAt = fixture.now.Add(time.Hour)
	var replay authoringapp.GeneratedRevisionResult
	err = fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		replay, err = fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, replayRecord)
		return err
	})
	if err != nil || !replay.Replayed || replay.Document != original.Document || replay.Revision != original.Revision {
		t.Fatalf("committed generation did not exactly replay: result=%+v error=%v", replay, err)
	}
	assertGeneratedCount(t, fixture.database, "authoring.generated_article_revision", "origin_id", string(record.Request.OriginID), 1)
	assertGeneratedCount(t, fixture.database, "authoring.working_draft", "workspace_id", string(fixture.workspaceID), 0)

	changed := record
	changed.Request.ProjectionHash = strings.Repeat("e", 64)
	changed.RequestHash, err = domain.ComputeGeneratedRevisionRequestHash(changed.Request)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		_, err := fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, changed)
		return err
	})
	if !authoringIntegrationError(err, foundation.ErrorVersionConflict, authoringapp.ErrorCodeIdempotencyConflict) {
		t.Fatalf("same key accepted a changed semantic projection: %v", err)
	}

	var expired foundation.TransactionScope
	if err := fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error { expired = scope; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []foundation.TransactionScope{nil, &generatedForgedScope{}, expired} {
		if _, err := fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, record); !authoringIntegrationError(err, foundation.ErrorInvalidInput, domain.ErrorCodeGeneratedInvalid) {
			t.Fatalf("invalid transaction scope was accepted: %T error=%v", scope, err)
		}
	}
	for _, test := range []struct {
		name     string
		deadline bool
	}{
		{"cancel", false}, {"deadline", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("generation stopped")
			var stopped context.Context
			if test.deadline {
				var cancel context.CancelFunc
				stopped, cancel = context.WithDeadlineCause(ctx, time.Now().Add(-time.Second), cause)
				defer cancel()
			} else {
				var cancel context.CancelCauseFunc
				stopped, cancel = context.WithCancelCause(ctx)
				cancel(cause)
			}
			err := fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(_ context.Context, scope foundation.TransactionScope) error {
				_, err := fixture.repository.AppendGeneratedRevisionScoped(stopped, scope, record)
				return err
			})
			var classified *foundation.Error
			if !errors.As(err, &classified) || !errors.Is(err, cause) || !errors.Is(err, stopped.Err()) || classified.Retryable != test.deadline {
				t.Fatalf("caller context classification changed: %v", err)
			}
		})
	}
}

func TestGeneratedAuthoringPostgreSQLOriginAndMutationGuards(t *testing.T) {
	fixture := newGeneratedAuthoringFixture(t)
	ctx := t.Context()
	first := generatedIntegrationRecord(t, fixture.workspaceID, 21000, fixture.now)
	initial := fixture.append(t, first)
	for _, test := range []struct {
		name   string
		mutate func(*domain.GeneratedRevisionRequest)
	}{
		{"origin identity", func(value *domain.GeneratedRevisionRequest) { value.OriginID = first.Request.OriginID }},
		{"document identity", func(value *domain.GeneratedRevisionRequest) { value.DocumentID = first.Request.DocumentID }},
		{"article identity", func(value *domain.GeneratedRevisionRequest) {
			value.ArticleRevisionID = first.Request.ArticleRevisionID
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			duplicate := generatedIntegrationRecord(t, fixture.workspaceID, 21800, fixture.now)
			test.mutate(&duplicate.Request)
			duplicate.RequestHash, _ = domain.ComputeGeneratedRevisionRequestHash(duplicate.Request)
			err := fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
				_, err := fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, duplicate)
				return err
			})
			if !authoringIntegrationError(err, foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedOriginConflict) {
				t.Fatalf("duplicate generated identity did not return a stable conflict: %v", err)
			}
		})
	}
	next := generatedIntegrationSuccessor(t, first, initial, 21100, fixture.now.Add(time.Second))
	for _, test := range []struct {
		name   string
		mutate func(*domain.GeneratedRevisionRequest)
		kind   foundation.ErrorKind
		code   string
	}{
		{"origin", func(value *domain.GeneratedRevisionRequest) { value.OriginID = authoringIntegrationID(21998) }, foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedOriginConflict},
		{"document version", func(value *domain.GeneratedRevisionRequest) { value.ExpectedDocumentVersion++ }, foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedOriginConflict},
		{"parent", func(value *domain.GeneratedRevisionRequest) { value.ParentRevisionID = authoringIntegrationID(21997) }, foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedOriginConflict},
		{"workspace", func(value *domain.GeneratedRevisionRequest) { value.WorkspaceID = fixture.otherWorkspaceID }, foundation.ErrorNotFound, authoringapp.ErrorCodeNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := next
			test.mutate(&changed.Request)
			changed.RequestHash, _ = domain.ComputeGeneratedRevisionRequestHash(changed.Request)
			err := fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
				_, err := fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, changed)
				return err
			})
			if !authoringIntegrationError(err, test.kind, test.code) {
				t.Fatalf("forged generation binding accepted: %v", err)
			}
		})
	}
	second := fixture.append(t, next)
	if second.Revision.ParentRevisionID != initial.Revision.ID || second.Revision.Content != next.Request.Content || second.Document.Version != 2 {
		t.Fatalf("successor did not append exact content: %+v", second)
	}
	oldReplay := fixture.append(t, first)
	if !oldReplay.Replayed || oldReplay.Document != initial.Document || oldReplay.Revision != initial.Revision {
		t.Fatalf("later version changed old command replay: %+v", oldReplay)
	}
	assertGeneratedCount(t, fixture.database, "core.article_revision", "document_id", string(initial.Document.ID), 2)
	for _, statement := range []string{
		`UPDATE authoring.generated_document SET origin_id=? WHERE document_id=?`,
		`UPDATE authoring.generated_article_revision SET origin_id=? WHERE document_id=?`,
		`UPDATE core.article_revision SET source_version_id=? WHERE document_id=?`,
	} {
		err := fixture.database.Exec(statement, string(authoringIntegrationID(21996)), string(initial.Document.ID)).Error
		if platformpostgres.SQLState(err) != "55000" {
			t.Fatalf("immutable source identity was mutable: state=%q error=%v", platformpostgres.SQLState(err), err)
		}
	}
	if err := fixture.database.Exec(`UPDATE core.document SET lifecycle_status='PUBLISHED',current_published_revision_id=?,version=version+1 WHERE id=?`, string(second.Revision.ID), string(second.Document.ID)).Error; platformpostgres.SQLState(err) != "23514" {
		t.Fatalf("generated publication bypassed proposal_commit: state=%q error=%v", platformpostgres.SQLState(err), err)
	}
	service, err := authoringapp.NewService(authoringapp.Dependencies{Repository: fixture.repository, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.FixedClock{Value: fixture.now}, Proposals: authoringIntegrationProposalCreator{}})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.CreateWorkingDraft(ctx, authoringapp.CreateCommand{WorkspaceID: fixture.workspaceID, IdempotencyKey: "user-create"})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := service.UpdateWorkingDraft(ctx, authoringapp.UpdateCommand{WorkspaceID: fixture.workspaceID, DraftID: draft.Draft.ID, ExpectedVersion: 1, Title: "User", TargetPath: "user.md", Body: "# User\n", IdempotencyKey: "user-update"})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := service.FreezeWorkingDraft(ctx, authoringapp.FreezeCommand{WorkspaceID: fixture.workspaceID, DraftID: edited.Draft.ID, ExpectedVersion: 2, IdempotencyKey: "user-freeze"})
	if err != nil || frozen.Revision.CreatedByType != "USER" {
		t.Fatalf("USER freeze changed: %+v error=%v", frozen, err)
	}
	claimUser := next
	claimUser.IdempotencyKey = "claim-user-generated"
	claimUser.Request.DocumentID, claimUser.Request.ParentRevisionID = frozen.Document.ID, frozen.Revision.ID
	claimUser.Request.TargetPath, claimUser.Request.ExpectedDocumentVersion = frozen.Document.CanonicalPath, frozen.Document.Version
	claimUser.RequestHash, _ = domain.ComputeGeneratedRevisionRequestHash(claimUser.Request)
	err = fixture.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		_, err := fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, claimUser)
		return err
	})
	if !authoringIntegrationError(err, foundation.ErrorVersionConflict, domain.ErrorCodeGeneratedOriginConflict) {
		t.Fatalf("generated entry claimed a USER document: %v", err)
	}
	forgedOrigin := gormGeneratedDocumentRecord{DocumentID: string(frozen.Document.ID), WorkspaceID: string(fixture.workspaceID), OriginKind: string(domain.GeneratedOriginSynthesisNote), OriginID: string(authoringIntegrationID(21995)), CreatedAt: frozen.Document.CreatedAt}
	if err := fixture.database.Create(&forgedOrigin).Error; platformpostgres.SQLState(err) != "23514" {
		t.Fatalf("database accepted a USER origin forgery: state=%q error=%v", platformpostgres.SQLState(err), err)
	}
	stored, err := service.GetWorkingDraft(ctx, fixture.workspaceID, frozen.Draft.ID)
	if err != nil || stored != frozen.Draft {
		t.Fatalf("generated operations mutated the user draft: %+v error=%v", stored, err)
	}
}

type generatedAuthoringFixture struct {
	repository       *GORMRepository
	platform         *platformpostgres.Pool
	database         *gorm.DB
	uow              foundation.UnitOfWork
	workspaceID      foundation.ID
	otherWorkspaceID foundation.ID
	now              time.Time
}

func newGeneratedAuthoringFixture(t *testing.T) generatedAuthoringFixture {
	t.Helper()
	// This owner contract was introduced by 00094. Cross-owner semantic projection
	// FKs introduced later are covered by the Synthesis composition fixture.
	databaseFixture := testdb.Require(t, testdb.Config{MaxConns: 12, Availability: testdb.FailWhenUnavailable,
		Migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
			return platformmigration.MigrateAtlasToVersion(ctx, pool, 94)
		}})
	platform := databaseFixture.Pool()
	repository, err := NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	database, err := platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	uow, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	fixture := generatedAuthoringFixture{repository: repository, platform: platform, database: database, uow: uow,
		workspaceID: authoringIntegrationID(19000), otherWorkspaceID: authoringIntegrationID(19001), now: time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC)}
	seedAuthoringWorkspace(t, t.Context(), platform.DB(), fixture.workspaceID, "generated-authoring")
	seedAuthoringWorkspace(t, t.Context(), platform.DB(), fixture.otherWorkspaceID, "generated-authoring-other")
	return fixture
}

func generatedIntegrationRecord(t *testing.T, workspaceID foundation.ID, base int, at time.Time) authoringapp.GeneratedRevisionRecord {
	t.Helper()
	target, err := domain.DefaultGeneratedTargetPath(authoringIntegrationID(base + 1))
	if err != nil {
		t.Fatal(err)
	}
	request := domain.GeneratedRevisionRequest{WorkspaceID: workspaceID, DocumentID: authoringIntegrationID(base + 2), ArticleRevisionID: authoringIntegrationID(base + 3),
		OriginKind: domain.GeneratedOriginSynthesisNote, OriginID: authoringIntegrationID(base + 1), OriginRevisionID: authoringIntegrationID(base + 4),
		ProjectionHash: domain.ComputeContentHash(fmt.Sprintf("semantic-%d", base)), RevisionNo: 1, Title: "Generated note", TargetPath: target,
		Content: "# Generated note\n\nAn original fact.\n"}
	hash, err := domain.ComputeGeneratedRevisionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	return authoringapp.GeneratedRevisionRecord{Request: request, IdempotencyKey: fmt.Sprintf("generated-%d", base), RequestHash: hash, CreatedAt: at}
}

func generatedIntegrationSuccessor(t *testing.T, previous authoringapp.GeneratedRevisionRecord, result authoringapp.GeneratedRevisionResult, base int, at time.Time) authoringapp.GeneratedRevisionRecord {
	t.Helper()
	next := previous
	next.Request.ArticleRevisionID, next.Request.OriginRevisionID = authoringIntegrationID(base+3), authoringIntegrationID(base+4)
	next.Request.ParentRevisionID, next.Request.RevisionNo, next.Request.ExpectedDocumentVersion = result.Revision.ID, result.Revision.RevisionNo+1, result.Document.Version
	next.Request.Content += "\nA complementary fact with an independent source.\n"
	next.Request.ProjectionHash = domain.ComputeContentHash(fmt.Sprintf("semantic-%d", base))
	next.IdempotencyKey, next.CreatedAt = fmt.Sprintf("generated-%d", base), at
	var err error
	next.RequestHash, err = domain.ComputeGeneratedRevisionRequestHash(next.Request)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func generatedReservationRecord(t *testing.T, generated authoringapp.GeneratedRevisionRecord, id int, at time.Time) authoringapp.ReservePublicationRecord {
	t.Helper()
	return authoringapp.ReservePublicationRecord{Binding: authoringPublishBinding(t, generated.Request.WorkspaceID, generated.Request.DocumentID, generated.Request.ArticleRevisionID, "publish-"+generated.IdempotencyKey), ReservationID: authoringIntegrationID(id), ReservedAt: at}
}

func (fixture generatedAuthoringFixture) append(t *testing.T, record authoringapp.GeneratedRevisionRecord) authoringapp.GeneratedRevisionResult {
	t.Helper()
	var result authoringapp.GeneratedRevisionResult
	if err := fixture.uow.Within(t.Context(), foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		var err error
		result, err = fixture.repository.AppendGeneratedRevisionScoped(ctx, scope, record)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertGeneratedCount(t *testing.T, database *gorm.DB, table, column, value string, want int64) {
	t.Helper()
	var count int64
	// Table/column names are fixed test-owned selectors, never request input.
	if err := database.Table(table).Where(column+"=?", value).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%s count=%d want=%d error=%v", table, count, want, err)
	}
}

type generatedForgedScope struct{}

func (*generatedForgedScope) TransactionScope() {}
