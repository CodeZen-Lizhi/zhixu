package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ProvenanceRef 选择一个 Workspace 内不可变 Source Version 到 Source Span 的完整引用。
type ProvenanceRef struct {
	WorkspaceID     foundation.ID
	SourceVersionID foundation.ID
	SourceSpanID    foundation.ID
}

// ConfirmationMethod 是 Relation 正式确认的受控方法。
type ConfirmationMethod string

const (
	ConfirmationUserApproval  ConfirmationMethod = "USER_APPROVAL"
	ConfirmationSourceDerived ConfirmationMethod = "SOURCE_DERIVED"
)

// Confirmation 将确认方法绑定到不可变审批或来源引用。
type Confirmation struct {
	Method    ConfirmationMethod
	Reference string
}

// ClaimSupportType 表示来源支持还是反驳 Claim。
type ClaimSupportType string

const (
	ClaimSupportSupports ClaimSupportType = "SUPPORTS"
	ClaimSupportRefutes  ClaimSupportType = "REFUTES"
)

// ClaimSource 是 Claim 独有的不可变 Provenance 语义实体。
type ClaimSource struct {
	ID, WorkspaceID, ClaimID foundation.ID
	Provenance               ProvenanceRef
	SupportType              ClaimSupportType
	Reason                   string
	EvidenceHash             string
	ModelRunRef              *string
	CreatedAt                time.Time
}

// ValidateProvenanceRef 校验引用身份完整且 Workspace 绑定一致。
func ValidateProvenanceRef(ref ProvenanceRef) error {
	if !validID(ref.WorkspaceID) || !validID(ref.SourceVersionID) || !validID(ref.SourceSpanID) ||
		ref.WorkspaceID == ref.SourceVersionID || ref.WorkspaceID == ref.SourceSpanID || ref.SourceVersionID == ref.SourceSpanID {
		return invalid(ErrorCodeProvenanceInvalid, "provenance identity is invalid")
	}
	return nil
}

// ValidateConfirmation 校验确认方法与非空不可变引用。
func ValidateConfirmation(confirmation Confirmation) error {
	if confirmation.Method != ConfirmationUserApproval && confirmation.Method != ConfirmationSourceDerived {
		return invalid(ErrorCodeConfirmationInvalid, "confirmation method is unsupported")
	}
	reference, err := NormalizeReference(confirmation.Reference, true)
	if err != nil || reference != confirmation.Reference {
		return invalid(ErrorCodeConfirmationInvalid, "confirmation reference is required and canonical")
	}
	return nil
}

// ValidateClaimSource 校验 Claim Source 的 owner、Provenance、语义载荷和 Evidence Hash。
func ValidateClaimSource(source ClaimSource, applicability Applicability) error {
	if !validID(source.ID) || !validID(source.WorkspaceID) || !validID(source.ClaimID) || source.ID == source.ClaimID {
		return invalid(ErrorCodeClaimSourceInvalid, "claim source identity is invalid")
	}
	if err := ValidateProvenanceRef(source.Provenance); err != nil || source.Provenance.WorkspaceID != source.WorkspaceID {
		return inconsistent(ErrorCodeClaimSourceInvalid, "claim source provenance workspace is inconsistent")
	}
	if source.SupportType != ClaimSupportSupports && source.SupportType != ClaimSupportRefutes {
		return invalid(ErrorCodeClaimSourceInvalid, "claim source support type is unsupported")
	}
	reason, err := NormalizeReason(source.Reason, true)
	if err != nil || reason != source.Reason || source.CreatedAt.IsZero() {
		return invalid(ErrorCodeClaimSourceInvalid, "claim source reason or creation time is invalid")
	}
	if err := validateOptionalReference(source.ModelRunRef); err != nil {
		return invalid(ErrorCodeClaimSourceInvalid, "claim source model run reference is invalid")
	}
	if err := ValidateApplicability(applicability); err != nil {
		return invalid(ErrorCodeClaimSourceInvalid, "claim source applicability binding is invalid")
	}
	if !validSHA256(source.EvidenceHash) || source.EvidenceHash != ComputeClaimSourceEvidenceHash(source, applicability) {
		return inconsistent(ErrorCodeClaimSourceInvalid, "claim source evidence hash is inconsistent")
	}
	return nil
}

// ComputeClaimSourceEvidenceHash 计算只属于 Claim Source 语义的 canonical SHA-256。
func ComputeClaimSourceEvidenceHash(source ClaimSource, applicability Applicability) string {
	payload := struct {
		SchemaVersion   string `json:"schema_version"`
		WorkspaceID     string `json:"workspace_id"`
		ClaimID         string `json:"claim_id"`
		SourceVersionID string `json:"source_version_id"`
		SourceSpanID    string `json:"source_span_id"`
		SupportType     string `json:"support_type"`
		Reason          string `json:"reason"`
		Applicability   string `json:"applicability_hash"`
		ModelRunRef     string `json:"model_run_ref"`
	}{
		SchemaVersion: "knowledge-claim-source/v1", WorkspaceID: string(source.WorkspaceID), ClaimID: string(source.ClaimID),
		SourceVersionID: string(source.Provenance.SourceVersionID), SourceSpanID: string(source.Provenance.SourceSpanID),
		SupportType: string(source.SupportType), Reason: source.Reason, Applicability: applicability.Hash, ModelRunRef: optionalString(source.ModelRunRef),
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateOptionalReference(value *string) error {
	if value == nil {
		return nil
	}
	normalized, err := NormalizeReference(*value, true)
	if err != nil || normalized != *value {
		return invalid(ErrorCodeTextInvalid, "reference is not canonical")
	}
	return nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
