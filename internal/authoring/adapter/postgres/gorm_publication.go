package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

func gormScanReservation(ctx context.Context, database *gorm.DB, query string, args ...any) (authoringapp.PublicationReservation, bool, error) {
	row, e := gormRow(database, query, args...)
	if e != nil {
		return authoringapp.PublicationReservation{}, false, classifyGORM(ctx, e, "AUTHORING_PUBLICATION_RESERVATION_QUERY_FAILED")
	}
	var r authoringapp.PublicationReservation
	e = row.Scan(&r.ID, &r.WorkspaceID, &r.DocumentID, &r.ArticleRevisionID, &r.IdempotencyKey, &r.RequestHash, &r.ProposalIdempotencyKey, &r.TargetPath, &r.ContentHash, &r.TargetMode, &r.BaseVersion, &r.AbsenceToken, &r.Status, &r.ErrorCode, &r.CreatedAt, &r.UpdatedAt, &r.ClosedAt, &r.AbandonedAt)
	if gormNoRows(e) {
		return authoringapp.PublicationReservation{}, false, nil
	}
	if e != nil {
		return authoringapp.PublicationReservation{}, false, classifyGORM(ctx, e, "AUTHORING_PUBLICATION_RESERVATION_QUERY_FAILED")
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = r.UpdatedAt.UTC()
	if r.ClosedAt != nil {
		v := r.ClosedAt.UTC()
		r.ClosedAt = &v
	}
	if r.AbandonedAt != nil {
		v := r.AbandonedAt.UTC()
		r.AbandonedAt = &v
	}
	return r, true, nil
}
func gormScanPublication(ctx context.Context, database *gorm.DB, query string, args ...any) (domain.PublicationBinding, bool, error) {
	row, e := gormRow(database, query, args...)
	if e != nil {
		return domain.PublicationBinding{}, false, classifyGORM(ctx, e, "AUTHORING_PUBLICATION_QUERY_FAILED")
	}
	return gormScanPublicationRow(ctx, row)
}

func gormScanPublicationRow(ctx context.Context, row interface{ Scan(...any) error }) (domain.PublicationBinding, bool, error) {
	var p domain.PublicationBinding
	var publishedAt *time.Time
	e := row.Scan(&p.ID, &p.ReservationID, &p.WorkspaceID, &p.DocumentID, &p.ArticleRevisionID, &p.ProposalID, &p.ProposalRevisionID, &p.TargetPath, &p.ContentHash, &p.TargetMode, &p.AbsenceToken, &p.Status, &p.GitCommit, &p.ErrorCode, &p.Version, &p.CreatedAt, &p.UpdatedAt, &publishedAt)
	if gormNoRows(e) {
		return domain.PublicationBinding{}, false, nil
	}
	if e != nil {
		return domain.PublicationBinding{}, false, classifyGORM(ctx, e, "AUTHORING_PUBLICATION_QUERY_FAILED")
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	if publishedAt != nil {
		v := publishedAt.UTC()
		p.PublishedAt = &v
	}
	if p.Validate() != nil {
		return domain.PublicationBinding{}, false, inconsistent("stored publication binding is invalid")
	}
	return p, true, nil
}
func gormLoadReservation(ctx context.Context, tx *gorm.DB, w, id foundation.ID, lock bool) (authoringapp.PublicationReservation, bool, error) {
	q := `SELECT ` + reservationColumns + ` FROM authoring.document_publication_reservation WHERE workspace_id=? AND id=?`
	if lock {
		q += ` FOR UPDATE`
	}
	return gormScanReservation(ctx, tx.WithContext(ctx), q, string(w), string(id))
}
func gormLoadReservationKey(ctx context.Context, tx *gorm.DB, w foundation.ID, key string, lock bool) (authoringapp.PublicationReservation, bool, error) {
	q := `SELECT ` + reservationColumns + ` FROM authoring.document_publication_reservation WHERE workspace_id=? AND idempotency_key=?`
	if lock {
		q += ` FOR UPDATE`
	}
	return gormScanReservation(ctx, tx.WithContext(ctx), q, string(w), key)
}
func gormLoadPublicationReservation(ctx context.Context, tx *gorm.DB, w, id foundation.ID, lock bool) (domain.PublicationBinding, bool, error) {
	q := `SELECT ` + publicationColumns + ` FROM authoring.document_publication_binding WHERE workspace_id=? AND reservation_id=?`
	if lock {
		q += ` FOR UPDATE`
	}
	return gormScanPublication(ctx, tx.WithContext(ctx), q, string(w), string(id))
}
func gormLoadPublicationRevision(ctx context.Context, tx *gorm.DB, w, id foundation.ID) (domain.PublicationBinding, bool, error) {
	return gormScanPublication(ctx, tx.WithContext(ctx), `SELECT `+publicationColumns+` FROM authoring.document_publication_binding WHERE workspace_id=? AND article_revision_id=?`, string(w), string(id))
}
func gormLoadPublicationProposal(ctx context.Context, tx *gorm.DB, w, p, r foundation.ID) (domain.PublicationBinding, bool, error) {
	return gormScanPublication(ctx, tx.WithContext(ctx), `SELECT `+publicationColumns+` FROM authoring.document_publication_binding WHERE workspace_id=? AND proposal_id=? AND proposal_revision_id=?`, string(w), string(p), string(r))
}

func gormLoadPublicationID(ctx context.Context, tx *gorm.DB, w, id foundation.ID, lock bool) (domain.PublicationBinding, bool, error) {
	q := `SELECT ` + publicationColumns + ` FROM authoring.document_publication_binding WHERE workspace_id=? AND id=?`
	if lock {
		q += ` FOR UPDATE`
	}
	return gormScanPublication(ctx, tx.WithContext(ctx), q, string(w), string(id))
}

func (repository *GORMRepository) ReservePublication(ctx context.Context, record authoringapp.ReservePublicationRecord) (result authoringapp.PublicationPreparation, err error) {
	if err = validateReservePublicationRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		if existing, found, e := gormLoadPublishReceipt(ctx, tx, record.Binding); e != nil {
			return e
		} else if found {
			result.Existing = &existing
			result.Replayed = true
			return nil
		}
		reservation, found, e := gormLoadReservationKey(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey, false)
		if e != nil {
			return e
		}
		if found {
			if !sameReservationCommand(reservation, record.Binding) {
				return idempotencyConflict(errors.New("publication key is bound to another revision"))
			}
			document, revision, e := gormLoadReservedPublicationFactsGORM(ctx, tx, record.Binding)
			if e != nil {
				return e
			}
			reservation, found, e = gormLoadReservation(ctx, tx, record.Binding.WorkspaceID, reservation.ID, true)
			if e != nil || !found || !sameReservationCommand(reservation, record.Binding) {
				if e != nil {
					return e
				}
				return inconsistent("publication reservation changed while it was locked")
			}
			if reservation.Status == authoringapp.PublicationReservationClosed {
				p, pf, e := gormLoadPublicationReservation(ctx, tx, record.Binding.WorkspaceID, reservation.ID, false)
				if e != nil {
					return e
				}
				if !pf {
					return inconsistent("closed publication reservation has no binding")
				}
				result.Existing = &p
				result.Replayed = true
				return nil
			}
			if reservation.Status == authoringapp.PublicationReservationAbandoned {
				if e := validateAbandonedReservation(reservation); e != nil {
					return e
				}
				result.Reservation = reservation
				result.Replayed = true
				return nil
			}
			if e := validateStoredReservation(reservation, document, revision); e != nil {
				return e
			}
			result.Reservation = reservation
			result.Document = document
			result.Revision = revision
			result.Replayed = true
			return nil
		}
		document, revision, e := gormLoadPublicationFactsGORM(ctx, tx, record.Binding)
		if e != nil {
			return e
		}
		if _, found, e := gormLoadPublicationRevision(ctx, tx, record.Binding.WorkspaceID, record.Binding.RevisionID); e != nil {
			return e
		} else if found {
			return publicationConflict("article revision already has a publication binding")
		}
		if pending, pf, e := gormScanReservation(ctx, tx, `SELECT `+reservationColumns+` FROM authoring.document_publication_reservation WHERE workspace_id=? AND document_id=? AND status='PENDING' FOR UPDATE`, string(record.Binding.WorkspaceID), string(record.Binding.DocumentID)); e != nil {
			return e
		} else if pf {
			return publicationConflict("document already has a pending publication reservation: " + string(pending.ID))
		}
		mode := domain.ProposalTargetCreateOnly
		baseVersion, absenceToken := "", ""
		if document.Lifecycle == domain.DocumentDraft && document.CurrentPublishedRevisionID == "" {
			absenceToken, e = domain.ComputeAbsenceToken(document.WorkspaceID, document.CanonicalPath)
			if e != nil {
				return e
			}
			baseVersion = absenceToken
		} else if document.Lifecycle == domain.DocumentPublished && document.CurrentPublishedRevisionID != "" {
			mode = domain.ProposalTargetReplace
			current, e := gormLoadRevision(ctx, tx, document.WorkspaceID, document.ID, document.CurrentPublishedRevisionID, true)
			if e != nil {
				return e
			}
			if current.Status != domain.RevisionPublished || current.GitCommit == "" {
				return inconsistent("published document current revision is invalid")
			}
			baseVersion = current.ContentHash
		} else {
			return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodePublicationInvalid, false, errors.New("document lifecycle cannot start a publication"))
		}
		proposalKey, e := domain.ComputeProposalIdempotencyKey(document.WorkspaceID, document.ID, revision.ID, document.CanonicalPath, revision.ContentHash, mode, absenceToken)
		if e != nil {
			return e
		}
		at := record.ReservedAt.UTC().Truncate(time.Microsecond)
		reservation = authoringapp.PublicationReservation{ID: record.ReservationID, WorkspaceID: document.WorkspaceID, DocumentID: document.ID, ArticleRevisionID: revision.ID, IdempotencyKey: record.Binding.IdempotencyKey, RequestHash: record.Binding.RequestHash, ProposalIdempotencyKey: proposalKey, TargetPath: document.CanonicalPath, ContentHash: revision.ContentHash, TargetMode: mode, BaseVersion: baseVersion, AbsenceToken: absenceToken, Status: authoringapp.PublicationReservationPending, CreatedAt: at, UpdatedAt: at}
		if e := tx.Exec(`INSERT INTO authoring.document_publication_reservation(id,workspace_id,document_id,article_revision_id,idempotency_key,request_hash,proposal_idempotency_key,target_path,content_hash,target_mode,base_version,absence_token,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'PENDING',?,?)`, string(reservation.ID), string(reservation.WorkspaceID), string(reservation.DocumentID), string(reservation.ArticleRevisionID), reservation.IdempotencyKey, reservation.RequestHash, reservation.ProposalIdempotencyKey, reservation.TargetPath, reservation.ContentHash, string(reservation.TargetMode), reservation.BaseVersion, reservation.AbsenceToken, at, at).Error; e != nil {
			return classifyGORM(ctx, e, "AUTHORING_PUBLICATION_RESERVATION_FAILED")
		}
		result = authoringapp.PublicationPreparation{Reservation: reservation, Document: document, Revision: revision}
		return nil
	}, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	if err != nil {
		return authoringapp.PublicationPreparation{}, err
	}
	return result, err
}

