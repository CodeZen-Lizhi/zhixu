package postgres

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	publicationCommitMismatch = "AUTHORING_PUBLICATION_COMMIT_MISMATCH"
	publicationStateMismatch  = "AUTHORING_PUBLICATION_STATE_MISMATCH"
	publicationRejected       = "AUTHORING_PUBLICATION_PROPOSAL_REJECTED"
	publicationNeedsRevision  = "AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION"
	publicationCancelled      = "AUTHORING_PUBLICATION_PROPOSAL_CANCELLED"
)

// ReservePublication persists the immutable facts needed to recover Proposal creation.
func (repository *Repository) ReservePublication(ctx context.Context, record authoringapp.ReservePublicationRecord) (authoringapp.PublicationPreparation, error) {
	if err := validateReservePublicationRecord(record); err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return authoringapp.PublicationPreparation{}, classify(err, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockPublishCommand(ctx, tx, record.Binding); err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	reservedAt := record.ReservedAt.UTC().Truncate(time.Microsecond)

	existing, found, err := loadPublishReceipt(ctx, tx, record.Binding)
	if err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return authoringapp.PublicationPreparation{}, classify(err, "AUTHORING_COMMIT_FAILED")
		}
		return authoringapp.PublicationPreparation{Existing: &existing, Replayed: true}, nil
	}

	reservation, found, err := loadReservationByKey(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey, false)
	if err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	if found {
		if !sameReservationCommand(reservation, record.Binding) {
			return authoringapp.PublicationPreparation{}, idempotencyConflict(errors.New("publication key is bound to another revision"))
		}
		document, revision, err := loadReservedPublicationFacts(ctx, tx, record.Binding)
		if err != nil {
			return authoringapp.PublicationPreparation{}, err
		}
		reservation, found, err = loadReservationByID(ctx, tx, record.Binding.WorkspaceID, reservation.ID, true)
		if err != nil || !found || !sameReservationCommand(reservation, record.Binding) {
			return authoringapp.PublicationPreparation{}, inconsistent("publication reservation changed while it was locked")
		}
		if reservation.Status == authoringapp.PublicationReservationClosed {
			existing, bindingFound, loadErr := loadPublicationByReservation(ctx, tx, record.Binding.WorkspaceID, reservation.ID, false)
			if loadErr != nil {
				return authoringapp.PublicationPreparation{}, loadErr
			}
			if !bindingFound {
				return authoringapp.PublicationPreparation{}, inconsistent("closed publication reservation has no binding")
			}
			if err := tx.Commit(ctx); err != nil {
				return authoringapp.PublicationPreparation{}, classify(err, "AUTHORING_COMMIT_FAILED")
			}
			return authoringapp.PublicationPreparation{Existing: &existing, Replayed: true}, nil
		}
		if reservation.Status == authoringapp.PublicationReservationAbandoned {
			if err := validateAbandonedReservation(reservation); err != nil {
				return authoringapp.PublicationPreparation{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return authoringapp.PublicationPreparation{}, classify(err, "AUTHORING_COMMIT_FAILED")
			}
			return authoringapp.PublicationPreparation{Reservation: reservation, Replayed: true}, nil
		}
		if err := validateStoredReservation(reservation, document, revision); err != nil {
			return authoringapp.PublicationPreparation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return authoringapp.PublicationPreparation{}, classify(err, "AUTHORING_COMMIT_FAILED")
		}
		return authoringapp.PublicationPreparation{Reservation: reservation, Document: document, Revision: revision, Replayed: true}, nil
	}

	document, revision, err := loadPublicationFacts(ctx, tx, record.Binding)
	if err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	if _, boundFound, loadErr := loadPublicationByRevision(ctx, tx, record.Binding.WorkspaceID, record.Binding.RevisionID); loadErr != nil {
		return authoringapp.PublicationPreparation{}, loadErr
	} else if boundFound {
		return authoringapp.PublicationPreparation{}, publicationConflict("article revision already has a publication binding")
	}
	if pending, pendingFound, loadErr := loadPendingReservationByDocument(ctx, tx, record.Binding.WorkspaceID, record.Binding.DocumentID); loadErr != nil {
		return authoringapp.PublicationPreparation{}, loadErr
	} else if pendingFound {
		return authoringapp.PublicationPreparation{}, publicationConflict("document already has a pending publication reservation: " + string(pending.ID))
	}

	mode := domain.ProposalTargetCreateOnly
	baseVersion, absenceToken := "", ""
	if document.Lifecycle == domain.DocumentDraft && document.CurrentPublishedRevisionID == "" {
		absenceToken, err = domain.ComputeAbsenceToken(document.WorkspaceID, document.CanonicalPath)
		baseVersion = absenceToken
	} else if document.Lifecycle == domain.DocumentPublished && document.CurrentPublishedRevisionID != "" {
		mode = domain.ProposalTargetReplace
		current, loadErr := loadRevisionByID(ctx, tx, document.WorkspaceID, document.ID, document.CurrentPublishedRevisionID, true)
		if loadErr != nil {
			return authoringapp.PublicationPreparation{}, loadErr
		}
		if current.Status != domain.RevisionPublished || current.GitCommit == "" {
			return authoringapp.PublicationPreparation{}, inconsistent("published document current revision is invalid")
		}
		baseVersion = current.ContentHash
	} else {
		return authoringapp.PublicationPreparation{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodePublicationInvalid, false, errors.New("document lifecycle cannot start a publication"))
	}
	if err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	proposalKey, err := domain.ComputeProposalIdempotencyKey(
		document.WorkspaceID, document.ID, revision.ID, document.CanonicalPath, revision.ContentHash, mode, absenceToken,
	)
	if err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	reservation = authoringapp.PublicationReservation{
		ID: record.ReservationID, WorkspaceID: document.WorkspaceID, DocumentID: document.ID,
		ArticleRevisionID: revision.ID, IdempotencyKey: record.Binding.IdempotencyKey,
		RequestHash: record.Binding.RequestHash, ProposalIdempotencyKey: proposalKey,
		TargetPath: document.CanonicalPath, ContentHash: revision.ContentHash, TargetMode: mode,
		BaseVersion: baseVersion, AbsenceToken: absenceToken, Status: authoringapp.PublicationReservationPending,
		CreatedAt: reservedAt, UpdatedAt: reservedAt,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO authoring.document_publication_reservation(
			id,workspace_id,document_id,article_revision_id,idempotency_key,request_hash,
			proposal_idempotency_key,target_path,content_hash,target_mode,base_version,
			absence_token,status,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'PENDING',$13,$13)`,
		string(reservation.ID), string(reservation.WorkspaceID), string(reservation.DocumentID),
		string(reservation.ArticleRevisionID), reservation.IdempotencyKey, reservation.RequestHash,
		reservation.ProposalIdempotencyKey, reservation.TargetPath, reservation.ContentHash,
		string(reservation.TargetMode), reservation.BaseVersion, reservation.AbsenceToken,
		reservation.CreatedAt.UTC()); err != nil {
		return authoringapp.PublicationPreparation{}, classify(err, "AUTHORING_PUBLICATION_RESERVATION_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return authoringapp.PublicationPreparation{}, classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return authoringapp.PublicationPreparation{Reservation: reservation, Document: document, Revision: revision}, nil
}

// AbandonPublication records a proven pre-Proposal terminal failure without releasing immutable history.
func (repository *Repository) AbandonPublication(ctx context.Context, record authoringapp.AbandonPublicationRecord) (authoringapp.PublicationReservation, error) {
	if err := validateAbandonPublicationRecord(record); err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return authoringapp.PublicationReservation{}, classify(err, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockPublishCommand(ctx, tx, record.Binding); err != nil {
		return authoringapp.PublicationReservation{}, err
	}

	document, revision, err := loadReservedPublicationFacts(ctx, tx, record.Binding)
	if err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	reservation, found, err := loadReservationByID(ctx, tx, record.Binding.WorkspaceID, record.ReservationID, true)
	if err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	if !found || !sameReservationCommand(reservation, record.Binding) {
		return authoringapp.PublicationReservation{}, inconsistent("publication reservation is missing or mismatched")
	}
	binding, bindingFound, err := loadPublicationByReservation(ctx, tx, record.Binding.WorkspaceID, reservation.ID, true)
	if err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	if bindingFound {
		return authoringapp.PublicationReservation{}, publicationConflict("publication reservation already has an immutable binding: " + string(binding.ID))
	}
	if reservation.Status == authoringapp.PublicationReservationAbandoned {
		if err := validateAbandonedReservation(reservation); err != nil {
			return authoringapp.PublicationReservation{}, err
		}
		if reservation.ErrorCode != record.ErrorCode {
			return authoringapp.PublicationReservation{}, idempotencyConflict(errors.New("abandoned publication reservation has another error code"))
		}
		if err := commitTransaction(ctx, tx); err != nil {
			return authoringapp.PublicationReservation{}, err
		}
		return reservation, nil
	}
	if reservation.Status != authoringapp.PublicationReservationPending {
		return authoringapp.PublicationReservation{}, publicationConflict("publication reservation cannot be abandoned from its current status")
	}
	if err := validateStoredReservation(reservation, document, revision); err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	abandonedAt := record.AbandonedAt.UTC().Truncate(time.Microsecond)
	if abandonedAt.Before(reservation.CreatedAt) {
		return authoringapp.PublicationReservation{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("publication abandonment predates reservation"))
	}
	tag, err := tx.Exec(ctx, `
		UPDATE authoring.document_publication_reservation
		SET status='ABANDONED',error_code=$1,abandoned_at=$2,updated_at=$2
		WHERE id=$3 AND workspace_id=$4 AND status='PENDING'`,
		record.ErrorCode, abandonedAt, string(reservation.ID), string(reservation.WorkspaceID))
	if err != nil {
		return authoringapp.PublicationReservation{}, classify(err, "AUTHORING_PUBLICATION_RESERVATION_ABANDON_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return authoringapp.PublicationReservation{}, inconsistent("publication reservation abandonment compare-and-swap did not update one row")
	}
	reservation.Status = authoringapp.PublicationReservationAbandoned
	reservation.ErrorCode = record.ErrorCode
	reservation.UpdatedAt = abandonedAt
	reservation.ClosedAt = nil
	reservation.AbandonedAt = &abandonedAt
	if err := validateAbandonedReservation(reservation); err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	if err := commitTransaction(ctx, tx); err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	return reservation, nil
}

// CompletePublication binds the exact Proposal result and closes its reservation atomically.
func (repository *Repository) CompletePublication(ctx context.Context, record authoringapp.CompletePublicationRecord) (authoringapp.PublishResult, error) {
	if err := validateCompletePublicationRecord(record); err != nil {
		return authoringapp.PublishResult{}, err
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return authoringapp.PublishResult{}, classify(err, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockPublishCommand(ctx, tx, record.Binding); err != nil {
		return authoringapp.PublishResult{}, err
	}
	completedAt := record.CompletedAt.UTC().Truncate(time.Microsecond)
	if existing, found, loadErr := loadPublishReceipt(ctx, tx, record.Binding); loadErr != nil {
		return authoringapp.PublishResult{}, loadErr
	} else if found {
		if existing.ProposalID != record.ProposalID || existing.ProposalRevisionID != record.ProposalRevisionID {
			return authoringapp.PublishResult{}, idempotencyConflict(errors.New("publication receipt is bound to another proposal"))
		}
		if err := tx.Commit(ctx); err != nil {
			return authoringapp.PublishResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
		}
		return authoringapp.PublishResult{Publication: existing, Replayed: true}, nil
	}
	document, revision, err := loadReservedPublicationFacts(ctx, tx, record.Binding)
	if err != nil {
		return authoringapp.PublishResult{}, err
	}
	reservation, found, err := loadReservationByID(ctx, tx, record.Binding.WorkspaceID, record.ReservationID, true)
	if err != nil {
		return authoringapp.PublishResult{}, err
	}
	if !found || !sameReservationCommand(reservation, record.Binding) {
		return authoringapp.PublishResult{}, inconsistent("publication reservation is missing or mismatched")
	}
	if reservation.Status == authoringapp.PublicationReservationClosed {
		existing, bindingFound, loadErr := loadPublicationByReservation(ctx, tx, record.Binding.WorkspaceID, reservation.ID, false)
		if loadErr != nil {
			return authoringapp.PublishResult{}, loadErr
		}
		if !bindingFound || existing.ProposalID != record.ProposalID || existing.ProposalRevisionID != record.ProposalRevisionID {
			return authoringapp.PublishResult{}, idempotencyConflict(errors.New("closed publication reservation is bound to another proposal"))
		}
		if err := tx.Commit(ctx); err != nil {
			return authoringapp.PublishResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
		}
		return authoringapp.PublishResult{Publication: existing, Replayed: true}, nil
	}
	if reservation.Status != authoringapp.PublicationReservationPending || completedAt.Before(reservation.CreatedAt) {
		return authoringapp.PublishResult{}, inconsistent("publication reservation cannot be completed")
	}
	if err := validateStoredReservation(reservation, document, revision); err != nil {
		return authoringapp.PublishResult{}, err
	}
	if err := validateProposalSnapshot(ctx, tx, reservation, record.ProposalID, record.ProposalRevisionID, revision.Content); err != nil {
		return authoringapp.PublishResult{}, err
	}
	publication := domain.PublicationBinding{
		ID: record.PublicationID, ReservationID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
		DocumentID: reservation.DocumentID, ArticleRevisionID: reservation.ArticleRevisionID,
		ProposalID: record.ProposalID, ProposalRevisionID: record.ProposalRevisionID,
		TargetPath: reservation.TargetPath, ContentHash: reservation.ContentHash,
		TargetMode: reservation.TargetMode, AbsenceToken: reservation.AbsenceToken,
		Status: domain.PublicationPending, Version: 1, CreatedAt: completedAt, UpdatedAt: completedAt,
	}
	if err := publication.Validate(); err != nil {
		return authoringapp.PublishResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO authoring.document_publication_binding(
			id,reservation_id,workspace_id,document_id,article_revision_id,proposal_id,
			proposal_revision_id,target_path,content_hash,target_mode,absence_token,
			status,git_commit,error_code,version,created_at,updated_at,published_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'PENDING',NULL,'',1,$12,$12,NULL)`,
		string(publication.ID), string(publication.ReservationID), string(publication.WorkspaceID),
		string(publication.DocumentID), string(publication.ArticleRevisionID), string(publication.ProposalID),
		string(publication.ProposalRevisionID), publication.TargetPath, publication.ContentHash,
		string(publication.TargetMode), publication.AbsenceToken, publication.CreatedAt.UTC()); err != nil {
		return authoringapp.PublishResult{}, classify(err, "AUTHORING_PUBLICATION_BIND_FAILED")
	}
	tag, err := tx.Exec(ctx, `
			UPDATE authoring.document_publication_reservation
			SET status='CLOSED',updated_at=$1,closed_at=$1 WHERE id=$2 AND status='PENDING'`,
		completedAt, string(reservation.ID))
	if err != nil {
		return authoringapp.PublishResult{}, classify(err, "AUTHORING_PUBLICATION_RESERVATION_CLOSE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return authoringapp.PublishResult{}, inconsistent("publication reservation close did not update one row")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO authoring.working_draft_command(
			workspace_id,idempotency_key,request_hash,command_type,working_draft_id,
			expected_version,result_draft_version,document_id,document_version,
			article_revision_id,revision_no,parent_revision_id,response_draft_created_at,
			response_document_created_at,response_document_lifecycle,response_published_revision_id,
			response_title,response_target_path,response_content_hash,proposal_id,proposal_revision_id,created_at
		) VALUES($1,$2,$3,'PUBLISH',NULL,0,NULL,$4,$5,$6,$7,NULLIF($8,'')::uuid,NULL,
			$9,$10,NULLIF($11,'')::uuid,$12,$13,$14,$15,$16,$17)`,
		string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
		string(document.ID), document.Version, string(revision.ID), revision.RevisionNo,
		string(revision.ParentRevisionID), document.CreatedAt.UTC(), string(document.Lifecycle),
		string(document.CurrentPublishedRevisionID), document.Title, document.CanonicalPath,
		revision.ContentHash, string(record.ProposalID), string(record.ProposalRevisionID), completedAt); err != nil {
		return authoringapp.PublishResult{}, classify(err, "AUTHORING_RECEIPT_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return authoringapp.PublishResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return authoringapp.PublishResult{Publication: publication}, nil
}

// ReconcilePublications advances only bindings with an exact immutable proposal_commit mapping.
func (repository *Repository) ReconcilePublications(ctx context.Context, query authoringapp.ReconcileQuery) (int, error) {
	if !validID(query.WorkspaceID) || (query.DocumentID != "" && !validID(query.DocumentID)) ||
		(query.ProposalID == "") != (query.ProposalRevisionID == "") ||
		(query.ProposalID != "" && (!validID(query.ProposalID) || !validID(query.ProposalRevisionID))) ||
		query.Limit < 1 || query.Limit > 100 || query.Now.IsZero() {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("publication reconcile query is invalid"))
	}
	arguments := []any{string(query.WorkspaceID)}
	filterConditions := ""
	if query.DocumentID != "" {
		arguments = append(arguments, string(query.DocumentID))
		filterConditions += " AND binding.document_id=$" + itoa(len(arguments))
	}
	if query.ProposalID != "" {
		arguments = append(arguments, string(query.ProposalID))
		filterConditions += " AND binding.proposal_id=$" + itoa(len(arguments))
		arguments = append(arguments, string(query.ProposalRevisionID))
		filterConditions += " AND binding.proposal_revision_id=$" + itoa(len(arguments))
	}
	arguments = append(arguments, query.Limit)
	rows, err := repository.db.Query(ctx, `
		SELECT binding.id::text
			FROM authoring.document_publication_binding binding
		WHERE binding.workspace_id=$1
		  AND binding.status IN ('PENDING','RECOVERY_REQUIRED')`+filterConditions+`
		ORDER BY binding.created_at,binding.id
		LIMIT $`+itoa(len(arguments)), arguments...)
	if err != nil {
		return 0, classify(err, "AUTHORING_PUBLICATION_RECONCILE_QUERY_FAILED")
	}
	ids := make([]foundation.ID, 0, query.Limit)
	for rows.Next() {
		var id foundation.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, classify(err, "AUTHORING_PUBLICATION_RECONCILE_QUERY_FAILED")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, classify(err, "AUTHORING_PUBLICATION_RECONCILE_QUERY_FAILED")
	}
	rows.Close()
	advanced := 0
	for _, id := range ids {
		changed, err := repository.reconcilePublication(ctx, query.WorkspaceID, id, query.Now.UTC())
		if err != nil {
			return advanced, err
		}
		if changed {
			advanced++
		}
	}
	return advanced, nil
}

func (repository *Repository) reconcilePublication(ctx context.Context, workspaceID, publicationID foundation.ID, now time.Time) (bool, error) {
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, classify(err, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, found, err := loadPublicationByID(ctx, tx, workspaceID, publicationID, false)
	if err != nil || !found {
		return false, err
	}
	document, err := loadDocument(ctx, tx, snapshot.WorkspaceID, snapshot.DocumentID, true)
	if err != nil {
		return false, err
	}
	revision, err := loadRevisionByID(ctx, tx, snapshot.WorkspaceID, snapshot.DocumentID, snapshot.ArticleRevisionID, true)
	if err != nil {
		return false, err
	}
	binding, found, err := loadPublicationByID(ctx, tx, workspaceID, publicationID, true)
	if err != nil || !found {
		return false, err
	}
	if binding.DocumentID != snapshot.DocumentID || binding.ArticleRevisionID != snapshot.ArticleRevisionID {
		return false, inconsistent("publication binding identity changed while locking")
	}
	if binding.Status == domain.PublicationPublished {
		return false, commitTransaction(ctx, tx)
	}
	now = now.UTC().Truncate(time.Microsecond)
	reservation, reservationFound, err := loadReservationByID(ctx, tx, binding.WorkspaceID, binding.ReservationID, false)
	if err != nil {
		return false, err
	}
	if !reservationFound || !reservationMatchesBinding(reservation, binding) ||
		reservation.Status != authoringapp.PublicationReservationClosed {
		return markPublicationRecovery(ctx, tx, binding, publicationStateMismatch, now)
	}
	if err := validateProposalSnapshot(ctx, tx, reservation, binding.ProposalID, binding.ProposalRevisionID, revision.Content); err != nil {
		var classified *foundation.Error
		if !errors.As(err, &classified) ||
			(classified.Kind != foundation.ErrorConsistencyViolation && classified.Kind != foundation.ErrorNotFound) {
			return false, err
		}
		return markPublicationRecovery(ctx, tx, binding, publicationStateMismatch, now)
	}
	var proposalStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM change_control.proposal WHERE id=$1 AND workspace_id=$2`,
		string(binding.ProposalID), string(binding.WorkspaceID)).Scan(&proposalStatus); err != nil {
		return false, classify(err, "AUTHORING_PROPOSAL_QUERY_FAILED")
	}
	switch proposalStatus {
	case "rejected":
		return closePublication(ctx, tx, binding, publicationRejected, now)
	case "needs_revision":
		return closePublication(ctx, tx, binding, publicationNeedsRevision, now)
	case "cancelled":
		return closePublication(ctx, tx, binding, publicationCancelled, now)
	}
	var previous *domain.ArticleRevision
	var commitWorkspace, commitProposal, commitRevision foundation.ID
	var gitCommit, targetPath, commitTargetMode, resultHash string
	var committedAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT workspace_id::text,proposal_id::text,revision_id::text,git_commit,target_path,target_mode,result_hash,created_at
		FROM change_control.proposal_commit
			WHERE proposal_id=$1 AND revision_id=$2`,
		string(binding.ProposalID), string(binding.ProposalRevisionID)).Scan(
		&commitWorkspace, &commitProposal, &commitRevision, &gitCommit, &targetPath, &commitTargetMode, &resultHash, &committedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, commitTransaction(ctx, tx)
	}
	if err != nil {
		return false, classify(err, "AUTHORING_PUBLICATION_COMMIT_QUERY_FAILED")
	}
	commitValid := commitWorkspace == binding.WorkspaceID && commitProposal == binding.ProposalID &&
		commitRevision == binding.ProposalRevisionID && targetPath == binding.TargetPath &&
		changecontroldomain.TargetMode(commitTargetMode) == binding.TargetMode &&
		resultHash == binding.ContentHash && resultHash == revision.ContentHash && validGitCommit(gitCommit)
	if !commitValid {
		return markPublicationRecovery(ctx, tx, binding, publicationCommitMismatch, now)
	}
	alreadyPublished := document.Lifecycle == domain.DocumentPublished &&
		document.CurrentPublishedRevisionID == revision.ID && revision.Status == domain.RevisionPublished &&
		revision.GitCommit == gitCommit
	if !alreadyPublished {
		stateValid := document.CanonicalPath == binding.TargetPath &&
			revision.ContentHash == binding.ContentHash &&
			(revision.Status == domain.RevisionDraft || revision.Status == domain.RevisionReview || revision.Status == domain.RevisionApproved)
		if binding.TargetMode == domain.ProposalTargetCreateOnly {
			stateValid = stateValid && document.Lifecycle == domain.DocumentDraft && document.CurrentPublishedRevisionID == ""
		} else {
			stateValid = stateValid && binding.TargetMode == domain.ProposalTargetReplace &&
				document.Lifecycle == domain.DocumentPublished && document.CurrentPublishedRevisionID != ""
		}
		if !stateValid {
			return markPublicationRecovery(ctx, tx, binding, publicationStateMismatch, now)
		}
		if document.CurrentPublishedRevisionID != "" && document.CurrentPublishedRevisionID != revision.ID {
			loaded, loadErr := loadRevisionByID(ctx, tx, binding.WorkspaceID, binding.DocumentID, document.CurrentPublishedRevisionID, true)
			if loadErr != nil {
				return false, loadErr
			}
			previous = &loaded
		}
	}
	if previous != nil {
		if previous.Status != domain.RevisionPublished || previous.GitCommit == "" ||
			(binding.TargetMode == domain.ProposalTargetReplace && previous.ContentHash != reservation.BaseVersion) {
			return markPublicationRecovery(ctx, tx, binding, publicationStateMismatch, now)
		}
		tag, err := tx.Exec(ctx, `UPDATE core.article_revision SET status='SUPERSEDED' WHERE id=$1 AND workspace_id=$2 AND document_id=$3 AND status='PUBLISHED'`,
			string(previous.ID), string(previous.WorkspaceID), string(previous.DocumentID))
		if err != nil {
			return false, classify(err, "AUTHORING_REVISION_SUPERSEDE_FAILED")
		}
		if tag.RowsAffected() != 1 {
			return false, inconsistent("current published revision supersede lost its lock")
		}
	}
	if !alreadyPublished {
		tag, err := tx.Exec(ctx, `
			UPDATE core.article_revision SET status='PUBLISHED',git_commit=$1
			WHERE id=$2 AND workspace_id=$3 AND document_id=$4 AND status IN ('DRAFT','REVIEW','APPROVED')`,
			gitCommit, string(revision.ID), string(revision.WorkspaceID), string(revision.DocumentID))
		if err != nil {
			return false, classify(err, "AUTHORING_REVISION_PUBLISH_FAILED")
		}
		if tag.RowsAffected() != 1 {
			return false, inconsistent("article revision publish did not update one row")
		}
		tag, err = tx.Exec(ctx, `
			UPDATE core.document
			SET lifecycle_status='PUBLISHED',current_published_revision_id=$1,version=version+1,
				updated_at=GREATEST(updated_at,$2)
			WHERE id=$3 AND workspace_id=$4 AND version=$5 AND canonical_path=$6`,
			string(revision.ID), now, string(document.ID), string(document.WorkspaceID), document.Version, binding.TargetPath)
		if err != nil {
			return false, classify(err, "AUTHORING_DOCUMENT_PUBLISH_FAILED")
		}
		if tag.RowsAffected() != 1 {
			return false, inconsistent("document publish compare-and-swap did not update one row")
		}
	}
	publishedAt := committedAt.UTC()
	if publishedAt.Before(binding.CreatedAt) {
		publishedAt = binding.CreatedAt
	}
	tag, err := tx.Exec(ctx, `
			UPDATE authoring.document_publication_binding
			SET status='PUBLISHED',git_commit=$1,error_code='',version=version+1,
				updated_at=GREATEST(updated_at,$2),published_at=$3
			WHERE id=$4 AND workspace_id=$5 AND version=$6 AND status=$7`,
		gitCommit, now, publishedAt, string(binding.ID), string(binding.WorkspaceID), binding.Version, string(binding.Status))
	if err != nil {
		return false, classify(err, "AUTHORING_PUBLICATION_FINALIZE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return false, inconsistent("publication finalize compare-and-swap did not update one row")
	}
	if err := commitTransaction(ctx, tx); err != nil {
		return false, err
	}
	return true, nil
}

func markPublicationRecovery(
	ctx context.Context,
	tx pgx.Tx,
	binding domain.PublicationBinding,
	errorCode string,
	now time.Time,
) (bool, error) {
	if binding.Status == domain.PublicationRecoveryRequired {
		if err := commitTransaction(ctx, tx); err != nil {
			return false, err
		}
		return false, nil
	}
	if binding.Status != domain.PublicationPending {
		return false, inconsistent("publication cannot enter recovery from its current status")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE authoring.document_publication_binding
		SET status='RECOVERY_REQUIRED',git_commit=NULL,error_code=$1,version=version+1,
			updated_at=GREATEST(updated_at,$2),published_at=NULL
		WHERE id=$3 AND workspace_id=$4 AND version=$5 AND status='PENDING'`,
		errorCode, now, string(binding.ID), string(binding.WorkspaceID), binding.Version)
	if err != nil {
		return false, classify(err, "AUTHORING_PUBLICATION_RECOVERY_MARK_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return false, inconsistent("publication recovery compare-and-swap did not update one row")
	}
	if err := commitTransaction(ctx, tx); err != nil {
		return false, err
	}
	return true, nil
}

