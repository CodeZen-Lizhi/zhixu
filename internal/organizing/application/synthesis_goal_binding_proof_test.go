package application

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestSynthesisGoalBindingProofTraversesExplicitEmptySelectionsAndAllSpans(t *testing.T) {
	binding, progress, pages := goalBindingProofFixture(t)
	reader := &goalBindingProofReader{progress: progress, pages: pages}
	proof, err := NewSynthesisGoalBindingProof(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.VerifySynthesisGoalBinding(t.Context(), progress.Request.WorkspaceID, binding); err != nil {
		t.Fatal(err)
	}
	if len(reader.queries) != 3 || reader.queries[0].AfterSelectionID != "" || reader.queries[1].AfterSelectionID != synthesisContractID(209) || reader.queries[2].AfterSelectionID != synthesisContractID(210) {
		t.Fatalf("empty selection stopped proof traversal: %#v", reader.queries)
	}
}

func TestSynthesisGoalBindingProofRejectsDriftAndIncompleteProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SynthesisGoalBinding, *SynthesisGoalSelectionProgress, map[foundation.ID]GoalSelectionResultPage)
	}{
		{"goal text", func(_ *SynthesisGoalBinding, progress *SynthesisGoalSelectionProgress, _ map[foundation.ID]GoalSelectionResultPage) {
			progress.Request.Goal = "另一份目标"
		}},
		{"not terminal", func(_ *SynthesisGoalBinding, progress *SynthesisGoalSelectionProgress, _ map[foundation.ID]GoalSelectionResultPage) {
			progress.Pending, progress.Succeeded = 1, progress.Succeeded-1
		}},
		{"changed model run", func(_ *SynthesisGoalBinding, _ *SynthesisGoalSelectionProgress, pages map[foundation.ID]GoalSelectionResultPage) {
			changeGoalBindingProofPage(pages, synthesisContractID(209), func(page *GoalSelectionResultPage) { page.Items[0].ModelRunID = synthesisContractID(999) })
		}},
		{"omitted selected point", func(_ *SynthesisGoalBinding, _ *SynthesisGoalSelectionProgress, pages map[foundation.ID]GoalSelectionResultPage) {
			changeGoalBindingProofPage(pages, synthesisContractID(210), func(page *GoalSelectionResultPage) { page.Items[0].Points = []SynthesisGoalSelectedPoint{} })
		}},
		{"extra selected point", func(_ *SynthesisGoalBinding, _ *SynthesisGoalSelectionProgress, pages map[foundation.ID]GoalSelectionResultPage) {
			changeGoalBindingProofPage(pages, synthesisContractID(209), func(page *GoalSelectionResultPage) {
				point := page.Items[0].Points[0]
				point.Locator.Index++
				page.Items[0].Points = append(page.Items[0].Points, point)
			})
		}},
		{"missing one span", func(_ *SynthesisGoalBinding, _ *SynthesisGoalSelectionProgress, pages map[foundation.ID]GoalSelectionResultPage) {
			changeGoalBindingProofPage(pages, synthesisContractID(209), func(page *GoalSelectionResultPage) {
				page.Items[0].Points[0].SourceSpanIDs = page.Items[0].Points[0].SourceSpanIDs[:1]
			})
		}},
		{"cross goal page", func(_ *SynthesisGoalBinding, _ *SynthesisGoalSelectionProgress, pages map[foundation.ID]GoalSelectionResultPage) {
			changeGoalBindingProofPage(pages, synthesisContractID(210), func(page *GoalSelectionResultPage) { page.Progress.Request.ID = synthesisContractID(998) })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding, progress, pages := goalBindingProofFixture(t)
			test.mutate(binding, &progress, pages)
			proof, err := NewSynthesisGoalBindingProof(&goalBindingProofReader{progress: progress, pages: pages})
			if err != nil {
				t.Fatal(err)
			}
			if err := proof.VerifySynthesisGoalBinding(t.Context(), progress.Request.WorkspaceID, binding); err == nil {
				t.Fatal("drifted durable selection proof was accepted")
			}
		})
	}
}

func TestSynthesisGoalBindingProofRejectsBudgetAndPropagatesReaderFailure(t *testing.T) {
	binding, progress, pages := goalBindingProofFixture(t)
	progress.Selections = MaxGoalBindingProofSelections + 1
	progress.Succeeded = progress.Selections
	proof, err := NewSynthesisGoalBindingProof(&goalBindingProofReader{progress: progress, pages: pages})
	if err != nil {
		t.Fatal(err)
	}
	assertOrganizingApplicationError(t, proof.VerifySynthesisGoalBinding(t.Context(), progress.Request.WorkspaceID, binding), foundation.ErrorNonRetryableFailure, "SYNTHESIS_GOAL_SELECTION_PROOF_TOO_LARGE")
	want := errors.New("reader stopped")
	proof, err = NewSynthesisGoalBindingProof(&goalBindingProofReader{err: want})
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.VerifySynthesisGoalBinding(t.Context(), progress.Request.WorkspaceID, binding); !errors.Is(err, want) {
		t.Fatalf("reader error = %v, want %v", err, want)
	}
}

