package agent

import (
	"encoding/json"
	"fmt"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func decodeSynthesisRefreshDelta(raw []byte) (json.RawMessage, error) {
	if _, err := decodeSynthesisRefreshWire(raw); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), raw...), nil
}

func decodeSynthesisRefreshWire(raw []byte) (synthesisWireOutput, error) {
	root, err := synthesisObject(raw, "notes")
	if err != nil {
		return synthesisWireOutput{}, err
	}
	notes, err := synthesisArray[json.RawMessage](root["notes"], 0, 1)
	if err != nil {
		return synthesisWireOutput{}, err
	}
	out := synthesisWireOutput{Notes: []synthesisWireNote{}}
	for _, rawNote := range notes {
		note, err := synthesisObject(rawNote, "note", "operations")
		if err != nil {
			return out, err
		}
		label, err := synthesisString(note["note"], 4, false)
		if err != nil || label != "N001" {
			return out, synthesisOutputError()
		}
		operations, err := synthesisArray[json.RawMessage](note["operations"], 1, domain.MaxSynthesisItems)
		if err != nil {
			return out, err
		}
		value := synthesisWireNote{Label: label}
		seen := map[string]bool{}
		for _, rawOp := range operations {
			op, err := synthesisObject(rawOp, "kind", "target")
			if err != nil {
				return out, err
			}
			kind, err := synthesisString(op["kind"], 32, false)
			if err != nil || kind != string(domain.SynthesisRefreshItem) {
				return out, synthesisOutputError()
			}
			target, err := synthesisString(op["target"], 4, false)
			if err != nil || !synthesisLabel(target, 'I', domain.MaxSynthesisItems) || seen[target] {
				return out, synthesisOutputError()
			}
			seen[target] = true
			value.Operations = append(value.Operations, synthesisWireOperation{Kind: domain.SynthesisRefreshItem, Target: target})
		}
		out.Notes = append(out.Notes, value)
	}
	return out, nil
}

func synthesisRefreshDeltaSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"notes"}, "properties": map[string]any{"notes": map[string]any{"type": "array", "maxItems": 1, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"note", "operations"}, "properties": map[string]any{"note": map[string]any{"type": "string", "const": "N001"}, "operations": map[string]any{"type": "array", "minItems": 1, "maxItems": domain.MaxSynthesisItems, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "target"}, "properties": map[string]any{"kind": map[string]any{"type": "string", "const": "REFRESH_ITEM"}, "target": map[string]any{"type": "string", "pattern": "^I[0-9]{3}$"}}}}}}}}}
}

func synthesisRefreshProjection(input app.SynthesisGenerationInput, labels map[domain.SynthesisSourceRef]string) []synthesisInputItem {
	var result []synthesisInputItem
	if input.BodyRefresh == nil || len(input.Notes) != 1 {
		return result
	}
	for index, item := range input.Notes[0].Revision.Items {
		op, err := app.BodyRefreshOperation(input, item.ID)
		if err != nil {
			continue
		}
		item = *op.Item
		projected := synthesisInputItem{Label: fmt.Sprintf("I%03d", index+1), Kind: item.Kind}
		if item.Fact != nil {
			value := synthesisProjectStatement(*item.Fact, labels)
			projected.Fact = &value
		}
		if item.Conflict != nil {
			value := synthesisInputConflict{Subject: item.Conflict.Subject}
			for _, alt := range item.Conflict.Alternatives {
				value.Alternatives = append(value.Alternatives, synthesisProjectStatement(alt, labels))
			}
			projected.Conflict = &value
		}
		if item.Gap != nil {
			refs, unopened := synthesisProjectSources(item.Gap.Sources, labels)
			value := synthesisInputGap{Question: item.Gap.Question, Context: item.Gap.Context, Sources: refs, UnopenedSources: unopened}
			if item.Gap.Resolution != nil {
				statement := synthesisProjectStatement(*item.Gap.Resolution, labels)
				value.Resolution = &statement
			}
			projected.Gap = &value
		}
		result = append(result, projected)
	}
	return result
}
