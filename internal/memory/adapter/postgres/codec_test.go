package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

func TestMemoryReceiptSnapshotRoundTripsCanonicalContent(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	memory, err := domain.NewCandidate(domain.CandidateInput{
		ID: codecID(1), WorkspaceID: codecID(2), Owner: domain.Principal{Kind: domain.PrincipalUser, ID: codecID(3)},
		Type: domain.TypePreference, Content: json.RawMessage(`{"z":1,"a":"value"}`),
		Source: domain.Source{Type: domain.SourceAgent, Ref: "agent:candidate-1"}, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeMemorySnapshot(memory)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMemorySnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !sameMemory(decoded, memory) {
		t.Fatalf("snapshot round trip = %#v, want %#v", decoded, memory)
	}
}

func TestMemoryReceiptSnapshotFailsClosedForUnknownOrInvalidContent(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"unknown":true}`),
		[]byte(`{"id":"10000000-0000-4000-8000-000000000001","content":{}}`),
	} {
		if _, err := decodeMemorySnapshot(raw); err == nil {
			t.Fatalf("decodeMemorySnapshot(%s) unexpectedly succeeded", raw)
		}
	}
}

func TestMemoryReceiptSnapshotRejectsDuplicateFieldsAndReceiptVersionDrift(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	memory, err := domain.NewCandidate(domain.CandidateInput{
		ID: codecID(1), WorkspaceID: codecID(2), Owner: domain.Principal{Kind: domain.PrincipalUser, ID: codecID(3)},
		Type: domain.TypePreference, Content: json.RawMessage(`{"mode":"concise"}`),
		Source: domain.Source{Type: domain.SourceUser, Ref: "user:settings"}, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeMemorySnapshot(memory)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := bytes.Replace(raw, []byte(`"id":`), []byte(`"id":"30000000-0000-4000-8000-000000000009","id":`), 1)
	if _, err := decodeMemorySnapshot(duplicate); err == nil {
		t.Fatal("decodeMemorySnapshot accepted duplicate fields")
	}
	if _, err := commandResult(persistedCommand{
		workspaceID: memory.WorkspaceID, owner: memory.Owner, memoryID: memory.ID, version: memory.Version + 1, response: raw,
	}); err == nil {
		t.Fatal("commandResult accepted a receipt version that differs from its snapshot")
	}
}

func codecID(value int) foundation.ID {
	return foundation.ID("30000000-0000-4000-8000-00000000000" + string(rune('0'+value)))
}
