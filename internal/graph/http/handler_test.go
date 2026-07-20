package graphhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	graphHandlerTestWorkspaceID foundation.ID = "92000000-0000-4000-8000-000000000001"
	graphHandlerTopicID1        foundation.ID = "92000000-0000-4000-8000-000000000002"
	graphHandlerTopicID2        foundation.ID = "92000000-0000-4000-8000-000000000003"
	graphHandlerClaimID1        foundation.ID = "92000000-0000-4000-8000-000000000004"
	graphHandlerClaimID2        foundation.ID = "92000000-0000-4000-8000-000000000005"
	graphHandlerRelationID1     foundation.ID = "92000000-0000-4000-8000-000000000006"
	graphHandlerRelationID2     foundation.ID = "92000000-0000-4000-8000-000000000007"
	graphHandlerEvidenceID1     foundation.ID = "92000000-0000-4000-8000-000000000008"
	graphHandlerSourceVersionID foundation.ID = "92000000-0000-4000-8000-000000000009"
	graphHandlerSourceSpanID    foundation.ID = "92000000-0000-4000-8000-000000000010"
)

var graphHandlerNow = time.Date(2026, time.July, 20, 9, 30, 0, 0, time.UTC)

func TestHandleGlobalUsesDefaultsAndMapsResponse(t *testing.T) {
	t.Parallel()

	service := &fakeGraphService{
		globalResult: graphdomain.GlobalPage{
			WorkspaceID: graphHandlerTestWorkspaceID,
			Clusters: []graphdomain.GlobalCluster{
				{
					Topic:                 graphTopicNode(graphHandlerTopicID1, "图谱主题", graphHandlerNow),
					DirectClaimCount:      2,
					IncidentRelationCount: 3,
					ClusterScore:          5,
					UpdatedAt:             graphHandlerNow,
				},
			},
			Meta: graphdomain.PageMeta{
				Fingerprint: strings.Repeat("a", 64),
				NextCursor:  "cursor-1",
				Complete:    false,
				Truncated:   true,
				Reason:      "narrow_filter",
			},
		},
	}

	response := serveGraphRequest(t, service, 1500*time.Millisecond, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`"}`), "Application/JSON; Charset=UTF-8")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.globalCalls != 1 {
		t.Fatalf("global calls = %d, want 1", service.globalCalls)
	}
	if service.globalRequest.Cursor != "" || service.globalRequest.Request.Limit != graphdomain.DefaultLimit || service.globalRequest.Request.WorkspaceID != graphHandlerTestWorkspaceID {
		t.Fatalf("global request = %#v", service.globalRequest)
	}
	if service.globalDeadline.IsZero() {
		t.Fatal("global request missing deadline")
	}
	value := decodeGraphBody[globalResponse](t, response)
	if value.WorkspaceID != string(graphHandlerTestWorkspaceID) || len(value.Clusters) != 1 {
		t.Fatalf("global response = %#v", value)
	}
	cluster := value.Clusters[0]
	if cluster.Topic.Type != string(knowledge.NodeTypeTopic) || cluster.Topic.ID != string(graphHandlerTopicID1) || cluster.Topic.Name != "图谱主题" || cluster.Topic.TopicStatus != string(knowledge.TopicStatusActive) {
		t.Fatalf("cluster topic = %#v", cluster.Topic)
	}
	if cluster.DirectClaimCount != 2 || cluster.IncidentRelationCount != 3 || cluster.ClusterScore != 5 || cluster.UpdatedAt != "2026-07-20T09:30:00Z" {
		t.Fatalf("cluster = %#v", cluster)
	}
	if value.Meta.Fingerprint != strings.Repeat("a", 64) || value.Meta.NextCursor != "cursor-1" || value.Meta.Complete || !value.Meta.Truncated || value.Meta.Reason != "narrow_filter" {
		t.Fatalf("meta = %#v", value.Meta)
	}
}

func TestHandleGlobalRejectsStrictJSONViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body []byte
		code string
		path string
	}{
		{
			name: "unknown field",
			body: []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `","unknown":true}`),
			code: "INVALID_JSON",
			path: "/api/v1/graph/global",
		},
		{
			name: "nested unknown field",
			body: []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `","center":{"type":"TOPIC","id":"` + string(graphHandlerTopicID1) + `","extra":true}}`),
			code: "INVALID_JSON",
			path: "/api/v1/graph/neighborhood",
		},
		{
			name: "duplicate key",
			body: []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `","workspace_id":"` + string(graphHandlerTestWorkspaceID) + `"}`),
			code: "INVALID_JSON",
			path: "/api/v1/graph/global",
		},
		{
			name: "null field",
			body: []byte(`{"workspace_id":null}`),
			code: graphdomain.ErrorCodeRequestInvalid,
			path: "/api/v1/graph/global",
		},
		{
			name: "multiple json values",
			body: []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `"}` + "\n{}"),
			code: "INVALID_JSON",
			path: "/api/v1/graph/global",
		},
		{
			name: "invalid utf8",
			body: append([]byte(`{"workspace_id":"`), 0xff, '"', '}'),
			code: "INVALID_JSON",
			path: "/api/v1/graph/global",
		},
		{
			name: "invalid surrogate",
			body: []byte(`{"workspace_id":"\uD800"}`),
			code: "INVALID_JSON",
			path: "/api/v1/graph/global",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeGraphService{}
			response := serveGraphRequest(t, service, time.Second, http.MethodPost, test.path, test.body, "application/json")
			requireGraphProblem(t, response, http.StatusBadRequest, test.code, false)
			if service.globalCalls != 0 || service.neighborhoodCalls != 0 {
				t.Fatalf("service should not be called: global=%d neighborhood=%d", service.globalCalls, service.neighborhoodCalls)
			}
		})
	}
}

