// Package structured validates model output and permits one bounded repair.
package structured

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var ErrRepairExhausted = errors.New("structured output repair exhausted")

// Validator checks decoded JSON against the project-owned schema.
type Validator func(json.RawMessage) error

// Repairer receives invalid output and may return one corrected JSON document.
type Repairer func(invalid json.RawMessage, cause error) ([]byte, error)

// Parse validates JSON and allows at most one repair attempt.
func Parse(raw []byte, validate Validator, repair Repairer) (json.RawMessage, error) {
	valid, err := parse(raw, validate)
	if err == nil {
		return valid, nil
	}
	if repair == nil {
		return nil, ErrRepairExhausted
	}
	fixed, repairErr := repair(append([]byte(nil), raw...), err)
	if repairErr != nil {
		return nil, errors.Join(ErrRepairExhausted, repairErr)
	}
	valid, err = parse(fixed, validate)
	if err != nil {
		return nil, errors.Join(ErrRepairExhausted, err)
	}
	return valid, nil
}

func parse(raw []byte, validate Validator) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	if validate != nil {
		if err := validate(raw); err != nil {
			return nil, err
		}
	}
	return append(json.RawMessage(nil), raw...), nil
}
