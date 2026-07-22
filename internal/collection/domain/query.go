package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	// QuerySchemaVersionV1 是当前唯一支持的 Query AST 版本。
	QuerySchemaVersionV1 = "collection-query/v1"
	// MaxQueryDepth 限制 AST 深度，root 记为 1。
	MaxQueryDepth = 3
	// MaxQueryNodes 限制 AST 节点总数。
	MaxQueryNodes = 64
	// MaxQueryBytes 限制 canonical query JSON 体积。
	MaxQueryBytes = 32 * 1024
	// MaxINValues 限制单个 IN 谓词的值数量。
	MaxINValues = 100
	// MaxSortTerms 限制用户可配置排序键数量。
	MaxSortTerms = 3
)

const (
	tieBreakFieldObjectType = "object_type"
	tieBreakFieldID         = "id"
)

// ClauseKind 表示 AST 节点分型。
type ClauseKind string

const (
	// ClauseKindGroup 表示逻辑分组节点。
	ClauseKindGroup ClauseKind = "group"
	// ClauseKindPredicate 表示字段谓词节点。
	ClauseKindPredicate ClauseKind = "predicate"
)

// GroupOperator 表示 group 逻辑组合方式。
type GroupOperator string

const (
	// GroupOperatorAND 表示全部子句都需满足。
	GroupOperatorAND GroupOperator = "AND"
	// GroupOperatorOR 表示任一子句满足即可。
	GroupOperatorOR GroupOperator = "OR"
)

// Operator 表示字段谓词操作符。
type Operator string

const (
	// OperatorEQ 表示单值相等匹配。
	OperatorEQ Operator = "EQ"
	// OperatorIN 表示离散集合匹配。
	OperatorIN Operator = "IN"
	// OperatorGTE 表示大于等于比较。
	OperatorGTE Operator = "GTE"
	// OperatorLTE 表示小于等于比较。
	OperatorLTE Operator = "LTE"
	// OperatorBetween 表示有界范围比较。
	OperatorBetween Operator = "BETWEEN"
	// OperatorIsNull 表示字段值为 NULL。
	OperatorIsNull Operator = "IS_NULL"
	// OperatorPrefix 表示字符串前缀匹配。
	OperatorPrefix Operator = "PREFIX"
	// OperatorContains 表示字符串包含匹配。
	OperatorContains Operator = "CONTAINS"
)

// SortDirection 表示排序方向。
type SortDirection string

const (
	// SortDirectionASC 表示升序。
	SortDirectionASC SortDirection = "ASC"
	// SortDirectionDESC 表示降序。
	SortDirectionDESC SortDirection = "DESC"
)

// Query 是版本化 Smart Collection 查询定义。
type Query struct {
	SchemaVersion string     `json:"schema_version"`
	Root          Clause     `json:"root"`
	Sort          []SortTerm `json:"sort,omitempty"`
}

// Clause 是 AST 的 group 或 predicate 节点。
type Clause struct {
	Kind     ClauseKind        `json:"kind"`
	Field    string            `json:"field,omitempty"`
	Operator string            `json:"operator,omitempty"`
	Clauses  []Clause          `json:"clauses,omitempty"`
	Value    json.RawMessage   `json:"value,omitempty"`
	Values   []json.RawMessage `json:"values,omitempty"`
	Lower    json.RawMessage   `json:"lower,omitempty"`
	Upper    json.RawMessage   `json:"upper,omitempty"`
}

// SortTerm 是用户可见排序项。
type SortTerm struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// CanonicalQuery 保存规范化后的 Query 及其稳定摘要。
type CanonicalQuery struct {
	Definition    Query           `json:"definition"`
	CanonicalJSON json.RawMessage `json:"canonical_json"`
	Hash          string          `json:"hash"`
}

// ValidateQuery 校验 Query 是否满足冻结约束。
func ValidateQuery(query Query) error {
	_, err := CanonicalizeQuery(query)
	return err
}