func (repository *GORMRepository) AbandonPublication(ctx context.Context, record authoringapp.AbandonPublicationRecord) (result authoringapp.PublicationReservation, err error) {
	if err = validateAbandonPublicationRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if e := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); e != nil {
			return e
		}
		document, revision, e := gormLoadReservedPublicationFactsGORM(ctx, tx, record.Binding)
		if e != nil {
			return e
		}
		_ = document
		_ = revision
		reservation, found, e := gormLoadReservation(ctx, tx, record.Binding.WorkspaceID, record.ReservationID, true)
		if e != nil {
			return e
		}
		if !found || !sameReservationCommand(reservation, record.Binding) {
			return inconsistent("publication reservation is missing or mismatched")
		}
		if _, bf, e := gormLoadPublicationReservation(ctx, tx, record.Binding.WorkspaceID, reservation.ID, true); e != nil {
			return e
		} else if bf {
			return publicationConflict("publication reservation already has an immutable binding")
		}
		if reservation.Status == authoringapp.PublicationReservationAbandoned {
			if e := validateAbandonedReservation(reservation); e != nil {
				return e
			}
			if reservation.ErrorCode != record.ErrorCode {
				return idempotencyConflict(errors.New("abandoned publication reservation has another error code"))
			}
			result = reservation
			return nil
		}
		if reservation.Status != authoringapp.PublicationReservationPending {
			return publicationConflict("publication reservation cannot be abandoned")
		}
		if e := validateStoredReservation(reservation, document, revision); e != nil {
			return e
		}
		at := record.AbandonedAt.UTC().Truncate(time.Microsecond)
		if at.Before(reservation.CreatedAt) {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("publication abandonment predates reservation"))
		}
		res := tx.Exec(`UPDATE authoring.document_publication_reservation SET status='ABANDONED',error_code=?,abandoned_at=?,updated_at=? WHERE id=? AND workspace_id=? AND status='PENDING'`, record.ErrorCode, at, at, string(reservation.ID), string(reservation.WorkspaceID))
		if res.Error != nil {
			return classifyGORM(ctx, res.Error, "AUTHORING_PUBLICATION_RESERVATION_ABANDON_FAILED")
		}
		if res.RowsAffected != 1 {
			return inconsistent("publication reservation abandonment compare-and-swap did not update one row")
		}
		reservation.Status = authoringapp.PublicationReservationAbandoned
		reservation.ErrorCode = record.ErrorCode
		reservation.UpdatedAt = at
		reservation.AbandonedAt = &at
		result = reservation
		return validateAbandonedReservation(result)
	}, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	if err != nil {
		return authoringapp.PublicationReservation{}, err
	}
	return result, err
}

