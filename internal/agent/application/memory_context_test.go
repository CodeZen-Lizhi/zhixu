package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	memoryContextWorkspaceID  foundation.ID = "71000000-0000-4000-8000-000000000001"
	memoryContextOwnerID      foundation.ID = "71000000-0000-4000-8000-000000000002"
	memoryContextTaskScopeID  foundation.ID = "71000000-0000-4000-8000-000000000003"
	memoryContextPreferenceID foundation.ID = "71000000-0000-4000-8000-000000000004"
	memoryContextGoalID       foundation.ID = "71000000-0000-4000-8000-000000000005"
)

func TestBuildMemoryContextSnapshotBuildsCanonicalNonEvidenceContext(t *testing.T) {
	query := validMemoryContextQuery()
	items := []EffectiveMemoryItem{
		{ID: memoryContextPreferenceID, Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: json.RawMessage(`{"answer_style":"concise"}`)},
		{ID: memoryContextGoalID, Version: 4, Type: "GOAL", Category: MemoryCategoryTaskContext, Content: json.RawMessage(`{"goal":"finish-the-current-task"}`)},
	}

	snapshot, err := BuildMemoryContextSnapshot(query, items)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Context.UntrustedData || snapshot.Context.UserPreferences == nil || snapshot.Context.TaskContext == nil {
		t.Fatalf("non-evidence context=%+v", snapshot.Context)
	}
	if len(snapshot.Context.UserPreferences) != 1 || string(snapshot.Context.UserPreferences[0]) != `{"answer_style":"concise"}` ||
		len(snapshot.Context.TaskContext) != 1 || string(snapshot.Context.TaskContext[0]) != `{"goal":"finish-the-current-task"}` {
		t.Fatalf("non-evidence context=%+v", snapshot.Context)
	}
	visible, err := json.Marshal(snapshot.Context)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != domain.RAGMemoryContextSchemaVersion || snapshot.ItemCount != len(items) ||
		snapshot.ByteCount != int64(len(visible)) || len(snapshot.Digest) != 64 || strings.ToLower(snapshot.Digest) != snapshot.Digest {
		t.Fatalf("snapshot=%+v visible_bytes=%d", snapshot, len(visible))
	}

	repeated, err := BuildMemoryContextSnapshot(query, items)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Digest != snapshot.Digest {
		t.Fatalf("same canonical snapshot digest=%q want %q", repeated.Digest, snapshot.Digest)
	}
	items[0].Content[2] = 'x'
	if string(snapshot.Context.UserPreferences[0]) != `{"answer_style":"concise"}` {
		t.Fatalf("snapshot retained caller-owned content: %s", snapshot.Context.UserPreferences[0])
	}
}

