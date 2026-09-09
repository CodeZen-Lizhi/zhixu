package agent

import (
	"encoding/json"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const maxSynthesisSemanticChecks = organizingapp.MaxSynthesisSemanticChecks

type synthesisSemanticCheck struct {
	Index         int                      `json:"index"`
	Kind          string                   `json:"kind"`
	Topic         string                   `json:"topic"`
	Text          string                   `json:"text"`
	Applicability string                   `json:"applicability"`
	Question      string                   `json:"question"`
	Context       string                   `json:"context"`
	Sources       []string                 `json:"sources"`
	Alternatives  []synthesisWireStatement `json:"alternatives"`
}

type synthesisSemanticInput struct {
	Checks         []synthesisSemanticCheck `json:"checks"`
	Sources        []synthesisInputSource   `json:"sources"`
	UnchangedNotes []synthesisInputNote     `json:"unchanged_notes"`
}

type synthesisSourceVerdict struct {
	Source  string
	Verdict string
}

type synthesisCheckVerdict struct {
	Index   int
	Verdict string
	Sources []synthesisSourceVerdict
}

type synthesisSemanticOutput struct{ Checks []synthesisCheckVerdict }

func synthesisSemanticPlan(input organizingapp.SynthesisGenerationInput, result organizingapp.SynthesisGenerationResult) (synthesisSemanticInput, error) {
	if err := result.Validate(input); err != nil {
		return synthesisSemanticInput{}, err
	}
	projection := synthesisInput(input)
	value := synthesisSemanticInput{Checks: make([]synthesisSemanticCheck, 0), Sources: projection.Sources, UnchangedNotes: []synthesisInputNote{}}
	labels := make(map[domain.SynthesisSourceRef]string, len(input.Sources))
	for index, source := range input.Sources {
		labels[source.Reference] = projection.Sources[index].Label
	}
	appendCheck := func(check synthesisSemanticCheck) error {
		if len(value.Checks) >= maxSynthesisSemanticChecks {
			return synthesisOutputError()
		}
		check.Index = len(value.Checks) + 1
		if check.Sources == nil {
			check.Sources = []string{}
		}
		if check.Alternatives == nil {
			check.Alternatives = []synthesisWireStatement{}
		}
		value.Checks = append(value.Checks, check)
		return nil
	}
	statementCheck := func(kind, topic, question, context string, statement domain.SynthesisStatement) error {
		sources, unopened := synthesisProjectSources(statement.Sources, labels)
		if unopened != 0 {
			return synthesisOutputError()
		}
		return appendCheck(synthesisSemanticCheck{Kind: kind, Topic: topic, Text: statement.Text, Applicability: statement.Applicability, Question: question, Context: context, Sources: sources})
	}
	for _, generated := range result.Notes {
		current := map[string]domain.SynthesisItem{}
		for _, note := range input.Notes {
			if note.Note.ID != generated.NoteID {
				continue
			}
			for _, item := range note.Revision.Items {
				current[string(item.ID)] = item
			}
		}
		for _, operation := range generated.Delta.Operations {
			switch operation.Kind {
			case domain.SynthesisAddFact:
				if err := statementCheck("ASSERTION", generated.Title, "", "", *operation.Item.Fact); err != nil {
					return value, err
				}
			case domain.SynthesisAddConflict:
				conflict := operation.Item.Conflict
				alternatives := make([]synthesisWireStatement, 0, len(conflict.Alternatives))
				for _, alternative := range conflict.Alternatives {
					if err := statementCheck("CONFLICT_ALTERNATIVE", generated.Title, "", conflict.Subject, alternative); err != nil {
						return value, err
					}
					sources, unopened := synthesisProjectSources(alternative.Sources, labels)
					if unopened != 0 {
						return value, synthesisOutputError()
					}
					alternatives = append(alternatives, synthesisWireStatement{Text: alternative.Text, Applicability: alternative.Applicability, Sources: sources})
				}
				if err := appendCheck(synthesisSemanticCheck{Kind: "CONFLICT_RELATION", Topic: generated.Title, Text: conflict.Subject, Alternatives: alternatives}); err != nil {
					return value, err
				}
			case domain.SynthesisAddGap:
				gap := operation.Item.Gap
				sources, unopened := synthesisProjectSources(gap.Sources, labels)
				if unopened != 0 {
					return value, synthesisOutputError()
				}
				if err := appendCheck(synthesisSemanticCheck{Kind: "GAP_CONTEXT", Topic: generated.Title, Question: gap.Question, Context: gap.Context, Sources: sources}); err != nil {
					return value, err
				}
			case domain.SynthesisAddSupport:
				target, found := current[string(operation.TargetItemID)]
				if !found {
					return value, synthesisOutputError()
				}
				var statement domain.SynthesisStatement
				if target.Kind == domain.SynthesisFactItem {
					statement = *target.Fact
				} else {
					statement = target.Conflict.Alternatives[*operation.AlternativeIndex]
				}
				statement.Sources = operation.Sources
				if err := statementCheck("ADDITIONAL_SUPPORT", generated.Title, "", "", statement); err != nil {
					return value, err
				}
			case domain.SynthesisResolveGap:
				target, found := current[string(operation.TargetItemID)]
				if !found || target.Gap == nil {
					return value, synthesisOutputError()
				}
				if err := statementCheck("GAP_RESOLUTION", generated.Title, target.Gap.Question, target.Gap.Context, *operation.Resolution); err != nil {
					return value, err
				}
			default:
				return value, synthesisOutputError()
			}
		}
	}
	// Even a no-op is independently checked against the incoming excerpts and
	// current notes, instead of treating an empty model delta as proven coverage.
	if len(value.Checks) == 0 {
		value.UnchangedNotes = projection.Notes
		if err := appendCheck(synthesisSemanticCheck{Kind: "NO_CHANGE"}); err != nil {
			return value, err
		}
	}
	return value, nil
}

func decodeSynthesisSemantic(raw []byte) (json.RawMessage, error) {
	if _, err := decodeSynthesisSemanticOutput(raw); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), raw...), nil
}

