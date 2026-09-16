package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// 已准备材料是临时数据。生成工作流须将其冻结，并在应用时重新核验来源门禁后
// 才能创建候选。Sources 数组为空表示已完成的选择没有选中任何知识。
type SynthesisGoalPreparedInput struct {
	Progress SynthesisGoalSelectionProgress
	Sources  []SynthesisGoalPreparedSource
}

type SynthesisGoalPreparedSource struct {
	Excerpt SynthesisSourceExcerpt
	Points  []GoalSourcePointBinding
}

type SynthesisGoalPreparer struct {
	materials GoalSourceMaterialReader
	sources   SynthesisSourceReader
}

func NewSynthesisGoalPreparer(materials GoalSourceMaterialReader, sources SynthesisSourceReader) (*SynthesisGoalPreparer, error) {
	if nilSynthesisDependency(materials) || nilSynthesisDependency(sources) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("goal preparation dependencies are unavailable"))
	}
	return &SynthesisGoalPreparer{materials: materials, sources: sources}, nil
}

// Prepare 遍历每一个选择游标，包括空页，且仅打开选中的证据。
// 超过输入上限时明确失败，不会返回可能被误认为完整目标的部分集合。它不调用模型。
func (p *SynthesisGoalPreparer) Prepare(ctx context.Context, workspaceID, requestID foundation.ID) (SynthesisGoalPreparedInput, error) {
	var result SynthesisGoalPreparedInput
	if p == nil || ctx == nil || !validID(workspaceID) || !validID(requestID) {
		return result, goalSelectionInvalid()
	}
	query := GoalSelectionResultQuery{WorkspaceID: workspaceID, RequestID: requestID, Limit: 1}
	materials := []GoalSourceMaterial{}
	indices := map[string]int{}
	for {
		if err := ctx.Err(); err != nil {
			return SynthesisGoalPreparedInput{}, err
		}
		page, err := p.materials.ReadGoalSourceMaterials(ctx, query)
		if err != nil {
			return SynthesisGoalPreparedInput{}, err
		}
		if page.Progress.Request.ID != requestID || page.Progress.Request.WorkspaceID != workspaceID || page.Items == nil {
			return SynthesisGoalPreparedInput{}, goalSelectionInvalid()
		}
		if !page.Progress.Ready() {
			// 仅返回进度，不返回证据；待处理、失败和结果不确定的切片
			// 保持可区分，均不能生成初版笔记。
			return SynthesisGoalPreparedInput{Progress: page.Progress}, nil
		}
		if query.AfterSelectionID == "" {
			result.Progress = page.Progress
		} else if page.Progress != result.Progress {
			return SynthesisGoalPreparedInput{}, goalSelectionInvalid()
		}
		for _, material := range page.Items {
			key, err := material.Reference.IdentityKey()
			if err != nil || material.Reference.Source.WorkspaceID != workspaceID || len(material.Points) == 0 || len(material.Points) > MaxGoalSelectionPoints {
				return SynthesisGoalPreparedInput{}, goalSelectionInvalid()
			}
			index, exists := indices[key]
			if !exists {
				if len(materials) == domain.MaxSynthesisSources {
					return SynthesisGoalPreparedInput{}, goalPreparationBudget()
				}
				indices[key] = len(materials)
				material.Points = append([]GoalSourcePointBinding(nil), material.Points...)
				materials = append(materials, material)
				continue
			}
			if materials[index].Reference != material.Reference {
				return SynthesisGoalPreparedInput{}, goalSelectionInvalid()
			}
			// 不同选择切片可能选中共享同一片段的知识点。
			// 保留全部知识点定位信息，同时只打开一次原文片段。
			for _, binding := range material.Points {
				duplicate := false
				for _, existing := range materials[index].Points {
					if existing.Locator == binding.Locator {
						if existing != binding {
							return SynthesisGoalPreparedInput{}, goalSelectionInvalid()
						}
						duplicate = true
						break
					}
				}
				if !duplicate {
					if len(materials[index].Points) == 512 {
						return SynthesisGoalPreparedInput{}, goalPreparationBudget()
					}
					materials[index].Points = append(materials[index].Points, binding)
				}
			}
		}
		if page.NextAfterSelectionID == "" {
			break
		}
		if !validID(page.NextAfterSelectionID) || page.NextAfterSelectionID <= query.AfterSelectionID {
			return SynthesisGoalPreparedInput{}, goalSelectionInvalid()
		}
		query.AfterSelectionID = page.NextAfterSelectionID
	}
	result.Sources = make([]SynthesisGoalPreparedSource, 0, len(materials))
	totalBytes := 0
	for _, material := range materials {
		view, err := p.sources.OpenSynthesisSource(ctx, material.Reference)
		if err != nil {
			return SynthesisGoalPreparedInput{}, err
		}
		if view.Reference != material.Reference || view.Validate(workspaceID) != nil {
			return SynthesisGoalPreparedInput{}, goalSelectionInvalid()
		}
		if view.Availability != domain.MaterialAvailable {
			return SynthesisGoalPreparedInput{}, foundation.NewError(foundation.ErrorVersionConflict, "SYNTHESIS_GOAL_SOURCE_UNAVAILABLE", false, errors.New("selected original evidence is not currently available"))
		}
		totalBytes += len(view.Text)
		if totalBytes > MaxSynthesisSourceInputBytes {
			return SynthesisGoalPreparedInput{}, goalPreparationBudget()
		}
		result.Sources = append(result.Sources, SynthesisGoalPreparedSource{Excerpt: SynthesisSourceExcerpt{Reference: material.Reference, Text: view.Text}, Points: material.Points})
	}
	return result, nil
}

func goalPreparationBudget() error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeSynthesisModelInputTooLarge, false, errors.New("complete goal evidence exceeds the generation input budget"))
}
