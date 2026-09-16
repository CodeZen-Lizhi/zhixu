package application

import (
	"context"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"reflect"
	"strings"
	"testing"

	gitmerge "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/gitmerge"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
)

func TestSynthesisManuscriptMergePreservesUnpublishedWorkAndFileComments(t *testing.T) {
	engine, err := gitmerge.New(gitcli.New("git"))
	if err != nil {
		t.Fatal(err)
	}
	p := manuscriptMergeRevision(t, 1, "已发布知识 A")
	l := manuscriptMergeRevision(t, 2, "已发布知识 A", "尚未发布知识 B")
	newRevision := manuscriptMergeRevision(t, 3, "已发布知识 A", "尚未发布知识 B", "新增知识 C")
	file, _ := p.Content()
	// 独立前缀是用户批注，不是经过来源验证的断言。
	file = "我的复习备注：先读事务章节。\n\n" + file
	input := SynthesisManuscriptMergeInput{Latest: l, Published: &p, FileExists: true, FileContent: file, NextMachine: manuscriptMergeMachine(newRevision)}
	preview, err := PreviewSynthesisManuscript(context.Background(), input, engine, manuscript.Mapper{})
	if err != nil || preview.Review != nil || preview.Manuscript == nil {
		t.Fatalf("clean merge: %+v %v", preview, err)
	}
	for _, text := range []string{"我的复习备注：先读事务章节。", "已发布知识 A", "尚未发布知识 B", "新增知识 C"} {
		if !strings.Contains(preview.Manuscript.FullContent, text) {
			t.Fatalf("lost content %q", text)
		}
	}
	if len(preview.Manuscript.Assessment.Mappings) != 0 || !preview.Manuscript.Assessment.ContextReviewRequired {
		t.Fatal("manual context was falsely source-verified")
	}
	replayed, err := PreviewSynthesisManuscript(context.Background(), input, engine, manuscript.Mapper{})
	if err != nil || !reflect.DeepEqual(preview, replayed) {
		t.Fatalf("same frozen input is not deterministic: %v", err)
	}
	input.FileContent = "最新备注\n\n" + file
	fresh, err := PreviewSynthesisManuscript(context.Background(), input, engine, manuscript.Mapper{})
	if err != nil || fresh.Fingerprint == preview.Fingerprint || !strings.HasPrefix(fresh.Manuscript.FullContent, "最新备注") {
		t.Fatalf("changed capture failed to change preview identity: %v", err)
	}
}

func TestSynthesisManuscriptMergeConflictsNeverReturnApplicableManuscript(t *testing.T) {
	engine, _ := gitmerge.New(gitcli.New("git"))
	p := manuscriptMergeRevision(t, 1, "原始结论")
	next := manuscriptMergeRevision(t, 2, "AI 改写结论")
	file, _ := p.Content()
	file = strings.ReplaceAll(file, "原始结论", "人工改写结论")
	preview, err := PreviewSynthesisManuscript(context.Background(), SynthesisManuscriptMergeInput{Latest: p, Published: &p, FileExists: true, FileContent: file, NextMachine: manuscriptMergeMachine(next)}, engine, manuscript.Mapper{})
	if err != nil || preview.Manuscript != nil || preview.Review == nil || preview.Review.Stage != SynthesisMergeWorkspaceStage || len(preview.Review.Conflicts) == 0 {
		t.Fatalf("conflict was lost or treated as resolved: %+v %v", preview, err)
	}
	conflict := preview.Review.Conflicts[0]
	if !strings.Contains(string(conflict.Current), "人工改写结论") || !strings.Contains(string(conflict.Proposed), "AI 改写结论") || !strings.Contains(string(conflict.Base), "原始结论") {
		t.Fatalf("review does not contain actual competing bytes: %+v", conflict)
	}
}

func TestSynthesisManuscriptMergeMissingFileAndCollisionFailClosed(t *testing.T) {
	engine, _ := gitmerge.New(gitcli.New("git"))
	p := manuscriptMergeRevision(t, 1, "原始结论")
	base := SynthesisManuscriptMergeInput{Latest: p, NextMachine: manuscriptMergeMachine(p)}
	preview, err := PreviewSynthesisManuscript(context.Background(), base, engine, manuscript.Mapper{})
	if err != nil || preview.Manuscript == nil || preview.Manuscript.ManualChanges {
		t.Fatalf("absent unpublished target: %v", err)
	}
	for _, mutate := range []func(*SynthesisManuscriptMergeInput){
		func(i *SynthesisManuscriptMergeInput) { i.Published = &p },
		func(i *SynthesisManuscriptMergeInput) { i.FileExists = true },
		func(i *SynthesisManuscriptMergeInput) { i.FileContent = "hidden bytes on missing file" },
		func(i *SynthesisManuscriptMergeInput) { i.NextMachine.NoteID = synthesisContractID(999) },
	} {
		input := base
		mutate(&input)
		if _, err := PreviewSynthesisManuscript(context.Background(), input, engine, manuscript.Mapper{}); err == nil {
			t.Fatal("invalid file/publication/owner baseline accepted")
		}
	}
}

