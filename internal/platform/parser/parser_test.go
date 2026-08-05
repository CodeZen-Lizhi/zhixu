package parser

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

func TestMarkdownParserPreservesStructuralRanges(t *testing.T) {
	t.Parallel()
	source := []byte("# Guide\n\nIntro paragraph.\n\n```go\nfmt.Println(\"x\")\n```\n\n| A | B |\n|---|:--:|\n| 1 | 2 |\n")
	parser := NewMarkdownParser()
	got, err := parser.Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 4 {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	wantKinds := []string{"heading", "paragraph", "code_block", "table"}
	for i, want := range wantKinds {
		if got.Blocks[i].SpanType != want {
			t.Fatalf("block %d kind = %q, want %q", i, got.Blocks[i].SpanType, want)
		}
		block := got.Blocks[i]
		if block.StartByte < 0 || block.EndByte <= block.StartByte || int(block.EndByte) > len(source) {
			t.Fatalf("block %d invalid span: %#v", i, block)
		}
		if string(source[block.StartByte:block.EndByte]) != block.Content {
			t.Fatalf("block %d content does not round-trip", i)
		}
	}
	if got.Blocks[0].HeadingPath == nil || got.Blocks[1].HeadingPath[0] != "Guide" {
		t.Fatalf("heading path = %#v", got.Blocks[1].HeadingPath)
	}
	if !got.Blocks[2].Atomic || !got.Blocks[3].Atomic {
		t.Fatalf("code and table must be atomic: %#v", got.Blocks)
	}
	if !strings.HasPrefix(got.Blocks[2].Content, "```go") || !strings.Contains(got.Blocks[3].Content, "|---|:--:|") {
		t.Fatalf("raw delimiters were lost: %#v", got.Blocks)
	}
	if got.Blocks[3].StartLine != 9 || got.Blocks[3].EndLine != 11 {
		t.Fatalf("table lines = %d-%d", got.Blocks[3].StartLine, got.Blocks[3].EndLine)
	}
}

func TestMarkdownParserHandlesFrontmatterAndWarning(t *testing.T) {
	t.Parallel()
	source := []byte("---\ntitle: [broken\n---\n\n# Body\n")
	got, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 2 || got.Blocks[0].SpanType != "frontmatter" || got.Blocks[1].SpanType != "heading" {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	if got.Blocks[0].Content != "---\ntitle: [broken\n---\n" {
		t.Fatalf("frontmatter content = %q", got.Blocks[0].Content)
	}
	if !hasWarning(got.Warnings, "FRONTMATTER_PARSE_FAILED") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}

	noClose := []byte("---\n# ordinary body\n")
	without, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: noClose})
	if err != nil {
		t.Fatal(err)
	}
	if len(without.Blocks) == 0 || without.Blocks[0].SpanType == "frontmatter" {
		t.Fatalf("unclosed frontmatter was consumed: %#v", without.Blocks)
	}

	empty, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: []byte("---\n---\nbody\n")})
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(empty.Warnings, "FRONTMATTER_PARSE_FAILED") {
		t.Fatalf("empty frontmatter is valid YAML: %#v", empty.Warnings)
	}

	blockScalar := []byte("---\ndescription: |\n  ---\n  text\n---\nbody\n")
	parsed, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: blockScalar})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Blocks) != 2 || parsed.Blocks[0].SpanType != "frontmatter" || !strings.Contains(parsed.Blocks[0].Content, "  ---\n") {
		t.Fatalf("indented frontmatter marker was treated as a close: %#v", parsed.Blocks)
	}
}

