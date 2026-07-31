package observability

import (
	"errors"
	"testing"
)

func TestSensitiveCredentialKeysOverrideSummarySuffixes(t *testing.T) {
	for _, key := range []string{"token_hash", "credential_id", "session_status", "csrf_configured", "database_url_hash", "email", "phone", "mobile", "telephone", "address", "postalcode", "zipcode"} {
		if !IsSensitiveKey(key) {
			t.Fatalf("credential key %q must remain sensitive despite its suffix", key)
		}
		if got := RedactString(key, ""); got != RedactedValue {
			t.Fatalf("RedactString(%q, empty) = %q, want redacted", key, got)
		}
	}
	for _, key := range []string{"content_hash", "prompt_version", "source_count", "response_body_size"} {
		if IsSensitiveKey(key) {
			t.Fatalf("bounded summary key %q was classified as sensitive", key)
		}
	}
}

func TestRedactStringCoversSessionCSRFCookieJWTAndPaths(t *testing.T) {
	for _, value := range []string{
		"session=session-secret",
		"csrf: csrf-secret",
		"Set-Cookie: zhixu_session=cookie-secret; HttpOnly",
		"access_token=access-secret",
		"refresh-token: refresh-secret",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.signature123",
		"stack at /Users/private/app.go:42",
	} {
		if got := RedactString("dependency_error", value); got != RedactedValue {
			t.Fatalf("RedactString(%q) = %q, want redacted", value, got)
		}
	}
	if got := RedactString("documentation_url", "https://example.test/usr/local/help"); got != "https://example.test/usr/local/help" {
		t.Fatalf("safe HTTP URL was redacted: %q", got)
	}
	if got := RedactString("summary", "contact person@example.test for support"); got != RedactedValue {
		t.Fatalf("email text was not redacted: %q", got)
	}
	if got := RedactString("summary", "normal user-facing status"); got != "normal user-facing status" {
		t.Fatalf("normal text was redacted: %q", got)
	}
	if got := RedactString("http_route", "/api/v1/workspaces/{workspace_id}"); got != "/api/v1/workspaces/{workspace_id}" {
		t.Fatalf("route template was redacted: %q", got)
	}
	if got := RedactString("path", "/api/v1/workspaces/{workspace_id}"); got != RedactedValue {
		t.Fatalf("ordinary path was not redacted: %q", got)
	}
}

func TestRedactValueTreatsErrorAndStringerAsUntrusted(t *testing.T) {
	if got := RedactValue("error", errors.New("safe-looking but arbitrary detail")); got != RedactedValue {
		t.Fatalf("error = %#v, want redacted", got)
	}
	if got := RedactValue("object", safeLookingStringer{}); got != RedactedValue {
		t.Fatalf("Stringer = %#v, want redacted", got)
	}
	got, ok := RedactValue("details", map[string]any{
		"token": "nested-secret",
		"count": 2,
	}).(map[string]any)
	if !ok || got["token"] != RedactedValue || got["count"] != 2 {
		t.Fatalf("nested redaction = %#v", got)
	}
}

type safeLookingStringer struct{}

func (safeLookingStringer) String() string { return "apparently-safe-domain-value" }
