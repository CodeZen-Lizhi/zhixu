package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// eventJSONB keeps one validated JSON document in one database/sql bind.
// Returning string prevents pgx stdlib from treating []byte as bytea.
type eventJSONB []byte

func (value eventJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("SSE JSONB value is invalid")
	}
	return string(value), nil
}

func (value *eventJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("SSE JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("SSE JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted SSE JSONB value is invalid")
	}
	*value = eventJSONB(raw)
	return nil
}
