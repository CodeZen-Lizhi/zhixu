package domain

import "strings"

// ViewType 表示集合结果的受控展示形态。
type ViewType string

const (
	// ViewTypeList 表示列表视图。
	ViewTypeList ViewType = "LIST"
	// ViewTypeTable 表示表格视图。
	ViewTypeTable ViewType = "TABLE"
	// ViewTypeCompactCard 表示紧凑卡片视图。
	ViewTypeCompactCard ViewType = "COMPACT_CARD"
)

// ViewDensity 表示列表密度。
type ViewDensity string

const (
	// ViewDensityComfortable 表示默认舒适密度。
	ViewDensityComfortable ViewDensity = "COMFORTABLE"
	// ViewDensityCompact 表示更紧凑的密度。
	ViewDensityCompact ViewDensity = "COMPACT"
)

// GroupBy 表示视图分组字段。
type GroupBy struct {
	Field string `json:"field"`
}

// ViewConfig 保存不改变结果成员的视图偏好。
type ViewConfig struct {
	Columns      []string   `json:"columns,omitempty"`
	FixedColumns []string   `json:"fixed_columns,omitempty"`
	Sort         []SortTerm `json:"sort,omitempty"`
	GroupBy      *GroupBy   `json:"group_by,omitempty"`
	Density      string     `json:"density,omitempty"`
}

// ValidateViewConfig 校验视图类型与配置是否合法。
func ValidateViewConfig(viewType ViewType, config ViewConfig) error {
	_, err := CanonicalizeViewConfig(viewType, config)
	return err
}

// CanonicalizeViewConfig 规范化列、固定列、排序、分组和密度配置。
func CanonicalizeViewConfig(viewType ViewType, config ViewConfig) (ViewConfig, error) {
	normalizedType, err := canonicalizeViewType(viewType)
	if err != nil {
		return ViewConfig{}, err
	}
	registry := RegistryV1()
	columns, err := canonicalizeColumns(registry, config.Columns)
	if err != nil {
		return ViewConfig{}, err
	}
	fixedColumns, err := canonicalizeColumns(registry, config.FixedColumns)
	if err != nil {
		return ViewConfig{}, err
	}
	if !isSubset(columns, fixedColumns) {
		return ViewConfig{}, invalid(ErrorCodeViewConfigInvalid, "fixed columns must be selected columns")
	}
	sortTerms, err := canonicalizeSortTerms(registry, config.Sort, false)
	if err != nil {
		return ViewConfig{}, invalid(ErrorCodeViewConfigInvalid, err.Error())
	}
	var groupBy *GroupBy
	if config.GroupBy != nil {
		fieldName := normalizeName(config.GroupBy.Field)
		field, ok := registry.Field(fieldName)
		if !ok || !field.Groupable || field.Availability != FieldAvailabilityAvailable {
			return ViewConfig{}, invalid(ErrorCodeViewConfigInvalid, "group field is not registered or not groupable")
		}
		groupBy = &GroupBy{Field: field.Name}
	}
	density, err := canonicalizeViewDensity(config.Density)
	if err != nil {
		return ViewConfig{}, err
	}
	_ = normalizedType
	return ViewConfig{
		Columns:      columns,
		FixedColumns: fixedColumns,
		Sort:         sortTerms,
		GroupBy:      groupBy,
		Density:      string(density),
	}, nil
}

func canonicalizeViewType(viewType ViewType) (ViewType, error) {
	switch strings.ToUpper(strings.TrimSpace(string(viewType))) {
	case string(ViewTypeList):
		return ViewTypeList, nil
	case string(ViewTypeTable):
		return ViewTypeTable, nil
	case string(ViewTypeCompactCard):
		return ViewTypeCompactCard, nil
	default:
		return "", invalid(ErrorCodeViewConfigInvalid, "view type is invalid")
	}
}

func canonicalizeViewDensity(value string) (ViewDensity, error) {
	if strings.TrimSpace(value) == "" {
		return ViewDensityComfortable, nil
	}
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(ViewDensityComfortable):
		return ViewDensityComfortable, nil
	case string(ViewDensityCompact):
		return ViewDensityCompact, nil
	default:
		return "", invalid(ErrorCodeViewConfigInvalid, "view density is invalid")
	}
}

func canonicalizeColumns(registry Registry, columns []string) ([]string, error) {
	if len(columns) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		normalized := normalizeName(column)
		definition, ok := registry.ViewColumn(normalized)
		if !ok {
			return nil, invalid(ErrorCodeViewConfigInvalid, "view column is not registered")
		}
		if _, exists := seen[definition.Name]; exists {
			continue
		}
		seen[definition.Name] = struct{}{}
		result = append(result, definition.Name)
	}
	return result, nil
}

func isSubset(columns, fixed []string) bool {
	if len(fixed) == 0 {
		return true
	}
	available := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		available[column] = struct{}{}
	}
	for _, column := range fixed {
		if _, ok := available[column]; !ok {
			return false
		}
	}
	return true
}
