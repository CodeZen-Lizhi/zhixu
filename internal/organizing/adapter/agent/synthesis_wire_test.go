package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestSynthesisProviderWireRejectsMissingUnknownDuplicateAndUnionDrift(t *testing.T) {
	for _, raw := range []string{synthesisFactOutput, " \n" + synthesisFactOutput + "\n", `{"notes":[]}`, `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":null,"sources":["S001"]}]}]}`} {
		accepted, err := decodeSynthesisDelta([]byte(raw))
		if err != nil || string(accepted) != raw {
			t.Fatalf("valid wire rejected or rewritten: %v", err)
		}
	}
	cases := map[string]string{
		"missing root":                 `{}`,
		"null root array":              `{"notes":null}`,
		"unknown root":                 strings.Replace(synthesisFactOutput, `{"notes":`, `{"verified":true,"notes":`, 1),
		"case variant":                 strings.Replace(synthesisFactOutput, `"notes":`, `"Notes":`, 1),
		"duplicate root":               `{"notes":[],"notes":[]}`,
		"escaped duplicate":            `{"notes":[],"not\u0065s":[]}`,
		"missing title":                strings.Replace(synthesisFactOutput, `"title":"Cache lifetime",`, "", 1),
		"missing aliases":              strings.Replace(synthesisFactOutput, `"aliases":[],`, "", 1),
		"null aliases":                 strings.Replace(synthesisFactOutput, `"aliases":[]`, `"aliases":null`, 1),
		"missing applicability":        strings.Replace(synthesisFactOutput, `"applicability":"",`, "", 1),
		"null applicability":           strings.Replace(synthesisFactOutput, `"applicability":""`, `"applicability":null`, 1),
		"missing operation":            strings.Replace(synthesisFactOutput, `"op":"ADD_FACT",`, "", 1),
		"unknown operation":            strings.Replace(synthesisFactOutput, "ADD_FACT", "REPLACE_ALL", 1),
		"mixed operation":              strings.Replace(synthesisFactOutput, `"op":"ADD_FACT",`, `"op":"ADD_FACT","target":"I001",`, 1),
		"unknown statement":            strings.Replace(synthesisFactOutput, `"text":`, `"verified":true,"text":`, 1),
		"duplicate statement key":      strings.Replace(synthesisFactOutput, `"applicability":""`, `"applicability":"","applicability":""`, 1),
		"empty evidence":               strings.Replace(synthesisFactOutput, `["S001"]`, `[]`, 1),
		"duplicate label":              strings.Replace(synthesisFactOutput, `["S001"]`, `["S001","S001"]`, 1),
		"null label":                   strings.Replace(synthesisFactOutput, `["S001"]`, `[null]`, 1),
		"invalid label range":          strings.ReplaceAll(synthesisFactOutput, "S001", "S257"),
		"identity as label":            strings.ReplaceAll(synthesisFactOutput, "S001", string(synthesisTestID(3))),
		"noncanonical key":             strings.Replace(synthesisFactOutput, `"topic_key":"cache lifetime"`, `"topic_key":"Cache Lifetime"`, 1),
		"duplicate alias":              strings.Replace(synthesisFactOutput, `"aliases":[]`, `"aliases":["cache","cache"]`, 1),
		"control text":                 strings.Replace(synthesisFactOutput, "Entries expire after five minutes.", `Entries\nexpire`, 1),
		"invalid unicode":              strings.Replace(synthesisFactOutput, "Entries expire after five minutes.", `\ud800`, 1),
		"trailing value":               synthesisFactOutput + ` {}`,
		"trailing garbage":             synthesisFactOutput + ` ignored`,
		"fenced json":                  "```json\n" + synthesisFactOutput + "\n```",
		"empty new delta":              `{"notes":[{"note":"","topic_key":"cache","title":"Cache","aliases":[],"operations":[]}]}`,
		"existing metadata":            `{"notes":[{"note":"N001","title":"Edit","operations":[]}]}`,
		"missing nullable alternative": `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","sources":["S001"]}]}]}`,
		"negative alternative":         `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":-1,"sources":["S001"]}]}]}`,
		"false alternative":            `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":false,"sources":["S001"]}]}]}`,
		"gap pre-resolved":             `{"notes":[{"note":"N001","operations":[{"op":"ADD_GAP","question":"How?","context":"","sources":[],"resolution":null}]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSynthesisDelta([]byte(raw)); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
				t.Fatalf("invalid wire accepted: %v", err)
			}
		})
	}
}

func TestSynthesisPublishedBodyIncludeBindsExactItemAndReviewsEveryCopiedPart(t *testing.T) {
	input, _, _ := synthesisFixtureWithNote(t)
	input.Notes[0].PublicationID = synthesisTestID(80)
	gap := input.Notes[0].Revision.Items[2].Gap
	gap.Resolution = &domain.SynthesisStatement{Text: "Keep at most 100 entries.", Sources: []domain.SynthesisSourceRef{input.Sources[0].Reference}}
	unresolvedGap := domain.SynthesisItem{ID: input.Notes[0].Revision.Items[2].ID, Kind: domain.SynthesisGapItem,
		Gap: &domain.SynthesisGapContent{Question: gap.Question, Context: gap.Context, Sources: append([]domain.SynthesisSourceRef{}, gap.Sources...)}}
	input.Notes[0].Revision.Delta = domain.SynthesisDelta{Operations: []domain.SynthesisOperation{
		{Kind: domain.SynthesisAddFact, Item: &input.Notes[0].Revision.Items[0]},
		{Kind: domain.SynthesisAddConflict, Item: &input.Notes[0].Revision.Items[1]},
		{Kind: domain.SynthesisAddGap, Item: &unresolvedGap},
		{Kind: domain.SynthesisResolveGap, TargetItemID: unresolvedGap.ID, Resolution: gap.Resolution},
	}}
	body, err := domain.RenderSynthesisMarkdown(input.Notes[0].Note.WorkspaceID, input.Notes[0].Note.ID, input.Notes[0].Revision.Title, input.Notes[0].Revision.Items)
	if err != nil {
		t.Fatal(err)
	}
	input.Notes[0].Revision.ContentHash = synthesisHash([]byte(body))
	input.Notes[0].Revision.Hash, err = domain.ComputeSynthesisRevisionHash(input.Notes[0].Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := `{"notes":[{"note":"","topic_key":"included cache","title":"Included cache","aliases":[],"operations":[
{"op":"INCLUDE_ITEM","note":"N001","item":"I001"},
{"op":"INCLUDE_ITEM","note":"N001","item":"I002"},
{"op":"INCLUDE_ITEM","note":"N001","item":"I003"}
]}]}`
	result, err := bindSynthesisOutput([]byte(raw), input, synthesisTestID(81), (&synthesisTestIDs{}).New)
	if err != nil {
		t.Fatal(err)
	}
	operations := result.Notes[0].Delta.Operations
	if len(operations) != 3 || operations[0].Item.BodyReference == nil || operations[0].Item.BodyReference.PublicationID != input.Notes[0].PublicationID ||
		operations[0].Item.Fact.Applicability != input.Notes[0].Revision.Items[0].Fact.Applicability ||
		!reflect.DeepEqual(operations[1].Item.Conflict.Alternatives, input.Notes[0].Revision.Items[1].Conflict.Alternatives) ||
		operations[2].Item.Gap.Resolution == nil || !reflect.DeepEqual(operations[2].Item.Gap.Resolution, input.Notes[0].Revision.Items[2].Gap.Resolution) {
		t.Fatalf("included items lost exact published content: %+v", operations)
	}
	plan, err := synthesisSemanticPlan(input, result)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(plan.Checks))
	for index, check := range plan.Checks {
		got[index] = check.Kind
		if len(check.Sources) > 0 && !reflect.DeepEqual(check.Sources, []string{"S001"}) {
			t.Fatalf("check %s did not review exact copied evidence: %+v", check.Kind, check)
		}
	}
	want := []string{"ASSERTION", "CONFLICT_ALTERNATIVE", "CONFLICT_ALTERNATIVE", "CONFLICT_RELATION", "GAP_CONTEXT", "GAP_RESOLUTION"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic checks=%v want=%v", got, want)
	}
	if _, err := decodeSynthesisDelta([]byte(raw)); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
		t.Fatalf("v1 decoder accepted INCLUDE_ITEM: %v", err)
	}
}

func TestSynthesisPublishedBodyIncludeRejectsDraftSelfUnknownAndUnopenedReferences(t *testing.T) {
	input, _, _ := synthesisFixtureWithNote(t)
	input.Notes[0].PublicationID = synthesisTestID(82)
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"self":    `{"notes":[{"note":"N001","operations":[{"op":"INCLUDE_ITEM","note":"N001","item":"I001"}]}]}`,
		"unknown": `{"notes":[{"note":"","topic_key":"included cache","title":"Included cache","aliases":[],"operations":[{"op":"INCLUDE_ITEM","note":"N002","item":"I001"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := bindSynthesisOutput([]byte(raw), input, synthesisTestID(83), (&synthesisTestIDs{}).New); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
	draft := input
	draft.Notes = append(draft.Notes, synthesisDuplicateGenerationNote(t, input.Notes[0], 85))
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
	draftRaw := `{"notes":[{"note":"","topic_key":"included cache","title":"Included cache","aliases":[],"operations":[{"op":"INCLUDE_ITEM","note":"N002","item":"I001"}]}]}`
	if _, err := bindSynthesisOutput([]byte(draftRaw), draft, synthesisTestID(85), (&synthesisTestIDs{}).New); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
		t.Fatalf("draft include err=%v", err)
	}
	unopened := input
	unopened.Sources = unopened.Sources[1:]
	if err := unopened.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := `{"notes":[{"note":"","topic_key":"included cache","title":"Included cache","aliases":[],"operations":[{"op":"INCLUDE_ITEM","note":"N001","item":"I001"}]}]}`
	if _, err := bindSynthesisOutput([]byte(raw), unopened, synthesisTestID(84), (&synthesisTestIDs{}).New); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
		t.Fatalf("unopened include err=%v", err)
	}
}

func synthesisDuplicateGenerationNote(t *testing.T, source organizingapp.SynthesisGenerationNote, base int) organizingapp.SynthesisGenerationNote {
	t.Helper()
	result := source
	result.PublicationID = ""
	result.Note.ID, result.Note.DocumentID, result.Note.CurrentRevisionID = synthesisTestID(base), synthesisTestID(base+1), synthesisTestID(base+2)
	result.Revision.ID, result.Revision.NoteID, result.Revision.DocumentID = result.Note.CurrentRevisionID, result.Note.ID, result.Note.DocumentID
	result.Revision.ArticleRevisionID = synthesisTestID(base + 3)
	body, err := domain.RenderSynthesisMarkdown(result.Revision.WorkspaceID, result.Revision.NoteID, result.Revision.Title, result.Revision.Items)
	if err != nil {
		t.Fatal(err)
	}
	result.Revision.ContentHash = synthesisHash([]byte(body))
	hash, err := domain.ComputeSynthesisRevisionHash(result.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result.Revision.Hash = hash
	return result
}

func TestSynthesisSemanticPlanChecksEveryAssertionAndExactSupport(t *testing.T) {
	input, _, _ := synthesisFixtureWithNote(t)
	raw := `{"notes":[{"note":"N001","operations":[
{"op":"ADD_SUPPORT","target":"I001","alternative":null,"sources":["S002"]},
{"op":"ADD_SUPPORT","target":"I002","alternative":1,"sources":["S002"]},
{"op":"RESOLVE_GAP","target":"I003","resolution":{"text":"Keep at most 100 entries.","applicability":"","sources":["S002"]}},
{"op":"ADD_FACT","statement":{"text":"Size limits bound memory.","applicability":"","sources":["S002"]}},
{"op":"ADD_CONFLICT","subject":"Expiration interval","alternatives":[{"text":"Use five minutes.","applicability":"Stable values","sources":["S002"]},{"text":"Use one minute.","applicability":"Changing values","sources":["S002"]}]},
{"op":"ADD_GAP","question":"What is the failure policy?","context":"","sources":["S002"]}
]}]}`
	ids := &synthesisTestIDs{}
	result, err := bindSynthesisOutput([]byte(raw), input, synthesisTestID(50), ids.New)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := synthesisSemanticPlan(input, result)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(plan.Checks))
	for index, check := range plan.Checks {
		kinds[index] = check.Kind
	}
	expected := []string{"ADDITIONAL_SUPPORT", "ADDITIONAL_SUPPORT", "GAP_RESOLUTION", "ASSERTION", "CONFLICT_ALTERNATIVE", "CONFLICT_ALTERNATIVE", "CONFLICT_RELATION", "GAP_CONTEXT"}
	if !reflect.DeepEqual(kinds, expected) {
		t.Fatalf("check kinds=%v", kinds)
	}
	if plan.Checks[0].Text != input.Notes[0].Revision.Items[0].Fact.Text || plan.Checks[1].Text != input.Notes[0].Revision.Items[1].Conflict.Alternatives[1].Text ||
		plan.Checks[2].Question != input.Notes[0].Revision.Items[2].Gap.Question || plan.Checks[2].Text != "Keep at most 100 entries." || len(plan.Checks[6].Alternatives) != 2 {
		t.Fatal("semantic input lost immutable target text, conditions, or the original gap question")
	}
	for index, check := range plan.Checks {
		if index != 6 && !reflect.DeepEqual(check.Sources, []string{"S002"}) {
			t.Fatal("semantic input used the old support instead of checking each newly cited source")
		}
	}
	providerOutput := synthesisReviewForPlan(t, plan, "SUPPORTED")
	receipt, err := bindSynthesisSemanticOutput(providerOutput, plan, input, result, synthesisTestID(51))
	if err != nil || !receipt.Accepted || receipt.CheckCount != len(expected) {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	changed := strings.Replace(string(providerOutput), `"source":"S002"`, `"source":"S001"`, 1)
	if _, err := bindSynthesisSemanticOutput([]byte(changed), plan, input, result, synthesisTestID(51)); err == nil {
		t.Fatal("review of a different original source was accepted")
	}
	badVerdict := strings.ReplaceAll(string(providerOutput), "SUPPORTED", "UNCERTAIN")
	receipt, err = bindSynthesisSemanticOutput([]byte(badVerdict), plan, input, result, synthesisTestID(51))
	if err != nil || receipt.Accepted {
		t.Fatal("uncertain semantic evidence was accepted")
	}
}

func TestSynthesisSemanticWireRejectsMissingChecksAndSelfAttestation(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":                         `{"checks":[]}`,
		"missing checks":                `{}`,
		"null checks":                   `{"checks":null}`,
		"no index":                      strings.Replace(synthesisFactReview, `"index":1,`, "", 1),
		"null index":                    strings.Replace(synthesisFactReview, `"index":1`, `"index":null`, 1),
		"wrong index":                   strings.Replace(synthesisFactReview, `"index":1`, `"index":2`, 1),
		"boolean verified":              strings.Replace(synthesisFactReview, `"checks":`, `"verified":true,"checks":`, 1),
		"missing source verdict":        strings.Replace(synthesisFactReview, `{"source":"S001","verdict":"SUPPORTED"}`, `{"source":"S001"}`, 1),
		"contradictory overall verdict": strings.Replace(synthesisFactReview, `"source":"S001","verdict":"SUPPORTED"`, `"source":"S001","verdict":"UNSUPPORTED"`, 1),
		"duplicate source":              strings.Replace(synthesisFactReview, `[{"source":"S001","verdict":"SUPPORTED"}]`, `[{"source":"S001","verdict":"SUPPORTED"},{"source":"S001","verdict":"SUPPORTED"}]`, 1),
		"tail":                          synthesisFactReview + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSynthesisSemantic([]byte(raw)); err == nil {
				t.Fatal("invalid semantic wire accepted")
			}
		})
	}
}

func TestSynthesisIncrementalModelKeepsExistingTextAndAddsOnlySource(t *testing.T) {
	output := `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":null,"sources":["S002"]}]}]}`
	harness := newSynthesisHarness(t, output, strings.ReplaceAll(synthesisFactReview, "S001", "S002"))
	input, execution, validation := synthesisFixtureWithNote(t)
	result, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result); err != nil {
		t.Fatal(err)
	}
	available := []domain.SynthesisSourceRef{input.Sources[0].Reference, input.Sources[1].Reference}
	applied, err := domain.ApplySynthesisDelta(input.SourceEvent.Source.WorkspaceID, input.Notes[0].Revision.Items, result.Notes[0].Delta, available)
	if err != nil {
		t.Fatal(err)
	}
	before := input.Notes[0].Revision.Items
	if applied.Changed || !applied.SourcesChanged || applied.Items[0].ID != before[0].ID || applied.Items[0].Fact.Text != before[0].Fact.Text || len(applied.Items[0].Fact.Sources) != 2 ||
		!reflect.DeepEqual(applied.Items[1:], before[1:]) {
		t.Fatal("incremental model result rewrote existing text or unrelated items")
	}
	if result.Notes[0].NoteID != input.Notes[0].Note.ID || result.Notes[0].BaseRevisionID != input.Notes[0].Revision.ID {
		t.Fatal("existing note was not bound to its frozen base")
	}
}

func TestSynthesisAnchoredNoteRejectsUnacceptedIncomingSpan(t *testing.T) {
	input, _, _ := synthesisFixtureWithNote(t)
	accepted := input.Sources[1].Reference
	other := input.Sources[1]
	other.Reference.SourceSpanID = synthesisTestID(70)
	other.Text = "RAW-OTHER-SPAN: unrelated cache evidence"
	other.Reference.ExcerptHash = synthesisHash([]byte(other.Text))
	input.Sources = append(input.Sources, other)
	input.Notes[0].Anchor = &organizingapp.SynthesisAnchorBinding{
		AnchorID: synthesisTestID(60), ScopeVersion: 1,
		Scope:          domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"Interview"}, Description: "Redis revision"},
		AllowedSources: []domain.SynthesisSourceRef{accepted},
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":null,"sources":["S003"]}]}]}`
	if _, err := bindSynthesisOutput([]byte(raw), input, synthesisTestID(61), (&synthesisTestIDs{}).New); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
		t.Fatalf("anchored output cited unaccepted source: %v", err)
	}
}

