// Package parser adapts Markdown and plain-text content to the project-owned
// ingestion contract. Third-party AST values never cross this package boundary.
package parser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	goldmarkast "github.com/yuin/goldmark/extension/ast"
	goldmarktext "github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

const (
	// MarkdownParserID is the stable parser identity stored with a projection.
	MarkdownParserID = "goldmark"
	// MarkdownParserVersion is pinned with the module version and parser config.
	MarkdownParserVersion = "goldmark-1.8.4"
	// TextParserID identifies the parser used for plain text input.
	TextParserID = "plain_text"
	// TextParserVersion is the deterministic plain text parser contract version.
	TextParserVersion = "plain-text-v1"
	// ParseSchemaVersion identifies the adapter output contract.
	ParseSchemaVersion = "parse-v1"

	defaultMaxBytes            int64 = 10 * 1024 * 1024
	defaultMaxFrontmatterBytes int64 = 256 * 1024
	defaultMaxFrontmatterLines       = 1000
)

var (
	errInvalidUTF8     = errors.New("source is not valid UTF-8")
	errBinaryContent   = errors.New("source contains binary control bytes")
	errSourceTooLarge  = errors.New("source exceeds parser size limit")
	errParserConfig    = errors.New("parser configuration is invalid")
	errInvalidRawRange = errors.New("raw source range is invalid")
)

// Options bounds parser work. Zero values select the documented safe defaults.
// Limits are applied before third-party parsing so untrusted input cannot make
// the parser allocate an unbounded AST.
type Options struct {
	MaxBytes            int64
	MaxFrontmatterBytes int64
	MaxFrontmatterLines int
}

func defaultOptions() Options {
	return Options{
		MaxBytes:            defaultMaxBytes,
		MaxFrontmatterBytes: defaultMaxFrontmatterBytes,
		MaxFrontmatterLines: defaultMaxFrontmatterLines,
	}
}

func normalizeOptions(options Options) (Options, error) {
	defaults := defaultOptions()
	if options.MaxBytes == 0 {
		options.MaxBytes = defaults.MaxBytes
	}
	if options.MaxFrontmatterBytes == 0 {
		options.MaxFrontmatterBytes = defaults.MaxFrontmatterBytes
	}
	if options.MaxFrontmatterLines == 0 {
		options.MaxFrontmatterLines = defaults.MaxFrontmatterLines
	}
	if options.MaxBytes < 0 || options.MaxFrontmatterBytes < 0 || options.MaxFrontmatterLines < 0 {
		return Options{}, errParserConfig
	}
	return options, nil
}

// MarkdownParser parses CommonMark and the explicit GFM table extension.
type MarkdownParser struct {
	options    Options
	configHash string
	configErr  error
}

// NewMarkdownParser creates a Markdown parser with safe size limits.
func NewMarkdownParser() *MarkdownParser {
	return NewMarkdownParserWithOptions(Options{})
}

// NewMarkdownParserWithOptions creates a Markdown parser with explicit limits.
func NewMarkdownParserWithOptions(options Options) *MarkdownParser {
	normalized, err := normalizeOptions(options)
	return &MarkdownParser{
		options:    normalized,
		configHash: parserConfigHash("goldmark-v1.8.4|commonmark|extension:table|raw-range-v1", normalized),
		configErr:  err,
	}
}

// TextParser parses UTF-8 text as paragraphs and never interprets Markdown.
type TextParser struct {
	options    Options
	configHash string
	configErr  error
}

// NewTextParser creates a plain-text parser with safe size limits.
func NewTextParser() *TextParser {
	return NewTextParserWithOptions(Options{})
}

// NewTextParserWithOptions creates a plain-text parser with explicit limits.
func NewTextParserWithOptions(options Options) *TextParser {
	normalized, err := normalizeOptions(options)
	return &TextParser{
		options:    normalized,
		configHash: parserConfigHash("plain-text-v1|paragraphs|raw-range-v1", normalized),
		configErr:  err,
	}
}

// Registry selects the parser for the two supported MIME types.
type Registry struct {
	markdown *MarkdownParser
	text     *TextParser
}

// NewRegistry creates a registry sharing the same parser limits.
func NewRegistry(options Options) *Registry {
	return &Registry{
		markdown: NewMarkdownParserWithOptions(options),
		text:     NewTextParserWithOptions(options),
	}
}

