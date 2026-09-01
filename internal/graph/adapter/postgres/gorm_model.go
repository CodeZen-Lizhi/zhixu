package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

// graphJSONB binds one validated JSON document as text so pgx stdlib does not
// infer bytea from a raw byte slice.
type graphJSONB []byte

func (value graphJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("graph JSONB value is invalid")
	}
	return string(value), nil
}

func (value *graphJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("graph JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("graph JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted graph JSONB value is invalid")
	}
	*value = graphJSONB(raw)
	return nil
}

type graphNullableJSONB struct {
	JSON  graphJSONB
	Valid bool
}

func (value graphNullableJSONB) Value() (driver.Value, error) {
	if !value.Valid {
		return nil, nil
	}
	return value.JSON.Value()
}

func (value *graphNullableJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("graph nullable JSONB destination is nil")
	}
	if source == nil {
		value.JSON = nil
		value.Valid = false
		return nil
	}
	if err := value.JSON.Scan(source); err != nil {
		return err
	}
	value.Valid = true
	return nil
}

// graphStringArray is a single database/sql value for PostgreSQL text/UUID
// arrays and a strict scanner for array projections.
type graphStringArray []string

func (value graphStringArray) Value() (driver.Value, error) {
	return pq.Array([]string(value)).Value()
}

func (value *graphStringArray) Scan(source any) error {
	if value == nil {
		return errors.New("graph array destination is nil")
	}
	if source == nil {
		return errors.New("persisted graph array value is null")
	}
	var values pq.StringArray
	if err := values.Scan(source); err != nil {
		return fmt.Errorf("scan graph array: %w", err)
	}
	*value = append((*value)[:0], []string(values)...)
	return nil
}