func closePublication(
	ctx context.Context,
	tx pgx.Tx,
	binding domain.PublicationBinding,
	errorCode string,
	now time.Time,
) (bool, error) {
	if binding.Status == domain.PublicationClosed {
		return false, commitTransaction(ctx, tx)
	}
	if binding.Status != domain.PublicationPending && binding.Status != domain.PublicationRecoveryRequired {
		return false, inconsistent("publication cannot close from its current status")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE authoring.document_publication_binding
		SET status='CLOSED',git_commit=NULL,error_code=$1,version=version+1,
			updated_at=GREATEST(updated_at,$2),published_at=NULL
		WHERE id=$3 AND workspace_id=$4 AND version=$5 AND status=$6`,
		errorCode, now, string(binding.ID), string(binding.WorkspaceID), binding.Version, string(binding.Status))
	if err != nil {
		return false, classify(err, "AUTHORING_PUBLICATION_CLOSE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return false, inconsistent("publication close compare-and-swap did not update one row")
	}
	if err := commitTransaction(ctx, tx); err != nil {
		return false, err
	}
	return true, nil
}

func commitTransaction(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return nil
}

func lockPublishCommand(ctx context.Context, tx pgx.Tx, binding authoringapp.PublishBinding) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(binding.WorkspaceID)+":"+binding.IdempotencyKey); err != nil {
		return classify(err, "AUTHORING_COMMAND_LOCK_FAILED")
	}
	return nil
}

func loadPublicationFacts(ctx context.Context, tx pgx.Tx, binding authoringapp.PublishBinding) (domain.Document, domain.ArticleRevision, error) {
	document, revision, err := loadReservedPublicationFacts(ctx, tx, binding)
	if err != nil {
		return domain.Document{}, domain.ArticleRevision{}, err
	}
	latest, err := loadLatestRevision(ctx, tx, binding.WorkspaceID, binding.DocumentID)
	if err != nil {
		return domain.Document{}, domain.ArticleRevision{}, err
	}
	if latest.ID != revision.ID {
		return domain.Document{}, domain.ArticleRevision{}, foundation.NewError(foundation.ErrorVersionConflict, "AUTHORING_REVISION_NOT_LATEST", false, errors.New("only the latest frozen revision can be published"))
	}
	return document, revision, nil
}

func loadReservedPublicationFacts(ctx context.Context, tx pgx.Tx, binding authoringapp.PublishBinding) (domain.Document, domain.ArticleRevision, error) {
	document, err := loadDocument(ctx, tx, binding.WorkspaceID, binding.DocumentID, true)
	if err != nil {
		return domain.Document{}, domain.ArticleRevision{}, err
	}
	revision, err := loadRevisionByID(ctx, tx, binding.WorkspaceID, binding.DocumentID, binding.RevisionID, true)
	if err != nil {
		return domain.Document{}, domain.ArticleRevision{}, err
	}
	if revision.Status == domain.RevisionPublished || revision.Status == domain.RevisionSuperseded || revision.Status == domain.RevisionArchived {
		return domain.Document{}, domain.ArticleRevision{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodePublicationInvalid, false, errors.New("article revision cannot be published from its current status"))
	}
	return document, revision, nil
}

func loadRevisionByID(ctx context.Context, tx pgx.Tx, workspaceID, documentID, revisionID foundation.ID, forUpdate bool) (domain.ArticleRevision, error) {
	query := `SELECT ` + revisionColumns + ` FROM core.article_revision WHERE workspace_id=$1 AND document_id=$2 AND id=$3`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	revision, err := scanRevision(tx.QueryRow(ctx, query, string(workspaceID), string(documentID), string(revisionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ArticleRevision{}, notFound(err)
	}
	return revision, err
}

func loadReservationByKey(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string, forUpdate bool) (authoringapp.PublicationReservation, bool, error) {
	query := `SELECT ` + reservationColumns + ` FROM authoring.document_publication_reservation WHERE workspace_id=$1 AND idempotency_key=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanReservation(tx.QueryRow(ctx, query, string(workspaceID), key))
}

func loadReservationByID(ctx context.Context, tx pgx.Tx, workspaceID, id foundation.ID, forUpdate bool) (authoringapp.PublicationReservation, bool, error) {
	query := `SELECT ` + reservationColumns + ` FROM authoring.document_publication_reservation WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanReservation(tx.QueryRow(ctx, query, string(workspaceID), string(id)))
}

const reservationColumns = `id::text,workspace_id::text,document_id::text,article_revision_id::text,
		idempotency_key,request_hash,proposal_idempotency_key,target_path,content_hash,target_mode,
		base_version,absence_token,status,error_code,created_at,updated_at,closed_at,abandoned_at`

func scanReservation(row pgx.Row) (authoringapp.PublicationReservation, bool, error) {
	var reservation authoringapp.PublicationReservation
	if err := row.Scan(&reservation.ID, &reservation.WorkspaceID, &reservation.DocumentID,
		&reservation.ArticleRevisionID, &reservation.IdempotencyKey, &reservation.RequestHash,
		&reservation.ProposalIdempotencyKey, &reservation.TargetPath, &reservation.ContentHash,
		&reservation.TargetMode, &reservation.BaseVersion, &reservation.AbsenceToken, &reservation.Status,
		&reservation.ErrorCode, &reservation.CreatedAt, &reservation.UpdatedAt, &reservation.ClosedAt,
		&reservation.AbandonedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return authoringapp.PublicationReservation{}, false, nil
		}
		return authoringapp.PublicationReservation{}, false, classify(err, "AUTHORING_PUBLICATION_RESERVATION_QUERY_FAILED")
	}
	reservation.CreatedAt = reservation.CreatedAt.UTC()
	reservation.UpdatedAt = reservation.UpdatedAt.UTC()
	if reservation.ClosedAt != nil {
		value := reservation.ClosedAt.UTC()
		reservation.ClosedAt = &value
	}
	if reservation.AbandonedAt != nil {
		value := reservation.AbandonedAt.UTC()
		reservation.AbandonedAt = &value
	}
	return reservation, true, nil
}

func loadPendingReservationByDocument(ctx context.Context, tx pgx.Tx, workspaceID, documentID foundation.ID) (authoringapp.PublicationReservation, bool, error) {
	return scanReservation(tx.QueryRow(ctx, `SELECT `+reservationColumns+`
		FROM authoring.document_publication_reservation
		WHERE workspace_id=$1 AND document_id=$2 AND status='PENDING' FOR UPDATE`,
		string(workspaceID), string(documentID)))
}

func loadPublishReceipt(ctx context.Context, tx pgx.Tx, binding authoringapp.PublishBinding) (domain.PublicationBinding, bool, error) {
	var commandType, requestHash string
	var documentID, revisionID, proposalID, proposalRevisionID foundation.ID
	err := tx.QueryRow(ctx, `
			SELECT command_type,request_hash,COALESCE(document_id::text,''),COALESCE(article_revision_id::text,''),
				COALESCE(proposal_id::text,''),COALESCE(proposal_revision_id::text,'')
		FROM authoring.working_draft_command
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(binding.WorkspaceID), binding.IdempotencyKey).Scan(
		&commandType, &requestHash, &documentID, &revisionID, &proposalID, &proposalRevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PublicationBinding{}, false, nil
	}
	if err != nil {
		return domain.PublicationBinding{}, false, classify(err, "AUTHORING_RECEIPT_QUERY_FAILED")
	}
	if commandType != authoringapp.CommandPublish || requestHash != binding.RequestHash ||
		documentID != binding.DocumentID || revisionID != binding.RevisionID {
		return domain.PublicationBinding{}, false, idempotencyConflict(errors.New("idempotency key is bound to another authoring request"))
	}
	publication, found, err := loadPublicationByProposal(ctx, tx, binding.WorkspaceID, proposalID, proposalRevisionID)
	if err != nil {
		return domain.PublicationBinding{}, false, err
	}
	if !found {
		return domain.PublicationBinding{}, false, inconsistent("publication receipt has no immutable binding")
	}
	return publication, true, nil
}

