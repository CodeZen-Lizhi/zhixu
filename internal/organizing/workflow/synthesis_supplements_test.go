package workflow

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestSynthesisFreezeAndReopenSupplementaryEvidence(t *testing.T) {
	for _, count := range []int{1, domain.MaxSynthesisSources} {
		t.Run(fmt.Sprint(count), func(t *testing.T) { testSynthesisSupplementBudget(t, count) })
	}
}

func TestSynthesisAnchorFilteringKeepsSupplementsBoundToRemainingNote(t *testing.T) {
	executor, execution, store, _, _ := synthesisExecutorFixture(t)
	at := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	workspace := execution.WorkspaceID
	source := func(seed int, text string) app.SynthesisSourceExcerpt {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		return app.SynthesisSourceExcerpt{Text: text, Reference: domain.SynthesisSourceRef{Source: domain.SynthesisSourceVersion{WorkspaceID: workspace, SourceID: synthesisWorkflowTestID(seed), SourceVersionID: synthesisWorkflowTestID(seed + 1), ContentArtifactID: synthesisWorkflowTestID(seed + 2), ParseProjectionID: synthesisWorkflowTestID(seed + 3), ContentHash: hash}, SourceSpanID: synthesisWorkflowTestID(seed + 4), ExcerptHash: hash, Title: "evidence"}}
	}
	history, supplementSource, incoming := source(100, "old support"), source(200, "supplement"), source(300, "incoming")
	makeCandidate := func(seed int) app.SynthesisGenerationNote {
		note := domain.SynthesisNote{ID: synthesisWorkflowTestID(seed), WorkspaceID: workspace, DocumentID: synthesisWorkflowTestID(seed + 1), CurrentRevisionID: synthesisWorkflowTestID(seed + 2), TopicKey: fmt.Sprintf("topic %d", seed), Title: fmt.Sprintf("Topic %d", seed), Version: 1, Status: domain.SynthesisPendingApproval, CreatedAt: at, UpdatedAt: at}
		item := domain.SynthesisItem{ID: synthesisWorkflowTestID(seed + 3), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "old support", Sources: []domain.SynthesisSourceRef{history.Reference}}}
		markdown, err := domain.RenderSynthesisMarkdown(workspace, note.ID, note.Title, []domain.SynthesisItem{item})
		if err != nil {
			t.Fatal(err)
		}
		revision := domain.SynthesisRevision{ID: note.CurrentRevisionID, WorkspaceID: workspace, NoteID: note.ID, DocumentID: note.DocumentID, ArticleRevisionID: synthesisWorkflowTestID(seed + 4), RevisionNo: 1, ArticleRevisionNo: 1, Title: note.Title, RendererVersion: domain.SynthesisRendererVersion, ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(markdown))), Items: []domain.SynthesisItem{item}, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &item}}}, SourceEventID: synthesisWorkflowTestID(seed + 5), WorkflowRunID: execution.RunID, ModelRunID: synthesisWorkflowTestID(seed + 6), CreatedAt: at}
		revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
		if err != nil {
			t.Fatal(err)
		}
		return app.SynthesisGenerationNote{Note: note, Revision: revision}
	}
	filtered, retained := makeCandidate(500), makeCandidate(600)
	retained.Supplements = []app.SynthesisSourceSupplement{{ID: synthesisWorkflowTestID(700), WorkspaceID: workspace, NoteID: retained.Note.ID, BaseRevisionID: retained.Revision.ID, ItemID: retained.Revision.Items[0].ID, Slot: "FACT", AlternativeIndex: -1, ProcessingID: synthesisWorkflowTestID(701), Reference: supplementSource.Reference, CreatedAt: at}}
	owner := &admissionCandidateOwner{values: []app.SynthesisGenerationNote{filtered, retained}}
	freeze := &supplementFreezeStore{}
	executor.dependencies.Candidates, executor.dependencies.Store = owner, freeze
	executor.dependencies.Sources = &supplementSourceReader{values: []app.SynthesisSourceExcerpt{history, supplementSource, incoming}}
	executor.dependencies.Anchors = admissionReaderFixture{byNote: map[foundation.ID]app.SynthesisAnchorAdmission{filtered.Note.ID: {AnchorID: synthesisWorkflowTestID(800), ScopeVersion: 1, Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"Interview"}, Description: "Redis"}}}}
	loaded := store.value
	loaded.Input = nil
	loaded.Processing.SourceEvent.Source = incoming.Reference.Source
	if _, err := executor.prepareInput(t.Context(), execution, loaded); err != nil {
		t.Fatal(err)
	}
	if len(freeze.input.Notes) != 1 || freeze.input.Notes[0].Note.ID != retained.Note.ID || len(freeze.input.Notes[0].Supplements) != 1 || freeze.input.Notes[0].Supplements[0].ID != retained.Supplements[0].ID {
		t.Fatalf("anchor filtering shifted supplementary evidence: %+v", freeze.input.Notes)
	}
}

