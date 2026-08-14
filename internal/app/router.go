package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	artifacthttp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/http"
	authhttp "github.com/CodeZen-Lizhi/zhixu/internal/auth/http"
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
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"
	reviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/http"
	interviewhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/http"
	learningpathhttp "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/http"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"
	"github.com/gin-gonic/gin"
)

type requestIDKey struct{}

type readinessError struct {
	kind string
	err  error
}

func (e *readinessError) Error() string { return e.err.Error() }
func (e *readinessError) Unwrap() error { return e.err }

// Dependencies is the composition boundary for the HTTP application.
type Dependencies struct {
	Version           string
	Database          postgres.Pinger
	DatabaseConfigErr error
	DatabaseInitErr   error
	PingTimeout       time.Duration
	Static            http.Handler
	Workspace         *workspacehttp.Handler
	Workflow          *workflowhttp.Handler
	ChangeControl     *changecontrolhttp.Handler
	Collection        *collectionhttp.Handler
	Health            *healthhttp.Handler
	Ingestion         *ingestionhttp.Handler
	Retrieval         *retrievalhttp.Handler
	Graph             *graphhttp.Handler
	Candidate         *graphhttp.CandidateHandler
	Conversation      *conversationhttp.Handler
	DraftStream       *conversationhttp.DraftStreamHandler
	Events            *eventshttp.Handler
	Export            *exporthttp.Handler
	Review            *reviewhttp.Handler
	LearningPath      *learningpathhttp.Handler
	Memory            *memoryhttp.Handler
	Interview         *interviewhttp.Handler
	Knowledge         *knowledgehttp.Handler
	Artifact          *artifacthttp.Handler
	Authoring         *authoringhttp.Handler
	Capture           *capturehttp.Handler
	Organizing        *organizinghttp.Handler
	DocumentHistory   *documenthistoryhttp.Handler
	GitSync           *gitsynchttp.Handler
	ModelSettings     *modelsettingshttp.Handler
	Auth              *authhttp.Handler
	AuthRequired      bool
	AuthInitErr       error
	// AuthCheck confirms that both authentication credential tables remain readable.
	AuthCheck  func(context.Context) error
	RAGEnabled bool
	RAGInitErr error
	Logger     *slog.Logger
	Tracer     observability.Tracer
	// MetricsHandler exposes the process-local Prometheus registry.
	MetricsHandler http.Handler
}

