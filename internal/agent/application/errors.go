package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func applicationError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	if cause == nil {
		cause = errors.New("agent application failure")
	}
	return foundation.NewError(kind, code, retryable, cause)
}
