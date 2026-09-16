package manuscript_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func mappingFixture(t *testing.T) (domain.SynthesisManuscriptMachine, string) {
	t.Helper()
	machine := domain.SynthesisManuscriptMachine{WorkspaceID: mappingID(1), NoteID: mappingID(2), MachineTitle: "中文标题", MachineItems: []domain.SynthesisItem{
		{ID: mappingID(3), Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "原问题"}},
		{ID: mappingID(4), Kind: domain.SynthesisGapItem, Gap: &domain.SynthesisGapContent{Question: "未受影响问题"}},
	}}
	rendered, err := domain.RenderSynthesisMarkdown(machine.WorkspaceID, machine.NoteID, machine.MachineTitle, machine.MachineItems)
	if err != nil {
		t.Fatal(err)
	}
	return machine, rendered
}
func mappingID(n int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
}

func TestMapperUnchangedBlockByteOffsets(t *testing.T) {
	machine, content := mappingFixture(t)
	value, err := domain.NewSynthesisManuscript(machine, content, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Assessment.Mappings) != 2 || value.Assessment.ContextReviewRequired {
		t.Fatal("original mappings lost")
	}
	mapped := value.Assessment.Mappings[1]
	expectedStart := strings.Index(content, "<!-- synthesis-item:"+string(mappingID(4))+" -->\n")
	block := content[expectedStart:]
	if expectedStart == utf8.RuneCountInString(content[:expectedStart]) {
		t.Fatal("fixture does not distinguish byte offsets")
	}
	if mapped.Start != expectedStart || mapped.End != len(content) || mapped.BlockHash != fmt.Sprintf("%x", sha256.Sum256([]byte(block))) {
		t.Fatalf("wrong exact mapping: %+v", mapped)
	}
}

func TestMapperRejectsUnprovenContextAndMarkerStructure(t *testing.T) {
	machine, original := mappingFixture(t)
	first := strings.Index(original, "<!-- synthesis-item:"+string(mappingID(3)))
	second := strings.Index(original, "<!-- synthesis-item:"+string(mappingID(4)))
	endMarker := "<!-- /synthesis-item:" + string(mappingID(3)) + " -->\n"
	firstEnd := strings.Index(original, endMarker) + len(endMarker)
	cases := map[string]string{
		"local rewrite":                  strings.Replace(original, "原问题", "局部改写增加字节🙂", 1),
		"internal annotation":            strings.Replace(original, "原问题", "原问题\n  - 内部人工批注", 1),
		"cross-item prose inside marker": strings.Replace(original, "原问题", "本文所有后续条目均为虚构示例，不是待确认的真实问题。", 1),
		"cross-item nested list":         strings.Replace(original, "原问题", "原问题\n  - 本文所有后续条目均为虚构示例，不是待确认的真实问题。", 1),
		"outside context":                original + "\n以上结论无效。\n",
		"changed heading":                strings.Replace(original, "中文标题", "另一个标题", 1),
		"deleted block":                  original[:first] + original[firstEnd:],
		"reordered blocks":               original[:first] + original[second:] + "\n" + original[first:second],
		"duplicate block":                original + original[first:firstEnd],
		"unknown marker":                 strings.Replace(original, "原问题", "原问题\n  <!-- synthesis-item:unknown -->", 1),
		"nested known marker":            strings.Replace(original, "原问题", "原问题\n  <!-- synthesis-item:"+string(mappingID(4))+" -->", 1),
		"broken marker":                  strings.Replace(original, endMarker, "<!-- broken -->\n", 1),
		"fenced markers":                 "```markdown\n" + original + "```\n",
		"indented markers":               "    " + strings.ReplaceAll(original, "\n", "\n    "),
		"quoted markers":                 "> " + strings.ReplaceAll(original, "\n", "\n> "),
		"complex block":                  strings.Replace(original, "- 待确认：原问题", "人工段落", 1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			value, err := domain.NewSynthesisManuscript(machine, content, manuscript.Mapper{})
			if err != nil {
				t.Fatal(err)
			}
			if value.FullContent != content || !value.Assessment.ContextReviewRequired || len(value.Assessment.Mappings) != 0 {
				t.Fatal("unproven context mapped or original bytes lost")
			}
			if len(value.Assessment.ReviewItems) != len(machine.MachineItems) {
				t.Fatal("incomplete review")
			}
			for _, review := range value.Assessment.ReviewItems {
				if review.Reason != domain.ManuscriptContextReview {
					t.Fatalf("wrong global review: %+v", review)
				}
			}
		})
	}
}

