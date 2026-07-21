package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// QueryKindSemanticLinkCandidates 是候选列表 cursor 的查询类型绑定。
	QueryKindSemanticLinkCandidates = "semantic_link_candidates"
	maxCandidateIdempotencyKeyLen   = 128
	// MaxSemanticLinkCandidateWindow 是 Adapter 单次返回的稳定窗口上限。
	MaxSemanticLinkCandidateWindow = MaxResultWindowItems
)

// SemanticLinkCandidateResultWindow 是 Adapter 返回给应用层的完整有界窗口。
// Adapter 不生成 opaque cursor，分页由 Application 统一签发和校验。
type SemanticLinkCandidateResultWindow struct {
	Items     []graphdomain.SemanticLinkCandidate
	Truncated bool
	Reason    string
}

// SemanticLinkCandidateWindowPort 是候选查询 Adapter 的有界窗口端口。
type SemanticLinkCandidateWindowPort interface {
	SemanticLinkCandidateWindow(context.Context, graphdomain.SemanticLinkCandidateQuery) (SemanticLinkCandidateResultWindow, error)
}

type canonicalSemanticLinkCandidateQuery struct {
	WorkspaceID     foundation.ID                                     `json:"workspace_id"`
	NodeRef         *knowledge.NodeRef                                `json:"node_ref,omitempty"`
	Statuses        []graphdomain.SemanticLinkCandidateStatus         `json:"statuses,omitempty"`
	RelationTypes   []knowledge.RelationType                          `json:"relation_types,omitempty"`
	ReopenedReasons []graphdomain.SemanticLinkCandidateReopenedReason `json:"reopened_reasons,omitempty"`
	MinConfidence   *float64                                          `json:"min_confidence,omitempty"`
}

// SemanticLinkCandidateDecisionCommand 是候选决策的应用层命令信封。
type SemanticLinkCandidateDecisionCommand struct {
	WorkspaceID     foundation.ID
	CandidateID     foundation.ID
	ExpectedVersion int64
	IdempotencyKey  string
	Decision        graphdomain.SemanticLinkCandidateDecision
}

// SemanticLinkCandidateDecisionResult 是候选决策后的应用层结果。
type SemanticLinkCandidateDecisionResult struct {
	Candidate  graphdomain.SemanticLinkCandidate
	ProposalID *foundation.ID
	DecisionID foundation.ID
	DecidedAt  time.Time
	Replayed   bool
}

// CanonicalSemanticLinkCandidateQueryHash 对候选查询过滤器做稳定哈希。
func CanonicalSemanticLinkCandidateQueryHash(request graphdomain.SemanticLinkCandidateQuery) (string, error) {
	if err := graphdomain.ValidateSemanticLinkCandidateQuery(request); err != nil {
		return "", err
	}
	canonical := canonicalSemanticLinkCandidateQuery{
		WorkspaceID:     request.WorkspaceID,
		NodeRef:         request.NodeRef,
		Statuses:        sortedStrings(request.Statuses),
		RelationTypes:   sortedStrings(request.RelationTypes),
		ReopenedReasons: sortedStrings(request.ReopenedReasons),
		MinConfidence:   request.MinConfidence,
	}
	return hashCanonicalCursorValue(canonical)
}

// CanonicalizeSemanticLinkCandidateDecisionCommand 规范化幂等键和可选时间字段。
func CanonicalizeSemanticLinkCandidateDecisionCommand(command SemanticLinkCandidateDecisionCommand) (SemanticLinkCandidateDecisionCommand, error) {
	if !validCandidateDecisionCommand(command) {
		return SemanticLinkCandidateDecisionCommand{}, invalidCandidateDecisionCommand("candidate decision command is invalid")
	}
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	if !validCandidateDecisionAction(command.Decision.Action) {
		return SemanticLinkCandidateDecisionCommand{}, invalidCandidateDecisionCommand("candidate decision action is unsupported")
	}
	if command.Decision.RelationType != nil && !validCandidateRelationType(*command.Decision.RelationType) {
		return SemanticLinkCandidateDecisionCommand{}, invalidCandidateDecisionCommand("candidate decision relation type is invalid")
	}
	if command.Decision.Reason != "" {
		reason, err := knowledge.NormalizeReason(command.Decision.Reason, true)
		if err != nil {
			return SemanticLinkCandidateDecisionCommand{}, err
		}
		command.Decision.Reason = reason
	}
	if command.Decision.ResumeAfter != nil {
		resume := command.Decision.ResumeAfter.UTC()
		command.Decision.ResumeAfter = &resume
	}
	return command, nil
}