func (repository *GORMRepository) CompletePublication(ctx context.Context, record authoringapp.CompletePublicationRecord) (result authoringapp.PublishResult, err error) {
	if err = validateCompletePublicationRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if e := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); e != nil {
			return e
		}
		if existing, found, e := gormLoadPublishReceipt(ctx, tx, record.Binding); e != nil {
			return e
		} else if found {
			if existing.ProposalID != record.ProposalID || existing.ProposalRevisionID != record.ProposalRevisionID {
				return idempotencyConflict(errors.New("publication receipt is bound to another proposal"))
			}
			result.Publication = existing
			result.Replayed = true
			return nil
		}
		document, revision, e := gormLoadReservedPublicationFactsGORM(ctx, tx, record.Binding)
		if e != nil {
			return e
		}
		reservation, found, e := gormLoadReservation(ctx, tx, record.Binding.WorkspaceID, record.ReservationID, true)
		if e != nil {
			return e
		}
		if !found || !sameReservationCommand(reservation, record.Binding) {
			return inconsistent("publication reservation is missing or mismatched")
		}
		if reservation.Status == authoringapp.PublicationReservationClosed {
			p, pf, e := gormLoadPublicationReservation(ctx, tx, record.Binding.WorkspaceID, reservation.ID, false)
			if e != nil {
				return e
			}
			if !pf || p.ProposalID != record.ProposalID || p.ProposalRevisionID != record.ProposalRevisionID {
				return idempotencyConflict(errors.New("closed publication reservation is bound to another proposal"))
			}
			result.Publication = p
			result.Replayed = true
			return nil
		}
		if reservation.Status != authoringapp.PublicationReservationPending {
			return inconsistent("publication reservation cannot be completed")
		}
		if record.CompletedAt.UTC().Truncate(time.Microsecond).Before(reservation.CreatedAt) {
			return inconsistent("publication reservation cannot be completed")
		}
		if e := validateStoredReservation(reservation, document, revision); e != nil {
			return e
		}
		if e := gormValidateProposalSnapshot(ctx, tx, reservation, record.ProposalID, record.ProposalRevisionID, revision.Content); e != nil {
			return e
		}
		at := record.CompletedAt.UTC().Truncate(time.Microsecond)
		publication := domain.PublicationBinding{ID: record.PublicationID, ReservationID: reservation.ID, WorkspaceID: reservation.WorkspaceID, DocumentID: reservation.DocumentID, ArticleRevisionID: reservation.ArticleRevisionID, ProposalID: record.ProposalID, ProposalRevisionID: record.ProposalRevisionID, TargetPath: reservation.TargetPath, ContentHash: reservation.ContentHash, TargetMode: reservation.TargetMode, AbsenceToken: reservation.AbsenceToken, Status: domain.PublicationPending, Version: 1, CreatedAt: at, UpdatedAt: at}
		if e := publication.Validate(); e != nil {
			return e
		}
		if e := tx.Exec(`INSERT INTO authoring.document_publication_binding(id,reservation_id,workspace_id,document_id,article_revision_id,proposal_id,proposal_revision_id,target_path,content_hash,target_mode,absence_token,status,git_commit,error_code,version,created_at,updated_at,published_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'PENDING',NULL,'',1,?,?,NULL)`, string(publication.ID), string(publication.ReservationID), string(publication.WorkspaceID), string(publication.DocumentID), string(publication.ArticleRevisionID), string(publication.ProposalID), string(publication.ProposalRevisionID), publication.TargetPath, publication.ContentHash, string(publication.TargetMode), publication.AbsenceToken, at, at).Error; e != nil {
			return classifyGORM(ctx, e, "AUTHORING_PUBLICATION_BIND_FAILED")
		}
		res := tx.Exec(`UPDATE authoring.document_publication_reservation SET status='CLOSED',updated_at=?,closed_at=? WHERE id=? AND status='PENDING'`, at, at, string(reservation.ID))
		if res.Error != nil {
			return classifyGORM(ctx, res.Error, "AUTHORING_PUBLICATION_RESERVATION_CLOSE_FAILED")
		}
		if res.RowsAffected != 1 {
			return inconsistent("publication reservation close did not update one row")
		}
		if e := tx.Exec(`INSERT INTO authoring.working_draft_command(workspace_id,idempotency_key,request_hash,command_type,working_draft_id,expected_version,result_draft_version,document_id,document_version,article_revision_id,revision_no,parent_revision_id,response_draft_created_at,response_document_created_at,response_document_lifecycle,response_published_revision_id,response_title,response_target_path,response_content_hash,proposal_id,proposal_revision_id,created_at) VALUES(?,?,?,'PUBLISH',NULL,0,NULL,?,?,?,?,NULLIF(?, '')::uuid,NULL,?,?,NULLIF(?, '')::uuid,?,?,?,?,?,?)`, string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash, string(document.ID), document.Version, string(revision.ID), revision.RevisionNo, string(revision.ParentRevisionID), document.CreatedAt.UTC(), string(document.Lifecycle), string(document.CurrentPublishedRevisionID), document.Title, document.CanonicalPath, revision.ContentHash, string(record.ProposalID), string(record.ProposalRevisionID), at).Error; e != nil {
			return classifyGORM(ctx, e, "AUTHORING_RECEIPT_CREATE_FAILED")
		}
		result.Publication = publication
		return nil
	}, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	if err != nil {
		return authoringapp.PublishResult{}, err
	}
	return result, err
}

