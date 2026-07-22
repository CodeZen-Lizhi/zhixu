package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalizeQueryNormalizesValuesAndProducesStableHash(t *testing.T) {
	t.Parallel()
	query := Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root: Clause{
			Kind:     ClauseKindGroup,
			Operator: string(GroupOperatorAND),
			Clauses: []Clause{
				{
					Kind:     ClauseKindPredicate,
					Field:    "object_type",
					Operator: "eq",
					Value:    rawString(" topic "),
				},
				{
					Kind:     ClauseKindPredicate,
					Field:    "status",
					Operator: "in",
					Values: []json.RawMessage{
						rawString("deprecated"),
						rawString("confirmed"),
						rawString("confirmed"),
						rawString(" active "),
					},
				},
				{
					Kind:     ClauseKindPredicate,
					Field:    "file_path",
					Operator: "prefix",
					Value:    rawString(" notes/a.md "),
				},
			},
		},
		Sort: []SortTerm{
			{Field: "updated_at", Direction: "desc"},
			{Field: "updated_at", Direction: "DESC"},
			{Field: "status", Direction: "asc"},
		},
	}

	first, err := CanonicalizeQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalizeQuery(Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root: Clause{
			Kind:     ClauseKindGroup,
			Operator: string(GroupOperatorAND),
			Clauses: []Clause{
				{
					Kind:     ClauseKindPredicate,
					Field:    "object_type",
					Operator: string(OperatorEQ),
					Value:    rawString("TOPIC"),
				},
				{
					Kind:     ClauseKindPredicate,
					Field:    "status",
					Operator: string(OperatorIN),
					Values: []json.RawMessage{
						rawString("ACTIVE"),
						rawString("CONFIRMED"),
						rawString("DEPRECATED"),
					},
				},
				{
					Kind:     ClauseKindPredicate,
					Field:    "file_path",
					Operator: string(OperatorPrefix),
					Value:    rawString("notes/a.md"),
				},
			},
		},
		Sort: []SortTerm{
			{Field: "updated_at", Direction: string(SortDirectionDESC)},
			{Field: "status", Direction: string(SortDirectionASC)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	wantJSON := `{"schema_version":"collection-query/v1","root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"TOPIC"},{"kind":"predicate","field":"status","operator":"IN","values":["ACTIVE","CONFIRMED","DEPRECATED"]},{"kind":"predicate","field":"file_path","operator":"PREFIX","value":"notes/a.md"}]},"sort":[{"field":"updated_at","direction":"DESC"},{"field":"status","direction":"ASC"}]}`
	if string(first.CanonicalJSON) != wantJSON {
		t.Fatalf("canonical json = %s", first.CanonicalJSON)
	}
	if first.Hash == "" || first.Hash != second.Hash || string(first.CanonicalJSON) != string(second.CanonicalJSON) {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if string(query.Root.Clauses[0].Value) != `" topic "` || string(query.Root.Clauses[1].Values[0]) != `"deprecated"` {
		t.Fatal("CanonicalizeQuery mutated caller-owned raw values")
	}
}

func TestCanonicalizeQueryCanonicalizesSourceTypeToRegistryOwnedValue(t *testing.T) {
	t.Parallel()
	query := Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root: Clause{
			Kind:     ClauseKindGroup,
			Operator: string(GroupOperatorAND),
			Clauses: []Clause{
				{Kind: ClauseKindPredicate, Field: "source_type", Operator: string(OperatorEQ), Value: rawString(" FILE ")},
			},
		},
	}
	canonical, err := CanonicalizeQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"collection-query/v1","root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"source_type","operator":"EQ","value":"file"}]}}`
	if string(canonical.CanonicalJSON) != want {
		t.Fatalf("canonical json = %s", canonical.CanonicalJSON)
	}
}

func TestCanonicalizeQueryValidatesSourceTypePrefixAgainstRegisteredValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		value     string
		wantValue string
		wantErr   string
	}{
		{name: "file prefix", value: " FI ", wantValue: "fi"},
		{name: "markdown prefix", value: "mark", wantValue: "mark"},
		{name: "invalid prefix", value: "zip", wantErr: ErrorCodeQueryInvalid},
		{name: "invalid token", value: "fi/", wantErr: ErrorCodeQueryInvalid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := Query{
				SchemaVersion: QuerySchemaVersionV1,
				Root: Clause{
					Kind:     ClauseKindGroup,
					Operator: string(GroupOperatorAND),
					Clauses: []Clause{
						{Kind: ClauseKindPredicate, Field: "source_type", Operator: string(OperatorPrefix), Value: rawString(test.value)},
					},
				},
			}
			canonical, err := CanonicalizeQuery(query)
			if test.wantErr != "" {
				assertDomainError(t, err, foundation.ErrorInvalidInput, test.wantErr)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := string(canonical.Definition.Root.Clauses[0].Value); got != fmt.Sprintf("%q", test.wantValue) {
				t.Fatalf("prefix value = %s", got)
			}
		})
	}
}