// NewRouter builds the API and static-resource boundary. Domain modules are
// intentionally absent from M1; later milestones add them behind app seams.
func NewRouter(deps Dependencies) *gin.Engine {
	if deps.PingTimeout <= 0 {
		deps.PingTimeout = 2 * time.Second
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Tracer == nil {
		deps.Tracer = observability.NewNoopTracer()
	}
	if deps.Graph == nil {
		deps.Graph = graphhttp.NewHandler(nil, 0)
	}
	if deps.Candidate == nil {
		deps.Candidate = graphhttp.NewCandidateHandler(nil, 0)
	}
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = true
	router.RemoveExtraSlash = false
	router.UseRawPath = true
	router.UseEscapedPath = false
	router.UnescapePathValues = false
	router.ContextWithFallback = false
	router.ForwardedByClientIP = false
	router.RemoteIPHeaders = nil
	router.TrustedPlatform = ""
	if err := router.SetTrustedProxies(nil); err != nil {
		panic(fmt.Errorf("configure Gin trusted proxies: %w", err))
	}
	router.Use(modelSettingsNoStoreMiddleware)
	router.Use(requestIDMiddleware)
	router.Use(requestTraceMiddleware(deps.Tracer))
	router.Use(requestLogMiddleware(deps.Logger))
	router.Use(recoverPanicMiddleware(deps.Logger))
	router.GET("/livez", httpapi.GinHandler(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	}))
	router.GET("/readyz", httpapi.GinHandler(func(w http.ResponseWriter, r *http.Request) {
		if err := checkDatabase(r.Context(), deps); err != nil {
			reason := readinessReason(err)
			writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "服务尚未就绪", reason != "database_configuration_invalid", map[string]any{
				"dependency": "database",
				"reason":     reason,
			})
			return
		}
		if err := checkAuth(r.Context(), deps); err != nil {
			writeProblem(w, http.StatusServiceUnavailable, "AUTH_DEPENDENCY_UNAVAILABLE", "服务尚未就绪", true, map[string]any{
				"dependency": "auth",
				"reason":     "auth_dependencies_unavailable",
			})
			return
		}
		if deps.RAGEnabled && deps.RAGInitErr != nil {
			writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "服务尚未就绪", true, map[string]any{
				"dependency": "rag",
				"reason":     "rag_dependencies_unavailable",
			})
			return
		}
		if !deps.Graph.Available() {
			writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "服务尚未就绪", true, map[string]any{
				"dependency": "graph",
				"reason":     "graph_dependencies_unavailable",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}))
	if deps.MetricsHandler != nil {
		router.GET("/metrics", gin.WrapH(deps.MetricsHandler))
	}
	api := router.Group("/api/v1")
	api.GET("/system/status", httpapi.GinHandler(func(w http.ResponseWriter, r *http.Request) {
		handleSystemStatus(w, r, deps)
	}))
	if deps.Auth != nil {
		deps.Auth.OpenRoutes(api)
		protected := api.Group("")
		protected.Use(deps.Auth.Middleware)
		deps.Auth.ProtectedRoutes(protected)
		registerDomainRoutes(protected, deps)
	} else if deps.AuthRequired {
		api.POST("/auth/sessions", httpapi.GinHandler(authUnavailableHandler))
		protected := api.Group("")
		protected.Use(authUnavailableMiddleware)
		registerDomainRoutes(protected, deps)
	} else {
		registerDomainRoutes(api, deps)
	}
	router.NoMethod(func(context *gin.Context) {
		context.Header("Allow", "")
		context.Writer.Header().Del("Allow")
		writeProblem(context.Writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不被支持", false, nil)
		context.Abort()
	})
	router.NoRoute(func(context *gin.Context) {
		if !supportedHTTPMethod(context.Request.Method) {
			writeProblem(context.Writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不被支持", false, nil)
			context.Abort()
			return
		}
		if strings.HasPrefix(context.Request.URL.Path, "/api/") {
			writeProblem(context.Writer, http.StatusNotFound, "NOT_FOUND", "请求的 API 资源不存在", false, nil)
			context.Abort()
			return
		}
		if deps.Static != nil {
			deps.Static.ServeHTTP(context.Writer, context.Request)
			context.Abort()
			return
		}
		writeProblem(context.Writer, http.StatusNotFound, "WEB_ASSETS_UNAVAILABLE", "Web 静态资源不可用", false, nil)
		context.Abort()
	})
	return router
}

func modelSettingsNoStoreMiddleware(context *gin.Context) {
	request := context.Request
	if request != nil && (request.URL.Path == "/api/v1/settings/models" || request.URL.Path == "/api/v1/settings/models/test" ||
		request.URL.Path == "/api/v1/settings/models/activations" ||
		strings.Contains(request.URL.Path, "/git-remote")) {
		context.Header("Cache-Control", "no-store")
	}
	context.Next()
}

func registerDomainRoutes(api gin.IRouter, deps Dependencies) {
	if deps.Workspace != nil {
		deps.Workspace.Routes(api)
	}
	if deps.Workflow != nil {
		deps.Workflow.Routes(api)
	}
	if deps.ChangeControl != nil {
		deps.ChangeControl.Routes(api)
	}
	if deps.Collection != nil {
		deps.Collection.Routes(api)
	}
	if deps.Health != nil {
		deps.Health.Routes(api)
	}
	if deps.Ingestion != nil {
		deps.Ingestion.Routes(api)
	}
	if deps.Retrieval != nil {
		deps.Retrieval.Routes(api)
	}
	deps.Graph.Routes(api)
	deps.Candidate.Routes(api)
	if deps.Conversation != nil {
		deps.Conversation.Routes(api)
	}
	if deps.DraftStream != nil {
		deps.DraftStream.Routes(api)
	}
	if deps.Events != nil {
		deps.Events.Routes(api)
	}
	if deps.Export != nil {
		deps.Export.Routes(api)
	}
	if deps.Review != nil {
		deps.Review.Routes(api)
	}
	if deps.LearningPath != nil {
		deps.LearningPath.Routes(api)
	}
	if deps.Memory != nil {
		deps.Memory.Routes(api)
	}
	if deps.Interview != nil {
		deps.Interview.Routes(api)
	}
	if deps.Knowledge != nil {
		deps.Knowledge.Routes(api)
	}
	if deps.Artifact != nil {
		deps.Artifact.Routes(api)
	}
	if deps.Authoring != nil {
		deps.Authoring.Routes(api)
	}
	if deps.Capture != nil {
		deps.Capture.Routes(api)
	}
	if deps.Organizing != nil {
		deps.Organizing.Routes(api)
	}
	if deps.DocumentHistory != nil {
		deps.DocumentHistory.Routes(api)
	}
	if deps.GitSync != nil {
		deps.GitSync.Routes(api)
	}
	if deps.ModelSettings != nil {
		deps.ModelSettings.Routes(api)
	}
}

func authUnavailableHandler(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, http.StatusServiceUnavailable, "AUTH_DEPENDENCY_UNAVAILABLE", "认证服务不可用", true, nil)
}

func authUnavailableMiddleware(context *gin.Context) {
	authUnavailableHandler(context.Writer, nil)
	context.Abort()
}

func requestTraceMiddleware(tracer observability.Tracer) gin.HandlerFunc {
	return func(ginContext *gin.Context) {
		request := ginContext.Request
		if isOperationalPath(request.URL.Path) {
			ginContext.Next()
			return
		}
		ctx := request.Context()
		if incoming := strings.TrimSpace(request.Header.Get("traceparent")); incoming != "" {
			if decoded, err := observability.DecodeTraceMetadata(ctx, map[string]string{observability.TraceParentMetadataKey: incoming}); err == nil {
				ctx = decoded
			}
		}
		ctx = observability.WithCorrelation(ctx, observability.Correlation{RequestID: requestID(ctx)})
		traced, span, err := tracer.Start(ctx, "http.request")
		if err == nil {
			ctx = traced
			defer span.End()
			if trace, found := observability.TraceContextFromContext(ctx); found {
				if traceParent, encodeErr := trace.TraceParent(); encodeErr == nil {
					ginContext.Header("traceparent", traceParent)
				}
			}
		}
		ginContext.Request = request.WithContext(ctx)
		ginContext.Next()
	}
}

func isOperationalPath(path string) bool {
	switch path {
	case "/metrics", "/livez", "/readyz":
		return true
	default:
		return false
	}
}

func supportedHTTPMethod(method string) bool {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace, "QUERY":
		return true
	default:
		return false
	}
}