func gormLoadPublicationFactsGORM(ctx context.Context, tx *gorm.DB, b authoringapp.PublishBinding) (domain.Document, domain.ArticleRevision, error) {
	d, r, e := gormLoadReservedPublicationFactsGORM(ctx, tx, b)
	if e != nil {
		return domain.Document{}, domain.ArticleRevision{}, e
	}
	latest, e := gormLoadLatestRevision(ctx, tx, b.WorkspaceID, b.DocumentID, true)
	if e != nil {
		return domain.Document{}, domain.ArticleRevision{}, e
	}
	if latest.ID != r.ID {
		return domain.Document{}, domain.ArticleRevision{}, foundation.NewError(foundation.ErrorVersionConflict, "AUTHORING_REVISION_NOT_LATEST", false, errors.New("only the latest frozen revision can be published"))
	}
	return d, r, nil
}

func gormLoadReservedPublicationFactsGORM(ctx context.Context, tx *gorm.DB, b authoringapp.PublishBinding) (domain.Document, domain.ArticleRevision, error) {
	d, e := gormLoadDocument(ctx, tx, b.WorkspaceID, b.DocumentID, true)
	if e != nil {
		return domain.Document{}, domain.ArticleRevision{}, e
	}
	r, e := gormLoadRevision(ctx, tx, b.WorkspaceID, b.DocumentID, b.RevisionID, true)
	if e != nil {
		return domain.Document{}, domain.ArticleRevision{}, e
	}
	if r.Status == domain.RevisionPublished || r.Status == domain.RevisionSuperseded || r.Status == domain.RevisionArchived {
		return domain.Document{}, domain.ArticleRevision{}, foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodePublicationInvalid, false, errors.New("article revision cannot be published from its current status"))
	}
	return d, r, nil
}
func gormLoadPublishReceipt(ctx context.Context, tx *gorm.DB, b authoringapp.PublishBinding) (domain.PublicationBinding, bool, error) {
	row, e := gormRow(tx.WithContext(ctx), `SELECT command_type,request_hash,COALESCE(document_id::text,''),COALESCE(article_revision_id::text,''),COALESCE(proposal_id::text,''),COALESCE(proposal_revision_id::text,'') FROM authoring.working_draft_command WHERE workspace_id=? AND idempotency_key=?`, string(b.WorkspaceID), b.IdempotencyKey)
	if e != nil {
		return domain.PublicationBinding{}, false, classifyGORM(ctx, e, "AUTHORING_RECEIPT_QUERY_FAILED")
	}
	var typ, hash string
	var did, rid, pid, prid foundation.ID
	if e = row.Scan(&typ, &hash, &did, &rid, &pid, &prid); e != nil {
		if gormNoRows(e) {
			return domain.PublicationBinding{}, false, nil
		}
		return domain.PublicationBinding{}, false, classifyGORM(ctx, e, "AUTHORING_RECEIPT_QUERY_FAILED")
	}
	if typ != authoringapp.CommandPublish || hash != b.RequestHash || did != b.DocumentID || rid != b.RevisionID {
		return domain.PublicationBinding{}, false, idempotencyConflict(errors.New("publication receipt is bound to another request"))
	}
	p, found, e := gormLoadPublicationProposal(ctx, tx, b.WorkspaceID, pid, prid)
	if e != nil {
		return domain.PublicationBinding{}, false, e
	}
	if !found {
		return domain.PublicationBinding{}, false, inconsistent("publication receipt has no immutable binding")
	}
	return p, true, nil
}
func gormValidateProposalSnapshot(ctx context.Context, tx *gorm.DB, r authoringapp.PublicationReservation, p, pr foundation.ID, content string) error {
	row, e := gormRow(tx.WithContext(ctx), `SELECT proposal.workspace_id::text,proposal.proposal_type,proposal.idempotency_key,revision.proposal_id::text,revision.target_path,revision.target_mode,revision.base_hash,revision.content FROM change_control.proposal proposal JOIN change_control.proposal_revision revision ON revision.proposal_id=proposal.id WHERE proposal.id=? AND revision.id=? FOR SHARE OF proposal,revision`, string(p), string(pr))
	if e != nil {
		return classifyGORM(ctx, e, "AUTHORING_PROPOSAL_QUERY_FAILED")
	}
	var w, pType, key string
	var bound foundation.ID
	var path, mode, base, body string
	if e = row.Scan(&w, &pType, &key, &bound, &path, &mode, &base, &body); e != nil {
		if gormNoRows(e) {
			return notFound(e)
		}
		return classifyGORM(ctx, e, "AUTHORING_PROPOSAL_QUERY_FAILED")
	}
	if foundation.ID(w) != r.WorkspaceID || pType != "file_patch" || key != r.ProposalIdempotencyKey || bound != p || path != r.TargetPath || changecontroldomain.NormalizeTargetMode(changecontroldomain.TargetMode(mode)) != r.TargetMode || base != r.BaseVersion || body != content || domain.ComputeContentHash(body) != r.ContentHash {
		return inconsistent("proposal snapshot does not match frozen revision")
	}
	return nil
}

