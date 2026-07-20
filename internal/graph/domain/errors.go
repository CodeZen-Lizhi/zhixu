// Package domain 定义 Graph 只读投影的领域合同与纯校验规则。
package domain

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	ErrorCodeRequestInvalid         = "GRAPH_REQUEST_INVALID"
	ErrorCodeCursorInvalid          = "GRAPH_CURSOR_INVALID"
	ErrorCodeCursorStale            = "GRAPH_CURSOR_STALE"
	ErrorCodeNodeNotFound           = "GRAPH_NODE_NOT_FOUND"
	ErrorCodeRelationNotFound       = "GRAPH_RELATION_NOT_FOUND"
	ErrorCodeQueryTimeout           = "GRAPH_QUERY_TIMEOUT"
	ErrorCodeQueryBudgetExceeded    = "GRAPH_QUERY_BUDGET_EXCEEDED"
	ErrorCodeDependencyUnavailable  = "GRAPH_DEPENDENCY_UNAVAILABLE"
	ErrorCodeProjectionInconsistent = "GRAPH_PROJECTION_INCONSISTENT"
)

func invalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeRequestInvalid, false, errors.New(message))
}

func inconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeProjectionInconsistent, false, errors.New(message))
}
