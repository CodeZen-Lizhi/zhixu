package rootgrant

import (
	"os"
	"sync"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// CandidateRootCapability is intentionally separate from an Active Capability.
// Its method surface is limited to startup probing and explicit Git initialization.
type CandidateRootCapability struct {
	mu      sync.RWMutex
	binding rootBinding
	opened  openedRoot
	closed  bool
}

// NewCandidateRootCapability opens a controller-supplied candidate grant without treating it as Active.
func NewCandidateRootCapability(grant ProcessGrant) (*CandidateRootCapability, error) {
	if !validWorkspaceID(grant.workspaceID) || !validCanonicalRootSyntax(grant.canonicalRoot) || grant.generation < 1 {
		return nil, notGrantedError(errGrantInvalid)
	}
	opened, err := openCanonicalRoot(grant.canonicalRoot)
	if err != nil {
		return nil, err
	}
	return &CandidateRootCapability{binding: bindingFromProcessGrant(grant), opened: opened}, nil
}

// WorkspaceID returns the candidate Workspace identity.
func (c *CandidateRootCapability) WorkspaceID() foundation.ID {
	if c == nil {
		return ""
	}
	return c.binding.workspaceID
}

// Generation returns the candidate grant fence.
func (c *CandidateRootCapability) Generation() int64 {
	if c == nil {
		return 0
	}
	return c.binding.generation
}

// ProbeRoot revalidates and returns the opened root only for startup probe code.
func (c *CandidateRootCapability) ProbeRoot() (*os.Root, error) {
	if c == nil {
		return nil, staleDependencyError(false)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.opened.handle == nil {
		return nil, staleDependencyError(false)
	}
	if err := validateRootIdentity(c.binding.canonicalRoot, c.opened); err != nil {
		return nil, err
	}
	return c.opened.handle, nil
}

// PathForGitInit revalidates the candidate path immediately before explicit Git initialization.
func (c *CandidateRootCapability) PathForGitInit() (string, error) {
	if _, err := c.ProbeRoot(); err != nil {
		return "", err
	}
	return c.binding.canonicalRoot, nil
}

// Close releases the candidate root handle. It is safe to call more than once.
func (c *CandidateRootCapability) Close() error {
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
