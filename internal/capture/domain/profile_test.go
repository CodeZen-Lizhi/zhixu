package domain

import (
	"testing"
	"time"
)

func TestStaleProfileRequiresReadableRevision(t *testing.T) {
	now := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)
	profile := Profile{
		ID: "95000000-0000-4000-8000-000000000001", WorkspaceID: "95000000-0000-4000-8000-000000000002",
		CaptureID: "95000000-0000-4000-8000-000000000003", SourceVersionID: "95000000-0000-4000-8000-000000000004",
		Status: ProfileStatusStale, Version: 2, CreatedAt: now, UpdatedAt: now,
	}
	if err := profile.Validate(); err == nil {
		t.Fatal("stale profile without a readable revision passed validation")
	}
	profile.CurrentRevisionID = "95000000-0000-4000-8000-000000000005"
	if err := profile.Validate(); err != nil {
		t.Fatalf("stale profile with a readable revision failed validation: %v", err)
	}
}
