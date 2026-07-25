package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRedactJSONObjectRecursivelyMasksSecretsJWTAndAbsolutePaths(t *testing.T) {
	raw := json.RawMessage(`{
		"token": "top-secret",
		"cookie": "session=plain-cookie",
		"nested": {"authorization": "Bearer nested-secret", "path": "/Users/private/source.md"},
		"items": ["safe", "csrf=csrf-secret", "file:///etc/passwd"],
		"jwt": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.signature123",
		"content_hash": "sha256:stable-summary"
	}`)

	redacted, err := RedactJSONObject(raw)
	if err != nil {
		t.Fatalf("RedactJSONObject: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(redacted, &got); err != nil {
		t.Fatalf("decode redacted object: %v", err)
	}
	if got["token"] != RedactedValue || got["cookie"] != RedactedValue || got["jwt"] != RedactedValue {
		t.Fatalf("top-level secrets were not redacted: %#v", got)
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok || nested["authorization"] != RedactedValue || nested["path"] != RedactedValue {
		t.Fatalf("nested secrets were not redacted: %#v", got["nested"])
	}
	items, ok := got["items"].([]any)
	if !ok || items[0] != "safe" || items[1] != RedactedValue || items[2] != RedactedValue {
		t.Fatalf("array secrets were not redacted: %#v", got["items"])
	}
	if got["content_hash"] != "sha256:stable-summary" {
		t.Fatalf("safe summary was unexpectedly redacted: %#v", got["content_hash"])
	}
	if strings.Contains(string(redacted), "top-secret") || strings.Contains(string(redacted), "nested-secret") {
		t.Fatalf("redacted JSON still contains a secret: %s", redacted)
	}
}

func TestJSONObjectsRejectDuplicateKeysAndCanonicalize(t *testing.T) {
	if _, err := RedactJSONObject(json.RawMessage(`{"key":1,"key":2}`)); err == nil {
		t.Fatal("duplicate JSON keys must be rejected")
	}
	canonical, err := CanonicalJSONObject(json.RawMessage(" { \"z\": 1, \"a\": {\"b\":2} } "))
	if err != nil {
		t.Fatalf("CanonicalJSONObject: %v", err)
	}
	if got, want := string(canonical), `{"a":{"b":2},"z":1}`; got != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
}

func TestEventFingerprintChangesForDifferentBinding(t *testing.T) {
	first := validEvent()
	second := first
	second.Action = "security.block"
	if EqualBinding(first, second) {
		t.Fatal("different actions must not share an audit binding")
	}
	firstHash, err := first.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	secondHash, err := second.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(second): %v", err)
	}
	if firstHash == secondHash {
		t.Fatal("different bindings produced the same fingerprint")
	}
}

func TestEventStringNeverIncludesSensitiveFields(t *testing.T) {
	event := validEvent()
	event.ActorRef = "Bearer actor-secret"
	event.ResourceRef = "/Users/private/document.md"
	event.IdempotencyKey = "token=secret-key"
	got := event.String()
	for _, secret := range []string{"actor-secret", "document.md", "secret-key", "Bearer"} {
		if strings.Contains(got, secret) {
			t.Fatalf("String leaked %q: %s", secret, got)
		}
	}
}

func TestEventRejectsSensitiveStableFields(t *testing.T) {
	event := validEvent()
	event.Action = "token=secret"
	if err := event.Validate(); err == nil {
		t.Fatal("sensitive action must be rejected")
	}
}

func TestEventAcceptsAPITokenActor(t *testing.T) {
	event := validEvent()
	event.ActorType = ActorAPIToken
	if err := event.Validate(); err != nil {
		t.Fatalf("API Token actor must be valid: %v", err)
	}
}

func validEvent() Event {
	return Event{
		ID:             foundation.ID("10000000-0000-4000-8000-000000000001"),
		ActorType:      ActorUser,
		Action:         "settings.update",
		ResourceType:   "settings",
		ResourceRef:    "settings-1",
		Outcome:        OutcomeSucceeded,
		IdempotencyKey: "audit-key-1",
		Correlation:    json.RawMessage(`{"request_id":"request-1"}`),
		Metadata:       json.RawMessage(`{"content_hash":"sha256:stable-summary"}`),
		SchemaVersion:  SchemaVersion,
		OccurredAt:     time.Date(2026, 7, 23, 8, 9, 10, 123456000, time.UTC),
	}
}
