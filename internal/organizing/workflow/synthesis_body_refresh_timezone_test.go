package workflow

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func TestBodyRefreshFrozenHashTimezoneIndependent(t *testing.T) {
	instant := time.Date(2026, 9, 15, 1, 2, 3, 456789000, time.UTC)
	var expectedJSON []byte
	var expectedHash string
	for _, zone := range []*time.Location{time.UTC, time.FixedZone("UTC+8", 8*60*60), time.FixedZone("UTC-7", -7*60*60)} {
		input := SynthesisFrozenInput{BodyRefresh: &app.SynthesisBodyRefreshBinding{Request: app.SynthesisBodyRefreshRequest{CreatedAt: instant.In(zone)}}}
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := input.ComputeHash()
		if err != nil {
			t.Fatal(err)
		}
		if expectedJSON == nil {
			expectedJSON, expectedHash = encoded, hash
		}
		if !bytes.Equal(encoded, expectedJSON) || hash != expectedHash {
			t.Fatalf("timezone %s changed frozen JSON/hash", zone)
		}
		var replay SynthesisFrozenInput
		if err := json.Unmarshal(encoded, &replay); err != nil {
			t.Fatal(err)
		}
		if replay.BodyRefresh.Request.CreatedAt.Location() != time.UTC {
			t.Fatal("replayed timestamp is not UTC")
		}
		replayHash, err := replay.ComputeHash()
		if err != nil || replayHash != hash {
			t.Fatalf("replay hash changed: %v", err)
		}
	}
}
