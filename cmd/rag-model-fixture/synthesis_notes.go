package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	captureprofile "github.com/CodeZen-Lizhi/zhixu/internal/capture/profile"
	organizingagent "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/agent"
	organizingapplication "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

const (
	synthesisFixtureGenerateStage = "synthesis_generate"
	synthesisFixtureValidateStage = "synthesis_validate"
	synthesisFixtureProfileStage  = "capture_profile"
	synthesisFixtureTitle         = "Cache expiration"
	synthesisFixtureTopic         = "cache expiration"
	synthesisFixtureFact          = "Cache entries expire after five minutes."
	synthesisFixtureApplicability = "For the default configuration."
	synthesisFixtureGap           = "How is expiration handled during an outage?"
	synthesisFixtureGapContext    = "The supplied material does not describe outage behavior."
	synthesisFixtureRefresh       = "Refreshing invalidates the cache key."
	synthesisFixtureFirstSource   = "Cache source alpha. Cache entries expire after five minutes in the default configuration. The material does not explain expiration during an outage."
	synthesisFixtureSecondSource  = "Cache source beta. Cache entries expire after five minutes in the default configuration. In the high-traffic configuration, cache entries expire after ten minutes. Refreshing invalidates the cache key. The material does not explain expiration during an outage."
)

// Only the explicit smoke fixture synthesizes these marked sources. Other
// imported RAG material still completes a real, independently reviewed no-op.
var synthesisFixtureSnapshots = sync.OnceValues(loadSynthesisFixtureSnapshots)

func loadSynthesisFixtureSnapshots() (map[string]agentapplication.RuntimeSnapshot, error) {
	catalog := agentapplication.NewRuntimeCatalog()
	if err := organizingagent.RegisterSynthesisRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	if err := captureprofile.RegisterRuntimeCatalog(catalog); err != nil {
		return nil, err
	}
	profile := agentdomain.ModelProfileRef{ID: "synthesis-fixture", Version: "v1"}
	if err := catalog.RegisterProfile(agentapplication.ModelProfile{
		Ref: profile, Model: agentdomain.ModelRef{AdapterName: "openai-compatible", AdapterVersion: "fixture-v1", ModelID: "rag-smoke", ModelVersion: defaultModelVersion},
		Timeout: 5 * time.Second, MaxOutputTokens: 4096,
	}); err != nil {
		return nil, err
	}
	if err := catalog.Freeze(); err != nil {
		return nil, err
	}
	contracts := []struct {
		stage  string
		prompt agentdomain.PromptRef
		schema agentdomain.SchemaRef
	}{
		{synthesisFixtureGenerateStage, agentdomain.PromptRef{ID: organizingapplication.SynthesisDeltaPromptID, Version: organizingapplication.SynthesisRuntimeVersion}, agentdomain.SchemaRef{ID: agentdomain.SynthesisDeltaSchemaID, Version: organizingapplication.SynthesisRuntimeVersion}},
		{synthesisFixtureValidateStage, agentdomain.PromptRef{ID: organizingapplication.SynthesisSemanticPromptID, Version: organizingapplication.SynthesisRuntimeVersion}, agentdomain.SchemaRef{ID: agentdomain.SynthesisSemanticReviewSchemaID, Version: organizingapplication.SynthesisRuntimeVersion}},
		{synthesisFixtureProfileStage, captureprofile.PromptRef(), captureprofile.SchemaRef()},
	}
	snapshots := make(map[string]agentapplication.RuntimeSnapshot, len(contracts))
	for _, contract := range contracts {
		reduced := contract.schema
		if contract.stage == synthesisFixtureProfileStage {
			reduced = captureprofile.ReducedSchemaRef()
		}
		snapshot, err := catalog.Snapshot(contract.prompt, contract.schema, reduced, profile)
		if err != nil {
			return nil, err
		}
		snapshots[contract.stage] = snapshot
	}
	return snapshots, nil
}

// Compare the actual owner schemas after JSON normalization, including their
// nested limits. A notes/checks property alone must not select this fixture.
func synthesisNotesFixtureStage(raw json.RawMessage) string {
	snapshots, err := synthesisFixtureSnapshots()
	if err != nil {
		return ""
	}
	var actual any
	if json.Unmarshal(raw, &actual) != nil {
		return ""
	}
	for stage, snapshot := range snapshots {
		for _, schema := range []json.RawMessage{snapshot.Schema.JSONSchema, snapshot.ReducedSchema.JSONSchema} {
			var expected any
			if json.Unmarshal(schema, &expected) == nil && reflect.DeepEqual(actual, expected) {
				return stage
			}
		}
	}
	return synthesisNoteInterviewFixtureStage(raw)
}

