package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	interviewagent "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/adapter/agent"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestFixtureSynthesisNoteInterviewUsesFrozenLabelsAndExactQuestionCount(t *testing.T) {
	server, _ := newRecordingFixtureServer(t)
	model, _ := newProductionStructuredRuntime(t, server)
	catalog := agentapplication.NewRuntimeCatalog()
	if err := interviewagent.RegisterNoteInterviewRuntimeCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	snapshots, err := synthesisFixtureSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	profile := snapshots[synthesisFixtureGenerateStage].Profile
	if err := catalog.RegisterProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot(interviewapplication.NoteModelPromptRef(), interviewapplication.NoteModelSchemaRef(), interviewapplication.NoteModelSchemaRef(), profile.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if stage := synthesisNotesFixtureStage(snapshot.Schema.JSONSchema); stage != synthesisFixtureInterviewStage {
		t.Fatalf("note interview owner schema stage=%s", stage)
	}
	for _, scenario := range []struct {
		name      string
		conflict  bool
		followUps int
	}{
		{"published_v1_fact_gap_three_questions", false, 1},
		{"all_kinds", true, 1},
		{"follow_ups_disabled", false, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			preparation := synthesisInterviewTestPreparation(t, scenario.conflict, scenario.followUps)
			inputJSON, err := interviewapplication.EncodeNotePlanInput(preparation)
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]any
			if err := json.Unmarshal(inputJSON, &input); err != nil {
				t.Fatal(err)
			}
			output := callSynthesisProductionFixture(t, model, snapshot, input)
			raw, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := interviewapplication.DecodeNotePlan(raw, preparation)
			if err != nil {
				t.Fatalf("owner could not bind fixture plan: %v", err)
			}
			if len(plan) != 3 || plan[0].Source.ItemKind != organizingdomain.SynthesisFactItem || plan[0].Source.ItemID != preparation.Snapshot.Items[1].ID || len(plan[0].Source.Sources) != 1 {
				t.Fatalf("first question did not select the actual sourced FACT: %+v", plan)
			}
			if len(plan[0].FollowUps) != scenario.followUps || len(plan[0].AnswerPoints) != 2 {
				t.Fatal("question lost source answer points or follow-up option")
			}
			if scenario.conflict {
				if plan[1].Source.ItemKind != organizingdomain.SynthesisConflictItem || len(plan[1].AnswerPoints) != 4 || plan[2].Source.ItemKind != organizingdomain.SynthesisGapItem {
					t.Fatal("plan omitted a conflict condition or GAP kind")
				}
			} else if plan[1].Source.ItemKind != organizingdomain.SynthesisGapItem || plan[2].Source.ItemKind != organizingdomain.SynthesisFactItem {
				t.Fatal("published v1 did not cover FACT/GAP with exactly three questions")
			}
			if strings.Contains(plan[0].Prompt, synthesisFixtureFact) || strings.Contains(plan[0].Prompt, synthesisFixtureApplicability) {
				t.Fatal("question leaked the expected answer text")
			}
		})
	}
}

func TestFixtureSynthesisNoteInterviewRejectsUnboundPointsAndImpossibleCoverage(t *testing.T) {
	preparation := synthesisInterviewTestPreparation(t, true, 1)
	for _, mutation := range []func(map[string]any){
		func(input map[string]any) { input["workspace_id"] = "private-canary" },
		func(input map[string]any) { input["options"].(map[string]any)["question_count"] = float64(1) },
		func(input map[string]any) {
			input["items"].([]any)[1].(map[string]any)["answer_points"].([]any)[0].(map[string]any)["label"] = "P999"
		},
	} {
		raw, err := interviewapplication.EncodeNotePlanInput(preparation)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		mutation(input)
		if _, err := synthesisNotesFixtureResponse(synthesisFixtureInterviewStage, input); err == nil {
			t.Fatal("unbound note interview input accepted")
		}
	}
}

// This unit fixture proves the Provider/owner contract. It is not a substitute
// for the separately verified PostgreSQL publication and frozen-session facts.
func synthesisInterviewTestPreparation(t *testing.T, withConflict bool, followUps int) interviewapplication.NotePreparation {
	t.Helper()
	id := func(n int) foundation.ID { return foundation.ID(fmt.Sprintf("69000000-0000-4000-8000-%012d", n)) }
	source := organizingdomain.SynthesisSourceRef{
		Source:       organizingdomain.SynthesisSourceVersion{WorkspaceID: id(1), SourceID: id(11), SourceVersionID: id(12), ContentArtifactID: id(13), ParseProjectionID: id(14), ContentHash: strings.Repeat("a", 64)},
		SourceSpanID: id(15), ExcerptHash: strings.Repeat("b", 64), Title: "Original cache material",
	}
	items := []organizingdomain.SynthesisItem{
		{ID: id(21), Kind: organizingdomain.SynthesisGapItem, Gap: &organizingdomain.SynthesisGapContent{Question: synthesisFixtureGap, Context: synthesisFixtureGapContext, Sources: []organizingdomain.SynthesisSourceRef{source}}},
		{ID: id(22), Kind: organizingdomain.SynthesisFactItem, Fact: &organizingdomain.SynthesisStatement{Text: synthesisFixtureFact, Applicability: synthesisFixtureApplicability, Sources: []organizingdomain.SynthesisSourceRef{source}}},
	}
	if withConflict {
		items = append(items, organizingdomain.SynthesisItem{ID: id(23), Kind: organizingdomain.SynthesisConflictItem, Conflict: &organizingdomain.SynthesisConflictContent{
			Subject: "Cache expiration depends on the configured mode.", Alternatives: []organizingdomain.SynthesisStatement{
				{Text: synthesisFixtureFact, Applicability: synthesisFixtureApplicability, Sources: []organizingdomain.SynthesisSourceRef{source}},
				{Text: "Cache entries expire after ten minutes.", Applicability: "For the high-traffic configuration.", Sources: []organizingdomain.SynthesisSourceRef{source}},
			},
		}})
	}
	snapshot := organizingdomain.SynthesisNoteSnapshot{WorkspaceID: id(1), NoteID: id(2), RevisionID: id(3), RevisionNo: 1, DocumentID: id(4), ArticleRevisionID: id(5), ArticleRevisionNo: 1,
		ProjectionHash: strings.Repeat("c", 64), Title: synthesisFixtureTitle, RendererVersion: organizingdomain.SynthesisRendererVersion, Items: items}
	body, err := organizingdomain.RenderSynthesisMarkdown(snapshot.WorkspaceID, snapshot.NoteID, snapshot.Title, snapshot.Items)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ContentHash = interviewapplication.NoteOutputHash([]byte(body))
	ref, err := interviewdomain.NoteRevisionFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC)
	preparation := interviewapplication.NotePreparation{ID: id(6), WorkspaceID: id(1), NoteRevision: ref, Snapshot: snapshot,
		Options: interviewapplication.NoteInterviewOptions{Role: "Backend engineer", Difficulty: interviewdomain.DifficultyIntermediate, DurationMinutes: 30, QuestionCount: 3, MaxFollowUps: followUps},
		Status:  interviewapplication.NotePreparationGenerating, WorkflowRunID: id(7), NodeRunID: id(8), NodeAttemptID: id(9), Version: 2,
		IdempotencyKey: "synthesis-fixture", RequestHash: strings.Repeat("d", 64), CreatedAt: at, UpdatedAt: at}
	if err := preparation.Validate(); err != nil {
		t.Fatal(err)
	}
	return preparation
}
