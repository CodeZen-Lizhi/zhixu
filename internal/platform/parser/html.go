package parser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"golang.org/x/net/html"
)

const (
	// HTMLParserID 是 HTML 解析投影持久化使用的稳定解析器身份。
	HTMLParserID = "x_net_html"
	// HTMLParserVersion 冻结 tokenizer 版本与当前可见文本提取语义。
	HTMLParserVersion = "x-net-html-0.56.0-visible-text-v1"

	maxHTMLNesting      = 256
	maxHTMLBlocks       = 10_000
	maxHTMLHeadingBytes = 8 * 1024
)

var (
	errHTMLNestingTooDeep = errors.New("html nesting exceeds parser safety limit")
	errHTMLTooManyBlocks  = errors.New("html visible text block count exceeds parser safety limit")
	errHTMLHeadingTooLong = errors.New("html heading exceeds parser safety limit")
	errHTMLTokenStream    = errors.New("html tokenizer did not preserve the source byte stream")
)

// HTMLParser 将 HTML 可见文本 token 映射为可回溯到原始字节的结构块。
type HTMLParser struct {
	options    Options
	configHash string
	configErr  error
}

// NewHTMLParser 创建使用默认安全上限的 HTML Parser。
func NewHTMLParser() *HTMLParser {
	return NewHTMLParserWithOptions(Options{})
}

// NewHTMLParserWithOptions 创建使用显式输入上限的 HTML Parser。
func NewHTMLParserWithOptions(options Options) *HTMLParser {
	normalized, err := normalizeOptions(options)
	return &HTMLParser{
		options:    normalized,
		configHash: htmlConfigHash(normalized),
		configErr:  err,
	}
}

var _ domain.Parser = (*HTMLParser)(nil)

// Supports 判断当前 Parser 是否接受 text/html。
func (p *HTMLParser) Supports(mediaType string) bool {
	return p != nil && strings.EqualFold(strings.TrimSpace(mediaType), "text/html")
}

// Version 返回不可变的 HTML Parser 版本。
func (p *HTMLParser) Version() string { return HTMLParserVersion }

// Descriptor 返回解析开始前即可持久化的版本契约。
func (p *HTMLParser) Descriptor() domain.ParserDescriptor {
	descriptor := domain.ParserDescriptor{
		ID:            HTMLParserID,
		Version:       HTMLParserVersion,
		SchemaVersion: ParseSchemaVersion,
	}
	if p != nil {
		descriptor.ConfigHash = p.configHash
	}
	return descriptor
}

