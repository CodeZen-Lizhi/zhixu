//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryPostgreSQLPublicationReservationCompletionReplayAndReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
	workspaceID := authoringIntegrationID(300)
	otherWorkspaceID := authoringIntegrationID(301)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-publication")
	seedAuthoringWorkspace(t, ctx, pool, otherWorkspaceID, "authoring-publication-other")
	now := time.Date(2026, 8, 3, 11, 0, 0, 987654321, time.UTC)

	documentID := authoringIntegrationID(302)
	revisionID := authoringIntegrationID(303)
	content := "# Java AI\n\nFirst frozen revision."
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, documentID, revisionID, 1, "notes/java-ai.md", "Java AI", content, "", now)
	binding := authoringPublishBinding(t, workspaceID, documentID, revisionID, "publish-java-ai")
	reserved, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(304), ReservedAt: now,
	})
	if err != nil || reserved.Replayed || reserved.Reservation.TargetMode != authoringdomain.ProposalTargetCreateOnly ||
		reserved.Reservation.BaseVersion != reserved.Reservation.AbsenceToken || reserved.Reservation.Status != authoringapp.PublicationReservationPending {
		t.Fatalf("reserved=%#v err=%v", reserved, err)
	}

	// A later Freeze must not orphan a Proposal whose durable reservation was
	// already returned or created before a response loss.
	laterRevisionID := authoringIntegrationID(305)
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, documentID, laterRevisionID, 2,
		"notes/java-ai.md", "Java AI", content+"\n\nSecond frozen revision.", revisionID, now.Add(time.Second))
	replayedReservation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(399), ReservedAt: now.Add(2 * time.Second),
	})
	if err != nil || !replayedReservation.Replayed || replayedReservation.Reservation != reserved.Reservation ||
		replayedReservation.Revision.ID != revisionID {
		t.Fatalf("reservation replay=%#v err=%v", replayedReservation, err)
	}

	proposalID := authoringIntegrationID(306)
	proposalRevisionID := authoringIntegrationID(307)
	seedAuthoringPublicationProposal(t, ctx, pool, reserved.Reservation, content, proposalID, proposalRevisionID, now.Add(3*time.Second))
	completed, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: binding, ReservationID: reserved.Reservation.ID, PublicationID: authoringIntegrationID(308),
		ProposalID: proposalID, ProposalRevisionID: proposalRevisionID, CompletedAt: now.Add(4 * time.Second),
	})
	if err != nil || completed.Replayed || completed.Publication.Status != authoringdomain.PublicationPending ||
		completed.Publication.TargetMode != authoringdomain.ProposalTargetCreateOnly || completed.Publication.ArticleRevisionID != revisionID {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	replayedCompletion, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: binding, ReservationID: reserved.Reservation.ID, PublicationID: authoringIntegrationID(398),
		ProposalID: proposalID, ProposalRevisionID: proposalRevisionID, CompletedAt: now.Add(5 * time.Second),
	})
	if err != nil || !replayedCompletion.Replayed || replayedCompletion.Publication != completed.Publication {
		t.Fatalf("completion replay=%#v err=%v", replayedCompletion, err)
	}
	closedReplay, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(397), ReservedAt: now.Add(6 * time.Second),
	})
	if err != nil || !closedReplay.Replayed || closedReplay.Existing == nil || *closedReplay.Existing != completed.Publication {
		t.Fatalf("closed reservation replay=%#v err=%v", closedReplay, err)
	}

	detail, err := repository.GetDocumentDetail(ctx, workspaceID, documentID)
	if err != nil || detail.CurrentRevision == nil || detail.CurrentRevision.ID != laterRevisionID || detail.Publication != nil {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}
	if _, err := repository.GetDocumentDetail(ctx, otherWorkspaceID, documentID); !authoringIntegrationError(err, foundation.ErrorNotFound, authoringapp.ErrorCodeNotFound) {
		t.Fatalf("cross-workspace detail error=%v", err)
	}
	overview, err := repository.GetOverview(ctx, workspaceID, 10)
	if err != nil || len(overview.PendingPublications) != 1 || overview.PendingPublications[0] != completed.Publication ||
		len(overview.CompletedDocuments) != 0 {
		t.Fatalf("overview=%#v err=%v", overview, err)
	}
	otherOverview, err := repository.GetOverview(ctx, otherWorkspaceID, 10)
	if err != nil || len(otherOverview.PendingPublications) != 0 || len(otherOverview.CompletedDocuments) != 0 {
		t.Fatalf("other overview=%#v err=%v", otherOverview, err)
	}

	mismatchDocumentID := authoringIntegrationID(310)
	mismatchRevisionID := authoringIntegrationID(311)
	mismatchContent := "# Mismatch\n\nFrozen content."
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, mismatchDocumentID, mismatchRevisionID, 1,
		"notes/mismatch.md", "Mismatch", mismatchContent, "", now.Add(10*time.Second))
	mismatchBinding := authoringPublishBinding(t, workspaceID, mismatchDocumentID, mismatchRevisionID, "publish-mismatch")
	mismatchReservation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: mismatchBinding, ReservationID: authoringIntegrationID(312), ReservedAt: now.Add(11 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	mismatchProposalID := authoringIntegrationID(313)
	mismatchProposalRevisionID := authoringIntegrationID(314)
	seedAuthoringPublicationProposal(t, ctx, pool, mismatchReservation.Reservation, mismatchContent+" changed",
		mismatchProposalID, mismatchProposalRevisionID, now.Add(12*time.Second))
	_, err = repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: mismatchBinding, ReservationID: mismatchReservation.Reservation.ID, PublicationID: authoringIntegrationID(315),
		ProposalID: mismatchProposalID, ProposalRevisionID: mismatchProposalRevisionID, CompletedAt: now.Add(13 * time.Second),
	})
	if !authoringIntegrationError(err, foundation.ErrorConsistencyViolation, authoringapp.ErrorCodeResultInvalid) {
		t.Fatalf("mismatched proposal completion error=%v", err)
	}
	var reservationStatus string
	var bindingCount int
	if err := pool.QueryRow(ctx, `SELECT status FROM authoring.document_publication_reservation WHERE id=$1`,
		string(mismatchReservation.Reservation.ID)).Scan(&reservationStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authoring.document_publication_binding WHERE reservation_id=$1`,
		string(mismatchReservation.Reservation.ID)).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if reservationStatus != string(authoringapp.PublicationReservationPending) || bindingCount != 0 {
		t.Fatalf("mismatch reservation status=%s bindings=%d", reservationStatus, bindingCount)
	}
}

func TestRepositoryPostgreSQLPublicationFinalizerPublishesAndMarksRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
	workspaceID := authoringIntegrationID(800)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-finalizer")
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP - INTERVAL '1 hour'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Microsecond)

	documentID := authoringIntegrationID(801)
	previousRevisionID := authoringIntegrationID(802)
	targetRevisionID := authoringIntegrationID(803)
	previousContent := "# Published\n\nVersion one."
	targetContent := "# Published\n\nVersion two."
	seedAuthoringPublishedDocumentRevisions(t, ctx, pool, workspaceID, documentID, previousRevisionID,
		targetRevisionID, "notes/published.md", previousContent, targetContent, 1800, now)
	binding := authoringPublishBinding(t, workspaceID, documentID, targetRevisionID, "replace-published")
	preparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(804), ReservedAt: now.Add(2 * time.Second),
	})
	if err != nil || preparation.Reservation.TargetMode != authoringdomain.ProposalTargetReplace ||
		preparation.Reservation.BaseVersion != authoringdomain.ComputeContentHash(previousContent) {
		t.Fatalf("replace preparation=%#v err=%v", preparation, err)
	}
	proposal := seedAuthoringPublicationProposal(t, ctx, pool, preparation.Reservation, targetContent,
		authoringIntegrationID(805), authoringIntegrationID(806), now.Add(3*time.Second))
	completed, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: binding, ReservationID: preparation.Reservation.ID, PublicationID: authoringIntegrationID(807),
		ProposalID: proposal.ID, ProposalRevisionID: proposal.Revision.ID, CompletedAt: now.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	gitCommit := seedAuthoringProposalCommit(t, ctx, pool, proposal, preparation.Reservation, targetContent, 820, now.Add(5*time.Second))
	missed, err := repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: workspaceID, ProposalID: authoringIntegrationID(808),
		ProposalRevisionID: authoringIntegrationID(809), Limit: 1, Now: now.Add(6 * time.Second),
	})
	if err != nil || missed != 0 {
		t.Fatalf("mismatched proposal reconcile advanced=%d err=%v", missed, err)
	}
	advanced, err := repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: workspaceID, ProposalID: proposal.ID,
		ProposalRevisionID: proposal.Revision.ID, Limit: 1, Now: now.Add(7 * time.Second),
	})
	if err != nil || advanced != 1 {
		t.Fatalf("reconcile advanced=%d err=%v", advanced, err)
	}
	replayed, err := repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: workspaceID, DocumentID: documentID, Limit: 10, Now: now.Add(8 * time.Second),
	})
	if err != nil || replayed != 0 {
		t.Fatalf("reconcile replay advanced=%d err=%v", replayed, err)
	}
	detail, err := repository.GetDocumentDetail(ctx, workspaceID, documentID)
	if err != nil || detail.CurrentRevision == nil || detail.CurrentRevision.ID != targetRevisionID ||
		detail.CurrentRevision.Status != authoringdomain.RevisionPublished || detail.CurrentRevision.GitCommit != gitCommit ||
		detail.Publication == nil || detail.Publication.ID != completed.Publication.ID ||
		detail.Publication.Status != authoringdomain.PublicationPublished || detail.Publication.GitCommit != gitCommit {
		t.Fatalf("published detail=%#v err=%v", detail, err)
	}
	var previousStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM core.article_revision WHERE id=$1`, string(previousRevisionID)).Scan(&previousStatus); err != nil {
		t.Fatal(err)
	}
	if previousStatus != string(authoringdomain.RevisionSuperseded) {
		t.Fatalf("previous revision status=%s", previousStatus)
	}
	overview, err := repository.GetOverview(ctx, workspaceID, 10)
	if err != nil || len(overview.PendingPublications) != 0 || len(overview.CompletedDocuments) != 1 ||
		overview.CompletedDocuments[0].CurrentPublishedRevisionID != targetRevisionID {
		t.Fatalf("published overview=%#v err=%v", overview, err)
	}

	recoveryDocumentID := authoringIntegrationID(850)
	recoveryPreviousID := authoringIntegrationID(851)
	recoveryTargetID := authoringIntegrationID(852)
	recoveryPreviousContent := "# Recovery\n\nVersion one."
	recoveryTargetContent := "# Recovery\n\nVersion two."
	seedAuthoringPublishedDocumentRevisions(t, ctx, pool, workspaceID, recoveryDocumentID, recoveryPreviousID,
		recoveryTargetID, "notes/recovery.md", recoveryPreviousContent, recoveryTargetContent, 1900, now.Add(10*time.Second))
	recoveryBinding := authoringPublishBinding(t, workspaceID, recoveryDocumentID, recoveryTargetID, "replace-recovery")
	recoveryPreparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: recoveryBinding, ReservationID: authoringIntegrationID(853), ReservedAt: now.Add(12 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveryProposal := seedAuthoringPublicationProposal(t, ctx, pool, recoveryPreparation.Reservation,
		recoveryTargetContent, authoringIntegrationID(854), authoringIntegrationID(855), now.Add(13*time.Second))
	recoveryPublication, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: recoveryBinding, ReservationID: recoveryPreparation.Reservation.ID, PublicationID: authoringIntegrationID(856),
		ProposalID: recoveryProposal.ID, ProposalRevisionID: recoveryProposal.Revision.ID, CompletedAt: now.Add(14 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	seedAuthoringProposalCommit(t, ctx, pool, recoveryProposal, recoveryPreparation.Reservation,
		recoveryTargetContent, 870, now.Add(15*time.Second))
	if _, err := pool.Exec(ctx, `UPDATE core.document
		SET lifecycle_status='ARCHIVED',version=version+1,updated_at=$1
		WHERE id=$2 AND workspace_id=$3`, now.Add(16*time.Second), string(recoveryDocumentID), string(workspaceID)); err != nil {
		t.Fatal(err)
	}
	recoveryAdvanced, err := repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: workspaceID, DocumentID: recoveryDocumentID, Limit: 10, Now: now.Add(17 * time.Second),
	})
	if err != nil || recoveryAdvanced != 1 {
		t.Fatalf("recovery reconcile advanced=%d err=%v", recoveryAdvanced, err)
	}
	recoveryReplay, err := repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: workspaceID, DocumentID: recoveryDocumentID, Limit: 10, Now: now.Add(18 * time.Second),
	})
	if err != nil || recoveryReplay != 0 {
		t.Fatalf("recovery replay advanced=%d err=%v", recoveryReplay, err)
	}
	recoveryDetail, err := repository.GetDocumentDetail(ctx, workspaceID, recoveryDocumentID)
	if err != nil || recoveryDetail.Publication == nil || recoveryDetail.Publication.ID != recoveryPublication.Publication.ID ||
		recoveryDetail.Publication.Status != authoringdomain.PublicationRecoveryRequired ||
		recoveryDetail.Publication.ErrorCode != publicationStateMismatch || recoveryDetail.Publication.Version != 2 {
		t.Fatalf("recovery detail=%#v err=%v", recoveryDetail, err)
	}
}

