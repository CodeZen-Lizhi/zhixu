package domain

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// ErrorCodeGeneratedPublicationInvalid rejects an incomplete trusted retirement command.
	ErrorCodeGeneratedPublicationInvalid = "PROPOSAL_GENERATED_PUBLICATION_INVALID"
	// ErrorCodeGeneratedPublicationConflict rejects a changed immutable or current Revision binding.
	ErrorCodeGeneratedPublicationConflict = "PROPOSAL_GENERATED_PUBLICATION_CONFLICT"
	// ErrorCodeGeneratedPublicationBusy preserves every approval or external execution already started.
	ErrorCodeGeneratedPublicationBusy = "PROPOSAL_GENERATED_PUBLICATION_BUSY"
)

// GeneratedPublicationRetirement is populated only by the Authoring owner after
// proving its immutable AGENT origin and publication binding in the same scope.
// This command is not exposed by the public Proposal revision API.
type GeneratedPublicationRetirement struct {
	WorkspaceID             foundation.ID
	ProposalID              foundation.ID
	ProposalRevisionID      foundation.ID
	ExpectedProposalVersion int64
	ProposalIdempotencyKey  string
	TargetPath              string
	TargetMode              TargetMode
	BaseVersion             string
	ContentHash             string
	RetiredAt               time.Time
}

// Validate checks the complete Proposal-owned identity without accepting an Approval decision.
func (request GeneratedPublicationRetirement) Validate() error {
	for _, id := range []foundation.ID{request.WorkspaceID, request.ProposalID, request.ProposalRevisionID} {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeGeneratedPublicationInvalid, false, errors.New("generated publication identity is invalid"))
		}
	}
	path, err := ValidateTargetPath(request.TargetPath)
	if err != nil || path != request.TargetPath || request.ExpectedProposalVersion < 1 || request.ExpectedProposalVersion == math.MaxInt64 ||
		request.ProposalIdempotencyKey == "" || strings.TrimSpace(request.ProposalIdempotencyKey) != request.ProposalIdempotencyKey ||
		len(request.ProposalIdempotencyKey) > 128 || !utf8.ValidString(request.ProposalIdempotencyKey) || strings.ContainsAny(request.ProposalIdempotencyKey, "\r\n\x00") ||
		ValidateTargetBaseVersion(request.WorkspaceID, request.TargetPath, request.TargetMode, request.BaseVersion) != nil ||
		!ValidHash(request.ContentHash) || request.ContentHash != strings.ToLower(request.ContentHash) || request.RetiredAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeGeneratedPublicationInvalid, false, errors.New("generated publication retirement binding is invalid"))
	}
	return nil
}

// GeneratedPublicationRetirementRepository accepts only trusted generated retirement participants.
// The caller owns the transaction, records its receipt, and closes the Authoring publication binding.
type GeneratedPublicationRetirementRepository interface {
	RetireGeneratedPublicationScoped(context.Context, foundation.TransactionScope, GeneratedPublicationRetirement) error
}