func TestHandleGlobalEnforcesContentTypeAndBodyLimit(t *testing.T) {
	t.Parallel()

	response := serveGraphRequest(t, &fakeGraphService{}, time.Second, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`"}`), "text/plain")
	requireGraphProblem(t, response, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", false)

	base := `{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `"}`
	okBody := []byte(base + strings.Repeat(" ", maxGraphRequestBytes-len(base)))
	service := &fakeGraphService{
		globalResult: graphdomain.GlobalPage{
			WorkspaceID: graphHandlerTestWorkspaceID,
			Meta:        graphdomain.PageMeta{Fingerprint: strings.Repeat("b", 64), Complete: true},
		},
	}
	okResponse := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/global", okBody, "application/json")
	if okResponse.Code != http.StatusOK || service.globalCalls != 1 {
		t.Fatalf("exact limit response = %d, calls = %d, body = %s", okResponse.Code, service.globalCalls, okResponse.Body.String())
	}

	tooLarge := append(okBody, ' ')
	tooLargeResponse := serveGraphRequest(t, &fakeGraphService{}, time.Second, http.MethodPost, "/api/v1/graph/global", tooLarge, "application/json")
	requireGraphProblem(t, tooLargeResponse, http.StatusBadRequest, "INVALID_JSON", false)
}

func TestHandleGlobalMapsCursorInvalidAndStaleProblems(t *testing.T) {
	t.Parallel()

	port := &fakeGraphPort{
		globalWindow: graphapp.GlobalResultWindow{
			Items: []graphdomain.GlobalCluster{
				{
					Topic:                 graphTopicNode(graphHandlerTopicID1, "主题 A", graphHandlerNow),
					DirectClaimCount:      3,
					IncidentRelationCount: 2,
					ClusterScore:          5,
					UpdatedAt:             graphHandlerNow,
				},
				{
					Topic:                 graphTopicNode(graphHandlerTopicID2, "主题 B", graphHandlerNow.Add(-time.Minute)),
					DirectClaimCount:      1,
					IncidentRelationCount: 1,
					ClusterScore:          2,
					UpdatedAt:             graphHandlerNow.Add(-time.Minute),
				},
			},
		},
	}
	service := newGraphApplicationService(t, port)
	first := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`","limit":1}`), "application/json")
	if first.Code != http.StatusOK {
		t.Fatalf("first response = %d %s", first.Code, first.Body.String())
	}
	firstPage := decodeGraphBody[globalResponse](t, first)
	if firstPage.Meta.NextCursor == "" {
		t.Fatalf("first page = %#v", firstPage)
	}

	invalid := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`","limit":1,"cursor":"`+firstPage.Meta.NextCursor+`x"}`), "application/json")
	requireGraphProblem(t, invalid, http.StatusBadRequest, graphdomain.ErrorCodeCursorInvalid, false)

	port.globalWindow.Items[1].UpdatedAt = graphHandlerNow.Add(time.Minute)
	stale := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`","limit":1,"cursor":"`+firstPage.Meta.NextCursor+`"}`), "application/json")
	requireGraphProblem(t, stale, http.StatusConflict, graphdomain.ErrorCodeCursorStale, false)
}

func TestHandleNeighborhoodRejectsCursorForDepthGreaterThanOne(t *testing.T) {
	t.Parallel()

	service := &fakeGraphService{}
	response := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/neighborhood", []byte(`{
		"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`",
		"center":{"type":"TOPIC","id":"`+string(graphHandlerTopicID1)+`"},
		"depth":2,
		"cursor":"opaque"
	}`), "application/json")
	requireGraphProblem(t, response, http.StatusBadRequest, graphdomain.ErrorCodeRequestInvalid, false)
	if service.neighborhoodCalls != 0 {
		t.Fatalf("neighborhood calls = %d, want 0", service.neighborhoodCalls)
	}
}

func TestHandleNeighborhoodMapsEmptySlicesAndDefaults(t *testing.T) {
	t.Parallel()

	service := &fakeGraphService{
		neighborhoodResult: graphdomain.Neighborhood{
			WorkspaceID:    graphHandlerTestWorkspaceID,
			Center:         knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: graphHandlerTopicID1},
			Nodes:          []graphdomain.GraphNode{{Topic: pointer(graphTopicNode(graphHandlerTopicID1, "中心主题", graphHandlerNow))}},
			LayerCounts:    []int{0},
			CompletedDepth: 1,
			Meta:           graphdomain.PageMeta{Fingerprint: strings.Repeat("c", 64), Complete: true},
		},
	}
	response := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/neighborhood", []byte(`{
		"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`",
		"center":{"type":"TOPIC","id":"`+string(graphHandlerTopicID1)+`"}
	}`), "application/json")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.neighborhoodCalls != 1 {
		t.Fatalf("neighborhood calls = %d", service.neighborhoodCalls)
	}
	if got := service.neighborhoodRequest; got.Cursor != "" || got.Request.Depth != 1 || got.Request.Limit != graphdomain.DefaultLimit || got.Request.Direction != graphdomain.TraversalBoth || got.Request.MaxNodes != graphdomain.MaxNodes || got.Request.MaxEdges != graphdomain.MaxEdges || got.Request.MaxFrontier != graphdomain.MaxNodes {
		t.Fatalf("neighborhood request = %#v", got)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"edges":[]`) || !strings.Contains(body, `"boundary_nodes":[]`) {
		t.Fatalf("neighborhood body = %s", body)
	}
}

