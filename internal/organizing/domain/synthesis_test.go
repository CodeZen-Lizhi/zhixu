package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestSynthesisIncrementalNotesPreserveUnrelatedContentAndHistory(t *testing.T) {
	t.Parallel()
	workspaceID, noteID := testID(1), testID(2)
	first, second, third := synthesisTestSource(100), synthesisTestSource(200), synthesisTestSource(300)
	fact := synthesisTestFact(10, "缓存降低重复读取的延迟", first)
	unrelated := synthesisTestFact(11, "本地缓存适用于单实例部署", first)
	gap := SynthesisItem{ID: testID(12), Kind: SynthesisGapItem, Gap: &SynthesisGapContent{
		Question: "缓存最大容量应如何确定？", Context: "资料未提供容量基准", Sources: []SynthesisSourceRef{first},
	}}
	initialDelta := SynthesisDelta{Operations: []SynthesisOperation{
		{Kind: SynthesisAddFact, Item: &fact}, {Kind: SynthesisAddFact, Item: &unrelated}, {Kind: SynthesisAddGap, Item: &gap},
	}}
	initial, err := ApplySynthesisDelta(workspaceID, nil, initialDelta, []SynthesisSourceRef{first})
	if err != nil || !initial.Changed || len(initial.Items) != 3 {
		t.Fatalf("first source: changed=%v items=%d error=%v", initial.Changed, len(initial.Items), err)
	}
	initialBytes, _ := json.Marshal(initial.Items)
	duplicate := synthesisTestFact(13, fact.Fact.Text, second)
	complement := synthesisTestFact(14, "共享缓存支持多个实例读取同一份数据", second)
	conflict := synthesisTestConflict(15, first, second)
	secondDelta := SynthesisDelta{Operations: []SynthesisOperation{
		{Kind: SynthesisAddFact, Item: &duplicate}, {Kind: SynthesisAddFact, Item: &complement}, {Kind: SynthesisAddConflict, Item: &conflict},
	}}
	merged, err := ApplySynthesisDelta(workspaceID, initial.Items, secondDelta, []SynthesisSourceRef{first, second})
	if err != nil || !merged.Changed || len(merged.Items) != 5 {
		t.Fatalf("second source: changed=%v items=%d error=%v", merged.Changed, len(merged.Items), err)
	}
	if merged.Items[0].ID != fact.ID || len(merged.Items[0].Fact.Sources) != 2 || merged.Items[0].Fact.Text != fact.Fact.Text {
		t.Fatal("duplicate opinion replaced identity/text instead of appending support")
	}
	afterMerge, _ := json.Marshal(initial.Items)
	if string(afterMerge) != string(initialBytes) {
		t.Fatal("applying a later source mutated the prior semantic projection")
	}
	beforeMarkdown, err := RenderSynthesisMarkdown(workspaceID, noteID, "缓存设计", merged.Items)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"事实与互补", "冲突与适用条件", "待确认：", "60 秒", "30 秒", "低频读取", "高时效读取"} {
		if !strings.Contains(beforeMarkdown, text) {
			t.Fatalf("rendered note lost a fact, alternative, condition or gap: %s", text)
		}
	}
	thirdDelta := SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddSupport, TargetItemID: fact.ID, Sources: []SynthesisSourceRef{third}}}}
	strengthened, err := ApplySynthesisDelta(workspaceID, merged.Items, thirdDelta, []SynthesisSourceRef{third})
	if err != nil || !strengthened.Changed || len(strengthened.Items) != len(merged.Items) {
		t.Fatalf("third source should only add support: %v", err)
	}
	if !reflect.DeepEqual(strengthened.Items[0].Fact.Sources, []SynthesisSourceRef{first, second, third}) {
		t.Fatal("third source lost, reordered or rewrote historical sources")
	}
	afterMarkdown, err := RenderSynthesisMarkdown(workspaceID, noteID, "缓存设计", strengthened.Items)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range merged.Items[1:] {
		if synthesisTestItemBlock(t, beforeMarkdown, item.ID) != synthesisTestItemBlock(t, afterMarkdown, item.ID) {
			t.Fatalf("unrelated item %s changed bytes after a third-source support delta", item.ID)
		}
	}
	replayed, err := ApplySynthesisDelta(workspaceID, strengthened.Items, thirdDelta, []SynthesisSourceRef{third})
	if err != nil || replayed.Changed || !reflect.DeepEqual(replayed.Items, strengthened.Items) {
		t.Fatalf("replayed evidence should not request a new revision: %v", err)
	}
	empty, err := ApplySynthesisDelta(workspaceID, strengthened.Items, SynthesisDelta{}, nil)
	if err != nil || empty.Changed || !reflect.DeepEqual(empty.Items, strengthened.Items) {
		t.Fatalf("empty delta should not request a new revision: %v", err)
	}
	// New results must not alias either the previous revision or model input.
	strengthened.Items[0].Fact.Sources[0].Title = "changed copy"
	if merged.Items[0].Fact.Sources[0].Title != first.Title || fact.Fact.Sources[0].Title != first.Title {
		t.Fatal("a projected result shares mutable source slices with its input")
	}
}

