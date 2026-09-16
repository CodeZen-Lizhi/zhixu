package domain

import "github.com/CodeZen-Lizhi/zhixu/internal/foundation"

// ApplySynthesisDelta 将有界且绑定来源的变更应用到语义投影。
// 它不修改 current、delta 或 available，也不创建修订身份。
// 持久化必须对冻结基线执行 CAS，仅在 Changed 为 true 时提交修订。
// 语义支持还须由应用层检查。
func ApplySynthesisDelta(workspaceID foundation.ID, current []SynthesisItem, delta SynthesisDelta, available []SynthesisSourceRef) (SynthesisDeltaResult, error) {
	if err := ValidateSynthesisItems(workspaceID, current); err != nil {
		return SynthesisDeltaResult{}, err
	}
	if err := delta.Validate(workspaceID); err != nil {
		return SynthesisDeltaResult{}, err
	}
	if len(available) > MaxSynthesisSources {
		return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisSourceInvalid, "synthesis available source count exceeds limit")
	}
	allowed := make(map[string]SynthesisSourceRef, len(available))
	var allReferences []SynthesisSourceRef
	for _, reference := range available {
		if reference.Validate() != nil || reference.Source.WorkspaceID != workspaceID {
			return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisSourceInvalid, "synthesis available source is invalid")
		}
		key := synthesisSourceKey(reference)
		if _, duplicate := allowed[key]; duplicate {
			return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisSourceInvalid, "synthesis available source is duplicated")
		}
		allowed[key] = reference
		allReferences = append(allReferences, reference)
	}
	for _, item := range current {
		allReferences = append(allReferences, item.SourceReferences()...)
	}
	if err := validateSynthesisSourceBindings(allReferences); err != nil {
		return SynthesisDeltaResult{}, err
	}

	items := cloneSynthesisItems(current)
	byID := make(map[foundation.ID]int, len(items))
	bySemantics := make(map[string]int, len(items))
	requestedIDs := make(map[foundation.ID]string, len(items))
	for index, item := range items {
		key := synthesisItemKey(item)
		byID[item.ID], bySemantics[key], requestedIDs[item.ID] = index, index, key
	}
	changed := false
	sourcesChanged := false
	for _, operation := range delta.Operations {
		if err := requireAvailableSynthesisSources(operation, allowed); err != nil {
			return SynthesisDeltaResult{}, err
		}
		switch operation.Kind {
		case SynthesisRefreshItem:
			index, exists := byID[operation.TargetItemID]
			if !exists || items[index].BodyReference == nil || operation.Item.BodyReference.NoteID != items[index].BodyReference.NoteID || operation.Item.BodyReference.ItemID != items[index].BodyReference.ItemID {
				return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis refresh target reference changed")
			}
			if !synthesisRefreshContentEqual(items[index], *operation.Item) {
				items[index] = cloneSynthesisItem(*operation.Item)
				changed = true
			}
		case SynthesisAddFact, SynthesisAddConflict, SynthesisAddGap:
			item := *operation.Item
			key := synthesisItemKey(item)
			if prior, found := requestedIDs[item.ID]; found && prior != key {
				return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis item identity cannot change its content")
			}
			requestedIDs[item.ID] = key
			if index, duplicate := bySemantics[key]; duplicate {
				merged := mergeSynthesisItem(&items[index], item)
				sourcesChanged = merged || sourcesChanged
				continue
			}
			if len(items) >= MaxSynthesisItems {
				return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis item count exceeds limit")
			}
			bySemantics[key] = len(items)
			items = append(items, cloneSynthesisItem(item))
			changed = true
		case SynthesisAddSupport:
			// Targets are frozen input identities, not newly supplied item IDs.
			index, exists := byID[operation.TargetItemID]
			if !exists {
				return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis support target is unknown")
			}
			item := &items[index]
			switch item.Kind {
			case SynthesisFactItem:
				if operation.AlternativeIndex != nil {
					return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis fact cannot select a conflict alternative")
				}
				added := appendSynthesisSources(&item.Fact.Sources, operation.Sources)
				sourcesChanged = added || sourcesChanged
			case SynthesisConflictItem:
				if operation.AlternativeIndex == nil || *operation.AlternativeIndex >= len(item.Conflict.Alternatives) {
					return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis conflict alternative is out of range")
				}
				added := appendSynthesisSources(&item.Conflict.Alternatives[*operation.AlternativeIndex].Sources, operation.Sources)
				sourcesChanged = added || sourcesChanged
			default:
				return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis support target is not an assertion")
			}
		case SynthesisResolveGap:
			index, exists := byID[operation.TargetItemID]
			if !exists || items[index].Kind != SynthesisGapItem {
				return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis resolution target is not an existing gap")
			}
			gap := items[index].Gap
			if gap.Resolution == nil {
				resolution := cloneSynthesisStatement(*operation.Resolution)
				gap.Resolution = &resolution
				changed = true
			} else {
				if synthesisStatementKey(*gap.Resolution) != synthesisStatementKey(*operation.Resolution) {
					return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis gap resolution cannot be rewritten")
				}
				added := appendSynthesisSources(&gap.Resolution.Sources, operation.Resolution.Sources)
				sourcesChanged = added || sourcesChanged
			}
		default:
			return SynthesisDeltaResult{}, invalid(ErrorCodeSynthesisDeltaInvalid, "synthesis operation kind is unknown")
		}
	}
	if err := ValidateSynthesisItems(workspaceID, items); err != nil {
		return SynthesisDeltaResult{}, err
	}
	return SynthesisDeltaResult{Items: items, Changed: changed, SourcesChanged: sourcesChanged}, nil
}