// CanonicalizeQuery 返回不修改输入的规范查询、canonical JSON 与稳定 SHA-256。
func CanonicalizeQuery(query Query) (CanonicalQuery, error) {
	if strings.TrimSpace(query.SchemaVersion) != QuerySchemaVersionV1 {
		return CanonicalQuery{}, invalid(ErrorCodeQueryInvalid, "query schema version is unsupported")
	}
	if normalizeClauseKind(query.Root.Kind) != ClauseKindGroup {
		return CanonicalQuery{}, invalid(ErrorCodeQueryInvalid, "query root must be a group clause")
	}
	registry := RegistryV1()
	nodeCount := 0
	root, err := canonicalizeClause(registry, query.Root, 1, &nodeCount)
	if err != nil {
		return CanonicalQuery{}, err
	}
	sortTerms, err := canonicalizeSortTerms(registry, query.Sort, false)
	if err != nil {
		return CanonicalQuery{}, err
	}
	canonical := Query{
		SchemaVersion: QuerySchemaVersionV1,
		Root:          root,
		Sort:          sortTerms,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return CanonicalQuery{}, invalid(ErrorCodeQueryInvalid, "query cannot be encoded")
	}
	if len(encoded) > MaxQueryBytes {
		return CanonicalQuery{}, invalid(ErrorCodeQueryInvalid, "query exceeds canonical size limit")
	}
	digest := sha256.Sum256(encoded)
	return CanonicalQuery{
		Definition:    canonical,
		CanonicalJSON: append(json.RawMessage(nil), encoded...),
		Hash:          hex.EncodeToString(digest[:]),
	}, nil
}

// EffectiveSort 返回附带 `(object_type,id)` 确定性 tie-break 的排序序列。
func EffectiveSort(terms []SortTerm) ([]SortTerm, error) {
	registry := RegistryV1()
	canonical, err := canonicalizeSortTerms(registry, terms, false)
	if err != nil {
		return nil, err
	}
	result := append([]SortTerm(nil), canonical...)
	if !containsSortField(result, tieBreakFieldObjectType) {
		result = append(result, SortTerm{Field: tieBreakFieldObjectType, Direction: string(SortDirectionASC)})
	}
	if !containsSortField(result, tieBreakFieldID) {
		result = append(result, SortTerm{Field: tieBreakFieldID, Direction: string(SortDirectionASC)})
	}
	return result, nil
}

func canonicalizeClause(registry Registry, clause Clause, depth int, nodeCount *int) (Clause, error) {
	*nodeCount = *nodeCount + 1
	if *nodeCount > MaxQueryNodes {
		return Clause{}, invalid(ErrorCodeQueryInvalid, "query exceeds node limit")
	}
	if depth > MaxQueryDepth {
		return Clause{}, invalid(ErrorCodeQueryInvalid, "query exceeds depth limit")
	}
	switch normalizeClauseKind(clause.Kind) {
	case ClauseKindGroup:
		operator, err := canonicalizeGroupOperator(clause.Operator)
		if err != nil {
			return Clause{}, err
		}
		if len(clause.Clauses) == 0 || len(clause.Value) != 0 || len(clause.Values) != 0 || len(clause.Lower) != 0 || len(clause.Upper) != 0 || strings.TrimSpace(clause.Field) != "" {
			return Clause{}, invalid(ErrorCodeQueryInvalid, "group clause payload is invalid")
		}
		children := make([]Clause, 0, len(clause.Clauses))
		seenPredicates := make(map[string]struct{}, len(clause.Clauses))
		for _, child := range clause.Clauses {
			canonical, err := canonicalizeClause(registry, child, depth+1, nodeCount)
			if err != nil {
				return Clause{}, err
			}
			if canonical.Kind == ClauseKindPredicate {
				encoded, encodeErr := json.Marshal(canonical)
				if encodeErr != nil {
					return Clause{}, invalid(ErrorCodeQueryInvalid, "predicate cannot be encoded")
				}
				key := string(encoded)
				if _, exists := seenPredicates[key]; exists {
					return Clause{}, invalid(ErrorCodeQueryInvalid, "group contains duplicate predicates")
				}
				seenPredicates[key] = struct{}{}
			}
			children = append(children, canonical)
		}
		return Clause{
			Kind:     ClauseKindGroup,
			Operator: string(operator),
			Clauses:  children,
		}, nil
	case ClauseKindPredicate:
		return canonicalizePredicateClause(registry, clause)
	default:
		return Clause{}, invalid(ErrorCodeQueryInvalid, "clause kind is invalid")
	}
}