func TestRepositoryPostgreSQLRestorePublicationAppendsRevisionAndReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
	workspaceID := authoringIntegrationID(2100)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-restore-finalizer")
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP - INTERVAL '1 hour'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Microsecond)
	documentID := authoringIntegrationID(2101)
	currentRevisionID := authoringIntegrationID(2102)
	draftRevisionID := authoringIntegrationID(2103)
	currentContent := "# Restore\n\nCurrent publication."
	targetContent := "# Restore\n\nEarlier verified content."
	seedAuthoringPublishedDocumentRevisions(t, ctx, pool, workspaceID, documentID, currentRevisionID,
		draftRevisionID, "notes/restore.md", currentContent, "# Restore\n\nUnpublished draft.", 2120, now)
	detail, err := repository.GetDocumentDetail(ctx, workspaceID, documentID)
	if err != nil || detail.CurrentRevision == nil {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}

	const writebackIDsStart = 2160
	expectedHead := authoringIntegrationDigest("head:" + string(authoringIntegrationID(writebackIDsStart+6)))[:40]
	restore := changecontroldomain.RestoreDocument{
		WorkspaceID: workspaceID, DocumentID: documentID,
		TargetCommit: authoringIntegrationDigest("restore-target")[:40], ExpectedHead: expectedHead,
		ExpectedDocumentVersion: detail.Document.Version,
		PreviewHash:             authoringIntegrationDigest("restore-preview"),
		CurrentContentHash:      authoringdomain.ComputeContentHash(currentContent),
		TargetContentHash:       authoringdomain.ComputeContentHash(targetContent),
		SchemaVersion:           changecontroldomain.RestoreDocumentSchemaVersion,
	}
	changeHash, err := changecontroldomain.ComputeChangeHashForTarget(
		workspaceID, detail.Document.CanonicalPath, changecontroldomain.TargetModeReplace,
		restore.CurrentContentHash, targetContent,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposalID := authoringIntegrationID(2140)
	proposalRevisionID := authoringIntegrationID(2141)
	const evidence = "Document History verified the target commit and reverse diff"
	const risk = "Restoring can remove newer published information"
	const rollback = "Restore the previous publication through a new proposal"
	requestHash, err := changecontroldomain.ComputeRestoreDocumentRequestHash(
		workspaceID, restore, detail.Document.CanonicalPath, targetContent, evidence,
		changecontroldomain.ProposalRiskLevelHigh, risk, rollback,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := changecontroldomain.Proposal{
		ID: proposalID, WorkspaceID: workspaceID, Type: changecontroldomain.ProposalTypeRestoreDocument,
		RiskLevel: changecontroldomain.ProposalRiskLevelHigh, TargetPath: detail.Document.CanonicalPath,
		IdempotencyKey: "restore-publication-integration", RequestHash: requestHash,
		Status: changecontroldomain.StatusReady, Version: 1, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second),
		Revision: changecontroldomain.Revision{
			ID: proposalRevisionID, ProposalID: proposalID, RevisionNo: 1,
			TargetPath: detail.Document.CanonicalPath, TargetMode: changecontroldomain.TargetModeReplace,
			BaseHash: restore.CurrentContentHash, Content: targetContent, EvidenceSummary: evidence,
			Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash, RestoreDocument: &restore,
			CreatedAt: now.Add(2 * time.Second),
		},
	}
	changeControlRepository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = changeControlRepository.CreateRestoreDocumentProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	reservation := authoringapp.PublicationReservation{
		WorkspaceID: workspaceID, TargetPath: detail.Document.CanonicalPath,
		TargetMode: authoringdomain.ProposalTargetReplace, BaseVersion: restore.CurrentContentHash,
	}
	gitCommit := seedAuthoringProposalCommit(t, ctx, pool, proposal, reservation, targetContent,
		writebackIDsStart, now.Add(3*time.Second))
	writebackID := authoringIntegrationID(writebackIDsStart + 6)
	check := authoringapp.RestoreWritebackCheck{
		WorkspaceID: workspaceID, DocumentID: documentID, ExpectedDocumentVersion: restore.ExpectedDocumentVersion,
		TargetPath: detail.Document.CanonicalPath, CurrentContentHash: restore.CurrentContentHash,
	}
	if err := repository.ValidateRestoreWriteback(ctx, check); err != nil {
		t.Fatal(err)
	}
	record := authoringapp.RestorePublicationRecord{
		WorkspaceID: workspaceID, ProposalID: proposalID, ProposalRevisionID: proposalRevisionID,
		WritebackID: writebackID, GitCommit: gitCommit, ResultHash: restore.TargetContentHash,
		PublishedAt: now.Add(4 * time.Second),
	}
	type finalizeResult struct {
		applied bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan finalizeResult, 2)
	for range 2 {
		go func() {
			<-start
			applied, finalizeErr := repository.FinalizeRestorePublication(ctx, record)
			results <- finalizeResult{applied: applied, err: finalizeErr}
		}()
	}
	close(start)
	appliedCount := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			authoringIntegrationFatal(t, result.err)
		}
		if result.applied {
			appliedCount++
		}
	}
	if appliedCount != 1 {
		t.Fatalf("concurrent restore publication applied count=%d", appliedCount)
	}
	replayed, err := repository.FinalizeRestorePublication(ctx, record)
	if err != nil || replayed {
		t.Fatalf("replayed applied=%v err=%v", replayed, err)
	}
	restored, err := repository.GetDocumentDetail(ctx, workspaceID, documentID)
	if err != nil || restored.CurrentRevision == nil || restored.CurrentRevision.Content != targetContent ||
		restored.CurrentRevision.ContentHash != restore.TargetContentHash || restored.CurrentRevision.GitCommit != gitCommit ||
		restored.CurrentRevision.Status != authoringdomain.RevisionPublished ||
		restored.CurrentRevision.ParentRevisionID != currentRevisionID ||
		restored.Document.CurrentPublishedRevisionID != restored.CurrentRevision.ID ||
		restored.Document.Version != restore.ExpectedDocumentVersion+1 {
		t.Fatalf("restored detail=%#v err=%v", restored, err)
	}
	var previousStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM core.article_revision WHERE id=$1`, string(currentRevisionID)).Scan(&previousStatus); err != nil {
		t.Fatal(err)
	}
	if previousStatus != string(authoringdomain.RevisionSuperseded) {
		t.Fatalf("previous revision status=%s", previousStatus)
	}
	if err := repository.ValidateRestoreWriteback(ctx, check); !authoringIntegrationError(err, foundation.ErrorVersionConflict, authoringdomain.ErrorCodeVersionConflict) {
		t.Fatalf("stale restore owner check error=%v", err)
	}
}

func authoringPublishBinding(t *testing.T, workspaceID, documentID, revisionID foundation.ID, key string) authoringapp.PublishBinding {
	t.Helper()
	requestHash, err := authoringdomain.ComputePublishRequestHash(workspaceID, documentID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	return authoringapp.PublishBinding{
		WorkspaceID: workspaceID, DocumentID: documentID, RevisionID: revisionID,
		IdempotencyKey: key, RequestHash: requestHash,
	}
}

func seedAuthoringDraftDocumentRevision(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, documentID, revisionID foundation.ID,
	revisionNo int,
	targetPath, title, content string,
	parentRevisionID foundation.ID,
	createdAt time.Time,
) {
	t.Helper()
	if revisionNo == 1 {
		if _, err := pool.Exec(ctx, `INSERT INTO core.document(
			id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'DRAFT',NULL,1,$5,$5)`,
			string(documentID), string(workspaceID), targetPath, title, createdAt.UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,parent_revision_id,revision_no,content,content_hash,status,
		optimization_mode,git_commit,created_by_type,created_at
	) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,'DRAFT','NONE',NULL,'USER',$8)`,
		string(revisionID), string(workspaceID), string(documentID), string(parentRevisionID), revisionNo,
		content, authoringdomain.ComputeContentHash(content), createdAt.UTC()); err != nil {
		t.Fatal(err)
	}
}

