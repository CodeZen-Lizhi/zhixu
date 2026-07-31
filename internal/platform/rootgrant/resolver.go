package rootgrant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// AuthoritativeView is a narrow, non-capability projection of the control singleton and its active Workspace row.
// PersistedRoot remains untrusted until RootGrantResolver validates the complete binding.
type AuthoritativeView struct {
	ActiveWorkspaceID  foundation.ID
	WorkspaceID        foundation.ID
	WorkspaceActive    bool
	WorkspaceAvailable bool
	PersistedRoot      string
	GrantGeneration    int64
}

// AuthoritativeStore returns the current singleton and active Workspace in one authoritative view.
// A system with no active grant returns the zero view rather than a not-found error.
type AuthoritativeStore interface {
	CurrentRootGrant(context.Context) (AuthoritativeView, error)
}

type resolverMode uint8

const (
	resolverModeManaged resolverMode = iota + 1
	resolverModeDirect
)

type rootBinding struct {
	workspaceID   foundation.ID
	canonicalRoot string
	generation    int64
}

type openedRoot struct {
	handle   *os.Root
	identity os.FileInfo
}

// RootGrantResolver resolves only the current Active Workspace into an opened root capability.
type RootGrantResolver struct {
	mu     sync.RWMutex
	store  AuthoritativeStore
	mode   resolverMode
	grant  rootBinding
	anchor openedRoot
	closed bool
}

// NewRootGrantResolver creates a managed resolver pinned to the explicit process grant.
func NewRootGrantResolver(store AuthoritativeStore, grant ProcessGrant) (*RootGrantResolver, error) {
	if store == nil {
		return nil, staleDependencyError(true)
	}
	if !validWorkspaceID(grant.workspaceID) || !validCanonicalRootSyntax(grant.canonicalRoot) || grant.generation < 1 {
		return nil, notGrantedError(errGrantInvalid)
	}
	anchor, err := openCanonicalRoot(grant.canonicalRoot)
	if err != nil {
		return nil, err
	}
	return &RootGrantResolver{
		store:  store,
		mode:   resolverModeManaged,
		grant:  bindingFromProcessGrant(grant),
		anchor: anchor,
	}, nil
}

// NewDirectRootGrantResolver explicitly enables native development mode without a process grant.
// It never acts as a fallback when a managed store or runtime is unavailable.
func NewDirectRootGrantResolver(store AuthoritativeStore) (*RootGrantResolver, error) {
	if store == nil {
		return nil, staleDependencyError(true)
	}
	return &RootGrantResolver{store: store, mode: resolverModeDirect}, nil
}

// Resolve verifies the current authoritative grant and returns a newly opened capability.
func (r *RootGrantResolver) Resolve(ctx context.Context, workspaceID foundation.ID) (*Capability, error) {
	if r == nil {
		return nil, staleDependencyError(false)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, staleDependencyError(false)
	}
	if !validWorkspaceID(workspaceID) {
		return nil, notGrantedError(errRootNotGranted)
	}
	if r.mode == resolverModeManaged && workspaceID != r.grant.workspaceID {
		return nil, notGrantedError(errRootNotGranted)
	}
	if err := r.validateManagedAnchor(); err != nil {
		return nil, err
	}

	view, err := r.currentView(ctx)
	if err != nil {
		return nil, err
	}
	binding, err := r.bindingForView(workspaceID, view, nil)
	if err != nil {
		return nil, err
	}
	opened, err := openCanonicalRoot(binding.canonicalRoot)
	if err != nil {
		return nil, err
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = opened.handle.Close()
		}
	}()
	if r.mode == resolverModeManaged && !os.SameFile(r.anchor.identity, opened.identity) {
		return nil, staleConflictError()
	}

	view, err = r.currentView(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := r.bindingForView(workspaceID, view, &binding); err != nil {
		return nil, err
	}
	if err := validateRootIdentity(binding.canonicalRoot, opened); err != nil {
		return nil, err
	}

	keepOpen = true
	return &Capability{resolver: r, binding: binding, opened: opened}, nil
}

// Close releases the managed process anchor and prevents future resolution or external-path revalidation.
func (r *RootGrantResolver) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.anchor.handle == nil {
		return nil
	}
	if err := r.anchor.handle.Close(); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeGrantStale, true, errCapabilityCleanup)
	}
	r.anchor = openedRoot{}
	return nil
}

func (r *RootGrantResolver) revalidate(ctx context.Context, binding rootBinding, opened openedRoot) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return staleDependencyError(false)
	}
	if err := r.validateManagedAnchor(); err != nil {
		return err
	}
	view, err := r.currentView(ctx)
	if err != nil {
		return err
	}
	if _, err := r.bindingForView(binding.workspaceID, view, &binding); err != nil {
		return err
	}
	return validateRootIdentity(binding.canonicalRoot, opened)
}