func TestSynthesisConflictDedupPreservesBothSidesAndTheirOrder(t *testing.T) {
	t.Parallel()
	a, b, c := synthesisTestSource(100), synthesisTestSource(200), synthesisTestSource(300)
	conflict := synthesisTestConflict(20, a, b)
	reversed := cloneSynthesisItem(conflict)
	reversed.ID = testID(21)
	reversed.Conflict.Alternatives[0], reversed.Conflict.Alternatives[1] = reversed.Conflict.Alternatives[1], reversed.Conflict.Alternatives[0]
	result, err := ApplySynthesisDelta(testID(1), []SynthesisItem{conflict}, SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddConflict, Item: &reversed}}}, []SynthesisSourceRef{a, b})
	if err != nil || result.Changed || !reflect.DeepEqual(result.Items, []SynthesisItem{conflict}) {
		t.Fatalf("reversed duplicate conflict produced a new item or changed its alternatives: %v", err)
	}
	reversed.Conflict.Alternatives[0].Sources = []SynthesisSourceRef{c}
	result, err = ApplySynthesisDelta(testID(1), result.Items, SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddConflict, Item: &reversed}}}, []SynthesisSourceRef{a, c})
	if err != nil || !result.Changed || result.Items[0].ID != conflict.ID ||
		result.Items[0].Conflict.Alternatives[0].Text != conflict.Conflict.Alternatives[0].Text ||
		!reflect.DeepEqual(result.Items[0].Conflict.Alternatives[1].Sources, []SynthesisSourceRef{b, c}) {
		t.Fatalf("conflict support failed to preserve the original side order and identity: %v", err)
	}
}

func TestSynthesisGapResolutionPreservesQuestionAndCannotBeOverwritten(t *testing.T) {
	t.Parallel()
	source := synthesisTestSource(100)
	gap := SynthesisItem{ID: testID(30), Kind: SynthesisGapItem, Gap: &SynthesisGapContent{Question: "容量基准是什么？", Context: "尚未测量", Sources: []SynthesisSourceRef{source}}}
	resolution := SynthesisStatement{Text: "基准测试建议容量为 1000 项", Sources: []SynthesisSourceRef{source}}
	delta := SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisResolveGap, TargetItemID: gap.ID, Resolution: &resolution}}}
	result, err := ApplySynthesisDelta(testID(1), []SynthesisItem{gap}, delta, []SynthesisSourceRef{source})
	if err != nil || !result.Changed || result.Items[0].Gap.Question != gap.Gap.Question || result.Items[0].Gap.Context != gap.Gap.Context || gap.Gap.Resolution != nil {
		t.Fatalf("resolution changed the original question or prior projection: %v", err)
	}
	replayed, err := ApplySynthesisDelta(testID(1), result.Items, delta, []SynthesisSourceRef{source})
	if err != nil || replayed.Changed {
		t.Fatalf("same gap resolution should be a no-op: %v", err)
	}
	resolution.Text = "新的无关结论"
	_, err = ApplySynthesisDelta(testID(1), result.Items, delta, []SynthesisSourceRef{source})
	assertDomainCode(t, err, ErrorCodeSynthesisDeltaInvalid)
}

