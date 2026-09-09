package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const testWorkspace foundation.ID = "11111111-1111-4111-8111-111111111111"

type readerFunc func(context.Context, domain.ListQuery) ([]domain.Event, error)

func (read readerFunc) List(ctx context.Context, query domain.ListQuery) ([]domain.Event, error) {
	return read(ctx, query)
}

func TestRunRejectsInvalidArgumentsBeforeDatabaseAccess(t *testing.T) {
	tests := [][]string{
		{}, {"--global=false"}, {"--workspace", string(testWorkspace), "--global"},
		{"--workspace", "postgres://user:private-argument@host/db"},
		{"--global", "--limit", "0"}, {"--global", "--limit", "201"}, {"--global", "--limit", "-1"},
		{"--global", "--limit", "private-argument"}, {"--global", "private-argument"}, {"--private-argument"},
		{"--global", "--timeout", "0"}, {"--global", "--timeout", "-1s"}, {"--global", "--timeout", "61s"},
		{"--global", "--before", "2026-09-08T00:00:00Z"}, {"--global", "--before-id", string(testWorkspace)},
		{"--global", "--before", "private-argument", "--before-id", string(testWorkspace)},
		{"--global", "--before", "2026-09-08T00:00:00.000000001Z", "--before-id", string(testWorkspace)},
		{"--global", "--before", "2026-09-08T00:00:00Z", "--before-id", "private-argument"},
		{"--global", "--before", "0001-01-01T00:00:00Z", "--before-id", string(testWorkspace)},
	}
	for index, arguments := range tests {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			opened := false
			code := run(context.Background(), arguments, &stdout, &stderr, func(context.Context, string) (auditReader, func(), error) {
				opened = true
				return nil, nil, errors.New("unexpected database access")
			})
			if code != 2 || opened || stdout.Len() != 0 {
				t.Fatalf("invalid input: code=%d opened=%t stdout_bytes=%d", code, opened, stdout.Len())
			}
			assertFailure(t, &stderr, "AUDIT_QUERY_ARGUMENTS_INVALID", "private-argument")
		})
	}
}

func TestHelpDoesNotRequireDatabaseOrScope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &stdout, &stderr, nil)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "workspace_id IS NULL") {
		t.Fatalf("help: code=%d stdout_bytes=%d stderr_bytes=%d", code, stdout.Len(), stderr.Len())
	}
}

func TestRunPreservesWorkspaceAndCompositeCursor(t *testing.T) {
	workspace := testWorkspace
	events := []domain.Event{testEvent(t, 3, &workspace), testEvent(t, 2, &workspace), testEvent(t, 1, &workspace)}
	var queries []domain.ListQuery
	closed := 0
	open := func(ctx context.Context, path string) (auditReader, func(), error) {
		if path != "audit.yml" {
			t.Fatal("configuration path was not forwarded")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > defaultTimeout {
			t.Fatal("database operation has no bounded deadline")
		}
		return readerFunc(func(_ context.Context, query domain.ListQuery) ([]domain.Event, error) {
			queries = append(queries, query)
			if len(queries) == 1 {
				return events[:2], nil
			}
			return events[2:], nil
		}), func() { closed++ }, nil
	}
	arguments := []string{"--workspace", string(workspace), "--limit", "2", "--config", "audit.yml"}
	first := runPage(t, arguments, open)
	if len(first.Items) != 2 || first.NextCursor == nil || first.NextCursor.BeforeID != events[1].ID {
		t.Fatal("first page did not expose the last composite cursor")
	}
	arguments = append(arguments, "--before", first.NextCursor.Before.Format(time.RFC3339Nano), "--before-id", string(first.NextCursor.BeforeID))
	second := runPage(t, arguments, open)
	if len(second.Items) != 1 || second.Items[0].ID != events[2].ID || second.NextCursor != nil {
		t.Fatal("last page or cursor is incorrect")
	}
	if closed != 2 || len(queries) != 2 || queries[0].Limit != 2 || !queries[0].Before.IsZero() ||
		!sameWorkspace(queries[1].WorkspaceID, &workspace) || queries[1].BeforeID != events[1].ID || !queries[1].Before.Equal(events[1].OccurredAt) {
		t.Fatal("scope, page bound, cursor or cleanup changed across pages")
	}
}

func TestGlobalEmptyPageAndLimitBoundaries(t *testing.T) {
	for _, limit := range []string{"", "1", "200"} {
		arguments := []string{"--global"}
		wantLimit := defaultLimit
		if limit != "" {
			arguments = append(arguments, "--limit", limit)
			if limit == "1" {
				wantLimit = 1
			} else {
				wantLimit = domain.MaxListLimit
			}
		}
		page := runPage(t, arguments, fixedReader(func(_ context.Context, query domain.ListQuery) ([]domain.Event, error) {
			if query.WorkspaceID != nil || query.Limit != wantLimit {
				t.Fatal("global query changed scope or bound")
			}
			return nil, nil
		}))
		if page.WorkspaceID != nil || page.Limit != wantLimit || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
			t.Fatal("empty global page must contain an empty array and null cursor")
		}
	}
	settings, err := parseOptions([]string{"--global", "--before", "2026-09-08T08:00:00.123456+08:00", "--before-id", strings.ToUpper(string(testWorkspace))})
	if err != nil || settings.query.Before.Location() != time.UTC || settings.query.Before.Hour() != 0 {
		t.Fatal("RFC3339 cursor was not normalized to UTC")
	}
}

