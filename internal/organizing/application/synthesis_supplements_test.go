package application

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"strings"
	"testing"
)

func TestSynthesisMixedBodyAndSupplementReceipt(t *testing.T) {
	id := foundation.ID("00000000-0000-4000-8000-000000000001")
	result := SynthesisApplyResult{ProcessingID: id, Changed: true, SourcesChanged: true, RevisionIDs: []foundation.ID{id}, Publications: []SynthesisPublicationCommand{{NoteID: id, RevisionID: id, DocumentID: id, ArticleRevisionID: id, ContentHash: strings.Repeat("a", 64), IdempotencyKey: "synthesis-publish:" + string(id)}}}
	if err := ValidateSynthesisApplyResult(result); err != nil {
		t.Fatalf("mixed change receipt must permit both effects: %v", err)
	}
	result.RevisionIDs = []foundation.ID{}
	if err := ValidateSynthesisApplyResult(result); err == nil {
		t.Fatal("supplements must not bypass revision/publication binding")
	}
	result.Changed = false
	result.Publications = []SynthesisPublicationCommand{}
	if err := ValidateSynthesisApplyResult(result); err != nil {
		t.Fatalf("supplement-only receipt: %v", err)
	}
}
