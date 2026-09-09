package agent

import (
	"encoding/json"
	"fmt"
	"reflect"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type synthesisInputSource struct {
	Label    string `json:"label"`
	Incoming bool   `json:"incoming"`
	Excerpt  string `json:"excerpt"`
}

type synthesisInputStatement struct {
	Text            string   `json:"text"`
	Applicability   string   `json:"applicability"`
	Sources         []string `json:"sources"`
	UnopenedSources int      `json:"unopened_sources"`
}

type synthesisInputConflict struct {
	Subject      string                    `json:"subject"`
	Alternatives []synthesisInputStatement `json:"alternatives"`
}

type synthesisInputGap struct {
	Question        string                   `json:"question"`
	Context         string                   `json:"context"`
	Sources         []string                 `json:"sources"`
	UnopenedSources int                      `json:"unopened_sources"`
	Resolution      *synthesisInputStatement `json:"resolution"`
}

type synthesisInputItem struct {
	Label    string                   `json:"label"`
	Kind     domain.SynthesisItemKind `json:"kind"`
	Fact     *synthesisInputStatement `json:"fact,omitempty"`
	Conflict *synthesisInputConflict  `json:"conflict,omitempty"`
	Gap      *synthesisInputGap       `json:"gap,omitempty"`
}

type synthesisInputNote struct {
	Label    string               `json:"label"`
	TopicKey string               `json:"topic_key"`
	Title    string               `json:"title"`
	Aliases  []string             `json:"aliases"`
	Items    []synthesisInputItem `json:"items"`
}

type synthesisProviderInput struct {
	Notes   []synthesisInputNote   `json:"notes"`
	Sources []synthesisInputSource `json:"sources"`
}

func synthesisInput(input organizingapp.SynthesisGenerationInput) synthesisProviderInput {
	value := synthesisProviderInput{Notes: make([]synthesisInputNote, 0, len(input.Notes)), Sources: make([]synthesisInputSource, len(input.Sources))}
	labels := make(map[domain.SynthesisSourceRef]string, len(input.Sources))
	for index, source := range input.Sources {
		label := fmt.Sprintf("S%03d", index+1)
		labels[source.Reference] = label
		value.Sources[index] = synthesisInputSource{Label: label, Incoming: source.Reference.Source == input.SourceEvent.Source, Excerpt: source.Text}
	}
	for index, candidate := range input.Notes {
		note := synthesisInputNote{
			Label: fmt.Sprintf("N%03d", index+1), TopicKey: candidate.Note.TopicKey, Title: candidate.Note.Title,
			Aliases: append([]string{}, candidate.Note.Aliases...), Items: make([]synthesisInputItem, 0, len(candidate.Revision.Items)),
		}
		for itemIndex, item := range candidate.Revision.Items {
			projected := synthesisInputItem{Label: fmt.Sprintf("I%03d", itemIndex+1), Kind: item.Kind}
			switch item.Kind {
			case domain.SynthesisFactItem:
				statement := synthesisProjectStatement(*item.Fact, labels)
				projected.Fact = &statement
			case domain.SynthesisConflictItem:
				conflict := synthesisInputConflict{Subject: item.Conflict.Subject, Alternatives: make([]synthesisInputStatement, len(item.Conflict.Alternatives))}
				for alternative, statement := range item.Conflict.Alternatives {
					conflict.Alternatives[alternative] = synthesisProjectStatement(statement, labels)
				}
				projected.Conflict = &conflict
			case domain.SynthesisGapItem:
				sources, unopened := synthesisProjectSources(item.Gap.Sources, labels)
				gap := synthesisInputGap{Question: item.Gap.Question, Context: item.Gap.Context, Sources: sources, UnopenedSources: unopened}
				if item.Gap.Resolution != nil {
					statement := synthesisProjectStatement(*item.Gap.Resolution, labels)
					gap.Resolution = &statement
				}
				projected.Gap = &gap
			}
			note.Items = append(note.Items, projected)
		}
		value.Notes = append(value.Notes, note)
	}
	return value
}

func synthesisProjectStatement(statement domain.SynthesisStatement, labels map[domain.SynthesisSourceRef]string) synthesisInputStatement {
	sources, unopened := synthesisProjectSources(statement.Sources, labels)
	return synthesisInputStatement{Text: statement.Text, Applicability: statement.Applicability, Sources: sources, UnopenedSources: unopened}
}

func synthesisProjectSources(references []domain.SynthesisSourceRef, labels map[domain.SynthesisSourceRef]string) ([]string, int) {
	values := make([]string, 0, len(references))
	unopened := 0
	for _, reference := range references {
		if label, ok := labels[reference]; ok {
			values = append(values, label)
		} else {
			unopened++
		}
	}
	return values, unopened
}

func encodeSynthesisInput(input any) ([]byte, error) {
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) == 0 || len(encoded) > agentapp.MaxStructuredInputBytes {
		return nil, synthesisError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeSynthesisModelInputTooLarge, false, "synthesis model context exceeds its bounded input")
	}
	return encoded, nil
}

