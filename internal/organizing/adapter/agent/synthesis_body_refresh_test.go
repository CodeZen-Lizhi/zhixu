package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestBodyRefreshV5CopiesOnlyImpactedItemAndRequiresScopeReview(t *testing.T) {
	input, execution, validation := synthesisFixtureWithNote(t)
	original := input.Notes[0].Revision
	downstream := synthesisDuplicateGenerationNote(t, input.Notes[0], 300)
	included, err := domain.IncludeSynthesisPublishedItem(original, synthesisTestID(350), original.Items[2].ID, synthesisTestID(351))
	if err != nil {
		t.Fatal(err)
	}
	downstream.Revision.Items = append([]domain.SynthesisItem{}, downstream.Revision.Items...)
	downstream.Revision.Items[2] = *included.Item
	downstream.Revision.Delta.Operations = append([]domain.SynthesisOperation{}, downstream.Revision.Delta.Operations...)
	downstream.Revision.Delta.Operations[2] = included
	rehash := func(revision *domain.SynthesisRevision) {
		t.Helper()
		body, err := domain.RenderSynthesisMarkdown(revision.WorkspaceID, revision.NoteID, revision.Title, revision.Items)
		if err != nil {
			t.Fatal(err)
		}
		revision.ContentHash = synthesisHash([]byte(body))
		revision.Hash, err = domain.ComputeSynthesisRevisionHash(*revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	rehash(&downstream.Revision)
	var updated domain.SynthesisRevision
	rawRevision, _ := json.Marshal(original)
	if err := json.Unmarshal(rawRevision, &updated); err != nil {
		t.Fatal(err)
	}
	updated.ID, updated.ArticleRevisionID, updated.ParentRevisionID = synthesisTestID(352), synthesisTestID(353), original.ID
	updated.RevisionNo, updated.ArticleRevisionNo = 2, 2
	updated.Items[2].Gap.Resolution = &domain.SynthesisStatement{Text: "Keep at most 100 entries.", Sources: []domain.SynthesisSourceRef{input.Sources[1].Reference}}
	updated.Delta = domain.SynthesisDelta{Operations: []domain.SynthesisOperation{{Kind: domain.SynthesisResolveGap, TargetItemID: updated.Items[2].ID, Resolution: updated.Items[2].Gap.Resolution}}}
	rehash(&updated)
	downstream.Anchor = &app.SynthesisAnchorBinding{AnchorID: synthesisTestID(354), ScopeVersion: 1, Scope: domain.AnchorScope{Topics: []string{"Cache"}, Audiences: []string{"Interview"}, Description: "Cache lifetime and memory bounds"}, AllowedSources: []domain.SynthesisSourceRef{input.Sources[1].Reference}}
	input.Notes = []app.SynthesisGenerationNote{downstream}
	input.BodyRefresh = &app.SynthesisBodyRefreshBinding{Request: app.SynthesisBodyRefreshRequest{ID: synthesisTestID(355), WorkspaceID: downstream.Note.WorkspaceID, NoteID: downstream.Note.ID, PublicationID: synthesisTestID(356), ImpactID: synthesisTestID(357), CreatedAt: synthesisFixtureTime}, Items: []app.SynthesisBodyRefreshItemBinding{{ImpactID: synthesisTestID(357), ItemID: included.Item.ID, Original: *included.Item.BodyReference, Updated: domain.SynthesisBodyReference{WorkspaceID: updated.WorkspaceID, NoteID: updated.NoteID, RevisionID: updated.ID, PublicationID: synthesisTestID(356), ItemID: updated.Items[2].ID, ProjectionHash: updated.Hash}}}}
	input.BodyRefreshRevisions = []domain.SynthesisRevision{original, updated}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	output := `{"notes":[{"note":"N001","operations":[{"kind":"REFRESH_ITEM","target":"I003"}]}]}`
	prelim, err := bindSynthesisOutput([]byte(output), input, synthesisTestID(360), (&synthesisTestIDs{}).New)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := synthesisSemanticPlan(input, prelim)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, check := range plan.Checks {
		kinds = append(kinds, check.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"GAP_CONTEXT", "GAP_RESOLUTION", "REFRESH_SCOPE"}) || plan.Scope == nil || len(plan.RefreshUpdates) != 1 {
		t.Fatalf("missing independent checks: %+v", plan)
	}
	harness := newSynthesisHarness(t, output, string(synthesisReviewForPlan(t, plan, "SUPPORTED")))
	generated, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, generated)
	if err != nil || !receipt.Accepted {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	record, err := harness.store.GetModelRun(context.Background(), execution.WorkspaceID, generated.ModelRunID)
	if err != nil || record.Run.Prompt.Version != "v5" || record.Run.Schema.Version != "v3" {
		t.Fatalf("runtime=%+v %v", record.Run, err)
	}
	projected, err := domain.ApplySynthesisDelta(execution.WorkspaceID, downstream.Revision.Items, generated.Notes[0].Delta, []domain.SynthesisSourceRef{input.Sources[0].Reference, input.Sources[1].Reference})
	if err != nil || !projected.Changed || !reflect.DeepEqual(projected.Items[:2], downstream.Revision.Items[:2]) || projected.Items[2].ID != included.Item.ID {
		t.Fatalf("projection=%+v %v", projected, err)
	}
	if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input); err != nil || harness.provider.CallCount() != 2 {
		t.Fatalf("replay calls=%d %v", harness.provider.CallCount(), err)
	}
	var changedPublication app.SynthesisGenerationInput
	encodedInput, _ := json.Marshal(input)
	if err := json.Unmarshal(encodedInput, &changedPublication); err != nil {
		t.Fatal(err)
	}
	changedPublication.BodyRefresh.Request.PublicationID = synthesisTestID(370)
	changedPublication.BodyRefresh.Items[0].Updated.PublicationID = synthesisTestID(370)
	if _, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, changedPublication); err == nil || harness.provider.CallCount() != 2 {
		t.Fatal("another publication reused model proof or invoked the provider")
	}
	noChange, err := bindSynthesisOutput([]byte(`{"notes":[]}`), input, synthesisTestID(371), (&synthesisTestIDs{}).New)
	if err != nil {
		t.Fatal(err)
	}
	noChangePlan, err := synthesisSemanticPlan(input, noChange)
	if err != nil || len(noChangePlan.Checks) != 2 || noChangePlan.Checks[0].Kind != "NO_CHANGE" || noChangePlan.Checks[1].Kind != "REFRESH_SCOPE" || len(noChangePlan.RefreshUpdates) != 1 {
		t.Fatalf("no-change omitted independent comparison: %+v %v", noChangePlan, err)
	}
	for _, decoder := range []func([]byte) (json.RawMessage, error){decodeSynthesisDelta, decodeSynthesisBodyDelta} {
		if _, err := decoder([]byte(output)); err == nil {
			t.Fatal("legacy protocol accepted refresh")
		}
	}
	wrong := `{"notes":[{"note":"N001","operations":[{"kind":"REFRESH_ITEM","target":"I001"}]}]}`
	if _, err := bindSynthesisOutput([]byte(wrong), input, synthesisTestID(361), (&synthesisTestIDs{}).New); err == nil {
		t.Fatal("unrelated item refresh accepted")
	}
	review := synthesisReviewForPlan(t, plan, "UNCERTAIN")
	verdict, err := bindSynthesisSemanticOutput(review, plan, input, generated, synthesisTestID(362))
	if err != nil || verdict.Accepted {
		t.Fatal("uncertain scope/evidence accepted")
	}
}
