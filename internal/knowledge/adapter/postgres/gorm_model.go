package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// knowledgeJSONB binds one validated JSON document. Returning string keeps
// pgx stdlib from inferring bytea for a raw byte slice.
type knowledgeJSONB []byte

func (value knowledgeJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("knowledge JSONB value is invalid")
	}
	return string(value), nil
}

func (value *knowledgeJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("knowledge JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("knowledge JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted knowledge JSONB value is invalid")
	}
	*value = knowledgeJSONB(raw)
	return nil
}
