package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
)

const (
	draftRouteWorkspaceID foundation.ID = "94000000-0000-4000-8000-000000000001"
	draftRouteAnswerID    foundation.ID = "94000000-0000-4000-8000-000000000002"
	draftRouteContent                   = "workspace-a-only-draft"
)

func TestDraftStreamRouteEnforcesAuthenticationCapabilityAndWorkspaceBinding(t *testing.T) {
	t.Parallel()
	const (
		readToken     = "draft-stream-read-token"
		proposalToken = "draft-stream-proposal-token"
	)
	reader := newRouteBoundDraftReader()
	principal := func(id foundation.ID, scopes ...capability.Capability) authdomain.Principal {
		return authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: id, Scopes: scopes}
	}
	router := NewRouter(Dependencies{
		Version:      "draft-stream-auth-test",
		AuthRequired: true,
		Auth: readyScopedAuthHandler(t, map[string]authdomain.Principal{
			readToken:     principal("94000000-0000-4000-8000-000000000011", capability.ReadLocal),
			proposalToken: principal("94000000-0000-4000-8000-000000000012", capability.WriteProposal),
		}),
		DraftStream: conversationhttp.NewDraftStreamHandler(reader),
	})

	validPath := draftRoutePath(draftRouteWorkspaceID, draftRouteAnswerID)
	unauthenticated := serveDraftRouteRequest(t, router, validPath, "")
	requireDraftRouteProblem(t, unauthenticated, http.StatusUnauthorized, authapplication.ErrorCodeUnauthorized)
	if reader.callCount() != 0 {
		t.Fatalf("unauthenticated request reached draft reader: calls=%d", reader.callCount())
	}

	forbidden := serveDraftRouteRequest(t, router, validPath, proposalToken)
	requireDraftRouteProblem(t, forbidden, http.StatusForbidden, authapplication.ErrorCodeForbidden)
	if reader.callCount() != 0 {
		t.Fatalf("request without READ_LOCAL reached draft reader: calls=%d", reader.callCount())
	}

	for _, test := range []struct {
		name        string
		workspaceID foundation.ID
		answerID    foundation.ID
	}{
		{name: "answer requested through another workspace", workspaceID: "94000000-0000-4000-8000-000000000003", answerID: draftRouteAnswerID},
		{name: "another answer requested in the workspace", workspaceID: draftRouteWorkspaceID, answerID: "94000000-0000-4000-8000-000000000004"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveDraftRouteRequest(t, router, draftRoutePath(test.workspaceID, test.answerID), readToken)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
			}
			if response.Body.Len() != 0 || strings.Contains(response.Body.String(), draftRouteContent) {
				t.Fatalf("mismatched binding leaked draft content: %q", response.Body.String())
			}
		})
	}

	authorized := serveDraftRouteRequest(t, router, validPath, readToken)
	if authorized.Code != http.StatusOK || authorized.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d headers=%v body=%q", authorized.Code, authorized.Header(), authorized.Body.String())
	}
	if !strings.Contains(authorized.Body.String(), "id: 1:1\nevent: chunk\n") || !strings.Contains(authorized.Body.String(), `"content":"`+draftRouteContent+`"`) {
		t.Fatalf("authorized response did not contain the bound draft chunk: %q", authorized.Body.String())
	}

	queries := reader.queriesSnapshot()
	if len(queries) != 3 {
		t.Fatalf("draft reader queries=%#v", queries)
	}
	for _, query := range queries {
		if query.Limit != agentapplication.MaxDraftStreamReplayChunks || query.After != nil {
			t.Fatalf("draft reader query is not bounded: %#v", query)
		}
	}
}

func serveDraftRouteRequest(t *testing.T, router http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	ctx, cancel := context.WithCancel(request.Context())
	response := httptest.NewRecorder()
	router.ServeHTTP(&cancelOnFlushRecorder{ResponseRecorder: response, cancel: cancel}, request.WithContext(ctx))
	cancel()
	return response
}

type cancelOnFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (recorder *cancelOnFlushRecorder) Flush() {
	recorder.ResponseRecorder.Flush()
	recorder.cancel()
}

func requireDraftRouteProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != code {
		t.Fatalf("problem=%#v err=%v body=%q", problem, err, response.Body.String())
	}
}

func draftRoutePath(workspaceID, answerID foundation.ID) string {
	return "/api/v1/answers/" + string(answerID) + "/stream?workspace_id=" + string(workspaceID)
}

type routeBoundDraftReader struct {
	mu      sync.Mutex
	queries []agentapplication.DraftStreamReadQuery
	session agentapplication.DraftStreamSession
	chunk   agentapplication.DraftStreamChunk
}

func newRouteBoundDraftReader() *routeBoundDraftReader {
	now := time.Now().UTC()
	session := agentapplication.DraftStreamSession{
		ID: "94000000-0000-4000-8000-000000000005",
		Binding: agentapplication.DraftStreamBinding{
			WorkspaceID:   draftRouteWorkspaceID,
			AnswerID:      draftRouteAnswerID,
			WorkflowRunID: "94000000-0000-4000-8000-000000000006",
			NodeRunID:     "94000000-0000-4000-8000-000000000007",
			NodeAttemptID: "94000000-0000-4000-8000-000000000008",
			AttemptNo:     1,
			LeaseOwner:    "draft-stream-auth-test-worker",
		},
		Generation: 1,
		Status:     agentapplication.DraftStreamCompleted,
		NextSeq:    2,
		TotalBytes: len(draftRouteContent),
		CreatedAt:  now,
		UpdatedAt:  now,
		ExpiresAt:  now.Add(time.Minute),
	}
	return &routeBoundDraftReader{
		session: session,
		chunk: agentapplication.DraftStreamChunk{
			SessionID:  session.ID,
			Generation: session.Generation,
			Sequence:   1,
			Content:    draftRouteContent,
			CreatedAt:  now,
		},
	}
}

func (reader *routeBoundDraftReader) ReadDraftStream(_ context.Context, query agentapplication.DraftStreamReadQuery) (agentapplication.DraftStreamReadResult, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.queries = append(reader.queries, query)
	if query.WorkspaceID != reader.session.Binding.WorkspaceID || query.AnswerID != reader.session.Binding.AnswerID {
		return agentapplication.DraftStreamReadResult{}, nil
	}
	session := reader.session
	return agentapplication.DraftStreamReadResult{Session: &session, Chunks: []agentapplication.DraftStreamChunk{reader.chunk}}, nil
}

func (reader *routeBoundDraftReader) callCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return len(reader.queries)
}

func (reader *routeBoundDraftReader) queriesSnapshot() []agentapplication.DraftStreamReadQuery {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return append([]agentapplication.DraftStreamReadQuery(nil), reader.queries...)
}