func gormReconcilePublications(repository *GORMRepository, ctx context.Context, q authoringapp.ReconcileQuery) (count int, err error) {
	if err := repository.ready(); err != nil {
		return 0, err
	}
	if !validID(q.WorkspaceID) || (q.DocumentID != "" && !validID(q.DocumentID)) || (q.ProposalID == "") != (q.ProposalRevisionID == "") || (q.ProposalID != "" && (!validID(q.ProposalID) || !validID(q.ProposalRevisionID))) || q.Limit < 1 || q.Limit > 100 || q.Now.IsZero() {
		return 0, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("publication reconcile query is invalid"))
	}
	if err := validateGORMContext(ctx); err != nil {
		return 0, err
	}
	conditions := []string{"workspace_id=?", "status IN ('PENDING','RECOVERY_REQUIRED')"}
	args := []any{string(q.WorkspaceID)}
	if q.DocumentID != "" {
		conditions = append(conditions, "document_id=?")
		args = append(args, string(q.DocumentID))
	}
	if q.ProposalID != "" {
		conditions = append(conditions, "proposal_id=?", "proposal_revision_id=?")
		args = append(args, string(q.ProposalID), string(q.ProposalRevisionID))
	}
	args = append(args, q.Limit)
	rows, e := gormRows(repository.database.WithContext(ctx), `SELECT id::text FROM authoring.document_publication_binding WHERE `+strings.Join(conditions, " AND ")+` ORDER BY created_at,id LIMIT ?`, args...)
	if e != nil {
		return 0, classifyGORM(ctx, e, "AUTHORING_PUBLICATION_RECONCILE_QUERY_FAILED")
	}
	ids := make([]foundation.ID, 0, q.Limit)
	for rows.Next() {
		var id foundation.ID
		if e := rows.Scan(&id); e != nil {
			rows.Close()
			return 0, classifyGORM(ctx, e, "AUTHORING_PUBLICATION_RECONCILE_QUERY_FAILED")
		}
		ids = append(ids, id)
	}
	if e := rows.Err(); e != nil {
		rows.Close()
		return 0, classifyGORM(ctx, e, "AUTHORING_PUBLICATION_RECONCILE_QUERY_FAILED")
	}
	rows.Close()
	for _, id := range ids {
		changed, e := gormReconcileOne(repository, ctx, q.WorkspaceID, id, q.Now.UTC().Truncate(time.Microsecond))
		if e != nil {
			return count, e
		}
		if changed {
			count++
		}
	}
	return count, nil
}