func canonicalizePredicateClause(registry Registry, clause Clause) (Clause, error) {
	fieldName := normalizeName(clause.Field)
	field, ok := registry.Field(fieldName)
	if !ok {
		return Clause{}, invalid(ErrorCodeFieldUnknown, "query field is not registered")
	}
	if field.Availability == FieldAvailabilityUnavailable {
		return Clause{}, invalid(ErrorCodeFieldUnavailable, "query field capability is unavailable")
	}
	operator, err := canonicalizePredicateOperator(clause.Operator)
	if err != nil {
		return Clause{}, err
	}
	if !field.Supports(operator) {
		return Clause{}, invalid(ErrorCodeQueryInvalid, "query operator is not supported by field")
	}
	if len(clause.Clauses) != 0 {
		return Clause{}, invalid(ErrorCodeQueryInvalid, "predicate clause must not contain nested clauses")
	}
	canonical := Clause{
		Kind:     ClauseKindPredicate,
		Field:    field.Name,
		Operator: string(operator),
	}
	switch operator {
	case OperatorEQ, OperatorGTE, OperatorLTE, OperatorPrefix, OperatorContains:
		if len(clause.Value) == 0 || len(clause.Values) != 0 || len(clause.Lower) != 0 || len(clause.Upper) != 0 {
			return Clause{}, invalid(ErrorCodeQueryInvalid, "single-value predicate payload is invalid")
		}
		value, err := canonicalizeFieldValue(field, operator, clause.Value)
		if err != nil {
			return Clause{}, err
		}
		canonical.Value = value.Raw
	case OperatorIN:
		if len(clause.Value) != 0 || len(clause.Values) == 0 || len(clause.Lower) != 0 || len(clause.Upper) != 0 {
			return Clause{}, invalid(ErrorCodeQueryInvalid, "in predicate payload is invalid")
		}
		values, err := canonicalizeFieldValues(field, clause.Values)
		if err != nil {
			return Clause{}, err
		}
		canonical.Values = values
	case OperatorBetween:
		if len(clause.Value) != 0 || len(clause.Values) != 0 || len(clause.Lower) == 0 || len(clause.Upper) == 0 {
			return Clause{}, invalid(ErrorCodeQueryInvalid, "between predicate payload is invalid")
		}
		lower, err := canonicalizeFieldValue(field, operator, clause.Lower)
		if err != nil {
			return Clause{}, err
		}
		upper, err := canonicalizeFieldValue(field, operator, clause.Upper)
		if err != nil {
			return Clause{}, err
		}
		if !withinRange(field.ValueType, lower, upper) {
			return Clause{}, invalid(ErrorCodeQueryInvalid, "between predicate range is invalid")
		}
		canonical.Lower = lower.Raw
		canonical.Upper = upper.Raw
	case OperatorIsNull:
		if len(clause.Value) != 0 || len(clause.Values) != 0 || len(clause.Lower) != 0 || len(clause.Upper) != 0 || !field.Nullable {
			return Clause{}, invalid(ErrorCodeQueryInvalid, "is-null predicate payload is invalid")
		}
	default:
		return Clause{}, invalid(ErrorCodeQueryInvalid, "predicate operator is invalid")
	}
	return canonical, nil
}

func canonicalizeSortTerms(registry Registry, terms []SortTerm, allowHiddenID bool) ([]SortTerm, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	canonical := make([]SortTerm, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		fieldName := normalizeName(term.Field)
		direction, err := canonicalizeSortDirection(term.Direction)
		if err != nil {
			return nil, err
		}
		if allowHiddenID && fieldName == tieBreakFieldID {
			if _, exists := seen[fieldName]; !exists {
				seen[fieldName] = struct{}{}
				canonical = append(canonical, SortTerm{Field: fieldName, Direction: string(direction)})
			}
			continue
		}
		field, ok := registry.Field(fieldName)
		if !ok || !field.Sortable || field.Availability != FieldAvailabilityAvailable {
			return nil, invalid(ErrorCodeSortInvalid, "sort field is not registered or not sortable")
		}
		if _, exists := seen[field.Name]; exists {
			continue
		}
		seen[field.Name] = struct{}{}
		canonical = append(canonical, SortTerm{Field: field.Name, Direction: string(direction)})
	}
	if len(canonical) > MaxSortTerms {
		return nil, invalid(ErrorCodeSortInvalid, "sort term count exceeds limit")
	}
	return canonical, nil
}