const publicationColumns = `id::text,reservation_id::text,workspace_id::text,document_id::text,
	article_revision_id::text,proposal_id::text,proposal_revision_id::text,target_path,content_hash,
	target_mode,absence_token,status,COALESCE(git_commit,''),error_code,version,created_at,updated_at,published_at`

func loadPublicationByID(ctx context.Context, tx pgx.Tx, workspaceID, id foundation.ID, forUpdate bool) (domain.PublicationBinding, bool, error) {
	query := `SELECT ` + publicationColumns + ` FROM authoring.document_publication_binding WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanPublication(tx.QueryRow(ctx, query, string(workspaceID), string(id)))
}

func loadPublicationByRevision(ctx context.Context, tx pgx.Tx, workspaceID, revisionID foundation.ID) (domain.PublicationBinding, bool, error) {
	return scanPublication(tx.QueryRow(ctx, `SELECT `+publicationColumns+`
		FROM authoring.document_publication_binding WHERE workspace_id=$1 AND article_revision_id=$2`,
		string(workspaceID), string(revisionID)))
}

func loadPublicationByProposal(ctx context.Context, tx pgx.Tx, workspaceID, proposalID, revisionID foundation.ID) (domain.PublicationBinding, bool, error) {
	return scanPublication(tx.QueryRow(ctx, `SELECT `+publicationColumns+`
		FROM authoring.document_publication_binding
		WHERE workspace_id=$1 AND proposal_id=$2 AND proposal_revision_id=$3`,
		string(workspaceID), string(proposalID), string(revisionID)))
}

func loadPublicationByReservation(ctx context.Context, tx pgx.Tx, workspaceID, reservationID foundation.ID, forUpdate bool) (domain.PublicationBinding, bool, error) {
	query := `SELECT ` + publicationColumns + ` FROM authoring.document_publication_binding WHERE workspace_id=$1 AND reservation_id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanPublication(tx.QueryRow(ctx, query, string(workspaceID), string(reservationID)))
}

