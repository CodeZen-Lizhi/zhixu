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

	changecontrolhttp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/http"
	conversationhttp "github.com/CodeZen-Lizhi/zhixu/internal/conversation/http"
	eventshttp "github.com/CodeZen-Lizhi/zhixu/internal/events/http"
	graphhttp "github.com/CodeZen-Lizhi/zhixu/internal/graph/http"
	ingestionhttp "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/http"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalhttp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/http"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacehttp "github.com/CodeZen-Lizhi/zhixu/internal/workspace/http"
	"github.com/go-chi/chi/v5"
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
	Ingestion         *ingestionhttp.Handler
	Retrieval         *retrievalhttp.Handler
	Graph             *graphhttp.Handler
	Conversation      *conversationhttp.Handler
	Events            *eventshttp.Handler
	RAGEnabled        bool
	RAGInitErr        error
	Logger            *slog.Logger
	Tracer            observability.Tracer
}

// NewRouter builds the API and static-resource boundary. Domain modules are
// intentionally absent from M1; later milestones add them behind app seams.
func NewRouter(deps Dependencies) http.Handler {
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
	router := chi.NewRouter()
	router.Use(requestIDMiddleware)
	router.Use(requestTraceMiddleware(deps.Tracer))
	router.Use(requestLogMiddleware(deps.Logger))
	router.Get("/livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := checkDatabase(r.Context(), deps); err != nil {
			reason := readinessReason(err)
			writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "服务尚未就绪", reason != "database_configuration_invalid", map[string]any{
				"dependency": "database",
				"reason":     reason,
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
	})
	router.Route("/api/v1", func(api chi.Router) {
		api.Get("/system/status", func(w http.ResponseWriter, r *http.Request) {
			handleSystemStatus(w, r, deps)
		})
		if deps.Workspace != nil {
			deps.Workspace.Routes(api)
		}
		if deps.Workflow != nil {
			deps.Workflow.Routes(api)
		}
		if deps.ChangeControl != nil {
			deps.ChangeControl.Routes(api)
		}
		if deps.Ingestion != nil {
			deps.Ingestion.Routes(api)
		}
		if deps.Retrieval != nil {
			deps.Retrieval.Routes(api)
		}
		deps.Graph.Routes(api)
		if deps.Conversation != nil {
			deps.Conversation.Routes(api)
		}
		if deps.Events != nil {
			deps.Events.Routes(api)
		}
		api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "请求的 API 资源不存在", false, nil)
		})
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不被支持", false, nil)
	})
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "请求的 API 资源不存在", false, nil)
			return
		}
		if deps.Static != nil {
			deps.Static.ServeHTTP(w, r)
			return
		}
		writeProblem(w, http.StatusNotFound, "WEB_ASSETS_UNAVAILABLE", "Web 静态资源不可用", false, nil)
	})
	return router
}

func requestTraceMiddleware(tracer observability.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if incoming := strings.TrimSpace(r.Header.Get("traceparent")); incoming != "" {
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
						w.Header().Set("traceparent", traceParent)
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func handleSystemStatus(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	databaseStatus := map[string]string{"status": "unavailable"}
	graphStatus := map[string]string{"status": "ready"}
	ragStatus := map[string]string{"status": "disabled"}
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
	if !deps.Graph.Available() {
		graphStatus["status"] = "unavailable"
		graphStatus["reason"] = "graph_dependencies_unavailable"
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"version":    deps.Version,
		"database":   databaseStatus,
		"graph":      graphStatus,
		"rag":        ragStatus,
		"request_id": requestID(r.Context()),
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

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" || len(id) > 128 {
			id = newRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.status == 0 {
			w.WriteHeader(http.StatusOK)
		}
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func requestLogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(wrapped, r)
			status := wrapped.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http request completed", "request_id", requestID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", status)
		})
	}
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