func TestSummaryExcludesFreeTextAndMetadata(t *testing.T) {
	event := testEvent(t, 1, nil)
	event.ActorRef = "private-actor-canary"
	event.ResourceRef = "private-resource-canary"
	event.IdempotencyKey = "private-command-canary"
	event.Correlation = json.RawMessage(`{"request_id":"private-correlation-canary"}`)
	event.Metadata = json.RawMessage(`{"note":"private body canary","api_key":"private-secret-canary"}`)
	event, err := domain.NewEvent(event)
	if err != nil {
		t.Fatal("invalid safe output fixture")
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--global"}, &stdout, &stderr, fixedReader(func(context.Context, domain.ListQuery) ([]domain.Event, error) {
		return []domain.Event{event}, nil
	}))
	if code != 0 || stderr.Len() != 0 {
		t.Fatal("safe summary failed")
	}
	if strings.Contains(stdout.String(), "private") || strings.Contains(stdout.String(), "<redacted>") {
		t.Fatal("free text or metadata escaped the output projection")
	}
	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if json.Unmarshal(stdout.Bytes(), &page) != nil || len(page.Items) != 1 {
		t.Fatal("invalid page encoding")
	}
	wantFields := []string{"id", "occurred_at", "actor_type", "action", "resource_type", "outcome"}
	if len(page.Items[0]) != len(wantFields) {
		t.Fatal("summary exposed an unexpected field")
	}
	for _, field := range wantFields {
		if _, ok := page.Items[0][field]; !ok {
			t.Fatalf("summary is missing %s", field)
		}
	}
}