func TestSynthesisManuscriptMergeCarriesExistingManualCandidate(t *testing.T) {
	engine, _ := gitmerge.New(gitcli.New("git"))
	p := manuscriptMergeRevision(t, 1, "原始结论 A")
	l := manuscriptMergeRevision(t, 2, "原始结论 A", "尚未发布知识 B")
	content, _ := l.Content()
	content = strings.ReplaceAll(content, "原始结论 A", "人工保留的结论 A")
	machine := manuscriptMergeMachine(l)
	envelope, err := domain.NewSynthesisManuscript(machine, content, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	l.Manuscript, l.RendererVersion, l.ContentHash = &envelope, domain.SynthesisRendererVersionV2, envelope.ContentHash
	l.Items, err = envelope.TrustedItems(machine, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	l.Hash, err = domain.ComputeSynthesisRevisionHash(l)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := p.Content()
	next := manuscriptMergeRevision(t, 3, "原始结论 A", "尚未发布知识 B", "新增知识 C")
	input := SynthesisManuscriptMergeInput{Latest: l, Published: &p, FileExists: true, FileContent: file, NextMachine: manuscriptMergeMachine(next)}
	preview, err := PreviewSynthesisManuscript(context.Background(), input, engine, manuscript.Mapper{})
	if err != nil || preview.Manuscript == nil || preview.Review != nil || !strings.Contains(preview.Manuscript.FullContent, "人工保留的结论 A") || !strings.Contains(preview.Manuscript.FullContent, "新增知识 C") {
		t.Fatalf("existing manual candidate lost: %+v %v", preview, err)
	}
	if len(preview.Manuscript.Machine.IneligibleItemIDs) != len(machine.MachineItems) {
		t.Fatal("prior context review exclusions were not carried forward")
	}
	// AI 并发改写已被人工编辑的条目时，必须在外层 P/F 合并前停止，
	// 并展示真实的第一阶段冲突。
	next = manuscriptMergeRevision(t, 3, "AI 新结论 A", "尚未发布知识 B", "新增知识 C")
	input.NextMachine = manuscriptMergeMachine(next)
	preview, err = PreviewSynthesisManuscript(context.Background(), input, engine, manuscript.Mapper{})
	if err != nil || preview.Manuscript != nil || preview.Review == nil || preview.Review.Stage != SynthesisMergeCandidateStage {
		t.Fatalf("inner conflict did not stop merge: %+v %v", preview, err)
	}
}

func manuscriptMergeMachine(revision domain.SynthesisRevision) domain.SynthesisManuscriptMachine {
	return domain.SynthesisManuscriptMachine{WorkspaceID: revision.WorkspaceID, NoteID: revision.NoteID, MachineTitle: revision.Title, MachineItems: revision.Items}
}

func manuscriptMergeRevision(t *testing.T, number int64, statements ...string) domain.SynthesisRevision {
	t.Helper()
	input, generation := synthesisContractFixture()
	items := make([]domain.SynthesisItem, len(statements))
	for i, text := range statements {
		items[i] = domain.SynthesisItem{ID: synthesisContractID(200 + i), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: text, Sources: []domain.SynthesisSourceRef{input.Sources[0].Reference}}}
	}
	revision := domain.SynthesisRevision{ID: synthesisContractID(100 + int(number)), WorkspaceID: synthesisContractID(1), NoteID: synthesisContractID(30), DocumentID: synthesisContractID(31), ArticleRevisionID: synthesisContractID(120 + int(number)), RevisionNo: number, ArticleRevisionNo: number, Title: "数据库知识", RendererVersion: domain.SynthesisRendererVersion, Items: items, Delta: generation.Notes[0].Delta, SourceEventID: input.SourceEvent.ID, WorkflowRunID: input.WorkflowRunID, ModelRunID: generation.ModelRunID, CreatedAt: input.SourceEvent.CreatedAt}
	if number > 1 {
		revision.ParentRevisionID = synthesisContractID(99 + int(number))
	}
	content, err := domain.RenderSynthesisMarkdown(revision.WorkspaceID, revision.NoteID, revision.Title, items)
	if err != nil {
		t.Fatal(err)
	}
	revision.ContentHash = synthesisContractHash(content)
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestSynthesisManuscriptMergeTombstoneRemovalAndReintroduction(t *testing.T) {
	engine, err := gitmerge.New(gitcli.New("git"))
	if err != nil {
		t.Fatal(err)
	}
	latest := manuscriptMergeRevision(t, 1, "曾失信 X", "保留 Y")
	original := manuscriptMergeMachine(latest)
	initial := original
	initial.IneligibleItemIDs = []foundation.ID{original.MachineItems[0].ID}
	content, err := latest.Content()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := domain.NewSynthesisManuscript(initial, content, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	attachManuscriptPreview(t, &latest, envelope)
	// 三次实际 Git 预览：继承 X、移除 X、恢复精确的 X ID 和文本。
	for step, items := range [][]domain.SynthesisItem{original.MachineItems, original.MachineItems[1:], original.MachineItems} {
		file, err := latest.Content()
		if err != nil {
			t.Fatal(err)
		}
		next := original
		next.MachineItems = items
		next.IneligibleItemIDs = nil
		before := append([]domain.SynthesisItem{}, next.MachineItems...)
		preview, err := PreviewSynthesisManuscript(context.Background(), SynthesisManuscriptMergeInput{Latest: latest, Published: &latest, FileExists: true, FileContent: file, NextMachine: next}, engine, manuscript.Mapper{})
		if err != nil || preview.Review != nil || preview.Manuscript == nil {
			t.Fatalf("step %d: %+v %v", step, preview, err)
		}
		m := preview.Manuscript
		if len(m.Machine.IneligibleItemIDs) != 1 || m.Machine.IneligibleItemIDs[0] != original.MachineItems[0].ID {
			t.Fatalf("step %d lost absent tombstone", step)
		}
		trusted, err := m.TrustedItems(m.Machine, manuscript.Mapper{})
		if err != nil {
			t.Fatal(err)
		}
		if len(trusted) != 1 || trusted[0].ID != original.MachineItems[1].ID {
			t.Fatalf("step %d resurrected X", step)
		}
		if !reflect.DeepEqual(next.MachineItems, before) || next.IneligibleItemIDs != nil {
			t.Fatal("preview mutated caller machine")
		}
		latest = manuscriptMergeRevision(t, int64(step+2), "曾失信 X", "保留 Y")
		attachManuscriptPreview(t, &latest, *m)
	}
}

func TestSynthesisManuscriptMergeExclusionOverflowRequiresReview(t *testing.T) {
	engine, err := gitmerge.New(gitcli.New("git"))
	if err != nil {
		t.Fatal(err)
	}
	latest := manuscriptMergeRevision(t, 1, "人工待复核 X")
	machine := manuscriptMergeMachine(latest)
	content, err := latest.Content()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := domain.NewSynthesisManuscript(machine, content+"\n人工批注\n", manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	attachManuscriptPreview(t, &latest, envelope)
	next := machine
	for i := 0; i < domain.MaxSynthesisManuscriptExclusions; i++ {
		next.IneligibleItemIDs = append(next.IneligibleItemIDs, synthesisContractID(1000+i))
	}
	if err := next.Validate(); err != nil {
		t.Fatalf("bound should accept valid absent IDs: %v", err)
	}
	preview, err := PreviewSynthesisManuscript(context.Background(), SynthesisManuscriptMergeInput{Latest: latest, NextMachine: next}, engine, manuscript.Mapper{})
	if err == nil || !strings.Contains(err.Error(), domain.ErrorCodeSynthesisManuscriptHistoryReviewRequired) || preview.Manuscript != nil || preview.Review != nil {
		t.Fatalf("overflow silently discarded history: %+v %v", preview, err)
	}
	if len(next.IneligibleItemIDs) != domain.MaxSynthesisManuscriptExclusions {
		t.Fatal("caller history truncated")
	}
	next.IneligibleItemIDs = append(next.IneligibleItemIDs, synthesisContractID(2000))
	if next.Validate() == nil {
		t.Fatal("domain accepted overbound history")
	}
}

func attachManuscriptPreview(t *testing.T, revision *domain.SynthesisRevision, envelope domain.SynthesisManuscript) {
	t.Helper()
	revision.Manuscript = &envelope
	revision.RendererVersion = domain.SynthesisRendererVersionV2
	revision.ContentHash = envelope.ContentHash
	var err error
	revision.Items, err = envelope.TrustedItems(envelope.Machine, manuscript.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(*revision)
	if err != nil {
		t.Fatal(err)
	}
}
