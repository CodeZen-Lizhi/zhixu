package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapplication "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	graphhttp "github.com/CodeZen-Lizhi/zhixu/internal/graph/http"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

type fakePinger struct {
	err error
}

type routerGraphService struct{}

type routerCandidateService struct{}
type routerCandidateScanService struct{}

func (routerCandidateService) List(context.Context, graphapplication.CandidateListRequest) (graphdomain.SemanticLinkCandidatePage, error) {
	return graphdomain.SemanticLinkCandidatePage{}, nil
}

func (routerCandidateService) Get(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkCandidate, error) {
	return graphdomain.SemanticLinkCandidate{}, nil
}

func (routerCandidateService) Decide(context.Context, graphapplication.SemanticLinkCandidateDecisionCommand) (graphapplication.CandidateDecisionReceipt, error) {
	return graphapplication.CandidateDecisionReceipt{}, nil
}

func (routerCandidateScanService) StartTopicScan(context.Context, graphapplication.SemanticLinkTopicScanRequest) (graphapplication.SemanticLinkScanStartResult, error) {
	return graphapplication.SemanticLinkScanStartResult{}, nil
}

func (routerCandidateScanService) Get(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkScan, error) {
	return graphdomain.SemanticLinkScan{}, nil
}

func (routerGraphService) GlobalPage(context.Context, graphapplication.GlobalPageRequest) (graphdomain.GlobalPage, error) {
	return graphdomain.GlobalPage{}, nil
}

func (routerGraphService) SearchNodes(context.Context, graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	return graphdomain.NodeSearchResult{}, nil
}

func (routerGraphService) NeighborhoodPage(context.Context, graphapplication.NeighborhoodPageRequest) (graphdomain.Neighborhood, error) {
	return graphdomain.Neighborhood{}, nil
}

func (routerGraphService) FindPath(context.Context, graphdomain.PathRequest) (graphdomain.PathResult, error) {
	return graphdomain.PathResult{}, nil
}

func (routerGraphService) NodeDetail(context.Context, foundation.ID, knowledge.NodeRef) (graphdomain.GraphNode, error) {
	return graphdomain.GraphNode{}, nil
}

func (routerGraphService) RelationDetail(context.Context, foundation.ID, foundation.ID) (graphdomain.RelationDetail, error) {
	return graphdomain.RelationDetail{}, nil
}

func (routerGraphService) RelationEvidencePage(context.Context, graphapplication.RelationEvidencePageRequest) (graphdomain.RelationEvidencePage, error) {
	return graphdomain.RelationEvidencePage{}, nil
}

func readyGraphHandler() *graphhttp.Handler {
	return graphhttp.NewHandler(routerGraphService{}, time.Second)
}

func readyCandidateHandler() *graphhttp.CandidateHandler {
	return graphhttp.NewCandidateHandler(routerCandidateService{}, time.Second, routerCandidateScanService{})
}

