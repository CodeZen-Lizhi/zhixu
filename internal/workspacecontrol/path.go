package workspacecontrol

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

var fingerprintDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var reservedNamespaces = []string{
	"/app", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc",
	"/root", "/run", "/sbin", "/sys", "/usr", "/var/lib", "/var/run", "/workspace",
}

var exactReservedTargets = map[string]struct{}{
	"/tmp":         {},
	"/private/tmp": {},
}

// RootFingerprint records the physical directory identity bound to one Workspace.
type RootFingerprint struct {
	PhysicalPath   string `json:"physical_path"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	BindingVersion int64  `json:"binding_version"`
}

// ValidatedRoot is the canonical result of metadata-only host path validation.
type ValidatedRoot struct {
	CanonicalPath string
	Fingerprint   RootFingerprint
}

// PathError is a path-field-safe failure whose Error text never includes the host path.
type PathError struct {
	Code string
	Err  error
}

func (pathError *PathError) Error() string {
	if pathError == nil || pathError.Code == "" {
		return "workspace path is invalid"
	}
	return pathError.Code
}

func (pathError *PathError) Unwrap() error { return pathError.Err }

// PathValidator validates a single existing POSIX root without creating or enumerating it.
type PathValidator struct{}

func (PathValidator) Validate(value string) (ValidatedRoot, error) {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_INVALID", nil)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_INVALID", nil)
		}
	}
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || !filepath.IsAbs(value) {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_NOT_ABSOLUTE", nil)
	}

	cleaned := filepath.Clean(value)
	if err := validateMountTarget(cleaned); err != nil {
		return ValidatedRoot{}, err
	}
	canonical, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_NOT_FOUND", err)
		}
		if os.IsPermission(err) {
			return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_PERMISSION_DENIED", err)
		}
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_UNSAFE_SYMLINK", err)
	}
	canonical = filepath.Clean(canonical)
	if !strings.HasPrefix(canonical, "/") || !filepath.IsAbs(canonical) {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_UNSAFE_SYMLINK", nil)
	}
	if err := validateMountTarget(canonical); err != nil {
		return ValidatedRoot{}, err
	}

	physicalInfo, err := os.Stat(canonical)
	if err != nil {
		if os.IsPermission(err) {
			return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_PERMISSION_DENIED", err)
		}
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_NOT_FOUND", err)
	}
	if !physicalInfo.IsDir() {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_NOT_DIRECTORY", nil)
	}
	inputInfo, err := os.Stat(cleaned)
	if err != nil || !os.SameFile(inputInfo, physicalInfo) {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_IDENTITY_CHANGED", err)
	}
	secondCanonical, err := filepath.EvalSymlinks(cleaned)
	if err != nil || filepath.Clean(secondCanonical) != canonical {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_IDENTITY_CHANGED", err)
	}
	secondInfo, err := os.Stat(canonical)
	if err != nil || !os.SameFile(physicalInfo, secondInfo) {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_IDENTITY_CHANGED", err)
	}

	stat, ok := secondInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return ValidatedRoot{}, pathFailure("WORKSPACE_PATH_IDENTITY_UNAVAILABLE", nil)
	}
	return ValidatedRoot{
		CanonicalPath: canonical,
		Fingerprint: RootFingerprint{
			PhysicalPath: canonical, Device: uint64(stat.Dev), Inode: uint64(stat.Ino), BindingVersion: 1,
		},
	}, nil
}

// Digest returns the non-reversible persisted identity used by the Workspace Registry.
func (fingerprint RootFingerprint) Digest() string {
	value := fmt.Sprintf("%s\x00%d\x00%d\x00%d", fingerprint.PhysicalPath, fingerprint.Device, fingerprint.Inode, fingerprint.BindingVersion)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func validateMountTarget(value string) error {
	if value == "/" {
		return pathFailure("WORKSPACE_PATH_RESERVED", nil)
	}
	if _, reserved := exactReservedTargets[value]; reserved {
		return pathFailure("WORKSPACE_PATH_RESERVED", nil)
	}
	for _, reserved := range reservedNamespaces {
		if pathsOverlap(value, reserved) {
			return pathFailure("WORKSPACE_PATH_RESERVED", nil)
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func pathFailure(code string, err error) error {
	if err == nil {
		err = fmt.Errorf("path validation failed")
	}
	return &PathError{Code: code, Err: err}
}
