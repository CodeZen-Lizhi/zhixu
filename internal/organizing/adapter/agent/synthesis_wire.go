package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

type synthesisWireOutput struct{ Notes []synthesisWireNote }

type synthesisWireNote struct {
	Label      string
	TopicKey   string
	Title      string
	Aliases    []string
	Operations []synthesisWireOperation
}

type synthesisWireStatement struct {
	Text          string   `json:"text"`
	Applicability string   `json:"applicability"`
	Sources       []string `json:"sources"`
}

type synthesisWireOperation struct {
	Kind         domain.SynthesisOperationKind
	Target       string
	Alternative  *int
	Statement    *synthesisWireStatement
	Subject      string
	Alternatives []synthesisWireStatement
	Question     string
	Context      string
	Sources      []string
}

func decodeSynthesisDelta(raw []byte) (json.RawMessage, error) {
	if _, err := decodeSynthesisWire(raw); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), raw...), nil
}

func decodeSynthesisWire(raw []byte) (synthesisWireOutput, error) {
	object, err := synthesisObject(raw, "notes")
	if err != nil {
		return synthesisWireOutput{}, err
	}
	notes, err := synthesisArray[json.RawMessage](object["notes"], 0, organizingapp.MaxSynthesisGeneratedNotes)
	if err != nil {
		return synthesisWireOutput{}, err
	}
	result := synthesisWireOutput{Notes: make([]synthesisWireNote, 0, len(notes))}
	seen := make(map[string]bool)
	for _, rawNote := range notes {
		note, err := decodeSynthesisWireNote(rawNote)
		if err != nil {
			return synthesisWireOutput{}, err
		}
		key := note.Label
		if key == "" {
			key = "new:" + note.TopicKey
		}
		if seen[key] {
			return synthesisWireOutput{}, synthesisOutputError()
		}
		seen[key] = true
		result.Notes = append(result.Notes, note)
	}
	return result, nil
}

func decodeSynthesisWireNote(raw []byte) (synthesisWireNote, error) {
	object, err := synthesisObject(raw)
	if err != nil {
		return synthesisWireNote{}, err
	}
	label, err := synthesisString(object["note"], 4, true)
	if err != nil {
		return synthesisWireNote{}, err
	}
	value := synthesisWireNote{Label: label}
	if label == "" {
		if !synthesisKeys(object, "note", "topic_key", "title", "aliases", "operations") {
			return value, synthesisOutputError()
		}
		value.TopicKey, err = synthesisString(object["topic_key"], domain.MaxSynthesisTopicKeyBytes, false)
		if err != nil {
			return value, err
		}
		value.Title, err = synthesisString(object["title"], domain.MaxSynthesisTitleBytes, false)
		if err != nil {
			return value, err
		}
		value.Aliases, err = synthesisArray[string](object["aliases"], 0, domain.MaxSynthesisAliases)
		if err != nil {
			return value, err
		}
		if domain.ValidateSynthesisTopic(value.TopicKey, value.Title, value.Aliases) != nil {
			return value, synthesisOutputError()
		}
	} else if !synthesisKeys(object, "note", "operations") || !synthesisLabel(label, 'N', organizingapp.MaxSynthesisCandidateNotes) {
		return value, synthesisOutputError()
	}
	minimum := 0
	if label == "" {
		minimum = 1
	}
	operations, err := synthesisArray[json.RawMessage](object["operations"], minimum, domain.MaxSynthesisOperations)
	if err != nil {
		return value, err
	}
	value.Operations = make([]synthesisWireOperation, 0, len(operations))
	for _, rawOperation := range operations {
		operation, err := decodeSynthesisWireOperation(rawOperation)
		if err != nil {
			return value, err
		}
		value.Operations = append(value.Operations, operation)
	}
	return value, nil
}