func TestHandleNodeSearchRejectsUnknownOrDuplicateParametersAndUsesDefaultLimit(t *testing.T) {
	t.Parallel()

	router := graphTestRouter(&fakeGraphService{}, time.Second)
	unknown := httptest.NewRecorder()
	router.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/api/v1/graph/nodes?workspace_id="+string(graphHandlerTestWorkspaceID)+"&query=主题&extra=1", nil))
	requireGraphProblem(t, unknown, http.StatusBadRequest, graphdomain.ErrorCodeRequestInvalid, false)

	duplicate := httptest.NewRecorder()
	router.ServeHTTP(duplicate, httptest.NewRequest(http.MethodGet, "/api/v1/graph/nodes?workspace_id="+string(graphHandlerTestWorkspaceID)+"&query=主题&query=重复", nil))
	requireGraphProblem(t, duplicate, http.StatusBadRequest, graphdomain.ErrorCodeRequestInvalid, false)

	applicability := mustApplicability(t, `{"region":"cn"}`)
	confidence := 0.91
	service := &fakeGraphService{
		searchResult: graphdomain.NodeSearchResult{
			WorkspaceID: graphHandlerTestWorkspaceID,
			Matches: []graphdomain.NodeSearchMatch{
				{Kind: graphdomain.NodeSearchExact, Node: graphdomain.GraphNode{Topic: pointer(graphTopicNode(graphHandlerTopicID1, "图谱主题", graphHandlerNow))}},
				{Kind: graphdomain.NodeSearchPrefix, Node: graphdomain.GraphNode{Claim: pointer(graphClaimNode(graphHandlerClaimID1, "图谱支持结论", &confidence, applicability, graphHandlerNow.Add(time.Second)))}},
			},
		},
	}
	response := serveGraphRequest(t, service, time.Second, http.MethodGet, "/api/v1/graph/nodes?workspace_id="+string(graphHandlerTestWorkspaceID)+"&query=图谱", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.searchCalls != 1 || service.searchRequest.Limit != 20 || service.searchRequest.Query != "图谱" {
		t.Fatalf("search request = %#v, calls = %d", service.searchRequest, service.searchCalls)
	}
	var value struct {
		WorkspaceID string `json:"workspace_id"`
		Matches     []struct {
			Kind string         `json:"kind"`
			Node map[string]any `json:"node"`
		} `json:"matches"`
	}
	decodeGraphBodyInto(t, response, &value)
	if value.WorkspaceID != string(graphHandlerTestWorkspaceID) || len(value.Matches) != 2 {
		t.Fatalf("node search response = %#v", value)
	}
	topic := value.Matches[0]
	if topic.Kind != string(graphdomain.NodeSearchExact) || topic.Node["type"] != string(knowledge.NodeTypeTopic) || topic.Node["id"] != string(graphHandlerTopicID1) || topic.Node["name"] != "图谱主题" || topic.Node["topic_status"] != string(knowledge.TopicStatusActive) {
		t.Fatalf("topic match = %#v", topic)
	}
	if _, ok := topic.Node["statement"]; ok {
		t.Fatalf("topic match leaked claim fields: %#v", topic.Node)
	}
	claim := value.Matches[1]
	if claim.Kind != string(graphdomain.NodeSearchPrefix) || claim.Node["type"] != string(knowledge.NodeTypeClaim) || claim.Node["id"] != string(graphHandlerClaimID1) || claim.Node["statement"] != "图谱支持结论" || claim.Node["claim_status"] != string(knowledge.ClaimStatusConfirmed) {
		t.Fatalf("claim match = %#v", claim)
	}
	if _, ok := claim.Node["name"]; ok {
		t.Fatalf("claim match leaked topic fields: %#v", claim.Node)
	}
	if got, ok := claim.Node["confidence"].(float64); !ok || got != confidence {
		t.Fatalf("claim confidence = %#v", claim.Node["confidence"])
	}
	appValue, ok := claim.Node["applicability"].(map[string]any)
	if !ok || appValue["hash"] != applicability.Hash {
		t.Fatalf("claim applicability = %#v", claim.Node["applicability"])
	}
}

func TestHandleRejectsExplicitEmptyAndMalformedOptionalParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		path        string
		body        []byte
		contentType string
	}{
		{
			name:        "empty post cursor",
			method:      http.MethodPost,
			path:        "/api/v1/graph/global",
			body:        []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `","cursor":""}`),
			contentType: "application/json",
		},
		{
			name:   "empty search limit",
			method: http.MethodGet,
			path:   "/api/v1/graph/nodes?workspace_id=" + string(graphHandlerTestWorkspaceID) + "&query=graph&limit=",
		},
		{
			name:   "empty evidence cursor",
			method: http.MethodGet,
			path:   "/api/v1/graph/relations/" + string(graphHandlerRelationID1) + "/evidence?workspace_id=" + string(graphHandlerTestWorkspaceID) + "&cursor=",
		},
		{
			name:   "malformed query",
			method: http.MethodGet,
			path:   "/api/v1/graph/nodes?workspace_id=" + string(graphHandlerTestWorkspaceID) + "&query=graph&limit=20;extra=1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeGraphService{}
			response := serveGraphRequest(t, service, time.Second, test.method, test.path, test.body, test.contentType)
			requireGraphProblem(t, response, http.StatusBadRequest, graphdomain.ErrorCodeRequestInvalid, false)
			if service.globalCalls+service.searchCalls+service.relationEvidenceCalls != 0 {
				t.Fatalf("service was called: %#v", service)
			}
		})
	}
}