func gormReconcileOne(repository *GORMRepository, ctx context.Context, w, id foundation.ID, now time.Time) (changed bool, err error) {
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		snapshot, found, e := gormLoadPublicationID(ctx, tx, w, id, false)
		if e != nil || !found {
			return e
		}
		document, e := gormLoadDocument(ctx, tx, snapshot.WorkspaceID, snapshot.DocumentID, true)
		if e != nil {
			return e
		}
		revision, e := gormLoadRevision(ctx, tx, snapshot.WorkspaceID, snapshot.DocumentID, snapshot.ArticleRevisionID, true)
		if e != nil {
			return e
		}
		binding, found, e := gormLoadPublicationID(ctx, tx, w, id, true)
		if e != nil || !found {
			return e
		}
		if binding.DocumentID != snapshot.DocumentID || binding.ArticleRevisionID != snapshot.ArticleRevisionID {
			return inconsistent("publication binding identity changed while locking")
		}
		if binding.Status == domain.PublicationPublished {
			return nil
		}
		reservation, found, e := gormLoadReservation(ctx, tx, binding.WorkspaceID, binding.ReservationID, false)
		if e != nil {
			return e
		}
		if !found || !reservationMatchesBinding(reservation, binding) || reservation.Status != authoringapp.PublicationReservationClosed {
			return gormMarkPublication(ctx, tx, binding, domain.PublicationRecoveryRequired, publicationStateMismatch, "", nil, now, &changed)
		}
		if e := gormValidateProposalSnapshot(ctx, tx, reservation, binding.ProposalID, binding.ProposalRevisionID, revision.Content); e != nil {
			var classified *foundation.Error
			if !errors.As(e, &classified) || (classified.Kind != foundation.ErrorConsistencyViolation && classified.Kind != foundation.ErrorNotFound) {
				return e
			}
			return gormMarkPublication(ctx, tx, binding, domain.PublicationRecoveryRequired, publicationStateMismatch, "", nil, now, &changed)
		}
		row, e := gormRow(tx.WithContext(ctx), `SELECT status FROM change_control.proposal WHERE id=? AND workspace_id=?`, string(binding.ProposalID), string(binding.WorkspaceID))
		if e != nil {
			return classifyGORM(ctx, e, "AUTHORING_PROPOSAL_QUERY_FAILED")
		}
		var status string
		if e = row.Scan(&status); e != nil {
			return classifyGORM(ctx, e, "AUTHORING_PROPOSAL_QUERY_FAILED")
		}
		switch status {
		case "rejected":
			return gormMarkPublication(ctx, tx, binding, domain.PublicationClosed, publicationRejected, "", nil, now, &changed)
		case "needs_revision":
			return gormMarkPublication(ctx, tx, binding, domain.PublicationClosed, publicationNeedsRevision, "", nil, now, &changed)
		case "cancelled":
			return gormMarkPublication(ctx, tx, binding, domain.PublicationClosed, publicationCancelled, "", nil, now, &changed)
		}
		row, e = gormRow(tx.WithContext(ctx), `SELECT workspace_id::text,proposal_id::text,revision_id::text,git_commit,target_path,target_mode,result_hash,created_at FROM change_control.proposal_commit WHERE proposal_id=? AND revision_id=?`, string(binding.ProposalID), string(binding.ProposalRevisionID))
		if e != nil {
			return classifyGORM(ctx, e, "AUTHORING_PUBLICATION_COMMIT_QUERY_FAILED")
		}
		var cw, cp, cr foundation.ID
		var git, path, mode, hash string
		var committed time.Time
		if e = row.Scan(&cw, &cp, &cr, &git, &path, &mode, &hash, &committed); e != nil {
			if gormNoRows(e) {
				return nil
			}
			return classifyGORM(ctx, e, "AUTHORING_PUBLICATION_COMMIT_QUERY_FAILED")
		}
		if cw != binding.WorkspaceID || cp != binding.ProposalID || cr != binding.ProposalRevisionID || path != binding.TargetPath || changecontroldomain.TargetMode(mode) != binding.TargetMode || hash != binding.ContentHash || hash != revision.ContentHash || !validGitCommit(git) {
			return gormMarkPublication(ctx, tx, binding, domain.PublicationRecoveryRequired, publicationCommitMismatch, "", nil, now, &changed)
		}
		already := document.Lifecycle == domain.DocumentPublished && document.CurrentPublishedRevisionID == revision.ID && revision.Status == domain.RevisionPublished && revision.GitCommit == git
		if !already {
			valid := document.CanonicalPath == binding.TargetPath && (revision.Status == domain.RevisionDraft || revision.Status == domain.RevisionReview || revision.Status == domain.RevisionApproved)
			if binding.TargetMode == domain.ProposalTargetCreateOnly {
				valid = valid && document.Lifecycle == domain.DocumentDraft && document.CurrentPublishedRevisionID == ""
			} else {
				valid = valid && binding.TargetMode == domain.ProposalTargetReplace && document.Lifecycle == domain.DocumentPublished && document.CurrentPublishedRevisionID != ""
			}
			if !valid {
				return gormMarkPublication(ctx, tx, binding, domain.PublicationRecoveryRequired, publicationStateMismatch, "", nil, now, &changed)
			}
			if document.CurrentPublishedRevisionID != "" && document.CurrentPublishedRevisionID != revision.ID {
				previous, e := gormLoadRevision(ctx, tx, w, document.ID, document.CurrentPublishedRevisionID, true)
				if e != nil {
					return e
				}
				if previous.Status != domain.RevisionPublished || previous.GitCommit == "" || (binding.TargetMode == domain.ProposalTargetReplace && previous.ContentHash != reservation.BaseVersion) {
					return gormMarkPublication(ctx, tx, binding, domain.PublicationRecoveryRequired, publicationStateMismatch, "", nil, now, &changed)
				}
				res := tx.Exec(`UPDATE core.article_revision SET status='SUPERSEDED' WHERE id=? AND workspace_id=? AND document_id=? AND status='PUBLISHED'`, string(previous.ID), string(w), string(document.ID))
				if res.Error != nil {
					return classifyGORM(ctx, res.Error, "AUTHORING_REVISION_SUPERSEDE_FAILED")
				}
				if res.RowsAffected != 1 {
					return inconsistent("current published revision supersede lost its lock")
				}
			}
			res := tx.Exec(`UPDATE core.article_revision SET status='PUBLISHED',git_commit=? WHERE id=? AND workspace_id=? AND document_id=? AND status IN ('DRAFT','REVIEW','APPROVED')`, git, string(revision.ID), string(w), string(document.ID))
			if res.Error != nil {
				return classifyGORM(ctx, res.Error, "AUTHORING_REVISION_PUBLISH_FAILED")
			}
			if res.RowsAffected != 1 {
				return inconsistent("article revision publish did not update one row")
			}
			res = tx.Exec(`UPDATE core.document SET lifecycle_status='PUBLISHED',current_published_revision_id=?,version=version+1,updated_at=GREATEST(updated_at,?) WHERE id=? AND workspace_id=? AND version=? AND canonical_path=?`, string(revision.ID), now, string(document.ID), string(w), document.Version, binding.TargetPath)
			if res.Error != nil {
				return classifyGORM(ctx, res.Error, "AUTHORING_DOCUMENT_PUBLISH_FAILED")
			}
			if res.RowsAffected != 1 {
				return inconsistent("document publish compare-and-swap did not update one row")
			}
		}
		published := committed.UTC()
		if published.Before(binding.CreatedAt) {
			published = binding.CreatedAt
		}
		return gormMarkPublication(ctx, tx, binding, domain.PublicationPublished, "", git, &published, now, &changed)
	}, "AUTHORING_PUBLICATION_TRANSACTION_FAILED")
	if err != nil {
		return false, err
	}
	return changed, err
}