func decodeSynthesisSemanticOutput(raw []byte) (synthesisSemanticOutput, error) {
	object, err := synthesisObject(raw, "checks")
	if err != nil {
		return synthesisSemanticOutput{}, err
	}
	checks, err := synthesisArray[json.RawMessage](object["checks"], 1, maxSynthesisSemanticChecks)
	if err != nil {
		return synthesisSemanticOutput{}, err
	}
	result := synthesisSemanticOutput{Checks: make([]synthesisCheckVerdict, 0, len(checks))}
	for position, rawCheck := range checks {
		object, err := synthesisObject(rawCheck, "index", "verdict", "sources")
		if err != nil {
			return result, err
		}
		var index *int
		if json.Unmarshal(object["index"], &index) != nil || index == nil || *index != position+1 {
			return result, synthesisOutputError()
		}
		verdict, err := synthesisString(object["verdict"], 16, false)
		if err != nil || !validSynthesisVerdict(verdict) {
			return result, synthesisOutputError()
		}
		sources, err := synthesisArray[json.RawMessage](object["sources"], 0, domain.MaxSynthesisStatementSources)
		if err != nil {
			return result, err
		}
		check := synthesisCheckVerdict{Index: *index, Verdict: verdict, Sources: make([]synthesisSourceVerdict, 0, len(sources))}
		seen := make(map[string]bool, len(sources))
		for _, rawSource := range sources {
			sourceObject, err := synthesisObject(rawSource, "source", "verdict")
			if err != nil {
				return result, err
			}
			label, err := synthesisString(sourceObject["source"], 4, false)
			if err != nil || !synthesisLabel(label, 'S', domain.MaxSynthesisSources) || seen[label] {
				return result, synthesisOutputError()
			}
			seen[label] = true
			sourceVerdict, err := synthesisString(sourceObject["verdict"], 16, false)
			if err != nil || !validSynthesisVerdict(sourceVerdict) || verdict == "SUPPORTED" && sourceVerdict != "SUPPORTED" {
				return result, synthesisOutputError()
			}
			check.Sources = append(check.Sources, synthesisSourceVerdict{Source: label, Verdict: sourceVerdict})
		}
		result.Checks = append(result.Checks, check)
	}
	return result, nil
}

func bindSynthesisSemanticOutput(raw []byte, plan synthesisSemanticInput, input organizingapp.SynthesisGenerationInput, generated organizingapp.SynthesisGenerationResult, modelRunID foundation.ID) (organizingapp.SynthesisSemanticReceipt, error) {
	output, err := decodeSynthesisSemanticOutput(raw)
	if err != nil {
		return organizingapp.SynthesisSemanticReceipt{}, err
	}
	if len(output.Checks) != len(plan.Checks) {
		return organizingapp.SynthesisSemanticReceipt{}, synthesisOutputError()
	}
	accepted := true
	for index, verdict := range output.Checks {
		expected := plan.Checks[index]
		labels := make([]string, len(verdict.Sources))
		for sourceIndex, source := range verdict.Sources {
			labels[sourceIndex] = source.Source
			accepted = accepted && source.Verdict == "SUPPORTED"
		}
		if verdict.Index != expected.Index || !slices.Equal(labels, expected.Sources) {
			return organizingapp.SynthesisSemanticReceipt{}, synthesisOutputError()
		}
		accepted = accepted && verdict.Verdict == "SUPPORTED"
	}
	return organizingapp.SynthesisSemanticReceipt{
		ModelRunID: modelRunID, RequestHash: input.RequestHash,
		GenerationModelRunID: generated.ModelRunID, GenerationOutputHash: generated.OutputHash,
		OutputHash: synthesisHash(raw), CheckCount: len(plan.Checks), Accepted: accepted,
	}, nil
}

func validSynthesisVerdict(value string) bool {
	return value == "SUPPORTED" || value == "UNSUPPORTED" || value == "UNCERTAIN"
}
