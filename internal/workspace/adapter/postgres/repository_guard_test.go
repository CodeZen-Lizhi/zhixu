package workspacepostgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestBuildSourceVersionListQueryExcludesTombstonedSources(t *testing.T) {
	query, _ := buildSourceVersionListQuery(domain.SourceVersionListQuery{
		WorkspaceID: foundation.ID("00000000-0000-4000-8000-000000000001"),
		Limit:       1,
	})
	if !strings.Contains(query, "s.removed_at IS NULL") {
		t.Fatalf("source version list query includes tombstoned sources:\n%s", query)
	}
}

func TestManagedRepositoryRejectsRootListingBeforeDatabaseAccess(t *testing.T) {
	repository := &Repository{managed: true}

	_, err := repository.ListWorkspaceRoots(context.Background())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorPermissionDenied || classified.Code != rootgrant.ErrorCodeRootNotGranted {
		t.Fatalf("ListWorkspaceRoots() error=%v", err)
	}
}
