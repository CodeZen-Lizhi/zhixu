package manuscript

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// SourceReviewParagraphs 使用精确的 UTF-8 偏移和绝对出现位置身份；相同段落仍分别识别，包括嵌套文字。
func (Mapper) SourceReviewParagraphs(label, content string) ([]domain.SynthesisSourceReviewParagraph, error) {
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 || len(content) > domain.MaxSynthesisManuscriptBytes {
		return nil, fmt.Errorf("SYNTHESIS_SOURCE_REVIEW_TEXT_INVALID")
	}
	source := []byte(content)
	doc := goldmark.New().Parser().Parse(text.NewReader(source))
	out := []domain.SynthesisSourceReviewParagraph{}
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || (n.Kind() != ast.KindParagraph && n.Kind() != ast.KindTextBlock) {
			return ast.WalkContinue, nil
		}
		lines := n.Lines()
		if lines.Len() == 0 {
			return ast.WalkContinue, nil
		}
		first, last := lines.At(0), lines.At(lines.Len()-1)
		start, end := first.Start, last.Stop
		for end > start && (source[end-1] == '\n' || source[end-1] == '\r') {
			end--
		}
		if start < 0 || end > len(source) || start >= end {
			return ast.WalkStop, fmt.Errorf("SYNTHESIS_SOURCE_REVIEW_TEXT_INVALID")
		}
		// AST 排除 HTML 和代码块。依据必须支持包含行内条件的整段范围；不接受生成的子串偏移。
		out = append(out, domain.SynthesisSourceReviewParagraph{Label: fmt.Sprintf("%s/P%05d", label, len(out)+1), Ordinal: len(out) + 1, StartByte: start, EndByte: end, Hash: fmt.Sprintf("%x", sha256.Sum256(source[start:end]))})
		if len(out) > 4096 {
			return ast.WalkStop, fmt.Errorf("SYNTHESIS_SOURCE_REVIEW_INPUT_TOO_LARGE")
		}
		return ast.WalkContinue, nil
	})
	return out, err
}