func gormMarkPublication(ctx context.Context, tx *gorm.DB, b domain.PublicationBinding, status domain.PublicationStatus, code, git string, published *time.Time, now time.Time, changed *bool) error {
	if b.Status == status {
		return nil
	}
	allowed := (status == domain.PublicationRecoveryRequired && b.Status == domain.PublicationPending) || (status == domain.PublicationClosed && (b.Status == domain.PublicationPending || b.Status == domain.PublicationRecoveryRequired)) || (status == domain.PublicationPublished && (b.Status == domain.PublicationPending || b.Status == domain.PublicationRecoveryRequired))
	if !allowed {
		return inconsistent("publication terminal transition is invalid")
	}
	res := tx.Exec(`UPDATE authoring.document_publication_binding SET status=?,git_commit=NULLIF(?, ''),error_code=?,version=version+1,updated_at=GREATEST(updated_at,?),published_at=? WHERE id=? AND workspace_id=? AND version=? AND status=?`, string(status), git, code, now, published, string(b.ID), string(b.WorkspaceID), b.Version, string(b.Status))
	if res.Error != nil {
		return classifyGORM(ctx, res.Error, gormPublicationTransitionFallback(status))
	}
	if res.RowsAffected != 1 {
		return inconsistent("publication transition compare-and-swap did not update one row")
	}
	*changed = true
	return nil
}

func gormPublicationTransitionFallback(status domain.PublicationStatus) string {
	switch status {
	case domain.PublicationRecoveryRequired:
		return "AUTHORING_PUBLICATION_RECOVERY_MARK_FAILED"
	case domain.PublicationClosed:
		return "AUTHORING_PUBLICATION_CLOSE_FAILED"
	default:
		return "AUTHORING_PUBLICATION_FINALIZE_FAILED"
	}
}
