package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

const candidateFingerprintSchemaV1 = "semantic-link-candidate/v1"

type relationProposalRequestHashVersion string

const (
	relationProposalRequestHashV1 relationProposalRequestHashVersion = "v1"
	relationProposalRequestHashV2 relationProposalRequestHashVersion = "v2"
)

type relationApprovalBinding struct {
	workspaceID     foundation.ID
	status          changecontroldomain.ProposalStatus
	proposalVersion int64
	revisionHash    string
	existing        *changecontroldomain.Approval
}

// relationApplyLockTarget 是在锁 Proposal 前确定的 Candidate 锁目标。
// Proposal Revision 为不可变事实，先锁 Candidate 可让 Candidate Confirm、旧 Apply
// 与原子 Approval UoW 使用同一锁序，避免 Proposal↔Candidate 反向等待。
type relationApplyLockTarget struct {
	workspaceID foundation.ID
	candidateID foundation.ID
}

func validateRelationApprovalInput(approval changecontroldomain.Approval) error {
	for name, value := range map[string]foundation.ID{
		"proposal_id": approval.ProposalID,
		"revision_id": approval.RevisionID,
		"approval_id": approval.ID,
	} {
		parsed, err := foundation.ParseID(string(value))
		if err != nil || parsed != value {
			return errors.New(name + " is not a canonical UUID")
		}
	}
	if !changecontroldomain.ValidHash(approval.ChangeHash) {
		return errors.New("approval change hash is invalid")
	}
	if approval.Decision != changecontroldomain.DecisionApproved {
		return errors.New("knowledge relation approval must be approved")
	}
	if approval.ApprovedGitHead != nil {
		return errors.New("knowledge relation approval cannot bind a git head")
	}
	if approval.DecidedAt.IsZero() {
		return errors.New("approval decision time is required")
	}
	return nil
}

type approvedRelationProposal struct {
	ID, WorkspaceID foundation.ID
	Type            changecontroldomain.ProposalType
	RiskLevel       changecontroldomain.ProposalRiskLevel
	Status          changecontroldomain.ProposalStatus
	Version         int64
	RequestHash     string
	UpdatedAt       time.Time
	Revision        changecontroldomain.Revision
	Approval        changecontroldomain.Approval
	change          changecontroldomain.KnowledgeChange
}

func validateApprovedRelationProposalBinding(proposal approvedRelationProposal, command knowledgeapplication.ApprovedRelationApplyCommand) (relationProposalRequestHashVersion, error) {
	if proposal.Type != changecontroldomain.ProposalTypeKnowledgeChange ||
		proposal.ID != command.ProposalID || proposal.WorkspaceID != command.WorkspaceID ||
		proposal.Revision.ID != command.RevisionID || proposal.Revision.ProposalID != proposal.ID ||
		proposal.Approval.ID != command.ApprovalID || proposal.Approval.ProposalID != proposal.ID || proposal.Approval.RevisionID != proposal.Revision.ID ||
		proposal.Approval.Decision != changecontroldomain.DecisionApproved || proposal.Approval.ApprovedGitHead != nil {
		return "", errors.New("knowledge relation approval binding is inconsistent")
	}
	riskLevel, err := changecontroldomain.ValidateProposalRiskLevelForType(changecontroldomain.ProposalTypeKnowledgeChange, proposal.RiskLevel)
	if err != nil {
		return "", errors.New("knowledge relation proposal risk level is invalid")
	}
	if err := changecontroldomain.ValidateProposalRevisionForType(changecontroldomain.ProposalTypeKnowledgeChange, proposal.Revision); err != nil {
		return "", err
	}
	requestHashV2, err := changecontroldomain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		proposal.WorkspaceID,
		proposal.change,
		riskLevel,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return "", err
	}
	requestHashV1, err := changecontroldomain.ComputeKnowledgeChangeRequestHash(
		proposal.WorkspaceID,
		proposal.change,
		proposal.Revision.Risk,
		proposal.Revision.RollbackPlan,
	)
	if err != nil {
		return "", err
	}
	var version relationProposalRequestHashVersion
	switch proposal.RequestHash {
	case requestHashV2:
		version = relationProposalRequestHashV2
	case requestHashV1:
		version = relationProposalRequestHashV1
	default:
		return "", errors.New("knowledge relation proposal request hash binding is inconsistent")
	}
	if proposal.Approval.ChangeHash != proposal.Revision.ChangeHash {
		return "", errors.New("knowledge relation approval change hash binding is inconsistent")
	}
	return version, nil
}
func validateRelationApplyBinding(candidate graphdomain.SemanticLinkCandidate, proposal approvedRelationProposal) error {
	if candidate.WorkspaceID != proposal.WorkspaceID || candidate.Status != graphdomain.SemanticLinkCandidateStatusProposalCreated ||
		candidate.ProposalID == nil || *candidate.ProposalID != proposal.ID ||
		proposal.change.TargetRefs[0].ID != candidate.ID || proposal.change.TargetRefs[0].Fingerprint != candidate.Fingerprint {
		return errors.New("candidate and proposal binding changed")
	}
	expected := changecontroldomain.KnowledgeChange{
		TargetRefs: []changecontroldomain.KnowledgeTargetRef{{
			Type: changecontroldomain.KnowledgeTargetRefRelationCandidate, ID: candidate.ID, Fingerprint: candidate.Fingerprint,
		}},
		BaseVersions: []changecontroldomain.KnowledgeBaseVersion{
			{NodeType: candidate.Source.Ref.Type, NodeID: candidate.Source.Ref.ID, Version: candidate.Source.Version},
			{NodeType: candidate.Target.Ref.Type, NodeID: candidate.Target.Ref.ID, Version: candidate.Target.Version},
		},
		ChangeSet:     proposal.change.ChangeSet,
		SchemaVersion: changecontroldomain.KnowledgeChangeSchemaVersion,
	}
	for _, evidence := range candidate.Evidence {
		expected.EvidenceRefs = append(expected.EvidenceRefs, changecontroldomain.KnowledgeEvidenceRef{CandidateEvidenceID: evidence.ID, SemanticHash: evidence.SemanticHash})
	}
	canonical, err := changecontroldomain.ValidateKnowledgeChange(expected)
	if err != nil || !reflect.DeepEqual(canonical, proposal.change) {
		return errors.New("candidate snapshot differs from approved typed revision")
	}
	return nil
}

