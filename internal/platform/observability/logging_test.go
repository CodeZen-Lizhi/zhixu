package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLoggerAddsMergedCorrelationAndRedactsSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger("info", &output)
	ctx := WithCorrelation(context.Background(), Correlation{
		RequestID: "request-1", WorkspaceID: "workspace-1", WorkflowRunID: "run-1",
		AttemptNo: 1, DispatchNo: 2, RiverJobID: 42,
	})
	ctx = WithCorrelation(ctx, Correlation{NodeRunID: "node-1", ProposalID: "proposal-1", RetryNo: 3})

	logger.InfoContext(ctx, "workflow node completed",
		"credential", "credential-secret",
		"body", "approved body",
		"workspace_path", "/Users/example/private/workspace.md",
		"database_url", "postgres://user:database-secret@localhost/zhixu",
		"token", "token-secret",
		"dependency_error", "request rejected: Bearer bearer-secret",
		"error", errors.New("request failed with password=database-secret"),
		"content_hash", "sha256:public-summary",
	)

	entry := decodeLogEntry(t, output.Bytes())
	for key, want := range map[string]any{
		"request_id": "request-1", "workspace_id": "workspace-1", "workflow_run_id": "run-1",
		"node_run_id": "node-1", "proposal_id": "proposal-1",
		"attempt_no": float64(1), "dispatch_no": float64(2), "retry_no": float64(3), "river_job_id": float64(42),
	} {
		if got := entry[key]; got != want {
			t.Fatalf("%s = %#v, want %#v", key, got, want)
		}
	}
	for _, key := range []string{"credential", "body", "workspace_path", "database_url", "token", "dependency_error", "error"} {
		if got := entry[key]; got != RedactedValue {
			t.Fatalf("%s = %#v, want redacted", key, got)
		}
	}
	if got := entry["content_hash"]; got != "sha256:public-summary" {
		t.Fatalf("content_hash = %#v, want safe summary", got)
	}

	raw := output.String()
	for _, secret := range []string{"credential-secret", "approved body", "/Users/example", "database-secret", "token-secret", "bearer-secret"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("log leaked %q: %s", secret, raw)
		}
	}
}

func TestLoggerRedactsNestedAndBoundAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger("info", &output).With(
		"authorization", "Bearer bound-secret",
		"safe", map[string]any{"token": "nested-secret", "count": 2},
	)
	logger.InfoContext(context.Background(), "completed", "details", map[string]string{
		"dsn": "postgres://user:secret@localhost/db", "status": "failed",
	})

	entry := decodeLogEntry(t, output.Bytes())
	if entry["authorization"] != RedactedValue {
		t.Fatalf("authorization = %#v", entry["authorization"])
	}
	safe, ok := entry["safe"].(map[string]any)
	if !ok || safe["token"] != RedactedValue || safe["count"] != float64(2) {
		t.Fatalf("safe nested value = %#v", entry["safe"])
	}
	details, ok := entry["details"].(map[string]any)
	if !ok || details["dsn"] != RedactedValue || details["status"] != "failed" {
		t.Fatalf("details = %#v", entry["details"])
	}
	for _, secret := range []string{"bound-secret", "nested-secret", "postgres://", "user:secret"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("bound/nested log leaked %q: %s", secret, output.String())
		}
	}
}

func decodeLogEntry(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	entry := make(map[string]any)
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decode log: %v\n%s", err, raw)
	}
	return entry
}
