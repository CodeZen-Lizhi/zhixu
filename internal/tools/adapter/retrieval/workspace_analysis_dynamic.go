package retrieval

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// SearchKnowledgeV3Executor grants all returned hits. Global aliases are owned
// by the atomic receipt persistence transaction, not by this external read.
type SearchKnowledgeV3Executor struct{ inner *SearchKnowledgeV2Executor }

func NewSearchKnowledgeV3Executor(search searcher, citations retrievalapplication.CitationEvidenceStore) (*SearchKnowledgeV3Executor, error) {
	inner, err := NewSearchKnowledgeV2Executor(search, citations)
	if err != nil {
		return nil, err
	}
	return &SearchKnowledgeV3Executor{inner: inner}, nil
}
func (e *SearchKnowledgeV3Executor) Execute(ctx context.Context, r toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if r.Identity.DefinitionVersion != 2 {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeSearchInputInvalid, errors.New("dynamic search requires workspace analysis v2"))
	}
	var inner *SearchKnowledgeV2Executor
	if e != nil {
		inner = e.inner
	}
	return inner.execute(ctx, r, toolsdomain.ToolRef{Name: "SearchKnowledge", Version: 3}, maxWorkspaceAnalysisSearchHits)
}

// ReadSourceV4Executor opens an immutable tuple resolved from a run-global alias.
type ReadSourceV4Executor struct {
	authority toolsapplication.ReadSourceV4AuthorityReader
	reference citationSourceReference
}

func NewReadSourceV4Executor(authority toolsapplication.ReadSourceV4AuthorityReader, reference citationSourceReference) (*ReadSourceV4Executor, error) {
	if nilDependency(authority) || nilDependency(reference) {
		return nil, dependencyUnavailable(errors.New("dynamic source authority and reference are required"))
	}
	return &ReadSourceV4Executor{authority: authority, reference: reference}, nil
}

type readSourceV4PrivateBinding struct {
	SearchReceiptID   foundation.ID `json:"search_receipt_id"`
	SearchReceiptHash string        `json:"search_receipt_hash"`
	SearchEvidenceRef string        `json:"search_evidence_ref"`
	readSourceV3IdentityDocument
}

func (e *ReadSourceV4Executor) Execute(ctx context.Context, r toolsapplication.ExecutorRequest) (toolsapplication.ExecutorResult, error) {
	if e == nil || nilDependency(e.authority) || nilDependency(e.reference) {
		return toolsapplication.ExecutorResult{}, dependencyUnavailable(errors.New("dynamic source dependency is unavailable"))
	}
	if ctx == nil || validateExactRequestTool(r, toolsdomain.ToolRef{Name: "ReadSource", Version: 4}) != nil || r.Identity.DefinitionVersion != 2 {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeReadSourceInputInvalid, errors.New("dynamic source identity is invalid"))
	}
	input, err := strictjson.DecodeObject(r.Arguments, strictLimits(4*1024, 16, 0), func(v readSourceV3Input) error {
		if !toolsdomain.ValidDynamicEvidenceRef(v.EvidenceRef) {
			return errors.New("dynamic source reference is invalid")
		}
		return nil
	})
	if err != nil {
		return toolsapplication.ExecutorResult{}, inputError(errorCodeReadSourceInputInvalid, err)
	}
	authority, err := e.authority.LoadReadSourceV4Authority(ctx, toolsapplication.ReadSourceV4AuthorityQuery{WorkspaceID: r.Identity.WorkspaceID, WorkflowRunID: r.Identity.WorkflowRunID, EvidenceRef: input.EvidenceRef})
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	identity, err := validateReadSourceV4Authority(r, input.EvidenceRef, authority)
	if err != nil {
		return toolsapplication.ExecutorResult{}, receiptInvalid(err)
	}
	view, err := e.reference.OpenCitationEvidence(ctx, retrievaldomain.CitationReferenceQuery{WorkspaceID: r.Identity.WorkspaceID, IndexVersionID: identity.IndexVersionID, ChunkID: identity.ChunkID, SourceVersionID: identity.SourceVersionID, SourceSpanID: identity.SourceSpanID})
	if err != nil {
		return toolsapplication.ExecutorResult{}, err
	}
	if err := validateReadSourceV3View(r.Identity.WorkspaceID, identity, view); err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	output, err := json.Marshal(readSourceV3Output{EvidenceRef: identity.EvidenceRef, ContentHash: identity.ContentHash, Truncated: view.ExcerptTruncated, Excerpt: view.Excerpt})
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	binding, err := json.Marshal(readSourceV4PrivateBinding{SearchReceiptID: authority.SearchReceipt.ID, SearchReceiptHash: authority.SearchReceipt.OutputHash, SearchEvidenceRef: authority.SearchEvidenceRef, readSourceV3IdentityDocument: readSourceV3IdentityDocumentFrom(identity)})
	if err != nil {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, err)
	}
	contract, _ := toolsdomain.WorkspaceAnalysisResultReceiptContract(r.Tool)
	if int64(len(output)) > contract.MaxOutputBytes || int64(len(binding)) > contract.MaxPrivateBindingBytes {
		return toolsapplication.ExecutorResult{}, resultError(errorCodeReadSourceResultInvalid, errors.New("dynamic source receipt exceeds bound"))
	}
	return toolsapplication.ExecutorResult{Output: output, PrivateBinding: &toolsapplication.ExecutorPrivateBinding{Schema: contract.PrivateBindingSchema, Document: binding}}, nil
}

func validateReadSourceV4Authority(r toolsapplication.ExecutorRequest, ref string, a toolsapplication.ReadSourceV4Authority) (ReadSourceV3Identity, error) {
	if a.WorkspaceID != r.Identity.WorkspaceID || a.WorkflowRunID != r.Identity.WorkflowRunID || !canonicalID(a.AnalysisRunID) || a.SearchReceipt.WorkspaceID != a.WorkspaceID || a.SearchReceipt.WorkflowRunID != a.WorkflowRunID || a.EvidenceRef != ref || a.Identity.EvidenceRef != ref {
		return ReadSourceV3Identity{}, errors.New("dynamic source authority scope drifted")
	}
	items, err := toolsdomain.SearchKnowledgeV3ReceiptIdentities(a.SearchReceipt)
	if err != nil {
		return ReadSourceV3Identity{}, err
	}
	want := a.Identity
	want.EvidenceRef = a.SearchEvidenceRef
	matched := false
	for _, item := range items {
		if item == want {
			matched = true
			break
		}
	}
	if !matched {
		return ReadSourceV3Identity{}, errors.New("dynamic source search tuple drifted")
	}
	identity := readSourceIdentityFromDynamic(a.Identity)
	if err := validateReadSourceV3Identity(a.WorkspaceID, identity, toolsdomain.DynamicWorkspaceAnalysisMaxEvidenceRefs); err != nil {
		return ReadSourceV3Identity{}, err
	}
	return identity, nil
}

func readSourceIdentityFromDynamic(i toolsdomain.DynamicEvidenceIdentity) ReadSourceV3Identity {
	return ReadSourceV3Identity{EvidenceRef: i.EvidenceRef, CitationID: i.CitationID, IndexVersionID: i.IndexVersionID, ChunkID: i.ChunkID, SourceVersionID: i.SourceVersionID, SourceSpanID: i.SourceSpanID, ContentHash: i.ContentHash}
}
