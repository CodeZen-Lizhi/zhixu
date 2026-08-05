package parser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	pdfreader "github.com/ledongthuc/pdf"
)

const (
	// PDFParserID 是 PDF 文本投影持久化使用的稳定解析器身份。
	PDFParserID = "ledongthuc_pdf"
	// PDFParserVersion 冻结依赖版本与当前整篇文本提取语义。
	PDFParserVersion = "ledongthuc-pdf-5959a4027728-plain-text-v1"

	maxPDFPages            = 10_000
	maxPDFDerivedTextBytes = 2 * 1024 * 1024
)

var (
	errPDFSignature       = errors.New("source does not have a PDF signature")
	errPDFPageLimit       = errors.New("PDF page count exceeds parser safety limit")
	errPDFTextUnavailable = errors.New("PDF has no extractable text")
	errPDFTextTooLarge    = errors.New("PDF extracted text exceeds parser safety limit")
)

// PDFParser 从不可变 PDF 原件生成确定性的派生文本证据。
type PDFParser struct {
	options    Options
	configHash string
	configErr  error
}

// NewPDFParser 创建使用默认安全上限的 PDF Parser。
func NewPDFParser() *PDFParser { return NewPDFParserWithOptions(Options{}) }

// NewPDFParserWithOptions 创建使用显式原件大小上限的 PDF Parser。
func NewPDFParserWithOptions(options Options) *PDFParser {
	normalized, err := normalizeOptions(options)
	return &PDFParser{
		options:    normalized,
		configHash: pdfConfigHash(normalized),
		configErr:  err,
	}
}

var _ domain.Parser = (*PDFParser)(nil)

// Supports 判断当前 Parser 是否接受 application/pdf。
func (p *PDFParser) Supports(mediaType string) bool {
	return p != nil && strings.EqualFold(strings.TrimSpace(mediaType), "application/pdf")
}

// Version 返回不可变的 PDF Parser 版本。
func (p *PDFParser) Version() string { return PDFParserVersion }

// Descriptor 返回解析开始前即可持久化的版本契约。
func (p *PDFParser) Descriptor() domain.ParserDescriptor {
	descriptor := domain.ParserDescriptor{ID: PDFParserID, Version: PDFParserVersion, SchemaVersion: ParseSchemaVersion}
	if p != nil {
		descriptor.ConfigHash = p.configHash
	}
	return descriptor
}

// Parse 生成一个绑定完整 PDF 原件、带页范围选择器的派生文本块。
func (p *PDFParser) Parse(ctx context.Context, input domain.SourceInput) (domain.ParsedDocument, error) {
	if p == nil {
		return domain.ParsedDocument{}, &domain.ParserError{Code: "PARSER_UNAVAILABLE", Cause: domain.ErrParserUnavailable}
	}
	if p.configErr != nil {
		return domain.ParsedDocument{}, parserError("PARSER_CONFIG_INVALID", p.configErr)
	}
	if !p.Supports(input.MediaType) {
		return domain.ParsedDocument{}, parserError("PARSER_MEDIA_TYPE_UNSUPPORTED", domain.ErrUnsupportedMediaType)
	}
	if err := ctx.Err(); err != nil {
		return domain.ParsedDocument{}, err
	}
	if len(input.ImmutableBytes) == 0 {
		return domain.ParsedDocument{}, parserError("SOURCE_CONTENT_EMPTY", domain.ErrEmptyContent)
	}
	if p.options.MaxBytes > 0 && int64(len(input.ImmutableBytes)) > p.options.MaxBytes {
		return domain.ParsedDocument{}, parserError("SOURCE_CONTENT_TOO_LARGE", errSourceTooLarge)
	}
	if !bytes.HasPrefix(input.ImmutableBytes, []byte("%PDF-")) {
		return domain.ParsedDocument{}, parserError("SOURCE_PDF_SIGNATURE_INVALID", errPDFSignature)
	}

	content, pages, err := extractPDFText(ctx, input.ImmutableBytes)
	if err != nil {
		return domain.ParsedDocument{}, err
	}
	lineCount := int32(strings.Count(content, "\n") + 1)
	return domain.ParsedDocument{
		ParserID: PDFParserID, ParserVersion: PDFParserVersion, ParserConfigHash: p.configHash,
		SchemaVersion: ParseSchemaVersion, NormalizedContentHash: normalizedHash([]byte(content)),
		Blocks: []domain.ParsedBlock{{
			SpanType: "document", StartLine: 1, EndLine: lineCount,
			StartByte: 0, EndByte: int64(len(input.ImmutableBytes)), Content: content,
			Selector:     map[string]string{"format": "pdf", "page_start": "1", "page_end": strconv.Itoa(pages)},
			EvidenceKind: domain.EvidenceDerivedText,
		}},
		Warnings: []domain.Warning{{
			Code: "PDF_TEXT_DERIVED", Message: "PDF 正文由不可变原件确定性提取，证据位置使用页范围选择器",
			StartByte: 0, EndByte: int64(len(input.ImmutableBytes)),
		}},
	}, nil
}

func extractPDFText(ctx context.Context, source []byte) (text string, pages int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text, pages = "", 0
			err = parserError("SOURCE_PDF_PARSE_FAILED", fmt.Errorf("pdf reader panic: %v", recovered))
		}
	}()
	reader, err := pdfreader.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		return "", 0, parserError("SOURCE_PDF_PARSE_FAILED", err)
	}
	pages = reader.NumPage()
	if pages < 1 {
		return "", 0, parserError("SOURCE_PDF_TEXT_UNAVAILABLE", errPDFTextUnavailable)
	}
	if pages > maxPDFPages {
		return "", 0, parserError("SOURCE_PDF_PAGE_LIMIT_EXCEEDED", errPDFPageLimit)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	fonts := make(map[string]*pdfreader.Font)
	text, err = collectPDFPageText(ctx, pages, func(pageNumber int) (string, error) {
		page := reader.Page(pageNumber)
		for _, name := range page.Fonts() {
			if _, exists := fonts[name]; exists {
				continue
			}
			font := page.Font(name)
			fonts[name] = &font
		}
		return page.GetPlainText(fonts)
	})
	if err != nil {
		return "", 0, err
	}
	if text == "" {
		return "", 0, parserError("SOURCE_PDF_TEXT_UNAVAILABLE", errPDFTextUnavailable)
	}
	return text, pages, nil
}

func collectPDFPageText(ctx context.Context, pages int, extract func(int) (string, error)) (string, error) {
	var content strings.Builder
	for pageNumber := 1; pageNumber <= pages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		pageText, err := extract(pageNumber)
		if err != nil {
			return "", parserError("SOURCE_PDF_PARSE_FAILED", err)
		}
		if len(pageText) > maxPDFDerivedTextBytes-content.Len() {
			return "", parserError("SOURCE_PDF_TEXT_TOO_LARGE", errPDFTextTooLarge)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !utf8.ValidString(pageText) {
			return "", parserError("SOURCE_PDF_TEXT_INVALID_UTF8", errInvalidUTF8)
		}
		content.WriteString(pageText)
	}
	return strings.TrimSpace(content.String()), nil
}

func pdfConfigHash(options Options) string {
	value := fmt.Sprintf("%s|max_bytes=%d|max_pages=%d|max_derived_text_bytes=%d", PDFParserVersion, options.MaxBytes, maxPDFPages, maxPDFDerivedTextBytes)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