func seedAuthoringPublishedDocumentRevisions(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, documentID, previousRevisionID, targetRevisionID foundation.ID,
	targetPath, previousContent, targetContent string,
	idStart int,
	createdAt time.Time,
) {
	t.Helper()
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, documentID, previousRevisionID, 1,
		targetPath, "Published document", previousContent, "", createdAt)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	binding := authoringPublishBinding(t, workspaceID, documentID, previousRevisionID, "seed-published:"+string(documentID))
	preparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(idStart), ReservedAt: createdAt.Add(time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := seedAuthoringPublicationProposal(t, ctx, pool, preparation.Reservation, previousContent,
		authoringIntegrationID(idStart+1), authoringIntegrationID(idStart+2), createdAt.Add(2*time.Millisecond))
	if _, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: binding, ReservationID: preparation.Reservation.ID, PublicationID: authoringIntegrationID(idStart + 3),
		ProposalID: proposal.ID, ProposalRevisionID: proposal.Revision.ID, CompletedAt: createdAt.Add(3 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	seedAuthoringProposalCommit(t, ctx, pool, proposal, preparation.Reservation, previousContent,
		idStart+10, createdAt.Add(4*time.Millisecond))
	advanced, err := repository.ReconcilePublications(ctx, authoringapp.ReconcileQuery{
		WorkspaceID: workspaceID, DocumentID: documentID, Limit: 1, Now: createdAt.Add(5 * time.Millisecond),
	})
	if err != nil || advanced != 1 {
		t.Fatalf("seed publication reconcile advanced=%d err=%v", advanced, err)
	}
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, documentID, targetRevisionID, 2,
		targetPath, "Published document", targetContent, previousRevisionID, createdAt.Add(time.Second))
}

