package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// ValidateCitationV4Executor validates multiple historical searches and reads,
// then uses the same real openability and Knowledge eligibility gates as v1.
type ValidateCitationV4Executor struct {
	authority   toolsapplication.ValidateCitationV4AuthorityReader
	reference   citationReference
	eligibility evidenceEligibility
}

func NewValidateCitationV4Executor(authority toolsapplication.ValidateCitationV4AuthorityReader, reference citationReference, eligibility evidenceEligibility) (*ValidateCitationV4Executor, error) {
	if nilDependency(authority) || nilDependency(reference) || nilDependency(eligibility) {
		return nil, dependencyUnavailable(errors.New("dynamic citation authority, reference and eligibility are required"))
	}
	return &ValidateCitationV4Executor{authority: authority, reference: reference, eligibility: eligibility}, nil
}

type validateCitationV4Input struct {
	CandidateID   *foundation.ID `json:"candidate_id"`
	CandidateHash *string        `json:"candidate_hash"`
	EvidenceRefs  []string       `json:"evidence_refs"`
}

type validateCitationV4PrivateIdentity struct {
	SearchReceiptID   foundation.ID `json:"search_receipt_id"`
	SearchReceiptHash string        `json:"search_receipt_hash"`
	SearchEvidenceRef string        `json:"search_evidence_ref"`
	ReadReceiptID     foundation.ID `json:"read_receipt_id"`
	ReadReceiptHash   string        `json:"read_receipt_hash"`
	readSourceV3IdentityDocument
}
type validateCitationV4PrivateBinding struct {
	CandidateID   *foundation.ID                      `json:"candidate_id"`
	CandidateHash *string                             `json:"candidate_hash"`
	Results       []validateCitationV4PrivateIdentity `json:"results"`
}

func decodeValidateCitationV4Input(raw []byte) (validateCitationV4Input, error) {
	value, err := strictjson.DecodeObject(raw, receiptLimits(16*1024), func(v validateCitationV4Input) error {
		if (v.CandidateID == nil) != (v.CandidateHash == nil) || v.CandidateID != nil && (!canonicalID(*v.CandidateID) || !lowerHexValue(*v.CandidateHash, 64)) || len(v.EvidenceRefs) < 1 || len(v.EvidenceRefs) > 8 {
			return errors.New("dynamic citation request is invalid")
		}
		seen := map[string]bool{}
		for _, ref := range v.EvidenceRefs {
			if !toolsdomain.ValidDynamicEvidenceRef(ref) || seen[ref] {
				return errors.New("dynamic citation references are invalid")
			}
			seen[ref] = true
		}
		return nil
	})
	if err != nil {
		return validateCitationV4Input{}, err
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || len(keys) != 3 || keys["candidate_id"] == nil || keys["candidate_hash"] == nil || keys["evidence_refs"] == nil {
		return validateCitationV4Input{}, errors.New("dynamic citation union keys are required")
	}
	return value, nil
}

func (e *ValidateCitationV4Executor) Execute(ctx context.Context, r toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if e == nil || nilDependency(e.authority) || nilDependency(e.reference) || nilDependency(e.eligibility) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("dynamic citation dependency is unavailable"))
	}
	if ctx == nil || validateExactRequestTool(r, toolsdomain.ToolRef{Name: "ValidateCitation", Version: 4}) != nil || r.Identity.DefinitionVersion != 2 {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, errors.New("dynamic citation identity is invalid"))
	}
	input, err := decodeValidateCitationV4Input(r.Arguments)
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, err)
	}
	if input.CandidateID == nil && r.Identity.NodeKey != "decide_next" || input.CandidateID != nil && r.Identity.NodeKey != "validate_citations" {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeCitationInputInvalid, errors.New("dynamic citation purpose does not match its node"))
	}
	query := toolsapplication.ValidateCitationV4AuthorityQuery{WorkspaceID: r.Identity.WorkspaceID, WorkflowRunID: r.Identity.WorkflowRunID, EvidenceRefs: append([]string(nil), input.EvidenceRefs...)}
	if input.CandidateID != nil {
		query.CandidateID, query.CandidateHash = *input.CandidateID, *input.CandidateHash
	}
	a, err := e.authority.LoadValidateCitationV4Authority(ctx, query)
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	identities, bindings, err := validateCitationV4Authority(query, a)
	if err != nil {
		return toolsapplication.ExecutorResult{}, receiptInvalid(err)
	}
	results, err := validateWorkspaceAnalysisCitationIdentities(ctx, r.Identity.WorkspaceID, identities, e.reference, e.eligibility)
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	output, err := json.Marshal(validateCitationV3Output{Results: results})
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
	}
	private, err := json.Marshal(validateCitationV4PrivateBinding{CandidateID: input.CandidateID, CandidateHash: input.CandidateHash, Results: bindings})
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, err)
	}
	c, _ := toolsdomain.WorkspaceAnalysisResultReceiptContract(r.Tool)
	if int64(len(output)) > c.MaxOutputBytes || int64(len(private)) > c.MaxPrivateBindingBytes {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeCitationResultInvalid, errors.New("dynamic citation receipt exceeds bound"))
	}
	return toolsapplication.ExecutorResult{Output: output, PrivateBinding: &toolsapplication.ExecutorPrivateBinding{Schema: c.PrivateBindingSchema, Document: private}}, nil
}

