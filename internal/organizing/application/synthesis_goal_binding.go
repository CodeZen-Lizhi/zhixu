package application

import (
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// SynthesisGoalBinding 冻结用户目标和选中知识点的证据，不包含原文字节。
// 处理所属模块在冻结和应用此绑定时都会重新检查持久选择证明。
type SynthesisGoalBinding struct {
	RequestID foundation.ID        `json:"request_id"`
	Text      string               `json:"text"`
	Materials []GoalSourceMaterial `json:"materials"`
}

// Validate 要求来源集合精确匹配；仅凭目标 ID 不能准入证据。
func (b *SynthesisGoalBinding) Validate(workspaceID foundation.ID, sources []domain.SynthesisSourceRef) error {
	if b == nil || !validID(b.RequestID) || (CreateSynthesisGoalCommand{WorkspaceID: workspaceID, Goal: b.Text, IdempotencyKey: "goal-binding"}).Validate() != nil || len(sources) == 0 || len(sources) > domain.MaxSynthesisSources || len(b.Materials) != len(sources) {
		return goalSelectionInvalid()
	}
	refs := make(map[string]domain.SynthesisSourceRef, len(sources))
	for _, ref := range sources {
		key, err := ref.IdentityKey()
		if err != nil || ref.Source.WorkspaceID != workspaceID {
			return goalSelectionInvalid()
		}
		if _, ok := refs[key]; ok {
			return goalSelectionInvalid()
		}
		refs[key] = ref
	}
	type pointSource struct {
		source  domain.SynthesisSourceVersion
		binding GoalSourcePointBinding
	}
	points := map[KnowledgePointLocator]pointSource{}
	profiles := map[domain.SynthesisSourceVersion]foundation.ID{}
	type selectionBinding struct {
		source         domain.SynthesisSourceVersion
		profile, model foundation.ID
	}
	selections := map[foundation.ID]selectionBinding{}
	for _, material := range b.Materials {
		key, err := material.Reference.IdentityKey()
		ref, exists := refs[key]
		if err != nil || !exists || ref != material.Reference || len(material.Points) < 1 || len(material.Points) > 512 {
			return goalSelectionInvalid()
		}
		delete(refs, key)
		seen := map[KnowledgePointLocator]bool{}
		for _, point := range material.Points {
			loc := point.Locator
			if !validID(point.SelectionID) || !validID(point.ModelRunID) || !validID(loc.ProfileRevisionID) || loc.Index < 0 || loc.Index >= 256 || loc.Kind != KnowledgePointKindKnowledgePoint && loc.Kind != KnowledgePointKindExample || !anchorModelText(point.Reason, 2048) || seen[loc] {
				return goalSelectionInvalid()
			}
			seen[loc] = true
			value := pointSource{source: ref.Source, binding: point}
			if old, ok := points[loc]; ok && old != value {
				return goalSelectionInvalid()
			}
			points[loc] = value
			if old, ok := profiles[ref.Source]; ok && old != loc.ProfileRevisionID {
				return goalSelectionInvalid()
			}
			profiles[ref.Source] = loc.ProfileRevisionID
			binding := selectionBinding{ref.Source, loc.ProfileRevisionID, point.ModelRunID}
			if old, ok := selections[point.SelectionID]; ok && old != binding {
				return goalSelectionInvalid()
			}
			selections[point.SelectionID] = binding
		}
	}
	return nil
}

func BuildSynthesisGoalBinding(prepared SynthesisGoalPreparedInput) (*SynthesisGoalBinding, error) {
	request := prepared.Progress.Request
	if !prepared.Progress.Ready() {
		return nil, goalSelectionInvalid()
	}
	binding := &SynthesisGoalBinding{RequestID: request.ID, Text: request.Goal, Materials: make([]GoalSourceMaterial, len(prepared.Sources))}
	refs := make([]domain.SynthesisSourceRef, len(prepared.Sources))
	total := 0
	for i, source := range prepared.Sources {
		if err := source.Excerpt.Validate(request.WorkspaceID); err != nil {
			return nil, err
		}
		total += len(source.Excerpt.Text)
		if total > MaxSynthesisSourceInputBytes {
			return nil, goalPreparationBudget()
		}
		refs[i] = source.Excerpt.Reference
		binding.Materials[i] = GoalSourceMaterial{Reference: source.Excerpt.Reference, Points: append([]GoalSourcePointBinding(nil), source.Points...)}
	}
	if err := binding.Validate(request.WorkspaceID, refs); err != nil {
		return nil, err
	}
	return binding, nil
}
