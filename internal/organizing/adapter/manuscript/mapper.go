// manuscript 包以保守方式映射完整手稿的来源关系。
package manuscript

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Mapper 要求机器内容精确匹配，且边界已由 AST 证明。列表结构边界不足以证明人工改写只影响局部语义；在独立证明范围契约前，任何人工修改都需要结合全文评审。原始手稿字节保持不变。
type Mapper struct{}

var _ domain.SynthesisManuscriptMapper = Mapper{}

func (Mapper) Assess(machine domain.SynthesisManuscriptMachine, content string) (domain.SynthesisManuscriptAssessment, error) {
	out := domain.SynthesisManuscriptAssessment{
		Mappings:    []domain.SynthesisManuscriptMapping{},
		ReviewItems: []domain.SynthesisManuscriptReview{},
	}
	if err := machine.Validate(); err != nil {
		return out, err
	}
	if !utf8.ValidString(content) || len(content) > domain.MaxSynthesisManuscriptBytes {
		return out, fmt.Errorf("manuscript must be bounded UTF-8")
	}
	expected, err := domain.RenderSynthesisMarkdown(machine.WorkspaceID, machine.NoteID, machine.MachineTitle, machine.MachineItems)
	if err != nil {
		return out, err
	}
	source := []byte(content)
	// 每次调用独享解析器及其可变解析上下文。
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	expectedDocument := goldmark.New().Parser().Parse(text.NewReader([]byte(expected)))
	type blockRange struct {
		item                                   domain.SynthesisItem
		start, end, originalStart, originalEnd int
	}
	blocks := make([]blockRange, 0, len(machine.MachineItems))
	// 同时统计保留标记：未知、嵌套、格式错误和重复标记均不得藏在替换范围内，包括列表中的代码。
	structureOK := strings.Count(content, "synthesis-item:") == 2*len(machine.MachineItems)
	for _, item := range machine.MachineItems {
		startMarker := "<!-- synthesis-item:" + string(item.ID) + " -->\n"
		endMarker := "<!-- /synthesis-item:" + string(item.ID) + " -->\n"
		start, end := strings.Index(content, startMarker), strings.Index(content, endMarker)
		originalStart, originalEnd := strings.Index(expected, startMarker), strings.Index(expected, endMarker)
		if strings.Count(content, startMarker) != 1 || strings.Count(content, endMarker) != 1 ||
			!completeListBlock(document, source, start, end, startMarker, endMarker) ||
			!completeListBlock(expectedDocument, []byte(expected), originalStart, originalEnd, startMarker, endMarker) {
			structureOK = false
		}
		blocks = append(blocks, blockRange{item, start, end + len(endMarker), originalStart, originalEnd + len(endMarker)})
	}
	// 机器条目顺序可能不同于渲染后的章节顺序。
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].originalStart < blocks[j].originalStart })
	if structureOK {
		var skeleton strings.Builder
		previousEnd := 0
		for _, block := range blocks {
			if block.start < previousEnd {
				structureOK = false
				break
			}
			skeleton.WriteString(content[previousEnd:block.start])
			skeleton.WriteString(expected[block.originalStart:block.originalEnd])
			previousEnd = block.end
		}
		if structureOK {
			skeleton.WriteString(content[previousEnd:])
			structureOK = skeleton.String() == expected
		}
	}
	// 即使骨架匹配，也无法限制人工文字的语义影响范围。
	contextOK := structureOK && content == expected
	out.ContextReviewRequired = !contextOK
	excluded := make(map[string]bool, len(machine.IneligibleItemIDs))
	for _, id := range machine.IneligibleItemIDs {
		excluded[string(id)] = true
	}
	for _, block := range blocks {
		reason := ""
		switch {
		case excluded[string(block.item.ID)]:
			reason = domain.ManuscriptPreviouslyUntrusted
		case !contextOK:
			reason = domain.ManuscriptContextReview
		case content[block.start:block.end] != expected[block.originalStart:block.originalEnd]:
			reason = domain.ManuscriptChangedOrMissing
		}
		if reason != "" {
			out.ReviewItems = append(out.ReviewItems, domain.SynthesisManuscriptReview{ItemID: block.item.ID, Reason: reason})
			continue
		}
		sum := sha256.Sum256(source[block.start:block.end])
		out.Mappings = append(out.Mappings, domain.SynthesisManuscriptMapping{ItemID: block.item.ID, Start: block.start, End: block.end, BlockHash: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out.Mappings, func(i, j int) bool { return out.Mappings[i].Start < out.Mappings[j].Start })
	return out, nil
}

func topLevelComment(document ast.Node, source []byte, start int, marker string) ast.Node {
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		block, ok := node.(*ast.HTMLBlock)
		if !ok || block.Lines().Len() != 1 || block.HasClosure() {
			continue
		}
		line := block.Lines().At(0)
		if line.Start == start && string(line.Value(source)) == marker {
			return node
		}
	}
	return nil
}

// 仅接受顶层注释、列表、注释这一严格形态。更复杂的内部 Markdown 保持原样，但须进行全文评审。
func completeListBlock(document ast.Node, source []byte, start, end int, startMarker, endMarker string) bool {
	if start < 0 || end <= start {
		return false
	}
	opening := topLevelComment(document, source, start, startMarker)
	closing := topLevelComment(document, source, end, endMarker)
	return opening != nil && closing != nil && opening.NextSibling() != nil &&
		opening.NextSibling().Kind() == ast.KindList && opening.NextSibling().NextSibling() == closing
}
