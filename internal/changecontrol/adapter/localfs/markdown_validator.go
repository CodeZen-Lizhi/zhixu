package localfs

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/parser"
)

// MarkdownValidator 适配项目统一 Markdown Parser，避免 LocalFS 复制解析逻辑。
type MarkdownValidator struct {
	parser ingestiondomain.Parser
}

var _ domain.ContentValidator = (*MarkdownValidator)(nil)

// NewMarkdownValidator 创建使用指定 Parser 的 Markdown 校验器。
// Parser 由 Composition Root 注入，便于使用正式 Adapter 或契约测试 Fake。
func NewMarkdownValidator(markdownParser ingestiondomain.Parser) (*MarkdownValidator, error) {
	if markdownParser == nil {
		return nil, foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			"WRITEBACK_CONTENT_VALIDATOR_UNAVAILABLE",
			false,
			ingestiondomain.ErrParserUnavailable,
		)
	}
	return &MarkdownValidator{parser: markdownParser}, nil
}

// NewDefaultMarkdownValidator 创建使用项目正式 Markdown Parser 的校验器。
func NewDefaultMarkdownValidator() (*MarkdownValidator, error) {
	return NewMarkdownValidator(parser.NewMarkdownParser())
}

// Validate 验证 UTF-8 Markdown 内容，并保留 Parser 的稳定错误码。
func (v *MarkdownValidator) Validate(ctx context.Context, content []byte) error {
	if v == nil || v.parser == nil {
		return foundation.NewError(
			foundation.ErrorDependencyUnavailable,
			"WRITEBACK_CONTENT_VALIDATOR_UNAVAILABLE",
			false,
			ingestiondomain.ErrParserUnavailable,
		)
	}
	if ctx == nil {
		return foundation.NewError(
			foundation.ErrorInvalidInput,
			"WRITEBACK_CONTENT_VALIDATION_CONTEXT_INVALID",
			false,
			errors.New("validation context is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return classifyMarkdownValidationError(err)
	}

	// ParseContent 负责空内容、Parser Supports 检查和输入副本；Parser
	// 仍是唯一负责 UTF-8、二进制控制字节和 Markdown 语法的实现。
	_, err := ingestiondomain.ParseContent(ctx, v.parser, ingestiondomain.SourceInput{
		MediaType:      "text/markdown",
		ImmutableBytes: content,
	})
	return classifyMarkdownValidationError(err)
}

// classifyMarkdownValidationError 将 Parser 错误映射到项目稳定错误分类。
// 原始 ParserError 保留在 Cause 中，调用方仍可使用 errors.Is/errors.As。
func classifyMarkdownValidationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(
			foundation.ErrorNonRetryableFailure,
			"WRITEBACK_CONTENT_VALIDATION_CANCELLED",
			false,
			err,
		)
	}

	var parserErr *ingestiondomain.ParserError
	if errors.As(err, &parserErr) {
		code := strings.TrimSpace(parserErr.Code)
		if code == "" {
			code = "WRITEBACK_CONTENT_VALIDATION_FAILED"
		}
		switch {
		case code == "PARSER_UNAVAILABLE":
			return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
		case strings.HasPrefix(code, "SOURCE_"), code == "PARSER_MEDIA_TYPE_UNSUPPORTED":
			return foundation.NewError(foundation.ErrorInvalidInput, code, false, err)
		case code == "PARSER_CONFIGURATION_INVALID":
			return foundation.NewError(foundation.ErrorDependencyUnavailable, code, false, err)
		default:
			return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
		}
	}

	switch {
	case errors.Is(err, ingestiondomain.ErrUnsupportedMediaType):
		return foundation.NewError(foundation.ErrorInvalidInput, "PARSER_MEDIA_TYPE_UNSUPPORTED", false, err)
	case errors.Is(err, ingestiondomain.ErrEmptyContent):
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_CONTENT_EMPTY", false, err)
	default:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, "WRITEBACK_CONTENT_VALIDATION_FAILED", false, err)
	}
}
