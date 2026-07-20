package strictjson

import (
	"errors"
	"strings"
	"testing"
)

type fixture struct {
	Name   string `json:"name"`
	Nested struct {
		State string   `json:"state"`
		Items []string `json:"items"`
	} `json:"nested"`
}

func validateFixture(value fixture) error {
	if value.Name == "" || value.Nested.State != "READY" || len(value.Nested.Items) == 0 {
		return errors.New("fixture is invalid")
	}
	return nil
}

func TestDecodeObjectAcceptsOneBoundedDocument(t *testing.T) {
	raw := []byte(`{"name":"answer","nested":{"state":"READY","items":["one","two"]}}`)
	got, err := DecodeObject(raw, DefaultLimits(), validateFixture)
	if err != nil {
		t.Fatalf("DecodeObject() error = %v", err)
	}
	if got.Name != "answer" || got.Nested.State != "READY" || len(got.Nested.Items) != 2 {
		t.Fatalf("DecodeObject() = %#v", got)
	}
}

func TestDecodeObjectRejectsInvalidDocuments(t *testing.T) {
	tests := map[string][]byte{
		"invalid utf8":        append([]byte(`{"name":"`), 0xff, '"', '}'),
		"unpaired high":       []byte(`{"name":"\uD800"}`),
		"unpaired low":        []byte(`{"name":"\uDC00"}`),
		"duplicate root":      []byte(`{"name":"a","name":"b","nested":{"state":"READY","items":["x"]}}`),
		"duplicate nested":    []byte(`{"name":"a","nested":{"state":"READY","state":"READY","items":["x"]}}`),
		"unknown root":        []byte(`{"name":"a","unknown":1,"nested":{"state":"READY","items":["x"]}}`),
		"unknown nested":      []byte(`{"name":"a","nested":{"state":"READY","extra":1,"items":["x"]}}`),
		"trailing document":   []byte(`{"name":"a","nested":{"state":"READY","items":["x"]}} {}`),
		"wrong field type":    []byte(`{"name":7,"nested":{"state":"READY","items":["x"]}}`),
		"top-level array":     []byte(`[]`),
		"unterminated object": []byte(`{"name":"a"`),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeObject(raw, DefaultLimits(), validateFixture)
			if kind, ok := KindOf(err); !ok || kind != ErrorKindInvalid {
				t.Fatalf("KindOf(%v) = %q, %t", err, kind, ok)
			}
		})
	}
}

func TestDecodeObjectAcceptsPairedUnicodeSurrogates(t *testing.T) {
	type unicodeFixture struct {
		Name string `json:"name"`
	}
	value, err := DecodeObject[unicodeFixture]([]byte(`{"name":"\uD83D\uDE00"}`), DefaultLimits(), nil)
	if err != nil || value.Name != "\U0001F600" {
		t.Fatalf("DecodeObject() = %#v, %v", value, err)
	}
}

func TestDecodeObjectRejectsEmptyDocumentAsLimitExceeded(t *testing.T) {
	_, err := DecodeObject[map[string]any](nil, DefaultLimits(), nil)
	if kind, ok := KindOf(err); !ok || kind != ErrorKindLimitExceeded {
		t.Fatalf("KindOf(%v) = %q, %t", err, kind, ok)
	}
}

