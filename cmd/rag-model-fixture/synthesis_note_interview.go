package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
)

const synthesisFixtureInterviewStage = "synthesis_note_interview"

func synthesisNoteInterviewFixtureStage(raw json.RawMessage) string {
	schema, err := interviewapplication.NotePlanJSONSchema()
	if err != nil {
		return ""
	}
	var actual, expected any
	if json.Unmarshal(raw, &actual) != nil || json.Unmarshal(schema, &expected) != nil || !reflect.DeepEqual(actual, expected) {
		return ""
	}
	return synthesisFixtureInterviewStage
}

// Select actual frozen item/point labels and cover every available kind. The
// first question is a sourced FACT whenever one exists, even if it is not I001.
func synthesisNoteInterviewFixtureResponse(input map[string]any) (string, error) {
	type itemInput struct {
		Label        string `json:"label"`
		Kind         string `json:"kind"`
		Context      string `json:"context"`
		AnswerPoints []struct {
			Label string `json:"label"`
			Text  string `json:"text"`
		} `json:"answer_points"`
	}
	var value struct {
		Title   string                                    `json:"title"`
		Options interviewapplication.NoteInterviewOptions `json:"options"`
		Items   []itemInput                               `json:"items"`
	}
	if !hasExactAnyKeys(input, "title", "options", "items") || decodeSynthesisFixtureInput(input, &value) != nil ||
		strings.TrimSpace(value.Title) == "" || value.Options.Validate() != nil || len(value.Items) == 0 || len(value.Items) > 128 {
		return "", fixtureError("note_interview_input_invalid", "note interview fixture input is invalid")
	}
	byKind := make(map[string]itemInput)
	for index, item := range value.Items {
		if item.Label != fmt.Sprintf("I%03d", index+1) || len(item.AnswerPoints) == 0 || len(item.AnswerPoints) > 64 ||
			item.Kind != "FACT" && item.Kind != "CONFLICT" && item.Kind != "GAP" {
			return "", fixtureError("note_interview_item_invalid", "note interview fixture item binding is invalid")
		}
		for pointIndex, point := range item.AnswerPoints {
			if point.Label != fmt.Sprintf("P%03d", pointIndex+1) || strings.TrimSpace(point.Text) == "" {
				return "", fixtureError("note_interview_points_invalid", "note interview fixture answer point binding is invalid")
			}
		}
		if _, exists := byKind[item.Kind]; !exists {
			byKind[item.Kind] = item
		}
	}
	ordered := make([]itemInput, 0, len(byKind))
	for _, kind := range []string{"FACT", "CONFLICT", "GAP"} {
		if item, found := byKind[kind]; found {
			ordered = append(ordered, item)
		}
	}
	if value.Options.QuestionCount < len(ordered) {
		return "", fixtureError("note_interview_coverage_invalid", "question count cannot cover the frozen note kinds")
	}
	questions := make([]any, 0, value.Options.QuestionCount)
	for index := 0; index < value.Options.QuestionCount; index++ {
		item := ordered[index%len(ordered)]
		labels := make([]string, len(item.AnswerPoints))
		for pointIndex, point := range item.AnswerPoints {
			labels[pointIndex] = point.Label
		}
		prompt, followUp, condition := "Explain the supported fact and its applicability", "Clarify the missing fact or applicability", "LOW_COVERAGE"
		switch item.Kind {
		case "CONFLICT":
			prompt, followUp, condition = "Explain both supported alternatives and their conditions", "Clarify the conditions for each alternative", "LOW_BOUNDARIES"
		case "GAP":
			prompt, followUp, condition = "Explain what the evidence cannot determine and what is missing", "Clarify which additional evidence is needed", "LOW_CORRECTNESS"
		}
		followUps := []any{}
		if value.Options.MaxFollowUps > 0 {
			followUps = append(followUps, map[string]any{
				"condition": condition, "prompt": fmt.Sprintf("%s for %s (follow-up %d).", followUp, value.Title, index+1), "answer_point_labels": labels,
			})
		}
		questions = append(questions, map[string]any{
			"item_label": item.Label, "prompt": fmt.Sprintf("%s for %s (question %d).", prompt, value.Title, index+1),
			"answer_point_labels": labels, "follow_ups": followUps,
		})
	}
	raw, err := json.Marshal(map[string]any{"questions": questions})
	if err != nil {
		return "", fixtureError("note_interview_output_invalid", "note interview fixture output could not be encoded")
	}
	if _, err := interviewapplication.ValidateNotePlanOutput(raw); err != nil {
		return "", fixtureError("note_interview_output_invalid", "note interview fixture output violates its production schema")
	}
	return string(raw), nil
}
