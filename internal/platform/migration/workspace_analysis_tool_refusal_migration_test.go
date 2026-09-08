package migration

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"

	atlasmigrations "github.com/CodeZen-Lizhi/zhixu/atlas"
)

func TestWorkspaceAnalysisToolRefusalMigrationPrecedesSnapshotGuardFix(t *testing.T) {
	entries, err := fs.ReadDir(atlasmigrations.MigrationDir(), ".")
	if err != nil {
		t.Fatal(err)
	}
	latest := 0
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionText, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			t.Fatalf("invalid migration name %q", entry.Name())
		}
		version, err := strconv.Atoi(versionText)
		if err != nil {
			t.Fatalf("invalid migration name %q: %v", entry.Name(), err)
		}
		if version == 89 && entry.Name() == "00089_workspace_analysis_tool_refusal_audit.sql" {
			found = true
		}
		if version > latest {
			latest = version
		}
	}
	if !found || latest != 93 {
		t.Fatalf("workspace analysis tool refusal migration found=%t latest=%d", found, latest)
	}
}