func TestSynthesisDeltaRejectsUnknownUnscopedAndOutOfBoundsOperationsAtomically(t *testing.T) {
	t.Parallel()
	a, b := synthesisTestSource(100), synthesisTestSource(200)
	fact := synthesisTestFact(40, "已有正文", a)
	conflict := synthesisTestConflict(41, a, b)
	base := []SynthesisItem{fact, conflict}
	baseBytes, _ := json.Marshal(base)
	zero, two, negative := 0, 2, -1
	unknown := a
	unknown.SourceSpanID = testID(900)
	foreign := a
	foreign.Source.WorkspaceID = testID(900)
	drifted := a
	drifted.ExcerptHash = testHash("f")
	missing := synthesisTestFact(42, "缺少依据", a)
	missing.Fact.Sources = nil
	rewritten := synthesisTestFact(40, "偷偷替换已有正文", a)
	oversized := synthesisTestFact(43, strings.Repeat("x", MaxSynthesisTextBytes+1), a)
	union := synthesisTestFact(44, "错误联合", a)
	union.Gap = &SynthesisGapContent{Question: "混合联合"}
	resolution := SynthesisStatement{Text: "补充", Sources: []SynthesisSourceRef{a}}
	newItem := synthesisTestFact(45, "新条目", a)
	cases := []struct {
		name string
		op   SynthesisOperation
		code string
	}{
		{"unknown operation", SynthesisOperation{Kind: "REPLACE_ALL"}, ErrorCodeSynthesisDeltaInvalid},
		{"unknown source", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: fact.ID, Sources: []SynthesisSourceRef{unknown}}, ErrorCodeSynthesisSourceInvalid},
		{"cross workspace", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: fact.ID, Sources: []SynthesisSourceRef{foreign}}, ErrorCodeSynthesisSourceInvalid},
		{"excerpt drift", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: fact.ID, Sources: []SynthesisSourceRef{drifted}}, ErrorCodeSynthesisSourceInvalid},
		{"missing source", SynthesisOperation{Kind: SynthesisAddFact, Item: &missing}, ErrorCodeSynthesisSourceInvalid},
		{"unknown item", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: testID(900), Sources: []SynthesisSourceRef{a}}, ErrorCodeSynthesisDeltaInvalid},
		{"index on fact", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: fact.ID, AlternativeIndex: &zero, Sources: []SynthesisSourceRef{a}}, ErrorCodeSynthesisDeltaInvalid},
		{"alternative out of range", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: conflict.ID, AlternativeIndex: &two, Sources: []SynthesisSourceRef{a}}, ErrorCodeSynthesisDeltaInvalid},
		{"negative alternative", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: conflict.ID, AlternativeIndex: &negative, Sources: []SynthesisSourceRef{a}}, ErrorCodeSynthesisDeltaInvalid},
		{"missing alternative", SynthesisOperation{Kind: SynthesisAddSupport, TargetItemID: conflict.ID, Sources: []SynthesisSourceRef{a}}, ErrorCodeSynthesisDeltaInvalid},
		{"wrong resolution target", SynthesisOperation{Kind: SynthesisResolveGap, TargetItemID: fact.ID, Resolution: &resolution}, ErrorCodeSynthesisDeltaInvalid},
		{"item rewrite", SynthesisOperation{Kind: SynthesisAddFact, Item: &rewritten}, ErrorCodeSynthesisDeltaInvalid},
		{"oversized text", SynthesisOperation{Kind: SynthesisAddFact, Item: &oversized}, ErrorCodeSynthesisDeltaInvalid},
		{"mixed item union", SynthesisOperation{Kind: SynthesisAddFact, Item: &union}, ErrorCodeSynthesisDeltaInvalid},
		{"mixed operation union", SynthesisOperation{Kind: SynthesisAddFact, Item: &newItem, TargetItemID: fact.ID}, ErrorCodeSynthesisDeltaInvalid},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			// The first operation is valid; rejection of the second must still
			// leave every input unchanged and return no partial projection.
			delta := SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddSupport, TargetItemID: fact.ID, Sources: []SynthesisSourceRef{b}}, current.op}}
			result, err := ApplySynthesisDelta(testID(1), base, delta, []SynthesisSourceRef{a, b})
			assertDomainCode(t, err, current.code)
			if result.Changed || len(result.Items) != 0 {
				t.Fatal("a rejected delta returned a partial projection")
			}
			afterBytes, _ := json.Marshal(base)
			if string(afterBytes) != string(baseBytes) {
				t.Fatal("a rejected delta modified the prior revision")
			}
		})
	}
}

