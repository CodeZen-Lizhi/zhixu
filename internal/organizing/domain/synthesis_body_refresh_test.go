package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestBodyRefreshPreservesLocalItemsAndStopsUnchangedContent(t *testing.T) {
	source := synthesisTestSource(100)
	gap := SynthesisItem{ID: testID(50), Kind: SynthesisGapItem, Gap: &SynthesisGapContent{Question: "事务边界？", Context: "数据库", Sources: []SynthesisSourceRef{source}}}
	revision := func(id int, no int64, item SynthesisItem) SynthesisRevision {
		t.Helper()
		body, err := RenderSynthesisMarkdown(testID(1), testID(2), "数据库", []SynthesisItem{item})
		if err != nil {
			t.Fatal(err)
		}
		r := SynthesisRevision{ID: testID(id), WorkspaceID: testID(1), NoteID: testID(2), DocumentID: testID(4), ArticleRevisionID: testID(id + 100), RevisionNo: no, ArticleRevisionNo: no, Title: "数据库", RendererVersion: SynthesisRendererVersion, ContentHash: synthesisHash([]byte(body)), Items: []SynthesisItem{item}, Delta: SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddGap, Item: &item}}}, SourceEventID: testID(6), WorkflowRunID: testID(7), ModelRunID: testID(8), CreatedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
		if no > 1 {
			r.ParentRevisionID = testID(3)
		}
		if item.Gap.Resolution != nil {
			r.Delta = SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisResolveGap, TargetItemID: item.ID, Resolution: item.Gap.Resolution}}}
		}
		r.Hash, err = ComputeSynthesisRevisionHash(r)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	original := revision(3, 1, gap)
	included, err := IncludeSynthesisPublishedItem(original, testID(10), gap.ID, testID(51))
	if err != nil {
		t.Fatal(err)
	}
	unrelated := synthesisTestFact(52, "本地内容保持", source)
	current := []SynthesisItem{*included.Item, unrelated}
	resolved := cloneSynthesisItem(gap)
	resolved.Gap.Resolution = &SynthesisStatement{Text: "在提交时结束", Applicability: "已知条件", Sources: []SynthesisSourceRef{source}}
	updated := revision(9, 2, resolved)
	result, err := RefreshSynthesisPublishedItem(testID(1), current, testID(51), original, updated, testID(10), testID(11))
	if err != nil || !result.Changed || result.Items[0].Gap.Resolution == nil || result.Items[0].ID != testID(51) || !reflect.DeepEqual(result.Items[1], unrelated) {
		t.Fatalf("local refresh failed: %+v %v", result, err)
	}
	if current[0].Gap.Resolution != nil {
		t.Fatal("refresh mutated input")
	}
	same := revision(12, 2, gap)
	unchanged, err := RefreshSynthesisPublishedItem(testID(1), current, testID(51), original, same, testID(10), testID(13))
	if err != nil || unchanged.Changed || !reflect.DeepEqual(unchanged.Items, current) {
		t.Fatalf("reference-only update created body change: %v", err)
	}
	local := cloneSynthesisItems(current)
	local[0].Gap.Resolution = &SynthesisStatement{Text: "本地解决方案", Applicability: "未知", Sources: []SynthesisSourceRef{source}}
	if _, err := RefreshSynthesisPublishedItem(testID(1), local, testID(51), original, updated, testID(10), testID(11)); err == nil {
		t.Fatal("local resolution discarded")
	}
	missing := cloneSynthesisItem(resolved)
	missing.ID = testID(80)
	absent := revision(14, 2, missing)
	if _, err := RefreshSynthesisPublishedItem(testID(1), current, testID(51), original, absent, testID(10), testID(15)); err == nil {
		t.Fatal("missing upstream item accepted")
	}
}