func TestMarkdownParserMatchesGoldmarkForShortTableAndIndentedCode(t *testing.T) {
	t.Parallel()
	source := []byte("| a | b |\n|-|-|\n| 1 | 2 |\n\n    code\nparagraph\n")
	got, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 3 || got.Blocks[0].SpanType != "table" || got.Blocks[1].SpanType != "code_block" || got.Blocks[2].SpanType != "paragraph" {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	if got.Blocks[0].Content != "| a | b |\n|-|-|\n| 1 | 2 |\n" || got.Blocks[1].Content != "    code\n" {
		t.Fatalf("raw structure = %#v", got.Blocks)
	}
}

func TestMarkdownParserBOMCRLFAndEmptyFence(t *testing.T) {
	t.Parallel()
	source := []byte("\xef\xbb\xbf# 标题\r\n\r\n```\r\n```\r\n")
	got, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(got.Warnings, "UTF8_BOM_PRESENT") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
	if len(got.Blocks) != 2 || got.Blocks[1].SpanType != "code_block" {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	if got.Blocks[1].Content != "```\r\n```\r\n" || got.Blocks[1].StartLine != 3 || got.Blocks[1].EndLine != 4 {
		t.Fatalf("empty fence = %#v", got.Blocks[1])
	}
	if got.Blocks[0].HeadingPath == nil || got.Blocks[0].HeadingPath[0] != "标题" {
		t.Fatalf("heading path = %#v", got.Blocks[0].HeadingPath)
	}
}

func TestMarkdownParserPreservesATXAndMultilineSetextHeadingRanges(t *testing.T) {
	t.Parallel()
	source := []byte("# ATX title ###\n\nSetext first\nsecond line\n---\n\nbody\n")
	got, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 3 {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	if got.Blocks[0].HeadingPath[0] != "ATX title" || got.Blocks[0].Content != "# ATX title ###\n" {
		t.Fatalf("ATX heading = %#v", got.Blocks[0])
	}
	if got.Blocks[1].SpanType != "heading" || got.Blocks[1].StartLine != 3 || got.Blocks[1].EndLine != 5 || !reflect.DeepEqual(got.Blocks[1].HeadingPath, []string{"ATX title", "Setext first second line"}) {
		t.Fatalf("setext heading = %#v", got.Blocks[1])
	}
	if !reflect.DeepEqual(got.Blocks[2].HeadingPath, []string{"ATX title", "Setext first second line"}) {
		t.Fatalf("body heading path = %#v", got.Blocks[2].HeadingPath)
	}
}

func TestMarkdownParserReportsUnclosedFence(t *testing.T) {
	t.Parallel()
	got, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{
		MediaType:      "text/markdown",
		ImmutableBytes: []byte("```go\nconst x = 1\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 1 || got.Blocks[0].SpanType != "code_block" || !got.Blocks[0].Atomic {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	if !hasWarning(got.Warnings, "UNCLOSED_CODE_FENCE") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
}

func TestMarkdownParserRecognizesContainerBlocksDeterministically(t *testing.T) {
	t.Parallel()
	source := []byte("# Root\n\n> quoted\n> text\n\n- first\n- second\n\nplain\n")
	parser := NewMarkdownParser()
	first, err := parser.Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	second, err := parser.Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("parse result is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	want := []string{"heading", "blockquote", "list", "paragraph"}
	if len(first.Blocks) != len(want) {
		t.Fatalf("blocks = %#v", first.Blocks)
	}
	for index, kind := range want {
		if first.Blocks[index].SpanType != kind {
			t.Fatalf("block %d = %q, want %q", index, first.Blocks[index].SpanType, kind)
		}
	}
}

func TestMarkdownParserKeepsLazyBlockquoteContinuationInOneSpan(t *testing.T) {
	t.Parallel()
	got, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: []byte("> quote\ncontinuation\n\nbody\n")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 2 || got.Blocks[0].SpanType != "blockquote" || got.Blocks[0].Content != "> quote\ncontinuation\n" || got.Blocks[1].SpanType != "paragraph" {
		t.Fatalf("lazy blockquote = %#v", got.Blocks)
	}
}

func TestTextParserDoesNotInterpretMarkdown(t *testing.T) {
	t.Parallel()
	source := []byte("# not a heading\n\n```not code```\n")
	got, err := NewTextParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/plain", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 2 || got.Blocks[0].SpanType != "text" || got.Blocks[1].SpanType != "text" {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	if got.Blocks[0].Content != "# not a heading\n" {
		t.Fatalf("content = %q", got.Blocks[0].Content)
	}
}

func TestParserRejectsInvalidUTF8BinaryAndOversize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
		code string
	}{
		{name: "invalid utf8", data: []byte{0xff, 0xfe}, code: "SOURCE_INVALID_UTF8"},
		{name: "nul", data: []byte("ok\x00bad"), code: "SOURCE_BINARY_CONTENT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: test.data})
			var parserErr *domain.ParserError
			if !errors.As(err, &parserErr) || parserErr.Code != test.code {
				t.Fatalf("error = %#v", err)
			}
		})
	}
	_, err := NewMarkdownParserWithOptions(Options{MaxBytes: 3}).Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: []byte("long")})
	var parserErr *domain.ParserError
	if !errors.As(err, &parserErr) || parserErr.Code != "SOURCE_CONTENT_TOO_LARGE" {
		t.Fatalf("size error = %#v", err)
	}
}

func TestParserHonorsCancellationAndDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := []byte("# title\n")
	original := append([]byte(nil), source...)
	_, err := NewMarkdownParser().Parse(ctx, domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(source, original) {
		t.Fatalf("parser mutated source: %q", source)
	}
}

