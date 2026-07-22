package domain

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestCanonicalizeViewConfigAcceptsRegisteredColumnsAndSorts(t *testing.T) {
	t.Parallel()
	config, err := CanonicalizeViewConfig(ViewTypeTable, ViewConfig{
		Columns:      []string{"title", "status", "title", "updated_at"},
		FixedColumns: []string{"title", "title"},
		Sort: []SortTerm{
			{Field: "updated_at", Direction: "desc"},
			{Field: "updated_at", Direction: "DESC"},
		},
		GroupBy: &GroupBy{Field: "status"},
		Density: "compact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(config.Columns), 3; got != want || config.Columns[0] != "title" || config.FixedColumns[0] != "title" {
		t.Fatalf("config=%#v", config)
	}
	if config.Sort[0] != (SortTerm{Field: "updated_at", Direction: string(SortDirectionDESC)}) {
		t.Fatalf("sort=%#v", config.Sort)
	}
	if config.GroupBy == nil || config.GroupBy.Field != "status" || config.Density != string(ViewDensityCompact) {
		t.Fatalf("config=%#v", config)
	}
}

func TestCanonicalizeViewConfigRejectsInvalidViewTypesAndColumns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		viewType ViewType
		config   ViewConfig
		code     string
	}{
		{
			name:     "invalid view type",
			viewType: "GRID",
			config:   ViewConfig{},
			code:     ErrorCodeViewConfigInvalid,
		},
		{
			name:     "unknown column",
			viewType: ViewTypeList,
			config:   ViewConfig{Columns: []string{"title", "unknown"}},
			code:     ErrorCodeViewConfigInvalid,
		},
		{
			name:     "fixed column not selected",
			viewType: ViewTypeTable,
			config:   ViewConfig{Columns: []string{"title"}, FixedColumns: []string{"status"}},
			code:     ErrorCodeViewConfigInvalid,
		},
		{
			name:     "invalid group field",
			viewType: ViewTypeCompactCard,
			config:   ViewConfig{GroupBy: &GroupBy{Field: "file_path"}},
			code:     ErrorCodeViewConfigInvalid,
		},
		{
			name:     "invalid density",
			viewType: ViewTypeTable,
			config:   ViewConfig{Density: "ROOMY"},
			code:     ErrorCodeViewConfigInvalid,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := CanonicalizeViewConfig(test.viewType, test.config)
			assertDomainError(t, err, foundation.ErrorInvalidInput, test.code)
		})
	}
}

func TestCanonicalizeViewConfigSupportsAllFrozenViewTypes(t *testing.T) {
	t.Parallel()
	for _, viewType := range []ViewType{ViewTypeList, ViewTypeTable, ViewTypeCompactCard} {
		viewType := viewType
		t.Run(string(viewType), func(t *testing.T) {
			t.Parallel()
			if _, err := CanonicalizeViewConfig(viewType, ViewConfig{}); err != nil {
				t.Fatalf("view type %s rejected: %v", viewType, err)
			}
		})
	}
}
