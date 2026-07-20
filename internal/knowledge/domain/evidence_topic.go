package domain

import "github.com/CodeZen-Lizhi/zhixu/internal/foundation"

// EvidenceTopicBinding 是正式证据 Provenance 到 Active Topic 的只读绑定。
type EvidenceTopicBinding struct {
	Provenance ProvenanceRef
	TopicID    foundation.ID
	TopicName  string
}

// ValidateEvidenceTopicBinding 校验 Topic 身份、规范名称和完整 Provenance。
func ValidateEvidenceTopicBinding(binding EvidenceTopicBinding) error {
	if !validID(binding.TopicID) {
		return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "evidence topic identity is invalid")
	}
	display, _, err := NormalizeTopicText(binding.TopicName)
	if err != nil || display != binding.TopicName || ValidateProvenanceRef(binding.Provenance) != nil {
		return inconsistent(ErrorCodeEvidenceEligibilityInvalid, "evidence topic binding is invalid")
	}
	return nil
}