func TestRawRangeResolverUsesRequiredCoordinateSystems(t *testing.T) {
	t.Parallel()
	source := []byte("one\r\ntwo\nthree")
	raw, err := NewRawRangeResolver().Resolve(source, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if raw.StartByte != 0 || raw.EndByte != 9 || raw.StartLine != 1 || raw.EndLine != 2 {
		t.Fatalf("range = %#v", raw)
	}
	if string(source[raw.StartByte:raw.EndByte]) != "one\r\ntwo\n" {
		t.Fatalf("raw content = %q", source[raw.StartByte:raw.EndByte])
	}
	if _, err := NewRawRangeResolver().Resolve(source, 0, 1); err == nil {
		t.Fatal("expected invalid range")
	}
}

func TestRegistrySelectsOnlySupportedParsers(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Options{})
	markdown, err := registry.ParserFor("text/markdown")
	if err != nil || markdown.Version() != MarkdownParserVersion {
		t.Fatalf("markdown parser = %v, %v", markdown, err)
	}
	textParser, err := registry.ParserFor("text/plain")
	if err != nil || textParser.Version() != TextParserVersion {
		t.Fatalf("text parser = %v, %v", textParser, err)
	}
	htmlParser, err := registry.ParserFor("text/html")
	if err != nil || htmlParser.Version() != HTMLParserVersion {
		t.Fatalf("html parser = %v, %v", htmlParser, err)
	}
	pdfParser, err := registry.ParserFor("application/pdf")
	if err != nil || pdfParser.Version() != PDFParserVersion {
		t.Fatalf("pdf parser = %v, %v", pdfParser, err)
	}
	if _, err := (&Registry{}).ParserFor("text/markdown"); err == nil {
		t.Fatal("zero-value registry must report an unavailable parser")
	}
}

func TestParserDescriptorMatchesSuccessfulProjection(t *testing.T) {
	t.Parallel()
	markdown := NewMarkdownParser()
	descriptor := markdown.Descriptor()
	got, err := markdown.Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: []byte("# title\n")})
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.ID != got.ParserID || descriptor.Version != got.ParserVersion || descriptor.ConfigHash != got.ParserConfigHash || descriptor.SchemaVersion != got.SchemaVersion {
		t.Fatalf("descriptor = %#v, document = %#v", descriptor, got)
	}
	if len(descriptor.ConfigHash) != 64 {
		t.Fatalf("config hash = %q", descriptor.ConfigHash)
	}
	custom := NewMarkdownParserWithOptions(Options{MaxBytes: 1024}).Descriptor()
	if custom.ConfigHash == descriptor.ConfigHash {
		t.Fatal("behavior-affecting parser limits must change the config hash")
	}

	textParser := NewTextParser()
	textDescriptor := textParser.Descriptor()
	if textDescriptor.ID != TextParserID || textDescriptor.Version != TextParserVersion || textDescriptor.ConfigHash == "" || textDescriptor.SchemaVersion != ParseSchemaVersion {
		t.Fatalf("text descriptor = %#v", textDescriptor)
	}

	htmlParser := NewHTMLParser()
	htmlDescriptor := htmlParser.Descriptor()
	htmlDocument, err := htmlParser.Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: []byte("<p>body</p>")})
	if err != nil {
		t.Fatal(err)
	}
	if htmlDescriptor.ID != htmlDocument.ParserID || htmlDescriptor.Version != htmlDocument.ParserVersion || htmlDescriptor.ConfigHash != htmlDocument.ParserConfigHash || htmlDescriptor.SchemaVersion != htmlDocument.SchemaVersion {
		t.Fatalf("html descriptor = %#v, document = %#v", htmlDescriptor, htmlDocument)
	}
}

func hasWarning(warnings []domain.Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func FuzzMarkdownParserSpanBounds(f *testing.F) {
	f.Add([]byte("# title\n\nparagraph\n"))
	f.Add([]byte("```\n```\n"))
	f.Add([]byte("| a | b |\n|---|---|\n|1|2|\n"))
	f.Fuzz(func(t *testing.T, source []byte) {
		if len(source) == 0 {
			return
		}
		got, err := NewMarkdownParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source})
		if err != nil {
			return
		}
		for index, block := range got.Blocks {
			if block.StartLine <= 0 || block.EndLine < block.StartLine || block.StartByte < 0 || block.EndByte < block.StartByte || block.EndByte > int64(len(source)) {
				t.Fatalf("block %d has invalid bounds: %#v", index, block)
			}
			if string(source[block.StartByte:block.EndByte]) != block.Content {
				t.Fatalf("block %d does not round-trip", index)
			}
		}
	})
}

func BenchmarkMarkdownParserTenMB(b *testing.B) {
	unit := "## Section\n\nA deterministic paragraph with enough UTF-8 text to exercise the parser.\n\n"
	source := []byte(strings.Repeat(unit, (10*1024*1024)/len(unit)))
	parser := NewMarkdownParser()
	b.SetBytes(int64(len(source)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parser.Parse(context.Background(), domain.SourceInput{MediaType: "text/markdown", ImmutableBytes: source}); err != nil {
			b.Fatal(err)
		}
	}
}
