package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
)

func TestCompileQueryUsesRegistryColumnsAndParameters(t *testing.T) {
	query := domain.Query{
		SchemaVersion: domain.QuerySchemaVersionV1,
		Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{
			{Kind: domain.ClauseKindPredicate, Field: "status", Operator: string(domain.OperatorEQ), Value: json.RawMessage(`"CONFIRMED"`)},
			{Kind: domain.ClauseKindPredicate, Field: "text", Operator: string(domain.OperatorContains), Value: json.RawMessage(`"relation_type"`)},
		}},
		Sort: []domain.SortTerm{{Field: "updated_at", Direction: string(domain.SortDirectionDESC)}},
	}
	plan, err := CompileQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Where, "relation_type") || strings.Contains(plan.Where, "CONFIRMED") {
		t.Fatalf("user values leaked into SQL: %q", plan.Where)
	}
	if !strings.Contains(plan.Where, "item.status") || !strings.Contains(plan.Where, "core.topic_alias") {
		t.Fatalf("registry columns missing: %q", plan.Where)
	}
	if len(plan.Args) != 2 || len(plan.Sort) != 3 {
		t.Fatalf("args/sort = %d/%d, want 2/3", len(plan.Args), len(plan.Sort))
	}
	if plan.Sort[0].Column != "item.updated_at" || plan.Sort[1].Column != "item.object_type" || plan.Sort[2].Column != "item.id" {
		t.Fatalf("sort = %#v", plan.Sort)
	}
}

func TestCompileQueryRejectsUnavailableAndUnknownFields(t *testing.T) {
	for _, field := range []string{"tag", "review_status", "directory_id", "unknown_field"} {
		query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: field, Operator: string(domain.OperatorEQ), Value: json.RawMessage(`"x"`)}}}}
		if _, err := CompileQuery(query); err == nil {
			t.Fatalf("CompileQuery(%q) unexpectedly succeeded", field)
		}
	}
}

func TestCompileQueryRejectsNonStringPrefixValue(t *testing.T) {
	query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: "text", Operator: string(domain.OperatorPrefix), Value: json.RawMessage(`42`)}}}}
	if _, err := CompileQuery(query); err == nil {
		t.Fatal("numeric prefix unexpectedly succeeded")
	}
}

func TestCompileQueryUsesExistsForMultiValuedFacts(t *testing.T) {
	fields := []struct {
		field string
		value string
		want  string
	}{
		{"topic_id", `"10000000-0000-4000-8000-000000000001"`, "core.relation"},
		{"relation_type", `"SUPPORTS"`, "core.relation"},
		{"health_issue_type", `"ORPHAN"`, "ops.health_issue"},
		{"source_type", `"markdown"`, "core.claim_source"},
		{"file_path", `"docs/a.md"`, "core.claim_source"},
		{"text", `"alias"`, "core.topic_alias"},
	}
	for _, item := range fields {
		t.Run(item.field, func(t *testing.T) {
			operator := domain.OperatorEQ
			if item.field == "text" {
				operator = domain.OperatorContains
			}
			query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: item.field, Operator: string(operator), Value: json.RawMessage(item.value)}}}}
			plan, err := CompileQuery(query)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan.Where, "EXISTS") || !strings.Contains(plan.Where, item.want) || len(plan.Args) != 1 {
				t.Fatalf("where=%q args=%#v", plan.Where, plan.Args)
			}
		})
	}
}

func TestCompileQueryEscapesLiteralLikeWildcards(t *testing.T) {
	query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{{Kind: domain.ClauseKindPredicate, Field: "text", Operator: string(domain.OperatorContains), Value: json.RawMessage(`"A%_\\B"`)}}}}
	plan, err := CompileQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Where, `ESCAPE '\'`) || len(plan.Args) != 1 || plan.Args[0] != `a\%\_\\b` {
		t.Fatalf("where=%q args=%#v", plan.Where, plan.Args)
	}
}

func TestCompileQuerySupportsEveryRegisteredFieldOperator(t *testing.T) {
	t.Parallel()
	values := map[string]json.RawMessage{
		"object_type":       json.RawMessage(`"TOPIC"`),
		"topic_id":          json.RawMessage(`"10000000-0000-4000-8000-000000000001"`),
		"status":            json.RawMessage(`"CONFIRMED"`),
		"created_at":        json.RawMessage(`"2026-07-22T00:00:00Z"`),
		"updated_at":        json.RawMessage(`"2026-07-22T00:00:00Z"`),
		"confidence":        json.RawMessage(`0.5`),
		"relation_type":     json.RawMessage(`"SUPPORTS"`),
		"health_issue_type": json.RawMessage(`"ORPHAN"`),
		"source_type":       json.RawMessage(`"file"`),
		"file_path":         json.RawMessage(`"docs/readme.md"`),
		"text":              json.RawMessage(`"literal"`),
	}
	for _, field := range domain.RegistryV1().Fields {
		if field.Availability != domain.FieldAvailabilityAvailable {
			continue
		}
		for _, operator := range field.Operators {
			field, operator := field, operator
			t.Run(field.Name+"/"+string(operator), func(t *testing.T) {
				t.Parallel()
				clause := domain.Clause{Kind: domain.ClauseKindPredicate, Field: field.Name, Operator: string(operator)}
				switch operator {
				case domain.OperatorIN:
					clause.Values = []json.RawMessage{values[field.Name]}
				case domain.OperatorBetween:
					clause.Lower, clause.Upper = values[field.Name], values[field.Name]
				case domain.OperatorIsNull:
				default:
					clause.Value = values[field.Name]
				}
				query := domain.Query{SchemaVersion: domain.QuerySchemaVersionV1, Root: domain.Clause{Kind: domain.ClauseKindGroup, Operator: string(domain.GroupOperatorAND), Clauses: []domain.Clause{clause}}}
				plan, err := CompileQuery(query)
				if err != nil {
					t.Fatal(err)
				}
				wantArgs := 1
				if operator == domain.OperatorBetween {
					wantArgs = 2
				} else if operator == domain.OperatorIsNull {
					wantArgs = 0
				}
				if len(plan.Args) != wantArgs {
					t.Fatalf("args=%#v want=%d where=%q", plan.Args, wantArgs, plan.Where)
				}
			})
		}
	}
}