func handleSystemStatus(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	databaseStatus := map[string]string{"status": "unavailable"}
	graphStatus := map[string]string{"status": "ready"}
	semanticLinksStatus := map[string]string{"status": "ready"}
	ragStatus := map[string]string{"status": "disabled"}
	collectionsStatus := map[string]string{"status": "unavailable"}
	knowledgeHealthStatus := map[string]string{"status": "unavailable"}
	knowledgeTimelineStatus := map[string]string{"status": "unavailable"}
	reviewStatus := map[string]string{"status": "unavailable"}
	memoryStatus := map[string]string{"status": "unavailable"}
	interviewStatus := map[string]string{"status": "unavailable"}
	authoringStatus := map[string]string{"status": "unavailable"}
	captureStatus := map[string]string{"status": "unavailable"}
	organizingStatus := map[string]string{"status": "unavailable"}
	authStatus := map[string]string{"status": "disabled"}
	status := "degraded"
	if err := checkDatabase(r.Context(), deps); err == nil {
		databaseStatus["status"] = "ready"
		status = "ready"
	} else {
		databaseStatus["message"] = readinessReason(err)
	}
	if deps.RAGEnabled {
		ragStatus["status"] = "ready"
		if deps.RAGInitErr != nil {
			ragStatus["status"] = "unavailable"
			ragStatus["reason"] = "rag_dependencies_unavailable"
			status = "degraded"
		}
	}
	if deps.AuthRequired {
		authStatus["status"] = "ready"
		if err := checkAuth(r.Context(), deps); err != nil {
			authStatus["status"] = "unavailable"
			authStatus["reason"] = "auth_dependencies_unavailable"
			status = "degraded"
		}
	}
	if !deps.Graph.Available() {
		graphStatus["status"] = "unavailable"
		graphStatus["reason"] = "graph_dependencies_unavailable"
		status = "degraded"
	}
	if !deps.Candidate.Available() {
		semanticLinksStatus["status"] = "unavailable"
		semanticLinksStatus["reason"] = "semantic_link_dependencies_unavailable"
		status = "degraded"
	}
	if deps.Collection != nil && deps.Collection.Available() {
		collectionsStatus["status"] = "ready"
	}
	if deps.Health != nil && deps.Health.Available() {
		knowledgeHealthStatus["status"] = "ready"
	}
	if deps.Knowledge != nil && deps.Knowledge.Available() {
		knowledgeTimelineStatus["status"] = "ready"
	}
	if deps.Review != nil && deps.Review.Available() && deps.LearningPath != nil && deps.LearningPath.Available() {
		reviewStatus["status"] = "ready"
	}
	if deps.Memory != nil && deps.Memory.Available() {
		memoryStatus["status"] = "ready"
	}
	if deps.Interview != nil && deps.Interview.Available() {
		interviewStatus["status"] = "ready"
	}
	if deps.Authoring != nil && deps.Authoring.Available() {
		authoringStatus["status"] = "ready"
	}
	if deps.Capture != nil && deps.Capture.Available() {
		captureStatus["status"] = "ready"
	}
	if deps.Organizing != nil && deps.Organizing.Available() {
		organizingStatus["status"] = "ready"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":             status,
		"version":            deps.Version,
		"database":           databaseStatus,
		"graph":              graphStatus,
		"semantic_links":     semanticLinksStatus,
		"rag":                ragStatus,
		"collections":        collectionsStatus,
		"knowledge_health":   knowledgeHealthStatus,
		"knowledge_timeline": knowledgeTimelineStatus,
		"review":             reviewStatus,
		"memory":             memoryStatus,
		"interview":          interviewStatus,
		"authoring":          authoringStatus,
		"capture":            captureStatus,
		"organizing":         organizingStatus,
		"auth":               authStatus,
		"request_id":         requestID(r.Context()),
	})
}