func TestHandlePathMapsDiscriminatedNodesAndTraceableEdge(t *testing.T) {
	t.Parallel()

	applicability := mustApplicability(t, `{"audience":"engineers"}`)
	confidence := 0.93
	from := knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: graphHandlerClaimID1}
	to := knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: graphHandlerTopicID1}
	service := &fakeGraphService{pathResult: graphdomain.PathResult{
		WorkspaceID: graphHandlerTestWorkspaceID,
		From:        from,
		To:          to,
		Status:      graphdomain.PathFound,
		Nodes: []graphdomain.GraphNode{
			{Claim: pointer(graphClaimNode(graphHandlerClaimID1, "Graph paths are bounded", &confidence, applicability, graphHandlerNow))},
			{Topic: pointer(graphTopicNode(graphHandlerTopicID1, "Graph", graphHandlerNow))},
		},
		Edges:         []graphdomain.GraphEdge{graphEdge(graphHandlerRelationID1, from, to, graphHandlerNow)},
		HopCount:      1,
		ExploredNodes: 2,
	}}
	response := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/path", []byte(`{
		"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`",
		"from":{"type":"CLAIM","id":"`+string(graphHandlerClaimID1)+`"},
		"to":{"type":"TOPIC","id":"`+string(graphHandlerTopicID1)+`"}
	}`), "application/json")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var value struct {
		Status                 string           `json:"status"`
		Nodes                  []map[string]any `json:"nodes"`
		Edges                  []edgeResponse   `json:"edges"`
		HopCount               int              `json:"hop_count"`
		ExploredNodes          int              `json:"explored_nodes"`
		CommonTopicSuggestions json.RawMessage  `json:"common_topic_suggestions"`
	}
	decodeGraphBodyInto(t, response, &value)
	if value.Status != string(graphdomain.PathFound) || value.HopCount != 1 || value.ExploredNodes != 2 || len(value.Nodes) != 2 || len(value.Edges) != 1 {
		t.Fatalf("path response = %#v", value)
	}
	if value.Nodes[0]["type"] != string(knowledge.NodeTypeClaim) || value.Nodes[1]["type"] != string(knowledge.NodeTypeTopic) {
		t.Fatalf("path nodes = %#v", value.Nodes)
	}
	if _, ok := value.Nodes[0]["name"]; ok {
		t.Fatalf("claim node leaked topic fields: %#v", value.Nodes[0])
	}
	if _, ok := value.Nodes[1]["statement"]; ok {
		t.Fatalf("topic node leaked claim fields: %#v", value.Nodes[1])
	}
	if value.Edges[0].EvidenceHref != graphEdge(graphHandlerRelationID1, from, to, graphHandlerNow).EvidenceHref || !bytes.Equal(value.CommonTopicSuggestions, []byte("[]")) {
		t.Fatalf("path traceability or empty suggestions drifted: edge=%#v suggestions=%s", value.Edges[0], value.CommonTopicSuggestions)
	}
}

func TestHandleNodeDetailMapsNullableClaimConfidence(t *testing.T) {
	t.Parallel()

	applicability := mustApplicability(t, `{"scope":"all"}`)
	service := &fakeGraphService{nodeDetailResult: graphdomain.GraphNode{Claim: pointer(graphClaimNode(
		graphHandlerClaimID1, "A claim without a numeric confidence remains explicit.", nil, applicability, graphHandlerNow,
	))}}
	response := serveGraphRequest(t, service, time.Second, http.MethodGet, "/api/v1/graph/nodes/CLAIM/"+string(graphHandlerClaimID1)+"?workspace_id="+string(graphHandlerTestWorkspaceID), nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var value map[string]any
	decodeGraphBodyInto(t, response, &value)
	confidence, present := value["confidence"]
	if service.nodeDetailCalls != 1 || service.nodeDetailWorkspaceID != graphHandlerTestWorkspaceID || service.nodeDetailRef != (knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: graphHandlerClaimID1}) {
		t.Fatalf("node detail scope = calls:%d workspace:%q ref:%#v", service.nodeDetailCalls, service.nodeDetailWorkspaceID, service.nodeDetailRef)
	}
	if value["type"] != string(knowledge.NodeTypeClaim) || !present || confidence != nil || value["claim_status"] != string(knowledge.ClaimStatusConfirmed) {
		t.Fatalf("claim detail = %#v", value)
	}
	if _, ok := value["name"]; ok {
		t.Fatalf("claim detail leaked Topic fields: %#v", value)
	}
}

func TestHandleErrorMappings(t *testing.T) {
	t.Parallel()

	t.Run("not found", func(t *testing.T) {
		service := &fakeGraphService{
			nodeDetailErr: classifiedGraphError(foundation.ErrorNotFound, graphdomain.ErrorCodeNodeNotFound, false, "secret not found"),
		}
		response := serveGraphRequest(t, service, time.Second, http.MethodGet, "/api/v1/graph/nodes/TOPIC/"+string(graphHandlerTopicID1)+"?workspace_id="+string(graphHandlerTestWorkspaceID), nil, "")
		problem := requireGraphProblem(t, response, http.StatusNotFound, graphdomain.ErrorCodeNodeNotFound, false)
		if strings.Contains(response.Body.String(), "secret not found") || !strings.Contains(problem.Message, graphdomain.ErrorCodeNodeNotFound) {
			t.Fatalf("problem = %#v, body = %s", problem, response.Body.String())
		}
	})

	t.Run("timeout", func(t *testing.T) {
		service := &fakeGraphService{globalErr: context.DeadlineExceeded}
		response := serveGraphRequest(t, service, 5*time.Millisecond, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`"}`), "application/json")
		requireGraphProblem(t, response, http.StatusServiceUnavailable, graphdomain.ErrorCodeQueryTimeout, true)
	})

	t.Run("cancel", func(t *testing.T) {
		service := &fakeGraphService{globalErr: context.Canceled}
		response := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`"}`), "application/json")
		requireGraphProblem(t, response, http.StatusServiceUnavailable, graphdomain.ErrorCodeQueryCanceled, false)
	})

	t.Run("postgres explicit cancel", func(t *testing.T) {
		service := &fakeGraphService{globalErr: foundation.NewError(
			foundation.ErrorNonRetryableFailure,
			graphdomain.ErrorCodeQueryCanceled,
			false,
			&pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"},
		)}
		response := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/global", []byte(`{"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`"}`), "application/json")
		requireGraphProblem(t, response, http.StatusServiceUnavailable, graphdomain.ErrorCodeQueryCanceled, false)
	})

	t.Run("budget", func(t *testing.T) {
		service := &fakeGraphService{
			pathErr: classifiedGraphError(foundation.ErrorNonRetryableFailure, graphdomain.ErrorCodeQueryBudgetExceeded, false, "budget exceeded"),
		}
		response := serveGraphRequest(t, service, time.Second, http.MethodPost, "/api/v1/graph/path", []byte(`{
			"workspace_id":"`+string(graphHandlerTestWorkspaceID)+`",
			"from":{"type":"TOPIC","id":"`+string(graphHandlerTopicID1)+`"},
			"to":{"type":"CLAIM","id":"`+string(graphHandlerClaimID1)+`"}
		}`), "application/json")
		requireGraphProblem(t, response, http.StatusUnprocessableEntity, graphdomain.ErrorCodeQueryBudgetExceeded, false)
	})

	t.Run("unknown", func(t *testing.T) {
		service := &fakeGraphService{relationDetailErr: errors.New("secret unknown failure")}
		response := serveGraphRequest(t, service, time.Second, http.MethodGet, "/api/v1/graph/relations/"+string(graphHandlerRelationID1)+"?workspace_id="+string(graphHandlerTestWorkspaceID), nil, "")
		problem := requireGraphProblem(t, response, http.StatusInternalServerError, "INTERNAL_ERROR", false)
		if strings.Contains(response.Body.String(), "secret unknown failure") || !strings.Contains(problem.Message, "Graph 查询失败") {
			t.Fatalf("problem = %#v, body = %s", problem, response.Body.String())
		}
	})
}

