package domain

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestValidateCitationReferenceQueryRequiresCompleteUniqueTuple(t *testing.T) {
	query := CitationReferenceQuery{
		WorkspaceID: "97000000-0000-4000-8000-000000000001", IndexVersionID: "97000000-0000-4000-8000-000000000002",
		ChunkID: "97000000-0000-4000-8000-000000000003", SourceVersionID: "97000000-0000-4000-8000-000000000004",
		SourceSpanID: "97000000-0000-4000-8000-000000000005",
	}
	if err := ValidateCitationReferenceQuery(query); err != nil {
		t.Fatal(err)
	}
	query.ChunkID = query.SourceSpanID
	if err := ValidateCitationReferenceQuery(query); err == nil {
		t.Fatal("citation tuple accepted a reused identity")
	}
	query.ChunkID = foundation.ID("invalid")
	if err := ValidateCitationReferenceQuery(query); err == nil {
		t.Fatal("citation tuple accepted an invalid id")
	}
}