func containsSortField(terms []SortTerm, field string) bool {
	for _, term := range terms {
		if normalizeName(term.Field) == field {
			return true
		}
	}
	return false
}

func normalizeClauseKind(kind ClauseKind) ClauseKind {
	return ClauseKind(strings.ToLower(strings.TrimSpace(string(kind))))
}

func canonicalizeGroupOperator(value string) (GroupOperator, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(GroupOperatorAND):
		return GroupOperatorAND, nil
	case string(GroupOperatorOR):
		return GroupOperatorOR, nil
	default:
		return "", invalid(ErrorCodeQueryInvalid, "group operator is invalid")
	}
}

func canonicalizePredicateOperator(value string) (Operator, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(OperatorEQ):
		return OperatorEQ, nil
	case string(OperatorIN):
		return OperatorIN, nil
	case string(OperatorGTE):
		return OperatorGTE, nil
	case string(OperatorLTE):
		return OperatorLTE, nil
	case string(OperatorBetween):
		return OperatorBetween, nil
	case string(OperatorIsNull):
		return OperatorIsNull, nil
	case string(OperatorPrefix):
		return OperatorPrefix, nil
	case string(OperatorContains):
		return OperatorContains, nil
	default:
		return "", invalid(ErrorCodeQueryInvalid, "predicate operator is invalid")
	}
}

func canonicalizeSortDirection(value string) (SortDirection, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(SortDirectionASC):
		return SortDirectionASC, nil
	case string(SortDirectionDESC):
		return SortDirectionDESC, nil
	default:
		return "", invalid(ErrorCodeSortInvalid, "sort direction is invalid")
	}
}

type canonicalValue struct {
	Raw    json.RawMessage
	Key    string
	Number *float64
	Time   *time.Time
}

func canonicalizeFieldValue(field FieldDefinition, operator Operator, raw json.RawMessage) (canonicalValue, error) {
	switch field.ValueType {
	case FieldValueTypeEnum:
		value, err := decodeJSONString(raw)
		if err != nil {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "enum predicate value must be a string")
		}
		if operator == OperatorPrefix {
			value, err = canonicalizeTokenValue(value)
			if err != nil {
				return canonicalValue{}, err
			}
			if !field.EnumPrefixMatches(value) {
				return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "enum predicate prefix matches no registered values")
			}
			return canonicalValue{Raw: marshalString(value), Key: value}, nil
		}
		value, ok := field.CanonicalEnumValue(value)
		if !ok {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "enum predicate value is invalid")
		}
		return canonicalValue{Raw: marshalString(value), Key: value}, nil
	case FieldValueTypeID:
		value, err := decodeJSONString(raw)
		if err != nil {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "id predicate value must be a string")
		}
		parsed, parseErr := parseCanonicalID(value)
		if parseErr != nil {
			return canonicalValue{}, parseErr
		}
		return canonicalValue{Raw: marshalString(parsed), Key: parsed}, nil
	case FieldValueTypeTime:
		value, err := decodeJSONString(raw)
		if err != nil {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "time predicate value must be a string")
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		if parseErr != nil || parsed.IsZero() {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "time predicate value is invalid")
		}
		parsed = parsed.UTC()
		formatted := parsed.Format(time.RFC3339Nano)
		return canonicalValue{Raw: marshalString(formatted), Key: formatted, Time: &parsed}, nil
	case FieldValueTypeNumber:
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "number predicate value is invalid")
		}
		if field.Name == "confidence" && (value < 0 || value > 1) {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "confidence predicate must be within zero and one")
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "number predicate value cannot be encoded")
		}
		return canonicalValue{Raw: encoded, Key: string(encoded), Number: &value}, nil
	case FieldValueTypePath:
		value, err := decodeJSONString(raw)
		if err != nil {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "path predicate value must be a string")
		}
		value, err = canonicalizePathValue(value)
		if err != nil {
			return canonicalValue{}, err
		}
		return canonicalValue{Raw: marshalString(value), Key: value}, nil
	case FieldValueTypeText:
		value, err := decodeJSONString(raw)
		if err != nil {
			return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "text predicate value must be a string")
		}
		value, err = canonicalizeTextValue(value)
		if err != nil {
			return canonicalValue{}, err
		}
		return canonicalValue{Raw: marshalString(value), Key: value}, nil
	default:
		return canonicalValue{}, invalid(ErrorCodeQueryInvalid, "field value type is unsupported")
	}
}