func TestSynthesisSemanticReviewRequiresEveryExactCitedSource(t *testing.T) {
	input, execution, validation := synthesisFixtureWithNote(t)
	output := `{"notes":[{"note":"N001","operations":[{"op":"ADD_SUPPORT","target":"I001","alternative":null,"sources":["S001","S002"]}]}]}`
	harness := newSynthesisHarness(t, output, synthesisFactReview)
	result, err := harness.model.GenerateSynthesisForExecution(context.Background(), execution, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.model.ValidateSynthesisSemanticsForExecution(context.Background(), validation, input, result); synthesisTestCode(err) != organizingapp.ErrorCodeSynthesisModelOutputInvalid {
		t.Fatalf("partial citation review err=%v", err)
	}
	if harness.provider.CallCount() != 2 {
		t.Fatal("partial semantic response caused an unbounded retry")
	}
}

func synthesisReviewForPlan(t *testing.T, plan synthesisSemanticInput, verdict string) []byte {
	t.Helper()
	checks := make([]map[string]any, 0, len(plan.Checks))
	for _, check := range plan.Checks {
		sources := make([]map[string]string, 0, len(check.Sources))
		for _, label := range check.Sources {
			sources = append(sources, map[string]string{"source": label, "verdict": verdict})
		}
		checks = append(checks, map[string]any{"index": check.Index, "verdict": verdict, "sources": sources})
	}
	raw, err := json.Marshal(map[string]any{"checks": checks})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var _ foundation.IDGenerator = (*synthesisTestIDs)(nil)
