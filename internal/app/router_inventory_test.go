package app

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	artifacthttp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/http"
	authoringhttp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/http"
	capturehttp "github.com/CodeZen-Lizhi/zhixu/internal/capture/http"
	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	collectionhttp "github.com/CodeZen-Lizhi/zhixu/internal/collection/http"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	documenthistoryhttp "github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/http"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	exporthttp "github.com/CodeZen-Lizhi/zhixu/internal/export/http"
	gitsynchttp "github.com/CodeZen-Lizhi/zhixu/internal/gitsync/http"
	graphhttp "github.com/CodeZen-Lizhi/zhixu/internal/graph/http"
	healthhttp "github.com/CodeZen-Lizhi/zhixu/internal/health/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	ingestionhttp "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/http"
	knowledgehttp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/http"
	memoryhttp "github.com/CodeZen-Lizhi/zhixu/internal/memory/http"
	modelsettingshttp "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/http"
	organizinghttp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/http"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"
	reviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/http"
	interviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/http"
	learningpathhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/http"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"
	"github.com/gin-gonic/gin"
)

const expectedOpenAPIOperationCount = 183

var openAPIOperationMethods = map[string]struct{}{
	"delete":  {},
	"get":     {},
	"head":    {},
	"options": {},
	"patch":   {},
	"post":    {},
	"put":     {},
	"trace":   {},
}

type routeInventoryDocument struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

func TestRouterRoutesExactlyMatchOpenAPI(t *testing.T) {
	want := openAPIRouteSet(t)
	if len(want) != expectedOpenAPIOperationCount {
		t.Fatalf("OpenAPI operations=%d, want %d", len(want), expectedOpenAPIOperationCount)
	}

	router := NewRouter(completeRouteInventoryDependencies(t))
	got := ginRouteSet(t, router)
	assertRouteSetsEqual(t, got, want)
}

func TestRouterMetricsIsTheOnlyOptionalRuntimeRoute(t *testing.T) {
	want := openAPIRouteSet(t)
	want["GET /metrics"] = struct{}{}
	deps := completeRouteInventoryDependencies(t)
	deps.MetricsHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	got := ginRouteSet(t, NewRouter(deps))
	assertRouteSetsEqual(t, got, want)
}

func completeRouteInventoryDependencies(t *testing.T) Dependencies {
	t.Helper()
	modelSettings, err := modelsettingshttp.NewHandler(routerModelSettingsManager{}, modelsettingshttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return Dependencies{
		Version:         "route-inventory-test",
		Database:        fakePinger{},
		Workspace:       workspacehttp.NewHandler(nil),
		Workflow:        workflowhttp.NewHandler(nil),
		ChangeControl:   changecontrolhttp.NewHandler(nil),
		Collection:      collectionhttp.NewHandler(nil, time.Second),
		Health:          healthhttp.NewHandler(nil, nil, nil, nil, nil),
		Ingestion:       ingestionhttp.NewHandler(nil),
		Retrieval:       retrievalhttp.NewHandler(nil, nil, nil),
		Graph:           graphhttp.NewHandler(nil, time.Second),
		Candidate:       graphhttp.NewCandidateHandler(nil, time.Second),
		Conversation:    conversationhttp.NewHandler(nil, nil),
		Events:          eventshttp.NewHandler(nil),
		Export:          exporthttp.NewHandler(nil),
		Review:          reviewhttp.NewHandler(nil, time.Second),
		LearningPath:    learningpathhttp.NewHandler(nil, time.Second),
		Memory:          memoryhttp.NewHandler(nil, time.Second),
		Interview:       interviewhttp.NewHandler(nil, time.Second),
		Knowledge:       knowledgehttp.NewHandler(nil, nil, time.Second),
		Artifact:        artifacthttp.NewHandler(nil, nil, time.Second),
		Authoring:       authoringhttp.NewHandler(nil, time.Second),
		Capture:         capturehttp.NewHandler(nil, time.Second),
		Organizing:      organizinghttp.NewHandler(nil, nil, nil, time.Second),
		DocumentHistory: documenthistoryhttp.NewHandler(nil, time.Second),
		GitSync:         gitsynchttp.NewHandler(nil, time.Second),
		ModelSettings:   modelSettings,
		Auth:            readyAuthHandler(t),
		AuthRequired:    true,
	}
}

func openAPIRouteSet(t *testing.T) map[string]struct{} {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document routeInventoryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	routes := make(map[string]struct{})
	for path, item := range document.Paths {
		for method := range item {
			method = strings.ToLower(method)
			if _, ok := openAPIOperationMethods[method]; !ok {
				continue
			}
			routes[strings.ToUpper(method)+" "+path] = struct{}{}
		}
	}
	return routes
}

func ginRouteSet(t *testing.T, router *gin.Engine) map[string]struct{} {
	t.Helper()
	routes := make(map[string]struct{})
	for _, route := range router.Routes() {
		key := route.Method + " " + httpapi.CanonicalRoutePattern(route.Path)
		if _, duplicate := routes[key]; duplicate {
			t.Fatalf("duplicate runtime route %s", key)
		}
		routes[key] = struct{}{}
	}
	return routes
}

func assertRouteSetsEqual(t *testing.T, got, want map[string]struct{}) {
	t.Helper()
	missing := routeSetDifference(want, got)
	extra := routeSetDifference(got, want)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("runtime/OpenAPI route mismatch\nmissing: %v\nextra: %v", missing, extra)
	}
}

func routeSetDifference(left, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for route := range left {
		if _, found := right[route]; !found {
			difference = append(difference, route)
		}
	}
	sort.Strings(difference)
	return difference
}
