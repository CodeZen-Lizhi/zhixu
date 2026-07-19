package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// TopicStatus 是 Topic 的受控生命周期状态。
type TopicStatus string

const (
	TopicStatusActive     TopicStatus = "ACTIVE"
	TopicStatusMerged     TopicStatus = "MERGED"
	TopicStatusDeprecated TopicStatus = "DEPRECATED"
)

// TopicAlias 是 Topic 的规范化别名身份。
type TopicAlias struct {
	Name           string
	NormalizedName string
}

// Topic 是 Knowledge 模块拥有的主题聚合。
type Topic struct {
	ID, WorkspaceID      foundation.ID
	Name                 string
	NormalizedName       string
	Description          string
	Aliases              []TopicAlias
	Status               TopicStatus
	MergedIntoTopicID    *foundation.ID
	Version              int64
	CreatedAt, UpdatedAt time.Time
}

// ClaimStatus 是 Claim 的受控生命周期状态。
type ClaimStatus string

const (
	ClaimStatusSuggested  ClaimStatus = "SUGGESTED"
	ClaimStatusConfirmed  ClaimStatus = "CONFIRMED"
	ClaimStatusDisputed   ClaimStatus = "DISPUTED"
	ClaimStatusSuperseded ClaimStatus = "SUPERSEDED"
	ClaimStatusDeprecated ClaimStatus = "DEPRECATED"
	ClaimStatusInvalid    ClaimStatus = "INVALID"
)

// Claim 是带适用条件、来源和乐观锁版本的最小知识主张。
type Claim struct {
	ID, WorkspaceID      foundation.ID
	Statement            string
	NormalizedStatement  string
	Applicability        Applicability
	Fingerprint          string
	Status               ClaimStatus
	ConfidenceScore      *float64
	ConfidenceFactors    json.RawMessage
	Version              int64
	CreatedAt, UpdatedAt time.Time
}

// ValidateTopic 校验 Topic 的规范文本、别名和不可逆生命周期字段。
func ValidateTopic(topic Topic) error {
	if !validID(topic.ID) || !validID(topic.WorkspaceID) || topic.ID == topic.WorkspaceID {
		return invalid(ErrorCodeTopicInvalid, "topic identity is invalid")
	}
	display, normalized, err := NormalizeTopicText(topic.Name)
	if err != nil || display != topic.Name || normalized != topic.NormalizedName {
		return invalid(ErrorCodeTopicInvalid, "topic name is not canonical")
	}
	description, err := NormalizeDescription(topic.Description)
	if err != nil || description != topic.Description {
		return invalid(ErrorCodeTopicInvalid, "topic description is not canonical")
	}
	seen := map[string]struct{}{topic.NormalizedName: {}}
	for _, alias := range topic.Aliases {
		aliasDisplay, aliasNormalized, aliasErr := NormalizeTopicText(alias.Name)
		if aliasErr != nil || aliasDisplay != alias.Name || aliasNormalized != alias.NormalizedName {
			return invalid(ErrorCodeTopicInvalid, "topic alias is not canonical")
		}
		if _, duplicate := seen[alias.NormalizedName]; duplicate {
			return invalid(ErrorCodeTopicInvalid, "topic aliases collide after normalization")
		}
		seen[alias.NormalizedName] = struct{}{}
	}
	if !validTopicStatus(topic.Status) || topic.Version <= 0 || !validLifecycleTimes(topic.CreatedAt, topic.UpdatedAt) {
		return invalid(ErrorCodeTopicInvalid, "topic lifecycle metadata is invalid")
	}
	if topic.Status == TopicStatusMerged {
		if topic.MergedIntoTopicID == nil || !validID(*topic.MergedIntoTopicID) || *topic.MergedIntoTopicID == topic.ID {
			return invalid(ErrorCodeTopicInvalid, "merged topic requires a different canonical target")
		}
	} else if topic.MergedIntoTopicID != nil {
		return invalid(ErrorCodeTopicInvalid, "only merged topic can carry a merge target")
	}
	return nil
}

// ValidateTopicTransition 校验 Topic 只允许从 ACTIVE 进入历史状态。
func ValidateTopicTransition(from, to TopicStatus) error {
	if from == TopicStatusActive && (to == TopicStatusMerged || to == TopicStatusDeprecated) {
		return nil
	}
	return versionConflict(ErrorCodeTopicInvalid, "topic status transition is not allowed")
}

