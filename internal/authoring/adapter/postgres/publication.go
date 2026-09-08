package postgres

import (
	"errors"
	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"strings"
	"unicode/utf8"
)

const (
	publicationCommitMismatch = "AUTHORING_PUBLICATION_COMMIT_MISMATCH"
	publicationStateMismatch  = "AUTHORING_PUBLICATION_STATE_MISMATCH"
	publicationRejected       = "AUTHORING_PUBLICATION_PROPOSAL_REJECTED"
	publicationNeedsRevision  = "AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION"
	publicationCancelled      = "AUTHORING_PUBLICATION_PROPOSAL_CANCELLED"
)

const reservationColumns = `id::text,workspace_id::text,document_id::text,article_revision_id::text,
		idempotency_key,request_hash,proposal_idempotency_key,target_path,content_hash,target_mode,
		base_version,absence_token,status,error_code,created_at,updated_at,closed_at,abandoned_at`

const publicationColumns = `id::text,reservation_id::text,workspace_id::text,document_id::text,
	article_revision_id::text,proposal_id::text,proposal_revision_id::text,target_path,content_hash,
	target_mode,absence_token,status,COALESCE(git_commit,''),error_code,version,created_at,updated_at,published_at`

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
