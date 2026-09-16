package application

import (
	"context"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	manuscriptadapter "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/manuscript"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type manuscriptReadStore struct {
	SynthesisStore
	revision domain.SynthesisRevision
}

func (s *manuscriptReadStore) GetSynthesisRevision(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SynthesisRevision, error) {
	return s.revision, nil
}

type manuscriptReadSources struct {
	SynthesisSourceReader
	calls int
	text  string
}

func (s *manuscriptReadSources) OpenSynthesisSource(_ context.Context, reference domain.SynthesisSourceRef) (SynthesisSourceView, error) {
	s.calls++
	return SynthesisSourceView{Reference: reference, Availability: domain.MaterialAvailable, Text: s.text}, nil
}

func TestOpenSourceAuthorizesOnlyExactManuscriptHistory(t *testing.T) {
	input, result := synthesisContractFixture()
	reference := input.Sources[0].Reference
	applied, err := domain.ApplySynthesisDelta(input.SourceEvent.Source.WorkspaceID, nil, result.Notes[0].Delta, []domain.SynthesisSourceRef{reference})
	if err != nil {
		t.Fatal(err)
	}
	machine := domain.SynthesisManuscriptMachine{WorkspaceID: reference.Source.WorkspaceID, NoteID: synthesisContractID(30), MachineTitle: result.Notes[0].Title, MachineItems: applied.Items}
	manuscript, err := domain.NewSynthesisManuscript(machine, "人工批注：所有结论仍需复核。\n", manuscriptadapter.Mapper{})
	if err != nil {
		t.Fatal(err)
	}
	revision := domain.SynthesisRevision{ID: synthesisContractID(32), WorkspaceID: machine.WorkspaceID, NoteID: machine.NoteID, DocumentID: synthesisContractID(31), ArticleRevisionID: synthesisContractID(33), RevisionNo: 1, ArticleRevisionNo: 1, Title: machine.MachineTitle, RendererVersion: domain.SynthesisRendererVersionV2, ContentHash: manuscript.ContentHash, Items: []domain.SynthesisItem{}, Delta: result.Notes[0].Delta, SourceEventID: input.SourceEvent.ID, WorkflowRunID: input.WorkflowRunID, ModelRunID: result.ModelRunID, CreatedAt: input.SourceEvent.CreatedAt, Manuscript: &manuscript}
	revision.Hash, err = domain.ComputeSynthesisRevisionHash(revision)
	if err != nil {
		t.Fatal(err)
	}
	store := &manuscriptReadStore{revision: revision}
	sources := &manuscriptReadSources{text: input.Sources[0].Text}
	service := &SynthesisService{dependencies: SynthesisDependencies{Store: store, Sources: sources}}
	view, err := service.OpenSource(context.Background(), revision.WorkspaceID, revision.NoteID, revision.ID, reference)
	if err != nil || view.Reference != reference || sources.calls != 1 || len(store.revision.Items) != 0 {
		t.Fatalf("historical source: %v", err)
	}
	for _, mutate := range []func(*domain.SynthesisSourceRef){func(r *domain.SynthesisSourceRef) { r.Title += "forged" }, func(r *domain.SynthesisSourceRef) { r.SourceSpanID = synthesisContractID(99) }, func(r *domain.SynthesisSourceRef) { r.Source.WorkspaceID = synthesisContractID(99) }, func(r *domain.SynthesisSourceRef) { r.ExcerptHash = synthesisContractHash("other") }} {
		ref := reference
		mutate(&ref)
		if _, err := service.OpenSource(context.Background(), revision.WorkspaceID, revision.NoteID, revision.ID, ref); err == nil {
			t.Fatal("arbitrary ref accepted")
		}
	}
	for _, ids := range [][3]foundation.ID{{synthesisContractID(99), revision.NoteID, revision.ID}, {revision.WorkspaceID, synthesisContractID(99), revision.ID}, {revision.WorkspaceID, revision.NoteID, synthesisContractID(99)}} {
		if _, err := service.OpenSource(context.Background(), ids[0], ids[1], ids[2], reference); err == nil {
			t.Fatal("owner rebound accepted")
		}
	}
	store.revision.ContentHash = synthesisContractHash("wrong")
	if _, err := service.OpenSource(context.Background(), revision.WorkspaceID, revision.NoteID, revision.ID, reference); err == nil {
		t.Fatal("invalid revision accepted")
	}
	if sources.calls != 1 {
		t.Fatal("invalid request reached source reader")
	}
}