// ValidateClaim 校验 Claim 的 canonical statement、Applicability、置信信息和生命周期字段。
func ValidateClaim(claim Claim) error {
	if !validID(claim.ID) || !validID(claim.WorkspaceID) || claim.ID == claim.WorkspaceID {
		return invalid(ErrorCodeClaimInvalid, "claim identity is invalid")
	}
	statement, normalized, err := NormalizeStatement(claim.Statement)
	if err != nil || statement != claim.Statement || normalized != claim.NormalizedStatement {
		return invalid(ErrorCodeClaimInvalid, "claim statement is not canonical")
	}
	if err := ValidateApplicability(claim.Applicability); err != nil {
		return invalid(ErrorCodeClaimInvalid, "claim applicability is invalid")
	}
	if claim.Fingerprint != ComputeClaimFingerprint(claim.WorkspaceID, claim.NormalizedStatement, claim.Applicability) {
		return inconsistent(ErrorCodeClaimInvalid, "claim fingerprint is inconsistent")
	}
	if !validClaimStatus(claim.Status) || claim.Version <= 0 || !validLifecycleTimes(claim.CreatedAt, claim.UpdatedAt) {
		return invalid(ErrorCodeClaimInvalid, "claim lifecycle metadata is invalid")
	}
	if claim.ConfidenceScore != nil && (math.IsNaN(*claim.ConfidenceScore) || math.IsInf(*claim.ConfidenceScore, 0) || *claim.ConfidenceScore < 0 || *claim.ConfidenceScore > 1) {
		return invalid(ErrorCodeClaimInvalid, "claim confidence score must be finite and within zero and one")
	}
	factors, err := NormalizeConfidenceFactors(claim.ConfidenceFactors)
	if err != nil || !bytes.Equal(factors, claim.ConfidenceFactors) {
		return invalid(ErrorCodeClaimInvalid, "claim confidence factors must be a canonical json object")
	}
	return nil
}

// ValidateClaimAggregate 校验 Claim 与不可变来源的聚合不变量。
func ValidateClaimAggregate(claim Claim, sources []ClaimSource) error {
	if err := ValidateClaim(claim); err != nil {
		return err
	}
	hasSupport := false
	for _, source := range sources {
		if source.WorkspaceID != claim.WorkspaceID || source.ClaimID != claim.ID {
			return inconsistent(ErrorCodeClaimSourceInvalid, "claim source owner binding is inconsistent")
		}
		if err := ValidateClaimSource(source, claim.Applicability); err != nil {
			return err
		}
		hasSupport = hasSupport || source.SupportType == ClaimSupportSupports
	}
	if claim.Status == ClaimStatusConfirmed && !hasSupport {
		return inconsistent(ErrorCodeClaimInvalid, "confirmed claim requires at least one supporting source")
	}
	return nil
}

// ValidateClaimTransition 校验 Claim 状态是否属于冻结迁移表。
func ValidateClaimTransition(from, to ClaimStatus) error {
	allowed := map[ClaimStatus]map[ClaimStatus]struct{}{
		ClaimStatusSuggested: {
			ClaimStatusConfirmed: {}, ClaimStatusInvalid: {},
		},
		ClaimStatusConfirmed: {
			ClaimStatusDisputed: {}, ClaimStatusSuperseded: {}, ClaimStatusDeprecated: {}, ClaimStatusInvalid: {},
		},
		ClaimStatusDisputed: {
			ClaimStatusConfirmed: {}, ClaimStatusSuperseded: {}, ClaimStatusDeprecated: {}, ClaimStatusInvalid: {},
		},
	}
	if targets := allowed[from]; targets != nil {
		if _, ok := targets[to]; ok {
			return nil
		}
	}
	return versionConflict(ErrorCodeClaimTransitionInvalid, "claim status transition is not allowed")
}

// ComputeClaimFingerprint 绑定 Workspace、case-fold statement 与 Applicability Hash。
func ComputeClaimFingerprint(workspaceID foundation.ID, normalizedStatement string, applicability Applicability) string {
	payload := "knowledge-claim/v1\n" + string(workspaceID) + "\n" + foldedStatement(normalizedStatement) + "\n" + applicability.Hash
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// NormalizeConfidenceFactors 严格规范化置信因素 JSON object。
func NormalizeConfidenceFactors(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	parsed, err := ParseApplicability(raw)
	if err != nil {
		return nil, invalid(ErrorCodeClaimInvalid, "confidence factors must be a bounded json object")
	}
	return append(json.RawMessage(nil), parsed.CanonicalJSON...), nil
}

func validTopicStatus(status TopicStatus) bool {
	return status == TopicStatusActive || status == TopicStatusMerged || status == TopicStatusDeprecated
}

func validClaimStatus(status ClaimStatus) bool {
	switch status {
	case ClaimStatusSuggested, ClaimStatusConfirmed, ClaimStatusDisputed, ClaimStatusSuperseded, ClaimStatusDeprecated, ClaimStatusInvalid:
		return true
	default:
		return false
	}
}

func validLifecycleTimes(createdAt, updatedAt time.Time) bool {
	return !createdAt.IsZero() && !updatedAt.IsZero() && !updatedAt.Before(createdAt)
}

func validID(id foundation.ID) bool {
	parsed, err := foundation.ParseID(string(id))
	return err == nil && parsed == id
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
