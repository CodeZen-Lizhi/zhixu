package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ConflictStatus 是 Conflict 的受控调查与终结状态。
type ConflictStatus string

const (
	ConflictStatusOpen               ConflictStatus = "OPEN"
	ConflictStatusInvestigating      ConflictStatus = "INVESTIGATING"
	ConflictStatusResolutionProposed ConflictStatus = "RESOLUTION_PROPOSED"
	ConflictStatusResolved           ConflictStatus = "RESOLVED"
	ConflictStatusAcceptedDivergence ConflictStatus = "ACCEPTED_DIVERGENCE"
	ConflictStatusDeferred           ConflictStatus = "DEFERRED"
)

// ConflictSeverity 表示 Conflict 对下游知识消费的影响级别。
type ConflictSeverity string

const (
	ConflictSeverityLow      ConflictSeverity = "LOW"
	ConflictSeverityMedium   ConflictSeverity = "MEDIUM"
	ConflictSeverityHigh     ConflictSeverity = "HIGH"
	ConflictSeverityCritical ConflictSeverity = "CRITICAL"
)

// ApplicabilityAssessment 表示 Conflict 成员适用条件的确定性比较结论。
type ApplicabilityAssessment string

const (
	ApplicabilityAssessmentExact           ApplicabilityAssessment = "EXACT"
	ApplicabilityAssessmentReviewedOverlap ApplicabilityAssessment = "REVIEWED_OVERLAP"
)

// Conflict 是至少包含两个不同 Claim 的持续争议聚合。
type Conflict struct {
	ID, WorkspaceID         foundation.ID
	TopicID                 *foundation.ID
	Status                  ConflictStatus
	Severity                ConflictSeverity
	Summary                 string
	ApplicabilityAssessment ApplicabilityAssessment
	ReviewedOverlapReason   string
	Fingerprint             string
	Resolution              *string
	ResolutionReference     *string
	Version                 int64
	CreatedAt, UpdatedAt    time.Time
}

// ConflictMember 保存 Conflict 中 Claim 的不可变适用条件快照与立场摘要。
type ConflictMember struct {
	ConflictID, WorkspaceID, ClaimID foundation.ID
	Applicability                    Applicability
	ApplicabilityHash                string
	PositionSummary                  string
	CreatedAt                        time.Time
}

// ValidateConflict 校验 Conflict 的身份、生命周期、适用条件结论与解决信息。
func ValidateConflict(conflict Conflict) error {
	if !validID(conflict.ID) || !validID(conflict.WorkspaceID) || conflict.ID == conflict.WorkspaceID ||
		!validConflictStatus(conflict.Status) || !validConflictSeverity(conflict.Severity) || conflict.Version <= 0 ||
		!validLifecycleTimes(conflict.CreatedAt, conflict.UpdatedAt) || !validSHA256(conflict.Fingerprint) {
		return invalid(ErrorCodeConflictInvalid, "conflict identity or lifecycle is invalid")
	}
	if conflict.TopicID != nil && (!validID(*conflict.TopicID) || *conflict.TopicID == conflict.ID) {
		return invalid(ErrorCodeConflictInvalid, "conflict topic identity is invalid")
	}
	summary, err := NormalizeReason(conflict.Summary, true)
	if err != nil || summary != conflict.Summary {
		return invalid(ErrorCodeConflictInvalid, "conflict summary is invalid")
	}
	switch conflict.ApplicabilityAssessment {
	case ApplicabilityAssessmentExact:
		if conflict.ReviewedOverlapReason != "" {
			return invalid(ErrorCodeConflictInvalid, "exact applicability cannot carry overlap reason")
		}
	case ApplicabilityAssessmentReviewedOverlap:
		reason, reasonErr := NormalizeReason(conflict.ReviewedOverlapReason, true)
		if reasonErr != nil || reason != conflict.ReviewedOverlapReason {
			return invalid(ErrorCodeConflictInvalid, "reviewed overlap requires canonical reason")
		}
	default:
		return invalid(ErrorCodeConflictInvalid, "applicability assessment is unsupported")
	}
	if err := validateResolution(conflict.Status, conflict.Resolution, conflict.ResolutionReference); err != nil {
		return err
	}
	return nil
}