func testSynthesisSupplementBudget(t *testing.T, count int) {
	executor, execution, store, _, _ := synthesisExecutorFixture(t)
	at := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	workspace := execution.WorkspaceID
	source := func(seed int, text string) app.SynthesisSourceExcerpt {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		return app.SynthesisSourceExcerpt{Text: text, Reference: domain.SynthesisSourceRef{Source: domain.SynthesisSourceVersion{WorkspaceID: workspace, SourceID: synthesisWorkflowTestID(seed), SourceVersionID: synthesisWorkflowTestID(seed + 1), ContentArtifactID: synthesisWorkflowTestID(seed + 2), ParseProjectionID: synthesisWorkflowTestID(seed + 3), ContentHash: hash}, SourceSpanID: synthesisWorkflowTestID(seed + 4), ExcerptHash: hash, Title: "evidence"}}
	}
	first, second, incoming := source(100, "The cache expires."), source(200, "A second document confirms expiry."), source(300, "Another document enters the library.")
	note := domain.SynthesisNote{ID: synthesisWorkflowTestID(50), WorkspaceID: workspace, DocumentID: synthesisWorkflowTestID(51), CurrentRevisionID: synthesisWorkflowTestID(52), TopicKey: "cache", Title: "Cache", Aliases: []string{}, Version: 1, Status: domain.SynthesisPendingApproval, CreatedAt: at, UpdatedAt: at}
	item := domain.SynthesisItem{ID: synthesisWorkflowTestID(53), Kind: domain.SynthesisFactItem, Fact: &domain.SynthesisStatement{Text: "The cache expires.", Sources: []domain.SynthesisSourceRef{first.Reference}}}
	markdown, err := domain.RenderSynthesisMarkdown(workspace, note.ID, note.Title, []domain.SynthesisItem{item})
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.SynthesisRevision{ID: note.CurrentRevisionID, WorkspaceID: workspace, NoteID: note.ID, DocumentID: note.DocumentID, ArticleRevisionID: synthesisWorkflowTestID(54), RevisionNo: 1, ArticleRevisionNo: 1, Title: note.Title, RendererVersion: domain.SynthesisRendererVersion, ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(markdown))), Items: []domain.SynthesisItem{item}, Delta: domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisAddFact, Item: &item}}}, SourceEventID: synthesisWorkflowTestID(55), WorkflowRunID: execution.RunID, ModelRunID: synthesisWorkflowTestID(56), CreatedAt: at}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	supplement := app.SynthesisSourceSupplement{ID: synthesisWorkflowTestID(57), WorkspaceID: workspace, NoteID: note.ID, BaseRevisionID: revision.ID, ItemID: item.ID, Slot: "FACT", AlternativeIndex: -1, ProcessingID: synthesisWorkflowTestID(58), Reference: second.Reference, CreatedAt: at}
	owner := &supplementCandidateOwner{candidate: app.SynthesisGenerationNote{Note: note, Revision: revision, Supplements: []app.SynthesisSourceSupplement{supplement}}}
	frozenStore := &supplementFreezeStore{}
	executor.dependencies.Candidates = owner
	executor.dependencies.Store = frozenStore
	reader := &supplementSourceReader{values: []app.SynthesisSourceExcerpt{first, second, incoming}}
	for i := 1; i < count; i++ {
		extra := source(1000+i*10, fmt.Sprintf("Supplementary source %d supports expiry.", i))
		value := supplement
		value.ID, value.Reference = synthesisWorkflowTestID(10000+i), extra.Reference
		owner.candidate.Supplements = append(owner.candidate.Supplements, value)
		reader.values = append(reader.values, extra)
	}
	executor.dependencies.Sources = reader
	selectedCount := min(count, domain.MaxSynthesisSources-2)
	loaded := store.value
	loaded.Input = nil
	loaded.Processing.SourceEvent.Source = incoming.Reference.Source
	if _, err := executor.prepareInput(t.Context(), execution, loaded); err != nil {
		t.Fatal(err)
	}
	if len(frozenStore.input.Sources) != selectedCount+2 || len(frozenStore.input.Notes[0].Supplements) != selectedCount {
		t.Fatalf("freeze omitted supplementary evidence: %+v", frozenStore.input)
	}
	loaded.Input = &frozenStore.input
	// 冻结后新记录的补源不能使此次已付费模型输入发生漂移。
	owner.candidate.Supplements = nil
	reopened, err := executor.openInput(t.Context(), loaded, execution.NodeRunID, execution.NodeAttemptID)
	if err != nil || reopened.Validate() != nil || len(reopened.Sources) != selectedCount+2 || !reflect.DeepEqual(reopened.Notes[0].Supplements, frozenStore.input.Notes[0].Supplements) {
		t.Fatalf("reopen lost frozen evidence: %+v %v", reopened, err)
	}
	if !EqualSynthesisFrozenGeneration(frozenStore.input, reopened) || !reflect.DeepEqual(reopened.Notes[0].Revision, revision) {
		t.Fatal("reopen changed historical revision or frozen binding")
	}
	reopened.Notes[0].Supplements = append([]app.SynthesisSourceSupplement{}, reopened.Notes[0].Supplements...)
	reopened.Notes[0].Supplements[0].ID = synthesisWorkflowTestID(600)
	if EqualSynthesisFrozenGeneration(frozenStore.input, reopened) {
		t.Fatal("changed supplement identity escaped freeze fence")
	}
}

