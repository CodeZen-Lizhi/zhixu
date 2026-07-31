package rootgrant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	testWorkspaceID      foundation.ID = "550e8400-e29b-41d4-a716-446655440000"
	testOtherWorkspaceID foundation.ID = "c56a4180-65aa-42ec-a945-5fd21dec0538"
)

type authoritativeStoreStub struct {
	mu    sync.Mutex
	views []AuthoritativeView
	err   error
	calls int
}

func (s *authoritativeStoreStub) CurrentRootGrant(context.Context) (AuthoritativeView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return AuthoritativeView{}, s.err
	}
	if len(s.views) == 0 {
		return AuthoritativeView{}, nil
	}
	index := s.calls - 1
	if index >= len(s.views) {
		index = len(s.views) - 1
	}
	return s.views[index], nil
}

func TestRootGrantResolverHappyPathAndClose(t *testing.T) {
	root := canonicalDirectory(t)
	view := activeView(testWorkspaceID, root, 7)
	store := &authoritativeStoreStub{views: []AuthoritativeView{view}}
	resolver := newManagedResolver(t, store, testWorkspaceID, root, 7)

	capability, err := resolver.Resolve(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("resolve root grant: %v", err)
	}
	if capability.WorkspaceID() != testWorkspaceID || capability.CanonicalRoot() != root || capability.Generation() != 7 {
		t.Fatalf("unexpected capability binding: id=%q root=%q generation=%d", capability.WorkspaceID(), capability.CanonicalRoot(), capability.Generation())
	}
	handle, err := capability.Root()
	if err != nil {
		t.Fatalf("get opened root: %v", err)
	}
	if _, err := handle.Stat("."); err != nil {
		t.Fatalf("stat opened root: %v", err)
	}
	externalPath, err := capability.PathForExternalCommand(context.Background())
	if err != nil {
		t.Fatalf("revalidate external command path: %v", err)
	}
	if externalPath != root {
		t.Fatalf("external command path = %q, want %q", externalPath, root)
	}

	if err := capability.Close(); err != nil {
		t.Fatalf("close capability: %v", err)
	}
	if err := capability.Close(); err != nil {
		t.Fatalf("close capability twice: %v", err)
	}
	if _, err := handle.Stat("."); err == nil {
		t.Fatal("opened root remained usable after capability close")
	}
	_, err = capability.Root()
	requireGrantError(t, err, foundation.ErrorDependencyUnavailable, ErrorCodeGrantStale, false)
}

func TestRootGrantResolverRejectsMissingInactiveAndZeroGrant(t *testing.T) {
	root := canonicalDirectory(t)
	tests := []struct {
		name string
		view AuthoritativeView
	}{
		{name: "missing", view: AuthoritativeView{}},
		{name: "inactive", view: AuthoritativeView{
			ActiveWorkspaceID: testWorkspaceID, WorkspaceID: testWorkspaceID,
			WorkspaceAvailable: true, PersistedRoot: root, GrantGeneration: 9,
		}},
		{name: "unavailable", view: AuthoritativeView{
			ActiveWorkspaceID: testWorkspaceID, WorkspaceID: testWorkspaceID, WorkspaceActive: true,
			PersistedRoot: root, GrantGeneration: 9,
		}},
		{name: "zero generation", view: AuthoritativeView{
			ActiveWorkspaceID: testWorkspaceID, WorkspaceID: testWorkspaceID, WorkspaceActive: true, WorkspaceAvailable: true,
			PersistedRoot: root,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &authoritativeStoreStub{views: []AuthoritativeView{test.view}}
			resolver := newManagedResolver(t, store, testWorkspaceID, root, 9)
			_, err := resolver.Resolve(context.Background(), testWorkspaceID)
			requireGrantError(t, err, foundation.ErrorPermissionDenied, ErrorCodeRootNotGranted, false)
		})
	}
}

func TestRootGrantResolverRejectsWrongWorkspaceWithoutConsultingStore(t *testing.T) {
	root := canonicalDirectory(t)
	store := &authoritativeStoreStub{views: []AuthoritativeView{activeView(testWorkspaceID, root, 4)}}
	resolver := newManagedResolver(t, store, testWorkspaceID, root, 4)

	_, err := resolver.Resolve(context.Background(), testOtherWorkspaceID)
	requireGrantError(t, err, foundation.ErrorPermissionDenied, ErrorCodeRootNotGranted, false)
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want 0 for process-grant ID mismatch", store.calls)
	}
}

