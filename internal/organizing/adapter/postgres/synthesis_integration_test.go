//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	authoringchange "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/changecontrol"
	authoringpostgres "github.com/CodeZen-Lizhi/zhixu/internal/authoring/adapter/postgres"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

// This fixture exercises actual GORM/UoW/Authoring/Proposal persistence and
// source fences. Semantic model verification is an explicit test seam; the
// runtime composition suite owns the real model journal/Provider evidence.
func TestSynthesisPostgreSQLCandidateLifecycle(t *testing.T) {
	f := newSynthesisDBFixture(t)
	ctx := t.Context()
	first := f.generation(t, 41000, nil, "Go scheduling evidence.")
	first.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(first, "go scheduling")}
	result, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil || !result.Changed || result.Replayed || len(result.RevisionIDs) != 1 {
		t.Fatalf("first synthesis: %+v %v", result, err)
	}
	noteID := result.Publications[0].NoteID
	detail, err := f.store.GetSynthesisNote(ctx, f.workspace, noteID)
	if err != nil || detail.Publication == nil || detail.Note.Status != domain.SynthesisPendingApproval || detail.PublishedRevision != nil {
		t.Fatalf("candidate detail: %+v %v", detail, err)
	}
	f.count(t, "authoring.working_draft", "workspace_id", string(f.workspace), 0)
	if _, err := f.service.ReadPublishedSynthesisNote(ctx, f.workspace, noteID); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_NOTE_NOT_PUBLISHED") {
		t.Fatalf("unapproved snapshot accepted: %v", err)
	}
	view, err := f.service.OpenSource(ctx, f.workspace, noteID, detail.CurrentRevision.ID, first.Input.Sources[0].Reference)
	if err != nil || view.Availability != domain.MaterialAvailable || view.Text != first.Input.Sources[0].Text {
		t.Fatalf("exact source: %+v %v", view, err)
	}
	if _, err := f.store.GetSynthesisNote(ctx, f.otherWorkspace, noteID); !organizingIntegrationError(err, foundation.ErrorNotFound, organizingapp.ErrorCodeSynthesisNotFound) {
		t.Fatalf("cross workspace read: %v", err)
	}
	changed := first
	changed.Generation.OutputHash = organizingIntegrationHash("changed output")
	if _, err := f.service.ApplyGeneration(ctx, changed.Input, changed.Generation); !organizingIntegrationError(err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeSynthesisIdempotencyConflict) {
		t.Fatalf("changed replay: %v", err)
	}

	// The next source updates an unapproved candidate by retiring its exact old
	// proposal, preserving the old projection and its original content.
	oldRevision := *detail.CurrentRevision
	oldPublication := *detail.Publication
	second := f.generation(t, 42000, []organizingapp.SynthesisGenerationNote{{Note: detail.Note, Revision: oldRevision}}, "Additional scheduler support.")
	second.Generation.Notes = []organizingapp.SynthesisGeneratedNote{{NoteID: noteID, BaseRevisionID: oldRevision.ID, TopicKey: detail.Note.TopicKey, Title: detail.Note.Title, Aliases: detail.Note.Aliases, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddSupport, TargetItemID: oldRevision.Items[0].ID, Sources: []domain.SynthesisSourceRef{second.Input.Sources[0].Reference}}}}}}
	secondResult, err := f.service.ApplyGeneration(ctx, second.Input, second.Generation)
	if err != nil || !secondResult.Changed || len(secondResult.RevisionIDs) != 1 {
		t.Fatalf("continuous pending candidate: %+v %v", secondResult, err)
	}
	var oldStatus string
	if err := f.db.Raw(`SELECT status FROM change_control.proposal WHERE id=?`, string(oldPublication.ProposalID)).Scan(&oldStatus).Error; err != nil || oldStatus != string(changecontroldomain.StatusNeedsRevision) {
		t.Fatalf("old proposal retained/retired: %s %v", oldStatus, err)
	}
	updated, err := f.store.GetSynthesisNote(ctx, f.workspace, noteID)
	if err != nil || updated.CurrentRevision.RevisionNo != 2 || updated.CurrentRevision.Items[0].ID != oldRevision.Items[0].ID || len(updated.CurrentRevision.Items[0].Fact.Sources) != 2 || updated.Publication == nil || updated.Publication.ProposalID == oldPublication.ProposalID {
		t.Fatalf("updated note: %+v %v", updated, err)
	}
	history, err := f.store.GetSynthesisRevision(ctx, f.workspace, noteID, oldRevision.ID)
	if err != nil || !reflect.DeepEqual(history, oldRevision) {
		t.Fatalf("immutable previous revision changed: %v", err)
	}
	if err := f.db.Exec(`UPDATE organizing.synthesis_revision SET title='changed' WHERE id=?`, string(oldRevision.ID)).Error; platformpostgres.SQLState(err) != "55000" {
		t.Fatalf("revision append-only: %v", err)
	}
	if err := f.db.Exec(`UPDATE organizing.synthesis_revision_source SET excerpt_hash=? WHERE revision_id=?`, organizingIntegrationHash("forged"), string(oldRevision.ID)).Error; platformpostgres.SQLState(err) != "55000" {
		t.Fatalf("source append-only: %v", err)
	}
	page, err := f.service.ListNotes(ctx, organizingapp.SynthesisListQuery{WorkspaceID: f.workspace, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ItemCount != 1 || page.Items[0].CurrentRevision.RevisionNo != 2 {
		t.Fatalf("summary page: %+v %v", page, err)
	}
	revisions, err := f.service.ListRevisions(ctx, organizingapp.SynthesisRevisionListQuery{WorkspaceID: f.workspace, NoteID: noteID, Limit: 1})
	if err != nil || len(revisions.Items) != 1 || revisions.NextBeforeRevisionNo != 2 {
		t.Fatalf("history cursor: %+v %v", revisions, err)
	}

	// An already-covered third source records consumption without an empty
	// ArticleRevision, semantic projection or publication reservation.
	third := f.generation(t, 43000, []organizingapp.SynthesisGenerationNote{{Note: updated.Note, Revision: *updated.CurrentRevision}}, "Repeated scheduler coverage.")
	third.Generation.Notes = []organizingapp.SynthesisGeneratedNote{{NoteID: noteID, BaseRevisionID: updated.CurrentRevision.ID, TopicKey: updated.Note.TopicKey, Title: updated.Note.Title, Aliases: updated.Note.Aliases, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{}}}}
	noChange, err := f.service.ApplyGeneration(ctx, third.Input, third.Generation)
	if err != nil || noChange.Changed || len(noChange.RevisionIDs) != 0 {
		t.Fatalf("no change: %+v %v", noChange, err)
	}
	f.count(t, "organizing.synthesis_revision", "note_id", string(noteID), 2)
	f.count(t, "authoring.document_publication_reservation", "document_id", string(detail.Note.DocumentID), 2)
	if _, err := f.service.ApplyGeneration(ctx, third.Input, third.Generation); err != nil {
		t.Fatalf("no-change replay: %v", err)
	}

	// A later source removal invalidates newly opened evidence, while exact
	// committed apply replay still succeeds before consulting current sources.
	if err := f.db.Exec(`UPDATE core.source SET removed_at=? WHERE id=?`, f.now.Add(time.Hour), string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.RevisionIDs, result.RevisionIDs) {
		t.Fatalf("historical replay after source drift: %+v %v", replay, err)
	}
	view, err = f.service.OpenSource(ctx, f.workspace, noteID, oldRevision.ID, first.Input.Sources[0].Reference)
	if err != nil || view.Availability != domain.MaterialUnavailable || view.Text != "" {
		t.Fatalf("unavailable historical source substituted: %+v %v", view, err)
	}

	t.Run("source and model fences rollback atomically", func(t *testing.T) {
		blocked := f.generation(t, 44000, nil, "A blocked source.")
		blocked.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(blocked, "blocked")}
		f.validation.err = errors.New("semantic validation has not succeeded")
		if _, err := f.service.ApplyGeneration(ctx, blocked.Input, blocked.Generation); !errors.Is(err, f.validation.err) {
			t.Fatalf("semantic fence bypassed: %v", err)
		}
		f.validation.err = nil
		f.count(t, "organizing.synthesis_apply_receipt", "processing_id", string(blocked.Input.ProcessingID), 0)
		if err := f.db.Exec(`UPDATE core.source SET removed_at=? WHERE id=?`, f.now.Add(time.Hour), string(blocked.Input.SourceEvent.Source.SourceID)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.ApplyGeneration(ctx, blocked.Input, blocked.Generation); !organizingIntegrationError(err, foundation.ErrorVersionConflict, "SYNTHESIS_SOURCE_STALE") {
			t.Fatalf("stale source accepted: %v", err)
		}
		f.count(t, "organizing.synthesis_note", "topic_key", "blocked", 0)
	})

	t.Run("reservation failure rolls back generated authoring and note", func(t *testing.T) {
		candidate := f.generation(t, 45000, nil, "Atomic synthesis evidence.")
		candidate.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(candidate, "atomic")}
		failure := errors.New("injected publication reservation failure")
		original := f.store.dependencies.Authoring
		f.store.dependencies.Authoring = synthesisFailReservation{SynthesisGeneratedAuthoring: original, err: failure}
		_, err := f.service.ApplyGeneration(ctx, candidate.Input, candidate.Generation)
		f.store.dependencies.Authoring = original
		if !errors.Is(err, failure) {
			t.Fatalf("reservation failure: %v", err)
		}
		f.count(t, "organizing.synthesis_note", "topic_key", "atomic", 0)
		f.count(t, "organizing.synthesis_apply_receipt", "processing_id", string(candidate.Input.ProcessingID), 0)
		f.count(t, "core.document", "title", "atomic", 0)
	})

	t.Run("committed response loss recovers without sources or model", func(t *testing.T) {
		candidate := f.generation(t, 46000, nil, "Recoverable synthesis evidence.")
		candidate.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(candidate, "recovery")}
		failure := errors.New("commit succeeded but response was lost")
		f.store.uow = synthesisCommitLoss{delegate: f.uow, err: failure}
		_, err := f.service.ApplyGeneration(ctx, candidate.Input, candidate.Generation)
		f.store.uow = f.uow
		if !errors.Is(err, failure) {
			t.Fatalf("commit response loss: %v", err)
		}
		if err := f.db.Exec(`UPDATE core.source SET removed_at=? WHERE id=?`, f.now.Add(time.Hour), string(candidate.Input.SourceEvent.Source.SourceID)).Error; err != nil {
			t.Fatal(err)
		}
		f.validation.err = errors.New("recovery must not revalidate a model")
		recovered, found, err := f.service.RecoverAppliedGeneration(ctx, f.workspace, candidate.Input.ProcessingID)
		f.validation.err = nil
		if err != nil || !found || !recovered.Replayed || len(recovered.Publications) != 1 {
			t.Fatalf("receipt recovery: %+v %t %v", recovered, found, err)
		}
		f.count(t, "organizing.synthesis_apply_receipt", "processing_id", string(candidate.Input.ProcessingID), 1)
	})

	t.Run("approval and retirement have one winner", func(t *testing.T) {
		candidate := f.generation(t, 47000, nil, "Concurrent evidence.")
		candidate.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(candidate, "concurrent")}
		initial, err := f.service.ApplyGeneration(ctx, candidate.Input, candidate.Generation)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := f.service.GetNote(ctx, f.workspace, initial.Publications[0].NoteID)
		if err != nil {
			t.Fatal(err)
		}
		proposal, err := f.proposals.GetProposal(ctx, detail.Publication.ProposalID)
		if err != nil {
			t.Fatal(err)
		}
		next := f.generation(t, 48000, []organizingapp.SynthesisGenerationNote{{Note: detail.Note, Revision: *detail.CurrentRevision}}, "More concurrent evidence.")
		next.Generation.Notes = []organizingapp.SynthesisGeneratedNote{{NoteID: detail.Note.ID, BaseRevisionID: detail.CurrentRevision.ID, TopicKey: detail.Note.TopicKey, Title: detail.Note.Title, Aliases: detail.Note.Aliases, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddSupport, TargetItemID: detail.CurrentRevision.Items[0].ID, Sources: []domain.SynthesisSourceRef{next.Input.Sources[0].Reference}}}}}}
		var approvalErr, applyErr error
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			head := strings.Repeat("a", 40)
			_, approvalErr = f.proposals.Approve(ctx, changecontroldomain.Approval{ID: organizingIntegrationID(48999), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ChangeHash: proposal.Revision.ChangeHash, Decision: changecontroldomain.DecisionApproved, ApprovedGitHead: &head, DecidedAt: time.Now().UTC()})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, applyErr = f.service.ApplyGeneration(ctx, next.Input, next.Generation)
		}()
		close(start)
		wg.Wait()
		if (approvalErr == nil) == (applyErr == nil) {
			t.Fatalf("approval/retirement must have exactly one winner: approval=%v apply=%v", approvalErr, applyErr)
		}
		f.count(t, "change_control.approval", "proposal_id", string(proposal.ID), map[bool]int{true: 1, false: 0}[approvalErr == nil])
		f.count(t, "organizing.synthesis_revision", "note_id", string(detail.Note.ID), map[bool]int{true: 2, false: 1}[applyErr == nil])
	})

	t.Run("generated content cannot return as original evidence", func(t *testing.T) {
		markdown, err := domain.RenderSynthesisMarkdown(f.workspace, noteID, updated.Note.Title, updated.CurrentRevision.Items)
		if err != nil {
			t.Fatal(err)
		}
		copy := f.generationSource(t, 49000, nil, markdown, "SYNTHESIS_DERIVED_SOURCE_EXCLUDED")
		err = f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			derived, err := f.sources.IsGeneratedSynthesisSourceScoped(ctx, scope, copy.Input.SourceEvent.Source)
			if err == nil && !derived {
				return errors.New("generated source was not excluded")
			}
			return err
		})
		if err != nil {
			t.Fatalf("source-ready derived provenance gate: %v", err)
		}
		ref := domain.SynthesisSourceRef{Source: copy.Input.SourceEvent.Source, SourceSpanID: organizingIntegrationID(49005), ExcerptHash: copy.Input.SourceEvent.Source.ContentHash, Title: "source-49000"}
		view, err := f.sources.OpenSynthesisSource(ctx, ref)
		if err != nil || view.Availability != domain.MaterialUnavailable || view.Text != "" {
			t.Fatalf("derived source was exposed: %+v %v", view, err)
		}
	})

	t.Run("historical sources and aliases cannot grow outside the projection", func(t *testing.T) {
		err := f.db.Exec(`INSERT INTO organizing.synthesis_topic_key(workspace_id,topic_key,note_id) VALUES(?,?,?)`, string(f.workspace), "unrecorded alias", string(noteID)).Error
		if platformpostgres.SQLState(err) != "23514" {
			t.Fatalf("unrecorded historical alias accepted: %v", err)
		}
		err = f.db.Exec(`INSERT INTO organizing.synthesis_revision_source(workspace_id,revision_id,note_id,source_id,source_version_id,content_artifact_id,parse_projection_id,source_span_id,content_hash,excerpt_hash,title)
			SELECT workspace_id,?,note_id,source_id,source_version_id,content_artifact_id,parse_projection_id,source_span_id,content_hash,excerpt_hash,title
			FROM organizing.synthesis_revision_source WHERE revision_id=? AND source_version_id=?`, string(oldRevision.ID), string(updated.CurrentRevision.ID), string(second.Input.SourceEvent.Source.SourceVersionID)).Error
		if platformpostgres.SQLState(err) != "23514" {
			t.Fatalf("unrecorded historical source accepted: %v", err)
		}
		f.count(t, "organizing.synthesis_revision_source", "revision_id", string(oldRevision.ID), 1)
	})

	t.Run("receipt lookup uses the active caller transaction", func(t *testing.T) {
		candidate := f.generation(t, 50000, nil, "Transaction-local receipt evidence.")
		candidate.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(candidate, "scoped receipt")}
		var captured foundation.TransactionScope
		f.store.uow = synthesisTransactionObserver{delegate: f.uow, observe: func(ctx context.Context, scope foundation.TransactionScope) error {
			captured = scope
			result, found, err := f.store.LookupSynthesisApplyResultScoped(ctx, scope, f.workspace, candidate.Input.ProcessingID)
			if err != nil {
				return err
			}
			if !found || !result.Replayed || len(result.RevisionIDs) != 1 {
				return errors.New("uncommitted apply receipt was not visible on the caller transaction")
			}
			_, found, err = f.store.LookupSynthesisApplyResultScoped(ctx, scope, f.otherWorkspace, candidate.Input.ProcessingID)
			if err == nil && found {
				return errors.New("scoped receipt leaked across Workspaces")
			}
			return err
		}}
		t.Cleanup(func() { f.store.uow = f.uow })
		_, err := f.service.ApplyGeneration(ctx, candidate.Input, candidate.Generation)
		f.store.uow = f.uow
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range []foundation.TransactionScope{nil, captured} {
			if _, _, err := f.store.LookupSynthesisApplyResultScoped(ctx, scope, f.workspace, candidate.Input.ProcessingID); err == nil {
				t.Fatal("missing or closed transaction scope was accepted")
			}
		}
	})

	t.Run("candidate cannot commit without its application receipt", func(t *testing.T) {
		candidate := f.generation(t, 51000, nil, "Incomplete transaction evidence.")
		candidate.Generation.Notes = []organizingapp.SynthesisGeneratedNote{f.newTopic(candidate, "missing receipt")}
		candidate.Candidates = []organizingapp.SynthesisCandidateIDs{{NoteID: organizingIntegrationID(51020), DocumentID: organizingIntegrationID(51021), RevisionID: organizingIntegrationID(51022), ArticleRevisionID: organizingIntegrationID(51023), ReservationID: organizingIntegrationID(51024)}}
		delta, err := domain.ApplySynthesisDelta(f.workspace, nil, candidate.Generation.Notes[0].Delta, []domain.SynthesisSourceRef{candidate.Input.Sources[0].Reference})
		if err != nil {
			t.Fatal(err)
		}
		err = f.store.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
			_, err := f.store.appendCandidate(ctx, scope, tx, candidate, 0, nil, delta.Items)
			return err // Deliberately omit the application receipt before COMMIT.
		})
		if platformpostgres.SQLState(err) != "23514" {
			t.Fatalf("incomplete projection committed: %v", err)
		}
		f.count(t, "organizing.synthesis_note", "topic_key", "missing receipt", 0)
		f.count(t, "core.document", "title", "missing receipt", 0)
	})

	t.Run("oversized parser evidence is rejected without substitution", func(t *testing.T) {
		candidate := f.generation(t, 52000, nil, "Managed parser artifact.")
		source := candidate.Input.SourceEvent.Source
		text := strings.Repeat("a", organizingapp.MaxSynthesisSourceExcerptBytes+1)
		ref := domain.SynthesisSourceRef{Source: source, SourceSpanID: organizingIntegrationID(52025), ExcerptHash: organizingIntegrationHash(text), Title: "Parser evidence"}
		// Use another valid artifact range; the base fixture already owns the
		// full range under the ingestion source-span uniqueness constraint.
		err := f.db.Exec(`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,excerpt_hash,evidence_kind,derived_excerpt,parser_version,schema_version,created_at)
			VALUES(?,?,?,?,'paragraph',1,1,0,?,?,'derived_text',?,'v1','v1',?)`, string(ref.SourceSpanID), string(f.workspace), string(source.ContentArtifactID), string(source.ParseProjectionID), len(candidate.Input.Sources[0].Text)-1, ref.ExcerptHash, text, f.now).Error
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.sources.ReadSynthesisSource(ctx, source); !organizingIntegrationError(err, foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT") {
			t.Fatalf("oversized parser input accepted: %v", err)
		}
		view, err := f.sources.OpenSynthesisSource(ctx, ref)
		if err != nil || view.Availability != domain.MaterialUnavailable || view.Text != "" {
			t.Fatalf("oversized source was substituted: %+v %v", view, err)
		}
	})
}

