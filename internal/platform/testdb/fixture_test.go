package testdb

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactURLRemovesPasswordAndQuery(t *testing.T) {
	redacted := redactURL("postgres://alice:super-secret@example.test:5432/zhixu?sslmode=disable&password=also-secret")
	if strings.Contains(redacted, "super-secret") || strings.Contains(redacted, "also-secret") {
		t.Fatalf("redacted URL contains secret: %s", redacted)
	}
	if redacted != "postgres://alice@example.test:5432/zhixu" {
		t.Fatalf("redacted URL = %s", redacted)
	}
}

func TestOpenRejectsInvalidContextPoolLimitsAndPolicy(t *testing.T) {
	if _, err := Open(nil, Config{}); err == nil || !strings.Contains(err.Error(), "context is nil") {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := Open(context.Background(), Config{MaxConns: -1}); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("negative pool limit error = %v", err)
	}
	if _, err := Open(context.Background(), Config{MinConns: 4, MaxConns: 2}); err == nil || !strings.Contains(err.Error(), "minimum connections") {
		t.Fatalf("invalid pool range error = %v", err)
	}
	if _, err := Open(context.Background(), Config{Availability: AvailabilityPolicy(99)}); err == nil || !strings.Contains(err.Error(), "availability policy") {
		t.Fatalf("invalid policy error = %v", err)
	}
}

func TestValidateConfigAcceptsBothAvailabilityPolicies(t *testing.T) {
	for _, policy := range []AvailabilityPolicy{FailWhenUnavailable, SkipWhenUnavailable} {
		if err := validateConfig(Config{Availability: policy}); err != nil {
			t.Fatalf("availability policy %d rejected: %v", policy, err)
		}
	}
}

func TestOpenExternalAdminRejectsMalformedURLWithoutLeakingCredentials(t *testing.T) {
	const secret = "super-secret"
	_, err := Open(context.Background(), Config{ExternalAdminURL: "postgres://user:" + secret + "[%zz"})
	if err == nil {
		t.Fatal("expected malformed external URL error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "postgres://user:") {
		t.Fatalf("external URL leaked in error: %v", err)
	}
}

func TestParseAdminURLRequiresPostgresHostAndDatabase(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "scheme", raw: "mysql://postgres.example/admin"},
		{name: "host", raw: "postgres:///admin"},
		{name: "database", raw: "postgres://postgres@example"},
		{name: "nested database", raw: "postgres://postgres@example/admin/extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseAdminURL(test.raw); err == nil {
				t.Fatalf("parseAdminURL(%q) unexpectedly succeeded", test.raw)
			}
		})
	}
}

func TestParseAdminURLIgnoresDatabaseQueryOverride(t *testing.T) {
	parsed, err := parseAdminURL("postgres://postgres@example/admin?database=wrong&dbname=also-wrong&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("database"); got != "" {
		t.Fatalf("admin URL kept database query override %q", got)
	}
	if got := parsed.Query().Get("dbname"); got != "" {
		t.Fatalf("admin URL kept dbname query override %q", got)
	}
	if got := parsed.Query().Get("sslmode"); got != "disable" {
		t.Fatalf("admin URL lost connection query options: %q", got)
	}
}

func TestDatabaseURLForNamePreservesAdminCredentialsAndRemovesDatabaseQuery(t *testing.T) {
	admin, err := url.Parse("postgres://alice:secret@example.test/admin?sslmode=disable&database=admin")
	if err != nil {
		t.Fatal(err)
	}
	target := databaseURLForName(admin, "zhixu_test_abc123")
	if target != "postgres://alice:secret@example.test/zhixu_test_abc123?sslmode=disable" {
		t.Fatalf("target URL = %s", target)
	}
}

func TestRandomDatabaseNameIsUniqueAndSafe(t *testing.T) {
	first, err := randomDatabaseName()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomDatabaseName()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, databasePrefix) || !strings.HasPrefix(second, databasePrefix) {
		t.Fatalf("temporary database names are not unique/safe: %q %q", first, second)
	}
	if len(first) > 63 || strings.ContainsAny(first, "' \"") {
		t.Fatalf("temporary database name is not a PostgreSQL identifier: %q", first)
	}
}

func TestCleanupOpenFailurePreservesPrimaryError(t *testing.T) {
	primary := errors.New("migration sentinel")
	fixture := &Fixture{}
	if err := cleanupOpenFailure(fixture, primary); !errors.Is(err, primary) {
		t.Fatalf("cleanup error does not preserve primary: %v", err)
	}
	if !fixture.Diagnostics().Closed {
		t.Fatal("failed fixture was not marked closed")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	fixture := &Fixture{}
	if err := fixture.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fixture.Diagnostics().Closed {
		t.Fatal("fixture was not marked closed")
	}
}

func TestSafeErrorDoesNotExposeCauseText(t *testing.T) {
	cause := errors.New("password=secret /tmp/private.dsn")
	err := safeError("open test database", cause)
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/tmp/private.dsn") {
		t.Fatalf("safe error exposed sensitive cause: %v", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatal("safe error exposed its sensitive cause through Unwrap")
	}
	if !errors.Is(err, cause) {
		t.Fatal("safe error did not preserve cause matching")
	}
}