func canonicalizeFieldValues(field FieldDefinition, values []json.RawMessage) ([]json.RawMessage, error) {
	if len(values) == 0 || len(values) > MaxINValues {
		return nil, invalid(ErrorCodeQueryInvalid, "in predicate value count is invalid")
	}
	canonical := make([]canonicalValue, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value, err := canonicalizeFieldValue(field, OperatorIN, raw)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[value.Key]; exists {
			continue
		}
		seen[value.Key] = struct{}{}
		canonical = append(canonical, value)
	}
	sort.Slice(canonical, func(left, right int) bool { return canonical[left].Key < canonical[right].Key })
	result := make([]json.RawMessage, 0, len(canonical))
	for _, value := range canonical {
		result = append(result, value.Raw)
	}
	return result, nil
}

func withinRange(valueType FieldValueType, lower, upper canonicalValue) bool {
	switch valueType {
	case FieldValueTypeTime:
		return lower.Time != nil && upper.Time != nil && (lower.Time.Before(*upper.Time) || lower.Time.Equal(*upper.Time))
	case FieldValueTypeNumber:
		return lower.Number != nil && upper.Number != nil && *lower.Number <= *upper.Number
	default:
		return false
	}
}

func decodeJSONString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func marshalString(value string) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func parseCanonicalID(value string) (string, error) {
	parsed, err := foundation.ParseID(value)
	if err != nil {
		return "", invalid(ErrorCodeQueryInvalid, "id predicate value is invalid")
	}
	return string(parsed), nil
}

func canonicalizePathValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", invalid(ErrorCodeQueryInvalid, "path predicate value must be a relative posix path")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", invalid(ErrorCodeQueryInvalid, "path predicate value escapes workspace")
	}
	return cleaned, nil
}

func canonicalizeTextValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxQueryBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "", invalid(ErrorCodeQueryInvalid, "text predicate value is invalid")
	}
	return value, nil
}

func canonicalizeTokenValue(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "", invalid(ErrorCodeQueryInvalid, "token predicate value is invalid")
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return "", invalid(ErrorCodeQueryInvalid, "token predicate value is invalid")
		}
	}
	return value, nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func statusEnumValues() []string {
	return []string{
		string(knowledge.TopicStatusActive),
		string(knowledge.TopicStatusMerged),
		string(knowledge.TopicStatusDeprecated),
		string(knowledge.ClaimStatusSuggested),
		string(knowledge.ClaimStatusConfirmed),
		string(knowledge.ClaimStatusDisputed),
		string(knowledge.ClaimStatusSuperseded),
		string(knowledge.ClaimStatusDeprecated),
		string(knowledge.ClaimStatusInvalid),
	}
}

func relationTypeEnumValues() []string {
	return []string{
		string(knowledge.RelationBelongsTo),
		string(knowledge.RelationSupports),
		string(knowledge.RelationCites),
		string(knowledge.RelationDerivedFrom),
		string(knowledge.RelationComplements),
		string(knowledge.RelationDuplicates),
		string(knowledge.RelationConflictsWith),
		string(knowledge.RelationPrerequisiteOf),
		string(knowledge.RelationVersionOf),
		string(knowledge.RelationImpacts),
	}
}
