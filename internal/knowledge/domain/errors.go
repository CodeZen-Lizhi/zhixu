// Package domain 定义 Knowledge 模块的稳定领域合同与纯业务规则。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	ErrorCodeTextInvalid                = "KNOWLEDGE_TEXT_INVALID"
	ErrorCodeApplicabilityInvalid       = "KNOWLEDGE_APPLICABILITY_INVALID"
	ErrorCodeProvenanceInvalid          = "KNOWLEDGE_PROVENANCE_INVALID"
	ErrorCodeConfirmationInvalid        = "KNOWLEDGE_CONFIRMATION_INVALID"
	ErrorCodeTopicInvalid               = "KNOWLEDGE_TOPIC_INVALID"
	ErrorCodeClaimInvalid               = "KNOWLEDGE_CLAIM_INVALID"
	ErrorCodeClaimTransitionInvalid     = "KNOWLEDGE_CLAIM_TRANSITION_INVALID"
	ErrorCodeClaimSourceInvalid         = "KNOWLEDGE_CLAIM_SOURCE_INVALID"
	ErrorCodeRelationInvalid            = "KNOWLEDGE_RELATION_INVALID"
	ErrorCodeRelationTransitionInvalid  = "KNOWLEDGE_RELATION_TRANSITION_INVALID"
	ErrorCodeRelationEvidenceInvalid    = "KNOWLEDGE_RELATION_EVIDENCE_INVALID"
	ErrorCodeRelationAssessmentInvalid  = "KNOWLEDGE_RELATION_ASSESSMENT_INVALID"
	ErrorCodeEvidenceEligibilityInvalid = "KNOWLEDGE_EVIDENCE_ELIGIBILITY_INVALID"
	ErrorCodeConflictInvalid            = "KNOWLEDGE_CONFLICT_INVALID"
	ErrorCodeConflictTransitionInvalid  = "KNOWLEDGE_CONFLICT_TRANSITION_INVALID"
	ErrorCodeRequestInvalid             = "KNOWLEDGE_REQUEST_INVALID"
	ErrorCodeVersionConflict            = "KNOWLEDGE_VERSION_CONFLICT"
	ErrorCodeIdempotencyConflict        = "KNOWLEDGE_IDEMPOTENCY_CONFLICT"
)

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}

func versionConflict(code, message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, code, false, errors.New(message))
}

func inconsistent(code, message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New(message))
}