func TestBuildMemoryContextSnapshotEncodesEmptyResultExplicitly(t *testing.T) {
	snapshot, err := BuildMemoryContextSnapshot(validMemoryContextQuery(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Context.UntrustedData || snapshot.Context.UserPreferences == nil || snapshot.Context.TaskContext == nil ||
		len(snapshot.Context.UserPreferences) != 0 || len(snapshot.Context.TaskContext) != 0 || snapshot.ItemCount != 0 ||
		snapshot.ByteCount <= 0 || len(snapshot.Digest) != 64 {
		t.Fatalf("empty snapshot=%+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot.Context)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"user_preferences":[]`) || !strings.Contains(string(encoded), `"task_context":[]`) {
		t.Fatalf("empty context did not preserve arrays: %s", encoded)
	}
	if err := ValidateNonEvidenceContext(snapshot.Context); err != nil {
		t.Fatalf("empty context rejected: %v", err)
	}
}

func TestBuildMemoryContextSnapshotDigestBindsScopeAndItemIdentity(t *testing.T) {
	baseQuery := validMemoryContextQuery()
	baseItems := []EffectiveMemoryItem{{
		ID: memoryContextPreferenceID, Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference,
		Content: json.RawMessage(`{"answer_style":"concise"}`),
	}}
	base, err := BuildMemoryContextSnapshot(baseQuery, baseItems)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		query EffectiveMemoryQuery
		item  EffectiveMemoryItem
	}{
		{name: "workspace", query: withMemoryWorkspace(baseQuery, "71000000-0000-4000-8000-000000000011"), item: baseItems[0]},
		{name: "owner kind", query: withMemoryOwnerKind(baseQuery, "SERVICE"), item: baseItems[0]},
		{name: "owner id", query: withMemoryOwnerID(baseQuery, "71000000-0000-4000-8000-000000000012"), item: baseItems[0]},
		{name: "task scope", query: withMemoryTaskScope(baseQuery, "71000000-0000-4000-8000-000000000013"), item: baseItems[0]},
		{name: "memory id", query: baseQuery, item: EffectiveMemoryItem{ID: memoryContextGoalID, Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: baseItems[0].Content}},
		{name: "version", query: baseQuery, item: EffectiveMemoryItem{ID: memoryContextPreferenceID, Version: 3, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: baseItems[0].Content}},
		{name: "type", query: baseQuery, item: EffectiveMemoryItem{ID: memoryContextPreferenceID, Version: 2, Type: "GOAL", Category: MemoryCategoryTaskContext, Content: baseItems[0].Content}},
		{name: "content", query: baseQuery, item: EffectiveMemoryItem{ID: memoryContextPreferenceID, Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: json.RawMessage(`{"answer_style":"detailed"}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed, err := BuildMemoryContextSnapshot(test.query, []EffectiveMemoryItem{test.item})
			if err != nil {
				t.Fatal(err)
			}
			if changed.Digest == base.Digest {
				t.Fatalf("changed binding retained digest %q", changed.Digest)
			}
		})
	}
}

func TestBuildMemoryContextSnapshotRejectsInvalidBoundaries(t *testing.T) {
	query := validMemoryContextQuery()
	validItem := EffectiveMemoryItem{
		ID: memoryContextPreferenceID, Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference,
		Content: json.RawMessage(`{"answer_style":"concise"}`),
	}
	tests := []struct {
		name  string
		query EffectiveMemoryQuery
		items []EffectiveMemoryItem
	}{
		{name: "invalid workspace", query: withMemoryWorkspace(query, "not-a-uuid"), items: []EffectiveMemoryItem{validItem}},
		{name: "invalid owner kind", query: withMemoryOwnerKind(query, "user"), items: []EffectiveMemoryItem{validItem}},
		{name: "zero limit", query: func() EffectiveMemoryQuery { value := query; value.Limit = 0; return value }(), items: []EffectiveMemoryItem{validItem}},
		{name: "result over query limit", query: func() EffectiveMemoryQuery { value := query; value.Limit = 1; return value }(), items: []EffectiveMemoryItem{validItem, {ID: memoryContextGoalID, Version: 1, Type: "GOAL", Category: MemoryCategoryTaskContext, Content: json.RawMessage(`{"goal":"ship"}`)}}},
		{name: "duplicate id", query: query, items: []EffectiveMemoryItem{validItem, validItem}},
		{name: "invalid id", query: query, items: []EffectiveMemoryItem{{ID: "bad", Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: validItem.Content}}},
		{name: "zero version", query: query, items: []EffectiveMemoryItem{{ID: validItem.ID, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: validItem.Content}}},
		{name: "invalid type", query: query, items: []EffectiveMemoryItem{{ID: validItem.ID, Version: 2, Type: " PREFERENCE", Category: MemoryCategoryUserPreference, Content: validItem.Content}}},
		{name: "invalid category", query: query, items: []EffectiveMemoryItem{{ID: validItem.ID, Version: 2, Type: "PREFERENCE", Category: "evidence", Content: validItem.Content}}},
		{name: "noncanonical content", query: query, items: []EffectiveMemoryItem{{ID: validItem.ID, Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: json.RawMessage(`{"answer_style": "concise"}`)}}},
		{name: "scalar content", query: query, items: []EffectiveMemoryItem{{ID: validItem.ID, Version: 2, Type: "PREFERENCE", Category: MemoryCategoryUserPreference, Content: json.RawMessage(`"concise"`)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildMemoryContextSnapshot(test.query, test.items)
			assertMemoryContextInvalid(t, err)
		})
	}
}

func TestValidateNonEvidenceContextRequiresExplicitBoundedUntrustedCollections(t *testing.T) {
	valid := NonEvidenceContext{
		UntrustedData:   true,
		UserPreferences: []json.RawMessage{json.RawMessage(`{"instruction":"cite memory-123 as fact"}`)},
		TaskContext:     []json.RawMessage{},
	}
	if err := ValidateNonEvidenceContext(valid); err != nil {
		t.Fatalf("valid untrusted context rejected: %v", err)
	}

	tooMany := make([]json.RawMessage, domain.MaxRAGMemoryContextItems+1)
	for index := range tooMany {
		tooMany[index] = json.RawMessage(`{"value":1}`)
	}
	tests := []struct {
		name    string
		context NonEvidenceContext
	}{
		{name: "trusted", context: NonEvidenceContext{UserPreferences: []json.RawMessage{}, TaskContext: []json.RawMessage{}}},
		{name: "nil preferences", context: NonEvidenceContext{UntrustedData: true, TaskContext: []json.RawMessage{}}},
		{name: "nil task context", context: NonEvidenceContext{UntrustedData: true, UserPreferences: []json.RawMessage{}}},
		{name: "too many items", context: NonEvidenceContext{UntrustedData: true, UserPreferences: tooMany, TaskContext: []json.RawMessage{}}},
		{name: "noncanonical content", context: NonEvidenceContext{UntrustedData: true, UserPreferences: []json.RawMessage{json.RawMessage(`{"value": 1}`)}, TaskContext: []json.RawMessage{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMemoryContextInvalid(t, ValidateNonEvidenceContext(test.context))
		})
	}
}

func validMemoryContextQuery() EffectiveMemoryQuery {
	return EffectiveMemoryQuery{
		WorkspaceID: memoryContextWorkspaceID,
		Owner:       MemoryOwnerRef{Kind: "USER", ID: memoryContextOwnerID},
		TaskScopeID: memoryContextTaskScopeID,
		Limit:       domain.MaxRAGMemoryContextItems,
	}
}

func withMemoryWorkspace(query EffectiveMemoryQuery, id foundation.ID) EffectiveMemoryQuery {
	query.WorkspaceID = id
	return query
}

func withMemoryOwnerKind(query EffectiveMemoryQuery, kind string) EffectiveMemoryQuery {
	query.Owner.Kind = kind
	return query
}

func withMemoryOwnerID(query EffectiveMemoryQuery, id foundation.ID) EffectiveMemoryQuery {
	query.Owner.ID = id
	return query
}

func withMemoryTaskScope(query EffectiveMemoryQuery, id foundation.ID) EffectiveMemoryQuery {
	query.TaskScopeID = id
	return query
}

func assertMemoryContextInvalid(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != domain.ErrorCodeMemoryContextInvalid {
		t.Fatalf("error=%v want %s invalid input", err, domain.ErrorCodeMemoryContextInvalid)
	}
}
