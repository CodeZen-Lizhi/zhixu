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
	if !ContainsPII("contact person@example.test for support") || ContainsPII("safe excerpt") {
		t.Fatal("email PII classification drifted")
	}
	if ContainsSecret("contact person@example.test for support") {
		t.Fatal("email must not be classified as a credential secret")
	}
}

func TestRedactTextMasksSecretsAndLocalPathsWithoutDamagingHTTPURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "authorization", input: "Authorization: Bearer top-secret", want: maskedValue},
		{name: "bearer", input: "request used Bearer top-secret", want: "request used " + maskedValue},
		{name: "bare jwt", input: "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.signature123", want: "jwt " + maskedValue},
		{name: "session", input: "session=plain-session", want: maskedValue},
		{name: "email", input: "contact person@example.test for support", want: "contact " + maskedValue + " for support"},
		{name: "dsn assignment", input: "dsn=postgres://user:pass@db.example/app", want: maskedValue},
		{name: "credential url", input: "connect postgres://user:pass@db.example/app now", want: "connect " + maskedValue + " now"},
		{name: "unix path", input: "read /Users/private/source.md next", want: "read " + maskedValue + " next"},
		{name: "windows path", input: `read C:\\Users\\private\\source.md next`, want: "read " + maskedValue + " next"},
		{name: "http url", input: "see https://example.test/usr/local/help?q=1", want: "see https://example.test/usr/local/help?q=1"},
		{name: "plain text", input: "safe relative docs/source.md", want: "safe relative docs/source.md"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RedactText(test.input); got != test.want {
				t.Fatalf("RedactText() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRedactSecretsPreservesLocalPathButStillMasksCredentials(t *testing.T) {
	input := "read /Users/private/source.md for person@example.test with Bearer top-secret"
	want := "read /Users/private/source.md for person@example.test with " + maskedValue
	if got := RedactSecrets(input); got != want {
		t.Fatalf("RedactSecrets() = %q, want %q", got, want)
	}
}
