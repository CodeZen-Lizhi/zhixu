package application

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// QueryPlan 是统一 read model 使用的参数化查询片段。
// SQL 标识只来自固定 registry，Args 中的值始终作为 PostgreSQL 参数传递。
type QueryPlan struct {
	Canonical domain.CanonicalQuery
	Where     string
	Args      []any
	Sort      []SortExpression
}

// SortExpression 是已经映射到固定 read-model 列的排序表达式。
type SortExpression struct {
	Column    string
	Direction string
}

// CompileQuery 将 Collection AST 编译成参数化 WHERE 与排序片段。
func CompileQuery(query domain.Query) (QueryPlan, error) {
	canonical, err := domain.CanonicalizeQuery(query)
	if err != nil {
		return QueryPlan{}, err
	}
	sortTerms, err := domain.EffectiveSort(canonical.Definition.Sort)
	if err != nil {
		return QueryPlan{}, err
	}
	compiler := queryCompiler{}
	where, err := compiler.clause(canonical.Definition.Root)
	if err != nil {
		return QueryPlan{}, err
	}
	sortExpressions := make([]SortExpression, 0, len(sortTerms))
	for _, term := range sortTerms {
		column, ok := sortColumn(term.Field)
		if !ok {
			return QueryPlan{}, invalidQuery(fmt.Errorf("sort field %q is not executable", term.Field))
		}
		direction := strings.ToUpper(strings.TrimSpace(term.Direction))
		if direction != string(domain.SortDirectionASC) && direction != string(domain.SortDirectionDESC) {
			return QueryPlan{}, invalidQuery(fmt.Errorf("sort direction %q is invalid", term.Direction))
		}
		sortExpressions = append(sortExpressions, SortExpression{Column: column, Direction: direction})
	}
	return QueryPlan{Canonical: canonical, Where: where, Args: compiler.args, Sort: sortExpressions}, nil
}

type queryCompiler struct{ args []any }

