package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func SourceReviewError(code string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, errors.New("current manuscript source review could not be completed"))
}
func DecodeSourceReviewOutput(raw []byte) (SynthesisSourceReviewOutput, error) {
	var out SynthesisSourceReviewOutput
	if len(raw) == 0 || len(raw) > MaxSynthesisModelOutputBytes || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
	}
	decoded, err := strictjson.DecodeObject[SynthesisSourceReviewOutput](raw, strictjson.Limits{MaxDocumentBytes: MaxSynthesisModelOutputBytes, MaxDepth: 6, MaxStringBytes: 128, MaxArrayItems: 1024, MaxObjectFields: 6}, nil)
	if err != nil {
		return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
	}
	out = decoded
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || len(root) != 1 || root["checks"] == nil {
		return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(root["checks"], &entries) != nil {
		return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
	}
	for _, entry := range entries {
		if len(entry) != 5 {
			return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
		}
		for _, key := range []string{"obligation", "source", "targets", "verdict", "reason_code"} {
			if entry[key] == nil {
				return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
			}
		}
	}
	if out.Checks == nil || len(out.Checks) < 1 || len(out.Checks) > 1024 {
		return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
	}
	seen := map[string]bool{}
	for _, c := range out.Checks {
		if c.Obligation == "" || c.Source == "" || c.Targets == nil || seen[c.Obligation] {
			return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
		}
		seen[c.Obligation] = true
		if c.Verdict == "SUPPORTED" {
			if len(c.Targets) < 1 || c.ReasonCode != "CURRENT_TEXT_SUPPORTED" {
				return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
			}
		} else if c.Verdict == "UNSUPPORTED" || c.Verdict == "UNCERTAIN" {
			if c.ReasonCode != "CURRENT_TEXT_CONTRADICTS" && c.ReasonCode != "CONDITIONS_UNSUPPORTED" && c.ReasonCode != "NO_CURRENT_MATCH" && c.ReasonCode != "UNCERTAIN" {
				return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
			}
		} else {
			return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
		}
		seenTargets := map[string]bool{}
		for _, target := range c.Targets {
			if target == "" || seenTargets[target] {
				return out, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OUTPUT_INVALID")
			}
			seenTargets[target] = true
		}
	}
	return out, nil
}
func BindSourceReviewOutput(raw []byte, s SynthesisSourceReviewSnapshot) (SynthesisSourceReviewOutput, bool, error) {
	out, err := DecodeSourceReviewOutput(raw)
	if err != nil {
		return out, false, err
	}
	if len(out.Checks) != len(s.Obligations) {
		return out, false, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OBLIGATIONS_INCOMPLETE")
	}
	paragraphs := map[string]string{}
	for _, t := range s.Targets {
		for _, p := range t.Paragraphs {
			paragraphs[p.Label] = t.Label
		}
	}
	accepted := true
	for i, c := range out.Checks {
		o := s.Obligations[i]
		if c.Obligation != o.Label || c.Source != o.SourceLabel {
			return out, false, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_OBLIGATIONS_INCOMPLETE")
		}
		for _, p := range c.Targets {
			if paragraphs[p] != o.NoteLabel {
				return out, false, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_TARGET_INVALID")
			}
		}
		accepted = accepted && c.Verdict == "SUPPORTED"
	}
	return out, accepted, nil
}

