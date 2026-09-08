package postgres

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGORMNamedQueryPreservesRepeatedAndMultiDigitMarkerOrder(t *testing.T) {
	t.Parallel()
	values := []any{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	args := make(map[string]any, len(values))
	for index, value := range values {
		args["query"+strconv.Itoa(index+1)] = value
	}
	query := "SELECT (@query10)::text, (@query2)::text, (@query10)::text, (@query1)::text, (@query3)::text, (@query4)::text, (@query5)::text, (@query6)::text, (@query7)::text, (@query8)::text, (@query9)::text"
	database, err := gorm.Open(gormpostgres.New(gormpostgres.Config{DSN: "postgres://localhost/unused"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	statement := database.Raw(query, collectionQueryArguments("10000000-0000-4000-8000-000000000001", args)).Statement
	wantQuery := "SELECT ($1)::text, ($2)::text, ($3)::text, ($4)::text, ($5)::text, ($6)::text, ($7)::text, ($8)::text, ($9)::text, ($10)::text, ($11)::text"
	if statement.SQL.String() != wantQuery {
		t.Fatalf("rendered=%q want=%q", statement.SQL.String(), wantQuery)
	}
	wantBound := []any{"ten", "two", "ten", "one", "three", "four", "five", "six", "seven", "eight", "nine"}
	if !reflect.DeepEqual(statement.Vars, wantBound) {
		t.Fatalf("bound=%#v want=%#v", statement.Vars, wantBound)
	}
}

func TestCollectionQueryRejectsInvalidBindings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		field    string
		operator string
		value    json.RawMessage
	}{
		{name: "unknown registry field", field: "unknown", operator: "EQ", value: json.RawMessage(`"one"`)},
		{name: "unsupported operator", field: "object_type", operator: "RAW", value: json.RawMessage(`"TOPIC"`)},
		{name: "missing value", field: "object_type", operator: "EQ"},
		{name: "invalid typed value", field: "topic_id", operator: "EQ", value: json.RawMessage(`"not-a-uuid"`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{
				Kind: domain.ClauseKindGroup, Operator: "AND", Clauses: []domain.Clause{{
					Kind: domain.ClauseKindPredicate, Field: test.field, Operator: test.operator, Value: test.value,
				}},
			}}
			if _, err := collectionapp.CompileQuery(query); err == nil {
				t.Fatal("expected invalid query binding to fail")
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
			predicate, args, err := buildKeysetPredicate(sortTerms, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if len(args) != 3 || args["cursor1"] != nil || args["cursor2"] != "TOPIC" {
				t.Fatalf("args=%#v", args)
			}
			for _, expected := range []string{
				"item.confidence IS NOT DISTINCT FROM (@cursor1)::double precision",
				"item.object_type IS NOT DISTINCT FROM (@cursor2)::text",
				"item.id > (@cursor3)::text",
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
	predicate, _, err := buildKeysetPredicate(sortTerms, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(predicate, "item.confidence < (@cursor1)::double precision") || !strings.Contains(predicate, "(@cursor1)::double precision IS NOT NULL AND item.confidence IS NULL") {
		t.Fatalf("predicate=%q", predicate)
	}
}
