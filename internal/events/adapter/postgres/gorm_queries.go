package postgres

import (
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

const (
	gormEventWatermarkSQL = `
		SELECT COALESCE(MAX(seq),0)
		FROM ops.server_event
		WHERE workspace_id=?::uuid`

	gormEventEarliestRetainedSQL = `
		SELECT MIN(seq)
		FROM ops.server_event
		WHERE workspace_id=?::uuid AND expires_at > CURRENT_TIMESTAMP`

	gormEventListAfterSQL = eventSelect + `
		WHERE workspace_id=?::uuid AND seq>? AND expires_at > CURRENT_TIMESTAMP
		ORDER BY seq ASC
		LIMIT ?`

	gormAppendEventSQL = `
		INSERT INTO ops.server_event(
			workspace_id,conversation_id,workflow_run_id,event_type,resource_ref,resource_version,
			payload_summary,schema_version,source_event_ref,occurred_at,expires_at
		) VALUES(?::uuid,?::uuid,?::uuid,?,?,?,?::jsonb,?,?,?::timestamptz,?::timestamptz + INTERVAL '24 hours')
		ON CONFLICT (workspace_id,source_event_ref) DO NOTHING
		RETURNING seq,workspace_id::text,conversation_id::text,workflow_run_id::text,
		          event_type,resource_ref,resource_version,payload_summary::text,
		          schema_version,source_event_ref,occurred_at,expires_at`

	gormEventBySourceSQL = eventSelect + `
		WHERE workspace_id=?::uuid AND source_event_ref=?`
)

func gormEventRawRow(database *gorm.DB, query string, args ...any) (scanner, error) {
	if database == nil {
		return nil, errors.New("SSE GORM database is nil")
	}
	statement := database.Raw(query, args...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("SSE GORM query returned a nil row")
	}
	return row, nil
}

func gormEventRawRows(database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if database == nil {
		return nil, errors.New("SSE GORM database is nil")
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
		return nil, errors.New("SSE GORM query returned nil rows")
	}
	return rows, nil
}