func TestHandleNilServiceRoutesReturnDependencyUnavailable(t *testing.T) {
	t.Parallel()

	router := graphTestRouter(nil, time.Second)
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        []byte
	}{
		{
			name:        "global",
			method:      http.MethodPost,
			path:        "/api/v1/graph/global",
			contentType: "application/json",
			body:        []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `"}`),
		},
		{
			name:        "neighborhood",
			method:      http.MethodPost,
			path:        "/api/v1/graph/neighborhood",
			contentType: "application/json",
			body:        []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `","center":{"type":"TOPIC","id":"` + string(graphHandlerTopicID1) + `"}}`),
		},
		{
			name:        "path",
			method:      http.MethodPost,
			path:        "/api/v1/graph/path",
			contentType: "application/json",
			body:        []byte(`{"workspace_id":"` + string(graphHandlerTestWorkspaceID) + `","from":{"type":"TOPIC","id":"` + string(graphHandlerTopicID1) + `"},"to":{"type":"CLAIM","id":"` + string(graphHandlerClaimID1) + `"}}`),
		},
		{
			name:   "nodes",
			method: http.MethodGet,
			path:   "/api/v1/graph/nodes?workspace_id=" + string(graphHandlerTestWorkspaceID) + "&query=图谱",
		},
		{
			name:   "node detail",
			method: http.MethodGet,
			path:   "/api/v1/graph/nodes/TOPIC/" + string(graphHandlerTopicID1) + "?workspace_id=" + string(graphHandlerTestWorkspaceID),
		},
		{
			name:   "relation detail",
			method: http.MethodGet,
			path:   "/api/v1/graph/relations/" + string(graphHandlerRelationID1) + "?workspace_id=" + string(graphHandlerTestWorkspaceID),
		},
		{
			name:   "relation evidence",
			method: http.MethodGet,
			path:   "/api/v1/graph/relations/" + string(graphHandlerRelationID1) + "/evidence?workspace_id=" + string(graphHandlerTestWorkspaceID),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			requireGraphProblem(t, response, http.StatusServiceUnavailable, graphdomain.ErrorCodeDependencyUnavailable, false)
		})
	}
}

