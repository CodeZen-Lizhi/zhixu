package parser

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

func TestHTMLParserPreservesVisibleTextTokenRanges(t *testing.T) {
	t.Parallel()
	source := []byte("<!doctype html>\r\n<h1>Java <em>AI</em></h1>\r\n<p>Visible &amp; traceable</p>\r\n<pre>code&lt;x&gt;</pre>")

	document, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	wantContent := []string{"Java ", "AI", "Visible &amp; traceable", "code&lt;x&gt;"}
	if len(document.Blocks) != len(wantContent) {
		t.Fatalf("blocks = %#v", document.Blocks)
	}
	for index, block := range document.Blocks {
		if block.Content != wantContent[index] {
			t.Fatalf("block %d content = %q, want %q", index, block.Content, wantContent[index])
		}
		if block.StartByte < 0 || block.EndByte <= block.StartByte || block.EndByte > int64(len(source)) {
			t.Fatalf("block %d range = %#v", index, block)
		}
		if string(source[block.StartByte:block.EndByte]) != block.Content {
			t.Fatalf("block %d does not round-trip to immutable bytes", index)
		}
	}
	if document.Blocks[0].SpanType != "heading" || document.Blocks[1].SpanType != "heading" ||
		!reflect.DeepEqual(document.Blocks[0].HeadingPath, []string{"Java AI"}) ||
		!reflect.DeepEqual(document.Blocks[2].HeadingPath, []string{"Java AI"}) {
		t.Fatalf("heading projection = %#v", document.Blocks)
	}
	if document.Blocks[0].StartLine != 2 || document.Blocks[2].StartLine != 3 || document.Blocks[3].StartLine != 4 {
		t.Fatalf("line mapping = %#v", document.Blocks)
	}
	if document.Blocks[3].SpanType != "code_block" || !document.Blocks[3].Atomic || document.Blocks[3].Selector["tag"] != "pre" {
		t.Fatalf("pre block = %#v", document.Blocks[3])
	}
}

func TestHTMLParserExcludesIgnoredElementContent(t *testing.T) {
	t.Parallel()
	source := []byte("<p>before</p><script>secret <b>script markup</b></script><style>.secret{}</style><noscript>fallback secret</noscript><template><p>template secret</p></template><p>after</p>")

	document, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 2 || document.Blocks[0].Content != "before" || document.Blocks[1].Content != "after" {
		t.Fatalf("ignored content leaked into blocks: %#v", document.Blocks)
	}
	for _, block := range document.Blocks {
		if strings.Contains(block.Content, "secret") || string(source[block.StartByte:block.EndByte]) != block.Content {
			t.Fatalf("invalid visible block: %#v", block)
		}
	}
}

func TestHTMLParserFailsClosedForSelfClosingIgnoredTag(t *testing.T) {
	t.Parallel()
	source := []byte("<p>before</p><script/>must stay hidden<p>also hidden</p>")
	document, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 1 || document.Blocks[0].Content != "before" {
		t.Fatalf("self-closing ignored tag leaked content: %#v", document.Blocks)
	}
}

func TestHTMLParserClosesMalformedHeadingAtBlockBoundary(t *testing.T) {
	t.Parallel()
	source := []byte("<h1>Title<p>Body")
	document, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 2 || document.Blocks[0].SpanType != "heading" || document.Blocks[1].SpanType != "paragraph" ||
		!reflect.DeepEqual(document.Blocks[1].HeadingPath, []string{"Title"}) {
		t.Fatalf("malformed heading projection = %#v", document.Blocks)
	}
}

func TestHTMLParserHandlesBOMLimitsAndCancellation(t *testing.T) {
	t.Parallel()
	source := append([]byte{0xef, 0xbb, 0xbf}, []byte("<p>正文</p>")...)
	original := append([]byte(nil), source...)
	document, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Warnings) != 1 || document.Warnings[0].Code != "UTF8_BOM_PRESENT" || len(document.Blocks) != 1 || document.Blocks[0].StartByte != 6 {
		t.Fatalf("BOM result = %#v", document)
	}
	if !reflect.DeepEqual(source, original) {
		t.Fatalf("parser mutated source: %q", source)
	}

	_, err = NewHTMLParserWithOptions(Options{MaxBytes: 4}).Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: []byte("<p>x</p>")})
	var parserErr *domain.ParserError
	if !errors.As(err, &parserErr) || parserErr.Code != "SOURCE_CONTENT_TOO_LARGE" {
		t.Fatalf("size error = %#v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewHTMLParser().Parse(ctx, domain.SourceInput{MediaType: "text/html", ImmutableBytes: []byte("<p>x</p>")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestHTMLParserFailsClosedOnExcessiveNesting(t *testing.T) {
	t.Parallel()
	source := []byte(strings.Repeat("<div>", maxHTMLNesting+1) + "text" + strings.Repeat("</div>", maxHTMLNesting+1))
	_, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
	var parserErr *domain.ParserError
	if !errors.As(err, &parserErr) || parserErr.Code != "SOURCE_HTML_NESTING_TOO_DEEP" {
		t.Fatalf("nesting error = %#v", err)
	}
}

func TestHTMLParserFailsClosedOnExcessiveVisibleBlocks(t *testing.T) {
	t.Parallel()
	source := []byte(strings.Repeat("<b>x</b>", maxHTMLBlocks+1))
	_, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
	var parserErr *domain.ParserError
	if !errors.As(err, &parserErr) || parserErr.Code != "SOURCE_HTML_BLOCK_LIMIT_EXCEEDED" {
		t.Fatalf("block limit error = %#v", err)
	}
}

func FuzzHTMLParserSpanBounds(f *testing.F) {
	f.Add([]byte("<h1>Title</h1><p>body &amp; evidence</p>"))
	f.Add([]byte("<script>hidden</script><p>visible</p>"))
	f.Add([]byte("<p>malformed <em>body"))
	f.Fuzz(func(t *testing.T, source []byte) {
		if len(source) == 0 {
			return
		}
		document, err := NewHTMLParser().Parse(context.Background(), domain.SourceInput{MediaType: "text/html", ImmutableBytes: source})
		if err != nil {
			return
		}
		for index, block := range document.Blocks {
			if block.StartLine <= 0 || block.EndLine < block.StartLine || block.StartByte < 0 || block.EndByte <= block.StartByte || block.EndByte > int64(len(source)) {
				t.Fatalf("block %d has invalid bounds: %#v", index, block)
			}
			if string(source[block.StartByte:block.EndByte]) != block.Content {
				t.Fatalf("block %d does not round-trip", index)
			}
		}
	})
}
