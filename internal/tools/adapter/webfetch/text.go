package webfetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"golang.org/x/net/html"
)

const maxIgnoredHTMLNesting = 256

type ignoredHTMLTag uint8

const (
	ignoredHTMLTagNone ignoredHTMLTag = iota
	ignoredHTMLTagScript
	ignoredHTMLTagStyle
	ignoredHTMLTagNoscript
	ignoredHTMLTagTemplate
)

func extractText(ctx context.Context, contentType string, body []byte, maxBytes int) (string, error) {
	if contentType == "text/html" {
		return extractHTMLText(ctx, body, maxBytes)
	}
	return normalizeText(ctx, body, maxBytes)
}

func extractHTMLText(ctx context.Context, body []byte, maxBytes int) (string, error) {
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	builder := boundedTextBuilder{maxBytes: maxBytes}
	// 合法页面通常只有极浅的忽略标签嵌套；固定 256 层既保留兼容性，
	// 又阻止 32 MiB 恶意正文把标签栈线性放大到数百 MiB。
	ignoredStack := make([]ignoredHTMLTag, 0, 8)
	for {
		if contextErr := classifyContextError(ctx); contextErr != nil {
			return "", contextErr
		}
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if contextErr := classifyContextError(ctx); contextErr != nil {
				return "", contextErr
			}
			if err := tokenizer.Err(); err != nil && !errors.Is(err, io.EOF) {
				return "", fetchError(foundation.ErrorInvalidInput, ErrorCodeBodyInvalid, false, errBodyInvalid)
			}
			return builder.String()
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			tagName := classifyIgnoredHTMLTag(name)
			if len(ignoredStack) > 0 {
				if tagName != ignoredHTMLTagNone {
					var err error
					ignoredStack, err = pushIgnoredHTMLTag(ignoredStack, tagName)
					if err != nil {
						return "", err
					}
				}
				continue
			}
			if tagName != ignoredHTMLTagNone {
				var err error
				ignoredStack, err = pushIgnoredHTMLTag(ignoredStack, tagName)
				if err != nil {
					return "", err
				}
			} else {
				builder.Separate()
			}
		case html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			tagName := classifyIgnoredHTMLTag(name)
			if tagName != ignoredHTMLTagNone {
				// HTML 对 script/style/template 等非 void 元素忽略自闭合标志；
				// 保守地视为开始标签，避免后续 raw-text 内容泄漏。
				var err error
				ignoredStack, err = pushIgnoredHTMLTag(ignoredStack, tagName)
				if err != nil {
					return "", err
				}
			} else if len(ignoredStack) == 0 {
				builder.Separate()
			}
		case html.EndTagToken:
			if len(ignoredStack) > 0 {
				name, _ := tokenizer.TagName()
				tagName := classifyIgnoredHTMLTag(name)
				last := len(ignoredStack) - 1
				if tagName == ignoredStack[last] {
					ignoredStack = ignoredStack[:last]
					if len(ignoredStack) == 0 {
						builder.Separate()
					}
				}
			} else {
				builder.Separate()
			}
		case html.TextToken:
			if len(ignoredStack) == 0 {
				if err := builder.Append(ctx, tokenizer.Text()); err != nil {
					return "", err
				}
			}
		}
	}
}

func normalizeText(ctx context.Context, body []byte, maxBytes int) (string, error) {
	builder := boundedTextBuilder{maxBytes: maxBytes}
	if err := builder.Append(ctx, body); err != nil {
		return "", err
	}
	if contextErr := classifyContextError(ctx); contextErr != nil {
		return "", contextErr
	}
	return builder.String()
}

func pushIgnoredHTMLTag(stack []ignoredHTMLTag, tag ignoredHTMLTag) ([]ignoredHTMLTag, error) {
	if len(stack) >= maxIgnoredHTMLNesting {
		return nil, fetchError(foundation.ErrorInvalidInput, ErrorCodeBodyInvalid, false, errBodyInvalid)
	}
	return append(stack, tag), nil
}

func classifyIgnoredHTMLTag(name []byte) ignoredHTMLTag {
	switch {
	case equalASCIIFold(name, "script"):
		return ignoredHTMLTagScript
	case equalASCIIFold(name, "style"):
		return ignoredHTMLTagStyle
	case equalASCIIFold(name, "noscript"):
		return ignoredHTMLTagNoscript
	case equalASCIIFold(name, "template"):
		return ignoredHTMLTagTemplate
	default:
		return ignoredHTMLTagNone
	}
}

func equalASCIIFold(value []byte, expected string) bool {
	if len(value) != len(expected) {
		return false
	}
	for index, character := range value {
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		if character != expected[index] {
			return false
		}
	}
	return true
}

type boundedTextBuilder struct {
	builder    strings.Builder
	maxBytes   int
	needsSpace bool
}

func (builder *boundedTextBuilder) Separate() {
	if builder.builder.Len() > 0 {
		builder.needsSpace = true
	}
}

func (builder *boundedTextBuilder) Append(ctx context.Context, value []byte) error {
	const contextCheckInterval = 4096
	processedSinceCheck := contextCheckInterval
	for len(value) > 0 {
		if processedSinceCheck >= contextCheckInterval {
			if contextErr := classifyContextError(ctx); contextErr != nil {
				return contextErr
			}
			processedSinceCheck = 0
		}
		runeValue, size := utf8.DecodeRune(value)
		value = value[size:]
		processedSinceCheck += size
		if unicode.IsSpace(runeValue) || unicode.IsControl(runeValue) {
			if builder.builder.Len() > 0 {
				builder.needsSpace = true
			}
			continue
		}
		additional := size
		if builder.needsSpace {
			additional++
		}
		if builder.builder.Len()+additional > builder.maxBytes {
			return fetchError(foundation.ErrorInvalidInput, ErrorCodeOutputTooLarge, false, errOutputTooLarge)
		}
		if builder.needsSpace {
			builder.builder.WriteByte(' ')
			builder.needsSpace = false
		}
		builder.builder.WriteRune(runeValue)
	}
	return nil
}

func (builder *boundedTextBuilder) String() (string, error) {
	return builder.builder.String(), nil
}