func synthesisNotesFixtureResponse(stage string, input map[string]any) (string, error) {
	var output any
	var err error
	switch stage {
	case synthesisFixtureGenerateStage:
		output, err = synthesisFixtureGenerate(input)
	case synthesisFixtureValidateStage:
		output, err = synthesisFixtureValidate(input)
	case synthesisFixtureProfileStage:
		output, err = synthesisFixtureProfile(input)
	case synthesisFixtureInterviewStage:
		return synthesisNoteInterviewFixtureResponse(input)
	default:
		return "", fixtureError("synthesis_stage_unsupported", "unsupported synthesis fixture stage")
	}
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return "", fixtureError("synthesis_output_invalid", "synthesis fixture output could not be encoded")
	}
	snapshots, err := synthesisFixtureSnapshots()
	if err != nil {
		return "", fixtureError("synthesis_catalog_unavailable", "synthesis fixture catalog is unavailable")
	}
	if _, err := snapshots[stage].Schema.Decode(raw); err != nil {
		return "", fixtureError("synthesis_output_invalid", "synthesis fixture output violates its production schema")
	}
	return string(raw), nil
}

type synthesisFixtureSource struct {
	Label    string `json:"label"`
	Incoming bool   `json:"incoming"`
	Excerpt  string `json:"excerpt"`
}

type synthesisFixtureStatement struct {
	Text            string   `json:"text"`
	Applicability   string   `json:"applicability"`
	Sources         []string `json:"sources"`
	UnopenedSources int      `json:"unopened_sources"`
}

type synthesisFixtureNote struct {
	Label    string   `json:"label"`
	TopicKey string   `json:"topic_key"`
	Title    string   `json:"title"`
	Aliases  []string `json:"aliases"`
	Items    []struct {
		Label    string                     `json:"label"`
		Kind     string                     `json:"kind"`
		Fact     *synthesisFixtureStatement `json:"fact,omitempty"`
		Conflict json.RawMessage            `json:"conflict,omitempty"`
		Gap      json.RawMessage            `json:"gap,omitempty"`
	} `json:"items"`
}

