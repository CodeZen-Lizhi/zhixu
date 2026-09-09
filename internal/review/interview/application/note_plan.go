package application

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

const MaxNotePlanBytes = 256 * 1024
const UnresolvedNoteGapPoint = "Insufficient evidence; 不足以判断，需要补充资料。"

type notePlanWire struct {
	Questions []noteQuestionWire `json:"questions"`
}
type noteQuestionWire struct {
	ItemLabel         string             `json:"item_label"`
	Prompt            string             `json:"prompt"`
	AnswerPointLabels []string           `json:"answer_point_labels"`
	FollowUps         []noteFollowUpWire `json:"follow_ups"`
}
type noteFollowUpWire struct {
	Condition         domain.NoteFollowUpCondition `json:"condition"`
	Prompt            string                       `json:"prompt"`
	AnswerPointLabels []string                     `json:"answer_point_labels"`
}
type notePointInput struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}
type noteItemInput struct {
	Label        string                             `json:"label"`
	Kind         organizingdomain.SynthesisItemKind `json:"kind"`
	Context      string                             `json:"context"`
	AnswerPoints []notePointInput                   `json:"answer_points"`
}

// EncodeNotePlanInput contains text and request-local labels only. Immutable
// owner IDs, source tuples and hashes stay outside the Provider boundary.
func EncodeNotePlanInput(preparation NotePreparation) ([]byte, error) {
	if err := preparation.Validate(); err != nil {
		return nil, err
	}
	items := make([]noteItemInput, 0, len(preparation.Snapshot.Items))
	for index, item := range preparation.Snapshot.Items {
		points, context := noteItemPoints(item)
		labelled := make([]notePointInput, len(points))
		for pointIndex, text := range points {
			labelled[pointIndex] = notePointInput{Label: fmt.Sprintf("P%03d", pointIndex+1), Text: text}
		}
		items = append(items, noteItemInput{Label: fmt.Sprintf("I%03d", index+1), Kind: item.Kind, Context: context, AnswerPoints: labelled})
	}
	encoded, err := json.Marshal(struct {
		Title   string               `json:"title"`
		Options NoteInterviewOptions `json:"options"`
		Items   []noteItemInput      `json:"items"`
	}{preparation.Snapshot.Title, preparation.Options, items})
	if err != nil || len(encoded) > MaxNotePlanBytes {
		return nil, domain.InvalidError(ErrorCodeNotePlanInvalid, "note interview input exceeded its bounded contract")
	}
	return encoded, nil
}

func noteItemPoints(item organizingdomain.SynthesisItem) ([]string, string) {
	statement := func(value organizingdomain.SynthesisStatement) []string {
		points := []string{value.Text}
		if value.Applicability != "" && value.Applicability != value.Text {
			points = append(points, value.Applicability)
		}
		return points
	}
	switch item.Kind {
	case organizingdomain.SynthesisFactItem:
		return statement(*item.Fact), ""
	case organizingdomain.SynthesisConflictItem:
		points := make([]string, 0, len(item.Conflict.Alternatives))
		for _, alternative := range item.Conflict.Alternatives {
			for _, point := range statement(alternative) {
				if !slices.Contains(points, point) {
					points = append(points, point)
				}
			}
		}
		return points, item.Conflict.Subject
	case organizingdomain.SynthesisGapItem:
		if item.Gap.Resolution == nil {
			return []string{UnresolvedNoteGapPoint}, item.Gap.Question + " " + item.Gap.Context
		}
		return statement(*item.Gap.Resolution), item.Gap.Question + " " + item.Gap.Context
	default:
		return nil, ""
	}
}

func ValidateNotePlanOutput(raw []byte) (json.RawMessage, error) {
	if _, err := decodeNotePlanWire(raw); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), raw...), nil
}

func decodeNotePlanWire(raw []byte) (notePlanWire, error) {
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = MaxNotePlanBytes
	limits.MaxStringBytes = 8192
	limits.MaxArrayItems = 64
	limits.MaxObjectFields = 4
	return agentdomain.DecodeStrict(raw, limits, func(value notePlanWire) error {
		if len(value.Questions) < 1 || len(value.Questions) > 20 {
			return invalidNotePlan()
		}
		for _, question := range value.Questions {
			if !noteLabel(question.ItemLabel, 'I') || !notePlanText(question.Prompt) || !notePointLabels(question.AnswerPointLabels) || question.FollowUps == nil || len(question.FollowUps) > domain.MaxNoteFollowUps {
				return invalidNotePlan()
			}
			for _, followUp := range question.FollowUps {
				if !notePlanText(followUp.Prompt) || !notePointLabels(followUp.AnswerPointLabels) ||
					(followUp.Condition != domain.NoteFollowUpLowCoverage && followUp.Condition != domain.NoteFollowUpLowCorrectness && followUp.Condition != domain.NoteFollowUpLowBoundaries) {
					return invalidNotePlan()
				}
			}
		}
		return nil
	})
}

