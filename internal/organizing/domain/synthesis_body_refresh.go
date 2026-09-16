package domain

import (
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RefreshSynthesisPublishedItem 计算一次精确的局部替换。
// 调用方须在持久化前证明两个历史发布、来源准入及模型复核；引用本身不授予替换权限。
// 本地补源或本地缺口解决会构成冲突，不能在复制上游条目时丢弃这些数据。
func RefreshSynthesisPublishedItem(workspaceID foundation.ID, current []SynthesisItem, targetID foundation.ID, original, updated SynthesisRevision, oldPublicationID, newPublicationID foundation.ID) (SynthesisDeltaResult, error) {
	if ValidateSynthesisItems(workspaceID, current) != nil || original.Validate() != nil || updated.Validate() != nil ||
		original.WorkspaceID != workspaceID || updated.WorkspaceID != workspaceID || original.NoteID != updated.NoteID || original.DocumentID != updated.DocumentID ||
		updated.RevisionNo <= original.RevisionNo || !validID(oldPublicationID) || !validID(newPublicationID) || oldPublicationID == newPublicationID {
		return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis refresh publication binding is invalid")
	}
	for index, item := range current {
		if item.ID != targetID {
			continue
		}
		if !MatchesSynthesisBodyItem(item, original, oldPublicationID) {
			return SynthesisDeltaResult{}, invalid("SYNTHESIS_BODY_REFRESH_CONFLICT", "synthesis refresh target contains local changes or a different reference")
		}
		op, err := IncludeSynthesisPublishedItem(updated, newPublicationID, item.BodyReference.ItemID, item.ID)
		if err != nil {
			return SynthesisDeltaResult{}, invalid("SYNTHESIS_BODY_REFRESH_REVIEW_REQUIRED", "synthesis refresh upstream item is unavailable")
		}
		items := cloneSynthesisItems(current)
		// 判断正文版本变化时忽略溯源链变化及额外证据。
		// 尤其是已解决缺口必须同时比较解决结论和问题/上下文，普通去重键不包含解决结论。
		if synthesisRefreshContentEqual(item, *op.Item) {
			return SynthesisDeltaResult{Items: items}, nil
		}
		items[index] = cloneSynthesisItem(*op.Item)
		if err := ValidateSynthesisItems(workspaceID, items); err != nil {
			return SynthesisDeltaResult{}, err
		}
		return SynthesisDeltaResult{Items: items, Changed: true}, nil
	}
	return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis refresh target is missing")
}

func synthesisRefreshContentEqual(a, b SynthesisItem) bool {
	strip := func(item SynthesisItem) SynthesisItem {
		item = cloneSynthesisItem(item)
		item.ID, item.BodyReference = "", nil
		if item.Fact != nil {
			item.Fact.Sources = nil
		}
		if item.Conflict != nil {
			for i := range item.Conflict.Alternatives {
				item.Conflict.Alternatives[i].Sources = nil
			}
		}
		if item.Gap != nil {
			item.Gap.Sources = nil
			if item.Gap.Resolution != nil {
				item.Gap.Resolution.Sources = nil
			}
		}
		return item
	}
	return reflect.DeepEqual(strip(a), strip(b))
}
