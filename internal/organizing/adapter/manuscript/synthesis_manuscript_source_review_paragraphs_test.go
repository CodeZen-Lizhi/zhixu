package manuscript

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestSourceReviewParagraphsPreserveUTF8Occurrences(t *testing.T) {
	text := "# 标题\r\n\r\n相同文字😀。\r\n\r\n相同文字😀。\r\n\r\n- 列表条件：版本 2。\r\n\r\n> 引用条件。\r\n\r\n```\r\n代码不作为目标\r\n```\r\n"
	parts, err := (Mapper{}).SourceReviewParagraphs("N001", text)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 4 {
		t.Fatalf("paragraphs=%+v", parts)
	}
	if parts[0].StartByte == parts[1].StartByte || parts[0].Hash != parts[1].Hash || parts[0].Label == parts[1].Label {
		t.Fatal("identical occurrences collapsed")
	}
	for _, p := range parts {
		actual := text[p.StartByte:p.EndByte]
		if p.Hash != fmt.Sprintf("%x", sha256.Sum256([]byte(actual))) || strings.Contains(actual, "代码") {
			t.Fatalf("bad exact paragraph: %+v", p)
		}
	}
}