func TestSynthesisSourceLabelsAndProcessingKeyBindExactOriginalInput(t *testing.T) {
	t.Parallel()
	a, b := synthesisTestSource(100), synthesisTestSource(200)
	catalog := []SynthesisLabelledSource{{Label: "S001", Reference: a}, {Label: "S002", Reference: b}}
	resolved, err := ResolveSynthesisSourceLabels(testID(1), []string{"S002", "S001"}, catalog)
	if err != nil || !reflect.DeepEqual(resolved, []SynthesisSourceRef{b, a}) {
		t.Fatalf("source label resolution lost exact tuples: %v", err)
	}
	for _, labels := range [][]string{{"S003"}, {"S001", "S001"}, {"s001"}} {
		_, err := ResolveSynthesisSourceLabels(testID(1), labels, catalog)
		assertDomainCode(t, err, ErrorCodeSynthesisSourceInvalid)
	}
	_, err = ResolveSynthesisSourceLabels(testID(2), []string{"S001"}, catalog)
	assertDomainCode(t, err, ErrorCodeSynthesisSourceInvalid)
	badCatalog := append([]SynthesisLabelledSource{}, catalog...)
	badCatalog[1].Label = "S001"
	_, err = ResolveSynthesisSourceLabels(testID(1), nil, badCatalog)
	assertDomainCode(t, err, ErrorCodeSynthesisSourceInvalid)
	event := SynthesisSourceReady{ID: testID(3), Source: a.Source, IngestionAttemptID: testID(4), ProcessorVersion: SynthesisProcessorVersion, CreatedAt: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)}
	key, err := event.ProcessingKey()
	if err != nil {
		t.Fatal(err)
	}
	event.ID, event.IngestionAttemptID = testID(5), testID(6)
	replayKey, err := event.ProcessingKey()
	if err != nil || replayKey != key {
		t.Fatal("transport or ingestion attempt IDs changed the exact-input consumption key")
	}
	event.Source.ParseProjectionID = testID(7)
	nextKey, err := event.ProcessingKey()
	if err != nil || nextKey == key {
		t.Fatal("parser projection change reused an old consumption key")
	}
}

