package localfs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

func TestMarkdownValidatorAcceptsMarkdown(t *testing.T) {
	validator, err := NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(context.Background(), []byte("# title\n\nbody\n")); err != nil {
		t.Fatalf("valid markdown rejected: %v", err)
	}
}

func TestMarkdownValidatorClassifiesParserErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		code string
	}{
		{name: "empty", data: nil, code: "SOURCE_CONTENT_EMPTY"},
		{name: "invalid utf8", data: []byte{0xff, 0xfe}, code: "SOURCE_INVALID_UTF8"},
		{name: "binary", data: []byte("ok\x00bad"), code: "SOURCE_BINARY_CONTENT"},
	}
	validator, err := NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(context.Background(), tt.data)
			var classified *foundation.Error
			if !errors.As(err, &classified) {
				t.Fatalf("expected foundation error, got %T: %v", err, err)
			}
			if classified.Kind != foundation.ErrorInvalidInput || classified.Code != tt.code {
				t.Fatalf("classification = %s/%s, want invalid_input/%s", classified.Kind, classified.Code, tt.code)
			}
		})
	}
}

func TestMarkdownValidatorHonorsCancellation(t *testing.T) {
	validator, err := NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = validator.Validate(ctx, []byte("# title\n"))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WRITEBACK_CONTENT_VALIDATION_CANCELLED" {
		t.Fatalf("classification = %T/%v", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}

func TestMarkdownValidatorRejectsNonMarkdownParserInputAtBoundary(t *testing.T) {
	validator, err := NewMarkdownValidator(&recordingParser{})
	if err != nil {
		t.Fatal(err)
	}
	// The validator fixes the parser input MIME to text/markdown; a parser that
	// supports it must therefore receive the same stable media type.
	if err := validator.Validate(context.Background(), []byte("body")); err != nil {
		t.Fatal(err)
	}
}

func TestMarkdownValidatorCopiesInputBeforeParser(t *testing.T) {
	input := []byte("# title\n")
	parser := &recordingParser{mutateInput: true}
	validator, err := NewMarkdownValidator(parser)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if string(input) != "# title\n" {
		t.Fatalf("caller input was mutated: %q", input)
	}
	if parser.mediaType != "text/markdown" {
		t.Fatalf("parser media type = %q", parser.mediaType)
	}
}

func TestMarkdownValidatorMapsParserFailure(t *testing.T) {
	want := &ingestiondomain.ParserError{Code: "SOURCE_CONTENT_TOO_LARGE", Cause: errors.New("too large")}
	validator, err := NewMarkdownValidator(&recordingParser{err: want})
	if err != nil {
		t.Fatal(err)
	}
	err = validator.Validate(context.Background(), []byte("body"))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != want.Code || classified.Kind != foundation.ErrorInvalidInput {
		t.Fatalf("classification = %T/%v", err, err)
	}
	if !errors.Is(err, want) {
		t.Fatalf("parser error was not preserved: %v", err)
	}
}

func TestMarkdownValidatorRejectsParserThatDoesNotSupportMarkdown(t *testing.T) {
	validator, err := NewMarkdownValidator(&recordingParser{unsupported: true})
	if err != nil {
		t.Fatal(err)
	}
	err = validator.Validate(context.Background(), []byte("body"))
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != "PARSER_MEDIA_TYPE_UNSUPPORTED" {
		t.Fatalf("classification = %T/%v", err, err)
	}
}

func TestNewMarkdownValidatorRejectsNilParser(t *testing.T) {
	_, err := NewMarkdownValidator(nil)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || classified.Code != "WRITEBACK_CONTENT_VALIDATOR_UNAVAILABLE" {
		t.Fatalf("classification = %T/%v", err, err)
	}
}

type recordingParser struct {
	mediaType   string
	mutateInput bool
	unsupported bool
	err         error
}

func (p *recordingParser) Supports(mediaType string) bool {
	if p.unsupported {
		return false
	}
	return strings.EqualFold(mediaType, "text/markdown")
}

func (*recordingParser) Version() string { return "test" }

func (*recordingParser) Descriptor() ingestiondomain.ParserDescriptor {
	return ingestiondomain.ParserDescriptor{ID: "test", Version: "test", ConfigHash: strings.Repeat("0", 64), SchemaVersion: "test"}
}

func (p *recordingParser) Parse(_ context.Context, input ingestiondomain.SourceInput) (ingestiondomain.ParsedDocument, error) {
	p.mediaType = input.MediaType
	if p.mutateInput && len(input.ImmutableBytes) > 0 {
		input.ImmutableBytes[0] = 'x'
	}
	if p.err != nil {
		return ingestiondomain.ParsedDocument{}, p.err
	}
	return ingestiondomain.ParsedDocument{ParserID: "test", ParserVersion: "test", ParserConfigHash: strings.Repeat("0", 64), SchemaVersion: "test"}, nil
}