func TestCanonicalizeQueryRejectsDepthNodeAndSizeLimits(t *testing.T) {
	t.Parallel()
	depth4 := Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root: Clause{
			Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
			Clauses: []Clause{{
				Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
				Clauses: []Clause{{
					Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
					Clauses: []Clause{{
						Kind: ClauseKindPredicate, Field: "text", Operator: string(OperatorContains), Value: rawString("deep"),
					}},
				}},
			}},
		},
	}
	assertDomainError(t, mustErr(CanonicalizeQuery(depth4)), foundation.ErrorInvalidInput, ErrorCodeQueryInvalid)

	nodes := make([]Clause, 0, MaxQueryNodes)
	for index := 0; index < MaxQueryNodes; index++ {
		nodes = append(nodes, Clause{
			Kind: ClauseKindPredicate, Field: "text", Operator: string(OperatorContains), Value: rawString(fmt.Sprintf("node-%02d", index)),
		})
	}
	tooManyNodes := Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root: Clause{
			Kind: ClauseKindGroup, Operator: string(GroupOperatorAND), Clauses: nodes,
		},
	}
	assertDomainError(t, mustErr(CanonicalizeQuery(tooManyNodes)), foundation.ErrorInvalidInput, ErrorCodeQueryInvalid)

	oversized := Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root: Clause{
			Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
			Clauses: []Clause{{
				Kind: ClauseKindPredicate, Field: "text", Operator: string(OperatorContains), Value: rawString(strings.Repeat("x", MaxQueryBytes)),
			}},
		},
	}
	assertDomainError(t, mustErr(CanonicalizeQuery(oversized)), foundation.ErrorInvalidInput, ErrorCodeQueryInvalid)
}

func TestCanonicalizeQueryRejectsFieldAvailabilityUnknownAndTypeMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		code  string
		query Query
	}{
		{
			name: "unavailable field",
			code: ErrorCodeFieldUnavailable,
			query: Query{
				SchemaVersion: QuerySchemaVersionV1,
				Root: Clause{
					Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
					Clauses: []Clause{{Kind: ClauseKindPredicate, Field: "tag", Operator: string(OperatorEQ), Value: rawString("infra")}},
				},
			},
		},
		{
			name: "unknown field",
			code: ErrorCodeFieldUnknown,
			query: Query{
				SchemaVersion: QuerySchemaVersionV1,
				Root: Clause{
					Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
					Clauses: []Clause{{Kind: ClauseKindPredicate, Field: "unknown_field", Operator: string(OperatorEQ), Value: rawString("x")}},
				},
			},
		},
		{
			name: "operator mismatch",
			code: ErrorCodeQueryInvalid,
			query: Query{
				SchemaVersion: QuerySchemaVersionV1,
				Root: Clause{
					Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
					Clauses: []Clause{{Kind: ClauseKindPredicate, Field: "created_at", Operator: string(OperatorContains), Value: rawString("2026-07-21T00:00:00Z")}},
				},
			},
		},
		{
			name: "invalid enum",
			code: ErrorCodeQueryInvalid,
			query: Query{
				SchemaVersion: QuerySchemaVersionV1,
				Root: Clause{
					Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
					Clauses: []Clause{{Kind: ClauseKindPredicate, Field: "object_type", Operator: string(OperatorEQ), Value: rawString("RELATION")}},
				},
			},
		},
		{
			name: "type mismatch",
			code: ErrorCodeQueryInvalid,
			query: Query{
				SchemaVersion: QuerySchemaVersionV1,
				Root: Clause{
					Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
					Clauses: []Clause{{Kind: ClauseKindPredicate, Field: "confidence", Operator: string(OperatorGTE), Value: rawString("0.7")}},
				},
			},
		},
		{
			name: "too many in values",
			code: ErrorCodeQueryInvalid,
			query: Query{
				SchemaVersion: QuerySchemaVersionV1,
				Root: Clause{
					Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
					Clauses: []Clause{{Kind: ClauseKindPredicate, Field: "topic_id", Operator: string(OperatorIN), Values: manyIDs(MaxINValues + 1)}},
				},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertDomainError(t, mustErr(CanonicalizeQuery(test.query)), foundation.ErrorInvalidInput, test.code)
		})
	}
}