func scanPublication(row pgx.Row) (domain.PublicationBinding, bool, error) {
	var publication domain.PublicationBinding
	var publishedAt *time.Time
	if err := row.Scan(&publication.ID, &publication.ReservationID, &publication.WorkspaceID,
		&publication.DocumentID, &publication.ArticleRevisionID, &publication.ProposalID,
		&publication.ProposalRevisionID, &publication.TargetPath, &publication.ContentHash,
		&publication.TargetMode, &publication.AbsenceToken, &publication.Status, &publication.GitCommit,
		&publication.ErrorCode, &publication.Version, &publication.CreatedAt, &publication.UpdatedAt,
		&publishedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PublicationBinding{}, false, nil
		}
		return domain.PublicationBinding{}, false, classify(err, "AUTHORING_PUBLICATION_QUERY_FAILED")
	}
	publication.CreatedAt = publication.CreatedAt.UTC()
	publication.UpdatedAt = publication.UpdatedAt.UTC()
	if publishedAt != nil {
		value := publishedAt.UTC()
		publication.PublishedAt = &value
	}
	if err := publication.Validate(); err != nil {
		return domain.PublicationBinding{}, false, inconsistent("stored publication binding is invalid")
	}
	return publication, true, nil
}

func validateProposalSnapshot(ctx context.Context, tx pgx.Tx, reservation authoringapp.PublicationReservation, proposalID, revisionID foundation.ID, content string) error {
	var workspaceID, boundProposalID foundation.ID
	var proposalType, proposalKey, targetPath, targetMode, baseHash, proposalContent string
	err := tx.QueryRow(ctx, `
			SELECT proposal.workspace_id::text,proposal.proposal_type,proposal.idempotency_key,
				revision.proposal_id::text,revision.target_path,
				revision.target_mode,revision.base_hash,revision.content
			FROM change_control.proposal proposal
			JOIN change_control.proposal_revision revision ON revision.proposal_id=proposal.id
			WHERE proposal.id=$1 AND revision.id=$2
			FOR SHARE OF proposal,revision`, string(proposalID), string(revisionID)).Scan(
		&workspaceID, &proposalType, &proposalKey, &boundProposalID, &targetPath, &targetMode, &baseHash, &proposalContent)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(err)
	}
	if err != nil {
		return classify(err, "AUTHORING_PROPOSAL_QUERY_FAILED")
	}
	if workspaceID != reservation.WorkspaceID || proposalType != "file_patch" ||
		proposalKey != reservation.ProposalIdempotencyKey || boundProposalID != proposalID || targetPath != reservation.TargetPath ||
		changecontroldomain.NormalizeTargetMode(changecontroldomain.TargetMode(targetMode)) != reservation.TargetMode ||
		baseHash != reservation.BaseVersion || proposalContent != content ||
		domain.ComputeContentHash(proposalContent) != reservation.ContentHash {
		return inconsistent("proposal snapshot does not match the frozen article revision")
	}
	return nil
}