func requireAvailableSynthesisSources(operation SynthesisOperation, allowed map[string]SynthesisSourceRef) error {
	var references []SynthesisSourceRef
	switch operation.Kind {
	case SynthesisAddFact, SynthesisAddConflict, SynthesisAddGap, SynthesisRefreshItem:
		references = operation.Item.SourceReferences()
	case SynthesisAddSupport:
		references = operation.Sources
	case SynthesisResolveGap:
		references = operation.Resolution.Sources
	}
	for _, reference := range references {
		opened, found := allowed[synthesisSourceKey(reference)]
		if !found || opened != reference {
			return invalid(ErrorCodeSynthesisSourceInvalid, "synthesis operation references an unopened or changed source")
		}
	}
	return nil
}

// mergeSynthesisItem preserves the first accepted ID, text, conditions and
// alternative order. The semantic key has already proved their equivalence.
func mergeSynthesisItem(existing *SynthesisItem, addition SynthesisItem) bool {
	switch existing.Kind {
	case SynthesisFactItem:
		return appendSynthesisSources(&existing.Fact.Sources, addition.Fact.Sources)
	case SynthesisConflictItem:
		changed := false
		for _, incoming := range addition.Conflict.Alternatives {
			for index := range existing.Conflict.Alternatives {
				current := &existing.Conflict.Alternatives[index]
				if synthesisStatementKey(*current) == synthesisStatementKey(incoming) {
					changed = appendSynthesisSources(&current.Sources, incoming.Sources) || changed
					break
				}
			}
		}
		return changed
	case SynthesisGapItem:
		return appendSynthesisSources(&existing.Gap.Sources, addition.Gap.Sources)
	default:
		return false
	}
}

func appendSynthesisSources(current *[]SynthesisSourceRef, additions []SynthesisSourceRef) bool {
	seen := make(map[string]bool, len(*current))
	for _, reference := range *current {
		seen[synthesisSourceKey(reference)] = true
	}
	changed := false
	for _, reference := range additions {
		key := synthesisSourceKey(reference)
		if !seen[key] {
			*current = append(*current, reference)
			seen[key] = true
			changed = true
		}
	}
	return changed
}

func cloneSynthesisItems(items []SynthesisItem) []SynthesisItem {
	result := make([]SynthesisItem, len(items))
	for index, item := range items {
		result[index] = cloneSynthesisItem(item)
	}
	return result
}

func cloneSynthesisItem(item SynthesisItem) SynthesisItem {
	if item.BodyReference != nil {
		reference := *item.BodyReference
		item.BodyReference = &reference
	}
	if item.Fact != nil {
		statement := cloneSynthesisStatement(*item.Fact)
		item.Fact = &statement
	}
	if item.Conflict != nil {
		conflict := *item.Conflict
		conflict.Alternatives = make([]SynthesisStatement, len(item.Conflict.Alternatives))
		for index, alternative := range item.Conflict.Alternatives {
			conflict.Alternatives[index] = cloneSynthesisStatement(alternative)
		}
		item.Conflict = &conflict
	}
	if item.Gap != nil {
		gap := *item.Gap
		gap.Sources = append([]SynthesisSourceRef{}, gap.Sources...)
		if gap.Resolution != nil {
			resolution := cloneSynthesisStatement(*gap.Resolution)
			gap.Resolution = &resolution
		}
		item.Gap = &gap
	}
	return item
}

func cloneSynthesisStatement(statement SynthesisStatement) SynthesisStatement {
	statement.Sources = append([]SynthesisSourceRef{}, statement.Sources...)
	return statement
}
