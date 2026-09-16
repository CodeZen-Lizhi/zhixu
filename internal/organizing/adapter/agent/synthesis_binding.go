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
	Label     string                `json:"label"`
	TopicKey  string                `json:"topic_key"`
	Title     string                `json:"title"`
	Aliases   []string              `json:"aliases"`
	Published bool                  `json:"published,omitempty"`
	Anchor    *synthesisInputAnchor `json:"anchor,omitempty"`
	Items     []synthesisInputItem  `json:"items"`
}

type synthesisInputAnchor struct {
	ScopeVersion   int64    `json:"scope_version"`
	Topics         []string `json:"topics"`
	Audiences      []string `json:"audiences"`
	Description    string   `json:"description"`
	AllowedSources []string `json:"allowed_sources"`
}

type synthesisProviderInput struct {
	FusionTarget   string                 `json:"fusion_target,omitempty"`
	RefreshUpdates []synthesisInputItem   `json:"refresh_updates,omitempty"`
	Goal           string                 `json:"user_goal,omitempty"`
	Notes          []synthesisInputNote   `json:"notes"`
	Sources        []synthesisInputSource `json:"sources"`
}

func synthesisInput(input organizingapp.SynthesisGenerationInput) synthesisProviderInput {
	value := synthesisProviderInput{Notes: make([]synthesisInputNote, 0, len(input.Notes)), Sources: make([]synthesisInputSource, len(input.Sources))}
	if input.Goal != nil {
		value.Goal = input.Goal.Text
	}
	labels := make(map[domain.SynthesisSourceRef]string, len(input.Sources))
	for index, source := range input.Sources {
		label := fmt.Sprintf("S%03d", index+1)
		labels[source.Reference] = label
		value.Sources[index] = synthesisInputSource{Label: label, Incoming: input.Goal != nil || source.Reference.Source == input.SourceEvent.Source, Excerpt: source.Text}
	}
	for index, candidate := range input.Notes {
		note := synthesisInputNote{
			Label: fmt.Sprintf("N%03d", index+1), TopicKey: candidate.Note.TopicKey, Title: candidate.Note.Title,
			Aliases: append([]string{}, candidate.Note.Aliases...), Published: candidate.PublicationID != "", Items: make([]synthesisInputItem, 0, len(candidate.Revision.Items)),
		}
		if candidate.Anchor != nil {
			allowed := make([]string, 0, len(candidate.Anchor.AllowedSources))
			for _, ref := range candidate.Anchor.AllowedSources {
				if label, ok := labels[ref]; ok {
					allowed = append(allowed, label)
				}
			}
			note.Anchor = &synthesisInputAnchor{ScopeVersion: candidate.Anchor.ScopeVersion,
				Topics: append([]string{}, candidate.Anchor.Scope.Topics...), Audiences: append([]string{}, candidate.Anchor.Scope.Audiences...),
				Description: candidate.Anchor.Scope.Description, AllowedSources: allowed}
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
			for _, supplement := range candidate.Supplements {
				if supplement.ItemID != item.ID {
					continue
				}
				var sources *[]string
				var unopened *int
				switch supplement.Slot {
				case "FACT":
					if projected.Fact != nil {
						sources = &projected.Fact.Sources
						unopened = &projected.Fact.UnopenedSources
					}
				case "CONFLICT":
					if projected.Conflict != nil && supplement.AlternativeIndex >= 0 && supplement.AlternativeIndex < len(projected.Conflict.Alternatives) {
						alt := &projected.Conflict.Alternatives[supplement.AlternativeIndex]
						sources = &alt.Sources
						unopened = &alt.UnopenedSources
					}
				case "GAP_CONTEXT":
					if projected.Gap != nil {
						sources = &projected.Gap.Sources
						unopened = &projected.Gap.UnopenedSources
					}
				case "GAP_RESOLUTION":
					if projected.Gap != nil && projected.Gap.Resolution != nil {
						sources = &projected.Gap.Resolution.Sources
						unopened = &projected.Gap.Resolution.UnopenedSources
					}
				}
				if sources == nil {
					continue
				}
				if label, ok := labels[supplement.Reference]; ok {
					present := false
					for _, existing := range *sources {
						if existing == label {
							present = true
							break
						}
					}
					if !present {
						*sources = append(*sources, label)
					}
				} else {
					*unopened++
				}
			}
			note.Items = append(note.Items, projected)
		}
		if organizingapp.IsSynthesisFusionSemanticVersion(input.SemanticPromptVersion) && input.SourceEvent.Fusion != nil && candidate.Note.ID == input.SourceEvent.Fusion.NoteID {
			value.FusionTarget = note.Label
		}
		value.Notes = append(value.Notes, note)
	}
	if input.BodyRefresh != nil {
		value.RefreshUpdates = synthesisRefreshProjection(input, labels)
		wanted := map[string]bool{}
		for _, item := range value.RefreshUpdates {
			wanted[item.Label] = true
		}
		for index := range value.Notes {
			var items []synthesisInputItem
			for _, item := range value.Notes[index].Items {
				if wanted[item.Label] {
					items = append(items, item)
				}
			}
			value.Notes[index].Items = items
		}
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
	decode := decodeSynthesisWire
	if input.BodyRefresh != nil {
		decode = decodeSynthesisRefreshWire
	}
	if synthesisPromptVersion(input) == organizingapp.SynthesisBodyPromptVersion {
		decode = decodeSynthesisBodyWire
	}
	wire, err := decode(raw)
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
			var operation domain.SynthesisOperation
			var err error
			if input.BodyRefresh != nil {
				operation, err = organizingapp.BodyRefreshOperation(input, targets[wireOperation.Target])
			} else {
				operation, err = bindSynthesisWireOperation(wireOperation, input.SourceEvent.Source.WorkspaceID, catalog, targets, notes, value.Label, nextID)
			}
			if err != nil {
				return result, err
			}
			if candidate, ok := notes[value.Label]; ok && candidate.Anchor != nil && !synthesisOperationAllowedForAnchor(operation, candidate) {
				return result, synthesisOutputError()
			}
			if operation.Item != nil && operation.Kind != domain.SynthesisRefreshItem {
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

func bindSynthesisWireOperation(wire synthesisWireOperation, workspaceID foundation.ID, catalog []domain.SynthesisLabelledSource, targets map[string]foundation.ID, notes map[string]organizingapp.SynthesisGenerationNote, targetNote string, nextID func() (foundation.ID, error)) (domain.SynthesisOperation, error) {
	if wire.Kind != "INCLUDE_ITEM" {
		return bindSynthesisOperation(wire, workspaceID, catalog, targets, nextID)
	}
	if wire.IncludeNote == targetNote {
		return domain.SynthesisOperation{}, synthesisOutputError()
	}
	candidate, found := notes[wire.IncludeNote]
	if !found || candidate.PublicationID == "" {
		return domain.SynthesisOperation{}, synthesisOutputError()
	}
	index := 0
	for ; index < len(candidate.Revision.Items); index++ {
		if wire.IncludeItem == fmt.Sprintf("I%03d", index+1) {
			break
		}
	}
	if index == len(candidate.Revision.Items) {
		return domain.SynthesisOperation{}, synthesisOutputError()
	}
	itemID, err := nextID()
	if err != nil {
		return domain.SynthesisOperation{}, err
	}
	operation, err := domain.IncludeSynthesisPublishedItem(candidate.Revision, candidate.PublicationID, candidate.Revision.Items[index].ID, itemID)
	if err != nil {
		return domain.SynthesisOperation{}, synthesisOutputError()
	}
	return operation, nil
}

func synthesisOperationAllowedForAnchor(operation domain.SynthesisOperation, candidate organizingapp.SynthesisGenerationNote) bool {
	allowed := make(map[string]bool)
	for _, item := range candidate.Revision.Items {
		for _, ref := range item.SourceReferences() {
			key, err := ref.IdentityKey()
			if err == nil {
				allowed[key] = true
			}
		}
	}
	for _, ref := range candidate.Anchor.AllowedSources {
		key, err := ref.IdentityKey()
		if err == nil {
			allowed[key] = true
		}
	}
	for _, supplement := range candidate.Supplements {
		key, err := supplement.Reference.IdentityKey()
		if err == nil {
			allowed[key] = true
		}
	}
	refs := operation.Sources
	if operation.Item != nil {
		refs = append(refs, operation.Item.SourceReferences()...)
	}
	if operation.Resolution != nil {
		refs = append(refs, operation.Resolution.Sources...)
	}
	for _, ref := range refs {
		key, err := ref.IdentityKey()
		if err != nil || !allowed[key] {
			return false
		}
	}
	return true
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
			if operation.Item != nil && operation.Kind != domain.SynthesisRefreshItem {
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
