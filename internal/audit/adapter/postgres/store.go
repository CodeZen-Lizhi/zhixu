package postgres

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Truncate(time.Microsecond)
}

func auditListArguments(query domain.ListQuery) (any, any, error) {
	if query.Limit <= 0 || query.Limit > domain.MaxListLimit {
		return nil, nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit list limit is invalid"))
	}
	if query.Before.IsZero() != (query.BeforeID == "") {
		return nil, nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit list cursor is incomplete"))
	}
	var workspace any
	if query.WorkspaceID != nil {
		parsed, err := foundation.ParseID(string(*query.WorkspaceID))
		if err != nil || parsed != *query.WorkspaceID {
			return nil, nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit workspace id is invalid"))
		}
		workspace = string(*query.WorkspaceID)
	}
	var beforeID any
	if query.BeforeID != "" {
		parsed, err := foundation.ParseID(string(query.BeforeID))
		if err != nil || parsed != query.BeforeID {
			return nil, nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit list cursor id is invalid"))
		}
		beforeID = string(query.BeforeID)
	}
	return workspace, beforeID, nil
}
