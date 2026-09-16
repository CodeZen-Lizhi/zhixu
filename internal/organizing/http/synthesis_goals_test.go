package organizinghttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

type goalHTTPStub struct {
	app.SynthesisGoalViewReader
	app.SynthesisGoalSelectionCommands
	create func(context.Context, app.CreateSynthesisGoalCommand) (app.SynthesisGoalCreateResult, error)
	view   app.SynthesisGoalView
}

func (s *goalHTTPStub) CreateSynthesisGoal(ctx context.Context, c app.CreateSynthesisGoalCommand) (app.SynthesisGoalCreateResult, error) {
	return s.create(ctx, c)
}
func (s *goalHTTPStub) GetSynthesisGoalView(context.Context, foundation.ID, foundation.ID) (app.SynthesisGoalView, error) {
	return s.view, nil
}

func goalHTTPView() app.SynthesisGoalView {
	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	r := app.SynthesisGoalRequest{ID: testID(40), WorkspaceID: testID(1), Goal: "整理 Redis", Status: app.SynthesisGoalCatalogReady, CatalogBatches: 1, Version: 2, CreatedAt: now, UpdatedAt: now}
	return app.SynthesisGoalView{Request: r, Progress: app.SynthesisGoalSelectionProgress{Request: r, CatalogBatches: 1, PreparedBatches: 1, Selections: 1, Succeeded: 1}}
}

func TestGoalHTTPValidatesCommandBeforeCreating(t *testing.T) {
	calls := 0
	stub := &goalHTTPStub{view: goalHTTPView()}
	stub.create = func(_ context.Context, c app.CreateSynthesisGoalCommand) (app.SynthesisGoalCreateResult, error) {
		calls++
		if c.WorkspaceID != testID(1) || c.Goal != "整理 Redis" || c.IdempotencyKey != "create-goal" {
			t.Fatalf("unexpected command %+v", c)
		}
		return app.SynthesisGoalCreateResult{Request: stub.view.Request}, nil
	}
	router := synthesisRouter(NewSynthesisHandlerWithGoals(synthesisNotesStub{}, synthesisProcessingStub{}, nil, stub, stub, stub, time.Second))
	url := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/goals"
	for _, body := range []string{`{}`, `{"goal":null}`, `{"goal":""}`, `{"goal":" x "}`, `{"goal":"x","goal":"y"}`, `{"goal":"x","workspace_id":"` + string(testID(2)) + `"}`, `{"goal":"x\ny"}`} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, newJSONRequest(http.MethodPost, url, body, "create-goal"))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s status %d %s", body, response.Code, response.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("invalid command wrote goal")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, newJSONRequest(http.MethodPost, url, `{"goal":"整理 Redis"}`, "create-goal"))
	if response.Code != http.StatusAccepted || calls != 1 {
		t.Fatalf("valid command %d %s", response.Code, response.Body.String())
	}
}

func TestGoalHTTPPreservesEmptyCompletionAndRejectsMisboundResults(t *testing.T) {
	base := goalHTTPView()
	tests := []struct {
		name   string
		change func(*app.SynthesisGoalView)
		want   int
	}{
		{"empty complete", func(*app.SynthesisGoalView) {}, 200},
		{"selection still pending", func(v *app.SynthesisGoalView) { v.Progress.Succeeded = 0; v.Progress.Pending = 1 }, 200},
		{"wrong workspace", func(v *app.SynthesisGoalView) { v.Request.WorkspaceID = testID(2) }, http.StatusConflict},
		{"wrong request", func(v *app.SynthesisGoalView) { v.Request.ID = testID(41); v.Progress.Request = v.Request }, http.StatusConflict},
		{"inconsistent counts", func(v *app.SynthesisGoalView) { v.Progress.Pending = 1 }, http.StatusConflict},
		{"unbound candidate", func(v *app.SynthesisGoalView) {
			v.Candidate = &app.SynthesisGoalCandidate{NoteID: testID(42), RevisionID: testID(43)}
		}, http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			tc.change(&v)
			stub := &goalHTTPStub{view: v}
			router := synthesisRouter(NewSynthesisHandlerWithGoals(synthesisNotesStub{}, synthesisProcessingStub{}, nil, stub, stub, stub, time.Second))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+string(testID(1))+"/synthesis/goals/"+string(testID(40)), nil))
			if response.Code != tc.want {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
			if tc.want == 200 {
				var wire synthesisGoalViewResponse
				if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil || wire.Progress.Ready != v.Progress.Ready() || wire.Candidate != nil {
					t.Fatalf("progress %s %v", response.Body.String(), err)
				}
			}
		})
	}
}

type goalSelectionHTTPStub struct {
	app.SynthesisGoalSelectionCommands
	selection app.SynthesisGoalSelection
	calls     int
}

func (s *goalSelectionHTTPStub) GetGoalSelection(context.Context, foundation.ID, foundation.ID) (app.SynthesisGoalSelection, error) {
	return s.selection, nil
}
func (s *goalSelectionHTTPStub) RetryGoalSelection(_ context.Context, c app.RetryGoalSelectionCommand) (app.SynthesisGoalSelection, error) {
	s.calls++
	if c.WorkspaceID != s.selection.WorkspaceID || c.SelectionID != s.selection.ID || c.ExpectedVersion != s.selection.Version || c.IdempotencyKey != "retry-selection" {
		return app.SynthesisGoalSelection{}, requestInvalid("wrong retry command")
	}
	result := s.selection
	result.Status = app.GoalSelectionPending
	result.ErrorCode = ""
	result.Retryable = false
	result.Version++
	return result, nil
}
func TestGoalSelectionHTTPBindsRetryToGoalBeforeWrite(t *testing.T) {
	v := goalHTTPView()
	goals := &goalHTTPStub{view: v}
	selections := &goalSelectionHTTPStub{selection: app.SynthesisGoalSelection{ID: testID(50), WorkspaceID: testID(1), RequestID: v.Request.ID, Status: app.GoalSelectionFailed, ErrorCode: "MODEL_UNAVAILABLE", Retryable: true, Version: 4, CreatedAt: v.Request.CreatedAt, UpdatedAt: v.Request.UpdatedAt}}
	router := synthesisRouter(NewSynthesisHandlerWithGoals(synthesisNotesStub{}, synthesisProcessingStub{}, nil, goals, goals, selections, time.Second))
	base := "/api/v1/workspaces/" + string(testID(1)) + "/synthesis/goals/"
	for _, tc := range []struct {
		goal foundation.ID
		want int
	}{{testID(41), http.StatusNotFound}, {v.Request.ID, http.StatusAccepted}} {
		before := selections.calls
		response := httptest.NewRecorder()
		router.ServeHTTP(response, newJSONRequest(http.MethodPost, base+string(tc.goal)+"/selections/"+string(testID(50))+"/retry", `{"expected_version":4}`, "retry-selection"))
		if response.Code != tc.want {
			t.Fatalf("retry %d %s", response.Code, response.Body.String())
		}
		if tc.want == http.StatusNotFound && selections.calls != before {
			t.Fatal("cross-goal retry wrote a selection")
		}
		if tc.want == http.StatusAccepted {
			var out synthesisGoalSelectionResponse
			if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil || out.ID != testID(50) || out.RequestID != v.Request.ID || out.Status != app.GoalSelectionPending || out.Version != 5 {
				t.Fatalf("retry response %s %v", response.Body.String(), err)
			}
		}
	}
}
