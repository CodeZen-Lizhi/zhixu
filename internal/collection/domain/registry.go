package domain

import "strings"

// FieldAvailability 表示字段当前是否真实可执行。
type FieldAvailability string

const (
	// FieldAvailabilityAvailable 表示字段已落地并可用于执行。
	FieldAvailabilityAvailable FieldAvailability = "available"
	// FieldAvailabilityUnavailable 表示字段被识别但能力尚未落地。
	FieldAvailabilityUnavailable FieldAvailability = "unavailable"
)

// FieldValueType 表示字段值的受控类型。
type FieldValueType string

const (
	// FieldValueTypeEnum 表示由 registry 持有 canonical 值的枚举。
	FieldValueTypeEnum FieldValueType = "enum"
	// FieldValueTypeID 表示 canonical UUID。
	FieldValueTypeID FieldValueType = "id"
	// FieldValueTypeTime 表示 RFC3339Nano 时间。
	FieldValueTypeTime FieldValueType = "time"
	// FieldValueTypeNumber 表示有限数值。
	FieldValueTypeNumber FieldValueType = "number"
	// FieldValueTypePath 表示 workspace 内相对路径。
	FieldValueTypePath FieldValueType = "path"
	// FieldValueTypeText 表示规范文本。
	FieldValueTypeText FieldValueType = "text"
)

// FieldDefinition 描述字段、操作符和值类型白名单。
type FieldDefinition struct {
	Name         string
	Availability FieldAvailability
	ValueType    FieldValueType
	Operators    []Operator
	Sortable     bool
	Groupable    bool
	Nullable     bool
	EnumValues   []string
}

// ViewColumnDefinition 描述视图可见列白名单。
type ViewColumnDefinition struct {
	Name string
}

// Registry 是 collection-query/v1 唯一字段/排序/视图注册表。
type Registry struct {
	Fields      []FieldDefinition
	ViewColumns []ViewColumnDefinition
}

var registryV1 = newRegistryV1()

// RegistryV1 返回 collection-query/v1 的注册表副本。
func RegistryV1() Registry {
	fields := make([]FieldDefinition, 0, len(registryV1.Fields))
	for _, field := range registryV1.Fields {
		fields = append(fields, FieldDefinition{
			Name:         field.Name,
			Availability: field.Availability,
			ValueType:    field.ValueType,
			Operators:    append([]Operator(nil), field.Operators...),
			Sortable:     field.Sortable,
			Groupable:    field.Groupable,
			Nullable:     field.Nullable,
			EnumValues:   append([]string(nil), field.EnumValues...),
		})
	}
	columns := make([]ViewColumnDefinition, len(registryV1.ViewColumns))
	copy(columns, registryV1.ViewColumns)
	return Registry{Fields: fields, ViewColumns: columns}
}

// Field 返回给定字段的注册定义。
func (registry Registry) Field(name string) (FieldDefinition, bool) {
	normalized := normalizeName(name)
	for _, field := range registry.Fields {
		if field.Name == normalized {
			return field, true
		}
	}
	return FieldDefinition{}, false
}

// ViewColumn 返回给定列的注册定义。
func (registry Registry) ViewColumn(name string) (ViewColumnDefinition, bool) {
	normalized := normalizeName(name)
	for _, column := range registry.ViewColumns {
		if column.Name == normalized {
			return column, true
		}
	}
	return ViewColumnDefinition{}, false
}

// Supports 判断字段是否支持指定操作符。
func (field FieldDefinition) Supports(operator Operator) bool {
	for _, candidate := range field.Operators {
		if candidate == operator {
			return true
		}
	}
	return false
}

// HasEnumValue 判断字段是否包含给定枚举值。
func (field FieldDefinition) HasEnumValue(value string) bool {
	if len(field.EnumValues) == 0 {
		return false
	}
	for _, candidate := range field.EnumValues {
		if candidate == value {
			return true
		}
	}
	return false
}

