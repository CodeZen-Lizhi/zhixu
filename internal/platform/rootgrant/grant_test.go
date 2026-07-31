package rootgrant

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestLoadProcessGrantFromEnvironment(t *testing.T) {
	t.Setenv(EnvWorkspaceGrantedID, string(testWorkspaceID))
	t.Setenv(EnvWorkspaceGrantedRoot, "/tmp/zhixu workspace")
	t.Setenv(EnvWorkspaceGrantGeneration, "17")

	grant, err := LoadProcessGrantFromEnvironment()
	if err != nil {
		t.Fatalf("load process grant: %v", err)
	}
	if grant.WorkspaceID() != testWorkspaceID || grant.CanonicalRoot() != "/tmp/zhixu workspace" || grant.Generation() != 17 {
		t.Fatalf("unexpected process grant: id=%q root=%q generation=%d", grant.WorkspaceID(), grant.CanonicalRoot(), grant.Generation())
	}
}

func TestLoadProcessGrantRejectsIncompleteOrNonCanonicalValues(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "missing generation", values: map[string]string{
			EnvWorkspaceGrantedID: string(testWorkspaceID), EnvWorkspaceGrantedRoot: "/tmp/workspace",
		}},
		{name: "relative root", values: map[string]string{
			EnvWorkspaceGrantedID: string(testWorkspaceID), EnvWorkspaceGrantedRoot: "workspace", EnvWorkspaceGrantGeneration: "1",
		}},
		{name: "unclean root", values: map[string]string{
			EnvWorkspaceGrantedID: string(testWorkspaceID), EnvWorkspaceGrantedRoot: "/tmp/a/../workspace", EnvWorkspaceGrantGeneration: "1",
		}},
		{name: "zero generation", values: map[string]string{
			EnvWorkspaceGrantedID: string(testWorkspaceID), EnvWorkspaceGrantedRoot: "/tmp/workspace", EnvWorkspaceGrantGeneration: "0",
		}},
		{name: "noncanonical generation", values: map[string]string{
			EnvWorkspaceGrantedID: string(testWorkspaceID), EnvWorkspaceGrantedRoot: "/tmp/workspace", EnvWorkspaceGrantGeneration: "01",
		}},
		{name: "noncanonical id", values: map[string]string{
			EnvWorkspaceGrantedID: "550E8400-E29B-41D4-A716-446655440000", EnvWorkspaceGrantedRoot: "/tmp/workspace", EnvWorkspaceGrantGeneration: "1",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadProcessGrant(func(key string) (string, bool) {
				value, ok := test.values[key]
				return value, ok
			})
			requireGrantError(t, err, foundation.ErrorPermissionDenied, ErrorCodeRootNotGranted, false)
		})
	}
}

func TestLoadProcessGrantRejectsNilLookup(t *testing.T) {
	_, err := LoadProcessGrant(nil)
	requireGrantError(t, err, foundation.ErrorPermissionDenied, ErrorCodeRootNotGranted, false)
}

func TestLoadRuntimeGrantUsesDirectModeOnlyWhenGrantIsFullyAbsent(t *testing.T) {
	grant, mode, err := LoadRuntimeGrant(func(string) (string, bool) { return "", false })
	if err != nil || mode != RuntimeGrantDirect || grant.WorkspaceID() != "" {
		t.Fatalf("direct runtime grant = %#v mode=%d error=%v", grant, mode, err)
	}

	values := map[string]string{
		EnvWorkspaceGrantedID:       string(testWorkspaceID),
		EnvWorkspaceGrantedRoot:     "/tmp/workspace",
		EnvWorkspaceGrantGeneration: "2",
	}
	grant, mode, err = LoadRuntimeGrant(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil || mode != RuntimeGrantManaged || grant.WorkspaceID() != testWorkspaceID || grant.Generation() != 2 {
		t.Fatalf("managed runtime grant = %#v mode=%d error=%v", grant, mode, err)
	}
}

func TestLoadRuntimeGrantRejectsPartialManagedGrant(t *testing.T) {
	_, _, err := LoadRuntimeGrant(func(key string) (string, bool) {
		if key == EnvWorkspaceGrantedRoot {
			return "/tmp/workspace", true
		}
		return "", false
	})
	requireGrantError(t, err, foundation.ErrorPermissionDenied, ErrorCodeRootNotGranted, false)
}

func requireGrantError(t *testing.T, err error, kind foundation.ErrorKind, code string, retryable bool) *foundation.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		t.Fatalf("expected foundation error, got %T: %v", err, err)
	}
	if classified.Kind != kind || classified.Code != code || classified.Retryable != retryable {
		t.Fatalf("unexpected error classification: kind=%q code=%q retryable=%t", classified.Kind, classified.Code, classified.Retryable)
	}
	return classified
}
