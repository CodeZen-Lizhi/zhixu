package domain

import "context"

// ParserRegistry 选择支持指定 MIME 的 Parser Adapter。
type ParserRegistry interface {
	ParserFor(mediaType string) (Parser, error)
}

// ParseContent 在 Parser 端口上执行一次不可变内容解析。
func ParseContent(ctx context.Context, parser Parser, input SourceInput) (ParsedDocument, error) {
	if parser == nil {
		return ParsedDocument{}, &ParserError{Code: "PARSER_UNAVAILABLE", Cause: ErrParserUnavailable}
	}
	if err := ctx.Err(); err != nil {
		return ParsedDocument{}, err
	}
	if len(input.ImmutableBytes) == 0 {
		return ParsedDocument{}, &ParserError{Code: "SOURCE_CONTENT_EMPTY", Cause: ErrEmptyContent}
	}
	if !parser.Supports(input.MediaType) {
		return ParsedDocument{}, &ParserError{Code: "PARSER_MEDIA_TYPE_UNSUPPORTED", Cause: ErrUnsupportedMediaType}
	}
	// Parser 不得修改调用方的缓冲区；复制一次作为 Adapter 边界的不可变快照。
	input.ImmutableBytes = append([]byte(nil), input.ImmutableBytes...)
	return parser.Parse(ctx, input)
}

// ParserError 是 Parser Adapter 到 Application 层的稳定错误。
type ParserError struct {
	Code  string
	Cause error
}

func (e *ParserError) Error() string { return e.Code }
func (e *ParserError) Unwrap() error { return e.Cause }

// Parser 端口的基础错误。
var (
	ErrParserUnavailable    = errorString("parser unavailable")
	ErrEmptyContent         = errorString("source content is empty")
	ErrUnsupportedMediaType = errorString("media type is unsupported")
)

type errorString string

func (e errorString) Error() string { return string(e) }
