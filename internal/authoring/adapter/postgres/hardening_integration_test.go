//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryPostgreSQLHardeningRejectsForgedPublicationFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
	workspaceID := authoringIntegrationID(900)
	documentID := authoringIntegrationID(901)
	revisionID := authoringIntegrationID(902)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-hardening")
	now := time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC)
	content := "# Exact publication\n\nFrozen content."
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, documentID, revisionID, 1,
		"notes/exact.md", "Exact publication", content, "", now)

	binding := authoringPublishBinding(t, workspaceID, documentID, revisionID, "hardening-publish")
	preparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(903), ReservedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := seedAuthoringPublicationProposal(t, ctx, pool, preparation.Reservation, content+"\nforged",
		authoringIntegrationID(904), authoringIntegrationID(905), now.Add(2*time.Second))
	_, err = pool.Exec(ctx, `INSERT INTO authoring.document_publication_binding(
		id,reservation_id,workspace_id,document_id,article_revision_id,proposal_id,
		proposal_revision_id,target_path,content_hash,target_mode,absence_token,
		status,git_commit,error_code,version,created_at,updated_at,published_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'PENDING',NULL,'',1,$12,$12,NULL)`,
		string(authoringIntegrationID(906)), string(preparation.Reservation.ID), string(workspaceID),
		string(documentID), string(revisionID), string(proposal.ID), string(proposal.Revision.ID),
		preparation.Reservation.TargetPath, preparation.Reservation.ContentHash,
		string(preparation.Reservation.TargetMode), preparation.Reservation.AbsenceToken, now.Add(3*time.Second))
	if err == nil {
		t.Fatal("database accepted a publication binding whose Proposal content differs from the frozen Revision")
	}
	authoringIntegrationPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE authoring.document_publication_reservation
		SET status='CLOSED',updated_at=$1,closed_at=$1 WHERE id=$2`,
		now.Add(4*time.Second), string(preparation.Reservation.ID))
	if err == nil {
		t.Fatal("database closed a publication reservation without an exact pending binding")
	}
	authoringIntegrationPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE authoring.document_publication_reservation
		SET status='ABANDONED',error_code='AUTHORING_UNCONTROLLED_ABANDONMENT',
			updated_at=$1,abandoned_at=$1 WHERE id=$2`,
		now.Add(4*time.Second), string(preparation.Reservation.ID))
	if err == nil {
		t.Fatal("database accepted an uncontrolled publication abandonment reason")
	}
	authoringIntegrationPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `UPDATE core.article_revision
		SET status='PUBLISHED',git_commit=$1
		WHERE id=$2 AND workspace_id=$3 AND document_id=$4`,
		authoringIntegrationDigest("forged-commit"), string(revisionID), string(workspaceID), string(documentID))
	if err == nil {
		t.Fatal("database accepted a published Revision without an exact proposal_commit")
	}
	authoringIntegrationPostgresCode(t, err, "23514")

	var status string
	var gitCommit *string
	if err := pool.QueryRow(ctx, `SELECT status,git_commit FROM core.article_revision
		WHERE id=$1 AND workspace_id=$2`, string(revisionID), string(workspaceID)).Scan(&status, &gitCommit); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.RevisionDraft) || gitCommit != nil {
		t.Fatalf("forged revision mutation leaked status=%s git_commit=%v", status, gitCommit)
	}

	_, err = pool.Exec(ctx, `UPDATE core.document
		SET lifecycle_status='PUBLISHED',current_published_revision_id=$1,version=version+1,updated_at=$2
		WHERE id=$3 AND workspace_id=$4`, string(revisionID), now.Add(5*time.Second),
		string(documentID), string(workspaceID))
	if err == nil {
		t.Fatal("database accepted a published Document pointer to an unverified Revision")
	}
	authoringIntegrationPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `INSERT INTO core.article_revision(
		id,workspace_id,document_id,parent_revision_id,revision_no,content,content_hash,status,
		optimization_mode,git_commit,created_by_type,created_at
	) VALUES($1,$2,$3,$4,2,$5,$6,'PUBLISHED','NONE',$7,'USER',$8)`,
		string(authoringIntegrationID(907)), string(workspaceID), string(documentID), string(revisionID),
		"# Forged insert", domain.ComputeContentHash("# Forged insert"), authoringIntegrationDigest("forged-insert"),
		now.Add(6*time.Second))
	if err == nil {
		t.Fatal("database accepted a Revision inserted directly as Published")
	}
	authoringIntegrationPostgresCode(t, err, "23514")

	workingDraftID := authoringIntegrationID(930)
	if _, err := pool.Exec(ctx, `INSERT INTO authoring.working_draft(
		id,workspace_id,document_id,title,target_path,body,status,version,created_at,updated_at
	) VALUES($1,$2,NULL,'','','','EDITING',1,$3,$3)`,
		string(workingDraftID), string(workspaceID), now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO authoring.working_draft_command(
		workspace_id,idempotency_key,request_hash,command_type,working_draft_id,
		expected_version,result_draft_version,response_draft_created_at,created_at
	) VALUES($1,'forged-create',$2,'CREATE',$3,0,NULL,$4,$4)`,
		string(workspaceID), authoringIntegrationDigest("forged-create"), string(workingDraftID), now.Add(8*time.Second))
	if err == nil {
		t.Fatal("database accepted a nullable CREATE command receipt shape")
	}
	authoringIntegrationPostgresCode(t, err, "23514")

	wrongKeyDocumentID := authoringIntegrationID(910)
	wrongKeyRevisionID := authoringIntegrationID(911)
	wrongKeyContent := "# Wrong proposal key"
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, wrongKeyDocumentID, wrongKeyRevisionID, 1,
		"notes/wrong-key.md", "Wrong proposal key", wrongKeyContent, "", now.Add(10*time.Second))
	wrongKeyBinding := authoringPublishBinding(t, workspaceID, wrongKeyDocumentID, wrongKeyRevisionID, "wrong-key-publish")
	wrongKeyPreparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: wrongKeyBinding, ReservationID: authoringIntegrationID(912), ReservedAt: now.Add(11 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongKeyReservation := wrongKeyPreparation.Reservation
	wrongKeyReservation.ProposalIdempotencyKey = "intentionally-different-proposal-key"
	wrongKeyProposal := seedAuthoringPublicationProposal(t, ctx, pool, wrongKeyReservation, wrongKeyContent,
		authoringIntegrationID(913), authoringIntegrationID(914), now.Add(12*time.Second))
	_, err = pool.Exec(ctx, `INSERT INTO authoring.document_publication_binding(
		id,reservation_id,workspace_id,document_id,article_revision_id,proposal_id,
		proposal_revision_id,target_path,content_hash,target_mode,absence_token,
		status,git_commit,error_code,version,created_at,updated_at,published_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'PENDING',NULL,'',1,$12,$12,NULL)`,
		string(authoringIntegrationID(915)), string(wrongKeyPreparation.Reservation.ID), string(workspaceID),
		string(wrongKeyDocumentID), string(wrongKeyRevisionID), string(wrongKeyProposal.ID),
		string(wrongKeyProposal.Revision.ID), wrongKeyPreparation.Reservation.TargetPath,
		wrongKeyPreparation.Reservation.ContentHash, string(wrongKeyPreparation.Reservation.TargetMode),
		wrongKeyPreparation.Reservation.AbsenceToken, now.Add(13*time.Second))
	if err == nil {
		t.Fatal("database accepted a Proposal idempotency key different from its Reservation")
	}
	authoringIntegrationPostgresCode(t, err, "23514")

	terminalDocumentID := authoringIntegrationID(920)
	terminalRevisionID := authoringIntegrationID(921)
	terminalContent := "# Terminal binding proof"
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, terminalDocumentID, terminalRevisionID, 1,
		"notes/terminal-proof.md", "Terminal binding proof", terminalContent, "", now.Add(20*time.Second))
	terminalBinding := authoringPublishBinding(t, workspaceID, terminalDocumentID, terminalRevisionID, "terminal-proof-publish")
	terminalPreparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: terminalBinding, ReservationID: authoringIntegrationID(922), ReservedAt: now.Add(21 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalProposal := seedAuthoringPublicationProposal(t, ctx, pool, terminalPreparation.Reservation, terminalContent,
		authoringIntegrationID(923), authoringIntegrationID(924), now.Add(22*time.Second))
	terminalResult, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: terminalBinding, ReservationID: terminalPreparation.Reservation.ID,
		PublicationID: authoringIntegrationID(925), ProposalID: terminalProposal.ID,
		ProposalRevisionID: terminalProposal.Revision.ID, CompletedAt: now.Add(23 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	forgedCommit := authoringIntegrationDigest("forged-terminal-commit")
	_, err = pool.Exec(ctx, `UPDATE authoring.document_publication_binding
		SET status='PUBLISHED',git_commit=$1,error_code='',version=2,updated_at=$2,published_at=$2
		WHERE id=$3`, forgedCommit, now.Add(24*time.Second), string(terminalResult.Publication.ID))
	if err == nil {
		t.Fatal("database accepted a Published binding without exact terminal facts")
	}
	authoringIntegrationPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE authoring.document_publication_binding
		SET status='RECOVERY_REQUIRED',git_commit=NULL,error_code='AUTHORING_UNCONTROLLED_RECOVERY',
			version=2,updated_at=$1,published_at=NULL WHERE id=$2`,
		now.Add(25*time.Second), string(terminalResult.Publication.ID))
	if err == nil {
		t.Fatal("database accepted an uncontrolled publication recovery reason")
	}
	authoringIntegrationPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE authoring.document_publication_binding
		SET status='CLOSED',git_commit=NULL,error_code='AUTHORING_PUBLICATION_PROPOSAL_REJECTED',
			version=2,updated_at=$1,published_at=NULL WHERE id=$2`,
		now.Add(26*time.Second), string(terminalResult.Publication.ID))
	if err == nil {
		t.Fatal("database accepted a Closed binding while its Proposal remained nonterminal")
	}
	authoringIntegrationPostgresCode(t, err, "23514")
}

func TestRepositoryPostgreSQLDeferredPublicationClosureRejectsPartialTransactions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	workspaceID := authoringIntegrationID(1000)
	repository, pool := newAuthoringIntegrationRepository(t, ctx)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-deferred-closure")
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP - INTERVAL '1 hour'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Microsecond)

	t.Run("revision-only", func(t *testing.T) {
		documentID, revisionID, _, gitCommit := seedAuthoringPendingPublicationWithCommit(
			t, ctx, pool, repository, workspaceID, 1010, "revision-only", now)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.article_revision
			SET status='PUBLISHED',git_commit=$1
			WHERE id=$2 AND workspace_id=$3 AND document_id=$4`, gitCommit,
			string(revisionID), string(workspaceID), string(documentID)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err == nil {
			t.Fatal("partial Revision publication committed")
		} else {
			authoringIntegrationPostgresCode(t, err, "23514")
		}
	})

	t.Run("revision-and-document-without-binding", func(t *testing.T) {
		documentID, revisionID, _, gitCommit := seedAuthoringPendingPublicationWithCommit(
			t, ctx, pool, repository, workspaceID, 1110, "revision-document-only", now.Add(time.Minute))
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.article_revision
			SET status='PUBLISHED',git_commit=$1
			WHERE id=$2 AND workspace_id=$3 AND document_id=$4`, gitCommit,
			string(revisionID), string(workspaceID), string(documentID)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.document
			SET lifecycle_status='PUBLISHED',current_published_revision_id=$1,
				version=version+1,updated_at=$2
			WHERE id=$3 AND workspace_id=$4`, string(revisionID), now.Add(61*time.Second),
			string(documentID), string(workspaceID)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err == nil {
			t.Fatal("Revision + Document partial publication committed")
		} else {
			authoringIntegrationPostgresCode(t, err, "23514")
		}
	})

	t.Run("supersede-without-replacement-binding", func(t *testing.T) {
		documentID := authoringIntegrationID(1210)
		previousRevisionID := authoringIntegrationID(1211)
		targetRevisionID := authoringIntegrationID(1212)
		previousContent := "# Deferred replace\n\nOne."
		targetContent := "# Deferred replace\n\nTwo."
		seedAuthoringPublishedDocumentRevisions(t, ctx, pool, workspaceID, documentID,
			previousRevisionID, targetRevisionID, "notes/deferred-replace.md",
			previousContent, targetContent, 1220, now.Add(2*time.Minute))
		binding := authoringPublishBinding(t, workspaceID, documentID, targetRevisionID, "deferred-replace")
		preparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
			Binding: binding, ReservationID: authoringIntegrationID(1240), ReservedAt: now.Add(121 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		proposal := seedAuthoringPublicationProposal(t, ctx, pool, preparation.Reservation, targetContent,
			authoringIntegrationID(1241), authoringIntegrationID(1242), now.Add(122*time.Second))
		if _, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
			Binding: binding, ReservationID: preparation.Reservation.ID, PublicationID: authoringIntegrationID(1243),
			ProposalID: proposal.ID, ProposalRevisionID: proposal.Revision.ID, CompletedAt: now.Add(123 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		seedAuthoringProposalCommit(t, ctx, pool, proposal, preparation.Reservation, targetContent,
			1250, now.Add(124*time.Second))
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.article_revision SET status='SUPERSEDED'
			WHERE id=$1 AND workspace_id=$2 AND document_id=$3 AND status='PUBLISHED'`,
			string(previousRevisionID), string(workspaceID), string(documentID)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err == nil {
			t.Fatal("partial replacement supersede committed")
		} else {
			authoringIntegrationPostgresCode(t, err, "23514")
		}
	})
}

func seedAuthoringPendingPublicationWithCommit(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repository *Repository,
	workspaceID foundation.ID,
	idStart int,
	name string,
	createdAt time.Time,
) (foundation.ID, foundation.ID, foundation.ID, string) {
	t.Helper()
	documentID := authoringIntegrationID(idStart)
	revisionID := authoringIntegrationID(idStart + 1)
	content := "# " + name + "\n\nFrozen content."
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, documentID, revisionID, 1,
		"notes/"+name+".md", name, content, "", createdAt)
	binding := authoringPublishBinding(t, workspaceID, documentID, revisionID, "deferred-"+name)
	preparation, err := repository.ReservePublication(ctx, authoringapp.ReservePublicationRecord{
		Binding: binding, ReservationID: authoringIntegrationID(idStart + 2), ReservedAt: createdAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := seedAuthoringPublicationProposal(t, ctx, pool, preparation.Reservation, content,
		authoringIntegrationID(idStart+3), authoringIntegrationID(idStart+4), createdAt.Add(2*time.Second))
	if _, err := repository.CompletePublication(ctx, authoringapp.CompletePublicationRecord{
		Binding: binding, ReservationID: preparation.Reservation.ID, PublicationID: authoringIntegrationID(idStart + 5),
		ProposalID: proposal.ID, ProposalRevisionID: proposal.Revision.ID, CompletedAt: createdAt.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	gitCommit := seedAuthoringProposalCommit(t, ctx, pool, proposal, preparation.Reservation, content,
		idStart+10, createdAt.Add(4*time.Second))
	return documentID, revisionID, preparation.Reservation.ID, gitCommit
}

func TestDocumentDraftAuthoringHardeningMigrationRejectsLegacyProposalKeyDrift(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newAuthoringIntegrationDatabaseToVersion(t, ctx, 70)
	workspaceID := authoringIntegrationID(1300)
	documentID := authoringIntegrationID(1301)
	revisionID := authoringIntegrationID(1302)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-legacy-binding")
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP - INTERVAL '1 hour'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Microsecond)
	content := "# Legacy Proposal key\n\nFrozen."
	seedAuthoringDraftDocumentRevision(t, ctx, pool, workspaceID, documentID, revisionID, 1,
		"notes/legacy-key.md", "Legacy key", content, "", now)
	binding := authoringPublishBinding(t, workspaceID, documentID, revisionID, "legacy-key")
	absenceToken, err := domain.ComputeAbsenceToken(workspaceID, "notes/legacy-key.md")
	if err != nil {
		t.Fatal(err)
	}
	proposalKey, err := domain.ComputeProposalIdempotencyKey(
		workspaceID, documentID, revisionID, "notes/legacy-key.md", domain.ComputeContentHash(content),
		domain.ProposalTargetCreateOnly, absenceToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	reservation := authoringapp.PublicationReservation{
		ID: authoringIntegrationID(1303), WorkspaceID: workspaceID, DocumentID: documentID,
		ArticleRevisionID: revisionID, IdempotencyKey: binding.IdempotencyKey,
		RequestHash: binding.RequestHash, ProposalIdempotencyKey: proposalKey,
		TargetPath: "notes/legacy-key.md", ContentHash: domain.ComputeContentHash(content),
		TargetMode: domain.ProposalTargetCreateOnly, BaseVersion: absenceToken, AbsenceToken: absenceToken,
		Status: authoringapp.PublicationReservationPending, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}
	if _, err := pool.Exec(ctx, `INSERT INTO authoring.document_publication_reservation(
		id,workspace_id,document_id,article_revision_id,idempotency_key,request_hash,
		proposal_idempotency_key,target_path,content_hash,target_mode,base_version,
		absence_token,status,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'PENDING',$13,$13)`,
		string(reservation.ID), string(reservation.WorkspaceID), string(reservation.DocumentID),
		string(reservation.ArticleRevisionID), reservation.IdempotencyKey, reservation.RequestHash,
		reservation.ProposalIdempotencyKey, reservation.TargetPath, reservation.ContentHash,
		string(reservation.TargetMode), reservation.BaseVersion, reservation.AbsenceToken,
		reservation.CreatedAt.UTC()); err != nil {
		t.Fatal(err)
	}
	// Seed the legacy drift with raw SQL: at version 00070 the current
	// change-control repository cannot run against the older proposal shape.
	proposalID := authoringIntegrationID(1304)
	proposalRevisionID := authoringIntegrationID(1305)
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,status,created_at,updated_at,idempotency_key,request_hash,version,proposal_type,risk_level
	) VALUES($1,$2,'draft',$3,$3,'legacy-wrong-proposal-key',$4,1,'file_patch','MEDIUM')`,
		string(proposalID), string(workspaceID), now.Add(2*time.Second),
		authoringIntegrationDigest("legacy-key-request")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,
		change_hash,created_at,target_mode
	) VALUES($1,$2,1,$3,$4,$5,'legacy evidence','legacy risk','legacy rollback',$6,$7,'CREATE_ONLY')`,
		string(proposalRevisionID), string(proposalID), reservation.TargetPath, reservation.BaseVersion,
		content, authoringIntegrationDigest("legacy-key-change"), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO authoring.document_publication_binding(
		id,reservation_id,workspace_id,document_id,article_revision_id,proposal_id,
		proposal_revision_id,target_path,content_hash,target_mode,absence_token,
		status,git_commit,error_code,version,created_at,updated_at,published_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'PENDING',NULL,'',1,$12,$12,NULL)`,
		string(authoringIntegrationID(1306)), string(reservation.ID), string(workspaceID),
		string(documentID), string(revisionID), string(proposalID), string(proposalRevisionID),
		reservation.TargetPath, reservation.ContentHash,
		string(reservation.TargetMode), reservation.AbsenceToken,
		now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE authoring.document_publication_reservation
		SET status='CLOSED',updated_at=$1,closed_at=$1 WHERE id=$2`,
		now.Add(3*time.Second), string(reservation.ID)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := platformmigration.MigrateAtlasToVersion(ctx, pool, 71); err == nil {
		t.Fatal("00071 accepted an existing Proposal idempotency binding drift")
	} else {
		authoringIntegrationPostgresCode(t, err, "55000")
	}
	assertAuthoringMigrationVersion(t, ctx, pool, "00070")
}

func TestDocumentDraftAuthoringTerminalMigrationRejectsLegacyNullableCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newAuthoringIntegrationDatabaseToVersion(t, ctx, 72)
	workspaceID := authoringIntegrationID(1400)
	draftID := authoringIntegrationID(1401)
	seedAuthoringWorkspace(t, ctx, pool, workspaceID, "authoring-legacy-command")
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP - INTERVAL '1 hour'`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO authoring.working_draft(
		id,workspace_id,document_id,title,target_path,body,status,version,created_at,updated_at
	) VALUES($1,$2,NULL,'','','','EDITING',1,$3,$3)`, string(draftID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE authoring.working_draft_command
		DROP CONSTRAINT authoring_command_shape,
		ADD CONSTRAINT authoring_command_shape CHECK (
			command_type<>'CREATE'
			OR (working_draft_id IS NOT NULL AND expected_version=0 AND result_draft_version=1)
		)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO authoring.working_draft_command(
		workspace_id,idempotency_key,request_hash,command_type,working_draft_id,
		expected_version,result_draft_version,response_draft_created_at,created_at
	) VALUES($1,'legacy-nullable-create',$2,'CREATE',$3,0,NULL,$4,$4)`,
		string(workspaceID), authoringIntegrationDigest("legacy-nullable-create"), string(draftID), now); err != nil {
		t.Fatal(err)
	}
	if err := platformmigration.MigrateAtlasToVersion(ctx, pool, 73); err == nil {
		t.Fatal("00073 accepted a legacy nullable command receipt")
	} else {
		authoringIntegrationPostgresCode(t, err, "23514")
	}
	assertAuthoringMigrationVersion(t, ctx, pool, "00072")
}

// assertAuthoringMigrationVersion asserts the highest applied Atlas revision.
func assertAuthoringMigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version string) {
	t.Helper()
	var applied string
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(max(version), '0') FROM atlas_schema_revisions.atlas_schema_revisions`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != version {
		t.Fatalf("database version after rejected upgrade=%s want=%s", applied, version)
	}
}
