package structured

import (
	"encoding/json"
	"errors"
	"testing"
)

func objectValidator(raw json.RawMessage) error {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if _, ok := v["answer"]; !ok {
		return errors.New("answer required")
	}
	return nil
}

func TestParseValidJSON(t *testing.T) {
	out, err := Parse([]byte(`{"answer":"ok"}`), objectValidator, nil)
	if err != nil || string(out) != `{"answer":"ok"}` {
		t.Fatalf("out=%s err=%v", out, err)
	}
}
func TestParseRepairsOnce(t *testing.T) {
	calls := 0
	out, err := Parse([]byte(`{"bad":1}`), objectValidator, func(_ json.RawMessage, _ error) ([]byte, error) { calls++; return []byte(`{"answer":"fixed"}`), nil })
	if err != nil || calls != 1 || string(out) != `{"answer":"fixed"}` {
		t.Fatalf("out=%s err=%v calls=%d", out, err, calls)
	}
}
func TestParseRepairExhausted(t *testing.T) {
	calls := 0
	_, err := Parse([]byte(`not-json`), objectValidator, func(_ json.RawMessage, _ error) ([]byte, error) { calls++; return []byte(`still bad`), nil })
	if !errors.Is(err, ErrRepairExhausted) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