// ParserFor returns the parser matching a supported media type.
func (r *Registry) ParserFor(mediaType string) (domain.Parser, error) {
	if r == nil {
		return nil, &domain.ParserError{Code: "PARSER_UNAVAILABLE", Cause: domain.ErrParserUnavailable}
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "text/markdown", "text/x-markdown":
		if r.markdown == nil {
			return nil, &domain.ParserError{Code: "PARSER_UNAVAILABLE", Cause: domain.ErrParserUnavailable}
		}
		return r.markdown, nil
	case "text/plain":
		if r.text == nil {
			return nil, &domain.ParserError{Code: "PARSER_UNAVAILABLE", Cause: domain.ErrParserUnavailable}
		}
		return r.text, nil
	default:
		return nil, &domain.ParserError{Code: "PARSER_MEDIA_TYPE_UNSUPPORTED", Cause: domain.ErrUnsupportedMediaType}
	}
}

var _ domain.Parser = (*MarkdownParser)(nil)
var _ domain.Parser = (*TextParser)(nil)
var _ domain.ParserRegistry = (*Registry)(nil)

// Supports reports whether the parser accepts the given MIME type.
func (p *MarkdownParser) Supports(mediaType string) bool {
	if p == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "text/markdown", "text/x-markdown":
		return true
	default:
		return false
	}
}

// Version returns the immutable Markdown parser version.
func (p *MarkdownParser) Version() string { return MarkdownParserVersion }

// Descriptor returns the stable metadata persisted before parsing starts.
func (p *MarkdownParser) Descriptor() domain.ParserDescriptor {
	descriptor := domain.ParserDescriptor{
		ID:            MarkdownParserID,
		Version:       MarkdownParserVersion,
		SchemaVersion: ParseSchemaVersion,
	}
	if p != nil {
		descriptor.ConfigHash = p.configHash
	}
	return descriptor
}