func TestCanonicalizeQueryRejectsDuplicatePredicatesWithinSameGroup(t *testing.T) {
	t.Parallel()
	query := Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root: Clause{
			Kind: ClauseKindGroup, Operator: string(GroupOperatorAND),
			Clauses: []Clause{
				{Kind: ClauseKindPredicate, Field: "object_type", Operator: string(OperatorEQ), Value: rawString("TOPIC")},
				{Kind: ClauseKindPredicate, Field: "object_type", Operator: string(OperatorEQ), Value: rawString(" topic ")},
			},
		},
	}
	assertDomainError(t, mustErr(CanonicalizeQuery(query)), foundation.ErrorInvalidInput, ErrorCodeQueryInvalid)
}

func TestEffectiveSortNormalizesDeduplicatesAndAppendsDeterministicTieBreak(t *testing.T) {
	t.Parallel()
	sortTerms, err := EffectiveSort([]SortTerm{
		{Field: "updated_at", Direction: "desc"},
		{Field: "updated_at", Direction: string(SortDirectionDESC)},
		{Field: "object_type", Direction: string(SortDirectionASC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []SortTerm{
		{Field: "updated_at", Direction: string(SortDirectionDESC)},
		{Field: "object_type", Direction: string(SortDirectionASC)},
		{Field: tieBreakFieldID, Direction: string(SortDirectionASC)},
	}
	if len(sortTerms) != len(want) {
		t.Fatalf("sort terms = %#v", sortTerms)
	}
	for index := range want {
		if sortTerms[index] != want[index] {
			t.Fatalf("sort[%d]=%#v want=%#v", index, sortTerms[index], want[index])
		}
	}

	_, err = EffectiveSort([]SortTerm{
		{Field: "updated_at", Direction: string(SortDirectionASC)},
		{Field: "status", Direction: string(SortDirectionASC)},
		{Field: "object_type", Direction: string(SortDirectionASC)},
		{Field: "created_at", Direction: string(SortDirectionASC)},
	})
	assertDomainError(t, err, foundation.ErrorInvalidInput, ErrorCodeSortInvalid)
}

func TestRegistryExposesAvailableAndUnavailableFieldsWithoutDuplicates(t *testing.T) {
	t.Parallel()
	registry := RegistryV1()
	seen := make(map[string]struct{}, len(registry.Fields))
	for _, field := range registry.Fields {
		if _, exists := seen[field.Name]; exists {
			t.Fatalf("duplicate field definition: %s", field.Name)
		}
		seen[field.Name] = struct{}{}
	}
	if field, ok := registry.Field("object_type"); !ok || field.Availability != FieldAvailabilityAvailable {
		t.Fatalf("object_type field = %#v ok=%t", field, ok)
	}
	if field, ok := registry.Field("source_type"); !ok || len(field.EnumValues) != 5 || field.EnumValues[0] != "markdown" || field.EnumValues[4] != "file" {
		t.Fatalf("source_type field = %#v ok=%t", field, ok)
	}
	if field, ok := registry.Field("tag"); !ok || field.Availability != FieldAvailabilityUnavailable {
		t.Fatalf("tag field = %#v ok=%t", field, ok)
	}
	if _, ok := registry.Field("unknown"); ok {
		t.Fatal("unknown field unexpectedly registered")
	}
}

func rawString(value string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%q", value))
}

func rawNumber(value string) json.RawMessage {
	return json.RawMessage(value)
}

func manyIDs(count int) []json.RawMessage {
	values := make([]json.RawMessage, 0, count)
	for index := 0; index < count; index++ {
		values = append(values, rawString(fmt.Sprintf("10000000-0000-4000-8000-%012d", index)))
	}
	return values
}

func mustErr[T any](_ T, err error) error { return err }

func assertDomainError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("err=%v kind=%v code=%s", err, kind, code)
	}
}
