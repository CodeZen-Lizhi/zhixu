package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type strictFixture struct {
	Name   string `json:"name"`
	Nested struct {
		State string   `json:"state"`
		Items []string `json:"items"`
	} `json:"nested"`
}

func validateStrictFixture(value strictFixture) error {
	if value.Name == "" || value.Nested.State != "READY" || len(value.Nested.Items) == 0 {
		return invalid(ErrorCodeSchemaInvalid, "strict fixture is invalid")
	}
	return nil
}

func TestDecodeStrictAcceptsOneBoundedDocument(t *testing.T) {
	raw := []byte(`{"name":"answer","nested":{"state":"READY","items":["one","two"]}}`)
	got, err := DecodeStrict(raw, DefaultDecodeLimits(), validateStrictFixture)
	if err != nil {
		t.Fatalf("decode strict: %v", err)
	}
	if got.Name != "answer" || got.Nested.State != "READY" || len(got.Nested.Items) != 2 {
		t.Fatalf("decoded=%#v", got)
	}
}

func TestDecodeStrictRejectsMalformedShapeAndSchema(t *testing.T) {
	tests := map[string][]byte{
		"invalid utf8":        append([]byte(`{"name":"`), 0xff, '"', '}'),
		"duplicate root":      []byte(`{"name":"a","name":"b","nested":{"state":"READY","items":["x"]}}`),
		"duplicate nested":    []byte(`{"name":"a","nested":{"state":"READY","state":"READY","items":["x"]}}`),
		"unknown root":        []byte(`{"name":"a","unknown":1,"nested":{"state":"READY","items":["x"]}}`),
		"unknown nested":      []byte(`{"name":"a","nested":{"state":"READY","extra":1,"items":["x"]}}`),
		"trailing document":   []byte(`{"name":"a","nested":{"state":"READY","items":["x"]}} {}`),
		"wrong field type":    []byte(`{"name":7,"nested":{"state":"READY","items":["x"]}}`),
		"unsupported enum":    []byte(`{"name":"a","nested":{"state":"UNKNOWN","items":["x"]}}`),
		"missing field":       []byte(`{"name":"a","nested":{"state":"READY"}}`),
		"top-level array":     []byte(`[]`),
		"unterminated object": []byte(`{"name":"a"`),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeStrict(raw, DefaultDecodeLimits(), validateStrictFixture)
			if err == nil {
				t.Fatal("invalid structured output accepted")
			}
			if code := errorCode(err); code != ErrorCodeStructuredOutputInvalid && code != ErrorCodeSchemaInvalid {
				t.Fatalf("code=%s err=%v", code, err)
			}
		})
	}
}

func TestDecodeStrictEnforcesEveryResourceLimit(t *testing.T) {
	base := DefaultDecodeLimits()
	tests := []struct {
		name   string
		raw    []byte
		limits DecodeLimits
	}{
		{name: "document", raw: []byte(`{"name":"a","nested":{"state":"READY","items":["x"]}}`), limits: DecodeLimits{MaxDocumentBytes: 4, MaxDepth: 16, MaxStringBytes: 100, MaxArrayItems: 10, MaxObjectFields: 10}},
		{name: "depth", raw: []byte(`{"name":"a","nested":{"state":"READY","items":["x"]}}`), limits: DecodeLimits{MaxDocumentBytes: 1000, MaxDepth: 1, MaxStringBytes: 100, MaxArrayItems: 10, MaxObjectFields: 10}},
		{name: "string", raw: []byte(`{"name":"long","nested":{"state":"READY","items":["x"]}}`), limits: DecodeLimits{MaxDocumentBytes: 1000, MaxDepth: 16, MaxStringBytes: 3, MaxArrayItems: 10, MaxObjectFields: 10}},
		{name: "array", raw: []byte(`{"name":"a","nested":{"state":"READY","items":["x","y"]}}`), limits: DecodeLimits{MaxDocumentBytes: 1000, MaxDepth: 16, MaxStringBytes: 100, MaxArrayItems: 1, MaxObjectFields: 10}},
		{name: "object fields", raw: []byte(`{"name":"a","nested":{"state":"READY","items":["x"]}}`), limits: DecodeLimits{MaxDocumentBytes: 1000, MaxDepth: 16, MaxStringBytes: 100, MaxArrayItems: 10, MaxObjectFields: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeStrict(test.raw, test.limits, validateStrictFixture)
			if code := errorCode(err); code != ErrorCodeStructuredOutputLimitExceeded {
				t.Fatalf("code=%s err=%v", code, err)
			}
		})
	}

	invalidLimits := base
	invalidLimits.MaxDepth = 0
	if _, err := DecodeStrict([]byte(`{}`), invalidLimits, validateStrictFixture); errorCode(err) != ErrorCodeStructuredOutputInvalid {
		t.Fatalf("invalid limits err=%v", err)
	}
}

func TestDecodeStrictDoesNotMutateInput(t *testing.T) {
	raw := []byte(`{"name":"answer","nested":{"state":"READY","items":["one"]}}`)
	want := append([]byte(nil), raw...)
	if _, err := DecodeStrict(raw, DefaultDecodeLimits(), validateStrictFixture); err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) {
		t.Fatal("decoder mutated input")
	}
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var typed *foundation.Error
	if !errors.As(err, &typed) {
		return ""
	}
	return typed.Code
}

func TestDefaultDecodeLimitsAreFinite(t *testing.T) {
	limits := DefaultDecodeLimits()
	if limits != (DecodeLimits{
		MaxDocumentBytes: 256 * 1024,
		MaxDepth:         16,
		MaxStringBytes:   32 * 1024,
		MaxArrayItems:    500,
		MaxObjectFields:  128,
	}) {
		t.Fatalf("default compatibility limits=%#v", limits)
	}
	if limits.MaxDocumentBytes <= 0 || limits.MaxDocumentBytes > 1024*1024 || limits.MaxDepth <= 0 ||
		limits.MaxStringBytes <= 0 || limits.MaxArrayItems <= 0 || limits.MaxObjectFields <= 0 {
		t.Fatalf("limits=%#v", limits)
	}
	if strings.Contains(strings.ToLower(ErrorCodeStructuredOutputInvalid), "secret") {
		t.Fatal("stable error code must not carry sensitive data")
	}
}