// SourceReviewObligations 使用与生成阶段相同的领域增量引擎，
// 证明仅审计投影中的纯重复范围，不会因此将审计投影提升为可信内容。
func SourceReviewObligations(input SynthesisGenerationInput, result SynthesisGenerationResult) ([]SynthesisSourceReviewObligation, []foundation.ID, error) {
	if input.Validate() != nil || result.Validate(input) != nil || input.BodyRefresh != nil {
		return nil, nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_SCOPE_UNSUPPORTED")
	}
	notes := map[foundation.ID]SynthesisGenerationNote{}
	for _, n := range input.Notes {
		notes[n.Note.ID] = n
	}
	refs := []domain.SynthesisSourceRef{}
	for _, s := range input.Sources {
		refs = append(refs, s.Reference)
	}
	type entry struct {
		note                foundation.ID
		ref                 domain.SynthesisSourceRef
		text, applicability string
	}
	entries := []entry{}
	ids := []foundation.ID{}
	seenNotes := map[foundation.ID]bool{}
	for _, g := range result.Notes {
		n, ok := notes[g.NoteID]
		if !ok || seenNotes[g.NoteID] || n.Revision.Manuscript == nil || g.Title != n.Note.Title || !reflect.DeepEqual(g.Aliases, n.Note.Aliases) {
			return nil, nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_SCOPE_UNSUPPORTED")
		}
		seenNotes[g.NoteID] = true
		ids = append(ids, g.NoteID)
		for _, op := range g.Delta.Operations {
			if op.Kind != domain.SynthesisAddFact || op.Item == nil || op.Item.BodyReference != nil {
				return nil, nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_SCOPE_UNSUPPORTED")
			}
		}
		delta, err := domain.ApplySynthesisDelta(n.Note.WorkspaceID, n.Revision.Manuscript.Machine.MachineItems, g.Delta, refs)
		if err != nil {
			return nil, nil, err
		}
		if delta.Changed {
			return nil, nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_SCOPE_UNSUPPORTED")
		}
		old := map[foundation.ID]domain.SynthesisItem{}
		for _, item := range n.Revision.Manuscript.Machine.MachineItems {
			old[item.ID] = item
		}
		for _, item := range delta.Items {
			before := old[item.ID]
			if reflect.DeepEqual(before, item) {
				continue
			}
			if item.Fact == nil || before.Fact == nil {
				return nil, nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_SCOPE_UNSUPPORTED")
			}
			known := map[string]bool{}
			for _, ref := range before.Fact.Sources {
				key, _ := ref.IdentityKey()
				known[key] = true
			}
			for _, ref := range item.Fact.Sources {
				key, _ := ref.IdentityKey()
				if !known[key] {
					entries = append(entries, entry{g.NoteID, ref, item.Fact.Text, item.Fact.Applicability})
				}
			}
		}
	}
	if len(entries) == 0 || len(entries) > 1024 {
		return nil, nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_SCOPE_UNSUPPORTED")
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	labels := map[foundation.ID]string{}
	for i, id := range ids {
		labels[id] = fmt.Sprintf("N%03d", i+1)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		ka, _ := a.ref.IdentityKey()
		kb, _ := b.ref.IdentityKey()
		return string(a.note)+a.text+a.applicability+ka < string(b.note)+b.text+b.applicability+kb
	})
	out := []SynthesisSourceReviewObligation{}
	sources := map[string]string{}
	for i, e := range entries {
		key, _ := e.ref.IdentityKey()
		if sources[key] == "" {
			sources[key] = fmt.Sprintf("S%03d", len(sources)+1)
		}
		out = append(out, SynthesisSourceReviewObligation{fmt.Sprintf("O%03d", i+1), labels[e.note], sources[key], e.ref, e.text, e.applicability})
	}
	return out, ids, nil
}
func BuildSourceReviewPayload(input SynthesisSourceReviewInput) ([]byte, error) {
	type paragraph struct {
		Label string `json:"label"`
		Text  string `json:"text"`
	}
	type target struct {
		Label       string      `json:"label"`
		FullContent string      `json:"full_content"`
		Scope       any         `json:"scope"`
		Paragraphs  []paragraph `json:"paragraphs"`
	}
	type source struct {
		Label string `json:"label"`
		Text  string `json:"text"`
	}
	type obligation struct {
		Label               string `json:"label"`
		Note                string `json:"note"`
		Source              string `json:"source"`
		HistoricalStatement string `json:"untrusted_historical_statement"`
		Applicability       string `json:"untrusted_historical_applicability"`
	}
	payload := struct {
		Targets     []target     `json:"targets"`
		Sources     []source     `json:"sources"`
		Obligations []obligation `json:"obligations"`
	}{[]target{}, []source{}, []obligation{}}
	for _, t := range input.Snapshot.Targets {
		p := target{Label: t.Label, FullContent: t.FullContent, Paragraphs: []paragraph{}}
		if t.Anchor != nil {
			p.Scope = t.Anchor.Scope
		}
		for _, part := range t.Paragraphs {
			if part.StartByte < 0 || part.EndByte > len(t.FullContent) || part.StartByte >= part.EndByte {
				return nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_TARGET_INVALID")
			}
			p.Paragraphs = append(p.Paragraphs, paragraph{part.Label, t.FullContent[part.StartByte:part.EndByte]})
		}
		payload.Targets = append(payload.Targets, p)
	}
	available := map[string]string{}
	for _, s := range input.Sources {
		key, _ := s.Reference.IdentityKey()
		available[key] = s.Text
	}
	seen := map[string]bool{}
	for _, o := range input.Snapshot.Obligations {
		key, _ := o.Source.IdentityKey()
		text := available[key]
		if strings.TrimSpace(text) == "" {
			return nil, SourceReviewError("SYNTHESIS_SOURCE_REVIEW_SOURCE_INVALID")
		}
		if !seen[o.SourceLabel] {
			payload.Sources = append(payload.Sources, source{o.SourceLabel, text})
			seen[o.SourceLabel] = true
		}
		payload.Obligations = append(payload.Obligations, obligation{o.Label, o.NoteLabel, o.SourceLabel, o.HistoricalStatement, o.HistoricalApplicability})
	}
	return json.Marshal(payload)
}
