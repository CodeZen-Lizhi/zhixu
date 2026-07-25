package redaction

import "testing"

func TestSensitiveTextDetectionCoversCredentialsAndPaths(t *testing.T) {
	for _, value := range []string{
		"Authorization: Bearer secret", "password=hunter2", "passwd: hunter2", "dsn=postgres://user:pass@host/db",
		"database_url=postgres://user:pass@host/db", "postgres://user:pass@host/db", "https://user:pass@example.test/private",
		"session=plain-session", "csrf=plain-csrf", `{"token":"plain-token"}`, `{"nested":{"password":"plain-password"}}`,
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.signature123",
	} {
		if !ContainsSecret(value) {
			t.Fatalf("secret not detected: %q", value)
		}
	}
	for _, value := range []string{
		"/Users/private/source.md", `C:\\Users\\private\\source.md`, "file:///etc/passwd", "prefix /workspace/source.md",
		"stack at /usr/local/lib/app.go", "mounted at /srv/app/data", `file C:\Users\private\source.md`,
	} {
		if !ContainsAbsolutePath(value) {
			t.Fatalf("absolute path not detected: %q", value)
		}
	}
	for _, value := range []string{"docs/source.md", "https://example.test/etc/status", "see https://example.test/usr/local/help"} {
		if ContainsAbsolutePath(value) {
			t.Fatalf("safe text was classified as a path: %q", value)
		}
	}
	if ContainsSecret("safe excerpt") || ContainsSecret("https://example.test/private") {
		t.Fatal("safe text was classified as a secret")
	}
}