func (compiler *queryCompiler) clause(clause domain.Clause) (string, error) {
	switch clause.Kind {
	case domain.ClauseKindGroup:
		if len(clause.Clauses) == 0 {
			return "", invalidQuery(fmt.Errorf("query group is empty"))
		}
		operator := strings.ToUpper(strings.TrimSpace(clause.Operator))
		if operator != string(domain.GroupOperatorAND) && operator != string(domain.GroupOperatorOR) {
			return "", invalidQuery(fmt.Errorf("query group operator %q is invalid", clause.Operator))
		}
		parts := make([]string, 0, len(clause.Clauses))
		for _, child := range clause.Clauses {
			part, err := compiler.clause(child)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
		return "(" + strings.Join(parts, " "+operator+" ") + ")", nil
	case domain.ClauseKindPredicate:
		return compiler.predicate(clause)
	default:
		return "", invalidQuery(fmt.Errorf("query clause kind %q is invalid", clause.Kind))
	}
}

func (compiler *queryCompiler) predicate(clause domain.Clause) (string, error) {
	field := strings.ToLower(strings.TrimSpace(clause.Field))
	column, ok := predicateColumn(field)
	if !ok {
		return "", invalidQuery(fmt.Errorf("query field %q is not executable", clause.Field))
	}
	operator := domain.Operator(strings.ToUpper(strings.TrimSpace(clause.Operator)))
	switch operator {
	case domain.OperatorEQ:
		placeholder, err := compiler.addValue(field, clause.Value)
		if err != nil {
			return "", err
		}
		if expression, matched := relationshipPredicate(field, operator, placeholder); matched {
			return expression, nil
		}
		return column + " = " + placeholder, nil
	case domain.OperatorIN:
		values, err := decodeValues(clause.Values)
		if err != nil {
			return "", err
		}
		if len(values) == 0 {
			return "", invalidQuery(fmt.Errorf("IN predicate has no values"))
		}
		converted, err := convertValues(field, values)
		if err != nil {
			return "", err
		}
		placeholder := compiler.addArray(converted)
		cast := "text[]"
		if strings.EqualFold(strings.TrimSpace(clause.Field), "topic_id") {
			cast = "uuid[]"
		}
		if expression, matched := relationshipPredicate(field, operator, placeholder); matched {
			return expression, nil
		}
		return column + " = ANY(" + placeholder + "::" + cast + ")", nil
	case domain.OperatorGTE, domain.OperatorLTE:
		placeholder, err := compiler.addValue(field, clause.Value)
		if err != nil {
			return "", err
		}
		return column + " " + comparisonOperator(operator) + " " + placeholder, nil
	case domain.OperatorBetween:
		lower, err := compiler.addValue(field, clause.Lower)
		if err != nil {
			return "", err
		}
		upper, err := compiler.addValue(field, clause.Upper)
		if err != nil {
			return "", err
		}
		return column + " BETWEEN " + lower + " AND " + upper, nil
	case domain.OperatorIsNull:
		return column + " IS NULL", nil
	case domain.OperatorPrefix, domain.OperatorContains:
		placeholder, err := compiler.addLikeValue(field, clause.Value)
		if err != nil {
			return "", err
		}
		if operator == domain.OperatorPrefix {
			if expression, matched := relationshipPredicate(field, operator, placeholder); matched {
				return expression, nil
			}
			return column + " LIKE " + placeholder + " || '%' ESCAPE '\\'", nil
		}
		if expression, matched := relationshipPredicate(field, operator, placeholder); matched {
			return expression, nil
		}
		return column + " LIKE '%' || " + placeholder + " || '%' ESCAPE '\\'", nil
	default:
		return "", invalidQuery(fmt.Errorf("query operator %q is not executable", clause.Operator))
	}
}

func relationshipPredicate(field string, operator domain.Operator, placeholder string) (string, bool) {
	value := " = " + placeholder
	if operator == domain.OperatorIN {
		value = " = ANY(" + placeholder + "::text[])"
	}
	switch field {
	case "topic_id":
		if operator == domain.OperatorIN {
			value = " = ANY(" + placeholder + "::uuid[])"
		} else {
			value = " = " + placeholder + "::uuid"
		}
		return "((item.object_type = 'TOPIC' AND item.id::uuid" + value + ") OR (item.object_type = 'CLAIM' AND EXISTS (SELECT 1 FROM core.relation r WHERE r.workspace_id=item.workspace_id::uuid AND r.source_node_type='CLAIM' AND r.source_node_id=item.id::uuid AND r.target_node_type='TOPIC' AND r.target_node_id" + value + " AND r.relation_type='BELONGS_TO' AND r.status='CONFIRMED')))", true
	case "relation_type":
		return "EXISTS (SELECT 1 FROM core.relation r WHERE r.workspace_id=item.workspace_id::uuid AND r.status='CONFIRMED' AND ((r.source_node_type=item.object_type AND r.source_node_id=item.id::uuid) OR (r.target_node_type=item.object_type AND r.target_node_id=item.id::uuid)) AND r.relation_type" + value + ")", true
	case "health_issue_type":
		return "EXISTS (SELECT 1 FROM ops.health_issue i WHERE i.workspace_id=item.workspace_id::uuid AND i.target_type=item.object_type AND i.target_id=item.id::uuid AND i.status NOT IN ('RESOLVED','IGNORED','FALSE_POSITIVE') AND i.type" + value + ")", true
	case "source_type", "file_path":
		column := "s.type"
		if field == "file_path" {
			column = "s.original_location"
		}
		if operator == domain.OperatorPrefix {
			return "(item.object_type='CLAIM' AND EXISTS (SELECT 1 FROM core.claim_source cs JOIN core.source_version sv ON sv.id=cs.source_version_id JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=item.workspace_id::uuid WHERE cs.workspace_id=item.workspace_id::uuid AND cs.claim_id=item.id::uuid AND " + column + " LIKE " + placeholder + " || '%' ESCAPE '\\'))", true
		}
		return "(item.object_type='CLAIM' AND EXISTS (SELECT 1 FROM core.claim_source cs JOIN core.source_version sv ON sv.id=cs.source_version_id JOIN core.source s ON s.id=sv.source_id AND s.workspace_id=item.workspace_id::uuid WHERE cs.workspace_id=item.workspace_id::uuid AND cs.claim_id=item.id::uuid AND " + column + value + "))", true
	case "text":
		pattern := "'%' || " + placeholder + " || '%'"
		if operator == domain.OperatorPrefix {
			pattern = placeholder + " || '%'"
		}
		match := " LIKE " + pattern + " ESCAPE '\\'"
		return "(lower(item.title)" + match + " OR lower(item.summary)" + match + " OR (item.object_type='TOPIC' AND EXISTS (SELECT 1 FROM core.topic_alias a WHERE a.workspace_id=item.workspace_id::uuid AND a.topic_id=item.id::uuid AND a.normalized_alias" + match + ")))", true
	default:
		return "", false
	}
}

func comparisonOperator(operator domain.Operator) string {
	if operator == domain.OperatorGTE {
		return ">="
	}
	return "<="
}

func (compiler *queryCompiler) addValue(field string, raw json.RawMessage) (string, error) {
	value, err := convertValue(field, raw)
	if err != nil {
		return "", err
	}
	compiler.args = append(compiler.args, value)
	return "$" + strconv.Itoa(len(compiler.args)), nil
}

func (compiler *queryCompiler) addLikeValue(field string, raw json.RawMessage) (string, error) {
	value, err := convertValue(field, raw)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", invalidQuery(fmt.Errorf("query pattern value is invalid"))
	}
	text = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
	if strings.EqualFold(strings.TrimSpace(field), "text") {
		text = strings.ToLower(text)
	}
	compiler.args = append(compiler.args, text)
	return "$" + strconv.Itoa(len(compiler.args)), nil
}

