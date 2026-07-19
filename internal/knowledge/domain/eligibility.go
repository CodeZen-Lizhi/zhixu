package domain

import (
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// EvidenceEligibility 表示 Provenance 是否可作为 Agent 的正式知识证据。
type EvidenceEligibility string

const (
	// EvidenceIneligible 表示 Provenance 没有绑定当前可发布的正式知识。
	EvidenceIneligible EvidenceEligibility = "INELIGIBLE"
	// EvidenceEligible 表示 Provenance 绑定已确认的 Claim 或 Relation。
	EvidenceEligible EvidenceEligibility = "ELIGIBLE"
	// EvidenceEligibleWithConflict 表示 Provenance 绑定 Disputed Claim，使用时必须披露 Conflict。
	EvidenceEligibleWithConflict EvidenceEligibility = "ELIGIBLE_WITH_CONFLICT"
)

// EvidenceOwnerType 标识正式 Evidence 所属的知识聚合类型。
type EvidenceOwnerType string

const (
	// EvidenceOwnerClaim 表示 Claim Source 证据。
	EvidenceOwnerClaim EvidenceOwnerType = "CLAIM"
	// EvidenceOwnerRelation 表示 Relation Evidence 证据。
	EvidenceOwnerRelation EvidenceOwnerType = "RELATION"
)

// EvidenceEligibilityBinding 保留一个正式知识聚合对 Provenance 的确定绑定。
type EvidenceEligibilityBinding struct {
	OwnerType                 EvidenceOwnerType
	OwnerID                   foundation.ID
	EvidenceID                foundation.ID
	ClaimStatus               ClaimStatus
	RelationStatus            RelationStatus
	SupportType               ClaimSupportType
	RelationType              RelationType
	ConflictIDs               []foundation.ID
	DisputedApplicability     Applicability
	DisputedClaimUpdatedAtUTC time.Time
}

// ProvenanceEligibility 是一个 Provenance 的 fail-closed 资格判断。
type ProvenanceEligibility struct {
	Provenance  ProvenanceRef
	Eligibility EvidenceEligibility
	Bindings    []EvidenceEligibilityBinding
}

// EvidenceEligibilityQuery 是最多 500 个 Provenance 的 Workspace 作用域批量查询。
type EvidenceEligibilityQuery struct {
	WorkspaceID foundation.ID
	Provenance  []ProvenanceRef
}

// ValidateEvidenceEligibilityQuery 校验 Workspace、数量、身份和重复 Provenance。
func ValidateEvidenceEligibilityQuery(query EvidenceEligibilityQuery) error {
	if !validID(query.WorkspaceID) || len(query.Provenance) == 0 || len(query.Provenance) > MaxBatchLimit {
		return invalid(ErrorCodeRequestInvalid, "evidence eligibility query scope is invalid")
	}
	seen := make(map[string]struct{}, len(query.Provenance))
	for _, ref := range query.Provenance {
		if ValidateProvenanceRef(ref) != nil || ref.WorkspaceID != query.WorkspaceID {
			return invalid(ErrorCodeRequestInvalid, "evidence eligibility query provenance is invalid")
		}
		key := provenanceKey(ref)
		if _, duplicate := seen[key]; duplicate {
			return invalid(ErrorCodeRequestInvalid, "evidence eligibility query contains duplicate provenance")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// CanonicalEvidenceEligibilityQuery 返回不修改入参且按 Provenance 身份稳定排序的查询。
func CanonicalEvidenceEligibilityQuery(query EvidenceEligibilityQuery) (EvidenceEligibilityQuery, error) {
	if err := ValidateEvidenceEligibilityQuery(query); err != nil {
		return EvidenceEligibilityQuery{}, err
	}
	canonical := EvidenceEligibilityQuery{
		WorkspaceID: query.WorkspaceID,
		Provenance:  append([]ProvenanceRef(nil), query.Provenance...),
	}
	sort.Slice(canonical.Provenance, func(left, right int) bool {
		return provenanceKey(canonical.Provenance[left]) < provenanceKey(canonical.Provenance[right])
	})
	return canonical, nil
}

// ValidateProvenanceEligibility 校验资格结果、正式绑定和 Conflict 披露不变量。
func ValidateProvenanceEligibility(result ProvenanceEligibility) error {
	if err := ValidateProvenanceRef(result.Provenance); err != nil {
		return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "evidence eligibility provenance is invalid")
	}
	previous := ""
	hasConflict := false
	for _, binding := range result.Bindings {
		if !validID(binding.OwnerID) || !validID(binding.EvidenceID) || binding.OwnerID == binding.EvidenceID {
			return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "evidence eligibility binding identity is invalid")
		}
		key := eligibilityBindingKey(binding)
		if previous != "" && key <= previous {
			return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "evidence eligibility bindings are not uniquely ordered")
		}
		previous = key
		switch binding.OwnerType {
		case EvidenceOwnerClaim:
			if binding.RelationStatus != "" || binding.RelationType != "" ||
				(binding.ClaimStatus != ClaimStatusConfirmed && binding.ClaimStatus != ClaimStatusDisputed) ||
				(binding.SupportType != ClaimSupportSupports && binding.SupportType != ClaimSupportRefutes) {
				return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "claim eligibility binding is invalid")
			}
			if binding.ClaimStatus == ClaimStatusDisputed {
				if len(binding.ConflictIDs) == 0 || !validOrderedIDs(binding.ConflictIDs) ||
					ValidateApplicability(binding.DisputedApplicability) != nil || binding.DisputedClaimUpdatedAtUTC.IsZero() ||
					binding.DisputedClaimUpdatedAtUTC.Location() != time.UTC {
					return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "disputed claim eligibility lacks conflict disclosure")
				}
				hasConflict = true
			} else if len(binding.ConflictIDs) != 0 || !emptyApplicability(binding.DisputedApplicability) || !binding.DisputedClaimUpdatedAtUTC.IsZero() {
				return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "confirmed claim eligibility contains conflict bindings")
			}
		case EvidenceOwnerRelation:
			if binding.ClaimStatus != "" || binding.SupportType != "" || len(binding.ConflictIDs) != 0 ||
				!emptyApplicability(binding.DisputedApplicability) || !binding.DisputedClaimUpdatedAtUTC.IsZero() ||
				binding.RelationStatus != RelationStatusConfirmed || !validRelationType(binding.RelationType) {
				return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "relation eligibility binding is invalid")
			}
		default:
			return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "evidence eligibility owner type is invalid")
		}
	}
	want := EvidenceIneligible
	if len(result.Bindings) > 0 {
		want = EvidenceEligible
	}
	if hasConflict {
		want = EvidenceEligibleWithConflict
	}
	if result.Eligibility != want {
		return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "evidence eligibility classification is inconsistent")
	}
	return nil
}