func TestRootGrantResolverRejectsRootAndGenerationMismatch(t *testing.T) {
	root := canonicalDirectory(t)
	otherRoot := canonicalDirectory(t)
	tests := []struct {
		name string
		view AuthoritativeView
	}{
		{name: "root", view: activeView(testWorkspaceID, otherRoot, 5)},
		{name: "generation", view: activeView(testWorkspaceID, root, 6)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &authoritativeStoreStub{views: []AuthoritativeView{test.view}}
			resolver := newManagedResolver(t, store, testWorkspaceID, root, 5)
			_, err := resolver.Resolve(context.Background(), testWorkspaceID)
			requireGrantError(t, err, foundation.ErrorVersionConflict, ErrorCodeGrantStale, false)
			if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), otherRoot) {
				t.Fatalf("grant error leaked a host path: %v", err)
			}
		})
	}
}

func TestRootGrantResolverRejectsInconsistentStoreIdentity(t *testing.T) {
	root := canonicalDirectory(t)
	view := activeView(testWorkspaceID, root, 3)
	view.WorkspaceID = testOtherWorkspaceID
	store := &authoritativeStoreStub{views: []AuthoritativeView{view}}
	resolver := newManagedResolver(t, store, testWorkspaceID, root, 3)

	_, err := resolver.Resolve(context.Background(), testWorkspaceID)
	requireGrantError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeGrantStale, false)
}

func TestRootGrantResolverDoesNotFallbackWhenAuthorityFails(t *testing.T) {
	root := canonicalDirectory(t)
	store := &authoritativeStoreStub{err: errors.New("database unavailable at /private/secret/workspace")}
	resolver := newManagedResolver(t, store, testWorkspaceID, root, 2)

	capability, err := resolver.Resolve(context.Background(), testWorkspaceID)
	if capability != nil {
		_ = capability.Close()
		t.Fatal("managed resolver returned a capability after authority failure")
	}
	requireGrantError(t, err, foundation.ErrorDependencyUnavailable, ErrorCodeGrantStale, true)
	if strings.Contains(err.Error(), "/private/secret/workspace") {
		t.Fatalf("grant error leaked an authority path: %v", err)
	}
}

func TestRootGrantResolverRejectsStoreChangeDuringResolve(t *testing.T) {
	root := canonicalDirectory(t)
	first := activeView(testWorkspaceID, root, 11)
	second := activeView(testWorkspaceID, root, 12)
	store := &authoritativeStoreStub{views: []AuthoritativeView{first, second}}
	resolver := newManagedResolver(t, store, testWorkspaceID, root, 11)

	capability, err := resolver.Resolve(context.Background(), testWorkspaceID)
	if capability != nil {
		_ = capability.Close()
		t.Fatal("resolver returned a capability across a generation change")
	}
	requireGrantError(t, err, foundation.ErrorVersionConflict, ErrorCodeGrantStale, false)
}

func TestRootGrantResolverRejectsSymlinkGrant(t *testing.T) {
	parent := canonicalDirectory(t)
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	grant, err := NewProcessGrant(testWorkspaceID, link, 1)
	if err != nil {
		t.Fatalf("create lexical process grant: %v", err)
	}
	store := &authoritativeStoreStub{views: []AuthoritativeView{activeView(testWorkspaceID, link, 1)}}

	resolver, err := NewRootGrantResolver(store, grant)
	if resolver != nil {
		_ = resolver.Close()
		t.Fatal("symlink grant unexpectedly created a resolver")
	}
	requireGrantError(t, err, foundation.ErrorVersionConflict, ErrorCodeGrantStale, false)
}

