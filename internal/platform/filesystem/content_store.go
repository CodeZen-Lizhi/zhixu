package filesystem

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const managedSourceDirectory = ".knowledge/sources"

// Capture copies an observed Workspace file into the immutable managed source store.
func (r Root) Capture(ctx context.Context, relative, expectedHash string, expectedSize int64) (string, bool, error) {
	if err := contextError(ctx); err != nil {
		return "", false, err
	}
	if expectedSize < 0 || !validSHA256(expectedHash) {
		return "", false, fileError(foundation.ErrorInvalidInput, "SOURCE_VERSION_CAPTURE_INVALID", false, errors.New("invalid expected content metadata"))
	}
	workspaceRoot, err := os.OpenRoot(r.path)
	if err != nil {
		return "", false, fileError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	defer workspaceRoot.Close()
	if err := validateRelativePath(relative); err != nil {
		return "", false, fileError(foundation.ErrorInvalidInput, "WORKSPACE_PATH_INVALID", false, err)
	}
	source, err := workspaceRoot.Open(filepath.ToSlash(filepath.Clean(relative)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, fileError(foundation.ErrorNotFound, "SOURCE_FILE_NOT_FOUND", false, err)
		}
		return "", false, fileError(foundation.ErrorInvalidInput, "WORKSPACE_PATH_INVALID", false, err)
	}
	info, statErr := source.Stat()
	if closeErr := source.Close(); statErr == nil {
		statErr = closeErr
	}
	if statErr != nil {
		return "", false, fileError(foundation.ErrorDependencyUnavailable, "SOURCE_FILE_READ_FAILED", true, statErr)
	}
	if !info.Mode().IsRegular() {
		return "", false, fileError(foundation.ErrorInvalidInput, "SOURCE_FILE_NOT_REGULAR", false, errors.New("source is not a regular file"))
	}
	if err := ensureManagedSourceDirectoryRoot(workspaceRoot); err != nil {
		return "", false, err
	}
	if err := r.ensureManagedSourceIgnored(); err != nil {
		return "", false, err
	}
	temporary, temporaryRelative, err := createManagedTemp(workspaceRoot)
	if err != nil {
		return "", false, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_TEMP_CREATE_FAILED", true, err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = workspaceRoot.Remove(temporaryRelative)
		}
	}()
	source, err = workspaceRoot.Open(filepath.ToSlash(filepath.Clean(relative)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, fileError(foundation.ErrorNotFound, "SOURCE_FILE_NOT_FOUND", false, err)
		}
		return "", false, fileError(foundation.ErrorInvalidInput, "WORKSPACE_PATH_INVALID", false, err)
	}
	copyErr := copyWithContext(ctx, temporary, source)
	closeSourceErr := source.Close()
	if copyErr == nil {
		copyErr = closeSourceErr
	}
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeTemporaryErr := temporary.Close()
	if copyErr == nil {
		copyErr = closeTemporaryErr
	}
	if copyErr != nil {
		if errors.Is(copyErr, context.Canceled) || errors.Is(copyErr, context.DeadlineExceeded) {
			return "", false, contextError(ctx)
		}
		return "", false, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_WRITE_FAILED", true, copyErr)
	}
	actualHash, actualSize, err := hashRootFileWithSize(ctx, workspaceRoot, temporaryRelative)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", false, contextError(ctx)
		}
		return "", false, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_VERIFY_FAILED", true, err)
	}
	if actualHash != expectedHash || actualSize != expectedSize {
		return "", false, fileError(foundation.ErrorVersionConflict, "SOURCE_VERSION_CONTENT_CONFLICT", false, errors.New("source changed after scan"))
	}
	finalRelative := filepath.ToSlash(filepath.Join(managedSourceDirectory, expectedHash))
	created := false
	if err := contextError(ctx); err != nil {
		return "", false, err
	}
	if err := workspaceRoot.Link(temporaryRelative, finalRelative); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", false, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_PUBLISH_FAILED", true, err)
		}
		if err := verifyRootFile(workspaceRoot, finalRelative, expectedHash, expectedSize); err != nil {
			return "", false, err
		}
	} else {
		created = true
		if err := contextError(ctx); err != nil {
			return "", false, err
		}
		if err := syncRootDirectory(workspaceRoot, filepath.ToSlash(filepath.Dir(finalRelative))); err != nil {
			return "", false, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_PUBLISH_FAILED", true, err)
		}
	}
	if err := workspaceRoot.Remove(temporaryRelative); err != nil {
		return "", false, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_TEMP_CLEANUP_FAILED", true, err)
	}
	removeTemporary = false
	return filepath.ToSlash(filepath.Join(managedSourceDirectory, expectedHash)), created, nil
}

