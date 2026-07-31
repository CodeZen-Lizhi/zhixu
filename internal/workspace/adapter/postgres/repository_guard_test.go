package workspacepostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/rootgrant"
)

func TestManagedRepositoryRejectsRootListingBeforeDatabaseAccess(t *testing.T) {
	repository := &Repository{managed: true}

	_, err := repository.ListWorkspaceRoots(context.Background())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorPermissionDenied || classified.Code != rootgrant.ErrorCodeRootNotGranted {
		t.Fatalf("ListWorkspaceRoots() error=%v", err)
	}
}
