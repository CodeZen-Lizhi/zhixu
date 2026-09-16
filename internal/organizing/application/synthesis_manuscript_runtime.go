package application

import (
	"context"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"reflect"
)

// SynthesisManuscriptCandidate 绑定所属模块封存的回执，不接受调用方文本。
type SynthesisManuscriptCandidate struct {
	NoteID      foundation.ID `json:"note_id"`
	ReceiptID   foundation.ID `json:"receipt_id"`
	ReceiptHash string        `json:"receipt_hash"`
}

// SynthesisMachineDelta 仅用于更新基线。模型提示、已发布内容的包含操作
// 和面试仍使用修订中的可信 Items。
func SynthesisMachineDelta(input SynthesisGenerationInput, generated SynthesisGeneratedNote, base *domain.SynthesisRevision) (domain.SynthesisDeltaResult, error) {
	var items []domain.SynthesisItem
	if base != nil {
		items = base.Items
		if base.Manuscript != nil {
			items = base.Manuscript.Machine.MachineItems
		}
	}
	refs := make([]domain.SynthesisSourceRef, len(input.Sources))
	for i, source := range input.Sources {
		refs[i] = source.Reference
	}
	result, err := domain.ApplySynthesisDelta(input.SourceEvent.Source.WorkspaceID, items, generated.Delta, refs)
	if err != nil {
		return result, err
	}
	if base != nil && base.Manuscript != nil && result.SourcesChanged {
		trusted := map[foundation.ID]bool{}
		for _, item := range base.Items {
			trusted[item.ID] = true
		}
		previous := map[foundation.ID]domain.SynthesisItem{}
		for _, item := range items {
			previous[item.ID] = item
		}
		for _, item := range result.Items {
			if old, ok := previous[item.ID]; ok && !trusted[item.ID] && !reflect.DeepEqual(old, item) {
				return domain.SynthesisDeltaResult{}, foundation.NewError(foundation.ErrorManualRecoveryRequired, "SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED", false, errors.New("supplement targets an untrusted historical machine item"))
			}
		}
	}
	return result, nil
}

func (service *SynthesisService) ApplyManuscriptGeneration(ctx context.Context, input SynthesisGenerationInput, generation SynthesisGenerationResult, manuscripts []SynthesisManuscriptCandidate) (SynthesisApplyResult, error) {
	return service.applyGeneration(ctx, input, generation, manuscripts)
}