func TestRouterRegistersRetrievalRoutes(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Retrieval: retrievalhttp.NewHandler(nil, nil, nil)})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(`{"workspace_id":"92000000-0000-4000-8000-000000000001","query":"q"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "RETRIEVAL_SEARCH_SERVICE_UNAVAILABLE") {
		t.Fatalf("retrieval route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterRegistersConversationRoutes(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Conversation: conversationhttp.NewHandler(nil, nil)})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/conversations?workspace_id=92000000-0000-4000-8000-000000000001", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "CONVERSATION_HTTP_UNAVAILABLE") {
		t.Fatalf("conversation route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterRegistersEventsRoutes(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Events: eventshttp.NewHandler(nil)})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events?workspace_id=92000000-0000-4000-8000-000000000001", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "SSE_SERVICE_UNAVAILABLE") {
		t.Fatalf("events route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterRegistersGraphRoutesAndFailsClosedWithoutService(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Graph: graphhttp.NewHandler(nil, time.Second)})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/graph/global", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "GRAPH_DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("graph route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterGraphMethodNotAllowedReturnsProblem(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Graph: graphhttp.NewHandler(nil, time.Second)})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/graph/global", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || !strings.Contains(response.Body.String(), `"error_code":"METHOD_NOT_ALLOWED"`) {
		t.Fatalf("graph method status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterRegistersSemanticLinkRoutesAndFailsClosedWithoutService(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Candidate: graphhttp.NewCandidateHandler(nil, time.Second)})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/graph/candidates?workspace_id=92000000-0000-4000-8000-000000000001", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "SEMANTIC_LINK_DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("semantic link route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterSemanticLinkFailureDoesNotAffectReadiness(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "test", Database: fakePinger{}, Graph: readyGraphHandler(),
		Candidate: graphhttp.NewCandidateHandler(nil, time.Second),
	})
	readyRequest := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyResponse := httptest.NewRecorder()
	router.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != http.StatusOK {
		t.Fatalf("readyz status=%d body=%s", readyResponse.Code, readyResponse.Body.String())
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"semantic_links":{"reason":"semantic_link_dependencies_unavailable","status":"unavailable"}`) {
		t.Fatalf("system status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
}

func TestRouterMissingGraphHandlerRetainsRoutesAndFailsReadiness(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Database: fakePinger{}})

	readyRequest := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyResponse := httptest.NewRecorder()
	router.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != http.StatusServiceUnavailable || !strings.Contains(readyResponse.Body.String(), "graph_dependencies_unavailable") {
		t.Fatalf("readyz status=%d body=%s", readyResponse.Code, readyResponse.Body.String())
	}

	graphRequest := httptest.NewRequest(http.MethodPost, "/api/v1/graph/global", nil)
	graphResponse := httptest.NewRecorder()
	router.ServeHTTP(graphResponse, graphRequest)
	if graphResponse.Code != http.StatusServiceUnavailable || !strings.Contains(graphResponse.Body.String(), "GRAPH_DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("graph status=%d body=%s", graphResponse.Code, graphResponse.Body.String())
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"graph":{"reason":"graph_dependencies_unavailable","status":"unavailable"}`) {
		t.Fatalf("system status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
}

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestRouterLivezDoesNotNeedDatabase(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Database: fakePinger{err: errors.New("down")}})
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("livez status = %d", res.Code)
	}
}

func TestRouterReadyzFailsWhenDatabaseUnavailable(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Database: fakePinger{err: errors.New("down")}, PingTimeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d", res.Code)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
}

func TestRouterReadyzFailsClosedOnlyWhenEnabledRAGIsUnavailable(t *testing.T) {
	for _, test := range []struct {
		name       string
		ragEnabled bool
		wantStatus int
	}{
		{name: "disabled remains ready", wantStatus: http.StatusOK},
		{name: "enabled fails closed", ragEnabled: true, wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(Dependencies{
				Version: "test", Database: fakePinger{}, Graph: readyGraphHandler(), RAGEnabled: test.ragEnabled,
				RAGInitErr: errors.New("private rag composition detail"),
			})
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("readyz status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private rag composition detail") {
				t.Fatalf("readiness leaked composition detail: %s", response.Body.String())
			}
			if test.ragEnabled && !strings.Contains(response.Body.String(), "rag_dependencies_unavailable") {
				t.Fatalf("readiness omitted stable RAG reason: %s", response.Body.String())
			}
		})
	}
}

func TestRouterReadyzFailsClosedWhenGraphCompositionIsUnavailable(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "test", Database: fakePinger{}, Graph: graphhttp.NewHandler(nil, time.Second),
	})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "graph_dependencies_unavailable") {
		t.Fatalf("readyz status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterReadyzReportsMissingDatabaseConfiguration(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", DatabaseConfigErr: errors.New("configuration missing")})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), "database_not_configured") {
		t.Fatalf("unexpected readiness response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterSystemStatusReturnsDegradedButOK(t *testing.T) {
	router := NewRouter(Dependencies{Version: "v-test", Database: fakePinger{err: errors.New("down")}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d", res.Code)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"status":"degraded"`, `"version":"v-test"`, `"status":"unavailable"`, `"request_id"`} {
		if !strings.Contains(string(body), fragment) {
			t.Fatalf("body missing %s: %s", fragment, body)
		}
	}
}

