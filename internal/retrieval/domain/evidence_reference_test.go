package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidateSourceVersionReference(t *testing.T) {
	valid := validSourceVersionReference()
	if err := ValidateSourceVersionReference(valid); err != nil {
		t.Fatalf("valid source version reference: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SourceVersionReference)
	}{
		{name: "workspace id", mutate: func(value *SourceVersionReference) { value.WorkspaceID = "invalid" }},
		{name: "source id", mutate: func(value *SourceVersionReference) { value.SourceID = "" }},
		{name: "artifact id", mutate: func(value *SourceVersionReference) { value.ContentArtifactID = "invalid" }},
		{name: "source type", mutate: func(value *SourceVersionReference) { value.SourceType = " source " }},
		{name: "logical name", mutate: func(value *SourceVersionReference) { value.LogicalName = "" }},
		{name: "relative path", mutate: func(value *SourceVersionReference) { value.RelativePath = "../secret.md" }},
		{name: "content hash", mutate: func(value *SourceVersionReference) { value.ContentHash = "ABC" }},
		{name: "byte size", mutate: func(value *SourceVersionReference) { value.ByteSize = -1 }},
		{name: "media type", mutate: func(value *SourceVersionReference) { value.MediaType = "" }},
		{name: "security status", mutate: func(value *SourceVersionReference) { value.SecurityStatus = " pending " }},
		{name: "captured at", mutate: func(value *SourceVersionReference) { value.CapturedAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := ValidateSourceVersionReference(candidate); err == nil {
				t.Fatal("expected invalid source version reference")
			}
		})
	}
}

func TestValidateSourceSpanReference(t *testing.T) {
	valid := validSourceSpanReference()
	if err := ValidateSourceSpanReference(valid); err != nil {
		t.Fatalf("valid source span reference: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SourceSpanReference)
	}{
		{name: "projection id", mutate: func(value *SourceSpanReference) { value.ParseProjectionID = "invalid" }},
		{name: "span id", mutate: func(value *SourceSpanReference) { value.Span.ID = "invalid" }},
		{name: "span type", mutate: func(value *SourceSpanReference) { value.SpanType = "" }},
		{name: "line range", mutate: func(value *SourceSpanReference) { value.Span.EndLine = 0 }},
		{name: "byte range", mutate: func(value *SourceSpanReference) { value.Span.EndByte = value.SourceVersion.ByteSize + 1 }},
		{name: "selector", mutate: func(value *SourceSpanReference) { value.Selector = json.RawMessage(`[]`) }},
		{name: "excerpt hash", mutate: func(value *SourceSpanReference) { value.ExcerptHash = "invalid" }},
		{name: "parser version", mutate: func(value *SourceSpanReference) { value.ParserVersion = " parser " }},
		{name: "schema version", mutate: func(value *SourceSpanReference) { value.SchemaVersion = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Selector = append(json.RawMessage(nil), valid.Selector...)
			test.mutate(&candidate)
			if err := ValidateSourceSpanReference(candidate); err == nil {
				t.Fatal("expected invalid source span reference")
			}
		})
	}
}

func TestValidateSourceSpanReferenceAcceptsBoundedDerivedText(t *testing.T) {
	value := validSourceSpanReference()
	value.Span.EndByte = value.SourceVersion.ByteSize
	value.EvidenceKind = EvidenceDerivedText
	value.DerivedExcerpt = "PDF extracted evidence"
	if err := ValidateSourceSpanReference(value); err != nil {
		t.Fatalf("valid derived source span: %v", err)
	}
	value.Span.StartByte = 1
	if err := ValidateSourceSpanReference(value); err == nil {
		t.Fatal("expected derived evidence to bind the complete artifact")
	}
}

func validSourceVersionReference() SourceVersionReference {
	return SourceVersionReference{
		WorkspaceID: "91000000-0000-4000-8000-000000000001", SourceID: "91000000-0000-4000-8000-000000000002",
		SourceVersionID: "91000000-0000-4000-8000-000000000003", ContentArtifactID: "91000000-0000-4000-8000-000000000004",
		SourceType: "local_file", LogicalName: "search.md", RelativePath: "docs/search.md",
		ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ByteSize: 128,
		MediaType: "text/markdown", SecurityStatus: "passed", CapturedAt: time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC),
	}
}

func validSourceSpanReference() SourceSpanReference {
	return SourceSpanReference{
		SourceVersion: validSourceVersionReference(), ParseProjectionID: "91000000-0000-4000-8000-000000000005",
		Span:     EvidenceSpan{ID: "91000000-0000-4000-8000-000000000006", StartLine: 1, EndLine: 2, StartByte: 0, EndByte: 12},
		SpanType: "section", Selector: json.RawMessage(`{"heading":["Search"]}`),
		ExcerptHash:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ParserVersion: "goldmark-1", SchemaVersion: "parse-v1",
	}
}
