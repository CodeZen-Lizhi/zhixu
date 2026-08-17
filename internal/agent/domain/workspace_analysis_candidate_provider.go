package domain

import (
	"bytes"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WorkspaceAnalysisCandidateProviderResult 是 Provider 可见的无身份候选答案 wire。
// 服务端在持久化前注入实际 Model Run 身份，Provider 不得生成任何运行时 UUID。
type WorkspaceAnalysisCandidateProviderResult struct {
	ResultType    string                            `json:"result_type"`
	SchemaID      string                            `json:"schema_id"`
	SchemaVersion string                            `json:"schema_version"`
	Payload       WorkspaceAnalysisCandidatePayload `json:"payload"`
}

type workspaceAnalysisCandidateProviderDocument struct {
	ResultType    *string                            `json:"result_type"`
	SchemaID      *string                            `json:"schema_id"`
	SchemaVersion *string                            `json:"schema_version"`
	Payload       *workspaceAnalysisCandidatePayload `json:"payload"`
}

// Validate 校验 Provider 只能返回有界正文、短引用和可选 Proposal 建议。
func (result WorkspaceAnalysisCandidateProviderResult) Validate() error {
	if result.ResultType != ResultTypeWorkspaceAnalysisCandidate || result.SchemaID != WorkspaceAnalysisCandidateSchemaID ||
		result.SchemaVersion != workspaceAnalysisCandidateDocumentVersion ||
		!boundedText(result.Payload.AnswerMarkdown, maxWorkspaceAnalysisCandidateAnswerBytes, true) ||
		!validWorkspaceAnalysisCandidateReferences(result.Payload.CitationRefs, true) {
		return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate provider document is invalid")
	}
	if proposal := result.Payload.ProposalSuggestion; proposal != nil {
		if !boundedText(proposal.Summary, maxWorkspaceAnalysisCandidateSummaryBytes, true) ||
			!validWorkspaceAnalysisCandidateReferences(proposal.CitationRefs, true) ||
			!workspaceAnalysisCandidateReferenceSubset(proposal.CitationRefs, result.Payload.CitationRefs) {
			return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate provider proposal is invalid")
		}
	}
	return nil
}

// Compose 注入服务端 Model Run 身份并生成现有不可变 Candidate 文档。
func (result WorkspaceAnalysisCandidateProviderResult) Compose(modelRunRef foundation.ID) (WorkspaceAnalysisCandidateResult, error) {
	if err := result.Validate(); err != nil {
		return WorkspaceAnalysisCandidateResult{}, err
	}
	if !canonicalID(modelRunRef) {
		return WorkspaceAnalysisCandidateResult{}, invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate model run reference is invalid")
	}
	composed := WorkspaceAnalysisCandidateResult{
		ResultType: result.ResultType, SchemaID: result.SchemaID, SchemaVersion: result.SchemaVersion,
		ModelRunRef: modelRunRef,
		Payload: WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: result.Payload.AnswerMarkdown,
			CitationRefs:   append([]string(nil), result.Payload.CitationRefs...),
		},
	}
	if proposal := result.Payload.ProposalSuggestion; proposal != nil {
		composed.Payload.ProposalSuggestion = &WorkspaceAnalysisCandidateProposal{
			Summary: proposal.Summary, CitationRefs: append([]string(nil), proposal.CitationRefs...),
		}
	}
	if err := composed.Validate(); err != nil {
		return WorkspaceAnalysisCandidateResult{}, err
	}
	return composed, nil
}

// DecodeWorkspaceAnalysisCandidateProvider 严格解析无身份候选 wire。
func DecodeWorkspaceAnalysisCandidateProvider(raw []byte, limits DecodeLimits) (WorkspaceAnalysisCandidateProviderResult, error) {
	document, err := DecodeStrict(raw, limits, func(document workspaceAnalysisCandidateProviderDocument) error {
		if document.ResultType == nil || document.SchemaID == nil || document.SchemaVersion == nil || document.Payload == nil ||
			document.Payload.AnswerMarkdown == nil || document.Payload.CitationRefs == nil ||
			len(document.Payload.ProposalSuggestion) == 0 {
			return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate provider document is incomplete")
		}
		return nil
	})
	if err != nil {
		return WorkspaceAnalysisCandidateProviderResult{}, err
	}
	payload, err := decodeWorkspaceAnalysisCandidatePayload(*document.Payload, limits)
	if err != nil {
		return WorkspaceAnalysisCandidateProviderResult{}, err
	}
	result := WorkspaceAnalysisCandidateProviderResult{
		ResultType: *document.ResultType, SchemaID: *document.SchemaID, SchemaVersion: *document.SchemaVersion,
		Payload: payload,
	}
	if err := result.Validate(); err != nil {
		return WorkspaceAnalysisCandidateProviderResult{}, err
	}
	return result, nil
}

// String 不回显候选正文、建议或短引用。
func (result WorkspaceAnalysisCandidateProviderResult) String() string {
	return fmt.Sprintf("WorkspaceAnalysisCandidateProviderResult{citation_refs:%d proposal:%t}",
		len(result.Payload.CitationRefs), result.Payload.ProposalSuggestion != nil)
}

// GoString 避免调试格式展开候选正文。
func (result WorkspaceAnalysisCandidateProviderResult) GoString() string { return result.String() }

func decodeWorkspaceAnalysisCandidatePayload(
	document workspaceAnalysisCandidatePayload,
	limits DecodeLimits,
) (WorkspaceAnalysisCandidatePayload, error) {
	if document.AnswerMarkdown == nil || document.CitationRefs == nil || len(document.ProposalSuggestion) == 0 {
		return WorkspaceAnalysisCandidatePayload{}, invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate payload is incomplete")
	}
	var proposal *WorkspaceAnalysisCandidateProposal
	if !bytes.Equal(bytes.TrimSpace(document.ProposalSuggestion), []byte("null")) {
		decoded, err := DecodeStrict(document.ProposalSuggestion, limits, func(value workspaceAnalysisCandidateProposal) error {
			if value.Summary == nil || value.CitationRefs == nil {
				return invalid(ErrorCodeWorkspaceAnalysisCandidateInvalid, "workspace analysis candidate proposal is incomplete")
			}
			return nil
		})
		if err != nil {
			return WorkspaceAnalysisCandidatePayload{}, err
		}
		proposal = &WorkspaceAnalysisCandidateProposal{
			Summary: *decoded.Summary, CitationRefs: append([]string(nil), (*decoded.CitationRefs)...),
		}
	}
	return WorkspaceAnalysisCandidatePayload{
		AnswerMarkdown: *document.AnswerMarkdown,
		CitationRefs:   append([]string(nil), (*document.CitationRefs)...), ProposalSuggestion: proposal,
	}, nil
}