func TestDecodeObjectEnforcesEveryResourceLimit(t *testing.T) {
	tests := []struct {
		name   string
		raw    []byte
		limits Limits
	}{
		{name: "document", raw: []byte(`{"name":"a","nested":{"state":"READY","items":["x"]}}`), limits: Limits{MaxDocumentBytes: 4, MaxDepth: 16, MaxStringBytes: 100, MaxArrayItems: 10, MaxObjectFields: 10}},
		{name: "depth", raw: []byte(`{"name":"a","nested":{"state":"READY","items":["x"]}}`), limits: Limits{MaxDocumentBytes: 1000, MaxDepth: 1, MaxStringBytes: 100, MaxArrayItems: 10, MaxObjectFields: 10}},
		{name: "key string", raw: []byte(`{"long":"a"}`), limits: Limits{MaxDocumentBytes: 1000, MaxDepth: 16, MaxStringBytes: 3, MaxArrayItems: 10, MaxObjectFields: 10}},
		{name: "value string", raw: []byte(`{"a":"long"}`), limits: Limits{MaxDocumentBytes: 1000, MaxDepth: 16, MaxStringBytes: 3, MaxArrayItems: 10, MaxObjectFields: 10}},
		{name: "array", raw: []byte(`{"items":["x","y"]}`), limits: Limits{MaxDocumentBytes: 1000, MaxDepth: 16, MaxStringBytes: 100, MaxArrayItems: 1, MaxObjectFields: 10}},
		{name: "object fields", raw: []byte(`{"name":"a","nested":{}}`), limits: Limits{MaxDocumentBytes: 1000, MaxDepth: 16, MaxStringBytes: 100, MaxArrayItems: 10, MaxObjectFields: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeObject[map[string]any](test.raw, test.limits, nil)
			if kind, ok := KindOf(err); !ok || kind != ErrorKindLimitExceeded {
				t.Fatalf("KindOf(%v) = %q, %t", err, kind, ok)
			}
		})
	}
}

func TestDecodeObjectRejectsInvalidLimits(t *testing.T) {
	tests := map[string]Limits{
		"document":      {MaxDocumentBytes: 1, MaxDepth: 1, MaxStringBytes: 1, MaxArrayItems: 0, MaxObjectFields: 1},
		"depth":         {MaxDocumentBytes: 2, MaxDepth: 0, MaxStringBytes: 1, MaxArrayItems: 0, MaxObjectFields: 1},
		"string":        {MaxDocumentBytes: 2, MaxDepth: 1, MaxStringBytes: 0, MaxArrayItems: 0, MaxObjectFields: 1},
		"array":         {MaxDocumentBytes: 2, MaxDepth: 1, MaxStringBytes: 1, MaxArrayItems: -1, MaxObjectFields: 1},
		"object fields": {MaxDocumentBytes: 2, MaxDepth: 1, MaxStringBytes: 1, MaxArrayItems: 0, MaxObjectFields: 0},
	}
	for name, limits := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeObject[map[string]any]([]byte(`{}`), limits, nil)
			if kind, ok := KindOf(err); !ok || kind != ErrorKindInvalid {
				t.Fatalf("KindOf(%v) = %q, %t", err, kind, ok)
			}
		})
	}
}

func TestDecodeObjectPreservesValidatorErrorAndInput(t *testing.T) {
	raw := []byte(`{"name":"answer","nested":{"state":"READY","items":["one"]}}`)
	wantRaw := append([]byte(nil), raw...)
	wantErr := errors.New("rejected by validator")
	_, err := DecodeObject(raw, DefaultLimits(), func(fixture) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("DecodeObject() error = %v", err)
	}
	if string(raw) != string(wantRaw) {
		t.Fatal("DecodeObject() mutated input")
	}
	if _, ok := KindOf(err); ok {
		t.Fatal("validator error was reclassified")
	}
}

func TestDefaultLimitsAreFinite(t *testing.T) {
	limits := DefaultLimits()
	if limits.MaxDocumentBytes <= 0 || limits.MaxDocumentBytes > 1024*1024 || limits.MaxDepth <= 0 ||
		limits.MaxStringBytes <= 0 || limits.MaxArrayItems <= 0 || limits.MaxObjectFields <= 0 {
		t.Fatalf("DefaultLimits() = %#v", limits)
	}
	if strings.Contains(strings.ToLower(string(ErrorKindInvalid)), "secret") ||
		strings.Contains(strings.ToLower(string(ErrorKindLimitExceeded)), "secret") {
		t.Fatal("error kinds must not carry sensitive data")
	}
}
