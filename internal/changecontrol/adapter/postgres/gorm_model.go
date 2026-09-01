package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// changeControlJSONB binds one validated JSON document. Returning string keeps
// pgx stdlib from inferring bytea for a raw byte slice.
type changeControlJSONB []byte

func (value changeControlJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("change control JSONB value is invalid")
	}
	return string(value), nil
}

func (value *changeControlJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("change control JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("change control JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted change control JSONB value is invalid")
	}
	*value = changeControlJSONB(raw)
	return nil
}