func TestExternalPathRevalidationRejectsDirectoryReplacement(t *testing.T) {
	parent := canonicalDirectory(t)
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	view := activeView(testWorkspaceID, root, 8)
	store := &authoritativeStoreStub{views: []AuthoritativeView{view}}
	resolver := newManagedResolver(t, store, testWorkspaceID, root, 8)
	capability, err := resolver.Resolve(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("resolve root grant: %v", err)
	}
	t.Cleanup(func() { _ = capability.Close() })
	handle, err := capability.Root()
	if err != nil {
		t.Fatalf("get opened root: %v", err)
	}

	moved := filepath.Join(parent, "workspace-old")
	if err := os.Rename(root, moved); err != nil {
		t.Fatalf("move granted root: %v", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("replace granted root: %v", err)
	}
	if _, err := handle.Stat("."); err != nil {
		t.Fatalf("opened capability stopped referencing the original directory: %v", err)
	}

	path, err := capability.PathForExternalCommand(context.Background())
	if path != "" {
		t.Fatalf("external path = %q after replacement, want empty", path)
	}
	requireGrantError(t, err, foundation.ErrorVersionConflict, ErrorCodeGrantStale, false)
	_, err = resolver.Resolve(context.Background(), testWorkspaceID)
	requireGrantError(t, err, foundation.ErrorVersionConflict, ErrorCodeGrantStale, false)
}

func TestExternalPathRevalidationRejectsUnavailableRoot(t *testing.T) {
	parent := canonicalDirectory(t)
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	view := activeView(testWorkspaceID, root, 10)
	store := &authoritativeStoreStub{views: []AuthoritativeView{view}}
	resolver := newManagedResolver(t, store, testWorkspaceID, root, 10)
	capability, err := resolver.Resolve(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("resolve root grant: %v", err)
	}
	t.Cleanup(func() { _ = capability.Close() })

	if err := os.Rename(root, filepath.Join(parent, "workspace-unavailable")); err != nil {
		t.Fatalf("make granted root unavailable: %v", err)
	}
	_, err = capability.PathForExternalCommand(context.Background())
	requireGrantError(t, err, foundation.ErrorVersionConflict, ErrorCodeGrantStale, false)
}

func TestDirectRootGrantResolverRequiresExplicitConstructor(t *testing.T) {
	root := canonicalDirectory(t)
	view := activeView(testWorkspaceID, root, 15)
	store := &authoritativeStoreStub{views: []AuthoritativeView{view}}
	resolver, err := NewDirectRootGrantResolver(store)
	if err != nil {
		t.Fatalf("create direct resolver: %v", err)
	}
	t.Cleanup(func() { _ = resolver.Close() })

	capability, err := resolver.Resolve(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("resolve direct root grant: %v", err)
	}
	t.Cleanup(func() { _ = capability.Close() })
	if capability.CanonicalRoot() != root || capability.Generation() != 15 {
		t.Fatalf("unexpected direct capability: root=%q generation=%d", capability.CanonicalRoot(), capability.Generation())
	}
}

func TestCandidateRootCapabilityHasStartupOnlySurface(t *testing.T) {
	root := canonicalDirectory(t)
	grant, err := NewProcessGrant(testWorkspaceID, root, 21)
	if err != nil {
		t.Fatalf("create process grant: %v", err)
	}
	candidate, err := NewCandidateRootCapability(grant)
	if err != nil {
		t.Fatalf("create candidate capability: %v", err)
	}
	if candidate.WorkspaceID() != testWorkspaceID || candidate.Generation() != 21 {
		t.Fatalf("unexpected candidate binding: id=%q generation=%d", candidate.WorkspaceID(), candidate.Generation())
	}
	probeRoot, err := candidate.ProbeRoot()
	if err != nil {
		t.Fatalf("get candidate probe root: %v", err)
	}
	if _, err := probeRoot.Stat("."); err != nil {
		t.Fatalf("stat candidate probe root: %v", err)
	}
	gitRoot, err := candidate.PathForGitInit()
	if err != nil || gitRoot != root {
		t.Fatalf("candidate git init path = %q, err=%v", gitRoot, err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatalf("close candidate: %v", err)
	}
	if _, err := candidate.ProbeRoot(); err == nil {
		t.Fatal("closed candidate still exposed its probe root")
	}
}

func TestCandidateGitInitPathRejectsDirectoryReplacement(t *testing.T) {
	parent := canonicalDirectory(t)
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	grant, err := NewProcessGrant(testWorkspaceID, root, 22)
	if err != nil {
		t.Fatalf("create process grant: %v", err)
	}
	candidate, err := NewCandidateRootCapability(grant)
	if err != nil {
		t.Fatalf("create candidate capability: %v", err)
	}
	t.Cleanup(func() { _ = candidate.Close() })

	if err := os.Rename(root, filepath.Join(parent, "workspace-old")); err != nil {
		t.Fatalf("move candidate root: %v", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("replace candidate root: %v", err)
	}
	path, err := candidate.PathForGitInit()
	if path != "" {
		t.Fatalf("candidate git init path = %q after replacement, want empty", path)
	}
	requireGrantError(t, err, foundation.ErrorVersionConflict, ErrorCodeGrantStale, false)
}

func activeView(workspaceID foundation.ID, root string, generation int64) AuthoritativeView {
	return AuthoritativeView{
		ActiveWorkspaceID:  workspaceID,
		WorkspaceID:        workspaceID,
		WorkspaceActive:    true,
		WorkspaceAvailable: true,
		PersistedRoot:      root,
		GrantGeneration:    generation,
	}
}

func canonicalDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temporary directory: %v", err)
	}
	return filepath.Clean(directory)
}

func newManagedResolver(t *testing.T, store AuthoritativeStore, workspaceID foundation.ID, root string, generation int64) *RootGrantResolver {
	t.Helper()
	grant, err := NewProcessGrant(workspaceID, root, generation)
	if err != nil {
		t.Fatalf("create process grant: %v", err)
	}
	resolver, err := NewRootGrantResolver(store, grant)
	if err != nil {
		t.Fatalf("create managed resolver: %v", err)
	}
	t.Cleanup(func() {
		if err := resolver.Close(); err != nil {
			t.Errorf("close resolver: %v", err)
		}
	})
	return resolver
}
