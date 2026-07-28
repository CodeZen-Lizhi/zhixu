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

	authapplication "github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	authdomain "github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapplication "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	graphhttp "github.com/CodeZen-Lizhi/zhixu/internal/graph/http"
	knowledgeapplication "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	knowledgehttp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/http"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"
	reviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/http"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

type fakePinger struct {
	err error
}

type routerGraphService struct{}

type routerCandidateService struct{}
type routerCandidateScanService struct{}

type routerTimelineService struct{}
type routerImpactService struct{}

type routerAuthService struct{}

type routerScopedAuthService struct {
	routerAuthService
	principals map[string]authdomain.Principal
}

func (service routerScopedAuthService) AuthenticateAPIToken(_ context.Context, plain string) (authdomain.Principal, error) {
	principal, ok := service.principals[plain]
	if !ok {
		return authdomain.Principal{}, foundation.NewError(
			foundation.ErrorPermissionDenied,
			authapplication.ErrorCodeUnauthorized,
			false,
			errors.New("api token does not exist"),
		)
	}
	return principal, nil
}

func routerAuthScopes() []capability.Capability {
	scopes, _ := authdomain.CanonicalScopes(capability.All())
	return scopes
}

func (routerAuthService) ExchangeBootstrap(context.Context, string) (authapplication.SessionCredential, error) {
	return authapplication.SessionCredential{}, nil
}

func (routerAuthService) AuthenticateSession(context.Context, string, string, bool) (authdomain.Principal, error) {
	return authdomain.Principal{Kind: authdomain.PrincipalSession, ID: foundation.ID("92000000-0000-4000-8000-000000000001"), Scopes: routerAuthScopes()}, nil
}

func (routerAuthService) CurrentSession(context.Context, string, string, bool) (authdomain.SessionInfo, error) {
	return authdomain.SessionInfo{ID: foundation.ID("92000000-0000-4000-8000-000000000001"), UserLabel: "owner", Scopes: routerAuthScopes(), CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, nil
}

func (routerAuthService) RotateSession(context.Context, authdomain.Principal, string, string, bool) (authapplication.SessionCredential, error) {
	return authapplication.SessionCredential{}, nil
}

func (routerAuthService) AuthenticateAPIToken(context.Context, string) (authdomain.Principal, error) {
	return authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: foundation.ID("92000000-0000-4000-8000-000000000002"), Scopes: routerAuthScopes()}, nil
}

func (routerAuthService) CreateAPIToken(context.Context, authdomain.Principal, string, []capability.Capability, time.Duration) (authapplication.APITokenCredential, error) {
	return authapplication.APITokenCredential{}, nil
}

func (routerAuthService) ListAPITokens(context.Context, authdomain.Principal, authdomain.APITokenListQuery) (authdomain.APITokenListPage, error) {
	return authdomain.APITokenListPage{Items: []authdomain.APITokenInfo{}}, nil
}