// ValidateConflictAggregate 校验 Conflict 与全部成员之间的数量、归属、条件和 fingerprint 不变量。
func ValidateConflictAggregate(conflict Conflict, members []ConflictMember) error {
	if err := ValidateConflict(conflict); err != nil {
		return err
	}
	if len(members) < 2 {
		return inconsistent(ErrorCodeConflictInvalid, "conflict requires at least two members")
	}
	claimIDs := make(map[foundation.ID]struct{}, len(members))
	hashes := make(map[string]struct{}, len(members))
	for _, member := range members {
		if err := ValidateConflictMember(member); err != nil {
			return err
		}
		if member.WorkspaceID != conflict.WorkspaceID || member.ConflictID != conflict.ID {
			return inconsistent(ErrorCodeConflictInvalid, "conflict member owner binding is inconsistent")
		}
		if _, duplicate := claimIDs[member.ClaimID]; duplicate {
			return invalid(ErrorCodeConflictInvalid, "conflict members must be distinct claims")
		}
		claimIDs[member.ClaimID] = struct{}{}
		hashes[member.ApplicabilityHash] = struct{}{}
	}
	if conflict.ApplicabilityAssessment == ApplicabilityAssessmentExact && len(hashes) != 1 {
		return invalid(ErrorCodeConflictInvalid, "exact conflict members require equal applicability hashes")
	}
	if conflict.ApplicabilityAssessment == ApplicabilityAssessmentReviewedOverlap && len(hashes) < 2 {
		return invalid(ErrorCodeConflictInvalid, "reviewed overlap requires different applicability hashes")
	}
	if conflict.Status == ConflictStatusAcceptedDivergence && len(hashes) < 2 {
		return invalid(ErrorCodeConflictInvalid, "accepted divergence requires different applicability hashes")
	}
	if conflict.Fingerprint != ComputeConflictFingerprint(conflict.WorkspaceID, conflict.ApplicabilityAssessment, members) {
		return inconsistent(ErrorCodeConflictInvalid, "conflict fingerprint is inconsistent")
	}
	return nil
}

// ValidateConflictMember 校验成员身份、Applicability 快照和规范化立场摘要。
func ValidateConflictMember(member ConflictMember) error {
	if !validID(member.ConflictID) || !validID(member.WorkspaceID) || !validID(member.ClaimID) || member.ConflictID == member.ClaimID || member.CreatedAt.IsZero() {
		return invalid(ErrorCodeConflictInvalid, "conflict member identity is invalid")
	}
	if err := ValidateApplicability(member.Applicability); err != nil || member.ApplicabilityHash != member.Applicability.Hash {
		return inconsistent(ErrorCodeConflictInvalid, "conflict member applicability is inconsistent")
	}
	position, err := NormalizeReason(member.PositionSummary, true)
	if err != nil || position != member.PositionSummary {
		return invalid(ErrorCodeConflictInvalid, "conflict member position summary is invalid")
	}
	return nil
}

// ValidateConflictTransition 校验 Conflict 是否遵循冻结的状态迁移矩阵。
func ValidateConflictTransition(from, to ConflictStatus) error {
	allowed := map[ConflictStatus]map[ConflictStatus]struct{}{
		ConflictStatusOpen:               {ConflictStatusInvestigating: {}, ConflictStatusDeferred: {}},
		ConflictStatusInvestigating:      {ConflictStatusResolutionProposed: {}, ConflictStatusDeferred: {}},
		ConflictStatusDeferred:           {ConflictStatusInvestigating: {}},
		ConflictStatusResolutionProposed: {ConflictStatusResolved: {}, ConflictStatusAcceptedDivergence: {}, ConflictStatusInvestigating: {}},
	}
	if _, ok := allowed[from][to]; ok {
		return nil
	}
	return versionConflict(ErrorCodeConflictTransitionInvalid, "conflict status transition is not allowed")
}

// ComputeConflictFingerprint 根据 Workspace、适用条件结论和排序后的成员集合计算稳定指纹。
func ComputeConflictFingerprint(workspaceID foundation.ID, assessment ApplicabilityAssessment, members []ConflictMember) string {
	if !validID(workspaceID) || len(members) < 2 {
		return ""
	}
	identities := make([]string, len(members))
	for index, member := range members {
		identities[index] = string(member.ClaimID) + "\x00" + member.ApplicabilityHash
	}
	sort.Strings(identities)
	digest := sha256.Sum256([]byte("knowledge-conflict/v1\n" + string(workspaceID) + "\n" + string(assessment) + "\n" + strings.Join(identities, "\n")))
	return hex.EncodeToString(digest[:])
}

func validateResolution(status ConflictStatus, resolution, reference *string) error {
	requires := status == ConflictStatusResolved || status == ConflictStatusAcceptedDivergence
	if !requires {
		if resolution != nil || reference != nil {
			return invalid(ErrorCodeConflictInvalid, "non-terminal conflict cannot carry resolution")
		}
		return nil
	}
	if resolution == nil || reference == nil {
		return invalid(ErrorCodeConflictInvalid, "terminal conflict requires resolution and reference")
	}
	text, err := NormalizeReason(*resolution, true)
	if err != nil || text != *resolution || validateOptionalReference(reference) != nil {
		return invalid(ErrorCodeConflictInvalid, "conflict resolution is invalid")
	}
	return nil
}

func validConflictStatus(status ConflictStatus) bool {
	switch status {
	case ConflictStatusOpen, ConflictStatusInvestigating, ConflictStatusResolutionProposed, ConflictStatusResolved, ConflictStatusAcceptedDivergence, ConflictStatusDeferred:
		return true
	default:
		return false
	}
}

func validConflictSeverity(severity ConflictSeverity) bool {
	return severity == ConflictSeverityLow || severity == ConflictSeverityMedium || severity == ConflictSeverityHigh || severity == ConflictSeverityCritical
}