func emptyApplicability(value Applicability) bool {
	return value.SchemaVersion == "" && len(value.CanonicalJSON) == 0 && value.Hash == ""
}

// ClassifyEvidenceEligibility 根据已过滤的正式绑定计算 fail-closed 资格。
func ClassifyEvidenceEligibility(bindings []EvidenceEligibilityBinding) EvidenceEligibility {
	if len(bindings) == 0 {
		return EvidenceIneligible
	}
	for _, binding := range bindings {
		if binding.OwnerType == EvidenceOwnerClaim && binding.ClaimStatus == ClaimStatusDisputed {
			return EvidenceEligibleWithConflict
		}
	}
	return EvidenceEligible
}

func provenanceKey(ref ProvenanceRef) string {
	return string(ref.SourceVersionID) + "\x00" + string(ref.SourceSpanID)
}

func eligibilityBindingKey(binding EvidenceEligibilityBinding) string {
	return string(binding.OwnerType) + "\x00" + string(binding.OwnerID) + "\x00" + string(binding.EvidenceID)
}

func validOrderedIDs(ids []foundation.ID) bool {
	previous := foundation.ID("")
	for _, id := range ids {
		if !validID(id) || (previous != "" && id <= previous) {
			return false
		}
		previous = id
	}
	return true
}

func validRelationType(value RelationType) bool {
	switch value {
	case RelationCites, RelationDerivedFrom, RelationBelongsTo, RelationSupports, RelationComplements,
		RelationDuplicates, RelationConflictsWith, RelationPrerequisiteOf, RelationVersionOf, RelationImpacts:
		return true
	default:
		return false
	}
}