// Parse converts Markdown into project-owned structural blocks.
func (p *MarkdownParser) Parse(ctx context.Context, input domain.SourceInput) (domain.ParsedDocument, error) {
	if p == nil {
		return domain.ParsedDocument{}, &domain.ParserError{Code: "PARSER_UNAVAILABLE", Cause: domain.ErrParserUnavailable}
	}
	if err := validateContextAndInput(ctx, p.configErr, p.Supports(input.MediaType), input.ImmutableBytes, p.options); err != nil {
		return domain.ParsedDocument{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ParsedDocument{}, err
	}

	source := input.ImmutableBytes
	lines := buildLines(source)
	warnings := make([]domain.Warning, 0, 2)
	bomOffset := 0
	if hasBOM(source) {
		bomOffset = 3
		warnings = append(warnings, domain.Warning{
			Code:      "UTF8_BOM_PRESENT",
			Message:   "UTF-8 BOM was accepted and excluded from normalized content",
			StartByte: 0,
			EndByte:   3,
		})
	}
	if bomOffset == len(source) {
		return domain.ParsedDocument{}, parserError("SOURCE_CONTENT_EMPTY", domain.ErrEmptyContent)
	}

	frontmatter := detectFrontmatter(source, lines, bomOffset, p.options)
	if frontmatter.found {
		warnings = append(warnings, frontmatter.warnings...)
	}
	parseStart := bomOffset
	blocks := make([]domain.ParsedBlock, 0)
	if frontmatter.found {
		blocks = append(blocks, domain.ParsedBlock{
			SpanType:  "frontmatter",
			StartLine: int32(frontmatter.startLine + 1),
			EndLine:   int32(frontmatter.endLine + 1),
			StartByte: int64(frontmatter.startByte),
			EndByte:   int64(frontmatter.endByte),
			Selector:  map[string]string{"format": "yaml"},
			Content:   string(source[frontmatter.startByte:frontmatter.endByte]),
			Atomic:    true,
		})
		parseStart = frontmatter.bodyStart
	}

	if parseStart < len(source) {
		if err := ctx.Err(); err != nil {
			return domain.ParsedDocument{}, err
		}
		hints, err := parseGoldmarkHints(ctx, source[parseStart:])
		if err != nil {
			return domain.ParsedDocument{}, err
		}
		hints = shiftHints(hints, lineNumberAt(lines, parseStart))
		rawBlocks, scanWarnings := scanMarkdown(source, lines, parseStart, hints)
		if err := ctx.Err(); err != nil {
			return domain.ParsedDocument{}, err
		}
		warnings = append(warnings, scanWarnings...)
		parsedBlocks, err := buildBlocks(source, rawBlocks)
		if err != nil {
			return domain.ParsedDocument{}, parserError("SOURCE_SPAN_INVALID", err)
		}
		blocks = append(blocks, parsedBlocks...)
	}
	if err := ctx.Err(); err != nil {
		return domain.ParsedDocument{}, err
	}

	return domain.ParsedDocument{
		ParserID:              MarkdownParserID,
		ParserVersion:         MarkdownParserVersion,
		ParserConfigHash:      p.configHash,
		SchemaVersion:         ParseSchemaVersion,
		NormalizedContentHash: normalizedHash(source[bomOffset:]),
		Blocks:                blocks,
		Warnings:              warnings,
	}, nil
}

// Supports reports whether the parser accepts plain text.
func (p *TextParser) Supports(mediaType string) bool {
	return p != nil && strings.EqualFold(strings.TrimSpace(mediaType), "text/plain")
}

// Version returns the immutable plain-text parser version.
func (p *TextParser) Version() string { return TextParserVersion }

// Descriptor returns the stable metadata persisted before parsing starts.
func (p *TextParser) Descriptor() domain.ParserDescriptor {
	descriptor := domain.ParserDescriptor{
		ID:            TextParserID,
		Version:       TextParserVersion,
		SchemaVersion: ParseSchemaVersion,
	}
	if p != nil {
		descriptor.ConfigHash = p.configHash
	}
	return descriptor
}

// Parse converts plain UTF-8 text into paragraph blocks without Markdown rules.
func (p *TextParser) Parse(ctx context.Context, input domain.SourceInput) (domain.ParsedDocument, error) {
	if p == nil {
		return domain.ParsedDocument{}, &domain.ParserError{Code: "PARSER_UNAVAILABLE", Cause: domain.ErrParserUnavailable}
	}
	if err := validateContextAndInput(ctx, p.configErr, p.Supports(input.MediaType), input.ImmutableBytes, p.options); err != nil {
		return domain.ParsedDocument{}, err
	}
	source := input.ImmutableBytes
	lines := buildLines(source)
	warnings := make([]domain.Warning, 0, 1)
	bomOffset := 0
	if hasBOM(source) {
		bomOffset = 3
		warnings = append(warnings, domain.Warning{
			Code:      "UTF8_BOM_PRESENT",
			Message:   "UTF-8 BOM was accepted and excluded from normalized content",
			StartByte: 0,
			EndByte:   3,
		})
	}
	if bomOffset == len(source) {
		return domain.ParsedDocument{}, parserError("SOURCE_CONTENT_EMPTY", domain.ErrEmptyContent)
	}

	blocks := make([]domain.ParsedBlock, 0)
	start := -1
	for i := range lines {
		if err := ctx.Err(); err != nil {
			return domain.ParsedDocument{}, err
		}
		if i == 0 && lines[i].contentStart < bomOffset {
			continue
		}
		if lines[i].blank(source) {
			if start >= 0 {
				block, err := textBlock(source, start, i-1)
				if err != nil {
					return domain.ParsedDocument{}, parserError("SOURCE_SPAN_INVALID", err)
				}
				blocks = append(blocks, block)
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		block, err := textBlock(source, start, len(lines)-1)
		if err != nil {
			return domain.ParsedDocument{}, parserError("SOURCE_SPAN_INVALID", err)
		}
		blocks = append(blocks, block)
	}

	return domain.ParsedDocument{
		ParserID:              TextParserID,
		ParserVersion:         TextParserVersion,
		ParserConfigHash:      p.configHash,
		SchemaVersion:         ParseSchemaVersion,
		NormalizedContentHash: normalizedHash(source[bomOffset:]),
		Blocks:                blocks,
		Warnings:              warnings,
	}, nil
}

// RawRange is a complete raw source range. Bytes are zero-based half-open and
// lines are one-based closed, matching the domain SourceSpan contract.
type RawRange struct {
	StartLine int32
	EndLine   int32
	StartByte int64
	EndByte   int64
}

// RawRangeResolver expands line boundaries to complete source byte ranges.
// It is deliberately independent of goldmark's AST segment details: delimiters
// such as heading markers, code fences, and table separator rows remain inside
// the returned range.
type RawRangeResolver struct{}

// NewRawRangeResolver creates a source range resolver.
func NewRawRangeResolver() RawRangeResolver { return RawRangeResolver{} }

// Resolve returns the raw range covering the inclusive line interval.
func (RawRangeResolver) Resolve(source []byte, startLine, endLine int32) (RawRange, error) {
	if startLine <= 0 || endLine < startLine {
		return RawRange{}, errInvalidRawRange
	}
	lines := buildLines(source)
	if int(endLine) > len(lines) {
		return RawRange{}, errInvalidRawRange
	}
	start := lines[startLine-1].contentStart
	end := lines[endLine-1].end
	if start > end || end > len(source) {
		return RawRange{}, errInvalidRawRange
	}
	return RawRange{
		StartLine: startLine,
		EndLine:   endLine,
		StartByte: int64(start),
		EndByte:   int64(end),
	}, nil
}

type sourceLine struct {
	contentStart int
	contentEnd   int
	end          int
}

func (l sourceLine) text(source []byte) string {
	return string(source[l.contentStart:l.contentEnd])
}

func (l sourceLine) blank(source []byte) bool {
	return strings.TrimSpace(l.text(source)) == ""
}

func buildLines(source []byte) []sourceLine {
	if len(source) == 0 {
		return nil
	}
	lines := make([]sourceLine, 0, bytes.Count(source, []byte{'\n'})+1)
	start := 0
	lineNumber := 0
	for start < len(source) {
		lineNumber++
		end := bytes.IndexByte(source[start:], '\n')
		if end < 0 {
			end = len(source)
		} else {
			end += start + 1
		}
		contentEnd := end
		if contentEnd > start && source[contentEnd-1] == '\n' {
			contentEnd--
		}
		if contentEnd > start && source[contentEnd-1] == '\r' {
			contentEnd--
		}
		contentStart := start
		if lineNumber == 1 && hasBOM(source) {
			contentStart += 3
			if contentStart > contentEnd {
				contentStart = contentEnd
			}
		}
		lines = append(lines, sourceLine{contentStart: contentStart, contentEnd: contentEnd, end: end})
		start = end
	}
	return lines
}

func hasBOM(source []byte) bool {
	return len(source) >= 3 && source[0] == 0xef && source[1] == 0xbb && source[2] == 0xbf
}

func validateContextAndInput(ctx context.Context, configErr error, supported bool, source []byte, options Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if configErr != nil {
		return parserError("PARSER_CONFIGURATION_INVALID", configErr)
	}
	if !supported {
		return parserError("PARSER_MEDIA_TYPE_UNSUPPORTED", domain.ErrUnsupportedMediaType)
	}
	if len(source) == 0 {
		return parserError("SOURCE_CONTENT_EMPTY", domain.ErrEmptyContent)
	}
	if options.MaxBytes > 0 && int64(len(source)) > options.MaxBytes {
		return parserError("SOURCE_CONTENT_TOO_LARGE", errSourceTooLarge)
	}
	if !utf8.Valid(source) {
		return parserError("SOURCE_INVALID_UTF8", errInvalidUTF8)
	}
	for _, value := range source {
		if value == 0 || (value < 0x09) || value == 0x0b || value == 0x0c || (value >= 0x0e && value < 0x20) || value == 0x7f {
			return parserError("SOURCE_BINARY_CONTENT", errBinaryContent)
		}
	}
	return nil
}

func parserError(code string, cause error) error {
	return &domain.ParserError{Code: code, Cause: cause}
}

type frontmatterResult struct {
	found     bool
	startLine int
	endLine   int
	startByte int
	endByte   int
	bodyStart int
	warnings  []domain.Warning
}

func detectFrontmatter(source []byte, lines []sourceLine, bomOffset int, options Options) frontmatterResult {
	result := frontmatterResult{}
	if len(lines) == 0 || !frontmatterMarker(lines[0].text(source), "---") {
		return result
	}
	for i := 1; i < len(lines); i++ {
		marker := strings.TrimSpace(lines[i].text(source))
		if !frontmatterMarker(lines[i].text(source), marker) || (marker != "---" && marker != "...") {
			continue
		}
		start := lines[0].contentStart
		if start < bomOffset {
			start = bomOffset
		}
		end := lines[i].end
		result = frontmatterResult{
			found:     true,
			startLine: 0,
			endLine:   i,
			startByte: start,
			endByte:   end,
			bodyStart: end,
		}
		if int64(end-start) > options.MaxFrontmatterBytes || i+1 > options.MaxFrontmatterLines {
			result.warnings = append(result.warnings, domain.Warning{
				Code:      "FRONTMATTER_TOO_LARGE",
				Message:   "frontmatter exceeds the configured safety limit and was retained as raw content",
				StartByte: int64(start),
				EndByte:   int64(end),
			})
			return result
		}
		var document yaml.Node
		decoder := yaml.NewDecoder(bytes.NewReader(source[lines[0].end:lines[i].contentStart]))
		if err := decoder.Decode(&document); err != nil && !errors.Is(err, io.EOF) {
			result.warnings = append(result.warnings, domain.Warning{
				Code:      "FRONTMATTER_PARSE_FAILED",
				Message:   "frontmatter was retained as raw content because YAML parsing failed",
				StartByte: int64(start),
				EndByte:   int64(end),
			})
		}
		return result
	}
	// An unclosed opening marker is ordinary Markdown (usually a thematic break).
	return frontmatterResult{}
}

func frontmatterMarker(line, marker string) bool {
	if marker != "---" && marker != "..." {
		return false
	}
	// A marker must occupy the whole line and start at column zero. In
	// particular, an indented `---` inside a YAML block scalar is content.
	return strings.TrimRight(line, " \t") == marker
}

type rawBlock struct {
	startLine int
	endLine   int
	kind      string
	path      []string
	selector  map[string]string
	atomic    bool
}

type astHint struct {
	kind    string
	heading string
}

func parseGoldmarkHints(ctx context.Context, source []byte) (map[int]astHint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	markdown := goldmark.New(goldmark.WithExtensions(extension.Table))
	document := markdown.Parser().Parse(goldmarktext.NewReader(source))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lineIndex := buildLines(source)
	hints := make(map[int]astHint)
	startCache := make(map[ast.Node]astNodePosition)
	err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if err := ctx.Err(); err != nil {
			return ast.WalkStop, err
		}
		if !entering || node.Kind() == ast.KindDocument {
			return ast.WalkContinue, nil
		}
		kind := astNodeKind(node)
		if kind == "" {
			return ast.WalkContinue, nil
		}
		start, ok := astNodeStart(node, source, startCache)
		if !ok {
			return ast.WalkContinue, nil
		}
		line := lineNumberAt(lineIndex, start)
		hint := astHint{kind: kind}
		if heading, ok := node.(*ast.Heading); ok {
			hint.heading = strings.Join(strings.Fields(string(heading.Lines().Value(source))), " ")
		}
		if previous, exists := hints[line]; !exists || previous.kind == "paragraph" {
			hints[line] = hint
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}
	return hints, nil
}

func shiftHints(hints map[int]astHint, lineOffset int) map[int]astHint {
	if lineOffset <= 0 || len(hints) == 0 {
		return hints
	}
	shifted := make(map[int]astHint, len(hints))
	for line, hint := range hints {
		shifted[line+lineOffset] = hint
	}
	return shifted
}

func astNodeKind(node ast.Node) string {
	switch node.(type) {
	case *ast.Heading:
		return "heading"
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return "code_block"
	case *goldmarkast.Table:
		return "table"
	case *ast.Blockquote:
		return "blockquote"
	case *ast.List:
		return "list"
	case *ast.Paragraph:
		return "paragraph"
	default:
		return ""
	}
}

type astNodePosition struct {
	offset int
	ok     bool
}

func astNodeStart(node ast.Node, source []byte, cache map[ast.Node]astNodePosition) (int, bool) {
	if cached, exists := cache[node]; exists {
		return cached.offset, cached.ok
	}
	best := len(source) + 1
	if lines := node.Lines(); lines != nil {
		for i := 0; i < lines.Len(); i++ {
			segment := lines.At(i)
			if segment.Start < best {
				best = segment.Start
			}
		}
	}
	if fenced, ok := node.(*ast.FencedCodeBlock); ok && fenced.Info != nil && fenced.Info.Segment.Start < best {
		best = fenced.Info.Segment.Start
	}
	if best > len(source) {
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			if value, ok := astNodeStart(child, source, cache); ok && value < best {
				best = value
			}
		}
	}
	position := astNodePosition{offset: best, ok: best <= len(source)}
	cache[node] = position
	return position.offset, position.ok
}

func scanMarkdown(source []byte, lines []sourceLine, parseStart int, hints map[int]astHint) ([]rawBlock, []domain.Warning) {
	start := lineNumberAt(lines, parseStart)
	if start < 0 {
		start = len(lines)
	}
	blocks := make([]rawBlock, 0)
	warnings := make([]domain.Warning, 0)
	path := make([]string, 0, 4)
	for i := start; i < len(lines); {
		if lines[i].blank(source) {
			i++
			continue
		}
		if marker, length, info, ok := fenceOpening(lines[i].text(source)); ok {
			end := i
			closed := false
			for j := i + 1; j < len(lines); j++ {
				if fenceClosing(lines[j].text(source), marker, length) {
					end = j
					closed = true
					break
				}
				end = j
			}
			block := rawBlock{startLine: i, endLine: end, kind: "code_block", path: clonePath(path), atomic: true}
			if info != "" {
				block.selector = map[string]string{"language": info}
			}
			blocks = append(blocks, applyHint(block, hints))
			if !closed {
				warnings = append(warnings, domain.Warning{
					Code:      "UNCLOSED_CODE_FENCE",
					Message:   "code fence reaches end of source without a closing fence",
					StartByte: int64(lines[i].contentStart),
					EndByte:   int64(lines[end].end),
				})
			}
			i = end + 1
			continue
		}
		if level, heading, ok := atxHeading(lines[i].text(source)); ok {
			path = updateHeadingPath(path, level, heading)
			blocks = append(blocks, rawBlock{
				startLine: i,
				endLine:   i,
				kind:      "heading",
				path:      clonePath(path),
				selector:  map[string]string{"level": fmt.Sprintf("%d", level)},
			})
			i++
			continue
		}
		if i+1 < len(lines) && strings.Contains(lines[i].text(source), "|") && isTableDelimiter(lines[i+1].text(source)) {
			end := i + 1
			for end+1 < len(lines) && !lines[end+1].blank(source) && strings.Contains(lines[end+1].text(source), "|") {
				end++
			}
			blocks = append(blocks, applyHint(rawBlock{
				startLine: i,
				endLine:   end,
				kind:      "table",
				path:      clonePath(path),
				atomic:    true,
			}, hints))
			i = end + 1
			continue
		}
		if indentedCodeLine(lines[i].text(source)) {
			end := i
			for end+1 < len(lines) {
				next := lines[end+1].text(source)
				if lines[end+1].blank(source) || indentedCodeLine(next) {
					end++
					continue
				}
				break
			}
			blocks = append(blocks, applyHint(rawBlock{startLine: i, endLine: end, kind: "code_block", path: clonePath(path), atomic: true}, hints))
			i = end + 1
			continue
		}
		if end, level, heading, ok := setextHeadingAt(source, lines, i); ok {
			path = updateHeadingPath(path, level, heading)
			blocks = append(blocks, rawBlock{
				startLine: i,
				endLine:   end,
				kind:      "heading",
				path:      clonePath(path),
				selector:  map[string]string{"level": fmt.Sprintf("%d", level)},
			})
			i = end + 1
			continue
		}
		if startsBlockquote(lines[i].text(source)) {
			end := consumeBlockquote(source, lines, i)
			blocks = append(blocks, applyHint(rawBlock{startLine: i, endLine: end, kind: "blockquote", path: clonePath(path)}, hints))
			i = end + 1
			continue
		}
		if listMarker(lines[i].text(source)) {
			end := consumeList(source, lines, i)
			blocks = append(blocks, applyHint(rawBlock{startLine: i, endLine: end, kind: "list", path: clonePath(path)}, hints))
			i = end + 1
			continue
		}

		end := i
		for end+1 < len(lines) && !lines[end+1].blank(source) && !startsStructural(source, lines, end+1) {
			end++
		}
		blocks = append(blocks, applyHint(rawBlock{startLine: i, endLine: end, kind: "paragraph", path: clonePath(path)}, hints))
		i = end + 1
	}
	return blocks, warnings
}

func buildBlocks(source []byte, rawBlocks []rawBlock) ([]domain.ParsedBlock, error) {
	resolver := RawRangeResolver{}
	result := make([]domain.ParsedBlock, 0, len(rawBlocks))
	for _, block := range rawBlocks {
		raw, err := resolver.Resolve(source, int32(block.startLine+1), int32(block.endLine+1))
		if err != nil {
			return nil, err
		}
		selector := cloneSelector(block.selector)
		result = append(result, domain.ParsedBlock{
			SpanType:    block.kind,
			StartLine:   raw.StartLine,
			EndLine:     raw.EndLine,
			StartByte:   raw.StartByte,
			EndByte:     raw.EndByte,
			HeadingPath: clonePath(block.path),
			Selector:    selector,
			Content:     string(source[raw.StartByte:raw.EndByte]),
			Atomic:      block.atomic,
		})
	}
	return result, nil
}

func textBlock(source []byte, start, end int) (domain.ParsedBlock, error) {
	resolver := RawRangeResolver{}
	raw, err := resolver.Resolve(source, int32(start+1), int32(end+1))
	if err != nil {
		return domain.ParsedBlock{}, err
	}
	return domain.ParsedBlock{
		SpanType:  "text",
		StartLine: raw.StartLine,
		EndLine:   raw.EndLine,
		StartByte: raw.StartByte,
		EndByte:   raw.EndByte,
		Content:   string(source[raw.StartByte:raw.EndByte]),
	}, nil
}

func applyHint(block rawBlock, hints map[int]astHint) rawBlock {
	selected := false
	for line := block.startLine; line <= block.endLine; line++ {
		hint, ok := hints[line]
		if !ok {
			continue
		}
		if !selected && (hint.kind != "paragraph" || block.kind == "paragraph") {
			block.kind = hint.kind
			selected = true
		}
		if hint.heading != "" && block.kind == "heading" && len(block.path) > 0 {
			block.path[len(block.path)-1] = hint.heading
		}
		if hint.kind == "code_block" || hint.kind == "table" {
			// A container with a protected descendant stays atomic until a
			// container-aware chunk strategy can split it without data loss.
			block.atomic = true
		}
	}
	return block
}

func updateHeadingPath(path []string, level int, heading string) []string {
	if level < 1 {
		level = 1
	}
	if len(path) >= level {
		path = path[:level-1]
	}
	for len(path) < level-1 {
		path = append(path, "")
	}
	return append(path, strings.TrimSpace(heading))
}

func clonePath(path []string) []string {
	if len(path) == 0 {
		return nil
	}
	return append([]string(nil), path...)
}

func cloneSelector(selector map[string]string) map[string]string {
	if len(selector) == 0 {
		return nil
	}
	result := make(map[string]string, len(selector))
	for key, value := range selector {
		result[key] = value
	}
	return result
}

func lineNumberAt(lines []sourceLine, offset int) int {
	if len(lines) == 0 {
		return -1
	}
	index := sort.Search(len(lines), func(i int) bool { return lines[i].end > offset })
	if index >= len(lines) {
		return len(lines) - 1
	}
	return index
}

func atxHeading(line string) (int, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, "#") {
		return 0, "", false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level > 6 || (level < len(trimmed) && trimmed[level] != ' ' && trimmed[level] != '\t') {
		return 0, "", false
	}
	text := strings.TrimSpace(trimmed[level:])
	closing := len(text)
	for closing > 0 && text[closing-1] == '#' {
		closing--
	}
	if closing < len(text) && (closing == 0 || text[closing-1] == ' ' || text[closing-1] == '\t') {
		text = strings.TrimSpace(text[:closing])
	}
	return level, text, true
}

func isSetextUnderline(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) == 0 {
		return false
	}
	mark := trimmed[0]
	if mark != '=' && mark != '-' {
		return false
	}
	for i := 1; i < len(trimmed); i++ {
		if trimmed[i] != mark {
			return false
		}
	}
	return true
}

func setextHeadingAt(source []byte, lines []sourceLine, start int) (int, int, string, bool) {
	parts := make([]string, 0, 2)
	for current := start; current < len(lines); current++ {
		if current > start && startsStructural(source, lines, current) {
			break
		}
		if lines[current].blank(source) {
			break
		}
		parts = append(parts, strings.TrimSpace(lines[current].text(source)))
		if current+1 >= len(lines) || !isSetextUnderline(lines[current+1].text(source)) {
			continue
		}
		level := 1
		if strings.HasPrefix(strings.TrimSpace(lines[current+1].text(source)), "-") {
			level = 2
		}
		return current + 1, level, strings.Join(parts, " "), true
	}
	return 0, 0, "", false
}

func isTableDelimiter(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") {
		return false
	}
	parts := strings.Split(trimmed, "|")
	valid := 0
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(part) < 1 {
			return false
		}
		if part[0] == ':' {
			part = part[1:]
		}
		if len(part) > 0 && part[len(part)-1] == ':' {
			part = part[:len(part)-1]
		}
		// Align with goldmark's Table extension, which accepts one or more
		// dashes in an unaligned delimiter cell.
		minimumDashes := 1
		if len(part) < minimumDashes || strings.Trim(part, "-") != "" {
			return false
		}
		valid++
	}
	return valid >= 2
}

