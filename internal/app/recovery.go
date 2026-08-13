package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	panicStackFrameLimit = 24
	panicStackByteLimit  = 4096
)

// recoverPanicMiddleware keeps unexpected handler panics inside the HTTP
// boundary without exposing panic values or local filesystem paths.
func recoverPanicMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(ginContext *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if isAbortHandlerPanic(recovered) {
				panic(http.ErrAbortHandler)
			}

			request := ginContext.Request
			logger.ErrorContext(
				request.Context(),
				"http request panic recovered",
				"error_code", "HTTP_PANIC_RECOVERED",
				"request_id", requestID(request.Context()),
				"http_route", requestRoutePattern(ginContext),
				"panic_stack", boundedPanicStack(),
			)
			if !responseStarted(ginContext.Writer) {
				writeProblem(ginContext.Writer, http.StatusInternalServerError, "INTERNAL_ERROR", "服务处理失败", false, nil)
			}
			ginContext.Abort()
		}()

		ginContext.Next()
	}
}

func isAbortHandlerPanic(recovered any) bool {
	err, ok := recovered.(error)
	if !ok {
		return false
	}
	typeOfError := reflect.TypeOf(err)
	return typeOfError != nil && typeOfError.Comparable() && err == http.ErrAbortHandler
}

func responseStarted(writer gin.ResponseWriter) bool {
	tracked, ok := writer.(*responseTracker)
	return ok && tracked.started
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
