package application

import (
	"encoding/json"
	"fmt"

	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// BuildAnchorRecommendationInput 根据一个冻结请求构造不含身份标识的模型材料。
// 片段原文必须已由其所属模块打开并核验。
func BuildAnchorRecommendationInput(request AnchorRecommendationRequest, revision domain.SynthesisRevision, anchor *domain.Anchor, opened []SynthesisSourceView) ([]byte, error) {
	if _, err := BindAnchorModelOutput([]byte(`{"recommendation":null,"no_recommendation":true}`), request); err != nil {
		return nil, err
	}
	if revision.Validate() != nil || revision.ID != request.BasisRevisionID || revision.NoteID != request.NoteID || revision.WorkspaceID != request.WorkspaceID || len(opened) != len(request.Evidence) {
		return nil, AnchorConflict()
	}
	var confirmed *domain.AnchorScope
	var anchorTitle *string
	if request.Kind == AnchorInitialScopeRecommendation {
		if anchor != nil {
			return nil, AnchorConflict()
		}
	} else {
		if anchor == nil || anchor.Validate() != nil || anchor.ID != request.AnchorID || anchor.NoteID != request.NoteID || anchor.WorkspaceID != request.WorkspaceID || anchor.ScopeVersion != request.ExpectedScopeVersion {
			return nil, AnchorConflict()
		}
		confirmed = &anchor.Scope
		anchorTitle = &anchor.Title
	}
	type excerpt struct {
		Label string `json:"label"`
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	excerpts := make([]excerpt, len(opened))
	total := 0
	for i, view := range opened {
		if view.Validate(request.WorkspaceID) != nil || view.Availability != domain.MaterialAvailable || view.Reference != request.Evidence[i] {
			return nil, AnchorConflict()
		}
		excerpts[i] = excerpt{Label: fmt.Sprintf("S%03d", i+1), Title: view.Reference.Title, Text: view.Text}
		total += len(view.Text)
	}
	if total > MaxSynthesisSourceInputBytes {
		return nil, AnchorInvalid()
	}
	type assertion struct {
		Text          string `json:"text"`
		Applicability string `json:"applicability"`
	}
	items := make([]map[string]any, 0, len(revision.Items))
	for _, item := range revision.Items {
		switch item.Kind {
		case domain.SynthesisFactItem:
			items = append(items, map[string]any{"kind": "FACT", "statement": assertion{item.Fact.Text, item.Fact.Applicability}})
		case domain.SynthesisConflictItem:
			alternatives := make([]assertion, 0, len(item.Conflict.Alternatives))
			for _, alternative := range item.Conflict.Alternatives {
				alternatives = append(alternatives, assertion{alternative.Text, alternative.Applicability})
			}
			items = append(items, map[string]any{"kind": "CONFLICT", "subject": item.Conflict.Subject, "alternatives": alternatives})
		case domain.SynthesisGapItem:
			var resolution *assertion
			if item.Gap.Resolution != nil {
				resolution = &assertion{item.Gap.Resolution.Text, item.Gap.Resolution.Applicability}
			}
			items = append(items, map[string]any{"kind": "GAP", "question": item.Gap.Question, "context": item.Gap.Context, "resolution": resolution})
		}
	}
	payload, err := json.Marshal(struct {
		Task        string              `json:"task"`
		Title       string              `json:"note_title"`
		Items       []map[string]any    `json:"note_items"`
		Scope       *domain.AnchorScope `json:"confirmed_scope"`
		AnchorTitle *string             `json:"confirmed_anchor_title"`
		Sources     []excerpt           `json:"sources"`
	}{request.Kind, revision.Title, items, confirmed, anchorTitle, excerpts})
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxSynthesisSourceInputBytes {
		return nil, AnchorInvalid()
	}
	return payload, nil
}
