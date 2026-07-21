// Package candidateconfirm defines the cross-module seam for confirming a
// semantic link candidate. The seam creates a typed Relation Proposal, but it
// deliberately does not apply the Proposal to Knowledge.
package candidateconfirm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const maxIdempotencyKeyBytes = 128

// Command is the complete semantic-link confirmation command. A nil
// RelationType confirms the candidate's suggested type; a non-nil value is a
// typed confirmation and must be compatible with the candidate endpoints.
type Command struct {
	WorkspaceID     foundation.ID
	CandidateID     foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
	Action          graphdomain.SemanticLinkCandidateDecisionAction
	RelationType    *knowledge.RelationType
	Risk            string
	RollbackPlan    string
}

// Result is the durable outcome of one confirmation transaction.
type Result struct {
	Candidate  graphdomain.SemanticLinkCandidate
	Proposal   changecontroldomain.Proposal
	DecisionID foundation.ID
	Replayed   bool
}

// Port is the single write seam used by Candidate Application code. An
// implementation must commit Proposal, Revision, Decision and Candidate state
// together or return an error with no durable partial result.
type Port interface {
	Confirm(context.Context, Command) (Result, error)
}

// Canonicalize validates and normalizes user-controlled command fields before
// they participate in an idempotency hash.
func Canonicalize(command Command) (Command, error) {
	workspaceID, err := foundation.ParseID(string(command.WorkspaceID))
	if err != nil {
		return Command{}, invalid("workspace id is invalid")
	}
	candidateID, err := foundation.ParseID(string(command.CandidateID))
	if err != nil {
		return Command{}, invalid("candidate id is invalid")
	}
	command.WorkspaceID = workspaceID
	command.CandidateID = candidateID
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if command.ExpectedVersion < 1 || command.IdempotencyKey == "" || len(command.IdempotencyKey) > maxIdempotencyKeyBytes || strings.ContainsAny(command.IdempotencyKey, "\r\n") {
		return Command{}, invalid("confirmation identity or expected version is invalid")
	}
	if command.Action != graphdomain.SemanticLinkCandidateDecisionConfirm && command.Action != graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType {
		return Command{}, invalid("confirmation action is unsupported")
	}
	if command.Action == graphdomain.SemanticLinkCandidateDecisionConfirm && command.RelationType != nil {
		return Command{}, invalid("confirm must not include a relation type")
	}
	if command.Action == graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType && command.RelationType == nil {
		return Command{}, invalid("typed confirm requires a relation type")
	}
	if command.RelationType != nil {
		value := knowledge.RelationType(strings.ToUpper(strings.TrimSpace(string(*command.RelationType))))
		if !isRelationType(value) {
			return Command{}, invalid("confirmation relation type is unsupported")
		}
		command.RelationType = &value
	}
	risk, err := knowledge.NormalizeReason(command.Risk, true)
	if err != nil {
		return Command{}, invalid("confirmation risk is invalid")
	}
	rollbackPlan, err := knowledge.NormalizeReason(command.RollbackPlan, true)
	if err != nil {
		return Command{}, invalid("confirmation rollback plan is invalid")
	}
	command.Risk = risk
	command.RollbackPlan = rollbackPlan
	return command, nil
}

// RequestHash returns a canonical hash for the complete command. Risk and
// rollback are included so an idempotency key cannot silently change the
// Proposal's operational contract.
func RequestHash(command Command) (string, error) {
	canonical, err := Canonicalize(command)
	if err != nil {
		return "", err
	}
	payload := struct {
		SchemaVersion   string                                          `json:"schema_version"`
		WorkspaceID     foundation.ID                                   `json:"workspace_id"`
		CandidateID     foundation.ID                                   `json:"candidate_id"`
		ExpectedVersion int64                                           `json:"expected_version"`
		IdempotencyKey  string                                          `json:"idempotency_key"`
		Action          graphdomain.SemanticLinkCandidateDecisionAction `json:"action"`
		RelationType    *knowledge.RelationType                         `json:"relation_type,omitempty"`
		Risk            string                                          `json:"risk"`
		RollbackPlan    string                                          `json:"rollback_plan"`
	}{
		SchemaVersion:   "semantic-link-candidate-confirm/v1",
		WorkspaceID:     canonical.WorkspaceID,
		CandidateID:     canonical.CandidateID,
		ExpectedVersion: canonical.ExpectedVersion,
		IdempotencyKey:  canonical.IdempotencyKey,
		Action:          canonical.Action,
		RelationType:    canonical.RelationType,
		Risk:            canonical.Risk,
		RollbackPlan:    canonical.RollbackPlan,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func isRelationType(value knowledge.RelationType) bool {
	switch value {
	case knowledge.RelationCites, knowledge.RelationDerivedFrom, knowledge.RelationBelongsTo,
		knowledge.RelationSupports, knowledge.RelationComplements, knowledge.RelationDuplicates,
		knowledge.RelationConflictsWith, knowledge.RelationPrerequisiteOf, knowledge.RelationVersionOf,
		knowledge.RelationImpacts:
		return true
	default:
		return false
	}
}

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_CONFIRM_INVALID", false, errors.New(message))
}
