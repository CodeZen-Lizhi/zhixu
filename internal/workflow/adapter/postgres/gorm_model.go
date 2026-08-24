package workflowpostgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// workflowJSONB keeps one validated JSON value in one database/sql bind.
// Returning string prevents pgx stdlib from treating []byte as bytea.
type workflowJSONB []byte

func (value workflowJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("workflow JSONB value is invalid")
	}
	return string(value), nil
}

func (value *workflowJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("workflow JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("workflow JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted workflow JSONB value is invalid")
	}
	*value = workflowJSONB(raw)
	return nil
}
