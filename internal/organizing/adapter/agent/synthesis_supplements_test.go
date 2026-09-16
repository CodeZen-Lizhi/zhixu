package agent

import (
	"reflect"
	"testing"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func TestSynthesisPromptUsesSupplementWithoutChangingRevision(t *testing.T) {
	input, _, _ := synthesisFixtureWithNote(t)
	source := synthesisFixtureSource(400, "Another source confirms five-minute expiration.")
	note := &input.Notes[0]
	prior := note.Revision
	supplement := app.SynthesisSourceSupplement{ID: synthesisTestID(500), WorkspaceID: note.Note.WorkspaceID, NoteID: note.Note.ID, BaseRevisionID: prior.ID, ItemID: prior.Items[0].ID, Slot: "FACT", AlternativeIndex: -1, ProcessingID: synthesisTestID(501), Reference: source.Reference, CreatedAt: synthesisFixtureTime}
	note.Supplements = []app.SynthesisSourceSupplement{supplement}
	input.Sources = append(input.Sources, source)
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	projected := synthesisInput(input)
	if !reflect.DeepEqual(projected.Notes[0].Items[0].Fact.Sources, []string{"S001", "S003"}) {
		t.Fatalf("prompt omitted supplement: %+v", projected.Notes[0].Items[0].Fact)
	}
	if !reflect.DeepEqual(note.Revision, prior) || note.Revision.Validate() != nil {
		t.Fatal("supplement altered immutable source projection")
	}
	// 后续版本可能已经包含相同依据；避免重复生成标签。
	note.Supplements[0].Reference = input.Sources[0].Reference
	projected = synthesisInput(input)
	if !reflect.DeepEqual(projected.Notes[0].Items[0].Fact.Sources, []string{"S001"}) {
		t.Fatal("prompt duplicated known evidence")
	}
}
