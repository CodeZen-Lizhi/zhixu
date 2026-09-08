//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	auditpostgres "github.com/CodeZen-Lizhi/zhixu/internal/audit/adapter/postgres"
	auditapplication "github.com/CodeZen-Lizhi/zhixu/internal/audit/application"
	auditdomain "github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func newWorkspaceAnalysisAuditIntegration(
	t *testing.T,
	pool *platformpostgres.Pool,
) (*auditapplication.Recorder, *auditpostgres.GORMStore) {
	t.Helper()
	store, err := auditpostgres.NewGORMStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditapplication.NewRecorder(store)
	if err != nil {
		t.Fatal(err)
	}
	return recorder, store
}

func requireWorkspaceAnalysisAuditEventIntegration(
	t *testing.T,
	ctx context.Context,
	store *auditpostgres.GORMStore,
	workspaceID foundation.ID,
	idempotencyKey string,
) auditdomain.Event {
	t.Helper()
	events, err := store.List(ctx, auditdomain.ListQuery{WorkspaceID: &workspaceID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.IdempotencyKey == idempotencyKey {
			return event
		}
	}
	t.Fatalf("workspace analysis audit key=%q events=%v", idempotencyKey, events)
	return auditdomain.Event{}
}

func requireWorkspaceAnalysisAuditJSONObjectIntegration(
	t *testing.T,
	raw json.RawMessage,
	want map[string]any,
) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode audit JSON %s: %v", raw, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit JSON=%v want=%v", got, want)
	}
}

func requireWorkspaceAnalysisAuditSafeIntegration(t *testing.T, event auditdomain.Event, unsafeValues ...string) {
	t.Helper()
	document := strings.ToLower(string(event.Correlation) + string(event.Metadata))
	for _, forbidden := range []string{
		"question_text", "answer_markdown", "prompt", "receipt", "private_binding", "hash", "root_path",
	} {
		if strings.Contains(document, forbidden) {
			t.Fatalf("workspace analysis audit contains forbidden field %q: %s", forbidden, document)
		}
	}
	for _, unsafe := range unsafeValues {
		if unsafe != "" && strings.Contains(document, strings.ToLower(unsafe)) {
			t.Fatalf("workspace analysis audit contains unsafe value %q: %s", unsafe, document)
		}
	}
}
