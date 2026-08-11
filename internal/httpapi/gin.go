package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GinHandler adapts a standard-library handler to Gin while preserving the
// request path-value and route-pattern contracts used by the HTTP layer.
func GinHandler(handler http.HandlerFunc) gin.HandlerFunc {
	wrapped := gin.WrapF(handler)
	return func(context *gin.Context) {
		for _, parameter := range context.Params {
			context.Request.SetPathValue(parameter.Key, parameter.Value)
		}
		context.Request.Pattern = CanonicalRoutePattern(context.FullPath())
		wrapped(context)
	}
}

// CanonicalRoutePattern converts Gin wildcards to the brace form used by
// OpenAPI, request logs, and the previous HTTP router.
func CanonicalRoutePattern(pattern string) string {
	segments := strings.Split(pattern, "/")
	for index, segment := range segments {
		if len(segment) > 1 && (segment[0] == ':' || segment[0] == '*') {
			segments[index] = "{" + segment[1:] + "}"
		}
	}
	return strings.Join(segments, "/")
}
