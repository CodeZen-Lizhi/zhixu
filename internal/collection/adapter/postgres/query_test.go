package postgres

import (
	"strings"
	"testing"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

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
