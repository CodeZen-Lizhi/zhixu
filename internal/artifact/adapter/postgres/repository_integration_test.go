//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGORMRepositoryPostgreSQLCreateTransitionReplayAndWorkspaceIsolation(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	workspaceID := artifactIntegrationID(9001)
	otherWorkspaceID := artifactIntegrationID(9002)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-gorm-a")
	seedArtifactWorkspace(t, ctx, pool, otherWorkspaceID, "artifact-gorm-b")
	now := time.Now().UTC().Truncate(time.Microsecond)
	state := artifactIntegrationPlan(t, workspaceID, 9011, 9012, now)
	binding := artifactIntegrationBinding(workspaceID, state.Artifact.ID, "gorm-plan", 'a', artifactapp.CommandPlan, 0)
	created, err := repository.Create(ctx, artifactapp.CreateRecord{Binding: binding, State: state})
	if err != nil || created.Replayed || created.State.Artifact.ID != state.Artifact.ID {
		t.Fatalf("gorm create result=%#v err=%v", created, err)
	}
	replayed, err := repository.Create(ctx, artifactapp.CreateRecord{Binding: binding, State: state})
	if err != nil || !replayed.Replayed || replayed.State.Artifact.ID != state.Artifact.ID {
		t.Fatalf("gorm create replay=%#v err=%v", replayed, err)
	}
	if _, err := repository.Get(ctx, otherWorkspaceID, state.Artifact.ID); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeNotFound) {
		t.Fatalf("gorm cross-workspace get err=%v", err)
	}

	outlinedArtifact, outlinedRevision, err := artifactdomain.SubmitOutline(state.Artifact, state.Revision, artifactIntegrationID(9013), []artifactdomain.OutlineSection{{Key: "scope", Title: "Scope"}}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	outlined := artifactapp.State{Artifact: outlinedArtifact, Revision: outlinedRevision}
	transitionBinding := artifactIntegrationBinding(workspaceID, state.Artifact.ID, "gorm-outline", 'b', artifactapp.CommandSubmitOutline, state.Artifact.Version)
	transitioned, err := repository.Transition(ctx, artifactapp.TransitionRecord{Binding: transitionBinding, CurrentRevisionID: state.Revision.ID, State: outlined, NewRevision: true})
	if err != nil || transitioned.Replayed || transitioned.State.Revision.ID != outlined.Revision.ID {
		t.Fatalf("gorm transition result=%#v err=%v", transitioned, err)
	}
	replayedTransition, err := repository.Transition(ctx, artifactapp.TransitionRecord{Binding: transitionBinding, CurrentRevisionID: state.Revision.ID, State: outlined, NewRevision: true})
	if err != nil || !replayedTransition.Replayed || replayedTransition.State.Revision.ID != outlined.Revision.ID {
		t.Fatalf("gorm transition replay=%#v err=%v", replayedTransition, err)
	}
	if _, err := repository.Transition(ctx, artifactapp.TransitionRecord{Binding: artifactIntegrationBinding(workspaceID, state.Artifact.ID, "gorm-stale", 'c', artifactapp.CommandSubmitOutline, state.Artifact.Version), CurrentRevisionID: state.Revision.ID, State: outlined, NewRevision: true}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeVersionConflict) {
		t.Fatalf("gorm stale transition err=%v", err)
	}
	loaded, err := repository.Get(ctx, workspaceID, state.Artifact.ID)
	if err != nil || loaded.Artifact.Version != outlined.Artifact.Version || loaded.Revision.ID != outlined.Revision.ID {
		t.Fatalf("gorm loaded=%#v err=%v", loaded, err)
	}
	page, err := repository.List(ctx, artifactapp.ListQuery{WorkspaceID: workspaceID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Artifact.ID != state.Artifact.ID || page.Next != nil {
		t.Fatalf("gorm list=%#v err=%v", page, err)
	}
}

func TestGORMRepositoryPostgreSQLDocumentSourceProjectionFailsClosed(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	workspaceID := artifactIntegrationID(9201)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-gorm-v2")
	now := time.Now().UTC().Truncate(time.Microsecond)

	documentID := artifactIntegrationID(9204)
	articleRevisionID := artifactIntegrationID(9205)
	articleHash := artifactIntegrationHash('d')
	if _, err := pool.Exec(ctx, `INSERT INTO core.document(
		id,workspace_id,canonical_path,title,lifecycle_status,version,created_at,updated_at
	) VALUES($1,$2,'artifact-gorm-v2.md','Artifact GORM V2','DRAFT',1,$3,$3)`,
		string(documentID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,parent_revision_id,revision_no,content,content_hash,status,
		optimization_mode,created_by_type,created_at
	) VALUES($1,$2,$3,NULL,1,'document-backed artifact content',$4,'DRAFT','NONE','USER',$5)`,
		string(articleRevisionID), string(workspaceID), string(documentID), articleHash, now); err != nil {
		t.Fatal(err)
	}

	planned := artifactIntegrationPlan(t, workspaceID, 9202, 9203, now)
	if _, err := repository.Create(ctx, artifactapp.CreateRecord{
		Binding: artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "gorm-v2-plan", 'a', artifactapp.CommandPlan, 0),
		State:   planned,
	}); err != nil {
		t.Fatal(err)
	}
	outlinedArtifact, outlinedRevision, err := artifactdomain.SubmitOutline(
		planned.Artifact,
		planned.Revision,
		artifactIntegrationID(9206),
		[]artifactdomain.OutlineSection{{Key: "source", Title: "Verified source"}},
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	outlined := artifactapp.State{Artifact: outlinedArtifact, Revision: outlinedRevision}
	artifactIntegrationTransition(t, repository,
		artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "gorm-v2-outline", 'b', artifactapp.CommandSubmitOutline, planned.Artifact.Version),
		planned, outlined, true, nil, nil,
	)
	approvedArtifact, approvedRevision, err := artifactdomain.ApproveOutline(
		outlined.Artifact,
		outlined.Revision,
		artifactIntegrationID(9207),
		now.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	approved := artifactapp.State{Artifact: approvedArtifact, Revision: approvedRevision}
	artifactIntegrationTransition(t, repository,
		artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "gorm-v2-approve", 'c', artifactapp.CommandApproveOutline, outlined.Artifact.Version),
		outlined, approved, true, nil, nil,
	)
	source := artifactdomain.DocumentSource{
		DocumentID: documentID, ArticleRevisionID: articleRevisionID, RevisionNo: 1,
		VerifiedContentHash: articleHash, Verified: true,
	}
	section := artifactdomain.Section{
		Key: "source", Title: "Verified source", Content: "Content from an immutable document revision.",
		Citations: []artifactdomain.Citation{}, DocumentSources: []artifactdomain.DocumentSource{source},
		Coverage: artifactdomain.Coverage{SectionKey: "source", Status: artifactdomain.CoverageCovered, Gaps: []artifactdomain.Gap{}},
	}
	draftArtifact, draftRevision, err := artifactdomain.RecordSection(
		approved.Artifact,
		approved.Revision,
		artifactIntegrationID(9208),
		section,
		artifactdomain.CreatorHuman,
		nil,
		now.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	draft := artifactapp.State{Artifact: draftArtifact, Revision: draftRevision}
	invalidSource := source
	invalidSource.VerifiedContentHash = artifactIntegrationHash('f')
	invalidSection := section
	invalidSection.DocumentSources = []artifactdomain.DocumentSource{invalidSource}
	invalidArtifact, invalidRevision, err := artifactdomain.RecordSection(
		approved.Artifact,
		approved.Revision,
		artifactIntegrationID(9209),
		invalidSection,
		artifactdomain.CreatorHuman,
		nil,
		now.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Transition(ctx, artifactapp.TransitionRecord{
		Binding:           artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "gorm-v2-invalid-source", 'd', artifactapp.CommandRecordSection, approved.Artifact.Version),
		CurrentRevisionID: approved.Revision.ID,
		State:             artifactapp.State{Artifact: invalidArtifact, Revision: invalidRevision},
		NewRevision:       true,
	})
	if !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeResultInconsistent) {
		t.Fatalf("GORM mismatched document source err=%v", err)
	}
	artifactIntegrationPostgresCode(t, err, "23514")
	var invalidRevisions, invalidReceipts int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM learning.artifact_revision WHERE id=$1),
		(SELECT count(*) FROM learning.artifact_command WHERE workspace_id=$2 AND idempotency_key='gorm-v2-invalid-source')`,
		string(invalidRevision.ID), string(workspaceID)).Scan(&invalidRevisions, &invalidReceipts); err != nil {
		t.Fatal(err)
	}
	if invalidRevisions != 0 || invalidReceipts != 0 {
		t.Fatalf("GORM rejected v2 source left revisions=%d receipts=%d", invalidRevisions, invalidReceipts)
	}
	artifactIntegrationTransition(t, repository,
		artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "gorm-v2-record", 'e', artifactapp.CommandRecordSection, approved.Artifact.Version),
		approved, draft, true, nil, nil,
	)

	loaded, err := repository.Get(ctx, workspaceID, planned.Artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if schema, schemaErr := artifactdomain.RevisionSchemaVersion(loaded.Revision); schemaErr != nil || schema != artifactdomain.RevisionSchemaV2 {
		t.Fatalf("loaded schema=%s err=%v", schema, schemaErr)
	}
	if len(loaded.Revision.Sections) != 1 || len(loaded.Revision.Sections[0].DocumentSources) != 1 ||
		!reflect.DeepEqual(loaded.Revision.Sections[0].DocumentSources[0], source) {
		t.Fatalf("loaded document sources=%#v", loaded.Revision.Sections)
	}
	var projectedDocumentID, projectedArticleRevisionID, projectedHash string
	var projectedRevisionNo int64
	if err := pool.QueryRow(ctx, `SELECT document_id::text,article_revision_id::text,revision_no,content_hash
		FROM learning.artifact_revision_document_source
		WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(draft.Revision.ID)).Scan(
		&projectedDocumentID, &projectedArticleRevisionID, &projectedRevisionNo, &projectedHash,
	); err != nil {
		t.Fatal(err)
	}
	if projectedDocumentID != string(documentID) || projectedArticleRevisionID != string(articleRevisionID) ||
		projectedRevisionNo != source.RevisionNo || projectedHash != source.VerifiedContentHash {
		t.Fatalf("document source projection=%s/%s/%d/%s", projectedDocumentID, projectedArticleRevisionID, projectedRevisionNo, projectedHash)
	}

	triggerDisabled := true
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_revision_document_source DISABLE TRIGGER artifact_revision_document_source_append_only`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if triggerDisabled {
			if _, cleanupErr := pool.Exec(context.Background(), `ALTER TABLE learning.artifact_revision_document_source ENABLE TRIGGER artifact_revision_document_source_append_only`); cleanupErr != nil {
				t.Errorf("restore document source append-only trigger: %v", cleanupErr)
			}
		}
	})
	if _, err := pool.Exec(ctx, `DELETE FROM learning.artifact_revision_document_source WHERE workspace_id=$1 AND revision_id=$2`, string(workspaceID), string(draft.Revision.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE learning.artifact_revision_document_source ENABLE TRIGGER artifact_revision_document_source_append_only`); err != nil {
		t.Fatal(err)
	}
	triggerDisabled = false

	if _, err := repository.Get(ctx, workspaceID, planned.Artifact.ID); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeResultInconsistent) {
		t.Fatalf("missing document source projection get err=%v", err)
	}
	if _, err := repository.List(ctx, artifactapp.ListQuery{WorkspaceID: workspaceID, Limit: 10}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeResultInconsistent) {
		t.Fatalf("missing document source projection list err=%v", err)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("GORM projection validation retained %d PostgreSQL connections", acquired)
	}
}

func TestRepositoryPostgreSQLWorkspaceCASReceiptsAndImmutableBindings(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	workspaceA := artifactIntegrationID(1)
	workspaceB := artifactIntegrationID(2)
	seedArtifactWorkspace(t, ctx, pool, workspaceA, "artifact-integration-a")
	seedArtifactWorkspace(t, ctx, pool, workspaceB, "artifact-integration-b")

	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	state := artifactIntegrationPlan(t, workspaceA, 11, 12, now)
	planBinding := artifactIntegrationBinding(workspaceA, state.Artifact.ID, "plan-a", 'a', artifactapp.CommandPlan, 0)
	created, err := repository.Create(ctx, artifactapp.CreateRecord{Binding: planBinding, State: state})
	if err != nil {
		t.Fatalf("create artifact: %v (cause: %v)", err, errors.Unwrap(err))
	}
	if created.Replayed || created.State.Artifact.Version != 1 {
		t.Fatalf("created result = %#v", created)
	}

	replayed, err := repository.Create(ctx, artifactapp.CreateRecord{Binding: planBinding, State: state})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.State.Artifact.ID != state.Artifact.ID || replayed.RequestHash != planBinding.RequestHash {
		t.Fatalf("receipt replay = %#v", replayed)
	}
	conflictingBinding := planBinding
	conflictingBinding.RequestHash = artifactIntegrationHash('b')
	if _, err := repository.Create(ctx, artifactapp.CreateRecord{Binding: conflictingBinding, State: state}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeIdempotencyConflict) {
		t.Fatalf("conflicting idempotency error = %v", err)
	}

	if _, err := repository.Get(ctx, workspaceB, state.Artifact.ID); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeNotFound) {
		t.Fatalf("cross-workspace get error = %v", err)
	}
	stateB := artifactIntegrationPlan(t, workspaceB, 21, 22, now.Add(time.Second))
	if _, err := repository.Create(ctx, artifactapp.CreateRecord{
		Binding: artifactIntegrationBinding(workspaceB, stateB.Artifact.ID, "plan-b", 'c', artifactapp.CommandPlan, 0),
		State:   stateB,
	}); err != nil {
		t.Fatal(err)
	}
	for _, workspaceID := range []foundation.ID{workspaceA, workspaceB} {
		page, err := repository.List(ctx, artifactapp.ListQuery{WorkspaceID: workspaceID, Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].Artifact.WorkspaceID != workspaceID || page.Next != nil {
			t.Fatalf("workspace=%s list=%#v err=%v", workspaceID, page, err)
		}
	}

	outline := []artifactdomain.OutlineSection{{Key: "gap", Title: "Known gap"}}
	outlinedArtifact, outlinedRevision, err := artifactdomain.SubmitOutline(state.Artifact, state.Revision, artifactIntegrationID(13), outline, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	outlined := artifactapp.State{Artifact: outlinedArtifact, Revision: outlinedRevision}
	outlineBinding := artifactIntegrationBinding(workspaceA, state.Artifact.ID, "outline-a", 'd', artifactapp.CommandSubmitOutline, state.Artifact.Version)
	if _, err := repository.Transition(ctx, artifactapp.TransitionRecord{
		Binding: outlineBinding, CurrentRevisionID: state.Revision.ID, State: outlined, NewRevision: true,
	}); err != nil {
		t.Fatal(err)
	}
	staleBinding := artifactIntegrationBinding(workspaceA, state.Artifact.ID, "outline-stale", 'e', artifactapp.CommandSubmitOutline, state.Artifact.Version)
	if _, err := repository.Transition(ctx, artifactapp.TransitionRecord{
		Binding: staleBinding, CurrentRevisionID: state.Revision.ID, State: outlined, NewRevision: true,
	}); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeVersionConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE learning.artifact_revision SET content_markdown='tampered' WHERE id=$1`, string(state.Revision.ID)); err == nil {
		t.Fatal("immutable revision accepted mutation")
	} else {
		artifactIntegrationPostgresCode(t, err, "55000")
	}

	approvedOutlineArtifact, approvedOutlineRevision, err := artifactdomain.ApproveOutline(outlined.Artifact, outlined.Revision, artifactIntegrationID(14), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approvedOutline := artifactapp.State{Artifact: approvedOutlineArtifact, Revision: approvedOutlineRevision}
	artifactIntegrationTransition(t, repository, artifactIntegrationBinding(workspaceA, state.Artifact.ID, "approve-outline", 'f', artifactapp.CommandApproveOutline, outlined.Artifact.Version), outlined, approvedOutline, true, nil, nil)

	gapSection := artifactdomain.Section{
		Key: "gap", Title: "Known gap", Content: "", Citations: []artifactdomain.Citation{},
		Coverage: artifactdomain.Coverage{SectionKey: "gap", Status: artifactdomain.CoverageGap, Gaps: []artifactdomain.Gap{{Code: "NO_SOURCE", Description: "approved knowledge is unavailable"}}},
	}
	draftArtifact, draftRevision, err := artifactdomain.RecordSection(approvedOutline.Artifact, approvedOutline.Revision, artifactIntegrationID(15), gapSection, artifactdomain.CreatorHuman, nil, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	draft := artifactapp.State{Artifact: draftArtifact, Revision: draftRevision}
	artifactIntegrationTransition(t, repository, artifactIntegrationBinding(workspaceA, state.Artifact.ID, "record-gap", '1', artifactapp.CommandRecordSection, approvedOutline.Artifact.Version), approvedOutline, draft, true, nil, nil)
	if persisted, err := repository.Get(ctx, workspaceA, state.Artifact.ID); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(persisted.Revision, draft.Revision) {
		t.Fatalf("persisted revision differs from transition revision: persisted=%#v transition=%#v", persisted.Revision, draft.Revision)
	}

	approvedArtifact, err := artifactdomain.ApproveDraft(draft.Artifact, draft.Revision, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approved := artifactapp.State{Artifact: approvedArtifact, Revision: artifactdomain.CloneRevision(draft.Revision)}
	artifactIntegrationTransition(t, repository, artifactIntegrationBinding(workspaceA, state.Artifact.ID, "approve-draft", '2', artifactapp.CommandApproveDraft, draft.Artifact.Version), draft, approved, false, nil, nil)

	exportedArtifact, err := artifactdomain.MarkExported(approved.Artifact, approved.Revision, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	exported := artifactapp.State{Artifact: exportedArtifact, Revision: artifactdomain.CloneRevision(approved.Revision)}
	export := artifactapp.ExportRecord{
		ID: artifactIntegrationID(16), WorkspaceID: workspaceA, ArtifactID: exported.Artifact.ID, RevisionID: exported.Revision.ID,
		ArtifactVersion: exported.Artifact.Version, RevisionNo: exported.Revision.RevisionNo, RevisionHash: exported.Revision.ContentHash,
		OutputPath: fmt.Sprintf(".knowledge/exports/artifacts/%s/%s.md", exported.Artifact.ID, artifactIntegrationID(16)),
		OutputHash: artifactIntegrationHash('3'), OutputSize: 42, ExportedAt: exported.Artifact.UpdatedAt,
	}
	artifactIntegrationTransition(t, repository, artifactIntegrationBinding(workspaceA, state.Artifact.ID, "export", '4', artifactapp.CommandExportMarkdown, approved.Artifact.Version), approved, exported, false, &export, nil)
	loadedExport, err := repository.GetExport(ctx, workspaceA, state.Artifact.ID, export.ID)
	if err != nil || loadedExport.RevisionID != exported.Revision.ID || loadedExport.RevisionHash != exported.Revision.ContentHash || loadedExport.OutputHash != export.OutputHash {
		t.Fatalf("export binding=%#v err=%v", loadedExport, err)
	}
	if _, err := repository.GetExport(ctx, workspaceB, state.Artifact.ID, export.ID); !artifactIntegrationErrorCode(err, artifactapp.ErrorCodeNotFound) {
		t.Fatalf("cross-workspace export error = %v", err)
	}

	proposedArtifact, request, err := artifactdomain.CreatePublicationRequest(exported.Artifact, exported.Revision, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposed := artifactapp.State{Artifact: proposedArtifact, Revision: artifactdomain.CloneRevision(exported.Revision)}
	publication := artifactapp.PublicationRecord{
		WorkspaceID: request.WorkspaceID, ArtifactID: request.ArtifactID, RevisionID: request.RevisionID,
		ArtifactVersion: request.ArtifactVersion, RevisionNo: request.RevisionNo, ContentHash: request.ContentHash,
		ProposalID: artifactIntegrationID(17), IdempotencyKey: "publish", CreatedAt: request.RequestedAt,
	}
	artifactIntegrationTransition(t, repository, artifactIntegrationBinding(workspaceA, state.Artifact.ID, "publish", '5', artifactapp.CommandPublish, exported.Artifact.Version), exported, proposed, false, nil, &publication)
	var persistedRevision, persistedHash, persistedProposal string
	if err := pool.QueryRow(ctx, `SELECT revision_id::text,content_hash,proposal_id::text FROM learning.artifact_publication WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceA), string(state.Artifact.ID)).Scan(&persistedRevision, &persistedHash, &persistedProposal); err != nil {
		t.Fatal(err)
	}
	if persistedRevision != string(request.RevisionID) || persistedHash != request.ContentHash || persistedProposal != string(publication.ProposalID) {
		t.Fatalf("publication binding revision=%s hash=%s proposal=%s", persistedRevision, persistedHash, persistedProposal)
	}
}

func TestRepositoryPostgreSQLTransitionRejectsUnclosedSideFactsWithoutPersistence(t *testing.T) {
	ctx := context.Background()
	repository, platformPool := newArtifactIntegrationGORMRepository(t, ctx)
	pool := platformPool.DB()
	workspaceID := artifactIntegrationID(500)
	seedArtifactWorkspace(t, ctx, pool, workspaceID, "artifact-side-fact-closure")
	now := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)

	planned := artifactIntegrationPlan(t, workspaceID, 501, 502, now)
	if _, err := repository.Create(ctx, artifactapp.CreateRecord{
		Binding: artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "side-fact-plan", 'a', artifactapp.CommandPlan, 0),
		State:   planned,
	}); err != nil {
		t.Fatal(err)
	}
	outlinedArtifact, outlinedRevision, err := artifactdomain.SubmitOutline(
		planned.Artifact,
		planned.Revision,
		artifactIntegrationID(503),
		[]artifactdomain.OutlineSection{{Key: "gap", Title: "Known gap"}},
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	outlined := artifactapp.State{Artifact: outlinedArtifact, Revision: outlinedRevision}

	approved := createApprovedArtifactForConcurrency(t, ctx, repository, workspaceID, now.Add(2*time.Minute))
	exportedArtifact, err := artifactdomain.MarkExported(approved.Artifact, approved.Revision, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	exported := artifactapp.State{Artifact: exportedArtifact, Revision: artifactdomain.CloneRevision(approved.Revision)}
	proposedArtifact, publicationRequest, err := artifactdomain.CreatePublicationRequest(approved.Artifact, approved.Revision, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	proposed := artifactapp.State{Artifact: proposedArtifact, Revision: artifactdomain.CloneRevision(approved.Revision)}

	normalExport := artifactIntegrationExportRecord(outlined, artifactIntegrationID(504), now.Add(5*time.Minute))
	normalPublication := artifactIntegrationPublicationRecord(outlined, artifactIntegrationID(505), "side-fact-normal-publication", now.Add(5*time.Minute))
	exportRecord := artifactIntegrationExportRecord(exported, artifactIntegrationID(506), now.Add(6*time.Minute))
	exportPublication := artifactIntegrationPublicationRecord(exported, artifactIntegrationID(507), "side-fact-export-both", now.Add(6*time.Minute))
	publishExport := artifactIntegrationExportRecord(proposed, artifactIntegrationID(508), now.Add(7*time.Minute))
	publishPublication := artifactapp.PublicationRecord{
		WorkspaceID: publicationRequest.WorkspaceID, ArtifactID: publicationRequest.ArtifactID, RevisionID: publicationRequest.RevisionID,
		ArtifactVersion: publicationRequest.ArtifactVersion, RevisionNo: publicationRequest.RevisionNo, ContentHash: publicationRequest.ContentHash,
		ProposalID: artifactIntegrationID(509), IdempotencyKey: "side-fact-publish-both", CreatedAt: publicationRequest.RequestedAt,
	}

	tests := []struct {
		name   string
		record artifactapp.TransitionRecord
	}{
		{
			name: "ordinary command with legal export",
			record: artifactapp.TransitionRecord{
				Binding:           artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "side-fact-normal-export", 'b', artifactapp.CommandSubmitOutline, planned.Artifact.Version),
				CurrentRevisionID: planned.Revision.ID, State: outlined, NewRevision: true, Export: &normalExport,
			},
		},
		{
			name: "ordinary command with legal publication",
			record: artifactapp.TransitionRecord{
				Binding:           artifactIntegrationBinding(workspaceID, planned.Artifact.ID, "side-fact-normal-publication", 'c', artifactapp.CommandSubmitOutline, planned.Artifact.Version),
				CurrentRevisionID: planned.Revision.ID, State: outlined, NewRevision: true, Publication: &normalPublication,
			},
		},
		{
			name: "export without export fact",
			record: artifactapp.TransitionRecord{
				Binding:           artifactIntegrationBinding(workspaceID, approved.Artifact.ID, "side-fact-export-nil", 'd', artifactapp.CommandExportMarkdown, approved.Artifact.Version),
				CurrentRevisionID: approved.Revision.ID, State: exported,
			},
		},
		{
			name: "export with publication fact",
			record: artifactapp.TransitionRecord{
				Binding:           artifactIntegrationBinding(workspaceID, approved.Artifact.ID, "side-fact-export-both", 'e', artifactapp.CommandExportMarkdown, approved.Artifact.Version),
				CurrentRevisionID: approved.Revision.ID, State: exported, Export: &exportRecord, Publication: &exportPublication,
			},
		},
		{
			name: "publish without publication fact",
			record: artifactapp.TransitionRecord{
				Binding:           artifactIntegrationBinding(workspaceID, approved.Artifact.ID, "side-fact-publish-nil", 'f', artifactapp.CommandPublish, approved.Artifact.Version),
				CurrentRevisionID: approved.Revision.ID, State: proposed,
			},
		},
		{
			name: "publish with export fact",
			record: artifactapp.TransitionRecord{
				Binding:           artifactIntegrationBinding(workspaceID, approved.Artifact.ID, "side-fact-publish-both", '1', artifactapp.CommandPublish, approved.Artifact.Version),
				CurrentRevisionID: approved.Revision.ID, State: proposed, Export: &publishExport, Publication: &publishPublication,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifactIntegrationAssertInvalidTransitionLeavesNoSideEffects(t, ctx, repository, pool, test.record)
		})
	}
}

func newArtifactIntegrationGORMRepository(t *testing.T, ctx context.Context) (*GORMRepository, *platformpostgres.Pool) {
	t.Helper()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 8})
	platformPool := fixture.Pool()
	repository, err := NewGORMRepository(platformPool)
	if err != nil {
		t.Fatal(err)
	}
	return repository, platformPool
}

func seedArtifactWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, name string) {
	t.Helper()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), name, "/tmp/"+name, now); err != nil {
		t.Fatal(err)
	}
}

func artifactIntegrationPlan(t *testing.T, workspaceID foundation.ID, artifactNumber, revisionNumber int, now time.Time) artifactapp.State {
	t.Helper()
	artifact, revision, err := artifactdomain.PlanArtifact(artifactdomain.PlanInput{
		ArtifactID: artifactIntegrationID(artifactNumber), InitialRevisionID: artifactIntegrationID(revisionNumber), WorkspaceID: workspaceID,
		Type: "study-guide", Title: "Artifact integration", ScopeDefinition: "approved source boundary", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifactapp.State{Artifact: artifact, Revision: revision}
}

type artifactIntegrationTransitionRepository interface {
	ReserveExternalTransition(context.Context, artifactapp.CommandBinding) (artifactapp.State, error)
	Transition(context.Context, artifactapp.TransitionRecord) (artifactapp.CommandResult, error)
}

func artifactIntegrationTransition(t *testing.T, repository artifactIntegrationTransitionRepository, binding artifactapp.CommandBinding, current, next artifactapp.State, newRevision bool, export *artifactapp.ExportRecord, publication *artifactapp.PublicationRecord) {
	t.Helper()
	if binding.CommandType == artifactapp.CommandExportMarkdown || binding.CommandType == artifactapp.CommandPublish {
		if _, err := repository.ReserveExternalTransition(context.Background(), binding); err != nil {
			t.Fatalf("reserve %s: %v", binding.CommandType, err)
		}
	}
	result, err := repository.Transition(context.Background(), artifactapp.TransitionRecord{
		Binding: binding, CurrentRevisionID: current.Revision.ID, State: next, NewRevision: newRevision, Export: export, Publication: publication,
	})
	if err != nil {
		t.Fatalf("transition %s: %v (cause: %v)", binding.CommandType, err, errors.Unwrap(err))
	}
	if result.Replayed || result.State.Artifact.Version != next.Artifact.Version || result.State.Revision.ID != next.Revision.ID {
		t.Fatalf("transition result=%#v", result)
	}
}

func artifactIntegrationExportRecord(state artifactapp.State, id foundation.ID, exportedAt time.Time) artifactapp.ExportRecord {
	return artifactapp.ExportRecord{
		ID: id, WorkspaceID: state.Artifact.WorkspaceID, ArtifactID: state.Artifact.ID, RevisionID: state.Revision.ID,
		ArtifactVersion: state.Artifact.Version, RevisionNo: state.Revision.RevisionNo, RevisionHash: state.Revision.ContentHash,
		OutputPath: fmt.Sprintf(".knowledge/exports/artifacts/%s/%s.md", state.Artifact.ID, id),
		OutputHash: artifactIntegrationHash('a'), OutputSize: 42, ExportedAt: exportedAt,
	}
}

func artifactIntegrationPublicationRecord(state artifactapp.State, proposalID foundation.ID, idempotencyKey string, createdAt time.Time) artifactapp.PublicationRecord {
	return artifactapp.PublicationRecord{
		WorkspaceID: state.Artifact.WorkspaceID, ArtifactID: state.Artifact.ID, RevisionID: state.Revision.ID,
		ArtifactVersion: state.Artifact.Version, RevisionNo: state.Revision.RevisionNo, ContentHash: state.Revision.ContentHash,
		ProposalID: proposalID, IdempotencyKey: idempotencyKey, CreatedAt: createdAt,
	}
}

type artifactIntegrationSideEffectSnapshot struct {
	Status       string
	Version      int64
	RevisionID   string
	Receipts     int64
	Exports      int64
	Publications int64
	Reservations int64
}

func artifactIntegrationAssertInvalidTransitionLeavesNoSideEffects(t *testing.T, ctx context.Context, repository *GORMRepository, pool *pgxpool.Pool, record artifactapp.TransitionRecord) {
	t.Helper()
	before := artifactIntegrationSideEffectState(t, ctx, pool, record.Binding.WorkspaceID, record.Binding.ArtifactID)
	if before.Exports != 0 || before.Publications != 0 || before.Reservations != 0 {
		t.Fatalf("test setup already has external side effects: %+v", before)
	}
	if _, err := repository.Transition(ctx, record); err == nil {
		t.Fatal("transition unexpectedly succeeded")
	} else {
		var typed *foundation.Error
		if !errors.As(err, &typed) || typed.Kind != foundation.ErrorInvalidInput || typed.Code != artifactapp.ErrorCodeRequestInvalid || typed.Retryable {
			t.Fatalf("transition error=%v, want stable invalid input", err)
		}
	}
	after := artifactIntegrationSideEffectState(t, ctx, pool, record.Binding.WorkspaceID, record.Binding.ArtifactID)
	if after != before {
		t.Fatalf("rejected transition changed durable Artifact state: before=%+v after=%+v", before, after)
	}
	var receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_command WHERE workspace_id=$1 AND idempotency_key=$2`, string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("rejected transition receipt count=%d want=0 err=%v", receipts, err)
	}
}

func artifactIntegrationSideEffectState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, artifactID foundation.ID) artifactIntegrationSideEffectSnapshot {
	t.Helper()
	var snapshot artifactIntegrationSideEffectSnapshot
	if err := pool.QueryRow(ctx, `
		SELECT a.status,a.version,a.current_revision_id::text,
		       (SELECT count(*) FROM learning.artifact_command c WHERE c.workspace_id=$1 AND c.artifact_id=$2),
		       (SELECT count(*) FROM learning.artifact_export e WHERE e.workspace_id=$1 AND e.artifact_id=$2),
		       (SELECT count(*) FROM learning.artifact_publication p WHERE p.workspace_id=$1 AND p.artifact_id=$2),
		       (SELECT count(*) FROM learning.artifact_external_transition_reservation r WHERE r.workspace_id=$1 AND r.artifact_id=$2)
		FROM learning.artifact a
		WHERE a.workspace_id=$1 AND a.id=$2`, string(workspaceID), string(artifactID)).Scan(
		&snapshot.Status, &snapshot.Version, &snapshot.RevisionID, &snapshot.Receipts, &snapshot.Exports, &snapshot.Publications, &snapshot.Reservations,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func artifactIntegrationBinding(workspaceID, artifactID foundation.ID, key string, hashByte byte, command artifactapp.CommandType, version int64) artifactapp.CommandBinding {
	return artifactapp.CommandBinding{WorkspaceID: workspaceID, ArtifactID: artifactID, IdempotencyKey: key, RequestHash: artifactIntegrationHash(hashByte), CommandType: command, ExpectedVersion: version}
}

func artifactIntegrationHash(byteValue byte) string { return strings.Repeat(string(byteValue), 64) }

func artifactIntegrationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("93000000-0000-4000-8000-%012d", value))
}

func artifactIntegrationErrorCode(err error, want string) bool {
	var typed *foundation.Error
	return errors.As(err, &typed) && typed.Code == want
}

func artifactIntegrationPostgresCode(t *testing.T, err error, want string) {
	t.Helper()
	var typed *pgconn.PgError
	if !errors.As(err, &typed) || typed.Code != want {
		t.Fatalf("postgres error=%v want code=%s", err, want)
	}
}