func TestHandleRelationDetailAndEvidencePageContract(t *testing.T) {
	t.Parallel()

	confirmation := &knowledge.Confirmation{Method: knowledge.ConfirmationUserApproval, Reference: "approval:graph-1"}
	service := &fakeGraphService{
		relationDetailResult: graphdomain.RelationDetail{
			Edge:         graphEdge(graphHandlerRelationID1, knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: graphHandlerClaimID1}, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: graphHandlerTopicID1}, graphHandlerNow),
			Confirmation: confirmation,
			Fingerprint:  strings.Repeat("d", 64),
			CreatedAt:    graphHandlerNow.Add(-time.Hour),
			UpdatedAt:    graphHandlerNow,
		},
		relationEvidenceResult: graphdomain.RelationEvidencePage{
			WorkspaceID: graphHandlerTestWorkspaceID,
			RelationID:  graphHandlerRelationID1,
			Meta:        graphdomain.PageMeta{Fingerprint: strings.Repeat("e", 64), Complete: true},
		},
	}

	detail := serveGraphRequest(t, service, time.Second, http.MethodGet, "/api/v1/graph/relations/"+string(graphHandlerRelationID1)+"?workspace_id="+string(graphHandlerTestWorkspaceID), nil, "")
	if detail.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", detail.Code, detail.Body.String())
	}
	detailValue := decodeGraphBody[relationDetailResponse](t, detail)
	wantEvidenceHref := "/api/v1/graph/relations/" + string(graphHandlerRelationID1) + "/evidence?workspace_id=" + string(graphHandlerTestWorkspaceID)
	if detailValue.Edge.EvidenceHref != wantEvidenceHref || detailValue.Confirmation == nil || detailValue.Confirmation.Method != string(knowledge.ConfirmationUserApproval) || detailValue.Confirmation.Reference != "approval:graph-1" {
		t.Fatalf("detail response = %#v", detailValue)
	}

	evidence := serveGraphRequest(t, service, time.Second, http.MethodGet, detailValue.Edge.EvidenceHref, nil, "")
	if evidence.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", evidence.Code, evidence.Body.String())
	}
	if got := service.relationEvidenceRequest; got.WorkspaceID != graphHandlerTestWorkspaceID || got.RelationID != graphHandlerRelationID1 || got.Limit != graphdomain.DefaultEvidenceLimit || got.Cursor != "" {
		t.Fatalf("evidence request = %#v", got)
	}
	if !strings.Contains(evidence.Body.String(), `"items":[]`) {
		t.Fatalf("evidence body = %s", evidence.Body.String())
	}

	applicability := mustApplicability(t, `{"scope":"v1"}`)
	service.relationEvidenceResult.Items = []graphdomain.RelationEvidenceItem{{
		ID:          graphHandlerEvidenceID1,
		WorkspaceID: graphHandlerTestWorkspaceID,
		RelationID:  graphHandlerRelationID1,
		Provenance: knowledge.ProvenanceRef{
			WorkspaceID:     graphHandlerTestWorkspaceID,
			SourceVersionID: graphHandlerSourceVersionID,
			SourceSpanID:    graphHandlerSourceSpanID,
		},
		Reason:        "The source span supports this relation.",
		Applicability: applicability,
		Confirmation:  confirmation,
		SourceHref:    "/api/v1/workspaces/" + string(graphHandlerTestWorkspaceID) + "/source-versions/" + string(graphHandlerSourceVersionID),
		SpanHref:      "/api/v1/workspaces/" + string(graphHandlerTestWorkspaceID) + "/source-versions/" + string(graphHandlerSourceVersionID) + "/spans/" + string(graphHandlerSourceSpanID),
		CreatedAt:     graphHandlerNow,
	}}
	itemResponse := serveGraphRequest(t, service, time.Second, http.MethodGet, detailValue.Edge.EvidenceHref, nil, "")
	if itemResponse.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", itemResponse.Code, itemResponse.Body.String())
	}
	itemPage := decodeGraphBody[relationEvidenceResponse](t, itemResponse)
	if len(itemPage.Items) != 1 || itemPage.Items[0].SourceHref != service.relationEvidenceResult.Items[0].SourceHref || itemPage.Items[0].SpanHref != service.relationEvidenceResult.Items[0].SpanHref || itemPage.Items[0].Applicability.Hash != applicability.Hash {
		t.Fatalf("evidence item = %#v", itemPage)
	}
	if strings.Contains(itemResponse.Body.String(), "excerpt") || strings.Contains(itemResponse.Body.String(), "/Users/") {
		t.Fatalf("evidence response leaked body or absolute path: %s", itemResponse.Body.String())
	}
}