type synthesisDBFixture struct {
	store                     *GORMSynthesisStore
	service                   *organizingapp.SynthesisService
	platform                  *platformpostgres.Pool
	db                        *gorm.DB
	uow                       foundation.UnitOfWork
	authoring                 *authoringpostgres.GORMRepository
	proposals                 *changecontrolpostgres.GORMRepository
	sources                   *organizingowner.GORMSynthesisSourceReader
	artifacts                 *synthesisArtifactFixture
	validation                *synthesisValidationFixture
	workspace, otherWorkspace foundation.ID
	now                       time.Time
}

func newSynthesisDBFixture(t *testing.T) *synthesisDBFixture {
	t.Helper()
	return newSynthesisDBFixtureAtVersion(t, 95)
}

func newSynthesisDBFixtureAtVersion(t *testing.T, version int64) *synthesisDBFixture {
	t.Helper()
	database := testdb.Require(t, testdb.Config{MaxConns: 12, Availability: testdb.FailWhenUnavailable, Migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
		return platformmigration.MigrateAtlasToVersion(ctx, pool, version)
	}})
	platform := database.Pool()
	db, err := platform.GORM()
	if err != nil {
		t.Fatal(err)
	}
	uow, err := platform.UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	f := &synthesisDBFixture{platform: platform, db: db, uow: uow, workspace: organizingIntegrationID(40000), otherWorkspace: organizingIntegrationID(40001), now: time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond), artifacts: &synthesisArtifactFixture{items: map[foundation.ID]retrievalapp.EvidenceArtifact{}}, validation: &synthesisValidationFixture{}}
	seedOrganizingWorkspace(t, t.Context(), platform.DB(), f.workspace, "synthesis-main")
	seedOrganizingWorkspace(t, t.Context(), platform.DB(), f.otherWorkspace, "synthesis-other")
	f.authoring, err = authoringpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	f.proposals, err = changecontrolpostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	ids := foundation.UUIDGenerator{}
	clock := foundation.SystemClock{}
	targets := synthesisTargetFixture{}
	proposals, err := changecontrolapp.NewService(f.proposals, ids, clock, targets, targets)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := authoringchange.NewProposalCreator(proposals, targets)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := authoringapp.NewService(authoringapp.Dependencies{Repository: f.authoring, IDs: ids, Clock: clock, Proposals: creator})
	if err != nil {
		t.Fatal(err)
	}
	retirer, err := authoringchange.NewGeneratedPublicationRetirer(f.proposals)
	if err != nil {
		t.Fatal(err)
	}
	f.sources, err = organizingowner.NewGORMSynthesisSourceReader(platform, f.artifacts)
	if err != nil {
		t.Fatal(err)
	}
	f.store, err = NewGORMSynthesisStore(platform, SynthesisStoreDependencies{Authoring: f.authoring, Retirer: retirer, Sources: f.sources, Validated: f.validation})
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = organizingapp.NewSynthesisService(organizingapp.SynthesisDependencies{Store: f.store, Sources: f.sources, Publications: publisher, IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *synthesisDBFixture) generation(t *testing.T, seed int, notes []organizingapp.SynthesisGenerationNote, content string) organizingapp.SynthesisApplyRecord {
	return f.generationSource(t, seed, notes, content, "")
}

func (f *synthesisDBFixture) generationSource(t *testing.T, seed int, notes []organizingapp.SynthesisGenerationNote, content, expectedSourceError string) organizingapp.SynthesisApplyRecord {
	t.Helper()
	ctx := t.Context()
	id := func(offset int) foundation.ID { return organizingIntegrationID(seed + offset) }
	at := f.now.Add(time.Duration(seed) * time.Microsecond)
	hash := organizingIntegrationHash(content)
	parserHash := organizingIntegrationHash("synthesis fixture parser")
	source := domain.SynthesisSourceVersion{WorkspaceID: f.workspace, SourceID: id(1), SourceVersionID: id(2), ContentArtifactID: id(3), ParseProjectionID: id(4), ContentHash: hash}
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES(?,?,'file',?,?,?)`, []any{string(id(1)), string(f.workspace), fmt.Sprintf("source-%d", seed), fmt.Sprintf("source-%d.md", seed), at}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES(?,?,?,?,?,?)`, []any{string(id(3)), string(f.workspace), hash, len(content), ".knowledge/sources/" + hash, at}},
		{`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES(?,?,?,?,?,?,'text/markdown',?,'passed',?)`, []any{string(id(2)), string(id(1)), string(f.workspace), string(id(3)), hash, len(content), fmt.Sprintf("source-%d.md", seed), at}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,created_at) VALUES(?,?,?,'test-parser','v1',?,'v1',?,?)`, []any{string(id(4)), string(f.workspace), string(id(3)), parserHash, hash, at}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES(?,?,?,?)`, []any{string(id(2)), string(id(4)), string(f.workspace), at}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,excerpt_hash,evidence_kind,derived_excerpt,parser_version,schema_version,created_at) VALUES(?,?,?,?,'paragraph',1,1,0,?,?,'raw_bytes','','v1','v1',?)`, []any{string(id(5)), string(f.workspace), string(id(3)), string(id(4)), len(content), hash, at}},
		{`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at) VALUES(?,?,?,?,'chunked','passed','test-parser','v1',?,'v1','v1',?,1,?,?)`, []any{string(id(6)), string(f.workspace), string(id(2)), string(id(4)), parserHash, fmt.Sprintf("source-%d", seed), at, at}},
		{`INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at) VALUES(?,?,?,1,'{}',?)`, []any{string(id(7)), string(f.workspace), fmt.Sprintf("synthesis.fixture.%d", seed), at}},
		{`INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash) VALUES(?,?,?,'running','{}',1,?,?,?,?)`, []any{string(id(8)), string(f.workspace), string(id(7)), at, at, fmt.Sprintf("synthesis-fixture-%d", seed), organizingIntegrationHash(fmt.Sprint(seed))}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at) VALUES(?,?,'simple','v1',?,'{}',?,?,0,?,'building','["vector"]',1,?,?)`, []any{string(id(12)), string(f.workspace), parserHash, fmt.Sprintf("synthesis-%d", seed), hash, fmt.Sprintf("synthesis-%d", seed), at, at}},
	}
	for _, statement := range statements {
		if err := f.db.WithContext(ctx).Exec(statement.sql, statement.args...).Error; err != nil {
			t.Fatalf("seed source/runtime fixture: %v", err)
		}
	}
	seedOrganizingRunningNodeAttempt(t, ctx, f.platform.DB(), id(8), id(9), id(10), fmt.Sprintf("synthesis-node-%d", seed), at.Add(2*time.Hour), at)
	agent, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	model := agentdomain.ModelRun{ID: id(11), WorkspaceID: f.workspace, WorkflowRunID: id(8), NodeRunID: id(9), NodeAttemptID: id(10), Model: agentdomain.ModelRef{AdapterName: "fixture", AdapterVersion: "v1", ModelID: "fixture", ModelVersion: "v1"}, Profile: agentdomain.ModelProfileRef{ID: "fixture", Version: "v1"}, Prompt: agentdomain.PromptRef{ID: "synthesis-fixture", Version: "v1"}, Schema: agentdomain.SchemaRef{ID: "synthesis-fixture", Version: "v1"}, ReducedSchema: agentdomain.SchemaRef{ID: "synthesis-fixture-reduced", Version: "v1"}, Retrieval: agentdomain.RetrievalRef{IndexVersionID: id(12)}, Status: agentdomain.ModelRunRunning, Version: 1, CreatedAt: at, UpdatedAt: at}
	if _, _, err := agent.CreateModelRun(ctx, model); err != nil {
		t.Fatal(err)
	}
	outbox, err := workflowpostgres.NewGORMSourceReadyOutbox(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	ready := ingestiondomain.SourceReady{WorkspaceID: f.workspace, SourceID: id(1), SourceVersionID: id(2), ContentArtifactID: id(3), ParseProjectionID: id(4), ContentHash: hash, IngestionAttemptID: id(6), OccurredAt: at}
	if err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
		return outbox.AppendSourceReadyScoped(ctx, scope, ready)
	}); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := f.db.Raw(`SELECT id FROM workflow.outbox_event WHERE workspace_id=? AND event_type='ingestion.source.ready' AND payload->>'source_version_id'=?`, string(f.workspace), string(id(2))).Scan(&eventID).Error; err != nil || eventID == "" {
		t.Fatalf("source event: %s %v", eventID, err)
	}
	f.artifacts.items[id(2)] = retrievalapp.EvidenceArtifact{WorkspaceID: f.workspace, SourceVersionID: id(2), ContentArtifactID: id(3), ContentHash: hash, ByteSize: int64(len(content)), Bytes: []byte(content)}
	excerpts, err := f.sources.ReadSynthesisSource(ctx, source)
	if expectedSourceError != "" {
		if !organizingIntegrationError(err, foundation.ErrorVersionConflict, expectedSourceError) {
			t.Fatalf("derived source rejection: %v", err)
		}
	} else if err != nil {
		t.Fatalf("open exact source: %v", err)
	}
	input := organizingapp.SynthesisGenerationInput{ProcessingID: id(13), SourceEvent: domain.SynthesisSourceReady{ID: foundation.ID(eventID), Source: source, IngestionAttemptID: id(6), ProcessorVersion: domain.SynthesisProcessorVersion, CreatedAt: at}, WorkflowRunID: id(8), NodeRunID: id(9), NodeAttemptID: id(10), RequestHash: organizingIntegrationHash(fmt.Sprintf("input-%d", seed)), Notes: notes, Sources: excerpts}
	return organizingapp.SynthesisApplyRecord{Input: input, Generation: organizingapp.SynthesisGenerationResult{ModelRunID: id(11), RequestHash: input.RequestHash, OutputHash: organizingIntegrationHash(fmt.Sprintf("output-%d", seed))}, AppliedAt: time.Now().UTC()}
}

