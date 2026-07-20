package domain

import "testing"

func TestValidateEvidenceTopicBindingRequiresCanonicalActiveTopicProjection(t *testing.T) {
	workspace := uuid(1)
	binding := EvidenceTopicBinding{
		TopicID: uuid(2), TopicName: "Go 语言",
		Provenance: ProvenanceRef{WorkspaceID: workspace, SourceVersionID: uuid(3), SourceSpanID: uuid(4)},
	}
	if err := ValidateEvidenceTopicBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.TopicName = " Go 语言 "
	if err := ValidateEvidenceTopicBinding(binding); err == nil {
		t.Fatal("expected non-canonical name rejection")
	}
}