func (r *RootGrantResolver) currentView(ctx context.Context) (AuthoritativeView, error) {
	if ctx == nil {
		return AuthoritativeView{}, staleCancellationError(context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return AuthoritativeView{}, staleCancellationError(err)
	}
	view, err := r.store.CurrentRootGrant(ctx)
	if err == nil {
		return view, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return AuthoritativeView{}, staleCancellationError(err)
	}
	return AuthoritativeView{}, staleDependencyError(true)
}

func (r *RootGrantResolver) bindingForView(workspaceID foundation.ID, view AuthoritativeView, expected *rootBinding) (rootBinding, error) {
	if view.ActiveWorkspaceID == "" || view.WorkspaceID == "" || !view.WorkspaceActive || !view.WorkspaceAvailable || view.GrantGeneration < 1 {
		return rootBinding{}, notGrantedError(errRootNotGranted)
	}
	if !validWorkspaceID(view.ActiveWorkspaceID) || !validWorkspaceID(view.WorkspaceID) || view.ActiveWorkspaceID != view.WorkspaceID {
		return rootBinding{}, staleConsistencyError()
	}
	if workspaceID != view.ActiveWorkspaceID {
		return rootBinding{}, notGrantedError(errRootNotGranted)
	}
	if !validCanonicalRootSyntax(view.PersistedRoot) {
		return rootBinding{}, staleConsistencyError()
	}
	binding := rootBinding{
		workspaceID:   view.WorkspaceID,
		canonicalRoot: view.PersistedRoot,
		generation:    view.GrantGeneration,
	}
	if r.mode == resolverModeManaged && binding != r.grant {
		return rootBinding{}, staleConflictError()
	}
	if expected != nil && binding != *expected {
		return rootBinding{}, staleConflictError()
	}
	return binding, nil
}

func (r *RootGrantResolver) validateManagedAnchor() error {
	if r.mode != resolverModeManaged {
		return nil
	}
	if r.anchor.handle == nil {
		return staleDependencyError(false)
	}
	return validateRootIdentity(r.grant.canonicalRoot, r.anchor)
}

func bindingFromProcessGrant(grant ProcessGrant) rootBinding {
	return rootBinding{
		workspaceID:   grant.workspaceID,
		canonicalRoot: grant.canonicalRoot,
		generation:    grant.generation,
	}
}

func openCanonicalRoot(path string) (openedRoot, error) {
	if !validCanonicalRootSyntax(path) {
		return openedRoot{}, staleConflictError()
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != path {
		return openedRoot{}, staleConflictError()
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return openedRoot{}, staleConflictError()
	}
	handle, err := os.OpenRoot(path)
	if err != nil {
		return openedRoot{}, staleConflictError()
	}
	identity, err := handle.Stat(".")
	if err != nil || !identity.IsDir() || !os.SameFile(pathInfo, identity) {
		_ = handle.Close()
		return openedRoot{}, staleConflictError()
	}
	opened := openedRoot{handle: handle, identity: identity}
	if err := validateRootIdentity(path, opened); err != nil {
		_ = handle.Close()
		return openedRoot{}, err
	}
	return opened, nil
}

func validateRootIdentity(path string, opened openedRoot) error {
	if opened.handle == nil || opened.identity == nil || !validCanonicalRootSyntax(path) {
		return staleConflictError()
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != path {
		return staleConflictError()
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return staleConflictError()
	}
	handleInfo, err := opened.handle.Stat(".")
	if err != nil || !handleInfo.IsDir() || !os.SameFile(opened.identity, handleInfo) || !os.SameFile(pathInfo, handleInfo) {
		return staleConflictError()
	}
	return nil
}

// Capability binds an Active Workspace grant to one opened filesystem root.
type Capability struct {
	mu       sync.RWMutex
	resolver *RootGrantResolver
	binding  rootBinding
	opened   openedRoot
	closed   bool
}

// WorkspaceID returns the Workspace identity bound to this capability.
func (c *Capability) WorkspaceID() foundation.ID {
	if c == nil {
		return ""
	}
	return c.binding.workspaceID
}

// CanonicalRoot returns the validated capability path, never a separately loaded persisted path.
func (c *Capability) CanonicalRoot() string {
	if c == nil {
		return ""
	}
	return c.binding.canonicalRoot
}

// Generation returns the active grant fence bound to this capability.
func (c *Capability) Generation() int64 {
	if c == nil {
		return 0
	}
	return c.binding.generation
}

// Root returns the already opened filesystem root while the capability remains open.
func (c *Capability) Root() (*os.Root, error) {
	if c == nil {
		return nil, staleDependencyError(false)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.opened.handle == nil {
		return nil, staleDependencyError(false)
	}
	return c.opened.handle, nil
}

// Revalidate verifies Active identity, generation and current path identity again.
func (c *Capability) Revalidate(ctx context.Context) error {
	if c == nil {
		return staleDependencyError(false)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.opened.handle == nil || c.resolver == nil {
		return staleDependencyError(false)
	}
	return c.resolver.revalidate(ctx, c.binding, c.opened)
}

// PathForExternalCommand revalidates the capability immediately before Git or another fixed-argv command.
func (c *Capability) PathForExternalCommand(ctx context.Context) (string, error) {
	if err := c.Revalidate(ctx); err != nil {
		return "", err
	}
	return c.binding.canonicalRoot, nil
}

// Close releases the opened root handle. It is safe to call more than once.
func (c *Capability) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.opened.handle == nil {
		return nil
	}
	if err := c.opened.handle.Close(); err != nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeGrantStale, true, errCapabilityCleanup)
	}
	c.opened = openedRoot{}
	return nil
}