func validateStoredReservation(reservation authoringapp.PublicationReservation, document domain.Document, revision domain.ArticleRevision) error {
	if err := validateReservationShape(reservation); err != nil {
		return err
	}
	if reservation.WorkspaceID != document.WorkspaceID || reservation.DocumentID != document.ID ||
		reservation.ArticleRevisionID != revision.ID || revision.DocumentID != document.ID ||
		reservation.TargetPath != document.CanonicalPath || reservation.ContentHash != revision.ContentHash ||
		reservation.Status != authoringapp.PublicationReservationPending {
		return inconsistent("stored publication reservation is invalid")
	}
	proposalKey, err := domain.ComputeProposalIdempotencyKey(
		reservation.WorkspaceID, reservation.DocumentID, reservation.ArticleRevisionID,
		reservation.TargetPath, reservation.ContentHash, reservation.TargetMode, reservation.AbsenceToken,
	)
	if err != nil || proposalKey != reservation.ProposalIdempotencyKey {
		return inconsistent("stored publication reservation proposal key is invalid")
	}
	if reservation.TargetMode == domain.ProposalTargetCreateOnly {
		token, err := domain.ComputeAbsenceToken(reservation.WorkspaceID, reservation.TargetPath)
		if err != nil || token != reservation.AbsenceToken || reservation.BaseVersion != token ||
			document.Lifecycle != domain.DocumentDraft || document.CurrentPublishedRevisionID != "" {
			return inconsistent("stored create-only reservation is invalid")
		}
	} else if reservation.TargetMode != domain.ProposalTargetReplace || reservation.AbsenceToken != "" ||
		!validHash(reservation.BaseVersion) || document.Lifecycle != domain.DocumentPublished ||
		document.CurrentPublishedRevisionID == "" {
		return inconsistent("stored replace reservation is invalid")
	}
	return nil
}

