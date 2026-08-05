package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func classify(err error, fallbackCode string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(err)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			switch postgresError.ConstraintName {
			case "uq_core_document_workspace_path":
				return foundation.NewError(foundation.ErrorVersionConflict, authoringapp.ErrorCodePathConflict, false, err)
			case "working_draft_command_pkey":
				return idempotencyConflict(err)
			case "uq_authoring_reservation_workspace_key":
				return idempotencyConflict(err)
			case "uq_authoring_reservation_pending_document", "uq_authoring_publication_nonterminal_document",
				"uq_authoring_publication_revision", "uq_authoring_publication_proposal_revision",
				"document_publication_binding_reservation_id_key":
				return publicationConflict("publication already has an active immutable binding")
			case "uq_core_article_revision_no", "uq_authoring_working_draft_document":
				return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, err)
			}
		case "23503":
			return notFound(err)
		case "23514", "22001", "22021":
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, err)
		case "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, authoringapp.ErrorCodeResultInvalid, false, err)
		case "40001", "40P01", "55P03", "57P01", "57P02", "57P03", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, fallbackCode, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, fallbackCode, true, err)
}

func notFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, authoringapp.ErrorCodeNotFound, false, cause)
}

func idempotencyConflict(cause error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, authoringapp.ErrorCodeIdempotencyConflict, false, cause)
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, authoringapp.ErrorCodeResultInvalid, false, errors.New(message))
}

func versionConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict, false, errors.New(message))
}

func publicationConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, authoringapp.ErrorCodePublicationConflict, false, errors.New(message))
}