func indentedCodeLine(line string) bool {
	if strings.HasPrefix(line, "\t") {
		return true
	}
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	return spaces >= 4
}

func fenceOpening(line string) (byte, int, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, "", false
	}
	marker := trimmed[0]
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	info := strings.TrimSpace(trimmed[length:])
	if marker == '`' && strings.Contains(info, "`") {
		return 0, 0, "", false
	}
	if fields := strings.Fields(info); len(fields) > 0 {
		info = fields[0]
	}
	return marker, length, info, true
}

func fenceClosing(line string, marker byte, length int) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < length || trimmed[0] != marker {
		return false
	}
	count := 0
	for count < len(trimmed) && trimmed[count] == marker {
		count++
	}
	return count >= length && strings.TrimSpace(trimmed[count:]) == ""
}

func startsBlockquote(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	return len(line)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, ">")
}

func listMarker(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 2 {
		return false
	}
	if (trimmed[0] == '-' || trimmed[0] == '+' || trimmed[0] == '*') && (trimmed[1] == ' ' || trimmed[1] == '\t') {
		return true
	}
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == ')') && (trimmed[i+1] == ' ' || trimmed[i+1] == '\t')
}

func consumeBlockquote(source []byte, lines []sourceLine, start int) int {
	end := start
	for end+1 < len(lines) {
		next := lines[end+1].text(source)
		if startsBlockquote(next) || !lines[end+1].blank(source) && !startsStructural(source, lines, end+1) {
			end++
			continue
		}
		// A blank line terminates a blockquote unless the following line
		// explicitly continues the quoted container.
		if lines[end+1].blank(source) && end+2 < len(lines) && startsBlockquote(lines[end+2].text(source)) {
			end++
			continue
		}
		break
	}
	for end > start && lines[end].blank(source) {
		end--
	}
	return end
}

func consumeList(source []byte, lines []sourceLine, start int) int {
	end := start
	for end+1 < len(lines) {
		next := lines[end+1].text(source)
		if lines[end+1].blank(source) || listMarker(next) || len(next)-len(strings.TrimLeft(next, " ")) >= 2 {
			end++
			continue
		}
		break
	}
	for end > start && lines[end].blank(source) {
		end--
	}
	return end
}

func startsStructural(source []byte, lines []sourceLine, index int) bool {
	if _, _, _, ok := fenceOpening(lines[index].text(source)); ok {
		return true
	}
	if _, _, ok := atxHeading(lines[index].text(source)); ok || startsBlockquote(lines[index].text(source)) || listMarker(lines[index].text(source)) {
		return true
	}
	return index+1 < len(lines) && strings.Contains(lines[index].text(source), "|") && isTableDelimiter(lines[index+1].text(source))
}

func normalizedHash(source []byte) string {
	normalized := bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:])
}

func parserConfigHash(seed string, options Options) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|max_bytes:%d|max_frontmatter_bytes:%d|max_frontmatter_lines:%d",
		seed, options.MaxBytes, options.MaxFrontmatterBytes, options.MaxFrontmatterLines)))
	return hex.EncodeToString(digest[:])
}
