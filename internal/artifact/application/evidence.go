package application

import (
	"context"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrieval "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

// CitationEvidenceOpener is the public Retrieval seam used to open immutable
// source spans from full frozen citation tuples.
type CitationEvidenceOpener interface {
	OpenCitationEvidenceBatch(context.Context, []retrieval.CitationReferenceQuery) ([]retrievalapp.OpenedCitationEvidence, error)
}

// EvidenceEligibilityChecker is the public Knowledge seam used to prove that a
// source span belongs to approved formal knowledge.
type EvidenceEligibilityChecker interface {
	CheckEvidenceEligibility(context.Context, knowledge.EvidenceEligibilityQuery) ([]knowledge.ProvenanceEligibility, error)
}

// ServerCitationVerifier reconstructs Artifact citations from Retrieval and
// Knowledge facts. It intentionally has no input fields for client-provided
// verified flags, hashes, or excerpts.
type ServerCitationVerifier struct {
	opener      CitationEvidenceOpener
	eligibility EvidenceEligibilityChecker
}

// NewServerCitationVerifier builds the fail-closed server evidence verifier.
func NewServerCitationVerifier(opener CitationEvidenceOpener, eligibility EvidenceEligibilityChecker) (*ServerCitationVerifier, error) {
	if nilInterface(opener) || nilInterface(eligibility) {
		return nil, unavailable("artifact retrieval and knowledge evidence services are required")
	}
	return &ServerCitationVerifier{opener: opener, eligibility: eligibility}, nil
}

// VerifyCitations validates every frozen retrieval tuple then requires a
// confirmed formal Knowledge binding before returning reconstructed citations.
func (verifier *ServerCitationVerifier) VerifyCitations(ctx context.Context, workspaceID foundation.ID, input []CitationInput) ([]domain.Citation, error) {
	if verifier == nil || nilInterface(verifier.opener) || nilInterface(verifier.eligibility) {
		return nil, unavailable("artifact citation verifier is unavailable")
	}
	if ctx == nil || !validID(workspaceID) || len(input) == 0 || len(input) > knowledge.MaxBatchLimit {
		return nil, evidenceInvalid("artifact citation request is invalid")
	}
	queries := make([]retrieval.CitationReferenceQuery, len(input))
	requested := make(map[retrieval.CitationReferenceQuery]struct{}, len(input))
	for index, value := range input {
		query := retrieval.CitationReferenceQuery{
			WorkspaceID: workspaceID, IndexVersionID: value.IndexVersionID, ChunkID: value.ChunkID,
			SourceVersionID: value.SourceVersionID, SourceSpanID: value.SourceSpanID,
		}
		if err := retrieval.ValidateCitationReferenceQuery(query); err != nil {
			return nil, evidenceInvalid("artifact citation tuple is invalid")
		}
		if _, duplicate := requested[query]; duplicate {
			return nil, evidenceInvalid("artifact citation tuple is duplicated")
		}
		requested[query] = struct{}{}
		queries[index] = query
	}
	opened, err := verifier.opener.OpenCitationEvidenceBatch(ctx, queries)
	if err != nil {
		return nil, err
	}
	if len(opened) != len(queries) {
		return nil, resultInconsistent("retrieval evidence result is incomplete")
	}
	provenance := make([]knowledge.ProvenanceRef, len(opened))
	openedQueries := make(map[retrieval.CitationReferenceQuery]struct{}, len(opened))
	for index, openedCitation := range opened {
		if _, requestedQuery := requested[openedCitation.Query]; !requestedQuery {
			return nil, resultInconsistent("retrieval evidence result is not bound to the requested citation tuple")
		}
		if _, duplicate := openedQueries[openedCitation.Query]; duplicate {
			return nil, resultInconsistent("retrieval evidence result contains a duplicate citation tuple")
		}
		openedQueries[openedCitation.Query] = struct{}{}
		if openedCitation.Query.WorkspaceID != workspaceID ||
			openedCitation.View.Reference.SourceVersion.WorkspaceID != workspaceID ||
			openedCitation.Query.SourceVersionID != openedCitation.View.Reference.SourceVersion.SourceVersionID ||
			openedCitation.Query.SourceSpanID != openedCitation.View.Reference.Span.ID {
			return nil, resultInconsistent("retrieval evidence result is not bound to the requested workspace")
		}
		provenance[index] = knowledge.ProvenanceRef{
			WorkspaceID: workspaceID, SourceVersionID: openedCitation.Query.SourceVersionID, SourceSpanID: openedCitation.Query.SourceSpanID,
		}
	}
	eligibility, err := verifier.eligibility.CheckEvidenceEligibility(ctx, knowledge.EvidenceEligibilityQuery{WorkspaceID: workspaceID, Provenance: provenance})
	if err != nil {
		return nil, err
	}
	if len(eligibility) != len(provenance) {
		return nil, resultInconsistent("knowledge eligibility result is incomplete")
	}
	eligible := make(map[string]knowledge.ProvenanceEligibility, len(eligibility))
	for _, result := range eligibility {
		if result.Provenance.WorkspaceID != workspaceID || knowledge.ValidateProvenanceEligibility(result) != nil {
			return nil, resultInconsistent("knowledge eligibility result is invalid")
		}
		key := provenanceKey(result.Provenance)
		if _, duplicate := eligible[key]; duplicate {
			return nil, resultInconsistent("knowledge eligibility result contains duplicate provenance")
		}
		eligible[key] = result
	}
	citations := make([]domain.Citation, len(opened))
	for index, openedCitation := range opened {
		result, found := eligible[provenanceKey(provenance[index])]
		if !found || result.Eligibility != knowledge.EvidenceEligible {
			return nil, foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeEvidenceInvalid, false, errors.New("artifact citation is not eligible formal knowledge"))
		}
		citations[index] = domain.Citation{
			SourceVersionID:     openedCitation.Query.SourceVersionID,
			SourceSpanID:        openedCitation.Query.SourceSpanID,
			VerifiedContentHash: openedCitation.View.Reference.SourceVersion.ContentHash,
			Excerpt:             openedCitation.View.Excerpt,
			Verified:            true,
		}
	}
	return citations, nil
}

func provenanceKey(value knowledge.ProvenanceRef) string {
	return string(value.SourceVersionID) + "\x00" + string(value.SourceSpanID)
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ CitationVerifier = (*ServerCitationVerifier)(nil)
