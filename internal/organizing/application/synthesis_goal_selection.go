package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const MaxGoalSelectionPoints = 32

// 一个模型切片只包含派生的简介和知识点文本。规范身份保留在服务端绑定中，
// 模型只看到局部的 P001..P032 标签。
type SynthesisGoalSelectionInput struct {
	RequestID         foundation.ID
	CatalogBatchID    foundation.ID
	SourceOrdinal     int
	PointOffset       int
	Goal              string
	Source            domain.SynthesisSourceVersion
	ProfileRevisionID foundation.ID
	Title             string
	Summary           string
	Points            []KnowledgeDirectoryPoint
}

type SynthesisGoalSelectionOutput struct {
	Selections  []SynthesisGoalSelectedLabel `json:"selections"`
	Explanation string                       `json:"explanation"`
}

type SynthesisGoalSelectedLabel struct {
	Point  string `json:"point"`
	Reason string `json:"reason"`
}

type SynthesisGoalSelectedPoint struct {
	Source        domain.SynthesisSourceVersion
	Locator       KnowledgePointLocator
	SourceSpanIDs []foundation.ID
	Reason        string
}

// BuildGoalSelectionInputs 对全部目录知识点分片。
// 画像或页面超过单次模型请求预算时，不能截断后续知识点。
func BuildGoalSelectionInputs(request SynthesisGoalRequest, batch SynthesisGoalCatalogBatch, items []SynthesisGoalCatalogItem) ([]SynthesisGoalSelectionInput, error) {
	if !validID(request.ID) || (CreateSynthesisGoalCommand{WorkspaceID: request.WorkspaceID, Goal: request.Goal, IdempotencyKey: "selection"}).Validate() != nil || batch.RequestID != request.ID || batch.WorkspaceID != request.WorkspaceID || !validID(batch.ID) || batch.BatchNo < 1 || batch.Items == nil || len(batch.Items) > 32 || len(batch.Items) != len(items) {
		return nil, goalSelectionInvalid()
	}
	result := []SynthesisGoalSelectionInput{}
	for ordinal, item := range items {
		binding := batch.Items[ordinal]
		directory := item.Directory
		if item.Source != binding.Source || item.Title != binding.Title || directory.ProfileRevisionID != binding.ProfileRevisionID || directory.WorkspaceID != request.WorkspaceID || directory.SourceVersionID != item.Source.SourceVersionID || directory.ParseProjectionID != item.Source.ParseProjectionID || directory.Status != KnowledgeDirectoryAnalyzed || len(directory.Points) < 1 || len(directory.Points) > 512 {
			return nil, goalSelectionInvalid()
		}
		seen := map[KnowledgePointLocator]bool{}
		for _, point := range directory.Points {
			if point.Locator.ProfileRevisionID != binding.ProfileRevisionID || seen[point.Locator] {
				return nil, goalSelectionInvalid()
			}
			seen[point.Locator] = true
		}
		for offset := 0; offset < len(directory.Points); {
			input := SynthesisGoalSelectionInput{RequestID: request.ID, CatalogBatchID: batch.ID, SourceOrdinal: ordinal, PointOffset: offset, Goal: request.Goal, Source: item.Source, ProfileRevisionID: binding.ProfileRevisionID, Title: item.Title, Summary: directory.Summary, Points: []KnowledgeDirectoryPoint{}}
			for offset+len(input.Points) < len(directory.Points) && len(input.Points) < MaxGoalSelectionPoints {
				point := directory.Points[offset+len(input.Points)]
				next := input
				next.Points = append(append([]KnowledgeDirectoryPoint(nil), input.Points...), point)
				payload, err := goalSelectionPayload(next)
				if err != nil {
					return nil, err
				}
				if len(payload) > MaxSynthesisSourceInputBytes {
					break
				}
				input = next
			}
			if len(input.Points) == 0 {
				return nil, goalSelectionInvalid()
			}
			// 复制片段切片，避免调用方后续修改影响已保存的批次。
			for i := range input.Points {
				input.Points[i].SourceSpanIDs = append([]foundation.ID(nil), input.Points[i].SourceSpanIDs...)
			}
			result = append(result, input)
			offset += len(input.Points)
		}
	}
	return result, nil
}

func BuildGoalSelectionPayload(input SynthesisGoalSelectionInput) ([]byte, error) {
	payload, err := goalSelectionPayload(input)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxSynthesisSourceInputBytes {
		return nil, goalSelectionInvalid()
	}
	return payload, nil
}