func TestRouterSystemStatusReady(t *testing.T) {
	router := NewRouter(Dependencies{Version: "v-test", Database: fakePinger{}, Graph: readyGraphHandler(), Candidate: readyCandidateHandler()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"status":"ready"`) || !strings.Contains(res.Body.String(), `"graph":{"status":"ready"}`) ||
		!strings.Contains(res.Body.String(), `"semantic_links":{"status":"ready"}`) {
		t.Fatalf("body = %s", res.Body.String())
	}
}

func TestRouterSystemStatusReportsMissingSemanticLinkScanDependency(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "v-test", Database: fakePinger{}, Graph: readyGraphHandler(),
		Candidate: graphhttp.NewCandidateHandler(routerCandidateService{}, time.Second),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"graph":{"status":"ready"}`) ||
		!strings.Contains(response.Body.String(), `"semantic_links":{"reason":"semantic_link_dependencies_unavailable","status":"unavailable"}`) {
		t.Fatalf("system status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterSystemStatusReportsEnabledRAGCompositionFailure(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "v-test", Database: fakePinger{}, Graph: readyGraphHandler(), RAGEnabled: true,
		RAGInitErr: errors.New("private dependency failure"),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"degraded"`) ||
		!strings.Contains(response.Body.String(), `"reason":"rag_dependencies_unavailable"`) {
		t.Fatalf("system status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private dependency failure") {
		t.Fatalf("system status leaked dependency detail: %s", response.Body.String())
	}
}

func TestRouterSystemStatusReportsGraphCompositionFailure(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "v-test", Database: fakePinger{}, Graph: graphhttp.NewHandler(nil, time.Second),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"degraded"`) ||
		!strings.Contains(response.Body.String(), `"graph":{"reason":"graph_dependencies_unavailable","status":"unavailable"}`) {
		t.Fatalf("system status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterUnknownAPIReturnsProblem(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), `"error_code":"NOT_FOUND"`) {
		t.Fatalf("unexpected response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterMethodNotAllowedReturnsProblem(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/status", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed || !strings.Contains(res.Body.String(), `"error_code":"METHOD_NOT_ALLOWED"`) {
		t.Fatalf("unexpected response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterInvalidDatabaseConfigurationIsNotRetryable(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", DatabaseConfigErr: errors.New("database_url is invalid")})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), `"retryable":false`) || !strings.Contains(res.Body.String(), "database_configuration_invalid") {
		t.Fatalf("unexpected response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterPropagatesIncomingTraceToHandlers(t *testing.T) {
	var output bytes.Buffer
	tracer := observability.NewMemoryTracer()
	router := NewRouter(Dependencies{
		Version: "test",
		Tracer:  tracer,
		Logger:  observability.NewLogger("info", &output),
	})
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	req.Header.Set("X-Request-ID", "request-trace-test")
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d", res.Code)
	}
	traceParent := res.Header().Get("traceparent")
	if !strings.HasPrefix(traceParent, "00-0123456789abcdef0123456789abcdef-") || strings.Contains(traceParent, "-0123456789abcdef-") {
		t.Fatalf("traceparent=%q", traceParent)
	}
	spans := tracer.Snapshot()
	if len(spans) != 1 || spans[0].TraceContext.TraceID != "0123456789abcdef0123456789abcdef" || spans[0].Attributes["request_id"] == "" {
		t.Fatalf("spans=%+v", spans)
	}
	entry := make(map[string]any)
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode request log: %v\n%s", err, output.String())
	}
	if entry["trace_id"] != "0123456789abcdef0123456789abcdef" || entry["request_id"] != "request-trace-test" {
		t.Fatalf("request log correlation=%+v", entry)
	}
}