func checkDatabase(parent context.Context, deps Dependencies) error {
	if deps.DatabaseConfigErr != nil {
		return &readinessError{kind: "config", err: deps.DatabaseConfigErr}
	}
	if deps.DatabaseInitErr != nil {
		return &readinessError{kind: "init", err: deps.DatabaseInitErr}
	}
	if deps.Database == nil {
		return &readinessError{kind: "unavailable", err: errors.New("database pool is unavailable")}
	}
	ctx, cancel := context.WithTimeout(parent, deps.PingTimeout)
	defer cancel()
	if err := deps.Database.Ping(ctx); err != nil {
		return &readinessError{kind: "ping", err: fmt.Errorf("database ping failed: %w", err)}
	}
	return nil
}

func checkAuth(parent context.Context, deps Dependencies) error {
	if !deps.AuthRequired {
		return nil
	}
	if deps.AuthInitErr != nil {
		return errors.New("authentication initialization failed")
	}
	if deps.Auth == nil || deps.AuthCheck == nil {
		return errors.New("authentication health check is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, deps.PingTimeout)
	defer cancel()
	if err := deps.AuthCheck(ctx); err != nil {
		return fmt.Errorf("authentication health check failed: %w", err)
	}
	return nil
}

func readinessReason(err error) string {
	if err == nil {
		return ""
	}
	var typed *readinessError
	if errors.As(err, &typed) {
		switch typed.kind {
		case "config":
			if strings.Contains(typed.err.Error(), "invalid") {
				return "database_configuration_invalid"
			}
			return "database_not_configured"
		case "unavailable":
			return "database_unavailable"
		case "ping":
			return "database_ping_failed"
		case "init":
			return "database_configuration_invalid"
		}
	}
	return "database_ping_failed"
}

func requestIDMiddleware(ginContext *gin.Context) {
	request := ginContext.Request
	id := strings.TrimSpace(request.Header.Get("X-Request-ID"))
	if id == "" || len(id) > 128 {
		id = newRequestID()
	}
	ctx := context.WithValue(request.Context(), requestIDKey{}, id)
	ginContext.Header("X-Request-ID", id)
	ginContext.Request = request.WithContext(ctx)
	ginContext.Next()
}

type responseTracker struct {
	gin.ResponseWriter
	started bool
}

func (writer *responseTracker) WriteHeader(status int) {
	if writer.started {
		return
	}
	writer.started = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseTracker) Write(body []byte) (int, error) {
	if !writer.started {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func (writer *responseTracker) WriteString(body string) (int, error) {
	if !writer.started {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.WriteString(body)
}

func (writer *responseTracker) Flush() {
	if !writer.started {
		writer.WriteHeader(http.StatusOK)
	}
	writer.ResponseWriter.Flush()
}

func (writer *responseTracker) Unwrap() http.ResponseWriter {
	if unwrapper, ok := writer.ResponseWriter.(interface{ Unwrap() http.ResponseWriter }); ok {
		return unwrapper.Unwrap()
	}
	return writer.ResponseWriter
}

func requestLogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(ginContext *gin.Context) {
		tracked := &responseTracker{ResponseWriter: ginContext.Writer}
		ginContext.Writer = tracked
		ginContext.Next()
		request := ginContext.Request
		logger.InfoContext(request.Context(), "http request completed", "request_id", requestID(request.Context()), "method", request.Method,
			"http_route", requestRoutePattern(ginContext), "status", tracked.Status())
	}
}

// requestRoutePattern 只返回 Gin 已匹配的路由模板，避免将客户端请求路径写入日志。
func requestRoutePattern(ginContext *gin.Context) string {
	if ginContext == nil {
		return ""
	}
	return httpapi.CanonicalRoutePattern(ginContext.FullPath())
}

func requestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("request-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