func goalSelectionPayload(input SynthesisGoalSelectionInput) ([]byte, error) {
	if !validID(input.RequestID) || !validID(input.CatalogBatchID) || input.Source.Validate() != nil || !validID(input.ProfileRevisionID) || input.SourceOrdinal < 0 || input.SourceOrdinal >= 32 || input.PointOffset < 0 || input.PointOffset >= 512 || (CreateSynthesisGoalCommand{WorkspaceID: input.Source.WorkspaceID, Goal: input.Goal, IdempotencyKey: "selection"}).Validate() != nil || !anchorModelText(input.Title, 512) || input.Summary == "" || !utf8.ValidString(input.Summary) || len(input.Summary) > 16*1024 || len(input.Points) < 1 || len(input.Points) > MaxGoalSelectionPoints || input.PointOffset+len(input.Points) > 512 {
		return nil, goalSelectionInvalid()
	}
	type point struct {
		Label string             `json:"label"`
		Kind  KnowledgePointKind `json:"kind"`
		Text  string             `json:"text"`
	}
	points := make([]point, len(input.Points))
	seen := map[KnowledgePointLocator]bool{}
	for i, p := range input.Points {
		if p.Locator.ProfileRevisionID != input.ProfileRevisionID || p.Locator.Index < 0 || p.Locator.Index >= 256 || p.Locator.Kind != KnowledgePointKindKnowledgePoint && p.Locator.Kind != KnowledgePointKindExample || p.Text == "" || !utf8.ValidString(p.Text) || len(p.Text) > 4096 || len(p.SourceSpanIDs) < 1 || len(p.SourceSpanIDs) > 500 || seen[p.Locator] {
			return nil, goalSelectionInvalid()
		}
		seen[p.Locator] = true
		spans := map[foundation.ID]bool{}
		for _, id := range p.SourceSpanIDs {
			if !validID(id) || spans[id] {
				return nil, goalSelectionInvalid()
			}
			spans[id] = true
		}
		points[i] = point{Label: fmt.Sprintf("P%03d", i+1), Kind: p.Locator.Kind, Text: p.Text}
	}
	value := struct {
		Goal    string  `json:"user_goal"`
		Title   string  `json:"source_title"`
		Summary string  `json:"source_summary"`
		Points  []point `json:"points"`
	}{input.Goal, input.Title, input.Summary, points}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func DecodeGoalSelectionOutput(raw []byte) (SynthesisGoalSelectionOutput, error) {
	var result SynthesisGoalSelectionOutput
	limits := agentdomain.DefaultDecodeLimits()
	limits.MaxDocumentBytes = 128 * 1024
	limits.MaxArrayItems = 32
	limits.MaxObjectFields = 2
	fields, err := agentdomain.DecodeStrict[map[string]json.RawMessage](raw, limits, nil)
	if err != nil || len(fields) != 2 || fields["selections"] == nil || fields["explanation"] == nil {
		return result, goalSelectionInvalid()
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Selections == nil || len(result.Selections) > 32 || !anchorModelText(result.Explanation, 2048) {
		return result, goalSelectionInvalid()
	}
	var entries []json.RawMessage
	if json.Unmarshal(fields["selections"], &entries) != nil {
		return result, goalSelectionInvalid()
	}
	seen := map[string]bool{}
	for i, entry := range entries {
		item, err := agentdomain.DecodeStrict[map[string]json.RawMessage](entry, limits, nil)
		selection := result.Selections[i]
		if err != nil || len(item) != 2 || item["point"] == nil || item["reason"] == nil || !anchorModelText(selection.Reason, 2048) || seen[selection.Point] {
			return result, goalSelectionInvalid()
		}
		if _, ok := goalSelectionPointIndex(selection.Point, 32); !ok {
			return result, goalSelectionInvalid()
		}
		seen[selection.Point] = true
	}
	return result, nil
}

func BindGoalSelectionOutput(raw []byte, input SynthesisGoalSelectionInput) ([]SynthesisGoalSelectedPoint, error) {
	if _, err := BuildGoalSelectionPayload(input); err != nil {
		return nil, err
	}
	output, err := DecodeGoalSelectionOutput(raw)
	if err != nil {
		return nil, err
	}
	selected := make([]SynthesisGoalSelectedPoint, 0, len(output.Selections))
	for _, selection := range output.Selections {
		index, ok := goalSelectionPointIndex(selection.Point, len(input.Points))
		if !ok {
			return nil, goalSelectionInvalid()
		}
		point := input.Points[index]
		selected = append(selected, SynthesisGoalSelectedPoint{Source: input.Source, Locator: point.Locator, SourceSpanIDs: append([]foundation.ID(nil), point.SourceSpanIDs...), Reason: selection.Reason})
	}
	return selected, nil
}

func goalSelectionPointIndex(label string, count int) (int, bool) {
	if len(label) != 4 || label[0] != 'P' {
		return 0, false
	}
	index, err := strconv.Atoi(label[1:])
	return index - 1, err == nil && index >= 1 && index <= count && fmt.Sprintf("P%03d", index) == label
}
func goalSelectionInvalid() error {
	return invalid("SYNTHESIS_GOAL_SELECTION_INVALID", "goal knowledge-point selection is invalid")
}
