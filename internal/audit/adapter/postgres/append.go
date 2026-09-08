package postgres

import (
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

const (
	reservedOutcomeKey       = "audit_outcome"
	reservedErrorCodeKey     = "audit_error_code"
	reservedSchemaVersionKey = "audit_schema_version"
)

func encodeAuditJSON(event domain.Event) ([]byte, []byte, error) {
	metadata := make(map[string]any)
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil || metadata == nil {
		return nil, nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit metadata cannot be decoded"))
	}
	for _, key := range []string{reservedOutcomeKey, reservedErrorCodeKey, reservedSchemaVersionKey} {
		if _, exists := metadata[key]; exists {
			return nil, nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit metadata uses a reserved key"))
		}
	}
	metadata[reservedOutcomeKey] = string(event.Outcome)
	if event.ErrorCode != "" {
		metadata[reservedErrorCodeKey] = event.ErrorCode
	}
	metadata[reservedSchemaVersionKey] = event.SchemaVersion
	payloadJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, nil, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit metadata cannot be encoded"))
	}
	return append([]byte(nil), event.Correlation...), payloadJSON, nil
}

func classifyAppendFailure(err error) error {
	if err == nil {
		return nil
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	switch platformpostgres.SQLState(err) {
	case "23503", "23514":
		return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeInvalid, false, errors.New("audit database constraint rejected event"))
	case "23505":
		return appendConflict()
	case "40001", "40P01":
		return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeUnavailable, true, errors.New("audit append transaction should be retried"))
	}
	return classifyStoreError(err)
}

func optionalWorkspaceValue(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func auditLockScope(event domain.Event) string {
	if event.WorkspaceID == nil {
		return event.IdempotencyKey
	}
	// Workspace IDs are canonical fixed-width UUIDs, so the printable separator
	// is unambiguous and remains valid PostgreSQL text input.
	return string(*event.WorkspaceID) + ":" + event.IdempotencyKey
}
