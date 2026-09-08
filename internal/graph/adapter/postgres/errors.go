package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func classify(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeQueryTimeout, true, err)
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, graphdomain.ErrorCodeQueryCanceled, false, err)
	}
	switch platformpostgres.SQLState(err) {
	case "57014":
		if platformpostgres.IsStatementTimeout(err) {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeQueryTimeout, true, err)
		}
		return foundation.NewError(foundation.ErrorNonRetryableFailure, graphdomain.ErrorCodeQueryCanceled, false, err)
	case "40001", "40P01", "55P03", "08000", "08003", "08006", "57P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, graphdomain.ErrorCodeDependencyUnavailable, true, err)
	case "23502", "23503", "23514":
		return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent, false, err)
	}
	return unavailable(err)
}

func classifyProjectionScan(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return classify(err)
	}
	if platformpostgres.SQLState(err) != "" {
		return classify(err)
	}
	return inconsistent(err)
}

func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable, true, err)
}

func notFound(code string, err error) error {
	return foundation.NewError(foundation.ErrorNotFound, code, false, err)
}

func inconsistent(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent, false, err)
}
