//go:build integration

package postgres

import (
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func TestSynthesisSourceGraphUsesPublishedPeersAndExactHistory(t *testing.T) {
	f := newSynthesisGitFixture(t)
	ctx := t.Context()
	first := f.generation(t, 161000, nil, "Shared scheduler evidence.")
	for _, title := range []string{"master scheduling", "interview scheduling", "project scheduling", "draft scheduling"} {
		first.Generation.Notes = append(first.Generation.Notes, f.newTopic(first, title))
	}
	created, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil || len(created.Publications) != 4 {
		t.Fatalf("create graph notes: %+v %v", created, err)
	}
	notes := make([]app.SynthesisNoteDetail, 4)
	for i, pub := range created.Publications {
		notes[i], err = f.service.GetNote(ctx, f.workspace, pub.NoteID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			continue
		}
		execution := f.preparePublication(t, notes[i])
		if _, err = f.node.Execute(ctx, execution.input, execution.identity); err != nil {
			t.Fatal(err)
		}
	}
	query := app.SynthesisSourceGraphQuery{WorkspaceID: f.workspace, NoteID: notes[0].Note.ID, RevisionID: notes[0].CurrentRevision.ID, Limit: 1}
	page, err := f.service.ReadSynthesisSourceGraph(ctx, query)
	if err != nil || len(page.Sources) != 1 || page.Sources[0] != first.Input.Sources[0].Reference || len(page.SharedNotes) != 1 || page.NextAfterNoteID == nil {
		t.Fatalf("graph: %+v %v", page, err)
	}
	query.AfterNoteID = *page.NextAfterNoteID
	tail, err := f.service.ReadSynthesisSourceGraph(ctx, query)
	if err != nil || len(tail.SharedNotes) != 1 || tail.NextAfterNoteID != nil || page.SharedNotes[0].NoteID == tail.SharedNotes[0].NoteID {
		t.Fatalf("graph tail: %+v %v", tail, err)
	}
	for _, peer := range append(page.SharedNotes, tail.SharedNotes...) {
		if peer.NoteID == notes[3].Note.ID || peer.NoteID == notes[0].Note.ID || !reflect.DeepEqual(peer.Sources, page.Sources) {
			t.Fatalf("invalid shared peer: %+v", peer)
		}
	}
	query.AfterNoteID = ""
	query.WorkspaceID = f.otherWorkspace
	if _, err = f.service.ReadSynthesisSourceGraph(ctx, query); !organizingIntegrationError(err, foundation.ErrorNotFound, app.ErrorCodeSynthesisNotFound) {
		t.Fatalf("cross-workspace graph: %v", err)
	}
	query.WorkspaceID = f.workspace
	query.RevisionID = notes[1].CurrentRevision.ID
	if _, err = f.service.ReadSynthesisSourceGraph(ctx, query); err == nil {
		t.Fatal("another note's revision was accepted")
	}
	query.RevisionID = notes[0].CurrentRevision.ID
	if err = f.db.Exec("UPDATE core.source SET removed_at=? WHERE id=?", f.now.AddDate(0, 0, 1), string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	after, err := f.service.ReadSynthesisSourceGraph(ctx, query)
	if err != nil || !reflect.DeepEqual(page, after) {
		t.Fatalf("source removal changed historical graph: %+v %v", after, err)
	}
}