func TestSynthesisRevisionHashSnapshotAndSafeMarkdown(t *testing.T) {
	t.Parallel()
	source := synthesisTestSource(100)
	source.Title = "资料 ](javascript:alert(1)) <img src=x>"
	fact := synthesisTestFact(50, "<script>alert(1)</script> [外链](https://example.invalid) ![图](javascript:alert(1))", source)
	delta := SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddFact, Item: &fact}}}
	projection, err := ApplySynthesisDelta(testID(1), nil, delta, []SynthesisSourceRef{source})
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderSynthesisMarkdown(testID(1), testID(2), "# 标题 <script>", projection.Items)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"<script>", "<img", "](javascript:", "](https://", "![图]"} {
		if strings.Contains(body, unsafe) {
			t.Fatalf("untrusted text entered Markdown syntax: %q", unsafe)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "parse_projection_id="+string(source.Source.ParseProjectionID)) ||
		!strings.Contains(body, "excerpt_hash="+source.ExcerptHash) || strings.Count(body, "](/authoring/notes/") != 1 {
		t.Fatal("safe Markdown lost literal text or exact original-source navigation")
	}
	revision := SynthesisRevision{
		ID: testID(3), WorkspaceID: testID(1), NoteID: testID(2), DocumentID: testID(4), ArticleRevisionID: testID(5), RevisionNo: 1, ArticleRevisionNo: 1,
		Title: "# 标题 <script>", RendererVersion: SynthesisRendererVersion, ContentHash: synthesisHash([]byte(body)), Items: projection.Items, Delta: delta,
		SourceEventID: testID(6), WorkflowRunID: testID(7), ModelRunID: testID(8), CreatedAt: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC),
	}
	revision.Hash, err = ComputeSynthesisRevisionHash(revision)
	if err != nil || revision.Validate() != nil {
		t.Fatalf("valid immutable revision rejected: %v", err)
	}
	snapshot, err := SynthesisSnapshotFromRevision(revision)
	if err != nil || snapshot.Validate() != nil || snapshot.ProjectionHash != revision.Hash || snapshot.ArticleRevisionID != revision.ArticleRevisionID {
		t.Fatalf("snapshot lost its exact article/semantic binding: %v", err)
	}
	changedIdentity := revision
	changedIdentity.ArticleRevisionID = testID(900)
	assertDomainCode(t, changedIdentity.Validate(), ErrorCodeSynthesisRevisionInvalid)
	changedContent := revision
	changedContent.ContentHash = testHash("f")
	assertDomainCode(t, changedContent.Validate(), ErrorCodeSynthesisRevisionInvalid)
	snapshot.Items[0].Fact.Text = "changed snapshot"
	assertDomainCode(t, snapshot.Validate(), ErrorCodeSynthesisRevisionInvalid)
	if revision.Items[0].Fact.Text != fact.Fact.Text {
		t.Fatal("interview snapshot aliases the source revision")
	}
}

func TestSynthesisBoundsAndReparsePreserveOriginalSpans(t *testing.T) {
	t.Parallel()
	first := synthesisTestSource(100)
	fact := synthesisTestFact(60, "稳定条目", first)
	secondProjection := first
	secondProjection.Source.ParseProjectionID = testID(501)
	secondProjection.SourceSpanID = testID(502)
	delta := SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddSupport, TargetItemID: fact.ID, Sources: []SynthesisSourceRef{secondProjection}}}}
	result, err := ApplySynthesisDelta(testID(1), []SynthesisItem{fact}, delta, []SynthesisSourceRef{secondProjection})
	if err != nil || !result.Changed || !reflect.DeepEqual(result.Items[0].Fact.Sources, []SynthesisSourceRef{first, secondProjection}) {
		t.Fatalf("a new parser projection should retain old original-source references: %v", err)
	}
	tooMany := SynthesisDelta{Operations: make([]SynthesisOperation, MaxSynthesisOperations+1)}
	_, err = ApplySynthesisDelta(testID(1), []SynthesisItem{fact}, tooMany, nil)
	assertDomainCode(t, err, ErrorCodeSynthesisDeltaInvalid)
	for index := 1; index < MaxSynthesisStatementSources; index++ {
		reference := first
		reference.SourceSpanID = testID(600 + index)
		fact.Fact.Sources = append(fact.Fact.Sources, reference)
	}
	_, err = ApplySynthesisDelta(testID(1), []SynthesisItem{fact}, delta, []SynthesisSourceRef{secondProjection})
	assertDomainCode(t, err, ErrorCodeSynthesisSourceInvalid)
	if len(fact.Fact.Sources) != MaxSynthesisStatementSources {
		t.Fatal("source-count failure mutated the prior projection")
	}
}

func TestSynthesisRejectsRebindingAnImmutableArtifactOrProjection(t *testing.T) {
	t.Parallel()
	original := synthesisTestSource(100)
	fact := synthesisTestFact(60, "冻结来源", original)
	for _, kind := range []string{"artifact content", "projection parent"} {
		t.Run(kind, func(t *testing.T) {
			forged := original
			forged.Source.SourceVersionID = testID(701)
			forged.SourceSpanID = testID(702)
			if kind == "artifact content" {
				forged.Source.ContentHash = testHash("f")
			} else {
				forged.Source.ContentArtifactID = testID(703)
			}
			delta := SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddSupport, TargetItemID: fact.ID, Sources: []SynthesisSourceRef{forged}}}}
			_, err := ApplySynthesisDelta(testID(1), []SynthesisItem{fact}, delta, []SynthesisSourceRef{forged})
			assertDomainCode(t, err, ErrorCodeSynthesisSourceInvalid)
		})
	}
}