// Parse 只输出可见文本 token；每个 Content 都是 ImmutableBytes 的精确连续切片。
func (p *HTMLParser) Parse(ctx context.Context, input domain.SourceInput) (domain.ParsedDocument, error) {
	if p == nil {
		return domain.ParsedDocument{}, &domain.ParserError{Code: "PARSER_UNAVAILABLE", Cause: domain.ErrParserUnavailable}
	}
	if err := validateContextAndInput(ctx, p.configErr, p.Supports(input.MediaType), input.ImmutableBytes, p.options); err != nil {
		return domain.ParsedDocument{}, err
	}

	source := input.ImmutableBytes
	warnings := make([]domain.Warning, 0, 1)
	startOffset := 0
	if hasBOM(source) {
		startOffset = 3
		warnings = append(warnings, domain.Warning{
			Code:      "UTF8_BOM_PRESENT",
			Message:   "UTF-8 BOM was accepted and excluded from normalized content",
			StartByte: 0,
			EndByte:   int64(startOffset),
		})
	}
	if startOffset == len(source) {
		return domain.ParsedDocument{}, parserError("SOURCE_CONTENT_EMPTY", domain.ErrEmptyContent)
	}

	state := htmlParseState{
		source: source,
		lines:  buildLines(source),
		blocks: make([]domain.ParsedBlock, 0, 32),
	}
	tokenizer := html.NewTokenizer(bytes.NewReader(source[startOffset:]))
	offset := startOffset
	for {
		if err := ctx.Err(); err != nil {
			return domain.ParsedDocument{}, err
		}
		tokenType := tokenizer.Next()
		raw := tokenizer.Raw()
		tokenStart := offset
		tokenEnd := tokenStart + len(raw)
		if tokenEnd > len(source) || !bytes.Equal(raw, source[tokenStart:tokenEnd]) {
			return domain.ParsedDocument{}, parserError("SOURCE_HTML_TOKEN_STREAM_INVALID", errHTMLTokenStream)
		}
		offset = tokenEnd

		switch tokenType {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != nil && !errors.Is(err, io.EOF) {
				return domain.ParsedDocument{}, parserError("SOURCE_HTML_PARSE_FAILED", err)
			}
			if offset != len(source) {
				return domain.ParsedDocument{}, parserError("SOURCE_HTML_TOKEN_STREAM_INVALID", errHTMLTokenStream)
			}
			if err := state.finishHeading(); err != nil {
				return domain.ParsedDocument{}, err
			}
			return domain.ParsedDocument{
				ParserID:              HTMLParserID,
				ParserVersion:         HTMLParserVersion,
				ParserConfigHash:      p.configHash,
				SchemaVersion:         ParseSchemaVersion,
				NormalizedContentHash: normalizedHash(source[startOffset:]),
				Blocks:                state.blocks,
				Warnings:              warnings,
			}, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if err := state.startTag(normalizeHTMLTag(name), tokenType == html.SelfClosingTagToken); err != nil {
				return domain.ParsedDocument{}, err
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if err := state.endTag(normalizeHTMLTag(name)); err != nil {
				return domain.ParsedDocument{}, err
			}
		case html.TextToken:
			if state.ignoring() {
				continue
			}
			visibleText := string(tokenizer.Text())
			if strings.TrimSpace(visibleText) == "" {
				continue
			}
			if err := state.appendText(tokenStart, tokenEnd, visibleText); err != nil {
				return domain.ParsedDocument{}, err
			}
		}
	}
}

type htmlHeadingState struct {
	tag        string
	level      int
	blockStart int
	text       strings.Builder
}

type htmlParseState struct {
	source       []byte
	lines        []sourceLine
	blocks       []domain.ParsedBlock
	openTags     []string
	ignoredTags  []string
	headingPath  []string
	activeHeader *htmlHeadingState
}

func (s *htmlParseState) ignoring() bool { return len(s.ignoredTags) > 0 }

func (s *htmlParseState) startTag(tag string, selfClosing bool) error {
	if tag == "" {
		return nil
	}
	if s.ignoring() {
		if ignoredHTMLTag(tag) {
			return s.pushIgnored(tag)
		}
		return nil
	}
	if ignoredHTMLTag(tag) {
		// HTML 把 script/style 等非 void 元素的 `/>` 仍视为开始标签；保守地
		// 等待明确结束标签，避免恶意输入把后续 raw text 暴露为正文。
		return s.pushIgnored(tag)
	}
	level := htmlHeadingLevel(tag)
	if s.activeHeader != nil && level == 0 && htmlHeadingBoundaryTag(tag) {
		if err := s.finishHeading(); err != nil {
			return err
		}
		s.dropOpenHeading()
	}
	if selfClosing || voidHTMLTag(tag) {
		return nil
	}
	if level > 0 {
		if err := s.finishHeading(); err != nil {
			return err
		}
		s.dropOpenHeading()
		s.activeHeader = &htmlHeadingState{tag: tag, level: level, blockStart: len(s.blocks)}
	}
	if len(s.openTags) >= maxHTMLNesting {
		return parserError("SOURCE_HTML_NESTING_TOO_DEEP", errHTMLNestingTooDeep)
	}
	s.openTags = append(s.openTags, tag)
	return nil
}

func (s *htmlParseState) endTag(tag string) error {
	if tag == "" {
		return nil
	}
	if s.ignoring() {
		last := len(s.ignoredTags) - 1
		if s.ignoredTags[last] == tag {
			s.ignoredTags = s.ignoredTags[:last]
		}
		return nil
	}
	if s.activeHeader != nil && s.activeHeader.tag == tag {
		if err := s.finishHeading(); err != nil {
			return err
		}
	}
	s.popOpenTag(tag)
	return nil
}

func (s *htmlParseState) pushIgnored(tag string) error {
	if len(s.ignoredTags) >= maxHTMLNesting {
		return parserError("SOURCE_HTML_NESTING_TOO_DEEP", errHTMLNestingTooDeep)
	}
	s.ignoredTags = append(s.ignoredTags, tag)
	return nil
}

func (s *htmlParseState) appendText(start, end int, visibleText string) error {
	if start < 0 || end <= start || end > len(s.source) {
		return parserError("SOURCE_HTML_TOKEN_STREAM_INVALID", errHTMLTokenStream)
	}
	if len(s.blocks) >= maxHTMLBlocks {
		return parserError("SOURCE_HTML_BLOCK_LIMIT_EXCEEDED", errHTMLTooManyBlocks)
	}

	spanType, tag, atomic := s.currentTextStructure()
	selector := map[string]string{"format": "html"}
	if tag != "" {
		selector["tag"] = tag
	}
	path := clonePath(s.headingPath)
	if s.activeHeader != nil {
		spanType = "heading"
		tag = s.activeHeader.tag
		selector["tag"] = tag
		selector["level"] = strconv.Itoa(s.activeHeader.level)
		path = nil
		if err := appendHTMLHeadingText(&s.activeHeader.text, visibleText); err != nil {
			return err
		}
	}

	startLine := lineNumberAt(s.lines, start) + 1
	endLine := lineNumberAt(s.lines, end-1) + 1
	s.blocks = append(s.blocks, domain.ParsedBlock{
		SpanType:    spanType,
		StartLine:   int32(startLine),
		EndLine:     int32(endLine),
		StartByte:   int64(start),
		EndByte:     int64(end),
		HeadingPath: path,
		Selector:    selector,
		Content:     string(s.source[start:end]),
		Atomic:      atomic,
	})
	return nil
}

func (s *htmlParseState) finishHeading() error {
	if s.activeHeader == nil {
		return nil
	}
	heading := s.activeHeader
	s.activeHeader = nil
	title := strings.TrimSpace(heading.text.String())
	if title == "" {
		return nil
	}
	s.headingPath = updateHeadingPath(s.headingPath, heading.level, title)
	for index := heading.blockStart; index < len(s.blocks); index++ {
		if s.blocks[index].SpanType == "heading" {
			s.blocks[index].HeadingPath = clonePath(s.headingPath)
		}
	}
	return nil
}

func (s *htmlParseState) currentTextStructure() (string, string, bool) {
	for index := len(s.openTags) - 1; index >= 0; index-- {
		tag := s.openTags[index]
		switch tag {
		case "pre", "code":
			return "code_block", tag, true
		case "blockquote":
			return "blockquote", tag, false
		case "li", "ul", "ol":
			return "list", tag, false
		case "td", "th":
			return "table_cell", tag, false
		case "p", "dt", "dd", "figcaption", "caption", "article", "section", "main", "aside", "nav", "header", "footer", "title":
			return "paragraph", tag, false
		}
	}
	if len(s.openTags) > 0 {
		return "paragraph", s.openTags[len(s.openTags)-1], false
	}
	return "paragraph", "", false
}

func (s *htmlParseState) popOpenTag(tag string) {
	for index := len(s.openTags) - 1; index >= 0; index-- {
		if s.openTags[index] == tag {
			s.openTags = s.openTags[:index]
			return
		}
	}
}

func (s *htmlParseState) dropOpenHeading() {
	for index := len(s.openTags) - 1; index >= 0; index-- {
		if htmlHeadingLevel(s.openTags[index]) > 0 {
			s.openTags = s.openTags[:index]
			return
		}
	}
}

func appendHTMLHeadingText(builder *strings.Builder, value string) error {
	normalized := strings.Join(strings.Fields(value), " ")
	if normalized == "" {
		return nil
	}
	additional := len(normalized)
	if builder.Len() > 0 {
		additional++
	}
	if builder.Len()+additional > maxHTMLHeadingBytes {
		return parserError("SOURCE_HTML_HEADING_TOO_LARGE", errHTMLHeadingTooLong)
	}
	if builder.Len() > 0 {
		builder.WriteByte(' ')
	}
	builder.WriteString(normalized)
	return nil
}

func normalizeHTMLTag(value []byte) string {
	return strings.ToLower(strings.TrimSpace(string(value)))
}

func ignoredHTMLTag(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "template":
		return true
	default:
		return false
	}
}

func voidHTMLTag(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func htmlHeadingLevel(tag string) int {
	if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
		return int(tag[1] - '0')
	}
	return 0
}

func htmlHeadingBoundaryTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "div", "dl", "fieldset", "figure", "footer", "form", "header", "hr", "main", "nav", "ol", "p", "pre", "section", "table", "ul":
		return true
	default:
		return false
	}
}

func htmlConfigHash(options Options) string {
	contract := fmt.Sprintf(
		"x-net-html-v0.56.0|visible-text-token-range-v1|ignored:script,style,noscript,template|max_bytes:%d|max_nesting:%d|max_blocks:%d|max_heading_bytes:%d",
		options.MaxBytes,
		maxHTMLNesting,
		maxHTMLBlocks,
		maxHTMLHeadingBytes,
	)
	digest := sha256.Sum256([]byte(contract))
	return hex.EncodeToString(digest[:])
}
