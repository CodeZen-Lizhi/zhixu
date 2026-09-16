package application

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestGoalSelectionSplitsEveryPointAndPreservesFrozenLocators(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprintf("large=%v", large), func(t *testing.T) {
			request, batch, items := goalSelectionFixture(512)
			if large {
				items[0].Directory.Summary = strings.Repeat("\x01", 16*1024)
				for i := range items[0].Directory.Points {
					items[0].Directory.Points[i].Text = strings.Repeat("\x01", 4096)
				}
			}
			inputs, err := BuildGoalSelectionInputs(request, batch, items)
			if err != nil {
				t.Fatal(err)
			}
			if len(inputs) < 16 || large && len(inputs) <= 16 {
				t.Fatalf("missing budget splitting: %d", len(inputs))
			}
			seen := map[KnowledgePointLocator]bool{}
			offset := 0
			for _, input := range inputs {
				if input.PointOffset != offset || input.CatalogBatchID != batch.ID || input.RequestID != request.ID {
					t.Fatalf("slice binding %+v", input)
				}
				payload, err := BuildGoalSelectionPayload(input)
				if err != nil || len(payload) > MaxSynthesisSourceInputBytes {
					t.Fatalf("input budget %d %v", len(payload), err)
				}
				for _, private := range []string{string(batch.ID), string(request.ID), string(input.Source.SourceVersionID), string(input.ProfileRevisionID), `"source_span_id"`, `"content_hash"`} {
					if bytes.Contains(payload, []byte(private)) {
						t.Fatalf("private binding leaked %s", private)
					}
				}
				for _, point := range input.Points {
					if seen[point.Locator] {
						t.Fatal("point repeated across slices")
					}
					seen[point.Locator] = true
				}
				offset += len(input.Points)
			}
			if offset != 512 || len(seen) != 512 {
				t.Fatalf("catalog silently truncated: %d %d", offset, len(seen))
			}
			span := inputs[0].Points[0].SourceSpanIDs[0]
			items[0].Directory.Points[0].SourceSpanIDs[0] = synthesisContractID(99)
			if inputs[0].Points[0].SourceSpanIDs[0] != span {
				t.Fatal("caller mutation altered selection input")
			}
		})
	}
}

func TestGoalSelectionRejectsInventedLabelsAndControlFields(t *testing.T) {
	request, batch, items := goalSelectionFixture(2)
	inputs, err := BuildGoalSelectionInputs(request, batch, items)
	if err != nil {
		t.Fatal(err)
	}
	input := inputs[0]
	raw := []byte(`{"selections":[{"point":"P002","reason":"Redis persistence supports the database learning goal."}],"explanation":"Only this point addresses persistence."}`)
	selected, err := BindGoalSelectionOutput(raw, input)
	if err != nil || len(selected) != 1 || selected[0].Locator != items[0].Directory.Points[1].Locator || selected[0].Source != input.Source {
		t.Fatalf("selection %+v %v", selected, err)
	}
	for _, bad := range []string{
		`{"selections":null,"explanation":"none"}`,
		`{"selections":[],"selections":[],"explanation":"none"}`,
		`{"selections":[],"explanation":"none","publish":true}`,
		`{"selections":[{"point":"P003","reason":"invented"}],"explanation":"invalid"}`,
		`{"selections":[{"point":"P001","reason":"yes"},{"point":"P001","reason":"again"}],"explanation":"duplicate"}`,
		`{"selections":[{"point":"P001","reason":"yes","source_id":"invented"}],"explanation":"invalid"}`,
	} {
		if _, err := BindGoalSelectionOutput([]byte(bad), input); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	empty, err := BindGoalSelectionOutput([]byte(`{"selections":[],"explanation":"These points do not help this goal."}`), input)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("explicit no-match %+v %v", empty, err)
	}
	items[0].Directory.Points[1].Locator = items[0].Directory.Points[0].Locator
	if _, err := BuildGoalSelectionInputs(request, batch, items); err == nil {
		t.Fatal("duplicate frozen point accepted")
	}
}

func goalSelectionFixture(count int) (SynthesisGoalRequest, SynthesisGoalCatalogBatch, []SynthesisGoalCatalogItem) {
	original, _ := synthesisContractFixture()
	source := original.SourceEvent.Source
	request := SynthesisGoalRequest{ID: synthesisContractID(70), WorkspaceID: source.WorkspaceID, Goal: "整理数据库知识，用于系统学习"}
	binding := SynthesisGoalCatalogBinding{Source: source, ProfileRevisionID: synthesisContractID(72), Title: "Redis 专项与面试笔记"}
	batch := SynthesisGoalCatalogBatch{ID: synthesisContractID(71), RequestID: request.ID, WorkspaceID: request.WorkspaceID, BatchNo: 1, Items: []SynthesisGoalCatalogBinding{binding}}
	directory := SourceKnowledgeDirectory{WorkspaceID: source.WorkspaceID, SourceVersionID: source.SourceVersionID, Status: KnowledgeDirectoryAnalyzed, ProfileRevisionID: binding.ProfileRevisionID, ParseProjectionID: source.ParseProjectionID, Summary: "Redis persistence and interview modules", Points: []KnowledgeDirectoryPoint{}}
	for i := 0; i < count; i++ {
		kind := KnowledgePointKindKnowledgePoint
		if i >= 256 {
			kind = KnowledgePointKindExample
		}
		directory.Points = append(directory.Points, KnowledgeDirectoryPoint{Locator: KnowledgePointLocator{ProfileRevisionID: binding.ProfileRevisionID, Kind: kind, Index: i % 256}, Text: fmt.Sprintf("Redis persistence point %d", i), SourceSpanIDs: []foundation.ID{original.Sources[0].Reference.SourceSpanID}})
	}
	return request, batch, []SynthesisGoalCatalogItem{{Source: source, Title: binding.Title, Directory: directory}}
}