func validateReservationShape(reservation authoringapp.PublicationReservation) error {
	if !validID(reservation.ID) || !validID(reservation.WorkspaceID) || !validID(reservation.DocumentID) ||
		!validID(reservation.ArticleRevisionID) || reservation.IdempotencyKey == "" ||
		reservation.IdempotencyKey != strings.TrimSpace(reservation.IdempotencyKey) ||
		!utf8.ValidString(reservation.IdempotencyKey) || strings.ContainsAny(reservation.IdempotencyKey, "\r\n\x00") ||
		!validHash(reservation.RequestHash) || reservation.CreatedAt.IsZero() ||
		reservation.UpdatedAt.Before(reservation.CreatedAt) {
		return inconsistent("stored publication reservation fields are invalid")
	}
	if (reservation.Status == authoringapp.PublicationReservationPending && (reservation.ClosedAt != nil || reservation.AbandonedAt != nil || reservation.ErrorCode != "")) ||
		(reservation.Status == authoringapp.PublicationReservationClosed &&
			(reservation.ClosedAt == nil || reservation.ClosedAt.Before(reservation.CreatedAt) || reservation.AbandonedAt != nil || reservation.ErrorCode != "")) ||
		(reservation.Status == authoringapp.PublicationReservationAbandoned &&
			(reservation.ClosedAt != nil || reservation.AbandonedAt == nil || reservation.AbandonedAt.Before(reservation.CreatedAt) || reservation.ErrorCode == "")) ||
		(reservation.Status != authoringapp.PublicationReservationPending &&
			reservation.Status != authoringapp.PublicationReservationClosed &&
			reservation.Status != authoringapp.PublicationReservationAbandoned) {
		return inconsistent("stored publication reservation state is invalid")
	}
	proposalKey, err := domain.ComputeProposalIdempotencyKey(
		reservation.WorkspaceID, reservation.DocumentID, reservation.ArticleRevisionID,
		reservation.TargetPath, reservation.ContentHash, reservation.TargetMode, reservation.AbsenceToken,
	)
	if err != nil || proposalKey != reservation.ProposalIdempotencyKey {
		return inconsistent("stored publication reservation proposal key is invalid")
	}
	if reservation.TargetMode == domain.ProposalTargetCreateOnly {
		token, err := domain.ComputeAbsenceToken(reservation.WorkspaceID, reservation.TargetPath)
		if err != nil || token != reservation.AbsenceToken || reservation.BaseVersion != token {
			return inconsistent("stored create-only reservation shape is invalid")
		}
	} else if reservation.TargetMode != domain.ProposalTargetReplace || reservation.AbsenceToken != "" || !validHash(reservation.BaseVersion) {
		return inconsistent("stored replace reservation shape is invalid")
	}
	return nil
}

