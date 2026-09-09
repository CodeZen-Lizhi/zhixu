package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestSourceReadyRequiresCompleteImmutableTuple(t *testing.T) {
	valid := SourceReady{
		WorkspaceID: "11000000-0000-4000-8000-000000000001", SourceID: "11000000-0000-4000-8000-000000000002",
		SourceVersionID: "11000000-0000-4000-8000-000000000003", ContentArtifactID: "11000000-0000-4000-8000-000000000004",
		ParseProjectionID: "11000000-0000-4000-8000-000000000005", IngestionAttemptID: "11000000-0000-4000-8000-000000000006",
		ContentHash: strings.Repeat("a", 64), OccurredAt: time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*SourceReady){
		"workspace":   func(ready *SourceReady) { ready.WorkspaceID = "" },
		"source":      func(ready *SourceReady) { ready.SourceID = "" },
		"version":     func(ready *SourceReady) { ready.SourceVersionID = "" },
		"artifact":    func(ready *SourceReady) { ready.ContentArtifactID = "" },
		"projection":  func(ready *SourceReady) { ready.ParseProjectionID = "" },
		"attempt":     func(ready *SourceReady) { ready.IngestionAttemptID = "" },
		"padded id":   func(ready *SourceReady) { ready.SourceID = foundation.ID(" " + string(ready.SourceID)) },
		"short hash":  func(ready *SourceReady) { ready.ContentHash = "a" },
		"upper hash":  func(ready *SourceReady) { ready.ContentHash = strings.Repeat("A", 64) },
		"broken hash": func(ready *SourceReady) { ready.ContentHash = strings.Repeat("z", 64) },
		"zero time":   func(ready *SourceReady) { ready.OccurredAt = time.Time{} },
		"large year":  func(ready *SourceReady) { ready.OccurredAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			ready := valid
			change(&ready)
			if err := ready.Validate(); err == nil {
				t.Fatal("accepted incomplete source-ready tuple")
			}
		})
	}
}
