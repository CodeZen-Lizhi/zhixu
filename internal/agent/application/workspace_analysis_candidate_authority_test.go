package application

import (
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWorkspaceAnalysisCandidateAuthorityQueryRequiresExactDistinctBinding(t *testing.T) {
	valid := WorkspaceAnalysisCandidateAuthorityQuery{
		WorkspaceID:   foundation.ID("97000000-0000-4000-8000-000000000001"),
		WorkflowRunID: foundation.ID("97000000-0000-4000-8000-000000000002"),
		AnalysisRunID: foundation.ID("97000000-0000-4000-8000-000000000003"),
		CandidateID:   foundation.ID("97000000-0000-4000-8000-000000000004"),
		CandidateHash: strings.Repeat("a", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid query: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*WorkspaceAnalysisCandidateAuthorityQuery)
	}{
		{name: "invalid id", mutate: func(value *WorkspaceAnalysisCandidateAuthorityQuery) { value.CandidateID = "candidate" }},
		{name: "reused id", mutate: func(value *WorkspaceAnalysisCandidateAuthorityQuery) { value.CandidateID = value.AnalysisRunID }},
		{name: "uppercase hash", mutate: func(value *WorkspaceAnalysisCandidateAuthorityQuery) { value.CandidateHash = strings.Repeat("A", 64) }},
		{name: "short hash", mutate: func(value *WorkspaceAnalysisCandidateAuthorityQuery) { value.CandidateHash = strings.Repeat("a", 63) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := valid
			test.mutate(&query)
			if err := query.Validate(); err == nil {
				t.Fatal("accepted invalid candidate authority query")
			}
		})
	}
}