func TestHandleRelationIDsAreIndependentlyScoped(t *testing.T) {
	t.Parallel()

	service := &fakeGraphService{relationDetailErr: classifiedGraphError(foundation.ErrorNotFound, graphdomain.ErrorCodeRelationNotFound, false, "not found")}
	response := serveGraphRequest(t, service, time.Second, http.MethodGet, "/api/v1/graph/relations/"+string(graphHandlerTestWorkspaceID)+"?workspace_id="+string(graphHandlerTestWorkspaceID), nil, "")
	requireGraphProblem(t, response, http.StatusNotFound, graphdomain.ErrorCodeRelationNotFound, false)
	if service.relationDetailCalls != 1 || service.relationDetailWorkspaceID != graphHandlerTestWorkspaceID || service.relationDetailID != graphHandlerTestWorkspaceID {
		t.Fatalf("relation detail scope = calls:%d workspace:%q relation:%q", service.relationDetailCalls, service.relationDetailWorkspaceID, service.relationDetailID)
	}
}

func graphTestRouter(service Service, timeout time.Duration) http.Handler {
	router := chi.NewRouter()
	router.Route("/api/v1", NewHandler(service, timeout).Routes)
	return router
}

func serveGraphRequest(t *testing.T, service Service, timeout time.Duration, method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	graphTestRouter(service, timeout).ServeHTTP(response, request)
	return response
}

func decodeGraphBody[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	requireGraphJSONContentType(t, response)
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
	return value
}

func decodeGraphBodyInto(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	requireGraphJSONContentType(t, response)
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
}

func requireGraphProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string, retryable bool) httpapi.Problem {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, status, response.Body.String())
	}
	problem := decodeGraphBody[httpapi.Problem](t, response)
	if problem.ErrorCode != code || problem.Retryable != retryable {
		t.Fatalf("problem = %#v, want code=%q retryable=%t", problem, code, retryable)
	}
	return problem
}

func requireGraphJSONContentType(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func newGraphApplicationService(t *testing.T, port graphapp.QueryPort) *graphapp.Service {
	t.Helper()
	service, err := graphapp.NewService(port, mustCursorCodec(t))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func mustCursorCodec(t *testing.T) *graphapp.CursorCodec {
	t.Helper()
	codec, err := graphapp.NewCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewCursorCodec() error = %v", err)
	}
	return codec
}

func graphTopicNode(id foundation.ID, name string, updatedAt time.Time) graphdomain.TopicNode {
	return graphdomain.TopicNode{
		Ref:         knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: id},
		WorkspaceID: graphHandlerTestWorkspaceID,
		Name:        name,
		Description: "用于测试的主题",
		Status:      knowledge.TopicStatusActive,
		Version:     1,
		UpdatedAt:   updatedAt,
	}
}

func graphClaimNode(id foundation.ID, statement string, confidence *float64, applicability knowledge.Applicability, updatedAt time.Time) graphdomain.ClaimNode {
	return graphdomain.ClaimNode{
		Ref:           knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: id},
		WorkspaceID:   graphHandlerTestWorkspaceID,
		Statement:     statement,
		Status:        knowledge.ClaimStatusConfirmed,
		Confidence:    confidence,
		Applicability: applicability,
		Version:       1,
		UpdatedAt:     updatedAt,
	}
}

func graphEdge(relationID foundation.ID, source, target knowledge.NodeRef, updatedAt time.Time) graphdomain.GraphEdge {
	confidence := 0.87
	return graphdomain.GraphEdge{
		RelationID:          relationID,
		WorkspaceID:         graphHandlerTestWorkspaceID,
		Source:              source,
		Target:              target,
		Type:                knowledge.RelationBelongsTo,
		Status:              knowledge.RelationStatusConfirmed,
		Traversal:           graphdomain.EdgeTraversalForward,
		Confidence:          &confidence,
		Version:             1,
		EvidenceCount:       1,
		EvidenceFingerprint: strings.Repeat("f", 64),
		EvidenceHref:        "/api/v1/graph/relations/" + string(relationID) + "/evidence?workspace_id=" + string(graphHandlerTestWorkspaceID),
		UpdatedAt:           updatedAt,
	}
}

func mustApplicability(t *testing.T, raw string) knowledge.Applicability {
	t.Helper()
	value, err := knowledge.ParseApplicability(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseApplicability(%s) error = %v", raw, err)
	}
	return value
}

func classifiedGraphError(kind foundation.ErrorKind, code string, retryable bool, cause string) error {
	return foundation.NewError(kind, code, retryable, errors.New(cause))
}

func pointer[T any](value T) *T { return &value }