// DecodeNotePlan resolves all model labels against this exact frozen note.
// Every conflicting alternative remains in the expected answer; GAP never
// gains an unsupported affirmative conclusion through a model answer point.
func DecodeNotePlan(raw []byte, preparation NotePreparation) ([]NotePlannedQuestion, error) {
	wire, err := decodeNotePlanWire(raw)
	if err != nil {
		return nil, err
	}
	if preparation.Validate() != nil || len(wire.Questions) != preparation.Options.QuestionCount {
		return nil, invalidNotePlan()
	}
	items := make(map[string]organizingdomain.SynthesisItem, len(preparation.Snapshot.Items))
	for index, item := range preparation.Snapshot.Items {
		items[fmt.Sprintf("I%03d", index+1)] = item
	}
	plan := make([]NotePlannedQuestion, 0, len(wire.Questions))
	seenPrompts := make(map[string]bool)
	kinds := make(map[organizingdomain.SynthesisItemKind]bool)
	for _, question := range wire.Questions {
		item, found := items[question.ItemLabel]
		if !found || seenPrompts[question.Prompt] {
			return nil, invalidNotePlan()
		}
		seenPrompts[question.Prompt] = true
		points, _ := noteItemPoints(item)
		if !allNotePointLabels(question.AnswerPointLabels, len(points)) || preparation.Options.MaxFollowUps == 0 && len(question.FollowUps) != 0 || preparation.Options.MaxFollowUps > 0 && len(question.FollowUps) == 0 {
			return nil, invalidNotePlan()
		}
		planned := NotePlannedQuestion{Prompt: question.Prompt, AnswerPoints: points,
			Source: domain.NoteQuestionSource{Revision: preparation.NoteRevision, ItemID: item.ID, ItemKind: item.Kind, Sources: append([]organizingdomain.SynthesisSourceRef{}, item.SourceReferences()...)}, FollowUps: []domain.NoteFollowUp{}}
		for _, followUp := range question.FollowUps {
			if !allNotePointLabels(followUp.AnswerPointLabels, len(points)) || seenPrompts[followUp.Prompt] {
				return nil, invalidNotePlan()
			}
			seenPrompts[followUp.Prompt] = true
			planned.FollowUps = append(planned.FollowUps, domain.NoteFollowUp{Condition: followUp.Condition, Prompt: followUp.Prompt, AnswerPoints: append([]string(nil), points...)})
		}
		plan = append(plan, planned)
		kinds[item.Kind] = true
	}
	for _, item := range preparation.Snapshot.Items {
		if !kinds[item.Kind] {
			return nil, invalidNotePlan()
		}
	}
	return plan, nil
}

func noteLabel(label string, prefix byte) bool {
	if len(label) != 4 || label[0] != prefix || label[1:] == "000" {
		return false
	}
	for _, character := range label[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
func notePointLabels(labels []string) bool {
	if len(labels) == 0 || len(labels) > 64 {
		return false
	}
	previous := ""
	for _, label := range labels {
		if !noteLabel(label, 'P') || label <= previous {
			return false
		}
		previous = label
	}
	return true
}
func allNotePointLabels(labels []string, count int) bool {
	if len(labels) != count {
		return false
	}
	for index, label := range labels {
		if label != fmt.Sprintf("P%03d", index+1) {
			return false
		}
	}
	return true
}
func notePlanText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 8192 && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
func invalidNotePlan() error {
	return domain.InvalidError(ErrorCodeNotePlanInvalid, "note interview model output is invalid")
}

// NotePlanJSONSchema describes only Provider fields. Trusted identities and
// answer text never arrive from the model as authoritative data.
func NotePlanJSONSchema() ([]byte, error) {
	object := func(fields map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": fields, "required": required}
	}
	text := map[string]any{"type": "string", "minLength": 1, "maxLength": 8192}
	points := map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": map[string]any{"type": "string", "pattern": "^P[0-9]{3}$"}}
	followUp := object(map[string]any{"condition": map[string]any{"type": "string", "enum": []string{"LOW_COVERAGE", "LOW_CORRECTNESS", "LOW_BOUNDARIES"}}, "prompt": text, "answer_point_labels": points}, []string{"condition", "prompt", "answer_point_labels"})
	question := object(map[string]any{"item_label": map[string]any{"type": "string", "pattern": "^I[0-9]{3}$"}, "prompt": text, "answer_point_labels": points,
		"follow_ups": map[string]any{"type": "array", "minItems": 0, "maxItems": domain.MaxNoteFollowUps, "items": followUp}}, []string{"item_label", "prompt", "answer_point_labels", "follow_ups"})
	return json.Marshal(object(map[string]any{"questions": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": question}}, []string{"questions"}))
}
