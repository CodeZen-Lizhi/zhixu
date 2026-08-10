package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	panicStackFrameLimit = 24
	panicStackByteLimit  = 4096
)

// recoverPanicMiddleware keeps unexpected handler panics inside the HTTP
// boundary without exposing panic values or local filesystem paths.
func recoverPanicMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				logger.ErrorContext(
					r.Context(),
					"http request panic recovered",
					"error_code", "HTTP_PANIC_RECOVERED",
					"request_id", requestID(r.Context()),
					"http_route", requestRoutePattern(r),
					"panic_stack", boundedPanicStack(),
				)
				if !responseStarted(w) {
					writeProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务处理失败", false, nil)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

func responseStarted(w http.ResponseWriter) bool {
	tracked, ok := w.(*statusWriter)
	return ok && tracked.status != 0
}

func boundedPanicStack() string {
	programCounters := make([]uintptr, panicStackFrameLimit)
	count := runtime.Callers(4, programCounters)
	frames := runtime.CallersFrames(programCounters[:count])

	var stack strings.Builder
	for {
		frame, more := frames.Next()
		line := fmt.Sprintf("%s %s:%d", frame.Function, filepath.Base(frame.File), frame.Line)
		if stack.Len() > 0 {
			line = "\n" + line
		}
		if stack.Len()+len(line) > panicStackByteLimit {
			break
		}
		stack.WriteString(line)
		if !more {
			break
		}
	}
	return stack.String()
}
