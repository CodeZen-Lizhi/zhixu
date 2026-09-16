package domain

import (
	"encoding/json"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 这些样例只验证领域结构契约，不证明解析或来源。
// 生产所属模块在接受 v2 前还必须提供这些证明。
func manuscriptRevisionFixture(t *testing.T, zeroTrusted bool) SynthesisRevision {
	t.Helper()
	source := synthesisTestSource(100)
	fact := synthesisTestFact(50, "机器事实", source)
	conflict := synthesisTestConflict(51, source, synthesisTestSource(200))
	gap := SynthesisItem{ID: testID(52), Kind: SynthesisGapItem, Gap: &SynthesisGapContent{Question: "问题", Sources: []SynthesisSourceRef{source}, Resolution: &SynthesisStatement{Text: "已补充", Sources: []SynthesisSourceRef{source}}}}
	machine := SynthesisManuscriptMachine{WorkspaceID: testID(1), NoteID: testID(2), MachineTitle: "机器标题", MachineItems: []SynthesisItem{fact, conflict, gap}, IneligibleItemIDs: []foundation.ID{gap.ID}}
	content, err := RenderSynthesisMarkdown(machine.WorkspaceID, machine.NoteID, machine.MachineTitle, machine.MachineItems)
	if err != nil {
		t.Fatal(err)
	}
	m := SynthesisManuscript{Version: SynthesisManuscriptVersion, Machine: machine, FullContent: content, MachineContentHash: synthesisHash([]byte(content)), ContentHash: synthesisHash([]byte(content)), Assessment: SynthesisManuscriptAssessment{Mappings: []SynthesisManuscriptMapping{}, ReviewItems: []SynthesisManuscriptReview{}}}
	trusted := []SynthesisItem{}
	for _, item := range machine.MachineItems {
		if zeroTrusted || item.ID == gap.ID {
			m.Assessment.ReviewItems = append(m.Assessment.ReviewItems, SynthesisManuscriptReview{ItemID: item.ID, Reason: ManuscriptPreviouslyUntrusted})
			continue
		}
		start := strings.Index(content, "<!-- synthesis-item:"+string(item.ID)+" -->\n")
		marker := "<!-- /synthesis-item:" + string(item.ID) + " -->\n"
		end := strings.Index(content, marker) + len(marker)
		m.Assessment.Mappings = append(m.Assessment.Mappings, SynthesisManuscriptMapping{ItemID: item.ID, Start: start, End: end, BlockHash: synthesisHash([]byte(content[start:end]))})
		trusted = append(trusted, item)
	}
	if zeroTrusted {
		m.FullContent = "人工全文\n"
		m.ContentHash = synthesisHash([]byte(m.FullContent))
		m.ManualChanges = true
		m.Assessment.ContextReviewRequired = true
	}
	m.Hash, err = manuscriptHash(m)
	if err != nil {
		t.Fatal(err)
	}
	revision := SynthesisRevision{ID: testID(3), WorkspaceID: machine.WorkspaceID, NoteID: machine.NoteID, DocumentID: testID(4), ArticleRevisionID: testID(5), RevisionNo: 1, ArticleRevisionNo: 1, Title: "目录元数据标题", RendererVersion: SynthesisRendererVersionV2, ContentHash: m.ContentHash, Items: trusted, Delta: SynthesisDelta{Operations: []SynthesisOperation{{Kind: SynthesisAddFact, Item: &fact}}}, SourceEventID: testID(6), WorkflowRunID: testID(7), ModelRunID: testID(8), CreatedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), Manuscript: &m}
	revision.Hash, err = ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestSynthesisManuscriptRevisionZeroTrustedAndVersionedContent(t *testing.T) {
	for _, zero := range []bool{false, true} {
		revision := manuscriptRevisionFixture(t, zero)
		if err := revision.Validate(); err != nil {
			t.Fatal(err)
		}
		body, err := revision.Content()
		if err != nil || body != revision.Manuscript.FullContent {
			t.Fatalf("body lost: %v", err)
		}
		snapshot, err := SynthesisSnapshotFromRevision(revision)
		if err != nil {
			t.Fatal(err)
		}
		if err := snapshot.Validate(); err != nil {
			t.Fatal(err)
		}
		read, err := snapshot.Content()
		if err != nil || read != body {
			t.Fatalf("snapshot body lost: %v", err)
		}
		if zero && len(snapshot.Items) != 0 {
			t.Fatal("machine items leaked into trusted snapshot")
		}
		revision.RendererVersion = SynthesisRendererVersion
		if _, err := ComputeSynthesisRevisionHash(revision); err == nil {
			t.Fatal("v2 silently accepted as v1")
		}
	}
}

func TestSynthesisManuscriptRevisionRejectsTamperingAndSubsetBypass(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SynthesisRevision)
	}{
		{"missing envelope", func(r *SynthesisRevision) { r.Manuscript = nil }},
		{"unsupported version", func(r *SynthesisRevision) { r.RendererVersion = "unknown" }},
		{"content hash", func(r *SynthesisRevision) { r.ContentHash = testHash("f") }},
		{"full content", func(r *SynthesisRevision) { r.Manuscript.FullContent += "改写" }},
		{"envelope hash", func(r *SynthesisRevision) { r.Manuscript.Hash = testHash("f") }},
		{"machine leak", func(r *SynthesisRevision) { r.Items = cloneSynthesisItems(r.Manuscript.Machine.MachineItems) }},
		{"omitted trusted item", func(r *SynthesisRevision) { r.Items = r.Items[:1] }},
		{"reordered items", func(r *SynthesisRevision) { r.Items[0], r.Items[1] = r.Items[1], r.Items[0] }},
		{"owner rebound", func(r *SynthesisRevision) { r.NoteID = testID(999) }},
		{"rehash full content only", func(r *SynthesisRevision) {
			r.Manuscript.FullContent += "额外文本"
			r.Manuscript.ContentHash = synthesisHash([]byte(r.Manuscript.FullContent))
			r.Manuscript.ManualChanges = true
			r.Manuscript.Hash, _ = manuscriptHash(*r.Manuscript)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			revision := manuscriptRevisionFixture(t, false)
			tc.mutate(&revision)
			if _, err := ComputeSynthesisRevisionHash(revision); err == nil {
				t.Fatal("invalid v2 accepted")
			}
			snapshot := SynthesisNoteSnapshot{WorkspaceID: revision.WorkspaceID, NoteID: revision.NoteID, RevisionID: revision.ID, RevisionNo: revision.RevisionNo, DocumentID: revision.DocumentID, ArticleRevisionID: revision.ArticleRevisionID, ArticleRevisionNo: revision.ArticleRevisionNo, ContentHash: revision.ContentHash, ProjectionHash: revision.Hash, Title: revision.Title, RendererVersion: revision.RendererVersion, Items: revision.Items, Manuscript: revision.Manuscript}
			if snapshot.Validate() == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestSynthesisManuscriptSnapshotDeepCopy(t *testing.T) {
	revision := manuscriptRevisionFixture(t, false)
	before, _ := json.Marshal(revision)
	snapshot, err := SynthesisSnapshotFromRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Manuscript, revision.Manuscript) {
		t.Fatal("copy changed hashed envelope")
	}
	snapshot.Items[0].Fact.Sources[0].Title = "public item changed"
	m := snapshot.Manuscript
	m.Machine.MachineItems[0].Fact.Sources[0].Title = "source changed"
	m.Machine.MachineItems[1].Conflict.Alternatives[0].Sources[0].Title = "alternative source changed"
	m.Machine.MachineItems[2].Gap.Sources[0].Title = "gap source changed"
	m.Machine.MachineItems[2].Gap.Resolution.Sources[0].Title = "resolution source changed"
	m.Machine.IneligibleItemIDs[0] = testID(900)
	m.Assessment.Mappings[0].Start++
	m.Assessment.ReviewItems[0].Reason = ManuscriptContextReview
	after, _ := json.Marshal(revision)
	if string(before) != string(after) {
		t.Fatal("snapshot aliases revision nested data")
	}
	if revision.Validate() != nil || snapshot.Validate() == nil {
		t.Fatal("mutation integrity not isolated")
	}
}

func TestSynthesisManuscriptV1ReadAndOmission(t *testing.T) {
	r := manuscriptRevisionFixture(t, false)
	r.RendererVersion = SynthesisRendererVersion
	r.Manuscript = nil
	content, err := RenderSynthesisMarkdown(r.WorkspaceID, r.NoteID, r.Title, r.Items)
	if err != nil {
		t.Fatal(err)
	}
	r.ContentHash = synthesisHash([]byte(content))
	r.Hash, err = ComputeSynthesisRevisionHash(r)
	if err != nil {
		t.Fatal(err)
	}
	body, err := r.Content()
	if err != nil || body != content {
		t.Fatalf("v1 reader mismatch: %v", err)
	}
	snapshot, err := SynthesisSnapshotFromRevision(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{r, snapshot} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "manuscript") {
			t.Fatal("v1 serialized extension")
		}
	}
	if body, err := snapshot.Content(); err != nil || body != content {
		t.Fatalf("v1 snapshot reader mismatch: %v", err)
	}
	r.RendererVersion = SynthesisRendererVersionV2
	if _, err := r.Content(); err == nil {
		t.Fatal("v2 without envelope fell back to renderer")
	}
}