// CanonicalEnumValue 返回输入匹配到的 registry-owned canonical 枚举值。
func (field FieldDefinition) CanonicalEnumValue(value string) (string, bool) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", false
	}
	for _, candidate := range field.EnumValues {
		if strings.EqualFold(candidate, normalized) {
			return candidate, true
		}
	}
	return "", false
}

// EnumPrefixMatches 判断规范前缀是否至少匹配一个 registry-owned 枚举值。
func (field FieldDefinition) EnumPrefixMatches(prefix string) bool {
	normalized := strings.ToLower(strings.TrimSpace(prefix))
	if normalized == "" {
		return false
	}
	for _, candidate := range field.EnumValues {
		if strings.HasPrefix(strings.ToLower(candidate), normalized) {
			return true
		}
	}
	return false
}

func newRegistryV1() Registry {
	fields := []FieldDefinition{
		{Name: "object_type", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeEnum, Operators: []Operator{OperatorEQ, OperatorIN}, Sortable: true, Groupable: true, EnumValues: []string{"TOPIC", "CLAIM"}},
		{Name: "topic_id", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeID, Operators: []Operator{OperatorEQ, OperatorIN}, Groupable: true},
		{Name: "status", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeEnum, Operators: []Operator{OperatorEQ, OperatorIN}, Sortable: true, Groupable: true, EnumValues: statusEnumValues()},
		{Name: "created_at", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeTime, Operators: []Operator{OperatorGTE, OperatorLTE, OperatorBetween}, Sortable: true},
		{Name: "updated_at", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeTime, Operators: []Operator{OperatorGTE, OperatorLTE, OperatorBetween}, Sortable: true},
		{Name: "confidence", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeNumber, Operators: []Operator{OperatorGTE, OperatorLTE, OperatorBetween, OperatorIsNull}, Sortable: true, Nullable: true},
		{Name: "relation_type", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeEnum, Operators: []Operator{OperatorEQ, OperatorIN}, Sortable: true, Groupable: true, EnumValues: relationTypeEnumValues()},
		{Name: "health_issue_type", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeEnum, Operators: []Operator{OperatorEQ, OperatorIN}, Sortable: true, Groupable: true, EnumValues: []string{"ORPHAN", "DUPLICATE", "CONFLICT", "STALE", "MISSING_SOURCE", "LOW_CONFIDENCE", "BROKEN_REFERENCE", "INDEX_ERROR", "SUPERSEDED_USAGE", "REVIEW_INVALIDATED"}},
		{Name: "source_type", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeEnum, Operators: []Operator{OperatorEQ, OperatorIN, OperatorPrefix}, Sortable: true, Groupable: true, EnumValues: []string{"markdown", "text", "pdf", "html", "file"}},
		{Name: "file_path", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypePath, Operators: []Operator{OperatorEQ, OperatorIN, OperatorPrefix}, Sortable: true},
		{Name: "text", Availability: FieldAvailabilityAvailable, ValueType: FieldValueTypeText, Operators: []Operator{OperatorContains, OperatorPrefix}, Sortable: true},
		{Name: "tag", Availability: FieldAvailabilityUnavailable, ValueType: FieldValueTypeText, Operators: []Operator{OperatorEQ, OperatorIN}},
		{Name: "review_status", Availability: FieldAvailabilityUnavailable, ValueType: FieldValueTypeEnum, Operators: []Operator{OperatorEQ, OperatorIN}},
		{Name: "document_id", Availability: FieldAvailabilityUnavailable, ValueType: FieldValueTypeID, Operators: []Operator{OperatorEQ, OperatorIN}},
		{Name: "directory_id", Availability: FieldAvailabilityUnavailable, ValueType: FieldValueTypeID, Operators: []Operator{OperatorEQ, OperatorIN}},
	}
	columns := []ViewColumnDefinition{
		{Name: "object_type"},
		{Name: "title"},
		{Name: "summary"},
		{Name: "status"},
		{Name: "topic"},
		{Name: "source"},
		{Name: "relation_summary"},
		{Name: "health_summary"},
		{Name: "confidence"},
		{Name: "created_at"},
		{Name: "updated_at"},
	}
	return Registry{
		Fields:      fields,
		ViewColumns: columns,
	}
}