func goalBindingProofFixture(t *testing.T) (*SynthesisGoalBinding, SynthesisGoalSelectionProgress, map[foundation.ID]GoalSelectionResultPage) {
	t.Helper()
	prepared, _, _ := goalPreparedFixture(t)
	// 一个选中知识点有两个精确片段。绑定保留两者，
	// 不能把它们折叠成任意一个代表性原文片段。
	extra := prepared.Sources[0]
	extra.Excerpt.Reference.SourceSpanID = synthesisContractID(240)
	extra.Excerpt.Text = "缓存资料的另一段原文。"
	extra.Excerpt.Reference.ExcerptHash = synthesisContractHash(extra.Excerpt.Text)
	extra.Points = append([]GoalSourcePointBinding(nil), prepared.Sources[0].Points...)
	prepared.Sources = append(prepared.Sources, extra)
	binding, err := BuildSynthesisGoalBinding(prepared)
	if err != nil {
		t.Fatal(err)
	}
	progress := prepared.Progress
	progress.Selections, progress.Succeeded = 3, 3
	emptyID := synthesisContractID(209)
	first := goalBindingProofSelection(binding.Materials[0], binding.Materials[2])
	second := goalBindingProofSelection(binding.Materials[1])
	pages := map[foundation.ID]GoalSelectionResultPage{
		"": {
			Progress:             progress,
			Items:                []GoalSelectionResult{{SelectionID: emptyID, ModelRunID: synthesisContractID(219), Points: []SynthesisGoalSelectedPoint{}}},
			NextAfterSelectionID: emptyID,
		},
		emptyID: {
			Progress:             progress,
			Items:                []GoalSelectionResult{first},
			NextAfterSelectionID: first.SelectionID,
		},
		first.SelectionID: {
			Progress: progress,
			Items:    []GoalSelectionResult{second},
		},
	}
	return binding, progress, pages
}

func goalBindingProofSelection(materials ...GoalSourceMaterial) GoalSelectionResult {
	selection := GoalSelectionResult{SelectionID: materials[0].Points[0].SelectionID, ModelRunID: materials[0].Points[0].ModelRunID, Points: []SynthesisGoalSelectedPoint{}}
	byPoint := map[goalBindingProofPoint]int{}
	for _, material := range materials {
		for _, binding := range material.Points {
			key := goalBindingProofPoint{SelectionID: binding.SelectionID, ModelRunID: binding.ModelRunID, Source: material.Reference.Source, Locator: binding.Locator, Reason: binding.Reason}
			index, exists := byPoint[key]
			if !exists {
				index = len(selection.Points)
				byPoint[key] = index
				selection.Points = append(selection.Points, SynthesisGoalSelectedPoint{Source: material.Reference.Source, Locator: binding.Locator, Reason: binding.Reason, SourceSpanIDs: []foundation.ID{}})
			}
			selection.Points[index].SourceSpanIDs = append(selection.Points[index].SourceSpanIDs, material.Reference.SourceSpanID)
		}
	}
	return selection
}

func changeGoalBindingProofPage(pages map[foundation.ID]GoalSelectionResultPage, id foundation.ID, change func(*GoalSelectionResultPage)) {
	page := pages[id]
	change(&page)
	pages[id] = page
}

type goalBindingProofReader struct {
	progress SynthesisGoalSelectionProgress
	pages    map[foundation.ID]GoalSelectionResultPage
	err      error
	queries  []GoalSelectionResultQuery
}

func (r *goalBindingProofReader) ReadGoalSelectionProgress(context.Context, foundation.ID, foundation.ID) (SynthesisGoalSelectionProgress, error) {
	return r.progress, r.err
}

func (r *goalBindingProofReader) ReadGoalSelectionResults(_ context.Context, query GoalSelectionResultQuery) (GoalSelectionResultPage, error) {
	r.queries = append(r.queries, query)
	if r.err != nil {
		return GoalSelectionResultPage{}, r.err
	}
	return r.pages[query.AfterSelectionID], nil
}

var _ GoalSelectionResultReader = (*goalBindingProofReader)(nil)