func validateCitationV4Authority(q toolsapplication.ValidateCitationV4AuthorityQuery, a toolsapplication.ValidateCitationV4Authority) ([]ReadSourceV3Identity, []validateCitationV4PrivateIdentity, error) {
	if a.WorkspaceID != q.WorkspaceID || a.WorkflowRunID != q.WorkflowRunID || !canonicalID(a.AnalysisRunID) || a.CandidateID != q.CandidateID || a.CandidateHash != q.CandidateHash || !slices.Equal(a.EvidenceRefs, q.EvidenceRefs) || len(a.SearchReceipts) != len(q.EvidenceRefs) || len(a.ReadSourceReceipts) != len(q.EvidenceRefs) {
		return nil, nil, errors.New("dynamic citation authority scope drifted")
	}
	identities := make([]ReadSourceV3Identity, len(q.EvidenceRefs))
	bindings := make([]validateCitationV4PrivateIdentity, len(q.EvidenceRefs))
	for i, ref := range q.EvidenceRefs {
		search, read := a.SearchReceipts[i], a.ReadSourceReceipts[i]
		if search.WorkspaceID != q.WorkspaceID || search.WorkflowRunID != q.WorkflowRunID || read.WorkspaceID != q.WorkspaceID || read.WorkflowRunID != q.WorkflowRunID {
			return nil, nil, errors.New("dynamic citation receipt owner drifted")
		}
		evidence, err := toolsdomain.ReadSourceV4ReceiptEvidenceForSearch(read, search)
		if err != nil || evidence.EvidenceRef != ref {
			return nil, nil, errors.New("dynamic citation read does not prove requested reference")
		}
		b, err := toolsdomain.ReadSourceV4ReceiptBinding(read)
		if err != nil {
			return nil, nil, err
		}
		identity := readSourceIdentityFromDynamic(b.Identity)
		if err := validateReadSourceV3Identity(q.WorkspaceID, identity, toolsdomain.DynamicWorkspaceAnalysisMaxEvidenceRefs); err != nil {
			return nil, nil, err
		}
		identities[i] = identity
		bindings[i] = validateCitationV4PrivateIdentity{SearchReceiptID: search.ID, SearchReceiptHash: search.OutputHash, SearchEvidenceRef: b.SearchEvidenceRef, ReadReceiptID: read.ID, ReadReceiptHash: read.OutputHash, readSourceV3IdentityDocument: readSourceV3IdentityDocumentFrom(identity)}
	}
	return identities, bindings, nil
}