type supplementCandidateOwner struct {
	SynthesisCandidateOwner
	candidate app.SynthesisGenerationNote
}

type admissionCandidateOwner struct {
	SynthesisCandidateOwner
	values []app.SynthesisGenerationNote
}

func (s *admissionCandidateOwner) ListCandidates(context.Context, foundation.ID) ([]app.SynthesisGenerationNote, error) {
	return s.values, nil
}
func (s *admissionCandidateOwner) GetSynthesisRevision(_ context.Context, _ foundation.ID, noteID, revisionID foundation.ID) (domain.SynthesisRevision, error) {
	for _, value := range s.values {
		if value.Note.ID == noteID && value.Revision.ID == revisionID {
			return value.Revision, nil
		}
	}
	return domain.SynthesisRevision{}, fmt.Errorf("missing revision")
}

type admissionReaderFixture struct {
	byNote map[foundation.ID]app.SynthesisAnchorAdmission
}

func (s admissionReaderFixture) ReadSynthesisAnchorAdmission(_ context.Context, _ foundation.ID, noteID foundation.ID, _ domain.SynthesisSourceVersion) (app.SynthesisAnchorAdmission, error) {
	return s.byNote[noteID], nil
}

func (s *supplementCandidateOwner) ListCandidates(context.Context, foundation.ID) ([]app.SynthesisGenerationNote, error) {
	return []app.SynthesisGenerationNote{s.candidate}, nil
}
func (s *supplementCandidateOwner) GetSynthesisRevision(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SynthesisRevision, error) {
	return s.candidate.Revision, nil
}

type supplementFreezeStore struct {
	SynthesisProcessingStore
	input SynthesisFrozenInput
}

func (s *supplementFreezeStore) FreezeSynthesisInput(_ context.Context, _ workflowapp.ExecutionContext, input SynthesisFrozenInput) (SynthesisFrozenInput, error) {
	s.input = input
	return input, nil
}

type supplementSourceReader struct {
	app.SynthesisSourceReader
	values []app.SynthesisSourceExcerpt
}

func (s *supplementSourceReader) ReadSynthesisSource(_ context.Context, version domain.SynthesisSourceVersion) ([]app.SynthesisSourceExcerpt, error) {
	for _, source := range s.values {
		if source.Reference.Source == version {
			return []app.SynthesisSourceExcerpt{source}, nil
		}
	}
	return nil, fmt.Errorf("unexpected version")
}
