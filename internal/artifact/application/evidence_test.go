package application

import (
	"context"
	"testing"

	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestServerCitationVerifierRejectsSubstitutedFrozenCitationIdentity(t *testing.T) {
	workspaceID := appID(600)
	input := CitationInput{
		IndexVersionID: appID(601), ChunkID: appID(602),
		SourceVersionID: appID(603), SourceSpanID: appID(604),
	}
	tests := []struct {
		name   string
		mutate func(*retrievaldomain.CitationReferenceQuery)
	}{
		{name: "index version", mutate: func(query *retrievaldomain.CitationReferenceQuery) { query.IndexVersionID = appID(605) }},
		{name: "chunk", mutate: func(query *retrievaldomain.CitationReferenceQuery) { query.ChunkID = appID(606) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := retrievaldomain.CitationReferenceQuery{
				WorkspaceID: workspaceID, IndexVersionID: input.IndexVersionID, ChunkID: input.ChunkID,
				SourceVersionID: input.SourceVersionID, SourceSpanID: input.SourceSpanID,
			}
			test.mutate(&query)
			opener := &citationEvidenceOpenerFake{opened: []retrievalapplication.OpenedCitationEvidence{{Query: query}}}
			eligibility := &citationEligibilityFake{}
			verifier, err := NewServerCitationVerifier(opener, eligibility)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.VerifyCitations(context.Background(), workspaceID, []CitationInput{input}); err == nil {
				t.Fatal("substituted frozen citation identity was accepted")
			}
			if eligibility.calls != 0 {
				t.Fatalf("eligibility calls=%d, want 0", eligibility.calls)
			}
		})
	}
}

type citationEvidenceOpenerFake struct {
	opened []retrievalapplication.OpenedCitationEvidence
}

func (fake *citationEvidenceOpenerFake) OpenCitationEvidenceBatch(context.Context, []retrievaldomain.CitationReferenceQuery) ([]retrievalapplication.OpenedCitationEvidence, error) {
	return append([]retrievalapplication.OpenedCitationEvidence(nil), fake.opened...), nil
}

type citationEligibilityFake struct{ calls int }

func (fake *citationEligibilityFake) CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	fake.calls++
	return nil, nil
}

var _ CitationEvidenceOpener = (*citationEvidenceOpenerFake)(nil)
var _ EvidenceEligibilityChecker = (*citationEligibilityFake)(nil)