// ComputeSemanticLinkCandidateDecisionRequestHash 计算绑定候选决策语义字段的稳定请求哈希。
func ComputeSemanticLinkCandidateDecisionRequestHash(command SemanticLinkCandidateDecisionCommand) (string, error) {
	canonical, err := CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		return "", err
	}
	payload := struct {
		WorkspaceID     foundation.ID                             `json:"workspace_id"`
		CandidateID     foundation.ID                             `json:"candidate_id"`
		ExpectedVersion int64                                     `json:"expected_version"`
		Decision        graphdomain.SemanticLinkCandidateDecision `json:"decision"`
	}{
		WorkspaceID:     canonical.WorkspaceID,
		CandidateID:     canonical.CandidateID,
		ExpectedVersion: canonical.ExpectedVersion,
		Decision:        canonical.Decision,
	}
	return hashCanonicalCursorValue(payload)
}

// ValidateSemanticLinkCandidateDecisionResult 校验应用结果是否与命令绑定。
func ValidateSemanticLinkCandidateDecisionResult(command SemanticLinkCandidateDecisionCommand, result SemanticLinkCandidateDecisionResult) error {
	canonical, err := CanonicalizeSemanticLinkCandidateDecisionCommand(command)
	if err != nil {
		return err
	}
	if err := graphdomain.ValidateSemanticLinkCandidate(result.Candidate); err != nil {
		return err
	}
	if result.Candidate.WorkspaceID != canonical.WorkspaceID || result.Candidate.ID != canonical.CandidateID || result.Candidate.Version != canonical.ExpectedVersion+1 {
		return candidateResultInconsistent("candidate decision result is not bound to the command")
	}
	switch canonical.Decision.Action {
	case graphdomain.SemanticLinkCandidateDecisionConfirm, graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType:
		if result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusProposalCreated || result.ProposalID == nil || result.Candidate.ProposalID == nil || *result.ProposalID != *result.Candidate.ProposalID {
			return candidateResultInconsistent("confirming a candidate must bind a proposal")
		}
	case graphdomain.SemanticLinkCandidateDecisionIgnore:
		if result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusIgnored || result.ProposalID != nil || result.Candidate.ProposalID != nil {
			return candidateResultInconsistent("ignored candidate result is inconsistent")
		}
	case graphdomain.SemanticLinkCandidateDecisionFalsePositive:
		if result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusFalsePositive || result.ProposalID != nil || result.Candidate.ProposalID != nil {
			return candidateResultInconsistent("false-positive candidate result is inconsistent")
		}
	case graphdomain.SemanticLinkCandidateDecisionDefer:
		if result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusDeferred || result.ProposalID != nil || result.Candidate.ProposalID != nil {
			return candidateResultInconsistent("deferred candidate result is inconsistent")
		}
	case graphdomain.SemanticLinkCandidateDecisionResume:
		if result.Candidate.Status != graphdomain.SemanticLinkCandidateStatusActive || result.ProposalID != nil || result.Candidate.ProposalID != nil {
			return candidateResultInconsistent("resumed candidate result is inconsistent")
		}
	}
	return nil
}

func validCandidateDecisionCommand(command SemanticLinkCandidateDecisionCommand) bool {
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	if !validID(command.WorkspaceID) || !validID(command.CandidateID) || command.ExpectedVersion < 1 || idempotencyKey == "" || len(idempotencyKey) > maxCandidateIdempotencyKeyLen {
		return false
	}
	return true
}

func validCandidateDecisionAction(action graphdomain.SemanticLinkCandidateDecisionAction) bool {
	switch action {
	case graphdomain.SemanticLinkCandidateDecisionConfirm,
		graphdomain.SemanticLinkCandidateDecisionConfirmWithRelationType,
		graphdomain.SemanticLinkCandidateDecisionIgnore,
		graphdomain.SemanticLinkCandidateDecisionFalsePositive,
		graphdomain.SemanticLinkCandidateDecisionDefer,
		graphdomain.SemanticLinkCandidateDecisionResume:
		return true
	default:
		return false
	}
}

func validCandidateRelationType(value knowledge.RelationType) bool {
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

func invalidCandidateDecisionCommand(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, graphdomain.ErrorCodeSemanticLinkCandidateRequestInvalid, false, errors.New(message))
}

func candidateResultInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeSemanticLinkCandidateResultInvalid, false, errors.New(message))
}
