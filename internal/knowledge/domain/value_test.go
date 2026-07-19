package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestNormalizeTopicTextUsesNFCUnicodeFoldAndCollapsedWhitespace(t *testing.T) {
	display, normalized, err := NormalizeTopicText("  Cafe\u0301\t知识  ")
	if err != nil {
		t.Fatalf("normalize topic text: %v", err)
	}
	if display != "Café 知识" || normalized != "café 知识" {
		t.Fatalf("unexpected normalization: display=%q normalized=%q", display, normalized)
	}

	upper, folded, err := NormalizeTopicText("Straße")
	if err != nil || upper != "Straße" || folded != "strasse" {
		t.Fatalf("unicode case fold mismatch: display=%q normalized=%q err=%v", upper, folded, err)
	}
}

func TestNormalizeKnowledgeTextRejectsInvalidOrOversizedInput(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		fn    func(string) (string, string, error)
	}{
		{name: "empty topic", value: " \t\n ", fn: NormalizeTopicText},
		{name: "invalid utf8 topic", value: string([]byte{0xff}), fn: NormalizeTopicText},
		{name: "control topic", value: "a\x00b", fn: NormalizeTopicText},
		{name: "oversized topic", value: strings.Repeat("a", MaxTopicNameBytes+1), fn: NormalizeTopicText},
		{name: "oversized statement", value: strings.Repeat("a", MaxStatementBytes+1), fn: NormalizeStatement},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := test.fn(test.value)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != ErrorCodeTextInvalid {
				t.Fatalf("expected %s, got %v", ErrorCodeTextInvalid, err)
			}
		})
	}
}

func TestParseApplicabilityCanonicalizesEquivalentObjectsAndNumbers(t *testing.T) {
	first, err := ParseApplicability(json.RawMessage(`{"z":1.00,"a":[true,{"n":1e2}],"empty":null}`))
	if err != nil {
		t.Fatalf("parse first applicability: %v", err)
	}
	second, err := ParseApplicability(json.RawMessage(` { "empty" : null, "a" : [ true, { "n" : 100.0 } ], "z": 1 } `))
	if err != nil {
		t.Fatalf("parse second applicability: %v", err)
	}
	const want = `{"a":[true,{"n":100}],"empty":null,"z":1}`
	if string(first.CanonicalJSON) != want || string(second.CanonicalJSON) != want {
		t.Fatalf("unexpected canonical json: first=%s second=%s", first.CanonicalJSON, second.CanonicalJSON)
	}
	if first.Hash != second.Hash || first.SchemaVersion != ApplicabilitySchemaV1 {
		t.Fatalf("equivalent applicability must share v1 hash: %#v %#v", first, second)
	}
	if err := ValidateApplicability(first); err != nil {
		t.Fatalf("validate canonical applicability: %v", err)
	}
}

func TestParseApplicabilityRejectsMalformedAmbiguousOrUnsafeJSON(t *testing.T) {
	deep := strings.Repeat(`{"a":`, MaxApplicabilityDepth) + `{}` + strings.Repeat(`}`, MaxApplicabilityDepth)
	for _, value := range []string{
		`[]`,
		`{"a":1,"a":2}`,
		`{"a":1} {"b":2}`,
		`{"a":1e999999}`,
		deep,
		string([]byte{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'}),
		strings.Repeat(" ", MaxApplicabilityBytes) + `{}`,
	} {
		_, err := ParseApplicability(json.RawMessage(value))
		var classified *foundation.Error
		if !errors.As(err, &classified) || classified.Code != ErrorCodeApplicabilityInvalid {
			t.Fatalf("input %q: expected %s, got %v", value, ErrorCodeApplicabilityInvalid, err)
		}
	}
}

func TestParseApplicabilityAllowsExactlyMaximumContainerDepth(t *testing.T) {
	value := strings.Repeat(`{"a":`, MaxApplicabilityDepth) + `1` + strings.Repeat(`}`, MaxApplicabilityDepth)
	if _, err := ParseApplicability(json.RawMessage(value)); err != nil {
		t.Fatalf("exact maximum depth must remain valid: %v", err)
	}
}

func TestValidateApplicabilityDetectsTampering(t *testing.T) {
	value, err := ParseApplicability(json.RawMessage(`{"region":"cn"}`))
	if err != nil {
		t.Fatal(err)
	}
	value.CanonicalJSON = json.RawMessage(`{"region":"us"}`)
	if err := ValidateApplicability(value); err == nil {
		t.Fatal("tampered canonical json must fail closed")
	}
}
