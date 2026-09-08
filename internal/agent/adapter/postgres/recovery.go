package postgres

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func validateRecoveryQuery(query application.UnknownRecoveryQuery) error {
	if query.Before.IsZero() || query.At.IsZero() || query.At.Before(query.Before) || query.Limit <= 0 || query.Limit > 500 {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeModelRunInvalid, false, errors.New("agent recovery query is invalid"))
	}
	return nil
}