func (f *synthesisDBFixture) newTopic(record organizingapp.SynthesisApplyRecord, topic string) organizingapp.SynthesisGeneratedNote {
	itemID, _ := foundation.UUIDGenerator{}.New()
	return organizingapp.SynthesisGeneratedNote{TopicKey: topic, Title: topic, Aliases: []string{}, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &domain.SynthesisItem{ID: itemID, Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "The scheduler manages runnable work.", Sources: []domain.SynthesisSourceRef{record.Input.Sources[0].Reference}}}}}}}
}

func (f *synthesisDBFixture) count(t *testing.T, table, column, value string, want int) {
	t.Helper()
	var count int64
	if err := f.db.Table(table).Where(column+"=?", value).Count(&count).Error; err != nil || count != int64(want) {
		t.Fatalf("%s %s count=%d want=%d err=%v", table, column, count, want, err)
	}
}

type synthesisArtifactFixture struct {
	items map[foundation.ID]retrievalapp.EvidenceArtifact
}

func (f *synthesisArtifactFixture) ReadEvidenceArtifact(_ context.Context, w, v foundation.ID) (retrievalapp.EvidenceArtifact, error) {
	artifact, found := f.items[v]
	if !found || artifact.WorkspaceID != w {
		return retrievalapp.EvidenceArtifact{}, errors.New("fixture artifact missing")
	}
	return artifact, nil
}