func TestSynthesisNoteRejectsInvalidStatusAndTopicShapes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	note := SynthesisNote{ID: testID(1), WorkspaceID: testID(2), DocumentID: testID(3), TopicKey: "cache design", Title: "缓存设计", Aliases: []string{"缓存"}, Version: 1, Status: SynthesisQueued, CreatedAt: now, UpdatedAt: now}
	if err := note.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []SynthesisStatus{SynthesisGenerating, SynthesisPendingApproval, SynthesisReady, SynthesisFailed, SynthesisRecoveryRequired, "PUBLISHED"} {
		invalidNote := note
		invalidNote.Status = status
		assertDomainCode(t, invalidNote.Validate(), ErrorCodeSynthesisInvalid)
	}
	note.Status, note.Failure = SynthesisRecoveryRequired, &SynthesisFailure{Code: "SYNTHESIS_PROVIDER_RESULT_UNKNOWN", Retryable: true}
	assertDomainCode(t, note.Validate(), ErrorCodeSynthesisInvalid)
	note.Failure.Retryable = false
	if err := note.Validate(); err != nil {
		t.Fatal(err)
	}
	key, err := CanonicalSynthesisTopicKey("  Cache   DESIGN  ")
	if err != nil || key != "cache design" {
		t.Fatalf("unexpected canonical key %q: %v", key, err)
	}
	for _, title := range []string{"正文\n# 注入标题", strings.Repeat("x", MaxSynthesisTitleBytes+1)} {
		assertDomainCode(t, ValidateSynthesisTopic("cache", title, nil), ErrorCodeSynthesisInvalid)
	}
}

func synthesisTestSource(seed int) SynthesisSourceRef {
	return SynthesisSourceRef{
		Source: SynthesisSourceVersion{WorkspaceID: testID(1), SourceID: testID(seed), SourceVersionID: testID(seed + 1),
			ContentArtifactID: testID(seed + 2), ParseProjectionID: testID(seed + 3), ContentHash: synthesisHash([]byte(fmtInt(seed)))},
		SourceSpanID: testID(seed + 4), ExcerptHash: synthesisHash([]byte("excerpt " + fmtInt(seed))), Title: "原始资料 " + fmtInt(seed),
	}
}

func synthesisTestFact(seed int, text string, reference SynthesisSourceRef) SynthesisItem {
	return SynthesisItem{ID: testID(seed), Kind: SynthesisFactItem, Fact: &SynthesisStatement{Text: text, Sources: []SynthesisSourceRef{reference}}}
}

func synthesisTestConflict(seed int, first, second SynthesisSourceRef) SynthesisItem {
	return SynthesisItem{ID: testID(seed), Kind: SynthesisConflictItem, Conflict: &SynthesisConflictContent{Subject: "缓存过期时间存在不同建议", Alternatives: []SynthesisStatement{
		{Text: "使用 60 秒过期时间", Applicability: "低频读取", Sources: []SynthesisSourceRef{first}},
		{Text: "使用 30 秒过期时间", Applicability: "高时效读取", Sources: []SynthesisSourceRef{second}},
	}}}
}

func synthesisTestItemBlock(t *testing.T, markdown string, id foundation.ID) string {
	t.Helper()
	start := strings.Index(markdown, "<!-- synthesis-item:"+string(id)+" -->")
	endMarker := "<!-- /synthesis-item:" + string(id) + " -->"
	end := strings.Index(markdown, endMarker)
	if start < 0 || end < start {
		t.Fatalf("rendered item %s is missing", id)
	}
	return markdown[start : end+len(endMarker)]
}