func bindSynthesisOutput(raw []byte, input organizingapp.SynthesisGenerationInput, modelRunID foundation.ID, nextID func() (foundation.ID, error)) (organizingapp.SynthesisGenerationResult, error) {
	wire, err := decodeSynthesisWire(raw)
	if err != nil {
		return organizingapp.SynthesisGenerationResult{}, err
	}
	result := organizingapp.SynthesisGenerationResult{ModelRunID: modelRunID, RequestHash: input.RequestHash, OutputHash: synthesisHash(raw), Notes: make([]organizingapp.SynthesisGeneratedNote, 0, len(wire.Notes))}
	notes := make(map[string]organizingapp.SynthesisGenerationNote, len(input.Notes))
	identities := make(map[foundation.ID]bool)
	for index, candidate := range input.Notes {
		notes[fmt.Sprintf("N%03d", index+1)] = candidate
		for _, item := range candidate.Revision.Items {
			identities[item.ID] = true
		}
	}
	catalog := make([]domain.SynthesisLabelledSource, len(input.Sources))
	for index, source := range input.Sources {
		catalog[index] = domain.SynthesisLabelledSource{Label: fmt.Sprintf("S%03d", index+1), Reference: source.Reference}
	}
	for _, value := range wire.Notes {
		generated := organizingapp.SynthesisGeneratedNote{TopicKey: value.TopicKey, Title: value.Title, Aliases: append([]string{}, value.Aliases...), Delta: domain.SynthesisDelta{Operations: make([]domain.SynthesisOperation, 0, len(value.Operations))}}
		targets := make(map[string]foundation.ID)
		if value.Label != "" {
			candidate, ok := notes[value.Label]
			if !ok {
				return result, synthesisOutputError()
			}
			generated.NoteID, generated.BaseRevisionID = candidate.Note.ID, candidate.Revision.ID
			generated.TopicKey, generated.Title, generated.Aliases = candidate.Note.TopicKey, candidate.Note.Title, append([]string{}, candidate.Note.Aliases...)
			for index, item := range candidate.Revision.Items {
				targets[fmt.Sprintf("I%03d", index+1)] = item.ID
			}
		}
		for _, wireOperation := range value.Operations {
			operation, err := bindSynthesisOperation(wireOperation, input.SourceEvent.Source.WorkspaceID, catalog, targets, nextID)
			if err != nil {
				return result, err
			}
			if operation.Item != nil {
				if identities[operation.Item.ID] {
					return result, synthesisOutputError()
				}
				identities[operation.Item.ID] = true
			}
			generated.Delta.Operations = append(generated.Delta.Operations, operation)
		}
		result.Notes = append(result.Notes, generated)
	}
	if err := result.Validate(input); err != nil {
		return organizingapp.SynthesisGenerationResult{}, synthesisOutputError()
	}
	return result, nil
}

func bindSynthesisOperation(wire synthesisWireOperation, workspaceID foundation.ID, catalog []domain.SynthesisLabelledSource, targets map[string]foundation.ID, nextID func() (foundation.ID, error)) (domain.SynthesisOperation, error) {
	value := domain.SynthesisOperation{Kind: wire.Kind}
	resolve := func(labels []string) ([]domain.SynthesisSourceRef, error) {
		return domain.ResolveSynthesisSourceLabels(workspaceID, labels, catalog)
	}
	statement := func(wire synthesisWireStatement) (domain.SynthesisStatement, error) {
		sources, err := resolve(wire.Sources)
		return domain.SynthesisStatement{Text: wire.Text, Applicability: wire.Applicability, Sources: sources}, err
	}
	var err error
	switch wire.Kind {
	case domain.SynthesisAddFact, domain.SynthesisAddConflict, domain.SynthesisAddGap:
		id, err := nextID()
		if err != nil {
			return value, err
		}
		item := domain.SynthesisItem{ID: id}
		switch wire.Kind {
		case domain.SynthesisAddFact:
			fact, err := statement(*wire.Statement)
			if err != nil {
				return value, synthesisOutputError()
			}
			item.Kind, item.Fact = domain.SynthesisFactItem, &fact
		case domain.SynthesisAddConflict:
			conflict := domain.SynthesisConflictContent{Subject: wire.Subject, Alternatives: make([]domain.SynthesisStatement, 0, len(wire.Alternatives))}
			for _, alternative := range wire.Alternatives {
				bound, err := statement(alternative)
				if err != nil {
					return value, synthesisOutputError()
				}
				conflict.Alternatives = append(conflict.Alternatives, bound)
			}
			item.Kind, item.Conflict = domain.SynthesisConflictItem, &conflict
		case domain.SynthesisAddGap:
			sources, err := resolve(wire.Sources)
			if err != nil {
				return value, synthesisOutputError()
			}
			item.Kind, item.Gap = domain.SynthesisGapItem, &domain.SynthesisGapContent{Question: wire.Question, Context: wire.Context, Sources: sources}
		}
		value.Item = &item
	case domain.SynthesisAddSupport:
		value.TargetItemID, value.AlternativeIndex = targets[wire.Target], wire.Alternative
		value.Sources, err = resolve(wire.Sources)
	case domain.SynthesisResolveGap:
		value.TargetItemID = targets[wire.Target]
		resolved, resolveErr := statement(*wire.Statement)
		value.Resolution, err = &resolved, resolveErr
	default:
		return value, synthesisOutputError()
	}
	if err != nil || value.Validate(workspaceID) != nil {
		return value, synthesisOutputError()
	}
	return value, nil
}

func validateSynthesisBoundOutput(raw []byte, input organizingapp.SynthesisGenerationInput, stored organizingapp.SynthesisGenerationResult) error {
	ids := make([]foundation.ID, 0)
	for _, note := range stored.Notes {
		for _, operation := range note.Delta.Operations {
			if operation.Item != nil {
				ids = append(ids, operation.Item.ID)
			}
		}
	}
	position := 0
	bound, err := bindSynthesisOutput(raw, input, stored.ModelRunID, func() (foundation.ID, error) {
		if position >= len(ids) {
			return "", synthesisOutputError()
		}
		value := ids[position]
		position++
		return value, nil
	})
	if err != nil || position != len(ids) || !reflect.DeepEqual(bound, stored) {
		return synthesisOutputError()
	}
	return nil
}
