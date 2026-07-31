// Package rootgrant owns the process-local Workspace root capability boundary.
package rootgrant

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// EnvWorkspaceGrantedID identifies the only Workspace granted to this process.
	EnvWorkspaceGrantedID = "ZHIXU_WORKSPACE_GRANTED_ID"
	// EnvWorkspaceGrantedRoot is the canonical mount source and target granted to this process.
	EnvWorkspaceGrantedRoot = "ZHIXU_WORKSPACE_GRANTED_ROOT"
	// EnvWorkspaceGrantGeneration fences this process against later root grants.
	EnvWorkspaceGrantGeneration = "ZHIXU_WORKSPACE_GRANT_GENERATION"

	// ErrorCodeRootNotGranted means the requested Workspace has no usable grant in this process.
	ErrorCodeRootNotGranted = "WORKSPACE_ROOT_NOT_GRANTED"
	// ErrorCodeGrantStale means grant authority or root identity no longer matches this process.
	ErrorCodeGrantStale = "WORKSPACE_GRANT_STALE"
)

var (
	errGrantInvalid      = errors.New("workspace process grant is invalid")
	errRootNotGranted    = errors.New("workspace root is not granted")
	errGrantStale        = errors.New("workspace root grant is stale")
	errAuthorityFailed   = errors.New("workspace root grant authority is unavailable")
	errCapabilityCleanup = errors.New("workspace root capability could not be closed")
)

// LookupEnv reads one process environment value.
type LookupEnv func(string) (string, bool)

// RuntimeGrantMode distinguishes explicit native development from a
// controller-managed Docker grant. A partial managed grant never falls back
// to direct mode.
type RuntimeGrantMode uint8

const (
	RuntimeGrantDirect RuntimeGrantMode = iota + 1
	RuntimeGrantManaged
)

// ProcessGrant is the immutable grant injected into one API or Worker process.
type ProcessGrant struct {
	workspaceID   foundation.ID
	canonicalRoot string
	generation    int64
}

// NewProcessGrant validates a controller-supplied process grant without using it as a filesystem capability.
func NewProcessGrant(workspaceID foundation.ID, canonicalRoot string, generation int64) (ProcessGrant, error) {
	if !validWorkspaceID(workspaceID) || !validCanonicalRootSyntax(canonicalRoot) || generation < 1 {
		return ProcessGrant{}, notGrantedError(errGrantInvalid)
	}
	return ProcessGrant{workspaceID: workspaceID, canonicalRoot: canonicalRoot, generation: generation}, nil
}

// LoadProcessGrant strictly parses the three required grant environment variables.
func LoadProcessGrant(lookup LookupEnv) (ProcessGrant, error) {
	if lookup == nil {
		return ProcessGrant{}, notGrantedError(errGrantInvalid)
	}
	rawID, idPresent := lookup(EnvWorkspaceGrantedID)
	rawRoot, rootPresent := lookup(EnvWorkspaceGrantedRoot)
	rawGeneration, generationPresent := lookup(EnvWorkspaceGrantGeneration)
	if !idPresent || !rootPresent || !generationPresent {
		return ProcessGrant{}, notGrantedError(errGrantInvalid)
	}
	workspaceID, err := foundation.ParseID(rawID)
	if err != nil || string(workspaceID) != rawID {
		return ProcessGrant{}, notGrantedError(errGrantInvalid)
	}
	generation, err := strconv.ParseInt(rawGeneration, 10, 64)
	if err != nil || generation < 1 || strconv.FormatInt(generation, 10) != rawGeneration {
		return ProcessGrant{}, notGrantedError(errGrantInvalid)
	}
	return NewProcessGrant(workspaceID, rawRoot, generation)
}

// LoadProcessGrantFromEnvironment reads the current process environment explicitly.
func LoadProcessGrantFromEnvironment() (ProcessGrant, error) {
	return LoadProcessGrant(os.LookupEnv)
}

// LoadRuntimeGrant selects direct mode only when all grant variables are absent.
func LoadRuntimeGrant(lookup LookupEnv) (ProcessGrant, RuntimeGrantMode, error) {
	if lookup == nil {
		return ProcessGrant{}, 0, notGrantedError(errGrantInvalid)
	}
	present := 0
	for _, key := range []string{EnvWorkspaceGrantedID, EnvWorkspaceGrantedRoot, EnvWorkspaceGrantGeneration} {
		if _, ok := lookup(key); ok {
			present++
		}
	}
	switch present {
	case 0:
		return ProcessGrant{}, RuntimeGrantDirect, nil
	case 3:
		grant, err := LoadProcessGrant(lookup)
		if err != nil {
			return ProcessGrant{}, 0, err
		}
		return grant, RuntimeGrantManaged, nil
	default:
		return ProcessGrant{}, 0, notGrantedError(errGrantInvalid)
	}
}

// LoadRuntimeGrantFromEnvironment selects the current process runtime mode.
func LoadRuntimeGrantFromEnvironment() (ProcessGrant, RuntimeGrantMode, error) {
	return LoadRuntimeGrant(os.LookupEnv)
}

// WorkspaceID returns the process-granted Workspace identity.
func (g ProcessGrant) WorkspaceID() foundation.ID { return g.workspaceID }

// CanonicalRoot returns the process-granted canonical mount path.
func (g ProcessGrant) CanonicalRoot() string { return g.canonicalRoot }

// Generation returns the process grant fence.
func (g ProcessGrant) Generation() int64 { return g.generation }

func validWorkspaceID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validCanonicalRootSyntax(value string) bool {
	return value != "" && utf8.ValidString(value) && !containsControl(value) &&
		filepath.IsAbs(value) && filepath.Clean(value) == value
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func notGrantedError(cause error) error {
	if cause == nil {
		cause = errRootNotGranted
	}
	return foundation.NewError(foundation.ErrorPermissionDenied, ErrorCodeRootNotGranted, false, cause)
}

func staleConflictError() error {
	return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeGrantStale, false, errGrantStale)
}

func staleConsistencyError() error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeGrantStale, false, errGrantStale)
}

func staleDependencyError(retryable bool) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeGrantStale, retryable, errAuthorityFailed)
}

func staleCancellationError(cause error) error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, ErrorCodeGrantStale, false, cause)
}