type synthesisValidationFixture struct{ err error }

func (f *synthesisValidationFixture) VerifyValidatedSynthesisGenerationScoped(ctx context.Context, scope foundation.TransactionScope, input organizingapp.SynthesisGenerationInput, result organizingapp.SynthesisGenerationResult) error {
	if _, err := platformpostgres.GORMTransaction(scope); err != nil {
		return err
	}
	if f.err != nil {
		return f.err
	}
	return result.Validate(input)
}

type synthesisTargetFixture struct{}

func (synthesisTargetFixture) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	return "", errors.New("fixture has no published files")
}
func (synthesisTargetFixture) EnsureTargetAbsent(context.Context, foundation.ID, string, string) error {
	return nil
}
func (synthesisTargetFixture) CaptureApprovalSnapshot(context.Context, foundation.ID) (changecontroldomain.GitSnapshot, error) {
	return changecontroldomain.GitSnapshot{}, errors.New("fixture does not perform approval through HTTP")
}

type synthesisFailReservation struct {
	SynthesisGeneratedAuthoring
	err error
}

func (f synthesisFailReservation) ReservePublicationScoped(context.Context, foundation.TransactionScope, authoringapp.ReservePublicationRecord) (authoringapp.PublicationPreparation, error) {
	return authoringapp.PublicationPreparation{}, f.err
}

type synthesisCommitLoss struct {
	delegate foundation.UnitOfWork
	err      error
}

type synthesisTransactionObserver struct {
	delegate foundation.UnitOfWork
	observe  func(context.Context, foundation.TransactionScope) error
}

func (f synthesisTransactionObserver) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	return f.delegate.Within(ctx, options, func(ctx context.Context, scope foundation.TransactionScope) error {
		if err := work(ctx, scope); err != nil {
			return err
		}
		return f.observe(ctx, scope)
	})
}

func (f synthesisCommitLoss) Within(ctx context.Context, options foundation.TransactionOptions, work foundation.TransactionFunc) error {
	if err := f.delegate.Within(ctx, options, work); err != nil {
		return err
	}
	return f.err
}
