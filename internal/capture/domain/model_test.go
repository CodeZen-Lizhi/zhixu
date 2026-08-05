package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCaptureValidateKeepsURLWithoutSourceVersion(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	capture := Capture{
		ID: "10000000-0000-4000-8000-000000000001", WorkspaceID: "10000000-0000-4000-8000-000000000002",
		Kind: KindURL, DisplayName: "Java AI", OriginalLocation: "capture://10000000-0000-4000-8000-000000000001/input",
		OriginalURL: "https://example.com/java-ai", SourceID: "10000000-0000-4000-8000-000000000003",
		Status: StatusReceived, FetchStatus: StagePending, IngestionStatus: StagePending,
		IndexStatus: StagePending, ProfileStatus: StagePending, Version: 1, CapturedAt: now, UpdatedAt: now,
	}
	if err := capture.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	capture.Status = StatusReady
	if err := capture.Validate(); captureErrorCode(err) != "CAPTURE_INVALID" {
		t.Fatalf("Validate() error = %#v", err)
	}
}

func TestCaptureValidateRequiresMaterializedInputHash(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	capture := Capture{
		ID: "20000000-0000-4000-8000-000000000001", WorkspaceID: "20000000-0000-4000-8000-000000000002",
		Kind: KindText, DisplayName: "Quick note", OriginalLocation: "capture://20000000-0000-4000-8000-000000000001/input",
		SourceID: "20000000-0000-4000-8000-000000000003", LatestSourceVersionID: "20000000-0000-4000-8000-000000000004",
		Status: StatusSourceSaved, FetchStatus: StageNotApplicable, IngestionStatus: StagePending,
		IndexStatus: StagePending, ProfileStatus: StagePending, Version: 1, CapturedAt: now, UpdatedAt: now,
	}
	if err := capture.Validate(); captureErrorCode(err) != "CAPTURE_INVALID" {
		t.Fatalf("Validate() error = %#v", err)
	}
	capture.OriginalInputHash = strings.Repeat("a", 64)
	if err := capture.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestStageStatusSupportsStaleProfileProjection(t *testing.T) {
	if !StageStale.Valid() {
		t.Fatal("StageStale.Valid() = false")
	}
	if StageStatus("UNKNOWN").Valid() {
		t.Fatal("unknown StageStatus.Valid() = true")
	}
}

func TestProfileRevisionDigestRequiresEvidence(t *testing.T) {
	content := ProfileContent{
		Summary: "Java AI summary", Topics: []ProfileCandidate{{Label: "Spring AI"}},
		KnowledgePoints: []ProfilePoint{{Text: "Spring AI integrates model providers."}},
	}
	if _, err := ComputeProfileDigest(content); captureErrorCode(err) != "PROFILE_INVALID" {
		t.Fatalf("ComputeProfileDigest() error = %#v", err)
	}
	spanIDs := []foundation.ID{"30000000-0000-4000-8000-000000000001"}
	content.Topics[0].SourceSpanIDs = spanIDs
	content.KnowledgePoints[0].SourceSpanIDs = spanIDs
	digest, err := ComputeProfileDigest(content)
	if err != nil || len(digest) != 64 {
		t.Fatalf("ComputeProfileDigest() = %q, %v", digest, err)
	}
}

func TestNormalizeProfileContentRequiresTopicAndKnowledgePoint(t *testing.T) {
	spanID := foundation.ID("30000000-0000-4000-8000-000000000001")
	validTopic := []ProfileCandidate{{Label: "Java AI", SourceSpanIDs: []foundation.ID{spanID}}}
	validPoint := []ProfilePoint{{Text: "Spring AI integrates model providers.", SourceSpanIDs: []foundation.ID{spanID}}}
	for name, content := range map[string]ProfileContent{
		"topic": {Summary: "Java AI summary", KnowledgePoints: validPoint},
		"point": {Summary: "Java AI summary", Topics: validTopic},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeProfileContent(content); captureErrorCode(err) != "PROFILE_INVALID" {
				t.Fatalf("NormalizeProfileContent() error = %#v", err)
			}
		})
	}
}

func TestNormalizeProfileContentRejectsInvalidOrDuplicateAliases(t *testing.T) {
	spanID := foundation.ID("30000000-0000-4000-8000-000000000001")
	for _, aliases := range [][]string{{" Spring AI"}, {"Spring AI", "Spring AI"}} {
		content := ProfileContent{
			Summary: "Java AI summary",
			Topics:  []ProfileCandidate{{Label: "Java AI", Aliases: aliases, SourceSpanIDs: []foundation.ID{spanID}}},
			KnowledgePoints: []ProfilePoint{{
				Text: "Spring AI integrates model providers.", SourceSpanIDs: []foundation.ID{spanID},
			}},
		}
		if _, err := NormalizeProfileContent(content); captureErrorCode(err) != "PROFILE_INVALID" {
			t.Fatalf("NormalizeProfileContent(%q) error = %#v", aliases, err)
		}
	}
}

func captureErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