func decodeSynthesisWireOperation(raw []byte) (synthesisWireOperation, error) {
	object, err := synthesisObject(raw)
	if err != nil {
		return synthesisWireOperation{}, err
	}
	kind, err := synthesisString(object["op"], 32, false)
	if err != nil {
		return synthesisWireOperation{}, err
	}
	value := synthesisWireOperation{Kind: domain.SynthesisOperationKind(kind)}
	switch value.Kind {
	case domain.SynthesisAddFact:
		if !synthesisKeys(object, "op", "statement") {
			return value, synthesisOutputError()
		}
		statement, err := decodeSynthesisStatement(object["statement"])
		if err != nil {
			return value, err
		}
		value.Statement = &statement
	case domain.SynthesisAddConflict:
		if !synthesisKeys(object, "op", "subject", "alternatives") {
			return value, synthesisOutputError()
		}
		value.Subject, err = synthesisString(object["subject"], domain.MaxSynthesisTextBytes, false)
		if err != nil {
			return value, err
		}
		alternatives, err := synthesisArray[json.RawMessage](object["alternatives"], 2, domain.MaxSynthesisAlternatives)
		if err != nil {
			return value, err
		}
		value.Alternatives = make([]synthesisWireStatement, 0, len(alternatives))
		seen := make(map[string]bool)
		for _, rawStatement := range alternatives {
			statement, err := decodeSynthesisStatement(rawStatement)
			if err != nil {
				return value, err
			}
			key := statement.Text + "\x00" + statement.Applicability
			if seen[key] {
				return value, synthesisOutputError()
			}
			seen[key] = true
			value.Alternatives = append(value.Alternatives, statement)
		}
	case domain.SynthesisAddGap:
		if !synthesisKeys(object, "op", "question", "context", "sources") {
			return value, synthesisOutputError()
		}
		value.Question, err = synthesisString(object["question"], domain.MaxSynthesisTextBytes, false)
		if err != nil {
			return value, err
		}
		value.Context, err = synthesisString(object["context"], domain.MaxSynthesisContextBytes, true)
		if err != nil {
			return value, err
		}
		value.Sources, err = synthesisSourceLabels(object["sources"], false)
		if err != nil {
			return value, err
		}
	case domain.SynthesisAddSupport:
		if !synthesisKeys(object, "op", "target", "alternative", "sources") {
			return value, synthesisOutputError()
		}
		value.Target, err = synthesisString(object["target"], 4, false)
		if err != nil || !synthesisLabel(value.Target, 'I', domain.MaxSynthesisItems) {
			return value, synthesisOutputError()
		}
		if !bytes.Equal(bytes.TrimSpace(object["alternative"]), []byte("null")) {
			var index int
			if json.Unmarshal(object["alternative"], &index) != nil || index < 0 || index >= domain.MaxSynthesisAlternatives {
				return value, synthesisOutputError()
			}
			value.Alternative = &index
		}
		value.Sources, err = synthesisSourceLabels(object["sources"], true)
		if err != nil {
			return value, err
		}
	case domain.SynthesisResolveGap:
		if !synthesisKeys(object, "op", "target", "resolution") {
			return value, synthesisOutputError()
		}
		value.Target, err = synthesisString(object["target"], 4, false)
		if err != nil || !synthesisLabel(value.Target, 'I', domain.MaxSynthesisItems) {
			return value, synthesisOutputError()
		}
		statement, err := decodeSynthesisStatement(object["resolution"])
		if err != nil {
			return value, err
		}
		value.Statement = &statement
	default:
		return value, synthesisOutputError()
	}
	return value, nil
}

func decodeSynthesisStatement(raw []byte) (synthesisWireStatement, error) {
	object, err := synthesisObject(raw, "text", "applicability", "sources")
	if err != nil {
		return synthesisWireStatement{}, err
	}
	text, err := synthesisString(object["text"], domain.MaxSynthesisTextBytes, false)
	if err != nil {
		return synthesisWireStatement{}, err
	}
	conditions, err := synthesisString(object["applicability"], domain.MaxSynthesisContextBytes, true)
	if err != nil {
		return synthesisWireStatement{}, err
	}
	sources, err := synthesisSourceLabels(object["sources"], true)
	if err != nil {
		return synthesisWireStatement{}, err
	}
	return synthesisWireStatement{Text: text, Applicability: conditions, Sources: sources}, nil
}

// The shared strict JSON decoder owns Unicode, duplicate-key, depth and EOF
// checks. Explicit key sets also reject missing nullable values and Go's
// otherwise case-insensitive struct-field matching at this Provider boundary.
func synthesisObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = organizingapp.MaxSynthesisModelOutputBytes
	limits.MaxStringBytes = domain.MaxSynthesisTextBytes
	limits.MaxArrayItems = maxSynthesisSemanticChecks
	limits.MaxObjectFields = 12
	object, err := agentdomain.DecodeStrict[map[string]json.RawMessage](raw, limits, nil)
	if err != nil || len(keys) > 0 && !synthesisKeys(object, keys...) {
		return nil, synthesisOutputError()
	}
	return object, nil
}

func synthesisKeys(object map[string]json.RawMessage, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func synthesisString(raw json.RawMessage, maximum int, optional bool) (string, error) {
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil || !validSynthesisText(*value, maximum, optional) {
		return "", synthesisOutputError()
	}
	return *value, nil
}

func synthesisArray[T any](raw json.RawMessage, minimum, maximum int) ([]T, error) {
	var values []T
	if json.Unmarshal(raw, &values) != nil || values == nil || len(values) < minimum || len(values) > maximum {
		return nil, synthesisOutputError()
	}
	return values, nil
}

func synthesisSourceLabels(raw json.RawMessage, required bool) ([]string, error) {
	minimum := 0
	if required {
		minimum = 1
	}
	values, err := synthesisArray[string](raw, minimum, domain.MaxSynthesisStatementSources)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !synthesisLabel(value, 'S', domain.MaxSynthesisSources) || seen[value] {
			return nil, synthesisOutputError()
		}
		seen[value] = true
	}
	return values, nil
}

func validSynthesisText(value string, maximum int, optional bool) bool {
	if (!optional && value == "") || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '\u2028' || character == '\u2029' {
			return false
		}
	}
	return true
}

func synthesisLabel(value string, prefix byte, maximum int) bool {
	if len(value) != 4 || value[0] != prefix {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	index, err := strconv.Atoi(value[1:])
	return err == nil && index >= 1 && index <= maximum && value == fmt.Sprintf("%c%03d", prefix, index)
}

func synthesisOutputError() error {
	return synthesisError(foundation.ErrorNonRetryableFailure, organizingapp.ErrorCodeSynthesisModelOutputInvalid, false, "synthesis model output is invalid")
}