func validateAbandonedReservation(reservation authoringapp.PublicationReservation) error {
	if err := validateReservationShape(reservation); err != nil {
		return err
	}
	if reservation.Status != authoringapp.PublicationReservationAbandoned || reservation.ErrorCode != authoringapp.ErrorCodePublicationTargetParentNotFound {
		return inconsistent("stored abandoned publication reservation is invalid")
	}
	return nil
}

func sameReservationCommand(reservation authoringapp.PublicationReservation, binding authoringapp.PublishBinding) bool {
	return reservation.WorkspaceID == binding.WorkspaceID && reservation.DocumentID == binding.DocumentID &&
		reservation.ArticleRevisionID == binding.RevisionID && reservation.IdempotencyKey == binding.IdempotencyKey &&
		reservation.RequestHash == binding.RequestHash
}

func reservationMatchesBinding(reservation authoringapp.PublicationReservation, binding domain.PublicationBinding) bool {
	return validateReservationShape(reservation) == nil && reservation.ID == binding.ReservationID && reservation.WorkspaceID == binding.WorkspaceID &&
		reservation.DocumentID == binding.DocumentID && reservation.ArticleRevisionID == binding.ArticleRevisionID &&
		reservation.TargetPath == binding.TargetPath && reservation.ContentHash == binding.ContentHash &&
		reservation.TargetMode == binding.TargetMode && reservation.AbsenceToken == binding.AbsenceToken
}

