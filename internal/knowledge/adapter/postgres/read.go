package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func validateClaimStatuses(statuses []domain.ClaimStatus) error {
	for _, status := range statuses {
		switch status {
		case domain.ClaimStatusSuggested, domain.ClaimStatusConfirmed, domain.ClaimStatusDisputed,
			domain.ClaimStatusSuperseded, domain.ClaimStatusDeprecated, domain.ClaimStatusInvalid:
		default:
			return invalidQuery(errors.New("claim status filter is invalid"))
		}
	}
	return nil
}

func validateRelationStatuses(statuses []domain.RelationStatus) error {
	for _, status := range statuses {
		switch status {
		case domain.RelationStatusSuggested, domain.RelationStatusConfirmed, domain.RelationStatusRejected,
			domain.RelationStatusStale, domain.RelationStatusDeprecated:
		default:
			return invalidQuery(errors.New("relation status filter is invalid"))
		}
	}
	return nil
}

func validateConflictStatuses(statuses []domain.ConflictStatus) error {
	for _, status := range statuses {
		switch status {
		case domain.ConflictStatusOpen, domain.ConflictStatusInvestigating, domain.ConflictStatusResolutionProposed,
			domain.ConflictStatusResolved, domain.ConflictStatusAcceptedDivergence, domain.ConflictStatusDeferred:
		default:
			return invalidQuery(errors.New("conflict status filter is invalid"))
		}
	}
	return nil
}

func invalidQuery(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeRequestInvalid, false, err)
}
