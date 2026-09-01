package postgres

import (
	"reflect"
	"strings"
	"testing"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRenderGORMPositionalPreservesRepeatedAndMultiDigitMarkerOrder(t *testing.T) {
	t.Parallel()
	args := []any{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	query := "SELECT $10::text, $2::text, $10::text, $1::text, $3::text, $4::text, $5::text, $6::text, $7::text, $8::text, $9::text"

	rendered, bound, err := renderGORMPositional(query, args)
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := "SELECT ?::text, ?::text, ?::text, ?::text, ?::text, ?::text, ?::text, ?::text, ?::text, ?::text, ?::text"
	if rendered != wantQuery {
		t.Fatalf("rendered=%q want=%q", rendered, wantQuery)
	}
	wantBound := []any{"ten", "two", "ten", "one", "three", "four", "five", "six", "seven", "eight", "nine"}
	if !reflect.DeepEqual(bound, wantBound) {
		t.Fatalf("bound=%#v want=%#v", bound, wantBound)
	}
}

func TestRenderGORMPositionalRejectsInvalidBindings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "zero marker", query: "SELECT $0", args: []any{"one"}},
		{name: "out of range marker", query: "SELECT $2", args: []any{"one"}},
		{name: "malformed marker", query: "SELECT $", args: nil},
		{name: "unused argument", query: "SELECT $1", args: []any{"one", "two"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := renderGORMPositional(test.query, test.args); err == nil {
				t.Fatal("expected invalid positional bindings to fail")
			}
		})
	}
}

func TestBuildKeysetPredicatePreservesNullsLastAcrossDirections(t *testing.T) {
	t.Parallel()
	for _, direction := range []string{"ASC", "DESC"} {
		direction := direction
		t.Run(direction, func(t *testing.T) {
			t.Parallel()
			sortTerms := []collectionapp.SortExpression{
				{Column: "item.confidence", Direction: direction},
				{Column: "item.object_type", Direction: "ASC"},
				{Column: "item.id", Direction: "ASC"},
			}
			cursor := collectionapp.ResultCursor{
				LastObjectType: "TOPIC",
				LastID:         foundation.ID("20000000-0000-4000-8000-000000000001"),
				LastSortValues: []*string{nil, stringPointer("TOPIC"), stringPointer("20000000-0000-4000-8000-000000000001")},
			}
			predicate, args, err := buildKeysetPredicate(sortTerms, cursor, 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(args) != 3 || args[0] != nil || args[1] != "TOPIC" {
				t.Fatalf("args=%#v", args)
			}
			for _, expected := range []string{
				"item.confidence IS NOT DISTINCT FROM $3::double precision",
				"item.object_type IS NOT DISTINCT FROM $4::text",
				"item.id > $5::text",
			} {
				if !strings.Contains(predicate, expected) {
					t.Fatalf("predicate=%q missing=%q", predicate, expected)
				}
			}
		})
	}
}

func TestBuildKeysetPredicateMovesFromNonNullValueIntoNullTail(t *testing.T) {
	t.Parallel()
	sortTerms := []collectionapp.SortExpression{
		{Column: "item.confidence", Direction: "DESC"},
		{Column: "item.object_type", Direction: "ASC"},
		{Column: "item.id", Direction: "ASC"},
	}
	cursor := collectionapp.ResultCursor{
		LastObjectType: "CLAIM",
		LastID:         foundation.ID("20000000-0000-4000-8000-000000000001"),
		LastSortValues: []*string{stringPointer("0.5"), stringPointer("CLAIM"), stringPointer("20000000-0000-4000-8000-000000000001")},
	}
	predicate, _, err := buildKeysetPredicate(sortTerms, cursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(predicate, "item.confidence < $1::double precision") || !strings.Contains(predicate, "$1::double precision IS NOT NULL AND item.confidence IS NULL") {
		t.Fatalf("predicate=%q", predicate)
	}
}