func TestMapperPreservesExclusionAndRejectsRehashedClientMapping(t *testing.T) {
	machine, original := mappingFixture(t)
	machine.IneligibleItemIDs = []foundation.ID{mappingID(3)}
	value, err := domain.NewSynthesisManuscript(machine, original, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := value.TrustedItems(machine, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != mappingID(4) || value.Assessment.ReviewItems[0].Reason != domain.ManuscriptPreviouslyUntrusted {
		t.Fatal("restoring original bytes resurrected excluded item")
	}
	machine.IneligibleItemIDs = nil
	value, err = domain.NewSynthesisManuscript(machine, "人工正文", manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	value.Assessment.Mappings = []domain.SynthesisManuscriptMapping{{ItemID: mappingID(3), Start: 0, End: len(value.FullContent), BlockHash: fmt.Sprintf("%x", sha256.Sum256([]byte(value.FullContent)))}}
	value.Assessment.ReviewItems = []domain.SynthesisManuscriptReview{{ItemID: mappingID(4), Reason: domain.ManuscriptContextReview}}
	value.Hash = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	value.Hash = fmt.Sprintf("%x", sha256.Sum256(encoded))
	if err := value.ValidateIntegrity(); err != nil {
		t.Fatalf("fixture must be internally consistent: %v", err)
	}
	if value.Validate(machine, manuscript.Mapper{}) == nil {
		t.Fatal("client rehash bypassed AST proof")
	}
}

// 所属模块必须提供继承的墓碑集合并集。本测试验证条目缺失时，领域存储、校验及映射器仍保留该集合；应用层预览流程须另行验证所属模块的继承行为。
func TestMapperTombstoneSurvivesRemovalAndReintroduction(t *testing.T) {
	machine, _ := mappingFixture(t)
	originalItems := machine.MachineItems
	machine.IneligibleItemIDs = []foundation.ID{mappingID(3)}
	for generation, items := range [][]domain.SynthesisItem{originalItems, originalItems[1:], originalItems} {
		machine.MachineItems = items
		content, err := domain.RenderSynthesisMarkdown(machine.WorkspaceID, machine.NoteID, machine.MachineTitle, items)
		if err != nil {
			t.Fatal(err)
		}
		value, err := domain.NewSynthesisManuscript(machine, content, manuscript.Mapper{})
		if err != nil {
			t.Fatalf("generation %d: %v", generation, err)
		}
		// 按持久化结构往返转换时，必须保留缺失条目的 ID，不能只保留当前评审。
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var restored domain.SynthesisManuscript
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		if err := restored.ValidateIntegrity(); err != nil {
			t.Fatal(err)
		}
		trusted, err := restored.TrustedItems(machine, manuscript.Mapper{})
		if err != nil {
			t.Fatal(err)
		}
		if len(trusted) != 1 || trusted[0].ID != mappingID(4) || len(restored.Machine.IneligibleItemIDs) != 1 || restored.Machine.IneligibleItemIDs[0] != mappingID(3) {
			t.Fatalf("tombstone lost at generation %d", generation)
		}
		machine.IneligibleItemIDs = restored.Machine.IneligibleItemIDs
	}
	for _, ids := range [][]foundation.ID{{"invalid"}, {mappingID(3), mappingID(3)}} {
		machine.IneligibleItemIDs = ids
		if machine.Validate() == nil {
			t.Fatal("invalid/duplicate tombstone accepted")
		}
	}
}

func TestManuscriptRejectsNULAtCreationAndIntegrityBoundary(t *testing.T) {
	machine, _ := mappingFixture(t)
	for _, content := range []string{"\x00正文", "正\x00文", "正文\x00"} {
		if _, err := domain.NewSynthesisManuscript(machine, content, manuscript.Mapper{}); err == nil {
			t.Fatal("NUL accepted at creation")
		}
		value, err := domain.NewSynthesisManuscript(machine, "人工正文", manuscript.Mapper{})
		if err != nil {
			t.Fatal(err)
		}
		// 重新计算哈希，确保拒绝原因来自正文契约，而非过期哈希。
		value.FullContent = content
		value.ContentHash = fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		value.Hash = ""
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		value.Hash = fmt.Sprintf("%x", sha256.Sum256(encoded))
		if value.ValidateIntegrity() == nil {
			t.Fatal("rehashing allowed NUL through integrity validation")
		}
	}
}
