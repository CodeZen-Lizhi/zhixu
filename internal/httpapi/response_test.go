package httpapi

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsBodyBeyondLimitEvenWhenPrefixIsValid(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"valid"}`+strings.Repeat(" ", (1<<20)+1)))
	var target struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(request, &target); err == nil {
		t.Fatalf("oversized request was accepted: %+v", target)
	}
}

func TestDecodeJSONEnforcesExactByteBoundary(t *testing.T) {
	const prefix = `{"name":"`
	const suffix = `"}`
	payloadBytes := maxJSONRequestBytes - len(prefix) - len(suffix)
	bodyAtLimit := prefix + strings.Repeat("x", payloadBytes) + suffix
	request := httptest.NewRequest("POST", "/", strings.NewReader(bodyAtLimit))
	var target struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(request, &target); err != nil || len(target.Name) != payloadBytes {
		t.Fatalf("request at exact limit was rejected: decoded_bytes=%d err=%v", len(target.Name), err)
	}

	bodyBeyondLimit := prefix + strings.Repeat("x", payloadBytes+1) + suffix
	request = httptest.NewRequest("POST", "/", strings.NewReader(bodyBeyondLimit))
	if err := DecodeJSON(request, &target); err == nil {
		t.Fatal("request one byte beyond the limit was accepted")
	}
}

func TestDecodeJSONRejectsInvalidUTF8AndUnpairedSurrogate(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "invalid utf8 byte", body: append(append([]byte(`{"name":"`), 0xff), []byte(`"}`)...)},
		{name: "unpaired high surrogate", body: []byte(`{"name":"\uD800"}`)},
		{name: "unpaired low surrogate", body: []byte(`{"name":"\uDC00"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", bytes.NewReader(test.body))
			var target struct {
				Name string `json:"name"`
			}
			if err := DecodeJSON(request, &target); err == nil {
				t.Fatalf("invalid JSON text was accepted: %q", target.Name)
			}
		})
	}
}

func TestDecodeJSONAcceptsValidSurrogatePair(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"\uD83D\uDE00"}`))
	var target struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(request, &target); err != nil || target.Name != "😀" {
		t.Fatalf("valid surrogate pair was rejected: name=%q err=%v", target.Name, err)
	}
}