func TestInvalidPageFailsBeforeAnyOutput(t *testing.T) {
	workspace := testWorkspace
	otherWorkspace := foundation.ID("22222222-2222-4222-8222-222222222222")
	tests := map[string]func([]domain.Event) []domain.Event{
		"wrong workspace": func(events []domain.Event) []domain.Event { events[1].WorkspaceID = &otherWorkspace; return events },
		"global leakage":  func(events []domain.Event) []domain.Event { events[1].WorkspaceID = nil; return events },
		"duplicate row":   func(events []domain.Event) []domain.Event { events[1] = events[0]; return events },
		"wrong order":     func(events []domain.Event) []domain.Event { events[0], events[1] = events[1], events[0]; return events },
		"beyond limit":    func(events []domain.Event) []domain.Event { return append(events, events[1]) },
		"invalid id":      func(events []domain.Event) []domain.Event { events[1].ID = "private-id-canary"; return events },
		"stored secret": func(events []domain.Event) []domain.Event {
			events[1].Metadata = json.RawMessage(`{"password":"private-secret-canary"}`)
			return events
		},
		"free text action": func(events []domain.Event) []domain.Event { events[1].Action = "private body canary"; return events },
		"resource endpoint": func(events []domain.Event) []domain.Event {
			events[1].ResourceType = "https://private-host/resource"
			return events
		},
		"unsafe error code": func(events []domain.Event) []domain.Event {
			events[1].Outcome, events[1].ErrorCode = domain.OutcomeFailed, "error at /private/path"
			return events
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			events := mutate([]domain.Event{testEvent(t, 2, &workspace), testEvent(t, 1, &workspace)})
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"--workspace", string(workspace), "--limit", "2"}, &stdout, &stderr,
				fixedReader(func(context.Context, domain.ListQuery) ([]domain.Event, error) { return events, nil }))
			if code != 1 || stdout.Len() != 0 {
				t.Fatal("invalid page was partially returned")
			}
			assertFailure(t, &stderr, domain.ErrorCodeCorrupt, "private")
		})
	}
	var stdout, stderr bytes.Buffer
	event := testEvent(t, 1, nil)
	code := run(context.Background(), []string{"--global", "--before", event.OccurredAt.Format(time.RFC3339Nano), "--before-id", string(event.ID)},
		&stdout, &stderr, fixedReader(func(context.Context, domain.ListQuery) ([]domain.Event, error) { return []domain.Event{event}, nil }))
	if code != 1 || stdout.Len() != 0 {
		t.Fatal("cursor boundary was not exclusive")
	}
	assertFailure(t, &stderr, domain.ErrorCodeCorrupt, "private")
}

func TestQueryFailuresAndCancellationAreSafeAndCloseReader(t *testing.T) {
	private := errors.New("postgres://user:private-password@host/db: /private/path SQL body canary")
	tests := []struct {
		err  error
		code string
	}{
		{private, "AUDIT_QUERY_UNAVAILABLE"},
		{foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeCorrupt, false, private), domain.ErrorCodeCorrupt},
		{foundation.NewError(foundation.ErrorDependencyUnavailable, "private-code-canary", true, private), "AUDIT_QUERY_UNAVAILABLE"},
		{errors.Join(private, context.Canceled), "AUDIT_QUERY_CANCELLED"},
		{errors.Join(private, context.DeadlineExceeded), "AUDIT_QUERY_TIMEOUT"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		closed := false
		code := run(context.Background(), []string{"--global"}, &stdout, &stderr, func(context.Context, string) (auditReader, func(), error) {
			return readerFunc(func(context.Context, domain.ListQuery) ([]domain.Event, error) { return nil, test.err }), func() { closed = true }, nil
		})
		if code != 1 || stdout.Len() != 0 || !closed {
			t.Fatal("query failure returned success or did not close the reader")
		}
		assertFailure(t, &stderr, test.code, "private")
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--global", "--timeout", "5ms"}, &stdout, &stderr,
		fixedReader(func(ctx context.Context, _ domain.ListQuery) ([]domain.Event, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}))
	if code != 1 || stdout.Len() != 0 {
		t.Fatal("expired query did not fail")
	}
	assertFailure(t, &stderr, "AUDIT_QUERY_TIMEOUT", "private")

	stderr.Reset()
	code = run(context.Background(), []string{"--global"}, &stdout, &stderr, func(context.Context, string) (auditReader, func(), error) {
		return nil, nil, private
	})
	if code != 1 {
		t.Fatal("connection failure returned success")
	}
	assertFailure(t, &stderr, "AUDIT_QUERY_UNAVAILABLE", "private")
}

func TestConfigAndOutputFailuresAreSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-config-canary.yml")
	if err := os.WriteFile(path, []byte("unknown_private_key: private-config-body\n"), 0o600); err != nil {
		t.Fatal("write invalid configuration fixture")
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--global", "--config", path}, &stdout, &stderr, openAuditReader)
	if code != 1 || stdout.Len() != 0 {
		t.Fatal("invalid configuration returned success")
	}
	assertFailure(t, &stderr, "AUDIT_QUERY_CONFIG_INVALID", "private")
	stderr.Reset()
	code = run(context.Background(), []string{"--global"}, errorWriter{}, &stderr, fixedReader(func(context.Context, domain.ListQuery) ([]domain.Event, error) { return nil, nil }))
	if code != 1 {
		t.Fatal("output failure returned success")
	}
	assertFailure(t, &stderr, "AUDIT_QUERY_OUTPUT_FAILED", "private")
}

func TestReadOnlyDatabaseURLPreservesCredentialsAndTLSWithoutPrintingThem(t *testing.T) {
	original := &url.URL{Scheme: "postgres", Host: "localhost:5432", Path: "/audit", User: url.UserPassword("operator", "private@?#password")}
	original.RawQuery = "sslmode=verify-full&default_transaction_read_only=off&default_transaction_read_only=off"
	connection, err := readOnlyDatabaseURL(original.String())
	if err != nil {
		t.Fatal("could not configure read-only connection")
	}
	parsed, err := url.Parse(connection)
	if err != nil || parsed.User.String() != original.User.String() || parsed.Host != original.Host || parsed.Path != original.Path ||
		parsed.Query().Get("sslmode") != "verify-full" || len(parsed.Query()["default_transaction_read_only"]) != 1 || parsed.Query().Get("default_transaction_read_only") != "on" {
		t.Fatal("read-only startup option altered other connection settings")
	}
	for _, value := range []string{"", "host=localhost password=private-canary", "https://private-canary", "postgres://user:private%xx@host/db", "postgres://host/db?x=%zz"} {
		_, err := readOnlyDatabaseURL(value)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid connection settings were accepted or exposed")
		}
	}
}

func fixedReader(read readerFunc) readerOpener {
	return func(context.Context, string) (auditReader, func(), error) { return read, func() {}, nil }
}

func runPage(t *testing.T, arguments []string, open readerOpener) auditPage {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), arguments, &stdout, &stderr, open); code != 0 || stderr.Len() != 0 {
		t.Fatalf("query failed: exit=%d stderr_bytes=%d", code, stderr.Len())
	}
	var page auditPage
	if json.Unmarshal(stdout.Bytes(), &page) != nil || page.Schema != "audit-query/v1" {
		t.Fatal("query output is not a valid audit page")
	}
	return page
}

func testEvent(t *testing.T, ordinal int, workspace *foundation.ID) domain.Event {
	t.Helper()
	event, err := domain.NewEvent(domain.Event{
		ID: foundation.ID(fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012x", ordinal)), WorkspaceID: workspace,
		ActorType: domain.ActorSystem, Action: "security.check", ResourceType: "workspace",
		Outcome: domain.OutcomeSucceeded, IdempotencyKey: fmt.Sprintf("audit-cli:%d", ordinal),
		SchemaVersion: domain.SchemaVersion, OccurredAt: time.Date(2026, 9, 8, 0, 0, 0, 123456000, time.UTC),
	})
	if err != nil {
		t.Fatal("invalid Audit fixture")
	}
	return event
}

func assertFailure(t *testing.T, output *bytes.Buffer, code, forbidden string) {
	t.Helper()
	var failure commandFailure
	if json.Unmarshal(output.Bytes(), &failure) != nil || failure.Code != code || failure.Message == "" {
		t.Fatalf("unexpected failure response; expected code=%s", code)
	}
	if strings.Contains(output.String(), forbidden) {
		t.Fatal("failure response exposed sensitive input")
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("private-output-canary at /private/output")
}

var _ io.Writer = errorWriter{}
