package application

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

// MaxGoalBindingProofSelections 限定一次同步证明校验的上限。
// 目录清单在一个不可变批次内最多允许此数量的切片。跨越更多切片的请求须采用
// 单独持久化的证明策略；本校验器不会在任意一页静默停止。
const MaxGoalBindingProofSelections = 16_384

// SynthesisGoalBindingProofVerifier 在持久处理操作信任目标绑定前，
// 重新检查服务端管理的不可变选择记录。它不依赖来源读取器；
// 来源元组与原文片段门禁由后续生成和应用事务负责。
type SynthesisGoalBindingProofVerifier interface {
	VerifySynthesisGoalBinding(context.Context, foundation.ID, *SynthesisGoalBinding) error
}

// SynthesisGoalBindingProof 读取终态选择记录。进度成为 Ready 后选择不可变，
// 因此各次独立分页读取对应同一个请求和完整终态集合。任何漂移都必须拒绝。
type SynthesisGoalBindingProof struct {
	results GoalSelectionResultReader
}

func NewSynthesisGoalBindingProof(results GoalSelectionResultReader) (*SynthesisGoalBindingProof, error) {
	if nilSynthesisDependency(results) {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("goal selection result reader is unavailable"))
	}
	return &SynthesisGoalBindingProof{results: results}, nil
}

// VerifySynthesisGoalBinding 将绑定中每个选中知识点与片段的原子项
// 同每个成功的不可变选择切片比较。显式空切片仍参与游标遍历，并计入终态集合。
func (p *SynthesisGoalBindingProof) VerifySynthesisGoalBinding(ctx context.Context, workspaceID foundation.ID, binding *SynthesisGoalBinding) error {
	if p == nil || nilSynthesisDependency(p.results) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSynthesisUnavailable, true, errors.New("goal selection proof is unavailable"))
	}
	if ctx == nil || !validID(workspaceID) || binding == nil {
		return goalSelectionInvalid()
	}
	refs := make([]domain.SynthesisSourceRef, len(binding.Materials))
	for i, material := range binding.Materials {
		refs[i] = material.Reference
	}
	if err := binding.Validate(workspaceID, refs); err != nil {
		return err
	}

	progress, err := p.results.ReadGoalSelectionProgress(ctx, workspaceID, binding.RequestID)
	if err != nil {
		return err
	}
	if !goalBindingProofProgress(progress, workspaceID, binding) {
		return goalBindingProofInvalid()
	}
	if progress.Selections > MaxGoalBindingProofSelections {
		return goalBindingProofBudget()
	}

	expected, err := goalBindingProofAtoms(binding)
	if err != nil {
		return err
	}
	query := GoalSelectionResultQuery{WorkspaceID: workspaceID, RequestID: binding.RequestID, Limit: MaxGoalSelectionPoints}
	seenSelections := make(map[foundation.ID]bool)
	seenAtoms := make(map[goalBindingProofAtom]bool)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := p.results.ReadGoalSelectionResults(ctx, query)
		if err != nil {
			return err
		}
		if page.Progress != progress || page.Items == nil || len(page.Items) > query.Limit {
			return goalBindingProofInvalid()
		}
		if len(page.Items) == 0 {
			if page.NextAfterSelectionID != "" {
				return goalBindingProofInvalid()
			}
			break
		}

		last := query.AfterSelectionID
		for _, selection := range page.Items {
			if !validID(selection.SelectionID) || !validID(selection.ModelRunID) || selection.SelectionID <= last || seenSelections[selection.SelectionID] || selection.Points == nil || len(selection.Points) > MaxGoalSelectionPoints {
				return goalBindingProofInvalid()
			}
			seenSelections[selection.SelectionID] = true
			last = selection.SelectionID
			seenPoints := make(map[goalBindingProofPoint]bool, len(selection.Points))
			for _, point := range selection.Points {
				if !goalBindingProofPointValid(point, workspaceID) {
					return goalBindingProofInvalid()
				}
				pointKey := goalBindingProofPoint{SelectionID: selection.SelectionID, ModelRunID: selection.ModelRunID, Source: point.Source, Locator: point.Locator, Reason: point.Reason}
				if seenPoints[pointKey] {
					return goalBindingProofInvalid()
				}
				seenPoints[pointKey] = true
				spans := make(map[foundation.ID]bool, len(point.SourceSpanIDs))
				for _, spanID := range point.SourceSpanIDs {
					if !validID(spanID) || spans[spanID] {
						return goalBindingProofInvalid()
					}
					spans[spanID] = true
					atom := goalBindingProofAtom{goalBindingProofPoint: pointKey, SpanID: spanID}
					if seenAtoms[atom] || !expected[atom] {
						return goalBindingProofInvalid()
					}
					seenAtoms[atom] = true
				}
			}
		}
		if int64(len(seenSelections)) > progress.Selections {
			return goalBindingProofInvalid()
		}
		if page.NextAfterSelectionID == "" {
			break
		}
		if page.NextAfterSelectionID != last {
			return goalBindingProofInvalid()
		}
		query.AfterSelectionID = page.NextAfterSelectionID
	}
	if int64(len(seenSelections)) != progress.Selections || len(seenAtoms) != len(expected) {
		return goalBindingProofInvalid()
	}
	return nil
}

type goalBindingProofPoint struct {
	SelectionID, ModelRunID foundation.ID
	Source                  domain.SynthesisSourceVersion
	Locator                 KnowledgePointLocator
	Reason                  string
}

type goalBindingProofAtom struct {
	goalBindingProofPoint
	SpanID foundation.ID
}

func goalBindingProofAtoms(binding *SynthesisGoalBinding) (map[goalBindingProofAtom]bool, error) {
	result := make(map[goalBindingProofAtom]bool)
	for _, material := range binding.Materials {
		for _, point := range material.Points {
			atom := goalBindingProofAtom{goalBindingProofPoint: goalBindingProofPoint{
				SelectionID: point.SelectionID,
				ModelRunID:  point.ModelRunID,
				Source:      material.Reference.Source,
				Locator:     point.Locator,
				Reason:      point.Reason,
			}, SpanID: material.Reference.SourceSpanID}
			if result[atom] {
				return nil, goalSelectionInvalid()
			}
			result[atom] = true
		}
	}
	return result, nil
}

func goalBindingProofProgress(progress SynthesisGoalSelectionProgress, workspaceID foundation.ID, binding *SynthesisGoalBinding) bool {
	return progress.Ready() && progress.Request.ID == binding.RequestID && progress.Request.WorkspaceID == workspaceID && progress.Request.Goal == binding.Text &&
		progress.Selections > 0
}

func goalBindingProofPointValid(point SynthesisGoalSelectedPoint, workspaceID foundation.ID) bool {
	return point.Source.Validate() == nil && point.Source.WorkspaceID == workspaceID && validID(point.Locator.ProfileRevisionID) &&
		point.Locator.Index >= 0 && point.Locator.Index < 256 && (point.Locator.Kind == KnowledgePointKindKnowledgePoint || point.Locator.Kind == KnowledgePointKindExample) &&
		anchorModelText(point.Reason, 2048) && len(point.SourceSpanIDs) >= 1 && len(point.SourceSpanIDs) <= 500
}

func goalBindingProofInvalid() error {
	return inconsistent("goal binding does not match immutable selection proof")
}

func goalBindingProofBudget() error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, "SYNTHESIS_GOAL_SELECTION_PROOF_TOO_LARGE", false, errors.New("goal selection proof exceeds verification budget"))
}

var _ SynthesisGoalBindingProofVerifier = (*SynthesisGoalBindingProof)(nil)
