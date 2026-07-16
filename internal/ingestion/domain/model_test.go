package domain

import (
	"context"
	"errors"
	"testing"
)

func TestValidateAttemptTransitionSeparatesQuarantineAndWorkflowRetry(t *testing.T) {
	if err := ValidateAttemptTransition(AttemptValidating, AttemptParsing, SecurityPassed); err != nil {
		t.Fatalf("validating -> parsing = %v", err)
	}
	if err := ValidateAttemptState(AttemptParsing, SecurityQuarantined); err == nil {
		t.Fatal("quarantined parsing state accepted")
	}
	if err := ValidateAttemptState(AttemptParsing, SecurityPending); err == nil {
		t.Fatal("pending parsing state accepted")
	}
	if err := ValidateAttemptTransition(AttemptValidating, AttemptParsed, SecurityPassed); err == nil {
		t.Fatal("skipped parser state accepted")
	}
	if err := ValidateAttemptTransition(AttemptChunked, AttemptParsing, SecurityPassed); err == nil {
		t.Fatal("terminal chunked retry accepted")
	}
}

func TestParseContentCopiesInputAndRejectsUnsupported(t *testing.T) {
	parser := &fakeParser{}
	input := SourceInput{MediaType: "text/plain", ImmutableBytes: []byte("hello")}
	parsed, err := ParseContent(context.Background(), parser, input)
	if err != nil || parsed.ParserVersion != "fake-1" {
		t.Fatalf("ParseContent() = %#v, %v", parsed, err)
	}
	input.ImmutableBytes[0] = 'X'
	if parser.seen[0] != 'h' {
		t.Fatalf("parser input was not copied: %q", parser.seen)
	}
	_, err = ParseContent(context.Background(), parser, SourceInput{MediaType: "application/pdf", ImmutableBytes: []byte("pdf")})
	var parserErr *ParserError
	if !errors.As(err, &parserErr) || parserErr.Code != "PARSER_MEDIA_TYPE_UNSUPPORTED" {
		t.Fatalf("unsupported error = %v", err)
	}
}

type fakeParser struct{ seen []byte }

func (f *fakeParser) Supports(mediaType string) bool { return mediaType == "text/plain" }
func (f *fakeParser) Version() string                { return "fake-1" }
func (f *fakeParser) Descriptor() ParserDescriptor {
	return ParserDescriptor{ID: "fake", Version: "fake-1", ConfigHash: "hash", SchemaVersion: "v1"}
}
func (f *fakeParser) Parse(_ context.Context, input SourceInput) (ParsedDocument, error) {
	f.seen = input.ImmutableBytes
	return ParsedDocument{ParserID: "fake", ParserVersion: "fake-1"}, nil
}
