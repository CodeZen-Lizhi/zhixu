package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
)

func requestInvalid(message string) error { return domain.InvalidError(message) }

func unavailable(message string) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeUnavailable, true, errors.New(message))
}

func resultInconsistent(message string) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeResultInconsistent, false, errors.New(message))
}
