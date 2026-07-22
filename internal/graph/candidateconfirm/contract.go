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

// ProposalRiskLevel 是 Semantic Candidate 创建 Relation Proposal 时唯一允许的风险等级。
const ProposalRiskLevel = changecontroldomain.ProposalRiskLevelHigh

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
	RiskLevel       changecontroldomain.ProposalRiskLevel
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
	return canonicalize(command, true)
}

// CanonicalizeLegacy 保留 v1 回放所需的命令规范化，并允许旧请求缺少风险等级字段。
// 该入口只应用于已存在的 v1 receipt；持久 Proposal 等级仍须单独校验为 HIGH。
func CanonicalizeLegacy(command Command) (Command, error) {
	return canonicalizeLegacy(command)
}

// canonicalizeLegacy 保留 v1 receipt 所需的输入规范化，但不要求新 v2 的固定 HIGH 等级。
// 它不会从 Revision.Risk 派生等级，也不能用于创建新 Proposal。
func canonicalizeLegacy(command Command) (Command, error) {
	return canonicalize(command, false)
}

func canonicalize(command Command, requireHighRisk bool) (Command, error) {
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
	if strings.TrimSpace(string(command.RiskLevel)) != "" {
		riskLevel, err := changecontroldomain.ParseProposalRiskLevel(command.RiskLevel)
		if err != nil {
			return Command{}, invalid("confirmation risk level is invalid")
		}
		command.RiskLevel = riskLevel
	} else if requireHighRisk {
		return Command{}, invalid("confirmation risk level must be HIGH")
	}
	if requireHighRisk && command.RiskLevel != ProposalRiskLevel {
		return Command{}, invalid("confirmation risk level must be HIGH")
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

// RequestHash returns the v2 canonical hash for the complete command. The
// fixed HIGH risk level participates in the binding so a receipt cannot be
// replayed against a differently classified Proposal.
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
		RiskLevel       changecontroldomain.ProposalRiskLevel           `json:"risk_level"`
		Risk            string                                          `json:"risk"`
		RollbackPlan    string                                          `json:"rollback_plan"`
	}{
		SchemaVersion:   "semantic-link-candidate-confirm/v2",
		WorkspaceID:     canonical.WorkspaceID,
		CandidateID:     canonical.CandidateID,
		ExpectedVersion: canonical.ExpectedVersion,
		IdempotencyKey:  canonical.IdempotencyKey,
		Action:          canonical.Action,
		RelationType:    canonical.RelationType,
		RiskLevel:       canonical.RiskLevel,
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

// LegacyRequestHash reproduces the immutable v1 receipt hash. It is only for
// exact replay of rows created before risk_level became part of the public Proposal contract;
// new receipts must always use RequestHash.
func LegacyRequestHash(command Command) (string, error) {
	canonical, err := canonicalizeLegacy(command)
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
