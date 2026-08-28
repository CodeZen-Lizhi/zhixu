package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// memoryJSONB keeps JSON values as one database/sql bind parameter. Returning
// a string avoids pgx stdlib interpreting a raw byte slice as bytea.
type memoryJSONB []byte

func (value memoryJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("memory JSONB value is invalid")
	}
	return string(value), nil
}

func (value *memoryJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("memory JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("memory JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted memory JSONB value is invalid")
	}
	*value = memoryJSONB(raw)
	return nil
}
