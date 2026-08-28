package postgres

import (
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

const (
	gormAuditLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`

	gormAppendAuditSQL = `
	INSERT INTO ops.audit_event(
		id,workspace_id,actor_type,actor_ref,action,resource_type,resource_ref,
		idempotency_key,correlation,payload,occurred_at
	) VALUES(?,?::uuid,?,?,?,?,?,?,?::jsonb,?::jsonb,?::timestamptz)
	ON CONFLICT (workspace_id,idempotency_key) DO NOTHING
	RETURNING id::text,workspace_id::text,actor_type,actor_ref,action,resource_type,resource_ref,
	          idempotency_key,correlation::text,payload::text,occurred_at`

	gormAuditByIdempotencySQL = auditSelect + `
	WHERE workspace_id IS NOT DISTINCT FROM ?::uuid AND idempotency_key=?
	ORDER BY occurred_at,id`

	gormAuditGetSQL = auditSelect + ` WHERE id=?`

	gormAuditListSQL = auditSelect + `
	WHERE workspace_id IS NOT DISTINCT FROM ?::uuid
	  AND (?::timestamptz IS NULL OR (occurred_at,id) < (?::timestamptz,?::uuid))
	ORDER BY occurred_at DESC, id DESC
	LIMIT ?`
)

func gormAuditRawRow(database *gorm.DB, query string, args ...any) (scanner, error) {
	if database == nil {
		return nil, errors.New("audit GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("audit GORM query returned a nil row")
	}
	return row, nil
}

func gormAuditRawRows(database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if database == nil {
		return nil, errors.New("audit GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("audit GORM query returned nil rows")
	}
	return rows, nil
}