func seedAuthoringProposalCommit(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proposal changecontroldomain.Proposal,
	reservation authoringapp.PublicationReservation,
	content string,
	idStart int,
	createdAt time.Time,
) string {
	t.Helper()
	repository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	runID := authoringIntegrationID(idStart)
	nodeID := authoringIntegrationID(idStart + 1)
	definitionID := authoringIntegrationID(idStart + 2)
	approvalID := authoringIntegrationID(idStart + 3)
	writeAuthorizationID := authoringIntegrationID(idStart + 4)
	gitAuthorizationID := authoringIntegrationID(idStart + 5)
	executionID := authoringIntegrationID(idStart + 6)
	approvedGitHead := authoringIntegrationDigest("head:" + string(executionID))[:40]
	approved, err := repository.Approve(ctx, changecontroldomain.Approval{
		ID: approvalID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash, Decision: changecontroldomain.DecisionApproved,
		ApprovedGitHead: &approvedGitHead, DecidedAt: createdAt.UTC(),
	})
	if err != nil || approved.ApprovedGitHead == nil || *approved.ApprovedGitHead != approvedGitHead {
		t.Fatalf("approval=%#v err=%v", approved, err)
	}
	graph, err := json.Marshal(workflowdomain.CanonicalGraph{Nodes: []workflowdomain.NodeDefinition{{
		Key: "writeback", Kind: "tool", InputSchemaVersion: 1, OutputSchemaVersion: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,$3,1,$4,$5)`, string(definitionID), string(reservation.WorkspaceID),
		"authoring-"+string(definitionID), graph, createdAt.UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	) VALUES($1,$2,$3,'running','{}',1,$4,$4)`, string(runID), string(reservation.WorkspaceID),
		string(definitionID), createdAt.UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,attempt,input,version,created_at,updated_at,
		lease_owner,lease_until,idempotency_key,input_schema_version,output_schema_version,dispatch_no
	) VALUES($1,$2,'writeback','tool','running',1,'{}',1,$3,$3,'authoring-test',$4,$5,1,1,1)`,
		string(nodeID), string(runID), createdAt.UTC(), createdAt.Add(time.Hour).UTC(),
		"authoring-node-"+string(nodeID)); err != nil {
		t.Fatal(err)
	}
	if tag, err := pool.Exec(ctx, `UPDATE change_control.proposal
		SET workflow_run_id=$1,version=version+1,updated_at=$2
		WHERE id=$3 AND workspace_id=$4 AND status='approved' AND workflow_run_id IS NULL`,
		string(runID), createdAt.UTC(), string(proposal.ID), string(reservation.WorkspaceID)); err != nil {
		t.Fatal(err)
	} else if tag.RowsAffected() != 1 {
		t.Fatal("authoring proposal was not bound to its writeback workflow")
	}
	scope := changecontroldomain.ExpectedAuthorizationScopeForTarget(reservation.TargetPath, reservation.TargetMode)
	for _, authorization := range []changecontroldomain.ToolAuthorization{
		{
			ID: writeAuthorizationID, WorkspaceID: reservation.WorkspaceID, WorkflowRunID: runID, NodeRunID: nodeID,
			ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: approvalID,
			ToolName: "ApplyApprovedPatch", Capability: changecontroldomain.CapabilityWriteKnowledge,
			Scope: scope, ApprovedChangeHash: proposal.Revision.ChangeHash, TargetMode: reservation.TargetMode,
			TargetVersion: reservation.BaseVersion, TokenHash: authoringIntegrationDigest("write:" + string(executionID)),
			IdempotencyKey: "authoring-write-" + string(executionID), Status: changecontroldomain.AuthorizationIssued,
			IssuedAt: createdAt.UTC(), ExpiresAt: createdAt.Add(2 * time.Minute).UTC(), Version: 1,
		},
		{
			ID: gitAuthorizationID, WorkspaceID: reservation.WorkspaceID, WorkflowRunID: runID, NodeRunID: nodeID,
			ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: approvalID,
			ToolName: "CreateGitCommit", Capability: changecontroldomain.CapabilityGitWrite,
			Scope: scope, ApprovedChangeHash: proposal.Revision.ChangeHash, TargetMode: reservation.TargetMode,
			TargetVersion: reservation.BaseVersion, TokenHash: authoringIntegrationDigest("git:" + string(executionID)),
			IdempotencyKey: "authoring-git-" + string(executionID), Status: changecontroldomain.AuthorizationIssued,
			IssuedAt: createdAt.UTC(), ExpiresAt: createdAt.Add(2 * time.Minute).UTC(), Version: 1,
		},
	} {
		if _, err := repository.CreateAuthorization(ctx, authorization); err != nil {
			t.Fatal(err)
		}
	}
	temporaryRef := "tmp/" + string(executionID)
	backupRef := ""
	backupLockToken := ""
	baseBlobID := ""
	if changecontroldomain.NormalizeTargetMode(reservation.TargetMode) == changecontroldomain.TargetModeReplace {
		backupRef = "backup/" + string(executionID)
		backupLockToken = authoringIntegrationDigest("backup-lock:" + string(executionID))
		baseBlobID = authoringIntegrationDigest("base-blob:" + string(executionID))[:40]
	}
	resultHash := changecontroldomain.ComputeWritebackResultHash([]byte(content))
	execution, err := repository.CreateWritebackExecution(ctx, changecontroldomain.CreateWriteback{
		ID: executionID, WorkspaceID: reservation.WorkspaceID, WorkflowRunID: runID, NodeRunID: nodeID,
		ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ApprovalID: approvalID,
		WriteAuthorizationID: writeAuthorizationID, GitAuthorizationID: gitAuthorizationID,
		TargetPath: reservation.TargetPath, TargetMode: reservation.TargetMode, BaseHash: reservation.BaseVersion,
		ResultHash: resultHash, ApprovedChangeHash: proposal.Revision.ChangeHash, ApprovedGitHead: approvedGitHead,
		IdempotencyKey: "authoring-execution-" + string(executionID), TemporaryRef: temporaryRef,
		BackupRef: backupRef, CreatedAt: createdAt.UTC(),
	})
	if err != nil {
		authoringIntegrationFatal(t, err)
	}
	filePrepared, err := repository.CheckpointWritebackExecution(ctx, changecontroldomain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: execution.Version, Status: changecontroldomain.WritebackStatusFilePrepared,
		ResultHash: resultHash, TemporaryRef: temporaryRef, BackupRef: backupRef, FileByteSize: int64(len(content)),
		FileMode: 0o644, FileLockToken: authoringIntegrationDigest("lock:" + string(executionID)),
		FileResultLockToken: authoringIntegrationDigest("result-lock:" + string(executionID)),
		FileBackupLockToken: backupLockToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	fileApplied, err := repository.CheckpointWritebackExecution(ctx, changecontroldomain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: filePrepared.Version,
		Status: changecontroldomain.WritebackStatusFileApplied, ResultHash: resultHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	diffHash := authoringIntegrationDigest("diff:" + string(executionID))
	resultBlobID := authoringIntegrationDigest("result-blob:" + string(executionID))[:40]
	gitPrepared, err := repository.CheckpointWritebackExecution(ctx, changecontroldomain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: fileApplied.Version,
		Status: changecontroldomain.WritebackStatusGitPrepared, ResultHash: resultHash, DiffHash: diffHash,
		BaseBlobID: baseBlobID, ResultBlobID: resultBlobID, BaseMode: changecontroldomain.GitFileModeRegular,
	})
	if err != nil {
		t.Fatal(err)
	}
	gitCommit := authoringIntegrationDigest("commit:" + string(executionID))
	parentGitCommit := authoringIntegrationDigest("parent:" + string(executionID))
	gitCommitted, err := repository.CheckpointWritebackExecution(ctx, changecontroldomain.CheckpointWriteback{
		ExecutionID: execution.ID, ExpectedVersion: gitPrepared.Version,
		Status: changecontroldomain.WritebackStatusGitCommitted, ResultHash: resultHash,
		GitCommit: gitCommit, ParentGitCommit: parentGitCommit, DiffHash: diffHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := reindexcontract.EncodeCanonical(reindexcontract.RequestV1{
		SchemaVersion: reindexcontract.SchemaVersionV1, WorkspaceID: reservation.WorkspaceID,
		WorkflowRunID: runID, NodeRunID: nodeID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ApprovalID: approvalID, WritebackExecutionID: execution.ID, TargetPath: reservation.TargetPath,
		ResultHash: resultHash, GitCommit: gitCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := repository.PublishWriteback(ctx, changecontroldomain.PublishWriteback{
		ExecutionID: execution.ID, ExpectedVersion: gitCommitted.Version,
		Commit: changecontroldomain.ProposalCommit{
			ID: authoringIntegrationID(idStart + 7), WorkspaceID: reservation.WorkspaceID,
			WritebackExecutionID: execution.ID, ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
			ApprovalID: approvalID, GitCommit: gitCommit, ParentGitCommit: parentGitCommit,
			TargetPath: reservation.TargetPath, TargetMode: reservation.TargetMode,
			DiffHash: diffHash, ResultHash: resultHash,
		},
		Event: changecontroldomain.WritebackOutboxEvent{
			ID: authoringIntegrationID(idStart + 8), WorkspaceID: reservation.WorkspaceID, RunID: runID,
			Type:           reindexcontract.EventTypeReindexRequested,
			IdempotencyKey: changecontroldomain.ExpectedWritebackReindexKey(gitCommitted), Payload: payload,
		},
	})
	if err != nil || published.Commit.GitCommit != gitCommit || published.Commit.TargetMode != reservation.TargetMode {
		t.Fatalf("published writeback=%#v err=%v", published, err)
	}
	return gitCommit
}

func authoringIntegrationDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func seedAuthoringPublicationProposal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	reservation authoringapp.PublicationReservation,
	content string,
	proposalID, proposalRevisionID foundation.ID,
	createdAt time.Time,
) changecontroldomain.Proposal {
	t.Helper()
	repository, err := changecontrolpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	changeHash, err := changecontroldomain.ComputeChangeHashForTarget(
		reservation.WorkspaceID, reservation.TargetPath, reservation.TargetMode, reservation.BaseVersion, content,
	)
	if err != nil {
		t.Fatal(err)
	}
	const evidence = "Authoring integration frozen revision"
	const risk = "Publishing changes a governed workspace file"
	const rollback = "Revert the governed Git commit"
	requestHash, err := changecontroldomain.ComputeRequestHashWithTargetMode(
		reservation.WorkspaceID, reservation.TargetPath, reservation.TargetMode, reservation.BaseVersion,
		content, evidence, changecontroldomain.ProposalRiskLevelMedium, risk, rollback,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := changecontroldomain.Proposal{
		ID: proposalID, WorkspaceID: reservation.WorkspaceID, Type: changecontroldomain.ProposalTypeFilePatch,
		RiskLevel: changecontroldomain.ProposalRiskLevelMedium, TargetPath: reservation.TargetPath,
		IdempotencyKey: reservation.ProposalIdempotencyKey, RequestHash: requestHash,
		Status: changecontroldomain.StatusReady, Version: 1, CreatedAt: createdAt.UTC(), UpdatedAt: createdAt.UTC(),
		Revision: changecontroldomain.Revision{
			ID: proposalRevisionID, ProposalID: proposalID, RevisionNo: 1, TargetPath: reservation.TargetPath,
			TargetMode: reservation.TargetMode, BaseHash: reservation.BaseVersion, Content: content,
			EvidenceSummary: evidence, Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash, CreatedAt: createdAt.UTC(),
		},
	}
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != proposal.ID || created.Revision.ID != proposal.Revision.ID {
		t.Fatalf("created proposal=%#v, want %#v", created, proposal)
	}
	return created
}
