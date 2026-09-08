package postgres

import (
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func revisionCommandError(err error) error {
	switch {
	case errors.Is(err, domain.ErrProposalRevisionInputTooLarge):
		return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INPUT_TOO_LARGE", false, err)
	case errors.Is(err, domain.ErrProposalRevisionStale):
		return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_REVISION_STALE", false, err)
	default:
		return foundation.NewError(foundation.ErrorInvalidInput, "PROPOSAL_REVISION_INVALID", false, err)
	}
}