// ReadArtifact safely re-reads and verifies an immutable managed content artifact.
func (r Root) ReadArtifact(ctx context.Context, managedLocation, expectedHash string, expectedSize int64) ([]byte, error) {
	return r.ReadArtifactLimited(ctx, managedLocation, expectedHash, expectedSize, 0)
}

// ReadArtifactLimited is the bounded form used by the Workspace Scanner. The
// size check happens before opening and is repeated while reading so corrupt
// metadata or a replaced file cannot force unbounded allocation.
func (r Root) ReadArtifactLimited(ctx context.Context, managedLocation, expectedHash string, expectedSize, maxBytes int64) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	expectedLocation := filepath.ToSlash(filepath.Join(managedSourceDirectory, expectedHash))
	if !validSHA256(expectedHash) || expectedSize < 0 || maxBytes < 0 || filepath.ToSlash(filepath.Clean(managedLocation)) != expectedLocation {
		return nil, fileError(foundation.ErrorInvalidInput, "CONTENT_ARTIFACT_REFERENCE_INVALID", false, errors.New("invalid content artifact reference"))
	}
	if maxBytes > 0 && expectedSize > maxBytes {
		return nil, fileError(foundation.ErrorInvalidInput, "SOURCE_FILE_TOO_LARGE", false, errors.New("content artifact exceeds read limit"))
	}
	workspaceRoot, err := os.OpenRoot(r.path)
	if err != nil {
		return nil, fileError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	defer workspaceRoot.Close()
	if err := validateManagedSourceDirectoryRoot(workspaceRoot); err != nil {
		return nil, err
	}
	info, err := workspaceRoot.Lstat(expectedLocation)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fileError(foundation.ErrorNotFound, "CONTENT_ARTIFACT_NOT_FOUND", false, err)
		}
		return nil, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_READ_FAILED", true, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fileError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_INVALID", false, errors.New("artifact is not a regular file"))
	}
	file, err := workspaceRoot.OpenFile(expectedLocation, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_READ_FAILED", true, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return nil, fileError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_INVALID", false, errors.New("artifact is not a regular file"))
	}
	hash := sha256.New()
	// Do not use the database-reported size as an allocation request. A corrupt
	// or malicious metadata row must not be able to trigger an enormous upfront
	// allocation before the file has been read and verified.
	content := make([]byte, 0, 64*1024)
	buffer := make([]byte, 64*1024)
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			content = append(content, buffer[:count]...)
			if maxBytes > 0 && int64(len(content)) > maxBytes {
				return nil, fileError(foundation.ErrorInvalidInput, "SOURCE_FILE_TOO_LARGE", false, errors.New("content artifact exceeds read limit"))
			}
			_, _ = hash.Write(buffer[:count])
			if int64(len(content)) > expectedSize {
				return nil, fileError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_CONTENT_CONFLICT", false, errors.New("artifact size changed"))
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_READ_FAILED", true, readErr)
		}
	}
	if int64(len(content)) != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return nil, fileError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_CONTENT_CONFLICT", false, errors.New("artifact content does not match metadata"))
	}
	return content, nil
}

func ensureManagedSourceDirectoryRoot(root *os.Root) error {
	for _, path := range []string{".knowledge", managedSourceDirectory} {
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_STORE_CREATE_FAILED", true, err)
			}
			info, err = root.Lstat(path)
		}
		if err != nil {
			return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_STORE_OPEN_FAILED", true, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fileError(foundation.ErrorPermissionDenied, "CONTENT_ARTIFACT_STORE_UNSAFE", false, errors.New("managed store is not a local directory"))
		}
	}
	return nil
}

func validateManagedSourceDirectoryRoot(root *os.Root) error {
	for _, path := range []string{".knowledge", managedSourceDirectory} {
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return fileError(foundation.ErrorNotFound, "CONTENT_ARTIFACT_NOT_FOUND", false, err)
		}
		if err != nil {
			return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_READ_FAILED", true, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fileError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_INVALID", false, errors.New("managed store directory is unsafe"))
		}
	}
	return nil
}

func createManagedTemp(root *os.Root) (*os.File, string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, "", err
		}
		name := filepath.ToSlash(filepath.Join(managedSourceDirectory, ".capture-"+hex.EncodeToString(raw[:])))
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", errors.New("unable to allocate unique capture temporary file")
}

func validateRelativePath(relative string) error {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return errors.New("relative path is required")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path escapes workspace root")
	}
	return nil
}

func hashRootFileWithSize(ctx context.Context, root *os.Root, relative string) (string, int64, error) {
	file, err := root.Open(relative)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	return hashReaderWithContext(ctx, file)
}