func (compiler *queryCompiler) addArray(values any) string {
	compiler.args = append(compiler.args, values)
	return "$" + strconv.Itoa(len(compiler.args))
}

func predicateColumn(field string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "object_type":
		return "item.object_type", true
	case "topic_id":
		return "item.topic_id", true
	case "status":
		return "item.status", true
	case "created_at":
		return "item.created_at", true
	case "updated_at":
		return "item.updated_at", true
	case "confidence":
		return "item.confidence", true
	case "relation_type":
		return "item.relation_type", true
	case "health_issue_type":
		return "item.health_issue_type", true
	case "source_type":
		return "item.source_type", true
	case "file_path":
		return "item.file_path", true
	case "text":
		return "lower(item.search_text)", true
	default:
		return "", false
	}
}

func sortColumn(field string) (string, bool) {
	if field == "id" {
		return "item.id", true
	}
	return predicateColumn(field)
}

func convertValues(field string, values []any) (any, error) {
	converted := make([]string, 0, len(values))
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, invalidQuery(err)
		}
		item, err := convertValue(field, encoded)
		if err != nil {
			return nil, err
		}
		switch typed := item.(type) {
		case foundation.ID:
			converted = append(converted, string(typed))
		case string:
			converted = append(converted, typed)
		default:
			return nil, invalidQuery(fmt.Errorf("IN value type is unsupported"))
		}
	}
	return converted, nil
}

func convertValue(field string, raw json.RawMessage) (any, error) {
	definition, ok := domain.RegistryV1().Field(strings.ToLower(strings.TrimSpace(field)))
	if !ok || definition.Availability != domain.FieldAvailabilityAvailable {
		return nil, invalidQuery(fmt.Errorf("query field %q is unavailable", field))
	}
	switch definition.ValueType {
	case domain.FieldValueTypeID:
		value, err := decodeString(raw)
		if err != nil {
			return nil, err
		}
		id, err := foundation.ParseID(value)
		if err != nil {
			return nil, invalidQuery(fmt.Errorf("query id is invalid"))
		}
		return id, nil
	case domain.FieldValueTypeTime:
		value, err := decodeString(raw)
		if err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, invalidQuery(fmt.Errorf("query time is invalid"))
		}
		return parsed.UTC(), nil
	case domain.FieldValueTypeNumber:
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, invalidQuery(fmt.Errorf("query number is invalid"))
		}
		return value, nil
	case domain.FieldValueTypeEnum, domain.FieldValueTypePath, domain.FieldValueTypeText:
		return decodeString(raw)
	default:
		return nil, invalidQuery(fmt.Errorf("query field %q type is unsupported", field))
	}
}

func decodeValues(raw []json.RawMessage) ([]any, error) {
	values := make([]any, 0, len(raw))
	for _, item := range raw {
		var value any
		if err := json.Unmarshal(item, &value); err != nil {
			return nil, invalidQuery(err)
		}
		values = append(values, value)
	}
	return values, nil
}

func decodeString(raw json.RawMessage) (string, error) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", invalidQuery(fmt.Errorf("query value must be a non-empty string"))
	}
	return value, nil
}

func invalidQuery(err error) error {
	if err == nil {
		return requestInvalid("query is invalid")
	}
	return requestInvalid(err.Error())
}