type fakeGraphService struct {
	globalResult   graphdomain.GlobalPage
	globalErr      error
	globalRequest  graphapp.GlobalPageRequest
	globalDeadline time.Time
	globalCalls    int

	searchResult  graphdomain.NodeSearchResult
	searchErr     error
	searchRequest graphdomain.NodeSearchRequest
	searchCalls   int

	neighborhoodResult   graphdomain.Neighborhood
	neighborhoodErr      error
	neighborhoodRequest  graphapp.NeighborhoodPageRequest
	neighborhoodDeadline time.Time
	neighborhoodCalls    int

	pathResult  graphdomain.PathResult
	pathErr     error
	pathRequest graphdomain.PathRequest
	pathCalls   int

	nodeDetailResult      graphdomain.GraphNode
	nodeDetailErr         error
	nodeDetailWorkspaceID foundation.ID
	nodeDetailRef         knowledge.NodeRef
	nodeDetailCalls       int

	relationDetailResult      graphdomain.RelationDetail
	relationDetailErr         error
	relationDetailWorkspaceID foundation.ID
	relationDetailID          foundation.ID
	relationDetailCalls       int

	relationEvidenceResult   graphdomain.RelationEvidencePage
	relationEvidenceErr      error
	relationEvidenceRequest  graphapp.RelationEvidencePageRequest
	relationEvidenceDeadline time.Time
	relationEvidenceCalls    int
}

func (service *fakeGraphService) GlobalPage(ctx context.Context, request graphapp.GlobalPageRequest) (graphdomain.GlobalPage, error) {
	service.globalCalls++
	service.globalRequest = request
	service.globalDeadline, _ = ctx.Deadline()
	return service.globalResult, service.globalErr
}

func (service *fakeGraphService) SearchNodes(_ context.Context, request graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	service.searchCalls++
	service.searchRequest = request
	return service.searchResult, service.searchErr
}

func (service *fakeGraphService) NeighborhoodPage(ctx context.Context, request graphapp.NeighborhoodPageRequest) (graphdomain.Neighborhood, error) {
	service.neighborhoodCalls++
	service.neighborhoodRequest = request
	service.neighborhoodDeadline, _ = ctx.Deadline()
	return service.neighborhoodResult, service.neighborhoodErr
}

func (service *fakeGraphService) FindPath(_ context.Context, request graphdomain.PathRequest) (graphdomain.PathResult, error) {
	service.pathCalls++
	service.pathRequest = request
	return service.pathResult, service.pathErr
}

func (service *fakeGraphService) NodeDetail(_ context.Context, workspaceID foundation.ID, ref knowledge.NodeRef) (graphdomain.GraphNode, error) {
	service.nodeDetailCalls++
	service.nodeDetailWorkspaceID = workspaceID
	service.nodeDetailRef = ref
	return service.nodeDetailResult, service.nodeDetailErr
}

func (service *fakeGraphService) RelationDetail(_ context.Context, workspaceID, relationID foundation.ID) (graphdomain.RelationDetail, error) {
	service.relationDetailCalls++
	service.relationDetailWorkspaceID = workspaceID
	service.relationDetailID = relationID
	return service.relationDetailResult, service.relationDetailErr
}

func (service *fakeGraphService) RelationEvidencePage(ctx context.Context, request graphapp.RelationEvidencePageRequest) (graphdomain.RelationEvidencePage, error) {
	service.relationEvidenceCalls++
	service.relationEvidenceRequest = request
	service.relationEvidenceDeadline, _ = ctx.Deadline()
	return service.relationEvidenceResult, service.relationEvidenceErr
}

type fakeGraphPort struct {
	globalWindow graphapp.GlobalResultWindow
	globalErr    error
	globalCalls  int
	lastGlobal   graphdomain.GlobalRequest
}

func (port *fakeGraphPort) GlobalWindow(_ context.Context, request graphdomain.GlobalRequest) (graphapp.GlobalResultWindow, error) {
	port.globalCalls++
	port.lastGlobal = request
	return port.globalWindow, port.globalErr
}

func (*fakeGraphPort) SearchNodes(context.Context, graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	return graphdomain.NodeSearchResult{}, nil
}

func (*fakeGraphPort) NeighborhoodWindow(context.Context, graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error) {
	return graphdomain.Neighborhood{}, nil
}

func (*fakeGraphPort) FindPath(context.Context, graphdomain.PathRequest) (graphdomain.PathResult, error) {
	return graphdomain.PathResult{}, nil
}

func (*fakeGraphPort) NodeDetail(context.Context, foundation.ID, knowledge.NodeRef) (graphdomain.GraphNode, error) {
	return graphdomain.GraphNode{}, nil
}

func (*fakeGraphPort) RelationDetail(context.Context, foundation.ID, foundation.ID) (graphdomain.RelationDetail, error) {
	return graphdomain.RelationDetail{}, nil
}

func (*fakeGraphPort) RelationEvidenceWindow(context.Context, foundation.ID, foundation.ID) (graphapp.RelationEvidenceResultWindow, error) {
	return graphapp.RelationEvidenceResultWindow{}, nil
}

var _ Service = (*fakeGraphService)(nil)
var _ graphapp.QueryPort = (*fakeGraphPort)(nil)
