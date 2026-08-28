package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// auditJSONB keeps canonical JSON as one database/sql bind parameter. A
// string value prevents pgx stdlib from treating a raw byte slice as bytea.
type auditJSONB []byte

func (value auditJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("audit JSONB value is invalid")
	}
	return string(value), nil
}

func (value *auditJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("audit JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("audit JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted audit JSONB value is invalid")
	}
	*value = auditJSONB(raw)
	return nil
}