func hashReaderWithContext(ctx context.Context, reader io.Reader) (string, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", 0, err
			}
			size += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func verifyRootFile(root *os.Root, relative, expectedHash string, expectedSize int64) error {
	actualHash, actualSize, err := hashRootFileWithSize(context.Background(), root, relative)
	if err != nil {
		return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_VERIFY_FAILED", true, err)
	}
	if actualHash != expectedHash || actualSize != expectedSize {
		return fileError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_CONTENT_CONFLICT", false, errors.New("published artifact differs from expected content"))
	}
	return nil
}

func syncRootDirectory(root *os.Root, relative string) error {
	directory, err := root.Open(relative)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (r Root) ensureManagedSourceIgnored() error {
	return r.EnsureGitExcludePatterns("/.knowledge/")
}

// EnsureGitExcludePatterns adds stable local-only ignore patterns without
// modifying tracked .gitignore files. It rejects unsafe Git metadata paths.
func (r Root) EnsureGitExcludePatterns(patterns ...string) error {
	if r.path == "" {
		return fileError(foundation.ErrorDependencyUnavailable, "GIT_EXCLUDE_UNAVAILABLE", false, errors.New("workspace root is not initialized"))
	}
	requested := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.ContainsAny(pattern, "\r\n\x00") {
			return fileError(foundation.ErrorInvalidInput, "GIT_EXCLUDE_PATTERN_INVALID", false, errors.New("git exclude pattern is invalid"))
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		requested = append(requested, pattern)
	}
	if len(requested) == 0 {
		return nil
	}
	gitDirectory, err := resolveGitDirectory(r.path)
	if err != nil {
		return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, err)
	}
	infoDirectory := filepath.Join(gitDirectory, "info")
	if info, err := os.Lstat(infoDirectory); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fileError(foundation.ErrorPermissionDenied, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, errors.New("git info directory is unsafe"))
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(infoDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, err)
		}
	} else {
		return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, err)
	}
	if info, err := os.Lstat(infoDirectory); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err == nil {
			err = errors.New("git info directory is unsafe")
		}
		return fileError(foundation.ErrorPermissionDenied, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, err)
	}
	excludePath := filepath.Join(infoDirectory, "exclude")
	if info, err := os.Lstat(excludePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fileError(foundation.ErrorPermissionDenied, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, errors.New("git exclude is a symlink"))
	}
	current, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, err)
	}
	existing := make(map[string]struct{})
	for _, line := range strings.Split(string(current), "\n") {
		existing[strings.TrimSpace(line)] = struct{}{}
	}
	missing := make([]string, 0, len(requested))
	for _, pattern := range requested {
		if _, ok := existing[pattern]; !ok {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	file, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, err)
	}
	prefix := ""
	if len(current) > 0 && current[len(current)-1] != '\n' {
		prefix = "\n"
	}
	_, writeErr := io.WriteString(file, prefix+strings.Join(missing, "\n")+"\n")
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, writeErr)
	}
	if err := syncDirectory(infoDirectory); err != nil {
		return fileError(foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED", false, err)
	}
	return nil
}

func resolveGitDirectory(root string) (string, error) {
	marker := filepath.Join(root, ".git")
	info, err := os.Lstat(marker)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return marker, nil
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("unsupported git metadata marker")
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(content))
	const prefix = "gitdir:"
	if !strings.HasPrefix(strings.ToLower(value), prefix) {
		return "", errors.New("invalid git metadata marker")
	}
	gitDirectory := strings.TrimSpace(value[len(prefix):])
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(root, gitDirectory)
	}
	canonical, err := filepath.EvalSymlinks(gitDirectory)
	if err != nil {
		return "", err
	}
	info, err = os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("git directory is unavailable")
	}
	commonDirFile := filepath.Join(canonical, "commondir")
	commonBytes, err := os.ReadFile(commonDirFile)
	if err != nil {
		return "", errors.New("git worktree metadata is unavailable")
	}
	commonDir, err := filepath.Abs(filepath.Join(canonical, strings.TrimSpace(string(commonBytes))))
	if err != nil {
		return "", err
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "", err
	}
	commonInfo, err := os.Stat(commonDir)
	if err != nil || !commonInfo.IsDir() || !within(filepath.Join(commonDir, "worktrees"), canonical) {
		return "", errors.New("gitdir marker is not a valid workspace worktree")
	}
	return commonDir, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if _, err := destination.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fileError(foundation.ErrorNonRetryableFailure, "OPERATION_CANCELLED", false, err)
	}
	return nil
}

func fileError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, fmt.Errorf("filesystem operation failed: %w", cause))
}
