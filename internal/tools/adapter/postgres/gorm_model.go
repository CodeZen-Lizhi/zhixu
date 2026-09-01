package postgres

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// gormToolsJSONB preserves a validated JSON value in one database/sql bind.
// Returning string ensures PostgreSQL receives jsonb rather than bytea.
type gormToolsJSONB []byte

func (value gormToolsJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("Tools JSONB value is invalid")
	}
	return string(value), nil
}

func (value *gormToolsJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("Tools JSONB destination is nil")
	}
	raw, err := gormToolsCopyBytes(source)
	if err != nil {
		return fmt.Errorf("Tools JSONB source: %w", err)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted Tools JSONB value is invalid")
	}
	*value = gormToolsJSONB(raw)
	return nil
}

// gormToolsBytes copies bytea values so a scan buffer cannot escape a row.
type gormToolsBytes []byte

func (value *gormToolsBytes) Scan(source any) error {
	if value == nil {
		return errors.New("Tools bytea destination is nil")
	}
	raw, err := gormToolsCopyBytes(source)
	if err != nil {
		return err
	}
	*value = gormToolsBytes(raw)
	return nil
}

func gormToolsCopyBytes(source any) ([]byte, error) {
	switch typed := source.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(typed), nil
	case []byte:
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("unsupported bytea source type %T", source)
	}
}

func gormToolsOptionalBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func gormJSONB(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return gormToolsJSONB(value)
}

func gormToolsNullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	utc := value.Time.UTC()
	return &utc
}

func gormToolsUTC(value time.Time) time.Time {
	return value.UTC()
}

type gormToolsVersion struct {
	Version int64
}