func validateReservePublicationRecord(record authoringapp.ReservePublicationRecord) error {
	hash, err := domain.ComputePublishRequestHash(record.Binding.WorkspaceID, record.Binding.DocumentID, record.Binding.RevisionID)
	if err != nil || hash != record.Binding.RequestHash || !validID(record.ReservationID) ||
		record.Binding.IdempotencyKey == "" || record.Binding.IdempotencyKey != strings.TrimSpace(record.Binding.IdempotencyKey) ||
		!utf8.ValidString(record.Binding.IdempotencyKey) || strings.ContainsAny(record.Binding.IdempotencyKey, "\r\n\x00") ||
		len(record.Binding.IdempotencyKey) > authoringapp.MaxIdempotencyKeyBytes || record.ReservedAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("publication reservation record is invalid"))
	}
	return nil
}

func validateCompletePublicationRecord(record authoringapp.CompletePublicationRecord) error {
	if err := validateReservePublicationRecord(authoringapp.ReservePublicationRecord{
		Binding: record.Binding, ReservationID: record.ReservationID, ReservedAt: record.CompletedAt,
	}); err != nil || !validID(record.PublicationID) || !validID(record.ProposalID) || !validID(record.ProposalRevisionID) {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("publication completion record is invalid"))
	}
	return nil
}

func validateAbandonPublicationRecord(record authoringapp.AbandonPublicationRecord) error {
	if err := validateReservePublicationRecord(authoringapp.ReservePublicationRecord{
		Binding: record.Binding, ReservationID: record.ReservationID, ReservedAt: record.AbandonedAt,
	}); err != nil || record.ErrorCode != authoringapp.ErrorCodePublicationTargetParentNotFound {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("publication abandonment record is invalid"))
	}
	return nil
}

func validGitCommit(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validLowerHex(value)
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func itoa(value int) string {
	const digits = "0123456789"
	if value < 10 {
		return string(digits[value])
	}
	return string(digits[value/10]) + string(digits[value%10])
}
