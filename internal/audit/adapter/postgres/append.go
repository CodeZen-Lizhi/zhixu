package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/CodeZen-Lizhi/zhixu/internal/audit/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	reservedOutcomeKey       = "audit_outcome"
	reservedErrorCodeKey     = "audit_error_code"
	reservedSchemaVersionKey = "audit_schema_version"
)

const appendAuditSQL = `
	INSERT INTO ops.audit_event(
		id,workspace_id,actor_type,actor_ref,action,resource_type,resource_ref,
		idempotency_key,correlation,payload,occurred_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11::timestamptz)
	ON CONFLICT (workspace_id,idempotency_key) DO NOTHING
	RETURNING id::text,workspace_id::text,actor_type,actor_ref,action,resource_type,resource_ref,
	          idempotency_key,correlation::text,payload::text,occurred_at`

// AppendTx 在调用方事务中追加或精确重放一个 Audit 事件；不会提交或回滚事务。
func (store *Store) AppendTx(ctx context.Context, transaction any, event domain.Event) (domain.Event, bool, error) {
	if store == nil || isNilDB(store.db) {
		return domain.Event{}, false, domainErrorUnavailable(errors.New("audit store is unavailable"))
	}
	if ctx == nil {
		return domain.Event{}, false, domainErrorInvalid(domain.ErrorCodeInvalid, errors.New("audit append context is nil"))
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || isNilTx(tx) {
		return domain.Event{}, false, foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeTransactionUnavailable, true, errors.New("audit append transaction is unavailable"))
	}
	redacted, err := event.Redacted()
	if err != nil {
		return domain.Event{}, false, err
	}
	correlationJSON, payloadJSON, err := encodeAuditJSON(redacted)
	if err != nil {
		return domain.Event{}, false, err
	}

	// PostgreSQL UNIQUE 对 NULL workspace 不去重，因此所有幂等键先取得
	// transaction-scoped advisory lock，再用 IS NOT DISTINCT FROM 精确查询。
	lockScope := redacted.IdempotencyKey
	if redacted.WorkspaceID != nil {
		// Workspace IDs are canonical fixed-width UUIDs, so the printable separator
		// is unambiguous and remains valid PostgreSQL text input.
		lockScope = string(*redacted.WorkspaceID) + ":" + lockScope
	}
	if _, lockErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockScope); lockErr != nil {
		return domain.Event{}, false, classifyStoreError(lockErr)
	}
	if existing, found, lookupErr := loadByIdempotency(ctx, tx, redacted.WorkspaceID, redacted.IdempotencyKey); lookupErr != nil {
		return domain.Event{}, false, lookupErr
	} else if found {
		if !domain.EqualBinding(existing, redacted) {
			return domain.Event{}, false, appendConflict()
		}
		return existing, true, nil
	}

	created, err := scanEvent(tx.QueryRow(ctx, appendAuditSQL,
		string(redacted.ID), optionalWorkspaceValue(redacted.WorkspaceID), string(redacted.ActorType),
		optionalText(redacted.ActorRef), redacted.Action, optionalText(redacted.ResourceType),
		optionalText(redacted.ResourceRef), redacted.IdempotencyKey, correlationJSON, payloadJSON,
		redacted.OccurredAt,
	))
	if err == nil {
		if created.ID != redacted.ID {
			return domain.Event{}, false, corruptEvent(errors.New("audit insert returned a different event id"))
		}
		if !domain.EqualBinding(created, redacted) {
			return domain.Event{}, false, appendConflict()
		}
		return created, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, false, classifyAppendFailure(err)
	}
	existing, found, lookupErr := loadByIdempotency(ctx, tx, redacted.WorkspaceID, redacted.IdempotencyKey)
	if lookupErr != nil {
		return domain.Event{}, false, lookupErr
	}
	if !found || !domain.EqualBinding(existing, redacted) {
		return domain.Event{}, false, appendConflict()
	}
	return existing, true, nil
}

func loadByIdempotency(ctx context.Context, tx pgx.Tx, workspaceID *foundation.ID, key string) (domain.Event, bool, error) {
	var workspace any
	if workspaceID != nil {
		workspace = string(*workspaceID)
	}
	rows, err := tx.Query(ctx, auditSelect+`
		WHERE workspace_id IS NOT DISTINCT FROM $1::uuid AND idempotency_key=$2
		ORDER BY occurred_at,id`, workspace, key)
	if err != nil {
		return domain.Event{}, false, classifyStoreError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Event{}, false, classifyStoreError(err)
		}
		return domain.Event{}, false, nil
	}
	event, err := scanEvent(rows)
	if err != nil {
		return domain.Event{}, false, err
	}
	if rows.Next() {
		return domain.Event{}, false, corruptEvent(errors.New("audit idempotency key has duplicate rows"))
	}
	if err := rows.Err(); err != nil {
		return domain.Event{}, false, classifyStoreError(err)
	}
	return event, true, nil
}

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
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, domain.ErrorCodeInvalid, false, errors.New("audit database constraint rejected event"))
		case "23505":
			return appendConflict()
		case "40001", "40P01":
			return foundation.NewError(foundation.ErrorRetryableFailure, domain.ErrorCodeUnavailable, true, errors.New("audit append transaction should be retried"))
		}
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

func isNilTx(tx pgx.Tx) bool {
	if tx == nil {
		return true
	}
	value := reflect.ValueOf(tx)
	return (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil()
}