func (routerAuthService) RevokeSession(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

func (routerAuthService) RevokeAPIToken(context.Context, authdomain.Principal, foundation.ID) error {
	return nil
}

func readyAuthHandler(t *testing.T) *authhttp.Handler {
	t.Helper()
	handler, err := authhttp.NewHandler(routerAuthService{}, authhttp.Options{SecureCookie: true, AllowedOrigins: []string{"https://app.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func readyScopedAuthHandler(t *testing.T, principals map[string]authdomain.Principal) *authhttp.Handler {
	t.Helper()
	handler, err := authhttp.NewHandler(routerScopedAuthService{principals: principals}, authhttp.Options{
		SecureCookie: true, AllowedOrigins: []string{"https://app.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

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

func (routerTimelineService) List(context.Context, knowledgeapplication.TimelineListRequest) (knowledgeapplication.TimelinePage, error) {
	return knowledgeapplication.TimelinePage{}, nil
}

func (routerTimelineService) Get(context.Context, foundation.ID, foundation.ID) (knowledge.KnowledgeEvent, error) {
	return knowledge.KnowledgeEvent{}, nil
}

func (routerImpactService) Analyze(context.Context, knowledgeapplication.ImpactAnalysisRequest) (knowledgeapplication.ImpactAnalysisResult, error) {
	return knowledgeapplication.ImpactAnalysisResult{}, nil
}

func (routerImpactService) GetReport(context.Context, foundation.ID, foundation.ID) (knowledge.ImpactReport, error) {
	return knowledge.ImpactReport{}, nil
}

func readyKnowledgeHandler() *knowledgehttp.Handler {
	return knowledgehttp.NewHandler(routerTimelineService{}, routerImpactService{}, time.Second)
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

func TestRouterRegistersKnowledgeTimelineAndImpactRoutes(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Knowledge: knowledgehttp.NewHandler(nil, nil, time.Second)})
	workspaceID := "92000000-0000-4000-8000-000000000001"
	eventID := "92000000-0000-4000-8000-000000000002"
	reportID := "92000000-0000-4000-8000-000000000003"
	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "timeline list", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/timeline"},
		{name: "timeline detail", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/timeline/" + eventID},
		{name: "impact analysis", method: http.MethodPost, path: "/api/v1/workspaces/" + workspaceID + "/timeline/" + eventID + "/impact-analysis"},
		{name: "impact report", method: http.MethodGet, path: "/api/v1/workspaces/" + workspaceID + "/impact-reports/" + reportID},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "KNOWLEDGE_HTTP_UNAVAILABLE") {
				t.Fatalf("knowledge route status=%d body=%s", response.Code, response.Body.String())
			}
		})
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
	router := NewRouter(Dependencies{Version: "v-test", Database: fakePinger{}, Graph: readyGraphHandler(), Candidate: readyCandidateHandler(), Knowledge: readyKnowledgeHandler()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"status":"ready"`) || !strings.Contains(res.Body.String(), `"graph":{"status":"ready"}`) ||
		!strings.Contains(res.Body.String(), `"semantic_links":{"status":"ready"}`) ||
		!strings.Contains(res.Body.String(), `"knowledge_timeline":{"status":"ready"}`) {
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

func TestRouterAuthProtectsBusinessRoutesButKeepsPublicHealth(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "auth-test", Database: fakePinger{}, Auth: readyAuthHandler(t), AuthRequired: true,
		AuthCheck: func(context.Context) error { return nil },
		Graph:     readyGraphHandler(), Candidate: readyCandidateHandler(), Knowledge: readyKnowledgeHandler(),
	})
	for _, path := range []string{
		"/api/v1/graph/nodes?workspace_id=92000000-0000-4000-8000-000000000001",
		"/api/v1/workspaces/92000000-0000-4000-8000-000000000001/timeline",
	} {
		unauthenticated := httptest.NewRequest(http.MethodGet, path, nil)
		unauthenticatedResponse := httptest.NewRecorder()
		router.ServeHTTP(unauthenticatedResponse, unauthenticated)
		if unauthenticatedResponse.Code != http.StatusUnauthorized || !strings.Contains(unauthenticatedResponse.Body.String(), "AUTH_UNAUTHORIZED") {
			t.Fatalf("business route %s status=%d body=%s", path, unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
		}
	}

	for _, test := range []struct {
		name string
		path string
		want int
	}{
		{name: "livez", path: "/livez", want: http.StatusOK},
		{name: "readyz", path: "/readyz", want: http.StatusOK},
		{name: "system status", path: "/api/v1/system/status", want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body.String())
			}
		})
	}
}

func TestRouterScopedBearerCapabilityMatrixAndBootstrapIsolation(t *testing.T) {
	const (
		readToken      = "router-read-token"
		indexToken     = "router-index-token"
		proposalToken  = "router-proposal-token"
		bootstrapToken = "bootstrap-token-with-at-least-32-bytes"
	)
	principal := func(id foundation.ID, scopes ...capability.Capability) authdomain.Principal {
		return authdomain.Principal{Kind: authdomain.PrincipalAPIToken, ID: id, Scopes: scopes}
	}
	router := NewRouter(Dependencies{
		Version: "auth-scope-test", Database: fakePinger{}, AuthRequired: true,
		Auth: readyScopedAuthHandler(t, map[string]authdomain.Principal{
			readToken:     principal("93000000-0000-4000-8000-000000000001", capability.ReadLocal),
			indexToken:    principal("93000000-0000-4000-8000-000000000002", capability.IndexMaintenance),
			proposalToken: principal("93000000-0000-4000-8000-000000000003", capability.WriteProposal),
		}),
		Workspace:     workspacehttp.NewHandler(nil),
		ChangeControl: changecontrolhttp.NewHandler(nil),
		Graph:         readyGraphHandler(),
		Candidate:     readyCandidateHandler(),
		Review:        reviewhttp.NewHandler(nil, time.Second),
	})

	request := func(method, path, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	readResponse := request(http.MethodGet, "/api/v1/graph/nodes?workspace_id=92000000-0000-4000-8000-000000000001&query=topic", readToken)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("READ_LOCAL read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}

	for _, test := range []struct {
		name   string
		token  string
		method string
		path   string
	}{
		{name: "read cannot index", token: readToken, method: http.MethodPost, path: "/api/v1/workspaces/92000000-0000-4000-8000-000000000001/scan"},
		{name: "read cannot propose", token: readToken, method: http.MethodPost, path: "/api/v1/workspaces"},
		{name: "read cannot approve", token: readToken, method: http.MethodPost, path: "/api/v1/proposals/92000000-0000-4000-8000-000000000001/approvals"},
		{name: "index cannot read", token: indexToken, method: http.MethodGet, path: "/api/v1/graph/nodes?workspace_id=92000000-0000-4000-8000-000000000001"},
		{name: "index alone cannot run composite scan", token: indexToken, method: http.MethodPost, path: "/api/v1/workspaces/92000000-0000-4000-8000-000000000001/scan"},
		{name: "proposal cannot read", token: proposalToken, method: http.MethodGet, path: "/api/v1/graph/nodes?workspace_id=92000000-0000-4000-8000-000000000001"},
		{name: "proposal cannot approve knowledge", token: proposalToken, method: http.MethodPost, path: "/api/v1/proposals/92000000-0000-4000-8000-000000000001/approvals"},
		{name: "read cannot mutate review", token: readToken, method: http.MethodPost, path: "/api/v1/review/decks"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(test.method, test.path, test.token)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), authapplication.ErrorCodeForbidden) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	reviewResponse := request(http.MethodPost, "/api/v1/review/decks", proposalToken)
	if reviewResponse.Code != http.StatusBadRequest || !strings.Contains(reviewResponse.Body.String(), "REVIEW_JSON_INVALID") {
		t.Fatalf("WRITE_PROPOSAL review status=%d body=%s", reviewResponse.Code, reviewResponse.Body.String())
	}

	bootstrapResponse := request(http.MethodGet, "/api/v1/graph/nodes?workspace_id=92000000-0000-4000-8000-000000000001", bootstrapToken)
	if bootstrapResponse.Code != http.StatusUnauthorized || !strings.Contains(bootstrapResponse.Body.String(), authapplication.ErrorCodeUnauthorized) {
		t.Fatalf("bootstrap business status=%d body=%s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
}

func TestRouterAuthInitializationFailureFailsClosed(t *testing.T) {
	router := NewRouter(Dependencies{
		Version: "auth-failure", Database: fakePinger{}, AuthRequired: true, AuthInitErr: errors.New("private auth composition detail"),
		Graph: readyGraphHandler(), Candidate: readyCandidateHandler(),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/graph/nodes?workspace_id=92000000-0000-4000-8000-000000000001", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "AUTH_DEPENDENCY_UNAVAILABLE") || strings.Contains(response.Body.String(), "private auth composition detail") {
		t.Fatalf("business fail-open status=%d body=%s", response.Code, response.Body.String())
	}
	readyRequest := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyResponse := httptest.NewRecorder()
	router.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != http.StatusServiceUnavailable || !strings.Contains(readyResponse.Body.String(), "auth_dependencies_unavailable") {
		t.Fatalf("auth readiness status=%d body=%s", readyResponse.Code, readyResponse.Body.String())
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"auth":{"reason":"auth_dependencies_unavailable","status":"unavailable"}`) {
		t.Fatalf("auth system status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	livezRequest := httptest.NewRequest(http.MethodGet, "/livez", nil)
	livezResponse := httptest.NewRecorder()
	router.ServeHTTP(livezResponse, livezRequest)
	if livezResponse.Code != http.StatusOK {
		t.Fatalf("livez must remain public status=%d", livezResponse.Code)
	}
}

func TestRouterAuthReadinessFailsClosedWhenCredentialTablesAreUnavailable(t *testing.T) {
	privateErr := errors.New("auth.session relation is missing")
	router := NewRouter(Dependencies{
		Version: "auth-readiness", Database: fakePinger{}, AuthRequired: true, Auth: readyAuthHandler(t),
		AuthCheck: func(context.Context) error { return privateErr },
		Graph:     readyGraphHandler(), Candidate: readyCandidateHandler(),
	})

	readyRequest := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyResponse := httptest.NewRecorder()
	router.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != http.StatusServiceUnavailable || !strings.Contains(readyResponse.Body.String(), "AUTH_DEPENDENCY_UNAVAILABLE") || strings.Contains(readyResponse.Body.String(), privateErr.Error()) {
		t.Fatalf("auth readiness status=%d body=%s", readyResponse.Code, readyResponse.Body.String())
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"auth":{"reason":"auth_dependencies_unavailable","status":"unavailable"}`) || strings.Contains(statusResponse.Body.String(), privateErr.Error()) {
		t.Fatalf("auth system status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
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