func decodeSynthesisFixtureInput(input map[string]any, target any) error {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > agentapplication.MaxStructuredInputBytes {
		return fixtureError("synthesis_input_invalid", "synthesis fixture input is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return fixtureError("synthesis_input_invalid", "synthesis fixture input is invalid")
	}
	return nil
}

func synthesisFixtureSources(sources []synthesisFixtureSource) (map[string]synthesisFixtureSource, *synthesisFixtureSource, error) {
	if len(sources) == 0 || len(sources) > 256 {
		return nil, nil, fixtureError("synthesis_sources_invalid", "synthesis fixture sources are unavailable")
	}
	byLabel := make(map[string]synthesisFixtureSource, len(sources))
	var marked *synthesisFixtureSource
	incoming := false
	for index, source := range sources {
		if source.Label != fmt.Sprintf("S%03d", index+1) || source.Excerpt == "" {
			return nil, nil, fixtureError("synthesis_sources_invalid", "synthesis fixture source binding is invalid")
		}
		byLabel[source.Label] = source
		incoming = incoming || source.Incoming
		if source.Incoming && (strings.Contains(source.Excerpt, "Cache source alpha.") || strings.Contains(source.Excerpt, "Cache source beta.")) {
			text := strings.TrimSpace(source.Excerpt)
			if marked != nil || text != synthesisFixtureFirstSource && text != synthesisFixtureSecondSource {
				return nil, nil, fixtureError("synthesis_marked_source_invalid", "marked synthesis source differs from the documented fixture")
			}
			copy := source
			copy.Excerpt = text
			marked = &copy
		}
	}
	if !incoming {
		return nil, nil, fixtureError("synthesis_incoming_source_missing", "synthesis fixture input has no incoming source")
	}
	return byLabel, marked, nil
}

func synthesisFixtureGenerate(input map[string]any) (any, error) {
	var value struct {
		Notes   []synthesisFixtureNote   `json:"notes"`
		Sources []synthesisFixtureSource `json:"sources"`
	}
	if !hasExactAnyKeys(input, "notes", "sources") || decodeSynthesisFixtureInput(input, &value) != nil || value.Notes == nil {
		return nil, fixtureError("synthesis_input_invalid", "synthesis generation input is invalid")
	}
	byLabel, incoming, err := synthesisFixtureSources(value.Sources)
	if err != nil {
		return nil, err
	}
	if incoming == nil {
		return map[string]any{"notes": []any{}}, nil
	}
	var note *synthesisFixtureNote
	for index := range value.Notes {
		if value.Notes[index].TopicKey == synthesisFixtureTopic {
			if note != nil || value.Notes[index].Title != synthesisFixtureTitle {
				return nil, fixtureError("synthesis_note_invalid", "synthesis fixture note identity changed")
			}
			note = &value.Notes[index]
		}
	}
	if note == nil {
		if incoming.Excerpt != synthesisFixtureFirstSource {
			return nil, fixtureError("synthesis_initial_source_missing", "import the documented alpha source before beta")
		}
		return map[string]any{"notes": []any{map[string]any{
			"note": "", "topic_key": synthesisFixtureTopic, "title": synthesisFixtureTitle, "aliases": []string{},
			"operations": []any{
				map[string]any{"op": "ADD_FACT", "statement": synthesisFixtureWireStatement(synthesisFixtureFact, synthesisFixtureApplicability, incoming.Label)},
				map[string]any{"op": "ADD_GAP", "question": synthesisFixtureGap, "context": synthesisFixtureGapContext, "sources": []string{incoming.Label}},
			},
		}}}, nil
	}
	factLabel, fact, err := synthesisFixtureExistingFact(*note)
	if err != nil {
		return nil, err
	}
	previous := ""
	for _, label := range fact.Sources {
		source, found := byLabel[label]
		if !found {
			return nil, fixtureError("synthesis_prior_source_missing", "synthesis fixture historical source was not reopened")
		}
		if strings.TrimSpace(source.Excerpt) == incoming.Excerpt {
			return map[string]any{"notes": []any{}}, nil
		}
		if strings.TrimSpace(source.Excerpt) == synthesisFixtureFirstSource {
			previous = label
		}
	}
	if incoming.Excerpt != synthesisFixtureSecondSource || previous == "" || len(note.Items) != 2 {
		return nil, fixtureError("synthesis_update_invalid", "synthesis fixture update is not bound to the initial note")
	}
	return map[string]any{"notes": []any{map[string]any{
		"note": note.Label,
		"operations": []any{
			map[string]any{"op": "ADD_SUPPORT", "target": factLabel, "alternative": nil, "sources": []string{incoming.Label}},
			map[string]any{"op": "ADD_FACT", "statement": synthesisFixtureWireStatement(synthesisFixtureRefresh, "", incoming.Label)},
			map[string]any{"op": "ADD_CONFLICT", "subject": "Cache expiration depends on the configured mode.", "alternatives": []any{
				synthesisFixtureWireStatement(synthesisFixtureFact, synthesisFixtureApplicability, previous),
				synthesisFixtureWireStatement("Cache entries expire after ten minutes.", "For the high-traffic configuration.", incoming.Label),
			}},
		},
	}}}, nil
}

func synthesisFixtureExistingFact(note synthesisFixtureNote) (string, *synthesisFixtureStatement, error) {
	for _, item := range note.Items {
		if item.Kind == "FACT" && item.Fact != nil && item.Fact.Text == synthesisFixtureFact && item.Fact.Applicability == synthesisFixtureApplicability &&
			item.Fact.UnopenedSources == 0 && len(item.Fact.Sources) > 0 {
			return item.Label, item.Fact, nil
		}
	}
	return "", nil, fixtureError("synthesis_prior_fact_missing", "synthesis fixture initial fact was not preserved")
}

func synthesisFixtureWireStatement(text, applicability, source string) map[string]any {
	return map[string]any{"text": text, "applicability": applicability, "sources": []string{source}}
}

// The fixture accepts every expected check only after verifying the real short
// source bindings. This exercises the independent model call, not model quality.
func synthesisFixtureValidate(input map[string]any) (any, error) {
	var value struct {
		Checks []struct {
			Index         int               `json:"index"`
			Kind          string            `json:"kind"`
			Topic         string            `json:"topic"`
			Text          string            `json:"text"`
			Applicability string            `json:"applicability"`
			Question      string            `json:"question"`
			Context       string            `json:"context"`
			Sources       []string          `json:"sources"`
			Alternatives  []json.RawMessage `json:"alternatives"`
		} `json:"checks"`
		Sources        []synthesisFixtureSource `json:"sources"`
		UnchangedNotes []synthesisFixtureNote   `json:"unchanged_notes"`
	}
	if !hasExactAnyKeys(input, "checks", "sources", "unchanged_notes") || decodeSynthesisFixtureInput(input, &value) != nil || len(value.Checks) == 0 {
		return nil, fixtureError("synthesis_review_invalid", "synthesis review input is invalid")
	}
	byLabel, _, err := synthesisFixtureSources(value.Sources)
	if err != nil {
		return nil, err
	}
	checks := make([]any, 0, len(value.Checks))
	for index, check := range value.Checks {
		if check.Index != index+1 || check.Sources == nil || check.Alternatives == nil {
			return nil, fixtureError("synthesis_review_invalid", "synthesis review check binding is invalid")
		}
		switch check.Kind {
		case "NO_CHANGE":
			if len(value.Checks) != 1 || len(check.Sources) != 0 || value.UnchangedNotes == nil {
				return nil, fixtureError("synthesis_no_change_invalid", "NO_CHANGE review is missing its unchanged note context")
			}
		case "ASSERTION", "ADDITIONAL_SUPPORT", "CONFLICT_ALTERNATIVE", "GAP_CONTEXT":
			if check.Topic != synthesisFixtureTitle || len(check.Sources) == 0 {
				return nil, fixtureError("synthesis_review_invalid", "synthesis assertion has no expected source binding")
			}
		case "CONFLICT_RELATION":
			if check.Topic != synthesisFixtureTitle || len(check.Sources) != 0 || len(check.Alternatives) != 2 {
				return nil, fixtureError("synthesis_conflict_invalid", "synthesis conflict review is not bound to two alternatives")
			}
		default:
			return nil, fixtureError("synthesis_review_kind_invalid", "unsupported synthesis fixture review kind")
		}
		sources := make([]any, 0, len(check.Sources))
		for _, label := range check.Sources {
			if _, found := byLabel[label]; !found {
				return nil, fixtureError("synthesis_review_source_missing", "synthesis review references an unavailable source")
			}
			sources = append(sources, map[string]any{"source": label, "verdict": "SUPPORTED"})
		}
		checks = append(checks, map[string]any{"index": check.Index, "verdict": "SUPPORTED", "sources": sources})
	}
	return map[string]any{"checks": checks}, nil
}

func synthesisFixtureProfile(input map[string]any) (any, error) {
	var value struct {
		SchemaVersion string `json:"schema_version"`
		Evidence      []struct {
			Label       string   `json:"label"`
			Sequence    int      `json:"sequence"`
			HeadingPath []string `json:"heading_path"`
			Content     string   `json:"content"`
		} `json:"evidence"`
	}
	if !hasExactAnyKeys(input, "schema_version", "evidence") || decodeSynthesisFixtureInput(input, &value) != nil ||
		value.SchemaVersion != "capture-profile-input/v1" || len(value.Evidence) == 0 {
		return nil, fixtureError("capture_profile_input_invalid", "Capture Profile fixture input is invalid")
	}
	evidence := value.Evidence[0]
	if evidence.Label != "E0001" || strings.TrimSpace(evidence.Content) == "" {
		return nil, fixtureError("capture_profile_evidence_invalid", "Capture Profile fixture evidence is unavailable")
	}
	topic, summary := "Workspace material", "A source excerpt supplied by the current Workspace."
	point := string([]rune(strings.TrimSpace(evidence.Content))[:min(len([]rune(strings.TrimSpace(evidence.Content))), 256)])
	if strings.Contains(evidence.Content, "Cache source alpha.") || strings.Contains(evidence.Content, "Cache source beta.") {
		topic, summary, point = synthesisFixtureTitle, "Cache expiration and refresh behavior.", synthesisFixtureFact
	}
	labels := []string{evidence.Label}
	return captureprofile.Output{
		ResultType: agentdomain.ResultTypeDocumentKnowledgeProfile, SchemaID: captureprofile.SchemaID, SchemaVersion: capturedomain.ProfileSchemaVersion,
		Summary: summary, Topics: []captureprofile.CandidateOutput{{Label: topic, Aliases: []string{}, EvidenceLabels: labels}},
		Terms: []captureprofile.CandidateOutput{}, KnowledgePoints: []captureprofile.PointOutput{{Text: point, EvidenceLabels: labels}}, Examples: []captureprofile.PointOutput{},
	}, nil
}