type relationApplyEndpoint struct {
	Ref           domain.NodeRef
	Version       int64
	Lifecycle     string
	Applicability *domain.Applicability
}

func validateRelationApplyReplayFact(command knowledgeapplication.ApprovedRelationApplyCommand, proposal approvedRelationProposal, receipt *commandReceipt, relation domain.Relation) error {
	if proposal.Status != changecontroldomain.StatusApplied {
		return errors.New("relation apply receipt and proposal state are inconsistent")
	}
	if receipt == nil || relation.ID != receipt.AggregateID || relation.Version != receipt.AggregateVersion || relation.Confirmation == nil ||
		relation.Confirmation.Method != domain.ConfirmationUserApproval || relation.Confirmation.Reference != string(command.ApprovalID) ||
		relation.WorkspaceID != proposal.WorkspaceID || relation.Source != proposal.change.ChangeSet.Source ||
		relation.Target != proposal.change.ChangeSet.Target || relation.Type != proposal.change.ChangeSet.RelationType {
		return errors.New("relation apply receipt points to a different relation fact")
	}
	return nil
}

type relationApplyNeedsRevision struct{ reason string }

func (err *relationApplyNeedsRevision) Error() string {
	if err == nil {
		return "relation proposal needs revision"
	}
	return "relation proposal needs revision: " + err.reason
}

type relationApplyBaselineError struct{ reason string }

func (err *relationApplyBaselineError) Error() string {
	if err == nil {
		return "relation proposal baseline changed"
	}
	return err.reason
}

func baselineChanged(reason string) error { return &relationApplyBaselineError{reason: reason} }

func relationApplyNodeKey(ref domain.NodeRef) string {
	return string(ref.Type) + "\x00" + string(ref.ID)
}

func relationApplyLifecycleActive(nodeType domain.NodeType, lifecycle string) bool {
	if nodeType == domain.NodeTypeTopic {
		return lifecycle == string(domain.TopicStatusActive)
	}
	return lifecycle == string(domain.ClaimStatusSuggested) || lifecycle == string(domain.ClaimStatusConfirmed) || lifecycle == string(domain.ClaimStatusDisputed)
}

func latestRelationApplyTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value.UTC()
		}
	}
	return latest
}

func relationVersionBeforeConfirm(existing *domain.RelationResult) int64 {
	if existing == nil {
		return 1
	}
	return existing.Relation.Version
}

func validateReusableSuggestedRelation(existing domain.RelationResult, workspaceID foundation.ID, changeSet changecontroldomain.KnowledgeChangeSet, fingerprint string) error {
	if existing.Relation.Status != domain.RelationStatusSuggested ||
		existing.Relation.WorkspaceID != workspaceID ||
		existing.Relation.Source != changeSet.Source ||
		existing.Relation.Target != changeSet.Target ||
		existing.Relation.Type != changeSet.RelationType ||
		existing.Relation.Fingerprint != fingerprint {
		return errors.New("canonical relation is not a reusable suggested relation")
	}
	return nil
}
func relationApplyInvalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "RELATION_PROPOSAL_APPLY_INVALID", false, cause)
}

func relationApplyNotFound(cause error) error {
	return foundation.NewError(foundation.ErrorNotFound, "RELATION_PROPOSAL_APPLY_NOT_FOUND", false, cause)
}

func relationApplyApprovalRequired(cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, "RELATION_PROPOSAL_APPROVAL_REQUIRED", false, cause)
}

func relationApplyConsistency(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "RELATION_PROPOSAL_APPLY_CONSISTENCY", false, cause)
}

func relationApplyUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "RELATION_PROPOSAL_APPLY_UNAVAILABLE", true, cause)
}

func relationApplyClassify(err error, code string) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
	}
	if sqlState := platformpostgres.SQLState(err); sqlState != "" {
		switch sqlState {
		case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, code, false, err)
		}
	}
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}
